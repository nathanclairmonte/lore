package central

import (
	"os"
	"path/filepath"
	"testing"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/git"
	"github.com/stretchr/testify/require"
)

func TestInitializeCreatesDirAndRepo(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agent-context")
	c, err := Initialize(dir)
	require.NoError(t, err)
	require.Equal(t, dir, c.Path)

	require.DirExists(t, filepath.Join(dir, ".git"))
	require.FileExists(t, filepath.Join(dir, "README.md"))

	// initial commit exists; HEAD resolves.
	out, err := git.Run(dir, "rev-parse", "HEAD")
	require.NoError(t, err)
	require.NotEmpty(t, out)
}

func TestInitializeIntoExistingEmptyDir(t *testing.T) {
	dir := t.TempDir() // t.TempDir returns an empty existing dir
	_, err := Initialize(dir)
	require.NoError(t, err)
	require.DirExists(t, filepath.Join(dir, ".git"))
}

func TestOpenSucceedsOnInitializedRepo(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ac")
	_, err := Initialize(dir)
	require.NoError(t, err)

	c, err := Open(dir)
	require.NoError(t, err)
	require.Equal(t, dir, c.Path)
}

func TestOpenFailsOnMissingDir(t *testing.T) {
	_, err := Open(filepath.Join(t.TempDir(), "missing"))
	require.Error(t, err)
	require.Equal(t, lerr.CodeNotFound, lerr.CodeOf(err))
}

func TestOpenFailsOnNonGitDir(t *testing.T) {
	dir := t.TempDir()
	_, err := Open(dir)
	require.Error(t, err)
	require.Equal(t, lerr.CodeNotFound, lerr.CodeOf(err))
}

func TestHasRemote(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ac")
	c, err := Initialize(dir)
	require.NoError(t, err)

	has, err := c.HasRemote()
	require.NoError(t, err)
	require.False(t, has, "fresh repo has no remote")

	_, err = git.Run(dir, "remote", "add", "origin", "https://example.com/x.git")
	require.NoError(t, err)

	has, err = c.HasRemote()
	require.NoError(t, err)
	require.True(t, has)
}

func TestIsEmpty(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	ok, err := IsEmpty(missing)
	require.NoError(t, err)
	require.True(t, ok, "non-existent dir reports as empty")

	empty := t.TempDir()
	ok, err = IsEmpty(empty)
	require.NoError(t, err)
	require.True(t, ok)

	nonEmpty := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(nonEmpty, "f"), []byte("x"), 0o644))
	ok, err = IsEmpty(nonEmpty)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestExists(t *testing.T) {
	dir := t.TempDir()
	ok, err := Exists(dir)
	require.NoError(t, err)
	require.True(t, ok)

	ok, err = Exists(filepath.Join(dir, "nope"))
	require.NoError(t, err)
	require.False(t, ok)
}
