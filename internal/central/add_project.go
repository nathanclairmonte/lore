package central

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/git"
)

// AddAgentDir moves the directory at sourcePath into
// projects/<name>/<dirName>/ inside the central repo and stages it.
// Returns the absolute destination path along with an undo closure the
// orchestrator drives on failure:
//
//   - undo() reverses the rename, drops the staged path from the index,
//     and removes projects/<name>/ if (and only if) AddAgentDir created
//     it and no other agent dirs were added to it.
//
// Commits are NOT part of AddAgentDir's contract: a single `lore add`
// may onboard multiple agent dirs and the orchestrator emits one commit
// covering them all via Central.Commit once every per-dir side effect
// has succeeded.
//
// Refuses with CodeConflict if projects/<name>/<dirName>/ already exists.
// A project directory that already contains *other* agent dirs is fine
// (the slice-5 multi-dir case: `.claude` is tracked, user runs `lore add
// --dir .cursor` to extend the same project).
func (c *Central) AddAgentDir(name, dirName, sourcePath string) (
	destPath string,
	undo func() error,
	err error,
) {
	destProjectDir := filepath.Join(c.Path, "projects", name)
	destAgentDir := filepath.Join(destProjectDir, dirName)

	if _, statErr := os.Stat(destAgentDir); statErr == nil {
		return "", nil, lerr.Newf(lerr.CodeConflict,
			"projects/%s/%s already exists in the central repo", name, dirName)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", nil, lerr.Wrap(lerr.CodeGenericError, statErr,
			fmt.Sprintf("cannot stat %s", destAgentDir))
	}

	// track whether AddAgentDir created projects/<name>/ so undo only
	// removes it when we did. extending an existing tracked project (the
	// second agent dir flow) must leave the original dir alone.
	createdProjectDir := false
	if _, statErr := os.Stat(destProjectDir); errors.Is(statErr, os.ErrNotExist) {
		if mkErr := os.MkdirAll(destProjectDir, 0o755); mkErr != nil {
			return "", nil, lerr.Wrap(lerr.CodeGenericError, mkErr,
				fmt.Sprintf("cannot create %s", destProjectDir))
		}
		createdProjectDir = true
	} else if statErr != nil {
		return "", nil, lerr.Wrap(lerr.CodeGenericError, statErr,
			fmt.Sprintf("cannot stat %s", destProjectDir))
	}

	if renameErr := os.Rename(sourcePath, destAgentDir); renameErr != nil {
		// MkdirAll above may have created destProjectDir; clean it up
		// (when we made it) so the caller can retry without inheriting
		// the half-built state.
		if createdProjectDir {
			_ = os.Remove(destProjectDir)
		}
		return "", nil, lerr.Wrap(lerr.CodeGenericError, renameErr,
			fmt.Sprintf("cannot move %s into central repo", sourcePath))
	}

	// -f forces past any gitignore rule that would otherwise exclude
	// this path. the central repo's job is to track these agent dirs
	// explicitly, regardless of what the user's global excludes file
	// (slice 4's "global gitignore" picker option) or any inherited rule
	// might say. without -f, picking global excludes would block the
	// add: the same rule that ignores .claude/ in the user's projects
	// also fires inside the central repo.
	relStaged := filepath.Join("projects", name, dirName)
	if _, addErr := git.Run(c.Path, "add", "-f", relStaged); addErr != nil {
		// undo the rename so callers see a clean slate; bare RemoveAll
		// of the project dir would destroy user content if rename had
		// landed under projects/<name>/.
		if revertErr := os.Rename(destAgentDir, sourcePath); revertErr != nil {
			fmt.Fprintf(os.Stderr,
				"warning: failed to revert rename %s -> %s: %v\n",
				destAgentDir, sourcePath, revertErr)
		}
		if createdProjectDir {
			_ = os.Remove(destProjectDir)
		}
		return "", nil, addErr
	}

	undo = func() error {
		// drop the staged path from the index. on failure surface as a
		// non-fatal warning: filesystem revert is the load-bearing part.
		if _, resetErr := git.Run(c.Path, "reset", "HEAD", "--", relStaged); resetErr != nil {
			fmt.Fprintf(os.Stderr,
				"warning: failed to reset index for %s: %v\n",
				relStaged, resetErr)
		}
		if renameBackErr := os.Rename(destAgentDir, sourcePath); renameBackErr != nil {
			return lerr.Wrap(lerr.CodeGenericError, renameBackErr,
				fmt.Sprintf("cannot move %s back to %s", destAgentDir, sourcePath))
		}
		// only remove projects/<name>/ if we created it for this add AND
		// it's now empty. extending a tracked project leaves the dir
		// alone; the slice 19 doctor will surface any orphan from a
		// partial concurrent run.
		if createdProjectDir {
			_ = os.Remove(destProjectDir)
		}
		return nil
	}

	return destAgentDir, undo, nil
}

// Commit creates a commit on the central repo with the given message,
// using the user's real git identity (no inline -c flags) per ADR 0003.
// Stages must already be set up by prior AddAgentDir / RemoveAgentDir
// calls.
func (c *Central) Commit(message string) error {
	_, err := git.Run(c.Path, "commit", "-m", message)
	return err
}

// CommitPaths commits only the staged changes under the given repo-relative
// pathspecs, leaving any unrelated staged content in central untouched. mv
// uses this so a rename can't fold a user's pre-staged work elsewhere in
// central into the rename commit. Pass both the old and new project paths so
// git records the delete-at-old and add-at-new sides of the rename. Uses the
// real git identity per ADR 0003, same as Commit.
func (c *Central) CommitPaths(message string, paths ...string) error {
	args := append([]string{"commit", "-m", message, "--"}, paths...)
	_, err := git.Run(c.Path, args...)
	return err
}

// ProjectPath returns the absolute on-disk location of a tracked agent
// directory inside the central repo. Pure path computation; does not
// check that the path exists.
func (c *Central) ProjectPath(name, dirName string) string {
	return filepath.Join(c.Path, "projects", name, dirName)
}

// ProjectAgentDirs returns the names of every agent directory tracked
// under projects/<name>/, in deterministic (sorted) order. Returns
// CodeNotFound when the project dir is missing or empty.
//
// The sorted result is the source of truth for "what dirs does this
// project have?" — state's per-project AgentDirs is a cache populated
// from this when state was migrated from v1 or when the orchestrator
// needs to backfill.
func (c *Central) ProjectAgentDirs(name string) ([]string, error) {
	projectDir := filepath.Join(c.Path, "projects", name)
	entries, err := os.ReadDir(projectDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, lerr.Newf(lerr.CodeNotFound,
				"project %q has no directory in the central repo", name)
		}
		return nil, lerr.Wrap(lerr.CodeGenericError, err,
			fmt.Sprintf("cannot read %s", projectDir))
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	if len(dirs) == 0 {
		return nil, lerr.Newf(lerr.CodeNotFound,
			"project %q has no agent directories tracked", name)
	}
	sort.Strings(dirs)
	return dirs, nil
}
