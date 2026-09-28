package session

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matheuscamposmt/oat/internal/audio"
	"github.com/matheuscamposmt/oat/internal/chunk"
	"github.com/matheuscamposmt/oat/internal/config"
	"github.com/matheuscamposmt/oat/internal/groq"
	"github.com/matheuscamposmt/oat/internal/store"
	"github.com/matheuscamposmt/oat/internal/transcribe"
)

// --- audio frames ---

func tone(idx int) []int16 {
	f := make([]int16, chunk.FrameSamples)
	for i := range f {
		t := float64(idx*chunk.FrameSamples+i) / chunk.SampleRate
		f[i] = int16(0.3 * 32767 * math.Sin(2*math.Pi*440*t))
	}
	return f
}

func noise() []int16 {
	f := make([]int16, chunk.FrameSamples)
	for i := range f {
		f[i] = 10 - 20*int16(i%2)
	}
	return f
}

// utterance is 1 s of noise, 1.9 s of speech, and 4 s of noise: one chunk.
func utterance() [][]int16 {
	var out [][]int16
	for range 50 {
		out = append(out, noise())
	}
	for i := range 95 {
		if i%25 < 20 {
			out = append(out, tone(i))
		} else {
			out = append(out, noise())
		}
	}
	for range 200 {
		out = append(out, noise())
	}
	return out
}

// --- fake capture ---

type fakeSource struct {
	ch      chan []int16
	gate    chan struct{}
	quit    chan struct{}
	drained chan struct{}
	once    sync.Once
}

// newSource sends frames after the gate opens, then waits for Close.
// With end, it closes its channel after the frames, as when parec exits.
func newSource(frames [][]int16, gate chan struct{}, end bool) *fakeSource {
	s := &fakeSource{ch: make(chan []int16), gate: gate, quit: make(chan struct{}), drained: make(chan struct{})}
	go func() {
		defer close(s.ch)
		select {
		case <-gate:
		case <-s.quit:
			return
		}
		for _, f := range frames {
			select {
			case s.ch <- f:
			case <-s.quit:
				return
			}
		}
		close(s.drained)
		if end {
			return
		}
		<-s.quit
	}()
	return s
}

func (s *fakeSource) Frames() <-chan []int16 { return s.ch }
func (s *fakeSource) Err() error             { return nil }
func (s *fakeSource) Close() error {
	s.once.Do(func() { close(s.quit) })
	for range s.ch {
	}
	return nil
}

// opener hands out a source for each device and records the devices.
type opener struct {
	mu      sync.Mutex
	gate    chan struct{}
	frames  map[string][][]int16
	ends    map[string]bool // the source ends after its frames
	fails   map[string]bool // Open fails for the device after its first open
	opened  []string
	sources []*fakeSource
}

func newOpener() *opener {
	return &opener{gate: make(chan struct{}), frames: map[string][][]int16{}, ends: map[string]bool{}, fails: map[string]bool{}}
}

func (o *opener) open(ctx context.Context, dev string) (Source, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	again := slices.Contains(o.opened, dev)
	o.opened = append(o.opened, dev)
	if again && o.fails[dev] {
		return nil, errors.New("parec did not start")
	}
	s := newSource(o.frames[dev], o.gate, o.ends[dev])
	delete(o.frames, dev) // a reopened device gives silence
	o.sources = append(o.sources, s)
	return s, nil
}

func (o *opener) didOpen(dev string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, d := range o.opened {
		if d == dev {
			return true
		}
	}
	return false
}

// devices returns a copy of the opened devices.
func (o *opener) devices() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.opened...)
}

// waitOpened waits until n sources exist.
func (o *opener) waitOpened(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(o.devices()) < n {
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d sources opened", len(o.devices()), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitDrained waits until the first n sources exist and sent all their frames.
func (o *opener) waitDrained(t *testing.T, n int) {
	t.Helper()
	o.waitOpened(t, n)
	deadline := time.Now().Add(5 * time.Second)
	for i := 0; i < n; i++ {
		o.mu.Lock()
		s := o.sources[i]
		o.mu.Unlock()
		select {
		case <-s.drained:
		case <-time.After(time.Until(deadline)):
			t.Fatal("the sources were not drained")
		}
	}
}

// --- fake pactl ---

type fakePactl struct {
	mu     sync.Mutex
	sink   string
	source string
	port   string
	calls  []string
}

func (f *fakePactl) run(ctx context.Context, name string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := strings.Join(args, " ")
	f.calls = append(f.calls, key)
	switch {
	case key == "get-default-sink":
		return f.sink + "\n", nil
	case key == "get-default-source":
		return f.source + "\n", nil
	case key == "list sinks":
		return "Sink #1\n\tName: " + f.sink + "\n\tActive Port: " + f.port + "\n", nil
	case strings.HasPrefix(key, "load-module"):
		return "7\n", nil
	case strings.HasPrefix(key, "set-default-sink "):
		f.sink = strings.TrimPrefix(key, "set-default-sink ")
	}
	return "", nil
}

func (f *fakePactl) set(sink string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sink = sink
}

func (f *fakePactl) called(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// --- fake Groq ---

type fakeClient struct {
	mu    sync.Mutex
	calls int
	fn    func(ctx context.Context) (groq.Result, error)
}

func (f *fakeClient) Transcribe(ctx context.Context, wav []byte, opts groq.Options) (groq.Result, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.fn != nil {
		return f.fn(ctx)
	}
	return groq.Result{Segments: []groq.Segment{{Start: 0, End: 1, Text: "olá mundo"}}}, nil
}

func (f *fakeClient) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// --- helpers ---

var fixedNow = time.Date(2026, 9, 23, 14, 30, 0, 0, time.Local)

func setup(t *testing.T, echoMode, port string) (Options, *fakePactl, *opener, *fakeClient) {
	t.Helper()
	fp := &fakePactl{sink: "alsa_output.speaker", source: "alsa_input.mic", port: port}
	op := newOpener()
	fc := &fakeClient{}
	o := Options{
		Title:       "Test",
		Config:      config.Config{Lang: "pt", Model: "m", Echo: echoMode},
		Store:       store.New(t.TempDir()),
		Client:      fc,
		Pactl:       audio.Pactl{Run: fp.run},
		Echo:        &audio.Echo{Pactl: audio.Pactl{Run: fp.run}, StatePath: filepath.Join(t.TempDir(), "echo.json")},
		Open:        op.open,
		Now:         func() time.Time { return fixedNow },
		PollEvery:   time.Hour,
		RenderEvery: time.Hour,
	}
	return o, fp, op, fc
}

// waitFor reads events until pred is true, and fails after 5 s.
func waitFor(t *testing.T, s *Session, what string, pred func(any) bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-s.Events():
			if pred(ev) {
				return
			}
		case <-deadline:
			t.Fatalf("no event: %s", what)
		}
	}
}

func isStopped(ev any) bool { _, ok := ev.(StoppedEvent); return ok }

func TestRecordsBothStreams(t *testing.T) {
	o, _, op, _ := setup(t, "off", "[Out] Speaker")
	op.frames["alsa_input.mic"] = utterance()
	op.frames["alsa_output.speaker.monitor"] = utterance()
	s, err := Start(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	close(op.gate)
	op.waitDrained(t, 2)
	s.Stop()
	waitFor(t, s, "StoppedEvent", isStopped)

	m, err := o.Store.Load(s.Meeting().Snapshot().ID)
	if err != nil {
		t.Fatal(err)
	}
	if m.Meta.Status != store.Done || m.Meta.EndedAt.IsZero() {
		t.Fatalf("meta %+v", m.Meta)
	}
	segs, _ := m.Segments()
	speakers := map[store.Speaker]bool{}
	for _, seg := range segs {
		speakers[seg.Speaker] = true
		if math.Abs(seg.Start-0.70) > 1e-6 {
			t.Errorf("segment start %v, want 0.70", seg.Start)
		}
	}
	if len(segs) != 2 || !speakers[store.Me] || !speakers[store.Them] {
		t.Fatalf("segments %+v", segs)
	}
	if left, _ := m.PendingChunks(); len(left) != 0 || m.IsLive() {
		t.Fatalf("chunks left %v, live %v", left, m.IsLive())
	}
	if _, err := os.Stat(filepath.Join(m.Dir, "transcript.md")); err != nil {
		t.Fatal(err)
	}
}

func TestPauseDropsAudio(t *testing.T) {
	o, _, op, fc := setup(t, "off", "[Out] Speaker")
	op.frames["alsa_input.mic"] = utterance()
	op.frames["alsa_output.speaker.monitor"] = utterance()
	s, err := Start(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	s.Pause()
	close(op.gate)
	op.waitDrained(t, 2)
	s.Resume()
	s.Stop()
	waitFor(t, s, "StoppedEvent", isStopped)
	if fc.count() != 0 {
		t.Fatalf("Groq got %d calls during a pause", fc.count())
	}
}

func TestEchoCancelOnSpeakers(t *testing.T) {
	o, fp, op, _ := setup(t, "auto", "[Out] Speaker")
	s, err := Start(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	op.waitOpened(t, 2)
	if !s.EchoOn() || !op.didOpen(audio.ECSource) || !op.didOpen("alsa_output.speaker.monitor") {
		t.Fatalf("echo %v, opened %v", s.EchoOn(), op.devices())
	}
	close(op.gate)
	s.Stop()
	waitFor(t, s, "StoppedEvent", isStopped)
	if !fp.called("set-default-sink alsa_output.speaker") || !fp.called("unload-module 7") {
		t.Fatal("oat did not restore the output and unload the module")
	}
}

func TestNoEchoCancelOnHeadphones(t *testing.T) {
	o, fp, op, _ := setup(t, "auto", "[Out] Headphones")
	s, err := Start(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	close(op.gate)
	op.waitOpened(t, 2)
	if s.EchoOn() || fp.called("load-module") || !op.didOpen("alsa_input.mic") {
		t.Fatal("echo cancel started on headphones")
	}
	s.Stop()
	waitFor(t, s, "StoppedEvent", isStopped)
}

func TestFollowsNewOutput(t *testing.T) {
	o, fp, op, _ := setup(t, "auto", "[Out] Speaker")
	o.PollEvery = 10 * time.Millisecond
	s, err := Start(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	close(op.gate)
	// A Bluetooth headset connects, and the desktop makes it the default output.
	fp.mu.Lock()
	fp.sink, fp.port = "bluez_output.headset", "[Out] Headset"
	fp.mu.Unlock()
	waitFor(t, s, "DevicesEvent for the headset", func(ev any) bool {
		d, ok := ev.(DevicesEvent)
		return ok && d.Monitor == "bluez_output.headset.monitor" && d.Mic == "alsa_input.mic"
	})
	deadline := time.Now().Add(5 * time.Second)
	for !op.didOpen("bluez_output.headset.monitor") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !op.didOpen("bluez_output.headset.monitor") || s.EchoOn() {
		t.Fatalf("opened %v, echo %v", op.devices(), s.EchoOn())
	}
	if fp.called("set-default-sink alsa_output.speaker") {
		t.Fatal("oat moved the output back to the speaker")
	}
	s.Stop()
	waitFor(t, s, "StoppedEvent", isStopped)
}

func TestDiskFailureStopsRecording(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	o, _, op, _ := setup(t, "off", "[Out] Speaker")
	op.frames["alsa_input.mic"] = utterance()
	s, err := Start(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(s.Meeting().ChunksDir(), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(s.Meeting().ChunksDir(), 0o755) })
	close(op.gate)
	waitFor(t, s, "fatal warning", func(ev any) bool {
		w, ok := ev.(transcribe.Warning)
		return ok && w.Fatal && strings.Contains(w.Msg, "Disk write failed")
	})
	waitFor(t, s, "StoppedEvent", isStopped)
}

func TestAbandonKeepsChunks(t *testing.T) {
	o, _, op, fc := setup(t, "off", "[Out] Speaker")
	fc.fn = func(ctx context.Context) (groq.Result, error) {
		<-ctx.Done()
		return groq.Result{}, ctx.Err()
	}
	op.frames["alsa_input.mic"] = utterance()
	s, err := Start(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	close(op.gate)
	waitFor(t, s, "QueueEvent", func(ev any) bool { q, ok := ev.(transcribe.QueueEvent); return ok && q.Pending == 1 })
	s.Abandon()
	m, err := o.Store.Load(s.Meeting().Snapshot().ID)
	if err != nil {
		t.Fatal(err)
	}
	if left, _ := m.PendingChunks(); len(left) != 1 {
		t.Fatalf("chunks left %v", left)
	}
	if m.Meta.Status != store.Interrupted {
		t.Fatalf("status %s, want interrupted", m.Meta.Status)
	}
}

func TestReopenFailureKeepsAudio(t *testing.T) {
	o, _, op, fc := setup(t, "off", "[Out] Speaker")
	// The mic stream ends after speech with no trailing silence, and it does not open again.
	op.frames["alsa_input.mic"] = utterance()[:145]
	op.ends["alsa_input.mic"] = true
	op.fails["alsa_input.mic"] = true
	s, err := Start(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	close(op.gate)
	op.waitOpened(t, 3) // the mic, the monitor, and the failed mic reopen
	s.Stop()
	waitFor(t, s, "StoppedEvent", isStopped)
	if fc.count() != 1 {
		t.Fatalf("Groq got %d calls, want 1: the speech in the chunker was lost", fc.count())
	}
	m, err := o.Store.Load(s.Meeting().Snapshot().ID)
	if err != nil {
		t.Fatal(err)
	}
	segs, _ := m.Segments()
	if len(segs) != 1 || segs[0].Speaker != store.Me {
		t.Fatalf("segments %+v", segs)
	}
}

func TestDrainSendsLeftovers(t *testing.T) {
	st := store.New(t.TempDir())
	m, err := st.Create("Crash", fixedNow, "pt", "m", "off")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(m.ChunksDir(), store.ChunkName(store.Them, 30)), chunk.EncodeWAV(make([]int16, 16000)), 0o644)
	os.WriteFile(filepath.Join(m.Dir, ".lock"), []byte(strconv.Itoa(999999999)), 0o644)

	var mu sync.Mutex
	var events []DrainEvent
	Drain(context.Background(), st, &fakeClient{}, config.Default(), func(ev any) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, ev.(DrainEvent))
	})
	got, _ := st.Load(m.Meta.ID)
	if got.Meta.Status != store.Done {
		t.Fatalf("status %s", got.Meta.Status)
	}
	if want := fixedNow.Add(31 * time.Second); !got.Meta.EndedAt.Equal(want) {
		t.Fatalf("EndedAt %v, want %v", got.Meta.EndedAt, want)
	}
	if segs, _ := got.Segments(); len(segs) != 1 {
		t.Fatalf("segments %+v", segs)
	}
	if len(events) == 0 || !events[len(events)-1].Done {
		t.Fatalf("events %+v", events)
	}
}

// statusOnDisk reads the status in meta.json without Load, which marks an
// unlocked meeting as interrupted.
func statusOnDisk(t *testing.T, dir string) store.Status {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta store.Meta
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatal(err)
	}
	return meta.Status
}

func TestLeftoverChunksKeepProcessing(t *testing.T) {
	o, _, op, _ := setup(t, "off", "[Out] Speaker")
	op.frames["alsa_input.mic"] = utterance()
	s, err := Start(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	// A folder with the name transcript.jsonl makes AppendSegments fail.
	if err := os.Mkdir(filepath.Join(s.Meeting().Dir, "transcript.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}
	close(op.gate)
	op.waitDrained(t, 2)
	s.Stop()
	var stopped StoppedEvent
	waitFor(t, s, "StoppedEvent", func(ev any) bool {
		e, ok := ev.(StoppedEvent)
		if ok {
			stopped = e
		}
		return ok
	})
	if stopped.Err == nil {
		t.Fatal("StoppedEvent.Err is nil, but a chunk was not transcribed")
	}
	if got := s.Meeting().Snapshot().Status; got != store.Processing {
		t.Fatalf("status %s, want processing", got)
	}
	if got := statusOnDisk(t, s.Meeting().Dir); got != store.Processing {
		t.Fatalf("status in meta.json %s, want processing", got)
	}
	if left, err := s.Meeting().PendingChunks(); err != nil || len(left) != 1 {
		t.Fatalf("chunks left %v (%v), want 1", left, err)
	}
}

func TestDrainKeepsProcessingWhenWriteFails(t *testing.T) {
	st := store.New(t.TempDir())
	m, err := st.Create("Crash", fixedNow, "pt", "m", "off")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(m.ChunksDir(), store.ChunkName(store.Them, 30))
	os.WriteFile(path, chunk.EncodeWAV(make([]int16, 16000)), 0o644)
	os.WriteFile(filepath.Join(m.Dir, ".lock"), []byte(strconv.Itoa(999999999)), 0o644)
	blocker := filepath.Join(m.Dir, "transcript.jsonl")
	if err := os.Mkdir(blocker, 0o755); err != nil {
		t.Fatal(err)
	}

	Drain(context.Background(), st, &fakeClient{}, config.Default(), func(any) {})
	if got := statusOnDisk(t, m.Dir); got != store.Processing {
		t.Fatalf("status %s after a failed write, want processing", got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the chunk is not on disk: %v", err)
	}

	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	Drain(context.Background(), st, &fakeClient{}, config.Default(), func(any) {})
	if got := statusOnDisk(t, m.Dir); got != store.Done {
		t.Fatalf("status %s after the second drain, want done", got)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the chunk is still on disk: %v", err)
	}
}

func TestSetLangSavesMeta(t *testing.T) {
	o, _, op, _ := setup(t, "off", "[Out] Speaker")
	s, err := Start(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	close(op.gate)
	if err := s.SetLang("en"); err != nil {
		t.Fatal(err)
	}
	if s.Lang() != "en" || s.Meeting().Snapshot().Lang != "en" {
		t.Fatal("language not changed")
	}
	s.Stop()
	waitFor(t, s, "StoppedEvent", isStopped)
}
