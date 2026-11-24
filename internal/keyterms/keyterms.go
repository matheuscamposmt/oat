// Package keyterms builds the names and acronyms that oat sends to the
// speech-to-text backend, and keeps the named lists of config.toml.
package keyterms

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/matheuscamposmt/oat/internal/store"
)

// The limits of Claude Code for its voice key terms.
const (
	MaxTerms = 50
	MaxChars = 1024
)

// Base is the last source of terms: words of a coding talk that a speech model gets wrong.
var Base = []string{"MCP", "OAuth", "webhook", "worktree", "subagent", "JSON", "TypeScript", "localhost", "regex", "codebase", "gRPC"}

// The origins of a term, besides "list NAME".
const (
	FromFlag  = "--keyterms"
	FromTitle = "title"
	FromBase  = "base"
)

// List is a named list of config.toml.
type List struct {
	Name   string
	Folder string
	Terms  []string
}

// Term is one term and the source that gave it.
type Term struct {
	Text   string
	Origin string
}

// Merge returns the terms of a recording: the flag terms, the list, the title
// words, then Base, without case-insensitive duplicates, up to MaxTerms terms
// and MaxChars characters joined with commas. dropped has the terms past the limits.
func Merge(flag []string, list List, title string) (kept []Term, dropped []string) {
	seen := map[string]bool{}
	chars := -1
	add := func(origin string, terms []string) {
		for _, t := range terms {
			t = strings.TrimSpace(t)
			key := strings.ToLower(t)
			if t == "" || seen[key] {
				continue
			}
			seen[key] = true
			if len(kept) == MaxTerms || chars+1+len(t) > MaxChars {
				dropped = append(dropped, t)
				continue
			}
			kept = append(kept, Term{Text: t, Origin: origin})
			chars += 1 + len(t)
		}
	}
	add(FromFlag, flag)
	add("list "+list.Name, list.Terms)
	add(FromTitle, TitleWords(title))
	add(FromBase, Base)
	return kept, dropped
}

// Texts returns the text of each term.
func Texts(terms []Term) []string {
	out := make([]string, len(terms))
	for i, t := range terms {
		out[i] = t.Text
	}
	return out
}

// TitleWords splits a title as Claude Code splits a git branch: at spaces,
// "-_./", and camelCase. It keeps words of 3 to 20 characters. A word with
// accents is left out, because Claude drops the accented letters and the rest
// is a word nobody said.
func TitleWords(title string) []string {
	var words []string
	for _, w := range strings.FieldsFunc(title, func(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune("-_./", r) }) {
		start := 0
		r := []rune(w)
		for i := 1; i <= len(r); i++ {
			if i == len(r) || (unicode.IsLower(r[i-1]) && unicode.IsUpper(r[i])) {
				words = append(words, string(r[start:i]))
				start = i
			}
		}
	}
	return slices.DeleteFunc(words, func(w string) bool {
		return len(w) < 3 || len(w) > 20 || strings.IndexFunc(w, func(r rune) bool { return r > unicode.MaxASCII }) >= 0
	})
}

// Pick returns the list called name. Without a name, it returns the first
// list whose name shows in the title as whole words, with case and accents
// ignored, or no list.
func Pick(lists []List, name, title string) (List, error) {
	if name != "" {
		for _, l := range lists {
			if l.Name == name {
				return l, nil
			}
		}
		return List{}, fmt.Errorf("no key term list %q in config.toml. oat keyterms list shows the lists", name)
	}
	words := " " + strings.Join(store.Words(title), " ") + " "
	for _, l := range lists {
		if n := strings.Join(store.Words(l.Name), " "); n != "" && strings.Contains(words, " "+n+" ") {
			return l, nil
		}
	}
	return List{}, nil
}
