package output

import (
	"encoding/json"
	"fmt"

	"github.com/nathanclairmonte/lore/internal/config"
)

type MoveCheckoutResult struct {
	OldPath  string `json:"old_path"`
	Path     string `json:"path"`
	SyncBase string `json:"sync_base"`
}

type MoveResult struct {
	OldName       string               `json:"old_name"`
	ProjectName   string               `json:"project"`
	CentralRepo   string               `json:"central_repo"`
	CommitMessage string               `json:"commit_message"`
	Commit        string               `json:"commit"`
	CentralOnly   bool                 `json:"central_only"`
	Checkouts     []MoveCheckoutResult `json:"checkouts"`
}

func PrintMoveResult(result MoveResult, ok bool) {
	if mode == ModeJSON {
		_ = json.NewEncoder(Out).Encode(struct {
			OK     bool       `json:"ok"`
			Result MoveResult `json:"result"`
		}{OK: ok, Result: result})
		return
	}
	g := activeGlyphs()
	Success("Renamed projects/%s %s projects/%s in central", result.OldName, g.arrowRight, result.ProjectName)
	for _, checkout := range result.Checkouts {
		if checkout.OldPath != checkout.Path {
			_, _ = fmt.Fprintf(Out, "  - checkout path: %s %s %s\n", config.Contract(checkout.OldPath), g.arrowRight, config.Contract(checkout.Path))
		} else {
			_, _ = fmt.Fprintf(Out, "  - checkout retained: %s\n", config.Contract(checkout.Path))
		}
	}
	Blank()
	Success("Committed to central repo: %q", result.CommitMessage)
}
