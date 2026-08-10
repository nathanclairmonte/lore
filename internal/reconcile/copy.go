// Package reconcile compares and copies project and central trees.
package reconcile

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
)

// TreeReplacement names one central-to-project tree publication.
type TreeReplacement struct {
	Source      string
	Destination string
}

// ReplaceTrees atomically publishes a set of replacement trees.
func ReplaceTrees(replacements []TreeReplacement) (err error) {
	if len(replacements) == 0 {
		return nil
	}

	ordered := append([]TreeReplacement(nil), replacements...)
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].Destination < ordered[j].Destination
	})

	type stagedTree struct {
		replacement TreeReplacement
		staged      string
		backup      string
		published   bool
	}
	staged := make([]stagedTree, 0, len(ordered))
	finished := false
	defer func() {
		if finished {
			return
		}
		var rollbackErrors []error
		for i := len(staged) - 1; i >= 0; i-- {
			item := staged[i]
			if item.published {
				if removeErr := os.RemoveAll(item.replacement.Destination); removeErr != nil {
					rollbackErrors = append(rollbackErrors,
						fmt.Errorf("cannot remove %s: %w", item.replacement.Destination, removeErr))
				}
			}
			if item.backup != "" {
				if restoreErr := os.Rename(item.backup, item.replacement.Destination); restoreErr != nil {
					rollbackErrors = append(rollbackErrors,
						fmt.Errorf("cannot restore %s from %s: %w",
							item.replacement.Destination, item.backup, restoreErr))
				}
			}
			_ = os.RemoveAll(item.staged)
		}
		// a stranded backup is the user's only copy of the original tree, so
		// naming it in the error is the only pointer they get to their content.
		if rollbackErr := errors.Join(rollbackErrors...); rollbackErr != nil {
			err = lerr.Wrap(lerr.CodeInconsistency, err,
				fmt.Sprintf("project copy replacement failed and rollback also failed: %v", rollbackErr))
		}
	}()

	for i, replacement := range ordered {
		// sorting by destination puts any duplicate next to its twin.
		if i > 0 && replacement.Destination == ordered[i-1].Destination {
			return lerr.Newf(lerr.CodeInconsistency,
				"duplicate tree replacement destination %s", replacement.Destination)
		}
		stagePath, err := stageTree(replacement.Source, filepath.Dir(replacement.Destination))
		if err != nil {
			return err
		}
		staged = append(staged, stagedTree{replacement: replacement, staged: stagePath})
	}

	for i := range staged {
		item := &staged[i]
		info, err := os.Lstat(item.replacement.Destination)
		switch {
		case err == nil:
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return lerr.Newf(lerr.CodeRefusedSafety,
					"project copy %s is not a real directory", item.replacement.Destination)
			}
			backupPath, backupErr := unusedSiblingPath(filepath.Dir(item.replacement.Destination), ".lore-backup-")
			if backupErr != nil {
				return backupErr
			}
			if err := os.Rename(item.replacement.Destination, backupPath); err != nil {
				return lerr.Wrap(lerr.CodeGenericError, err,
					fmt.Sprintf("cannot stage existing project copy %s", item.replacement.Destination))
			}
			item.backup = backupPath
		case errors.Is(err, os.ErrNotExist):
			// a missing destination is the normal materialisation path.
		default:
			return lerr.Wrap(lerr.CodeGenericError, err,
				fmt.Sprintf("cannot inspect %s", item.replacement.Destination))
		}
		if err := os.Rename(item.staged, item.replacement.Destination); err != nil {
			return lerr.Wrap(lerr.CodeGenericError, err,
				fmt.Sprintf("cannot publish project copy %s", item.replacement.Destination))
		}
		item.published = true
	}

	// publication is now committed. Backup cleanup failures must not roll
	// back a complete checkout to a mixture of old and new trees.
	finished = true
	// one undeletable backup must not strand the rest, so every backup is
	// attempted before the failures are reported together.
	var cleanupErrors []error
	for _, item := range staged {
		if item.backup != "" {
			if cleanupErr := os.RemoveAll(item.backup); cleanupErr != nil {
				cleanupErrors = append(cleanupErrors, cleanupErr)
			}
		}
	}
	if cleanupErr := errors.Join(cleanupErrors...); cleanupErr != nil {
		return lerr.Wrap(lerr.CodeInconsistency, cleanupErr,
			"project copy updated but backup cleanup failed")
	}
	return nil
}

func unusedSiblingPath(parent, prefix string) (string, error) {
	path, err := os.MkdirTemp(parent, prefix)
	if err != nil {
		return "", lerr.Wrap(lerr.CodeGenericError, err, "cannot reserve project-copy staging path")
	}
	if err := os.Remove(path); err != nil {
		return "", lerr.Wrap(lerr.CodeGenericError, err, "cannot prepare project-copy staging path")
	}
	return path, nil
}

// CopyTree copies source to a new destination and publishes it atomically.
func CopyTree(source, destination string) error {
	if _, err := os.Lstat(destination); err == nil {
		return lerr.Newf(lerr.CodeConflict, "%s already exists", destination)
	} else if !errors.Is(err, os.ErrNotExist) {
		return lerr.Wrap(lerr.CodeGenericError, err, fmt.Sprintf("cannot inspect %s", destination))
	}
	staged, err := stageTree(source, filepath.Dir(destination))
	if err != nil {
		return err
	}
	if err := os.Rename(staged, destination); err != nil {
		_ = os.RemoveAll(staged)
		return lerr.Wrap(lerr.CodeGenericError, err, fmt.Sprintf("cannot publish %s", destination))
	}
	return nil
}

// stageTree copies source into a fresh directory beside the eventual
// destination and returns that path. Staging as a sibling keeps publication a
// same-filesystem rename, so the caller owns the staged tree and must either
// rename it into place or remove it.
func stageTree(source, parent string) (string, error) {
	info, err := os.Lstat(source)
	if err != nil {
		return "", lerr.Wrap(lerr.CodeGenericError, err, fmt.Sprintf("cannot inspect %s", source))
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", lerr.Newf(lerr.CodeRefusedSafety,
			"agent directory %s must be a real directory", source)
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", lerr.Wrap(lerr.CodeGenericError, err, fmt.Sprintf("cannot create %s", parent))
	}
	temporary, err := os.MkdirTemp(parent, ".lore-stage-*")
	if err != nil {
		return "", lerr.Wrap(lerr.CodeGenericError, err, "cannot create temporary copy directory")
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(temporary)
		}
	}()
	if err := os.Chmod(temporary, 0o755); err != nil {
		return "", lerr.Wrap(lerr.CodeGenericError, err, "cannot set temporary copy permissions")
	}

	err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil || relative == "." {
			return err
		}
		target := filepath.Join(temporary, relative)
		switch {
		case entry.IsDir():
			return os.Mkdir(target, 0o755)
		case entry.Type()&os.ModeSymlink != 0:
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(linkTarget, target)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(linkTarget, target)
		}
		if info.Mode().IsRegular() {
			return copyRegularFile(path, target, info.Mode()&0o111 != 0)
		}
		return lerr.Newf(lerr.CodeRefusedSafety,
			"agent directory contains unsupported special file %s", path)
	})
	if err != nil {
		if lerr.CodeOf(err) != lerr.CodeGenericError {
			return "", err
		}
		return "", lerr.Wrap(lerr.CodeGenericError, err, fmt.Sprintf("cannot copy %s", source))
	}
	complete = true
	return temporary, nil
}

func copyRegularFile(source, destination string, executable bool) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}

	mode := os.FileMode(0o644)
	if executable {
		mode = 0o755
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		_ = input.Close()
		return err
	}
	_, copyErr := io.Copy(output, input)
	inputCloseErr := input.Close()
	outputCloseErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if inputCloseErr != nil {
		return inputCloseErr
	}
	return outputCloseErr
}

type treeEntry struct {
	kind       fs.FileMode
	executable bool
	linkTarget string
}

// TreesEqual reports whether two trees contain the same Git-reproducible data.
func TreesEqual(left, right string) (bool, error) {
	leftEntries, err := inspectTree(left)
	if err != nil {
		return false, err
	}
	rightEntries, err := inspectTree(right)
	if err != nil {
		return false, err
	}
	if len(leftEntries) != len(rightEntries) {
		return false, nil
	}
	leftBuffer := make([]byte, 32*1024)
	rightBuffer := make([]byte, len(leftBuffer))
	for path, leftEntry := range leftEntries {
		rightEntry, ok := rightEntries[path]
		if !ok || leftEntry != rightEntry {
			return false, nil
		}
		if leftEntry.kind == 0 {
			equal, err := filesEqual(
				filepath.Join(left, filepath.FromSlash(path)),
				filepath.Join(right, filepath.FromSlash(path)),
				leftBuffer,
				rightBuffer,
			)
			if err != nil || !equal {
				return equal, err
			}
		}
	}
	return true, nil
}

func inspectTree(root string) (map[string]treeEntry, error) {
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return nil, lerr.Wrap(lerr.CodeGenericError, err, fmt.Sprintf("cannot inspect %s", root))
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return nil, lerr.Newf(lerr.CodeRefusedSafety, "%s must be a real directory", root)
	}
	entries := map[string]treeEntry{}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == "." {
			return err
		}
		// Git cannot reproduce empty directories, so tree equality ignores
		// directory entries and compares only reproducible content.
		if entry.IsDir() {
			return nil
		}
		item := treeEntry{}
		switch {
		case entry.Type()&os.ModeSymlink != 0:
			item.kind = os.ModeSymlink
			item.linkTarget, err = os.Readlink(path)
			if err != nil {
				return err
			}
		default:
			info, infoErr := entry.Info()
			if infoErr != nil {
				return infoErr
			}
			if info.Mode()&os.ModeSymlink != 0 {
				item.kind = os.ModeSymlink
				item.linkTarget, err = os.Readlink(path)
				if err != nil {
					return err
				}
			} else if info.Mode().IsRegular() {
				item.executable = info.Mode()&0o111 != 0
			} else {
				return lerr.Newf(lerr.CodeRefusedSafety,
					"agent directory contains unsupported special file %s", path)
			}
		}
		entries[filepath.ToSlash(relative)] = item
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

func filesEqual(leftPath, rightPath string, leftBuffer, rightBuffer []byte) (equal bool, err error) {
	left, err := os.Open(leftPath)
	if err != nil {
		return false, err
	}
	defer func() {
		if closeErr := left.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	right, err := os.Open(rightPath)
	if err != nil {
		return false, err
	}
	defer func() {
		if closeErr := right.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()

	for {
		leftN, leftErr := io.ReadFull(left, leftBuffer)
		rightN, rightErr := io.ReadFull(right, rightBuffer)
		leftDone := errors.Is(leftErr, io.EOF) || errors.Is(leftErr, io.ErrUnexpectedEOF)
		rightDone := errors.Is(rightErr, io.EOF) || errors.Is(rightErr, io.ErrUnexpectedEOF)
		if leftErr != nil && !leftDone {
			return false, leftErr
		}
		if rightErr != nil && !rightDone {
			return false, rightErr
		}
		if leftN != rightN || !bytes.Equal(leftBuffer[:leftN], rightBuffer[:rightN]) {
			return false, nil
		}
		if leftDone || rightDone {
			return leftDone && rightDone, nil
		}
	}
}
