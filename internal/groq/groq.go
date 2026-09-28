// Package groq calls the Groq speech-to-text API.
package groq

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the OpenAI-compatible Groq API.
const DefaultBaseURL = "https://api.groq.com/openai/v1"

const defaultRetryAfter = 10 * time.Second

// Client calls Groq with one API key.
type Client struct {
	BaseURL string
	Key     string
	HTTP    *http.Client
}

// New returns a Client for the Groq API.
func New(key string) *Client {
	return &Client{BaseURL: DefaultBaseURL, Key: key, HTTP: &http.Client{Timeout: 90 * time.Second}}
}

// Options are the request fields. An empty or "auto" Language lets Whisper detect it.
type Options struct {
	Model    string
	Language string
	Prompt   string
}

// Segment is one segment of a verbose_json response. Times are seconds from the chunk start.
type Segment struct {
	Start            float64 `json:"start"`
	End              float64 `json:"end"`
	Text             string  `json:"text"`
	NoSpeechProb     float64 `json:"no_speech_prob"`
	CompressionRatio float64 `json:"compression_ratio"`
	AvgLogprob       float64 `json:"avg_logprob"`
}

// Result is a verbose_json response.
type Result struct {
	Text     string    `json:"text"`
	Language string    `json:"language"`
	Segments []Segment `json:"segments"`
}

// RateLimitError means HTTP 429.
type RateLimitError struct{ RetryAfter time.Duration }

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("groq rate limit, retry after %s", e.RetryAfter)
}

// AuthError means HTTP 401: Groq rejected the key.
type AuthError struct{ Msg string }

func (e *AuthError) Error() string { return "groq rejected the API key: " + e.Msg }

// TemporaryError means a network error or HTTP 5xx. A retry can work.
type TemporaryError struct{ Err error }

func (e *TemporaryError) Error() string { return "groq temporary error: " + e.Err.Error() }
func (e *TemporaryError) Unwrap() error { return e.Err }

// RequestError means another HTTP 4xx. A retry does not help.
type RequestError struct {
	Status int
	Msg    string
}

func (e *RequestError) Error() string {
	return fmt.Sprintf("groq refused the request (HTTP %d): %s", e.Status, e.Msg)
}

// Transcribe sends one WAV file and returns the verbose_json result.
func (c *Client) Transcribe(ctx context.Context, wav []byte, opts Options) (Result, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", "chunk.wav")
	if err != nil {
		return Result{}, err
	}
	if _, err := part.Write(wav); err != nil {
		return Result{}, err
	}
	fields := [][2]string{{"model", opts.Model}, {"response_format", "verbose_json"}, {"temperature", "0"}}
	if opts.Language != "" && opts.Language != "auto" {
		fields = append(fields, [2]string{"language", opts.Language})
	}
	if opts.Prompt != "" {
		fields = append(fields, [2]string{"prompt", opts.Prompt})
	}
	for _, f := range fields {
		if err := w.WriteField(f[0], f[1]); err != nil {
			return Result{}, err
		}
	}
	if err := w.Close(); err != nil {
		return Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/audio/transcriptions", &body)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	var res Result
	err = c.do(req, &res)
	return res, err
}

// Ping calls GET /models to test the key. It uses no audio quota.
func (c *Client) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/models", nil)
	if err != nil {
		return err
	}
	return c.do(req, nil)
}

func (c *Client) do(req *http.Request, out any) error {
	req.Header.Set("Authorization", "Bearer "+c.Key)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if ctxErr := req.Context().Err(); ctxErr != nil {
			return ctxErr
		}
		return &TemporaryError{Err: err}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return &TemporaryError{Err: err}
	}
	switch {
	case resp.StatusCode == http.StatusOK:
		if out == nil {
			return nil
		}
		if err := json.Unmarshal(data, out); err != nil {
			return &TemporaryError{Err: fmt.Errorf("decode the response: %w", err)}
		}
		return nil
	case resp.StatusCode == http.StatusTooManyRequests:
		return &RateLimitError{RetryAfter: retryAfter(resp.Header.Get("Retry-After"))}
	case resp.StatusCode == http.StatusUnauthorized:
		return &AuthError{Msg: errorMessage(data)}
	case resp.StatusCode >= 500:
		return &TemporaryError{Err: fmt.Errorf("HTTP %d: %s", resp.StatusCode, errorMessage(data))}
	default:
		return &RequestError{Status: resp.StatusCode, Msg: errorMessage(data)}
	}
}

func retryAfter(v string) time.Duration {
	secs, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || secs <= 0 {
		return defaultRetryAfter
	}
	return time.Duration(secs * float64(time.Second))
}

func errorMessage(data []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	s := strings.TrimSpace(string(data))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
