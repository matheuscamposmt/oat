// Package transcribe sends chunk files to a speech-to-text backend and saves the segments.
package transcribe

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/matheuscamposmt/oat/internal/chunk"
	"github.com/matheuscamposmt/oat/internal/store"
	"github.com/matheuscamposmt/oat/internal/stt"
)

// Client is the part of a backend client that the pipeline uses.
type Client interface {
	Transcribe(ctx context.Context, wav []byte, opts stt.Options) (stt.Result, error)
}

// Backend names a speech-to-text service in the warnings.
type Backend struct {
	Name       string // Groq
	Credential string // API key
	AuthFix    string // what the user does after an auth failure
}

// The backends that oat knows.
var (
	GroqBackend   = Backend{Name: "Groq", Credential: "API key", AuthFix: "Fix the key and start oat again."}
	ClaudeBackend = Backend{Name: "Claude", Credential: "login", AuthFix: "Open Claude Code to log in again, then start oat again."}
)

// BackendFor returns the backend for a transcriber value of the configuration.
func BackendFor(transcriber string) Backend {
	if transcriber == "claude" {
		return ClaudeBackend
	}
	return GroqBackend
}

// SegmentsEvent carries the new segments of one chunk.
type SegmentsEvent struct{ Segments []store.Segment }

// QueueEvent carries the number of chunks that wait.
type QueueEvent struct{ Pending int }

// Warning is a message for the HUD. Fatal warnings show in red.
type Warning struct {
	Fatal bool
	Msg   string
}

// ErrAuth means that the backend rejected the credentials. The chunks stay on disk.
var ErrAuth = errors.New("the transcription service rejected the credentials")

// Config changes how the pipeline sends chunks.
type Config struct {
	Model     string
	Lang      func() string // the current language; nil uses the language in meta.json
	KeepAudio bool
	Workers   int     // 2 when zero
	Backend   Backend // GroqBackend when zero
}

const (
	queueSize      = 4096
	promptWords    = 30
	maxNoSpeech    = 0.6
	maxCompression = 2.4
)

// Pipeline sends the chunk files of one meeting to the backend with a pool of workers.
type Pipeline struct {
	m      *store.Meeting
	client Client
	cfg    Config
	terms  []string
	emit   func(any)
	sleep  func(ctx context.Context, d time.Duration) error

	queue     chan string
	pending   atomic.Int64
	stopped   atomic.Bool
	wg        sync.WaitGroup
	cancel    context.CancelFunc
	closeOnce sync.Once

	mu      sync.Mutex
	tails   map[store.Speaker]string
	authErr error
}

// New returns a pipeline for the meeting. emit receives SegmentsEvent,
// QueueEvent, and Warning values from the worker goroutines.
func New(m *store.Meeting, client Client, cfg Config, emit func(any)) *Pipeline {
	if cfg.Workers <= 0 {
		cfg.Workers = 2
	}
	if cfg.Backend.Name == "" {
		cfg.Backend = GroqBackend
	}
	if cfg.Lang == nil {
		lang := m.Snapshot().Lang
		cfg.Lang = func() string { return lang }
	}
	if emit == nil {
		emit = func(any) {}
	}
	p := &Pipeline{
		m: m, client: client, cfg: cfg, terms: m.Snapshot().Keyterms, emit: emit, sleep: sleepCtx,
		queue: make(chan string, queueSize), tails: map[store.Speaker]string{},
	}
	if segs, err := m.Segments(); err == nil {
		for _, s := range store.Clean(segs) {
			p.tails[s.Speaker] = lastWords(p.tails[s.Speaker] + " " + s.Text)
		}
	}
	return p
}

// Start runs the workers until Close and an empty queue, or until ctx ends.
func (p *Pipeline) Start(ctx context.Context) {
	ctx, p.cancel = context.WithCancel(ctx)
	for range p.cfg.Workers {
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			p.work(ctx)
		}()
	}
}

// Enqueue adds a chunk file. It must not run after Close.
// After an auth failure, the file only waits on disk.
func (p *Pipeline) Enqueue(path string) {
	n := p.pending.Add(1)
	p.emit(QueueEvent{Pending: int(n)})
	if !p.stopped.Load() {
		p.queue <- path
	}
}

// EnqueuePending adds the chunk files that wait in the meeting folder.
func (p *Pipeline) EnqueuePending() (int, error) {
	paths, err := p.m.PendingChunks()
	if err != nil {
		return 0, err
	}
	for _, path := range paths {
		p.Enqueue(path)
	}
	return len(paths), nil
}

// Close tells the workers that no more chunks come.
func (p *Pipeline) Close() { p.closeOnce.Do(func() { close(p.queue) }) }

// Abort stops the workers now. The chunks that wait stay on disk.
func (p *Pipeline) Abort() {
	if p.cancel != nil {
		p.cancel()
	}
}

// Wait blocks until the workers end. It returns ErrAuth after an auth failure.
func (p *Pipeline) Wait() error {
	p.wg.Wait()
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.authErr
}

// Pending returns the number of chunks that wait.
func (p *Pipeline) Pending() int { return int(p.pending.Load()) }

func (p *Pipeline) work(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case path, ok := <-p.queue:
			if !ok {
				return
			}
			err := p.process(ctx, path)
			if errors.Is(err, ErrAuth) {
				p.mu.Lock()
				p.authErr = ErrAuth
				p.mu.Unlock()
				if !p.stopped.Swap(true) {
					b := p.cfg.Backend
					p.emit(Warning{Fatal: true, Msg: fmt.Sprintf("%s rejected the %s. The chunks stay on disk. %s", b.Name, b.Credential, b.AuthFix)})
				}
				p.cancel()
				return
			}
			if err != nil {
				return // ctx ended; the file stays on disk
			}
			p.emit(QueueEvent{Pending: int(p.pending.Add(-1))})
		}
	}
}

// process sends one chunk and retries until it succeeds, fails for good, or ctx ends.
func (p *Pipeline) process(ctx context.Context, path string) error {
	speaker, start, ok := store.ParseChunkName(path)
	if !ok {
		return p.fail(path, "the file name is not a chunk name")
	}
	wav, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil // another process sent it
	}
	if err != nil {
		p.emit(Warning{Msg: "Could not read " + filepath.Base(path) + ": " + err.Error()})
		return nil
	}
	tries := 0
	for {
		opts := stt.Options{Model: p.cfg.Model, Language: p.cfg.Lang(), Prompt: p.promptFor(speaker), Keyterms: p.terms}
		res, err := p.client.Transcribe(ctx, wav, opts)
		var rl *stt.RateLimitError
		var auth *stt.AuthError
		var req *stt.RequestError
		switch {
		case err == nil:
			return p.save(path, speaker, start, chunk.WAVDuration(wav), res)
		case errors.As(err, &auth):
			return ErrAuth
		case errors.As(err, &req):
			return p.fail(path, req.Msg)
		case ctx.Err() != nil:
			return ctx.Err()
		case errors.As(err, &rl):
			p.emit(Warning{Msg: fmt.Sprintf("%s rate limit. Next try in %s.", p.cfg.Backend.Name, rl.RetryAfter.Round(time.Second))})
			if err := p.sleep(ctx, rl.RetryAfter); err != nil {
				return err
			}
		default:
			d := backoff(tries)
			tries++
			p.emit(Warning{Msg: fmt.Sprintf("%s did not answer (%v). Next try in %s.", p.cfg.Backend.Name, err, d)})
			if err := p.sleep(ctx, d); err != nil {
				return err
			}
		}
	}
}

// Save writes the segments of a result that another path transcribed, for
// example the live transcript. Then it deletes or keeps the WAV file at path.
// An empty path has no file. Save does not change the queue count.
func (p *Pipeline) Save(path string, speaker store.Speaker, start, dur float64, res stt.Result) {
	_ = p.save(path, speaker, start, dur, res)
}

// save writes the segments of a chunk, then deletes or keeps the WAV file.
func (p *Pipeline) save(path string, speaker store.Speaker, start, dur float64, res stt.Result) error {
	segs := Segments(res, speaker, start, dur)
	if err := p.m.AppendSegments(segs); err != nil {
		p.emit(Warning{Fatal: true, Msg: "Could not write the transcript: " + err.Error()})
		return nil // the file stays on disk for the next start
	}
	if len(segs) > 0 {
		p.mu.Lock()
		for _, s := range segs {
			p.tails[speaker] = lastWords(p.tails[speaker] + " " + s.Text)
		}
		p.mu.Unlock()
		p.emit(SegmentsEvent{Segments: segs})
	}
	if path == "" {
		return nil
	}
	if p.cfg.KeepAudio {
		if err := os.MkdirAll(p.m.AudioDir(), 0o755); err == nil {
			_ = os.Rename(path, filepath.Join(p.m.AudioDir(), filepath.Base(path)))
			return nil
		}
	}
	_ = os.Remove(path)
	return nil
}

// fail moves a chunk that the backend refused to failed/ and emits a warning.
func (p *Pipeline) fail(path, msg string) error {
	if err := os.MkdirAll(p.m.FailedDir(), 0o755); err == nil {
		_ = os.Rename(path, filepath.Join(p.m.FailedDir(), filepath.Base(path)))
	}
	p.emit(Warning{Msg: fmt.Sprintf("%s refused %s: %s. The file is in failed/.", p.cfg.Backend.Name, filepath.Base(path), msg)})
	return nil
}

func (p *Pipeline) promptFor(sp store.Speaker) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.tails[sp]
}

// Segments converts a backend result to segments on the meeting clock. It drops
// segments that look like silence, a repetition loop, or a Whisper hallucination.
func Segments(res stt.Result, speaker store.Speaker, start, dur float64) []store.Segment {
	var out []store.Segment
	if len(res.Segments) == 0 {
		if t := strings.TrimSpace(res.Text); t != "" && !Hallucination(t) {
			out = append(out, store.Segment{Start: start, End: start + dur, Speaker: speaker, Text: t})
		}
		return out
	}
	for _, s := range res.Segments {
		t := strings.TrimSpace(s.Text)
		if t == "" || s.NoSpeechProb > maxNoSpeech || s.CompressionRatio > maxCompression || Hallucination(t) {
			continue
		}
		out = append(out, store.Segment{Start: start + s.Start, End: start + s.End, Speaker: speaker, Text: t})
	}
	return out
}

var hallucinations = map[string]bool{
	"legendas pela comunidade amara org":   true,
	"obrigado por assistir":                true,
	"inscreva se no canal":                 true,
	"legenda adriana zanotto":              true,
	"thank you for watching":               true,
	"thanks for watching":                  true,
	"subtitles by the amara org community": true,
}

// Hallucination reports whether text is a phrase that Whisper invents on silence.
func Hallucination(text string) bool {
	n := strings.Join(store.Words(text), " ")
	return hallucinations[n] || strings.Contains(n, "amara org")
}

func backoff(tries int) time.Duration {
	if tries >= 5 {
		return 60 * time.Second
	}
	return time.Duration(2<<tries) * time.Second // 2, 4, 8, 16, 32 s
}

func lastWords(s string) string {
	w := strings.Fields(s)
	if len(w) > promptWords {
		w = w[len(w)-promptWords:]
	}
	return strings.Join(w, " ")
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
