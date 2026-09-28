package doctor

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

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
		LookPath:  func(s string) (string, error) { return "/usr/bin/" + s, nil },
		Pactl:     pactl,
		GroqKey:   func() (string, string, error) { return "gsk_abcdefgh1234", "/home/u/.claude/settings.local.json", nil },
		Ping:      func(ctx context.Context, key string) error { return nil },
		MCPStatus: func(ctx context.Context) error { return nil },
		DataDir:   filepath.Join(t.TempDir(), "oat"),
		Echo:      &audio.Echo{Pactl: pactl, StatePath: filepath.Join(t.TempDir(), "echo.json")},
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
	d.MCPStatus = func(ctx context.Context) error { return errors.New("oat is not registered") }
	var buf bytes.Buffer
	if Print(&buf, Run(context.Background(), d)) {
		t.Fatal("Print reported success")
	}
	out := buf.String()
	for _, want := range []string{"✗ parec", "sudo apt install pulseaudio-utils", "✗ Groq API", "✗ MCP server", "fix: Run: oat setup"} {
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
	r := &runLog{fail: map[string]bool{"claude mcp get oat": true}}
	var buf bytes.Buffer
	err := Setup(context.Background(), r.run, func(string) (string, error) { return "/usr/bin/claude", nil }, "/home/u/.local/bin/oat", &buf)
	if err != nil {
		t.Fatal(err)
	}
	want := "claude mcp add --scope user oat -- /home/u/.local/bin/oat mcp"
	if len(r.calls) != 2 || r.calls[1] != want {
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
