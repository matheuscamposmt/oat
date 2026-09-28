package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/matheuscamposmt/oat/internal/session"
	"github.com/matheuscamposmt/oat/internal/store"
)

// Deps holds what the HUD needs from the rest of oat.
type Deps struct {
	Store *store.Store
	Theme Theme
	// Start begins a recording.
	Start func(ctx context.Context, title string) (Recorder, error)
	// Drain sends leftover chunks in the background. It can be nil.
	Drain func(ctx context.Context, emit func(any))
	// AutoStart begins a recording with NewTitle at once (oat new).
	AutoStart bool
	NewTitle  string
}

type screen int

const (
	homeScreen screen = iota
	recordScreen
	viewerScreen
)

type (
	sessionStartedMsg struct{ rec Recorder }
	sessionErrMsg     struct{ err error }
	drainEventMsg     struct{ ev any }
	signalMsg         struct{}
)

type app struct {
	deps    Deps
	ctx     context.Context
	screen  screen
	home    homeModel
	rec     recordModel
	recOn   bool
	view    viewerModel
	width   int
	height  int
	drainCh chan any
}

func newApp(ctx context.Context, d Deps) app {
	return app{deps: d, ctx: ctx, home: newHomeModel(d.Theme, d.Store), drainCh: make(chan any, 256)}
}

func waitDrain(ch <-chan any) tea.Cmd {
	return func() tea.Msg { return drainEventMsg{<-ch} }
}

func (a app) Init() tea.Cmd {
	cmds := []tea.Cmd{loadRows(a.deps.Store), waitDrain(a.drainCh)}
	if a.deps.AutoStart {
		cmds = append(cmds, a.startCmd(a.deps.NewTitle))
	}
	return tea.Batch(cmds...)
}

func (a app) startCmd(title string) tea.Cmd {
	start, ctx := a.deps.Start, a.ctx
	return func() tea.Msg {
		rec, err := start(ctx, title)
		if err != nil {
			return sessionErrMsg{err}
		}
		return sessionStartedMsg{rec}
	}
}

func (a app) size() tea.WindowSizeMsg { return tea.WindowSizeMsg{Width: a.width, Height: a.height} }

func (a app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		var c1, c2, c3 tea.Cmd
		a.home, c1 = a.home.Update(msg)
		if a.recOn {
			a.rec, c2 = a.rec.Update(msg)
		}
		if a.screen == viewerScreen {
			a.view, c3 = a.view.Update(msg)
		}
		return a, tea.Batch(c1, c2, c3)
	case startRecordingMsg:
		if a.recOn {
			return a, nil
		}
		return a, a.startCmd(msg.title)
	case sessionStartedMsg:
		a.rec = newRecordModel(a.deps.Theme, msg.rec)
		a.recOn, a.screen = true, recordScreen
		a.rec, _ = a.rec.Update(a.size())
		return a, a.rec.Init()
	case sessionErrMsg:
		a.home.err = "Could not start the recording: " + msg.err.Error()
		a.screen = homeScreen
		return a, nil
	case openMeetingMsg:
		v, err := newViewerModel(a.deps.Theme, a.deps.Store, msg.id)
		if err != nil {
			a.home.err = err.Error()
			return a, nil
		}
		a.view, _ = v.Update(a.size())
		a.screen = viewerScreen
		return a, nil
	case backHomeMsg:
		if a.screen == recordScreen && !a.rec.stopped {
			return a, nil
		}
		if a.screen == recordScreen {
			a.recOn = false
		}
		a.screen = homeScreen
		return a, loadRows(a.deps.Store)
	case drainEventMsg:
		var cmd tea.Cmd
		if ev, ok := msg.ev.(session.DrainEvent); ok {
			a.home, cmd = a.home.Update(drainMsg(ev))
		}
		return a, tea.Batch(cmd, waitDrain(a.drainCh))
	case quitDuringDrain, signalMsg:
		if a.recOn && !a.rec.stopped {
			rec := a.rec.rec
			text := a.rec.notes.Value()
			mt := a.rec.rec.Meeting()
			return a, func() tea.Msg {
				_ = mt.WriteNotes(text)
				rec.Abandon()
				return tea.Quit()
			}
		}
		return a, tea.Quit
	}
	var cmd tea.Cmd
	switch a.screen {
	case recordScreen:
		a.rec, cmd = a.rec.Update(msg)
	case viewerScreen:
		a.view, cmd = a.view.Update(msg)
	default:
		if _, ok := msg.(recEventMsg); ok {
			return a, nil
		}
		a.home, cmd = a.home.Update(msg)
	}
	return a, cmd
}

const (
	minWidth  = 60
	minHeight = 16
)

func (a app) View() tea.View {
	var s string
	switch {
	case a.width > 0 && (a.width < minWidth || a.height < minHeight):
		s = fmt.Sprintf("oat needs a terminal of at least %d×%d. This one is %d×%d.", minWidth, minHeight, a.width, a.height)
		if a.recOn {
			s += "\nThe recording continues."
		}
	case a.screen == recordScreen:
		s = a.rec.View()
	case a.screen == viewerScreen:
		s = a.view.View()
	default:
		s = a.home.View()
	}
	v := tea.NewView(s)
	v.AltScreen = true
	v.WindowTitle = "oat"
	return v
}

// Run shows the HUD and blocks until you quit.
func Run(ctx context.Context, d Deps) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	a := newApp(ctx, d)

	var wg sync.WaitGroup
	if d.Drain != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.Drain(ctx, func(ev any) {
				select {
				case a.drainCh <- ev:
				case <-ctx.Done():
				}
			})
		}()
	}

	p := tea.NewProgram(a, tea.WithContext(ctx), tea.WithoutSignalHandler())
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	go func() {
		select {
		case <-sigs:
			p.Send(signalMsg{})
		case <-ctx.Done():
		}
	}()

	_, err := p.Run()
	cancel()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
	if errors.Is(err, tea.ErrProgramKilled) {
		return nil
	}
	return err
}
