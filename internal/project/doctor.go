package project

import (
	"os"
	"path/filepath"
	"sort"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/git"
	"github.com/nathanclairmonte/lore/internal/reconcile"
	"github.com/nathanclairmonte/lore/internal/state"
)

type DoctorResult struct {
	Status  StatusResult
	Fixed   []string
	Healthy bool
}

var stateSaveDoctor = state.Save

// Doctor checks copy integrity and sync-base health. Fix mode restores only
// missing project-copy directories from committed central content.
func Doctor(fix bool) (DoctorResult, error) {
	ctx, err := openMutationContext()
	if err != nil {
		return DoctorResult{}, err
	}
	defer ctx.close()
	inspect := func() (StatusResult, error) {
		status, err := statusFromState(ctx.state, ctx.central, "", "")
		if err != nil {
			return status, err
		}
		return addCentralOnlyDoctorProjects(ctx, status)
	}
	status, err := inspect()
	if err != nil {
		return DoctorResult{}, err
	}
	fixed := []string{}
	if fix {
		fixed, err = repairMissingCopies(ctx, status)
		if err != nil {
			return DoctorResult{Status: status, Fixed: fixed}, err
		}
		// a second full-store scan only earns its cost when something changed.
		if len(fixed) > 0 {
			status, err = inspect()
			if err != nil {
				return DoctorResult{Status: status, Fixed: fixed}, err
			}
		}
	}
	sort.Strings(fixed)
	return DoctorResult{Status: status, Fixed: fixed, Healthy: !status.Unhealthy()}, nil
}

func repairMissingCopies(ctx *mutationContext, status StatusResult) ([]string, error) {
	fixed := []string{}
	var repairErr error
	for _, projectStatus := range status.Projects {
		if hasIssueKind(projectStatus.Issues, reconcile.IssueCentralDirty) {
			continue
		}
		for _, checkout := range projectStatus.Checkouts {
			info, err := os.Stat(checkout.Path)
			if err != nil || !info.IsDir() {
				continue
			}
			replacements := []reconcile.TreeReplacement{}
			dirs := make([]string, 0, len(checkout.Dirs))
			completeEqualsCentral := true
			for _, dir := range checkout.Dirs {
				dirs = append(dirs, dir.Name)
				destination := filepath.Join(checkout.Path, dir.Name)
				if onlyCopyMissing(dir.Issues) {
					replacements = append(replacements, reconcile.TreeReplacement{
						Source: ctx.central.ProjectPath(projectStatus.Name, dir.Name), Destination: destination,
					})
					continue
				}
				if len(dir.Issues) > 0 {
					completeEqualsCentral = false
					continue
				}
				equal, err := reconcile.TreesEqual(destination, ctx.central.ProjectPath(projectStatus.Name, dir.Name))
				if err != nil {
					return fixed, err
				}
				completeEqualsCentral = completeEqualsCentral && equal
			}
			if len(replacements) == 0 {
				continue
			}
			ignored := true
			for _, replacement := range replacements {
				dirName := filepath.Base(replacement.Destination)
				isIgnored, err := git.CheckIgnore(checkout.Path, dirName+"/")
				if err != nil {
					return fixed, err
				}
				if !isIgnored {
					ignored = false
					if repairErr == nil {
						repairErr = lerr.Newf(lerr.CodeRefusedSafety,
							"cannot restore %s because the project copy is not gitignored", replacement.Destination).
							WithHint("add an effective ignore rule, then re-run `lore doctor --fix`")
					}
				}
			}
			if !ignored {
				continue
			}
			intendedBase := checkout.SyncBase
			if completeEqualsCentral {
				intendedBase = status.CentralHead
			}
			transaction, err := publishProjectReplacements(projectReplacementPublication{
				project: projectStatus.Name, checkout: checkout.Path, syncBase: intendedBase,
				agentDirs: dirs, replacements: replacements,
			})
			if err != nil {
				return fixed, err
			}
			before := ctx.state.Clone()
			for _, dir := range dirs {
				if err := ctx.state.AddAgentDir(projectStatus.Name, dir); err != nil {
					ctx.state = before
					return fixed, err
				}
			}
			if err := ctx.state.RecordCheckout(projectStatus.Name, checkout.Path, intendedBase); err != nil {
				ctx.state = before
				return fixed, err
			}
			if err := stateSaveDoctor(ctx.state); err != nil {
				ctx.state = before
				return fixed, lerr.Wrap(lerr.CodeInconsistency, err, "project copy was restored but its sync-base could not be saved").
					WithHint("re-run lore; the interrupted repair will complete safely")
			}
			if err := transaction.finish(); err != nil {
				return fixed, err
			}
			for _, replacement := range replacements {
				fixed = append(fixed, replacement.Destination)
			}
		}
	}
	return fixed, repairErr
}

func addCentralOnlyDoctorProjects(ctx *mutationContext, result StatusResult) (StatusResult, error) {
	recorded := map[string]struct{}{}
	for _, project := range result.Projects {
		recorded[project.Name] = struct{}{}
	}
	names, err := ctx.central.ListProjects()
	if err != nil {
		return result, err
	}
	for _, name := range names {
		if _, ok := recorded[name]; ok {
			continue
		}
		_, issues, err := centralOnlyStatus(ctx.central, name)
		if err != nil {
			return result, err
		}
		result.Projects = append(result.Projects, ProjectStatus{Name: name, Checkouts: []CheckoutStatus{}, Issues: issues})
	}
	sort.Slice(result.Projects, func(i, j int) bool { return result.Projects[i].Name < result.Projects[j].Name })
	return result, nil
}

func hasIssueKind(issues []reconcile.Issue, kind reconcile.IssueKind) bool {
	for _, issue := range issues {
		if issue.Kind == kind {
			return true
		}
	}
	return false
}
