package tui

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/matheuscamposmt/oat/internal/store"
)

type viewerModel struct {
	theme       Theme
	m           *store.Meeting
	meta        store.Meta
	segs        []store.Segment
	notesText   string
	summary     string
	left        viewport.Model
	right       viewport.Model
	editor      textarea.Model
	editing     bool
	showSummary bool
	focusRight  bool
	width       int
	height      int
	err         string
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
		theme: t, m: m, meta: m.Meta, segs: store.Clean(segs), notesText: notes, summary: summary,
		left: viewport.New(), right: viewport.New(), editor: ed,
	}, nil
}

func (v viewerModel) Update(msg tea.Msg) (viewerModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width, v.height = msg.Width, msg.Height
		v.layout()
		return v, nil
	case tea.KeyPressMsg:
		return v.key(msg)
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
	if v.err != "" {
		status = t.style(t.Err).Render("! " + v.err)
	}
	keys := "tab focus · e edit notes · s summary · esc back"
	divider := t.style(t.Border).Render(strings.Repeat("─", v.width))
	return lipgloss.JoinVertical(lipgloss.Left, header, "", body, "  "+status, divider, "  "+dim.Render(keys))
}
