package central

import (
	"os"
	"path/filepath"
	"testing"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/git"
	"github.com/stretchr/testify/require"
)

// initCentralWithProject returns a sandbox central repo with name onboarded
// and committed, so RenameProjectStaged sees a clean, tracked projects/<name>/.
func initCentralWithProject(t *testing.T, name string) *Central {
	t.Helper()
	c := initCentralWithIdentity(t)
	src := makeProjectDir(t, ".claude")
	_, _, err := c.AddAgentDir(name, ".claude", src)
	require.NoError(t, err)
	require.NoError(t, c.Commit("feat: add "+name))
	return c
}

func TestRenameProjectStagesMove(t *testing.T) {
	c := initCentralWithProject(t, "foo")

	undo, err := c.RenameProjectStaged("foo", "bar")
	require.NoError(t, err)
	require.NotNil(t, undo)

	// working tree shows the rename.
	require.NoDirExists(t, filepath.Join(c.Path, "projects", "foo"))
	require.FileExists(t, filepath.Join(c.Path, "projects", "bar", ".claude", "CLAUDE.md"))

	// staged but not committed: HEAD still at the add commit (2 commits:
	// bootstrap + add).
	rev, err := git.Run(c.Path, "rev-list", "--count", "HEAD")
	require.NoError(t, err)
	require.Equal(t, "2", rev)
}

func TestRenameProjectOldMissingNotFound(t *testing.T) {
	c := initCentralWithProject(t, "foo")

	_, err := c.RenameProjectStaged("nope", "bar")
	require.Error(t, err)
	require.Equal(t, lerr.CodeNotFound, lerr.CodeOf(err))
}

func TestRenameProjectNewExistsConflict(t *testing.T) {
	c := initCentralWithProject(t, "foo")
	// onboard a second project to collide with.
	src := makeProjectDir(t, ".claude")
	_, _, err := c.AddAgentDir("bar", ".claude", src)
	require.NoError(t, err)
	require.NoError(t, c.Commit("feat: add bar"))

	_, err = c.RenameProjectStaged("foo", "bar")
	require.Error(t, err)
	require.Equal(t, lerr.CodeConflict, lerr.CodeOf(err))
}

func TestRenameProjectDirtyRefuses(t *testing.T) {
	c := initCentralWithProject(t, "foo")

	// uncommitted edit under projects/foo/ → refuse before the git mv.
	dirty := filepath.Join(c.Path, "projects", "foo", ".claude", "CLAUDE.md")
	require.NoError(t, os.WriteFile(dirty, []byte("edited"), 0o644))

	_, err := c.RenameProjectStaged("foo", "bar")
	require.Error(t, err)
	require.Equal(t, lerr.CodeRefusedSafety, lerr.CodeOf(err))

	// nothing moved.
	require.DirExists(t, filepath.Join(c.Path, "projects", "foo"))
	require.NoDirExists(t, filepath.Join(c.Path, "projects", "bar"))
}

func TestRenameProjectUndoRestores(t *testing.T) {
	c := initCentralWithProject(t, "foo")

	undo, err := c.RenameProjectStaged("foo", "bar")
	require.NoError(t, err)
	require.NoError(t, undo())

	// reverse git mv restored the old path and cleared the new one.
	require.FileExists(t, filepath.Join(c.Path, "projects", "foo", ".claude", "CLAUDE.md"))
	require.NoDirExists(t, filepath.Join(c.Path, "projects", "bar"))
}
