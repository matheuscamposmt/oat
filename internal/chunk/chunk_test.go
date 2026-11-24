package chunk

import (
	"encoding/binary"
	"math"
	"testing"
)

// tone returns a 440 Hz frame at about -13.5 dBFS.
func tone(idx int) []int16 {
	f := make([]int16, FrameSamples)
	for i := range f {
		t := float64(idx*FrameSamples+i) / SampleRate
		f[i] = int16(0.3 * 32767 * math.Sin(2*math.Pi*440*t))
	}
	return f
}

// noise returns a frame at about -70 dBFS.
func noise() []int16 {
	f := make([]int16, FrameSamples)
	for i := range f {
		f[i] = 10
		if i%2 == 1 {
			f[i] = -10
		}
	}
	return f
}

// speech returns n frames that sound like speech: 400 ms of tone, then 100 ms of noise.
// With n = 25k+20, the last frame is a tone frame.
func speech(n int) [][]int16 {
	out := make([][]int16, n)
	for i := range out {
		if i%25 < 20 {
			out[i] = tone(i)
		} else {
			out[i] = noise()
		}
	}
	return out
}

func silence(n int, digital bool) [][]int16 {
	out := make([][]int16, n)
	for i := range out {
		if digital {
			out[i] = make([]int16, FrameSamples)
		} else {
			out[i] = noise()
		}
	}
	return out
}

// feed pushes frames with consecutive indexes from *idx and collects the chunks.
func feed(c *Chunker, idx *int64, frames [][]int16) []*Chunk {
	var out []*Chunk
	for _, f := range frames {
		if ch := c.Push(*idx, f); ch != nil {
			out = append(out, ch)
		}
		*idx++
	}
	return out
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestLevelDB(t *testing.T) {
	if got := LevelDB(make([]int16, FrameSamples)); got != MinDB {
		t.Fatalf("zeros: got %v, want %v", got, MinDB)
	}
	half := make([]int16, FrameSamples)
	for i := range half {
		half[i] = 16384
	}
	if got := LevelDB(half); math.Abs(got-(-6.02)) > 0.01 {
		t.Fatalf("half scale: got %v, want -6.02", got)
	}
}

func TestNormalCut(t *testing.T) {
	c, idx := New(), int64(0)
	var got []*Chunk
	got = append(got, feed(c, &idx, silence(50, false))...)
	got = append(got, feed(c, &idx, speech(595))...)
	got = append(got, feed(c, &idx, silence(100, false))...)
	if len(got) != 1 {
		t.Fatalf("got %d chunks, want 1", len(got))
	}
	ch := got[0]
	if !near(ch.Start, 0.70) {
		t.Errorf("Start = %v, want 0.70 (300 ms of preroll)", ch.Start)
	}
	if len(ch.Samples) != 637*FrameSamples {
		t.Errorf("frames = %d, want 637", len(ch.Samples)/FrameSamples)
	}
	if !near(ch.Speech, 12.1) {
		t.Errorf("Speech = %v, want 12.1", ch.Speech)
	}
}

func TestEarlyCut(t *testing.T) {
	c, idx := New(), int64(0)
	var got []*Chunk
	got = append(got, feed(c, &idx, silence(50, false))...)
	got = append(got, feed(c, &idx, speech(95))...)
	got = append(got, feed(c, &idx, silence(200, false))...)
	if len(got) != 1 {
		t.Fatalf("got %d chunks, want 1", len(got))
	}
	if len(got[0].Samples) != 135*FrameSamples {
		t.Errorf("frames = %d, want 135", len(got[0].Samples)/FrameSamples)
	}
	if !near(got[0].Speech, 2.1) {
		t.Errorf("Speech = %v, want 2.1", got[0].Speech)
	}
}

func TestDropShortSpeech(t *testing.T) {
	c, idx := New(), int64(0)
	var got []*Chunk
	got = append(got, feed(c, &idx, silence(50, false))...)
	got = append(got, feed(c, &idx, speech(20))...)
	got = append(got, feed(c, &idx, silence(200, false))...)
	if ch := c.Flush(); ch != nil {
		got = append(got, ch)
	}
	if len(got) != 0 {
		t.Fatalf("got %d chunks, want 0", len(got))
	}
}

func TestHardCutAtQuietestFrame(t *testing.T) {
	c, idx := New(), int64(0)
	var got []*Chunk
	got = append(got, feed(c, &idx, silence(50, false))...)
	got = append(got, feed(c, &idx, speech(1795))...)
	got = append(got, feed(c, &idx, silence(200, false))...)
	if ch := c.Flush(); ch != nil {
		got = append(got, ch)
	}
	if len(got) != 2 {
		t.Fatalf("got %d chunks, want 2", len(got))
	}
	// The first noise frame in the last 2 s of the 30 s chunk is at chunk offset 1410.
	if len(got[0].Samples) != 1411*FrameSamples {
		t.Errorf("first chunk frames = %d, want 1411", len(got[0].Samples)/FrameSamples)
	}
	if !near(got[1].Start, 0.70+1411*FrameSeconds) {
		t.Errorf("second chunk Start = %v, want %v", got[1].Start, 0.70+1411*FrameSeconds)
	}
}

func TestGapEndsChunk(t *testing.T) {
	c, idx := New(), int64(0)
	var got []*Chunk
	got = append(got, feed(c, &idx, silence(50, false))...)
	got = append(got, feed(c, &idx, speech(145))...)
	if len(got) != 0 {
		t.Fatalf("got a chunk before the gap")
	}
	ch := c.Push(idx+300, noise())
	if ch == nil {
		t.Fatal("the gap did not end the chunk")
	}
	if !near(ch.Start, 0.70) || len(ch.Samples) != 160*FrameSamples {
		t.Errorf("Start = %v, frames = %d, want 0.70 and 160", ch.Start, len(ch.Samples)/FrameSamples)
	}
}

func TestDigitalSilence(t *testing.T) {
	c, idx := New(), int64(0)
	var got []*Chunk
	got = append(got, feed(c, &idx, silence(50, true))...)
	got = append(got, feed(c, &idx, speech(95))...)
	got = append(got, feed(c, &idx, silence(200, true))...)
	if len(got) != 1 || !near(got[0].Start, 0.70) {
		t.Fatalf("got %d chunks, want 1 that starts at 0.70", len(got))
	}
}

func TestEncodeWAV(t *testing.T) {
	b := EncodeWAV([]int16{1, -2})
	if len(b) != 48 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" || string(b[36:40]) != "data" {
		t.Fatalf("bad header: %q", b[:44])
	}
	if binary.LittleEndian.Uint32(b[4:]) != 40 || binary.LittleEndian.Uint32(b[40:]) != 4 {
		t.Fatal("bad sizes")
	}
	if b[44] != 0x01 || b[45] != 0x00 || b[46] != 0xFE || b[47] != 0xFF {
		t.Fatalf("bad samples: % x", b[44:])
	}
	if d := WAVDuration(EncodeWAV(make([]int16, SampleRate))); d != 1 {
		t.Fatalf("WAVDuration = %v, want 1", d)
	}
}

type obsLog struct {
	starts []int64
	first  []int // frames given with each start
	frames []int // frames seen for each chunk, including the start frames
	ends   []bool
}

func (o *obsLog) ChunkStart(start int64, frames [][]int16) {
	o.starts = append(o.starts, start)
	o.first = append(o.first, len(frames))
	o.frames = append(o.frames, len(frames))
}
func (o *obsLog) ChunkFrame(samples []int16, speech bool) { o.frames[len(o.frames)-1]++ }
func (o *obsLog) ChunkEnd(next bool) {
	o.ends = append(o.ends, next)
	if next {
		o.frames = append(o.frames, 0) // the next chunk continues with no new start
	}
}

func TestObserverSeesEachChunk(t *testing.T) {
	c := New()
	o := &obsLog{}
	c.Obs = o
	var idx int64
	frames := append(silence(100, false), speech(600)...) // 12 s of speech
	frames = append(frames, silence(200, false)...)
	chunks := feed(c, &idx, frames)
	if len(chunks) != 1 || len(o.starts) != 1 || len(o.ends) != 1 || o.ends[0] {
		t.Fatalf("chunks %d, starts %v, ends %v", len(chunks), o.starts, o.ends)
	}
	ch := chunks[0]
	if float64(o.starts[0])*FrameSeconds != ch.Start {
		t.Fatalf("observer start %d, chunk start %v", o.starts[0], ch.Start)
	}
	if o.first[0] != prerollFrames+1 {
		t.Fatalf("the start gave %d frames, want the preroll and one", o.first[0])
	}
	if got := o.frames[0] * FrameSamples; got < len(ch.Samples) {
		t.Fatalf("the observer saw %d samples, fewer than the %d of the chunk", got, len(ch.Samples))
	}
}

func TestObserverHardCutContinues(t *testing.T) {
	c := New()
	o := &obsLog{}
	c.Obs = o
	var idx int64
	chunks := feed(c, &idx, speech(2000)) // 40 s without a long pause
	chunks = append(chunks, c.Flush())
	if len(o.starts) != 1 {
		t.Fatalf("starts %v: the chunk after a hard cut must continue without a new start", o.starts)
	}
	if len(o.ends) != 2 || !o.ends[0] || o.ends[1] {
		t.Fatalf("ends %v, want [true false]", o.ends)
	}
	total := 0
	for _, n := range o.frames {
		total += n
	}
	if total != 2000 {
		t.Fatalf("the observer saw %d frames, want each of the 2000 once", total)
	}
	if len(chunks) != 2 || chunks[0] == nil || chunks[1] == nil {
		t.Fatalf("chunks %v", chunks)
	}
}

func TestObserverSeesAShortBurst(t *testing.T) {
	c := New()
	o := &obsLog{}
	c.Obs = o
	var idx int64
	frames := append(silence(100, false), speech(20)...) // 0.4 s
	frames = append(frames, silence(200, false)...)
	if chunks := feed(c, &idx, frames); len(chunks) != 0 {
		t.Fatalf("got %d chunks for 0.4 s of speech", len(chunks))
	}
	if len(o.starts) != 1 || len(o.ends) != 1 {
		t.Fatalf("starts %v ends %v: the observer must see the dropped burst", o.starts, o.ends)
	}
}
