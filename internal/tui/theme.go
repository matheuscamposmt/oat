// Package tui is the terminal HUD: the home screen, the recording screen, and the viewer.
package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"

	"github.com/matheuscamposmt/oat/internal/store"
)

// Theme holds the HUD colors for a light or a dark terminal.
type Theme struct {
	Accent color.Color // the Claude orange
	Me     color.Color
	Them   color.Color
	Dim    color.Color
	Text   color.Color
	Warn   color.Color
	Err    color.Color
	Border color.Color
}

// NewTheme returns the colors for a dark or a light terminal background.
func NewTheme(dark bool) Theme {
	ld := lipgloss.LightDark(dark)
	return Theme{
		Accent: lipgloss.Color("#D97757"),
		Me:     ld(lipgloss.Color("#2E7D32"), lipgloss.Color("#8BC34A")),
		Them:   ld(lipgloss.Color("#1565C0"), lipgloss.Color("#64B5F6")),
		Dim:    ld(lipgloss.Color("#8A8A8A"), lipgloss.Color("#6C6C6C")),
		Text:   ld(lipgloss.Color("#1F1F1F"), lipgloss.Color("#E6E6E6")),
		Warn:   ld(lipgloss.Color("#B26A00"), lipgloss.Color("#E5C07B")),
		Err:    ld(lipgloss.Color("#C62828"), lipgloss.Color("#EF5350")),
		Border: ld(lipgloss.Color("#C8C8C8"), lipgloss.Color("#444444")),
	}
}

func (t Theme) style(c color.Color) lipgloss.Style { return lipgloss.NewStyle().Foreground(c) }

// SpeakerColor returns the color of a speaker label.
func (t Theme) SpeakerColor(sp store.Speaker) color.Color {
	if sp == store.Me {
		return t.Me
	}
	return t.Them
}
