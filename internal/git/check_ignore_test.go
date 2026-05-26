package git

import (
	"os"
	"path/filepath"
	"testing"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/stretchr/testify/require"
)

// initRepo bootstraps a temp git repo and isolates the git environment
// (HOME / XDG_CONFIG_HOME) so check-ignore tests don't pick up the
// developer's global ~/.config/git/ignore or ~/.gitignore_global rules.
func initRepo(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	dir := t.TempDir()
	require.NoError(t, Init(dir))
	return dir
}

func TestCheckIgnoreMatched(t *testing.T) {
	dir := initRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"),
		[]byte(".claude/\n"), 0o644))
	// `.claude/` rule matches a directory; check-ignore needs the path
	// to exist on disk as a dir for the rule to fire. mirrors what
	// `lore add` sees at runtime (agent dir always exists when we check).
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".claude"), 0o755))

	ignored, err := CheckIgnore(dir, ".claude")
	require.NoError(t, err)
	require.True(t, ignored)
}

func TestCheckIgnoreNotMatched(t *testing.T) {
	dir := initRepo(t)
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".claude"), 0o755))
	// no .gitignore present at all.
	ignored, err := CheckIgnore(dir, ".claude")
	require.NoError(t, err, "exit 1 must surface as a normal answer, not an error")
	require.False(t, ignored)
}

func TestCheckIgnoreNonGitDirectory(t *testing.T) {
	dir := t.TempDir() // no `git init`

	// `lore add` on a non-git project must fall through to the
	// "not gitignored" branch rather than surface a CodeGitNetwork.
	// (false, nil) lets the orchestrator emit the standard
	// CodeRefusedSafety message; the user still needs a global
	// gitignore entry in case they later `git init`.
	ignored, err := CheckIgnore(dir, ".claude")
	require.NoError(t, err)
	require.False(t, ignored)
}

func TestCheckIgnoreMissingGitBinary(t *testing.T) {
	t.Setenv("PATH", "")
	_, err := CheckIgnore("", ".claude")
	require.Error(t, err)
	require.Equal(t, lerr.CodeSetupMissing, lerr.CodeOf(err))
}
