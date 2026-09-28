package mcpserver

import (
	"context"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/matheuscamposmt/oat/internal/store"
)

var day1 = time.Date(2026, 9, 22, 10, 0, 0, 0, time.Local)

func connect(t *testing.T, st *store.Store) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	ct, stt := mcp.NewInMemoryTransports()
	if _, err := New(st, "test").Connect(ctx, stt, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return res.Content[0].(*mcp.TextContent).Text, res.IsError
}

// done creates a finished meeting.
func done(t *testing.T, st *store.Store, title string, start time.Time, segs []store.Segment, notes string) *store.Meeting {
	t.Helper()
	m, err := st.Create(title, start, "pt", "whisper-large-v3-turbo", "auto")
	if err != nil {
		t.Fatal(err)
	}
	m.AppendSegments(segs)
	if notes != "" {
		m.WriteNotes(notes)
	}
	m.Update(func(meta *store.Meta) { meta.Status = store.Done; meta.EndedAt = start.Add(time.Hour) })
	m.Unlock()
	return m
}

func fixture(t *testing.T) (*store.Store, *store.Meeting, *store.Meeting) {
	st := store.New(t.TempDir())
	weekly := done(t, st, "Weekly sync", day1, []store.Segment{
		{Start: 1, End: 3, Speaker: store.Them, Text: "Então, sobre o prazo"},
		{Start: 3.5, End: 4, Speaker: store.Me, Text: "Acho que dá"},
	}, "- prazo: outubro\n")
	plan := done(t, st, "Planejamento", day1.Add(24*time.Hour), nil, "")
	return st, weekly, plan
}

func TestListsFourTools(t *testing.T) {
	st, _, _ := fixture(t)
	res, err := connect(t, st).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "get_meeting,list_meetings,save_summary,search_meetings" {
		t.Fatalf("tools %v", names)
	}
}

func TestListMeetings(t *testing.T) {
	st, weekly, plan := fixture(t)
	cs := connect(t, st)
	out, isErr := call(t, cs, "list_meetings", map[string]any{})
	if isErr || strings.Index(out, plan.Meta.ID) > strings.Index(out, weekly.Meta.ID) || !strings.Contains(out, "notes: yes") {
		t.Fatalf("list:\n%s", out)
	}
	out, _ = call(t, cs, "list_meetings", map[string]any{"query": "WEEKLY"})
	if strings.Contains(out, plan.Meta.ID) || !strings.Contains(out, weekly.Meta.ID) {
		t.Fatalf("filtered list:\n%s", out)
	}
}

func TestGetMeeting(t *testing.T) {
	st, weekly, plan := fixture(t)
	cs := connect(t, st)
	out, isErr := call(t, cs, "get_meeting", map[string]any{"id": weekly.Meta.ID})
	for _, want := range []string{"# Weekly sync", "## Notes\n- prazo: outubro", "[00:00:01] Them: Então, sobre o prazo", "[00:00:03] Me: Acho que dá"} {
		if isErr || !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	out, _ = call(t, cs, "get_meeting", map[string]any{"id": "latest"})
	if !strings.Contains(out, plan.Meta.ID) || !strings.Contains(out, "(no lines yet)") {
		t.Fatalf("latest:\n%s", out)
	}
	out, isErr = call(t, cs, "get_meeting", map[string]any{"id": "nope"})
	if !isErr || !strings.Contains(out, "list_meetings") {
		t.Fatalf("unknown ID: %v %s", isErr, out)
	}
}

func TestGetLiveMeeting(t *testing.T) {
	st, _, _ := fixture(t)
	cs := connect(t, st)
	out, isErr := call(t, cs, "get_meeting", map[string]any{"id": "live"})
	if !isErr || !strings.Contains(out, "No recording in progress.") {
		t.Fatalf("no live meeting: %v %s", isErr, out)
	}
	live, err := st.Create("Now", time.Now(), "pt", "m", "off")
	if err != nil {
		t.Fatal(err)
	}
	live.AppendSegments([]store.Segment{
		{Start: 1, End: 1.5, Speaker: store.Them, Text: "primeira"},
		{Start: 5, End: 6, Speaker: store.Me, Text: "segunda"},
	})
	out, isErr = call(t, cs, "get_meeting", map[string]any{"id": "live", "since": 2})
	if isErr || strings.Contains(out, "primeira") || !strings.Contains(out, "segunda") {
		t.Fatalf("live since 2:\n%s", out)
	}
}

func TestGetMeetingPages(t *testing.T) {
	st := store.New(t.TempDir())
	var segs []store.Segment
	for i := range 5 {
		sp := store.Me
		if i%2 == 1 {
			sp = store.Them
		}
		segs = append(segs, store.Segment{Start: float64(i * 10), End: float64(i*10 + 1), Speaker: sp, Text: "linha " + string(rune('a'+i))})
	}
	m := done(t, st, "Long", day1, segs, "")
	cs := connect(t, st)
	out, _ := call(t, cs, "get_meeting", map[string]any{"id": m.Meta.ID, "limit": 2})
	if !strings.Contains(out, "lines 0 to 1 of 5") || !strings.Contains(out, "next_offset: 2") || strings.Contains(out, "linha c") {
		t.Fatalf("page 1:\n%s", out)
	}
	out, _ = call(t, cs, "get_meeting", map[string]any{"id": m.Meta.ID, "offset": 4, "limit": 2})
	if !strings.Contains(out, "linha e") || strings.Contains(out, "next_offset") || strings.Contains(out, "## Notes") {
		t.Fatalf("last page:\n%s", out)
	}
}

// alternating returns n segments that alternate between the speakers, so each is one line.
func alternating(n int) []store.Segment {
	var segs []store.Segment
	for i := range n {
		sp := store.Me
		if i%2 == 1 {
			sp = store.Them
		}
		segs = append(segs, store.Segment{Start: float64(i * 10), End: float64(i*10 + 1), Speaker: sp, Text: fmt.Sprintf("linha %d", i)})
	}
	return segs
}

func TestGetMeetingHugeLimit(t *testing.T) {
	st := store.New(t.TempDir())
	short := done(t, st, "Short", day1, alternating(3), "")
	// The SDK decodes the arguments through float64, so math.MaxInt64 cannot
	// come over the protocol. Call the handler directly.
	res, _, err := (&handlers{st: st}).get(context.Background(), nil, getIn{ID: short.Meta.ID, Offset: 1, Limit: math.MaxInt64})
	if err != nil {
		t.Fatal(err)
	}
	out := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(out, "lines 1 to 2 of 3") || !strings.Contains(out, "linha 1") || !strings.Contains(out, "linha 2") ||
		strings.Contains(out, "linha 0") || strings.Contains(out, "next_offset") {
		t.Fatalf("offset 1, limit MaxInt64:\n%s", out)
	}

	// Over the protocol, a limit near 2^63 with a large offset also overflows.
	long := done(t, st, "Long", day1.Add(time.Hour), alternating(1000), "")
	out, isErr := call(t, connect(t, st), "get_meeting", map[string]any{"id": long.Meta.ID, "offset": 900, "limit": int64(9223372036854775000)})
	if isErr || !strings.Contains(out, "lines 900 to 999 of 1000") || strings.Contains(out, "next_offset") {
		t.Fatalf("offset 900, limit near 2^63:\n%.300s", out)
	}
}

func TestSearchMeetings(t *testing.T) {
	st, weekly, _ := fixture(t)
	out, isErr := call(t, connect(t, st), "search_meetings", map[string]any{"query": "PRAZO"})
	if isErr || !strings.Contains(out, weekly.Meta.ID+" | [00:00:01] Them: Então, sobre o prazo") || !strings.Contains(out, "| notes | - prazo: outubro") {
		t.Fatalf("search:\n%s", out)
	}
}

func TestSaveSummary(t *testing.T) {
	st, weekly, _ := fixture(t)
	cs := connect(t, st)
	out, isErr := call(t, cs, "save_summary", map[string]any{"id": weekly.Meta.ID, "markdown": "# Notas\n"})
	if isErr || !strings.Contains(out, "summary.md") {
		t.Fatalf("save: %v %s", isErr, out)
	}
	data, _ := os.ReadFile(weekly.SummaryPath())
	if string(data) != "# Notas\n" {
		t.Fatalf("summary.md = %q", data)
	}
	out, _ = call(t, cs, "get_meeting", map[string]any{"id": weekly.Meta.ID})
	if !strings.Contains(out, "## Summary\n# Notas") {
		t.Fatalf("get after save:\n%s", out)
	}
	if _, isErr := call(t, cs, "save_summary", map[string]any{"id": weekly.Meta.ID, "markdown": " "}); !isErr {
		t.Fatal("saved an empty summary")
	}
}

func TestEnhancePrompt(t *testing.T) {
	st, _, _ := fixture(t)
	res, err := connect(t, st).GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "enhance", Arguments: map[string]string{"id": "abc"}})
	if err != nil {
		t.Fatal(err)
	}
	msg := res.Messages[0].Content.(*mcp.TextContent).Text
	if !strings.Contains(msg, `get_meeting tool of the oat server with id "abc"`) || !strings.Contains(msg, "save_summary") {
		t.Fatalf("prompt:\n%s", msg)
	}
}
