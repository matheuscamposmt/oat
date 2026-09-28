package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/matheuscamposmt/oat/internal/chunk"
	"github.com/matheuscamposmt/oat/internal/session"
	"github.com/matheuscamposmt/oat/internal/store"
	"github.com/matheuscamposmt/oat/internal/transcribe"
)

// Recorder is the part of a session that the recording screen uses.
type Recorder interface {
	Meeting() *store.Meeting
	Events() <-chan any
	Started() time.Time
	Pause()
	Resume()
	Paused() bool
	SetLang(string) error
	Lang() string
	Stop()
	Abandon()
	EchoOn() bool
}

const (
	historyLen  = 8
	narrowWidth = 90
	chromeLines = 10 // header 3, meters 1, blank 1, pane title 1, status 1, warning 1, divider 1, keys 1
	notesDelay  = 2 * time.Second
)

var langs = []string{"pt", "en", "auto"}

type recordModel struct {
	theme      Theme
	rec        Recorder
	title      string
	model      string
	segs       []store.Segment
	notes      textarea.Model
	vp         viewport.Model
	spin       spinner.Model
	focusNotes bool
	follow     bool
	levels     map[store.Speaker][]float64
	pending    int
	warn       string
	warnFatal  bool
	echoOn     bool
	confirm    bool
	help       bool
	stopping   bool
	stopped    bool
	stopErr    error
	width      int
	height     int
	now        time.Time
	notesDirty bool
	notesAt    time.Time
}

func newRecordModel(t Theme, rec Recorder) recordModel {
	meta := rec.Meeting().Snapshot()
	notes, _ := rec.Meeting().Notes()
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.Placeholder = "Type your notes here…"
	ta.SetValue(notes)
	ta.Focus()
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	sp.Style = t.style(t.Accent)
	return recordModel{
		theme: t, rec: rec, title: meta.Title, model: meta.Model,
		notes: ta, vp: viewport.New(), spin: sp,
		focusNotes: true, follow: true, levels: map[store.Speaker][]float64{},
		echoOn: rec.EchoOn(), now: time.Now(),
	}
}

func (m recordModel) Init() tea.Cmd {
	return tea.Batch(waitEvent(m.rec.Events()), tick(), m.spin.Tick)
}

func (m recordModel) Update(msg tea.Msg) (recordModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil
	case recEventMsg:
		save := m.handleEvent(msg.ev)
		if m.stopped {
			return m, tea.Batch(save, tea.Tick(4*time.Second, func(time.Time) tea.Msg { return backHomeMsg{} }))
		}
		return m, tea.Batch(save, waitEvent(m.rec.Events()))
	case tickMsg:
		m.now = time.Time(msg)
		var cmd tea.Cmd
		if m.notesDirty && m.now.Sub(m.notesAt) >= notesDelay {
			cmd = m.saveNotes()
		}
		if m.stopped {
			return m, cmd
		}
		return m, tea.Batch(tick(), cmd)
	case notesSavedMsg:
		if msg.err != nil {
			m.warn, m.warnFatal = "Could not save the notes: "+msg.err.Error(), true
		}
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	var cmd tea.Cmd
	m.notes, cmd = m.notes.Update(msg)
	return m, cmd
}

// handleEvent applies a session event. It returns a command that saves the
// notes when the session stopped by itself with unsaved notes, else nil.
func (m *recordModel) handleEvent(ev any) tea.Cmd {
	switch e := ev.(type) {
	case session.LevelEvent:
		h := append(m.levels[e.Speaker], e.DB)
		if len(h) > historyLen {
			h = h[len(h)-historyLen:]
		}
		m.levels[e.Speaker] = h
	case transcribe.SegmentsEvent:
		m.segs = append(m.segs, e.Segments...)
		if !m.warnFatal {
			m.warn = ""
		}
		m.refreshTranscript()
	case transcribe.QueueEvent:
		m.pending = e.Pending
	case transcribe.Warning:
		if e.Fatal || !m.warnFatal {
			m.warn, m.warnFatal = e.Msg, e.Fatal
		}
	case session.EchoEvent:
		m.echoOn = e.On
	case session.StoppedEvent:
		m.stopped, m.stopErr = true, e.Err
		if !m.stopping {
			// The session stopped by itself, for example after a disk write
			// failure. stop() did not run, so save the notes here.
			m.stopping = true
			m.notes.Blur()
			if m.notesDirty {
				return m.saveNotes()
			}
		}
	}
	return nil
}

func (m recordModel) handleKey(k tea.KeyPressMsg) (recordModel, tea.Cmd) {
	key := k.String()
	switch {
	case m.stopped:
		return m, msgCmd(backHomeMsg{})
	case m.stopping:
		if key == "q" || key == "ctrl+c" {
			return m, msgCmd(quitDuringDrain{})
		}
		return m, nil
	case m.confirm:
		switch key {
		case "y", "Y", "enter":
			m.confirm = false
			return m.stop()
		case "n", "N", "esc":
			m.confirm = false
		}
		return m, nil
	}
	switch key {
	case "ctrl+c":
		m.confirm = true
		return m, nil
	case "ctrl+s":
		return m.stop()
	case "ctrl+p":
		if m.rec.Paused() {
			m.rec.Resume()
		} else {
			m.rec.Pause()
		}
		return m, nil
	case "ctrl+l":
		next := langs[0]
		for i, l := range langs {
			if l == m.rec.Lang() {
				next = langs[(i+1)%len(langs)]
			}
		}
		if err := m.rec.SetLang(next); err != nil {
			m.warn = "Could not save the language: " + err.Error()
		}
		return m, nil
	case "tab":
		m.focusNotes = !m.focusNotes
		m.help = false
		if m.focusNotes {
			return m, m.notes.Focus()
		}
		m.notes.Blur()
		return m, nil
	}
	if m.focusNotes {
		before := m.notes.Value()
		var cmd tea.Cmd
		m.notes, cmd = m.notes.Update(k)
		if m.notes.Value() != before {
			m.notesDirty, m.notesAt = true, time.Now()
		}
		return m, cmd
	}
	if m.help {
		if key == "?" || key == "esc" {
			m.help = false
		}
		return m, nil
	}
	switch key {
	case "?":
		m.help = true
		return m, nil
	case "end":
		m.follow = true
		m.vp.GotoBottom()
		return m, nil
	}
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(k)
	m.follow = m.vp.AtBottom()
	return m, cmd
}

func (m recordModel) stop() (recordModel, tea.Cmd) {
	m.stopping = true
	m.notes.Blur()
	m.rec.Stop()
	return m, m.saveNotes()
}

func (m *recordModel) saveNotes() tea.Cmd {
	text := m.notes.Value()
	m.notesDirty = false
	mt := m.rec.Meeting()
	return func() tea.Msg { return notesSavedMsg{err: mt.WriteNotes(text)} }
}

func (m *recordModel) layout() {
	h := max(m.height-chromeLines, 3)
	if m.width < narrowWidth {
		m.vp.SetWidth(max(m.width-4, 10))
		m.notes.SetWidth(max(m.width-4, 10))
	} else {
		tw := m.width*6/10 - 3
		m.vp.SetWidth(tw)
		m.notes.SetWidth(m.width - tw - 7)
	}
	m.vp.SetHeight(h)
	m.notes.SetHeight(h)
	m.refreshTranscript()
}

func (m *recordModel) refreshTranscript() {
	m.vp.SetContent(RenderTranscript(m.theme, store.Clean(m.segs), m.vp.Width()))
	if m.follow {
		m.vp.GotoBottom()
	}
}

func (m recordModel) View() string {
	if m.width == 0 {
		return ""
	}
	t := m.theme
	dim := t.style(t.Dim)
	accent := t.style(t.Accent)

	elapsed := store.Clock(m.now.Sub(m.rec.Started()).Seconds())
	state := accent.Render("● REC " + elapsed)
	switch {
	case m.stopping:
		state = dim.Render("■ STOPPED " + elapsed)
	case m.rec.Paused():
		state = t.style(t.Warn).Render("❚❚ PAUSED " + elapsed)
	}
	echo := "echo off"
	if m.echoOn {
		echo = "echo on"
	}
	info := dim.Render(fmt.Sprintf("%s · %s · %s", m.rec.Lang(), shortModel(m.model), echo))
	header := Box(t, m.width, accent.Bold(true).Render("oat"), state, []string{Spread(m.title, info, m.width-4)})

	meters := "  " + m.meter(store.Me) + "      " + m.meter(store.Them)

	body := m.panes()
	if m.help {
		body = Indent(helpText(t), 2)
	}

	var status string
	switch {
	case m.stopped && m.stopErr != nil:
		status = t.style(t.Err).Render("! Transcription stopped: " + m.stopErr.Error() +
			". The chunks stay on disk, and the next start of oat sends them.")
	case m.stopped:
		status = accent.Render("✓ Saved") + dim.Render(" · In Claude Code: /mcp__oat__enhance")
	case m.stopping:
		status = m.spin.View() + dim.Render(fmt.Sprintf(" Finishing the transcription: %d chunks left", m.pending))
	case m.pending > 0:
		status = m.spin.View() + dim.Render(fmt.Sprintf(" %d chunks transcribing", m.pending))
	}

	// After the stop, the folder path replaces a normal warning. A fatal
	// warning stays.
	warn := ""
	switch {
	case m.warn != "" && (m.warnFatal || !m.stopped):
		c := t.Warn
		if m.warnFatal {
			c = t.Err
		}
		warn = t.style(c).Render("! " + m.warn)
	case m.stopped:
		warn = dim.Render(m.rec.Meeting().Dir)
	}

	keys := "tab focus · ctrl+p pause · ctrl+l lang · ctrl+s stop · ? help"
	switch {
	case m.stopped:
		keys = "any key goes back to the list"
	case m.stopping:
		keys = "q quits now (the next start finishes the transcription)"
	case m.confirm:
		keys = accent.Render("Stop and save? y/n")
	}
	divider := t.style(t.Border).Render(strings.Repeat("─", m.width))
	return lipgloss.JoinVertical(lipgloss.Left,
		header, meters, "", body,
		"  "+ansiTrunc(status, m.width-2), "  "+ansiTrunc(warn, m.width-2),
		divider, "  "+dim.Render(keys))
}

func (m recordModel) meter(sp store.Speaker) string {
	h := m.levels[sp]
	cur := chunk.MinDB
	if len(h) > 0 {
		cur = h[len(h)-1]
	}
	padded := make([]float64, historyLen)
	for i := range padded {
		padded[i] = chunk.MinDB
	}
	copy(padded[historyLen-len(h):], h)
	c := m.theme.style(m.theme.SpeakerColor(sp))
	filled, empty := Bar(cur, 14)
	return c.Bold(true).Width(5).Render(sp.Label()) + " " + c.Render(Spark(padded)) + "  " +
		c.Render(filled) + m.theme.style(m.theme.Dim).Render(empty)
}

func (m recordModel) panes() string {
	t := m.theme
	title := func(s string, focused bool) string {
		if focused {
			return t.style(t.Accent).Bold(true).Render(s)
		}
		return t.style(t.Dim).Render(s)
	}
	var trBody string
	if len(m.segs) == 0 {
		trBody = lipgloss.NewStyle().Width(m.vp.Width()).Height(m.vp.Height()).Render(t.style(t.Dim).Render("Waiting for speech…"))
	} else {
		trBody = m.vp.View()
	}
	tr := lipgloss.JoinVertical(lipgloss.Left, title("Transcript", !m.focusNotes), trBody)
	nt := lipgloss.JoinVertical(lipgloss.Left, title("Notes", m.focusNotes), m.notes.View())
	if m.width < narrowWidth {
		if m.focusNotes {
			return Indent(nt, 2)
		}
		return Indent(tr, 2)
	}
	sep := t.style(t.Border).Render(strings.TrimSuffix(strings.Repeat("│\n", m.vp.Height()+1), "\n"))
	return lipgloss.JoinHorizontal(lipgloss.Top, Indent(tr, 2), " ", sep, " ", nt)
}

func helpText(t Theme) string {
	rows := [][2]string{
		{"tab", "move the focus between the transcript and the notes"},
		{"ctrl+p", "pause or resume"},
		{"ctrl+l", "change the language for the next chunks: pt, en, auto"},
		{"ctrl+s", "stop and save"},
		{"ctrl+c", "stop and save, after a question"},
		{"arrows", "scroll the transcript"},
		{"end", "follow the newest line again"},
		{"?", "close this help"},
	}
	var b strings.Builder
	b.WriteString(t.style(t.Accent).Bold(true).Render("Keys") + "\n\n")
	for _, r := range rows {
		b.WriteString(t.style(t.Accent).Width(9).Render(r[0]) + r[1] + "\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}
