// Package groq calls the Groq speech-to-text API.
package groq

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/matheuscamposmt/oat/internal/stt"
)

// DefaultBaseURL is the OpenAI-compatible Groq API.
const DefaultBaseURL = "https://api.groq.com/openai/v1"

const (
	defaultRetryAfter = 10 * time.Second
	maxRetryAfter     = time.Hour
)

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

// The request, result, and error types live in package stt. These aliases
// keep the old names.
type (
	Options        = stt.Options
	Segment        = stt.Segment
	Result         = stt.Result
	RateLimitError = stt.RateLimitError
	AuthError      = stt.AuthError
	TemporaryError = stt.TemporaryError
	RequestError   = stt.RequestError
)

// service is the name in the error messages.
const service = "groq"

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
	prompt := opts.Prompt
	if len(opts.Keyterms) > 0 {
		prompt = strings.Join(opts.Keyterms, ", ") + "."
		if opts.Prompt != "" {
			prompt += " " + opts.Prompt
		}
	}
	if prompt != "" {
		fields = append(fields, [2]string{"prompt", prompt})
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
		return &TemporaryError{Service: service, Err: err}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return &TemporaryError{Service: service, Err: err}
	}
	switch {
	case resp.StatusCode == http.StatusOK:
		if out == nil {
			return nil
		}
		if err := json.Unmarshal(data, out); err != nil {
			return &TemporaryError{Service: service, Err: fmt.Errorf("decode the response: %w", err)}
		}
		return nil
	case resp.StatusCode == http.StatusTooManyRequests:
		return &RateLimitError{Service: service, RetryAfter: retryAfter(resp.Header.Get("Retry-After"))}
	case resp.StatusCode == http.StatusUnauthorized:
		return &AuthError{Service: service, Msg: errorMessage(data)}
	case resp.StatusCode >= 500:
		return &TemporaryError{Service: service, Err: fmt.Errorf("HTTP %d: %s", resp.StatusCode, errorMessage(data))}
	default:
		return &RequestError{Service: service, Status: resp.StatusCode, Msg: errorMessage(data)}
	}
}

// retryAfter reads the Retry-After header in seconds. A value that is not a
// number in (0, 3600] gives the default. ParseFloat accepts inf and NaN.
func retryAfter(v string) time.Duration {
	secs, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || math.IsNaN(secs) || secs <= 0 || secs > maxRetryAfter.Seconds() {
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
