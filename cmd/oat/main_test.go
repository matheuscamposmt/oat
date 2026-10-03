package main

import (
	"bytes"
	"flag"
	"strings"
	"testing"
)

func TestVersionAndHelp(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"version"}, &out, &errOut); code != 0 || !strings.HasPrefix(out.String(), "oat ") {
		t.Fatalf("version: %d %q", code, out.String())
	}
	out.Reset()
	if code := run([]string{"help"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "oat new [title]") {
		t.Fatalf("help: %d %q", code, out.String())
	}
}

func TestUnknownCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"bogus"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), `unknown command "bogus"`) {
		t.Fatalf("got %d %q", code, errOut.String())
	}
}

func TestBadEchoFlag(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out, errOut bytes.Buffer
	if code := run([]string{"new", "--echo", "maybe"}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "echo must be auto, on, or off") {
		t.Fatalf("got %d %q", code, errOut.String())
	}
}

func TestFlagsAfterTheTitle(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out, errOut bytes.Buffer
	if code := run([]string{"new", "Weekly sync", "--echo", "maybe"}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "echo must be auto, on, or off") {
		t.Fatalf("the flag after the title was not parsed: %d %q", code, errOut.String())
	}
}

func TestParseInterspersed(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	lang := fs.String("lang", "", "")
	words, err := parseInterspersed(fs, []string{"Weekly", "--lang", "en", "sync"})
	if err != nil || *lang != "en" || strings.Join(words, " ") != "Weekly sync" {
		t.Fatalf("got %q %q %v", words, *lang, err)
	}
}

func TestMissingKeyStopsBeforeRecording(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GROQ_API_KEY", "")
	var out, errOut bytes.Buffer
	if code := run([]string{"new"}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "no Groq API key found") {
		t.Fatalf("got %d %q", code, errOut.String())
	}
}
