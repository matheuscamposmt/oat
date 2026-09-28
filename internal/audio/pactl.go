// Package audio runs parec to capture audio and pactl to manage devices and
// the echo-cancel module.
package audio

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Runner runs a command and returns its standard output. Tests replace it.
type Runner func(ctx context.Context, name string, args ...string) (string, error)

// Exec runs a command with LC_ALL=C, so that pactl prints English labels.
func Exec(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// Pactl runs the pactl commands that oat uses.
type Pactl struct{ Run Runner }

// NewPactl returns a Pactl that runs the real pactl.
func NewPactl() Pactl { return Pactl{Run: Exec} }

func (p Pactl) run(ctx context.Context, args ...string) (string, error) {
	return p.Run(ctx, "pactl", args...)
}

// Info returns the output of pactl info.
func (p Pactl) Info(ctx context.Context) (string, error) { return p.run(ctx, "info") }

// DefaultSink returns the name of the default output.
func (p Pactl) DefaultSink(ctx context.Context) (string, error) {
	out, err := p.run(ctx, "get-default-sink")
	return strings.TrimSpace(out), err
}

// DefaultSource returns the name of the default input.
func (p Pactl) DefaultSource(ctx context.Context) (string, error) {
	out, err := p.run(ctx, "get-default-source")
	return strings.TrimSpace(out), err
}

// SetDefaultSink makes name the default output.
func (p Pactl) SetDefaultSink(ctx context.Context, name string) error {
	_, err := p.run(ctx, "set-default-sink", name)
	return err
}

// LoadEchoCancel loads module-echo-cancel for source and sink and returns its index.
func (p Pactl) LoadEchoCancel(ctx context.Context, source, sink string) (int, error) {
	out, err := p.run(ctx, "load-module", "module-echo-cancel", "aec_method=webrtc",
		"source_master="+source, "sink_master="+sink, "source_name="+ECSource, "sink_name="+ECSink)
	if err != nil {
		return 0, err
	}
	idx, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("pactl load-module printed %q", strings.TrimSpace(out))
	}
	return idx, nil
}

// UnloadModule unloads the module with the index.
func (p Pactl) UnloadModule(ctx context.Context, idx int) error {
	_, err := p.run(ctx, "unload-module", strconv.Itoa(idx))
	return err
}

// Module returns the name and the arguments of a loaded module.
func (p Pactl) Module(ctx context.Context, idx int) (name, args string, ok bool, err error) {
	out, err := p.run(ctx, "list", "modules", "short")
	if err != nil {
		return "", "", false, err
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(line, "\t", 3)
		if len(f) >= 2 && f[0] == strconv.Itoa(idx) {
			if len(f) == 3 {
				args = strings.TrimSpace(f[2])
			}
			return f[1], args, true, nil
		}
	}
	return "", "", false, nil
}

// Sink returns the details of one output from pactl list sinks.
func (p Pactl) Sink(ctx context.Context, name string) (SinkInfo, error) {
	out, err := p.run(ctx, "list", "sinks")
	if err != nil {
		return SinkInfo{}, err
	}
	for _, s := range ParseSinks(out) {
		if s.Name == name {
			return s, nil
		}
	}
	return SinkInfo{}, fmt.Errorf("sink %q not found", name)
}

// SinkInfo holds the fields of an output that decide the echo mode.
type SinkInfo struct {
	Name       string
	ActivePort string
	Bus        string
	FormFactor string
}

// ParseSinks reads the output of LC_ALL=C pactl list sinks.
func ParseSinks(out string) []SinkInfo {
	var sinks []SinkInfo
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "Sink #") {
			sinks = append(sinks, SinkInfo{})
			continue
		}
		if len(sinks) == 0 {
			continue
		}
		cur := &sinks[len(sinks)-1]
		if v, ok := strings.CutPrefix(line, "Name: "); ok {
			cur.Name = v
		} else if v, ok := strings.CutPrefix(line, "Active Port: "); ok {
			cur.ActivePort = v
		} else if v, ok := strings.CutPrefix(line, "device.bus = "); ok {
			cur.Bus = strings.Trim(v, `"`)
		} else if v, ok := strings.CutPrefix(line, "device.form_factor = "); ok {
			cur.FormFactor = strings.Trim(v, `"`)
		}
	}
	return sinks
}

// IsHeadphones reports whether the output goes to headphones or a headset.
func (s SinkInfo) IsHeadphones() bool {
	port := strings.ToLower(s.ActivePort)
	return strings.Contains(port, "headphone") || strings.Contains(port, "headset") ||
		s.Bus == "bluetooth" || s.FormFactor == "headphone" || s.FormFactor == "headset"
}

// WantEcho decides whether echo cancel runs for the echo mode and the output.
func WantEcho(mode string, sink SinkInfo) bool {
	switch mode {
	case "on":
		return true
	case "off":
		return false
	}
	return !sink.IsHeadphones()
}
