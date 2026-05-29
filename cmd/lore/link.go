package main

import (
	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/output"
	"github.com/nathanclairmonte/lore/internal/project"
	"github.com/spf13/cobra"
)

// newLinkCmd builds the `lore link` cobra subcommand.
func newLinkCmd() *cobra.Command {
	var (
		pathFlag string
		all      bool
	)
	cmd := &cobra.Command{
		Use:   "link [name]",
		Short: "Recreate a tracked project's symlinks into the central repo",
		Long: `Recreates the symlinks lore manages for a tracked project,
each pointing at its agent directory under projects/<name>/ in
the central repo, and records the on-disk path to local state.
Used after a fresh 'lore clone' on a new machine, after losing
local state, or any time a symlink goes missing.

Forms:

  lore link                      resolve the project from cwd
  lore link my-app               use the path recorded in local state
  lore link my-app --path <dir>  link at an explicit path (records it)

Central is never modified: the agent dirs already live there,
so nothing is committed. A correct symlink already in place is
left alone (idempotent). A real directory, or a symlink pointing
somewhere unexpected, is refused (exit 5) rather than clobbered;
the other dirs still link and the refusal is reported.

After linking, each dir is re-checked with git check-ignore. A
dir that comes up un-ignored (typical after a clone when the
original choice was .git/info/exclude) prompts to re-apply the
gitignore picker; this is advisory and never fails the run.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if all {
				if len(args) > 0 {
					return lerr.New(lerr.CodeGenericError,
						"--all and an explicit project name are mutually exclusive").
						WithHint("pass one or the other, not both")
				}
				if pathFlag != "" {
					return lerr.New(lerr.CodeGenericError,
						"--all and --path are mutually exclusive").
						WithHint("--path links one named project; --all scans your projects_roots")
				}
				res, err := project.LinkAll()
				output.PrintLinkAllResult(linkAllResultForOutput(res))
				return err
			}

			if pathFlag != "" && len(args) == 0 {
				return lerr.New(lerr.CodeGenericError,
					"--path requires a project name").
					WithHint("run `lore link <name> --path <path>`")
			}

			opts := project.LinkOptions{Path: pathFlag}
			if len(args) == 1 {
				opts.Name = args[0]
			}

			res, err := project.Link(opts)
			// render whenever the orchestrator produced a result; with
			// per-dir refusal aggregation a refused run still returns a
			// populated result, so this gate passes on refusals too.
			if res.ProjectName != "" {
				output.PrintLinkResult(linkResultForOutput(res))
			}
			return err
		},
	}
	cmd.Flags().StringVar(&pathFlag, "path", "",
		"explicit on-disk path to link at (requires a project name)")
	cmd.Flags().BoolVar(&all, "all", false,
		"scan projects_roots and link every tracked project found")
	return cmd
}

// linkDirsForOutput translates the orchestrator's per-dir verdicts into
// the output layer's shape. The two structs are intentionally separate so
// the output package doesn't import project.
func linkDirsForOutput(in []project.LinkDirResult) []output.LinkDirResult {
	out := make([]output.LinkDirResult, 0, len(in))
	for _, d := range in {
		out = append(out, output.LinkDirResult{
			Name:      d.Name,
			Status:    output.LinkDirStatus(d.Status),
			LinkPath:  d.LinkPath,
			Err:       d.Err,
			Unignored: d.Unignored,
		})
	}
	return out
}

// linkResultForOutput translates the orchestrator's LinkResult into the
// output layer's shape.
func linkResultForOutput(r project.LinkResult) output.LinkResult {
	return output.LinkResult{
		ProjectName: r.ProjectName,
		ProjectPath: r.ProjectPath,
		Dirs:        linkDirsForOutput(r.Dirs),
	}
}

// linkAllResultForOutput translates the orchestrator's LinkAllResult into
// the output layer's shape.
func linkAllResultForOutput(r project.LinkAllResult) output.LinkAllResult {
	projects := make([]output.LinkProjectResult, 0, len(r.Projects))
	for _, p := range r.Projects {
		projects = append(projects, output.LinkProjectResult{
			Name:    p.Name,
			Status:  output.LinkProjectStatus(p.Status),
			Path:    p.Path,
			Matches: p.Matches,
			Link:    linkResultForOutput(p.Link),
			Err:     p.Err,
		})
	}
	return output.LinkAllResult{
		ScannedRoots: r.ScannedRoots,
		Projects:     projects,
	}
}
