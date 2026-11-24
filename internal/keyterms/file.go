package keyterms

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/matheuscamposmt/oat/internal/store"
)

type tomlList struct {
	Folder string   `toml:"folder,omitempty"`
	Terms  []string `toml:"terms"`
}

// Load reads the [keyterms.NAME] tables of config.toml in file order.
// A missing file has no lists.
func Load(path string) ([]List, error) {
	var f struct {
		Keyterms map[string]tomlList `toml:"keyterms"`
	}
	md, err := toml.DecodeFile(path, &f)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var lists []List
	for _, k := range md.Keys() {
		if len(k) == 2 && k[0] == "keyterms" {
			l := f.Keyterms[k[1]]
			lists = append(lists, List{Name: k[1], Folder: l.Folder, Terms: l.Terms})
		}
	}
	return lists, nil
}

var bareName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Save writes one list to config.toml. It replaces the lines of the
// [keyterms.NAME] table, or adds the table at the end, so the other keys and
// comments stay as they are. It writes nothing when the result would change
// anything else, for example when the list is written with dotted keys.
func Save(path string, l List) error {
	if !bareName.MatchString(l.Name) {
		return fmt.Errorf("the list name %q can have only letters, digits, - and _", l.Name)
	}
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var b bytes.Buffer
	b.WriteString("[keyterms." + l.Name + "]\n")
	if err := toml.NewEncoder(&b).Encode(tomlList{Folder: l.Folder, Terms: l.Terms}); err != nil {
		return err
	}
	lines := strings.SplitAfter(string(old), "\n")
	header := regexp.MustCompile(`^\s*\[\s*keyterms\s*\.\s*"?` + l.Name + `"?\s*\]\s*(#.*)?$`)
	start := slices.IndexFunc(lines, func(s string) bool { return header.MatchString(strings.TrimRight(s, "\n")) })
	var out string
	if start < 0 {
		out = string(old)
		if out != "" {
			out = strings.TrimRight(out, "\n") + "\n\n"
		}
		out += b.String()
	} else {
		end := start + 1
		for end < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[end]), "[") {
			end++
		}
		// The comments and blank lines before the next table belong to it.
		for end > start+1 && (strings.TrimSpace(lines[end-1]) == "" || strings.HasPrefix(strings.TrimSpace(lines[end-1]), "#")) {
			end--
		}
		out = strings.Join(lines[:start], "") + b.String() + strings.Join(lines[end:], "")
	}
	if err := sameOtherwise(string(old), out, l); err != nil {
		return fmt.Errorf("cannot write the list %s to %s (%v). Edit the file by hand", l.Name, path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return store.WriteFileAtomic(path, []byte(out))
}

// sameOtherwise checks that after holds l and the same other values as before.
func sameOtherwise(before, after string, l List) error {
	var a, b map[string]any
	if _, err := toml.Decode(before, &a); err != nil {
		return err
	}
	if _, err := toml.Decode(after, &b); err != nil {
		return err
	}
	var got struct {
		Keyterms map[string]tomlList `toml:"keyterms"`
	}
	if _, err := toml.Decode(after, &got); err != nil {
		return err
	}
	if g := got.Keyterms[l.Name]; g.Folder != l.Folder || !slices.Equal(g.Terms, l.Terms) {
		return errors.New("the list did not read back")
	}
	for _, m := range []map[string]any{a, b} {
		if kt, ok := m["keyterms"].(map[string]any); ok {
			delete(kt, l.Name)
			if len(kt) == 0 {
				delete(m, "keyterms")
			}
		}
	}
	if !reflect.DeepEqual(a, b) {
		return errors.New("other values would change")
	}
	return nil
}
