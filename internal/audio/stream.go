package audio

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"syscall"
)

// ParecPath is the parec binary. Tests replace it with a script.
var ParecPath = "parec"

// FrameSamples is the number of samples in 20 ms at 16 kHz.
const FrameSamples = 320

// Stream is one running parec process that gives 20 ms frames.
type Stream struct {
	frames chan []int16
	cmd    *exec.Cmd
	done   chan struct{}
	closed atomic.Bool
	err    error
}

// Open starts parec on device: 16 kHz, mono, s16le. Frames closes when parec
// exits or Close runs.
func Open(ctx context.Context, device string) (*Stream, error) {
	cmd := exec.CommandContext(ctx, ParecPath, "--device="+device, "--format=s16le",
		"--rate=16000", "--channels=1", "--raw", "--latency-msec=100")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start parec: %w", err)
	}
	s := &Stream{frames: make(chan []int16, 64), cmd: cmd, done: make(chan struct{})}
	go func() {
		ReadFrames(stdout, s.frames)
		close(s.frames)
		if err := cmd.Wait(); err != nil && ctx.Err() == nil && !(s.closed.Load() && killedBySIGKILL(err)) {
			s.err = fmt.Errorf("parec on %s stopped: %v %s", device, err, strings.TrimSpace(stderr.String()))
		}
		close(s.done)
	}()
	return s, nil
}

// killedBySIGKILL reports whether err from cmd.Wait says that SIGKILL ended
// the process. Close stops parec with SIGKILL, so after Close only that exit
// is not a parec failure.
func killedBySIGKILL(err error) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	ws, ok := exitErr.Sys().(syscall.WaitStatus)
	return ok && ws.Signaled() && ws.Signal() == syscall.SIGKILL
}

// Frames returns the channel of 20 ms frames.
func (s *Stream) Frames() <-chan []int16 { return s.frames }

// Close stops parec and waits for it.
func (s *Stream) Close() error {
	s.closed.Store(true)
	_ = s.cmd.Process.Kill()
	for range s.frames {
	}
	<-s.done
	return nil
}

// Err waits for the end of the stream and returns why parec stopped.
func (s *Stream) Err() error {
	<-s.done
	return s.err
}

// ReadFrames reads frames of s16le PCM from r until EOF or an error.
// It drops a partial last frame.
func ReadFrames(r io.Reader, out chan<- []int16) {
	buf := make([]byte, FrameSamples*2)
	for {
		if _, err := io.ReadFull(r, buf); err != nil {
			return
		}
		f := make([]int16, FrameSamples)
		for i := range f {
			f[i] = int16(binary.LittleEndian.Uint16(buf[2*i:]))
		}
		out <- f
	}
}
