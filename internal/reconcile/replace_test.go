package reconcile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReplaceTreesPublishesMissingAndExistingDestinations(t *testing.T) {
	root := t.TempDir()
	sourceOne := filepath.Join(root, "source-one")
	sourceTwo := filepath.Join(root, "source-two")
	destinationOne := filepath.Join(root, "checkout", ".claude")
	destinationTwo := filepath.Join(root, "checkout", ".cursor")
	for _, path := range []string{sourceOne, sourceTwo, destinationOne} {
		require.NoError(t, os.MkdirAll(path, 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(sourceOne, "one"), []byte("new one"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(sourceTwo, "two"), []byte("new two"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(destinationOne, "old"), []byte("old"), 0o644))

	require.NoError(t, ReplaceTrees([]TreeReplacement{
		{Source: sourceOne, Destination: destinationOne},
		{Source: sourceTwo, Destination: destinationTwo},
	}))
	require.FileExists(t, filepath.Join(destinationOne, "one"))
	require.NoFileExists(t, filepath.Join(destinationOne, "old"))
	require.FileExists(t, filepath.Join(destinationTwo, "two"))
}

func TestReplaceTreesRefusalLeavesEveryDestinationUntouched(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	first := filepath.Join(root, "checkout", ".claude")
	second := filepath.Join(root, "checkout", ".cursor")
	require.NoError(t, os.MkdirAll(source, 0o755))
	require.NoError(t, os.MkdirAll(first, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Dir(second), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(source, "new"), []byte("new"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(first, "old"), []byte("old"), 0o644))
	require.NoError(t, os.WriteFile(second, []byte("blocker"), 0o644))

	err := ReplaceTrees([]TreeReplacement{
		{Source: source, Destination: first},
		{Source: source, Destination: second},
	})
	require.Error(t, err)
	require.FileExists(t, filepath.Join(first, "old"))
	require.NoFileExists(t, filepath.Join(first, "new"))
	data, readErr := os.ReadFile(second)
	require.NoError(t, readErr)
	require.Equal(t, "blocker", string(data))
}
