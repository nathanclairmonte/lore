// Package link wraps the POSIX symlink primitives lore uses to plant a
// link at a project's agent-directory location pointing into the central
// repo. Centralizing these operations keeps lore's symlink quirks
// (Lstat-not-Stat, exact-target compare, idempotent removal semantics) in
// one spot rather than scattered across the orchestrator.
//
// Linux + macOS only per ADR 0001; Windows users are expected to use WSL.
package link

import (
	"errors"
	"fmt"
	"os"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
)

// Create makes a symlink at linkPath pointing to target. target should be
// absolute (slice 2 design decision: absolute targets are easier to debug
// with `ls -la` and the central-repo-move case is already a `lore use`
// migration, not free).
//
// Returns an error if linkPath already exists (regular file, directory,
// or existing symlink). Callers that need replace-semantics should Remove
// first.
func Create(target, linkPath string) error {
	if err := os.Symlink(target, linkPath); err != nil {
		return lerr.Wrap(lerr.CodeGenericError, err,
			fmt.Sprintf("cannot create symlink at %s", linkPath))
	}
	return nil
}

// Remove deletes a symlink at linkPath. Uses Lstat (not Stat) so the link
// itself is inspected, not its target. Returns an error if linkPath is
// not a symlink (the orchestrator's rollback path must not destroy a
// real directory by mistake).
func Remove(linkPath string) error {
	info, err := os.Lstat(linkPath)
	if err != nil {
		return lerr.Wrap(lerr.CodeGenericError, err,
			fmt.Sprintf("cannot stat %s", linkPath))
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return lerr.Newf(lerr.CodeGenericError, "%s is not a symlink", linkPath)
	}
	if err := os.Remove(linkPath); err != nil {
		return lerr.Wrap(lerr.CodeGenericError, err,
			fmt.Sprintf("cannot remove symlink at %s", linkPath))
	}
	return nil
}

// Validate returns nil iff linkPath is a symlink whose stored target
// equals expectedTarget exactly. Used by the already-tracked detection
// path: state + symlink must agree on the same target string, no path
// resolution applied.
func Validate(linkPath, expectedTarget string) error {
	isLink, err := IsSymlink(linkPath)
	if err != nil {
		return err
	}
	if !isLink {
		return lerr.Newf(lerr.CodeGenericError, "%s is not a symlink", linkPath)
	}
	actual, err := os.Readlink(linkPath)
	if err != nil {
		return lerr.Wrap(lerr.CodeGenericError, err,
			fmt.Sprintf("cannot read symlink %s", linkPath))
	}
	if actual != expectedTarget {
		return lerr.Newf(lerr.CodeGenericError,
			"symlink %s points to %s, expected %s",
			linkPath, actual, expectedTarget)
	}
	return nil
}

// IsSymlink reports whether path exists and is a symlink. Uses Lstat so
// the link is inspected rather than followed. Missing path returns
// (false, nil); other stat failures bubble up.
func IsSymlink(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, lerr.Wrap(lerr.CodeGenericError, err,
			fmt.Sprintf("cannot stat %s", path))
	}
	return info.Mode()&os.ModeSymlink != 0, nil
}
