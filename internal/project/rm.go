package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nathanclairmonte/lore/internal/config"
	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/reconcile"
	"github.com/nathanclairmonte/lore/internal/state"
)

type RmCopyStatus string

const (
	RmCopyKept    RmCopyStatus = "kept"
	RmCopyPurged  RmCopyStatus = "purged"
	RmCopyMissing RmCopyStatus = "missing"
)

type RmCopyResult struct {
	Checkout string
	Dir      string
	Path     string
	State    reconcile.SyncState
	Issues   []reconcile.Issue
	Status   RmCopyStatus
}

type RmPlan struct {
	ProjectName string
	CentralRepo string
	CommitMsg   string
	RecoveryCmd string
	Purge       bool
	Copies      []RmCopyResult
	RefusalErr  error
	Force       bool

	ctx *mutationContext
}

type RmResult struct {
	ProjectName   string
	CentralRepo   string
	CommitMessage string
	Commit        string
	RecoveryCmd   string
	Purge         bool
	Copies        []RmCopyResult
}

// PreflightRm inspects the complete removal scope while holding the central
// mutation lock. Call Close when the plan will not be executed.
func PreflightRm(name string, purge bool, force bool) (*RmPlan, error) {
	ctx, err := openMutationContext()
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*RmPlan, error) {
		ctx.close()
		return nil, err
	}
	if err := ValidateProjectName(name, "<name>"); err != nil {
		return fail(err)
	}
	exists, err := centralProjectDirExists(ctx.central, name)
	if err != nil {
		return fail(err)
	}
	if !exists {
		return fail(lerr.Newf(lerr.CodeNotFound, "project %q is not tracked in central", name))
	}
	head, err := ctx.central.HeadSHA()
	if err != nil {
		return fail(err)
	}
	plan := RmPlan{
		ProjectName: name, CentralRepo: ctx.central.Path, CommitMsg: renderRmTemplate(ctx.config.Commit.RmTemplate, name),
		RecoveryCmd: rmRecoveryCommand(ctx.central.Path, head, name), Purge: purge,
		Copies: []RmCopyResult{}, Force: force, ctx: ctx,
	}
	entry, recorded := ctx.state.ProjectEntryFor(name)
	if !recorded {
		// no checkout to inspect, so ask central directly; the tracked path
		// below gets the same verdict from statusFromState.
		centralDirs, err := ctx.central.ProjectAgentDirs(name)
		if err != nil {
			return fail(err)
		}
		dirty, err := ctx.central.ScopedDirty(name, centralDirs)
		if err != nil {
			return fail(err)
		}
		if dirty {
			return fail(centralDirtyRmRefusal())
		}
		return &plan, nil
	}
	status, err := statusFromState(ctx.state, ctx.central, name, "")
	if err != nil {
		return fail(err)
	}
	projectStatus := status.Projects[0]
	if hasIssueKind(projectStatus.Issues, reconcile.IssueCentralDirty) {
		return fail(centralDirtyRmRefusal())
	}
	checkoutByPath := map[string]CheckoutStatus{}
	for _, checkout := range projectStatus.Checkouts {
		checkoutByPath[checkout.Path] = checkout
	}
	var unsafe []string
	for _, checkout := range entry.Checkouts {
		inspected := checkoutByPath[checkout.Path]
		if purge && !force && checkoutUnsafeToPurge(inspected) {
			unsafe = append(unsafe, fmt.Sprintf("%s is %s or unhealthy", config.Contract(checkout.Path), inspected.State))
		}
		for _, dir := range inspected.Dirs {
			copyStatus := RmCopyKept
			missingOnly := onlyCopyMissing(dir.Issues)
			if purge {
				if missingOnly {
					copyStatus = RmCopyMissing
				} else {
					copyStatus = RmCopyPurged
				}
				if !force && dirUnsafeToPurge(dir) {
					unsafe = append(unsafe, fmt.Sprintf("%s has health issues", config.Contract(filepath.Join(checkout.Path, dir.Name))))
				}
			}
			plan.Copies = append(plan.Copies, RmCopyResult{
				Checkout: checkout.Path, Dir: dir.Name, Path: filepath.Join(checkout.Path, dir.Name),
				State: dir.State, Issues: append([]reconcile.Issue(nil), dir.Issues...), Status: copyStatus,
			})
		}
	}
	sort.Slice(plan.Copies, func(i, j int) bool { return plan.Copies[i].Path < plan.Copies[j].Path })
	if len(unsafe) > 0 {
		plan.RefusalErr = lerr.Newf(lerr.CodeRefusedSafety, "purge may destroy unsynced or uninspectable work:\n- %s", strings.Join(unsafe, "\n- ")).
			WithHint("resolve or push the work first, or re-run with `--force` to purge it")
	}
	return &plan, nil
}

// Close releases a preflight plan that will not be executed.
func (p *RmPlan) Close() {
	if p.ctx != nil {
		p.ctx.close()
		p.ctx = nil
	}
}

// Remove commits the central deletion, drops all checkout state, and then
// purges project copies when requested. A closed preflight plan is reopened
// and revalidated so interactive confirmation never holds the mutation lock.
func Remove(plan *RmPlan) (RmResult, error) {
	if plan == nil {
		return RmResult{}, lerr.New(lerr.CodeGenericError, "removal plan is closed")
	}
	if plan.ctx == nil {
		ctx, err := openMutationContext()
		if err != nil {
			return RmResult{}, err
		}
		plan.ctx = ctx
		if err := validateRmBeforeMutation(ctx, plan); err != nil {
			plan.Close()
			return RmResult{}, err
		}
	}
	defer plan.Close()
	if plan.RefusalErr != nil {
		return RmResult{}, plan.RefusalErr
	}
	ctx := plan.ctx
	if plan.Purge && !plan.Force {
		if err := validatePurgeStillSafe(ctx, plan.ProjectName); err != nil {
			return RmResult{}, err
		}
	}
	original := ctx.state.Clone()
	undo, err := ctx.central.RemoveProjectStaged(plan.ProjectName)
	if err != nil {
		return RmResult{}, err
	}
	rollback := func() error {
		return errors.Join(undo(), state.Save(original))
	}
	ctx.state.RemoveProject(plan.ProjectName)
	if err := state.Save(ctx.state); err != nil {
		return RmResult{}, errors.Join(err, rollback())
	}
	if err := ctx.central.CommitPaths(plan.CommitMsg, filepath.Join("projects", plan.ProjectName)); err != nil {
		return RmResult{}, errors.Join(err, rollback())
	}
	result := RmResult{
		ProjectName: plan.ProjectName, CentralRepo: plan.CentralRepo, CommitMessage: plan.CommitMsg,
		RecoveryCmd: plan.RecoveryCmd, Purge: plan.Purge, Copies: append([]RmCopyResult(nil), plan.Copies...),
	}
	head, err := ctx.central.HeadSHA()
	if err != nil {
		return result, err
	}
	result.Commit = head
	if plan.Purge {
		var failures []string
		for i := range result.Copies {
			copy := &result.Copies[i]
			if copy.Status == RmCopyMissing {
				continue
			}
			if err := os.RemoveAll(copy.Path); err != nil {
				copy.Status = RmCopyKept
				failures = append(failures, fmt.Sprintf("%s: %v", copy.Path, err))
			}
		}
		if len(failures) > 0 {
			return result, lerr.Newf(lerr.CodeInconsistency, "project was untracked but some copies could not be purged:\n- %s", strings.Join(failures, "\n- "))
		}
	}
	return result, nil
}

func validateRmBeforeMutation(ctx *mutationContext, plan *RmPlan) error {
	name := plan.ProjectName
	exists, err := centralProjectDirExists(ctx.central, name)
	if err != nil {
		return err
	}
	if !exists {
		return lerr.Newf(lerr.CodeNotFound, "project %q is not tracked in central", name)
	}
	dirs, err := ctx.central.ProjectAgentDirs(name)
	if err != nil {
		return err
	}
	dirty, err := ctx.central.ScopedDirty(name, dirs)
	if err != nil {
		return err
	}
	if dirty {
		return centralDirtyRmRefusal()
	}
	if plan.Purge {
		current, err := statusFromState(ctx.state, ctx.central, name, "")
		if err != nil {
			return err
		}
		plannedPaths := map[string]struct{}{}
		for _, copy := range plan.Copies {
			plannedPaths[copy.Path] = struct{}{}
		}
		currentPaths := map[string]struct{}{}
		for _, project := range current.Projects {
			for _, checkout := range project.Checkouts {
				for _, dir := range checkout.Dirs {
					currentPaths[filepath.Join(checkout.Path, dir.Name)] = struct{}{}
				}
			}
		}
		if !samePathSet(plannedPaths, currentPaths) {
			return lerr.New(lerr.CodeRefusedSafety, "purge scope changed during confirmation").
				WithHint("review the updated scope and retry")
		}
	}
	if plan.Purge && !plan.Force {
		return validatePurgeStillSafe(ctx, name)
	}
	return nil
}

func samePathSet(left, right map[string]struct{}) bool {
	if len(left) != len(right) {
		return false
	}
	for path := range left {
		if _, ok := right[path]; !ok {
			return false
		}
	}
	return true
}

func validatePurgeStillSafe(ctx *mutationContext, name string) error {
	status, err := statusFromState(ctx.state, ctx.central, name, "")
	if err != nil {
		return err
	}
	for _, project := range status.Projects {
		if len(project.Issues) > 0 {
			return lerr.New(lerr.CodeRefusedSafety, "purge scope became unhealthy after preflight")
		}
		for _, checkout := range project.Checkouts {
			if checkoutUnsafeToPurge(checkout) {
				return lerr.Newf(lerr.CodeRefusedSafety, "checkout %s is no longer safe to purge", config.Contract(checkout.Path))
			}
			for _, dir := range checkout.Dirs {
				if dirUnsafeToPurge(dir) {
					return lerr.Newf(lerr.CodeRefusedSafety, "copy %s is no longer safe to purge", config.Contract(filepath.Join(checkout.Path, dir.Name)))
				}
			}
		}
	}
	return nil
}

// checkoutUnsafeToPurge and dirUnsafeToPurge carry the one purge safety rule
// that preflight and the post-confirmation recheck must agree on.
func checkoutUnsafeToPurge(checkout CheckoutStatus) bool {
	return checkout.State == reconcile.StateProjectAhead || checkout.State == reconcile.StateDiverged || len(checkout.Issues) > 0
}

func dirUnsafeToPurge(dir DirSyncStatus) bool {
	return len(dir.Issues) > 0 && !onlyCopyMissing(dir.Issues)
}

func centralDirtyRmRefusal() error {
	return lerr.New(lerr.CodeRefusedSafety, "central copy has uncommitted or ignored content").
		WithHint("commit, stash, or discard the central changes before removing the project")
}

func renderRmTemplate(tmpl, projectName string) string {
	return strings.ReplaceAll(tmpl, "{project}", projectName)
}

func rmRecoveryCommand(centralPath, commit, projectName string) string {
	return fmt.Sprintf("cd %s && git checkout %s -- projects/%s/", config.Contract(centralPath), commit, projectName)
}
