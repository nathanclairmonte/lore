package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nathanclairmonte/lore/internal/git"
	"github.com/stretchr/testify/require"
)

// postAddCmdSandbox returns a sandbox where my-app has already been
// onboarded via `lore add`. Used by unlink/rm tests so each can start
// from "project is tracked, symlink is in place, state.toml has the
// entry" without re-running the add wiring inline.
func postAddCmdSandbox(t *testing.T) (home, projectRoot string) {
	t.Helper()
	home, projectRoot = addCmdSandbox(t)
	require.Equal(t, 0, run([]string{"add", projectRoot}))
	return home, projectRoot
}

func TestLoreUnlink_HappyPathSingleDir(t *testing.T) {
	home, projectRoot := postAddCmdSandbox(t)

	require.Equal(t, 0, run([]string{"unlink", "my-app"}))

	// symlink is gone
	_, err := os.Lstat(filepath.Join(projectRoot, ".claude"))
	require.True(t, os.IsNotExist(err),
		"symlink should be removed; got err=%v", err)

	// central is untouched
	require.DirExists(t, filepath.Join(home, "agent-context", "projects", "my-app", ".claude"))

	// state no longer records the project
	stateBytes, _ := os.ReadFile(filepath.Join(home, ".local/state", "lore", "state.toml"))
	require.NotContains(t, string(stateBytes), "my-app",
		"state.toml should not reference my-app after unlink")
}

func TestLoreUnlink_RequiresNameOrAll(t *testing.T) {
	postAddCmdSandbox(t)

	// no args and no --all: usage error.
	require.Equal(t, 1, run([]string{"unlink"}))
}

func TestLoreUnlink_NameAndAllMutuallyExclusive(t *testing.T) {
	postAddCmdSandbox(t)

	require.Equal(t, 1, run([]string{"unlink", "--all", "my-app"}))
}

func TestLoreUnlink_AlreadyUnlinkedIdempotent(t *testing.T) {
	_, _ = postAddCmdSandbox(t)

	require.Equal(t, 0, run([]string{"unlink", "my-app"}))
	// second run on an already-unlinked project still exits 0.
	require.Equal(t, 0, run([]string{"unlink", "my-app"}),
		"idempotent re-run must exit 0")
}

func TestLoreUnlink_DriftAMissingSymlinkExitsZero(t *testing.T) {
	home, projectRoot := postAddCmdSandbox(t)

	// drift A: user removed the symlink manually before lore could.
	require.NoError(t, os.Remove(filepath.Join(projectRoot, ".claude")))

	require.Equal(t, 0, run([]string{"unlink", "my-app"}),
		"drift A (missing symlink) is benign; unlink scrubs state and exits 0")

	// state cleanup still happened.
	stateBytes, _ := os.ReadFile(filepath.Join(home, ".local/state", "lore", "state.toml"))
	require.NotContains(t, string(stateBytes), "my-app")
}

func TestLoreUnlink_DriftBRealDirectoryRefuses(t *testing.T) {
	home, projectRoot := postAddCmdSandbox(t)

	// drift B: replace the symlink with a real directory.
	linkPath := filepath.Join(projectRoot, ".claude")
	require.NoError(t, os.Remove(linkPath))
	require.NoError(t, os.MkdirAll(linkPath, 0o755))

	require.Equal(t, 5, run([]string{"unlink", "my-app"}),
		"drift B (real directory at link path) must exit 5")

	// state untouched — refusal leaves the entry intact for the user
	// to investigate (decision Q3b).
	stateBytes, _ := os.ReadFile(filepath.Join(home, ".local/state", "lore", "state.toml"))
	require.Contains(t, string(stateBytes), "my-app",
		"refusal must leave state.toml entry intact")
}

func TestLoreUnlink_DriftCWrongTargetRefuses(t *testing.T) {
	home, projectRoot := postAddCmdSandbox(t)

	// drift C: symlink exists but points somewhere unexpected.
	linkPath := filepath.Join(projectRoot, ".claude")
	require.NoError(t, os.Remove(linkPath))
	bogus := filepath.Join(home, "elsewhere")
	require.NoError(t, os.MkdirAll(bogus, 0o755))
	require.NoError(t, os.Symlink(bogus, linkPath))

	require.Equal(t, 5, run([]string{"unlink", "my-app"}),
		"drift C (wrong-target symlink) must exit 5")

	// state untouched.
	stateBytes, _ := os.ReadFile(filepath.Join(home, ".local/state", "lore", "state.toml"))
	require.Contains(t, string(stateBytes), "my-app")
}

func TestLoreUnlink_MultiDirHappyPath(t *testing.T) {
	home, projectRoot := postAddCmdSandbox(t)

	// extend with a second agent dir. matches slice 5's "extend a
	// tracked project" flow.
	require.NoError(t, os.Mkdir(filepath.Join(projectRoot, ".cursor"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(projectRoot, ".cursor", "rules.md"),
		[]byte("rules"), 0o644))
	require.NoError(t, os.WriteFile(
		filepath.Join(projectRoot, ".git", "info", "exclude"),
		[]byte(".claude/\n.cursor/\n"), 0o644))
	require.Equal(t, 0, run([]string{"add", "--dir", ".cursor", projectRoot}))

	// both should disappear in a single unlink.
	require.Equal(t, 0, run([]string{"unlink", "my-app"}))

	_, claudeErr := os.Lstat(filepath.Join(projectRoot, ".claude"))
	_, cursorErr := os.Lstat(filepath.Join(projectRoot, ".cursor"))
	require.True(t, os.IsNotExist(claudeErr))
	require.True(t, os.IsNotExist(cursorErr))

	// central still has both.
	require.DirExists(t, filepath.Join(home, "agent-context", "projects", "my-app", ".claude"))
	require.DirExists(t, filepath.Join(home, "agent-context", "projects", "my-app", ".cursor"))
}

func TestLoreUnlink_MultiDirPartialFailureRollsBack(t *testing.T) {
	_, projectRoot := postAddCmdSandbox(t)

	// add second agent dir.
	require.NoError(t, os.Mkdir(filepath.Join(projectRoot, ".cursor"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(projectRoot, ".cursor", "rules.md"),
		[]byte("rules"), 0o644))
	require.NoError(t, os.WriteFile(
		filepath.Join(projectRoot, ".git", "info", "exclude"),
		[]byte(".claude/\n.cursor/\n"), 0o644))
	require.Equal(t, 0, run([]string{"add", "--dir", ".cursor", projectRoot}))

	// pre-flight will see .claude is a valid symlink and .cursor is
	// drift B (real dir). decision Q1: refuse the whole project,
	// don't remove .claude. since refusal happens at pre-flight (no
	// side effect), no actual rollback fires — but the contract is the
	// same: state and disk untouched.
	cursorPath := filepath.Join(projectRoot, ".cursor")
	require.NoError(t, os.Remove(cursorPath))
	require.NoError(t, os.MkdirAll(cursorPath, 0o755))

	require.Equal(t, 5, run([]string{"unlink", "my-app"}),
		"multi-dir with one drift-B dir must refuse with exit 5")

	// .claude must still be a symlink (not removed).
	info, err := os.Lstat(filepath.Join(projectRoot, ".claude"))
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeSymlink,
		".claude symlink must still exist after multi-dir refusal")
}

func TestLoreUnlink_All(t *testing.T) {
	home, _ := postAddCmdSandbox(t)

	// add a second tracked project so --all has more than one to do.
	secondRoot := filepath.Join(home, "code", "second-app")
	require.NoError(t, os.MkdirAll(secondRoot, 0o755))
	_, err := git.Run(secondRoot, "init")
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(secondRoot, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(secondRoot, ".claude", "CLAUDE.md"),
		[]byte("ctx2"), 0o644))
	require.NoError(t, os.WriteFile(
		filepath.Join(secondRoot, ".git", "info", "exclude"),
		[]byte(".claude/\n"), 0o644))
	require.Equal(t, 0, run([]string{"add", secondRoot}))

	require.Equal(t, 0, run([]string{"unlink", "--all"}))

	// both symlinks gone.
	_, err1 := os.Lstat(filepath.Join(home, "code", "my-app", ".claude"))
	_, err2 := os.Lstat(filepath.Join(secondRoot, ".claude"))
	require.True(t, os.IsNotExist(err1))
	require.True(t, os.IsNotExist(err2))

	// state empty of project entries.
	stateBytes, _ := os.ReadFile(filepath.Join(home, ".local/state", "lore", "state.toml"))
	require.NotContains(t, string(stateBytes), "my-app")
	require.NotContains(t, string(stateBytes), "second-app")
}

func TestLoreUnlink_AllContinuesPastRefusal(t *testing.T) {
	home, projectRoot := postAddCmdSandbox(t)

	// second project, clean.
	secondRoot := filepath.Join(home, "code", "second-app")
	require.NoError(t, os.MkdirAll(secondRoot, 0o755))
	_, err := git.Run(secondRoot, "init")
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(secondRoot, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(secondRoot, ".claude", "CLAUDE.md"),
		[]byte("ctx2"), 0o644))
	require.NoError(t, os.WriteFile(
		filepath.Join(secondRoot, ".git", "info", "exclude"),
		[]byte(".claude/\n"), 0o644))
	require.Equal(t, 0, run([]string{"add", secondRoot}))

	// drift B on my-app: real dir instead of symlink.
	claudePath := filepath.Join(projectRoot, ".claude")
	require.NoError(t, os.Remove(claudePath))
	require.NoError(t, os.MkdirAll(claudePath, 0o755))

	// --all should refuse on my-app but still unlink second-app, and
	// exit with the worst code (5).
	require.Equal(t, 5, run([]string{"unlink", "--all"}))

	// second-app still got unlinked.
	_, err = os.Lstat(filepath.Join(secondRoot, ".claude"))
	require.True(t, os.IsNotExist(err),
		"--all must continue past per-project refusal (Q4a)")

	// my-app's state untouched (refusal preserves bookkeeping).
	stateBytes, _ := os.ReadFile(filepath.Join(home, ".local/state", "lore", "state.toml"))
	require.Contains(t, string(stateBytes), "my-app")
	require.NotContains(t, string(stateBytes), "second-app")
}
