package main

import (
	"github.com/nathanclairmonte/lore/internal/output"
	"github.com/nathanclairmonte/lore/internal/project"
	"github.com/spf13/cobra"
)

// newPullCmd builds the central-copy to project-copy command.
func newPullCmd() *cobra.Command {
	var opts project.PullOptions
	cmd := &cobra.Command{
		Use:   "pull [project]",
		Short: "Receive committed central changes into project copies",
		Long: `Copy committed central content into project copies.

Bare pull targets the checkout containing the current directory. A named
pull targets every recorded checkout for that project. Use --path to adopt
or correct one exact checkout. Use --all to scan configured project roots.

This command handles missing, matching, and central-ahead copies. It refuses
divergence rather than overwriting either side. Pull never contacts a Git
remote. Use plain Git in the central repository for remote pulls.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				opts.Name = args[0]
			}
			result, err := project.Pull(opts)
			// a failure before any project resolved leaves an empty result, which
			// would otherwise render as "no tracked projects" ahead of the error.
			if err == nil || len(result.Projects) > 0 {
				output.PrintPull(pullResultForOutput(result))
			}
			if output.GetMode() != output.ModeJSON {
				if err == nil && !opts.All {
					project.OfferPullGitignoreFixes(&result)
				}
				output.PrintPullGitignoreWarnings(pullResultForOutput(result))
			}
			return err
		},
	}
	cmd.Flags().StringVar(&opts.Path, "path", "", "use or adopt one exact checkout path")
	cmd.Flags().BoolVar(&opts.All, "all", false, "scan projects_roots and pull every tracked project found")
	return cmd
}

func pullResultForOutput(result project.PullResult) output.PullResult {
	out := output.PullResult{
		CentralRepo: result.CentralRepo, CentralHead: result.CentralHead,
		ScannedRoots: copyStrings(result.ScannedRoots), Projects: []output.PullProjectResult{},
	}
	for _, projectResult := range result.Projects {
		projectOut := output.PullProjectResult{
			Name: projectResult.Name, Status: output.PullProjectStatus(projectResult.Status),
			Matches: copyStrings(projectResult.Matches), Checkouts: []output.PullCheckoutResult{},
		}
		if projectResult.Err != nil {
			projectOut.Error = projectResult.Err.Error()
		}
		for _, checkout := range projectResult.Checkouts {
			checkoutOut := output.PullCheckoutResult{
				Path: checkout.Path, Status: output.PullCheckoutStatus(checkout.Status), SyncBase: checkout.SyncBase,
				Dirs: []output.PullDirResult{},
			}
			if checkout.Err != nil {
				checkoutOut.Error = checkout.Err.Error()
			}
			for _, dir := range checkout.Dirs {
				checkoutOut.Dirs = append(checkoutOut.Dirs, output.PullDirResult{
					Name: dir.Name, Path: dir.Path, Status: output.PullDirStatus(dir.Status), Unignored: dir.Unignored,
				})
			}
			projectOut.Checkouts = append(projectOut.Checkouts, checkoutOut)
		}
		out.Projects = append(out.Projects, projectOut)
	}
	return out
}

// copyStrings copies in without ever returning nil, so the JSON envelope
// renders an empty array rather than null.
func copyStrings(in []string) []string {
	return append(make([]string, 0, len(in)), in...)
}
