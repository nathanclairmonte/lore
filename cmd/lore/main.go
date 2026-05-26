// Command lore is the CLI entry point. It wires the cobra command tree,
// runs the requested subcommand, and translates any returned error into
// the global semantic exit code (0-7) via errors.CodeOf.
package main

import (
	"os"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/output"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

// run executes the root command with the given args and returns the
// process exit code. Extracted from main so tests can drive the CLI
// without os.Exit'ing the test runner.
func run(args []string) int {
	root := newRootCmd()
	root.SetArgs(args)
	err := root.Execute()
	if err != nil {
		output.PrintError(err)
	}
	return int(lerr.CodeOf(err))
}
