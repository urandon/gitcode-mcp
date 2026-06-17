package cli

import (
	"fmt"
	"io"
	"strings"
)

const version = "0.1.0"

var commands = []string{
	"sync",
	"search",
	"get",
	"link-check",
	"export",
	"diff",
}

// Execute runs the gitcode-mcp CLI.
func Execute(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		printHelp(stdout)
		return 0
	}

	if args[0] == "--version" || args[0] == "version" {
		fmt.Fprintf(stdout, "gitcode-mcp %s\n", version)
		return 0
	}

	if isKnownCommand(args[0]) {
		fmt.Fprintf(
			stdout,
			"%s: not implemented yet. See project/tasks/backlog.md for the current implementation plan.\n",
			args[0],
		)
		return 2
	}

	fmt.Fprintf(stderr, "unknown command %q\n\n", args[0])
	printHelp(stderr)
	return 2
}

func printHelp(w io.Writer) {
	fmt.Fprintln(w, "gitcode-mcp - cache-first GitCode MCP and CLI tooling")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  gitcode-mcp [command]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	for _, command := range commands {
		fmt.Fprintf(w, "  %s\n", command)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Global options:")
	fmt.Fprintln(w, "  -h, --help     Show help")
	fmt.Fprintln(w, "  --version      Show version")
}

func isKnownCommand(candidate string) bool {
	for _, command := range commands {
		if strings.EqualFold(candidate, command) {
			return true
		}
	}
	return false
}
