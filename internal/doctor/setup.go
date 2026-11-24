package doctor

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/matheuscamposmt/oat/internal/audio"
)

// PluginServer is the name of the MCP server of the oat plugin in Claude Code.
const PluginServer = "plugin:oat:oat"

// MCPState tells which oat MCP servers Claude Code knows.
type MCPState struct {
	User   bool // registered by oat setup
	Plugin bool // provided by the oat plugin
}

// Setup registers the MCP server in Claude Code at user scope. It does nothing
// when Claude Code already knows oat, from oat setup or from the plugin.
func Setup(ctx context.Context, run audio.Runner, lookPath func(string) (string, error), bin string, w io.Writer) error {
	addCmd := fmt.Sprintf("claude mcp add --scope user oat -- %s mcp", bin)
	if _, err := lookPath("claude"); err != nil {
		fmt.Fprintf(w, "The claude command was not found. After you install Claude Code, run:\n  %s\n", addCmd)
		return nil
	}
	if known(ctx, run, "oat") {
		fmt.Fprintln(w, "The MCP server oat is already registered in Claude Code.")
		return nil
	}
	if known(ctx, run, PluginServer) {
		fmt.Fprintln(w, "The oat plugin provides the MCP server oat. Nothing to register.")
		return nil
	}
	if _, err := run(ctx, "claude", "mcp", "add", "--scope", "user", "oat", "--", bin, "mcp"); err != nil {
		return fmt.Errorf("register the MCP server: %w", err)
	}
	fmt.Fprintln(w, "Registered the MCP server oat in Claude Code (user scope).")
	return nil
}

// MCPStatus tells which oat MCP servers Claude Code knows.
func MCPStatus(ctx context.Context, run audio.Runner, lookPath func(string) (string, error)) (MCPState, error) {
	if _, err := lookPath("claude"); err != nil {
		return MCPState{}, errors.New("the claude command was not found")
	}
	return MCPState{User: known(ctx, run, "oat"), Plugin: known(ctx, run, PluginServer)}, nil
}

func known(ctx context.Context, run audio.Runner, name string) bool {
	_, err := run(ctx, "claude", "mcp", "get", name)
	return err == nil
}
