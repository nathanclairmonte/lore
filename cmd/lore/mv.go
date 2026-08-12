package main

import (
	"github.com/nathanclairmonte/lore/internal/output"
	"github.com/nathanclairmonte/lore/internal/project"
	"github.com/spf13/cobra"
)

func newMvCmd() *cobra.Command {
	var pathFlag string
	cmd := &cobra.Command{
		Use:   "mv <old> <new>",
		Short: "Rename a tracked project in central",
		Long: `Rename projects/<old>/ to projects/<new>/ in central, commit the
rename, and move the local-state entry. Project copies stay in place.

Use --path when one checkout directory was moved separately. Lore updates
that checkout's recorded path but does not move files. A rename refuses when
central changes must be pulled or a checkout has unresolved health issues.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := project.Move(args[0], args[1], pathFlag)
			if result.ProjectName != "" {
				output.PrintMoveResult(moveResultForOutput(result), err == nil)
			}
			return err
		},
	}
	cmd.Flags().StringVar(&pathFlag, "path", "", "update one checkout record to this existing path")
	return cmd
}

func moveResultForOutput(result project.MoveResult) output.MoveResult {
	checkouts := make([]output.MoveCheckoutResult, 0, len(result.Checkouts))
	for _, checkout := range result.Checkouts {
		checkouts = append(checkouts, output.MoveCheckoutResult{OldPath: checkout.OldPath, Path: checkout.Path, SyncBase: checkout.SyncBase})
	}
	return output.MoveResult{
		OldName: result.OldName, ProjectName: result.ProjectName, CentralRepo: result.CentralRepo,
		CommitMessage: result.CommitMessage, Commit: result.Commit, CentralOnly: result.CentralOnly,
		Checkouts: checkouts,
	}
}
