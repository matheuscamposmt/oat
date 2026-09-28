package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
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
	if _, ok := cmd().(backHomeMsg); !ok {
		t.Fatal("a key after the stop did not go back home")
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
