// Command oat records meetings from the terminal and transcribes them with Groq.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

var version = "dev"

const usage = `oat records meetings from the terminal and transcribes them with Groq.

Usage:
  oat                  open the list of meetings
  oat new [title]      start a recording now
  oat mcp              run the MCP server (Claude Code starts it)
  oat doctor           test the environment
  oat setup            register the MCP server in Claude Code
  oat version          print the version
`

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

// run is the whole program. Task 12 adds the other commands.
func run(args []string, stdout, stderr io.Writer) int {
	cmd := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd = args[0]
	}
	switch cmd {
	case "version":
		fmt.Fprintln(stdout, "oat", version)
		return 0
	case "help":
		fmt.Fprint(stdout, usage)
		return 0
	}
	fmt.Fprintf(stderr, "oat: unknown command %q\n\n%s", cmd, usage)
	return 2
}
