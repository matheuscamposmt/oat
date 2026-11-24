// Package stt holds the request, result, and error types that the
// speech-to-text backends share.
package stt

import (
	"fmt"
	"time"
)

// Options are the request fields. An empty or "auto" Language lets the backend detect it.
type Options struct {
	Model    string
	Language string
	Prompt   string
}

// Segment is one piece of the transcript. Times are seconds from the chunk start.
type Segment struct {
	Start            float64 `json:"start"`
	End              float64 `json:"end"`
	Text             string  `json:"text"`
	NoSpeechProb     float64 `json:"no_speech_prob"`
	CompressionRatio float64 `json:"compression_ratio"`
	AvgLogprob       float64 `json:"avg_logprob"`
}

// Result is the transcript of one chunk.
type Result struct {
	Text     string    `json:"text"`
	Language string    `json:"language"`
	Segments []Segment `json:"segments"`
}

// RateLimitError means that the backend asks the client to wait.
type RateLimitError struct {
	Service    string
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("%s rate limit, retry after %s", e.Service, e.RetryAfter)
}

// AuthError means that the backend rejected the credentials. A retry does not help.
type AuthError struct {
	Service string
	Msg     string
}

func (e *AuthError) Error() string { return e.Service + " rejected the credentials: " + e.Msg }

// TemporaryError means a network error or a server error. A retry can work.
type TemporaryError struct {
	Service string
	Err     error
}

func (e *TemporaryError) Error() string { return e.Service + " temporary error: " + e.Err.Error() }
func (e *TemporaryError) Unwrap() error { return e.Err }

// RequestError means that the backend refused this chunk. A retry does not help.
type RequestError struct {
	Service string
	Status  int
	Msg     string
}

func (e *RequestError) Error() string {
	return fmt.Sprintf("%s refused the request (%d): %s", e.Service, e.Status, e.Msg)
}
