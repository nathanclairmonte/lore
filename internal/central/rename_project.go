package central

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/git"
)

// RenameProjectStaged runs `git mv projects/<old> projects/<new>` inside the
// central repo, staging the rename of the entire project directory (and
// every agent dir under it). Returns an undo closure the orchestrator
// drives on failure:
//
//   - undo() reverses the staged rename with `git mv projects/<new>
//     projects/<old>`, restoring both the index and the working tree.
//     Relies on the orchestrator's Commit not having fired yet, so the
//     reverse move re-stages cleanly against the same HEAD.
//
// Commits are NOT part of RenameProjectStaged's contract: the orchestrator
// emits the mv commit via Central.Commit at the success boundary, same
// as RemoveProjectStaged + Commit.
//
// Refuses with CodeNotFound if projects/<old>/ does not exist, with
// CodeConflict if projects/<new>/ already exists, and with
// CodeRefusedSafety if projects/<old>/ has uncommitted changes (the
// reverse-git-mv undo can't cleanly account for staged edits or
// untracked files, so we hand control back to commit / stash / discard
// first).
func (c *Central) RenameProjectStaged(old, newName string) (undo func() error, err error) {
	oldRel := filepath.Join("projects", old)
	newRel := filepath.Join("projects", newName)
	oldAbs := filepath.Join(c.Path, oldRel)
	newAbs := filepath.Join(c.Path, newRel)

	// surface a clean CodeNotFound when the source is missing rather than
	// letting git mv fail with a less specific error.
	if _, statErr := os.Stat(oldAbs); statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return nil, lerr.Newf(lerr.CodeNotFound,
				"projects/%s does not exist in the central repo", old)
		}
		return nil, lerr.Wrap(lerr.CodeGenericError, statErr,
			fmt.Sprintf("cannot stat %s", oldAbs))
	}

	// refuse to rename onto an existing project dir. git mv would either
	// fail or merge the trees depending on the case; a clean CodeConflict
	// is the honest answer.
	if _, statErr := os.Stat(newAbs); statErr == nil {
		return nil, lerr.Newf(lerr.CodeConflict,
			"projects/%s already exists in the central repo", newName)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, lerr.Wrap(lerr.CodeGenericError, statErr,
			fmt.Sprintf("cannot stat %s", newAbs))
	}

	// refuse if anything under projects/<old>/ has uncommitted changes.
	// the reverse-git-mv undo can't faithfully restore staged edits or
	// untracked files, so we'd rather have the user commit / stash /
	// discard before re-running. mirrors RemoveProjectStaged's gate.
	status, statusErr := git.Run(c.Path, "status", "--porcelain", "--", oldRel)
	if statusErr != nil {
		return nil, statusErr
	}
	if strings.TrimSpace(status) != "" {
		return nil, lerr.Newf(lerr.CodeRefusedSafety,
			"uncommitted changes under projects/%s/ in %s", old, c.Path).
			WithHint("commit, stash, or discard the changes in central, then re-run `lore mv`")
	}

	if _, mvErr := git.Run(c.Path, "mv", oldRel, newRel); mvErr != nil {
		return nil, mvErr
	}

	undo = func() error {
		// reverse the staged rename. git mv re-stages the move back to the
		// old path in one shot, undoing the forward mv above. relies on
		// HEAD still pointing at the pre-mv commit, which is true because
		// the orchestrator's Commit hasn't fired yet when an undo is
		// needed.
		if _, restoreErr := git.Run(c.Path, "mv", newRel, oldRel); restoreErr != nil {
			return lerr.Wrap(lerr.CodeGenericError, restoreErr,
				fmt.Sprintf("cannot reverse rename of %s back to %s", newRel, oldRel))
		}
		return nil
	}

	return undo, nil
}
