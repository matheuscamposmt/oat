package transcribe

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/matheuscamposmt/oat/internal/chunk"
	"github.com/matheuscamposmt/oat/internal/groq"
	"github.com/matheuscamposmt/oat/internal/store"
)

type reply func(opts groq.Options) (groq.Result, error)

// fakeClient returns the replies in order. The last reply repeats.
type fakeClient struct {
	mu      sync.Mutex
	replies []reply
	calls   []groq.Options
}

func (f *fakeClient) Transcribe(ctx context.Context, wav []byte, opts groq.Options) (groq.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, opts)
	i := min(len(f.calls)-1, len(f.replies)-1)
	return f.replies[i](opts)
}

func ok(segs ...groq.Segment) reply {
	return func(groq.Options) (groq.Result, error) { return groq.Result{Segments: segs}, nil }
}

func fails(err error) reply {
	return func(groq.Options) (groq.Result, error) { return groq.Result{}, err }
}

type recorder struct {
	mu     sync.Mutex
	events []any
}

func (r *recorder) emit(ev any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *recorder) has(pred func(any) bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ev := range r.events {
		if pred(ev) {
			return true
		}
	}
	return false
}

func newMeeting(t *testing.T) *store.Meeting {
	t.Helper()
	m, err := store.New(t.TempDir()).Create("t", time.Now(), "pt", "whisper-large-v3-turbo", "off")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func writeChunk(t *testing.T, m *store.Meeting, sp store.Speaker, start float64) string {
	t.Helper()
	path := filepath.Join(m.ChunksDir(), store.ChunkName(sp, start))
	if err := os.WriteFile(path, chunk.EncodeWAV(make([]int16, 2*chunk.SampleRate)), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// run drains the pending chunks of m and returns the Wait error.
func run(t *testing.T, p *Pipeline) error {
	t.Helper()
	p.Start(context.Background())
	if _, err := p.EnqueuePending(); err != nil {
		t.Fatal(err)
	}
	p.Close()
	done := make(chan error, 1)
	go func() { done <- p.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("the pipeline did not finish")
		return nil
	}
}

func TestSavesSegmentsOnTheMeetingClock(t *testing.T) {
	m := newMeeting(t)
	path := writeChunk(t, m, store.Them, 712.4)
	rec := &recorder{}
	fc := &fakeClient{replies: []reply{ok(groq.Segment{Start: 0.5, End: 2.0, Text: " Então, sobre o prazo", NoSpeechProb: 0.01})}}
	if err := run(t, New(m, fc, Config{Model: "whisper-large-v3-turbo"}, rec.emit)); err != nil {
		t.Fatal(err)
	}
	segs, _ := m.Segments()
	want := store.Segment{Start: 712.9, End: 714.4, Speaker: store.Them, Text: "Então, sobre o prazo"}
	if len(segs) != 1 || segs[0] != want {
		t.Fatalf("got %+v", segs)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("the chunk file still exists")
	}
	if !rec.has(func(ev any) bool { e, ok := ev.(QueueEvent); return ok && e.Pending == 0 }) {
		t.Fatal("no QueueEvent{0}")
	}
	if !rec.has(func(ev any) bool { _, ok := ev.(SegmentsEvent); return ok }) {
		t.Fatal("no SegmentsEvent")
	}
	if fc.calls[0].Language != "pt" || fc.calls[0].Model != "whisper-large-v3-turbo" {
		t.Fatalf("options %+v", fc.calls[0])
	}
}

func TestFiltersSilenceLoopsAndHallucinations(t *testing.T) {
	res := groq.Result{Segments: []groq.Segment{
		{Start: 0, End: 1, Text: "silêncio", NoSpeechProb: 0.9},
		{Start: 1, End: 2, Text: "la la la la", CompressionRatio: 3.0},
		{Start: 2, End: 3, Text: "Legendas pela comunidade Amara.org"},
		{Start: 3, End: 4, Text: "  "},
		{Start: 4, End: 5, Text: " ok, vamos lá"},
	}}
	got := Segments(res, store.Me, 10, 5)
	if len(got) != 1 || got[0].Text != "ok, vamos lá" || got[0].Start != 14 {
		t.Fatalf("got %+v", got)
	}
}

func TestTextOnlyResultSpansTheChunk(t *testing.T) {
	got := Segments(groq.Result{Text: " olá "}, store.Me, 10, 2.5)
	if len(got) != 1 || got[0] != (store.Segment{Start: 10, End: 12.5, Speaker: store.Me, Text: "olá"}) {
		t.Fatalf("got %+v", got)
	}
}

func TestRetriesTemporaryErrorsAndRateLimits(t *testing.T) {
	m := newMeeting(t)
	writeChunk(t, m, store.Me, 1)
	fc := &fakeClient{replies: []reply{
		fails(&groq.TemporaryError{Err: errors.New("net down")}),
		fails(&groq.RateLimitError{RetryAfter: 3 * time.Second}),
		fails(&groq.TemporaryError{Err: errors.New("net down")}),
		ok(groq.Segment{Start: 0, End: 1, Text: "voltou"}),
	}}
	p := New(m, fc, Config{Model: "m"}, nil)
	var mu sync.Mutex
	var slept []time.Duration
	p.sleep = func(ctx context.Context, d time.Duration) error {
		mu.Lock()
		slept = append(slept, d)
		mu.Unlock()
		return nil
	}
	if err := run(t, p); err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{2 * time.Second, 3 * time.Second, 4 * time.Second}
	if len(slept) != 3 || slept[0] != want[0] || slept[1] != want[1] || slept[2] != want[2] {
		t.Fatalf("slept %v, want %v", slept, want)
	}
	if segs, _ := m.Segments(); len(segs) != 1 {
		t.Fatalf("got %+v", segs)
	}
}

func TestAuthErrorStopsAndKeepsChunks(t *testing.T) {
	m := newMeeting(t)
	path := writeChunk(t, m, store.Me, 1)
	rec := &recorder{}
	p := New(m, &fakeClient{replies: []reply{fails(&groq.AuthError{Msg: "Invalid API Key"})}}, Config{Model: "m"}, rec.emit)
	if err := run(t, p); !errors.Is(err, ErrAuth) {
		t.Fatalf("Wait = %v, want ErrAuth", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("the chunk file is gone")
	}
	if p.Pending() != 1 {
		t.Fatalf("Pending = %d, want 1", p.Pending())
	}
	if !rec.has(func(ev any) bool { w, ok := ev.(Warning); return ok && w.Fatal }) {
		t.Fatal("no fatal warning")
	}
}

func TestRequestErrorMovesChunkToFailed(t *testing.T) {
	m := newMeeting(t)
	path := writeChunk(t, m, store.Me, 1)
	p := New(m, &fakeClient{replies: []reply{fails(&groq.RequestError{Status: 400, Msg: "bad audio"})}}, Config{Model: "m"}, nil)
	if err := run(t, p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.FailedDir(), filepath.Base(path))); err != nil {
		t.Fatal("the chunk is not in failed/")
	}
	if p.Pending() != 0 {
		t.Fatalf("Pending = %d", p.Pending())
	}
}

func TestKeepAudioMovesChunkToAudio(t *testing.T) {
	m := newMeeting(t)
	path := writeChunk(t, m, store.Me, 1)
	p := New(m, &fakeClient{replies: []reply{ok(groq.Segment{Text: "oi", End: 1})}}, Config{Model: "m", KeepAudio: true}, nil)
	if err := run(t, p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.AudioDir(), filepath.Base(path))); err != nil {
		t.Fatal("the chunk is not in audio/")
	}
	if left, _ := m.PendingChunks(); len(left) != 0 {
		t.Fatalf("chunks still wait: %v", left)
	}
}

func TestPromptUsesTheSameSpeaker(t *testing.T) {
	m := newMeeting(t)
	m.AppendSegments([]store.Segment{{Start: 0, End: 1, Speaker: store.Me, Text: "a b c"}, {Start: 5, End: 6, Speaker: store.Them, Text: "x y z"}})
	writeChunk(t, m, store.Me, 10)
	writeChunk(t, m, store.Them, 20)
	writeChunk(t, m, store.Me, 30)
	fc := &fakeClient{replies: []reply{ok(groq.Segment{Start: 0, End: 1, Text: "d e"})}}
	if err := run(t, New(m, fc, Config{Model: "m", Workers: 1}, nil)); err != nil {
		t.Fatal(err)
	}
	want := []string{"a b c", "x y z", "a b c d e"}
	for i, w := range want {
		if fc.calls[i].Prompt != w {
			t.Errorf("call %d prompt = %q, want %q", i, fc.calls[i].Prompt, w)
		}
	}
}

func TestLanguageComesFromTheFunction(t *testing.T) {
	m := newMeeting(t)
	writeChunk(t, m, store.Me, 1)
	fc := &fakeClient{replies: []reply{ok()}}
	if err := run(t, New(m, fc, Config{Model: "m", Lang: func() string { return "en" }}, nil)); err != nil {
		t.Fatal(err)
	}
	if fc.calls[0].Language != "en" {
		t.Fatalf("Language = %q", fc.calls[0].Language)
	}
}

func TestAbortLeavesChunksOnDisk(t *testing.T) {
	m := newMeeting(t)
	path := writeChunk(t, m, store.Me, 1)
	block := func(groq.Options) (groq.Result, error) {
		return groq.Result{}, &groq.TemporaryError{Err: errors.New("down")}
	}
	p := New(m, &fakeClient{replies: []reply{block}}, Config{Model: "m"}, nil)
	p.Start(context.Background())
	p.EnqueuePending()
	p.Close()
	time.Sleep(50 * time.Millisecond)
	p.Abort()
	p.Wait()
	if _, err := os.Stat(path); err != nil {
		t.Fatal("the chunk file is gone after Abort")
	}
}

func TestBackoff(t *testing.T) {
	want := []time.Duration{2, 4, 8, 16, 32, 60, 60}
	for i, w := range want {
		if got := backoff(i); got != w*time.Second {
			t.Errorf("backoff(%d) = %v, want %v", i, got, w*time.Second)
		}
	}
}
