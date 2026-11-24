package claudestt

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/matheuscamposmt/oat/internal/stt"
)

// Stream is one connection to the voice endpoint. The caller sends PCM with
// Send, as fast as it wants or in real time, then calls Finish for the final
// text. A Stream holds one of the Streams slots of its Client until it ends.
type Stream struct {
	c      *Client
	conn   *websocket.Conn
	outer  context.Context // the context of the caller
	ctx    context.Context // also ends at Close and at the timeout of a Transcribe call
	cancel context.CancelFunc
	onText func(string)

	wmu       sync.Mutex
	sent      atomic.Bool   // all the audio is written; CloseStream follows
	closing   chan struct{} // Finish closes it before it writes CloseStream
	closeOnce sync.Once
	done      chan struct{} // run closes it when the stream ends

	utts []string // set by run before done closes
	err  error
}

// Open opens a stream. onText, when not nil, gets the full text after each
// change: the final utterances and the current one. It runs on the goroutine
// of the stream, so it must not block. Open waits for a free slot when the
// Client already has Streams streams open.
func (c *Client) Open(ctx context.Context, lang string, keyterms []string, onText func(string)) (*Stream, error) {
	return c.open(ctx, ctx, lang, EncodeKeyterms(keyterms), onText)
}

func (c *Client) slots() chan struct{} {
	c.semOnce.Do(func() {
		n := c.Streams
		if n <= 0 {
			n = MaxStreams
		}
		c.sem = make(chan struct{}, n)
	})
	return c.sem
}

func (c *Client) open(outer, ctx context.Context, lang, keyterms string, onText func(string)) (*Stream, error) {
	sem := c.slots()
	select {
	case sem <- struct{}{}:
	case <-ctx.Done():
		if outer.Err() != nil {
			return nil, outer.Err()
		}
		return nil, &stt.TemporaryError{Service: service, Err: errors.New("no free stream slot")}
	}
	conn, err := c.dial(ctx, outer, lang, keyterms)
	if err != nil {
		<-sem
		return nil, err
	}
	sctx, cancel := context.WithCancel(ctx)
	s := &Stream{
		c: c, conn: conn, outer: outer, ctx: sctx, cancel: cancel, onText: onText,
		closing: make(chan struct{}), done: make(chan struct{}),
	}
	if err := s.write(websocket.MessageText, []byte(msgKeepAlive)); err != nil {
		cancel()
		conn.CloseNow()
		<-sem
		return nil, c.streamError(outer, ctx, err)
	}
	events := make(chan readEvent, 64)
	go s.read(events)
	go s.keepAlive()
	go func() {
		defer func() { <-sem }()
		s.run(events)
	}()
	return s, nil
}

// Send writes PCM (16 kHz mono s16le). It returns the error of the stream
// when the stream already ended.
func (s *Stream) Send(pcm []byte) error {
	select {
	case <-s.done:
		if s.err != nil {
			return s.err
		}
		return &stt.TemporaryError{Service: service, Err: errors.New("the stream ended")}
	default:
	}
	if err := s.write(websocket.MessageBinary, pcm); err != nil {
		// A close by the server breaks the write. The read gives the reason.
		select {
		case <-s.done:
			if s.err != nil {
				return s.err
			}
		case <-time.After(2 * time.Second):
		}
		return s.c.streamError(s.outer, s.ctx, err)
	}
	return nil
}

// Finish sends CloseStream and waits for the final text: one string for each
// utterance. It is safe to call more than once.
func (s *Stream) Finish() ([]string, error) {
	s.closeOnce.Do(func() {
		s.sent.Store(true)
		close(s.closing)
		_ = s.write(websocket.MessageText, []byte(msgCloseStream)) // a failure shows in the read
	})
	<-s.done
	return s.utts, s.err
}

// Close ends the stream now and drops its text. It is safe to call more than once.
func (s *Stream) Close() {
	s.cancel()
	<-s.done
}

// Alive reports whether the stream is still open.
func (s *Stream) Alive() bool {
	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

func (s *Stream) write(typ websocket.MessageType, data []byte) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	ctx, cancel := context.WithTimeout(s.ctx, writeTimeout)
	defer cancel()
	return s.conn.Write(ctx, typ, data)
}

// read sends the messages in order, then the read error, on one channel.
// With two channels, select could take the close before the last text.
func (s *Stream) read(events chan<- readEvent) {
	for {
		_, data, err := s.conn.Read(s.ctx)
		if err != nil {
			select {
			case events <- readEvent{err: err}:
			case <-s.done:
			}
			return
		}
		var m message
		if json.Unmarshal(data, &m) == nil {
			select {
			case events <- readEvent{msg: m}:
			case <-s.done:
				return
			}
		}
	}
}

// keepAlive keeps an idle stream open, as Claude Code does, until CloseStream.
func (s *Stream) keepAlive() {
	t := time.NewTicker(keepAliveEvery)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-s.closing:
			return
		case <-t.C:
			_ = s.write(websocket.MessageText, []byte(msgKeepAlive))
		}
	}
}

// run handles the messages until the stream ends, then sets utts or err.
func (s *Stream) run(events <-chan readEvent) {
	c := s.c
	var col collector
	var closed bool
	var finish, quiet, idle <-chan time.Time
	closing := s.closing
	last := ""
	defer func() {
		s.cancel()
		s.conn.CloseNow()
		close(s.done)
	}()
	end := func(reason string) {
		if c.Trace != nil {
			c.Trace(reason, col.endpoints)
		}
		s.utts = col.done()
	}
	notify := func() {
		if s.onText == nil {
			return
		}
		if t := col.preview(); t != last {
			last = t
			s.onText(t)
		}
	}
	for {
		select {
		case <-closing:
			closing = nil
			closed = true
			finish = time.After(c.Finish)
			idle = time.After(c.Idle)
		case ev := <-events:
			if ev.err != nil {
				st := websocket.CloseStatus(ev.err)
				switch {
				case !closed && !s.sent.Load():
					// The server ended the stream before it got all the audio.
					s.err = c.streamError(s.outer, s.ctx, ev.err)
				case st == websocket.StatusNormalClosure || st == websocket.StatusNoStatusRcvd:
					end("server close")
				case st == -1 && col.endpoints > 0 && s.ctx.Err() == nil:
					end("connection end") // the connection ended after the final text
				default:
					s.err = c.streamError(s.outer, s.ctx, ev.err)
				}
				return
			}
			m := ev.msg
			if closed {
				idle = time.After(c.Idle)
			}
			switch m.Type {
			case "TranscriptInterim", "TranscriptText":
				col.text(m.Data)
				quiet = nil
				notify()
			case "TranscriptEndpoint":
				col.endpoint()
				if closed {
					quiet = time.After(c.Quiet)
				}
			case "TranscriptError":
				msg := firstNonEmpty(m.Description, m.ErrorCode, "unknown transcription error")
				s.err = &stt.RequestError{Service: service, Status: 0, Msg: msg}
				return
			case "error":
				s.err = &stt.TemporaryError{Service: service, Err: errors.New(firstNonEmpty(m.Message, "server error"))}
				return
			}
		case <-quiet:
			end("quiet")
			return
		case <-idle:
			end("idle")
			return
		case <-finish:
			end("finish")
			return
		case <-s.ctx.Done():
			s.err = c.streamError(s.outer, s.ctx, s.ctx.Err())
			return
		}
	}
}

// preview returns the final utterances and the current one as one text.
func (c *collector) preview() string {
	parts := append(append([]string(nil), c.utterances...), c.cur)
	return strings.TrimSpace(strings.Join(parts, " "))
}
