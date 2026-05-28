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

// stdinIsTTY is a var so tests can stub it. rm needs a TTY gate at the
// cmd layer (not inside prompt.Confirm) so we can emit CodeRefusedSafety
// — exit 5 — for the non-TTY-without-force case instead of leaking the
// prompt package's CodeInconsistency.
var stdinIsTTY = func() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// confirmFn is the y/N prompt the cmd layer calls. var rather than
// direct prompt.Confirm so tests can stub the user's answer without
// driving the prompt package through a fake stdin.
var confirmFn = prompt.Confirm

// newRmCmd builds the `lore rm` cobra subcommand.
func newRmCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "rm <name>",
		Short: "Stop tracking a project entirely and remove it from central",
		Long: `Removes every symlink lore created for the project, deletes
its directory from the central repo's working tree, commits
the deletion, and clears it from local state.

Recoverable: the files persist in central's git history. The
output prints a 'cd <central> && git checkout HEAD~1 --
projects/<name>/' command pinned to the central repo path so
you can restore without having to look up the right
incantation.

By default 'lore rm' prompts for confirmation. Pass --force
to skip the prompt (useful for scripts / batch removal). In a
non-TTY context without --force the command refuses with exit
5 rather than hanging or silently destroying state.

If you only want to break the local symlinks while keeping
the project tracked, use 'lore unlink' instead.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			// non-TTY without --force: refuse with the actionable
			// safety message BEFORE doing any work.
			if !force && !stdinIsTTY() {
				return lerr.New(lerr.CodeRefusedSafety,
					"interactive confirmation required but stdin is not a terminal").
					WithHint("re-run with --force to skip confirmation, or run interactively")
			}

			plan, err := project.PreflightRm(name)
			if err != nil {
				return err
			}

			// not-in-state AND not-in-central → not found. surface
			// before showing any confirmation so the user doesn't see
			// a destructive-looking prompt for nothing.
			if plan.NotInCentral {
				return lerr.Newf(lerr.CodeNotFound,
					"project %q is not tracked in central", name)
			}

			// render confirmation + ask y/N when interactive.
			if !force {
				output.PrintRmConfirmation(rmConfirmationView(plan))
				output.Blank()
				ok, confirmErr := confirmFn("Continue?", false)
				if confirmErr != nil {
					return confirmErr
				}
				if !ok {
					output.Hint("Aborted.")
					return nil
				}
			}

			res, removeErr := project.Remove(plan)
			if removeErr != nil {
				return removeErr
			}
			output.PrintRmResult(rmResultForOutput(res))
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false,
		"skip interactive confirmation (required in non-TTY contexts)")
	return cmd
}

// rmConfirmationView translates a project.RmPlan into the output
// layer's view shape. Captures the joined refusal message verbatim if
// the pre-flight surfaced one; per-dir refusal reasons travel on Dirs.
func rmConfirmationView(p project.RmPlan) output.RmConfirmationView {
	refusal := ""
	if p.RefusalErr != nil {
		refusal = p.RefusalErr.Error()
	}
	return output.RmConfirmationView{
		ProjectName:  p.ProjectName,
		ProjectPath:  p.ProjectPath,
		CentralRepo:  p.CentralRepo,
		CommitMsg:    p.CommitMsg,
		RecoveryCmd:  p.RecoveryCmd,
		Dirs:         unlinkDirsForOutput(p.Dirs),
		RefusalNote:  refusal,
		NotInCentral: p.NotInCentral,
	}
}

// rmResultForOutput translates a project.RmResult into the output
// layer's shape.
func rmResultForOutput(r project.RmResult) output.RmResult {
	return output.RmResult{
		ProjectName:   r.ProjectName,
		ProjectPath:   r.ProjectPath,
		CentralRepo:   r.CentralRepo,
		CommitMessage: r.CommitMessage,
		Dirs:          unlinkDirsForOutput(r.Dirs),
		RecoveryCmd:   r.RecoveryCmd,
	}
}
