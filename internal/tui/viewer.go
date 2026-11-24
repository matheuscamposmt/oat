package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/matheuscamposmt/oat/internal/store"
)

type viewerModel struct {
	theme       Theme
	st          *store.Store
	m           *store.Meeting
	meta        store.Meta
	segs        []store.Segment
	notesText   string
	summary     string
	left        viewport.Model
	right       viewport.Model
	editor      textarea.Model
	editing     bool
	rename      textinput.Model
	renaming    bool
	showSummary bool
	focusRight  bool
	width       int
	height      int
	err         string
	note        string                // a short message, for example after a copy
	summarize   func(id string) error // nil turns the summary off
	writing     bool
	handoff     func(id, target string) error // see Deps.Handoff
	targets     []string
	asking      bool   // o asks y/n, or shows the targets
	handingTo   string // the target while the handoff runs
}

func newViewerModel(t Theme, st *store.Store, id string) (viewerModel, error) {
	m, err := st.Load(id)
	if err != nil {
		return viewerModel{}, err
	}
	segs, err := m.Segments()
	if err != nil {
		return viewerModel{}, err
	}
	notes, _ := m.Notes()
	summary, _ := m.Summary()
	ed := textarea.New()
	ed.ShowLineNumbers = false
	ed.Prompt = ""
	return viewerModel{
		theme: t, st: st, m: m, meta: m.Meta, segs: store.Clean(segs), notesText: notes, summary: summary,
		left: viewport.New(), right: viewport.New(), editor: ed, rename: newRenameInput(),
		showSummary: strings.TrimSpace(summary) != "", // the summary is what people open a meeting for
	}, nil
}

func (v viewerModel) Update(msg tea.Msg) (viewerModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width, v.height = msg.Width, msg.Height
		v.rename.SetWidth(max(msg.Width-14, 10))
		v.layout()
		return v, nil
	case tea.KeyPressMsg:
		return v.key(msg)
	case summaryDoneMsg:
		if msg.id != v.meta.ID {
			return v, nil
		}
		v.writing = false
		if msg.err != nil {
			v.err = "The summary failed: " + msg.err.Error()
			return v, nil
		}
		if m, err := v.st.Load(v.meta.ID); err == nil {
			v.m, v.meta = m, m.Meta
		}
		v.summary, _ = v.m.Summary()
		v.showSummary, v.note = true, "Summary saved."
		v.layout()
		return v, nil
	case handoffDoneMsg:
		if msg.id != v.meta.ID {
			return v, nil
		}
		v.handingTo = ""
		if msg.err != nil {
			first, _, _ := strings.Cut(msg.err.Error(), "\n")
			v.err = "The handoff to " + msg.target + " failed: " + first
			return v, nil
		}
		v.note = "Sent to " + msg.target + "."
		return v, nil
	}
	if v.editing {
		var cmd tea.Cmd
		v.editor, cmd = v.editor.Update(msg)
		return v, cmd
	}
	return v, nil
}

func (v viewerModel) key(k tea.KeyPressMsg) (viewerModel, tea.Cmd) {
	key := k.String()
	if v.renaming {
		switch key {
		case "enter", "esc":
			v.renaming = false
			v.rename.Blur()
			if key == "esc" {
				return v, nil
			}
			if err := v.st.Rename(v.meta.ID, v.rename.Value()); err != nil {
				v.err = "Could not rename the meeting: " + err.Error()
				return v, nil
			}
			if m, err := v.st.Load(v.meta.ID); err == nil {
				v.m, v.meta = m, m.Meta
			}
			return v, nil
		}
		var cmd tea.Cmd
		v.rename, cmd = v.rename.Update(k)
		return v, cmd
	}
	if v.asking {
		v.asking = false
		target := ""
		if len(v.targets) == 1 && (key == "y" || key == "Y") {
			target = v.targets[0]
		}
		if len(v.targets) > 1 && len(key) == 1 && key[0] >= '1' && int(key[0]-'0') <= len(v.targets) {
			target = v.targets[key[0]-'1']
		}
		if target == "" {
			return v, nil
		}
		v.handingTo = target
		fn, id := v.handoff, v.meta.ID
		return v, func() tea.Msg { return handoffDoneMsg{id: id, target: target, err: fn(id, target)} }
	}
	if v.editing {
		if key == "esc" {
			v.editing = false
			v.editor.Blur()
			v.notesText = v.editor.Value()
			if err := v.m.WriteNotes(v.notesText); err != nil {
				v.err = "Could not save the notes: " + err.Error()
			}
			v.layout()
			return v, nil
		}
		var cmd tea.Cmd
		v.editor, cmd = v.editor.Update(k)
		return v, cmd
	}
	switch key {
	case "esc", "q":
		return v, msgCmd(backHomeMsg{from: viewerScreen})
	case "tab":
		v.focusRight = !v.focusRight
		return v, nil
	case "t":
		v.renaming, v.err, v.note = true, "", ""
		v.rename.SetValue(v.meta.Title)
		return v, v.rename.Focus()
	case "e":
		v.editing, v.showSummary = true, false
		v.editor.SetValue(v.notesText)
		v.layout()
		return v, v.editor.Focus()
	case "s":
		if strings.TrimSpace(v.summary) != "" {
			v.showSummary = !v.showSummary
			v.layout()
		}
		return v, nil
	case "r":
		if v.summarize == nil || v.writing {
			return v, nil
		}
		v.writing, v.err, v.note = true, "", ""
		return v, summarizeCmd(v.summarize, v.meta.ID)
	case "o":
		v.err, v.note = "", ""
		switch {
		case v.handingTo != "":
		case v.handoff == nil || len(v.targets) == 0:
			v.err = "No handoff target. Add [handoff.NAME] with a command to config.toml."
		case strings.TrimSpace(v.summary) == "":
			v.err = "No summary to hand off. Write it first with r or oat summary."
		default:
			v.asking = true
		}
		return v, nil
	case "y":
		text := v.notesText
		what := "notes"
		if v.showSummary {
			text, what = v.summary, "summary"
		}
		if strings.TrimSpace(text) == "" {
			return v, nil
		}
		v.note = "Copied the " + what + "."
		return v, tea.SetClipboard(text)
	}
	var cmd tea.Cmd
	if v.focusRight {
		v.right, cmd = v.right.Update(k)
	} else {
		v.left, cmd = v.left.Update(k)
	}
	return v, cmd
}

func (v *viewerModel) layout() {
	h := max(v.height-8, 3)
	lw := v.width*6/10 - 3
	rw := v.width - lw - 7
	v.left.SetWidth(lw)
	v.left.SetHeight(h)
	v.right.SetWidth(rw)
	v.right.SetHeight(h)
	v.editor.SetWidth(rw)
	v.editor.SetHeight(h)
	if len(v.segs) == 0 {
		v.left.SetContent(v.theme.style(v.theme.Dim).Render("No transcript."))
	} else {
		v.left.SetContent(RenderTranscript(v.theme, v.segs, lw))
	}
	right := v.notesText
	if v.showSummary {
		right = v.summary
	}
	if strings.TrimSpace(right) == "" {
		right = v.theme.style(v.theme.Dim).Render("No notes. Press e to write some.")
	}
	v.right.SetContent(lipgloss.NewStyle().Width(rw).Render(right))
}

func (v viewerModel) View() string {
	if v.width == 0 {
		return ""
	}
	t := v.theme
	dim := t.style(t.Dim)
	accent := t.style(t.Accent)
	info := v.meta.StartedAt.Format("2006-01-02 15:04") + " · " + store.Clock(v.meta.Duration(time.Now()).Seconds()) + " · " + string(v.meta.Status)
	header := Box(t, v.width, accent.Bold(true).Render("oat"), dim.Render(info), []string{v.meta.Title})
	title := func(s string, focused bool) string {
		if focused {
			return accent.Bold(true).Render(s)
		}
		return dim.Render(s)
	}
	rightTitle, rightBody := "Notes", v.right.View()
	switch {
	case v.editing:
		rightTitle, rightBody = "Notes (editing, esc saves)", v.editor.View()
	case v.showSummary:
		rightTitle = "Summary"
	}
	left := lipgloss.JoinVertical(lipgloss.Left, title("Transcript", !v.focusRight && !v.editing), v.left.View())
	right := lipgloss.JoinVertical(lipgloss.Left, title(rightTitle, v.focusRight || v.editing), rightBody)
	sep := t.style(t.Border).Render(strings.TrimSuffix(strings.Repeat("│\n", v.left.Height()+1), "\n"))
	body := lipgloss.JoinHorizontal(lipgloss.Top, Indent(left, 2), " ", sep, " ", right)
	status := ""
	switch {
	case v.renaming:
		status = v.rename.View()
	case v.asking && len(v.targets) == 1:
		status = accent.Render("Hand off to " + v.targets[0] + "? y/n")
	case v.asking:
		var opts []string
		for i, name := range v.targets {
			opts = append(opts, fmt.Sprintf("%d %s", i+1, name))
		}
		status = accent.Render("Hand off to: " + strings.Join(opts, " · ") + " · esc cancel")
	case v.handingTo != "":
		status = dim.Render("Handing off to " + v.handingTo + "…")
	case v.err != "":
		status = t.style(t.Err).Render("! " + v.err)
	case v.writing:
		status = dim.Render("Writing the summary with Claude…")
	case v.note != "":
		status = accent.Render("✓ " + v.note)
	}
	keys := "esc back · s summary/notes · y copy · r rewrite · e edit · t title · o hand off · tab"
	if v.summarize == nil {
		keys = "esc back · s summary/notes · y copy · e edit · t title · o hand off · tab"
	}
	if v.renaming {
		keys = "enter save · esc cancel"
	}
	divider := t.style(t.Border).Render(strings.Repeat("─", v.width))
	return lipgloss.JoinVertical(lipgloss.Left, header, "", body, "  "+ansiTrunc(status, v.width-2), divider, "  "+ansiTrunc(dim.Render(keys), v.width-2))
}
