package doctor

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/matheuscamposmt/oat/internal/audio"
)

// Setup registers the MCP server in Claude Code at user scope. It does nothing
// when Claude Code already knows oat.
func Setup(ctx context.Context, run audio.Runner, lookPath func(string) (string, error), bin string, w io.Writer) error {
	addCmd := fmt.Sprintf("claude mcp add --scope user oat -- %s mcp", bin)
	if _, err := lookPath("claude"); err != nil {
		fmt.Fprintf(w, "The claude command was not found. After you install Claude Code, run:\n  %s\n", addCmd)
		return nil
	}
	if _, err := run(ctx, "claude", "mcp", "get", "oat"); err == nil {
		fmt.Fprintln(w, "The MCP server oat is already registered in Claude Code.")
		return nil
	}
	if _, err := run(ctx, "claude", "mcp", "add", "--scope", "user", "oat", "--", bin, "mcp"); err != nil {
		return fmt.Errorf("register the MCP server: %w", err)
	}
	fmt.Fprintln(w, "Registered the MCP server oat in Claude Code (user scope).")
	return nil
}

// MCPStatus returns nil when Claude Code knows the oat MCP server.
func MCPStatus(ctx context.Context, run audio.Runner, lookPath func(string) (string, error)) error {
	if _, err := lookPath("claude"); err != nil {
		return errors.New("the claude command was not found")
	}
	if _, err := run(ctx, "claude", "mcp", "get", "oat"); err != nil {
		return errors.New("oat is not registered")
	}
	return nil
}
