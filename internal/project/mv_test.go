package project

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/git"
	"github.com/nathanclairmonte/lore/internal/reconcile"
	"github.com/nathanclairmonte/lore/internal/state"
	"github.com/stretchr/testify/require"
)

func TestMoveRetryRecoversCommittedRenameAfterStateSaveFailure(t *testing.T) {
	centralPath, projectRoot := addSandbox(t)
	_, err := Add(AddOptions{ProjectPath: projectRoot})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, ".claude", "local.md"), []byte("project-ahead\n"), 0o644))
	previousSave := saveMoveState
	calls := 0
	saveMoveState = func(value state.State) error {
		calls++
		if calls == 1 {
			return errors.New("injected state failure")
		}
		return state.Save(value)
	}
	t.Cleanup(func() { saveMoveState = previousSave })

	_, err = Move("my-app", "renamed", "")
	require.Error(t, err)
	require.NoDirExists(t, filepath.Join(centralPath, "projects", "my-app"))
	require.DirExists(t, filepath.Join(centralPath, "projects", "renamed"))

	result, err := Move("my-app", "renamed", "")
	require.NoError(t, err)
	require.Equal(t, "renamed", result.ProjectName)
	loaded, _, err := state.Load()
	require.NoError(t, err)
	_, oldExists := loaded.ProjectEntryFor("my-app")
	entry, newExists := loaded.ProjectEntryFor("renamed")
	require.False(t, oldExists)
	require.True(t, newExists)
	require.Equal(t, result.Commit, entry.Checkouts[0].SyncBase)
}

func TestMoveRetryRefusesCentralChangesAfterCommittedRename(t *testing.T) {
	centralPath, projectRoot := addSandbox(t)
	_, err := Add(AddOptions{ProjectPath: projectRoot})
	require.NoError(t, err)
	previousSave := saveMoveState
	saveMoveState = func(state.State) error { return errors.New("injected state failure") }
	t.Cleanup(func() { saveMoveState = previousSave })

	_, err = Move("my-app", "renamed", "")
	require.Error(t, err)
	saveMoveState = previousSave
	centralFile := filepath.Join(centralPath, "projects", "renamed", ".claude", "CLAUDE.md")
	require.NoError(t, os.WriteFile(centralFile, []byte("later central change\n"), 0o644))
	_, err = git.Run(centralPath, "add", "-A")
	require.NoError(t, err)
	_, err = git.Run(centralPath, "commit", "-m", "test: advance renamed project")
	require.NoError(t, err)

	_, err = Move("my-app", "renamed", "")
	require.Error(t, err)
	require.Equal(t, lerr.CodeRefusedSafety, lerr.CodeOf(err))
	loaded, _, err := state.Load()
	require.NoError(t, err)
	_, oldExists := loaded.ProjectEntryFor("my-app")
	require.True(t, oldExists)
}

func TestMoveRetryRefusesUnrelatedCentralProjectWhenCopyIsMissing(t *testing.T) {
	centralPath, projectRoot := addSandbox(t)
	_, err := Add(AddOptions{ProjectPath: projectRoot})
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(filepath.Join(centralPath, "projects", "my-app")))
	_, err = git.Run(centralPath, "add", "-A")
	require.NoError(t, err)
	_, err = git.Run(centralPath, "commit", "-m", "test: remove old project")
	require.NoError(t, err)
	unrelated := filepath.Join(centralPath, "projects", "unrelated", ".claude")
	require.NoError(t, os.MkdirAll(unrelated, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(unrelated, "CLAUDE.md"), []byte("unrelated\n"), 0o644))
	_, err = git.Run(centralPath, "add", "-A")
	require.NoError(t, err)
	_, err = git.Run(centralPath, "commit", "-m", "test: add unrelated project")
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(filepath.Join(projectRoot, ".claude")))

	_, err = Move("my-app", "unrelated", "")
	require.Error(t, err)
	require.Equal(t, lerr.CodeRefusedSafety, lerr.CodeOf(err))
	loaded, _, err := state.Load()
	require.NoError(t, err)
	_, oldExists := loaded.ProjectEntryFor("my-app")
	require.True(t, oldExists)
}

func TestMoveRefusesNewNameAlreadyRecordedBeforeMutation(t *testing.T) {
	centralPath, projectRoot := addSandbox(t)
	_, err := Add(AddOptions{ProjectPath: projectRoot})
	require.NoError(t, err)
	loaded, _, err := state.Load()
	require.NoError(t, err)
	entry, _ := loaded.ProjectEntryFor("my-app")
	require.NoError(t, loaded.AddAgentDir("reserved", ".claude"))
	require.NoError(t, loaded.RecordCheckout("reserved", filepath.Join(filepath.Dir(projectRoot), "reserved"), entry.Checkouts[0].SyncBase))
	require.NoError(t, state.Save(loaded))

	_, err = Move("my-app", "reserved", "")
	require.Error(t, err)
	require.Equal(t, lerr.CodeConflict, lerr.CodeOf(err))
	require.DirExists(t, filepath.Join(centralPath, "projects", "my-app"))
	require.NoDirExists(t, filepath.Join(centralPath, "projects", "reserved"))
}

func TestMovePathRefusesCheckoutOwnedByAnotherProjectBeforeMutation(t *testing.T) {
	centralPath, projectRoot := addSandbox(t)
	_, err := Add(AddOptions{ProjectPath: projectRoot})
	require.NoError(t, err)
	otherRoot := filepath.Join(filepath.Dir(projectRoot), "other")
	require.NoError(t, os.MkdirAll(otherRoot, 0o755))
	otherRoot, err = filepath.EvalSymlinks(otherRoot)
	require.NoError(t, err)
	loaded, _, err := state.Load()
	require.NoError(t, err)
	entry, _ := loaded.ProjectEntryFor("my-app")
	require.NoError(t, loaded.AddAgentDir("other", ".claude"))
	require.NoError(t, loaded.RecordCheckout("other", otherRoot, entry.Checkouts[0].SyncBase))
	require.NoError(t, state.Save(loaded))

	_, err = Move("my-app", "renamed", otherRoot)
	require.Error(t, err)
	require.Equal(t, lerr.CodeConflict, lerr.CodeOf(err))
	require.DirExists(t, filepath.Join(centralPath, "projects", "my-app"))
	require.NoDirExists(t, filepath.Join(centralPath, "projects", "renamed"))
}

func TestMoveAllowsWhollyMissingCheckout(t *testing.T) {
	centralPath, projectRoot := addSandbox(t)
	_, err := Add(AddOptions{ProjectPath: projectRoot})
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(filepath.Join(projectRoot, ".claude")))

	_, err = Move("my-app", "renamed", "")
	require.NoError(t, err)
	require.DirExists(t, filepath.Join(centralPath, "projects", "renamed"))
}

func TestRenderMvTemplateBlankUsesDefault(t *testing.T) {
	for _, template := range []string{"", "   ", "\t\n"} {
		require.Equal(t, "refactor: rename old to new", renderMvTemplate(template, "old", "new"))
	}
}

func TestMovePathUpdatesTheOnlyMissingCheckout(t *testing.T) {
	centralPath, projectRoot := addSandbox(t)
	_, err := Add(AddOptions{ProjectPath: projectRoot})
	require.NoError(t, err)
	secondRoot := filepath.Join(filepath.Dir(projectRoot), "my-app-worktree")
	require.NoError(t, os.MkdirAll(secondRoot, 0o755))
	require.NoError(t, reconcile.CopyTree(filepath.Join(projectRoot, ".claude"), filepath.Join(secondRoot, ".claude")))
	loaded, _, err := state.Load()
	require.NoError(t, err)
	entry, _ := loaded.ProjectEntryFor("my-app")
	require.NoError(t, loaded.RecordCheckout("my-app", secondRoot, entry.Checkouts[0].SyncBase))
	require.NoError(t, state.Save(loaded))
	newRoot := filepath.Join(filepath.Dir(projectRoot), "renamed-on-disk")
	require.NoError(t, os.Rename(projectRoot, newRoot))
	canonicalNewRoot, err := filepath.EvalSymlinks(newRoot)
	require.NoError(t, err)

	result, err := Move("my-app", "renamed", newRoot)
	require.NoError(t, err)
	require.Len(t, result.Checkouts, 2)
	loaded, _, err = state.Load()
	require.NoError(t, err)
	renamed, exists := loaded.ProjectEntryFor("renamed")
	require.True(t, exists)
	require.ElementsMatch(t, []string{canonicalNewRoot, secondRoot}, []string{renamed.Checkouts[0].Path, renamed.Checkouts[1].Path})
	require.DirExists(t, filepath.Join(centralPath, "projects", "renamed"))
}
