package main

import (
	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/output"
	"github.com/nathanclairmonte/lore/internal/project"
	"github.com/nathanclairmonte/lore/internal/reconcile"
	"github.com/spf13/cobra"
)

// newStatusCmd builds the `lore status` command, which reports the sync state
// of every tracked checkout without touching either copy.
func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status [project]",
		Short: "Show project and central copy sync state",
		Long: `Show the sync state of every checkout, or every checkout for one project.

Each file is classified as in-sync, project-ahead, central-ahead, or
diverged by comparing the project copy and committed central HEAD with
the checkout's sync-base. Status reads only. It exits 7 for divergence
or health issues, and exits 0 for ordinary one-sided drift.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			result, err := project.Status(name)
			if err != nil {
				return err
			}
			output.PrintStatus(syncResultForOutput(result))
			if result.Unhealthy() {
				return lerr.New(lerr.CodeInconsistency, "status found unhealthy or diverged checkouts").
					WithHint("run `lore diff <project>` to review differences")
			}
			return nil
		},
	}
}

func syncResultForOutput(result project.StatusResult) output.SyncResult {
	out := output.SyncResult{CentralRepo: result.CentralRepo, CentralHead: result.CentralHead, Projects: make([]output.SyncProjectResult, 0, len(result.Projects))}
	for _, projectResult := range result.Projects {
		projectOut := output.SyncProjectResult{
			Name: projectResult.Name, Checkouts: make([]output.SyncCheckoutResult, 0, len(projectResult.Checkouts)),
			Issues: issuesForOutput(projectResult.Issues),
		}
		for _, checkout := range projectResult.Checkouts {
			checkoutOut := output.SyncCheckoutResult{
				Path: checkout.Path, SyncBase: checkout.SyncBase, State: output.SyncState(checkout.State), BaseStale: checkout.BaseStale,
				Dirs: make([]output.SyncDirResult, 0, len(checkout.Dirs)), Issues: issuesForOutput(checkout.Issues),
			}
			for _, dir := range checkout.Dirs {
				dirOut := output.SyncDirResult{
					Name: dir.Name, State: output.SyncState(dir.State), BaseStale: dir.BaseStale,
					Files: make([]output.SyncFileResult, 0, len(dir.Files)), Issues: issuesForOutput(dir.Issues),
				}
				for _, file := range dir.Files {
					dirOut.Files = append(dirOut.Files, output.SyncFileResult{
						Path: file.Path, State: output.SyncState(file.State), ProjectChange: string(file.ProjectChange), CentralChange: string(file.CentralChange),
						Project: entryForOutput(file.Project), Central: entryForOutput(file.Central), Base: entryForOutput(file.Base),
					})
				}
				checkoutOut.Dirs = append(checkoutOut.Dirs, dirOut)
			}
			projectOut.Checkouts = append(projectOut.Checkouts, checkoutOut)
		}
		out.Projects = append(out.Projects, projectOut)
	}
	return out
}

func entryForOutput(entry *reconcile.Entry) *output.SyncEntry {
	if entry == nil {
		return nil
	}
	// reconcile hands back entries that nothing mutates, so the bytes are
	// shared rather than copied a second time.
	return &output.SyncEntry{Type: output.SyncEntryType(entry.Kind), Executable: entry.Executable, Data: entry.Data}
}

func issuesForOutput(issues []reconcile.Issue) []output.SyncIssue {
	out := make([]output.SyncIssue, 0, len(issues))
	for _, issue := range issues {
		out = append(out, output.SyncIssue{Kind: output.SyncIssueKind(issue.Kind), Path: issue.Path, Message: issue.Message})
	}
	return out
}
