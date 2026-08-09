// Package central manages the lore central repository as a typed handle.
// It owns the projects/<name>/<agent-dir>/ layout and the git operations
// needed to maintain it.
package central

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/git"
)

// readmeBody is committed as README.md when a fresh central repo is
// initialized. Kept here rather than in the output module because this is
// repo content, not user-facing CLI copy.
const readmeBody = `# agent-context

Managed by [lore](https://github.com/nathanclairmonte/lore).

This repository holds per-project agentic AI context (` + "`.claude/`" + `,
` + "`.cursor/`" + `, ` + "`.aider/`" + `, etc.) under ` + "`projects/<name>/`" + `. Each
tracked checkout keeps real project copies and reconciles them with these
central copies through lore.

Use ` + "`lore push`" + ` and ` + "`lore pull`" + ` to reconcile copies. Use plain Git
inside this repository to reach its remote.
`

// Central is a typed handle on a central repo on disk.
type Central struct {
	Path string // absolute path
}

// Initialize creates path (if missing), runs git init, lays down the
// canonical repo structure (README.md + projects/.gitkeep), and makes an
// initial commit so the repo isn't in unborn-HEAD state.
//
// Atomicity: if Initialize created the directory and any step after the
// MkdirAll fails, the directory is removed before returning. This means a
// retry sees a clean slate rather than a half-built repo. If path already
// existed when we were called, we leave it alone on failure (it's not ours
// to delete).
//
// Caller is responsible for higher-level checks like "is the dir non-empty".
func Initialize(path string) (*Central, error) {
	if path == "" {
		return nil, lerr.New(lerr.CodeGenericError, "central path is empty")
	}

	preExisted, err := Exists(path)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(path, 0o755); err != nil {
		return nil, lerr.Wrap(lerr.CodeGenericError, err, "cannot create central directory")
	}

	// rollback closure: only fires on failure; only removes the dir if we
	// were the ones who created it. callers can re-run safely afterwards.
	// if cleanup itself fails, surface it to stderr as a secondary
	// warning: the primary error still bubbles up, but the user needs to
	// know they may be left with a half-built dir to clean by hand.
	success := false
	defer func() {
		if !success && !preExisted {
			if rmErr := os.RemoveAll(path); rmErr != nil {
				fmt.Fprintf(os.Stderr,
					"warning: failed to clean up partial central repo at %s: %v\n",
					path, rmErr)
			}
		}
	}()

	if err := git.Init(path); err != nil {
		return nil, err
	}

	readmePath := filepath.Join(path, "README.md")
	if err := os.WriteFile(readmePath, []byte(readmeBody), 0o644); err != nil {
		return nil, lerr.Wrap(lerr.CodeGenericError, err, "cannot write README.md")
	}

	// SPEC's central layout puts every tracked project under projects/.
	// laying it down now (with a .gitkeep so git tracks the empty dir)
	// means `lore use` and later structural checks see the canonical
	// shape from the very first commit.
	projectsDir := filepath.Join(path, "projects")
	if err := os.MkdirAll(projectsDir, 0o755); err != nil {
		return nil, lerr.Wrap(lerr.CodeGenericError, err, "cannot create projects directory")
	}
	gitkeep := filepath.Join(projectsDir, ".gitkeep")
	if err := os.WriteFile(gitkeep, nil, 0o644); err != nil {
		return nil, lerr.Wrap(lerr.CodeGenericError, err, "cannot write projects/.gitkeep")
	}

	if _, err := git.Run(path, "add", "README.md", "projects/.gitkeep"); err != nil {
		return nil, err
	}
	// supply identity AND disable gpg signing inline so users on fresh
	// machines (no global git identity) and users with commit.gpgsign=true
	// (no signing key for lore@localhost) can both bootstrap. lore's
	// bootstrap commit is system content, not user content; user-authored
	// commits in later slices use the user's real identity and signing
	// config.
	if _, err := git.Run(path,
		"-c", "user.name=lore",
		"-c", "user.email=lore@localhost",
		"-c", "commit.gpgsign=false",
		"commit", "-m", "chore: initialize lore central repository",
	); err != nil {
		return nil, err
	}
	success = true
	return &Central{Path: path}, nil
}

// Open returns a handle for an existing central repo. Verifies path exists
// and contains a .git directory; further structural validation (e.g.
// `projects/` present) is left to `lore use` in a later slice.
func Open(path string) (*Central, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, lerr.Newf(lerr.CodeNotFound, "central repo not found at %s", path)
		}
		return nil, lerr.Wrap(lerr.CodeGenericError, err, "cannot stat central path")
	}
	if !info.IsDir() {
		return nil, lerr.Newf(lerr.CodeNotFound, "central path %s is not a directory", path)
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
		return nil, lerr.Newf(lerr.CodeNotFound, "central path %s is not a git repository", path)
	}
	return &Central{Path: path}, nil
}

// HasRemote reports whether the central repo has any configured git remote.
// Used to decide whether to print the "no remote configured" hint.
func (c *Central) HasRemote() (bool, error) {
	out, err := git.Run(c.Path, "remote")
	if err != nil {
		return false, err
	}
	return out != "", nil
}

// osMetadataNames is the static denylist of cross-platform OS-generated
// files that should not count as user content when deciding whether a
// directory is effectively empty. Intentionally small; expand only on a
// real reported case rather than speculatively. .DS_Store is macOS
// Finder; Thumbs.db and desktop.ini are Windows Explorer (often appearing
// on SMB / sync mounts touched from a Windows host); .localized is the
// macOS Finder marker that turns localized folder names on.
var osMetadataNames = map[string]struct{}{
	".DS_Store":   {},
	"Thumbs.db":   {},
	"desktop.ini": {},
	".localized":  {},
}

// IsEmpty reports whether dir is empty (or contains only known OS-metadata
// junk), or doesn't exist. Used by `lore init` to decide whether to refuse
// onboarding a non-empty default location.
//
// The denylist exists because `ls` hides dotfiles by default: a user on
// macOS with only a `.DS_Store` in ~/agent-context sees an apparently
// empty dir but `lore init` would refuse, producing a confusing error.
// The denylist is deliberately narrow (four well-known names); a user who
// genuinely places `.foo` in this dir is respected.
func IsEmpty(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		return false, lerr.Wrap(lerr.CodeGenericError, err, fmt.Sprintf("cannot inspect %s", dir))
	}
	for _, e := range entries {
		if _, junk := osMetadataNames[e.Name()]; !junk {
			return false, nil
		}
	}
	return true, nil
}

// Exists reports whether path exists on disk (file or directory).
func Exists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, lerr.Wrap(lerr.CodeGenericError, err, fmt.Sprintf("cannot stat %s", path))
}
