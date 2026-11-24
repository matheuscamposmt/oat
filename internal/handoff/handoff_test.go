package handoff

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matheuscamposmt/oat/internal/store"
)

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadKeepsTheFileOrder(t *testing.T) {
	path := writeFile(t, `lang = "pt"

[handoff.zeta]
command = ["a", "{file}"]

[keyterms.cvm]
terms = ["RAIS"]

[handoff.alpha]
command = ["b"]
`)
	ts, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 2 || ts[0].Name != "zeta" || ts[1].Name != "alpha" || ts[0].Command[1] != "{file}" {
		t.Fatalf("got %+v", ts)
	}
}

func TestLoadRefusesAnEmptyCommand(t *testing.T) {
	if _, err := Load(writeFile(t, "[handoff.x]\ncommand = []\n")); err == nil || !strings.Contains(err.Error(), `"x" has no command`) {
		t.Fatalf("got %v", err)
	}
	if ts, err := Load(filepath.Join(t.TempDir(), "none.toml")); err != nil || len(ts) != 0 {
		t.Fatalf("a missing file: %v %v", ts, err)
	}
}

func TestPick(t *testing.T) {
	one := []Target{{Name: "techlead", Command: []string{"x"}}}
	two := append(one, Target{Name: "hook", Command: []string{"y"}})
	if _, err := Pick(nil, ""); err == nil || !strings.Contains(err.Error(), "[handoff.NAME]") {
		t.Fatalf("no targets: %v", err)
	}
	if got, err := Pick(one, ""); err != nil || got.Name != "techlead" {
		t.Fatalf("one target: %v %v", got, err)
	}
	if _, err := Pick(two, ""); err == nil || !strings.Contains(err.Error(), "--to techlead or hook") {
		t.Fatalf("two targets: %v", err)
	}
	if got, err := Pick(two, "hook"); err != nil || got.Name != "hook" {
		t.Fatalf("--to hook: %v %v", got, err)
	}
	if _, err := Pick(two, "nope"); err == nil || !strings.Contains(err.Error(), "techlead or hook") {
		t.Fatalf("unknown: %v", err)
	}
}

func meeting(t *testing.T, title string) *store.Meeting {
	t.Helper()
	st := store.New(t.TempDir())
	m, err := st.Create(title, time.Date(2026, 11, 24, 10, 5, 0, 0, time.Local), "pt", "m", "auto")
	if err != nil {
		t.Fatal(err)
	}
	m.Unlock()
	return m
}

func TestPrepareNeedsASummary(t *testing.T) {
	m := meeting(t, "Sync")
	if _, _, err := Prepare(m, Target{Command: []string{"x"}}); err == nil || !strings.Contains(err.Error(), "oat summary "+m.Meta.ID) {
		t.Fatalf("got %v", err)
	}
}

func TestPrepareWritesTheFileAndFillsTheArgs(t *testing.T) {
	m := meeting(t, "Sync {file}")
	m.WriteSummary("## Decisions\n\n- ship it\n")
	file, argv, err := Prepare(m, Target{Command: []string{"send", "--subject", "{title}", "--body-file", "{file}", "@{file}", "{date}|{meeting}"}})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(filepath.Dir(file))
	data, _ := os.ReadFile(file)
	want := "# Sync {file}\n\nDate: 2026-11-24 10:05\nMeeting: " + m.Meta.ID + "\n\n## Decisions\n\n- ship it\n"
	if string(data) != want {
		t.Fatalf("file:\n%q\nwant\n%q", data, want)
	}
	if fi, _ := os.Stat(file); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	got := strings.Join(argv, "\n")
	wantArgv := strings.Join([]string{"send", "--subject", "Sync {file}", "--body-file", file, "@" + file, "2026-11-24 10:05|" + m.Meta.ID}, "\n")
	if got != wantArgv {
		t.Fatalf("argv:\n%s\nwant\n%s", got, wantArgv)
	}
}

// fakeCommand writes a script that saves its args and the file in its first
// arg to dir, prints "sent", and exits with code.
func fakeCommand(t *testing.T, code int) (string, string) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "fake")
	body := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + dir + "/argv'\ncat \"$1\" > '" + dir + "/file'\necho sent\necho 'bad target' >&2\necho 'second line' >&2\nexit " + string(rune('0'+code)) + "\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script, dir
}

func TestSendRunsTheCommandAndRemovesTheFile(t *testing.T) {
	m := meeting(t, "Sync")
	m.WriteSummary("resumo\n")
	script, dir := fakeCommand(t, 0)
	out, err := Send(context.Background(), m, Target{Name: "x", Command: []string{script, "{file}", "{title}"}})
	if err != nil || string(out) != "sent\n" {
		t.Fatalf("got %q %v", out, err)
	}
	argv, _ := os.ReadFile(filepath.Join(dir, "argv"))
	lines := strings.Split(strings.TrimSpace(string(argv)), "\n")
	if len(lines) != 2 || lines[1] != "Sync" {
		t.Fatalf("argv %q", argv)
	}
	if file, _ := os.ReadFile(filepath.Join(dir, "file")); !strings.HasSuffix(string(file), "\n\nresumo\n") {
		t.Fatalf("file %q", file)
	}
	if _, err := os.Stat(lines[0]); !os.IsNotExist(err) {
		t.Fatal("the temp file is still there")
	}
}

func TestSendReturnsTheStderrOnFailure(t *testing.T) {
	m := meeting(t, "Sync")
	m.WriteSummary("resumo\n")
	script, _ := fakeCommand(t, 3)
	out, err := Send(context.Background(), m, Target{Name: "x", Command: []string{script, "{file}"}})
	if err == nil || string(out) != "sent\n" || err.Error() != "bad target\nsecond line" {
		t.Fatalf("got %q %v", out, err)
	}
	if _, err := Send(context.Background(), m, Target{Name: "x", Command: []string{filepath.Join(t.TempDir(), "missing")}}); err == nil {
		t.Fatal("no error for a missing program")
	}
}

func TestSendStopsAfterTheTimeout(t *testing.T) {
	m := meeting(t, "Sync")
	m.WriteSummary("resumo\n")
	old := Timeout
	Timeout = 100 * time.Millisecond
	defer func() { Timeout = old }()
	if _, err := Send(context.Background(), m, Target{Name: "x", Command: []string{"sleep", "5"}}); err == nil || !strings.Contains(err.Error(), "did not end in") {
		t.Fatalf("got %v", err)
	}
}
