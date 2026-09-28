package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/matheuscamposmt/oat/internal/store"
)

var theme = NewTheme(true)

func TestBoxLinesHaveTheWidth(t *testing.T) {
	box := Box(theme, 50, "oat", "● REC 00:00:01", []string{"short", strings.Repeat("long ", 30)})
	for i, line := range strings.Split(box, "\n") {
		if w := lipgloss.Width(line); w != 50 {
			t.Errorf("line %d has width %d: %q", i, w, ansi.Strip(line))
		}
	}
}

func TestSparkAndBar(t *testing.T) {
	if got := Spark([]float64{-100, -30, 0}); got != "▁▅█" {
		t.Fatalf("Spark = %q", got)
	}
	filled, empty := Bar(-30, 10)
	if filled != strings.Repeat("━", 5) || empty != strings.Repeat("─", 5) {
		t.Fatalf("Bar = %q %q", filled, empty)
	}
}

func TestRenderTranscriptWraps(t *testing.T) {
	segs := []store.Segment{{Start: 725, Speaker: store.Them, Text: strings.Repeat("palavra ", 12)}}
	lines := strings.Split(ansi.Strip(RenderTranscript(theme, segs, 40)), "\n")
	if len(lines) < 3 || !strings.HasPrefix(lines[0], "00:12:05  Them  palavra") {
		t.Fatalf("lines %q", lines)
	}
	for _, l := range lines[1:] {
		if !strings.HasPrefix(l, strings.Repeat(" ", prefixWidth)+"palavra") {
			t.Fatalf("no hanging indent: %q", l)
		}
	}
}

func TestSpread(t *testing.T) {
	got := Spread("left", "right", 20)
	if lipgloss.Width(got) != 20 || !strings.HasPrefix(got, "left") || !strings.HasSuffix(got, "right") {
		t.Fatalf("Spread = %q", got)
	}
}
