package groq

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func server(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New("gsk_test")
	c.BaseURL = srv.URL
	return c
}

func TestTranscribeSendsFieldsAndParses(t *testing.T) {
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/audio/transcriptions" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer gsk_test" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		want := map[string]string{"model": "whisper-large-v3-turbo", "language": "pt", "response_format": "verbose_json", "temperature": "0", "prompt": "antes"}
		for k, v := range want {
			if got := r.FormValue(k); got != v {
				t.Errorf("field %s = %q, want %q", k, got, v)
			}
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			t.Fatal(err)
		}
		if data, _ := io.ReadAll(f); string(data) != "RIFFdata" {
			t.Errorf("file = %q", data)
		}
		w.Write([]byte(`{"text":" olá","language":"portuguese","segments":[{"start":0.5,"end":2.0,"text":" olá","no_speech_prob":0.01,"compression_ratio":1.1}]}`))
	})
	res, err := c.Transcribe(context.Background(), []byte("RIFFdata"), Options{Model: "whisper-large-v3-turbo", Language: "pt", Prompt: "antes"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Segments) != 1 || res.Segments[0].Start != 0.5 || res.Segments[0].Text != " olá" || res.Segments[0].NoSpeechProb != 0.01 {
		t.Fatalf("got %+v", res)
	}
}

func TestTranscribeOmitsAutoLanguage(t *testing.T) {
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		r.ParseMultipartForm(1 << 20)
		if _, ok := r.MultipartForm.Value["language"]; ok {
			t.Error("sent a language field for auto")
		}
		if _, ok := r.MultipartForm.Value["prompt"]; ok {
			t.Error("sent an empty prompt")
		}
		w.Write([]byte(`{"text":""}`))
	})
	if _, err := c.Transcribe(context.Background(), nil, Options{Model: "m", Language: "auto"}); err != nil {
		t.Fatal(err)
	}
}

func TestErrorTypes(t *testing.T) {
	cases := []struct {
		name   string
		status int
		header string
		check  func(error) bool
	}{
		{"429 with header", 429, "7", func(err error) bool {
			var e *RateLimitError
			return errors.As(err, &e) && e.RetryAfter == 7*time.Second
		}},
		{"429 without header", 429, "", func(err error) bool {
			var e *RateLimitError
			return errors.As(err, &e) && e.RetryAfter == 10*time.Second
		}},
		{"500", 500, "", func(err error) bool {
			var e *TemporaryError
			return errors.As(err, &e)
		}},
		{"401", 401, "", func(err error) bool {
			var e *AuthError
			return errors.As(err, &e) && e.Msg == "Invalid API Key"
		}},
		{"400", 400, "", func(err error) bool {
			var e *RequestError
			return errors.As(err, &e) && e.Status == 400 && e.Msg == "Invalid API Key"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := server(t, func(w http.ResponseWriter, r *http.Request) {
				if tc.header != "" {
					w.Header().Set("Retry-After", tc.header)
				}
				w.WriteHeader(tc.status)
				w.Write([]byte(`{"error":{"message":"Invalid API Key"}}`))
			})
			_, err := c.Transcribe(context.Background(), nil, Options{Model: "m"})
			if !tc.check(err) {
				t.Fatalf("got %T %v", err, err)
			}
		})
	}
}

func TestRetryAfterClamp(t *testing.T) {
	cases := map[string]time.Duration{
		"7":     7 * time.Second,
		"2.5":   2500 * time.Millisecond,
		"3600":  time.Hour,
		"3601":  defaultRetryAfter,
		"1e300": defaultRetryAfter,
		"inf":   defaultRetryAfter,
		"-Inf":  defaultRetryAfter,
		"NaN":   defaultRetryAfter,
		"0":     defaultRetryAfter,
		"-5":    defaultRetryAfter,
		"soon":  defaultRetryAfter,
	}
	for in, want := range cases {
		if got := retryAfter(in); got != want {
			t.Errorf("retryAfter(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestNetworkErrorIsTemporary(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	c := New("k")
	c.BaseURL = srv.URL
	var e *TemporaryError
	if _, err := c.Transcribe(context.Background(), nil, Options{Model: "m"}); !errors.As(err, &e) {
		t.Fatalf("got %T %v", err, err)
	}
}

func TestCanceledContextIsNotTemporary(t *testing.T) {
	c := server(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Transcribe(ctx, nil, Options{Model: "m"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %T %v", err, err)
	}
}

func TestPing(t *testing.T) {
	c := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") == "Bearer bad" {
			w.WriteHeader(401)
		}
	})
	if err := c.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.Key = "bad"
	var e *AuthError
	if err := c.Ping(context.Background()); !errors.As(err, &e) {
		t.Fatalf("got %v", err)
	}
}
