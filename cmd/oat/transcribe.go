package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/matheuscamposmt/oat/internal/claudestt"
	"github.com/matheuscamposmt/oat/internal/config"
	"github.com/matheuscamposmt/oat/internal/filetx"
	"github.com/matheuscamposmt/oat/internal/keyterms"
	"github.com/matheuscamposmt/oat/internal/stt"
)

// ffmpegPath is the ffmpeg that decodes the file. Tests point it at a script.
var ffmpegPath = "ffmpeg"

// newTranscribeClient is newClient. Tests replace it.
var newTranscribeClient = newClient

func runTranscribe(args []string, stdout, stderr io.Writer) int {
	fset := flag.NewFlagSet("transcribe", flag.ContinueOnError)
	fset.SetOutput(stderr)
	fset.Usage = func() { fmt.Fprint(stderr, usage) }
	var o config.Overrides
	fset.StringVar(&o.Transcriber, "transcriber", "", "")
	fset.StringVar(&o.Lang, "lang", "", "")
	fset.StringVar(&o.Model, "model", "", "")
	terms := fset.String("keyterms", "", "")
	set := fset.String("keyterms-set", "", "")
	files, err := parseInterspersed(fset, args)
	if err != nil {
		return 2
	}
	if len(files) != 1 {
		fset.Usage()
		return 2
	}
	path := files[0]
	if _, err := os.Stat(path); err != nil {
		return fail(stderr, err)
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
	kt, err := keytermsFor(lists, *terms, *set, "")
	if err != nil {
		return fail(stderr, err)
	}
	client, _, err := newTranscribeClient(cfg, paths)
	if err != nil {
		return fail(stderr, err)
	}
	opts := filetx.Options{
		STT:     stt.Options{Model: cfg.Model, Language: cfg.Lang, Keyterms: kt},
		Workers: 2,
		Log:     func(format string, args ...any) { fmt.Fprintf(stderr, "oat: "+format+"\n", args...) },
	}
	if cfg.Transcriber == "claude" {
		opts.Workers = claudestt.MaxStreams
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var ffErr bytes.Buffer
	cmd := exec.CommandContext(ctx, ffmpegPath, "-nostdin", "-v", "error", "-i", path, "-f", "s16le", "-ac", "1", "-ar", "16000", "-")
	cmd.Stderr = &ffErr
	pcm, err := cmd.StdoutPipe()
	if err != nil {
		return fail(stderr, err)
	}
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			return fail(stderr, errors.New("ffmpeg was not found. Install it: sudo apt install ffmpeg"))
		}
		return fail(stderr, err)
	}
	paras, txErr := filetx.Transcribe(ctx, pcm, client, opts)
	if txErr != nil {
		cancel() // ffmpeg can be blocked on a full pipe
	}
	waitErr := cmd.Wait()
	if txErr != nil {
		fmt.Fprintf(stderr, "oat: %v\n%s", txErr, ffErr.String())
		return 1
	}
	if waitErr != nil {
		fmt.Fprintf(stderr, "oat: ffmpeg could not decode %s: %v\n%s", path, waitErr, ffErr.String())
		return 1
	}
	if len(paras) > 0 {
		fmt.Fprintln(stdout, joinParagraphs(paras))
	}
	return 0
}

// joinParagraphs starts a new paragraph only after a sentence end, because a chunk cut can fall mid-sentence.
func joinParagraphs(paras []filetx.Paragraph) string {
	var b strings.Builder
	for i, p := range paras {
		if i > 0 {
			if strings.ContainsAny(paras[i-1].Text[len(paras[i-1].Text)-1:], ".!?…") {
				b.WriteString("\n\n")
			} else {
				b.WriteString(" ")
			}
		}
		b.WriteString(p.Text)
	}
	return b.String()
}
