package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/matheuscamposmt/oat/internal/store"
)

// handoffViewer opens a meeting with a summary, with a fake handoff that
// records its calls and returns err.
func handoffViewer(t *testing.T, targets []string, summary string, err error) (viewerModel, *[]string) {
	t.Helper()
	st := store.New(t.TempDir())
	m, _ := st.Create("Sync", time.Now(), "pt", "m", "auto")
	m.Unlock()
	if summary != "" {
		m.WriteSummary(summary)
	}
	v, verr := newViewerModel(theme, st, m.Meta.ID)
	if verr != nil {
		t.Fatal(verr)
	}
	var calls []string
	v.targets = targets
	v.handoff = func(id, target string) error {
		calls = append(calls, id+" "+target)
		return err
	}
	v, _ = v.Update(tea.WindowSizeMsg{Width: 140, Height: 30})
	return v, &calls
}

func TestViewerHandoffWithOneTargetAsksFirst(t *testing.T) {
	v, calls := handoffViewer(t, []string{"techlead"}, "# Resumo", nil)
	if !strings.Contains(ansi.Strip(v.View()), "o hand off") {
		t.Fatal("the help line has no o")
	}
	v, _ = v.Update(key("o"))
	if !strings.Contains(ansi.Strip(v.View()), "Hand off to techlead? y/n") {
		t.Fatalf("no question:\n%s", ansi.Strip(v.View()))
	}
	v, cmd := v.Update(key("n"))
	if cmd != nil || len(*calls) != 0 || v.asking {
		t.Fatal("n did not cancel")
	}
	v, _ = v.Update(key("o"))
	v, cmd = v.Update(key("y"))
	if !strings.Contains(ansi.Strip(v.View()), "Handing off to techlead…") {
		t.Fatal("no progress")
	}
	v, _ = v.Update(cmd())
	if len(*calls) != 1 || !strings.HasSuffix((*calls)[0], " techlead") || !strings.Contains(ansi.Strip(v.View()), "Sent to techlead.") {
		t.Fatalf("calls %v, view:\n%s", *calls, ansi.Strip(v.View()))
	}
}

func TestViewerHandoffPicksAmongTargets(t *testing.T) {
	v, calls := handoffViewer(t, []string{"techlead", "hook"}, "# Resumo", errors.New("bad target\nsecond line"))
	v, _ = v.Update(key("o"))
	if !strings.Contains(ansi.Strip(v.View()), "Hand off to: 1 techlead · 2 hook") {
		t.Fatalf("no picker:\n%s", ansi.Strip(v.View()))
	}
	v, cmd := v.Update(key("2"))
	v, _ = v.Update(cmd())
	view := ansi.Strip(v.View())
	if len(*calls) != 1 || !strings.HasSuffix((*calls)[0], " hook") || !strings.Contains(view, "The handoff to hook failed: bad target") || strings.Contains(view, "second line") {
		t.Fatalf("calls %v, view:\n%s", *calls, view)
	}
}

func TestViewerHandoffExplainsWhatIsMissing(t *testing.T) {
	v, _ := handoffViewer(t, nil, "# Resumo", nil)
	v, cmd := v.Update(key("o"))
	if cmd != nil || !strings.Contains(ansi.Strip(v.View()), "Add [handoff.NAME]") {
		t.Fatalf("no hint:\n%s", ansi.Strip(v.View()))
	}
	v, _ = handoffViewer(t, []string{"techlead"}, "", nil)
	v, cmd = v.Update(key("o"))
	if cmd != nil || !strings.Contains(ansi.Strip(v.View()), "No summary to hand off") {
		t.Fatalf("no summary message:\n%s", ansi.Strip(v.View()))
	}
}

func TestViewerIgnoresTheHandoffOfAnotherMeeting(t *testing.T) {
	v, _ := handoffViewer(t, []string{"techlead"}, "# Resumo", nil)
	v, _ = v.Update(handoffDoneMsg{id: "other", target: "techlead"})
	if strings.Contains(ansi.Strip(v.View()), "Sent to") {
		t.Fatal("showed the result of another meeting")
	}
}

func TestAppGivesTheHandoffToTheViewer(t *testing.T) {
	st := store.New(t.TempDir())
	m, _ := st.Create("Sync", time.Now(), "pt", "m", "auto")
	m.Unlock()
	a := newApp(context.Background(), Deps{Store: st, Theme: theme, HandoffTargets: []string{"techlead"}, Handoff: func(string, string) error { return nil }})
	model, _ := a.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	model, _ = model.Update(openMeetingMsg{m.Meta.ID})
	if v := model.(app).view; len(v.targets) != 1 || v.handoff == nil {
		t.Fatal("the viewer has no handoff")
	}
}
