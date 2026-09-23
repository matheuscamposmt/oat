package store

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

var t1430 = time.Date(2026, 9, 23, 14, 30, 0, 0, time.Local)

// deadPID is above the Linux PID limit, so no process has it.
const deadPID = 999999999

func newMeeting(t *testing.T, st *Store, title string, start time.Time) *Meeting {
	t.Helper()
	m, err := st.Create(title, start, "pt", "whisper-large-v3-turbo", "auto")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestCreateMakesFolderLockAndMeta(t *testing.T) {
	st := New(t.TempDir())
	m := newMeeting(t, st, "Weekly sync: Acme", t1430)
	if m.Meta.ID != "2026-09-23-1430-weekly-sync-acme" {
		t.Fatalf("ID = %q", m.Meta.ID)
	}
	if m.Meta.Status != Recording {
		t.Fatalf("Status = %q", m.Meta.Status)
	}
	if m.LockPID() != os.Getpid() {
		t.Fatalf("LockPID = %d, want %d", m.LockPID(), os.Getpid())
	}
	for _, p := range []string{filepath.Join(m.Dir, "meta.json"), m.ChunksDir()} {
		if _, err := os.Stat(p); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := st.Load(m.Meta.ID)
	if err != nil || loaded.Meta.Title != "Weekly sync: Acme" || !loaded.Meta.StartedAt.Equal(t1430) {
		t.Fatalf("Load: %+v %v", loaded, err)
	}
}

func TestCreateCollisionAddsSuffix(t *testing.T) {
	st := New(t.TempDir())
	newMeeting(t, st, "Sync", t1430)
	m := newMeeting(t, st, "Sync", t1430)
	if m.Meta.ID != "2026-09-23-1430-sync-2" {
		t.Fatalf("ID = %q", m.Meta.ID)
	}
}

func TestCreateEmptyTitle(t *testing.T) {
	m := newMeeting(t, New(t.TempDir()), "  ", t1430)
	if m.Meta.Title != "Meeting 14:30" || m.Meta.ID != "2026-09-23-1430-meeting-14-30" {
		t.Fatalf("got %q %q", m.Meta.Title, m.Meta.ID)
	}
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Reunião de Diretoria": "reuniao-de-diretoria",
		"  !!! ":               "meeting",
		"Q4 / Planejamento":    "q4-planejamento",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
	long := Slug(strings.Repeat("abcde fghij ", 6))
	if len(long) > 40 || strings.HasSuffix(long, "-") {
		t.Errorf("long slug %q", long)
	}
}

func TestLoadMarksDeadRecordingInterrupted(t *testing.T) {
	st := New(t.TempDir())
	m := newMeeting(t, st, "Crash", t1430)
	if err := os.WriteFile(filepath.Join(m.Dir, ".lock"), []byte(strconv.Itoa(deadPID)), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := st.Load(m.Meta.ID)
	if err != nil || loaded.Meta.Status != Interrupted {
		t.Fatalf("got %v %v", loaded.Meta.Status, err)
	}
	again, _ := st.Load(m.Meta.ID)
	if again.Meta.Status != Interrupted {
		t.Fatal("the new status was not saved")
	}
}

func TestLoadRejectsBadIDs(t *testing.T) {
	st := New(t.TempDir())
	for _, id := range []string{"", "..", "../etc", "a/b", "missing"} {
		if _, err := st.Load(id); !errors.Is(err, ErrNotFound) {
			t.Errorf("Load(%q) = %v, want ErrNotFound", id, err)
		}
	}
}

func TestListLatestAndLive(t *testing.T) {
	st := New(t.TempDir())
	old := newMeeting(t, st, "Old", t1430)
	if err := old.Update(func(m *Meta) { m.Status = Done }); err != nil {
		t.Fatal(err)
	}
	old.Unlock()
	cur := newMeeting(t, st, "New", t1430.Add(time.Hour))

	all, err := st.List()
	if err != nil || len(all) != 2 || all[0].Meta.ID != cur.Meta.ID {
		t.Fatalf("List: %d %v", len(all), err)
	}
	if latest, _ := st.Latest(); latest.Meta.ID != cur.Meta.ID {
		t.Fatalf("Latest = %s", latest.Meta.ID)
	}
	if live, err := st.Live(); err != nil || live.Meta.ID != cur.Meta.ID {
		t.Fatalf("Live: %v", err)
	}
	cur.Update(func(m *Meta) { m.Status = Done })
	cur.Unlock()
	if _, err := st.Live(); !errors.Is(err, ErrNoLive) {
		t.Fatalf("Live after stop: %v", err)
	}
}

func TestDeleteRefusesLiveMeeting(t *testing.T) {
	st := New(t.TempDir())
	m := newMeeting(t, st, "Live", t1430)
	if err := st.Delete(m.Meta.ID); err == nil {
		t.Fatal("deleted a live meeting")
	}
	m.Unlock()
	if err := st.Delete(m.Meta.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.Dir); !os.IsNotExist(err) {
		t.Fatal("folder still exists")
	}
}

func TestSegmentsIgnorePartialLastLine(t *testing.T) {
	m := newMeeting(t, New(t.TempDir()), "Segs", t1430)
	in := []Segment{{1, 2, Them, "olá"}, {3, 4, Me, "oi"}}
	if err := m.AppendSegments(in); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(filepath.Join(m.Dir, "transcript.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(`{"start":9`)
	f.Close()
	got, err := m.Segments()
	if err != nil || len(got) != 2 || got[0] != in[0] || got[1] != in[1] {
		t.Fatalf("got %+v %v", got, err)
	}
}

func TestNotesAndSummary(t *testing.T) {
	m := newMeeting(t, New(t.TempDir()), "Notes", t1430)
	if m.HasNotes() || m.HasSummary() {
		t.Fatal("new meeting has notes or a summary")
	}
	m.WriteNotes("  \n")
	if m.HasNotes() {
		t.Fatal("blank notes count as notes")
	}
	m.WriteNotes("- prazo: outubro\n")
	m.WriteSummary("# Summary\n")
	notes, _ := m.Notes()
	summary, _ := m.Summary()
	if notes != "- prazo: outubro\n" || summary != "# Summary\n" || !m.HasNotes() || !m.HasSummary() {
		t.Fatalf("got %q %q", notes, summary)
	}
}

func TestChunkNames(t *testing.T) {
	if got := ChunkName(Them, 712.4); got != "them-000712400.wav" {
		t.Fatalf("ChunkName = %q", got)
	}
	sp, start, ok := ParseChunkName("/x/them-000712400.wav")
	if !ok || sp != Them || start != 712.4 {
		t.Fatalf("Parse = %v %v %v", sp, start, ok)
	}
	for _, bad := range []string{".them-000712400.wav.tmp1", "x-1.wav", "me-abc.wav", "me-1.txt"} {
		if _, _, ok := ParseChunkName(bad); ok {
			t.Errorf("ParseChunkName(%q) accepted a bad name", bad)
		}
	}
}

func TestPendingChunksSorted(t *testing.T) {
	m := newMeeting(t, New(t.TempDir()), "Chunks", t1430)
	for _, name := range []string{"them-000020000.wav", "me-000005000.wav", ".me-000001000.wav.tmp9"} {
		os.WriteFile(filepath.Join(m.ChunksDir(), name), nil, 0o644)
	}
	got, err := m.PendingChunks()
	if err != nil || len(got) != 2 || filepath.Base(got[0]) != "me-000005000.wav" {
		t.Fatalf("got %v %v", got, err)
	}
}

func TestDuration(t *testing.T) {
	meta := Meta{StartedAt: t1430}
	if d := meta.Duration(t1430.Add(90 * time.Second)); d != 90*time.Second {
		t.Fatalf("open meeting: %v", d)
	}
	meta.EndedAt = t1430.Add(time.Minute)
	if d := meta.Duration(t1430.Add(time.Hour)); d != time.Minute {
		t.Fatalf("ended meeting: %v", d)
	}
}
