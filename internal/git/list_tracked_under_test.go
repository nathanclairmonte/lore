package git

import (
	"os"
	"path/filepath"
	"testing"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/stretchr/testify/require"
)

func TestListTrackedUnderEmpty(t *testing.T) {
	dir := initRepo(t)
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".claude"), 0o755))

	// nothing staged or committed yet under .claude/.
	got, err := ListTrackedUnder(dir, ".claude")
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestListTrackedUnderUntrackedFile(t *testing.T) {
	dir := initRepo(t)
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".claude", "CLAUDE.md"),
		[]byte("ctx"), 0o644))

	// file on disk but never `git add`'d. ls-files must NOT list it.
	got, err := ListTrackedUnder(dir, ".claude")
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestListTrackedUnderTrackedFile(t *testing.T) {
	dir := initRepo(t)
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".claude", "CLAUDE.md"),
		[]byte("ctx"), 0o644))
	_, err := Run(dir, "add", ".claude/CLAUDE.md")
	require.NoError(t, err)

	// staged-but-not-committed counts as tracked for the safety gate
	// (the file is in the index; moving the dir would still corrupt
	// git's view of the project).
	got, err := ListTrackedUnder(dir, ".claude")
	require.NoError(t, err)
	require.Equal(t, []string{".claude/CLAUDE.md"}, got)
}

func TestListTrackedUnderMultipleTrackedFiles(t *testing.T) {
	dir := initRepo(t)
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".claude", "a.md"), []byte("a"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".claude", "b.md"), []byte("b"), 0o644))
	_, err := Run(dir, "add", ".claude")
	require.NoError(t, err)

	got, err := ListTrackedUnder(dir, ".claude")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{".claude/a.md", ".claude/b.md"}, got)
}

func TestListTrackedUnderNonGitDirectory(t *testing.T) {
	dir := t.TempDir() // no `git init`
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".claude"), 0o755))

	// a non-git project trivially has nothing tracked. mirrors
	// CheckIgnore's "fold exit 128 into a normal answer" convention so
	// the safety gate doesn't surface a confusing CodeGitNetwork.
	got, err := ListTrackedUnder(dir, ".claude")
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestListTrackedUnderMissingGitBinary(t *testing.T) {
	t.Setenv("PATH", "")
	_, err := ListTrackedUnder("", ".claude")
	require.Error(t, err)
	require.Equal(t, lerr.CodeSetupMissing, lerr.CodeOf(err))
}
