package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionAndHelp(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"version"}, &out, &errOut); code != 0 || !strings.HasPrefix(out.String(), "oat ") {
		t.Fatalf("version: %d %q", code, out.String())
	}
	out.Reset()
	if code := run([]string{"help"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "oat new [title]") {
		t.Fatalf("help: %d %q", code, out.String())
	}
}

func TestUnknownCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"bogus"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), `unknown command "bogus"`) {
		t.Fatalf("got %d %q", code, errOut.String())
	}
}
