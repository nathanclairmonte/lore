package main

import (
	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/output"
	"github.com/nathanclairmonte/lore/internal/project"
	"github.com/spf13/cobra"
)

// newUnlinkCmd builds the `lore unlink` cobra subcommand.
func newUnlinkCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "unlink [name]",
		Short: "Remove a project's on-disk symlinks without untracking it from central",
		Long: `Removes the symlinks lore created for a tracked project at its
recorded local-state path, drops the project entry from local
state, and leaves central untouched. The project stays
tracked in the central repo and shows up in 'lore ls'; only
this machine's bookkeeping changes.

Refuses to touch anything at the recorded path that isn't a
lore-created symlink (exit 5 with a clear message), so a real
directory or a manually-repointed symlink is never destroyed
by accident.

Use --all to unlink every tracked project on this machine in
one go. Per-project refusals don't abort the loop; the run
reports the verdict for each project and exits with the worst
code seen.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if all && len(args) > 0 {
				return lerr.New(lerr.CodeGenericError,
					"--all and an explicit project name are mutually exclusive").
					WithHint("pass one or the other, not both")
			}
			if !all && len(args) == 0 {
				return lerr.New(lerr.CodeGenericError,
					"unlink requires a project name (or --all to unlink every tracked project)")
			}

			if all {
				res, err := project.UnlinkAll()
				if len(res.Projects) > 0 {
					output.PrintUnlinkAllResult(unlinkAllResultForOutput(res))
				}
				return err
			}

			res, err := project.Unlink(args[0])
			// only render when the orchestrator produced something
			// meaningful: a refusal returns the zero-value result, and
			// printing its empty header is misleading. mirror the slice 6
			// add.go gate.
			if res.ProjectName != "" || res.AlreadyUnlinked {
				output.PrintUnlinkResult(unlinkResultForOutput(res))
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&all, "all", false,
		"unlink every tracked project on this machine")
	return cmd
}

// unlinkDirsForOutput translates the orchestrator's per-dir verdicts
// into the output layer's shape. Reused by unlinkResultForOutput,
// rmConfirmationView, and rmResultForOutput so a future addition to
// UnlinkDirResult only needs one site updated.
func unlinkDirsForOutput(in []project.UnlinkDirResult) []output.UnlinkDirResult {
	out := make([]output.UnlinkDirResult, 0, len(in))
	for _, d := range in {
		out = append(out, output.UnlinkDirResult{
			Name:     d.Name,
			Status:   output.UnlinkDirStatus(d.Status),
			LinkPath: d.LinkPath,
		})
	}
	return out
}

// unlinkResultForOutput translates the orchestrator's UnlinkResult
// into the output layer's shape. The two structs are intentionally
// separate so the output package doesn't import project.
func unlinkResultForOutput(r project.UnlinkResult) output.UnlinkResult {
	return output.UnlinkResult{
		ProjectName:     r.ProjectName,
		ProjectPath:     r.ProjectPath,
		Dirs:            unlinkDirsForOutput(r.Dirs),
		AlreadyUnlinked: r.AlreadyUnlinked,
	}
}

// unlinkAllResultForOutput translates the orchestrator's
// UnlinkAllResult into the output layer's shape.
func unlinkAllResultForOutput(r project.UnlinkAllResult) output.UnlinkAllResult {
	projects := make([]output.UnlinkProjectResult, 0, len(r.Projects))
	for _, p := range r.Projects {
		projects = append(projects, output.UnlinkProjectResult{
			UnlinkResult: unlinkResultForOutput(p.UnlinkResult),
			Err:          p.Err,
		})
	}
	return output.UnlinkAllResult{Projects: projects}
}
