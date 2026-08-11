package project

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nathanclairmonte/lore/internal/state"
	"github.com/stretchr/testify/require"
)

func TestRecoverProjectReplacementCompletesPublishedTransaction(t *testing.T) {
	_, projectRoot := addSandbox(t)
	st := state.Defaults()
	require.NoError(t, st.AddAgentDir("my-app", ".claude"))
	oldBase := "0123456789abcdef0123456789abcdef01234567"
	newBase := "89abcdef0123456789abcdef0123456789abcdef"
	require.NoError(t, st.RecordCheckout("my-app", projectRoot, oldBase))
	require.NoError(t, state.Save(st))

	destination := filepath.Join(projectRoot, ".claude")
	backup := filepath.Join(projectRoot, ".lore-backup-test")
	require.NoError(t, os.Rename(destination, backup))
	require.NoError(t, os.Mkdir(destination, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(destination, "CLAUDE.md"), []byte("new"), 0o644))
	journal, err := projectReplacementPath()
	require.NoError(t, err)
	transaction := projectReplacementTransaction{path: journal, record: projectReplacementRecord{
		Version: projectReplacementVersion, Project: "my-app", Checkout: projectRoot,
		SyncBase: newBase, AgentDirs: []string{".claude"},
		Items: []projectReplacementItem{{
			Destination: destination, Staged: filepath.Join(projectRoot, ".lore-stage-test"),
			Backup: backup, HadOriginal: true,
		}},
	}}
	require.NoError(t, transaction.save())

	require.NoError(t, recoverProjectReplacement(&st))
	data, err := os.ReadFile(filepath.Join(destination, "CLAUDE.md"))
	require.NoError(t, err)
	require.Equal(t, "new", string(data))
	require.NoDirExists(t, backup)
	require.NoFileExists(t, journal)
	checkout, ok := st.CheckoutForPath("my-app", projectRoot)
	require.True(t, ok)
	require.Equal(t, newBase, checkout.SyncBase)
}

func TestRecoverProjectReplacementRollsBackIncompleteTransaction(t *testing.T) {
	_, projectRoot := addSandbox(t)
	st := state.Defaults()
	destination := filepath.Join(projectRoot, ".claude")
	backup := filepath.Join(projectRoot, ".lore-backup-test")
	staged := filepath.Join(projectRoot, ".lore-stage-test")
	require.NoError(t, os.Rename(destination, backup))
	require.NoError(t, os.Mkdir(staged, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(staged, "CLAUDE.md"), []byte("new"), 0o644))
	journal, err := projectReplacementPath()
	require.NoError(t, err)
	transaction := projectReplacementTransaction{path: journal, record: projectReplacementRecord{
		Version: projectReplacementVersion, Project: "my-app", Checkout: projectRoot,
		SyncBase: "89abcdef0123456789abcdef0123456789abcdef", AgentDirs: []string{".claude"},
		Items: []projectReplacementItem{{
			Destination: destination, Staged: staged, Backup: backup, HadOriginal: true,
		}},
	}}
	require.NoError(t, transaction.save())

	require.NoError(t, recoverProjectReplacement(&st))
	data, err := os.ReadFile(filepath.Join(destination, "CLAUDE.md"))
	require.NoError(t, err)
	require.Equal(t, "context", string(data))
	require.NoDirExists(t, staged)
	require.NoFileExists(t, journal)
}

func TestRecoverProjectReplacementRestoresRecordedBackupBeforeHadOriginalSave(t *testing.T) {
	_, projectRoot := addSandbox(t)
	st := state.Defaults()
	destination := filepath.Join(projectRoot, ".claude")
	backup := filepath.Join(projectRoot, ".lore-backup-test")
	staged := filepath.Join(projectRoot, ".lore-stage-test")
	require.NoError(t, os.Rename(destination, backup))
	require.NoError(t, os.Mkdir(staged, 0o755))
	journal, err := projectReplacementPath()
	require.NoError(t, err)
	transaction := projectReplacementTransaction{path: journal, record: projectReplacementRecord{
		Version: projectReplacementVersion, Project: "my-app", Checkout: projectRoot,
		SyncBase: "89abcdef0123456789abcdef0123456789abcdef", AgentDirs: []string{".claude"},
		Items: []projectReplacementItem{{Destination: destination, Staged: staged, Backup: backup}},
	}}
	require.NoError(t, transaction.save())

	require.NoError(t, recoverProjectReplacement(&st))
	data, err := os.ReadFile(filepath.Join(destination, "CLAUDE.md"))
	require.NoError(t, err)
	require.Equal(t, "context", string(data))
	require.NoFileExists(t, journal)
}

func TestProjectReplacementRollbackKeepsJournalWhenRestoreFails(t *testing.T) {
	_, projectRoot := addSandbox(t)
	destination := filepath.Join(projectRoot, ".claude")
	backup := filepath.Join(projectRoot, ".lore-backup-test")
	staged := filepath.Join(projectRoot, ".lore-stage-test")
	require.NoError(t, os.Mkdir(backup, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(backup, "original"), []byte("content"), 0o644))
	require.NoError(t, os.Mkdir(staged, 0o755))
	journal, err := projectReplacementPath()
	require.NoError(t, err)
	transaction := projectReplacementTransaction{path: journal, record: projectReplacementRecord{
		Version: projectReplacementVersion, Project: "my-app", Checkout: projectRoot,
		SyncBase: "89abcdef0123456789abcdef0123456789abcdef", AgentDirs: []string{".claude"},
		Items: []projectReplacementItem{{Destination: destination, Staged: staged, Backup: backup, HadOriginal: true}},
	}}
	require.NoError(t, transaction.save())

	err = transaction.rollback()
	require.Error(t, err)
	require.ErrorContains(t, err, backup)
	require.FileExists(t, journal)
	require.DirExists(t, backup)
	require.DirExists(t, staged)
}
