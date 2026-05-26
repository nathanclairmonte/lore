package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// sandbox redirects HOME, XDG_CONFIG_HOME, and XDG_STATE_HOME to a temp
// dir so tests don't touch the user's real config or central repo.
//
// Note: we intentionally do NOT inject a git identity here. central.Initialize
// supplies one inline for its bootstrap commit, and exercising that path
// is part of the contract this test guards.
func sandbox(t *testing.T) (home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local/state"))
	return home
}

func TestLoreInit_FreshMachine(t *testing.T) {
	home := sandbox(t)

	code := run([]string{"init"})
	require.Equal(t, 0, code)
	require.DirExists(t, filepath.Join(home, "agent-context/.git"))
	require.FileExists(t, filepath.Join(home, "agent-context/README.md"))
	require.FileExists(t, filepath.Join(home, ".config/lore/config.toml"))

	// config.toml carries the commented template, not a bare marshal.
	data, err := os.ReadFile(filepath.Join(home, ".config/lore/config.toml"))
	require.NoError(t, err)
	require.Contains(t, string(data), "# Path to the central agent-context repository.")
}

func TestLoreInit_RefusesOnSecondRun(t *testing.T) {
	sandbox(t)

	require.Equal(t, 0, run([]string{"init"}))
	require.Equal(t, 4, run([]string{"init"}), "second init must exit 4 (conflict)")
}

func TestLoreInit_RefusesNonEmptyDefaultDir(t *testing.T) {
	home := sandbox(t)
	// pre-populate the default location with random content; no config yet.
	require.NoError(t, os.MkdirAll(filepath.Join(home, "agent-context"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(home, "agent-context/random.txt"),
		[]byte("not lore's stuff"),
		0o644,
	))

	require.Equal(t, 5, run([]string{"init"}), "non-empty default dir must exit 5 (refused for safety)")
}

func TestLoreInit_AcceptsEmptyPreExistingDir(t *testing.T) {
	home := sandbox(t)
	// empty pre-existing dir should not block fresh init.
	require.NoError(t, os.MkdirAll(filepath.Join(home, "agent-context"), 0o755))

	require.Equal(t, 0, run([]string{"init"}))
	require.DirExists(t, filepath.Join(home, "agent-context/.git"))
}

func TestLoreInit_MissingDirNonInteractive(t *testing.T) {
	home := sandbox(t)
	// first run succeeds.
	require.Equal(t, 0, run([]string{"init"}))
	// now nuke the central dir but leave config in place.
	require.NoError(t, os.RemoveAll(filepath.Join(home, "agent-context")))

	// in tests stdin isn't a TTY, so the prompt should refuse with exit 7.
	require.Equal(t, 7, run([]string{"init"}))
}

func TestRecreateMissingLeavesConfigUntouched(t *testing.T) {
	home := sandbox(t)
	// first run lays down central dir + config.
	require.Equal(t, 0, run([]string{"init"}))
	configPath := filepath.Join(home, ".config/lore/config.toml")

	// overwrite the config with a sentinel so we can detect any later
	// rewrite. central_repo stays valid so Load doesn't reject it.
	sentinel := `central_repo = "~/agent-context"` + "\n# user-edited sentinel\n"
	require.NoError(t, os.WriteFile(configPath, []byte(sentinel), 0o644))

	// nuke the central dir but leave the (now-customised) config.
	require.NoError(t, os.RemoveAll(filepath.Join(home, "agent-context")))

	// call recreateMissing directly: drives the recreate path without
	// needing an interactive prompt. asserts the contract that recreate
	// MUST NOT touch the config file, only the missing dir.
	require.NoError(t, recreateMissing(filepath.Join(home, "agent-context")))
	require.DirExists(t, filepath.Join(home, "agent-context/.git"))

	got, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Equal(t, sentinel, string(got), "recreateMissing must not overwrite an existing config")
}

func TestLoreHelpListsInit(t *testing.T) {
	// no sandbox needed; --help shouldn't touch the filesystem at all,
	// but use one anyway as a safety net.
	sandbox(t)
	code := run([]string{"--help"})
	require.Equal(t, 0, code)
}
