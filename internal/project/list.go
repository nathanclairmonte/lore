package project

import (
	"sort"

	"github.com/nathanclairmonte/lore/internal/reconcile"
)

type ListRow struct {
	Name      string
	Path      string
	AgentDirs []string
	State     string
	Issues    []reconcile.Issue
}

type ListResult struct {
	CentralRepo string
	CentralHead string
	Rows        []ListRow
}

// List reports one row per checkout plus one not-placed row for central-only
// projects.
func List() (ListResult, error) {
	ctx, err := openMutationContext()
	if err != nil {
		return ListResult{}, err
	}
	defer ctx.close()
	centralNames, err := ctx.central.ListProjects()
	if err != nil {
		return ListResult{}, err
	}
	status, err := statusFromState(ctx.state, ctx.central, "", "")
	if err != nil {
		return ListResult{}, err
	}
	rows := []ListRow{}
	recorded := map[string]struct{}{}
	for _, projectStatus := range status.Projects {
		recorded[projectStatus.Name] = struct{}{}
		for _, checkout := range projectStatus.Checkouts {
			// inspectProject already unioned state's cached dirs with central's
			// and sorted them, so ls and status agree on the dir set.
			dirs := make([]string, 0, len(checkout.Dirs))
			issues := append([]reconcile.Issue(nil), projectStatus.Issues...)
			issues = append(issues, checkout.Issues...)
			for _, dir := range checkout.Dirs {
				dirs = append(dirs, dir.Name)
				issues = append(issues, dir.Issues...)
			}
			reconcile.SortIssues(issues)
			rows = append(rows, ListRow{Name: projectStatus.Name, Path: checkout.Path, AgentDirs: dirs, State: string(checkout.State), Issues: issues})
		}
	}
	for _, name := range centralNames {
		if _, ok := recorded[name]; ok {
			continue
		}
		dirs, issues, err := centralOnlyStatus(ctx.central, name)
		if err != nil {
			return ListResult{}, err
		}
		rows = append(rows, ListRow{Name: name, AgentDirs: dirs, State: "not-placed", Issues: issues})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Name == rows[j].Name {
			return rows[i].Path < rows[j].Path
		}
		return rows[i].Name < rows[j].Name
	})
	return ListResult{CentralRepo: ctx.central.Path, CentralHead: status.CentralHead, Rows: rows}, nil
}
