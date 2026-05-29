package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nathanclairmonte/lore/internal/central"
	"github.com/nathanclairmonte/lore/internal/config"
	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/git"
	"github.com/nathanclairmonte/lore/internal/link"
	"github.com/nathanclairmonte/lore/internal/output"
	"github.com/nathanclairmonte/lore/internal/scan"
	"github.com/nathanclairmonte/lore/internal/state"
)

// LinkDirStatus names the per-dir outcome inside a LinkResult.
type LinkDirStatus string

const (
	// LinkStatusLinked is set when the symlink was missing and created in
	// this run.
	LinkStatusLinked LinkDirStatus = "linked"
	// LinkStatusAlreadyLinked is set when a correct symlink (one pointing
	// at the expected central target) was already in place, so this run
	// was a no-op for that dir.
	LinkStatusAlreadyLinked LinkDirStatus = "already-linked"
	// LinkStatusRefused is set when something other than a lore symlink
	// sits at the link location: a real directory or file (drift B), or a
	// symlink pointing somewhere unexpected (drift C). The per-dir Err
	// carries the reason; refused dirs are left untouched on disk.
	LinkStatusRefused LinkDirStatus = "refused"
)

// LinkDirResult is the per-dir outcome inside a LinkResult. Err is
// populated only when Status is LinkStatusRefused. Unignored is true when
// a correctly-linked dir ended up not gitignored and lore could not (or
// was not allowed to) fix it; the renderer surfaces an advisory warning.
type LinkDirResult struct {
	Name      string
	Status    LinkDirStatus
	LinkPath  string // absolute path where the symlink lives (or would)
	Err       error
	Unignored bool
}

// LinkResult is the structured outcome of a Link. cmd/lore/link.go
// converts it to the output package's render type.
type LinkResult struct {
	ProjectName string
	ProjectPath string
	Dirs        []LinkDirResult
}

// AllAlreadyLinked reports whether every dir was already correctly linked
// before this run (a fully idempotent no-op), so the renderer can print a
// terse re-run message. False for an empty dir set.
func (r LinkResult) AllAlreadyLinked() bool {
	if len(r.Dirs) == 0 {
		return false
	}
	for _, d := range r.Dirs {
		if d.Status != LinkStatusAlreadyLinked {
			return false
		}
	}
	return true
}

// LinkOptions selects which project to link and where. An empty Name means
// "resolve the project from cwd" (bare `lore link`). Path overrides the
// on-disk location and requires Name; an empty Path falls back to the
// recorded local-state path (or cwd, in the bare form). Batch is set by
// `lore link --all` to keep the run non-interactive: the gitignore
// re-check still runs but never prompts, so a recovery sweep can't stall
// on a picker.
type LinkOptions struct {
	Name  string
	Path  string
	Batch bool
}

// Link (re)creates the symlinks for a tracked project at its on-disk
// location, each pointing at projects/<name>/<dir>/ in the central repo,
// and records the path to local state. Used after a fresh clone, after
// losing local state, or any time a symlink goes missing.
//
// Central is never modified: the agent dirs already live there, so there
// is no commit. state.Save is the success boundary (mirrors unlink).
//
// Per-dir verdicts: a missing symlink is created (linked); a correct
// symlink is left alone (already-linked); a real dir/file (drift B) or a
// wrong-target symlink (drift C) is refused and left untouched. Unlike
// unlink, a refusal does NOT abort the safe dirs: lore links what it can,
// records those, and returns CodeRefusedSafety (exit 5) so the caller sees
// both halves of the outcome (mirrors add's partial-success contract).
//
// After linking, each correctly-linked dir is re-checked with
// git check-ignore. An un-ignored dir triggers an advisory offer to
// re-apply the gitignore picker (human, interactive runs only); declining,
// a non-interactive context, or Batch leaves a warning but never fails the
// run.
func Link(opts LinkOptions) (LinkResult, error) {
	cfg, _, err := config.Load()
	if err != nil {
		return LinkResult{}, err
	}
	st, _, err := state.Load()
	if err != nil {
		return LinkResult{}, err
	}
	c, err := openCentral(cfg.CentralRepo)
	if err != nil {
		return LinkResult{}, err
	}

	name, projectPath, err := resolveLinkTarget(opts, &st, c)
	if err != nil {
		return LinkResult{}, err
	}

	// success-gated unwind: rollback closures appended LIFO, drained on any
	// failure before state.Save lands. state.Save is the success boundary
	// (no central commit happens in link). An aggregated drift refusal is
	// NOT a failure for this purpose: the safe dirs are meant to persist,
	// so success flips true even when the refusal error is returned.
	success := false
	var rollbacks []func()
	defer func() {
		if success {
			return
		}
		for i := len(rollbacks) - 1; i >= 0; i-- {
			rollbacks[i]()
		}
	}()

	helperRollbacks, result, linkErr := linkProject(&st, c, name, projectPath)
	rollbacks = append(rollbacks, helperRollbacks...)
	// a hard failure (I/O mid-run) returns an empty result and rolls back;
	// an aggregated refusal returns a populated result whose safe dirs we
	// still want to save.
	if linkErr != nil && lerr.CodeOf(linkErr) != lerr.CodeRefusedSafety {
		return LinkResult{}, linkErr
	}

	if err := state.Save(st); err != nil {
		return LinkResult{}, err
	}
	success = true

	// advisory gitignore re-check, after the success boundary so it can
	// never roll back a link. mutates result.Dirs[].Unignored in place.
	recheckGitignore(projectPath, &result, opts.Batch)

	return result, linkErr
}

// LinkProjectStatus names the per-project outcome inside a LinkAllResult.
type LinkProjectStatus string

const (
	// LinkProjectLinked is set when at least one of the project's symlinks
	// was created this run.
	LinkProjectLinked LinkProjectStatus = "linked"
	// LinkProjectAlreadyLinked is set when every symlink was already correct.
	LinkProjectAlreadyLinked LinkProjectStatus = "already-linked"
	// LinkProjectRefused is set when the project was located but a drift
	// refusal (drift B/C) blocked one or more dirs.
	LinkProjectRefused LinkProjectStatus = "refused"
	// LinkProjectFailed is set when a hard (non-refusal) error hit this
	// project; the sweep records it and moves on rather than aborting.
	LinkProjectFailed LinkProjectStatus = "failed"
	// LinkProjectAmbiguous is set when scanning found the project in more
	// than one root, so lore can't pick a path unattended.
	LinkProjectAmbiguous LinkProjectStatus = "ambiguous"
	// LinkProjectMissing is set when the project has no valid recorded path
	// and scanning the roots found no match.
	LinkProjectMissing LinkProjectStatus = "missing"
)

// LinkProjectResult is the per-project outcome inside a LinkAllResult.
// Link carries the per-dir detail for linked/already-linked/refused;
// Matches lists the candidate paths for ambiguous; Err is set for
// refused/failed.
type LinkProjectResult struct {
	Name    string
	Status  LinkProjectStatus
	Path    string
	Matches []string
	Link    LinkResult
	Err     error
}

// LinkAllResult is the structured outcome of a LinkAll. ScannedRoots is
// the configured roots that were searched; Projects is one entry per
// tracked project, sorted by name.
type LinkAllResult struct {
	ScannedRoots []string
	Projects     []LinkProjectResult
}

// LinkAll is the new-machine recovery sweep. It enumerates every tracked
// project from central (not local state, so projects never linked on this
// machine still surface), then for each one:
//
//   - uses the recorded local-state path when it exists and still points at
//     a real directory;
//   - otherwise scans projects_roots (depth-limited) for a basename match:
//     exactly one match links there (correcting any stale recorded path),
//     several matches report "ambiguous" with a disambiguation command per
//     candidate, and none reports "missing".
//
// Fully non-interactive: per-project linking runs in Batch mode so the
// gitignore re-check never prompts mid-sweep. Missing and ambiguous are
// not failures (exit 0; the summary lists next steps); a drift refusal on a
// located project returns CodeRefusedSafety (exit 5), and a hard error
// returns its code. The sweep never aborts on a per-project problem - it
// records the verdict and continues, returning the worst code seen.
func LinkAll() (LinkAllResult, error) {
	cfg, _, err := config.Load()
	if err != nil {
		return LinkAllResult{}, err
	}
	st, _, err := state.Load()
	if err != nil {
		return LinkAllResult{}, err
	}
	c, err := openCentral(cfg.CentralRepo)
	if err != nil {
		return LinkAllResult{}, err
	}

	names, err := c.ListProjects()
	if err != nil {
		return LinkAllResult{}, err
	}

	result := LinkAllResult{ScannedRoots: append([]string(nil), cfg.ProjectsRoots...)}
	if len(names) == 0 {
		return result, nil
	}

	// partition up front: projects with a valid recorded path are linked
	// straight from it; the rest need locating, so collect their names for a
	// single scan pass. a stale recorded path (directory gone) falls through
	// to scanning so a relocated checkout is found at its new home.
	hasRecordedPath := make(map[string]bool, len(names))
	var needScan []string
	for _, name := range names {
		if p, ok := st.PathFor(name); ok && dirExists(p) {
			hasRecordedPath[name] = true
			continue
		}
		needScan = append(needScan, name)
	}

	scanned, err := scan.ScanRoots(cfg.ProjectsRoots, cfg.ProjectsDepth, needScan)
	if err != nil {
		return LinkAllResult{}, err
	}

	var worst error
	for _, name := range names {
		var pr LinkProjectResult
		switch matches := scanned[name]; {
		case hasRecordedPath[name]:
			// empty path lets Link use the recorded location.
			pr = linkOneInSweep(name, "")
		case len(matches) == 1:
			pr = linkOneInSweep(name, matches[0])
		case len(matches) > 1:
			pr = LinkProjectResult{Name: name, Status: LinkProjectAmbiguous, Matches: matches}
		default:
			pr = LinkProjectResult{Name: name, Status: LinkProjectMissing}
		}
		result.Projects = append(result.Projects, pr)
		if pr.Err != nil {
			worst = promoteWorst(worst, pr.Err)
		}
	}
	return result, worst
}

// linkOneInSweep links a single project inside a --all sweep (Batch mode)
// and classifies the outcome into a per-project verdict. path empty means
// "use the recorded local-state path"; a non-empty path relocates there.
func linkOneInSweep(name, path string) LinkProjectResult {
	res, err := Link(LinkOptions{Name: name, Path: path, Batch: true})
	pr := LinkProjectResult{Name: name, Link: res, Path: res.ProjectPath}
	switch {
	case err != nil && lerr.CodeOf(err) == lerr.CodeRefusedSafety:
		pr.Status = LinkProjectRefused
		pr.Err = err
	case err != nil:
		pr.Status = LinkProjectFailed
		pr.Err = err
	case res.AllAlreadyLinked():
		pr.Status = LinkProjectAlreadyLinked
	default:
		pr.Status = LinkProjectLinked
	}
	return pr
}

// openCentral opens the configured central repo, translating the
// "not there yet" case into a CodeSetupMissing error that points the user
// at `lore init`. Shared by Link and LinkAll.
func openCentral(centralRepo string) (*central.Central, error) {
	c, err := central.Open(centralRepo)
	if err != nil && lerr.CodeOf(err) == lerr.CodeNotFound {
		return nil, lerr.Newf(lerr.CodeSetupMissing,
			"central repo not found at %s", config.Contract(centralRepo)).
			WithHint("run `lore init` first")
	}
	return c, err
}

// dirExists reports whether path exists and is a directory (following
// symlinks: a project root reached via a symlinked parent still counts).
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// promoteWorst keeps the worst error across a sweep, preferring a safety
// refusal over other codes so scripts see exit 5 regardless of the order
// projects were processed in (mirrors UnlinkAll's promotion).
func promoteWorst(worst, err error) error {
	if worst == nil {
		return err
	}
	if lerr.CodeOf(err) == lerr.CodeRefusedSafety && lerr.CodeOf(worst) != lerr.CodeRefusedSafety {
		return err
	}
	return worst
}

// linkProject is the shared core for Link (and, via Link, LinkAll). Given
// a resolved project name and on-disk path it classifies every tracked
// agent dir, creates the missing symlinks, records (or relocates) the
// project in local state in memory, and returns LIFO rollback closures.
//
// Does NOT call state.Save and does NOT run the gitignore re-check; the
// caller owns the success boundary and the (interactive or batch)
// gitignore policy.
//
// On an aggregated drift refusal it returns a fully-populated result
// alongside the joined CodeRefusedSafety error: the safe dirs are already
// linked and recorded, so the caller saves them and surfaces the refusal.
// On a hard failure it returns an empty result and the rollbacks gathered
// so far, leaving the caller's unwind to undo any partial side effects.
func linkProject(st *state.State, c *central.Central, name, projectPath string) (
	rollbacks []func(),
	result LinkResult,
	err error,
) {
	dirNames, err := c.ProjectAgentDirs(name)
	if err != nil {
		return nil, LinkResult{}, err
	}

	result.ProjectName = name
	result.ProjectPath = projectPath

	// single pass: classify each dir and act on it immediately. unlike
	// unlink (which must know every refusal before touching disk, since one
	// refusal aborts the whole run), link's refusals don't abort the safe
	// dirs, so there's no need to plan first. refused dirs are reported and
	// left untouched; the rest are linked if missing and recorded either
	// way. recorded never includes refused dirs.
	var refusals []error
	var recorded []string
	for _, dirName := range dirNames {
		linkPath := filepath.Join(projectPath, dirName)
		expected := c.ProjectPath(name, dirName)
		status, classifyErr := classifyLinkDir(linkPath, expected)
		if classifyErr != nil {
			refusals = append(refusals, classifyErr)
			result.Dirs = append(result.Dirs, LinkDirResult{
				Name:     dirName,
				Status:   LinkStatusRefused,
				LinkPath: linkPath,
				Err:      classifyErr,
			})
			continue
		}
		if status == LinkStatusLinked {
			if createErr := link.Create(expected, linkPath); createErr != nil {
				return rollbacks, LinkResult{}, createErr
			}
			lp := linkPath
			rollbacks = append(rollbacks, func() { _ = link.Remove(lp) })
		}
		result.Dirs = append(result.Dirs, LinkDirResult{
			Name:     dirName,
			Status:   status,
			LinkPath: linkPath,
		})
		recorded = append(recorded, dirName)
	}

	// record (or relocate) the project in local state. snapshot the prior
	// entry first so the unwind restores it exactly. an existing entry at a
	// DIFFERENT path is overwritten: `lore link --path` and `--all`'s scan
	// both intentionally correct a stale recorded path, treating the new
	// location as authoritative (state's AddAgentDir otherwise refuses a
	// path change as drift).
	origEntry, hadEntry := st.ProjectEntryFor(name)
	rollbacks = append(rollbacks, func() {
		if hadEntry {
			if st.Projects == nil {
				st.Projects = map[string]state.ProjectEntry{}
			}
			st.Projects[name] = origEntry
			return
		}
		if st.Projects != nil {
			delete(st.Projects, name)
		}
	})
	if hadEntry && origEntry.Path != projectPath {
		delete(st.Projects, name)
	}
	for _, dirName := range recorded {
		if addErr := st.AddAgentDir(name, projectPath, dirName); addErr != nil {
			return rollbacks, LinkResult{}, addErr
		}
	}

	if len(refusals) > 0 {
		return rollbacks, result, errors.Join(refusals...)
	}
	return rollbacks, result, nil
}

// classifyLinkDir inspects linkPath against the central target lore
// expects it to point at, returning the per-dir verdict: LinkStatusLinked
// when nothing is there yet (the caller creates the symlink),
// LinkStatusAlreadyLinked when a correct symlink is already in place, or
// LinkStatusRefused with a CodeRefusedSafety error for a real dir/file
// (drift B) or a wrong-target symlink (drift C). Both refusals leave
// linkPath untouched. Pure read; no mutation.
func classifyLinkDir(linkPath, expectedTarget string) (LinkDirStatus, error) {
	info, lstatErr := os.Lstat(linkPath)
	if lstatErr != nil {
		if errors.Is(lstatErr, os.ErrNotExist) {
			return LinkStatusLinked, nil
		}
		return "", lerr.Wrap(lerr.CodeGenericError, lstatErr,
			fmt.Sprintf("cannot stat %s", linkPath))
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return LinkStatusRefused, lerr.Newf(lerr.CodeRefusedSafety,
			"%s already exists and is not a symlink (refusing to clobber a real directory or file)",
			linkPath).
			WithHint("remove it (or run `lore unlink` first), then re-run `lore link`")
	}
	// link.Validate does the exact-string compare; a mismatch is drift C.
	if validErr := link.Validate(linkPath, expectedTarget); validErr != nil {
		return LinkStatusRefused, lerr.Newf(lerr.CodeRefusedSafety,
			"%s is a symlink but points somewhere other than %s",
			linkPath, expectedTarget).
			WithHint("remove it (or run `lore unlink` first), then re-run `lore link`")
	}
	return LinkStatusAlreadyLinked, nil
}

// resolveLinkTarget turns LinkOptions into a concrete (name, projectPath).
//
// Named form: central must track the name (else CodeNotFound). --path wins;
// otherwise the recorded local-state path is used (CodeNotFound, with a
// --path / --all hint, when none is recorded or the recorded one is gone).
//
// Bare form (resolve from cwd): (a) the longest recorded path that is cwd
// or an ancestor of cwd wins outright; else (b) cwd's basename matching a
// tracked central project name links there, recording cwd; else
// CodeNotFound with a --path / --all hint.
func resolveLinkTarget(opts LinkOptions, st *state.State, c *central.Central) (name, projectPath string, err error) {
	if opts.Name != "" {
		if _, derr := c.ProjectAgentDirs(opts.Name); derr != nil {
			if lerr.CodeOf(derr) == lerr.CodeNotFound {
				return "", "", lerr.Newf(lerr.CodeNotFound,
					"project %q is not tracked in the central repo", opts.Name).
					WithHint("run `lore ls` to see tracked projects, or `lore add` from the project to track it")
			}
			return "", "", derr
		}
		if opts.Path != "" {
			p, perr := resolveProjectPath(opts.Path)
			if perr != nil {
				return "", "", perr
			}
			return opts.Name, p, nil
		}
		recorded, ok := st.PathFor(opts.Name)
		if !ok {
			return "", "", lerr.Newf(lerr.CodeNotFound,
				"no on-disk path recorded for %q on this machine", opts.Name).
				WithHint("pass --path <path>, or run `lore link --all` to scan your projects_roots")
		}
		p, perr := resolveProjectPath(recorded)
		if perr != nil {
			return "", "", lerr.Newf(lerr.CodeNotFound,
				"recorded path for %q (%s) no longer exists", opts.Name, config.Contract(recorded)).
				WithHint("pass --path <new-path>, or run `lore link --all` to relocate it")
		}
		return opts.Name, p, nil
	}

	cwd, gerr := os.Getwd()
	if gerr != nil {
		return "", "", lerr.Wrap(lerr.CodeGenericError, gerr,
			"cannot determine current working directory")
	}
	// canonicalize so the comparison against canonical recorded paths holds
	// even when cwd traverses intermediate symlinks.
	canon := cwd
	if resolved, rerr := filepath.EvalSymlinks(cwd); rerr == nil {
		canon = resolved
	}

	if n, p := longestRecordedAncestor(st, canon); n != "" {
		return n, p, nil
	}

	base := filepath.Base(canon)
	names, listErr := c.ListProjects()
	if listErr != nil {
		return "", "", listErr
	}
	for _, n := range names {
		if n == base {
			return n, canon, nil
		}
	}

	return "", "", lerr.Newf(lerr.CodeNotFound,
		"no tracked project resolves from %s", config.Contract(canon)).
		WithHint("run `lore link <name> --path <path>`, or `lore link --all` to scan your projects_roots")
}

// longestRecordedAncestor returns the tracked project whose recorded path
// is dir itself or the nearest ancestor of dir. Longest match wins so a
// project nested inside another resolves to the inner one.
func longestRecordedAncestor(st *state.State, dir string) (name, path string) {
	for n, entry := range st.Projects {
		if !isSelfOrAncestor(entry.Path, dir) {
			continue
		}
		if len(entry.Path) > len(path) {
			name, path = n, entry.Path
		}
	}
	return name, path
}

// isSelfOrAncestor reports whether descendant is ancestor itself or lives
// underneath it, using a cleaned relative path so ".." escapes don't count.
func isSelfOrAncestor(ancestor, descendant string) bool {
	rel, err := filepath.Rel(ancestor, descendant)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// recheckGitignore re-runs git check-ignore on each correctly-linked dir
// and, for any that come up un-ignored, either offers the gitignore picker
// (interactive human runs) or just flags Unignored for an advisory
// warning (batch, non-TTY, JSON, or a declined picker). Always advisory:
// it never fails the run and never rolls back a link.
//
// A project that isn't a git repo is skipped wholesale: check-ignore folds
// "not a repo" into "not ignored", so without a repo there is nothing to
// ignore and nothing worth warning about.
func recheckGitignore(projectPath string, result *LinkResult, batch bool) {
	repoTop, err := git.RepoToplevel(projectPath)
	if err != nil {
		return
	}
	for i := range result.Dirs {
		d := &result.Dirs[i]
		if d.Status == LinkStatusRefused {
			continue
		}
		ignored, checkErr := git.CheckIgnore(projectPath, d.Name)
		if checkErr != nil || ignored {
			continue
		}
		if batch || !applyGitignorePicker(d.Name, projectPath, repoTop) {
			d.Unignored = true
		}
	}
}

// applyGitignorePicker offers the slice-4 picker for dirName and applies
// the chosen entry, returning true only when the dir ends up ignored and
// verified. Any non-interactive context, a declined picker, a write
// failure, or a still-not-ignored re-check returns false so the caller
// leaves an advisory warning. Reuses lore add's gitignore helpers.
func applyGitignorePicker(dirName, projectPath, repoTop string) bool {
	target, err := pickGitignoreTarget(dirName)
	if err != nil {
		return false
	}
	targetPath, err := resolveGitignoreTarget(target, repoTop)
	if err != nil {
		return false
	}
	pattern := ignorePatternFor(target, dirName, projectPath, repoTop)
	if err := git.AppendIgnoreEntry(targetPath, pattern); err != nil {
		return false
	}
	ignored, err := git.CheckIgnore(projectPath, dirName)
	if err != nil || !ignored {
		return false
	}
	output.Success("added %s to %s", pattern, config.Contract(targetPath))
	return true
}
