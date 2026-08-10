package output

import (
	"encoding/json"
	"fmt"

	"github.com/nathanclairmonte/lore/internal/config"
)

// PushStatus mirrors project.PushStatus so this package doesn't have to
// import project. Values are identical; the cmd layer converts.
type PushStatus string

const (
	// PushUnchanged means the checkout already matches central at a live base.
	PushUnchanged PushStatus = "unchanged"
	// PushPushed means the checkout's content was captured in central.
	PushPushed PushStatus = "pushed"
	// PushAdopted means content already matched and only the base advanced.
	PushAdopted PushStatus = "adopted"
)

// PushCheckoutResult is one checkout considered by push.
type PushCheckoutResult struct {
	Path   string     `json:"path"`
	Status PushStatus `json:"status"`
}

// PushProjectResult is one selected project.
type PushProjectResult struct {
	Name      string               `json:"name"`
	Checkouts []PushCheckoutResult `json:"checkouts"`
}

// PushResult is the public push output model.
type PushResult struct {
	CentralRepo   string              `json:"central_repo"`
	PreviousHead  string              `json:"previous_head"`
	CommitSHA     string              `json:"commit_sha"`
	CommitMessage string              `json:"commit_message"`
	Projects      []PushProjectResult `json:"projects"`
}

// PrintPush renders a push result. Auto suppresses only genuine no-ops.
func PrintPush(result PushResult, auto bool) {
	if mode == ModeJSON {
		_ = json.NewEncoder(Out).Encode(result)
		return
	}
	if result.CommitSHA == "" {
		adopted := 0
		for _, project := range result.Projects {
			for _, checkout := range project.Checkouts {
				if checkout.Status == PushAdopted {
					adopted++
				}
			}
		}
		if adopted == 0 {
			if !auto {
				Success("Nothing to push")
			}
			return
		}
		Success("Advanced sync-base for %d checkout(s)", adopted)
		return
	}
	g := activeGlyphs()
	pathStyle := stylePath()
	for _, project := range result.Projects {
		for _, checkout := range project.Checkouts {
			if checkout.Status == PushPushed {
				_, _ = fmt.Fprintf(Out, "%s Pushed %s  %s\n", pathStyle.Render(g.arrowRight), project.Name, config.Contract(checkout.Path))
			}
		}
	}
	Success("Committed to central repo: %q", result.CommitMessage)
	Success("Recorded sync-base: %s", result.CommitSHA)
	Blank()
	printGitPushHint(result.CentralRepo)
}
