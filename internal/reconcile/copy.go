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

	lerr "github.com/nathanclairmonte/lore/internal/errors"
)

// CopyTree copies source to a new destination and publishes it atomically.
func CopyTree(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return lerr.Wrap(lerr.CodeGenericError, err, fmt.Sprintf("cannot inspect %s", source))
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return lerr.Newf(lerr.CodeRefusedSafety,
			"agent directory %s must be a real directory", source)
	}
	if _, err := os.Lstat(destination); err == nil {
		return lerr.Newf(lerr.CodeConflict, "%s already exists", destination)
	} else if !errors.Is(err, os.ErrNotExist) {
		return lerr.Wrap(lerr.CodeGenericError, err, fmt.Sprintf("cannot inspect %s", destination))
	}

	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return lerr.Wrap(lerr.CodeGenericError, err, fmt.Sprintf("cannot create %s", parent))
	}
	temporary, err := os.MkdirTemp(parent, ".lore-copy-*")
	if err != nil {
		return lerr.Wrap(lerr.CodeGenericError, err, "cannot create temporary copy directory")
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(temporary)
		}
	}()
	if err := os.Chmod(temporary, 0o755); err != nil {
		return lerr.Wrap(lerr.CodeGenericError, err, "cannot set temporary copy permissions")
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
			return err
		}
		return lerr.Wrap(lerr.CodeGenericError, err, fmt.Sprintf("cannot copy %s", source))
	}
	if err := os.Rename(temporary, destination); err != nil {
		return lerr.Wrap(lerr.CodeGenericError, err, fmt.Sprintf("cannot publish %s", destination))
	}
	published = true
	return nil
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
