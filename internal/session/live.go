package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/matheuscamposmt/oat/internal/chunk"
	"github.com/matheuscamposmt/oat/internal/claudestt"
	"github.com/matheuscamposmt/oat/internal/store"
	"github.com/matheuscamposmt/oat/internal/stt"
	"github.com/matheuscamposmt/oat/internal/transcribe"
)

// LiveStream is one live transcription stream. *claudestt.Stream is one.
type LiveStream interface {
	Send(pcm []byte) error
	Finish() ([]string, error)
	Close()
}

// LiveFunc opens a live stream. onText gets the full partial text after each change.
type LiveFunc func(ctx context.Context, lang string, keyterms []string, onText func(string)) (LiveStream, error)

// LiveStateEvent tells the HUD how the text arrives now: "live" while the
// live streams work, "replay" after a live stream failed and its chunk went to
// the queue, and "waiting for login" when the Claude Code token expired.
type LiveStateEvent struct{ State string }

// The states of LiveStateEvent.
const (
	StateLive    = "live"
	StateReplay  = "replay"
	StateNoLogin = "waiting for login"
)

// LiveEvent carries the partial text of a segment that is still open. Done
// comes when the final segments of that segment are written, or when the live
// transcript failed and its chunk went to the queue.
type LiveEvent struct {
	ID      int64
	Speaker store.Speaker
	Start   float64 // seconds on the meeting clock
	Text    string
	Done    bool
}

const (
	// gateFrames is the speech in a chunk before it opens a stream: 0.5 s.
	// A key press or a cough makes a chunk that ends before the gate, so it
	// opens no connection.
	gateFrames = 25
	// liveBuffer holds the frames that wait for the network: about 60 s.
	liveBuffer = 3000
	// sendFrames joins 5 frames of 20 ms into one message of 100 ms.
	sendFrames = 5
	// warnEvery is the shortest time between two live warnings.
	warnEvery = 30 * time.Second
)

// lane follows the chunks of one speaker and sends each one to its own live
// stream. The capture goroutine calls all its methods, so it needs no lock.
type lane struct {
	s     *Session
	sp    store.Speaker
	cur   *liveSeg // the segment of the chunk that grows
	ended *liveSeg // the segment that ended and waits for its chunk file
}

// liveSeg is the live transcript of one chunk.
type liveSeg struct {
	id     int64
	start  int64 // meeting-clock frame index of the first frame
	n      int64 // frames seen
	speech int
	held   [][]int16 // frames before the gate opens
	frames chan []int16
	path   chan string // the chunk file, or "" when the chunk had too little speech
	lost   bool        // the buffer was full, so the stream misses frames
}

func newLane(s *Session, sp store.Speaker) *lane { return &lane{s: s, sp: sp} }

// ChunkStart implements chunk.Observer.
func (l *lane) ChunkStart(start int64, frames [][]int16) {
	g := l.newSeg(start)
	for _, f := range frames {
		l.add(g, f)
	}
	g.speech = 1 // the frame that started the chunk is speech
	l.maybeOpen(g)
}

// ChunkFrame implements chunk.Observer.
func (l *lane) ChunkFrame(samples []int16, speech bool) {
	g := l.cur
	if g == nil {
		return
	}
	if speech {
		g.speech++
	}
	l.add(g, samples)
	l.maybeOpen(g)
}

// ChunkEnd implements chunk.Observer.
func (l *lane) ChunkEnd(next bool) {
	g := l.cur
	if g == nil {
		return
	}
	l.cur = nil
	if g.frames != nil {
		close(g.frames)
	}
	l.ended = g
	if next {
		// The speech goes on, so the next stream opens at once.
		n := l.newSeg(g.start + g.n)
		n.speech = gateFrames
		l.maybeOpen(n)
	}
}

// settle gives the chunk file of the segment that ended. The capture calls it
// after each Push and Flush. Without a segment that ended, it queues the file.
func (l *lane) settle(path string) {
	g := l.ended
	l.ended = nil
	switch {
	case g == nil:
		if path != "" {
			l.s.pipe.Enqueue(path)
		}
	case g.frames == nil:
		// The gate never opened, so no stream heard this chunk.
		if path != "" {
			l.s.pipe.Enqueue(path)
		}
	default:
		g.path <- path
	}
}

func (l *lane) newSeg(start int64) *liveSeg {
	l.s.liveMu.Lock()
	l.s.liveID++
	id := l.s.liveID
	l.s.liveMu.Unlock()
	g := &liveSeg{id: id, start: start, path: make(chan string, 1)}
	l.cur = g
	return g
}

// add counts a frame and sends it, or holds it until the gate opens.
func (l *lane) add(g *liveSeg, f []int16) {
	g.n++
	if g.frames == nil {
		g.held = append(g.held, f)
		return
	}
	select {
	case g.frames <- f:
	default:
		g.lost = true
	}
}

// maybeOpen starts the stream of a segment when it has enough speech.
func (l *lane) maybeOpen(g *liveSeg) {
	if g.frames != nil || g.speech < gateFrames {
		return
	}
	g.frames = make(chan []int16, liveBuffer)
	for _, f := range g.held {
		g.frames <- f // the buffer is larger than any held part
	}
	g.held = nil
	l.s.liveWG.Add(1)
	go l.s.runSeg(l, g, l.s.Lang())
}

// runSeg streams one segment, then writes its text, or queues its chunk file
// when the live transcript failed.
func (s *Session) runSeg(l *lane, g *liveSeg, lang string) {
	defer s.liveWG.Done()
	start := float64(g.start) * chunk.FrameSeconds
	onText := func(t string) {
		s.emit(LiveEvent{ID: g.id, Speaker: l.sp, Start: start, Text: t})
	}
	var utts []string
	st, err := s.opts.Live(s.liveCtx, lang, s.opts.Keyterms, onText)
	if err == nil {
		err = sendFramesTo(st, g.frames)
		if err == nil {
			utts, err = st.Finish()
		} else {
			st.Close()
		}
	} else {
		for range g.frames { // wait for the end of the chunk
		}
	}
	if err == nil && g.lost {
		err = errors.New("the network was too slow, and the live buffer was full")
	}
	path := <-g.path
	done := LiveEvent{ID: g.id, Speaker: l.sp, Start: start, Done: true}
	if err != nil {
		s.emit(done)
		if s.liveCtx.Err() != nil {
			return // abandoned: the chunk file stays on disk for the next start
		}
		s.setLiveState(stateFor(err))
		s.liveWarn(err, path != "")
		if path != "" {
			s.pipe.Enqueue(path)
		}
		return
	}
	s.setLiveState(StateLive)
	dur := float64(g.n) * chunk.FrameSeconds
	res := stt.Result{Text: strings.Join(utts, " "), Segments: claudestt.Place(utts, dur)}
	// Done comes after the final lines, so the HUD never shows neither of them.
	// When the write fails, Save leaves the chunk file for the next start.
	s.pipe.Save(path, l.sp, start, dur, res)
	s.emit(done)
}

// sendFramesTo sends the frames in messages of 100 ms until the channel closes.
// After an error it reads the rest of the frames, so the capture never waits.
func sendFramesTo(st LiveStream, frames <-chan []int16) error {
	var err error
	buf := make([]byte, 0, sendFrames*chunk.FrameSamples*2)
	for f := range frames {
		if err != nil {
			continue
		}
		buf = appendPCM(buf[:0], f)
	join:
		for n := 1; n < sendFrames; n++ {
			select {
			case more, ok := <-frames:
				if !ok {
					break join
				}
				buf = appendPCM(buf, more)
			default:
				break join
			}
		}
		err = st.Send(buf)
	}
	return err
}

func appendPCM(b []byte, f []int16) []byte {
	for _, v := range f {
		b = append(b, byte(v), byte(uint16(v)>>8))
	}
	return b
}

// stateFor returns the state after a live failure.
func stateFor(err error) string {
	var auth *stt.AuthError
	if errors.As(err, &auth) || strings.Contains(err.Error(), "login expired") {
		return StateNoLogin
	}
	return StateReplay
}

// setLiveState emits a LiveStateEvent when the state changes.
func (s *Session) setLiveState(state string) {
	s.liveMu.Lock()
	changed := s.liveStateNow != state
	s.liveStateNow = state
	s.liveMu.Unlock()
	if changed {
		s.emit(LiveStateEvent{State: state})
	}
}

// liveWarn shows a live failure in the HUD, at most once in warnEvery.
// queued tells whether a chunk file went to the queue. A part with less than
// 1 s of speech has no file, so its text is lost.
func (s *Session) liveWarn(err error, queued bool) {
	s.liveMu.Lock()
	now := time.Now()
	if now.Sub(s.liveWarned) < warnEvery {
		s.liveMu.Unlock()
		return
	}
	s.liveWarned = now
	s.liveMu.Unlock()
	what := "The chunk goes to the queue."
	if !queued {
		what = "The part was too short to keep on disk, so its text is lost."
	}
	s.emit(transcribe.Warning{Msg: fmt.Sprintf("Live transcript failed (%v). %s", err, what)})
}

// liveState holds the fields of a Session for the live transcript.
type liveState struct {
	liveCtx      context.Context
	liveCancel   context.CancelFunc
	liveWG       sync.WaitGroup
	liveMu       sync.Mutex
	liveID       int64
	liveWarned   time.Time
	liveStateNow string
	lanes        map[store.Speaker]*lane
}
