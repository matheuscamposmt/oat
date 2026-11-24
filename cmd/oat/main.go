// Command oat records meetings from the terminal and transcribes them with
// the Claude Code voice stream or with Groq.
package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
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
	"github.com/charmbracelet/x/term"

	"github.com/matheuscamposmt/oat/internal/audio"
	"github.com/matheuscamposmt/oat/internal/claudestt"
	"github.com/matheuscamposmt/oat/internal/config"
	"github.com/matheuscamposmt/oat/internal/doctor"
	"github.com/matheuscamposmt/oat/internal/groq"
	"github.com/matheuscamposmt/oat/internal/keyterms"
	"github.com/matheuscamposmt/oat/internal/mcpserver"
	"github.com/matheuscamposmt/oat/internal/session"
	"github.com/matheuscamposmt/oat/internal/store"
	"github.com/matheuscamposmt/oat/internal/summary"
	"github.com/matheuscamposmt/oat/internal/transcribe"
	"github.com/matheuscamposmt/oat/internal/tui"
)

var version = "dev"

const usage = `oat records meetings from the terminal and transcribes them.

Usage:
  oat                       open the list of meetings
  oat new [title]           start a recording now
  oat summary [id]          write summary.md with Claude Code (default: the latest meeting)
  oat transcribe FILE       print the text of an audio file
  oat keyterms list [NAME]  show the key term lists, or the terms that one list sends
  oat keyterms add NAME TERM...
                            add terms to a list, or create it
  oat keyterms rm NAME TERM...
                            remove terms from a list
  oat keyterms scan FOLDER [--name NAME]
                            propose a list from the documents of a folder, with Claude Code
  oat keyterms scan --all   scan again each list that has a folder
  oat mcp                   run the MCP server (Claude Code starts it)
  oat doctor                test the environment
  oat setup                 register the MCP server in Claude Code and offer the scan
  oat version               print the version

Flags for oat and oat new:
  --transcriber S      claude (the Claude Code voice stream) or groq
  --no-live            claude only: send each chunk after its pause, not while people speak
  --lang CODE          language of the meeting: pt, en, or auto
  --model NAME         Groq only: whisper-large-v3-turbo or whisper-large-v3
  --echo MODE          echo cancel: auto, on, or off
  --keep-audio         keep the WAV chunks in the meeting folder
  --keyterms LIST      names and acronyms, separated by commas
  --keyterms-set NAME  the key term list (default: the first list whose name is in the title)

Flags for oat transcribe:
  --transcriber S      claude or groq
  --lang CODE          pt, en, or auto
  --model NAME         Groq only
  --keyterms LIST      names and acronyms, separated by commas
  --keyterms-set NAME  the key term list
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
	case "summary":
		return runSummary(args, stdout, stderr)
	case "transcribe":
		return runTranscribe(args, stdout, stderr)
	case "keyterms":
		return runKeyterms(args, stdout, stderr)
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
	fs.StringVar(&o.Transcriber, "transcriber", "", "")
	fs.BoolVar(&o.NoLive, "no-live", false, "")
	fs.StringVar(&o.Lang, "lang", "", "")
	fs.StringVar(&o.Model, "model", "", "")
	fs.StringVar(&o.Echo, "echo", "", "")
	fs.BoolVar(&o.KeepAudio, "keep-audio", false, "")
	flagTerms := fs.String("keyterms", "", "")
	set := fs.String("keyterms-set", "", "")
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
	lists, err := keyterms.Load(paths.Config)
	if err != nil {
		return fail(stderr, err)
	}
	if _, err := keyterms.Pick(lists, *set, ""); err != nil {
		return fail(stderr, err)
	}
	client, claude, err := newClient(cfg, paths)
	if err != nil {
		return fail(stderr, err)
	}
	var live session.LiveFunc
	if claude != nil && cfg.Live {
		live = func(ctx context.Context, lang string, keyterms []string, onText func(string)) (session.LiveStream, error) {
			st, err := claude.Open(ctx, lang, keyterms, onText)
			if err != nil {
				return nil, err // not a typed nil in the interface
			}
			return st, nil
		}
	}

	ctx := context.Background()
	st := store.New(paths.Meetings())
	pactl := audio.NewPactl()
	echo := &audio.Echo{Pactl: pactl, StatePath: paths.EchoState()}
	if _, err := echo.CleanupStale(ctx); err != nil {
		fmt.Fprintln(stderr, "oat: remove the old echo-cancel module:", err)
	}
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
			terms, err := keytermsFor(lists, *flagTerms, *set, title)
			if err != nil {
				return nil, err
			}
			s, err := session.Start(ctx, session.Options{Title: title, Config: cfg, Store: st, Client: client, Live: live, Pactl: pactl, Echo: echo, Keyterms: terms})
			if err != nil {
				return nil, err
			}
			return s, nil
		},
		Drain: func(ctx context.Context, emit func(any)) {
			session.Drain(ctx, st, client, cfg, emit)
		},
		Summarize: summarizer(cfg),
		AutoStart: isNew,
		NewTitle:  title,
	}
	if err := tui.Run(ctx, deps); err != nil {
		return fail(stderr, err)
	}
	return 0
}

// summarizer returns the function that writes the summary after a meeting,
// or nil when auto_summary is off or Claude Code is not installed. It runs
// "oat summary" in its own session, so the summary ends even when you quit
// oat, or press ctrl+c, while it runs.
func summarizer(cfg config.Config) func(id string) error {
	if !cfg.AutoSummary {
		return nil
	}
	if _, err := exec.LookPath("claude"); err != nil {
		return nil
	}
	self, err := os.Executable()
	if err != nil {
		return nil
	}
	return func(id string) error {
		cmd := exec.Command(self, "summary", "--notify", id)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		var errOut bytes.Buffer
		cmd.Stderr = &errOut
		if err := cmd.Run(); err != nil {
			if msg := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(errOut.String()), "oat:")); msg != "" {
				return errors.New(msg)
			}
			return err
		}
		return nil
	}
}

func runSummary(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("summary", flag.ContinueOnError)
	fs.SetOutput(stderr)
	notify := fs.Bool("notify", false, "")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	paths, err := config.DefaultPaths()
	if err != nil {
		return fail(stderr, err)
	}
	st := store.New(paths.Meetings())
	var m *store.Meeting
	if id := fs.Arg(0); id == "" || id == "latest" {
		m, err = st.Latest()
	} else {
		m, err = st.Load(id)
	}
	if err != nil {
		return fail(stderr, err)
	}
	bin, err := exec.LookPath("claude")
	if err != nil {
		return fail(stderr, errors.New("claude is not installed. Install Claude Code to write summaries"))
	}
	title, err := summary.Write(context.Background(), m, summary.Claude(bin))
	if *notify {
		sendNotification(title, err)
	}
	if err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintln(stdout, m.SummaryPath())
	return 0
}

// sendNotification shows a desktop notification when notify-send exists.
func sendNotification(title string, err error) {
	bin, lerr := exec.LookPath("notify-send")
	if lerr != nil {
		return
	}
	body := "Summary ready: " + title
	if err != nil {
		body = "The summary failed: " + err.Error()
	}
	_ = exec.Command(bin, "--app-name=oat", "oat", body).Run()
}

// newClient returns the client of the configured backend. It never reads the
// Groq key when the backend is claude, and it never falls back to Groq.
// The second result is the Claude client, or nil for Groq.
func newClient(cfg config.Config, paths config.Paths) (transcribe.Client, *claudestt.Client, error) {
	if cfg.Transcriber == "groq" {
		key, _, err := config.GroqKey(cfg, paths.Config)
		if err != nil {
			return nil, nil, err
		}
		return groq.New(key), nil, nil
	}
	creds, err := claudestt.CredentialsPath()
	if err != nil {
		return nil, nil, err
	}
	// Only a missing login stops oat here. An expired token is renewed by
	// Claude Code, and the pipeline waits for it.
	if _, err := claudestt.ReadToken(creds); errors.Is(err, claudestt.ErrNoLogin) {
		return nil, nil, err
	}
	c := claudestt.New(creds, "oat/"+version)
	return c, c, nil
}

// keytermsFor returns the key terms of a recording. set names the list; without
// it, the title picks one.
func keytermsFor(lists []keyterms.List, flag, set, title string) ([]string, error) {
	list, err := keyterms.Pick(lists, set, title)
	if err != nil {
		return nil, err
	}
	kept, _ := keyterms.Merge(strings.Split(flag, ","), list, title)
	return keyterms.Texts(kept), nil
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
	cfg, cfgErr := config.Load(paths.Config)
	if cfgErr == nil {
		cfgErr = cfg.Validate()
	}
	rs := doctor.Run(ctx, doctor.Deps{
		ConfigPath:   paths.Config,
		ConfigErr:    cfgErr,
		ExpiryMargin: claudestt.ExpiryMargin,
		AutoSummary:  cfg.AutoSummary,
		LookPath:     exec.LookPath,
		Pactl:        pactl,
		Transcriber:  cfg.Transcriber,
		GroqKey: func() (string, string, error) {
			if cfgErr != nil {
				return "", "", cfgErr
			}
			return config.GroqKey(cfg, paths.Config)
		},
		Ping: func(ctx context.Context, key string) error {
			return groq.New(key).Ping(ctx)
		},
		ClaudeLogin: func() (time.Time, string, error) {
			path, err := claudestt.CredentialsPath()
			if err != nil {
				return time.Time{}, "", err
			}
			t, err := claudestt.ReadToken(path)
			return t.Expires, path, err
		},
		ClaudePing: func(ctx context.Context) error {
			path, err := claudestt.CredentialsPath()
			if err != nil {
				return err
			}
			return claudestt.New(path, "oat/"+version).Ping(ctx)
		},
		MCPStatus: func(ctx context.Context) (doctor.MCPState, error) {
			return doctor.MCPStatus(ctx, audio.Exec, exec.LookPath)
		},
		FFmpeg:  func() (string, error) { return exec.LookPath(ffmpegPath) },
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
	// Without a terminal, for example in make install from a script, oat asks nothing.
	if !term.IsTerminal(os.Stdin.Fd()) {
		return 0
	}
	paths, err := config.DefaultPaths()
	if err != nil {
		return fail(stderr, err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	offerScan(ctx, bufio.NewReader(os.Stdin), stdout, paths.Config)
	return 0
}
