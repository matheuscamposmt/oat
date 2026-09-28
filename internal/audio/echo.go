package audio

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

// The names of the virtual devices that module-echo-cancel creates for oat.
const (
	ECSource = "oat_ec_source"
	ECSink   = "oat_ec_sink"
)

type echoState struct {
	Module   int    `json:"module"`
	PrevSink string `json:"prev_sink"`
	PID      int    `json:"pid"`
}

// Echo loads and unloads the echo-cancel module. It records the module in a
// state file, so that the next start can remove it after a crash.
type Echo struct {
	Pactl     Pactl
	StatePath string

	mu    sync.Mutex
	state *echoState
}

// Active reports whether this process loaded the module.
func (e *Echo) Active() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state != nil
}

// Enable loads the module for source and sink, then makes its sink the default
// output, so that the call audio gives the module a reference signal.
func (e *Echo) Enable(ctx context.Context, source, sink string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.state != nil {
		return nil
	}
	idx, err := e.Pactl.LoadEchoCancel(ctx, source, sink)
	if err != nil {
		return err
	}
	st := &echoState{Module: idx, PrevSink: sink, PID: os.Getpid()}
	if err := writeState(e.StatePath, st); err != nil {
		_ = e.Pactl.UnloadModule(ctx, idx)
		return err
	}
	if err := e.Pactl.SetDefaultSink(ctx, ECSink); err != nil {
		_ = e.Pactl.UnloadModule(ctx, idx)
		_ = os.Remove(e.StatePath)
		return err
	}
	e.state = st
	return nil
}

// Disable unloads the module. With restore, it first sets the old default output again.
// Use a fresh context: a canceled one makes pactl fail.
func (e *Echo) Disable(ctx context.Context, restore bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.state == nil {
		return nil
	}
	var errs []error
	if restore {
		errs = append(errs, e.Pactl.SetDefaultSink(ctx, e.state.PrevSink))
	}
	errs = append(errs, e.Pactl.UnloadModule(ctx, e.state.Module))
	if err := os.Remove(e.StatePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, err)
	}
	e.state = nil
	return errors.Join(errs...)
}

// CleanupStale removes a module that a crashed oat left behind. It does
// nothing while the process that loaded the module is alive. It reports
// whether it removed a module.
func (e *Echo) CleanupStale(ctx context.Context) (bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.state != nil {
		return false, nil
	}
	st, err := readState(e.StatePath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		_ = os.Remove(e.StatePath)
		return false, err
	}
	if st.PID != 0 && st.PID != os.Getpid() && pidAlive(st.PID) {
		return false, nil
	}
	name, args, ok, err := e.Pactl.Module(ctx, st.Module)
	if err != nil {
		return false, err
	}
	removed := false
	if ok && name == "module-echo-cancel" && strings.Contains(args, "sink_name="+ECSink) {
		if cur, err := e.Pactl.DefaultSink(ctx); err == nil && cur == ECSink {
			_ = e.Pactl.SetDefaultSink(ctx, st.PrevSink)
		}
		if err := e.Pactl.UnloadModule(ctx, st.Module); err != nil {
			return false, err
		}
		removed = true
	}
	_ = os.Remove(e.StatePath)
	return removed, nil
}

func writeState(path string, st *echoState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func readState(path string) (*echoState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var st echoState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
