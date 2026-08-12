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

func TestPushKeepsCommitWhenStateSaveFailsAndRetryAdopts(t *testing.T) {
	centralPath, projectRoot := addSandbox(t)
	_, err := Add(AddOptions{ProjectPath: projectRoot})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, ".claude", "CLAUDE.md"), []byte("changed\n"), 0o644))
	originalSave := savePushState
	savePushState = func(state.State) error { return errors.New("injected state failure") }
	t.Cleanup(func() { savePushState = originalSave })

	result, err := Push(PushOptions{Project: "my-app"})
	require.Error(t, err)
	require.Equal(t, lerr.CodeInconsistency, lerr.CodeOf(err))
	require.Len(t, result.CommitSHA, 40)
	head, headErr := git.Run(centralPath, "rev-parse", "HEAD")
	require.NoError(t, headErr)
	require.Equal(t, head, result.CommitSHA)

	savePushState = originalSave
	retry, err := Push(PushOptions{Project: "my-app"})
	require.NoError(t, err)
	require.Empty(t, retry.CommitSHA)
	st, _, err := state.Load()
	require.NoError(t, err)
	entry, ok := st.ProjectEntryFor("my-app")
	require.True(t, ok)
	require.Equal(t, head, entry.Checkouts[0].SyncBase)
}
