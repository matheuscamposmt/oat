// Package mcpserver gives Claude access to the meetings through MCP.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/matheuscamposmt/oat/internal/store"
)

const defaultLines = 400

// New returns the MCP server with the oat tools and the enhance prompt.
func New(st *store.Store, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "oat", Title: "oat meeting notes", Version: version}, nil)
	h := &handlers{st: st}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_meetings",
		Description: "List the recorded meetings, newest first. Each line starts with the ID to use with get_meeting.",
	}, h.list)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_meeting",
		Description: `Read one meeting: metadata, the user's notes, the summary, and the transcript. Use id "latest" for the newest meeting or "live" for the recording in progress. Long transcripts come in pages: call again with the next_offset value.`,
	}, h.get)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "search_meetings",
		Description: "Find the lines of all transcripts and notes that contain a text. The search ignores case and accents.",
	}, h.search)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "save_summary",
		Description: "Save meeting notes in Markdown as summary.md in the meeting folder.",
	}, h.save)
	s.AddPrompt(&mcp.Prompt{
		Name:        "enhance",
		Title:       "Enhance meeting notes",
		Description: "Write Granola-style notes for a meeting from the transcript and the user's notes, then save them.",
		Arguments:   []*mcp.PromptArgument{{Name: "id", Description: "Meeting ID. The default is latest."}},
	}, h.enhance)
	return s
}

// Run serves MCP on stdin and stdout until ctx ends or the client leaves.
func Run(ctx context.Context, st *store.Store, version string) error {
	return New(st, version).Run(ctx, &mcp.StdioTransport{})
}

type handlers struct{ st *store.Store }

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

type listIn struct {
	Limit int    `json:"limit,omitempty" jsonschema:"maximum number of meetings (default 20)"`
	Query string `json:"query,omitempty" jsonschema:"only meetings whose title contains this text"`
}

func (h *handlers) list(ctx context.Context, _ *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, any, error) {
	if in.Limit <= 0 {
		in.Limit = 20
	}
	all, err := h.st.List()
	if err != nil {
		return nil, nil, err
	}
	q := store.Fold(strings.TrimSpace(in.Query))
	var b strings.Builder
	n := 0
	for _, m := range all {
		meta := m.Meta
		if q != "" && !strings.Contains(store.Fold(meta.Title), q) {
			continue
		}
		fmt.Fprintf(&b, "%s | %s | %s | %s | %s | notes: %s | summary: %s\n",
			meta.ID, meta.Title, meta.StartedAt.Format("2006-01-02 15:04"),
			store.Clock(meta.Duration(time.Now()).Seconds()), meta.Status,
			yesNo(m.HasNotes()), yesNo(m.HasSummary()))
		if n++; n >= in.Limit {
			break
		}
	}
	if n == 0 {
		return text("No meetings found."), nil, nil
	}
	return text("ID | title | start | length | status | notes | summary\n" + b.String()), nil, nil
}

type getIn struct {
	ID     string  `json:"id" jsonschema:"meeting ID, or latest, or live"`
	Since  float64 `json:"since,omitempty" jsonschema:"only transcript lines that start at or after this second of the meeting"`
	Offset int     `json:"offset,omitempty" jsonschema:"index of the first transcript line (default 0)"`
	Limit  int     `json:"limit,omitempty" jsonschema:"maximum number of transcript lines (default 400)"`
}

func (h *handlers) resolve(id string) (*store.Meeting, error) {
	switch id = strings.TrimSpace(id); id {
	case "", "latest":
		m, err := h.st.Latest()
		if err != nil {
			return nil, errors.New("No meetings yet. Record one with oat first.")
		}
		return m, nil
	case "live":
		m, err := h.st.Live()
		if err != nil {
			return nil, errors.New("No recording in progress.")
		}
		return m, nil
	}
	m, err := h.st.Load(id)
	if err != nil {
		return nil, fmt.Errorf("No meeting with ID %q. Call list_meetings to see the IDs.", id)
	}
	return m, nil
}

func (h *handlers) get(ctx context.Context, _ *mcp.CallToolRequest, in getIn) (*mcp.CallToolResult, any, error) {
	m, err := h.resolve(in.ID)
	if err != nil {
		return nil, nil, err
	}
	if in.Limit <= 0 {
		in.Limit = defaultLines
	}
	in.Offset = max(in.Offset, 0)
	meta := m.Snapshot()
	segs, err := m.Segments()
	if err != nil {
		return nil, nil, err
	}
	var shown []store.Segment
	for _, s := range store.Clean(segs) {
		if s.Start >= in.Since {
			shown = append(shown, s)
		}
	}
	lines := store.TranscriptLines(shown, false)

	var b strings.Builder
	fmt.Fprintf(&b, "# %s\nID: %s\nStart: %s | Length: %s | Status: %s | Language: %s\n",
		meta.Title, meta.ID, meta.StartedAt.Format("2006-01-02 15:04"),
		store.Clock(meta.Duration(time.Now()).Seconds()), meta.Status, meta.Lang)
	if in.Offset == 0 {
		notes, _ := m.Notes()
		b.WriteString("\n## Notes\n")
		if strings.TrimSpace(notes) == "" {
			b.WriteString("(none)\n")
		} else {
			b.WriteString(strings.TrimRight(notes, "\n") + "\n")
		}
		if summary, _ := m.Summary(); strings.TrimSpace(summary) != "" {
			b.WriteString("\n## Summary\n" + strings.TrimRight(summary, "\n") + "\n")
		}
	}
	total := len(lines)
	switch {
	case total == 0:
		b.WriteString("\n## Transcript\n(no lines yet)\n")
	case in.Offset >= total:
		fmt.Fprintf(&b, "\n## Transcript\n(no lines at offset %d. The transcript has %d lines.)\n", in.Offset, total)
	default:
		// in.Offset+in.Limit can overflow with a huge limit, so compare first.
		to := total
		if in.Limit < total-in.Offset {
			to = in.Offset + in.Limit
		}
		fmt.Fprintf(&b, "\n## Transcript (lines %d to %d of %d)\n", in.Offset, to-1, total)
		for _, l := range lines[in.Offset:to] {
			b.WriteString(l + "\n")
		}
		if to < total {
			fmt.Fprintf(&b, "\nnext_offset: %d\n", to)
		}
	}
	return text(b.String()), nil, nil
}

type searchIn struct {
	Query string `json:"query" jsonschema:"the text to find"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum number of results (default 20)"`
}

func (h *handlers) search(ctx context.Context, _ *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, any, error) {
	hits, err := h.st.Search(in.Query, in.Limit)
	if err != nil {
		return nil, nil, err
	}
	if len(hits) == 0 {
		return text("No matches."), nil, nil
	}
	var b strings.Builder
	for _, hit := range hits {
		if hit.At < 0 {
			fmt.Fprintf(&b, "%s | notes | %s\n", hit.MeetingID, hit.Text)
		} else {
			fmt.Fprintf(&b, "%s | [%s] %s: %s\n", hit.MeetingID, store.Clock(hit.At), hit.Speaker.Label(), hit.Text)
		}
	}
	return text(b.String()), nil, nil
}

type saveIn struct {
	ID       string `json:"id" jsonschema:"meeting ID, or latest"`
	Markdown string `json:"markdown" jsonschema:"the meeting notes in Markdown"`
}

func (h *handlers) save(ctx context.Context, _ *mcp.CallToolRequest, in saveIn) (*mcp.CallToolResult, any, error) {
	if strings.TrimSpace(in.Markdown) == "" {
		return nil, nil, errors.New("markdown must not be empty")
	}
	m, err := h.resolve(in.ID)
	if err != nil {
		return nil, nil, err
	}
	if err := m.WriteSummary(in.Markdown); err != nil {
		return nil, nil, err
	}
	return text("Saved " + m.SummaryPath()), nil, nil
}

func (h *handlers) enhance(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	id := "latest"
	if req != nil && req.Params != nil {
		if v := strings.TrimSpace(req.Params.Arguments["id"]); v != "" {
			id = v
		}
	}
	return &mcp.GetPromptResult{
		Description: "Enhance the notes of meeting " + id,
		Messages:    []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: EnhancePrompt(id)}}},
	}, nil
}

// EnhancePrompt returns the instructions for Granola-style meeting notes.
func EnhancePrompt(id string) string {
	return fmt.Sprintf(`Write meeting notes for the oat meeting %q.

1. Call the get_meeting tool of the oat server with id %q. If the result has a next_offset line, call it again with that offset until you have the full transcript.
2. In the transcript, "Me" is the user and "Them" is the other people.
3. Write the notes in the language of the meeting, in Markdown, with these sections:
   - Summary: 3 to 6 sentences.
   - Decisions: one line each.
   - Action items: owner, task, and due date when someone said one.
   - Notes: the user's own notes, expanded with facts from the transcript. Keep the user's words and order.
4. Use only facts from the transcript and the notes. If something is not clear, say so.
5. Show the notes to the user. Then call save_summary with the meeting ID from the get_meeting result and the Markdown.`, id, id)
}
