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
	"github.com/nathanclairmonte/lore/internal/link"
	"github.com/nathanclairmonte/lore/internal/state"
)

// UnlinkDirStatus names the per-dir outcome inside an UnlinkResult.
type UnlinkDirStatus string

const (
	// UnlinkStatusUnlinked is set when the symlink existed, validated
	// against the expected central target, and was removed in this run.
	UnlinkStatusUnlinked UnlinkDirStatus = "unlinked"
	// UnlinkStatusAlreadyUnlinked is set when the symlink was already
	// missing before this run (drift case A). State is still scrubbed so
	// the bookkeeping converges; the renderer surfaces a warning line so
	// the user sees the drift was observed, not silently ignored.
	UnlinkStatusAlreadyUnlinked UnlinkDirStatus = "already-unlinked"
)

// UnlinkDirResult is the per-dir outcome inside an UnlinkResult.
type UnlinkDirResult struct {
	Name     string
	Status   UnlinkDirStatus
	LinkPath string // absolute path where the symlink lived (or was supposed to)
}

// UnlinkResult is the structured outcome of an Unlink. cmd/lore/unlink.go
// converts it to the output package's render type.
type UnlinkResult struct {
	ProjectName string
	ProjectPath string // empty when AlreadyUnlinked is true
	Dirs        []UnlinkDirResult
	// AlreadyUnlinked is true when the project had no entry in local
	// state at all on entry. Distinct from "every tracked dir was
	// drift-A": this is "the project was already absent from this
	// machine's bookkeeping". Used by the renderer to print the terse
	// idempotent re-run message.
	AlreadyUnlinked bool
}

// UnlinkProjectResult is the per-project outcome inside an
// UnlinkAllResult. Mirrors UnlinkResult but carries an additional Err
// field so a refusal on one project doesn't poison the whole run.
type UnlinkProjectResult struct {
	UnlinkResult
	Err error // non-nil when this project's unlink refused or failed
}

// UnlinkAllResult is the structured outcome of an UnlinkAll. The
// per-project entries are sorted by project name for deterministic
// output across runs.
type UnlinkAllResult struct {
	Projects []UnlinkProjectResult
}

// Unlink removes lore-created symlinks for name at the recorded
// local-state path, drops the project entry from local state, and
// commits the change to disk via state.Save. Central is untouched: the
// project stays tracked in central, just no longer linked here.
//
// Idempotent: re-running on a project that has no entry in local state
// returns UnlinkResult{AlreadyUnlinked: true} with nil error so scripts
// can re-run unlinks without checking first.
//
// Refusals (CodeRefusedSafety, exit 5):
//   - drift B: a real directory or regular file sits at a tracked
//     symlink's recorded path
//   - drift C: a symlink at the recorded path points somewhere other
//     than the expected projects/<name>/<dir>/
//
// Both refusals leave both disk and state untouched so the user can
// investigate (slice 19's `lore doctor` is the eventual home).
//
// Partial-failure rollback: on a multi-agent-dir project, removing one
// symlink can succeed and a later step (e.g. a transient I/O error)
// fail. The unwind region recreates every symlink removed earlier in
// the same call before returning, so disk + state both revert to their
// pre-Unlink shape.
func Unlink(name string) (UnlinkResult, error) {
	cfg, _, err := config.Load()
	if err != nil {
		return UnlinkResult{}, err
	}

	st, _, err := state.Load()
	if err != nil {
		return UnlinkResult{}, err
	}

	c, err := central.Open(cfg.CentralRepo)
	if err != nil {
		if lerr.CodeOf(err) == lerr.CodeNotFound {
			return UnlinkResult{}, lerr.Newf(lerr.CodeSetupMissing,
				"central repo not found at %s", config.Contract(cfg.CentralRepo)).
				WithHint("run `lore init` first")
		}
		return UnlinkResult{}, err
	}

	// success-gated unwind mirrors the slice 6 Add pattern: rollback
	// closures appended LIFO, drained on any failure before state.Save
	// lands. state.Save is the success boundary for Unlink (no central
	// commit happens here).
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

	helperRollbacks, result, err := unlinkProject(&st, c, name)
	rollbacks = append(rollbacks, helperRollbacks...)
	if err != nil {
		return UnlinkResult{}, err
	}

	if result.AlreadyUnlinked {
		// no state mutation happened; nothing to save. success boundary
		// is here.
		success = true
		return result, nil
	}

	if err := state.Save(st); err != nil {
		return UnlinkResult{}, err
	}
	success = true
	return result, nil
}

// UnlinkAll iterates every tracked project on this machine, applying
// Unlink semantics per project. Continues past per-project refusals so
// the user sees the full drift picture in one run (the "tear down a
// machine" use case). Returns CodeRefusedSafety when any project
// refused, even when other projects unlinked cleanly, so scripts see
// the worst-case exit code.
//
// Per-project atomicity is preserved: each project's symlinks + state
// mutation either all succeed or all roll back inside its own Unlink
// call. A later project failing does NOT roll back an earlier project's
// successful unlink.
func UnlinkAll() (UnlinkAllResult, error) {
	st, _, err := state.Load()
	if err != nil {
		return UnlinkAllResult{}, err
	}

	// snapshot the tracked names BEFORE the loop so mutation of
	// st.Projects (each Unlink call deletes its own entry) doesn't
	// affect iteration order. sorted for deterministic output.
	names := make([]string, 0, len(st.Projects))
	for name := range st.Projects {
		names = append(names, name)
	}
	sort.Strings(names)

	if len(names) == 0 {
		return UnlinkAllResult{}, nil
	}

	result := UnlinkAllResult{
		Projects: make([]UnlinkProjectResult, 0, len(names)),
	}
	var worst error
	for _, name := range names {
		// each iteration calls the full Unlink (which re-loads state,
		// re-opens central, runs its own unwind region). repeated I/O
		// is fine here: --all is a recovery command, not a hot path.
		res, err := Unlink(name)
		result.Projects = append(result.Projects, UnlinkProjectResult{
			UnlinkResult: res,
			Err:          err,
		})
		if err != nil && worst == nil {
			// hold onto the first per-project error so the cmd layer
			// can propagate its exit code. additional refusals are
			// surfaced via the per-project verdicts in the rendered
			// output; only one exit code can win.
			worst = err
		}
	}
	return result, worst
}

// unlinkProject is the shared internal helper for Unlink and Remove.
// Pre-flight-classifies each tracked dir into a per-dir verdict,
// refuses with CodeRefusedSafety if any dir hits drift case B/C, then
// removes each symlink in order, mutates st in memory to drop the
// project entry, and returns LIFO rollback closures.
//
// Does NOT call state.Save. Callers own the Save (and the central
// commit, for Remove) at their own success boundary.
//
// When st has no entry for name, returns AlreadyUnlinked=true with no
// rollbacks and no error. Both Unlink (idempotent re-run) and Remove
// (project may exist in central without local-state record after an
// earlier unlink) rely on this.
func unlinkProject(st *state.State, c *central.Central, name string) (
	rollbacks []func(),
	result UnlinkResult,
	err error,
) {
	entry, tracked := st.ProjectEntryFor(name)
	if !tracked {
		return nil, UnlinkResult{
			ProjectName:     name,
			AlreadyUnlinked: true,
		}, nil
	}

	result.ProjectName = name
	result.ProjectPath = entry.Path

	// pre-flight: classify each tracked dir's drift state via the
	// shared classifyAgentDirDrift helper. drift A (silently missing)
	// proceeds with the already-unlinked verdict; B (real dir or file)
	// and C (symlink pointing somewhere unexpected) refuse the whole
	// run before any filesystem side effect.
	type dirPlan struct {
		name           string
		linkPath       string
		expectedTarget string
		status         UnlinkDirStatus
	}
	plans := make([]dirPlan, 0, len(entry.AgentDirs))
	for _, dirName := range entry.AgentDirs {
		linkPath := filepath.Join(entry.Path, dirName)
		expected := c.ProjectPath(name, dirName)
		status, classifyErr := classifyAgentDirDrift(linkPath, expected)
		if classifyErr != nil {
			return nil, UnlinkResult{}, classifyErr
		}
		plans = append(plans, dirPlan{
			name:           dirName,
			linkPath:       linkPath,
			expectedTarget: expected,
			status:         status,
		})
	}

	// execute: remove each symlink (skipping drift-A dirs that already
	// have nothing on disk), append a recreate rollback after each
	// success. drift-A dirs register no rollback because there's
	// nothing to put back.
	for _, p := range plans {
		if p.status == UnlinkStatusUnlinked {
			if rmErr := link.Remove(p.linkPath); rmErr != nil {
				return rollbacks, UnlinkResult{}, rmErr
			}
			linkPath, expectedTarget := p.linkPath, p.expectedTarget
			rollbacks = append(rollbacks, func() {
				if recreateErr := link.Create(expectedTarget, linkPath); recreateErr != nil {
					fmt.Fprintf(os.Stderr,
						"warning: cannot recreate symlink at %s during rollback: %v\n",
						linkPath, recreateErr)
				}
			})
		}
		result.Dirs = append(result.Dirs, UnlinkDirResult{
			Name:     p.name,
			Status:   p.status,
			LinkPath: p.linkPath,
		})
	}

	// state mutation: drop the entire project entry from local state.
	// unlink's contract is "the project stays in central but is no
	// longer linked on this machine", so the local-state record goes
	// away entirely. capture the original entry for rollback before
	// mutating.
	origEntry := entry
	delete(st.Projects, name)
	rollbacks = append(rollbacks, func() {
		if st.Projects == nil {
			st.Projects = map[string]state.ProjectEntry{}
		}
		st.Projects[name] = origEntry
	})

	return rollbacks, result, nil
}

// classifyAgentDirDrift inspects linkPath against expectedTarget (the
// central agent-dir path lore expects the symlink to point at) and
// returns either a benign verdict (Unlinked when the symlink is valid,
// AlreadyUnlinked when nothing's there) or a CodeRefusedSafety error
// for drift B (real dir or file in the way) and drift C (symlink
// pointing somewhere unexpected). Pure read; no mutation. Shared
// between unlinkProject (which executes the unlink) and previewUnlinkDirs
// (which renders the rm confirmation prompt) so the two paths can't
// drift apart.
func classifyAgentDirDrift(linkPath, expectedTarget string) (UnlinkDirStatus, error) {
	info, lstatErr := os.Lstat(linkPath)
	if lstatErr != nil {
		if errors.Is(lstatErr, os.ErrNotExist) {
			return UnlinkStatusAlreadyUnlinked, nil
		}
		return "", lerr.Wrap(lerr.CodeGenericError, lstatErr,
			fmt.Sprintf("cannot stat %s", linkPath))
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return "", lerr.Newf(lerr.CodeRefusedSafety,
			"%s exists but is not a symlink lore created (refusing to touch a real directory or file)",
			linkPath).
			WithHint("inspect and remove manually, or run `lore doctor` once it ships")
	}
	// link.Validate does the exact-string compare against expectedTarget;
	// its CodeGenericError gets rewrapped to CodeRefusedSafety here so
	// the user sees the drift-C exit code.
	if validErr := link.Validate(linkPath, expectedTarget); validErr != nil {
		return "", lerr.Newf(lerr.CodeRefusedSafety,
			"%s is a symlink but does not point to %s",
			linkPath, expectedTarget).
			WithHint("inspect and remove manually, or run `lore doctor` once it ships")
	}
	return UnlinkStatusUnlinked, nil
}
