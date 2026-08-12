package main

import (
	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/output"
	"github.com/nathanclairmonte/lore/internal/project"
	"github.com/spf13/cobra"
)

// newDiffCmd builds the `lore diff` command, which shows how a project's
// copies differ from committed central HEAD.
func newDiffCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "diff <project>",
		Short: "Show project-copy differences from committed central HEAD",
		Long: `Show how every checkout for a project differs from committed central HEAD.

Text files use unified patches. Binary files, symlinks, type changes,
and executable changes use metadata records. Dirty central content is
reported as a health issue and suppresses patches. Ordinary differences
exit 0. Health issues exit 7.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := project.Diff(args[0])
			if err != nil {
				return err
			}
			output.PrintDiff(syncResultForOutput(result))
			if result.HasIssues() {
				return lerr.New(lerr.CodeInconsistency, "diff found unhealthy checkout state")
			}
			return nil
		},
	}
}
