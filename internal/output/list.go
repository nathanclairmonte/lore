package output

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/nathanclairmonte/lore/internal/config"
)

type ListRow struct {
	Project   string      `json:"project"`
	Checkout  string      `json:"checkout"`
	AgentDirs []string    `json:"agent_dirs"`
	SyncState string      `json:"sync_state"`
	Issues    []SyncIssue `json:"issues"`
}

type ListResult struct {
	CentralRepo string    `json:"central_repo"`
	CentralHead string    `json:"central_head"`
	Rows        []ListRow `json:"rows"`
}

func PrintList(result ListResult) {
	if mode == ModeJSON {
		_ = json.NewEncoder(Out).Encode(struct {
			OK     bool       `json:"ok"`
			Result ListResult `json:"result"`
		}{OK: true, Result: result})
		return
	}
	if len(result.Rows) == 0 {
		Success("No tracked projects")
		return
	}
	w := tabwriter.NewWriter(Out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "PROJECT\tCHECKOUT\tAGENT DIRS\tSYNC STATE\tHEALTH")
	for _, row := range result.Rows {
		checkout := "not placed"
		if row.Checkout != "" {
			checkout = config.Contract(row.Checkout)
		}
		health := "healthy"
		if len(row.Issues) > 0 {
			health = fmt.Sprintf("%d issues", len(row.Issues))
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", row.Project, checkout, strings.Join(row.AgentDirs, ", "), row.SyncState, health)
	}
	_ = w.Flush()
}
