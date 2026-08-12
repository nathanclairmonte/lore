package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nathanclairmonte/lore/internal/central"
	"github.com/nathanclairmonte/lore/internal/git"
	"github.com/nathanclairmonte/lore/internal/reconcile"
	"github.com/nathanclairmonte/lore/internal/state"
	"github.com/stretchr/testify/require"
)

func TestLoreMvRenamesCentralAndStateWithoutMovingCopy(t *testing.T) {
	_, projectRoot, centralRoot := trackedCopySandbox(t)
	projectFile := filepath.Join(projectRoot, ".claude", "CLAUDE.md")

	require.Equal(t, 0, run([]string{"mv", "my-app", "renamed"}))
	require.FileExists(t, projectFile)
	require.NoDirExists(t, filepath.Join(centralRoot, "projects", "my-app"))
	require.FileExists(t, filepath.Join(centralRoot, "projects", "renamed", ".claude", "CLAUDE.md"))
	st, _, err := state.Load()
	require.NoError(t, err)
	_, oldExists := st.ProjectEntryFor("my-app")
	entry, newExists := st.ProjectEntryFor("renamed")
	require.False(t, oldExists)
	require.True(t, newExists)
	head, err := git.Run(centralRoot, "rev-parse", "HEAD")
	require.NoError(t, err)
	require.Equal(t, head, entry.Checkouts[0].SyncBase)
}

func TestLoreMvRefusesCentralAhead(t *testing.T) {
	_, _, centralRoot := trackedCopySandbox(t)
	require.NoError(t, os.WriteFile(filepath.Join(centralRoot, "projects", "my-app", ".claude", "CLAUDE.md"), []byte("central edit\n"), 0o644))
	commitCentralChange(t, centralRoot)

	require.Equal(t, 5, run([]string{"mv", "my-app", "renamed"}))
	require.DirExists(t, filepath.Join(centralRoot, "projects", "my-app"))
	require.NoDirExists(t, filepath.Join(centralRoot, "projects", "renamed"))
}

func TestLoreRmKeepsProjectCopiesByDefault(t *testing.T) {
	_, projectRoot, centralRoot := trackedCopySandbox(t)
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, ".claude", "local.md"), []byte("unsynced\n"), 0o644))

	require.Equal(t, 0, run([]string{"rm", "--force", "my-app"}))
	require.FileExists(t, filepath.Join(projectRoot, ".claude", "local.md"))
	require.NoDirExists(t, filepath.Join(centralRoot, "projects", "my-app"))
	st, _, err := state.Load()
	require.NoError(t, err)
	_, exists := st.ProjectEntryFor("my-app")
	require.False(t, exists)
}

func TestLoreRmPurgeGuardsUnsyncedCopy(t *testing.T) {
	_, projectRoot, centralRoot := trackedCopySandbox(t)
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, ".claude", "local.md"), []byte("unsynced\n"), 0o644))
	previousTTY, previousConfirm := stdinIsTTY, confirmFn
	stdinIsTTY = func() bool { return true }
	confirmFn = func(string, bool) (bool, error) { return true, nil }
	t.Cleanup(func() { stdinIsTTY, confirmFn = previousTTY, previousConfirm })

	require.Equal(t, 5, run([]string{"rm", "--purge", "my-app"}))
	require.DirExists(t, filepath.Join(projectRoot, ".claude"))
	require.DirExists(t, filepath.Join(centralRoot, "projects", "my-app"))
	require.Equal(t, 0, run([]string{"rm", "--purge", "--force", "my-app"}))
	require.NoDirExists(t, filepath.Join(projectRoot, ".claude"))
}

func TestLoreRmJSONRequiresForceWithoutPrompting(t *testing.T) {
	_, _, centralRoot := trackedCopySandbox(t)
	previousTTY, previousConfirm := stdinIsTTY, confirmFn
	stdinIsTTY = func() bool { return true }
	confirmFn = func(string, bool) (bool, error) {
		t.Fatal("JSON removal must not open an interactive prompt")
		return false, nil
	}
	t.Cleanup(func() { stdinIsTTY, confirmFn = previousTTY, previousConfirm })

	require.Equal(t, 5, run([]string{"--json", "rm", "my-app"}))
	require.DirExists(t, filepath.Join(centralRoot, "projects", "my-app"))
}

func TestLoreRmReleasesMutationLockDuringConfirmation(t *testing.T) {
	_, _, centralRoot := trackedCopySandbox(t)
	previousTTY, previousConfirm := stdinIsTTY, confirmFn
	stdinIsTTY = func() bool { return true }
	confirmFn = func(string, bool) (bool, error) {
		acquired := make(chan error, 1)
		go func() {
			c, err := central.Open(centralRoot)
			if err == nil {
				var unlock func() error
				unlock, err = c.LockMutations()
				if err == nil {
					err = unlock()
				}
			}
			acquired <- err
		}()
		select {
		case err := <-acquired:
			return false, err
		case <-time.After(2 * time.Second):
			return false, errors.New("mutation lock remained held during confirmation")
		}
	}
	t.Cleanup(func() { stdinIsTTY, confirmFn = previousTTY, previousConfirm })

	require.Equal(t, 0, run([]string{"rm", "my-app"}))
	require.DirExists(t, filepath.Join(centralRoot, "projects", "my-app"))
}

func TestLoreDoctorFixRestoresMissingCopy(t *testing.T) {
	_, projectRoot, centralRoot := trackedCopySandbox(t)
	require.NoError(t, os.WriteFile(filepath.Join(centralRoot, "README.md"), []byte("advanced central head\n"), 0o644))
	commitCentralChange(t, centralRoot)
	require.NoError(t, os.RemoveAll(filepath.Join(projectRoot, ".claude")))

	require.Equal(t, 7, run([]string{"doctor"}))
	require.Equal(t, 0, run([]string{"doctor", "--fix"}))
	require.FileExists(t, filepath.Join(projectRoot, ".claude", "CLAUDE.md"))
	st, _, err := state.Load()
	require.NoError(t, err)
	entry, exists := st.ProjectEntryFor("my-app")
	require.True(t, exists)
	head, err := git.Run(centralRoot, "rev-parse", "HEAD")
	require.NoError(t, err)
	require.Equal(t, head, entry.Checkouts[0].SyncBase)
}

func TestLoreLsShowsEveryCheckoutAndCentralOnlyProject(t *testing.T) {
	home, projectRoot, centralRoot := trackedCopySandbox(t)
	secondRoot := filepath.Join(home, "code", "my-app-worktree")
	require.NoError(t, os.MkdirAll(secondRoot, 0o755))
	require.NoError(t, reconcile.CopyTree(filepath.Join(projectRoot, ".claude"), filepath.Join(secondRoot, ".claude")))
	head, err := git.Run(centralRoot, "rev-parse", "HEAD")
	require.NoError(t, err)
	st, _, err := state.Load()
	require.NoError(t, err)
	require.NoError(t, st.RecordCheckout("my-app", secondRoot, head))
	require.NoError(t, state.Save(st))
	require.NoError(t, os.MkdirAll(filepath.Join(centralRoot, "projects", "central-only", ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(centralRoot, "projects", "central-only", ".claude", "CLAUDE.md"), []byte("central\n"), 0o644))
	commitCentralChange(t, centralRoot)
	buffer := capturedOut(t)

	require.Equal(t, 0, run([]string{"--json", "ls"}))
	var envelope struct {
		Result struct {
			Rows []struct {
				Project   string `json:"project"`
				SyncState string `json:"sync_state"`
			} `json:"rows"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(buffer.Bytes(), &envelope))
	require.Len(t, envelope.Result.Rows, 3)
	require.Equal(t, "central-only", envelope.Result.Rows[0].Project)
	require.Equal(t, "not-placed", envelope.Result.Rows[0].SyncState)
}

func TestLoreRetiredCommandsAreUnknown(t *testing.T) {
	addCmdSandbox(t)
	require.Equal(t, 1, run([]string{"link"}))
	require.Equal(t, 1, run([]string{"unlink"}))
}
