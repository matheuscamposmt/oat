// Package filetx transcribes an audio stream that is not a meeting, such as
// a file decoded by ffmpeg. It cuts the stream at pauses and sends the chunks
// to a backend, a few at the same time.
package filetx

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/matheuscamposmt/oat/internal/chunk"
	"github.com/matheuscamposmt/oat/internal/stt"
	"github.com/matheuscamposmt/oat/internal/transcribe"
)

const maxTries = 6

// Options configure Transcribe.
type Options struct {
	STT     stt.Options
	Workers int                                              // streams at the same time; 1 when zero
	Sleep   func(ctx context.Context, d time.Duration) error // nil: real sleep
	Log     func(format string, args ...any)                 // retries; nil: none
}

// Paragraph is the text of one chunk.
type Paragraph struct {
	Start float64 // seconds from the start of the file
	Text  string
}

type job struct {
	i int
	c *chunk.Chunk
}

// Transcribe reads 16 kHz mono s16le PCM from r until EOF and returns the text of each chunk, in time order.
func Transcribe(ctx context.Context, r io.Reader, c transcribe.Client, o Options) ([]Paragraph, error) {
	if o.Sleep == nil {
		o.Sleep = sleep
	}
	if o.Log == nil {
		o.Log = func(string, ...any) {}
	}
	// The first fatal error ends ctx and is its cause.
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	jobs := make(chan job)
	var mu sync.Mutex
	texts := map[int]Paragraph{}
	var wg sync.WaitGroup
	for range max(o.Workers, 1) {
		wg.Go(func() {
			for j := range jobs {
				text, err := transcribeChunk(ctx, c, j.c, o)
				if err != nil {
					cancel(err)
					continue
				}
				mu.Lock()
				texts[j.i] = Paragraph{Start: j.c.Start, Text: text}
				mu.Unlock()
			}
		})
	}

	n := 0
	send := func(ch *chunk.Chunk) bool {
		if ch == nil {
			return true
		}
		select {
		case jobs <- job{n, ch}:
			n++
			return true
		case <-ctx.Done():
			return false
		}
	}
	if err := read(r, chunk.New(), send); err != nil {
		cancel(err)
	}
	close(jobs)
	wg.Wait()
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}

	var out []Paragraph
	for i := range n {
		p := texts[i]
		if p.Text = strings.TrimSpace(p.Text); p.Text != "" && !transcribe.Hallucination(p.Text) {
			out = append(out, p)
		}
	}
	return out, nil
}

// read gives r to the chunker in 20 ms frames and sends each chunk. It stops
// when send returns false.
func read(r io.Reader, ck *chunk.Chunker, send func(*chunk.Chunk) bool) error {
	buf := make([]byte, 2*chunk.FrameSamples)
	for idx := int64(0); ; idx++ {
		k, err := io.ReadFull(r, buf)
		if err == io.EOF {
			break
		}
		if err != nil && err != io.ErrUnexpectedEOF {
			return err
		}
		clear(buf[k:])
		samples := make([]int16, chunk.FrameSamples)
		for i := range samples {
			samples[i] = int16(binary.LittleEndian.Uint16(buf[2*i:]))
		}
		if !send(ck.Push(idx, samples)) {
			return nil
		}
		if err == io.ErrUnexpectedEOF {
			break
		}
	}
	send(ck.Flush())
	return nil
}

// transcribeChunk tries one chunk until it has text, at most maxTries times.
func transcribeChunk(ctx context.Context, c transcribe.Client, ch *chunk.Chunk, o Options) (string, error) {
	wav := chunk.EncodeWAV(ch.Samples)
	fails := 0
	for try := 1; ; try++ {
		res, err := c.Transcribe(ctx, wav, o.STT)
		if err == nil {
			return res.Text, nil
		}
		var rl *stt.RateLimitError
		var auth *stt.AuthError
		var req *stt.RequestError
		switch {
		case errors.As(err, &auth), errors.As(err, &req), ctx.Err() != nil:
			return "", err
		case try == maxTries:
			return "", fmt.Errorf("chunk at %s failed after %d tries: %w", clock(ch.Start), maxTries, err)
		}
		d := time.Duration(2<<fails) * time.Second // 2, 4, 8, 16, 32 s
		if errors.As(err, &rl) {
			d = rl.RetryAfter
		} else {
			fails++
		}
		o.Log("chunk at %s: %v. Next try in %s.", clock(ch.Start), err, d)
		if err := o.Sleep(ctx, d); err != nil {
			return "", err
		}
	}
}

func clock(secs float64) string {
	s := int(secs)
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
