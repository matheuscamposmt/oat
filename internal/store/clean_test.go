package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanSortsAndJoins(t *testing.T) {
	got := Clean([]Segment{
		{10, 12, Them, "a"},
		{0, 2, Me, " x "},
		{12.5, 14, Them, "b"},
		{20, 21, Me, "y"},
		{30, 31, Me, "   "},
	})
	want := []Segment{{0, 2, Me, "x"}, {10, 14, Them, "a b"}, {20, 21, Me, "y"}}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("segment %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestCleanDropsEcho(t *testing.T) {
	got := Clean([]Segment{
		{10, 14, Them, "Então, sobre o prazo da entrega"},
		{10.3, 14.2, Me, "então sobre o prazo da entrega"},
	})
	if len(got) != 1 || got[0].Speaker != Them {
		t.Fatalf("got %+v", got)
	}
}

func TestCleanKeepsRealSpeech(t *testing.T) {
	them := Segment{10, 14, Them, "sim, claro, sobre o prazo"}
	cases := map[string]Segment{
		"short":     {11, 12, Me, "sim claro"},
		"far":       {30, 34, Me, "sim, claro, sobre o prazo"},
		"different": {11, 14, Me, "acho que dá se a API ficar pronta"},
	}
	for name, me := range cases {
		got := Clean([]Segment{them, me})
		if len(got) != 2 {
			t.Errorf("%s: dropped the Me segment: %+v", name, got)
		}
	}
}

func TestTranscriptLines(t *testing.T) {
	segs := []Segment{{725, 727, Me, "Acho que dá"}}
	if got := TranscriptLines(segs, false)[0]; got != "[00:12:05] Me: Acho que dá" {
		t.Fatalf("plain: %q", got)
	}
	if got := TranscriptLines(segs, true)[0]; got != "[00:12:05] **Me:** Acho que dá" {
		t.Fatalf("bold: %q", got)
	}
}

func TestWriteTranscriptMD(t *testing.T) {
	m := newMeeting(t, New(t.TempDir()), "Weekly", t1430)
	m.AppendSegments([]Segment{{725, 727, Me, "Acho que dá"}, {700, 702, Them, "Então"}})
	if err := m.WriteTranscriptMD(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(m.Dir, "transcript.md"))
	text := string(data)
	if !strings.HasPrefix(text, "# Weekly\n") || strings.Index(text, "**Them:** Então") > strings.Index(text, "[00:12:05] **Me:** Acho que dá") {
		t.Fatalf("transcript.md:\n%s", text)
	}
}

func TestSearchIgnoresCaseAndAccents(t *testing.T) {
	st := New(t.TempDir())
	m := newMeeting(t, st, "Budget", t1430)
	m.AppendSegments([]Segment{{5, 7, Them, "Reunião sobre o orçamento"}, {9, 10, Me, "outra coisa"}})
	m.WriteNotes("- revisar Orçamento\n- nada\n")
	hits, err := st.Search("ORCAMENTO", 10)
	if err != nil || len(hits) != 2 {
		t.Fatalf("got %+v %v", hits, err)
	}
	if hits[0].At != 5 || hits[0].Speaker != Them || hits[1].At != -1 || hits[1].Text != "- revisar Orçamento" {
		t.Fatalf("got %+v", hits)
	}
	if hits, _ := st.Search("orçamento", 1); len(hits) != 1 {
		t.Fatalf("limit: got %d hits", len(hits))
	}
	if _, err := st.Search("  ", 10); err == nil {
		t.Fatal("empty query must fail")
	}
}

func TestCleanDropsEmptyText(t *testing.T) {
	got := Clean([]Segment{{1, 2, Me, "   "}, {3, 4, Them, "oi"}})
	if len(got) != 1 || got[0].Text != "oi" {
		t.Fatalf("got %+v", got)
	}
}

func TestCleanDoesNotJoinAcrossALongGap(t *testing.T) {
	got := Clean([]Segment{{0, 1, Me, "um"}, {3, 4, Me, "dois"}})
	if len(got) != 2 {
		t.Fatalf("joined across a 2 s gap: %+v", got)
	}
}
