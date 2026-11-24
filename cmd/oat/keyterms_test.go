package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/matheuscamposmt/oat/internal/keyterms"
	"github.com/matheuscamposmt/oat/internal/stt"
)

// writeConfig writes config.toml in the XDG_CONFIG_HOME of the test.
func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "oat", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

type optsRecorder struct {
	mu   sync.Mutex
	opts []stt.Options
}

func (r *optsRecorder) Transcribe(ctx context.Context, wav []byte, opts stt.Options) (stt.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.opts = append(r.opts, opts)
	return stt.Result{Text: "ok"}, nil
}

func TestTranscribeUsesTheKeytermList(t *testing.T) {
	pcm := filepath.Join(t.TempDir(), "pcm")
	if err := os.WriteFile(pcm, tone(11), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := &optsRecorder{}
	fakeFFmpeg(t, "cat '"+pcm+"'", rec)
	writeConfig(t, "[keyterms.cvm]\nterms = [\"RAIS\", \"LabContas\"]\n")
	var out, errOut bytes.Buffer
	if code := run([]string{"transcribe", "--keyterms-set", "cvm", "--keyterms", "Entra ID", audioFile(t, "a.ogg")}, &out, &errOut); code != 0 {
		t.Fatalf("got %d %q", code, errOut.String())
	}
	got := strings.Join(rec.opts[0].Keyterms, ",")
	if !strings.HasPrefix(got, "Entra ID,RAIS,LabContas,MCP,") {
		t.Fatalf("keyterms %q", got)
	}
}

func TestUnknownKeytermSetStops(t *testing.T) {
	for _, args := range [][]string{{"new", "--keyterms-set", "none"}, {"transcribe", "--keyterms-set", "none", audioFile(t, "a.ogg")}} {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), `no key term list "none"`) {
			t.Errorf("%v: got %d %q", args, code, errOut.String())
		}
	}
}

func TestMeetingKeytermsPickTheListByTitle(t *testing.T) {
	lists := []keyterms.List{{Name: "nexo", Terms: []string{"NEXO"}}, {Name: "cvm", Terms: []string{"RAIS"}}}
	got, err := keytermsFor(lists, "GST, Entra ID", "", "Sync da CVM")
	if err != nil || !strings.HasPrefix(strings.Join(got, ","), "GST,Entra ID,RAIS,Sync,CVM,MCP,") {
		t.Fatalf("got %q %v", got, err)
	}
}

func runOK(t *testing.T, args ...string) string {
	t.Helper()
	var out, errOut bytes.Buffer
	if code := run(args, &out, &errOut); code != 0 {
		t.Fatalf("%v: got %d %q", args, code, errOut.String())
	}
	return out.String()
}

func TestKeytermsAddAndRemove(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := writeConfig(t, "# my settings\nlang = \"en\"\n")
	runOK(t, "keyterms", "add", "cvm", "RAIS")
	runOK(t, "keyterms", "add", "cvm", "Entra ID", "rais", "DBeaver")
	runOK(t, "keyterms", "rm", "cvm", "dbeaver")
	data, _ := os.ReadFile(path)
	if string(data) != "# my settings\nlang = \"en\"\n\n[keyterms.cvm]\nterms = [\"Entra ID\", \"RAIS\"]\n" {
		t.Fatalf("config:\n%s", data)
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"keyterms", "rm", "none", "X"}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), `no key term list "none"`) {
		t.Fatalf("rm from a missing list: %d %q", code, errOut.String())
	}
}

func TestKeytermsList(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if out := runOK(t, "keyterms", "list"); !strings.Contains(out, "No key term lists") {
		t.Fatalf("no lists: %q", out)
	}
	writeConfig(t, "[keyterms.cvm]\nfolder = \"/src/cvm\"\nterms = [\"RAIS\", \"Vinícius\"]\n[keyterms.nexo]\nterms = [\"NEXO\"]\n")
	out := runOK(t, "keyterms", "list")
	if !strings.Contains(out, "cvm") || !strings.Contains(out, "2 terms") || !strings.Contains(out, "/src/cvm") || !strings.Contains(out, "nexo") {
		t.Fatalf("lists: %q", out)
	}
	out = runOK(t, "keyterms", "list", "cvm")
	for _, want := range []string{"RAIS", "list cvm", "MCP", "base", "Vinícius"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}
	if !strings.Contains(out, "accent") || strings.Contains(out, "limit") {
		t.Fatalf("warnings: %q", out)
	}
	var many []string
	for i := range 55 {
		many = append(many, fmt.Sprintf("\"T%d\"", i))
	}
	writeConfig(t, "[keyterms.big]\nterms = ["+strings.Join(many, ", ")+"]\n")
	if out := runOK(t, "keyterms", "list", "big"); !strings.Contains(out, "limit") || !strings.Contains(out, "T54") {
		t.Fatalf("limit warning: %q", out)
	}
}

// fakeScan replaces the Claude scan and stdin.
func fakeScan(t *testing.T, found []string, input string) *[]string {
	t.Helper()
	var folders []string
	oldScanner, oldStdin := newScanner, stdin
	t.Cleanup(func() { newScanner, stdin = oldScanner, oldStdin })
	newScanner = func() (keyterms.Scanner, error) {
		return func(ctx context.Context, folder string) ([]string, error) {
			folders = append(folders, folder)
			return found, nil
		}, nil
	}
	stdin = strings.NewReader(input)
	return &folders
}

func TestKeytermsScanSavesTheConfirmedTerms(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	folder := filepath.Join(t.TempDir(), "cvm-app")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	folders := fakeScan(t, []string{"RAIS", "Lab Contas", "Neo4j"}, "2\ny\n")
	out := runOK(t, "keyterms", "scan", folder)
	if !strings.Contains(out, " 2. Lab Contas") {
		t.Fatalf("no numbered list in %q", out)
	}
	lists, _ := keyterms.Load(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "oat", "config.toml"))
	if len(lists) != 1 || lists[0].Name != "cvm-app" || lists[0].Folder != folder || strings.Join(lists[0].Terms, ",") != "RAIS,Neo4j" {
		t.Fatalf("lists %+v", lists)
	}
	if len(*folders) != 1 || (*folders)[0] != folder {
		t.Fatalf("scanned %q", *folders)
	}
}

func TestKeytermsScanWithoutYesSavesNothing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	fakeScan(t, []string{"RAIS"}, "\n")
	if out := runOK(t, "keyterms", "scan", t.TempDir(), "--name", "cvm"); !strings.Contains(out, "Not saved") {
		t.Fatalf("got %q", out)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "oat", "config.toml")); err == nil {
		t.Fatal("the scan wrote config.toml")
	}
}

func TestKeytermsScanAllKeepsTheOldTerms(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	folder := t.TempDir()
	path := writeConfig(t, "[keyterms.cvm]\nfolder = \""+folder+"\"\nterms = [\"RAIS\"]\n\n[keyterms.nexo]\nterms = [\"NEXO\"]\n")
	folders := fakeScan(t, []string{"Neo4j", "rais"}, "\ny\n")
	runOK(t, "keyterms", "scan", "--all")
	lists, _ := keyterms.Load(path)
	if strings.Join(lists[0].Terms, ",") != "RAIS,Neo4j" || len(*folders) != 1 {
		t.Fatalf("lists %+v, scanned %q", lists, *folders)
	}
}
