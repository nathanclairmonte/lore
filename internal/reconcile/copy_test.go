package reconcile

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/stretchr/testify/require"
)

func TestCopyTreePreservesGitReproducibleData(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	destination := filepath.Join(root, "destination")
	require.NoError(t, os.MkdirAll(filepath.Join(source, "empty"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(source, "plain.txt"), []byte("plain"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(source, "run.sh"), []byte("#!/bin/sh\n"), 0o700))
	require.NoError(t, os.Symlink("plain.txt", filepath.Join(source, "relative-link")))

	require.NoError(t, CopyTree(source, destination))
	require.FileExists(t, filepath.Join(source, "plain.txt"))
	require.DirExists(t, filepath.Join(destination, "empty"))
	target, err := os.Readlink(filepath.Join(destination, "relative-link"))
	require.NoError(t, err)
	require.Equal(t, "plain.txt", target)
	plainInfo, err := os.Stat(filepath.Join(destination, "plain.txt"))
	require.NoError(t, err)
	require.Zero(t, plainInfo.Mode()&0o111)
	executableInfo, err := os.Stat(filepath.Join(destination, "run.sh"))
	require.NoError(t, err)
	require.NotZero(t, executableInfo.Mode()&0o111)
	equal, err := TreesEqual(source, destination)
	require.NoError(t, err)
	require.True(t, equal)
}

func TestCopyTreeRejectsSpecialFileWithoutPublishingDestination(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	destination := filepath.Join(root, "destination")
	require.NoError(t, os.Mkdir(source, 0o755))
	require.NoError(t, syscall.Mkfifo(filepath.Join(source, "pipe"), 0o600))

	err := CopyTree(source, destination)
	require.Error(t, err)
	require.Equal(t, lerr.CodeRefusedSafety, lerr.CodeOf(err))
	require.NoDirExists(t, destination)
}

func TestTreesEqualDetectsContentModeAndSymlinkChanges(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	require.NoError(t, os.Mkdir(left, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(left, "file"), []byte("same"), 0o644))
	require.NoError(t, os.Symlink("file", filepath.Join(left, "link")))
	require.NoError(t, CopyTree(left, right))

	require.NoError(t, os.Chmod(filepath.Join(right, "file"), 0o755))
	equal, err := TreesEqual(left, right)
	require.NoError(t, err)
	require.False(t, equal)

	require.NoError(t, os.Chmod(filepath.Join(right, "file"), 0o644))
	require.NoError(t, os.Remove(filepath.Join(right, "link")))
	require.NoError(t, os.Symlink("other", filepath.Join(right, "link")))
	equal, err = TreesEqual(left, right)
	require.NoError(t, err)
	require.False(t, equal)
}

func TestTreesEqualIgnoresNonExecutablePermissionDifferences(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	require.NoError(t, os.Mkdir(left, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(left, "file"), []byte("same"), 0o600))
	require.NoError(t, CopyTree(left, right))
	require.NoError(t, os.Chmod(filepath.Join(right, "file"), 0o644))

	equal, err := TreesEqual(left, right)
	require.NoError(t, err)
	require.True(t, equal)
}

func TestTreesEqualIgnoresEmptyDirectories(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	require.NoError(t, os.MkdirAll(filepath.Join(left, "empty", "nested"), 0o755))
	require.NoError(t, os.Mkdir(right, 0o755))

	equal, err := TreesEqual(left, right)
	require.NoError(t, err)
	require.True(t, equal)
}
