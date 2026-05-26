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
	require.DirExists(t, filepath.Join(dir, "projects"))
	require.FileExists(t, filepath.Join(dir, "projects", ".gitkeep"))

	// initial commit exists; HEAD resolves.
	out, err := git.Run(dir, "rev-parse", "HEAD")
	require.NoError(t, err)
	require.NotEmpty(t, out)
}

func TestInitializeRollsBackOnFailure(t *testing.T) {
	// Force the bootstrap commit to fail by pointing git at a path that
	// already contains a file where .git/ would land. git init refuses
	// when .git exists as a non-dir.
	parent := t.TempDir()
	target := filepath.Join(parent, "agent-context")
	require.NoError(t, os.MkdirAll(target, 0o755))
	// place a file at .git so `git init` will fail (cannot create dir).
	require.NoError(t, os.WriteFile(filepath.Join(target, ".git"), []byte("not a dir"), 0o644))
	// since target pre-existed, our rollback should leave it alone
	// rather than removing it.
	_, err := Initialize(target)
	require.Error(t, err)
	require.DirExists(t, target, "pre-existing dir must not be removed by rollback")
}

func TestInitializeRollsBackCreatedDir(t *testing.T) {
	// emptied PATH forces exec.Command("git", ...) to fail with
	// exec.ErrNotFound at git.Init. Initialize will have already created
	// the target dir via MkdirAll; the deferred rollback must remove it.
	t.Setenv("PATH", "")

	parent := t.TempDir()
	target := filepath.Join(parent, "agent-context")
	// target does NOT exist yet; Initialize will create it.

	_, err := Initialize(target)
	require.Error(t, err)
	require.Equal(t, lerr.CodeSetupMissing, lerr.CodeOf(err))

	_, statErr := os.Stat(target)
	require.True(t, os.IsNotExist(statErr), "Initialize must remove the dir it created on failure")
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

func TestIsEmptyNonExistent(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	ok, err := IsEmpty(missing)
	require.NoError(t, err)
	require.True(t, ok, "non-existent dir reports as empty")
}

func TestIsEmpty(t *testing.T) {
	cases := []struct {
		name    string
		entries []string // file names to create at the root of a fresh tempdir
		want    bool
	}{
		{
			name:    "truly empty",
			entries: nil,
			want:    true,
		},
		{
			name:    "only .DS_Store",
			entries: []string{".DS_Store"},
			want:    true,
		},
		{
			name:    "all four denylist names",
			entries: []string{".DS_Store", "Thumbs.db", "desktop.ini", ".localized"},
			want:    true,
		},
		{
			name:    "one regular file",
			entries: []string{"f"},
			want:    false,
		},
		{
			name: ".DS_Store plus a regular file",
			// the denylist short-circuits per-entry, so a single non-junk
			// file is enough to flip the dir back to non-empty.
			entries: []string{".DS_Store", "f"},
			want:    false,
		},
		{
			name: "non-denylisted dotfile counts as content",
			// proves we are NOT just stripping all dotfiles: a user who
			// genuinely places `.envrc` (or `.gitkeep`) must be respected.
			entries: []string{".envrc"},
			want:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, name := range tc.entries {
				require.NoError(t, os.WriteFile(filepath.Join(dir, name),
					[]byte(""), 0o644))
			}
			got, err := IsEmpty(dir)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
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
