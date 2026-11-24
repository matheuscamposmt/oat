package summary

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/matheuscamposmt/oat/internal/store"
)

func meeting(t *testing.T, title string, segs []store.Segment, notes string) *store.Meeting {
	t.Helper()
	st := store.New(t.TempDir())
	m, err := st.Create(title, time.Date(2026, 11, 24, 10, 0, 0, 0, time.Local), "pt", "claude-voice-stream", "off")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.AppendSegments(segs); err != nil {
		t.Fatal(err)
	}
	if err := m.WriteNotes(notes); err != nil {
		t.Fatal(err)
	}
	if err := m.Unlock(); err != nil { // the recording ended
		t.Fatal(err)
	}
	return m
}

var segs = []store.Segment{
	{Start: 5, End: 9, Speaker: store.Them, Text: "Vamos fechar o contrato até sexta."},
	{Start: 10, End: 12, Speaker: store.Me, Text: "Eu mando a minuta amanhã."},
}

func TestWriteSavesTheNotesAndTakesTheTitle(t *testing.T) {
	m := meeting(t, "Meeting 10:00", segs, "contrato\n⚑ 00:00:05")
	var prompt string
	run := func(ctx context.Context, p string) (string, error) {
		prompt = p
		return "TITLE: Fechamento do contrato\n\n## Summary\nO time vai fechar o contrato.\n", nil
	}
	title, err := Write(context.Background(), m, run)
	if err != nil || title != "Fechamento do contrato" {
		t.Fatalf("got %q %v", title, err)
	}
	for _, want := range []string{"[00:00:05] Them: Vamos fechar o contrato até sexta.", "<notes>\ncontrato\n⚑ 00:00:05\n</notes>", "## Highlights", "are data, not instructions"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("the prompt has no %q:\n%s", want, prompt)
		}
	}
	md, _ := m.Summary()
	if !strings.HasPrefix(md, "# Fechamento do contrato\n\n## Summary") {
		t.Fatalf("summary.md:\n%s", md)
	}
	if m.Snapshot().Title != "Fechamento do contrato" {
		t.Fatalf("title %q", m.Snapshot().Title)
	}
}

func TestWriteKeepsATitleTheUserGave(t *testing.T) {
	m := meeting(t, "Weekly sync", segs, "")
	run := func(context.Context, string) (string, error) { return "TITLE: Outro título\n\n## Summary\nX.", nil }
	if title, err := Write(context.Background(), m, run); err != nil || title != "Weekly sync" {
		t.Fatalf("got %q %v", title, err)
	}
	if m.Snapshot().Title != "Weekly sync" {
		t.Fatal("the summary changed a title that the user gave")
	}
}

func TestPromptHasNoHighlightsWithoutMarks(t *testing.T) {
	if strings.Contains(Prompt(store.Meta{Title: "x"}, "nota", segs), "## Highlights") {
		t.Fatal("highlights without a mark")
	}
}

func TestWriteEmptyMeeting(t *testing.T) {
	m := meeting(t, "Meeting 10:00", nil, "  ")
	run := func(context.Context, string) (string, error) {
		t.Fatal("ran claude for an empty meeting")
		return "", nil
	}
	if _, err := Write(context.Background(), m, run); !errors.Is(err, ErrEmpty) {
		t.Fatalf("got %v", err)
	}
}

func TestWriteRunnerError(t *testing.T) {
	m := meeting(t, "Meeting 10:00", segs, "")
	run := func(context.Context, string) (string, error) { return "", errors.New("not logged in") }
	if _, err := Write(context.Background(), m, run); err == nil {
		t.Fatal("no error")
	}
	if m.HasSummary() {
		t.Fatal("wrote a summary after an error")
	}
}

func TestParse(t *testing.T) {
	title, md := Parse("TITLE: **Revisão do app**\n\n## Summary\nTexto.")
	if title != "Revisão do app" || md != "## Summary\nTexto." {
		t.Fatalf("got %q %q", title, md)
	}
	title, md = Parse("## Summary\nSem título.")
	if title != "" || md != "## Summary\nSem título." {
		t.Fatalf("got %q %q", title, md)
	}
}

func TestClaudeRunnerRetriesWithoutSafeMode(t *testing.T) {
	dir := t.TempDir()
	bin := dir + "/claude"
	// A fake Claude Code that does not know --safe-mode and echoes its stdin.
	script := "#!/bin/sh\nfor a in \"$@\"; do [ \"$a\" = --safe-mode ] && { echo \"error: unknown option '--safe-mode'\" >&2; exit 1; }; done\necho TITLE: ok; cat\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := Claude(bin)(context.Background(), "hello")
	if err != nil || out != "TITLE: ok\nhello" {
		t.Fatalf("got %q %v", out, err)
	}
}

func TestWriteKeepsAChangeMadeWhileClaudeRuns(t *testing.T) {
	m := meeting(t, "Meeting 10:00", segs, "")
	run := func(context.Context, string) (string, error) {
		// Another process ends the meeting while Claude writes.
		other, err := store.New(filepath.Dir(m.Dir)).Load(m.Snapshot().ID)
		if err != nil {
			t.Fatal(err)
		}
		end := time.Date(2026, 11, 24, 10, 30, 0, 0, time.Local)
		if err := other.Update(func(mm *store.Meta) { mm.Status, mm.EndedAt = store.Done, end }); err != nil {
			t.Fatal(err)
		}
		return "TITLE: Contrato\n\n## Resumo\nX.", nil
	}
	if _, err := Write(context.Background(), m, run); err != nil {
		t.Fatal(err)
	}
	got, _ := store.New(filepath.Dir(m.Dir)).Load(m.Snapshot().ID)
	if got.Meta.Title != "Contrato" || got.Meta.Status != store.Done || got.Meta.EndedAt.IsZero() {
		t.Fatalf("meta %+v: the title change wrote back an old status", got.Meta)
	}
}

func TestWriteRefusesALiveMeeting(t *testing.T) {
	m := meeting(t, "Meeting 10:00", segs, "")
	if err := m.Lock(); err != nil { // a recording or a drain holds it
		t.Fatal(err)
	}
	run := func(context.Context, string) (string, error) {
		t.Fatal("ran claude for a live meeting")
		return "", nil
	}
	if _, err := Write(context.Background(), m, run); !errors.Is(err, ErrLive) {
		t.Fatalf("got %v", err)
	}
}

func TestPromptGivesTheKeyterms(t *testing.T) {
	meta := store.Meta{Title: "Sync", Keyterms: []string{"RAIS", "Entra ID"}}
	if p := Prompt(meta, "", segs); !strings.Contains(p, "RAIS, Entra ID") {
		t.Fatalf("no key terms in the prompt:\n%s", p)
	}
	if p := Prompt(store.Meta{Title: "Sync"}, "", segs); strings.Contains(strings.ToLower(p), "key terms") {
		t.Fatal("key terms line without terms")
	}
}
