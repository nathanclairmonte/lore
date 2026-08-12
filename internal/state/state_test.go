package state

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const testSHA = "0123456789abcdef0123456789abcdef01234567"

func stateSandbox(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", root)
	return root
}

func validState(t *testing.T) State {
	t.Helper()
	st := Defaults()
	require.NoError(t, st.AddAgentDir("my-app", ".claude"))
	require.NoError(t, st.RecordCheckout("my-app", "/home/x/code/my-app", testSHA))
	return st
}

func TestDefaultsUsesSchemaV3(t *testing.T) {
	st := Defaults()
	require.Equal(t, 3, st.SchemaVersion)
	require.Empty(t, st.Projects)
}

func TestSaveLoadRoundTripWithMultipleCheckouts(t *testing.T) {
	stateSandbox(t)
	st := validState(t)
	require.NoError(t, st.AddAgentDir("my-app", ".cursor"))
	require.NoError(t, st.RecordCheckout("my-app", "/worktrees/my-app", "abcdef0123456789abcdef0123456789abcdef01"))
	require.NoError(t, Save(st))

	loaded, exists, err := Load()
	require.NoError(t, err)
	require.True(t, exists)
	entry, ok := loaded.ProjectEntryFor("my-app")
	require.True(t, ok)
	require.Equal(t, []string{".claude", ".cursor"}, entry.AgentDirs)
	require.Equal(t, []CheckoutEntry{
		{Path: "/home/x/code/my-app", SyncBase: testSHA},
		{Path: "/worktrees/my-app", SyncBase: "abcdef0123456789abcdef0123456789abcdef01"},
	}, entry.Checkouts)
}

func TestLoadRejectsSchemaV2(t *testing.T) {
	root := stateSandbox(t)
	path := filepath.Join(root, stateFileRelPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(`schema_version = 2

[projects.my-app]
path = "/home/x/code/my-app"
agent_dirs = [".claude"]
`), 0o644))

	_, _, err := Load()
	require.Error(t, err)
	require.Contains(t, err.Error(), "schema version 2")
	require.Contains(t, err.Error(), "expects 3")
}

func TestLoadRejectsMissingSyncBase(t *testing.T) {
	root := stateSandbox(t)
	path := filepath.Join(root, stateFileRelPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(`schema_version = 3

[[projects.my-app.checkouts]]
path = "/home/x/code/my-app"
sync_base = ""

[projects.my-app]
agent_dirs = [".claude"]
`), 0o644))

	_, _, err := Load()
	require.Error(t, err)
	require.Contains(t, err.Error(), "sync-base")
}

func TestRecordCheckoutUpdatesOnePathOnly(t *testing.T) {
	st := validState(t)
	updated := "fedcba9876543210fedcba9876543210fedcba98"
	require.NoError(t, st.RecordCheckout("my-app", "/home/x/code/my-app", updated))
	require.Len(t, st.Projects["my-app"].Checkouts, 1)
	checkout, ok := st.CheckoutForPath("my-app", "/home/x/code/my-app")
	require.True(t, ok)
	require.Equal(t, updated, checkout.SyncBase)
}

func TestRecordCheckoutValidatesPathAndSHA(t *testing.T) {
	st := Defaults()
	require.NoError(t, st.AddAgentDir("my-app", ".claude"))
	require.Error(t, st.RecordCheckout("my-app", "relative/path", testSHA))
	require.Error(t, st.RecordCheckout("my-app", "/absolute/path", "short"))
}

func TestRecordCheckoutRejectsPathOwnedByAnotherProject(t *testing.T) {
	st := validState(t)
	require.NoError(t, st.AddAgentDir("other", ".claude"))
	err := st.RecordCheckout("other", "/home/x/code/my-app", testSHA)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already recorded for project \"my-app\"")
}

func TestLoadRejectsPathOwnedByTwoProjects(t *testing.T) {
	root := stateSandbox(t)
	path := filepath.Join(root, stateFileRelPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(`schema_version = 3

[projects.first]
agent_dirs = [".claude"]

[[projects.first.checkouts]]
path = "/shared/project"
sync_base = "0123456789abcdef0123456789abcdef01234567"

[projects.second]
agent_dirs = [".claude"]

[[projects.second.checkouts]]
path = "/shared/project"
sync_base = "0123456789abcdef0123456789abcdef01234567"
`), 0o644))

	_, _, err := Load()
	require.Error(t, err)
	require.Contains(t, err.Error(), "for both")
}

func TestRemoveAgentDirKeepsCheckoutsUntilProjectIsEmpty(t *testing.T) {
	st := validState(t)
	require.NoError(t, st.AddAgentDir("my-app", ".cursor"))
	st.RemoveAgentDir("my-app", ".cursor")
	require.Equal(t, []string{".claude"}, st.Projects["my-app"].AgentDirs)
	require.Len(t, st.Projects["my-app"].Checkouts, 1)
	st.RemoveAgentDir("my-app", ".claude")
	_, ok := st.Projects["my-app"]
	require.False(t, ok)
}

func TestRemoveLastCheckoutDropsProjectEntry(t *testing.T) {
	st := validState(t)
	st.RemoveCheckout("my-app", "/home/x/code/my-app")
	_, ok := st.ProjectEntryFor("my-app")
	require.False(t, ok)
}

func TestCloneDoesNotAliasNestedSlices(t *testing.T) {
	st := validState(t)
	clone := st.Clone()
	entry := clone.Projects["my-app"]
	entry.AgentDirs[0] = ".changed"
	entry.Checkouts[0].Path = "/changed"
	clone.Projects["my-app"] = entry
	require.Equal(t, ".claude", st.Projects["my-app"].AgentDirs[0])
	require.Equal(t, "/home/x/code/my-app", st.Projects["my-app"].Checkouts[0].Path)
}

func TestSaveIsAtomic(t *testing.T) {
	stateSandbox(t)
	st := validState(t)
	require.NoError(t, Save(st))
	path, err := Path()
	require.NoError(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "schema_version = 3")
	require.Contains(t, string(data), "sync_base")
}
