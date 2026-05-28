// Package project orchestrates the multi-step use cases behind lore's
// project subcommands (add, path, and in later slices rm/mv/unlink/link).
// It composes the deep modules - config, state, central, git, detect,
// link - so the cmd/lore/* files stay thin Cobra adapters.
package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nathanclairmonte/lore/internal/central"
	"github.com/nathanclairmonte/lore/internal/config"
	"github.com/nathanclairmonte/lore/internal/detect"
	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/git"
	"github.com/nathanclairmonte/lore/internal/link"
	"github.com/nathanclairmonte/lore/internal/output"
	"github.com/nathanclairmonte/lore/internal/prompt"
	"github.com/nathanclairmonte/lore/internal/state"
)

// DirStatus names the per-dir outcome inside an AddResult.
type DirStatus string

const (
	// StatusAdded is set when the dir was freshly onboarded in this run.
	StatusAdded DirStatus = "added"
	// StatusAlreadyTracked is set when state already mapped the dir to
	// central and the on-disk symlink is valid; the dir is reported but
	// no side effects fired for it.
	StatusAlreadyTracked DirStatus = "already-tracked"
	// StatusSkippedSafety is set when a slice-3 safety check refused this
	// dir; other dirs in the same run may still have been added.
	StatusSkippedSafety DirStatus = "skipped-safety"
	// StatusScaffolded is set when --create created this dir fresh in
	// this run AND the normal add flow succeeded on it. Idempotent
	// re-creates (dir already existed) report as StatusAdded instead.
	StatusScaffolded DirStatus = "scaffolded"
)

// AgentDirResult is the per-dir outcome inside an AddResult.
type AgentDirResult struct {
	Name        string
	Status      DirStatus
	CentralPath string             // populated for StatusAdded, StatusAlreadyTracked, StatusScaffolded
	Violations  []detect.Violation // populated for StatusSkippedSafety
}

// AddResult is the structured outcome of an Add. cmd/lore/add.go converts
// it to the output package's render type.
type AddResult struct {
	ProjectName   string
	ProjectPath   string // resolved, absolute
	CentralRepo   string // central repo root (for the Next hint)
	CommitMessage string // empty when nothing was newly added
	Dirs          []AgentDirResult
	// ScaffoldRequested is true when the user passed --create. Carried
	// even when no scaffolding actually happened (all targets already
	// existed) so JSON consumers can distinguish "user asked for
	// create" from "lore decided to create".
	ScaffoldRequested bool
}

// AllAlreadyTracked reports whether every per-dir result is
// StatusAlreadyTracked. The output layer uses this to render the terse
// idempotent-reentry message instead of the full per-dir block.
func (r AddResult) AllAlreadyTracked() bool {
	if len(r.Dirs) == 0 {
		return false
	}
	for _, d := range r.Dirs {
		if d.Status != StatusAlreadyTracked {
			return false
		}
	}
	return true
}

// Add onboards a project: detects agent dirs, refuses dirs that fail
// slice-3 safety gates, prompts the slice-4 gitignore picker per non-
// ignored dir, moves each clean dir into the central repo, symlinks it
// back, records the path-and-dir in state, and commits ONCE with
// commit.add_template covering every dir added in this run.
//
// Per-dir semantics: a safety violation on one candidate skips just that
// dir; clean dirs in the same invocation still onboard. Infra failures
// (central, state, commit) unwind every dir added in this run. Add
// returns CodeRefusedSafety if any candidate failed safety, even when
// other candidates succeeded - the cmd layer renders the per-dir result
// before propagating the exit code so the user sees what was committed
// alongside what was refused.
//
// Idempotent: re-running on a fully-tracked project (or any subset of
// already-tracked dirs) returns AddResult with every selected dir's
// status set to StatusAlreadyTracked and nil error.
//
// `opts.Dirs` scopes which agent dirs to handle; an empty slice means
// "all detected agent dirs at the project root plus any already tracked
// in central".
func Add(opts AddOptions) (AddResult, error) {
	projectPath, err := resolveProjectPath(opts.ProjectPath)
	if err != nil {
		return AddResult{}, err
	}

	cfg, _, err := config.Load()
	if err != nil {
		return AddResult{}, err
	}

	st, _, err := state.Load()
	if err != nil {
		return AddResult{}, err
	}

	c, err := central.Open(cfg.CentralRepo)
	if err != nil {
		// surface central-not-set-up as setup-missing so the user sees
		// the right exit code class.
		if lerr.CodeOf(err) == lerr.CodeNotFound {
			return AddResult{}, lerr.Newf(lerr.CodeSetupMissing,
				"central repo not found at %s", config.Contract(cfg.CentralRepo)).
				WithHint("run `lore init` first")
		}
		return AddResult{}, err
	}

	projectName := opts.Name
	nameSource := "--name"
	if projectName == "" {
		projectName = filepath.Base(projectPath)
		nameSource = "project path basename"
	}
	if err := ValidateProjectName(projectName, nameSource); err != nil {
		return AddResult{}, err
	}

	trackedEntry, isTracked := st.ProjectEntryFor(projectName)
	if isTracked && trackedEntry.Path != projectPath {
		// drift check: same project name pointing at a different path is
		// a conflict the orchestrator can't silently resolve. Catch it
		// before any per-dir work so the user sees the cleanest error.
		return AddResult{}, lerr.Newf(lerr.CodeConflict,
			"project %q is already tracked at %s; use --name <alternative> to track this project under a different name",
			projectName, config.Contract(trackedEntry.Path))
	}

	// central-orphan probe: name not in this machine's state but the
	// dir already exists in central. happens on a fresh machine pointed
	// at an existing central repo, or after the user nuked their local
	// state. without this gate, the next central.AddAgentDir call would
	// land into an already-occupied projects/<name>/ and silently merge
	// histories under a misleading project label.
	if !isTracked {
		orphan, err := centralProjectDirExists(c, projectName)
		if err != nil {
			return AddResult{}, err
		}
		if orphan {
			return AddResult{}, lerr.Newf(lerr.CodeConflict,
				"name %q already exists in central but isn't tracked on this machine; use --name <alternative>, or run `lore use %s` to adopt it",
				projectName, projectName)
		}
	}

	// success-gated unwind covering every per-dir side effect this run
	// produces. each step registers its own undo closure; on failure the
	// list is drained LIFO so unwind order is the reverse of side-effect
	// order. central, link, state are reverted; the commit at the end is
	// the success boundary. Scoped here (rather than later in the
	// function) so the --create scaffold step below can also register
	// rollbacks: a fresh scaffold should be removed if any subsequent
	// step (gitignore picker dismissal, safety refusal, central write
	// failure) aborts the run.
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

	// --create pre-step: scaffold requested agent dirs (if any are
	// missing) before DetectAgentDirs runs. Detection then finds the
	// freshly-scaffolded dirs alongside any pre-existing ones, and
	// downstream selection/safety/picker/onboard flow is unchanged.
	// Errors here (target exists as a file, picker dismissal in
	// interactive mode, permission denied) abort BEFORE any central or
	// state writes happen. Successful scaffolds register a rollback that
	// removes the freshly-created dir on any later failure: a partial
	// multi-dir scaffold rolls back earlier dirs, and a downstream
	// picker dismissal rolls back the scaffold that just landed.
	scaffoldedSet := map[string]bool{}
	if opts.Create {
		choices, err := scaffoldChoicesFromOptions(opts.Dirs, cfg.AgentDirs)
		if err != nil {
			return AddResult{}, err
		}
		for _, choice := range choices {
			created, err := scaffoldAgentDir(projectPath, choice.DirName, choice.FileName)
			if err != nil {
				return AddResult{}, err
			}
			if created {
				scaffoldedSet[choice.DirName] = true
				scaffoldedPath := filepath.Join(projectPath, choice.DirName)
				rollbacks = append(rollbacks, func() {
					// RemoveAll because the dir contains the starter file
					// we wrote; safe because we know exactly what we put
					// there and the rollback window is the same goroutine
					// with no concurrent writers.
					if rmErr := os.RemoveAll(scaffoldedPath); rmErr != nil {
						fmt.Fprintf(os.Stderr,
							"warning: cannot remove scaffolded %s during rollback: %v\n",
							scaffoldedPath, rmErr)
					}
				})
				output.Success("Scaffolded %s/ with empty %s", choice.DirName, choice.FileName)
			} else {
				output.Hint("%s already exists; skipping scaffold", choice.DirName)
			}
		}
	}

	detected, err := detect.DetectAgentDirs(projectPath, cfg.AgentDirs)
	if err != nil {
		return AddResult{}, err
	}

	// surface bare-symlink candidates that DetectAgentDirs skipped:
	// slice-3 safety cases (a stale .claude symlink the user left around
	// but lore hasn't onboarded). without this, an untracked symlinked
	// candidate would silently produce a "no agent directory found"
	// instead of the proper safety refusal. resolveSelection ignores the
	// result in --dir mode, so the Lstat work is unused there, but the
	// candidate list is small and the gate's not worth a cross-function
	// invariant.
	symlinkedNames, err := untrackedSymlinkCandidates(projectPath, cfg.AgentDirs, trackedEntry.AgentDirs)
	if err != nil {
		return AddResult{}, err
	}

	selectedNames, err := resolveSelection(opts.Dirs, detected, trackedEntry.AgentDirs, symlinkedNames)
	if err != nil {
		return AddResult{}, err
	}
	if len(selectedNames) == 0 {
		// no --dir and no detected dirs and no tracked dirs - nothing to do.
		return AddResult{}, lerr.Newf(lerr.CodeNotFound,
			"no agent directory found at %s (looked for: %s)",
			config.Contract(projectPath), strings.Join(cfg.AgentDirs, ", ")).
			WithHint("pass --create to scaffold an empty agent dir, or create one manually and rerun")
	}

	// classify each selected name into one of three buckets: already
	// tracked (validate symlink), candidate for onboarding (validate
	// it's a real dir on disk), or hard-error (missing).
	var alreadyTracked []string
	var toCheck []detect.AgentDir
	for _, name := range selectedNames {
		if slices.Contains(trackedEntry.AgentDirs, name) {
			expectedTarget := c.ProjectPath(projectName, name)
			linkPath := filepath.Join(projectPath, name)
			if validErr := link.Validate(linkPath, expectedTarget); validErr != nil {
				return AddResult{}, lerr.Newf(lerr.CodeConflict,
					"state says %q's %s is tracked but the symlink at %s does not point to %s",
					projectName, name, linkPath, expectedTarget).
					WithHint("remove the stale symlink and re-run, or run `lore doctor` once it ships")
			}
			alreadyTracked = append(alreadyTracked, name)
			continue
		}
		path := filepath.Join(projectPath, name)
		info, statErr := os.Lstat(path)
		if statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				return AddResult{}, missingDirError(name, projectPath, detected, trackedEntry.AgentDirs)
			}
			return AddResult{}, lerr.Wrap(lerr.CodeGenericError, statErr,
				fmt.Sprintf("cannot stat %s", path))
		}
		if info.Mode()&os.ModeSymlink == 0 && !info.IsDir() {
			// regular file at this name. user-explicit --dir was unambiguous,
			// so surface as a not-found rather than silently skipping.
			return AddResult{}, missingDirError(name, projectPath, detected, trackedEntry.AgentDirs)
		}
		// either a real dir (the normal case) or a symlink (slice-3
		// safety case). CheckSafetyAll handles both: symlinks produce
		// the AgentDirSymlink violation; real dirs go through the
		// tracked-file + nested-.git checks.
		toCheck = append(toCheck, detect.AgentDir{Name: name, Path: path})
	}

	// per-candidate safety. violations partition toCheck into the
	// to-onboard set (no violations) and the skipped set (one or more).
	violationsByDir, err := detect.CheckSafetyAll(projectPath, namesOf(toCheck))
	if err != nil {
		return AddResult{}, err
	}
	var toOnboard []detect.AgentDir
	var skipped []AgentDirResult
	for _, d := range toCheck {
		if vs, hit := violationsByDir[d.Name]; hit && len(vs) > 0 {
			skipped = append(skipped, AgentDirResult{
				Name:       d.Name,
				Status:     StatusSkippedSafety,
				Violations: vs,
			})
			continue
		}
		toOnboard = append(toOnboard, d)
	}

	// gitignore precheck + picker, per dir. all writes here land BEFORE
	// the success-gated unwind region: a picker dismissal or a write
	// failure aborts the whole run without any central / state / commit
	// side effects landing.
	//
	// Asymmetry vs. safety violations is deliberate: a safety violation
	// on dir B lets dir A proceed (it's a mechanical refusal), but a
	// picker dismissal on dir B aborts the whole run (it's an active
	// "no" from the user). Earlier picker writes are NOT rewound on
	// abort: the .gitignore entry the user actively chose for dir A is
	// already a correct decision they made, and retrying after fixing
	// the dismissed dir shouldn't re-prompt them for choices they've
	// already made. The .gitignore file is also user-owned; treating
	// edits to it as lore-reversible side effects overclaims ownership.
	var repoTop string
	for _, d := range toOnboard {
		ignored, err := git.CheckIgnore(projectPath, d.Name)
		if err != nil {
			return AddResult{}, err
		}
		if ignored {
			continue
		}
		// fail fast on non-git projects BEFORE the picker fires. CheckIgnore
		// folds not-a-repo into "not ignored", so without this gate a
		// non-git project would land in the picker, write to disk, and only
		// fail on re-verify - leaving stray .gitignore / global-excludes
		// pollution behind. Resolved once and reused; RepoToplevel is the
		// same answer for every dir at this project root.
		if repoTop == "" {
			repoTop, err = git.RepoToplevel(projectPath)
			if err != nil {
				return AddResult{}, err
			}
		}

		target, err := pickGitignoreTarget(d.Name)
		if err != nil {
			return AddResult{}, err
		}
		targetPath, err := resolveGitignoreTarget(target, repoTop)
		if err != nil {
			return AddResult{}, err
		}
		pattern := ignorePatternFor(target, d.Name, projectPath, repoTop)
		if err := git.AppendIgnoreEntry(targetPath, pattern); err != nil {
			return AddResult{}, err
		}
		// re-verify via check-ignore: source of truth for "is it ignored",
		// catches a wrong-target write (e.g. user has a stale
		// core.excludesFile pointing somewhere git no longer reads).
		ignored, err = git.CheckIgnore(projectPath, d.Name)
		if err != nil {
			return AddResult{}, err
		}
		if !ignored {
			return AddResult{}, lerr.Newf(lerr.CodeInconsistency,
				"wrote %s to %s but git check-ignore still reports it as not ignored",
				d.Name, config.Contract(targetPath)).
				WithHint("check git's gitignore precedence (`git check-ignore -v` against the dir) and fix the conflict")
		}
		output.Success("added %s to %s", pattern, config.Contract(targetPath))
	}

	// per-dir onboard. each successful step appends an undo closure to
	// the rollbacks slice declared earlier (above the --create step);
	// the deferred drain runs them LIFO on any failure before this
	// function returns.
	addedDirs := make([]AgentDirResult, 0, len(toOnboard))
	for _, d := range toOnboard {
		destPath, undo, err := c.AddAgentDir(projectName, d.Name, d.Path)
		if err != nil {
			return AddResult{}, err
		}
		rollbacks = append(rollbacks, func() {
			if undoErr := undo(); undoErr != nil {
				fmt.Fprintf(os.Stderr, "warning: %v\n", undoErr)
			}
		})

		if err := link.Create(destPath, d.Path); err != nil {
			return AddResult{}, err
		}
		linkPath := d.Path
		rollbacks = append(rollbacks, func() { _ = link.Remove(linkPath) })

		if err := st.AddAgentDir(projectName, projectPath, d.Name); err != nil {
			return AddResult{}, err
		}
		dirName := d.Name
		rollbacks = append(rollbacks, func() {
			st.RemoveAgentDir(projectName, dirName)
		})

		status := StatusAdded
		if scaffoldedSet[d.Name] {
			status = StatusScaffolded
		}
		addedDirs = append(addedDirs, AgentDirResult{
			Name:        d.Name,
			Status:      status,
			CentralPath: destPath,
		})
	}

	commitMessage := ""
	if len(toOnboard) > 0 {
		// state.Save and the commit are the LAST two side effects of the
		// run, so their unwind is registered here rather than per-dir.
		// the in-memory st mutations above are reverted by the per-dir
		// rollbacks; this Save persists them to disk, after which a
		// Commit failure needs both the in-memory revert AND a re-Save
		// to put the file back to its pre-run shape.
		if err := state.Save(st); err != nil {
			return AddResult{}, err
		}
		rollbacks = append(rollbacks, func() {
			if saveErr := state.Save(st); saveErr != nil {
				fmt.Fprintf(os.Stderr, "warning: cannot revert state file: %v\n", saveErr)
			}
		})

		commitMessage = renderAddTemplate(cfg.Commit.AddTemplate, projectName, namesOf(toOnboard))
		if err := c.Commit(commitMessage); err != nil {
			return AddResult{}, err
		}
	}
	success = true

	// build the per-dir result list. order: already-tracked first (stable
	// reporting of state that pre-dated this run), then this run's added
	// dirs, then skipped. each section preserves the discovery order from
	// upstream so the user sees a deterministic listing across runs.
	allDirs := make([]AgentDirResult, 0, len(alreadyTracked)+len(addedDirs)+len(skipped))
	for _, name := range alreadyTracked {
		allDirs = append(allDirs, AgentDirResult{
			Name:        name,
			Status:      StatusAlreadyTracked,
			CentralPath: c.ProjectPath(projectName, name),
		})
	}
	allDirs = append(allDirs, addedDirs...)
	allDirs = append(allDirs, skipped...)

	result := AddResult{
		ProjectName:       projectName,
		ProjectPath:       projectPath,
		CentralRepo:       c.Path,
		CommitMessage:     commitMessage,
		Dirs:              allDirs,
		ScaffoldRequested: opts.Create,
	}

	if len(skipped) > 0 {
		return result, buildSafetyError(skipped)
	}
	return result, nil
}

// Path returns the absolute on-disk path of an agent directory tracked
// for projectName inside the central repo. When dirName is empty:
// returns the single tracked dir's path, or CodeConflict if the project
// has multiple. When dirName is non-empty: validates against the tracked
// set and returns CodeNotFound if the named dir isn't tracked.
func Path(projectName, dirName string) (string, error) {
	st, _, err := state.Load()
	if err != nil {
		return "", err
	}
	entry, tracked := st.ProjectEntryFor(projectName)
	if !tracked {
		return "", lerr.Newf(lerr.CodeNotFound, "project %q is not tracked", projectName)
	}

	cfg, _, err := config.Load()
	if err != nil {
		return "", err
	}
	c, err := central.Open(cfg.CentralRepo)
	if err != nil {
		return "", err
	}

	dirs := entry.AgentDirs
	if dirName == "" {
		switch len(dirs) {
		case 0:
			return "", lerr.Newf(lerr.CodeInconsistency,
				"project %q is tracked but has no agent dirs recorded", projectName).
				WithHint("remove the stale state entry and re-run `lore add`")
		case 1:
			return c.ProjectPath(projectName, dirs[0]), nil
		default:
			return "", lerr.Newf(lerr.CodeConflict,
				"project %q has multiple agent dirs (%s); pass --dir to choose one",
				projectName, strings.Join(dirs, ", "))
		}
	}

	if !slices.Contains(dirs, dirName) {
		return "", lerr.Newf(lerr.CodeNotFound,
			"project %q has no %s agent dir; tracked: %s",
			projectName, dirName, strings.Join(dirs, ", "))
	}
	return c.ProjectPath(projectName, dirName), nil
}

// PathRepo returns the absolute path of the central repo.
func PathRepo() (string, error) {
	cfg, _, err := config.Load()
	if err != nil {
		return "", err
	}
	return cfg.CentralRepo, nil
}

// resolveSelection produces the ordered, deduplicated list of agent-dir
// names this Add call should operate on. requested is opts.Dirs verbatim
// when the user passed --dir, or nil to mean "auto-detect". In auto mode
// the selection is the union of already-tracked dirs (so a re-run
// reports them as AlreadyTracked), freshly-detected real dirs at the
// project root (so newly-added dirs get onboarded alongside), and any
// untracked symlinked candidates (so a stale .claude symlink surfaces
// as a safety violation rather than a "no agent dir found").
func resolveSelection(requested []string, detected []detect.AgentDir, tracked, symlinked []string) ([]string, error) {
	if len(requested) > 0 {
		// dedupe in requested order so the user's stated priority is
		// preserved on output without double-processing.
		seen := map[string]bool{}
		out := make([]string, 0, len(requested))
		for _, name := range requested {
			if name == "" {
				return nil, lerr.New(lerr.CodeGenericError,
					"--dir cannot be an empty string")
			}
			if seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
		return out, nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(tracked)+len(detected)+len(symlinked))
	for _, name := range tracked {
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	for _, d := range detected {
		if !seen[d.Name] {
			seen[d.Name] = true
			out = append(out, d.Name)
		}
	}
	for _, name := range symlinked {
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out, nil
}

// centralProjectDirExists reports whether central/projects/<name>/ is
// present on disk. The orchestrator uses this to surface a collision
// when --name (or the basename-derived name) would land into a central
// project the local state doesn't know about, typically a fresh machine
// pointed at an existing central repo or a state-file wipe.
//
// Returns CodeInconsistency if the path exists but isn't a directory.
// That shouldn't happen in a lore-managed central repo, but surfacing
// it clearly here beats letting central.AddAgentDir fail with a less
// obvious error a few steps later.
func centralProjectDirExists(c *central.Central, name string) (bool, error) {
	path := c.ProjectPath(name, "")
	// ProjectPath joins projects/<name>/"" → projects/<name> (trailing
	// separator stripped by Join). Stat the directory itself rather than
	// any specific agent dir inside it.
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, lerr.Wrap(lerr.CodeGenericError, err,
			fmt.Sprintf("cannot stat %s", path))
	}
	if !info.IsDir() {
		return false, lerr.Newf(lerr.CodeInconsistency,
			"%s exists but is not a directory; central layout is corrupted", path).
			WithHint("remove or rename the file at that path, or use --name <alternative>")
	}
	return true, nil
}

// untrackedSymlinkCandidates returns candidate names from cfg.AgentDirs
// that exist at root as symlinks and aren't recorded in tracked. These
// are the slice-3 safety case (an agent dir name lore would manage that
// the user has left as a stale symlink): they need to flow into the
// safety pass so the AgentDirSymlink violation surfaces, rather than
// being silently dropped by DetectAgentDirs.
func untrackedSymlinkCandidates(root string, candidates, tracked []string) ([]string, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	trackedSet := map[string]bool{}
	for _, name := range tracked {
		trackedSet[name] = true
	}
	var out []string
	for _, name := range candidates {
		if trackedSet[name] {
			continue
		}
		info, err := os.Lstat(filepath.Join(root, name))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, lerr.Wrap(lerr.CodeGenericError, err,
				fmt.Sprintf("cannot stat %s", filepath.Join(root, name)))
		}
		if info.Mode()&os.ModeSymlink != 0 {
			out = append(out, name)
		}
	}
	return out, nil
}

// missingDirError builds the error returned when the user explicitly
// asked for an agent dir that isn't present at the project root.
// Suggestions distinguish detected (fresh candidates at the project
// root the user could onboard) from tracked-only (already-onboarded
// dirs the user could --dir back into for an idempotent re-run):
// different next steps for different intents. Detected names that
// are also tracked render under "detected:" only, since they're
// usable for either intent.
func missingDirError(name, projectPath string, detected []detect.AgentDir, tracked []string) error {
	detectedNames := namesOf(detected)
	detectedSet := map[string]bool{}
	for _, n := range detectedNames {
		detectedSet[n] = true
	}
	trackedOnly := make([]string, 0, len(tracked))
	for _, n := range tracked {
		if !detectedSet[n] {
			trackedOnly = append(trackedOnly, n)
		}
	}
	var hints []string
	if len(detectedNames) > 0 {
		hints = append(hints, "detected: "+strings.Join(detectedNames, ", "))
	}
	if len(trackedOnly) > 0 {
		hints = append(hints, "tracked: "+strings.Join(trackedOnly, ", "))
	}
	if len(hints) == 0 {
		return lerr.Newf(lerr.CodeNotFound,
			"%s not found at %s and no other agent dirs were detected",
			name, config.Contract(projectPath))
	}
	return lerr.Newf(lerr.CodeNotFound,
		"%s not found at %s (%s)",
		name, config.Contract(projectPath), strings.Join(hints, "; "))
}

// namesOf extracts the Name field from a slice of AgentDir.
func namesOf(dirs []detect.AgentDir) []string {
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		out = append(out, d.Name)
	}
	return out
}

// resolveProjectPath normalises opts.ProjectPath to an absolute,
// symlink-evaluated path. Empty input means cwd.
func resolveProjectPath(in string) (string, error) {
	raw := in
	if raw == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", lerr.Wrap(lerr.CodeGenericError, err, "cannot determine current working directory")
		}
		raw = cwd
	} else {
		expanded, err := config.Expand(raw)
		if err == nil {
			raw = expanded
		}
		if !filepath.IsAbs(raw) {
			abs, err := filepath.Abs(raw)
			if err != nil {
				return "", lerr.Wrap(lerr.CodeGenericError, err,
					fmt.Sprintf("cannot resolve %s", in))
			}
			raw = abs
		}
	}
	// EvalSymlinks doubles as an existence check; we want state to
	// store the canonical path so intermediate-symlink moves don't drift.
	resolved, err := filepath.EvalSymlinks(raw)
	if err != nil {
		if os.IsNotExist(err) {
			return "", lerr.Newf(lerr.CodeNotFound,
				"project path %s does not exist", config.Contract(raw))
		}
		return "", lerr.Wrap(lerr.CodeGenericError, err,
			fmt.Sprintf("cannot resolve symlinks in %s", raw))
	}
	return resolved, nil
}

// buildSafetyError collapses one or more skipped-safety dirs into a
// single CodeRefusedSafety error. Each violation's full message becomes
// a bullet under a short header so the user sees every gate that fired
// across every refused dir in one go. Used even when some dirs in the
// same run were successfully onboarded - the cmd layer renders the
// AddResult (showing what landed) before propagating the error code,
// so the user sees both halves of the outcome.
func buildSafetyError(skipped []AgentDirResult) error {
	var b strings.Builder
	b.WriteString("refused for safety:")
	for _, d := range skipped {
		for _, v := range d.Violations {
			b.WriteString("\n  - ")
			b.WriteString(v.Message)
		}
	}
	return lerr.New(lerr.CodeRefusedSafety, b.String())
}

// pickGitignoreTarget is var (not const) so tests in this package can
// stub the interactive picker with a fixed choice without driving huh
// through a real terminal. Mirrors the prompt.isTTY test-seam pattern.
var pickGitignoreTarget = prompt.PickGitignoreTarget

// resolveGitignoreTarget maps a picker choice to the filesystem path the
// entry should be appended to. repoTop (from git.RepoToplevel) anchors
// the TargetRepoGitignore case to the actual repo root, not the project
// path: when the user runs `lore add` from a subdirectory of a monorepo,
// we still want the entry to land in the repo's root .gitignore rather
// than scattering a new .gitignore under a subdir.
func resolveGitignoreTarget(target prompt.GitignoreTarget, repoTop string) (string, error) {
	switch target {
	case prompt.TargetRepoGitignore:
		return filepath.Join(repoTop, ".gitignore"), nil
	case prompt.TargetGlobalExcludes:
		return git.GlobalExcludesPath()
	case prompt.TargetRepoExclude:
		return git.RepoExcludePath(repoTop)
	default:
		// unreachable today: only fires if GitignoreTarget gains a variant
		// without a matching case here. CodeInconsistency reflects the
		// programmer-error nature better than CodeGenericError would.
		return "", lerr.Newf(lerr.CodeInconsistency,
			"unknown gitignore target %d", target)
	}
}

// ignorePatternFor computes the literal pattern to write to the chosen
// gitignore target. Repo-scoped targets (root .gitignore and
// .git/info/exclude) get an anchored path relative to the repo root:
// "/.claude" when the project IS the repo root, "/services/foo/.claude"
// when the project is at services/foo inside a larger repo. Anchoring
// stops a bare ".claude" from also matching e.g. vendor/foo/.claude/,
// silently swallowing untracked content the user never intended to
// ignore - while still applying at the exact project path the user
// onboarded, even when that's a subdir.
//
// Global excludes get a bare pattern: anchored global rules defeat the
// purpose of a machine-wide rule, which is to apply across every repo
// on the user's machine regardless of path.
//
// No trailing slash on any variant: ".claude/" matches only directories,
// which fails once lore replaces the directory with a symlink (the
// symlink becomes visible to `git status` again).
func ignorePatternFor(target prompt.GitignoreTarget, name, projectPath, repoTop string) string {
	switch target {
	case prompt.TargetRepoGitignore, prompt.TargetRepoExclude:
		rel, err := filepath.Rel(repoTop, projectPath)
		// rel == "." when projectPath IS repoTop; treat the same as the
		// monorepo-subdir case by collapsing to a bare leading-slash entry.
		if err != nil || rel == "" || rel == "." {
			return "/" + name
		}
		return "/" + filepath.ToSlash(filepath.Join(rel, name))
	default:
		return name
	}
}

// renderAddTemplate expands {project} and {dirs} in commit.add_template.
// {dirs} is comma-joined. Other planned variables ({date}, {hostname})
// land with the slices that consume them.
func renderAddTemplate(tmpl, projectName string, dirNames []string) string {
	if tmpl == "" {
		tmpl = "feat: add {project}"
	}
	out := strings.ReplaceAll(tmpl, "{project}", projectName)
	out = strings.ReplaceAll(out, "{dirs}", strings.Join(dirNames, ", "))
	return out
}
