package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// Messages that move the app between screens.
type (
	startRecordingMsg struct{ title string }
	openMeetingMsg    struct{ id string }
	backHomeMsg       struct{ from screen }
	quitDuringDrain   struct{}
	quitRequestMsg    struct{}
	// summaryDoneMsg comes when a summary that the user or the stop started ends.
	summaryDoneMsg struct {
		id  string
		err error
	}
)

// summarizeCmd runs fn for the meeting in the background.
func summarizeCmd(fn func(string) error, id string) tea.Cmd {
	return func() tea.Msg { return summaryDoneMsg{id: id, err: fn(id)} }
}

// Messages of the recording screen.
type (
	recEventMsg   struct{ ev any }
	tickMsg       time.Time
	notesSavedMsg struct{ err error }
)

// waitEvent reads the next session event.
func waitEvent(ch <-chan any) tea.Cmd {
	return func() tea.Msg { return recEventMsg{<-ch} }
}

// tick sends a tickMsg after one second.
func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func msgCmd(m tea.Msg) tea.Cmd { return func() tea.Msg { return m } }
