//go:build live

package session

// A live test of the whole pipeline with the Claude backend. It needs a Claude Code login.
//
//	OAT_LIVE_PCM=/path/16k-mono-s16le.raw go test -tags live -run Live -v ./internal/session/

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matheuscamposmt/oat/internal/audio"
	"github.com/matheuscamposmt/oat/internal/chunk"
	"github.com/matheuscamposmt/oat/internal/claudestt"
	"github.com/matheuscamposmt/oat/internal/config"
	"github.com/matheuscamposmt/oat/internal/store"
	"github.com/matheuscamposmt/oat/internal/transcribe"
)

func TestLiveDrainWithClaude(t *testing.T) {
	path := os.Getenv("OAT_LIVE_PCM")
	if path == "" {
		t.Skip("set OAT_LIVE_PCM")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	creds, err := claudestt.CredentialsPath()
	if err != nil {
		t.Fatal(err)
	}
	st := store.New(t.TempDir())
	cfg := config.Default()
	m, err := st.Create("Live", time.Now().Add(-time.Minute), cfg.Lang, cfg.MeetingModel(), "off")
	if err != nil {
		t.Fatal(err)
	}
	// Three chunks: Me at 0 s, Them at 20 s, Me at 40 s.
	for i, sp := range []store.Speaker{store.Me, store.Them, store.Me} {
		a := (400 + i*20) * 32000
		pcm := raw[a : a+20*32000]
		s := make([]int16, len(pcm)/2)
		for j := range s {
			s[j] = int16(uint16(pcm[2*j]) | uint16(pcm[2*j+1])<<8)
		}
		name := filepath.Join(m.ChunksDir(), store.ChunkName(sp, float64(i*20)))
		if err := os.WriteFile(name, chunk.EncodeWAV(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_ = m.Update(func(mm *store.Meta) { mm.Status = store.Interrupted })
	m.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	Drain(ctx, st, claudestt.New(creds, "oat/live-test"), cfg, func(ev any) { t.Logf("event %+v", ev) })
	md, err := os.ReadFile(filepath.Join(filepath.Dir(m.ChunksDir()), "transcript.md"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("transcript.md:\n%s", md)
	left, _ := m.PendingChunks()
	if len(left) != 0 {
		t.Fatalf("%d chunks were not sent", len(left))
	}
}

// TestLiveEchoFilter sends the same audio as Them and as Me, with the chunk
// borders 7 s apart, and counts the Me segments that store.Clean keeps.
func TestLiveEchoFilter(t *testing.T) {
	path := os.Getenv("OAT_LIVE_PCM")
	if path == "" {
		t.Skip("set OAT_LIVE_PCM")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	creds, _ := claudestt.CredentialsPath()
	st := store.New(t.TempDir())
	cfg := config.Default()
	m, err := st.Create("Echo", time.Now().Add(-time.Minute), cfg.Lang, cfg.MeetingModel(), "off")
	if err != nil {
		t.Fatal(err)
	}
	write := func(sp store.Speaker, at, secs float64) {
		a := int(400+at) * 32000
		pcm := raw[a : a+int(secs)*32000]
		s := make([]int16, len(pcm)/2)
		for j := range s {
			s[j] = int16(uint16(pcm[2*j]) | uint16(pcm[2*j+1])<<8)
		}
		_ = os.WriteFile(filepath.Join(m.ChunksDir(), store.ChunkName(sp, at)), chunk.EncodeWAV(s), 0o644)
	}
	for _, at := range []float64{0, 20, 40} {
		write(store.Them, at, 20)
	}
	write(store.Me, 7, 20)
	write(store.Me, 27, 20)
	_ = m.Update(func(mm *store.Meta) { mm.Status = store.Interrupted })
	m.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	Drain(ctx, st, claudestt.New(creds, "oat/live-test"), cfg, func(any) {})
	segs, _ := m.Segments()
	me, kept := 0, 0
	for _, s := range segs {
		if s.Speaker == store.Me {
			me++
		}
	}
	for _, s := range store.Clean(segs) {
		if s.Speaker == store.Me {
			kept++
			t.Logf("kept Me [%5.1f-%5.1f] %s", s.Start, s.End, s.Text)
		}
	}
	t.Logf("%d raw segments; Me: %d raw, %d kept after Clean", len(segs), me, kept)
}

// fileSource plays 16 kHz mono s16le PCM as 20 ms frames, faster than real
// time, then waits for Close.
type fileSource struct {
	frames chan []int16
	done   chan struct{}
}

func newFileSource(pcm []byte, speed float64) *fileSource {
	s := &fileSource{frames: make(chan []int16), done: make(chan struct{})}
	go func() {
		const n = 320 // 20 ms
		for i := 0; i+2*n <= len(pcm); i += 2 * n {
			f := make([]int16, n)
			for j := range f {
				f[j] = int16(uint16(pcm[i+2*j]) | uint16(pcm[i+2*j+1])<<8)
			}
			select {
			case s.frames <- f:
			case <-s.done:
				return
			}
			time.Sleep(time.Duration(float64(20*time.Millisecond) / speed))
		}
		<-s.done
	}()
	return s
}

func (s *fileSource) Frames() <-chan []int16 { return s.frames }
func (s *fileSource) Close() error {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	return nil
}
func (s *fileSource) Err() error { return nil }

// TestLiveSession records two files as Me and Them through the whole session:
// the chunker, the pipeline, the Claude backend, and transcript.md.
//
//	OAT_LIVE_ME=a.raw OAT_LIVE_THEM=b.raw go test -tags live -run LiveSession -v ./internal/session/
func TestLiveSession(t *testing.T) {
	mePath, themPath := os.Getenv("OAT_LIVE_ME"), os.Getenv("OAT_LIVE_THEM")
	if mePath == "" || themPath == "" {
		t.Skip("set OAT_LIVE_ME and OAT_LIVE_THEM")
	}
	secs := 120
	read := func(p string, from int) []byte {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return b[from*32000 : (from+secs)*32000]
	}
	me, them := read(mePath, 0), read(themPath, 120)
	creds, _ := claudestt.CredentialsPath()
	cfg := config.Default()
	cfg.Echo = "off"
	st := store.New(t.TempDir())
	var opened sync.Map
	speed := 5.0
	client := claudestt.New(creds, "oat/live-test")
	var live LiveFunc
	var liveEvents int
	if os.Getenv("OAT_LIVE_MODE") == "live" {
		speed = 1 // the live stream must get real time
		live = func(ctx context.Context, lang string, onText func(string)) (LiveStream, error) {
			st, err := client.Open(ctx, lang, onText)
			if err != nil {
				return nil, err
			}
			return st, nil
		}
	}
	s, err := Start(context.Background(), Options{
		Title: "Live session", Config: cfg, Store: st,
		Client: client, Live: live,
		Pactl: audio.NewPactl(),
		Open: func(ctx context.Context, dev string) (Source, error) {
			if _, again := opened.LoadOrStore(dev, true); again {
				return nil, fmt.Errorf("device %s opened twice", dev)
			}
			if strings.HasSuffix(dev, ".monitor") {
				return newFileSource(them, speed), nil
			}
			return newFileSource(me, speed), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	var segs, warnings int
	stopAt := time.After(time.Duration(float64(secs) * float64(time.Second) / speed))
	for {
		select {
		case <-stopAt:
			t.Logf("stop after %v; %d chunks wait", time.Since(t0).Round(time.Second), s.Pending())
			s.Stop()
		case ev := <-s.Events():
			switch e := ev.(type) {
			case transcribe.SegmentsEvent:
				segs += len(e.Segments)
			case LiveEvent:
				liveEvents++
			case transcribe.Warning:
				warnings++
				t.Logf("warning: %+v", e)
			case StoppedEvent:
				t.Logf("stopped after %v, err=%v; %d segments, %d warnings, %d live events", time.Since(t0).Round(time.Second), e.Err, segs, warnings, liveEvents)
				md, _ := os.ReadFile(filepath.Join(filepath.Dir(s.Meeting().ChunksDir()), "transcript.md"))
				t.Logf("transcript.md:\n%s", md)
				meta := s.Meeting().Snapshot()
				if e.Err != nil || meta.Status != store.Done || segs == 0 {
					t.Fatalf("status %s, err %v, %d segments", meta.Status, e.Err, segs)
				}
				return
			}
		case <-time.After(4 * time.Minute):
			t.Fatal("no StoppedEvent")
		}
	}
}
