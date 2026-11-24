package filetx

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matheuscamposmt/oat/internal/chunk"
	"github.com/matheuscamposmt/oat/internal/stt"
)

// fakeClient answers by the length of the chunk in whole seconds.
type fakeClient struct {
	mu      sync.Mutex
	texts   map[int]string
	delay   map[int]time.Duration
	errs    []error // returned in order before any text
	forever error   // returned after errs when set
	block   bool    // waits for the end of ctx
	called  chan struct{}
	calls   int
	opts    []stt.Options
}

func (f *fakeClient) Transcribe(ctx context.Context, wav []byte, opts stt.Options) (stt.Result, error) {
	secs := int(chunk.WAVDuration(wav))
	f.mu.Lock()
	f.calls++
	f.opts = append(f.opts, opts)
	err := f.forever
	if len(f.errs) > 0 {
		err, f.errs = f.errs[0], f.errs[1:]
	}
	f.mu.Unlock()
	if f.called != nil {
		select {
		case f.called <- struct{}{}:
		default:
		}
	}
	if f.block {
		<-ctx.Done()
		return stt.Result{}, ctx.Err()
	}
	if err != nil {
		return stt.Result{}, err
	}
	select {
	case <-time.After(f.delay[secs]):
	case <-ctx.Done():
		return stt.Result{}, ctx.Err()
	}
	return stt.Result{Text: f.texts[secs]}, nil
}

// tone is a 440 Hz sine at -20 dBFS. One silent frame every 2 s keeps the
// noise floor of the chunker low, as the pauses of real speech do.
func tone(secs float64) []byte {
	n := int(secs * chunk.SampleRate)
	b := make([]byte, 2*n)
	for i := range n {
		v := 4634 * math.Sin(2*math.Pi*440*float64(i)/chunk.SampleRate)
		if (i/chunk.FrameSamples)%100 == 99 {
			v = 0
		}
		binary.LittleEndian.PutUint16(b[2*i:], uint16(int16(v)))
	}
	return b
}

func silence(secs float64) []byte { return make([]byte, 2*int(secs*chunk.SampleRate)) }

func pcm(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

// threeBlocks gives chunks of 11, 13 and 15 whole seconds.
func threeBlocks() []byte {
	return pcm(tone(11), silence(2), tone(13), silence(2), tone(15))
}

func run(t *testing.T, r io.Reader, f *fakeClient, o Options) ([]Paragraph, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if o.Sleep == nil {
		o.Sleep = func(context.Context, time.Duration) error { return nil }
	}
	return Transcribe(ctx, r, f, o)
}

func TestTranscribeKeepsTimeOrder(t *testing.T) {
	f := &fakeClient{
		texts: map[int]string{11: "first", 13: "second", 15: "third"},
		delay: map[int]time.Duration{11: 300 * time.Millisecond},
	}
	got, err := run(t, bytes.NewReader(threeBlocks()), f, Options{Workers: 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Text != "first" || got[1].Text != "second" || got[2].Text != "third" {
		t.Fatalf("got %+v", got)
	}
	if !(got[0].Start < got[1].Start && got[1].Start < got[2].Start) {
		t.Fatalf("starts %+v", got)
	}
}

func TestTranscribeSilenceGivesNoParagraphs(t *testing.T) {
	f := &fakeClient{}
	got, err := run(t, bytes.NewReader(silence(20)), f, Options{})
	if err != nil || len(got) != 0 || f.calls != 0 {
		t.Fatalf("got %+v %v, %d calls", got, err, f.calls)
	}
}

func TestTranscribeDropsEmptyAndHallucinatedText(t *testing.T) {
	f := &fakeClient{texts: map[int]string{11: "", 13: "Obrigado por assistir", 15: "kept"}}
	got, err := run(t, bytes.NewReader(threeBlocks()), f, Options{Workers: 2})
	if err != nil || len(got) != 1 || got[0].Text != "kept" {
		t.Fatalf("got %+v %v", got, err)
	}
}

func TestTranscribeRetriesRateLimitAndTemporary(t *testing.T) {
	f := &fakeClient{
		texts: map[int]string{11: "ok"},
		errs:  []error{&stt.RateLimitError{RetryAfter: 5 * time.Second}, &stt.TemporaryError{Err: errors.New("down")}},
	}
	var slept []time.Duration
	sleep := func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	got, err := run(t, bytes.NewReader(tone(11)), f, Options{Sleep: sleep})
	if err != nil || len(got) != 1 || got[0].Text != "ok" {
		t.Fatalf("got %+v %v", got, err)
	}
	if len(slept) != 2 || slept[0] != 5*time.Second || slept[1] != 2*time.Second {
		t.Fatalf("slept %v", slept)
	}
}

func TestTranscribeGivesUpAfterSixTries(t *testing.T) {
	f := &fakeClient{forever: &stt.TemporaryError{Err: errors.New("down")}}
	_, err := run(t, bytes.NewReader(tone(11)), f, Options{})
	if err == nil || !strings.Contains(err.Error(), "after 6 tries") || !strings.Contains(err.Error(), "0:00") {
		t.Fatalf("err %v", err)
	}
	if f.calls != 6 {
		t.Fatalf("%d calls", f.calls)
	}
}

func TestTranscribeStopsOnAuthError(t *testing.T) {
	want := &stt.AuthError{Msg: "no"}
	f := &fakeClient{forever: want}
	if _, err := run(t, bytes.NewReader(tone(11)), f, Options{}); err != want || f.calls != 1 {
		t.Fatalf("err %v, %d calls", err, f.calls)
	}
}

func TestTranscribeStopsOnRequestError(t *testing.T) {
	want := &stt.RequestError{Msg: "no"}
	f := &fakeClient{forever: want}
	if _, err := run(t, bytes.NewReader(tone(11)), f, Options{}); err != want || f.calls != 1 {
		t.Fatalf("err %v, %d calls", err, f.calls)
	}
}

func TestTranscribePassesOptions(t *testing.T) {
	f := &fakeClient{texts: map[int]string{11: "ok"}}
	if _, err := run(t, bytes.NewReader(tone(11)), f, Options{STT: stt.Options{Language: "pt", Keyterms: []string{"Entra ID"}}}); err != nil {
		t.Fatal(err)
	}
	if o := f.opts[0]; o.Language != "pt" || len(o.Keyterms) != 1 || o.Keyterms[0] != "Entra ID" {
		t.Fatalf("opts %+v", o)
	}
}

func TestTranscribeStreamsWhileReading(t *testing.T) {
	f := &fakeClient{texts: map[int]string{11: "first", 13: "second"}, called: make(chan struct{}, 1)}
	pr, pw := io.Pipe()
	go func() {
		pw.Write(pcm(tone(11), silence(2)))
		select {
		case <-f.called:
			pw.Write(tone(13))
			pw.Close()
		case <-time.After(5 * time.Second):
			pw.CloseWithError(errors.New("the first chunk did not reach the client before the end of the input"))
		}
	}()
	got, err := run(t, pr, f, Options{})
	if err != nil || len(got) != 2 {
		t.Fatalf("got %+v %v", got, err)
	}
}

func TestTranscribeCancel(t *testing.T) {
	f := &fakeClient{block: true, called: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-f.called
		cancel()
	}()
	_, err := Transcribe(ctx, bytes.NewReader(threeBlocks()), f, Options{Workers: 2})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
}
