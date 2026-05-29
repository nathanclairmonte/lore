package scan

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// mkdirs creates every path (joined under base) as a directory.
func mkdirs(t *testing.T, base string, rel ...string) {
	t.Helper()
	for _, r := range rel {
		require.NoError(t, os.MkdirAll(filepath.Join(base, r), 0o755))
	}
}

func TestScanRoots_DirectChildMatch(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "my-app", "other")

	got, err := ScanRoots([]string{root}, 2, []string{"my-app"})
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Join(root, "my-app")}, got["my-app"])
	require.NotContains(t, got, "other")
}

func TestScanRoots_GrandchildMatchAtDepth2(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, filepath.Join("org", "nested-app"))

	got, err := ScanRoots([]string{root}, 2, []string{"nested-app"})
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Join(root, "org", "nested-app")}, got["nested-app"])
}

func TestScanRoots_Depth1DoesNotReachGrandchild(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, filepath.Join("org", "nested-app"))

	got, err := ScanRoots([]string{root}, 1, []string{"nested-app"})
	require.NoError(t, err)
	require.NotContains(t, got, "nested-app",
		"depth 1 should only visit direct children")
}

func TestScanRoots_MultipleMatchesAcrossRoots(t *testing.T) {
	rootA, rootB := t.TempDir(), t.TempDir()
	mkdirs(t, rootA, "api")
	mkdirs(t, rootB, "api")

	got, err := ScanRoots([]string{rootA, rootB}, 1, []string{"api"})
	require.NoError(t, err)
	require.ElementsMatch(t,
		[]string{filepath.Join(rootA, "api"), filepath.Join(rootB, "api")},
		got["api"])
}

func TestScanRoots_SkipsSymlinkedDir(t *testing.T) {
	root := t.TempDir()
	real := t.TempDir()
	require.NoError(t, os.Symlink(real, filepath.Join(root, "my-app")))

	got, err := ScanRoots([]string{root}, 2, []string{"my-app"})
	require.NoError(t, err)
	require.NotContains(t, got, "my-app",
		"a symlinked dir must not be matched (avoids loops + agent-dir symlinks)")
}

func TestScanRoots_SkipsDotPrefixedDir(t *testing.T) {
	root := t.TempDir()
	// a project literally named ".claude" would never be a real checkout;
	// dot-prefixed dirs are skipped so .git/.cache aren't descended into.
	mkdirs(t, root, ".claude")

	got, err := ScanRoots([]string{root}, 2, []string{".claude"})
	require.NoError(t, err)
	require.NotContains(t, got, ".claude")
}

func TestScanRoots_MissingRootSkippedSilently(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "does-not-exist")
	mkdirs(t, root, "my-app")

	got, err := ScanRoots([]string{missing, root}, 1, []string{"my-app"})
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Join(root, "my-app")}, got["my-app"])
}

func TestScanRoots_NoNamesReturnsEmpty(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "my-app")

	got, err := ScanRoots([]string{root}, 2, nil)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestScanRoots_UnmatchedNameAbsentFromResult(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "my-app")

	got, err := ScanRoots([]string{root}, 2, []string{"ghost"})
	require.NoError(t, err)
	require.NotContains(t, got, "ghost")
}
