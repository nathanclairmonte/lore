package main

import (
	"github.com/nathanclairmonte/lore/internal/output"
	"github.com/nathanclairmonte/lore/internal/project"
	"github.com/spf13/cobra"
)

// newAddCmd builds the `lore add` cobra subcommand.
func newAddCmd() *cobra.Command {
	var (
		dirs   []string
		name   string
		create bool
	)
	cmd := &cobra.Command{
		Use:   "add [path]",
		Short: "Move a project's agent directories into the central repo and link them back",
		Long: `Onboards a project: detects its agent directories, moves each
into the central repo at projects/<name>/<dir>/, plants an
absolute symlink back at the original location, records the
paths locally, and commits using commit.add_template.

When the project root contains multiple agent dirs (e.g. both
.claude/ and .cursor/), all are tracked by default. Use --dir
to scope a run to a subset, or to extend an already-tracked
project with a new agent dir.

Use --name to record the project in central under a name other
than the basename of its path (useful when two projects share a
basename). Use --create to scaffold an empty agent dir if none
exists at the project root; without --dir, an interactive
picker collects the dir name and starter filename.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := project.AddOptions{
				Dirs:   dirs,
				Name:   name,
				Create: create,
			}
			if len(args) == 1 {
				opts.ProjectPath = args[0]
			}
			res, addErr := project.Add(opts)
			// render whatever the orchestrator produced before propagating
			// any error: a partial-failure run (one dir added, one
			// skipped) returns CodeRefusedSafety but its result still
			// describes what landed.
			if len(res.Dirs) > 0 {
				output.PrintAddResult(addResultForOutput(res))
			}
			return addErr
		},
	}
	cmd.Flags().StringSliceVar(&dirs, "dir", nil,
		"agent dir name to add (repeatable; default: all detected)")
	cmd.Flags().StringVar(&name, "name", "",
		"project name to record in central (default: basename of project path)")
	cmd.Flags().BoolVar(&create, "create", false,
		"scaffold an empty agent dir if none exists at the project root")
	return cmd
}

// addResultForOutput translates the orchestrator's AddResult into the
// output layer's AddResult. The two structs are intentionally separate so
// the output package depends only on config, not project.
func addResultForOutput(r project.AddResult) output.AddResult {
	dirs := make([]output.AddDirResult, 0, len(r.Dirs))
	for _, d := range r.Dirs {
		vs := make([]output.AddSafetyViolation, 0, len(d.Violations))
		for _, v := range d.Violations {
			vs = append(vs, output.AddSafetyViolation{
				Kind:    string(v.Kind),
				Message: v.Message,
			})
		}
		dirs = append(dirs, output.AddDirResult{
			Name:             d.Name,
			Status:           output.AddDirStatus(d.Status),
			CentralPath:      d.CentralPath,
			SafetyViolations: vs,
		})
	}
	return output.AddResult{
		ProjectName:       r.ProjectName,
		ProjectPath:       r.ProjectPath,
		CentralRepo:       r.CentralRepo,
		CommitMessage:     r.CommitMessage,
		Dirs:              dirs,
		ScaffoldRequested: r.ScaffoldRequested,
	}
}
