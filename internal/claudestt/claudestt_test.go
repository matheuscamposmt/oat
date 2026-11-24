package claudestt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/matheuscamposmt/oat/internal/chunk"
	"github.com/matheuscamposmt/oat/internal/stt"
)

// fakeServer runs handle for each connection after the upgrade.
type fakeServer struct {
	mu      sync.Mutex
	queries []string
	auth    []string
	audio   int
}

func newServer(t *testing.T, status int, handle func(ctx context.Context, c *websocket.Conn, f *fakeServer)) (*httptest.Server, *fakeServer) {
	t.Helper()
	f := &fakeServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.queries = append(f.queries, r.URL.RawQuery)
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		f.mu.Unlock()
		if status != 0 {
			if status == http.StatusTooManyRequests {
				w.Header().Set("Retry-After", "7")
			}
			w.WriteHeader(status)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		handle(r.Context(), c, f)
	}))
	t.Cleanup(srv.Close)
	return srv, f
}

// readUntilClose reads the audio and returns after CloseStream.
func readUntilClose(ctx context.Context, c *websocket.Conn, f *fakeServer) bool {
	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			return false
		}
		if typ == websocket.MessageBinary {
			f.mu.Lock()
			f.audio += len(data)
			f.mu.Unlock()
			continue
		}
		if strings.Contains(string(data), "CloseStream") {
			return true
		}
	}
}

func send(ctx context.Context, c *websocket.Conn, typ, data string) {
	b, _ := json.Marshal(map[string]string{"type": typ, "data": data})
	_ = c.Write(ctx, websocket.MessageText, b)
}

func writeCreds(t *testing.T, token string, expires time.Time) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".credentials.json")
	body := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q,"refreshToken":"r","expiresAt":%d}}`, token, expires.UnixMilli())
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func testClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	c := New(writeCreds(t, "tok-123", time.Now().Add(time.Hour)), "oat/test")
	c.URL = "ws" + strings.TrimPrefix(srv.URL, "http")
	c.Quiet = 50 * time.Millisecond
	c.Idle = 500 * time.Millisecond
	c.Finish = 2 * time.Second
	return c
}

// wav returns a chunk of secs seconds.
func wav(secs float64) []byte { return chunk.EncodeWAV(make([]int16, int(secs*sampleRate))) }

func transcribe(t *testing.T, c *Client, secs float64, lang string) (stt.Result, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return c.Transcribe(ctx, wav(secs), stt.Options{Language: lang, Model: "ignored", Prompt: "ignored"})
}

func TestTranscribeKeepsTheLongerTextBeforeTheEndpoint(t *testing.T) {
	srv, f := newServer(t, 0, func(ctx context.Context, c *websocket.Conn, f *fakeServer) {
		if !readUntilClose(ctx, c, f) {
			return
		}
		send(ctx, c, "TranscriptInterim", "Bom dia")
		send(ctx, c, "TranscriptText", "Bom dia a todos. Vamos ver o prazo")
		send(ctx, c, "TranscriptText", "Bom dia a todos.") // the quirk: a shorter prefix
		send(ctx, c, "TranscriptEndpoint", "")
		time.Sleep(time.Second) // the real server waits about 5 s before it closes
	})
	c := testClient(t, srv)
	res, err := transcribe(t, c, 10, "pt")
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "Bom dia a todos. Vamos ver o prazo" {
		t.Fatalf("text %q", res.Text)
	}
	if len(res.Segments) != 2 || res.Segments[0].Text != "Bom dia a todos." || res.Segments[1].Text != "Vamos ver o prazo" {
		t.Fatalf("segments %+v", res.Segments)
	}
	if res.Segments[0].Start != 0 || res.Segments[1].End != 10 || res.Segments[0].End != res.Segments[1].Start {
		t.Fatalf("times %+v", res.Segments)
	}
	if f.audio != 10*sampleRate*2 {
		t.Fatalf("server got %d bytes of audio, want %d (no WAV header)", f.audio, 10*sampleRate*2)
	}
	if f.auth[0] != "Bearer tok-123" {
		t.Fatal("wrong Authorization header")
	}
	if !strings.Contains(f.queries[0], "language=pt") || !strings.Contains(f.queries[0], "encoding=linear16") {
		t.Fatalf("query %q", f.queries[0])
	}
}

func TestTranscribeOmitsAutoLanguage(t *testing.T) {
	srv, f := newServer(t, 0, func(ctx context.Context, c *websocket.Conn, f *fakeServer) {
		readUntilClose(ctx, c, f)
		_ = c.Close(websocket.StatusNormalClosure, "")
	})
	if _, err := transcribe(t, testClient(t, srv), 1, "auto"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.queries[0], "language") {
		t.Fatalf("query %q has a language", f.queries[0])
	}
}

func TestTranscribeSeveralUtterances(t *testing.T) {
	srv, _ := newServer(t, 0, func(ctx context.Context, c *websocket.Conn, f *fakeServer) {
		send(ctx, c, "TranscriptText", "Primeiro")
		send(ctx, c, "TranscriptEndpoint", "")
		if !readUntilClose(ctx, c, f) {
			return
		}
		send(ctx, c, "TranscriptText", "Segundo")
		send(ctx, c, "TranscriptEndpoint", "")
		time.Sleep(time.Second)
	})
	res, err := transcribe(t, testClient(t, srv), 4, "pt")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Segments) != 2 || res.Segments[0].Text != "Primeiro" || res.Segments[1].Text != "Segundo" {
		t.Fatalf("segments %+v", res.Segments)
	}
}

func TestTranscribeTextWithoutEndpointCountsOnClose(t *testing.T) {
	srv, _ := newServer(t, 0, func(ctx context.Context, c *websocket.Conn, f *fakeServer) {
		readUntilClose(ctx, c, f)
		send(ctx, c, "TranscriptText", "sem endpoint")
		_ = c.Close(websocket.StatusNormalClosure, "")
	})
	res, err := transcribe(t, testClient(t, srv), 2, "pt")
	if err != nil || res.Text != "sem endpoint" {
		t.Fatalf("got %q %v", res.Text, err)
	}
}

func TestTranscribeSilenceEndsAfterIdle(t *testing.T) {
	srv, _ := newServer(t, 0, func(ctx context.Context, c *websocket.Conn, f *fakeServer) {
		readUntilClose(ctx, c, f)
		<-ctx.Done() // never answers
	})
	start := time.Now()
	res, err := transcribe(t, testClient(t, srv), 3, "pt")
	if err != nil || len(res.Segments) != 0 {
		t.Fatalf("got %+v %v", res, err)
	}
	if d := time.Since(start); d > 1500*time.Millisecond {
		t.Fatalf("took %v, want about the idle wait", d)
	}
}

func TestTranscribeTimeoutIsTemporary(t *testing.T) {
	srv, _ := newServer(t, 0, func(ctx context.Context, c *websocket.Conn, f *fakeServer) {
		readUntilClose(ctx, c, f)
		send(ctx, c, "TranscriptText", "parcial")
		<-ctx.Done() // a dead server: no endpoint, no close
	})
	c := testClient(t, srv)
	c.Idle, c.Finish, c.Timeout = time.Minute, time.Minute, 300*time.Millisecond
	_, err := transcribe(t, c, 1, "pt")
	var tmp *stt.TemporaryError
	if !errors.As(err, &tmp) {
		t.Fatalf("got %T %v, want a temporary error", err, err)
	}
}

func TestTranscribeKeepsTheLastTextBeforeTheClose(t *testing.T) {
	srv, _ := newServer(t, 0, func(ctx context.Context, c *websocket.Conn, f *fakeServer) {
		if !readUntilClose(ctx, c, f) {
			return
		}
		send(ctx, c, "TranscriptText", "e depois fechamos o contrato.")
		send(ctx, c, "TranscriptEndpoint", "")
		_ = c.Close(websocket.StatusNormalClosure, "")
	})
	c := testClient(t, srv)
	for i := range 30 {
		res, err := transcribe(t, c, 0.5, "pt")
		if err != nil || res.Text != "e depois fechamos o contrato." {
			t.Fatalf("try %d: got %q %v", i, res.Text, err)
		}
	}
}

func TestNormalCloseBeforeTheEndOfTheAudioIsTemporary(t *testing.T) {
	srv, _ := newServer(t, 0, func(ctx context.Context, c *websocket.Conn, f *fakeServer) {
		_, _, _ = c.Read(ctx) // KeepAlive
		_, _, _ = c.Read(ctx) // the first frame
		_ = c.Close(websocket.StatusNormalClosure, "session limit")
	})
	c := testClient(t, srv)
	// Hold the last frame until the close arrives, so the close comes while
	// the audio is still in flight. Without this, the socket buffers can take
	// all the audio first, and then the client cannot see the early close.
	c.beforeLastFrame = func() { time.Sleep(200 * time.Millisecond) }
	_, err := transcribe(t, c, 30, "pt")
	var tmp *stt.TemporaryError
	if !errors.As(err, &tmp) {
		t.Fatalf("got %T %v, want a temporary error: the chunk must stay on disk", err, err)
	}
}

func TestTimeoutAfterAnEndpointIsTemporary(t *testing.T) {
	srv, _ := newServer(t, 0, func(ctx context.Context, c *websocket.Conn, f *fakeServer) {
		if !readUntilClose(ctx, c, f) {
			return
		}
		send(ctx, c, "TranscriptText", "primeira parte")
		send(ctx, c, "TranscriptEndpoint", "")
		<-ctx.Done()
	})
	c := testClient(t, srv)
	c.Quiet, c.Idle, c.Finish, c.Timeout = time.Minute, time.Minute, time.Minute, 300*time.Millisecond
	_, err := transcribe(t, c, 1, "pt")
	var tmp *stt.TemporaryError
	if !errors.As(err, &tmp) {
		t.Fatalf("got %T %v, want a temporary error", err, err)
	}
}

func TestCollector(t *testing.T) {
	var c collector
	for _, s := range []string{"Bom", "Bom dia", "Bom dia a todos", "Então, bom dia a todos.", "Então,"} {
		c.text(s)
	}
	c.endpoint()
	c.text("Vamos começar")
	got := c.done()
	// A text with other first words is a revision. Only an endpoint starts an utterance.
	if len(got) != 2 || got[0] != "Então, bom dia a todos." || got[1] != "Vamos começar" {
		t.Fatalf("got %q", got)
	}
}

func TestTranscribeEmptyWAVSendsNothing(t *testing.T) {
	srv, f := newServer(t, 0, func(ctx context.Context, c *websocket.Conn, f *fakeServer) {})
	res, err := testClient(t, srv).Transcribe(context.Background(), chunk.EncodeWAV(nil), stt.Options{})
	if err != nil || len(res.Segments) != 0 || len(f.queries) != 0 {
		t.Fatalf("got %+v %v, %d connections", res, err, len(f.queries))
	}
}

func TestTranscribeErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		handle func(ctx context.Context, c *websocket.Conn, f *fakeServer)
		check  func(error) bool
	}{
		{"401 is an auth error", http.StatusUnauthorized, nil, func(err error) bool {
			var e *stt.AuthError
			return errors.As(err, &e)
		}},
		{"429 is a rate limit", http.StatusTooManyRequests, nil, func(err error) bool {
			var e *stt.RateLimitError
			return errors.As(err, &e) && e.RetryAfter == 7*time.Second
		}},
		{"502 is temporary", http.StatusBadGateway, nil, func(err error) bool {
			var e *stt.TemporaryError
			return errors.As(err, &e)
		}},
		{"close 1003 refuses the chunk", 0, func(ctx context.Context, c *websocket.Conn, f *fakeServer) {
			_ = c.Close(websocket.StatusUnsupportedData, "harmonic provider does not support the requested language")
		}, func(err error) bool {
			var e *stt.RequestError
			return errors.As(err, &e) && strings.Contains(e.Msg, "does not support")
		}},
		{"TranscriptError refuses the chunk", 0, func(ctx context.Context, c *websocket.Conn, f *fakeServer) {
			_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"TranscriptError","description":"bad audio"}`))
			time.Sleep(time.Second)
		}, func(err error) bool {
			var e *stt.RequestError
			return errors.As(err, &e) && e.Msg == "bad audio"
		}},
		{"an error frame is temporary", 0, func(ctx context.Context, c *websocket.Conn, f *fakeServer) {
			_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"error","message":"overloaded"}`))
			time.Sleep(time.Second)
		}, func(err error) bool {
			var e *stt.TemporaryError
			return errors.As(err, &e) && strings.Contains(err.Error(), "overloaded")
		}},
		{"an abnormal close is temporary", 0, func(ctx context.Context, c *websocket.Conn, f *fakeServer) {
			_ = c.Close(websocket.StatusInternalError, "boom")
		}, func(err error) bool {
			var e *stt.TemporaryError
			return errors.As(err, &e)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newServer(t, tc.status, tc.handle)
			_, err := transcribe(t, testClient(t, srv), 1, "pt")
			if !tc.check(err) {
				t.Fatalf("got %T %v", err, err)
			}
		})
	}
}

func TestTokenProblems(t *testing.T) {
	srv, f := newServer(t, 0, func(ctx context.Context, c *websocket.Conn, f *fakeServer) {})
	c := testClient(t, srv)

	c.CredentialsPath = filepath.Join(t.TempDir(), "none.json")
	var auth *stt.AuthError
	if _, err := transcribe(t, c, 1, "pt"); !errors.As(err, &auth) || !strings.Contains(err.Error(), "no Claude Code login") {
		t.Fatalf("missing file: %v", err)
	}

	c.CredentialsPath = writeCreds(t, "", time.Now().Add(time.Hour))
	if _, err := transcribe(t, c, 1, "pt"); !errors.As(err, &auth) {
		t.Fatalf("empty token: %v", err)
	}

	// An expired token is temporary: Claude Code can renew it while oat waits.
	c.CredentialsPath = writeCreds(t, "old", time.Now().Add(10*time.Second))
	var tmp *stt.TemporaryError
	if _, err := transcribe(t, c, 1, "pt"); !errors.As(err, &tmp) || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired token: %v", err)
	}
	if len(f.queries) != 0 {
		t.Fatalf("the client connected %d times without a valid token", len(f.queries))
	}
}

func TestErrorsDoNotShowTheToken(t *testing.T) {
	srv, _ := newServer(t, http.StatusUnauthorized, nil)
	_, err := transcribe(t, testClient(t, srv), 1, "pt")
	if err == nil || strings.Contains(err.Error(), "tok-123") {
		t.Fatalf("got %v", err)
	}
}

func TestPing(t *testing.T) {
	srv, f := newServer(t, 0, func(ctx context.Context, c *websocket.Conn, f *fakeServer) {
		_, _, _ = c.Read(ctx)
		_, _, _ = c.Read(ctx)
	})
	if err := testClient(t, srv).Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.audio != 0 {
		t.Fatal("Ping sent audio")
	}
}

func TestSentencesAndPlace(t *testing.T) {
	got := Sentences("Olá. Tudo bem? Sim! E aí… beleza")
	want := []string{"Olá.", "Tudo bem?", "Sim!", "E aí…", "beleza"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", got)
	}
	if Sentences("v1.2 é a versão")[0] != "v1.2 é a versão" {
		t.Fatal("a dot without a space split the text")
	}
	segs := Place([]string{"ab. cd.", "efghij"}, 12) // 3, 3, and 6 runes
	if len(segs) != 3 || segs[0].End != 3 || segs[1].End != 6 || segs[2].Start != 6 || segs[2].End != 12 {
		t.Fatalf("got %+v", segs)
	}
	if len(Place(nil, 5)) != 0 {
		t.Fatal("no text must give no segments")
	}
}

func TestCredentialsPath(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/x/claude")
	if p, _ := CredentialsPath(); p != "/x/claude/.credentials.json" {
		t.Fatalf("got %q", p)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("HOME", "/home/u")
	if p, _ := CredentialsPath(); p != "/home/u/.claude/.credentials.json" {
		t.Fatalf("got %q", p)
	}
}

func TestStreamSendsPartialText(t *testing.T) {
	srv, _ := newServer(t, 0, func(ctx context.Context, c *websocket.Conn, f *fakeServer) {
		_, _, _ = c.Read(ctx) // KeepAlive
		_, _, _ = c.Read(ctx) // audio
		send(ctx, c, "TranscriptText", "Bom")
		send(ctx, c, "TranscriptText", "Bom dia")
		send(ctx, c, "TranscriptEndpoint", "")
		send(ctx, c, "TranscriptText", "Vamos")
		readUntilClose(ctx, c, f)
		send(ctx, c, "TranscriptText", "Vamos lá.")
		send(ctx, c, "TranscriptEndpoint", "")
		_ = c.Close(websocket.StatusNormalClosure, "")
	})
	var mu sync.Mutex
	var seen []string
	st, err := testClient(t, srv).Open(context.Background(), "pt", func(s string) {
		mu.Lock()
		seen = append(seen, s)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Send(make([]byte, 3200)); err != nil {
		t.Fatal(err)
	}
	utts, err := st.Finish()
	if err != nil || strings.Join(utts, "|") != "Bom dia|Vamos lá." {
		t.Fatalf("got %q %v", utts, err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"Bom", "Bom dia", "Bom dia Vamos", "Bom dia Vamos lá."}
	if strings.Join(seen, "|") != strings.Join(want, "|") {
		t.Fatalf("partial texts %q, want %q", seen, want)
	}
	if st.Alive() {
		t.Fatal("the stream is alive after Finish")
	}
}

func TestStreamsWaitForAFreeSlot(t *testing.T) {
	release := make(chan struct{})
	srv, f := newServer(t, 0, func(ctx context.Context, c *websocket.Conn, f *fakeServer) {
		readUntilClose(ctx, c, f)
		<-release
		_ = c.Close(websocket.StatusNormalClosure, "")
	})
	c := testClient(t, srv)
	c.Streams = 1
	first, err := c.Open(context.Background(), "pt", nil)
	if err != nil {
		t.Fatal(err)
	}
	opened := make(chan *Stream, 1)
	go func() {
		s, err := c.Open(context.Background(), "pt", nil)
		if err != nil {
			t.Error(err)
		}
		opened <- s
	}()
	select {
	case <-opened:
		t.Fatal("a second stream opened while the only slot was taken")
	case <-time.After(200 * time.Millisecond):
	}
	go func() { _, _ = first.Finish() }()
	close(release)
	select {
	case s := <-opened:
		s.Close()
	case <-time.After(3 * time.Second):
		t.Fatal("the second stream did not open after the first ended")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.queries) != 2 {
		t.Fatalf("%d connections, want 2", len(f.queries))
	}
}

func TestRateLimitCloseIsARateLimit(t *testing.T) {
	srv, _ := newServer(t, 0, func(ctx context.Context, c *websocket.Conn, f *fakeServer) {
		_ = c.Close(4029, "Rate limit exceeded. Wait a few seconds before retrying.")
	})
	_, err := transcribe(t, testClient(t, srv), 1, "pt")
	var rl *stt.RateLimitError
	if !errors.As(err, &rl) || rl.RetryAfter != rateLimitWait {
		t.Fatalf("got %T %v", err, err)
	}
}

func TestCloseDropsTheStream(t *testing.T) {
	srv, _ := newServer(t, 0, func(ctx context.Context, c *websocket.Conn, f *fakeServer) {
		<-ctx.Done()
	})
	st, err := testClient(t, srv).Open(context.Background(), "pt", nil)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	if st.Alive() {
		t.Fatal("alive after Close")
	}
	if err := st.Send([]byte{0, 0}); err == nil {
		t.Fatal("Send after Close worked")
	}
}
