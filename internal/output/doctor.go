package output

import (
	"encoding/json"

	"github.com/nathanclairmonte/lore/internal/config"
)

type DoctorResult struct {
	Healthy bool       `json:"healthy"`
	Fixed   []string   `json:"fixed"`
	Status  SyncResult `json:"status"`
}

func PrintDoctor(result DoctorResult) {
	if mode == ModeJSON {
		_ = json.NewEncoder(Out).Encode(struct {
			OK     bool         `json:"ok"`
			Result DoctorResult `json:"result"`
		}{OK: true, Result: result})
		return
	}
	for _, path := range result.Fixed {
		Success("Restored missing copy: %s", config.Contract(path))
	}
	if result.Healthy {
		Success("No unhealthy checkouts found")
		return
	}
	for _, project := range result.Status.Projects {
		if len(project.Issues) > 0 {
			Warn("%s", project.Name)
			printIssues(project.Issues, "  ")
		}
		for _, checkout := range project.Checkouts {
			if checkout.State == SyncDiverged {
				Warn("%s at %s is diverged", project.Name, config.Contract(checkout.Path))
			}
			if len(checkout.Issues) > 0 {
				Warn("%s at %s", project.Name, config.Contract(checkout.Path))
				printIssues(checkout.Issues, "  ")
			}
			printDirIssues(checkout.Dirs, "  ")
		}
	}
}
