// Package handoff sends the summary of a meeting to a command from
// config.toml, for example a message to another agent or a webhook.
package handoff

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/matheuscamposmt/oat/internal/store"
)

// Target is one [handoff.NAME] table.
type Target struct {
	Name    string
	Command []string
}

// Timeout stops a command that hangs.
var Timeout = 2 * time.Minute

// ErrNoTargets is the error when config.toml has no [handoff.NAME] table.
var ErrNoTargets = errors.New("no handoff target. Add a [handoff.NAME] table with a command to config.toml")

// Load reads the [handoff.NAME] tables of config.toml in file order.
// A missing file has no targets.
func Load(path string) ([]Target, error) {
	var f struct {
		Handoff map[string]struct {
			Command []string `toml:"command"`
		} `toml:"handoff"`
	}
	md, err := toml.DecodeFile(path, &f)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var ts []Target
	for _, k := range md.Keys() {
		if len(k) == 2 && k[0] == "handoff" {
			cmd := f.Handoff[k[1]].Command
			if len(cmd) == 0 || cmd[0] == "" {
				return nil, fmt.Errorf("read %s: the handoff target %q has no command", path, k[1])
			}
			ts = append(ts, Target{Name: k[1], Command: cmd})
		}
	}
	return ts, nil
}

// Names returns the names of the targets.
func Names(ts []Target) []string {
	names := make([]string, len(ts))
	for i, t := range ts {
		names[i] = t.Name
	}
	return names
}

func orList(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
}

// Pick returns the target called name. Without a name, it returns the only
// target, or an error that lists the names.
func Pick(ts []Target, name string) (Target, error) {
	if len(ts) == 0 {
		return Target{}, ErrNoTargets
	}
	if name == "" {
		if len(ts) == 1 {
			return ts[0], nil
		}
		return Target{}, fmt.Errorf("pick a target with --to %s", orList(Names(ts)))
	}
	for _, t := range ts {
		if t.Name == name {
			return t, nil
		}
	}
	return Target{}, fmt.Errorf("no handoff target %q. The targets are %s", name, orList(Names(ts)))
}

const dateFormat = "2006-01-02 15:04"

// Prepare writes the title, the date, the ID and summary.md of the meeting to
// a Markdown file in a new temp folder, and returns the file and the command
// of t with {file}, {title}, {date} and {meeting} replaced in each argument.
func Prepare(m *store.Meeting, t Target) (string, []string, error) {
	meta := m.Snapshot()
	summary, err := m.Summary()
	if err != nil {
		return "", nil, err
	}
	if strings.TrimSpace(summary) == "" {
		return "", nil, fmt.Errorf("meeting %s has no summary. Run oat summary %s first", meta.ID, meta.ID)
	}
	dir, err := os.MkdirTemp("", "oat-handoff-")
	if err != nil {
		return "", nil, err
	}
	file := filepath.Join(dir, meta.ID+".md")
	date := meta.StartedAt.Format(dateFormat)
	body := fmt.Sprintf("# %s\n\nDate: %s\nMeeting: %s\n\n%s", meta.Title, date, meta.ID, summary)
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		os.RemoveAll(dir)
		return "", nil, err
	}
	r := strings.NewReplacer("{file}", file, "{title}", meta.Title, "{date}", date, "{meeting}", meta.ID)
	argv := make([]string, len(t.Command))
	for i, a := range t.Command {
		argv[i] = r.Replace(a)
	}
	return file, argv, nil
}

// Send runs the command of t for the meeting, without a shell, and removes
// the file afterwards. It returns the stdout of the command. When the command
// fails, the error is its stderr.
func Send(ctx context.Context, m *store.Meeting, t Target) ([]byte, error) {
	file, argv, err := Prepare(m, t)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(filepath.Dir(file))
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	cmd.WaitDelay = time.Second // a child that keeps the pipes open does not hold oat
	err = cmd.Run()
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return out.Bytes(), fmt.Errorf("%s did not end in %s", argv[0], Timeout)
	case err == nil:
		return out.Bytes(), nil
	case strings.TrimSpace(errOut.String()) != "":
		return out.Bytes(), errors.New(strings.TrimSpace(errOut.String()))
	}
	return out.Bytes(), fmt.Errorf("%s: %w", argv[0], err)
}
