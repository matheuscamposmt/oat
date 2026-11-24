package keyterms

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseTerms(t *testing.T) {
	got, err := ParseTerms("Here they are:\n```json\n[\"RAIS\", \" LabContas \", \"rais\", \"\", \"Entra ID\"]\n```\n")
	if err != nil || strings.Join(got, "|") != "RAIS|LabContas|Entra ID" {
		t.Fatalf("got %q %v", got, err)
	}
	var many []string
	for i := range 60 {
		many = append(many, fmt.Sprintf("%q", fmt.Sprint("T", i)))
	}
	if got, err := ParseTerms("[" + strings.Join(many, ",") + "]"); err != nil || len(got) != MaxTerms {
		t.Fatalf("got %d terms %v", len(got), err)
	}
	if _, err := ParseTerms("I could not read the folder."); err == nil {
		t.Fatal("want an error without a JSON array")
	}
}

func TestClaudeScannerRunsInTheFolderWithReadOnlyTools(t *testing.T) {
	dir := t.TempDir()
	folder := t.TempDir()
	bin := filepath.Join(dir, "claude")
	// A fake Claude Code that saves its folder, its arguments and its stdin.
	script := "#!/bin/sh\npwd > " + dir + "/pwd\necho \"$@\" > " + dir + "/args\ncat > " + dir + "/stdin\necho '[\"RAIS\"]'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := Claude(bin)(context.Background(), folder)
	if err != nil || strings.Join(got, "|") != "RAIS" {
		t.Fatalf("got %q %v", got, err)
	}
	read := func(name string) string {
		b, _ := os.ReadFile(filepath.Join(dir, name))
		return strings.TrimSpace(string(b))
	}
	if read("pwd") != folder {
		t.Fatalf("ran in %q, want %q", read("pwd"), folder)
	}
	for _, want := range []string{"-p", "--restricted", "--tools Read,Glob,Grep", "--allowed-tools Read,Glob,Grep", "--permission-mode dontAsk"} {
		if !strings.Contains(" "+read("args")+" ", " "+want+" ") {
			t.Errorf("args %q miss %q", read("args"), want)
		}
	}
	if !strings.Contains(read("stdin"), "JSON array") {
		t.Fatalf("prompt %q", read("stdin"))
	}
}

func TestParseTermsDropsAccents(t *testing.T) {
	got, err := ParseTerms(`["Supernó", "Vinícius", "superno"]`)
	if err != nil || strings.Join(got, "|") != "Superno|Vinicius" {
		t.Fatalf("got %q %v", got, err)
	}
}
