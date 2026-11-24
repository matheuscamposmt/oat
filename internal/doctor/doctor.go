// Package doctor tests the environment that oat needs and registers the MCP server.
package doctor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/matheuscamposmt/oat/internal/audio"
	"github.com/matheuscamposmt/oat/internal/config"
)

// Result is the outcome of one test.
type Result struct {
	Name   string
	OK     bool
	Detail string
	Fix    string
	// Optional marks a test whose failure does not fail doctor.
	Optional bool
}

// Deps holds the functions that the tests call. Tests replace them.
type Deps struct {
	ConfigPath  string
	ConfigErr   error // from loading or validating the configuration file
	LookPath    func(string) (string, error)
	Pactl       audio.Pactl
	Transcriber string // claude or groq
	GroqKey     func() (key, source string, err error)
	Ping        func(ctx context.Context, key string) error
	// ClaudeLogin returns the expiry of the claude.ai token and the file that holds it.
	ClaudeLogin func() (expires time.Time, source string, err error)
	ClaudePing  func(ctx context.Context) error
	Now         func() time.Time
	// ExpiryMargin is how long before its expiry the client stops using a token.
	ExpiryMargin time.Duration
	// AutoSummary is on when oat writes a summary after each meeting with claude -p.
	AutoSummary bool
	MCPStatus   func(ctx context.Context) error // nil when Claude Code knows the oat server
	FFmpeg      func() (string, error)          // the path of ffmpeg, which only oat transcribe needs
	DataDir     string
	Echo        *audio.Echo
}

// Run runs all tests in order.
func Run(ctx context.Context, d Deps) []Result {
	var rs []Result
	add := func(r Result) { rs = append(rs, r) }

	if d.ConfigErr != nil {
		add(Result{Name: "Configuration", Detail: d.ConfigErr.Error(), Fix: "Fix " + d.ConfigPath + ". The README lists the keys."})
	}

	for _, bin := range []string{"parec", "pactl"} {
		if p, err := d.LookPath(bin); err != nil {
			add(Result{Name: bin, Detail: "not found", Fix: "Install pulseaudio-utils: sudo apt install pulseaudio-utils"})
		} else {
			add(Result{Name: bin, OK: true, Detail: p})
		}
	}

	if info, err := d.Pactl.Info(ctx); err != nil {
		add(Result{Name: "Pulse server", Detail: err.Error(), Fix: "Start PipeWire with its Pulse layer (pipewire-pulse), or PulseAudio."})
	} else {
		add(Result{Name: "Pulse server", OK: true, Detail: serverName(info)})
	}

	src, err1 := d.Pactl.DefaultSource(ctx)
	sink, err2 := d.Pactl.DefaultSink(ctx)
	if err := errors.Join(err1, err2); err != nil || src == "" || sink == "" {
		add(Result{Name: "Default devices", Detail: fmt.Sprint(err), Fix: "Select an input and an output in the sound configuration."})
	} else {
		add(Result{Name: "Default devices", OK: true, Detail: "mic " + src + ", output " + sink})
	}

	if d.Transcriber == "claude" {
		for _, r := range claudeChecks(ctx, d) {
			add(r)
		}
	} else {
		for _, r := range groqChecks(ctx, d) {
			add(r)
		}
	}

	if d.AutoSummary {
		if p, err := d.LookPath("claude"); err != nil {
			add(Result{Name: "Summary", Detail: "claude is not in the PATH", Fix: "Install Claude Code, or set auto_summary = false in ~/.config/oat/config.toml."})
		} else {
			add(Result{Name: "Summary", OK: true, Detail: "written after each meeting by " + p + " -p"})
		}
	}

	if p, err := d.FFmpeg(); err != nil {
		add(Result{Name: "ffmpeg", Optional: true, Detail: "not found", Fix: "Install ffmpeg to use oat transcribe: sudo apt install ffmpeg"})
	} else {
		add(Result{Name: "ffmpeg", OK: true, Detail: p})
	}

	if err := d.MCPStatus(ctx); err != nil {
		add(Result{Name: "MCP server", Detail: err.Error(), Fix: "Run: oat setup"})
	} else {
		add(Result{Name: "MCP server", OK: true, Detail: "registered in Claude Code as oat"})
	}

	if err := writable(d.DataDir); err != nil {
		add(Result{Name: "Data folder", Detail: err.Error(), Fix: "Make sure that you can write to " + d.DataDir})
	} else {
		add(Result{Name: "Data folder", OK: true, Detail: d.DataDir})
	}

	switch removed, err := d.Echo.CleanupStale(ctx); {
	case err != nil:
		add(Result{Name: "Echo cancel", Detail: err.Error(), Fix: "Run pactl list modules short, then pactl unload-module for module-echo-cancel with sink_name=oat_ec_sink."})
	case removed:
		add(Result{Name: "Echo cancel", OK: true, Detail: "removed a module that a crash left behind"})
	default:
		add(Result{Name: "Echo cancel", OK: true, Detail: "no module left behind"})
	}
	return rs
}

func groqChecks(ctx context.Context, d Deps) []Result {
	key, source, err := d.GroqKey()
	if err != nil {
		return []Result{{Name: "Groq key", Detail: err.Error(), Fix: "Set GROQ_API_KEY, or add groq_api_key to ~/.config/oat/config.toml and chmod 600 the file."}}
	}
	rs := []Result{{Name: "Groq key", OK: true, Detail: config.MaskKey(key) + " from " + source}}
	if err := d.Ping(ctx, key); err != nil {
		return append(rs, Result{Name: "Groq API", Detail: err.Error(), Fix: "Make sure that the key is valid at console.groq.com and that the network works."})
	}
	return append(rs, Result{Name: "Groq API", OK: true, Detail: "the key works"})
}

func claudeChecks(ctx context.Context, d Deps) []Result {
	now := time.Now
	if d.Now != nil {
		now = d.Now
	}
	expires, source, err := d.ClaudeLogin()
	if err != nil {
		return []Result{{Name: "Claude login", Detail: err.Error(), Fix: "Run claude and log in with your claude.ai account."}}
	}
	if !expires.IsZero() && !now().Add(d.ExpiryMargin).Before(expires) {
		return []Result{{Name: "Claude login", Detail: "the token expired at " + expires.Format("2006-01-02 15:04"), Fix: "Open Claude Code. It renews the token when it starts."}}
	}
	detail := "token from " + source
	if !expires.IsZero() {
		detail = fmt.Sprintf("token valid for %s, from %s", expires.Sub(now()).Round(time.Minute), source)
	}
	rs := []Result{{Name: "Claude login", OK: true, Detail: detail}}
	if err := d.ClaudePing(ctx); err != nil {
		return append(rs, Result{Name: "Claude voice", Detail: err.Error(), Fix: `The voice stream is private and can change in a Claude Code release. To use Groq, set transcriber = "groq" in ~/.config/oat/config.toml.`})
	}
	return append(rs, Result{Name: "Claude voice", OK: true, Detail: "the voice stream accepts the token"})
}

// Print writes the results and returns true when all tests that are not optional passed.
func Print(w io.Writer, rs []Result) bool {
	ok := true
	for _, r := range rs {
		mark := "✓"
		switch {
		case !r.OK && r.Optional:
			mark = "!"
		case !r.OK:
			mark, ok = "✗", false
		}
		fmt.Fprintf(w, "%s %-16s %s\n", mark, r.Name, r.Detail)
		if !r.OK && r.Fix != "" {
			fmt.Fprintf(w, "  %-16s fix: %s\n", "", r.Fix)
		}
	}
	return ok
}

func serverName(info string) string {
	for _, line := range strings.Split(info, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "Server Name: "); ok {
			return v
		}
	}
	return "running"
}

func writable(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".doctor*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}
