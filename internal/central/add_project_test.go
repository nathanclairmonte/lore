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
// identity wired so `Commit` (no inline -c) succeeds. AddAgentDir's
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

// makeProjectDir creates an agent dir of the given name under a fresh
// project root and returns the source path.
func makeProjectDir(t *testing.T, name string) string {
	t.Helper()
	root := t.TempDir()
	agent := filepath.Join(root, name)
	require.NoError(t, os.Mkdir(agent, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(agent, "CLAUDE.md"),
		[]byte("context"), 0o644))
	return agent
}

func TestAddAgentDirMovesAndStages(t *testing.T) {
	c := initCentralWithIdentity(t)
	src := makeProjectDir(t, ".claude")

	dest, undo, err := c.AddAgentDir("my-app", ".claude", src)
	require.NoError(t, err)
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
}

func TestCommitAdvancesHeadWithUserIdentity(t *testing.T) {
	c := initCentralWithIdentity(t)
	src := makeProjectDir(t, ".claude")

	_, _, err := c.AddAgentDir("my-app", ".claude", src)
	require.NoError(t, err)

	require.NoError(t, c.Commit("feat: add my-app"))
	rev, err := git.Run(c.Path, "rev-list", "--count", "HEAD")
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

func TestAddAgentDirUndoReversesRename(t *testing.T) {
	c := initCentralWithIdentity(t)
	src := makeProjectDir(t, ".claude")

	headBefore, err := git.Run(c.Path, "rev-parse", "HEAD")
	require.NoError(t, err)

	_, undo, err := c.AddAgentDir("my-app", ".claude", src)
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

func TestAddAgentDirSecondDirReusesProjectDir(t *testing.T) {
	// slice 5 multi-dir core invariant: extending a tracked project with
	// a new agent dir must succeed (the project dir already exists from
	// the first add).
	c := initCentralWithIdentity(t)

	src1 := makeProjectDir(t, ".claude")
	_, _, err := c.AddAgentDir("my-app", ".claude", src1)
	require.NoError(t, err)
	require.NoError(t, c.Commit("feat: add my-app .claude"))

	src2 := makeProjectDir(t, ".cursor")
	dest2, undo2, err := c.AddAgentDir("my-app", ".cursor", src2)
	require.NoError(t, err)
	require.NotNil(t, undo2)
	require.FileExists(t, filepath.Join(dest2, "CLAUDE.md"))

	// both dirs are now siblings under projects/my-app/.
	require.DirExists(t, filepath.Join(c.Path, "projects", "my-app", ".claude"))
	require.DirExists(t, filepath.Join(c.Path, "projects", "my-app", ".cursor"))
}

func TestAddAgentDirUndoOfSecondDirPreservesFirst(t *testing.T) {
	// rolling back the second add must NOT remove the project dir or
	// the previously-tracked .claude inside it.
	c := initCentralWithIdentity(t)

	src1 := makeProjectDir(t, ".claude")
	_, _, err := c.AddAgentDir("my-app", ".claude", src1)
	require.NoError(t, err)
	require.NoError(t, c.Commit("feat: add my-app .claude"))

	src2 := makeProjectDir(t, ".cursor")
	_, undo2, err := c.AddAgentDir("my-app", ".cursor", src2)
	require.NoError(t, err)
	require.NoError(t, undo2())

	// .claude survives; .cursor is gone from central; src2 is restored.
	require.DirExists(t, filepath.Join(c.Path, "projects", "my-app", ".claude"))
	require.NoDirExists(t, filepath.Join(c.Path, "projects", "my-app", ".cursor"))
	require.FileExists(t, filepath.Join(src2, "CLAUDE.md"))
}

func TestAddAgentDirRefusesWhenAgentDirAlreadyTracked(t *testing.T) {
	c := initCentralWithIdentity(t)
	src := makeProjectDir(t, ".claude")

	// pre-create projects/my-app/.claude/ to simulate the slice-5
	// "this specific agent dir is already tracked" case.
	require.NoError(t, os.MkdirAll(filepath.Join(c.Path, "projects", "my-app", ".claude"), 0o755))

	_, _, err := c.AddAgentDir("my-app", ".claude", src)
	require.Error(t, err)
	require.Equal(t, lerr.CodeConflict, lerr.CodeOf(err))

	// source must not have moved.
	require.FileExists(t, filepath.Join(src, "CLAUDE.md"))
}

func TestAddAgentDirAllowsExtendingExistingProjectDir(t *testing.T) {
	// slice-5 sibling of the refusal case above: a pre-existing
	// projects/<name>/ but NO collision on dirName must NOT refuse.
	c := initCentralWithIdentity(t)
	src := makeProjectDir(t, ".cursor")

	require.NoError(t, os.MkdirAll(filepath.Join(c.Path, "projects", "my-app"), 0o755))

	dest, undo, err := c.AddAgentDir("my-app", ".cursor", src)
	require.NoError(t, err)
	require.NotNil(t, undo)
	require.FileExists(t, filepath.Join(dest, "CLAUDE.md"))
}

func TestProjectPathIsPure(t *testing.T) {
	c := &Central{Path: "/some/central"}
	got := c.ProjectPath("my-app", ".claude")
	require.Equal(t, "/some/central/projects/my-app/.claude", got)
}

func TestProjectAgentDirsSingle(t *testing.T) {
	c := initCentralWithIdentity(t)
	src := makeProjectDir(t, ".claude")

	_, _, err := c.AddAgentDir("my-app", ".claude", src)
	require.NoError(t, err)
	require.NoError(t, c.Commit("feat: add my-app"))

	got, err := c.ProjectAgentDirs("my-app")
	require.NoError(t, err)
	require.Equal(t, []string{".claude"}, got)
}

func TestProjectAgentDirsMultipleSorted(t *testing.T) {
	c := initCentralWithIdentity(t)

	// hand-build two agent dirs under projects/foo/ to mirror what
	// successive AddAgentDir calls would produce. Sorted-string order
	// is the contract.
	pdir := filepath.Join(c.Path, "projects", "foo")
	require.NoError(t, os.MkdirAll(filepath.Join(pdir, ".cursor"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(pdir, ".claude"), 0o755))

	got, err := c.ProjectAgentDirs("foo")
	require.NoError(t, err)
	require.Equal(t, []string{".claude", ".cursor"}, got,
		"results must be sorted so callers get a deterministic order")
}

func TestProjectAgentDirsMissingProject(t *testing.T) {
	c := initCentralWithIdentity(t)
	_, err := c.ProjectAgentDirs("never-tracked")
	require.Error(t, err)
	require.Equal(t, lerr.CodeNotFound, lerr.CodeOf(err))
}

func TestProjectAgentDirsEmptyProject(t *testing.T) {
	c := initCentralWithIdentity(t)
	// project dir exists but has no agent-dir subdirs (a half-state that
	// shouldn't normally exist, but be defensive).
	require.NoError(t, os.MkdirAll(filepath.Join(c.Path, "projects", "ghost"), 0o755))

	_, err := c.ProjectAgentDirs("ghost")
	require.Error(t, err)
	require.Equal(t, lerr.CodeNotFound, lerr.CodeOf(err))
}
