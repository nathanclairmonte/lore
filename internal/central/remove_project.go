package central

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/git"
)

// RemoveProjectStaged runs `git rm -r projects/<name>/` inside the
// central repo, staging the deletion of the entire project directory
// (and every agent dir under it). Returns an undo closure the
// orchestrator drives on failure:
//
//   - undo() restores both the index and the working tree via
//     `git checkout HEAD -- projects/<name>/`, mirroring git's own
//     "oops, I git rm'd" recovery primitive.
//
// Commits are NOT part of RemoveProjectStaged's contract: the
// orchestrator emits the rm commit via Central.Commit at the success
// boundary, same as AddAgentDir + Commit.
//
// Refuses with CodeNotFound if projects/<name>/ does not exist in the
// central repo working tree. lore rm catches this and surfaces it as
// "project not tracked in central" before any other side effects fire.
func (c *Central) RemoveProjectStaged(name string) (undo func() error, err error) {
	relPath := filepath.Join("projects", name)
	absPath := filepath.Join(c.Path, relPath)

	// pre-flight: refuse cleanly when the project dir is missing in
	// central, so the orchestrator can surface CodeNotFound rather than
	// letting git rm fail with a less specific exit-6 git error.
	if _, statErr := os.Stat(absPath); statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return nil, lerr.Newf(lerr.CodeNotFound,
				"projects/%s does not exist in the central repo", name)
		}
		return nil, lerr.Wrap(lerr.CodeGenericError, statErr,
			fmt.Sprintf("cannot stat %s", absPath))
	}

	if _, rmErr := git.Run(c.Path, "rm", "-r", "--quiet", relPath); rmErr != nil {
		return nil, rmErr
	}

	undo = func() error {
		// git checkout HEAD restores both the index entry and the
		// working-tree files for the path in one shot, undoing the git
		// rm above. relies on HEAD still pointing at the pre-rm commit,
		// which is true because the orchestrator's Commit hasn't fired
		// yet when an undo is needed.
		if _, restoreErr := git.Run(c.Path, "checkout", "HEAD", "--", relPath); restoreErr != nil {
			return lerr.Wrap(lerr.CodeGenericError, restoreErr,
				fmt.Sprintf("cannot restore %s from HEAD", relPath))
		}
		return nil
	}

	return undo, nil
}
