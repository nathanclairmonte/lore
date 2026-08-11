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
	"strings"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
)

const (
	StagePathPrefix  = ".lore-stage-"
	BackupPathPrefix = ".lore-backup-"
)

// TreeReplacement names one central-to-project tree publication.
type TreeReplacement struct {
	Source      string
	Destination string
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
	temporary, err := createStagingTree(parent)
	if err != nil {
		return "", err
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(temporary)
		}
	}()
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

// StageSnapshot creates a sibling tree containing a merged snapshot.
func StageSnapshot(destinationRoot string, snapshot Snapshot) (string, error) {
	if err := validateSnapshot(snapshot); err != nil {
		return "", err
	}
	info, err := os.Lstat(destinationRoot)
	if err != nil {
		return "", lerr.Wrap(lerr.CodeGenericError, err, fmt.Sprintf("cannot inspect %s", destinationRoot))
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", lerr.Newf(lerr.CodeRefusedSafety, "agent directory %s must be a real directory", destinationRoot)
	}
	parent := filepath.Dir(destinationRoot)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", lerr.Wrap(lerr.CodeGenericError, err, fmt.Sprintf("cannot create %s", parent))
	}
	staged, err := createStagingTree(parent)
	if err != nil {
		return "", err
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(staged)
		}
	}()
	err = filepath.WalkDir(destinationRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == destinationRoot || !entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(destinationRoot, path)
		if err != nil {
			return err
		}
		return os.Mkdir(filepath.Join(staged, relative), 0o755)
	})
	if err != nil {
		return "", lerr.Wrap(lerr.CodeGenericError, err, "cannot prepare merge workspace")
	}
	if err := validateSnapshotCase(snapshot, staged); err != nil {
		return "", err
	}

	paths := make([]string, 0, len(snapshot))
	for path := range snapshot {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if err := validateSnapshotPath(path); err != nil {
			return "", lerr.Wrap(lerr.CodeInconsistency, err, "cannot materialise merged tree")
		}
		destination := filepath.Join(staged, filepath.FromSlash(path))
		if info, statErr := os.Lstat(destination); statErr == nil && info.IsDir() {
			if err := os.RemoveAll(destination); err != nil {
				return "", lerr.Wrap(lerr.CodeGenericError, err, "cannot replace merged directory path")
			}
		} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return "", lerr.Wrap(lerr.CodeGenericError, statErr, "cannot inspect merged path")
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return "", lerr.Wrap(lerr.CodeGenericError, err, "cannot create merged tree directory")
		}
		entry := snapshot[path]
		switch entry.Kind {
		case KindFile:
			mode := os.FileMode(0o644)
			if entry.Executable {
				mode = 0o755
			}
			if err := os.WriteFile(destination, entry.Data, mode); err != nil {
				return "", lerr.Wrap(lerr.CodeGenericError, err, "cannot write merged file")
			}
		case KindSymlink:
			if err := os.Symlink(string(entry.Data), destination); err != nil {
				return "", lerr.Wrap(lerr.CodeGenericError, err, "cannot write merged symlink")
			}
		default:
			return "", lerr.Newf(lerr.CodeInconsistency, "cannot materialise unknown entry kind %q", entry.Kind)
		}
	}
	complete = true
	return staged, nil
}

func createStagingTree(parent string) (string, error) {
	temporary, err := os.MkdirTemp(parent, StagePathPrefix+"*")
	if err != nil {
		return "", lerr.Wrap(lerr.CodeGenericError, err, "cannot create temporary copy directory")
	}
	if err := os.Chmod(temporary, 0o755); err != nil {
		_ = os.RemoveAll(temporary)
		return "", lerr.Wrap(lerr.CodeGenericError, err, "cannot set temporary copy permissions")
	}
	return temporary, nil
}

func validateSnapshot(snapshot Snapshot) error {
	for name := range snapshot {
		if err := validateSnapshotPath(name); err != nil {
			return lerr.Wrap(lerr.CodeInconsistency, err, "cannot materialise merged tree")
		}
		for parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(name))); parent != "."; parent = filepath.ToSlash(filepath.Dir(filepath.FromSlash(parent))) {
			if _, exists := snapshot[parent]; exists {
				return lerr.Newf(lerr.CodeRefusedSafety, "snapshot path %s conflicts with child path %s", parent, name)
			}
		}
	}
	return nil
}

func validateSnapshotPath(name string) error {
	local := filepath.FromSlash(name)
	if name == "" || name == "." || !filepath.IsLocal(local) {
		return lerr.Newf(lerr.CodeInconsistency, "invalid snapshot path %q", name)
	}
	return nil
}

func validateSnapshotCase(snapshot Snapshot, staged string) error {
	probeFile, err := os.CreateTemp(staged, ".lore-case-probe-")
	if err != nil {
		return lerr.Wrap(lerr.CodeGenericError, err, "cannot test merge workspace case handling")
	}
	probe := probeFile.Name()
	if err := probeFile.Close(); err != nil {
		_ = os.Remove(probe)
		return lerr.Wrap(lerr.CodeGenericError, err, "cannot close merge workspace case probe")
	}
	_, statErr := os.Lstat(filepath.Join(staged, strings.ToUpper(filepath.Base(probe))))
	if err := os.Remove(probe); err != nil {
		return lerr.Wrap(lerr.CodeGenericError, err, "cannot remove merge workspace case probe")
	}
	if errors.Is(statErr, os.ErrNotExist) {
		return nil
	}
	if statErr != nil {
		return lerr.Wrap(lerr.CodeGenericError, statErr, "cannot test merge workspace case handling")
	}
	spellings := map[string]string{}
	for name := range snapshot {
		parts := strings.Split(filepath.ToSlash(name), "/")
		for i := range parts {
			spelling := strings.Join(parts[:i+1], "/")
			folded := strings.ToLower(spelling)
			if prior, exists := spellings[folded]; exists && prior != spelling {
				return lerr.Newf(lerr.CodeRefusedSafety, "snapshot paths %s and %s collide on this filesystem", prior, spelling)
			}
			spellings[folded] = spelling
		}
	}
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
