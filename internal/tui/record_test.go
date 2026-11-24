package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/matheuscamposmt/oat/internal/session"
	"github.com/matheuscamposmt/oat/internal/store"
	"github.com/matheuscamposmt/oat/internal/transcribe"
)

type fakeRecorder struct {
	m         *store.Meeting
	events    chan any
	paused    bool
	lang      string
	stopped   bool
	abandoned bool
}

func newFakeRecorder(t *testing.T) *fakeRecorder {
	t.Helper()
	m, err := store.New(t.TempDir()).Create("Weekly sync", time.Now(), "pt", "whisper-large-v3-turbo", "auto")
	if err != nil {
		t.Fatal(err)
	}
	return &fakeRecorder{m: m, events: make(chan any, 16), lang: "pt"}
}

func (f *fakeRecorder) Meeting() *store.Meeting { return f.m }
func (f *fakeRecorder) Events() <-chan any      { return f.events }
func (f *fakeRecorder) Started() time.Time      { return time.Now().Add(-754 * time.Second) }
func (f *fakeRecorder) Pause()                  { f.paused = true }
func (f *fakeRecorder) Resume()                 { f.paused = false }
func (f *fakeRecorder) Paused() bool            { return f.paused }
func (f *fakeRecorder) SetLang(l string) error  { f.lang = l; return nil }
func (f *fakeRecorder) Lang() string            { return f.lang }
func (f *fakeRecorder) Stop()                   { f.stopped = true }
func (f *fakeRecorder) Abandon()                { f.abandoned = true }
func (f *fakeRecorder) EchoOn() bool            { return true }

func key(s string) tea.KeyPressMsg {
	switch s {
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	}
	if c, ok := strings.CutPrefix(s, "ctrl+"); ok {
		return tea.KeyPressMsg{Code: rune(c[0]), Mod: tea.ModCtrl}
	}
	return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
}

func recording(t *testing.T, width int) (recordModel, *fakeRecorder) {
	f := newFakeRecorder(t)
	m := newRecordModel(theme, f)
	m, _ = m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
	return m, f
}

func TestRecordShowsSegmentsAndHeader(t *testing.T) {
	m, _ := recording(t, 120)
	m, _ = m.Update(recEventMsg{transcribe.SegmentsEvent{Segments: []store.Segment{{Start: 725, End: 727, Speaker: store.Them, Text: "Então, sobre o prazo"}}}})
	m, _ = m.Update(recEventMsg{session.LevelEvent{Speaker: store.Me, DB: -20}})
	view := ansi.Strip(m.View())
	for _, want := range []string{"oat", "● REC 00:12:3", "Weekly sync", "pt · turbo · echo on", "00:12:05", "Them", "Então, sobre o prazo", "Transcript", "Notes"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q in:\n%s", want, view)
		}
	}
	for i, line := range strings.Split(m.View(), "\n") {
		if w := ansi.StringWidth(line); w > 120 {
			t.Fatalf("line %d is %d cells wide", i, w)
		}
	}
}

func TestRecordPauseAndLanguage(t *testing.T) {
	m, f := recording(t, 120)
	m, _ = m.Update(key("ctrl+p"))
	if !f.paused || !strings.Contains(ansi.Strip(m.View()), "PAUSED") {
		t.Fatal("ctrl+p did not pause")
	}
	m, _ = m.Update(key("ctrl+p"))
	if f.paused {
		t.Fatal("ctrl+p did not resume")
	}
	for _, want := range []string{"en", "auto", "pt"} {
		m, _ = m.Update(key("ctrl+l"))
		if f.lang != want {
			t.Fatalf("lang = %q, want %q", f.lang, want)
		}
	}
}

func TestRecordCtrlCAsksFirst(t *testing.T) {
	m, f := recording(t, 120)
	m, _ = m.Update(key("ctrl+c"))
	if !strings.Contains(ansi.Strip(m.View()), "Stop and save? y/n") {
		t.Fatal("no question")
	}
	m, _ = m.Update(key("n"))
	if f.stopped {
		t.Fatal("n stopped the recording")
	}
	m, _ = m.Update(key("ctrl+c"))
	m, _ = m.Update(key("y"))
	if !f.stopped || !m.stopping {
		t.Fatal("y did not stop the recording")
	}
}

func TestRecordTypingGoesToNotes(t *testing.T) {
	m, f := recording(t, 120)
	for _, k := range []string{"a", "b", "?"} {
		m, _ = m.Update(key(k))
	}
	if m.notes.Value() != "ab?" || !m.notesDirty || m.help {
		t.Fatalf("notes %q, dirty %v, help %v", m.notes.Value(), m.notesDirty, m.help)
	}
	m, cmd := m.Update(key("ctrl+s"))
	msg := cmd()
	if _, ok := msg.(notesSavedMsg); !ok {
		t.Fatalf("ctrl+s gave %T, want notesSavedMsg", msg)
	}
	if notes, _ := f.m.Notes(); notes != "ab?" {
		t.Fatalf("notes.md = %q", notes)
	}
	_ = m
}

func TestRecordHelpInTranscriptPane(t *testing.T) {
	m, _ := recording(t, 120)
	m, _ = m.Update(key("tab"))
	m, _ = m.Update(key("?"))
	if !strings.Contains(ansi.Strip(m.View()), "change the language") {
		t.Fatal("no help")
	}
	m, _ = m.Update(key("?"))
	if m.help {
		t.Fatal("? did not close the help")
	}
}

func TestRecordNarrowShowsOnePane(t *testing.T) {
	m, _ := recording(t, 80)
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "Notes") || strings.Contains(view, "Transcript") {
		t.Fatalf("narrow view with notes focus:\n%s", view)
	}
	m, _ = m.Update(key("tab"))
	view = ansi.Strip(m.View())
	if strings.Contains(view, "Notes") || !strings.Contains(view, "Transcript") {
		t.Fatalf("narrow view with transcript focus:\n%s", view)
	}
}

func TestRecordStoppedShowsHint(t *testing.T) {
	m, _ := recording(t, 120)
	m, _ = m.Update(key("ctrl+s"))
	m, _ = m.Update(recEventMsg{transcribe.QueueEvent{Pending: 2}})
	if !strings.Contains(ansi.Strip(m.View()), "2 chunks left") {
		t.Fatal("no drain progress")
	}
	m, _ = m.Update(recEventMsg{session.StoppedEvent{}})
	if !strings.Contains(ansi.Strip(m.View()), "/mcp__oat__enhance") {
		t.Fatal("no hint after the stop")
	}
	_, cmd := m.Update(key("x"))
	if msg, ok := cmd().(backHomeMsg); !ok || msg.from != recordScreen {
		t.Fatal("a key after the stop did not go back home from the recording screen")
	}
}

func TestRecordFatalWarningStays(t *testing.T) {
	m, _ := recording(t, 120)
	m, _ = m.Update(recEventMsg{transcribe.Warning{Fatal: true, Msg: "Groq rejected the API key"}})
	m, _ = m.Update(recEventMsg{transcribe.Warning{Msg: "Groq rate limit"}})
	if !strings.Contains(ansi.Strip(m.View()), "Groq rejected the API key") {
		t.Fatal("a normal warning replaced a fatal one")
	}
}

// savesNotes runs cmd and the commands of each tea.BatchMsg in goroutines, so
// a slow tick does not block. It reports whether one of them gives a
// notesSavedMsg without an error within one second.
func savesNotes(t *testing.T, cmd tea.Cmd) bool {
	t.Helper()
	out := make(chan tea.Msg, 16)
	run := func(c tea.Cmd) {
		if c != nil {
			go func() { out <- c() }()
		}
	}
	run(cmd)
	deadline := time.After(time.Second)
	for {
		select {
		case msg := <-out:
			switch msg := msg.(type) {
			case tea.BatchMsg:
				for _, c := range msg {
					run(c)
				}
			case notesSavedMsg:
				if msg.err != nil {
					t.Fatalf("notes save: %v", msg.err)
				}
				return true
			}
		case <-deadline:
			return false
		}
	}
}

func TestRecordSelfStopSavesNotes(t *testing.T) {
	m, f := recording(t, 120)
	m, _ = m.Update(key("a"))
	m, cmd := m.Update(recEventMsg{session.StoppedEvent{}})
	if !savesNotes(t, cmd) {
		t.Fatal("a stop by the session did not save the notes")
	}
	if notes, _ := f.m.Notes(); notes != "a" {
		t.Fatalf("notes.md = %q, want %q", notes, "a")
	}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "STOPPED") || strings.Contains(view, "● REC") {
		t.Fatalf("header after a stop by the session:\n%s", view)
	}
}

func TestRecordStoppedWithErrorShowsIt(t *testing.T) {
	m, _ := recording(t, 120)
	m, _ = m.Update(key("ctrl+s"))
	m, _ = m.Update(recEventMsg{session.StoppedEvent{Err: errors.New("groq rejected the API key")}})
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "groq rejected the API key") || strings.Contains(view, "✓ Saved") {
		t.Fatalf("view after a failed stop:\n%s", view)
	}
}

func TestRecordStoppedShowsFolderPath(t *testing.T) {
	m, f := recording(t, 120)
	// Make the screen wide enough for the full temp path.
	m, _ = m.Update(tea.WindowSizeMsg{Width: max(120, len(f.m.Dir)+10), Height: 30})
	m, _ = m.Update(key("ctrl+s"))
	m, _ = m.Update(recEventMsg{transcribe.Warning{Msg: "Groq rate limit"}})
	m, _ = m.Update(recEventMsg{session.StoppedEvent{}})
	if view := ansi.Strip(m.View()); !strings.Contains(view, f.m.Dir) {
		t.Fatalf("no folder path %q in:\n%s", f.m.Dir, view)
	}
}

func TestRecordShowsLiveTextAndItsCorrections(t *testing.T) {
	m, _ := recording(t, 120)
	m, _ = m.Update(recEventMsg{session.LiveEvent{ID: 1, Speaker: store.Them, Start: 12, Text: "documentos envio"}})
	view := ansi.Strip(m.View())
	if strings.Contains(view, "Waiting for speech") || !strings.Contains(view, "documentos envio …") {
		t.Fatalf("the first live line is not visible:\n%s", view)
	}
	if !strings.Contains(view, "claude live") && !strings.Contains(view, " live") {
		t.Fatalf("the header does not say live:\n%s", view)
	}
	m, _ = m.Update(recEventMsg{session.LiveEvent{ID: 1, Speaker: store.Them, Start: 12, Text: "documentos em review"}})
	view = ansi.Strip(m.View())
	if strings.Contains(view, "documentos envio") || !strings.Contains(view, "documentos em review …") {
		t.Fatalf("a correction did not replace the live line:\n%s", view)
	}
	m, _ = m.Update(recEventMsg{session.LiveEvent{ID: 1, Speaker: store.Them, Start: 12, Done: true}})
	m, _ = m.Update(recEventMsg{transcribe.SegmentsEvent{Segments: []store.Segment{{Start: 12, End: 15, Speaker: store.Them, Text: "Documentos em review."}}}})
	view = ansi.Strip(m.View())
	if strings.Contains(view, "review …") || !strings.Contains(view, "Documentos em review.") {
		t.Fatalf("the final line did not replace the live one:\n%s", view)
	}
}

func TestRecordMarkGoesToTheNotes(t *testing.T) {
	m, _ := recording(t, 120)
	m, _ = m.Update(key("a"))
	m, _ = m.Update(key("ctrl+b"))
	if v := m.notes.Value(); !strings.HasPrefix(v, "a\n⚑ 00:12:3") {
		t.Fatalf("notes %q", v)
	}
	if !m.notesDirty {
		t.Fatal("the mark is not saved")
	}
}

func TestRecordWritesTheSummaryAfterTheStop(t *testing.T) {
	m, f := recording(t, 120)
	var asked string
	m.summarize = func(id string) error { asked = id; return nil }
	m, _ = m.Update(recEventMsg{transcribe.SegmentsEvent{Segments: []store.Segment{{Start: 1, End: 2, Speaker: store.Me, Text: "oi"}}}})
	m, _ = m.Update(key("ctrl+s"))
	m, cmd := m.Update(recEventMsg{session.StoppedEvent{}})
	if !strings.Contains(ansi.Strip(m.View()), "Writing the summary") {
		t.Fatalf("no summary progress:\n%s", ansi.Strip(m.View()))
	}
	var done summaryDoneMsg
	out := make(chan tea.Msg, 16)
	var run func(c tea.Cmd)
	run = func(c tea.Cmd) {
		if c != nil {
			go func() { out <- c() }()
		}
	}
	run(cmd)
	deadline := time.After(2 * time.Second)
	for done.id == "" {
		select {
		case msg := <-out:
			switch msg := msg.(type) {
			case tea.BatchMsg:
				for _, c := range msg {
					run(c)
				}
			case summaryDoneMsg:
				done = msg
			}
		case <-deadline:
			t.Fatal("no summaryDoneMsg")
		}
	}
	if asked != f.m.Snapshot().ID {
		t.Fatalf("summarized %q", asked)
	}
	m, _ = m.Update(done)
	if !strings.Contains(ansi.Strip(m.View()), "Summary ready") {
		t.Fatal("no ready status")
	}
	_, cmd = m.Update(key("enter"))
	if msg, ok := cmd().(openMeetingMsg); !ok || msg.id != asked {
		t.Fatal("enter did not open the meeting")
	}
}

func TestNoLineWiderThanTheScreen(t *testing.T) {
	for _, w := range []int{60, 80} {
		m, _ := recording(t, w)
		m, _ = m.Update(tea.WindowSizeMsg{Width: w, Height: 24})
		m, _ = m.Update(recEventMsg{session.LiveStateEvent{State: session.StateNoLogin}})
		m, _ = m.Update(recEventMsg{session.LiveEvent{ID: 1, Speaker: store.Me, Start: 3, Text: "uma frase longa que passa da largura da tela e precisa quebrar a linha"}})
		for i, l := range strings.Split(m.View(), "\n") {
			if lw := lipgloss.Width(l); lw > w {
				t.Errorf("recording at %d columns: line %d has %d cells: %q", w, i, lw, ansi.Strip(l))
			}
		}
		st := store.New(t.TempDir())
		mt, _ := st.Create("Weekly", time.Now(), "pt", "m", "auto")
		mt.WriteSummary("# Resumo")
		mt.Unlock()
		v, err := newViewerModel(theme, st, mt.Meta.ID)
		if err != nil {
			t.Fatal(err)
		}
		v.summarize = func(string) error { return nil }
		v, _ = v.Update(tea.WindowSizeMsg{Width: w, Height: 24})
		h := newHomeModel(theme, st)
		h.summarize = func(string) error { return nil }
		h, _ = h.Update(tea.WindowSizeMsg{Width: w, Height: 24})
		h, _ = h.Update(loadRows(st)())
		for i, l := range strings.Split(h.View(), "\n") {
			if lw := lipgloss.Width(l); lw > w {
				t.Errorf("list at %d columns: line %d has %d cells: %q", w, i, lw, ansi.Strip(l))
			}
		}
		for i, l := range strings.Split(v.View(), "\n") {
			if lw := lipgloss.Width(l); lw > w {
				t.Errorf("viewer at %d columns: line %d has %d cells: %q", w, i, lw, ansi.Strip(l))
			}
		}
	}
}

func TestHeaderShowsTheLiveState(t *testing.T) {
	m, _ := recording(t, 120)
	m, _ = m.Update(recEventMsg{session.LiveStateEvent{State: session.StateLive}})
	if !strings.Contains(ansi.Strip(m.View()), " live · ") {
		t.Fatal("no live state")
	}
	m, _ = m.Update(recEventMsg{session.LiveStateEvent{State: session.StateReplay}})
	if v := ansi.Strip(m.View()); !strings.Contains(v, " replay · ") || strings.Contains(v, " live · ") {
		t.Fatalf("no replay state:\n%s", v)
	}
}
