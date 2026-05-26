// Package central manages the lore central repository as a typed handle.
// It owns the projects/<name>/<agent-dir>/ layout and the git operations
// needed to maintain it.
//
// Slice 1 ships only Initialize, Open, and HasRemote. AddProject /
// RemoveProject / RenameProject arrive with the slices that need them.
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
tracked project's agent directories are symlinked from their on-disk
locations into this repo.

Do not edit files here by hand while a session is running — your harness's
session-end hook may overwrite them.
`

// Central is a typed handle on a central repo on disk.
type Central struct {
	Path string // absolute path
}

// Initialize creates path (if missing), runs git init, writes README, and
// makes an initial commit so the repo isn't in unborn-HEAD state. Caller is
// responsible for higher-level checks (e.g. dir non-empty). Returns the
// handle on success.
func Initialize(path string) (*Central, error) {
	if path == "" {
		return nil, lerr.New(lerr.CodeGenericError, "central path is empty")
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return nil, lerr.Wrap(lerr.CodeGenericError, err, "cannot create central directory")
	}
	if err := git.Init(path); err != nil {
		return nil, err
	}

	readmePath := filepath.Join(path, "README.md")
	if err := os.WriteFile(readmePath, []byte(readmeBody), 0o644); err != nil {
		return nil, lerr.Wrap(lerr.CodeGenericError, err, "cannot write README.md")
	}
	if _, err := git.Run(path, "add", "README.md"); err != nil {
		return nil, err
	}
	// supply identity inline so users who haven't configured a global git
	// identity yet (common on fresh machines) don't hit "author identity
	// unknown" on lore's very first commit. Subsequent user-initiated
	// commits (sync, add, etc.) use the user's real git identity.
	if _, err := git.Run(path,
		"-c", "user.name=lore",
		"-c", "user.email=lore@localhost",
		"commit", "-m", "chore: initialize lore central repository",
	); err != nil {
		return nil, err
	}
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

// IsEmpty reports whether dir is empty or doesn't exist. Used by callers
// who need to decide whether to refuse onboarding a non-empty default
// location. A non-existent dir counts as empty.
func IsEmpty(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		return false, lerr.Wrap(lerr.CodeGenericError, err, fmt.Sprintf("cannot inspect %s", dir))
	}
	return len(entries) == 0, nil
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
