package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	lerrPkg "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/stretchr/testify/require"
)

// withSandbox redirects HOME and XDG_CONFIG_HOME to a temp dir so tests don't
// touch the user's real config.
func withSandbox(t *testing.T) (home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	return home
}

func TestPathHonorsXDG(t *testing.T) {
	home := withSandbox(t)
	p, err := Path()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(home, ".config", "lore", "config.toml"), p)
}

func TestPathFallsBackToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	p, err := Path()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(home, ".config", "lore", "config.toml"), p)
}

func TestDefaultsExpandsPaths(t *testing.T) {
	home := withSandbox(t)
	cfg := Defaults()
	require.Equal(t, filepath.Join(home, "agent-context"), cfg.CentralRepo)
	require.NotEmpty(t, cfg.ProjectsRoots)
	for _, root := range cfg.ProjectsRoots {
		require.False(t, strings.HasPrefix(root, "~"), "ProjectsRoots should be expanded: %q", root)
	}
	require.Equal(t, []string{".agents", ".claude", ".codex", ".cursor"}, cfg.AgentDirs)
	require.True(t, cfg.Commit.Auto)
	require.False(t, cfg.Sync.AutoPush)
}

func TestWriteDefaultsWritesCommentedTemplate(t *testing.T) {
	withSandbox(t)
	require.NoError(t, WriteDefaults())

	path, _ := Path()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "# Path to the central agent-context repository.")
	require.Contains(t, string(data), `central_repo = "~/agent-context"`)
}

func TestWriteDefaultsRefusesExisting(t *testing.T) {
	withSandbox(t)
	require.NoError(t, WriteDefaults())

	err := WriteDefaults()
	require.Error(t, err)
	require.Contains(t, err.Error(), "already exists")
}

func TestLoadReturnsDefaultsWhenAbsent(t *testing.T) {
	home := withSandbox(t)
	cfg, exists, err := Load()
	require.NoError(t, err)
	require.False(t, exists)
	require.Equal(t, filepath.Join(home, "agent-context"), cfg.CentralRepo)
}

func TestLoadAfterWriteDefaultsRoundTrips(t *testing.T) {
	withSandbox(t)
	require.NoError(t, WriteDefaults())

	cfg, exists, err := Load()
	require.NoError(t, err)
	require.True(t, exists)

	// the loaded config should match what Defaults() produces.
	require.Equal(t, Defaults(), cfg)
}

func TestLoadAppliesDefaultsForMissingKeys(t *testing.T) {
	home := withSandbox(t)
	path, _ := Path()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	// partial config: only central_repo is set.
	partial := `central_repo = "~/custom-loc"` + "\n"
	require.NoError(t, os.WriteFile(path, []byte(partial), 0o644))

	cfg, exists, err := Load()
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, filepath.Join(home, "custom-loc"), cfg.CentralRepo)
	// defaults preserved for other fields.
	require.Equal(t, []string{".agents", ".claude", ".codex", ".cursor"}, cfg.AgentDirs)
	require.True(t, cfg.Commit.Auto)
	require.Equal(t, 2, cfg.ProjectsDepth)
}

func TestSaveRoundTrip(t *testing.T) {
	home := withSandbox(t)
	cfg := Defaults()
	cfg.CentralRepo = filepath.Join(home, "elsewhere")
	cfg.Commit.AddTemplate = "custom: {project}"

	require.NoError(t, Save(cfg))

	got, exists, err := Load()
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, cfg, got)
}

func TestSaveContractsPathsBackToTilde(t *testing.T) {
	home := withSandbox(t)
	cfg := Defaults()
	cfg.CentralRepo = filepath.Join(home, "agent-context")
	require.NoError(t, Save(cfg))

	path, _ := Path()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), `central_repo = "~/agent-context"`)
}

func TestLoadMalformedReturnsError(t *testing.T) {
	withSandbox(t)
	path, _ := Path()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("this is not = valid = toml"), 0o644))

	_, _, err := Load()
	require.Error(t, err)
}

func TestLoadRejectsEmptyCentralRepo(t *testing.T) {
	withSandbox(t)
	path, _ := Path()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	// explicitly empty central_repo: user hand-edited their config into
	// a broken state. should be rejected at Load with CodeInconsistency
	// rather than flowing downstream to surface elsewhere.
	require.NoError(t, os.WriteFile(path, []byte(`central_repo = ""`+"\n"), 0o644))

	_, _, err := Load()
	require.Error(t, err)

	var le *lerrPkg.Error
	require.ErrorAs(t, err, &le)
	require.Equal(t, lerrPkg.CodeInconsistency, le.Code)
}

func TestExpand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"/abs/path", "/abs/path"},
		{"~", home},
		{"~/foo/bar", filepath.Join(home, "foo/bar")},
		{"relative/path", "relative/path"},
	}
	for _, c := range cases {
		got, err := Expand(c.in)
		require.NoError(t, err)
		require.Equal(t, c.want, got, "Expand(%q)", c.in)
	}
}

func TestContract(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{home, "~"},
		{filepath.Join(home, "foo"), "~/foo"},
		{filepath.Join(home, "foo/bar"), "~/foo/bar"},
		{"/elsewhere/path", "/elsewhere/path"},
	}
	for _, c := range cases {
		require.Equal(t, c.want, Contract(c.in), "Contract(%q)", c.in)
	}
}
