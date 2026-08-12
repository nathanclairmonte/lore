package main

import (
	"os"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/output"
	"github.com/nathanclairmonte/lore/internal/project"
	"github.com/nathanclairmonte/lore/internal/prompt"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var stdinIsTTY = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }
var confirmFn = prompt.Confirm

func newRmCmd() *cobra.Command {
	var force bool
	var purge bool
	cmd := &cobra.Command{
		Use:   "rm <name>",
		Short: "Stop tracking a project and remove its central copy",
		Long: `Remove a project from central, commit the deletion, and drop every
local checkout record. Project copies stay on disk by default.

Use --purge to delete every recorded project copy. Purge refuses when a
copy may contain unsynced or uninspectable work. Use --force only when you
intend to override that guard and skip confirmation.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !force && (output.GetMode() == output.ModeJSON || !stdinIsTTY()) {
				return lerr.New(lerr.CodeRefusedSafety, "interactive confirmation is unavailable in this mode").
					WithHint("re-run with --force to skip confirmation, or run interactively without --json")
			}
			plan, err := project.PreflightRm(args[0], purge, force)
			if err != nil {
				return err
			}
			// Remove closes the plan itself and Close is idempotent, so this
			// only covers the abort paths.
			defer plan.Close()
			if !force {
				// confirmation must not hold the store-wide mutation lock.
				// Remove reopens and revalidates the operation after consent.
				plan.Close()
				output.PrintRmConfirmation(rmConfirmationView(plan))
				output.Blank()
				ok, err := confirmFn("Continue?", false)
				if err != nil {
					return err
				}
				if !ok {
					output.Hint("Aborted.")
					return nil
				}
			}
			result, err := project.Remove(plan)
			if result.ProjectName != "" {
				output.PrintRmResult(rmResultForOutput(result), err == nil)
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&purge, "purge", false, "also delete every recorded project copy")
	cmd.Flags().BoolVar(&force, "force", false, "skip confirmation and override the purge safety guard")
	return cmd
}

func rmConfirmationView(plan *project.RmPlan) output.RmConfirmationView {
	refusal := ""
	if plan.RefusalErr != nil {
		refusal = plan.RefusalErr.Error()
	}
	return output.RmConfirmationView{
		ProjectName: plan.ProjectName, CentralRepo: plan.CentralRepo, CommitMsg: plan.CommitMsg,
		RecoveryCmd: plan.RecoveryCmd, Purge: plan.Purge, Copies: rmCopiesForOutput(plan.Copies), RefusalNote: refusal,
	}
}

func rmResultForOutput(result project.RmResult) output.RmResult {
	return output.RmResult{
		ProjectName: result.ProjectName, CentralRepo: result.CentralRepo, CommitMessage: result.CommitMessage,
		Commit: result.Commit, RecoveryCmd: result.RecoveryCmd, Purge: result.Purge, Copies: rmCopiesForOutput(result.Copies),
	}
}

func rmCopiesForOutput(copies []project.RmCopyResult) []output.RmCopyResult {
	out := make([]output.RmCopyResult, 0, len(copies))
	for _, copy := range copies {
		out = append(out, output.RmCopyResult{
			Checkout: copy.Checkout, Dir: copy.Dir, Path: copy.Path, State: output.SyncState(copy.State),
			Issues: issuesForOutput(copy.Issues), Status: string(copy.Status),
		})
	}
	return out
}
