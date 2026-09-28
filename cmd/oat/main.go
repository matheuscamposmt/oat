// Command oat records meetings from the terminal and transcribes them with Groq.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/matheuscamposmt/oat/internal/audio"
	"github.com/matheuscamposmt/oat/internal/config"
	"github.com/matheuscamposmt/oat/internal/doctor"
	"github.com/matheuscamposmt/oat/internal/groq"
	"github.com/matheuscamposmt/oat/internal/mcpserver"
	"github.com/matheuscamposmt/oat/internal/session"
	"github.com/matheuscamposmt/oat/internal/store"
	"github.com/matheuscamposmt/oat/internal/tui"
)

var version = "dev"

const usage = `oat records meetings from the terminal and transcribes them with Groq.

Usage:
  oat                  open the list of meetings
  oat new [title]      start a recording now
  oat mcp              run the MCP server (Claude Code starts it)
  oat doctor           test the environment
  oat setup            register the MCP server in Claude Code
  oat version          print the version

Flags for oat and oat new:
  --lang CODE      language of the meeting: pt, en, or auto
  --model NAME     whisper-large-v3-turbo or whisper-large-v3
  --echo MODE      echo cancel: auto, on, or off
  --keep-audio     keep the WAV chunks in the meeting folder
`

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	cmd := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "", "new":
		return runHUD(cmd == "new", args, stderr)
	case "mcp":
		return runMCP(stderr)
	case "doctor":
		return runDoctor(stdout, stderr)
	case "setup":
		return runSetup(stdout, stderr)
	case "version":
		fmt.Fprintln(stdout, "oat", version)
		return 0
	case "help":
		fmt.Fprint(stdout, usage)
		return 0
	}
	fmt.Fprintf(stderr, "oat: unknown command %q\n\n%s", cmd, usage)
	return 2
}

func fail(stderr io.Writer, err error) int {
	fmt.Fprintln(stderr, "oat:", err)
	return 1
}

func runHUD(isNew bool, args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("oat", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	var o config.Overrides
	fs.StringVar(&o.Lang, "lang", "", "")
	fs.StringVar(&o.Model, "model", "", "")
	fs.StringVar(&o.Echo, "echo", "", "")
	fs.BoolVar(&o.KeepAudio, "keep-audio", false, "")
	words, err := parseInterspersed(fs, args)
	if err != nil {
		return 2
	}
	paths, err := config.DefaultPaths()
	if err != nil {
		return fail(stderr, err)
	}
	cfg, err := config.Load(paths.Config)
	if err != nil {
		return fail(stderr, err)
	}
	cfg = cfg.Apply(o)
	if err := cfg.Validate(); err != nil {
		return fail(stderr, err)
	}
	key, _, err := config.GroqKey()
	if err != nil {
		return fail(stderr, err)
	}

	ctx := context.Background()
	st := store.New(paths.Meetings())
	pactl := audio.NewPactl()
	echo := &audio.Echo{Pactl: pactl, StatePath: paths.EchoState()}
	if _, err := echo.CleanupStale(ctx); err != nil {
		fmt.Fprintln(stderr, "oat: remove the old echo-cancel module:", err)
	}
	client := groq.New(key)

	liveErr := func() error {
		if live, err := st.Live(); err == nil {
			return fmt.Errorf("a recording is in progress in another terminal (%s)", live.Meta.ID)
		}
		return nil
	}
	title := strings.TrimSpace(strings.Join(words, " "))
	if isNew {
		if err := liveErr(); err != nil {
			return fail(stderr, err)
		}
		if title == "" {
			title = store.DefaultTitle(time.Now())
		}
	}
	deps := tui.Deps{
		Store: st,
		Theme: tui.NewTheme(lipgloss.HasDarkBackground(os.Stdin, os.Stdout)),
		Start: func(ctx context.Context, title string) (tui.Recorder, error) {
			if err := liveErr(); err != nil {
				return nil, err
			}
			s, err := session.Start(ctx, session.Options{Title: title, Config: cfg, Store: st, Client: client, Pactl: pactl, Echo: echo})
			if err != nil {
				return nil, err
			}
			return s, nil
		},
		Drain: func(ctx context.Context, emit func(any)) {
			session.Drain(ctx, st, client, cfg, emit)
		},
		AutoStart: isNew,
		NewTitle:  title,
	}
	if err := tui.Run(ctx, deps); err != nil {
		return fail(stderr, err)
	}
	return 0
}

// parseInterspersed parses flags before and after the title words. The flag
// package alone stops at the first word that is not a flag.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var words []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return words, nil
		}
		words = append(words, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func runMCP(stderr io.Writer) int {
	paths, err := config.DefaultPaths()
	if err != nil {
		return fail(stderr, err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := mcpserver.Run(ctx, store.New(paths.Meetings()), version); err != nil && ctx.Err() == nil {
		return fail(stderr, err)
	}
	return 0
}

func runDoctor(stdout, stderr io.Writer) int {
	paths, err := config.DefaultPaths()
	if err != nil {
		return fail(stderr, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pactl := audio.NewPactl()
	rs := doctor.Run(ctx, doctor.Deps{
		LookPath: exec.LookPath,
		Pactl:    pactl,
		GroqKey:  config.GroqKey,
		Ping: func(ctx context.Context, key string) error {
			return groq.New(key).Ping(ctx)
		},
		MCPStatus: func(ctx context.Context) error {
			return doctor.MCPStatus(ctx, audio.Exec, exec.LookPath)
		},
		DataDir: paths.Data,
		Echo:    &audio.Echo{Pactl: pactl, StatePath: paths.EchoState()},
	})
	if doctor.Print(stdout, rs) {
		return 0
	}
	return 1
}

func runSetup(stdout, stderr io.Writer) int {
	bin, err := os.Executable()
	if err != nil {
		return fail(stderr, err)
	}
	if resolved, err := filepath.EvalSymlinks(bin); err == nil {
		bin = resolved
	}
	if err := doctor.Setup(context.Background(), audio.Exec, exec.LookPath, bin, stdout); err != nil {
		return fail(stderr, err)
	}
	return 0
}
