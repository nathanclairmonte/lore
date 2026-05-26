package main

import (
	"github.com/nathanclairmonte/lore/internal/output"
	"github.com/nathanclairmonte/lore/internal/project"
	"github.com/spf13/cobra"
)

// newAddCmd builds the `lore add` cobra subcommand. Slice 2 ships only
// the no-flag and `<path>` forms; --dir, --name, --create, --no-commit
// arrive in slices 5/6.
func newAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add [path]",
		Short: "Move a project's agent directory into the central repo and link it back",
		Long: "Onboards a project: detects its agent directory, moves it " +
			"into the central repo at projects/<name>/<dir>/, plants an " +
			"absolute symlink back at the original location, records the " +
			"path locally, and commits using commit.add_template.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var opts project.AddOptions
			if len(args) == 1 {
				opts.ProjectPath = args[0]
			}
			res, err := project.Add(opts)
			if err != nil {
				return err
			}
			output.PrintAddResult(addResultForOutput(res))
			return nil
		},
	}
}

// addResultForOutput translates the orchestrator's AddResult into the
// output layer's AddResult. The two structs are intentionally separate so
// the output package depends only on config, not project.
func addResultForOutput(r project.AddResult) output.AddResult {
	return output.AddResult{
		ProjectName:    r.ProjectName,
		ProjectPath:    r.ProjectPath,
		AgentDirName:   r.AgentDirName,
		CentralPath:    r.CentralPath,
		CentralRepo:    r.CentralRepo,
		CommitMessage:  r.CommitMessage,
		AlreadyTracked: r.AlreadyTracked,
	}
}
