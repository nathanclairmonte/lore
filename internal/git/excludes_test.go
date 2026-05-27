package git

import (
	"os"
	"path/filepath"
	"testing"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/stretchr/testify/require"
)

// isolateGitEnv points HOME and XDG_CONFIG_HOME at temp dirs and clears
// ~/.gitconfig so the developer's actual git config doesn't leak into
// these tests. Returns the temp home for tests that need to plant a
// .gitconfig.
func isolateGitEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	// explicit empty XDG so the helper's "XDG set" branch needs an
	// explicit Setenv. avoids leaking the developer's XDG_CONFIG_HOME.
	t.Setenv("XDG_CONFIG_HOME", "")
	// belt-and-suspenders: also clear the env vars git itself reads so
	// the test environment is hermetic regardless of developer setup.
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	return home
}

func TestGlobalExcludesPathFromCoreExcludesFile(t *testing.T) {
	home := isolateGitEnv(t)
	// plant a global gitconfig that sets core.excludesFile to a known
	// path. simulates a user who has explicitly configured git.
	configPath := filepath.Join(home, ".gitconfig")
	customExcludes := filepath.Join(home, "my-custom-ignore")
	require.NoError(t, os.WriteFile(configPath,
		[]byte("[core]\n\texcludesFile = "+customExcludes+"\n"), 0o644))

	got, err := GlobalExcludesPath()
	require.NoError(t, err)
	require.Equal(t, customExcludes, got)
}

func TestGlobalExcludesPathExpandsTilde(t *testing.T) {
	home := isolateGitEnv(t)
	configPath := filepath.Join(home, ".gitconfig")
	require.NoError(t, os.WriteFile(configPath,
		[]byte("[core]\n\texcludesFile = ~/my-ignore\n"), 0o644))

	got, err := GlobalExcludesPath()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(home, "my-ignore"), got)
}

func TestGlobalExcludesPathFallsBackToXDG(t *testing.T) {
	isolateGitEnv(t)
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)

	got, err := GlobalExcludesPath()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(xdg, "git", "ignore"), got)
}

func TestGlobalExcludesPathFallsBackToHomeDefault(t *testing.T) {
	home := isolateGitEnv(t)
	// neither core.excludesFile nor XDG_CONFIG_HOME set; should land on
	// ~/.config/git/ignore as git's documented default.
	got, err := GlobalExcludesPath()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(home, ".config", "git", "ignore"), got)
}

func TestRepoToplevelInNormalRepo(t *testing.T) {
	isolateGitEnv(t)
	dir := t.TempDir()
	require.NoError(t, Init(dir))
	sub := filepath.Join(dir, "services", "foo")
	require.NoError(t, os.MkdirAll(sub, 0o755))

	// invoking from a subdir must still return the worktree root: the
	// slice 4 picker's TargetRepoGitignore option leans on this so a
	// monorepo subdir add doesn't scatter .gitignore files.
	got, err := RepoToplevel(sub)
	require.NoError(t, err)
	// EvalSymlinks because t.TempDir() can return a symlinked path on
	// macOS (/var/folders/... -> /private/var/folders/...) and git
	// rev-parse always reports the resolved path.
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	require.Equal(t, resolved, got)
}

func TestRepoToplevelRefusesOutsideGitRepo(t *testing.T) {
	isolateGitEnv(t)
	dir := t.TempDir() // no git init

	_, err := RepoToplevel(dir)
	require.Error(t, err)
	// CodeRefusedSafety, not CodeGitNetwork: this is the slice 4 gate
	// that keeps non-git projects from reaching the picker at all.
	// CodeGitNetwork would (mis)class a setup problem as a transient
	// network failure.
	require.Equal(t, lerr.CodeRefusedSafety, lerr.CodeOf(err))
	require.Contains(t, err.Error(), "not inside a git repository")
}

func TestRepoExcludePathInNormalRepo(t *testing.T) {
	isolateGitEnv(t)
	dir := t.TempDir()
	require.NoError(t, Init(dir))

	got, err := RepoExcludePath(dir)
	require.NoError(t, err)
	// inline repo: .git is a directory at <dir>/.git, so info/exclude
	// lives at <dir>/.git/info/exclude.
	require.Equal(t, filepath.Join(dir, ".git", "info", "exclude"), got)
}

func TestAppendIgnoreEntryCreatesMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "subdir", "ignore")

	require.NoError(t, AppendIgnoreEntry(path, ".claude/"))

	// parent dirs created, file created, content is exactly the entry
	// plus one trailing newline (no leading newline since the file was
	// empty before).
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, ".claude/\n", string(got))
}

func TestAppendIgnoreEntryPreservesExistingContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ignore")
	require.NoError(t, os.WriteFile(path, []byte("node_modules/\n"), 0o644))

	require.NoError(t, AppendIgnoreEntry(path, ".claude/"))

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "node_modules/\n.claude/\n", string(got))
}

func TestAppendIgnoreEntryAddsMissingTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ignore")
	// existing file without a trailing newline. naive append would
	// produce "node_modules/.claude/" on one line; AppendIgnoreEntry
	// must insert a separator.
	require.NoError(t, os.WriteFile(path, []byte("node_modules/"), 0o644))

	require.NoError(t, AppendIgnoreEntry(path, ".claude/"))

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "node_modules/\n.claude/\n", string(got))
}
