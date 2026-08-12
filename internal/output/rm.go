package output

import (
	"encoding/json"
	"fmt"

	"github.com/nathanclairmonte/lore/internal/config"
)

type RmCopyResult struct {
	Checkout string      `json:"checkout"`
	Dir      string      `json:"dir"`
	Path     string      `json:"path"`
	State    SyncState   `json:"state"`
	Issues   []SyncIssue `json:"issues"`
	Status   string      `json:"status"`
}

type RmConfirmationView struct {
	ProjectName string
	CentralRepo string
	CommitMsg   string
	RecoveryCmd string
	Purge       bool
	Copies      []RmCopyResult
	RefusalNote string
}

type RmResult struct {
	ProjectName   string         `json:"project"`
	CentralRepo   string         `json:"central_repo"`
	CommitMessage string         `json:"commit_message"`
	Commit        string         `json:"commit"`
	RecoveryCmd   string         `json:"recovery_command"`
	Purge         bool           `json:"purge"`
	Copies        []RmCopyResult `json:"copies"`
}

func PrintRmConfirmation(view RmConfirmationView) {
	if mode == ModeJSON {
		return
	}
	_, _ = fmt.Fprintln(Out, "This will:")
	_, _ = fmt.Fprintf(Out, "  - Remove projects/%s/ from central (%s)\n", view.ProjectName, config.Contract(view.CentralRepo))
	for _, copy := range view.Copies {
		if view.Purge {
			if copy.Status == "missing" {
				_, _ = fmt.Fprintf(Out, "  - Already missing: %s\n", config.Contract(copy.Path))
			} else {
				_, _ = fmt.Fprintf(Out, "  - Purge %s (%s)\n", config.Contract(copy.Path), copy.State)
			}
		} else {
			_, _ = fmt.Fprintf(Out, "  - Keep %s (%s)\n", config.Contract(copy.Path), copy.State)
		}
	}
	_, _ = fmt.Fprintf(Out, "  - Remove all local checkout records for %s\n", view.ProjectName)
	_, _ = fmt.Fprintf(Out, "  - Commit the central deletion: %q\n", view.CommitMsg)
	_, _ = fmt.Fprintf(Out, "  - Recover with: %s\n", view.RecoveryCmd)
	if view.RefusalNote != "" {
		Blank()
		Warn("Will refuse: %s", view.RefusalNote)
	}
}

func PrintRmResult(result RmResult, ok bool) {
	if mode == ModeJSON {
		_ = json.NewEncoder(Out).Encode(struct {
			OK     bool     `json:"ok"`
			Result RmResult `json:"result"`
		}{OK: ok, Result: result})
		return
	}
	Success("Removed project %s from central and local state", result.ProjectName)
	for _, copy := range result.Copies {
		_, _ = fmt.Fprintf(Out, "  - %s: %s\n", copy.Status, config.Contract(copy.Path))
	}
	Blank()
	Success("Committed to central repo: %q", result.CommitMessage)
	Blank()
	_, _ = fmt.Fprintf(Out, "Recover with: %s\n", result.RecoveryCmd)
}
