package config

import (
	"errors"
	"os"
	"path/filepath"
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

func TestGroqKeyOrder(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "")
	cfg := Config{GroqAPIKey: " gsk_from_file "}
	key, src, err := GroqKey(cfg, "/home/u/.config/oat/config.toml")
	if err != nil || key != "gsk_from_file" || src != "/home/u/.config/oat/config.toml" {
		t.Fatalf("config file: got %q %q %v", key, src, err)
	}

	t.Setenv("GROQ_API_KEY", "gsk_from_env")
	key, src, err = GroqKey(cfg, "/home/u/.config/oat/config.toml")
	if err != nil || key != "gsk_from_env" || src != "environment variable GROQ_API_KEY" {
		t.Fatalf("environment: got %q %q %v", key, src, err)
	}
}

func TestGroqKeyFromConfigFile(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "")
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("lang = \"en\"\ngroq_api_key = \"gsk_in_toml\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	key, src, err := GroqKey(cfg, path)
	if err != nil || key != "gsk_in_toml" || src != path {
		t.Fatalf("got %q %q %v", key, src, err)
	}
}

func TestGroqKeyMissing(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "")
	if _, _, err := GroqKey(Default(), "/none/config.toml"); !errors.Is(err, ErrNoKey) {
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
