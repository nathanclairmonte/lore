package main

import (
	"github.com/nathanclairmonte/lore/internal/output"
	"github.com/spf13/cobra"
)

// newRootCmd builds the top-level cobra command tree. Returning it from a
// function (instead of using a package-level var) keeps state out of
// init() and makes wiring easy to test.
//
// Two persistent flags are wired here:
//
//   - --json: switches every data-returning command to its JSON envelope.
//     Slice 03 wires `lore path`; slice 21 extends to ls/status/doctor.
//     Human-mode printers (Success/Warn/PrintAddResult/...) become no-ops
//     under --json so script callers see only the documented envelope.
//   - --no-color: disables ANSI styling. Honored in addition to the
//     NO_COLOR env var (per no-color.org). Does NOT disable unicode
//     glyphs; that degrades automatically when stdout isn't a terminal.
func newRootCmd() *cobra.Command {
	var (
		jsonFlag    bool
		noColorFlag bool
	)
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
		// flags are parsed before RunE/subcommand RunE, so this pre-run
		// is the right place to push their values into the output module
		// (subcommand RunEs already see the final mode).
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			if jsonFlag {
				output.SetMode(output.ModeJSON)
			} else {
				output.SetMode(output.ModeHuman)
			}
			output.SetNoColor(noColorFlag)
		},
	}
	root.PersistentFlags().BoolVar(&jsonFlag, "json", false,
		"Emit machine-readable JSON instead of human-formatted output")
	root.PersistentFlags().BoolVar(&noColorFlag, "no-color", false,
		"Disable ANSI color output (NO_COLOR env var is also honored)")
	root.AddCommand(newInitCmd())
	root.AddCommand(newAddCmd())
	root.AddCommand(newPathCmd())
	root.AddCommand(newUnlinkCmd())
	root.AddCommand(newRmCmd())
	root.AddCommand(newMvCmd())
	return root
}
