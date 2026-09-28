package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/matheuscamposmt/oat/internal/session"
	"github.com/matheuscamposmt/oat/internal/store"
)

type homeRow struct {
	Meta    store.Meta
	Notes   bool
	Summary bool
}

type (
	rowsMsg    struct{ rows []homeRow }
	homeErrMsg struct{ err error }
	drainMsg   session.DrainEvent
)

type homeModel struct {
	theme         Theme
	st            *store.Store
	rows          []homeRow
	cursor        int
	filter        textinput.Model
	filtering     bool
	title         textinput.Model
	naming        bool
	confirmDelete bool
	drain         map[string]int
	err           string
	width         int
	height        int
}

func newHomeModel(t Theme, st *store.Store) homeModel {
	filter := textinput.New()
	filter.Prompt = "/ "
	filter.Placeholder = "filter by title"
	title := textinput.New()
	title.Prompt = "Title: "
	title.Placeholder = "empty for Meeting HH:MM"
	return homeModel{theme: t, st: st, filter: filter, title: title, drain: map[string]int{}}
}

func loadRows(st *store.Store) tea.Cmd {
	return func() tea.Msg {
		ms, err := st.List()
		if err != nil {
			return homeErrMsg{err}
		}
		rows := make([]homeRow, 0, len(ms))
		for _, m := range ms {
			rows = append(rows, homeRow{Meta: m.Meta, Notes: m.HasNotes(), Summary: m.HasSummary()})
		}
		return rowsMsg{rows}
	}
}

func (h homeModel) visible() []homeRow {
	q := store.Fold(strings.TrimSpace(h.filter.Value()))
	if q == "" {
		return h.rows
	}
	var out []homeRow
	for _, r := range h.rows {
		if strings.Contains(store.Fold(r.Meta.Title), q) {
			out = append(out, r)
		}
	}
	return out
}

func (h homeModel) selected() (homeRow, bool) {
	rows := h.visible()
	if h.cursor < 0 || h.cursor >= len(rows) {
		return homeRow{}, false
	}
	return rows[h.cursor], true
}

func (h *homeModel) clampCursor() {
	h.cursor = max(min(h.cursor, len(h.visible())-1), 0)
}

func (h homeModel) Update(msg tea.Msg) (homeModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		h.width, h.height = msg.Width, msg.Height
		h.filter.SetWidth(max(msg.Width-10, 10))
		h.title.SetWidth(max(msg.Width-14, 10))
	case rowsMsg:
		h.rows = msg.rows
		h.clampCursor()
	case homeErrMsg:
		h.err = msg.err.Error()
	case drainMsg:
		if msg.Done {
			delete(h.drain, msg.ID)
			return h, loadRows(h.st)
		}
		h.drain[msg.ID] = msg.Pending
	case tea.KeyPressMsg:
		return h.key(msg)
	}
	return h, nil
}

func (h homeModel) key(k tea.KeyPressMsg) (homeModel, tea.Cmd) {
	key := k.String()
	switch {
	case h.naming:
		switch key {
		case "enter":
			title := strings.TrimSpace(h.title.Value())
			h.naming = false
			h.title.Reset()
			h.title.Blur()
			if title == "" {
				title = store.DefaultTitle(time.Now())
			}
			return h, msgCmd(startRecordingMsg{title})
		case "esc":
			h.naming = false
			h.title.Reset()
			h.title.Blur()
			return h, nil
		}
		var cmd tea.Cmd
		h.title, cmd = h.title.Update(k)
		return h, cmd
	case h.filtering:
		switch key {
		case "enter":
			h.filtering = false
			h.filter.Blur()
			return h, nil
		case "esc":
			h.filtering = false
			h.filter.Reset()
			h.filter.Blur()
			h.clampCursor()
			return h, nil
		}
		var cmd tea.Cmd
		h.filter, cmd = h.filter.Update(k)
		h.clampCursor()
		return h, cmd
	case h.confirmDelete:
		h.confirmDelete = false
		r, ok := h.selected()
		if !ok || (key != "y" && key != "Y") {
			return h, nil
		}
		st := h.st
		return h, func() tea.Msg {
			if err := st.Delete(r.Meta.ID); err != nil {
				return homeErrMsg{err}
			}
			return loadRows(st)()
		}
	}
	h.err = ""
	switch key {
	case "n":
		h.naming = true
		return h, h.title.Focus()
	case "/":
		h.filtering = true
		return h, h.filter.Focus()
	case "up", "k":
		h.cursor = max(h.cursor-1, 0)
	case "down", "j":
		h.cursor = min(h.cursor+1, max(len(h.visible())-1, 0))
	case "enter":
		if r, ok := h.selected(); ok {
			return h, msgCmd(openMeetingMsg{r.Meta.ID})
		}
	case "d":
		if _, ok := h.selected(); ok {
			h.confirmDelete = true
		}
	case "q", "ctrl+c":
		return h, tea.Quit
	}
	return h, nil
}

func (h homeModel) View() string {
	if h.width == 0 {
		return ""
	}
	t := h.theme
	dim := t.style(t.Dim)
	accent := t.style(t.Accent)
	count := fmt.Sprintf("%d meetings", len(h.rows))
	header := Box(t, h.width, accent.Bold(true).Render("oat"), dim.Render(count), []string{"Meeting notes for your terminal"})

	rows := h.visible()
	listHeight := max(h.height-8, 1)
	start := max(h.cursor-listHeight+1, 0)
	var lines []string
	if len(rows) == 0 {
		msg := "No meetings yet. Press n to record one."
		if len(h.rows) > 0 {
			msg = "No meeting matches the filter."
		}
		lines = append(lines, "  "+dim.Render(msg))
	}
	for i := start; i < len(rows) && i < start+listHeight; i++ {
		lines = append(lines, h.row(rows[i], i == h.cursor))
	}
	for len(lines) < listHeight {
		lines = append(lines, "")
	}

	var bottom string
	switch {
	case h.naming:
		bottom = "  " + h.title.View()
	case h.filtering:
		bottom = "  " + h.filter.View()
	case h.confirmDelete:
		r, _ := h.selected()
		bottom = "  " + accent.Render(fmt.Sprintf("Delete %q? y/n", r.Meta.Title))
	case h.err != "":
		bottom = "  " + t.style(t.Err).Render("! "+h.err)
	}
	keys := "n new · enter open · / filter · d delete · q quit"
	if h.naming {
		keys = "enter start recording · esc cancel"
	}
	divider := t.style(t.Border).Render(strings.Repeat("─", h.width))
	return lipgloss.JoinVertical(lipgloss.Left, header, "", strings.Join(lines, "\n"), bottom, divider, "  "+dim.Render(keys))
}

func (h homeModel) row(r homeRow, selected bool) string {
	t := h.theme
	dim := t.style(t.Dim)
	cursor := "  "
	title := r.Meta.Title
	if selected {
		cursor = t.style(t.Accent).Render("› ")
		title = t.style(t.Accent).Bold(true).Render(title)
	}
	var mark string
	switch r.Meta.Status {
	case store.Recording:
		mark = t.style(t.Accent).Render("●")
	case store.Processing:
		mark = t.style(t.Warn).Render("⠋")
	case store.Interrupted:
		mark = t.style(t.Warn).Render("!")
	default:
		mark = t.style(t.Me).Render("✓")
	}
	var badges []string
	if n, ok := h.drain[r.Meta.ID]; ok {
		badges = append(badges, fmt.Sprintf("%d chunks left", n))
	}
	if r.Notes {
		badges = append(badges, "notes")
	}
	if r.Summary {
		badges = append(badges, "summary")
	}
	left := cursor + dim.Render(r.Meta.StartedAt.Format("2006-01-02  15:04")) + "  " + title
	right := dim.Render(store.Clock(r.Meta.Duration(time.Now()).Seconds())) + "  " + mark + "  " + dim.Render(fmt.Sprintf("%-22s", strings.Join(badges, " · ")))
	return Spread(left, right, h.width-2)
}
