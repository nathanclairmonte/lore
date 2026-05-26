package main

import (
	"github.com/spf13/cobra"
)

// newRootCmd builds the top-level cobra command tree. Returning it from a
// function (instead of using a package-level var) keeps state out of
// init() and makes wiring easy to test.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "lore",
		Short: "Version-controlled per-project agentic AI context",
		Long: "lore keeps every tracked project's agent directories (.claude/, " +
			".cursor/, .agents/, ...) in a single private central repository " +
			"and symlinks them back into each project on disk.",
		// silence cobra's default error/usage printing; main.go owns
		// rendering errors through the output module.
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.AddCommand(newInitCmd())
	root.AddCommand(newAddCmd())
	root.AddCommand(newPathCmd())
	return root
}
