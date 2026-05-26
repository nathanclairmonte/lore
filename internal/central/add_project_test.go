package central

import (
	"os"
	"path/filepath"
	"testing"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/git"
	"github.com/stretchr/testify/require"
)

// initCentralWithIdentity prepares a sandbox central repo with a git
// identity wired so `git commit` (no inline -c) succeeds. AddProject
// commits use the user's identity per ADR 0003.
func initCentralWithIdentity(t *testing.T) *Central {
	t.Helper()
	// isolate from the developer's ~/.gitconfig so the test deterministically
	// uses the identity we set here.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_AUTHOR_NAME", "Test Author")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Author")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")

	dir := filepath.Join(t.TempDir(), "central")
	c, err := Initialize(dir)
	require.NoError(t, err)
	return c
}

// makeProject creates a project root with a populated .claude dir and
// returns the agent-dir's source path on disk.
func makeProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	agent := filepath.Join(root, ".claude")
	require.NoError(t, os.Mkdir(agent, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(agent, "CLAUDE.md"),
		[]byte("context"), 0o644))
	return agent
}

func TestAddProjectMovesStagesAndCommits(t *testing.T) {
	c := initCentralWithIdentity(t)
	src := makeProject(t)

	dest, commit, undo, err := c.AddProject("my-app", ".claude", src)
	require.NoError(t, err)
	require.NotNil(t, commit)
	require.NotNil(t, undo)

	// source side: moved away
	_, statErr := os.Stat(src)
	require.True(t, os.IsNotExist(statErr))

	// destination side: in place with content
	require.Equal(t, filepath.Join(c.Path, "projects", "my-app", ".claude"), dest)
	require.FileExists(t, filepath.Join(dest, "CLAUDE.md"))

	// index is staged but not yet committed: HEAD has only the bootstrap commit.
	rev, err := git.Run(c.Path, "rev-list", "--count", "HEAD")
	require.NoError(t, err)
	require.Equal(t, "1", rev)

	// commit closure advances HEAD with the supplied message.
	require.NoError(t, commit("feat: add my-app"))
	rev, err = git.Run(c.Path, "rev-list", "--count", "HEAD")
	require.NoError(t, err)
	require.Equal(t, "2", rev)

	msg, err := git.Run(c.Path, "log", "-1", "--pretty=%s")
	require.NoError(t, err)
	require.Equal(t, "feat: add my-app", msg)

	// confirm the user's identity is on the commit (NOT lore@localhost).
	author, err := git.Run(c.Path, "log", "-1", "--pretty=%ae")
	require.NoError(t, err)
	require.Equal(t, "test@example.com", author)
}

func TestAddProjectUndoReversesRename(t *testing.T) {
	c := initCentralWithIdentity(t)
	src := makeProject(t)

	headBefore, err := git.Run(c.Path, "rev-parse", "HEAD")
	require.NoError(t, err)

	_, _, undo, err := c.AddProject("my-app", ".claude", src)
	require.NoError(t, err)

	require.NoError(t, undo())

	// source is back where it was, with content intact.
	require.FileExists(t, filepath.Join(src, "CLAUDE.md"))

	// no projects/<name>/ left behind in central.
	_, statErr := os.Stat(filepath.Join(c.Path, "projects", "my-app"))
	require.True(t, os.IsNotExist(statErr))

	// HEAD unchanged; index empty of our path.
	headAfter, err := git.Run(c.Path, "rev-parse", "HEAD")
	require.NoError(t, err)
	require.Equal(t, headBefore, headAfter)

	staged, err := git.Run(c.Path, "diff", "--cached", "--name-only")
	require.NoError(t, err)
	require.Empty(t, staged)
}

func TestAddProjectRefusesWhenProjectDirExists(t *testing.T) {
	c := initCentralWithIdentity(t)
	src := makeProject(t)

	// pre-create projects/my-app/ to simulate a stale state from an
	// earlier add that wasn't cleaned up.
	require.NoError(t, os.MkdirAll(filepath.Join(c.Path, "projects", "my-app"), 0o755))

	_, _, _, err := c.AddProject("my-app", ".claude", src)
	require.Error(t, err)
	require.Equal(t, lerr.CodeConflict, lerr.CodeOf(err))

	// source must not have moved.
	require.FileExists(t, filepath.Join(src, "CLAUDE.md"))
}

func TestProjectPathIsPure(t *testing.T) {
	c := &Central{Path: "/some/central"}
	got := c.ProjectPath("my-app", ".claude")
	require.Equal(t, "/some/central/projects/my-app/.claude", got)
}

func TestSingleAgentDirName(t *testing.T) {
	c := initCentralWithIdentity(t)
	src := makeProject(t)

	_, commit, _, err := c.AddProject("my-app", ".claude", src)
	require.NoError(t, err)
	require.NoError(t, commit("feat: add my-app"))

	got, err := c.SingleAgentDirName("my-app")
	require.NoError(t, err)
	require.Equal(t, ".claude", got)
}

func TestSingleAgentDirNameMissingProject(t *testing.T) {
	c := initCentralWithIdentity(t)
	_, err := c.SingleAgentDirName("never-tracked")
	require.Error(t, err)
	require.Equal(t, lerr.CodeNotFound, lerr.CodeOf(err))
}

func TestSingleAgentDirNameMultipleErrors(t *testing.T) {
	c := initCentralWithIdentity(t)
	// hand-build two agent dirs under projects/foo/ to simulate the
	// multi-dir world slice 5 enables; slice 2 surfaces this as an
	// inconsistency rather than guessing.
	pdir := filepath.Join(c.Path, "projects", "foo")
	require.NoError(t, os.MkdirAll(filepath.Join(pdir, ".claude"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(pdir, ".cursor"), 0o755))

	_, err := c.SingleAgentDirName("foo")
	require.Error(t, err)
	require.Equal(t, lerr.CodeInconsistency, lerr.CodeOf(err))
}
