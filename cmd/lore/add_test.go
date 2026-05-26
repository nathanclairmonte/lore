package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nathanclairmonte/lore/internal/git"
	"github.com/stretchr/testify/require"
)

// addCmdSandbox is the CLI-level equivalent of project.addSandbox. Wires
// HOME/XDG, an initialized central repo, a config file pointing at it, a
// project under HOME/code/my-app with .claude/ ignored via
// .git/info/exclude, and git identity env vars (no developer ~/.gitconfig).
func addCmdSandbox(t *testing.T) (home, projectRoot string) {
	t.Helper()
	home = sandbox(t)

	// identity for `lore add`'s user-authored commit on the central repo.
	t.Setenv("GIT_AUTHOR_NAME", "Test Author")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Author")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")

	require.Equal(t, 0, run([]string{"init"}))

	projectRoot = filepath.Join(home, "code", "my-app")
	require.NoError(t, os.MkdirAll(projectRoot, 0o755))
	_, err := git.Run(projectRoot, "init")
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(projectRoot, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(projectRoot, ".claude", "CLAUDE.md"),
		[]byte("context"), 0o644))
	require.NoError(t, os.WriteFile(
		filepath.Join(projectRoot, ".git", "info", "exclude"),
		[]byte(".claude/\n"), 0o644))
	return home, projectRoot
}

func TestLoreAdd_HappyPath(t *testing.T) {
	home, projectRoot := addCmdSandbox(t)

	require.Equal(t, 0, run([]string{"add", projectRoot}))

	// symlink in place pointing into central
	linkInfo, err := os.Lstat(filepath.Join(projectRoot, ".claude"))
	require.NoError(t, err)
	require.NotZero(t, linkInfo.Mode()&os.ModeSymlink)

	target, err := os.Readlink(filepath.Join(projectRoot, ".claude"))
	require.NoError(t, err)
	require.Equal(t,
		filepath.Join(home, "agent-context", "projects", "my-app", ".claude"),
		target)

	// commit landed on central with the templated message
	msg, err := git.Run(filepath.Join(home, "agent-context"), "log", "-1", "--pretty=%s")
	require.NoError(t, err)
	require.Equal(t, "feat: add my-app", msg)
}

func TestLoreAdd_RefusesNotGitignored(t *testing.T) {
	_, projectRoot := addCmdSandbox(t)
	// blank out the exclude rule so check-ignore says "not ignored".
	require.NoError(t, os.WriteFile(
		filepath.Join(projectRoot, ".git", "info", "exclude"),
		[]byte(""), 0o644))

	require.Equal(t, 5, run([]string{"add", projectRoot}),
		"non-gitignored agent dir must exit 5 (refused for safety)")
}
