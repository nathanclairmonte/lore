package project

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/state"
	"github.com/stretchr/testify/require"
)

func TestDoctorRepairRecoversAfterStateSaveFailure(t *testing.T) {
	_, projectRoot := addSandbox(t)
	_, err := Add(AddOptions{ProjectPath: projectRoot})
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(filepath.Join(projectRoot, ".claude")))

	previousSave := stateSaveDoctor
	stateSaveDoctor = func(state.State) error { return errors.New("injected state failure") }
	t.Cleanup(func() { stateSaveDoctor = previousSave })

	result, err := Doctor(true)
	require.Error(t, err)
	require.Empty(t, result.Fixed)
	require.FileExists(t, filepath.Join(projectRoot, ".claude", "CLAUDE.md"))
	journal, err := projectReplacementPath()
	require.NoError(t, err)
	require.FileExists(t, journal)

	stateSaveDoctor = previousSave
	result, err = Doctor(false)
	require.NoError(t, err)
	require.True(t, result.Healthy)
	require.NoFileExists(t, journal)
}

func TestDoctorRepairRefusesUnignoredMissingCopy(t *testing.T) {
	_, projectRoot := addSandbox(t)
	_, err := Add(AddOptions{ProjectPath: projectRoot})
	require.NoError(t, err)
	unignoreSandbox(t, projectRoot)
	require.NoError(t, os.RemoveAll(filepath.Join(projectRoot, ".claude")))

	result, err := Doctor(true)
	require.Error(t, err)
	require.Equal(t, lerr.CodeRefusedSafety, lerr.CodeOf(err))
	require.Empty(t, result.Fixed)
	require.NoDirExists(t, filepath.Join(projectRoot, ".claude"))
}

func TestListAndDoctorReportEmptyCentralProjectWithoutAborting(t *testing.T) {
	centralPath, _ := addSandbox(t)
	require.NoError(t, os.Mkdir(filepath.Join(centralPath, "projects", "empty"), 0o755))

	listed, err := List()
	require.NoError(t, err)
	require.Len(t, listed.Rows, 1)
	require.Equal(t, "empty", listed.Rows[0].Name)
	require.NotEmpty(t, listed.Rows[0].Issues)
	diagnosed, err := Doctor(false)
	require.NoError(t, err)
	require.False(t, diagnosed.Healthy)
}

func TestDoctorRepairSkipsStaleCheckoutAndContinues(t *testing.T) {
	_, projectRoot := addSandbox(t)
	_, err := Add(AddOptions{ProjectPath: projectRoot})
	require.NoError(t, err)
	loaded, _, err := state.Load()
	require.NoError(t, err)
	entry, _ := loaded.ProjectEntryFor("my-app")
	stale := filepath.Join(filepath.Dir(projectRoot), "missing-checkout")
	require.NoError(t, loaded.RecordCheckout("my-app", stale, entry.Checkouts[0].SyncBase))
	require.NoError(t, state.Save(loaded))
	require.NoError(t, os.RemoveAll(filepath.Join(projectRoot, ".claude")))

	result, err := Doctor(true)
	require.NoError(t, err)
	require.Len(t, result.Fixed, 1)
	require.FileExists(t, filepath.Join(projectRoot, ".claude", "CLAUDE.md"))
	require.False(t, result.Healthy)
}
