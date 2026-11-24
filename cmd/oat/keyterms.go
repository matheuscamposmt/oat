package main

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
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
	default:
		fmt.Fprint(stderr, usage)
		return 2
	}
	return 0
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
