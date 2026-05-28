package output

import (
	"fmt"

	"github.com/nathanclairmonte/lore/internal/config"
)

// RmConfirmationView is the data PrintRmConfirmation renders for the
// pre-confirm prompt. cmd/lore/rm.go constructs it from project.RmPreflight,
// then calls this to render before invoking prompt.Confirm.
type RmConfirmationView struct {
	ProjectName  string
	ProjectPath  string // empty when AlreadyUnlinked
	CentralRepo  string
	CommitMsg    string
	RecoveryCmd  string
	Dirs         []UnlinkDirResult
	RefusalNote  string // non-empty when a drift gate would refuse before the rm landed
	NotInCentral bool
}

// RmResult is the data lore rm hands to the output layer after the
// destructive run completes.
type RmResult struct {
	ProjectName   string
	ProjectPath   string
	CentralRepo   string
	CommitMessage string
	Dirs          []UnlinkDirResult
	RecoveryCmd   string
}

// PrintRmConfirmation renders the prompt body the user reads before
// the y/N step. Lists every symlink that will be removed explicitly
// (no collapse) so the user can spot a typo-target before saying y.
// Surfaces any drift-refusal upstream so the user sees the refusal
// before pressing y, not after.
//
// Suppressed in JSON mode (JSON runs are non-interactive and shouldn't
// reach the prompt path anyway).
func PrintRmConfirmation(v RmConfirmationView) {
	if mode == ModeJSON {
		return
	}
	_, _ = fmt.Fprintln(Out, "This will:")
	_, _ = fmt.Fprintf(Out, "  - Remove projects/%s/ from the central repo working tree (%s)\n",
		v.ProjectName, config.Contract(v.CentralRepo))
	for _, d := range v.Dirs {
		switch d.Status {
		case UnlinkStatusUnlinked:
			_, _ = fmt.Fprintf(Out, "  - Remove symlink %s\n",
				config.Contract(d.LinkPath))
		case UnlinkStatusAlreadyUnlinked:
			_, _ = fmt.Fprintf(Out, "  - Note: %s is already absent on disk\n",
				config.Contract(d.LinkPath))
		}
	}
	if v.ProjectPath != "" {
		_, _ = fmt.Fprintf(Out, "  - Remove %s from local state\n", v.ProjectName)
	}
	_, _ = fmt.Fprintf(Out, "  - Commit the deletion to central: %q\n", v.CommitMsg)
	_, _ = fmt.Fprintf(Out, "  - (Recover with: %s)\n", v.RecoveryCmd)

	if v.NotInCentral {
		Warn("Project %s is not tracked in central; nothing to remove", v.ProjectName)
		return
	}
	if v.RefusalNote != "" {
		Blank()
		Warn("Will refuse: %s", v.RefusalNote)
	}
}

// PrintRmResult renders the post-rm success summary. Mirrors the shape
// of PrintAddResult's commit + next-step block but with rm-specific
// content: what was removed, the commit message, the recovery command
// pinned at the bottom (same string the prompt showed pre-confirm).
func PrintRmResult(r RmResult) {
	if mode == ModeJSON {
		return
	}
	if r.ProjectPath != "" {
		Success("Removed project %s (%s)", r.ProjectName, config.Contract(r.ProjectPath))
	} else {
		Success("Removed project %s from central", r.ProjectName)
	}
	for _, d := range r.Dirs {
		switch d.Status {
		case UnlinkStatusUnlinked:
			_, _ = fmt.Fprintf(Out, "  - removed symlink: %s\n", config.Contract(d.LinkPath))
		case UnlinkStatusAlreadyUnlinked:
			_, _ = fmt.Fprintf(Out, "  - note: %s was already absent\n", config.Contract(d.LinkPath))
		}
	}
	Blank()
	Success("Committed to central repo: %q", r.CommitMessage)
	Blank()
	_, _ = fmt.Fprintf(Out, "Recover with: %s\n", r.RecoveryCmd)
}
