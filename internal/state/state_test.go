package state

import (
	"os"
	"path/filepath"
	"testing"

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
