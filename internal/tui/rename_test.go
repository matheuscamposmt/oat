package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/matheuscamposmt/oat/internal/store"
)

func typeText(t *testing.T, update func(tea.KeyPressMsg), s string) {
	t.Helper()
	for _, r := range s {
		update(key(string(r)))
	}
}

func TestHomeRenamesTheSelectedMeeting(t *testing.T) {
	h, st := homeWith(t)
	if !strings.Contains(ansi.Strip(h.View()), "t title") {
		t.Fatal("the help line has no t")
	}
	h, _ = h.Update(key("t"))
	if v := ansi.Strip(h.View()); !strings.Contains(v, "Title: Planejamento Q4") || !strings.Contains(v, "enter save · esc cancel") {
		t.Fatalf("no input with the title:\n%s", v)
	}
	h, _ = h.Update(key("ctrl+u"))
	typeText(t, func(k tea.KeyPressMsg) { h, _ = h.Update(k) }, "Plano q")
	h, cmd := h.Update(key("enter"))
	h, _ = h.Update(cmd())
	if r, _ := h.selected(); r.Meta.Title != "Plano q" {
		t.Fatalf("row title %q", r.Meta.Title)
	}
	m, _ := st.Load(h.rows[0].Meta.ID)
	if m.Meta.Title != "Plano q" {
		t.Fatalf("meta.json title %q", m.Meta.Title)
	}
}

func TestHomeRenameEscAndEmptyKeepTheTitle(t *testing.T) {
	h, st := homeWith(t)
	h, _ = h.Update(key("t"))
	h, _ = h.Update(key("x"))
	h, _ = h.Update(key("esc"))
	h, _ = h.Update(key("t"))
	h, _ = h.Update(key("ctrl+u"))
	h, _ = h.Update(key(" "))
	h, cmd := h.Update(key("enter"))
	if cmd != nil {
		h, _ = h.Update(cmd())
	}
	m, _ := st.Load(h.rows[0].Meta.ID)
	if m.Meta.Title != "Planejamento Q4" || h.rows[0].Meta.Title != "Planejamento Q4" {
		t.Fatalf("title %q", m.Meta.Title)
	}
}

func TestViewerRenamesTheMeeting(t *testing.T) {
	st := store.New(t.TempDir())
	m, _ := st.Create("Meeting 10:00", time.Now(), "pt", "m", "auto")
	m.Unlock()
	v, err := newViewerModel(theme, st, m.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	v, _ = v.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	if !strings.Contains(ansi.Strip(v.View()), "t title") {
		t.Fatal("the help line has no t")
	}
	v, _ = v.Update(key("t"))
	v, _ = v.Update(key("ctrl+u"))
	// q and esc type or cancel; they must not leave the screen.
	v, cmd := v.Update(key("q"))
	if cmd != nil {
		if _, ok := cmd().(backHomeMsg); ok {
			t.Fatal("q left the meeting while renaming")
		}
	}
	typeText(t, func(k tea.KeyPressMsg) { v, _ = v.Update(k) }, "uick sync")
	v, _ = v.Update(key("enter"))
	if !strings.Contains(ansi.Strip(v.View()), "quick sync") {
		t.Fatalf("the header shows the old title:\n%s", ansi.Strip(v.View()))
	}
	if got, _ := st.Load(m.Meta.ID); got.Meta.Title != "quick sync" {
		t.Fatalf("meta.json title %q", got.Meta.Title)
	}
	v, _ = v.Update(key("t"))
	v, cmd = v.Update(key("esc"))
	if cmd != nil || v.renaming {
		t.Fatal("esc did not cancel the rename")
	}
}
