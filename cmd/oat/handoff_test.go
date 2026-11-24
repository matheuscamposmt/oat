package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matheuscamposmt/oat/internal/store"
)

// handoffEnv points the config and the data at temp folders, so no test reads
// the real config.toml, and creates a finished meeting with a summary.
func handoffEnv(t *testing.T, summary string) *store.Meeting {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	st := store.New(filepath.Join(os.Getenv("XDG_DATA_HOME"), "oat", "meetings"))
	m, err := st.Create("Sync CVM", time.Date(2026, 11, 24, 10, 5, 0, 0, time.Local), "pt", "m", "auto")
	if err != nil {
		t.Fatal(err)
	}
	m.Update(func(meta *store.Meta) { meta.Status = store.Done })
	m.Unlock()
	if summary != "" {
		m.WriteSummary(summary)
	}
	return m
}

// fakeSend writes a script that saves its args and the file in its first arg,
// prints "sent", and prints "bad target" to stderr when it fails.
func fakeSend(t *testing.T, code string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "send")
	body := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + dir + "/argv'\ncat \"$1\" > '" + dir + "/file'\necho sent\n[ " + code + " = 0 ] || echo 'bad target' >&2\nexit " + code + "\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script, dir
}

func TestHandoffRunsTheCommand(t *testing.T) {
	m := handoffEnv(t, "- ship it\n")
	script, dir := fakeSend(t, "0")
	writeConfig(t, "[handoff.techlead]\ncommand = [\""+script+"\", \"{file}\", \"--subject\", \"{title}\", \"{meeting}\"]\n")
	var out, errOut bytes.Buffer
	if code := run([]string{"handoff"}, &out, &errOut); code != 0 || out.String() != "sent\n" {
		t.Fatalf("got %d %q %q", code, out.String(), errOut.String())
	}
	argv, _ := os.ReadFile(filepath.Join(dir, "argv"))
	if !strings.HasSuffix(string(argv), "\n--subject\nSync CVM\n"+m.Meta.ID+"\n") {
		t.Fatalf("argv %q", argv)
	}
	if file, _ := os.ReadFile(filepath.Join(dir, "file")); !strings.HasPrefix(string(file), "# Sync CVM\n") || !strings.HasSuffix(string(file), "- ship it\n") {
		t.Fatalf("file %q", file)
	}
}

func TestHandoffFailurePrintsTheStderr(t *testing.T) {
	handoffEnv(t, "- ship it\n")
	script, _ := fakeSend(t, "1")
	writeConfig(t, "[handoff.techlead]\ncommand = [\""+script+"\", \"{file}\"]\n")
	var out, errOut bytes.Buffer
	if code := run([]string{"handoff"}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "bad target") {
		t.Fatalf("got %d %q", code, errOut.String())
	}
}

func TestHandoffDryRunRunsNothing(t *testing.T) {
	m := handoffEnv(t, "- ship it\n")
	script, dir := fakeSend(t, "0")
	writeConfig(t, "[handoff.techlead]\ncommand = [\""+script+"\", \"--body-file\", \"{file}\"]\n")
	var out, errOut bytes.Buffer
	// The flag after the ID must count, or the real command would run.
	if code := run([]string{"handoff", m.Meta.ID, "--dry-run"}, &out, &errOut); code != 0 {
		t.Fatalf("got %d %q", code, errOut.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "argv")); !os.IsNotExist(err) {
		t.Fatal("--dry-run ran the command")
	}
	for _, want := range []string{"File: /", "# Sync CVM\n\nDate: 2026-11-24 10:05\nMeeting: " + m.Meta.ID, "- ship it", `Command: ["` + script + `", "--body-file", "/`} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in:\n%s", want, out.String())
		}
	}
}

func TestHandoffErrors(t *testing.T) {
	m := handoffEnv(t, "")
	var out, errOut bytes.Buffer
	if code := run([]string{"handoff"}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "[handoff.NAME]") {
		t.Fatalf("no targets: %d %q", code, errOut.String())
	}
	writeConfig(t, "[handoff.techlead]\ncommand = [\"x\"]\n[handoff.hook]\ncommand = [\"y\"]\n")
	errOut.Reset()
	if code := run([]string{"handoff"}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "--to techlead or hook") {
		t.Fatalf("two targets: %d %q", code, errOut.String())
	}
	errOut.Reset()
	if code := run([]string{"handoff", "--to", "hook"}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "Run oat summary "+m.Meta.ID+" first") {
		t.Fatalf("no summary: %d %q", code, errOut.String())
	}
	errOut.Reset()
	if code := run([]string{"handoff", "a", "b"}, &out, &errOut); code != 2 {
		t.Fatalf("two IDs: %d %q", code, errOut.String())
	}
}
