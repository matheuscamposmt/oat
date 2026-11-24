package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/matheuscamposmt/oat/internal/config"
	"github.com/matheuscamposmt/oat/internal/handoff"
	"github.com/matheuscamposmt/oat/internal/store"
)

const handoffExample = `[handoff.techlead]
command = ["orca-ide", "orchestration", "send", "--to", "bot:Tech Lead",
           "--subject", "{title}", "--body-file", "{file}"]`

func runHandoff(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("handoff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	to := fs.String("to", "", "")
	dry := fs.Bool("dry-run", false, "")
	ids, err := parseInterspersed(fs, args)
	if err != nil {
		return 2
	}
	if len(ids) > 1 {
		fmt.Fprintln(stderr, "oat: handoff takes one meeting ID")
		return 2
	}
	paths, err := config.DefaultPaths()
	if err != nil {
		return fail(stderr, err)
	}
	targets, err := handoff.Load(paths.Config)
	if err != nil {
		return fail(stderr, err)
	}
	t, err := handoff.Pick(targets, *to)
	if errors.Is(err, handoff.ErrNoTargets) {
		return fail(stderr, fmt.Errorf("%w in %s, for example:\n\n%s", err, paths.Config, handoffExample))
	}
	if err != nil {
		return fail(stderr, err)
	}
	m, err := loadMeeting(store.New(paths.Meetings()), strings.Join(ids, ""))
	if err != nil {
		return fail(stderr, err)
	}
	if *dry {
		file, argv, err := handoff.Prepare(m, t)
		if err != nil {
			return fail(stderr, err)
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return fail(stderr, err)
		}
		quoted := make([]string, len(argv))
		for i, a := range argv {
			quoted[i] = strconv.Quote(a)
		}
		fmt.Fprintf(stdout, "File: %s\n\n%s\nCommand: [%s]\n", file, data, strings.Join(quoted, ", "))
		return 0
	}
	out, err := handoff.Send(context.Background(), m, t)
	stdout.Write(out)
	if err != nil {
		return fail(stderr, fmt.Errorf("the handoff to %s failed: %w", t.Name, err))
	}
	return 0
}

// loadMeeting returns the meeting with the ID, or the latest one for "" and "latest".
func loadMeeting(st *store.Store, id string) (*store.Meeting, error) {
	if id == "" || id == "latest" {
		return st.Latest()
	}
	return st.Load(id)
}
