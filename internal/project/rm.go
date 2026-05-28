package project

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/nathanclairmonte/lore/internal/central"
	"github.com/nathanclairmonte/lore/internal/config"
	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/state"
)

// RmPreflight is the result of the pre-flight check Remove runs before
// any side effect. cmd/lore/rm.go uses it to render the confirmation
// prompt (the user sees exactly what will happen, including any
// pre-detected drift refusals, before saying y/N).
//
// If RefusalErr is non-nil, Remove will refuse with that error if
// invoked; the cmd layer surfaces the refusal at prompt time so the
// user doesn't type y to a request that was always going to fail.
type RmPreflight struct {
	ProjectName  string
	ProjectPath  string // empty when AlreadyUnlinked is true
	CentralRepo  string
	Dirs         []UnlinkDirResult
	CommitMsg    string
	RecoveryCmd  string // materialised `git checkout HEAD~1 -- projects/<name>/` command
	RefusalErr   error  // non-nil when a drift gate would refuse before the rm landed
	NotInCentral bool   // project missing from central too: cmd layer treats as not-found
}

// RmResult is the structured outcome of a Remove. cmd/lore/rm.go
// converts it to the output package's render type.
type RmResult struct {
	ProjectName   string
	ProjectPath   string // empty when AlreadyUnlinked is true
	CentralRepo   string
	CommitMessage string
	Dirs          []UnlinkDirResult
	RecoveryCmd   string
}

// PreflightRm runs the read-only portion of Remove: validates the
// project exists, runs the unlink drift gates, and computes the
// materialised commit + recovery commands. Returns enough context for
// the cmd layer to render the confirmation prompt.
//
// Pre-flight does not mutate state or disk. If a drift refusal fires,
// RefusalErr is populated and the cmd layer surfaces it in the prompt
// before the y/N step; Remove will refuse with the same error if
// invoked.
func PreflightRm(name string) (RmPreflight, error) {
	cfg, _, err := config.Load()
	if err != nil {
		return RmPreflight{}, err
	}

	st, _, err := state.Load()
	if err != nil {
		return RmPreflight{}, err
	}

	c, err := central.Open(cfg.CentralRepo)
	if err != nil {
		if lerr.CodeOf(err) == lerr.CodeNotFound {
			return RmPreflight{}, lerr.Newf(lerr.CodeSetupMissing,
				"central repo not found at %s", config.Contract(cfg.CentralRepo)).
				WithHint("run `lore init` first")
		}
		return RmPreflight{}, err
	}

	pre := RmPreflight{
		ProjectName: name,
		CentralRepo: c.Path,
		CommitMsg:   renderRmTemplate(cfg.Commit.RmTemplate, name),
		RecoveryCmd: rmRecoveryCommand(c.Path, name),
	}

	entry, tracked := st.ProjectEntryFor(name)
	if tracked {
		pre.ProjectPath = entry.Path
	}

	// not in state AND not in central → cmd layer surfaces a clean
	// not-found instead of letting RemoveProjectStaged fail later.
	if !tracked {
		if _, statErr := os.Stat(c.ProjectPath(name, "")); statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				pre.NotInCentral = true
				return pre, nil
			}
			return RmPreflight{}, lerr.Wrap(lerr.CodeGenericError, statErr,
				fmt.Sprintf("cannot stat %s", c.ProjectPath(name, "")))
		}
		// in central but not in state: nothing to unlink, just show the
		// central removal in the prompt.
		return pre, nil
	}

	// dry-run the unlink classification by walking the same drift gates
	// the actual unlinkProject helper does. construct a synthetic per-
	// dir verdict list for the prompt. on a refusal, capture the error
	// in RefusalErr without aborting the prompt path: the cmd layer
	// renders the refusal alongside the rest of the planned actions so
	// the user sees the full picture before saying y/N.
	dirs, refusal := previewUnlinkDirs(c, entry.Path, name, entry.AgentDirs)
	pre.Dirs = dirs
	pre.RefusalErr = refusal
	return pre, nil
}

// Remove stops tracking name entirely. Calls unlinkProject to clean up
// symlinks + scrub local state, then `git rm -r projects/<name>/` in
// central, commits using commit.rm_template, and saves state. The
// commit is the success boundary; any failure before it unwinds every
// side effect via LIFO rollbacks (symlinks recreated, central index
// restored from HEAD, state entry put back).
//
// Confirmation lives in the cmd layer: this orchestrator just runs
// the destructive sequence. cmd/lore/rm.go calls PreflightRm first,
// renders the prompt, gets a y/N, then calls Remove when y.
//
// Drift refusals (CodeRefusedSafety) from the underlying unlinkProject
// abort before any central or state mutation. The cmd layer surfaces
// them in the confirmation prompt so the user sees the refusal before
// pressing y.
func Remove(name string) (RmResult, error) {
	cfg, _, err := config.Load()
	if err != nil {
		return RmResult{}, err
	}

	st, _, err := state.Load()
	if err != nil {
		return RmResult{}, err
	}

	c, err := central.Open(cfg.CentralRepo)
	if err != nil {
		if lerr.CodeOf(err) == lerr.CodeNotFound {
			return RmResult{}, lerr.Newf(lerr.CodeSetupMissing,
				"central repo not found at %s", config.Contract(cfg.CentralRepo)).
				WithHint("run `lore init` first")
		}
		return RmResult{}, err
	}

	// snapshot pre-mutation state for the post-Save rollback. without
	// this, the rollback's state.Save would re-write the mutated st
	// (project deleted) because the LIFO drain runs the Save rollback
	// before unlinkProject's in-memory restore. State.Projects is a
	// map reference type so a shallow copy of State isn't sufficient;
	// we copy the map entries explicitly. ProjectEntry's AgentDirs
	// slice isn't mutated by the rm path (we delete entries, never
	// edit slice contents), so a per-entry shallow copy is safe.
	origState := state.State{
		SchemaVersion: st.SchemaVersion,
		Projects:      make(map[string]state.ProjectEntry, len(st.Projects)),
	}
	maps.Copy(origState.Projects, st.Projects)

	// success-gated unwind. mirrors Add and Unlink: LIFO drain of
	// rollbacks on failure, success boundary at the final Commit.
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

	// step 1: unlink symlinks + scrub state in memory. drift refusals
	// surface here as CodeRefusedSafety; the project may be tracked
	// only in central (AlreadyUnlinked=true) which is fine.
	helperRollbacks, unlinkRes, err := unlinkProject(&st, c, name)
	rollbacks = append(rollbacks, helperRollbacks...)
	if err != nil {
		return RmResult{}, err
	}

	// step 2: remove the central project dir via git rm. the dir may be
	// missing too (e.g. a stale local-state record where central was
	// already cleaned elsewhere): surface as CodeNotFound so the user
	// sees the standard not-found exit code.
	undo, err := c.RemoveProjectStaged(name)
	if err != nil {
		return RmResult{}, err
	}
	rollbacks = append(rollbacks, func() {
		if undoErr := undo(); undoErr != nil {
			fmt.Fprintf(os.Stderr, "warning: %v\n", undoErr)
		}
	})

	// step 3: persist state mutations (the project entry was deleted
	// in-memory by unlinkProject; this writes the new shape to disk).
	// the rollback writes origState (the pre-mutation snapshot), NOT
	// the current st: at rollback time st still holds the mutated map
	// because LIFO drain runs this rollback before unlinkProject's
	// in-memory restore closure fires.
	if err := state.Save(st); err != nil {
		return RmResult{}, err
	}
	rollbacks = append(rollbacks, func() {
		if saveErr := state.Save(origState); saveErr != nil {
			fmt.Fprintf(os.Stderr, "warning: cannot revert state file: %v\n", saveErr)
		}
	})

	// step 4: commit. this is the success boundary. on success, no
	// rollback fires; on failure, every step above unwinds in reverse.
	commitMessage := renderRmTemplate(cfg.Commit.RmTemplate, name)
	if err := c.Commit(commitMessage); err != nil {
		return RmResult{}, err
	}
	success = true

	return RmResult{
		ProjectName:   name,
		ProjectPath:   unlinkRes.ProjectPath,
		CentralRepo:   c.Path,
		CommitMessage: commitMessage,
		Dirs:          unlinkRes.Dirs,
		RecoveryCmd:   rmRecoveryCommand(c.Path, name),
	}, nil
}

// renderRmTemplate expands {project} in commit.rm_template. Mirrors
// renderAddTemplate; kept separate so the default fallback below
// reflects the rm-specific shape rather than the generic "feat: add".
func renderRmTemplate(tmpl, projectName string) string {
	if tmpl == "" {
		tmpl = "chore: remove {project}"
	}
	return strings.ReplaceAll(tmpl, "{project}", projectName)
}

// rmRecoveryCommand returns the materialised recovery command the user
// can run to restore a removed project. Includes the cd into the
// central repo so the snippet is copy-pasteable from the prompt or the
// success output without additional context.
func rmRecoveryCommand(centralPath, projectName string) string {
	return fmt.Sprintf("cd %s && git checkout HEAD~1 -- projects/%s/",
		config.Contract(centralPath), projectName)
}

// previewUnlinkDirs runs the same drift classification as the
// unlinkProject helper but without mutating state or removing
// anything. Used by PreflightRm to build the per-dir verdict list
// shown in the confirmation prompt. On a drift-B or drift-C refusal
// returns the error in the second slot so the cmd layer can surface
// it in the prompt before the y/N step.
//
// Shares classifyAgentDirDrift with unlinkProject so the prompt's
// verdict can't drift from what the actual run would do.
func previewUnlinkDirs(c *central.Central, projectPath, name string, dirs []string) ([]UnlinkDirResult, error) {
	out := make([]UnlinkDirResult, 0, len(dirs))
	for _, dirName := range dirs {
		linkPath := filepath.Join(projectPath, dirName)
		expected := c.ProjectPath(name, dirName)
		status, err := classifyAgentDirDrift(linkPath, expected)
		if err != nil {
			return out, err
		}
		out = append(out, UnlinkDirResult{
			Name:     dirName,
			Status:   status,
			LinkPath: linkPath,
		})
	}
	return out, nil
}

