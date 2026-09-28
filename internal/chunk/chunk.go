// Package chunk splits a stream of 20 ms PCM frames into chunks for Whisper.
package chunk

import "math"

const (
	SampleRate   = 16000
	FrameSamples = 320 // 20 ms at 16 kHz
	FrameSeconds = 0.02
	MinDB        = -100.0
)

// Tuning values. They come from common VAD values and need tests on real meetings.
const (
	floorFrames      = 250  // 5 s window for the noise floor
	speechAboveFloor = 12.0 // dB
	speechMinDB      = -50.0
	hangoverFrames   = 10   // 200 ms
	prerollFrames    = 15   // 300 ms
	normalMinFrames  = 500  // 10 s
	normalSilence    = 35   // 700 ms
	earlySilence     = 150  // 3 s
	earlyTail        = 15   // 300 ms
	hardMaxFrames    = 1500 // 30 s
	hardSearchFrames = 100  // 2 s
	minSpeechFrames  = 50   // 1 s
)

// Chunk is a part of one stream that goes to Whisper in one request.
type Chunk struct {
	Start   float64 // seconds on the meeting clock
	Samples []int16
	Speech  float64 // seconds of speech in the chunk
}

// Duration returns the length of the chunk in seconds.
func (c *Chunk) Duration() float64 { return float64(len(c.Samples)) / SampleRate }

type frame struct {
	samples []int16
	db      float64
	speech  bool
}

// Chunker holds the VAD and cut state for one stream. It is not safe for concurrent use.
type Chunker struct {
	levels   []float64 // recent frame levels for the noise floor
	levelPos int
	hang     int
	preroll  []frame
	chunk    []frame
	start    int64 // meeting-clock frame index of chunk[0]
	last     int64 // index of the last pushed frame
	started  bool
}

// New returns an empty Chunker.
func New() *Chunker { return &Chunker{} }

// LevelDB returns the RMS level of a frame in dBFS, never below MinDB.
func LevelDB(samples []int16) float64 {
	if len(samples) == 0 {
		return MinDB
	}
	var sum float64
	for _, s := range samples {
		f := float64(s)
		sum += f * f
	}
	if sum == 0 {
		return MinDB
	}
	db := 20 * math.Log10(math.Sqrt(sum/float64(len(samples)))/32768)
	return math.Max(db, MinDB)
}

// Push adds the frame with meeting-clock index idx and returns a finished chunk or nil.
// A gap in idx ends the current chunk first.
func (c *Chunker) Push(idx int64, samples []int16) *Chunk {
	var out *Chunk
	if c.started && idx != c.last+1 {
		out = c.Flush()
	}
	c.started = true
	c.last = idx

	db := LevelDB(samples)
	floor := c.floor()
	c.addLevel(db)
	raw := db >= floor+speechAboveFloor && db > speechMinDB
	speech := raw
	if raw {
		c.hang = hangoverFrames
	} else if c.hang > 0 {
		c.hang--
		speech = true
	}
	f := frame{samples: samples, db: db, speech: speech}

	if len(c.chunk) == 0 {
		if !raw {
			c.pushPreroll(f)
			return out
		}
		c.start = idx - int64(len(c.preroll))
		c.chunk = append(append([]frame(nil), c.preroll...), f)
		c.preroll = c.preroll[:0]
		return out
	}
	c.chunk = append(c.chunk, f)
	return c.cut()
}

// Flush ends the current chunk, for a pause, a stop, or a gap in the stream.
func (c *Chunker) Flush() *Chunk {
	c.preroll = c.preroll[:0]
	c.hang = 0
	if len(c.chunk) == 0 {
		return nil
	}
	keep := min(len(c.chunk), lastSpeech(c.chunk)+1+earlyTail)
	out := build(c.start, c.chunk[:keep])
	c.chunk = nil
	return out
}

func (c *Chunker) floor() float64 {
	if len(c.levels) == 0 {
		return MinDB
	}
	m := c.levels[0]
	for _, v := range c.levels[1:] {
		m = math.Min(m, v)
	}
	return m
}

func (c *Chunker) addLevel(db float64) {
	if len(c.levels) < floorFrames {
		c.levels = append(c.levels, db)
		return
	}
	c.levels[c.levelPos] = db
	c.levelPos = (c.levelPos + 1) % floorFrames
}

func (c *Chunker) pushPreroll(f frame) {
	if len(c.preroll) == prerollFrames {
		copy(c.preroll, c.preroll[1:])
		c.preroll = c.preroll[:prerollFrames-1]
	}
	c.preroll = append(c.preroll, f)
}

// cut applies the cut rules to the current chunk and returns a chunk when one ends.
func (c *Chunker) cut() *Chunk {
	n := len(c.chunk)
	last := lastSpeech(c.chunk)
	silence := n - 1 - last
	switch {
	case n >= normalMinFrames && silence >= normalSilence:
		return c.emit(last + 1 + normalSilence/2)
	case silence >= earlySilence:
		return c.emit(last + 1 + earlyTail)
	case n >= hardMaxFrames:
		q := n - hardSearchFrames
		for i := q + 1; i < n; i++ {
			if c.chunk[i].db < c.chunk[q].db {
				q = i
			}
		}
		return c.emit(q + 1)
	}
	return nil
}

// emit ends the chunk after keep frames. The frames after keep start the next
// chunk when they hold speech, or go to the preroll buffer when they do not.
func (c *Chunker) emit(keep int) *Chunk {
	keep = min(keep, len(c.chunk))
	out := build(c.start, c.chunk[:keep])
	rest := append([]frame(nil), c.chunk[keep:]...)
	restStart := c.start + int64(keep)
	c.chunk = nil
	c.preroll = c.preroll[:0]
	if hasSpeech(rest) {
		c.chunk = rest
		c.start = restStart
		return out
	}
	for _, f := range rest {
		c.pushPreroll(f)
	}
	return out
}

func lastSpeech(fs []frame) int {
	for i := len(fs) - 1; i >= 0; i-- {
		if fs[i].speech {
			return i
		}
	}
	return 0
}

func hasSpeech(fs []frame) bool {
	for _, f := range fs {
		if f.speech {
			return true
		}
	}
	return false
}

// build returns the chunk for frames, or nil when it has too little speech.
func build(start int64, fs []frame) *Chunk {
	speech := 0
	for _, f := range fs {
		if f.speech {
			speech++
		}
	}
	if speech < minSpeechFrames {
		return nil
	}
	samples := make([]int16, 0, len(fs)*FrameSamples)
	for _, f := range fs {
		samples = append(samples, f.samples...)
	}
	return &Chunk{Start: float64(start) * FrameSeconds, Samples: samples, Speech: float64(speech) * FrameSeconds}
}
