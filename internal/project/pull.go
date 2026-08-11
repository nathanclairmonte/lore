package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/nathanclairmonte/lore/internal/central"
	"github.com/nathanclairmonte/lore/internal/config"
	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/git"
	"github.com/nathanclairmonte/lore/internal/reconcile"
	"github.com/nathanclairmonte/lore/internal/scan"
	"github.com/nathanclairmonte/lore/internal/state"
)

// PullOptions selects project copies that receive committed central content.
type PullOptions struct {
	Name    string
	Path    string
	All     bool
	Central bool
	Project bool
}

// PullDirStatus names one agent-directory outcome.
type PullDirStatus string

const (
	PullDirMaterialized  PullDirStatus = "materialized"
	PullDirUpdated       PullDirStatus = "updated"
	PullDirUnchanged     PullDirStatus = "unchanged"
	PullDirProjectAhead  PullDirStatus = "project-ahead"
	PullDirMerged        PullDirStatus = "merged"
	PullDirConflicted    PullDirStatus = "conflicted"
	PullDirKeepBoth      PullDirStatus = "keep-both"
	PullDirCentralForced PullDirStatus = "central-forced"
	PullDirProjectForced PullDirStatus = "project-forced"
)

// PullCheckoutStatus names one checkout outcome.
type PullCheckoutStatus string

const (
	PullCheckoutMaterialized  PullCheckoutStatus = "materialized"
	PullCheckoutUpdated       PullCheckoutStatus = "updated"
	PullCheckoutAdopted       PullCheckoutStatus = "adopted"
	PullCheckoutUnchanged     PullCheckoutStatus = "unchanged"
	PullCheckoutProjectAhead  PullCheckoutStatus = "project-ahead"
	PullCheckoutRefused       PullCheckoutStatus = "refused"
	PullCheckoutFailed        PullCheckoutStatus = "failed"
	PullCheckoutMerged        PullCheckoutStatus = "merged"
	PullCheckoutConflicted    PullCheckoutStatus = "conflicted"
	PullCheckoutCentralForced PullCheckoutStatus = "central-forced"
	PullCheckoutProjectForced PullCheckoutStatus = "project-forced"
)

// PullProjectStatus names one project outcome in a sweep.
type PullProjectStatus string

const (
	PullProjectPulled     PullProjectStatus = "pulled"
	PullProjectUnchanged  PullProjectStatus = "unchanged"
	PullProjectMissing    PullProjectStatus = "missing"
	PullProjectAmbiguous  PullProjectStatus = "ambiguous"
	PullProjectRefused    PullProjectStatus = "refused"
	PullProjectFailed     PullProjectStatus = "failed"
	PullProjectConflicted PullProjectStatus = "conflicted"
)

// PullConflictResult describes one unresolved merge artifact.
type PullConflictResult struct {
	Path        string
	Kind        reconcile.ConflictKind
	SiblingPath string
}

// PullDirResult describes one agent directory considered by pull.
type PullDirResult struct {
	Name      string
	Path      string
	Status    PullDirStatus
	Unignored bool
	Conflicts []PullConflictResult
}

// PullCheckoutResult describes one checkout considered by pull.
type PullCheckoutResult struct {
	Path     string
	Status   PullCheckoutStatus
	SyncBase string
	Dirs     []PullDirResult
	Err      error
}

// PullProjectResult describes one central project considered by pull.
type PullProjectResult struct {
	Name      string
	Status    PullProjectStatus
	Matches   []string
	Checkouts []PullCheckoutResult
	Err       error
}

// PullResult is the stable pull command result.
type PullResult struct {
	CentralRepo  string
	CentralHead  string
	ScannedRoots []string
	Projects     []PullProjectResult
}

// HasConflicts reports whether pull wrote unresolved conflict artifacts.
func (result PullResult) HasConflicts() bool {
	for _, project := range result.Projects {
		for _, checkout := range project.Checkouts {
			if checkout.Status == PullCheckoutConflicted {
				return true
			}
		}
	}
	return false
}

type pullTarget struct {
	name         string
	path         string
	replacePaths []string
}

type pullPlan struct {
	target       pullTarget
	dirs         []string
	result       PullCheckoutResult
	replacements []reconcile.TreeReplacement
	recordBase   bool
	cleanup      []string
}

var savePullState = state.Save

// Pull receives committed central content without merging divergent copies.
func Pull(opts PullOptions) (PullResult, error) {
	if opts.All && (opts.Name != "" || opts.Path != "") {
		return PullResult{}, lerr.New(lerr.CodeConflict, "--all cannot be combined with a project name or --path")
	}
	if opts.Path != "" && opts.Name == "" {
		return PullResult{}, lerr.New(lerr.CodeGenericError, "--path requires a project name").
			WithHint("run `lore pull <name> --path <path>`")
	}
	if opts.Central && opts.Project {
		return PullResult{}, lerr.New(lerr.CodeConflict, "--central and --project cannot be used together")
	}
	if opts.All && (opts.Central || opts.Project) {
		return PullResult{}, lerr.New(lerr.CodeConflict, "--all cannot be combined with --central or --project")
	}

	ctx, err := openMutationContext()
	if err != nil {
		return PullResult{}, err
	}
	defer ctx.close()
	cfg, c, st := ctx.config, ctx.central, ctx.state
	head, err := c.HeadSHA()
	if err != nil {
		return PullResult{}, err
	}
	result := PullResult{
		CentralRepo: c.Path, CentralHead: head,
		ScannedRoots: []string{}, Projects: []PullProjectResult{},
	}

	if opts.All {
		return pullAll(cfg, c, &st, head, result)
	}
	targets, err := resolvePullTargets(opts, &st, c, cfg.ProjectsRoots)
	if err != nil {
		return result, err
	}
	targetPaths := make([]string, 0, len(targets))
	for _, target := range targets {
		targetPaths = append(targetPaths, target.path)
	}
	if err := detectOrphanProjectReplacementPaths(targetPaths); err != nil {
		return result, err
	}
	statusPath := ""
	if opts.Path != "" {
		statusPath = targets[0].path
	}
	statuses, err := cachedPullStatuses(c, st, targets[0].name, statusPath)
	if err != nil {
		return result, err
	}
	plans := make([]pullPlan, 0, len(targets))
	for _, target := range targets {
		plan, err := planPullCheckout(c, st, head, target, opts, statuses[target.path])
		if err != nil {
			cleanupPullPlans(plans)
			projectResult := PullProjectResult{
				Name: target.name, Status: PullProjectRefused, Matches: []string{},
				Checkouts: []PullCheckoutResult{refusedPullCheckout(target.path, err)}, Err: err,
			}
			result.Projects = append(result.Projects, projectResult)
			return result, err
		}
		plans = append(plans, plan)
	}

	projectResult := PullProjectResult{Name: targets[0].name, Matches: []string{}, Checkouts: []PullCheckoutResult{}}
	for i, plan := range plans {
		checkoutResult, err := applyPullPlan(&st, head, plan)
		projectResult.Checkouts = append(projectResult.Checkouts, checkoutResult)
		if err != nil {
			cleanupPullPlans(plans[i+1:])
			projectResult.Status = failedPullProjectStatus(checkoutResult)
			projectResult.Err = err
			result.Projects = append(result.Projects, projectResult)
			return result, err
		}
	}
	projectResult.Status = rollupPullProject(projectResult.Checkouts)
	result.Projects = append(result.Projects, projectResult)
	return result, nil
}

func pullAll(cfg config.Config, c *central.Central, st *state.State, head string, result PullResult) (PullResult, error) {
	names, err := c.ListProjects()
	if err != nil {
		return result, err
	}
	if len(names) == 0 {
		return result, nil
	}

	targets := map[string][]pullTarget{}
	var needScan []string
	for _, name := range names {
		entry, ok := st.ProjectEntryFor(name)
		valid := []pullTarget{}
		if ok {
			for _, checkout := range entry.Checkouts {
				if dirExists(checkout.Path) {
					valid = append(valid, pullTarget{name: name, path: checkout.Path})
				}
			}
		}
		if len(valid) > 0 {
			targets[name] = valid
			continue
		}
		needScan = append(needScan, name)
	}
	if len(needScan) > 0 {
		result.ScannedRoots = append(result.ScannedRoots, cfg.ProjectsRoots...)
	}
	scanned, err := scan.ScanRoots(cfg.ProjectsRoots, cfg.ProjectsDepth, needScan)
	if err != nil {
		return result, err
	}

	var worst error
	for _, name := range names {
		projectResult := PullProjectResult{Name: name, Matches: []string{}, Checkouts: []PullCheckoutResult{}}
		projectTargets := targets[name]
		if len(projectTargets) == 0 {
			matches := scanned[name]
			switch len(matches) {
			case 0:
				projectResult.Status = PullProjectMissing
				result.Projects = append(result.Projects, projectResult)
				continue
			case 1:
				// state stores canonical paths, so a match reached through a
				// symlinked root must be canonicalised before it is recorded.
				// otherwise a later bare pull resolves the same directory to a
				// different string and appends a second checkout entry.
				match := matches[0]
				if resolved, resolveErr := filepath.EvalSymlinks(match); resolveErr == nil {
					match = resolved
				}
				projectTargets = []pullTarget{{name: name, path: match, replacePaths: staleCheckoutPaths(st, name, match)}}
			default:
				projectResult.Status = PullProjectAmbiguous
				projectResult.Matches = append([]string(nil), matches...)
				result.Projects = append(result.Projects, projectResult)
				continue
			}
		}

		paths := make([]string, 0, len(projectTargets))
		for _, target := range projectTargets {
			paths = append(paths, target.path)
		}
		if artifactErr := detectOrphanProjectReplacementPaths(paths); artifactErr != nil {
			projectResult.Status = PullProjectRefused
			projectResult.Err = artifactErr
			for _, target := range projectTargets {
				projectResult.Checkouts = append(projectResult.Checkouts, refusedPullCheckout(target.path, artifactErr))
			}
			result.Projects = append(result.Projects, projectResult)
			continue
		}
		statuses, statusErr := cachedPullStatuses(c, *st, name, "")
		if statusErr != nil {
			projectResult.Status = PullProjectFailed
			projectResult.Err = statusErr
			worst = promoteWorst(worst, statusErr)
			result.Projects = append(result.Projects, projectResult)
			continue
		}
		for _, target := range projectTargets {
			plan, planErr := planPullCheckout(c, *st, head, target, PullOptions{All: true}, statuses[target.path])
			if planErr != nil {
				projectResult.Checkouts = append(projectResult.Checkouts, refusedPullCheckout(target.path, planErr))
				projectResult.Status = PullProjectRefused
				projectResult.Err = planErr
				worst = promoteWorst(worst, planErr)
				continue
			}
			checkoutResult, applyErr := applyPullPlan(st, head, plan)
			projectResult.Checkouts = append(projectResult.Checkouts, checkoutResult)
			if applyErr != nil {
				projectResult.Status = failedPullProjectStatus(checkoutResult)
				projectResult.Err = applyErr
				worst = promoteWorst(worst, applyErr)
			}
		}
		if projectResult.Status == "" {
			projectResult.Status = rollupPullProject(projectResult.Checkouts)
		}
		result.Projects = append(result.Projects, projectResult)
	}
	return result, worst
}

func resolvePullTargets(opts PullOptions, st *state.State, c *central.Central, roots []string) ([]pullTarget, error) {
	if opts.Name != "" {
		if _, err := c.ProjectAgentDirs(opts.Name); err != nil {
			return nil, err
		}
		if opts.Path != "" {
			path, err := resolveProjectPath(opts.Path)
			if err != nil {
				return nil, err
			}
			return []pullTarget{{name: opts.Name, path: path, replacePaths: staleCheckoutPaths(st, opts.Name, path)}}, nil
		}
		entry, ok := st.ProjectEntryFor(opts.Name)
		if !ok || len(entry.Checkouts) == 0 {
			return nil, lerr.Newf(lerr.CodeNotFound, "no checkout is recorded for %q", opts.Name).
				WithHint(fmt.Sprintf("run `lore pull %s --path <path>`, or `lore pull --all`", opts.Name))
		}
		targets := make([]pullTarget, 0, len(entry.Checkouts))
		for _, checkout := range entry.Checkouts {
			if !dirExists(checkout.Path) {
				return nil, lerr.Newf(lerr.CodeNotFound, "recorded checkout path %s no longer exists", checkout.Path).
					WithHint(fmt.Sprintf("run `lore pull %s --path <new-path>` to correct it", opts.Name))
			}
			targets = append(targets, pullTarget{name: opts.Name, path: checkout.Path})
		}
		sort.Slice(targets, func(i, j int) bool { return targets[i].path < targets[j].path })
		return targets, nil
	}

	cwd, err := resolveProjectPath("")
	if err != nil {
		return nil, err
	}
	if name, path := longestRecordedAncestor(st, cwd); name != "" {
		return []pullTarget{{name: name, path: path}}, nil
	}
	name := filepath.Base(cwd)
	if !pathUnderAnyRoot(cwd, roots) {
		return nil, lerr.Newf(lerr.CodeNotFound, "no tracked checkout resolves from %s", config.Contract(cwd)).
			WithHint("run `lore pull <name> --path <path>` explicitly")
	}
	if _, err := c.ProjectAgentDirs(name); err != nil {
		return nil, lerr.Newf(lerr.CodeNotFound, "project %q is not tracked in central", name).
			WithHint("run `lore pull --all` to scan configured project roots")
	}
	return []pullTarget{{name: name, path: cwd}}, nil
}

func staleCheckoutPaths(st *state.State, name, replacement string) []string {
	entry, ok := st.ProjectEntryFor(name)
	if !ok {
		return nil
	}
	paths := make([]string, 0, len(entry.Checkouts))
	for _, checkout := range entry.Checkouts {
		if checkout.Path != replacement && !dirExists(checkout.Path) {
			paths = append(paths, checkout.Path)
		}
	}
	return paths
}

func planPullCheckout(c *central.Central, st state.State, head string, target pullTarget, opts PullOptions, cached *CheckoutStatus) (pullPlan, error) {
	dirs, err := c.ProjectAgentDirs(target.name)
	if err != nil {
		return pullPlan{}, err
	}
	dirty, err := c.ScopedDirty(target.name, dirs)
	if err != nil {
		return pullPlan{}, err
	}
	if dirty {
		return pullPlan{}, lerr.Newf(lerr.CodeRefusedSafety, "central copy for %q has uncommitted or ignored content", target.name).
			WithHint("commit or remove the central changes before pulling")
	}

	checkout, tracked := st.CheckoutForPath(target.name, target.path)
	if !tracked {
		return planTwoWayPull(c, head, target, dirs, opts)
	}
	baseExists, err := c.CommitExists(checkout.SyncBase)
	if err != nil {
		return pullPlan{}, err
	}
	if !baseExists {
		return planTwoWayPull(c, head, target, dirs, opts)
	}

	var checkoutStatus CheckoutStatus
	if cached != nil {
		checkoutStatus = *cached
	} else {
		status, err := statusFromState(st, c, target.name, target.path)
		if err != nil {
			return pullPlan{}, err
		}
		projectStatus := status.Projects[0]
		if len(projectStatus.Issues) > 0 {
			return pullPlan{}, pullIssuesError(target.path, projectStatus.Issues)
		}
		checkoutStatus = projectStatus.Checkouts[0]
	}
	if len(checkoutStatus.Issues) > 0 {
		return pullPlan{}, pullIssuesError(target.path, checkoutStatus.Issues)
	}

	plan := pullPlan{target: target, dirs: dirs, result: PullCheckoutResult{
		Path: target.path, SyncBase: checkout.SyncBase, Dirs: []PullDirResult{},
	}}
	if opts.Central || opts.Project {
		return planForcedPull(c, head, plan, checkoutStatus, opts)
	}
	for _, dir := range checkoutStatus.Dirs {
		rootMissing := false
		for _, issue := range dir.Issues {
			if issue.Kind == reconcile.IssueCopyMissing {
				rootMissing = true
				continue
			}
			if issue.Kind == reconcile.IssueConflictSibling {
				continue
			}
			// report only the blocking issue: the skipped copy-missing entry can
			// sort ahead of it and would name the wrong cause.
			cleanupPullPaths(plan.cleanup)
			return pullPlan{}, pullIssuesError(target.path, []reconcile.Issue{issue})
		}
		dirResult := PullDirResult{Name: dir.Name, Path: filepath.Join(target.path, dir.Name), Status: PullDirUnchanged, Conflicts: []PullConflictResult{}}
		switch {
		case rootMissing:
			dirResult.Status = PullDirMaterialized
			plan.replacements = append(plan.replacements, pullReplacement(c, target, dir.Name))
			plan.recordBase = true
		case dir.State == reconcile.StateCentralAhead:
			dirResult.Status = PullDirUpdated
			plan.replacements = append(plan.replacements, pullReplacement(c, target, dir.Name))
			plan.recordBase = true
		case dir.State == reconcile.StateProjectAhead:
			dirResult.Status = PullDirProjectAhead
		case dir.State == reconcile.StateDiverged:
			mergedDir, replacement, cleanup, conflicts, err := planMergedPullDir(target, dir)
			if err != nil {
				cleanupPullPaths(plan.cleanup)
				return pullPlan{}, err
			}
			dirResult.Status = mergedDir
			dirResult.Conflicts = conflicts
			plan.replacements = append(plan.replacements, replacement)
			plan.cleanup = append(plan.cleanup, cleanup)
			plan.recordBase = true
		case dir.BaseStale:
			plan.recordBase = true
		}
		plan.result.Dirs = append(plan.result.Dirs, dirResult)
	}
	plan.result.Status = rollupPullCheckout(plan.result.Dirs, plan.recordBase)
	if plan.recordBase {
		plan.result.SyncBase = head
	}
	return plan, nil
}

func cachedPullStatuses(c *central.Central, st state.State, name, checkoutPath string) (map[string]*CheckoutStatus, error) {
	entry, ok := st.ProjectEntryFor(name)
	if !ok || len(entry.Checkouts) == 0 {
		return map[string]*CheckoutStatus{}, nil
	}
	if checkoutPath != "" {
		if _, ok := st.CheckoutForPath(name, checkoutPath); !ok {
			return map[string]*CheckoutStatus{}, nil
		}
	}
	status, err := statusFromState(st, c, name, checkoutPath)
	if err != nil {
		return nil, err
	}
	result := make(map[string]*CheckoutStatus, len(status.Projects[0].Checkouts))
	for i := range status.Projects[0].Checkouts {
		checkout := &status.Projects[0].Checkouts[i]
		result[checkout.Path] = checkout
	}
	return result, nil
}

func planTwoWayPull(c *central.Central, head string, target pullTarget, dirs []string, opts PullOptions) (pullPlan, error) {
	plan := pullPlan{target: target, dirs: dirs, recordBase: true, result: PullCheckoutResult{
		Path: target.path, Status: PullCheckoutAdopted, SyncBase: head, Dirs: []PullDirResult{},
	}}
	for _, dir := range dirs {
		projectPath := filepath.Join(target.path, dir)
		dirResult := PullDirResult{Name: dir, Path: projectPath, Status: PullDirUnchanged, Conflicts: []PullConflictResult{}}
		info, err := os.Lstat(projectPath)
		switch {
		case errors.Is(err, os.ErrNotExist):
			dirResult.Status = PullDirMaterialized
			plan.replacements = append(plan.replacements, pullReplacement(c, target, dir))
		case err != nil:
			return pullPlan{}, lerr.Wrap(lerr.CodeGenericError, err, fmt.Sprintf("cannot inspect %s", projectPath))
		case info.Mode()&os.ModeSymlink != 0 || !info.IsDir():
			return pullPlan{}, lerr.Newf(lerr.CodeRefusedSafety, "project copy %s is not a real directory", projectPath).
				WithHint("remove or relocate it before pulling")
		default:
			equal, err := reconcile.TreesEqual(projectPath, c.ProjectPath(target.name, dir))
			if err != nil {
				return pullPlan{}, err
			}
			if !equal {
				switch {
				case opts.Central:
					dirResult.Status = PullDirCentralForced
					plan.replacements = append(plan.replacements, pullReplacement(c, target, dir))
				case opts.Project:
					dirResult.Status = PullDirProjectForced
				default:
					return pullPlan{}, lerr.Newf(lerr.CodeRefusedSafety, "project copy %s differs from central without a usable sync-base", projectPath).
						WithHint("review `lore diff " + target.name + "`, then choose `lore pull " + target.name + " --central` or `--project`")
				}
			}
		}
		plan.result.Dirs = append(plan.result.Dirs, dirResult)
	}
	if len(plan.replacements) > 0 {
		plan.result.Status = PullCheckoutMaterialized
	}
	plan.result.Status = rollupPullCheckout(plan.result.Dirs, plan.recordBase)
	return plan, nil
}

func planForcedPull(c *central.Central, head string, plan pullPlan, checkout CheckoutStatus, opts PullOptions) (pullPlan, error) {
	plan.recordBase = true
	plan.result.SyncBase = head
	for _, dir := range checkout.Dirs {
		result := PullDirResult{
			Name: dir.Name, Path: filepath.Join(plan.target.path, dir.Name),
			Status: PullDirUnchanged, Conflicts: []PullConflictResult{},
		}
		if dir.State == reconcile.StateInSync && len(dir.Issues) == 0 {
			plan.result.Dirs = append(plan.result.Dirs, result)
			continue
		}
		if opts.Central {
			for _, issue := range dir.Issues {
				if issue.Kind == reconcile.IssueCopyInvalid || issue.Kind == reconcile.IssueCentralMissing || issue.Kind == reconcile.IssueInspectionFailed {
					return pullPlan{}, pullIssuesError(plan.target.path, []reconcile.Issue{issue})
				}
			}
			result.Status = PullDirCentralForced
			plan.replacements = append(plan.replacements, pullReplacement(c, plan.target, dir.Name))
		} else {
			blocking := pullIssuesExcept(dir.Issues, reconcile.IssueConflictSibling)
			if len(blocking) > 0 {
				return pullPlan{}, pullIssuesError(plan.target.path, blocking)
			}
			result.Status = PullDirProjectForced
		}
		plan.result.Dirs = append(plan.result.Dirs, result)
	}
	plan.result.Status = rollupPullCheckout(plan.result.Dirs, plan.recordBase)
	return plan, nil
}

func pullIssuesExcept(issues []reconcile.Issue, allowed reconcile.IssueKind) []reconcile.Issue {
	result := make([]reconcile.Issue, 0, len(issues))
	for _, issue := range issues {
		if issue.Kind != allowed {
			result = append(result, issue)
		}
	}
	return result
}

func planMergedPullDir(target pullTarget, dir DirSyncStatus) (PullDirStatus, reconcile.TreeReplacement, string, []PullConflictResult, error) {
	merged, err := reconcile.MergeFiles(dir.Files)
	if err != nil {
		return "", reconcile.TreeReplacement{}, "", nil, err
	}
	projectRoot := filepath.Join(target.path, dir.Name)
	staged, err := reconcile.StageSnapshot(projectRoot, merged.Snapshot)
	if err != nil {
		return "", reconcile.TreeReplacement{}, "", nil, err
	}
	conflicts := make([]PullConflictResult, 0, len(merged.Conflicts))
	hasText := false
	for _, conflict := range merged.Conflicts {
		conflicts = append(conflicts, PullConflictResult{
			Path: conflict.Path, Kind: conflict.Kind, SiblingPath: conflict.SiblingPath,
		})
		hasText = hasText || conflict.Kind == reconcile.ConflictText
	}
	status := PullDirMerged
	if hasText {
		status = PullDirConflicted
	} else if len(conflicts) > 0 {
		status = PullDirKeepBoth
	}
	return status, reconcile.TreeReplacement{Source: staged, Destination: projectRoot}, staged, conflicts, nil
}

func cleanupPullPaths(paths []string) {
	for _, path := range paths {
		_ = os.RemoveAll(path)
	}
}

func cleanupPullPlans(plans []pullPlan) {
	for _, plan := range plans {
		cleanupPullPaths(plan.cleanup)
	}
}

func pullReplacement(c *central.Central, target pullTarget, dir string) reconcile.TreeReplacement {
	return reconcile.TreeReplacement{
		Source: c.ProjectPath(target.name, dir), Destination: filepath.Join(target.path, dir),
	}
}

func applyPullPlan(st *state.State, head string, plan pullPlan) (PullCheckoutResult, error) {
	defer cleanupPullPaths(plan.cleanup)
	transaction, err := publishPullReplacements(plan, head)
	if err != nil {
		// the replacement rolled back, so the planned per-dir statuses describe
		// work that never landed.
		plan.result.Dirs = []PullDirResult{}
		return failedPullCheckout(plan.result, err)
	}
	if plan.recordBase {
		before := st.Clone()
		for _, path := range plan.target.replacePaths {
			st.RemoveCheckout(plan.target.name, path)
		}
		for _, dir := range plan.dirs {
			if err := st.AddAgentDir(plan.target.name, dir); err != nil {
				*st = before
				return failedPullCheckout(plan.result, err)
			}
		}
		if err := st.RecordCheckout(plan.target.name, plan.target.path, head); err != nil {
			*st = before
			return failedPullCheckout(plan.result, err)
		}
		if err := savePullState(*st); err != nil {
			*st = before
			saveErr := lerr.Wrap(lerr.CodeInconsistency, err, "project copy was updated but its sync-base could not be saved").
				WithHint("re-run `lore pull`; matching copies will be adopted safely")
			return failedPullCheckout(plan.result, saveErr)
		}
	}
	if transaction != nil {
		if err := transaction.finish(); err != nil {
			return failedPullCheckout(plan.result, err)
		}
	}
	detectPullUnignored(plan.target.path, &plan.result)
	return plan.result, nil
}

func failedPullCheckout(result PullCheckoutResult, err error) (PullCheckoutResult, error) {
	result.Status = PullCheckoutFailed
	// the plan set a sync-base optimistically, but a failed pull never records one.
	result.SyncBase = ""
	result.Err = err
	return result, err
}

func failedPullProjectStatus(checkout PullCheckoutResult) PullProjectStatus {
	if checkout.Status == PullCheckoutConflicted {
		return PullProjectConflicted
	}
	return PullProjectFailed
}

func detectPullUnignored(projectPath string, result *PullCheckoutResult) {
	names := make([]string, 0, len(result.Dirs))
	for _, dir := range result.Dirs {
		names = append(names, dir.Name)
	}
	flagged := unignoredDirs(projectPath, names)
	for i := range result.Dirs {
		if flagged[result.Dirs[i].Name] {
			result.Dirs[i].Unignored = true
		}
	}
}

// OfferPullGitignoreFixes offers the existing picker for unignored copies.
func OfferPullGitignoreFixes(result *PullResult) {
	for pi := range result.Projects {
		for ci := range result.Projects[pi].Checkouts {
			offerCheckoutGitignoreFixes(&result.Projects[pi].Checkouts[ci])
		}
	}
}

func offerCheckoutGitignoreFixes(checkout *PullCheckoutResult) {
	// resolving the repo root costs a git call, so only pay it once a dir
	// actually needs an entry written.
	needsFix := false
	for _, dir := range checkout.Dirs {
		needsFix = needsFix || dir.Unignored
	}
	if !needsFix {
		return
	}
	repoTop, err := git.RepoToplevel(checkout.Path)
	if err != nil {
		return
	}
	for i := range checkout.Dirs {
		dir := &checkout.Dirs[i]
		if dir.Unignored && applyGitignorePicker(dir.Name, checkout.Path, repoTop, false) {
			dir.Unignored = false
		}
	}
}

func pullIssuesError(path string, issues []reconcile.Issue) error {
	return lerr.Newf(lerr.CodeRefusedSafety, "cannot pull checkout %s: %s", path, issues[0].Message).
		WithHint("run `lore status` for the complete health report")
}

func refusedPullCheckout(path string, err error) PullCheckoutResult {
	return PullCheckoutResult{Path: path, Status: PullCheckoutRefused, Dirs: []PullDirResult{}, Err: err}
}

func rollupPullCheckout(dirs []PullDirResult, baseAdvanced bool) PullCheckoutStatus {
	hasConflicted := false
	hasCentralForced := false
	hasProjectForced := false
	hasMerged := false
	hasMaterialized := false
	hasUpdated := false
	hasProjectAhead := false
	for _, dir := range dirs {
		switch dir.Status {
		case PullDirConflicted, PullDirKeepBoth:
			hasConflicted = true
		case PullDirCentralForced:
			hasCentralForced = true
		case PullDirProjectForced:
			hasProjectForced = true
		case PullDirMerged:
			hasMerged = true
		case PullDirMaterialized:
			hasMaterialized = true
		case PullDirUpdated:
			hasUpdated = true
		case PullDirProjectAhead:
			hasProjectAhead = true
		}
	}
	if hasConflicted {
		return PullCheckoutConflicted
	}
	if hasCentralForced {
		return PullCheckoutCentralForced
	}
	if hasProjectForced {
		return PullCheckoutProjectForced
	}
	if hasMerged {
		return PullCheckoutMerged
	}
	if hasMaterialized {
		return PullCheckoutMaterialized
	}
	if hasUpdated {
		return PullCheckoutUpdated
	}
	if hasProjectAhead {
		return PullCheckoutProjectAhead
	}
	if baseAdvanced {
		return PullCheckoutAdopted
	}
	return PullCheckoutUnchanged
}

func rollupPullProject(checkouts []PullCheckoutResult) PullProjectStatus {
	for _, checkout := range checkouts {
		if checkout.Status == PullCheckoutFailed {
			return PullProjectFailed
		}
		if checkout.Status == PullCheckoutRefused {
			return PullProjectRefused
		}
	}
	for _, checkout := range checkouts {
		if checkout.Status == PullCheckoutConflicted {
			return PullProjectConflicted
		}
	}
	for _, checkout := range checkouts {
		switch checkout.Status {
		case PullCheckoutMaterialized, PullCheckoutUpdated, PullCheckoutAdopted,
			PullCheckoutMerged, PullCheckoutCentralForced, PullCheckoutProjectForced:
			return PullProjectPulled
		}
	}
	return PullProjectUnchanged
}
