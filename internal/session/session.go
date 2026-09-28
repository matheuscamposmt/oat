// Package session runs one recording: capture, chunking, transcription, and storage.
package session

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/matheuscamposmt/oat/internal/audio"
	"github.com/matheuscamposmt/oat/internal/chunk"
	"github.com/matheuscamposmt/oat/internal/config"
	"github.com/matheuscamposmt/oat/internal/store"
	"github.com/matheuscamposmt/oat/internal/transcribe"
)

// Source is a running capture that gives 20 ms frames. *audio.Stream is one.
type Source interface {
	Frames() <-chan []int16
	Close() error
	Err() error
}

// Options holds what a session needs. Open, Now, PollEvery, and RenderEvery have defaults.
type Options struct {
	Title       string
	Config      config.Config
	Store       *store.Store
	Client      transcribe.Client
	Pactl       audio.Pactl
	Echo        *audio.Echo // nil turns echo cancel off
	Open        func(ctx context.Context, device string) (Source, error)
	Now         func() time.Time
	PollEvery   time.Duration // 2 s
	RenderEvery time.Duration // 10 s
}

// LevelEvent carries the level of one stream, about every 100 ms.
type LevelEvent struct {
	Speaker store.Speaker
	DB      float64
}

// EchoEvent reports whether echo cancel runs.
type EchoEvent struct{ On bool }

// DevicesEvent reports the devices that the captures use.
type DevicesEvent struct{ Mic, Monitor string }

// StoppedEvent comes when the queue drained after Stop, or when the drain failed.
type StoppedEvent struct{ Err error }

const (
	eventBuffer   = 1024
	restartLimit  = 3
	restartWindow = 10 * time.Second
	levelEvery    = 5 // frames
	frameDuration = 20 * time.Millisecond
)

// Session is one recording.
type Session struct {
	opts   Options
	m      *store.Meeting
	pipe   *transcribe.Pipeline
	events chan any
	start  time.Time

	ctx         context.Context
	cancel      context.CancelFunc
	stopCh      chan struct{}
	stopOnce    sync.Once
	captureOnce sync.Once
	capWG       sync.WaitGroup
	bgWG        sync.WaitGroup

	paused atomic.Bool
	lang   atomic.Value // string

	mu         sync.Mutex
	realSink   string
	realSource string
	micDev     string
	restart    map[store.Speaker]chan struct{}
}

// Start creates the meeting and starts both captures and the transcription workers.
func Start(ctx context.Context, o Options) (*Session, error) {
	if o.Open == nil {
		o.Open = func(ctx context.Context, dev string) (Source, error) { return audio.Open(ctx, dev) }
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.PollEvery == 0 {
		o.PollEvery = 2 * time.Second
	}
	if o.RenderEvery == 0 {
		o.RenderEvery = 10 * time.Second
	}
	sink, err := o.Pactl.DefaultSink(ctx)
	if err != nil {
		return nil, fmt.Errorf("find the default output: %w", err)
	}
	source, err := o.Pactl.DefaultSource(ctx)
	if err != nil {
		return nil, fmt.Errorf("find the default microphone: %w", err)
	}
	start := o.Now()
	m, err := o.Store.Create(o.Title, start, o.Config.Lang, o.Config.Model, o.Config.Echo)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &Session{
		opts: o, m: m, events: make(chan any, eventBuffer), start: start,
		ctx: ctx, cancel: cancel, stopCh: make(chan struct{}),
		realSink: sink, realSource: source,
		restart: map[store.Speaker]chan struct{}{
			store.Me:   make(chan struct{}, 1),
			store.Them: make(chan struct{}, 1),
		},
	}
	s.lang.Store(o.Config.Lang)

	s.mu.Lock()
	s.applyEcho(ctx)
	devices := DevicesEvent{Mic: s.micDev, Monitor: s.realSink + ".monitor"}
	s.mu.Unlock()
	s.emit(devices)

	s.pipe = transcribe.New(m, o.Client, transcribe.Config{Model: o.Config.Model, Lang: s.Lang, KeepAudio: o.Config.KeepAudio}, s.emit)
	s.pipe.Start(ctx)
	for _, sp := range []store.Speaker{store.Me, store.Them} {
		s.capWG.Add(1)
		go s.capture(sp)
	}
	s.bgWG.Add(2)
	go s.pollDevices()
	go s.renderLoop()
	return s, nil
}

// Meeting returns the meeting that this session records.
func (s *Session) Meeting() *store.Meeting { return s.m }

// Events returns the channel of events for the HUD. It never closes.
func (s *Session) Events() <-chan any { return s.events }

// Started returns the start time of the meeting clock.
func (s *Session) Started() time.Time { return s.start }

// Lang returns the language for the next chunks.
func (s *Session) Lang() string { return s.lang.Load().(string) }

// SetLang changes the language for the next chunks and saves it in meta.json.
func (s *Session) SetLang(lang string) error {
	s.lang.Store(lang)
	return s.m.Update(func(m *store.Meta) { m.Lang = lang })
}

// Pause drops the audio until Resume. The meeting clock continues.
func (s *Session) Pause() { s.paused.Store(true) }

// Resume ends a pause.
func (s *Session) Resume() { s.paused.Store(false) }

// Paused reports whether the session is paused.
func (s *Session) Paused() bool { return s.paused.Load() }

// EchoOn reports whether echo cancel runs.
func (s *Session) EchoOn() bool { return s.opts.Echo != nil && s.opts.Echo.Active() }

// Pending returns the number of chunks that wait for Groq.
func (s *Session) Pending() int { return s.pipe.Pending() }

// Stop ends the captures and drains the queue in the background.
// A StoppedEvent comes at the end.
func (s *Session) Stop() {
	s.stopOnce.Do(func() {
		go func() {
			s.StopCapture()
			s.finish(s.pipe.Wait())
		}()
	})
}

// StopCapture ends both captures, writes the last chunks to disk, and removes
// the echo-cancel module. It blocks until that is done. It is safe to call twice.
func (s *Session) StopCapture() {
	s.captureOnce.Do(func() {
		close(s.stopCh)
		s.capWG.Wait()
		s.bgWG.Wait()
		if s.opts.Echo != nil {
			ctx, cancel := cleanupContext()
			if err := s.opts.Echo.Disable(ctx, true); err != nil {
				s.emit(transcribe.Warning{Msg: "Could not remove the echo-cancel module: " + err.Error()})
			}
			cancel()
		}
		end := s.opts.Now()
		_ = s.m.Update(func(m *store.Meta) {
			m.EndedAt = end
			m.Status = store.Processing
		})
		s.pipe.Close()
	})
}

// Abandon stops the capture and the drain now. The chunks that wait stay on
// disk, and the next start sends them.
func (s *Session) Abandon() {
	s.StopCapture()
	s.cancel()
	s.pipe.Abort()
	_ = s.pipe.Wait()
	_ = s.m.WriteTranscriptMD()
	_ = s.m.Unlock()
}

// finish marks the meeting done when no chunk waits in memory or on disk.
// When chunks stay on disk, the meeting stays processing, and the next start
// of oat sends them.
func (s *Session) finish(err error) {
	if err == nil && s.ctx.Err() == nil {
		left, perr := s.m.PendingChunks()
		switch {
		case perr != nil:
			err = fmt.Errorf("list the chunks that wait: %w", perr)
		case len(left) > 0:
			err = fmt.Errorf("%d chunks were not transcribed", len(left))
		case s.pipe.Pending() == 0:
			_ = s.m.Update(func(m *store.Meta) { m.Status = store.Done })
		}
	}
	_ = s.m.WriteTranscriptMD()
	_ = s.m.Unlock()
	s.emit(StoppedEvent{Err: err})
}

// emit sends an event to the HUD. A level event drops when the buffer is full.
// Other events wait for space.
func (s *Session) emit(ev any) {
	if _, ok := ev.(LevelEvent); ok {
		select {
		case s.events <- ev:
		default:
		}
		return
	}
	select {
	case s.events <- ev:
	case <-s.ctx.Done():
	}
}

// applyEcho decides the echo mode for the current output and sets the mic device.
// The caller holds s.mu.
func (s *Session) applyEcho(ctx context.Context) {
	s.micDev = s.realSource
	if s.opts.Echo == nil {
		return
	}
	info, err := s.opts.Pactl.Sink(ctx, s.realSink)
	if err != nil {
		info = audio.SinkInfo{Name: s.realSink}
	}
	if audio.WantEcho(s.opts.Config.Echo, info) {
		if err := s.opts.Echo.Enable(ctx, s.realSource, s.realSink); err != nil {
			s.emit(transcribe.Warning{Msg: "Echo cancel did not start (" + err.Error() + "). oat uses the text filter only."})
		} else {
			s.micDev = audio.ECSource
		}
	}
	s.emit(EchoEvent{On: s.opts.Echo.Active()})
}

func (s *Session) device(sp store.Speaker) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sp == store.Me {
		return s.micDev
	}
	return s.realSink + ".monitor"
}

func (s *Session) requestRestart(sp store.Speaker) {
	select {
	case s.restart[sp] <- struct{}{}:
	default:
	}
}

// frameIndex returns the meeting-clock index of a frame that arrives now.
func (s *Session) frameIndex() int64 {
	return int64(s.opts.Now().Sub(s.start) / frameDuration)
}

type endReason int

const (
	stopped endReason = iota
	restarted
	ended
)

// capture runs one stream until the session stops. It opens the stream again
// after a device change, and after a failure up to restartLimit times in restartWindow.
func (s *Session) capture(sp store.Speaker) {
	defer s.capWG.Done()
	ch := chunk.New()
	// Flush on every exit. The closure makes Flush run at exit, not now.
	defer func() { s.saveChunk(sp, ch.Flush()) }()
	var failures []time.Time
	for {
		src, err := s.opts.Open(s.ctx, s.device(sp))
		if err != nil {
			if !s.retryCapture(sp, &failures, err) {
				return
			}
			continue
		}
		reason := s.pump(sp, src, ch)
		src.Close()
		switch reason {
		case stopped:
			return
		case ended:
			if !s.retryCapture(sp, &failures, src.Err()) {
				return
			}
		}
	}
}

// pump reads frames until the stream ends, a restart comes, or the session stops.
func (s *Session) pump(sp store.Speaker, src Source, ch *chunk.Chunker) endReason {
	idx := s.frameIndex()
	wasPaused := false
	n := 0
	for {
		select {
		case <-s.stopCh:
			return stopped
		case <-s.restart[sp]:
			return restarted
		case frame, ok := <-src.Frames():
			if !ok {
				select {
				case <-s.restart[sp]:
					return restarted
				default:
					return ended
				}
			}
			if s.paused.Load() {
				if !wasPaused {
					s.saveChunk(sp, ch.Flush())
				}
				wasPaused = true
				idx++
				continue
			}
			wasPaused = false
			if n%levelEvery == 0 {
				s.emit(LevelEvent{Speaker: sp, DB: chunk.LevelDB(frame)})
			}
			n++
			s.saveChunk(sp, ch.Push(idx, frame))
			idx++
		}
	}
}

// retryCapture counts a capture failure. It returns false when the stream must stop.
func (s *Session) retryCapture(sp store.Speaker, failures *[]time.Time, err error) bool {
	select {
	case <-s.stopCh:
		return false
	default:
	}
	now := time.Now()
	recent := (*failures)[:0]
	for _, t := range *failures {
		if now.Sub(t) < restartWindow {
			recent = append(recent, t)
		}
	}
	*failures = append(recent, now)
	if len(*failures) > restartLimit {
		msg := "Microphone capture stopped"
		if sp == store.Them {
			msg = "System audio capture stopped"
		}
		if err != nil {
			msg += ": " + err.Error()
		}
		s.emit(transcribe.Warning{Msg: msg + ". The meeting continues with the other stream."})
		return false
	}
	select {
	case <-s.stopCh:
		return false
	case <-time.After(500 * time.Millisecond):
		return true
	}
}

// saveChunk writes a chunk to disk and queues it. A write error stops the recording.
func (s *Session) saveChunk(sp store.Speaker, c *chunk.Chunk) {
	if c == nil {
		return
	}
	path := filepath.Join(s.m.ChunksDir(), store.ChunkName(sp, c.Start))
	if err := store.WriteFileAtomic(path, chunk.EncodeWAV(c.Samples)); err != nil {
		s.emit(transcribe.Warning{Fatal: true, Msg: "Disk write failed: " + err.Error() + ". oat stopped the recording."})
		go s.Stop()
		return
	}
	s.pipe.Enqueue(path)
}

func (s *Session) pollDevices() {
	defer s.bgWG.Done()
	t := time.NewTicker(s.opts.PollEvery)
	defer t.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-s.ctx.Done():
			return
		case <-t.C:
			sink, err1 := s.opts.Pactl.DefaultSink(s.ctx)
			source, err2 := s.opts.Pactl.DefaultSource(s.ctx)
			if err1 == nil && err2 == nil && sink != "" && source != "" {
				s.reconcile(sink, source)
			}
		}
	}
}

// reconcile follows a change of the default devices.
func (s *Session) reconcile(sink, source string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	echoOn := s.opts.Echo != nil && s.opts.Echo.Active()
	own := echoOn && sink == audio.ECSink // oat set this default itself
	if own {
		sink = s.realSink
	}
	outputMoved := !own && (echoOn || sink != s.realSink)
	micMoved := source != s.realSource && source != audio.ECSource
	if !outputMoved && !micMoved {
		return
	}
	if echoOn {
		// A new output is your choice, so oat keeps it as the default.
		ctx, cancel := cleanupContext()
		_ = s.opts.Echo.Disable(ctx, !outputMoved)
		cancel()
	}
	s.realSink = sink
	if micMoved {
		s.realSource = source
	}
	s.applyEcho(s.ctx)
	s.requestRestart(store.Me)
	if outputMoved {
		s.requestRestart(store.Them)
	}
	s.emit(DevicesEvent{Mic: s.micDev, Monitor: s.realSink + ".monitor"})
}

func (s *Session) renderLoop() {
	defer s.bgWG.Done()
	t := time.NewTicker(s.opts.RenderEvery)
	defer t.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-s.ctx.Done():
			return
		case <-t.C:
			_ = s.m.WriteTranscriptMD()
		}
	}
}

func cleanupContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}
