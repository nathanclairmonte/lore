package project

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nathanclairmonte/lore/internal/central"
	"github.com/nathanclairmonte/lore/internal/config"
	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/reconcile"
	"github.com/nathanclairmonte/lore/internal/state"
)

// StatusResult is the complete read-only sync report.
type StatusResult struct {
	CentralRepo string
	CentralHead string
	Projects    []ProjectStatus
}

// ProjectStatus groups every recorded checkout for one project.
type ProjectStatus struct {
	Name      string
	Checkouts []CheckoutStatus
	Issues    []reconcile.Issue
}

// CheckoutStatus is the rollup for one independently tracked checkout.
type CheckoutStatus struct {
	Path      string
	SyncBase  string
	State     reconcile.SyncState
	BaseStale bool
	Dirs      []DirSyncStatus
	Issues    []reconcile.Issue
}

// DirSyncStatus contains one agent directory's classification.
type DirSyncStatus struct {
	Name      string
	State     reconcile.SyncState
	BaseStale bool
	Files     []reconcile.FileResult
	Issues    []reconcile.Issue
}

// Status inspects one named project or all tracked projects.
func Status(projectName string) (StatusResult, error) {
	st, _, err := state.Load()
	if err != nil {
		return StatusResult{}, err
	}
	cfg, _, err := config.Load()
	if err != nil {
		return StatusResult{}, err
	}
	c, err := openCentral(cfg.CentralRepo)
	if err != nil {
		return StatusResult{}, err
	}
	return statusFromState(st, c, projectName, "")
}

func statusFromState(st state.State, c *central.Central, projectName, checkoutPath string) (StatusResult, error) {
	if projectName != "" {
		entry, ok := st.ProjectEntryFor(projectName)
		if !ok {
			return StatusResult{}, lerr.Newf(lerr.CodeNotFound, "project %q is not tracked", projectName)
		}
		if checkoutPath != "" {
			checkout, ok := st.CheckoutForPath(projectName, checkoutPath)
			if !ok {
				return StatusResult{}, lerr.Newf(lerr.CodeNotFound, "checkout %s is not tracked for project %q", checkoutPath, projectName)
			}
			// keep scoped inspection from changing the caller's state map.
			// push still needs every unselected sibling when it saves state.
			st = st.Clone()
			entry.Checkouts = []state.CheckoutEntry{checkout}
			st.Projects[projectName] = entry
		}
	}
	head, err := c.HeadSHA()
	if err != nil {
		return StatusResult{}, err
	}

	names := make([]string, 0, len(st.Projects))
	for name := range st.Projects {
		if projectName == "" || name == projectName {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	result := StatusResult{CentralRepo: c.Path, CentralHead: head, Projects: make([]ProjectStatus, 0, len(names))}
	trees := newTreeCache(c)
	for _, name := range names {
		entry := st.Projects[name]
		projectStatus, err := inspectProject(trees, head, name, entry)
		if err != nil {
			return result, err
		}
		result.Projects = append(result.Projects, projectStatus)
	}
	return result, nil
}

// Diff returns status data narrowed to current project-central differences.
func Diff(projectName string) (StatusResult, error) {
	result, err := Status(projectName)
	if err != nil {
		return result, err
	}
	for pi := range result.Projects {
		for ci := range result.Projects[pi].Checkouts {
			for di := range result.Projects[pi].Checkouts[ci].Dirs {
				dir := &result.Projects[pi].Checkouts[ci].Dirs[di]
				dir.Files = filterCurrentDifferences(dir.Files)
			}
		}
	}
	return result, nil
}

// Unhealthy reports whether status must return CodeInconsistency.
func (r StatusResult) Unhealthy() bool {
	return r.HasIssues() || r.hasDivergedCheckout()
}

func (r StatusResult) hasDivergedCheckout() bool {
	for _, project := range r.Projects {
		for _, checkout := range project.Checkouts {
			if checkout.State == reconcile.StateDiverged {
				return true
			}
		}
	}
	return false
}

// HasIssues reports whether inspection found a health issue.
func (r StatusResult) HasIssues() bool {
	for _, project := range r.Projects {
		if len(project.Issues) > 0 {
			return true
		}
		for _, checkout := range project.Checkouts {
			if len(checkout.Issues) > 0 {
				return true
			}
			for _, dir := range checkout.Dirs {
				if len(dir.Issues) > 0 {
					return true
				}
			}
		}
	}
	return false
}

// treeCache memoises central reads for the length of one Status call. Every
// checkout of a project shares HEAD and commonly shares a sync-base, so the
// same tree would otherwise be read from Git once per checkout.
type treeCache struct {
	central   *central.Central
	snapshots map[string]cachedSnapshot
	commits   map[string]bool
}

type cachedSnapshot struct {
	tree   reconcile.Snapshot
	issues []reconcile.Issue
}

func newTreeCache(c *central.Central) *treeCache {
	return &treeCache{
		central:   c,
		snapshots: map[string]cachedSnapshot{},
		commits:   map[string]bool{},
	}
}

// snapshot returns the committed content of one agent directory. Callers must
// treat the result as read-only because it is shared between checkouts.
func (t *treeCache) snapshot(commit, projectName, dirName string) (reconcile.Snapshot, []reconcile.Issue, error) {
	key := strings.Join([]string{commit, projectName, dirName}, "\x00")
	if snapshot, ok := t.snapshots[key]; ok {
		return snapshot.tree, snapshot.issues, nil
	}
	snapshot, issues, err := t.central.Snapshot(commit, projectName, dirName)
	if err != nil {
		return nil, nil, err
	}
	t.snapshots[key] = cachedSnapshot{tree: snapshot, issues: issues}
	return snapshot, issues, nil
}

func (t *treeCache) commitExists(sha string) (bool, error) {
	if exists, ok := t.commits[sha]; ok {
		return exists, nil
	}
	exists, err := t.central.CommitExists(sha)
	if err != nil {
		return false, err
	}
	t.commits[sha] = exists
	return exists, nil
}

func inspectProject(trees *treeCache, head, name string, entry state.ProjectEntry) (ProjectStatus, error) {
	c := trees.central
	dirs := append([]string(nil), entry.AgentDirs...)
	centralDirs, err := c.ProjectAgentDirs(name)
	if err == nil {
		dirs = unionStrings(dirs, centralDirs)
	} else if lerr.CodeOf(err) != lerr.CodeNotFound {
		return ProjectStatus{}, err
	}
	dirty, err := c.ScopedDirty(name, dirs)
	if err != nil {
		return ProjectStatus{}, err
	}
	checkouts := append([]state.CheckoutEntry(nil), entry.Checkouts...)
	sort.Slice(checkouts, func(i, j int) bool { return checkouts[i].Path < checkouts[j].Path })
	sort.Strings(dirs)
	result := ProjectStatus{Name: name, Checkouts: make([]CheckoutStatus, 0, len(checkouts)), Issues: []reconcile.Issue{}}
	if dirty {
		result.Issues = append(result.Issues, reconcile.Issue{
			Kind: reconcile.IssueCentralDirty, Path: filepath.Join(c.Path, "projects", name),
			Message: "central copy has uncommitted or ignored content",
		})
	}
	for _, checkout := range checkouts {
		baseExists, err := trees.commitExists(checkout.SyncBase)
		if err != nil {
			return result, err
		}
		checkoutStatus := CheckoutStatus{
			Path: checkout.Path, SyncBase: checkout.SyncBase,
			Dirs: make([]DirSyncStatus, 0, len(dirs)), Issues: []reconcile.Issue{},
		}
		if !baseExists {
			checkoutStatus.Issues = append(checkoutStatus.Issues, reconcile.Issue{
				Kind: reconcile.IssueBaseUnreachable, Path: checkout.SyncBase,
				Message: "sync-base is not a resolvable commit",
			})
		}
		for _, dirName := range dirs {
			dirStatus, err := inspectDir(trees, head, name, dirName, checkout, baseExists)
			if err != nil {
				return result, err
			}
			checkoutStatus.Dirs = append(checkoutStatus.Dirs, dirStatus)
			checkoutStatus.BaseStale = checkoutStatus.BaseStale || dirStatus.BaseStale
		}
		checkoutStatus.State = rollupDirs(checkoutStatus.Dirs)
		reconcile.SortIssues(checkoutStatus.Issues)
		result.Checkouts = append(result.Checkouts, checkoutStatus)
	}
	reconcile.SortIssues(result.Issues)
	return result, nil
}

func unionStrings(left, right []string) []string {
	seen := make(map[string]struct{}, len(left)+len(right))
	result := make([]string, 0, len(left)+len(right))
	for _, values := range [][]string{left, right} {
		for _, value := range values {
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	return result
}

func inspectDir(trees *treeCache, head, projectName, dirName string, checkout state.CheckoutEntry, baseExists bool) (DirSyncStatus, error) {
	projectPath := filepath.Join(checkout.Path, dirName)
	projectSnapshot, issues, err := reconcile.InspectTree(projectPath)
	if err != nil {
		return DirSyncStatus{}, err
	}
	centralPath := trees.central.ProjectPath(projectName, dirName)
	if info, err := os.Lstat(centralPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			issues = append(issues, reconcile.Issue{Kind: reconcile.IssueCentralMissing, Path: centralPath, Message: "central copy is missing"})
		} else {
			issues = append(issues, reconcile.Issue{Kind: reconcile.IssueInspectionFailed, Path: centralPath, Message: err.Error()})
		}
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		issues = append(issues, reconcile.Issue{Kind: reconcile.IssueCentralMissing, Path: centralPath, Message: "central copy is not a real directory"})
	}
	centralSnapshot, centralIssues, err := trees.snapshot(head, projectName, dirName)
	if err != nil {
		return DirSyncStatus{}, err
	}
	issues = mergeIssues(issues, centralIssues)
	if hasRootCopyIssue(issues) {
		state := reconcile.StateInSync
		if len(centralSnapshot) > 0 {
			state = reconcile.StateCentralAhead
		}
		return DirSyncStatus{
			Name: dirName, State: state,
			Files: []reconcile.FileResult{}, Issues: issues,
		}, nil
	}
	baseSnapshot := reconcile.Snapshot{}
	if baseExists {
		var baseIssues []reconcile.Issue
		baseSnapshot, baseIssues, err = trees.snapshot(checkout.SyncBase, projectName, dirName)
		if err != nil {
			return DirSyncStatus{}, err
		}
		issues = mergeIssues(issues, baseIssues)
	}
	classified := reconcile.Classify(projectSnapshot, centralSnapshot, baseSnapshot, issues)
	return DirSyncStatus{
		Name: dirName, State: classified.State, BaseStale: classified.BaseStale,
		Files: classified.Files, Issues: classified.Issues,
	}, nil
}

func mergeIssues(groups ...[]reconcile.Issue) []reconcile.Issue {
	seen := map[reconcile.Issue]struct{}{}
	merged := []reconcile.Issue{}
	for _, issues := range groups {
		for _, issue := range issues {
			if _, ok := seen[issue]; ok {
				continue
			}
			seen[issue] = struct{}{}
			merged = append(merged, issue)
		}
	}
	reconcile.SortIssues(merged)
	return merged
}

func hasRootCopyIssue(issues []reconcile.Issue) bool {
	for _, issue := range issues {
		if issue.Kind == reconcile.IssueCopyMissing || issue.Kind == reconcile.IssueCopyInvalid {
			return true
		}
	}
	return false
}

func rollupDirs(dirs []DirSyncStatus) reconcile.SyncState {
	states := make([]reconcile.SyncState, 0, len(dirs))
	for _, dir := range dirs {
		states = append(states, dir.State)
	}
	return reconcile.Rollup(states)
}

func filterCurrentDifferences(files []reconcile.FileResult) []reconcile.FileResult {
	filtered := make([]reconcile.FileResult, 0, len(files))
	for _, file := range files {
		if file.State != reconcile.StateInSync {
			filtered = append(filtered, file)
		}
	}
	return filtered
}
