package store

import (
	"fmt"
	"strings"
	"time"
)

// Clock formats seconds on the meeting clock as HH:MM:SS.
// A negative value gives 00:00:00.
func Clock(secIn float64) string {
	if secIn < 0 {
		return "00:00:00"
	}
	sec := int(secIn)
	seconds := sec % 60
	mins := (sec / 60) % 60
	hours := sec / 60 / 60
	return fmt.Sprintf("%02d:%02d:%02d", hours, mins, seconds)
}

// TranscriptLines renders segments as "[00:12:05] Me: text" lines.
// With bold, the speaker label is bold Markdown.
func TranscriptLines(segs []Segment, bold bool) []string {
	lines := make([]string, len(segs))
	for i, s := range segs {
		label := s.Speaker.Label() + ":"
		if bold {
			label = "**" + label + "**"
		}
		lines[i] = fmt.Sprintf("[%s] %s %s", Clock(s.Start), label, s.Text)
	}
	return lines
}

// WriteTranscriptMD rewrites transcript.md from the clean segments.
func (m *Meeting) WriteTranscriptMD() error {
	segs, err := m.Segments()
	if err != nil {
		return err
	}
	meta := m.Snapshot()
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n%s · %s\n\n", meta.Title, meta.StartedAt.Format("2006-01-02 15:04"), Clock(meta.Duration(time.Now()).Seconds()))
	for _, line := range TranscriptLines(Clean(segs), true) {
		b.WriteString(line + "\n\n")
	}
	return WriteFileAtomic(m.path(transcriptFile), []byte(b.String()))
}
