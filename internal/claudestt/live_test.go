//go:build live

package claudestt

// Live tests against the real endpoint. They need a Claude Code login.
//
//	OAT_LIVE_PCM=/path/16k-mono-s16le.raw go test -tags live -run Live -v ./internal/claudestt/

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matheuscamposmt/oat/internal/chunk"
	"github.com/matheuscamposmt/oat/internal/stt"
)

func liveClient(t *testing.T) *Client {
	t.Helper()
	p, err := CredentialsPath()
	if err != nil {
		t.Fatal(err)
	}
	return New(p, "oat/live-test")
}

func pcmSamples(t *testing.T, from, secs float64) []int16 {
	t.Helper()
	path := os.Getenv("OAT_LIVE_PCM")
	if path == "" {
		t.Skip("set OAT_LIVE_PCM")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	a, b := int(from*sampleRate)*2, int((from+secs)*sampleRate)*2
	raw = raw[a:min(b, len(raw))]
	s := make([]int16, len(raw)/2)
	for i := range s {
		s[i] = int16(uint16(raw[2*i]) | uint16(raw[2*i+1])<<8)
	}
	return s
}

func run(t *testing.T, name string, samples []int16, lang string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	t0 := time.Now()
	res, err := liveClient(t).Transcribe(ctx, chunk.EncodeWAV(samples), stt.Options{Language: lang})
	t.Logf("%s: %.1fs audio, %v, err=%v", name, float64(len(samples))/sampleRate, time.Since(t0).Round(time.Millisecond), err)
	for _, s := range res.Segments {
		t.Logf("  [%5.1f-%5.1f] %s", s.Start, s.End, s.Text)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestLiveSpeech(t *testing.T)   { run(t, "speech 30s", pcmSamples(t, 400, 30), "pt") }
func TestLiveShort(t *testing.T)    { run(t, "speech 1.5s", pcmSamples(t, 401, 1.5), "pt") }
func TestLiveSilence(t *testing.T)  { run(t, "silence 10s", make([]int16, 10*sampleRate), "pt") }
func TestLiveAutoLang(t *testing.T) { run(t, "auto 15s", pcmSamples(t, 200, 15), "auto") }

func TestLivePing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := liveClient(t).Ping(ctx); err != nil {
		t.Fatal(err)
	}
}

// TestLiveBatch sends 40 chunks of 18 s with 2 workers, like a meeting of 12 minutes.
func TestLiveBatch(t *testing.T) {
	c := liveClient(t)
	type job struct{ i int }
	jobs := make(chan int)
	errs := make(chan string, 40)
	done := make(chan struct{})
	t0 := time.Now()
	for w := 0; w < 2; w++ {
		go func() {
			for i := range jobs {
				ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				s := time.Now()
				res, err := c.Transcribe(ctx, chunk.EncodeWAV(pcmSamples(t, float64(i)*18, 18)), stt.Options{Language: "pt"})
				cancel()
				errs <- fmt.Sprintf("chunk %2d: %v, %d segments, %d chars, text=%q, err=%v", i, time.Since(s).Round(time.Millisecond), len(res.Segments), len(res.Text), res.Text, err)
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 40; i++ {
		jobs <- i
	}
	close(jobs)
	<-done
	<-done
	close(errs)
	failed := 0
	for e := range errs {
		t.Log(e)
		if !strings.HasSuffix(e, "err=<nil>") {
			failed++
		}
	}
	t.Logf("total %v, %d failed", time.Since(t0).Round(time.Second), failed)
	if failed > 0 {
		t.Fail()
	}
}

// TestLiveEarlyExit compares the normal end of a stream with a reference that
// waits for the server to close, on the same chunks.
func TestLiveEarlyExit(t *testing.T) {
	fast := liveClient(t)
	ref := liveClient(t)
	ref.Quiet, ref.Idle, ref.Finish = 20*time.Second, 20*time.Second, 40*time.Second
	var mu sync.Mutex
	reasons := map[string]int{}
	fast.Trace = func(r string, n int) { mu.Lock(); reasons[fmt.Sprintf("%s/%d endpoints", r, n)]++; mu.Unlock() }
	refReasons := map[string]int{}
	ref.Trace = func(r string, n int) { mu.Lock(); refReasons[fmt.Sprintf("%s/%d endpoints", r, n)]++; mu.Unlock() }
	n := 20
	if v := os.Getenv("OAT_LIVE_N"); v != "" {
		fmt.Sscan(v, &n)
	}
	diff := 0
	var wg sync.WaitGroup
	sem := make(chan struct{}, 2)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			w := chunk.EncodeWAV(pcmSamples(t, float64(i)*18, 18))
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			a, err1 := fast.Transcribe(ctx, w, stt.Options{Language: "pt"})
			b, err2 := ref.Transcribe(ctx, w, stt.Options{Language: "pt"})
			if err1 != nil || err2 != nil {
				t.Errorf("chunk %d: %v %v", i, err1, err2)
				return
			}
			if a.Text != b.Text {
				mu.Lock()
				diff++
				mu.Unlock()
				t.Logf("chunk %d differs:\n  fast: %s\n  ref:  %s", i, a.Text, b.Text)
			}
		}(i)
	}
	wg.Wait()
	t.Logf("fast end reasons %v; reference end reasons %v; %d of %d texts differ", reasons, refReasons, diff, n)
}

// TestLiveRepeat sends the same chunk several times and logs the end of each text.
func TestLiveRepeat(t *testing.T) {
	var from float64 = 400
	if v := os.Getenv("OAT_LIVE_START"); v != "" {
		fmt.Sscan(v, &from)
	}
	c := liveClient(t)
	var reason string
	c.Trace = func(r string, n int) { reason = fmt.Sprintf("%s/%d", r, n) }
	w := chunk.EncodeWAV(pcmSamples(t, from, 20))
	for i := 0; i < 4; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		res, err := c.Transcribe(ctx, w, stt.Options{Language: "pt"})
		cancel()
		tail := res.Text
		if len(tail) > 90 {
			tail = tail[len(tail)-90:]
		}
		t.Logf("run %d (%s, err=%v): ...%s", i, reason, err, tail)
	}
}
