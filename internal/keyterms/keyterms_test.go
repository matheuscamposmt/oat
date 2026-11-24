package keyterms

import (
	"fmt"
	"strings"
	"testing"
)

func TestTitleWords(t *testing.T) {
	got := TitleWords("Sync LabContas/rais-carga_v2.final de oAuthFlow averylongwordwithnobreaks Reunião")
	want := "Sync Lab Contas rais carga final Auth Flow"
	if strings.Join(got, " ") != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestMergeOrderAndDedupe(t *testing.T) {
	kept, dropped := Merge([]string{"Entra ID", " "}, List{Name: "cvm", Terms: []string{"RAIS", "entra id", "Neo4j"}}, "Neo4j json sync")
	var got []string
	for _, k := range kept {
		got = append(got, k.Text+"/"+k.Origin)
	}
	want := "Entra ID/--keyterms RAIS/list cvm Neo4j/list cvm json/title sync/title MCP/base OAuth/base webhook/base worktree/base subagent/base TypeScript/base localhost/base regex/base codebase/base gRPC/base"
	if strings.Join(got, " ") != want || len(dropped) != 0 {
		t.Fatalf("got %q dropped %q", got, dropped)
	}
}

func TestMergeLimits(t *testing.T) {
	var many []string
	for i := range 60 {
		many = append(many, fmt.Sprintf("T%02d", i))
	}
	kept, dropped := Merge(nil, List{Name: "big", Terms: many}, "")
	if len(kept) != MaxTerms || kept[49].Text != "T49" || len(dropped) != 10+len(Base) || dropped[0] != "T50" {
		t.Fatalf("kept %d, dropped %q", len(kept), dropped)
	}

	long := []string{strings.Repeat("x", 1000), strings.Repeat("y", 30), "short"}
	kept, dropped = Merge(long, List{}, "")
	n := len(kept) - 1
	for _, k := range kept {
		n += len(k.Text)
	}
	if n > MaxChars || kept[1].Text != "short" || dropped[0] != long[1] {
		t.Fatalf("%d chars, kept %d terms, dropped %q", n, len(kept), dropped)
	}
}

func TestPick(t *testing.T) {
	lists := []List{{Name: "nexo"}, {Name: "cvm-app"}, {Name: "cvm"}}
	for title, want := range map[string]string{
		"Reunião CVM App semanal": "cvm-app",
		"Alinhamento da CVM":      "cvm",
		"Revisar o anexo":         "",
		"NEXO: daily":             "nexo",
	} {
		l, err := Pick(lists, "", title)
		if err != nil || l.Name != want {
			t.Errorf("%q: got %q %v, want %q", title, l.Name, err, want)
		}
	}
	if l, err := Pick(lists, "cvm", "NEXO daily"); err != nil || l.Name != "cvm" {
		t.Fatalf("explicit: got %q %v", l.Name, err)
	}
	if _, err := Pick(lists, "none", ""); err == nil {
		t.Fatal("want an error for an unknown list")
	}
}
