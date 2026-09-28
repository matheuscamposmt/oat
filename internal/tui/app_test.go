package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/matheuscamposmt/oat/internal/store"
)

func homeWith(t *testing.T) (homeModel, *store.Store) {
	t.Helper()
	st := store.New(t.TempDir())
	for i, title := range []string{"Weekly sync", "Planejamento Q4"} {
		m, err := st.Create(title, time.Date(2026, 9, 22+i, 10, 0, 0, 0, time.Local), "pt", "m", "auto")
		if err != nil {
			t.Fatal(err)
		}
		m.Update(func(meta *store.Meta) { meta.Status = store.Done })
		m.Unlock()
		if i == 0 {
			m.WriteNotes("- prazo")
		}
	}
	h := newHomeModel(theme, st)
	h, _ = h.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	h, _ = h.Update(loadRows(st)())
	return h, st
}

func TestHomeListsMeetings(t *testing.T) {
	h, _ := homeWith(t)
	view := ansi.Strip(h.View())
	for _, want := range []string{"2 meetings", "Planejamento Q4", "Weekly sync", "✓", "notes", "n new"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q in:\n%s", want, view)
		}
	}
	if strings.Index(view, "Planejamento") > strings.Index(view, "Weekly") {
		t.Fatal("the newest meeting is not first")
	}
}

func TestHomeFilter(t *testing.T) {
	h, _ := homeWith(t)
	h, _ = h.Update(key("/"))
	for _, k := range []string{"w", "e", "e", "k"} {
		h, _ = h.Update(key(k))
	}
	if rows := h.visible(); len(rows) != 1 || rows[0].Meta.Title != "Weekly sync" {
		t.Fatalf("visible %+v", rows)
	}
	h, _ = h.Update(key("esc"))
	if len(h.visible()) != 2 {
		t.Fatal("esc did not clear the filter")
	}
}

func TestHomeNewMeeting(t *testing.T) {
	h, _ := homeWith(t)
	h, _ = h.Update(key("n"))
	for _, k := range []string{"S", "y", "n", "c"} {
		h, _ = h.Update(key(k))
	}
	h, cmd := h.Update(key("enter"))
	if msg, ok := cmd().(startRecordingMsg); !ok || msg.title != "Sync" {
		t.Fatalf("got %#v", cmd())
	}
	h, _ = h.Update(key("n"))
	_, cmd = h.Update(key("enter"))
	if msg, ok := cmd().(startRecordingMsg); !ok || !strings.HasPrefix(msg.title, "Meeting ") {
		t.Fatalf("empty title gave %#v", cmd())
	}
}

func TestHomeDeleteAsksFirst(t *testing.T) {
	h, st := homeWith(t)
	h, _ = h.Update(key("d"))
	if !strings.Contains(ansi.Strip(h.View()), `Delete "Planejamento Q4"? y/n`) {
		t.Fatal("no question")
	}
	h, cmd := h.Update(key("y"))
	h, _ = h.Update(cmd())
	if all, _ := st.List(); len(all) != 1 || len(h.rows) != 1 {
		t.Fatalf("meetings left: %d", len(all))
	}
}

func TestViewerShowsAndEditsNotes(t *testing.T) {
	st := store.New(t.TempDir())
	m, _ := st.Create("Weekly", time.Now(), "pt", "m", "auto")
	m.AppendSegments([]store.Segment{{Start: 1, End: 2, Speaker: store.Them, Text: "sobre o prazo"}})
	m.WriteNotes("- prazo")
	m.WriteSummary("# Resumo final")
	m.Unlock()
	v, err := newViewerModel(theme, st, m.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	v, _ = v.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	view := ansi.Strip(v.View())
	if !strings.Contains(view, "sobre o prazo") || !strings.Contains(view, "- prazo") {
		t.Fatalf("view:\n%s", view)
	}
	v, _ = v.Update(key("s"))
	if !strings.Contains(ansi.Strip(v.View()), "Resumo final") {
		t.Fatal("s did not show the summary")
	}
	v, _ = v.Update(key("e"))
	v, _ = v.Update(key("x"))
	v, _ = v.Update(key("esc"))
	if notes, _ := m.Notes(); !strings.HasSuffix(notes, "x") {
		t.Fatalf("notes.md = %q", notes)
	}
	_, cmd := v.Update(key("esc"))
	if _, ok := cmd().(backHomeMsg); !ok {
		t.Fatal("esc did not go back home")
	}
}

func TestAppStartsRecording(t *testing.T) {
	st := store.New(t.TempDir())
	f := newFakeRecorder(t)
	a := newApp(context.Background(), Deps{Store: st, Theme: theme, Start: func(ctx context.Context, title string) (Recorder, error) {
		return f, nil
	}})
	model, _ := a.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	model, cmd := model.Update(startRecordingMsg{"x"})
	model, _ = model.Update(cmd())
	got := model.(app)
	if got.screen != recordScreen || !got.recOn {
		t.Fatalf("screen %v", got.screen)
	}
	if !strings.Contains(ansi.Strip(got.View().Content), "● REC") {
		t.Fatal("no recording screen")
	}
	model, cmd = got.Update(signalMsg{})
	if cmd == nil {
		t.Fatal("no command for the signal")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok || !f.abandoned {
		t.Fatal("a signal did not abandon the recording")
	}
}

func TestAppAsksForALargerTerminal(t *testing.T) {
	a := newApp(context.Background(), Deps{Store: store.New(t.TempDir()), Theme: theme})
	model, _ := a.Update(tea.WindowSizeMsg{Width: 50, Height: 12})
	if view := model.(app).View().Content; !strings.Contains(view, "at least 60×16") {
		t.Fatalf("view %q", view)
	}
}

func TestAppSignalSavesNotes(t *testing.T) {
	st := store.New(t.TempDir())
	f := newFakeRecorder(t)
	a := newApp(context.Background(), Deps{Store: st, Theme: theme, Start: func(ctx context.Context, title string) (Recorder, error) {
		return f, nil
	}})
	model, _ := a.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	model, cmd := model.Update(startRecordingMsg{"x"})
	model, _ = model.Update(cmd())
	model, _ = model.Update(key("x"))
	_, cmd = model.Update(signalMsg{})
	if cmd == nil {
		t.Fatal("no command for the signal")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok || !f.abandoned {
		t.Fatal("a signal did not abandon the recording")
	}
	if notes, _ := f.m.Notes(); notes != "x" {
		t.Fatalf("notes.md = %q, want %q", notes, "x")
	}
}
