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
	"github.com/nathanclairmonte/lore/internal/link"
	"github.com/nathanclairmonte/lore/internal/state"
)

// MoveDirStatus names the per-dir outcome inside a MoveResult.
type MoveDirStatus string

const (
	// MoveStatusRetargeted: default mode, the symlink was healthy and was
	// repointed in place from projects/<old>/<dir> to projects/<new>/<dir>.
	MoveStatusRetargeted MoveDirStatus = "retargeted"
	// MoveStatusMovedToPath: --path mode, the healthy symlink at the old
	// path was removed and a fresh one created under the new path.
	MoveStatusMovedToPath MoveDirStatus = "moved"
	// MoveStatusRecreated: default mode, drift-A recovery. The old symlink
	// was already missing, so mv just creates the new one (the rename's
	// end state is "symlink exists pointing at <new>" regardless).
	MoveStatusRecreated MoveDirStatus = "recreated"
	// MoveStatusSkippedOld: --path mode, the old symlink was already
	// missing (drift-A). Nothing to remove at the old path; the new
	// symlink is still created under the new path.
	MoveStatusSkippedOld MoveDirStatus = "skipped-old"
	// MoveStatusLeftInPlace: --path mode, the old path held something that
	// isn't a clean lore symlink (drift B: real dir/file; drift C: symlink
	// to an unexpected target). We leave it untouched (we're abandoning
	// that path anyway) and still create the new symlink. Note carries the
	// human reason.
	MoveStatusLeftInPlace MoveDirStatus = "left-in-place"
)

// MoveDirResult is the per-dir outcome inside a MoveResult. Note is
// populated only for MoveStatusLeftInPlace / MoveStatusSkippedOld.
type MoveDirResult struct {
	Name    string
	Status  MoveDirStatus
	OldLink string // absolute path the symlink lived at (or was supposed to)
	NewLink string // absolute path the symlink now lives at (== OldLink in default mode)
	Note    string
}

// MoveResult is the structured outcome of a Move. cmd/lore/mv.go
// converts it to the output package's render type.
type MoveResult struct {
	OldName       string
	ProjectName   string // the new name
	OldPath       string // recorded local path before the move (empty when not linked here)
	NewPath       string // recorded local path after the move (== OldPath in default mode)
	CentralRepo   string
	CommitMessage string
	Dirs          []MoveDirResult
	// NotLinkedHere is true when <old> exists in central but has no
	// local-state entry on this machine: the central rename + commit still
	// happen, but there are no symlinks to retarget and no state to rename.
	NotLinkedHere bool
}

// Move renames a tracked project from old to new in the central repo and
// retargets its on-disk symlinks. Existence is central-authoritative:
// <old> must exist as projects/<old>/ in central (else CodeNotFound), and
// <new> must not already exist there (else CodeConflict). The central
// `git mv` + commit always happen when <old> exists.
//
// The symlink + local-state work is conditional on a local-state entry
// for <old>. When the project isn't linked on this machine, Move does the
// central rename + commit only and reports NotLinkedHere.
//
// Two modes:
//
//   - default (newPath == ""): retarget every symlink at the recorded
//     path in place (projects/<old>/<dir> → projects/<new>/<dir>), keep
//     the recorded path. Strict, all-or-nothing: a real directory (drift
//     B) or wrong-target symlink (drift C) at any tracked path refuses the
//     whole op with CodeRefusedSafety before any side effect. A missing
//     symlink (drift A) is non-fatal and is recreated.
//   - --path (newPath != ""): remove the symlink(s) at the old recorded
//     path and create them under newPath, then update the recorded path.
//     Lenient at the old path: only a healthy lore symlink is removed;
//     anything else (missing, real dir, foreign symlink) is left untouched
//     with a note. The new path is strict: a pre-existing entry in the way
//     refuses with CodeRefusedSafety.
//
// lore never renames the user's on-disk project directory in either mode.
//
// The central commit is the success boundary; any failure before it
// unwinds every side effect via LIFO rollbacks (symlinks restored to their
// old targets, state reverted, the git mv reversed).
func Move(old, newName, newPath string) (MoveResult, error) {
	cfg, _, err := config.Load()
	if err != nil {
		return MoveResult{}, err
	}

	st, _, err := state.Load()
	if err != nil {
		return MoveResult{}, err
	}

	c, err := central.Open(cfg.CentralRepo)
	if err != nil {
		if lerr.CodeOf(err) == lerr.CodeNotFound {
			return MoveResult{}, lerr.Newf(lerr.CodeSetupMissing,
				"central repo not found at %s", config.Contract(cfg.CentralRepo)).
				WithHint("run `lore init` first")
		}
		return MoveResult{}, err
	}

	// --- Phase 1: validation, no side effects -------------------------

	// validate both names before the same-name and existence checks, so a
	// malformed name reports as such rather than being masked. old is
	// validated too: a tracked project always has a valid name, so this
	// can only reject input that could never have matched central anyway.
	if err := ValidateProjectName(old, "<old>"); err != nil {
		return MoveResult{}, err
	}
	if err := ValidateProjectName(newName, "<new>"); err != nil {
		return MoveResult{}, err
	}
	if old == newName {
		return MoveResult{}, lerr.Newf(lerr.CodeConflict,
			"project is already named %q", old)
	}

	// central-authoritative existence + conflict. wording here mirrors
	// RenameProjectStaged's own gate so the user sees consistent copy
	// regardless of which layer's check fires first.
	oldExists, err := centralProjectDirExists(c, old)
	if err != nil {
		return MoveResult{}, err
	}
	if !oldExists {
		return MoveResult{}, lerr.Newf(lerr.CodeNotFound,
			"projects/%s does not exist in the central repo", old)
	}
	newExists, err := centralProjectDirExists(c, newName)
	if err != nil {
		return MoveResult{}, err
	}
	if newExists {
		return MoveResult{}, lerr.Newf(lerr.CodeConflict,
			"projects/%s already exists in the central repo", newName).
			WithHint("choose a different name")
	}

	commitMessage := renderMvTemplate(cfg.Commit.MvTemplate, old, newName)

	// snapshot pre-mutation state so the post-Save rollback can restore it.
	origState := st.Clone()

	entry, linkedHere := st.ProjectEntryFor(old)
	entryPath := primaryCheckoutPath(entry)

	// plans is the per-dir work list (empty when not linked here).
	type movePlan struct {
		name        string
		status      MoveDirStatus
		oldLink     string
		newLink     string
		oldExpected string
		newExpected string
		note        string
	}
	plans := make([]movePlan, 0, len(entry.AgentDirs))
	dstPath := ""

	if linkedHere {
		dstPath = entryPath
		if newPath != "" {
			expanded, expErr := config.Expand(newPath)
			if expErr != nil {
				return MoveResult{}, expErr
			}
			abs, absErr := filepath.Abs(expanded)
			if absErr != nil {
				return MoveResult{}, lerr.Wrap(lerr.CodeGenericError, absErr,
					fmt.Sprintf("cannot resolve new path %s", newPath))
			}
			// the new path must already exist as a directory: lore plants a
			// symlink inside it, it never creates the user's project tree.
			info, statErr := os.Stat(abs)
			if statErr != nil {
				if errors.Is(statErr, os.ErrNotExist) {
					return MoveResult{}, lerr.Newf(lerr.CodeNotFound,
						"new path %s does not exist", config.Contract(abs)).
						WithHint("create the directory first, or point --path at an existing location")
				}
				return MoveResult{}, lerr.Wrap(lerr.CodeGenericError, statErr,
					fmt.Sprintf("cannot stat %s", abs))
			}
			if !info.IsDir() {
				return MoveResult{}, lerr.Newf(lerr.CodeRefusedSafety,
					"new path %s exists but is not a directory", config.Contract(abs))
			}
			dstPath = abs
		}

		// classify every tracked dir up front. the drift verdict drives a
		// different per-dir plan in each mode.
		var refusals []error
		for _, dirName := range entry.AgentDirs {
			oldLink := filepath.Join(entryPath, dirName)
			newLink := filepath.Join(dstPath, dirName)
			oldExpected := c.ProjectPath(old, dirName)
			newExpected := c.ProjectPath(newName, dirName)
			status, classifyErr := classifyAgentDirDrift(oldLink, oldExpected)

			p := movePlan{
				name:        dirName,
				oldLink:     oldLink,
				newLink:     newLink,
				oldExpected: oldExpected,
				newExpected: newExpected,
			}
			if newPath == "" {
				// default mode: strict. drift B/C refuses; drift A recovers.
				if classifyErr != nil {
					// the refusal gate below aborts before any side effect, so
					// this plan's status is never read.
					refusals = append(refusals, classifyErr)
				} else if status == UnlinkStatusAlreadyUnlinked {
					p.status = MoveStatusRecreated
				} else {
					p.status = MoveStatusRetargeted
				}
			} else {
				// --path mode: lenient at the old path.
				if classifyErr != nil {
					p.status = MoveStatusLeftInPlace
					p.note = fmt.Sprintf("left in place (not a lore symlink): %s",
						config.Contract(oldLink))
				} else if status == UnlinkStatusAlreadyUnlinked {
					p.status = MoveStatusSkippedOld
					p.note = fmt.Sprintf("old symlink already absent: %s",
						config.Contract(oldLink))
				} else {
					p.status = MoveStatusMovedToPath
				}
			}
			plans = append(plans, p)
		}

		// default-mode refusal gate: abort before any side effect. return
		// an empty result so the cmd layer prints no success headline; the
		// joined refusals carry every problematic dir's reason, which
		// PrintError surfaces in one pass.
		if len(refusals) > 0 {
			return MoveResult{}, errors.Join(refusals...)
		}

		// new-path strictness (--path only): refuse (exit 5) if something is
		// already in the way at a location we'd create a symlink at and aren't
		// clearing ourselves first. link.Create alone would surface this as
		// exit 1, so we pre-check to honor the documented exit 5. A dir whose
		// old symlink we remove first (MovedToPath) is fine when newLink ==
		// oldLink; everything else must find newLink empty. In default mode
		// newLink == oldLink for every dir, so there's nothing to pre-check.
		if newPath != "" {
			for _, p := range plans {
				if p.status == MoveStatusMovedToPath && p.newLink == p.oldLink {
					continue
				}
				if _, statErr := os.Lstat(p.newLink); statErr == nil {
					return MoveResult{}, lerr.Newf(lerr.CodeRefusedSafety,
						"%s already exists; refusing to clobber it", config.Contract(p.newLink)).
						WithHint("remove it or run `lore unlink` there first")
				} else if !errors.Is(statErr, os.ErrNotExist) {
					return MoveResult{}, lerr.Wrap(lerr.CodeGenericError, statErr,
						fmt.Sprintf("cannot stat %s", p.newLink))
				}
			}
		}
	}

	// --- success-gated unwind region ----------------------------------

	success := false
	var rollbacks []func() error
	defer func() {
		if success {
			return
		}
		// drain LIFO. each closure logs its own specific failure; we count
		// them so a partial rollback ends with one prominent line telling
		// the user the rename is half-applied and what to inspect, rather
		// than leaving them to piece it together from scattered warnings.
		failed := 0
		for i := len(rollbacks) - 1; i >= 0; i-- {
			if rbErr := rollbacks[i](); rbErr != nil {
				failed++
			}
		}
		if failed > 0 {
			where := "central at " + config.Contract(c.Path)
			if linkedHere {
				where += " and the symlinks under " + config.Contract(entryPath)
			}
			fmt.Fprintf(os.Stderr,
				"warning: %d rollback step(s) failed; the rename may be half-applied. "+
					"inspect %s before re-running `lore mv`.\n", failed, where)
		}
	}()

	// --- Phase 2: central rename (first side effect) ------------------
	// the dirty-central gate lives inside RenameProjectStaged and fires here,
	// still before the commit.
	undo, err := c.RenameProjectStaged(old, newName)
	if err != nil {
		return MoveResult{}, err
	}
	rollbacks = append(rollbacks, func() error {
		if undoErr := undo(); undoErr != nil {
			fmt.Fprintf(os.Stderr, "warning: %v\n", undoErr)
			return undoErr
		}
		return nil
	})

	result := MoveResult{
		OldName:       old,
		ProjectName:   newName,
		CentralRepo:   c.Path,
		CommitMessage: commitMessage,
	}

	if !linkedHere {
		result.NotLinkedHere = true
	} else {
		result.OldPath = entryPath
		result.NewPath = dstPath

		// --- Phase 3: symlink work --------------------------------------
		// retarget = remove-old + create-new (no atomic repoint primitive).
		// register a rollback after each step; LIFO unwind restores the
		// originals (pointing at projects/<old>/<dir>, valid again once the
		// central rename is reversed).
		for _, p := range plans {
			// Retargeted/MovedToPath had a healthy old symlink to clear first;
			// the recovery/skipped/left-in-place statuses have nothing to
			// remove. Every plan then creates the new symlink.
			removeOld := p.status == MoveStatusRetargeted || p.status == MoveStatusMovedToPath
			if removeOld {
				if rmErr := link.Remove(p.oldLink); rmErr != nil {
					return MoveResult{}, rmErr
				}
				oldLink, oldExpected := p.oldLink, p.oldExpected
				rollbacks = append(rollbacks, func() error {
					if e := link.Create(oldExpected, oldLink); e != nil {
						fmt.Fprintf(os.Stderr,
							"warning: cannot recreate symlink at %s during rollback: %v\n",
							oldLink, e)
						return e
					}
					return nil
				})
			}
			if crErr := link.Create(p.newExpected, p.newLink); crErr != nil {
				return MoveResult{}, crErr
			}
			newLink := p.newLink
			rollbacks = append(rollbacks, func() error {
				if e := link.Remove(newLink); e != nil {
					fmt.Fprintf(os.Stderr,
						"warning: cannot remove symlink at %s during rollback: %v\n",
						newLink, e)
					return e
				}
				return nil
			})
			result.Dirs = append(result.Dirs, MoveDirResult{
				Name: p.name, Status: p.status, OldLink: p.oldLink, NewLink: p.newLink, Note: p.note,
			})
		}

		// --- Phase 3.5: state mutation (in-memory) ----------------------
		// rename the key; carry the same entry, updating the path only in
		// --path mode. AgentDirs are unchanged (the agent-dir names don't
		// change, only the central project folder did).
		movedEntry := entry
		if newPath != "" {
			for i := range movedEntry.Checkouts {
				if movedEntry.Checkouts[i].Path == entryPath {
					movedEntry.Checkouts[i].Path = dstPath
				}
			}
		}
		delete(st.Projects, old)
		st.Projects[newName] = movedEntry
		rollbacks = append(rollbacks, func() error {
			delete(st.Projects, newName)
			if st.Projects == nil {
				st.Projects = map[string]state.ProjectEntry{}
			}
			st.Projects[old] = entry
			return nil
		})

		// --- Phase 3.6: persist state BEFORE the commit -----------------
		// the rollback writes origState (the pre-mutation snapshot): at
		// rollback time st still holds the mutated map because LIFO drains
		// this before the in-memory restore closure above.
		if err := state.Save(st); err != nil {
			return MoveResult{}, err
		}
		rollbacks = append(rollbacks, func() error {
			if saveErr := state.Save(origState); saveErr != nil {
				fmt.Fprintf(os.Stderr, "warning: cannot revert state file: %v\n", saveErr)
				return saveErr
			}
			return nil
		})
	}

	// --- Phase 4: commit (success boundary) ---------------------------
	// scope the commit to the two project paths so a rename can never fold
	// unrelated staged content elsewhere in central into the rename commit.
	// both sides need naming: the delete at projects/<old> and the add at
	// projects/<new>.
	if err := c.CommitPaths(commitMessage,
		filepath.Join("projects", old), filepath.Join("projects", newName)); err != nil {
		return MoveResult{}, err
	}
	success = true

	return result, nil
}

// defaultMvTemplate is the fallback commit message when commit.mv_template
// is missing or explicitly blanked. Kept in sync with default_config.toml.
const defaultMvTemplate = "refactor: rename {old} to {new}"

// renderMvTemplate expands {old} and {new} in commit.mv_template. Config
// defaulting fills a missing key with the embedded default, but a user can
// still set mv_template to an empty string. Rather than hand git an empty
// commit message (which would fail only after every side effect is staged,
// forcing a needless rollback), fall back to the built-in default when the
// template is blank.
func renderMvTemplate(tmpl, old, newName string) string {
	if strings.TrimSpace(tmpl) == "" {
		tmpl = defaultMvTemplate
	}
	out := strings.ReplaceAll(tmpl, "{old}", old)
	out = strings.ReplaceAll(out, "{new}", newName)
	return out
}
