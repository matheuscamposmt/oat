package doctor

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matheuscamposmt/oat/internal/audio"
)

func fakePactl(ctx context.Context, name string, args ...string) (string, error) {
	switch strings.Join(args, " ") {
	case "info":
		return "Server String: /run/user/1000/pulse/native\nServer Name: PulseAudio (on PipeWire 1.0.3)\n", nil
	case "get-default-sink":
		return "alsa_output.speaker\n", nil
	case "get-default-source":
		return "alsa_input.mic\n", nil
	}
	return "", nil
}

func deps(t *testing.T) Deps {
	pactl := audio.Pactl{Run: fakePactl}
	return Deps{
		LookPath:    func(s string) (string, error) { return "/usr/bin/" + s, nil },
		Pactl:       pactl,
		Transcriber: "groq",
		GroqKey:     func() (string, string, error) { return "gsk_abcdefgh1234", "/home/u/.config/oat/config.toml", nil },
		Ping:        func(ctx context.Context, key string) error { return nil },
		MCPStatus:   func(ctx context.Context) (MCPState, error) { return MCPState{User: true}, nil },
		FFmpeg:      func() (string, error) { return "/usr/bin/ffmpeg", nil },
		DataDir:     filepath.Join(t.TempDir(), "oat"),
		Echo:        &audio.Echo{Pactl: pactl, StatePath: filepath.Join(t.TempDir(), "echo.json")},
	}
}

func byName(rs []Result) map[string]Result {
	out := map[string]Result{}
	for _, r := range rs {
		out[r.Name] = r
	}
	return out
}

func TestAllPass(t *testing.T) {
	rs := Run(context.Background(), deps(t))
	var buf bytes.Buffer
	if !Print(&buf, rs) {
		t.Fatalf("not all passed:\n%s", buf.String())
	}
	out := buf.String()
	for _, want := range []string{"✓ parec", "PulseAudio (on PipeWire 1.0.3)", "mic alsa_input.mic, output alsa_output.speaker", "gsk_…1234 from", "no module left behind"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "gsk_abcdefgh1234") {
		t.Fatal("printed the full key")
	}
}

func TestMissingKeySkipsPing(t *testing.T) {
	d := deps(t)
	d.GroqKey = func() (string, string, error) { return "", "", errors.New("no Groq API key found") }
	d.Ping = func(ctx context.Context, key string) error { t.Fatal("pinged without a key"); return nil }
	rs := byName(Run(context.Background(), d))
	if rs["Groq key"].OK || rs["Groq key"].Fix == "" {
		t.Fatalf("Groq key result %+v", rs["Groq key"])
	}
	if _, ok := rs["Groq API"]; ok {
		t.Fatal("tested the API without a key")
	}
}

func TestFailuresHaveFixes(t *testing.T) {
	d := deps(t)
	d.LookPath = func(s string) (string, error) {
		if s == "parec" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + s, nil
	}
	d.Ping = func(ctx context.Context, key string) error { return errors.New("groq rejected the API key") }
	d.MCPStatus = func(ctx context.Context) (MCPState, error) { return MCPState{}, nil }
	var buf bytes.Buffer
	if Print(&buf, Run(context.Background(), d)) {
		t.Fatal("Print reported success")
	}
	out := buf.String()
	for _, want := range []string{"✗ parec", "sudo apt install pulseaudio-utils", "✗ Groq API", "✗ MCP server", "fix: Install the oat plugin, or run: oat setup"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

type runLog struct {
	calls []string
	fail  map[string]bool
}

func (r *runLog) run(ctx context.Context, name string, args ...string) (string, error) {
	key := name + " " + strings.Join(args, " ")
	r.calls = append(r.calls, key)
	if r.fail[key] {
		return "", errors.New("exit status 1")
	}
	return "", nil
}

func TestSetupRegisters(t *testing.T) {
	r := &runLog{fail: map[string]bool{"claude mcp get oat": true, "claude mcp get plugin:oat:oat": true}}
	var buf bytes.Buffer
	err := Setup(context.Background(), r.run, func(string) (string, error) { return "/usr/bin/claude", nil }, "/home/u/.local/bin/oat", &buf)
	if err != nil {
		t.Fatal(err)
	}
	want := "claude mcp add --scope user oat -- /home/u/.local/bin/oat mcp"
	if len(r.calls) != 3 || r.calls[2] != want {
		t.Fatalf("calls %v", r.calls)
	}
}

func TestSetupIsIdempotent(t *testing.T) {
	r := &runLog{}
	var buf bytes.Buffer
	Setup(context.Background(), r.run, func(string) (string, error) { return "/usr/bin/claude", nil }, "/bin/oat", &buf)
	if len(r.calls) != 1 || !strings.Contains(buf.String(), "already registered") {
		t.Fatalf("calls %v, out %q", r.calls, buf.String())
	}
}

func TestSetupWithoutClaude(t *testing.T) {
	r := &runLog{}
	var buf bytes.Buffer
	Setup(context.Background(), r.run, func(string) (string, error) { return "", errors.New("not found") }, "/bin/oat", &buf)
	if len(r.calls) != 0 || !strings.Contains(buf.String(), "claude mcp add --scope user oat -- /bin/oat mcp") {
		t.Fatalf("calls %v, out %q", r.calls, buf.String())
	}
}

func claudeDeps(t *testing.T) Deps {
	d := deps(t)
	d.Transcriber = "claude"
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	d.Now = func() time.Time { return now }
	d.ClaudeLogin = func() (time.Time, string, error) {
		return now.Add(5 * time.Hour), "/home/u/.claude/.credentials.json", nil
	}
	d.ClaudePing = func(ctx context.Context) error { return nil }
	d.GroqKey = func() (string, string, error) {
		t.Fatal("read the Groq key with transcriber = claude")
		return "", "", nil
	}
	return d
}

func TestClaudeAllPass(t *testing.T) {
	rs := byName(Run(context.Background(), claudeDeps(t)))
	if r := rs["Claude login"]; !r.OK || !strings.Contains(r.Detail, "5h0m0s") {
		t.Fatalf("Claude login %+v", r)
	}
	if !rs["Claude voice"].OK {
		t.Fatalf("Claude voice %+v", rs["Claude voice"])
	}
	if _, ok := rs["Groq key"]; ok {
		t.Fatal("tested Groq with transcriber = claude")
	}
}

func TestClaudeExpiredSkipsPing(t *testing.T) {
	d := claudeDeps(t)
	d.ClaudeLogin = func() (time.Time, string, error) { return d.Now().Add(-time.Minute), "f", nil }
	d.ClaudePing = func(ctx context.Context) error { t.Fatal("pinged with an expired token"); return nil }
	rs := byName(Run(context.Background(), d))
	if r := rs["Claude login"]; r.OK || !strings.Contains(r.Fix, "Open Claude Code") {
		t.Fatalf("Claude login %+v", r)
	}
}

func TestClaudePingFailureNamesTheFallback(t *testing.T) {
	d := claudeDeps(t)
	d.ClaudePing = func(ctx context.Context) error { return errors.New("claude rejected the credentials: HTTP 401") }
	rs := byName(Run(context.Background(), d))
	if r := rs["Claude voice"]; r.OK || !strings.Contains(r.Fix, `transcriber = "groq"`) {
		t.Fatalf("Claude voice %+v", r)
	}
}

func TestClaudeNoLogin(t *testing.T) {
	d := claudeDeps(t)
	d.ClaudeLogin = func() (time.Time, string, error) { return time.Time{}, "", errors.New("no Claude Code login found") }
	rs := byName(Run(context.Background(), d))
	if r := rs["Claude login"]; r.OK || !strings.Contains(r.Fix, "log in") {
		t.Fatalf("Claude login %+v", r)
	}
	if _, ok := rs["Claude voice"]; ok {
		t.Fatal("pinged without a login")
	}
}

func TestConfigErrorHasItsOwnResult(t *testing.T) {
	d := claudeDeps(t)
	d.ConfigPath = "/home/u/.config/oat/config.toml"
	d.ConfigErr = errors.New(`transcriber must be claude or groq, not "Claude"`)
	rs := byName(Run(context.Background(), d))
	if r := rs["Configuration"]; r.OK || !strings.Contains(r.Fix, "config.toml") {
		t.Fatalf("Configuration %+v", r)
	}
	if !rs["Claude login"].OK {
		t.Fatal("the configuration error showed up as a login error")
	}
}

func TestClaudeTokenInsideTheMargin(t *testing.T) {
	d := claudeDeps(t)
	d.ExpiryMargin = 30 * time.Second
	d.ClaudeLogin = func() (time.Time, string, error) { return d.Now().Add(20 * time.Second), "f", nil }
	d.ClaudePing = func(ctx context.Context) error {
		t.Fatal("pinged with a token that the client does not use")
		return nil
	}
	if r := byName(Run(context.Background(), d))["Claude login"]; r.OK {
		t.Fatalf("Claude login %+v", r)
	}
}

func TestSummaryNeedsClaude(t *testing.T) {
	d := claudeDeps(t)
	d.AutoSummary = true
	if r := byName(Run(context.Background(), d))["Summary"]; !r.OK || !strings.Contains(r.Detail, "/usr/bin/claude -p") {
		t.Fatalf("Summary %+v", r)
	}
	d.LookPath = func(s string) (string, error) {
		if s == "claude" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + s, nil
	}
	if r := byName(Run(context.Background(), d))["Summary"]; r.OK || !strings.Contains(r.Fix, "auto_summary = false") {
		t.Fatalf("Summary %+v", r)
	}
}

func TestFFmpegIsOptional(t *testing.T) {
	d := deps(t)
	if r := byName(Run(context.Background(), d))["ffmpeg"]; !r.OK || r.Detail != "/usr/bin/ffmpeg" {
		t.Fatalf("ffmpeg %+v", r)
	}
	d.FFmpeg = func() (string, error) { return "", errors.New("not found") }
	var buf bytes.Buffer
	if !Print(&buf, Run(context.Background(), d)) {
		t.Fatalf("a missing ffmpeg failed doctor:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "Install ffmpeg to use oat transcribe: sudo apt install ffmpeg") {
		t.Fatalf("no fix in:\n%s", buf.String())
	}
}

func TestSetupSkipsWhenThePluginIsInstalled(t *testing.T) {
	r := &runLog{fail: map[string]bool{"claude mcp get oat": true}}
	var buf bytes.Buffer
	if err := Setup(context.Background(), r.run, func(string) (string, error) { return "/usr/bin/claude", nil }, "/bin/oat", &buf); err != nil {
		t.Fatal(err)
	}
	for _, c := range r.calls {
		if strings.Contains(c, "mcp add") {
			t.Fatalf("calls %v", r.calls)
		}
	}
	if !strings.Contains(buf.String(), "plugin provides") {
		t.Fatalf("out %q", buf.String())
	}
}

// mcpRow runs doctor with the MCP servers in exist known to Claude Code.
func mcpRow(t *testing.T, exist ...string) Result {
	t.Helper()
	r := &runLog{fail: map[string]bool{"claude mcp get oat": true, "claude mcp get plugin:oat:oat": true}}
	for _, name := range exist {
		delete(r.fail, "claude mcp get "+name)
	}
	d := deps(t)
	d.MCPStatus = func(ctx context.Context) (MCPState, error) {
		return MCPStatus(ctx, r.run, func(string) (string, error) { return "/usr/bin/claude", nil })
	}
	return byName(Run(context.Background(), d))["MCP server"]
}

func TestMCPStatusPluginOnly(t *testing.T) {
	if r := mcpRow(t, PluginServer); !r.OK || r.Detail != "provided by the oat plugin" {
		t.Fatalf("%+v", r)
	}
}

func TestMCPStatusUserOnly(t *testing.T) {
	if r := mcpRow(t, "oat"); !r.OK || r.Detail != "registered in Claude Code as oat" {
		t.Fatalf("%+v", r)
	}
}

func TestMCPStatusBoth(t *testing.T) {
	r := mcpRow(t, "oat", PluginServer)
	if r.OK || r.Detail != "registered twice: by oat setup and by the oat plugin" || r.Fix != "claude mcp remove --scope user oat" {
		t.Fatalf("%+v", r)
	}
}

func TestMCPStatusNone(t *testing.T) {
	if r := mcpRow(t); r.OK || r.Fix != "Install the oat plugin, or run: oat setup" {
		t.Fatalf("%+v", r)
	}
}
