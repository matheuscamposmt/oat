package keyterms

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const sample = `# oat settings
lang = "pt" # Portuguese

[keyterms.nexo]
terms = ["NEXO"]

# the CVM project
[keyterms.cvm]
folder = "/src/cvm"
terms = ["RAIS"]

# keep this comment
[other]
x = 1
`

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadKeepsTheFileOrder(t *testing.T) {
	lists, err := Load(writeFile(t, sample))
	if err != nil {
		t.Fatal(err)
	}
	want := []List{{Name: "nexo", Terms: []string{"NEXO"}}, {Name: "cvm", Folder: "/src/cvm", Terms: []string{"RAIS"}}}
	if !reflect.DeepEqual(lists, want) {
		t.Fatalf("got %+v", lists)
	}
	if lists, err := Load(filepath.Join(t.TempDir(), "none.toml")); err != nil || lists != nil {
		t.Fatalf("missing file: %v %v", lists, err)
	}
}

func TestSaveReplacesOnlyTheList(t *testing.T) {
	path := writeFile(t, sample)
	if err := Save(path, List{Name: "cvm", Folder: "/src/cvm", Terms: []string{"RAIS", "Entra ID"}}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	want := strings.Replace(sample, `terms = ["RAIS"]`, `terms = ["RAIS", "Entra ID"]`, 1)
	if string(data) != want {
		t.Fatalf("got:\n%s", data)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("mode %v, want 0600", fi.Mode())
	}
}

func TestSaveAddsANewList(t *testing.T) {
	path := writeFile(t, "lang = \"en\"")
	if err := Save(path, List{Name: "cvm-app", Terms: []string{"LabContas"}}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "lang = \"en\"\n\n[keyterms.cvm-app]\nterms = [\"LabContas\"]\n" {
		t.Fatalf("got:\n%s", data)
	}
	path = filepath.Join(t.TempDir(), "oat", "config.toml")
	if err := Save(path, List{Name: "a", Terms: []string{"B"}}); err != nil {
		t.Fatal(err)
	}
	if lists, err := Load(path); err != nil || len(lists) != 1 {
		t.Fatalf("new file: %+v %v", lists, err)
	}
}

func TestSaveRefusesWhatItCannotEdit(t *testing.T) {
	for name, tc := range map[string]struct{ file, list string }{
		"dotted keys": {"keyterms.cvm.terms = [\"RAIS\"]\n", "cvm"},
		"bad name":    {"", "my project"},
	} {
		path := writeFile(t, tc.file)
		if err := Save(path, List{Name: tc.list, Terms: []string{"X"}}); err == nil {
			t.Errorf("%s: want an error", name)
		}
		if data, _ := os.ReadFile(path); string(data) != tc.file {
			t.Errorf("%s: the file changed:\n%s", name, data)
		}
	}
}
