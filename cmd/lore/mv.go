package main

import (
	"github.com/nathanclairmonte/lore/internal/output"
	"github.com/nathanclairmonte/lore/internal/project"
	"github.com/spf13/cobra"
)

// newMvCmd builds the `lore mv` cobra subcommand.
func newMvCmd() *cobra.Command {
	var pathFlag string
	cmd := &cobra.Command{
		Use:   "mv <old> <new>",
		Short: "Rename a tracked project in central and retarget its symlinks",
		Long: `Renames projects/<old>/ to projects/<new>/ in the central repo,
retargets the symlink(s) at the project's recorded path to point
at the new location, renames the local-state entry, and commits
the rename.

By default the recorded path is kept and the symlink(s) are
repointed in place. Pass --path to also relocate the symlink(s):
the old-path symlink(s) are removed and fresh ones are created
under <new-path>, and the recorded path is updated.

lore never renames your on-disk project directory; that's yours
to move with 'mv'. Existence is judged against central, so a
project tracked in central but not linked on this machine is
still renamed (central only, no symlinks touched here).

Refuses (exit 5) rather than clobbering a real directory in the
way of a symlink, or renaming while central has uncommitted
changes under the project.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			old, newName := args[0], args[1]
			res, err := project.Move(old, newName, pathFlag)
			// render whenever the orchestrator produced a meaningful result
			// (a populated rename or the central-only not-linked-here case).
			if res.ProjectName != "" {
				output.PrintMoveResult(moveResultForOutput(res))
			}
			return err
		},
	}
	cmd.Flags().StringVar(&pathFlag, "path", "",
		"relocate the symlink(s) to this on-disk path (default: keep the recorded path)")
	return cmd
}

// moveResultForOutput translates the orchestrator's MoveResult into the
// output layer's shape. The two structs are intentionally separate so the
// output package doesn't import project.
func moveResultForOutput(r project.MoveResult) output.MoveResult {
	dirs := make([]output.MoveDirResult, 0, len(r.Dirs))
	for _, d := range r.Dirs {
		dirs = append(dirs, output.MoveDirResult{
			Name:    d.Name,
			Status:  output.MoveDirStatus(d.Status),
			OldLink: d.OldLink,
			NewLink: d.NewLink,
			Note:    d.Note,
		})
	}
	return output.MoveResult{
		OldName:       r.OldName,
		ProjectName:   r.ProjectName,
		OldPath:       r.OldPath,
		NewPath:       r.NewPath,
		CentralRepo:   r.CentralRepo,
		CommitMessage: r.CommitMessage,
		Dirs:          dirs,
		NotLinkedHere: r.NotLinkedHere,
	}
}
