package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadMissingFileGivesDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "none.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg != Default() {
		t.Fatalf("got %+v, want %+v", cfg, Default())
	}
}

func TestLoadFileOverridesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("lang = \"en\"\nkeep_audio = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := Config{Lang: "en", Model: "whisper-large-v3-turbo", Echo: "auto", KeepAudio: true}
	if cfg != want {
		t.Fatalf("got %+v, want %+v", cfg, want)
	}
}

func TestLoadBadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("lang = \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("want an error for a broken file")
	}
}

func TestApplyFlags(t *testing.T) {
	got := Default().Apply(Overrides{Lang: "en", Echo: "off"})
	want := Config{Lang: "en", Model: "whisper-large-v3-turbo", Echo: "off"}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestValidate(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (Config{Lang: "pt", Model: "m", Echo: "maybe"}).Validate(); err == nil {
		t.Fatal("want an error for echo = maybe")
	}
}

func TestDefaultPaths(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "/tmp/state")
	t.Setenv("XDG_CONFIG_HOME", "")
	p, err := DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	want := Paths{Data: "/home/u/.local/share/oat", State: "/tmp/state/oat", Config: "/home/u/.config/oat/config.toml"}
	if p != want {
		t.Fatalf("got %+v, want %+v", p, want)
	}
	if p.Meetings() != "/home/u/.local/share/oat/meetings" || p.EchoState() != "/tmp/state/oat/echo.json" {
		t.Fatalf("bad derived paths: %s %s", p.Meetings(), p.EchoState())
	}
}

func writeSettings(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGroqKeyOrder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("GROQ_API_KEY", "")
	claude := filepath.Join(home, ".claude")

	writeSettings(t, claude, "settings.json", `{"env":{"GROQ_API_KEY":"gsk_from_settings"}}`)
	key, src, err := GroqKey()
	if err != nil || key != "gsk_from_settings" || !strings.HasSuffix(src, "settings.json") {
		t.Fatalf("settings.json: got %q %q %v", key, src, err)
	}

	writeSettings(t, claude, "settings.local.json", `{"env":{"GROQ_API_KEY":"gsk_from_local","OTHER":1}}`)
	key, src, err = GroqKey()
	if err != nil || key != "gsk_from_local" || !strings.HasSuffix(src, "settings.local.json") {
		t.Fatalf("settings.local.json: got %q %q %v", key, src, err)
	}

	t.Setenv("GROQ_API_KEY", "gsk_from_env")
	key, src, err = GroqKey()
	if err != nil || key != "gsk_from_env" || src != "environment variable GROQ_API_KEY" {
		t.Fatalf("environment: got %q %q %v", key, src, err)
	}
}

func TestGroqKeyClaudeConfigDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GROQ_API_KEY", "")
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	writeSettings(t, dir, "settings.json", `{"env":{"GROQ_API_KEY":"gsk_custom_dir"}}`)
	key, _, err := GroqKey()
	if err != nil || key != "gsk_custom_dir" {
		t.Fatalf("got %q %v", key, err)
	}
}

func TestGroqKeyMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("GROQ_API_KEY", "")
	if _, _, err := GroqKey(); !errors.Is(err, ErrNoKey) {
		t.Fatalf("got %v, want ErrNoKey", err)
	}
}

func TestMaskKey(t *testing.T) {
	if got := MaskKey("gsk_abcdefgh1234"); got != "gsk_…1234" {
		t.Fatalf("got %q", got)
	}
	if got := MaskKey("short"); got != "*****" {
		t.Fatalf("got %q", got)
	}
}
