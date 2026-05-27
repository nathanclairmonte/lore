package central

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/git"
)

// AddProject moves the directory at sourcePath into projects/<name>/<dirName>/
// inside the central repo and stages it. Returns the absolute destination
// path along with two closures the orchestrator drives in sequence:
//
//   - commit(msg) runs `git commit -m msg`. Uses the user's real git
//     identity (no inline -c flags) per ADR 0003.
//   - undo() reverses the rename, drops the staged path from the index,
//     and removes the empty projects/<name>/ dir we created. Safe to call
//     after commit has already run (it skips the git steps if HEAD
//     advanced past our add).
//
// Splitting commit and undo lets project.Add fully roll back when a
// post-rename step (symlink, state-write) fails: we revert the move
// without a dangling commit on central's history. Commit fires last,
// only once every other side effect has succeeded.
//
// Refuses with CodeConflict if projects/<name>/ already exists, since
// AddProject must never silently merge with an existing tracked project.
func (c *Central) AddProject(name, dirName, sourcePath string) (
	destPath string,
	commit func(msg string) error,
	undo func() error,
	err error,
) {
	destProjectDir := filepath.Join(c.Path, "projects", name)
	destAgentDir := filepath.Join(destProjectDir, dirName)

	if _, statErr := os.Stat(destProjectDir); statErr == nil {
		return "", nil, nil, lerr.Newf(lerr.CodeConflict,
			"projects/%s already exists in the central repo", name)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", nil, nil, lerr.Wrap(lerr.CodeGenericError, statErr,
			fmt.Sprintf("cannot stat %s", destProjectDir))
	}

	if mkErr := os.MkdirAll(destProjectDir, 0o755); mkErr != nil {
		return "", nil, nil, lerr.Wrap(lerr.CodeGenericError, mkErr,
			fmt.Sprintf("cannot create %s", destProjectDir))
	}

	if renameErr := os.Rename(sourcePath, destAgentDir); renameErr != nil {
		// MkdirAll above may have created destProjectDir; clean it up
		// so the caller can retry without tripping the "already exists"
		// branch above.
		_ = os.Remove(destProjectDir)
		return "", nil, nil, lerr.Wrap(lerr.CodeGenericError, renameErr,
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
			// surfacing as a warning here mirrors central.Initialize's
			// approach: the primary error bubbles, but the user needs to
			// know they may have to recover manually.
			fmt.Fprintf(os.Stderr,
				"warning: failed to revert rename %s -> %s: %v\n",
				destAgentDir, sourcePath, revertErr)
		}
		_ = os.Remove(destProjectDir)
		return "", nil, nil, addErr
	}

	commit = func(msg string) error {
		_, runErr := git.Run(c.Path, "commit", "-m", msg)
		return runErr
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
		// best-effort cleanup of the now-empty projects/<name>/ dir. if
		// the user manually placed something here, leave it; the slice
		// 19 doctor will surface the orphan.
		_ = os.Remove(destProjectDir)
		return nil
	}

	return destAgentDir, commit, undo, nil
}

// ProjectPath returns the absolute on-disk location of a tracked agent
// directory inside the central repo. Pure path computation; does not
// check that the path exists.
func (c *Central) ProjectPath(name, dirName string) string {
	return filepath.Join(c.Path, "projects", name, dirName)
}

// SingleAgentDirName returns the lone agent-directory name tracked under
// projects/<name>/. Returns CodeNotFound when the project dir is missing
// or empty, and CodeInconsistency when multiple subdirs are present (the
// multi-dir case is not yet supported).
func (c *Central) SingleAgentDirName(name string) (string, error) {
	projectDir := filepath.Join(c.Path, "projects", name)
	entries, err := os.ReadDir(projectDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", lerr.Newf(lerr.CodeNotFound,
				"project %q has no directory in the central repo", name)
		}
		return "", lerr.Wrap(lerr.CodeGenericError, err,
			fmt.Sprintf("cannot read %s", projectDir))
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	switch len(dirs) {
	case 0:
		return "", lerr.Newf(lerr.CodeNotFound,
			"project %q has no agent directories tracked", name)
	case 1:
		return dirs[0], nil
	default:
		return "", lerr.Newf(lerr.CodeInconsistency,
			"project %q has multiple agent dirs tracked; multi-dir support is not wired up yet", name)
	}
}
