// Package claudestt sends audio to the speech-to-text stream of the Claude
// Code voice mode. Anthropic does not document this endpoint, so it can change
// in any Claude Code release.
//
// The client authenticates with the claude.ai OAuth token that Claude Code
// keeps in its credentials file. The client only reads that file. It never
// refreshes the token, because a refresh rotates the refresh token and can log
// Claude Code out.
package claudestt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"

	"github.com/matheuscamposmt/oat/internal/stt"
)

// ExpiryMargin is how long before its expiry the client stops using a token.
const ExpiryMargin = 30 * time.Second

// DefaultURL is the voice stream of Claude Code.
const DefaultURL = "wss://api.anthropic.com/api/ws/speech_to_text/voice_stream"

const (
	service    = "claude"
	sampleRate = 16000
	frameBytes = 3200 // 100 ms of 16 kHz mono s16le
	wavHeader  = 44

	defaultRetryAfter = 30 * time.Second
	maxRetryAfter     = time.Hour

	// statusRateLimit is the close code of the server when too many streams
	// are open: "Rate limit exceeded. Wait a few seconds before retrying."
	statusRateLimit = websocket.StatusCode(4029)
	rateLimitWait   = 5 * time.Second
	// The server refuses a sixth stream at the same time. MaxStreams is the
	// default limit of the client.
	MaxStreams     = 4
	keepAliveEvery = 8 * time.Second
	writeTimeout   = 10 * time.Second

	msgKeepAlive   = `{"type":"KeepAlive"}`
	msgCloseStream = `{"type":"CloseStream"}`
)

// Client opens streams to the voice endpoint. It is safe for concurrent use.
type Client struct {
	URL             string
	CredentialsPath string
	UserAgent       string
	HTTP            *http.Client
	Now             func() time.Time

	// Quiet is the wait for more messages after the endpoint that follows
	// CloseStream. Idle ends the stream when no message comes for that long
	// after CloseStream. Finish is the longest wait after CloseStream.
	Quiet  time.Duration
	Idle   time.Duration
	Finish time.Duration
	// Timeout bounds one call from the dial to the last message.
	Timeout time.Duration

	// Streams is the most streams open at the same time, for live streams and
	// for Transcribe together. MaxStreams when zero.
	Streams int
	semOnce sync.Once
	sem     chan struct{}

	// Trace, when set, gets the reason that ended each stream. Tests use it.
	Trace func(reason string, endpoints int)
	// beforeLastFrame, when set, runs before the last audio frame. Tests use it.
	beforeLastFrame func()
}

// New returns a Client for the voice stream of Claude Code.
func New(credentialsPath, userAgent string) *Client {
	return &Client{
		URL:             DefaultURL,
		CredentialsPath: credentialsPath,
		UserAgent:       userAgent,
		HTTP:            &http.Client{Timeout: 30 * time.Second},
		Now:             time.Now,
		Quiet:           time.Second,
		Idle:            5 * time.Second,
		Finish:          20 * time.Second,
		Timeout:         90 * time.Second,
	}
}

// CredentialsPath returns the credentials file of Claude Code:
// $CLAUDE_CONFIG_DIR/.credentials.json, or ~/.claude/.credentials.json.
func CredentialsPath() (string, error) {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, ".credentials.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", ".credentials.json"), nil
}

// ErrNoLogin means that the credentials file has no claude.ai login.
var ErrNoLogin = errors.New("no Claude Code login found. Run claude and log in with your claude.ai account")

// Token is the OAuth access token of the claude.ai login.
type Token struct {
	Access  string
	Expires time.Time // zero when the file has no expiry
}

// ReadToken reads the access token from the credentials file of Claude Code.
func ReadToken(path string) (Token, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Token{}, fmt.Errorf("%w (%s does not exist)", ErrNoLogin, path)
	}
	if err != nil {
		return Token{}, err
	}
	var f struct {
		ClaudeAiOauth *struct {
			AccessToken string `json:"accessToken"`
			ExpiresAt   int64  `json:"expiresAt"` // milliseconds
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return Token{}, fmt.Errorf("read %s: %w", path, err)
	}
	if f.ClaudeAiOauth == nil || f.ClaudeAiOauth.AccessToken == "" {
		return Token{}, ErrNoLogin
	}
	t := Token{Access: f.ClaudeAiOauth.AccessToken}
	if f.ClaudeAiOauth.ExpiresAt > 0 {
		t.Expires = time.UnixMilli(f.ClaudeAiOauth.ExpiresAt)
	}
	return t, nil
}

// token reads the file on each call, so a refresh by Claude Code takes effect at once.
// An expired token is a temporary error: the pipeline waits, and Claude Code can renew it.
func (c *Client) token() (string, error) {
	t, err := ReadToken(c.CredentialsPath)
	if errors.Is(err, ErrNoLogin) {
		return "", &stt.AuthError{Service: service, Msg: err.Error()}
	}
	if err != nil {
		return "", &stt.TemporaryError{Service: service, Err: err}
	}
	if !t.Expires.IsZero() && c.now().Add(ExpiryMargin).After(t.Expires) {
		return "", &stt.TemporaryError{Service: service, Err: fmt.Errorf("the Claude Code login expired at %s. Open Claude Code to renew it", t.Expires.Format("15:04"))}
	}
	return t.Access, nil
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Transcribe sends one WAV file from chunk.EncodeWAV over its own stream,
// as fast as the connection takes it, and returns its transcript.
// The endpoint gives no times, so the segments divide the chunk by text length.
func (c *Client) Transcribe(ctx context.Context, wav []byte, opts stt.Options) (stt.Result, error) {
	pcm := wav
	if len(wav) >= wavHeader && string(wav[:4]) == "RIFF" {
		pcm = wav[wavHeader:]
	}
	if len(pcm) == 0 {
		return stt.Result{}, nil
	}
	// outer is the context of the caller. ctx also ends at the timeout of the
	// call, and then the errors are temporary.
	outer := ctx
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	st, err := c.open(outer, ctx, opts.Language, nil)
	if err != nil {
		return stt.Result{}, err
	}
	defer st.Close()
	for i := 0; i < len(pcm); i += frameBytes {
		if c.beforeLastFrame != nil && i+frameBytes >= len(pcm) {
			c.beforeLastFrame()
		}
		if err := st.Send(pcm[i:min(i+frameBytes, len(pcm))]); err != nil {
			return stt.Result{}, err
		}
	}
	utts, err := st.Finish()
	if err != nil {
		return stt.Result{}, err
	}
	return c.result(utts, len(pcm)), nil
}

type readEvent struct {
	msg message
	err error
}

// streamError converts an error after the upgrade. When the timeout of the call
// ended ctx, the error is temporary, so the pipeline tries the chunk again.
func (c *Client) streamError(outer, ctx context.Context, err error) error {
	if outer.Err() != nil {
		return outer.Err()
	}
	if ctx.Err() != nil {
		return &stt.TemporaryError{Service: service, Err: fmt.Errorf("no answer in %s", c.Timeout)}
	}
	var ce websocket.CloseError
	if errors.As(err, &ce) && ce.Code == websocket.StatusUnsupportedData {
		return &stt.RequestError{Service: service, Status: int(ce.Code), Msg: ce.Reason}
	}
	if errors.As(err, &ce) && ce.Code == statusRateLimit {
		return &stt.RateLimitError{Service: service, RetryAfter: rateLimitWait}
	}
	if st := websocket.CloseStatus(err); st == websocket.StatusNormalClosure || st == websocket.StatusNoStatusRcvd {
		return &stt.TemporaryError{Service: service, Err: errors.New("the server ended the stream before the end of the audio")}
	}
	return &stt.TemporaryError{Service: service, Err: err}
}

// Ping opens one stream and closes it. It sends no audio.
func (c *Client) Ping(ctx context.Context) error {
	conn, err := c.dial(ctx, ctx, "")
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	if err := conn.Write(ctx, websocket.MessageText, []byte(msgKeepAlive)); err != nil {
		return mapError(ctx, err)
	}
	return conn.Close(websocket.StatusNormalClosure, "")
}

func (c *Client) dial(ctx, outer context.Context, lang string) (*websocket.Conn, error) {
	tok, err := c.token()
	if err != nil {
		return nil, err
	}
	q := url.Values{
		"encoding":                {"linear16"},
		"sample_rate":             {strconv.Itoa(sampleRate)},
		"channels":                {"1"},
		"endpointing_ms":          {"300"},
		"utterance_end_ms":        {"1000"},
		"use_conversation_engine": {"true"},
	}
	// The endpoint refuses "auto". Without the parameter, it detects the language.
	if lang != "" && lang != "auto" {
		q.Set("language", lang)
	}
	h := http.Header{}
	h.Set("Authorization", "Bearer "+tok)
	h.Set("User-Agent", c.UserAgent)
	h.Set("x-app", "cli")
	h.Set("anthropic-client-platform", "cli")
	conn, resp, err := websocket.Dial(ctx, c.URL+"?"+q.Encode(), &websocket.DialOptions{HTTPClient: c.HTTP, HTTPHeader: h})
	if err != nil {
		return nil, dialError(outer, resp, err)
	}
	conn.SetReadLimit(1 << 20)
	return conn, nil
}

type message struct {
	Type        string `json:"type"`
	Data        string `json:"data"`
	Description string `json:"description"`
	ErrorCode   string `json:"error_code"`
	Message     string `json:"message"`
}

// collector keeps the final text of each utterance.
type collector struct {
	utterances []string
	cur        string
	endpoints  int
}

// text takes the full text of the current utterance. Each text replaces the
// one before it. The server sends an endpoint between utterances, and a text
// that starts with other words is a revision of the same utterance, not a new
// one: a live test of 40 chunks showed that.
//
// The last text before an endpoint can drop the last words of the one before
// it. Then the longer text stays.
func (c *collector) text(t string) {
	t = strings.TrimSpace(t)
	if t == "" {
		return
	}
	if len(t) < len(c.cur) && strings.HasPrefix(c.cur, t) {
		return
	}
	c.cur = t
}

func (c *collector) endpoint() {
	c.endpoints++
	if c.cur != "" {
		c.utterances = append(c.utterances, c.cur)
		c.cur = ""
	}
}

// done returns the utterances. A text without an endpoint counts as final.
func (c *collector) done() []string {
	if c.cur != "" {
		c.utterances = append(c.utterances, c.cur)
		c.cur = ""
	}
	return c.utterances
}

var sentenceEnd = regexp.MustCompile(`[.!?…]+\s+`)

// Sentences splits a text after each sentence end.
func Sentences(text string) []string {
	var out []string
	last := 0
	for _, loc := range sentenceEnd.FindAllStringIndex(text, -1) {
		if s := strings.TrimSpace(text[last:loc[1]]); s != "" {
			out = append(out, s)
		}
		last = loc[1]
	}
	if s := strings.TrimSpace(text[last:]); s != "" {
		out = append(out, s)
	}
	return out
}

// Place gives each sentence a part of the chunk in proportion to its length.
func Place(utterances []string, dur float64) []stt.Segment {
	var sents []string
	for _, u := range utterances {
		sents = append(sents, Sentences(u)...)
	}
	total := 0
	for _, s := range sents {
		total += utf8.RuneCountInString(s)
	}
	segs := make([]stt.Segment, 0, len(sents))
	at := 0.0
	for i, s := range sents {
		end := at + dur*float64(utf8.RuneCountInString(s))/float64(total)
		if i == len(sents)-1 {
			end = dur
		}
		segs = append(segs, stt.Segment{Start: at, End: end, Text: s})
		at = end
	}
	return segs
}

func (c *Client) result(utterances []string, pcmBytes int) stt.Result {
	dur := float64(pcmBytes) / (2 * sampleRate)
	return stt.Result{Text: strings.Join(utterances, " "), Segments: Place(utterances, dur)}
}

func dialError(ctx context.Context, resp *http.Response, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if resp == nil {
		return &stt.TemporaryError{Service: service, Err: err}
	}
	code := resp.StatusCode
	switch {
	case resp.Header.Get("cf-mitigated") != "":
		return &stt.TemporaryError{Service: service, Err: fmt.Errorf("the Cloudflare filter blocked the connection (HTTP %d)", code)}
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return &stt.AuthError{Service: service, Msg: fmt.Sprintf("HTTP %d. Open Claude Code and log in again", code)}
	case code == http.StatusTooManyRequests:
		return &stt.RateLimitError{Service: service, RetryAfter: retryAfter(resp.Header.Get("Retry-After"))}
	case code >= 500:
		return &stt.TemporaryError{Service: service, Err: fmt.Errorf("HTTP %d", code)}
	default:
		return &stt.RequestError{Service: service, Status: code, Msg: "the WebSocket upgrade was refused"}
	}
}

// mapError converts a WebSocket error after the upgrade.
func mapError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var ce websocket.CloseError
	if errors.As(err, &ce) && ce.Code == websocket.StatusUnsupportedData {
		return &stt.RequestError{Service: service, Status: int(ce.Code), Msg: ce.Reason}
	}
	return &stt.TemporaryError{Service: service, Err: err}
}

func retryAfter(v string) time.Duration {
	secs, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || secs <= 0 || secs > maxRetryAfter.Seconds() || secs != secs {
		return defaultRetryAfter
	}
	return time.Duration(secs * float64(time.Second))
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
