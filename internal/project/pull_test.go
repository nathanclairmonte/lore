package project

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/git"
	"github.com/nathanclairmonte/lore/internal/state"
	"github.com/stretchr/testify/require"
)

func TestPullRetryAdoptsPublishedCopyWhenStateSaveFails(t *testing.T) {
	centralPath, projectRoot := addSandbox(t)
	_, err := Add(AddOptions{ProjectPath: projectRoot})
	require.NoError(t, err)
	centralFile := filepath.Join(centralPath, "projects", "my-app", ".claude", "CLAUDE.md")
	require.NoError(t, os.WriteFile(centralFile, []byte("central edit\n"), 0o644))
	_, err = git.Run(centralPath, "add", "-A")
	require.NoError(t, err)
	_, err = git.Run(centralPath, "commit", "-m", "test: central edit")
	require.NoError(t, err)
	head, err := git.Run(centralPath, "rev-parse", "HEAD")
	require.NoError(t, err)

	before, _, err := state.Load()
	require.NoError(t, err)
	entry, ok := before.ProjectEntryFor("my-app")
	require.True(t, ok)
	previousBase := entry.Checkouts[0].SyncBase

	originalSave := savePullState
	savePullState = func(state.State) error { return errors.New("injected state failure") }
	t.Cleanup(func() { savePullState = originalSave })

	result, err := Pull(PullOptions{Name: "my-app"})
	require.Error(t, err)
	require.Equal(t, lerr.CodeInconsistency, lerr.CodeOf(err))
	require.Equal(t, PullCheckoutFailed, result.Projects[0].Checkouts[0].Status)
	projectData, readErr := os.ReadFile(filepath.Join(projectRoot, ".claude", "CLAUDE.md"))
	require.NoError(t, readErr)
	require.Equal(t, "central edit\n", string(projectData))

	afterFailure, _, err := state.Load()
	require.NoError(t, err)
	entry, ok = afterFailure.ProjectEntryFor("my-app")
	require.True(t, ok)
	require.Equal(t, previousBase, entry.Checkouts[0].SyncBase)

	savePullState = originalSave
	retry, err := Pull(PullOptions{Name: "my-app"})
	require.NoError(t, err)
	require.Equal(t, PullCheckoutUnchanged, retry.Projects[0].Checkouts[0].Status)
	afterRetry, _, err := state.Load()
	require.NoError(t, err)
	entry, ok = afterRetry.ProjectEntryFor("my-app")
	require.True(t, ok)
	require.Equal(t, head, entry.Checkouts[0].SyncBase)
}
