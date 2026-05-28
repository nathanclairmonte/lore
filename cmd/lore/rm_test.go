package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nathanclairmonte/lore/internal/git"
	"github.com/stretchr/testify/require"
)

func TestLoreRm_ForceHappyPath(t *testing.T) {
	home, projectRoot := postAddCmdSandbox(t)

	require.Equal(t, 0, run([]string{"rm", "--force", "my-app"}))

	// symlink gone
	_, err := os.Lstat(filepath.Join(projectRoot, ".claude"))
	require.True(t, os.IsNotExist(err))

	// central project dir gone from working tree
	require.NoDirExists(t,
		filepath.Join(home, "agent-context", "projects", "my-app"))

	// state cleared
	stateBytes, _ := os.ReadFile(filepath.Join(home, ".local/state", "lore", "state.toml"))
	require.NotContains(t, string(stateBytes), "my-app")

	// commit landed with rm template
	msg, err := git.Run(filepath.Join(home, "agent-context"), "log", "-1", "--pretty=%s")
	require.NoError(t, err)
	require.Equal(t, "chore: remove my-app", msg)
}

func TestLoreRm_RecoverableViaGitCheckout(t *testing.T) {
	home, _ := postAddCmdSandbox(t)

	require.Equal(t, 0, run([]string{"rm", "--force", "my-app"}))

	// recovery: the literal command the prompt would have surfaced.
	central := filepath.Join(home, "agent-context")
	_, err := git.Run(central, "checkout", "HEAD~1", "--", "projects/my-app/")
	require.NoError(t, err)

	// recovered file is back with original content.
	got, err := os.ReadFile(filepath.Join(central, "projects/my-app/.claude/CLAUDE.md"))
	require.NoError(t, err)
	require.Equal(t, "context", string(got),
		"git checkout HEAD~1 must restore the project content")
}

func TestLoreRm_NonTTYWithoutForceExits5(t *testing.T) {
	postAddCmdSandbox(t)

	// in `go test` stdin is not a TTY. without --force, rm must
	// refuse with exit 5 BEFORE any side effect.
	require.Equal(t, 5, run([]string{"rm", "my-app"}),
		"non-TTY without --force must exit 5 (refused for safety)")
}

func TestLoreRm_NotFoundExits3(t *testing.T) {
	postAddCmdSandbox(t)

	// project never existed in state OR central → exit 3.
	require.Equal(t, 3, run([]string{"rm", "--force", "nonexistent"}))
}

func TestLoreRm_InteractiveYesProceeds(t *testing.T) {
	home, projectRoot := postAddCmdSandbox(t)

	// pretend stdin is a TTY and stub the confirm to return yes.
	prevTTY := stdinIsTTY
	stdinIsTTY = func() bool { return true }
	t.Cleanup(func() { stdinIsTTY = prevTTY })

	prevConfirm := confirmFn
	confirmFn = func(question string, defaultYes bool) (bool, error) {
		return true, nil
	}
	t.Cleanup(func() { confirmFn = prevConfirm })

	require.Equal(t, 0, run([]string{"rm", "my-app"}))

	// destructive flow ran: symlink + central + state all gone.
	_, err := os.Lstat(filepath.Join(projectRoot, ".claude"))
	require.True(t, os.IsNotExist(err))
	require.NoDirExists(t,
		filepath.Join(home, "agent-context", "projects", "my-app"))
}

func TestLoreRm_InteractiveNoAborts(t *testing.T) {
	home, projectRoot := postAddCmdSandbox(t)

	prevTTY := stdinIsTTY
	stdinIsTTY = func() bool { return true }
	t.Cleanup(func() { stdinIsTTY = prevTTY })

	prevConfirm := confirmFn
	confirmFn = func(question string, defaultYes bool) (bool, error) {
		return false, nil
	}
	t.Cleanup(func() { confirmFn = prevConfirm })

	require.Equal(t, 0, run([]string{"rm", "my-app"}),
		"saying no must exit 0 (clean abort, not error)")

	// nothing changed.
	info, err := os.Lstat(filepath.Join(projectRoot, ".claude"))
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeSymlink,
		"symlink must still be in place after declining")
	require.DirExists(t,
		filepath.Join(home, "agent-context", "projects", "my-app"))

	stateBytes, _ := os.ReadFile(filepath.Join(home, ".local/state", "lore", "state.toml"))
	require.Contains(t, string(stateBytes), "my-app")
}

func TestLoreRm_DriftBRefusalSurfacesBeforePromptCommit(t *testing.T) {
	home, projectRoot := postAddCmdSandbox(t)

	// drift B: real dir in the way. with --force, rm should refuse
	// with exit 5 before any central or state mutation (the unlink
	// pre-flight catches it).
	linkPath := filepath.Join(projectRoot, ".claude")
	require.NoError(t, os.Remove(linkPath))
	require.NoError(t, os.MkdirAll(linkPath, 0o755))

	require.Equal(t, 5, run([]string{"rm", "--force", "my-app"}))

	// central + state untouched.
	require.DirExists(t,
		filepath.Join(home, "agent-context", "projects", "my-app"))
	stateBytes, _ := os.ReadFile(filepath.Join(home, ".local/state", "lore", "state.toml"))
	require.Contains(t, string(stateBytes), "my-app")
}

func TestLoreRm_MultiAgentDirCleanRemoval(t *testing.T) {
	home, projectRoot := postAddCmdSandbox(t)

	// add a second agent dir.
	require.NoError(t, os.Mkdir(filepath.Join(projectRoot, ".cursor"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(projectRoot, ".cursor", "rules.md"),
		[]byte("rules"), 0o644))
	require.NoError(t, os.WriteFile(
		filepath.Join(projectRoot, ".git", "info", "exclude"),
		[]byte(".claude/\n.cursor/\n"), 0o644))
	require.Equal(t, 0, run([]string{"add", "--dir", ".cursor", projectRoot}))

	require.Equal(t, 0, run([]string{"rm", "--force", "my-app"}))

	// every symlink and the central dir all gone.
	_, claudeErr := os.Lstat(filepath.Join(projectRoot, ".claude"))
	_, cursorErr := os.Lstat(filepath.Join(projectRoot, ".cursor"))
	require.True(t, os.IsNotExist(claudeErr))
	require.True(t, os.IsNotExist(cursorErr))
	require.NoDirExists(t,
		filepath.Join(home, "agent-context", "projects", "my-app"))
}

func TestLoreRm_RemovesAfterUnlink(t *testing.T) {
	// covers the "project in central but not in state" path: unlink
	// first, then rm. unlinkProject's AlreadyUnlinked branch fires,
	// the central removal still proceeds.
	home, _ := postAddCmdSandbox(t)

	require.Equal(t, 0, run([]string{"unlink", "my-app"}))
	// at this point state has no entry, but central still has the dir.
	require.Equal(t, 0, run([]string{"rm", "--force", "my-app"}))

	require.NoDirExists(t,
		filepath.Join(home, "agent-context", "projects", "my-app"))
}

func TestLoreRm_DirtyCentralRefusesExit5(t *testing.T) {
	home, projectRoot := postAddCmdSandbox(t)

	// touch a file inside the central project dir so git status sees
	// uncommitted modifications. rm must refuse before running git rm,
	// so nothing on the local side moves either.
	dirty := filepath.Join(home, "agent-context", "projects", "my-app", ".claude", "CLAUDE.md")
	require.NoError(t, os.WriteFile(dirty, []byte("edited locally"), 0o644))

	require.Equal(t, 5, run([]string{"rm", "--force", "my-app"}),
		"dirty central must refuse with exit 5 before any side effect")

	// local symlink still in place; state untouched; central edit still
	// present so the user can choose what to do with it.
	info, err := os.Lstat(filepath.Join(projectRoot, ".claude"))
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeSymlink,
		".claude symlink must still exist after dirty refusal")
	stateBytes, _ := os.ReadFile(filepath.Join(home, ".local/state", "lore", "state.toml"))
	require.Contains(t, string(stateBytes), "my-app")
	got, err := os.ReadFile(dirty)
	require.NoError(t, err)
	require.Equal(t, "edited locally", string(got),
		"central-side edit must survive the refusal untouched")
}

func TestLoreRm_CommitFailureRollsBackEverything(t *testing.T) {
	// install a failing pre-commit hook in central so c.Commit fails
	// AFTER the destructive sequence already landed (symlink removed,
	// central staged, state saved). the LIFO drain must restore every
	// side effect: state file rewritten with the original entry,
	// central project dir checked back out from HEAD, symlink recreated.
	home, projectRoot := postAddCmdSandbox(t)

	hooksDir := filepath.Join(home, "agent-context", ".git", "hooks")
	require.NoError(t, os.MkdirAll(hooksDir, 0o755))
	hookPath := filepath.Join(hooksDir, "pre-commit")
	require.NoError(t, os.WriteFile(hookPath,
		[]byte("#!/bin/sh\necho 'blocking commit for test' >&2\nexit 1\n"), 0o755))

	require.NotEqual(t, 0, run([]string{"rm", "--force", "my-app"}),
		"commit failure must propagate as a nonzero exit")

	// rollback assertions: every side effect undone.

	// symlink restored.
	info, err := os.Lstat(filepath.Join(projectRoot, ".claude"))
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeSymlink,
		"rollback must recreate the symlink")

	// state still has my-app.
	stateBytes, _ := os.ReadFile(filepath.Join(home, ".local/state", "lore", "state.toml"))
	require.Contains(t, string(stateBytes), "my-app",
		"rollback must restore state.toml entry")

	// central project dir still present (git checkout HEAD restored it).
	require.DirExists(t,
		filepath.Join(home, "agent-context", "projects", "my-app"),
		"rollback must restore central project dir from HEAD")
	got, err := os.ReadFile(filepath.Join(home, "agent-context", "projects/my-app/.claude/CLAUDE.md"))
	require.NoError(t, err)
	require.Equal(t, "context", string(got),
		"restored content must match original")
}
