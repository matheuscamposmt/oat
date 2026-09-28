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
