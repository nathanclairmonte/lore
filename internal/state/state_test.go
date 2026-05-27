package state

import (
	"os"
	"path/filepath"
	"testing"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/stretchr/testify/require"
)

func withSandbox(t *testing.T) (home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local/state"))
	return home
}

func TestPathHonorsXDG(t *testing.T) {
	home := withSandbox(t)
	p, err := Path()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(home, ".local/state", "lore", "state.toml"), p)
}

func TestPathFallsBackToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "")
	p, err := Path()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(home, ".local/state/lore/state.toml"), p)
}

func TestLoadReturnsDefaultsWhenAbsent(t *testing.T) {
	withSandbox(t)
	st, exists, err := Load()
	require.NoError(t, err)
	require.False(t, exists)
	require.Equal(t, CurrentSchemaVersion, st.SchemaVersion)
	require.NotNil(t, st.Projects)
	require.Len(t, st.Projects, 0)
}

func TestRoundTrip(t *testing.T) {
	withSandbox(t)
	st := Defaults()
	require.NoError(t, st.AddAgentDir("my-app", "/home/x/code/my-app", ".claude"))
	require.NoError(t, st.AddAgentDir("my-app", "/home/x/code/my-app", ".cursor"))
	require.NoError(t, st.AddAgentDir("other", "/tmp/other", ".claude"))

	require.NoError(t, Save(st))

	got, exists, err := Load()
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, st, got)
}

func TestLoadMalformed(t *testing.T) {
	withSandbox(t)
	path, _ := Path()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("not = valid = toml"), 0o644))

	_, _, err := Load()
	require.Error(t, err)
}

func TestSchemaVersionPreserved(t *testing.T) {
	withSandbox(t)
	st := Defaults()
	require.NoError(t, Save(st))

	got, _, err := Load()
	require.NoError(t, err)
	require.Equal(t, CurrentSchemaVersion, got.SchemaVersion)
}

func TestAddAgentDirNew(t *testing.T) {
	st := Defaults()
	require.NoError(t, st.AddAgentDir("my-app", "/home/x/code/my-app", ".claude"))
	entry, ok := st.ProjectEntryFor("my-app")
	require.True(t, ok)
	require.Equal(t, "/home/x/code/my-app", entry.Path)
	require.Equal(t, []string{".claude"}, entry.AgentDirs)
}

func TestAddAgentDirIdempotent(t *testing.T) {
	st := Defaults()
	require.NoError(t, st.AddAgentDir("my-app", "/home/x/code/my-app", ".claude"))
	// same (name, path, dir) is a no-op; orchestrator's idempotent re-run
	// path leans on this.
	require.NoError(t, st.AddAgentDir("my-app", "/home/x/code/my-app", ".claude"))
	entry, _ := st.ProjectEntryFor("my-app")
	require.Equal(t, []string{".claude"}, entry.AgentDirs)
}

func TestAddAgentDirAppendsSecondDir(t *testing.T) {
	st := Defaults()
	require.NoError(t, st.AddAgentDir("my-app", "/home/x/code/my-app", ".claude"))
	require.NoError(t, st.AddAgentDir("my-app", "/home/x/code/my-app", ".cursor"))
	entry, _ := st.ProjectEntryFor("my-app")
	require.Equal(t, []string{".claude", ".cursor"}, entry.AgentDirs,
		"second dir should append, preserving insertion order")
}

func TestAddAgentDirConflictsOnDifferentPath(t *testing.T) {
	st := Defaults()
	require.NoError(t, st.AddAgentDir("my-app", "/home/x/code/my-app", ".claude"))

	err := st.AddAgentDir("my-app", "/tmp/other-location", ".claude")
	require.Error(t, err)
	require.Equal(t, lerr.CodeConflict, lerr.CodeOf(err))
	// existing entry must NOT have been overwritten.
	entry, _ := st.ProjectEntryFor("my-app")
	require.Equal(t, "/home/x/code/my-app", entry.Path)
}

func TestProjectEntryForMissing(t *testing.T) {
	st := Defaults()
	_, ok := st.ProjectEntryFor("never-added")
	require.False(t, ok)
}

func TestProjectEntryForNilMap(t *testing.T) {
	// safety: a State with no Projects map shouldn't panic on a lookup.
	st := State{SchemaVersion: CurrentSchemaVersion}
	_, ok := st.ProjectEntryFor("anything")
	require.False(t, ok)
}

func TestPathForWrapsProjectEntry(t *testing.T) {
	st := Defaults()
	require.NoError(t, st.AddAgentDir("my-app", "/home/x/code/my-app", ".claude"))
	got, ok := st.PathFor("my-app")
	require.True(t, ok)
	require.Equal(t, "/home/x/code/my-app", got)
}

func TestRemoveAgentDirDropsOne(t *testing.T) {
	st := Defaults()
	require.NoError(t, st.AddAgentDir("my-app", "/home/x/code/my-app", ".claude"))
	require.NoError(t, st.AddAgentDir("my-app", "/home/x/code/my-app", ".cursor"))
	st.RemoveAgentDir("my-app", ".claude")
	entry, ok := st.ProjectEntryFor("my-app")
	require.True(t, ok)
	require.Equal(t, []string{".cursor"}, entry.AgentDirs)
}

func TestRemoveAgentDirCollapsesProjectWhenLast(t *testing.T) {
	st := Defaults()
	require.NoError(t, st.AddAgentDir("my-app", "/home/x/code/my-app", ".claude"))
	st.RemoveAgentDir("my-app", ".claude")
	_, ok := st.ProjectEntryFor("my-app")
	require.False(t, ok, "project with no remaining dirs should be removed entirely")
}

func TestRemoveAgentDirNoOp(t *testing.T) {
	st := Defaults()
	// removing from an untracked project, and removing an untracked dir,
	// are both fine; rollback path may call this without first checking.
	st.RemoveAgentDir("never-added", ".claude")
	require.NoError(t, st.AddAgentDir("my-app", "/home/x/code/my-app", ".claude"))
	st.RemoveAgentDir("my-app", ".never-added")
	entry, _ := st.ProjectEntryFor("my-app")
	require.Equal(t, []string{".claude"}, entry.AgentDirs)
}

func TestAddAgentDirRejectsEmptyName(t *testing.T) {
	st := Defaults()
	err := st.AddAgentDir("", "/home/x/code/my-app", ".claude")
	require.Error(t, err)
	require.Equal(t, lerr.CodeGenericError, lerr.CodeOf(err))
}

func TestAddAgentDirRejectsEmptyPath(t *testing.T) {
	st := Defaults()
	err := st.AddAgentDir("my-app", "", ".claude")
	require.Error(t, err)
	require.Equal(t, lerr.CodeGenericError, lerr.CodeOf(err))
}

func TestAddAgentDirRejectsRelativePath(t *testing.T) {
	st := Defaults()
	err := st.AddAgentDir("my-app", "code/my-app", ".claude")
	require.Error(t, err)
	require.Equal(t, lerr.CodeGenericError, lerr.CodeOf(err))
	// no half-written entry left behind on rejection.
	_, ok := st.ProjectEntryFor("my-app")
	require.False(t, ok)
}

func TestAddAgentDirRejectsEmptyDirName(t *testing.T) {
	st := Defaults()
	err := st.AddAgentDir("my-app", "/home/x/code/my-app", "")
	require.Error(t, err)
	require.Equal(t, lerr.CodeGenericError, lerr.CodeOf(err))
}

func TestLoadRejectsOldSchemaVersion(t *testing.T) {
	withSandbox(t)
	// any non-current schema is a hard error; lore is pre-1.0 and
	// migration code is intentionally not carried.
	path, _ := Path()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	v1 := `schema_version = 1

[paths]
my-app = "/home/x/code/my-app"
`
	require.NoError(t, os.WriteFile(path, []byte(v1), 0o644))

	_, _, err := Load()
	require.Error(t, err)
	require.Equal(t, lerr.CodeInconsistency, lerr.CodeOf(err))
	require.Contains(t, err.Error(), "schema version 1")
}

func TestLoadRejectsZeroSchemaVersion(t *testing.T) {
	withSandbox(t)
	// a state file with no schema_version field at all decodes to 0;
	// reject it the same way as an old version so a hand-edited or
	// corrupted file doesn't silently become "valid empty state".
	path, _ := Path()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("[projects]\n"), 0o644))

	_, _, err := Load()
	require.Error(t, err)
	require.Equal(t, lerr.CodeInconsistency, lerr.CodeOf(err))
}

func TestRemoveAgentDirDoesNotCorruptCallerSliceCopy(t *testing.T) {
	// regression guard for the [:0] aliasing bug: a caller holding a
	// ProjectEntry copy must not see its AgentDirs slice mutated when
	// the state's copy is filtered.
	st := Defaults()
	require.NoError(t, st.AddAgentDir("my-app", "/home/x/code/my-app", ".claude"))
	require.NoError(t, st.AddAgentDir("my-app", "/home/x/code/my-app", ".cursor"))

	snapshot, _ := st.ProjectEntryFor("my-app")
	st.RemoveAgentDir("my-app", ".claude")
	require.Equal(t, []string{".claude", ".cursor"}, snapshot.AgentDirs,
		"caller's snapshot must be untouched by a later RemoveAgentDir")
}

func TestSaveAtomicityPreservesExistingFileOnEncodeFailure(t *testing.T) {
	withSandbox(t)

	// land a known-good state on disk first.
	good := Defaults()
	require.NoError(t, good.AddAgentDir("my-app", "/home/x/code/my-app", ".claude"))
	require.NoError(t, Save(good))

	// simulate a write failure mid-Save: make the state directory
	// read-only so creating the tempfile fails. atomicity contract:
	// the previous state.toml must still be intact and loadable.
	path, _ := Path()
	stateDir := filepath.Dir(path)
	require.NoError(t, os.Chmod(stateDir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(stateDir, 0o755) })

	next := Defaults()
	require.NoError(t, next.AddAgentDir("my-app", "/home/x/code/my-app", ".claude"))
	require.NoError(t, next.AddAgentDir("second", "/home/x/code/second", ".claude"))
	err := Save(next)
	require.Error(t, err)

	// restore perms so Load can read the file, then confirm the
	// pre-existing state survived untouched.
	require.NoError(t, os.Chmod(stateDir, 0o755))
	loaded, exists, err := Load()
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, good, loaded, "previous state file must survive a failed Save")
}

func TestSaveLeavesNoTempFilesBehind(t *testing.T) {
	withSandbox(t)
	st := Defaults()
	require.NoError(t, st.AddAgentDir("my-app", "/home/x/code/my-app", ".claude"))
	require.NoError(t, Save(st))

	path, _ := Path()
	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	for _, e := range entries {
		require.NotContains(t, e.Name(), ".tmp",
			"atomic Save must clean up its tempfile after a successful rename")
	}
}

func TestAddAgentDirSurvivesRoundTrip(t *testing.T) {
	withSandbox(t)
	st := Defaults()
	require.NoError(t, st.AddAgentDir("my-app", "/home/x/code/my-app", ".claude"))
	require.NoError(t, Save(st))

	loaded, exists, err := Load()
	require.NoError(t, err)
	require.True(t, exists)
	entry, ok := loaded.ProjectEntryFor("my-app")
	require.True(t, ok)
	require.Equal(t, "/home/x/code/my-app", entry.Path)
	require.Equal(t, []string{".claude"}, entry.AgentDirs)
}
