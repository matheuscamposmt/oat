package main

import (
	"bytes"
	"context"
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
