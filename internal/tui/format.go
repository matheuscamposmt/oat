package tui

import (
	"math"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/matheuscamposmt/oat/internal/store"
)

// Box draws a rounded box of the given outer width, with a title on the left
// and a label on the right of the top border.
func Box(t Theme, width int, left, right string, lines []string) string {
	border := t.style(t.Border)
	inner := max(width-4, 1)
	head := "─ " + left + " "
	tail := ""
	if right != "" {
		tail = " " + right + " ─"
	}
	fill := max(width-2-lipgloss.Width(head)-lipgloss.Width(tail), 1)
	var b strings.Builder
	b.WriteString(border.Render("╭─ ") + left + border.Render(" "+strings.Repeat("─", fill)))
	if right != "" {
		b.WriteString(" " + right + border.Render(" ─"))
	}
	b.WriteString(border.Render("╮") + "\n")
	for _, l := range lines {
		l = ansi.Truncate(l, inner, "…")
		b.WriteString(border.Render("│ ") + l + strings.Repeat(" ", max(inner-lipgloss.Width(l), 0)) + border.Render(" │") + "\n")
	}
	b.WriteString(border.Render("╰" + strings.Repeat("─", max(width-2, 0)) + "╯"))
	return b.String()
}

// Spread puts left and right on one line of the given width.
func Spread(left, right string, width int) string {
	room := width - lipgloss.Width(right) - 1
	left = ansi.Truncate(left, max(room, 0), "…")
	return left + strings.Repeat(" ", max(width-lipgloss.Width(left)-lipgloss.Width(right), 1)) + right
}

// Indent adds n spaces in front of each line.
func Indent(s string, n int) string {
	pad := strings.Repeat(" ", n)
	return pad + strings.ReplaceAll(s, "\n", "\n"+pad)
}

var sparkChars = []rune("▁▂▃▄▅▆▇█")

// levelFrac maps dBFS to 0..1 over the range -60..0 dB.
func levelFrac(db float64) float64 { return math.Max(0, math.Min(1, (db+60)/60)) }

// Spark renders a level history as block characters.
func Spark(history []float64) string {
	var b strings.Builder
	for _, db := range history {
		b.WriteRune(sparkChars[int(levelFrac(db)*float64(len(sparkChars)-1)+0.5)])
	}
	return b.String()
}

// Bar renders a level as a bar of width cells: the filled part and the empty part.
func Bar(db float64, width int) (filled, empty string) {
	n := int(levelFrac(db)*float64(width) + 0.5)
	return strings.Repeat("━", n), strings.Repeat("─", width-n)
}

const (
	labelWidth  = 4
	prefixWidth = 8 + 2 + labelWidth + 2 // "00:12:05  Them  "
)

// RenderTranscript renders clean segments for a pane of the given width, with
// a hanging indent under the time and the speaker.
func RenderTranscript(t Theme, segs []store.Segment, width int) string {
	textWidth := max(width-prefixWidth, 10)
	var b strings.Builder
	for i, s := range segs {
		if i > 0 {
			b.WriteString("\n")
		}
		label := t.style(t.SpeakerColor(s.Speaker)).Bold(true).Width(labelWidth).Render(s.Speaker.Label())
		lines := strings.Split(ansi.Wrap(s.Text, textWidth, ""), "\n")
		b.WriteString(t.style(t.Dim).Render(store.Clock(s.Start)) + "  " + label + "  " + lines[0])
		for _, l := range lines[1:] {
			b.WriteString("\n" + strings.Repeat(" ", prefixWidth) + l)
		}
	}
	return b.String()
}

// shortModel returns a short name for the HUD.
func shortModel(model string) string {
	switch model {
	case "whisper-large-v3-turbo":
		return "turbo"
	case "whisper-large-v3":
		return "large-v3"
	}
	return model
}

// ansiTrunc cuts a styled string to n cells.
func ansiTrunc(s string, n int) string { return ansi.Truncate(s, max(n, 0), "…") }
