package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"unicode"

	"github.com/matheuscamposmt/oat/internal/config"
	"github.com/matheuscamposmt/oat/internal/keyterms"
)

func runKeyterms(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	paths, err := config.DefaultPaths()
	if err != nil {
		return fail(stderr, err)
	}
	lists, err := keyterms.Load(paths.Config)
	if err != nil {
		return fail(stderr, err)
	}
	cmd, args := args[0], args[1:]
	switch {
	case cmd == "list" && len(args) == 0:
		printLists(stdout, lists)
	case cmd == "list" && len(args) == 1:
		l, err := keyterms.Pick(lists, args[0], "")
		if err != nil {
			return fail(stderr, err)
		}
		printTerms(stdout, l)
	case cmd == "add" && len(args) >= 2:
		l, _ := keyterms.Pick(lists, args[0], "")
		l.Name = args[0]
		var added []string
		for _, t := range args[1:] {
			if !slices.ContainsFunc(l.Terms, func(s string) bool { return strings.EqualFold(s, t) }) {
				added = append(added, t)
			}
		}
		// New terms go first: they come from a wrong transcription, and the
		// terms past the limit are not sent.
		l.Terms = append(added, l.Terms...)
		if err := keyterms.Save(paths.Config, l); err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintf(stdout, "The list %s has %d terms.\n", l.Name, len(l.Terms))
		warnAccents(stdout, added)
	case cmd == "rm" && len(args) >= 2:
		l, err := keyterms.Pick(lists, args[0], "")
		if err != nil {
			return fail(stderr, err)
		}
		n := len(l.Terms)
		l.Terms = slices.DeleteFunc(l.Terms, func(s string) bool {
			return slices.ContainsFunc(args[1:], func(t string) bool { return strings.EqualFold(s, t) })
		})
		if len(l.Terms) == n {
			return fail(stderr, errors.New("none of these terms is in the list "+l.Name))
		}
		if err := keyterms.Save(paths.Config, l); err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintf(stdout, "The list %s has %d terms.\n", l.Name, len(l.Terms))
	case cmd == "scan":
		return runScan(args, paths.Config, lists, stdout, stderr)
	default:
		fmt.Fprint(stderr, usage)
		return 2
	}
	return 0
}

// newScanner returns the scan with Claude Code. Tests replace it.
var newScanner = func() (keyterms.Scanner, error) {
	bin, err := exec.LookPath("claude")
	if err != nil {
		return nil, errors.New("claude is not installed. Install Claude Code to scan a folder")
	}
	return keyterms.Claude(bin), nil
}

// stdin gives the answers to the questions. Tests replace it.
var stdin io.Reader = os.Stdin

func runScan(args []string, cfgPath string, lists []keyterms.List, stdout, stderr io.Writer) int {
	fset := flag.NewFlagSet("scan", flag.ContinueOnError)
	fset.SetOutput(stderr)
	fset.Usage = func() { fmt.Fprint(stderr, usage) }
	name := fset.String("name", "", "")
	all := fset.Bool("all", false, "")
	folders, err := parseInterspersed(fset, args)
	if err != nil {
		return 2
	}
	type job struct{ folder, name string }
	var jobs []job
	switch {
	case *all && len(folders) == 0 && *name == "":
		for _, l := range lists {
			if l.Folder != "" {
				jobs = append(jobs, job{l.Folder, l.Name})
			}
		}
		if len(jobs) == 0 {
			return fail(stderr, errors.New("no list has a folder"))
		}
	case !*all && len(folders) == 1:
		jobs = append(jobs, job{folders[0], *name})
	default:
		fset.Usage()
		return 2
	}
	scan, err := newScanner()
	if err != nil {
		return fail(stderr, err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	in := bufio.NewReader(stdin)
	for _, j := range jobs {
		if err := scanFolder(ctx, scan, in, stdout, cfgPath, lists, j.folder, j.name); err != nil {
			return fail(stderr, err)
		}
	}
	return 0
}

// scanFolder asks Claude for the terms of a folder, adds them to the list,
// lets the user remove terms, and saves the list after a yes. The list name
// is the folder name when name is empty.
func scanFolder(ctx context.Context, scan keyterms.Scanner, in *bufio.Reader, w io.Writer, cfgPath string, lists []keyterms.List, folder, name string) error {
	folder, err := filepath.Abs(folder)
	if err != nil {
		return err
	}
	if fi, err := os.Stat(folder); err != nil {
		return err
	} else if !fi.IsDir() {
		return fmt.Errorf("%s is not a folder", folder)
	}
	if name == "" {
		name = filepath.Base(folder)
	}
	fmt.Fprintf(w, "Claude reads the documents of %s. This can take a few minutes.\n", folder)
	found, err := scan(ctx, folder)
	if err != nil {
		return err
	}
	l, _ := keyterms.Pick(lists, name, "")
	l.Name, l.Folder = name, folder
	for _, t := range found {
		if !slices.ContainsFunc(l.Terms, func(s string) bool { return strings.EqualFold(s, t) }) {
			l.Terms = append(l.Terms, t)
		}
	}
	for i, t := range l.Terms {
		fmt.Fprintf(w, "%2d. %s\n", i+1, t)
	}
	fmt.Fprint(w, "Numbers of the terms to remove, separated by spaces (enter keeps all): ")
	line, _ := in.ReadString('\n')
	drop := map[int]bool{}
	for _, f := range strings.Fields(line) {
		n, err := strconv.Atoi(f)
		if err != nil || n < 1 || n > len(l.Terms) {
			return fmt.Errorf("%q is not a number from the list", f)
		}
		drop[n-1] = true
	}
	var keep []string
	for i, t := range l.Terms {
		if !drop[i] {
			keep = append(keep, t)
		}
	}
	l.Terms = keep
	fmt.Fprintf(w, "Save %d terms in the list %s? [y/N] ", len(l.Terms), name)
	answer, _ := in.ReadString('\n')
	if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
		fmt.Fprintln(w, "Not saved.")
		return nil
	}
	if err := keyterms.Save(cfgPath, l); err != nil {
		return err
	}
	fmt.Fprintf(w, "Saved the list %s in %s.\n", name, cfgPath)
	warnAccents(w, l.Terms)
	return nil
}

func printLists(w io.Writer, lists []keyterms.List) {
	if len(lists) == 0 {
		fmt.Fprintln(w, "No key term lists. Create one with oat keyterms scan FOLDER or oat keyterms add NAME TERM.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, l := range lists {
		fmt.Fprintf(tw, "%s\t%d terms\t%s\n", l.Name, len(l.Terms), l.Folder)
	}
	tw.Flush()
}

// printTerms shows the terms that a recording with this list sends, without
// --keyterms and title words.
func printTerms(w io.Writer, l keyterms.List) {
	kept, dropped := keyterms.Merge(nil, l, "")
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, t := range kept {
		fmt.Fprintf(tw, "%s\t%s\n", t.Text, t.Origin)
	}
	tw.Flush()
	warnAccents(w, l.Terms)
	if len(dropped) > 0 {
		fmt.Fprintf(w, "Warning: past the limit of %d terms or %d characters, oat does not send: %s\n", keyterms.MaxTerms, keyterms.MaxChars, strings.Join(dropped, ", "))
	}
}

func warnAccents(w io.Writer, terms []string) {
	var accented []string
	for _, t := range terms {
		if strings.IndexFunc(t, func(r rune) bool { return r > unicode.MaxASCII }) >= 0 {
			accented = append(accented, t)
		}
	}
	if len(accented) > 0 {
		fmt.Fprintf(w, "Warning: Claude drops the accented letters of %s. Write them without accents.\n", strings.Join(accented, ", "))
	}
}

// offerScan asks for project folders and scans each one, for oat setup.
func offerScan(ctx context.Context, in *bufio.Reader, w io.Writer, cfgPath string) {
	fmt.Fprintln(w, "\nKey terms help the transcription spell the names of your projects. oat can find them in the documents of a project.")
	fmt.Fprintln(w, "Type the folders of your projects, one per line, and an empty line to end:")
	var folders []string
	for {
		line, err := in.ReadString('\n')
		if line = strings.TrimSpace(line); line == "" {
			break
		}
		folders = append(folders, line)
		if err != nil {
			break
		}
	}
	if len(folders) == 0 {
		return
	}
	scan, err := newScanner()
	if err != nil {
		fmt.Fprintln(w, err)
		return
	}
	fmt.Fprintln(w, "The documents of each folder go to Claude, the same as in a Claude Code session there.")
	for _, f := range folders {
		lists, err := keyterms.Load(cfgPath)
		if err == nil {
			err = scanFolder(ctx, scan, in, w, cfgPath, lists, f, "")
		}
		if err != nil {
			fmt.Fprintf(w, "Scan of %s: %v\n", f, err)
		}
	}
}
