package project

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nathanclairmonte/lore/internal/central"
	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/reconcile"
	"github.com/nathanclairmonte/lore/internal/state"
)

// PushOptions selects the checkouts whose project copies are captured.
type PushOptions struct {
	Project        string
	ProjectFromCWD bool
}

// PushStatus names the per-checkout outcome inside a PushResult. Push
// reads these back to decide which sync-bases to advance, so they are
// typed rather than bare strings.
type PushStatus string

const (
	// PushUnchanged means the checkout already matches central at a live base.
	PushUnchanged PushStatus = "unchanged"
	// PushPushed means the checkout's content was captured in central.
	PushPushed PushStatus = "pushed"
	// PushAdopted means content already matched and only the base advanced.
	PushAdopted PushStatus = "adopted"
)

// PushCheckoutResult describes one checkout considered by push.
type PushCheckoutResult struct {
	Path   string
	Status PushStatus
}

// PushProjectResult describes one selected project.
type PushProjectResult struct {
	Name      string
	Checkouts []PushCheckoutResult
}

// PushResult is the stable command result.
type PushResult struct {
	CentralRepo   string
	PreviousHead  string
	CommitSHA     string
	CommitMessage string
	Projects      []PushProjectResult
	ScopeProject  string
}

var savePushState = state.Save

// Push captures selected project copies in one central commit.
func Push(opts PushOptions) (PushResult, error) {
	if opts.Project != "" && opts.ProjectFromCWD {
		return PushResult{}, lerr.New(lerr.CodeConflict, "--project and --project-from-cwd cannot be used together")
	}
	ctx, err := openMutationContext()
	if err != nil {
		return PushResult{}, err
	}
	defer ctx.close()
	cfg, c, st := ctx.config, ctx.central, ctx.state
	scope := opts.Project
	cwdCheckout := ""
	if opts.ProjectFromCWD {
		// resolving cwd first keeps Status from inspecting every tracked
		// project only to discard all but one checkout below.
		scope, cwdCheckout, err = checkoutFromCWD(st)
		if err != nil {
			return PushResult{}, err
		}
	}
	status, err := statusFromState(st, c, scope, cwdCheckout)
	if err != nil {
		return PushResult{}, err
	}
	result := PushResult{CentralRepo: status.CentralRepo, PreviousHead: status.CentralHead, Projects: []PushProjectResult{}, ScopeProject: scope}
	if err := preflightPush(status); err != nil {
		return result, err
	}

	type source struct {
		name string
		path string
		dirs []string
	}
	var sources []source
	for _, projectStatus := range status.Projects {
		out := PushProjectResult{Name: projectStatus.Name, Checkouts: []PushCheckoutResult{}}
		var ahead []CheckoutStatus
		for _, checkout := range projectStatus.Checkouts {
			pushStatus := PushUnchanged
			if checkout.State == reconcile.StateProjectAhead {
				pushStatus = PushPushed
				ahead = append(ahead, checkout)
			} else if checkout.BaseStale {
				pushStatus = PushAdopted
			}
			out.Checkouts = append(out.Checkouts, PushCheckoutResult{Path: checkout.Path, Status: pushStatus})
		}
		if len(ahead) > 0 {
			chosen := ahead[0]
			allDirs := dirNames(chosen.Dirs)
			for _, other := range ahead[1:] {
				for _, dir := range allDirs {
					equal, compareErr := reconcile.TreesEqual(filepath.Join(chosen.Path, dir), filepath.Join(other.Path, dir))
					if compareErr != nil {
						return result, compareErr
					}
					if !equal {
						return result, lerr.Newf(lerr.CodeRefusedSafety, "project %q has competing changes in multiple checkouts", projectStatus.Name).
							WithHint("run `lore push --project-from-cwd` from the checkout to capture")
					}
				}
			}
			sources = append(sources, source{
				name: projectStatus.Name,
				path: chosen.Path,
				dirs: projectAheadDirNames(chosen.Dirs),
			})
		}
		result.Projects = append(result.Projects, out)
	}

	if len(sources) == 0 {
		changed, err := recordPushBases(&st, result, status.CentralHead, status.CentralHead)
		if err != nil {
			return result, err
		}
		if changed {
			if err := savePushState(st); err != nil {
				return result, err
			}
		}
		return result, nil
	}

	var replacements []central.AgentDirReplacement
	for _, src := range sources {
		for _, dir := range src.dirs {
			replacements = append(replacements, central.AgentDirReplacement{
				Project: src.name,
				Dir:     dir,
				Source:  filepath.Join(src.path, dir),
			})
		}
	}
	replacementSet, err := c.StageAgentDirReplacements(replacements)
	if err != nil {
		return result, err
	}
	names := make([]string, 0, len(sources))
	for _, src := range sources {
		names = append(names, src.name)
	}
	sort.Strings(names)
	result.CommitMessage = renderSyncTemplate(cfg.Commit.SyncTemplate, names)
	if err := c.CommitPaths(result.CommitMessage, replacementSet.Paths()...); err != nil {
		if rollbackErr := replacementSet.Rollback(); rollbackErr != nil {
			return result, lerr.Wrap(lerr.CodeInconsistency, err, "central commit failed and its replacements could not be rolled back").
				WithHint(rollbackErr.Error())
		}
		return result, err
	}
	result.CommitSHA, err = c.HeadSHA()
	if err != nil {
		return result, lerr.Wrap(lerr.CodeInconsistency, err, "central push commit succeeded but its SHA could not be read")
	}
	cleanupErr := replacementSet.Finish()
	if _, err := recordPushBases(&st, result, result.CommitSHA, status.CentralHead); err != nil {
		return result, lerr.Wrap(lerr.CodeInconsistency, err, "central copy is committed but its sync-bases could not be recorded").
			WithHint("re-run `lore push`; matching copies will be adopted safely")
	}
	if err := savePushState(st); err != nil {
		return result, lerr.Wrap(lerr.CodeInconsistency, err, "central copy is committed but local state could not be saved").
			WithHint("re-run `lore push`; matching copies will be adopted safely")
	}
	if cleanupErr != nil {
		return result, cleanupErr
	}
	return result, nil
}

// defaultSyncTemplate is the fallback commit message when commit.sync_template
// is missing or explicitly blanked. Kept in sync with default_config.toml.
const defaultSyncTemplate = "chore: sync ({projects})"

// renderSyncTemplate expands {projects} in commit.sync_template. Config
// defaulting fills a missing key with the embedded default, but a user can
// still blank the key. Handing git an empty message would fail only after
// every copy is staged, forcing a needless rollback.
func renderSyncTemplate(tmpl string, names []string) string {
	if strings.TrimSpace(tmpl) == "" {
		tmpl = defaultSyncTemplate
	}
	return strings.ReplaceAll(tmpl, "{projects}", strings.Join(names, ", "))
}

func dirNames(dirs []DirSyncStatus) []string {
	names := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		names = append(names, dir.Name)
	}
	return names
}

func projectAheadDirNames(dirs []DirSyncStatus) []string {
	names := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		if dir.State == reconcile.StateProjectAhead {
			names = append(names, dir.Name)
		}
	}
	return names
}

// recordPushBases advances the sync-base of every checkout push touched.
// It reports whether anything was recorded so a pure no-op can skip the
// state write entirely.
func recordPushBases(st *state.State, result PushResult, pushedBase, adoptedBase string) (bool, error) {
	recorded := false
	for _, projectResult := range result.Projects {
		for _, checkout := range projectResult.Checkouts {
			if checkout.Status == PushUnchanged {
				continue
			}
			base := pushedBase
			if checkout.Status == PushAdopted {
				base = adoptedBase
			}
			if err := st.RecordCheckout(projectResult.Name, checkout.Path, base); err != nil {
				return recorded, err
			}
			recorded = true
		}
	}
	return recorded, nil
}

func preflightPush(status StatusResult) error {
	for _, projectStatus := range status.Projects {
		if len(projectStatus.Issues) > 0 {
			issue := projectStatus.Issues[0]
			return lerr.Newf(lerr.CodeInconsistency, "push found %s at %s", issue.Kind, issue.Path).
				WithHint("run `lore status` for details")
		}
		for _, checkout := range projectStatus.Checkouts {
			if len(checkout.Issues) > 0 {
				issue := checkout.Issues[0]
				return lerr.Newf(lerr.CodeInconsistency, "push found %s for checkout %s at %s", issue.Kind, checkout.Path, issue.Path).
					WithHint("run `lore status` for details")
			}
			for _, dir := range checkout.Dirs {
				if len(dir.Issues) > 0 {
					issue := dir.Issues[0]
					return lerr.Newf(lerr.CodeInconsistency, "push found %s for checkout %s at %s", issue.Kind, checkout.Path, issue.Path).
						WithHint("run `lore status` for details")
				}
			}
		}
	}
	for _, projectStatus := range status.Projects {
		for _, checkout := range projectStatus.Checkouts {
			if checkout.State == reconcile.StateCentralAhead || checkout.State == reconcile.StateDiverged {
				return lerr.Newf(lerr.CodeRefusedSafety, "checkout %s is %s", checkout.Path, checkout.State).
					WithHint("run `lore pull` first")
			}
		}
	}
	return nil
}

// checkoutFromCWD resolves the tracked checkout containing the current
// directory. Nesting resolves to the innermost checkout, matching how
// cwd-scoped push resolves a project from its recorded checkout path.
func checkoutFromCWD(st state.State) (name, path string, err error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", "", lerr.Wrap(lerr.CodeGenericError, err, "cannot determine current working directory")
	}
	// canonicalize so the comparison against canonical recorded paths holds
	// even when cwd traverses intermediate symlinks.
	if resolved, resolveErr := filepath.EvalSymlinks(cwd); resolveErr == nil {
		cwd = resolved
	}
	if name, path = longestRecordedAncestor(&st, cwd); name != "" {
		return name, path, nil
	}
	return "", "", lerr.New(lerr.CodeNotFound, "cwd is not inside a tracked project").
		WithHint("run `lore add`, or invoke `lore push` without --project-from-cwd")
}
