package session

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/matheuscamposmt/oat/internal/store"
	"github.com/matheuscamposmt/oat/internal/transcribe"
)

// fakeLive records the streams that the session opens.
type fakeLive struct {
	mu      sync.Mutex
	opened  int
	bytes   int
	openErr error
	sendErr error
	text    string
	block   chan struct{} // Finish waits for it when not nil
}

type fakeStream struct {
	f      *fakeLive
	onText func(string)
	ctx    context.Context
}

func (f *fakeLive) open(ctx context.Context, lang string, onText func(string)) (LiveStream, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened++
	if f.openErr != nil {
		return nil, f.openErr
	}
	return &fakeStream{f: f, onText: onText, ctx: ctx}, nil
}

func (s *fakeStream) Send(pcm []byte) error {
	s.f.mu.Lock()
	s.f.bytes += len(pcm)
	err := s.f.sendErr
	s.f.mu.Unlock()
	if err == nil {
		s.onText("parcial")
	}
	return err
}

func (s *fakeStream) Finish() ([]string, error) {
	if s.f.block != nil {
		select {
		case <-s.f.block:
		case <-s.ctx.Done():
			return nil, s.ctx.Err()
		}
	}
	text := s.f.text
	if text == "" {
		text = "Texto ao vivo."
	}
	return []string{text}, nil
}

func (s *fakeStream) Close() {}

func (f *fakeLive) count() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opened, f.bytes
}

func TestLiveWritesTheTextAndDeletesTheChunk(t *testing.T) {
	o, _, op, fc := setup(t, "off", "[Out] Speaker")
	fl := &fakeLive{}
	o.Live = fl.open
	op.frames["alsa_input.mic"] = utterance()
	s, err := Start(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	close(op.gate)
	var partial, done bool
	waitFor(t, s, "the final live segment", func(ev any) bool {
		switch e := ev.(type) {
		case LiveEvent:
			if e.Done {
				done = true
			} else if e.Text == "parcial" && e.Speaker == store.Me {
				partial = true
			}
		case transcribe.SegmentsEvent:
			return len(e.Segments) == 1 && e.Segments[0].Text == "Texto ao vivo."
		}
		return false
	})
	op.waitDrained(t, 2)
	s.Stop()
	waitFor(t, s, "StoppedEvent", isStopped)
	if !partial || !done {
		t.Fatalf("partial %v, done %v", partial, done)
	}
	if fc.count() != 0 {
		t.Fatalf("the replay client got %d calls; the live text must replace it", fc.count())
	}
	opened, bytes := fl.count()
	if opened != 1 || bytes == 0 {
		t.Fatalf("opened %d streams, sent %d bytes", opened, bytes)
	}
	m := s.Meeting()
	if left, _ := m.PendingChunks(); len(left) != 0 {
		t.Fatalf("chunks left %v", left)
	}
	if m.Snapshot().Status != store.Done {
		t.Fatalf("status %s", m.Snapshot().Status)
	}
	segs, _ := m.Segments()
	if len(segs) != 1 || segs[0].Speaker != store.Me || segs[0].Start < 0.6 || segs[0].Start > 0.8 {
		t.Fatalf("segments %+v, want one Me segment that starts at about 0.7 s", segs)
	}
}

func TestLiveFailureQueuesTheChunk(t *testing.T) {
	for name, fl := range map[string]*fakeLive{
		"open fails": {openErr: errors.New("4029")},
		"send fails": {sendErr: errors.New("broken pipe")},
	} {
		t.Run(name, func(t *testing.T) {
			o, _, op, fc := setup(t, "off", "[Out] Speaker")
			o.Live = fl.open
			op.frames["alsa_input.mic"] = utterance()
			s, err := Start(context.Background(), o)
			if err != nil {
				t.Fatal(err)
			}
			close(op.gate)
			waitFor(t, s, "a live warning", func(ev any) bool {
				w, ok := ev.(transcribe.Warning)
				return ok && strings.Contains(w.Msg, "Live transcript failed")
			})
			op.waitDrained(t, 2)
			s.Stop()
			waitFor(t, s, "StoppedEvent", isStopped)
			if fc.count() != 1 {
				t.Fatalf("the replay client got %d calls, want 1", fc.count())
			}
			segs, _ := s.Meeting().Segments()
			if len(segs) != 1 || segs[0].Text != "olá mundo" {
				t.Fatalf("segments %+v, want the replay text", segs)
			}
		})
	}
}

func TestShortBurstOpensNoStream(t *testing.T) {
	o, _, op, _ := setup(t, "off", "[Out] Speaker")
	fl := &fakeLive{}
	o.Live = fl.open
	var frames [][]int16
	for range 50 {
		frames = append(frames, noise())
	}
	for i := range 10 { // 0.2 s: a key press
		frames = append(frames, tone(i))
	}
	for range 200 {
		frames = append(frames, noise())
	}
	op.frames["alsa_input.mic"] = frames
	s, err := Start(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	close(op.gate)
	op.waitDrained(t, 2)
	s.Stop()
	waitFor(t, s, "StoppedEvent", isStopped)
	if opened, _ := fl.count(); opened != 0 {
		t.Fatalf("a 0.2 s burst opened %d streams", opened)
	}
}

func TestAbandonDuringLiveKeepsTheChunk(t *testing.T) {
	o, _, op, fc := setup(t, "off", "[Out] Speaker")
	fl := &fakeLive{block: make(chan struct{})}
	o.Live = fl.open
	op.frames["alsa_input.mic"] = utterance()
	s, err := Start(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	close(op.gate)
	op.waitDrained(t, 2)
	waitFor(t, s, "a partial text", func(ev any) bool { e, ok := ev.(LiveEvent); return ok && !e.Done })
	s.Abandon()
	m, err := o.Store.Load(s.Meeting().Snapshot().ID)
	if err != nil {
		t.Fatal(err)
	}
	left, _ := m.PendingChunks()
	if len(left) != 1 {
		t.Fatalf("chunks left %v, want the chunk on disk", left)
	}
	if fc.count() != 0 {
		t.Fatalf("the replay client got %d calls", fc.count())
	}
	if _, err := os.Stat(left[0]); err != nil {
		t.Fatal(err)
	}
}

func TestStopWaitsForAFailingLiveSegment(t *testing.T) {
	o, _, op, fc := setup(t, "off", "[Out] Speaker")
	fl := &fakeLive{sendErr: errors.New("reset by peer")}
	o.Live = fl.open
	op.frames["alsa_input.mic"] = utterance()
	op.ends["alsa_input.mic"] = false
	s, err := Start(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	close(op.gate)
	op.waitDrained(t, 2)
	s.Stop() // the segment fails while the stop runs; its chunk must still reach the queue
	waitFor(t, s, "StoppedEvent", isStopped)
	if fc.count() != 1 {
		t.Fatalf("the replay client got %d calls, want 1", fc.count())
	}
}
