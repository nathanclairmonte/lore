package link

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCreateAndIsSymlinkAndReadlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	require.NoError(t, os.Mkdir(target, 0o755))

	linkPath := filepath.Join(dir, "the-link")
	require.NoError(t, Create(target, linkPath))

	isLink, err := IsSymlink(linkPath)
	require.NoError(t, err)
	require.True(t, isLink)

	resolved, err := os.Readlink(linkPath)
	require.NoError(t, err)
	require.Equal(t, target, resolved)
}

func TestCreateErrorsWhenLinkPathExists(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	require.NoError(t, os.Mkdir(target, 0o755))

	linkPath := filepath.Join(dir, "the-link")
	require.NoError(t, os.WriteFile(linkPath, []byte("blocker"), 0o644))

	err := Create(target, linkPath)
	require.Error(t, err)
}

func TestRemoveDeletesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	require.NoError(t, os.Mkdir(target, 0o755))
	linkPath := filepath.Join(dir, "the-link")
	require.NoError(t, Create(target, linkPath))

	require.NoError(t, Remove(linkPath))

	// link is gone but target survives. essential for rollback: we must
	// never delete the real directory by mistake.
	_, err := os.Lstat(linkPath)
	require.True(t, os.IsNotExist(err))
	_, err = os.Stat(target)
	require.NoError(t, err)
}

func TestRemoveRefusesRealDirectory(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	require.NoError(t, os.Mkdir(realDir, 0o755))

	err := Remove(realDir)
	require.Error(t, err)

	// directory must still exist; refusing is only meaningful if we
	// haven't already removed it.
	_, statErr := os.Stat(realDir)
	require.NoError(t, statErr)
}

func TestRemoveErrorsOnMissingPath(t *testing.T) {
	dir := t.TempDir()
	err := Remove(filepath.Join(dir, "nope"))
	require.Error(t, err)
}

func TestValidateMatches(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	require.NoError(t, os.Mkdir(target, 0o755))
	linkPath := filepath.Join(dir, "the-link")
	require.NoError(t, Create(target, linkPath))

	require.NoError(t, Validate(linkPath, target))
}

func TestValidateMismatch(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	require.NoError(t, os.Mkdir(target, 0o755))
	linkPath := filepath.Join(dir, "the-link")
	require.NoError(t, Create(target, linkPath))

	err := Validate(linkPath, filepath.Join(dir, "something-else"))
	require.Error(t, err)
}

func TestValidateNotASymlink(t *testing.T) {
	dir := t.TempDir()
	realFile := filepath.Join(dir, "regular")
	require.NoError(t, os.WriteFile(realFile, []byte(""), 0o644))

	err := Validate(realFile, "irrelevant")
	require.Error(t, err)
}

func TestIsSymlinkVariants(t *testing.T) {
	dir := t.TempDir()

	// missing path -> false, nil
	missing, err := IsSymlink(filepath.Join(dir, "absent"))
	require.NoError(t, err)
	require.False(t, missing)

	// regular file -> false, nil
	reg := filepath.Join(dir, "file")
	require.NoError(t, os.WriteFile(reg, []byte(""), 0o644))
	got, err := IsSymlink(reg)
	require.NoError(t, err)
	require.False(t, got)

	// real directory -> false, nil
	d := filepath.Join(dir, "subdir")
	require.NoError(t, os.Mkdir(d, 0o755))
	got, err = IsSymlink(d)
	require.NoError(t, err)
	require.False(t, got)

	// symlink -> true, nil
	link := filepath.Join(dir, "link")
	require.NoError(t, Create(d, link))
	got, err = IsSymlink(link)
	require.NoError(t, err)
	require.True(t, got)
}
