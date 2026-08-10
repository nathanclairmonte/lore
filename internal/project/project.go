// Package project orchestrates the multi-step use cases behind lore's
// project subcommands. It composes the domain modules so command adapters stay thin.
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
	// StatusAdopted is set when identical existing copies rebuild tracking.
	StatusAdopted DirStatus = "adopted"
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
	CommitSHA     string // full central commit SHA used as the sync-base
	SyncBase      string // checkout sync-base after this operation
	Dirs          []AgentDirResult
	// ScaffoldRequested is true when the user passed --create. Carried
	// even when no scaffolding actually happened (all targets already
	// existed) so JSON consumers can distinguish "user asked for
	// create" from "lore decided to create".
	ScaffoldRequested bool
}

// openCentral opens the configured central repo and maps an absent repo to
// the setup error shared by project commands.
func openCentral(centralRepo string) (*central.Central, error) {
	c, err := central.Open(centralRepo)
	if err != nil && lerr.CodeOf(err) == lerr.CodeNotFound {
		return nil, lerr.Newf(lerr.CodeSetupMissing,
			"central repo not found at %s", config.Contract(centralRepo)).
			WithHint("run `lore init` first")
	}
	return c, err
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

func validateAgentDirNames(names []string, source string) error {
	for _, name := range names {
		if err := validateScaffoldName(name, source); err != nil {
			return err
		}
	}
	return nil
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
// a trailing slash limits the rule to the real directory lore manages.
func ignorePatternFor(target prompt.GitignoreTarget, name, projectPath, repoTop string) string {
	switch target {
	case prompt.TargetRepoGitignore, prompt.TargetRepoExclude:
		rel, err := filepath.Rel(repoTop, projectPath)
		// rel == "." when projectPath IS repoTop; treat the same as the
		// monorepo-subdir case by collapsing to a bare leading-slash entry.
		if err != nil || rel == "" || rel == "." {
			return "/" + name + "/"
		}
		return "/" + filepath.ToSlash(filepath.Join(rel, name)) + "/"
	default:
		return name + "/"
	}
}

// unignoredDirs reports which of names are not covered by an effective Git
// ignore rule inside projectPath. Pure detection: it does not prompt or write.
//
// A project that isn't a git repo yields nothing: check-ignore folds "not a
// repo" into "not ignored", so without a repo there is nothing to ignore and
// nothing worth warning about.
func unignoredDirs(projectPath string, names []string) map[string]bool {
	flagged := map[string]bool{}
	if _, err := git.RepoToplevel(projectPath); err != nil {
		return flagged
	}
	for _, name := range names {
		ignored, err := git.CheckIgnore(projectPath, name)
		if err != nil || ignored {
			continue
		}
		flagged[name] = true
	}
	return flagged
}

// applyGitignorePicker offers the interactive picker for dirName and applies
// the chosen entry, returning true only when the dir ends up ignored and
// verified. Any non-interactive context, a declined picker, a write failure,
// or a still-not-ignored re-check returns false so the caller leaves an
// advisory warning.
//
// trimTrailingSlash serves the legacy symlink model, where the managed path is
// a symlink rather than the real directory ignorePatternFor writes for.
func applyGitignorePicker(dirName, projectPath, repoTop string, trimTrailingSlash bool) bool {
	target, err := pickGitignoreTarget(dirName)
	if err != nil {
		return false
	}
	targetPath, err := resolveGitignoreTarget(target, repoTop)
	if err != nil {
		return false
	}
	pattern := ignorePatternFor(target, dirName, projectPath, repoTop)
	if trimTrailingSlash {
		pattern = strings.TrimSuffix(pattern, "/")
	}
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

// renderAddTemplate expands {project} and {dirs} in commit.add_template.
// {dirs} is comma-joined. Config defaulting supplies the template when
// the user hasn't set one; the renderer assumes a non-empty template.
func renderAddTemplate(tmpl, projectName string, dirNames []string) string {
	out := strings.ReplaceAll(tmpl, "{project}", projectName)
	out = strings.ReplaceAll(out, "{dirs}", strings.Join(dirNames, ", "))
	return out
}
