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
	require.NotNil(t, st.Paths)
	require.Len(t, st.Paths, 0)
}

func TestRoundTrip(t *testing.T) {
	withSandbox(t)
	st := Defaults()
	st.Paths["my-app"] = "/home/x/code/my-app"
	st.Paths["other"] = "/tmp/other"

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

func TestAddPathNew(t *testing.T) {
	st := Defaults()
	require.NoError(t, st.AddPath("my-app", "/home/x/code/my-app"))
	got, ok := st.PathFor("my-app")
	require.True(t, ok)
	require.Equal(t, "/home/x/code/my-app", got)
}

func TestAddPathIdempotent(t *testing.T) {
	st := Defaults()
	require.NoError(t, st.AddPath("my-app", "/home/x/code/my-app"))
	// same (name, path) is a no-op; orchestrator's idempotent re-run
	// path leans on this.
	require.NoError(t, st.AddPath("my-app", "/home/x/code/my-app"))
	require.Len(t, st.Paths, 1)
}

func TestAddPathConflictsOnDifferentPath(t *testing.T) {
	st := Defaults()
	require.NoError(t, st.AddPath("my-app", "/home/x/code/my-app"))

	err := st.AddPath("my-app", "/tmp/other-location")
	require.Error(t, err)
	require.Equal(t, lerr.CodeConflict, lerr.CodeOf(err))
	// existing mapping must NOT have been overwritten.
	got, _ := st.PathFor("my-app")
	require.Equal(t, "/home/x/code/my-app", got)
}

func TestPathForMissing(t *testing.T) {
	st := Defaults()
	_, ok := st.PathFor("never-added")
	require.False(t, ok)
}

func TestPathForNilMap(t *testing.T) {
	// safety: a State with no Paths map shouldn't panic on a lookup.
	st := State{SchemaVersion: CurrentSchemaVersion}
	_, ok := st.PathFor("anything")
	require.False(t, ok)
}

func TestRemovePath(t *testing.T) {
	st := Defaults()
	require.NoError(t, st.AddPath("my-app", "/home/x/code/my-app"))
	st.RemovePath("my-app")
	_, ok := st.PathFor("my-app")
	require.False(t, ok)
}

func TestRemovePathNoOp(t *testing.T) {
	st := Defaults()
	// removing an untracked name is fine; rollback path may call this
	// without first checking whether the add ever happened.
	st.RemovePath("never-added")
}

func TestAddPathSurvivesRoundTrip(t *testing.T) {
	withSandbox(t)
	st := Defaults()
	require.NoError(t, st.AddPath("my-app", "/home/x/code/my-app"))
	require.NoError(t, Save(st))

	loaded, exists, err := Load()
	require.NoError(t, err)
	require.True(t, exists)
	got, ok := loaded.PathFor("my-app")
	require.True(t, ok)
	require.Equal(t, "/home/x/code/my-app", got)
}
