package detect

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

var defaultCandidates = []string{".claude", ".agents", ".cursor", ".aider"}

func TestDetectAgentDirSingleMatch(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".claude"), 0o755))

	got, ok, err := DetectAgentDir(root, defaultCandidates)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, ".claude", got.Name)
	require.Equal(t, filepath.Join(root, ".claude"), got.Path)
}

func TestDetectAgentDirNoMatch(t *testing.T) {
	root := t.TempDir()
	got, ok, err := DetectAgentDir(root, defaultCandidates)
	require.NoError(t, err)
	require.False(t, ok)
	require.Equal(t, AgentDir{}, got)
}

func TestDetectAgentDirHonoursCandidatesOrder(t *testing.T) {
	root := t.TempDir()
	// create .cursor and .claude; iteration order in candidates is
	// .claude before .cursor, so .claude must win regardless of
	// filesystem creation order.
	require.NoError(t, os.Mkdir(filepath.Join(root, ".cursor"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(root, ".claude"), 0o755))

	got, ok, err := DetectAgentDir(root, defaultCandidates)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, ".claude", got.Name)
}

func TestDetectAgentDirRespectsCustomOrder(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".claude"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(root, ".cursor"), 0o755))

	// caller-supplied order puts .cursor first.
	got, ok, err := DetectAgentDir(root, []string{".cursor", ".claude"})
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, ".cursor", got.Name)
}

func TestDetectAgentDirSkipsRegularFile(t *testing.T) {
	root := t.TempDir()
	// .claude as a file, not a dir
	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude"), []byte(""), 0o644))

	got, ok, err := DetectAgentDir(root, defaultCandidates)
	require.NoError(t, err)
	require.False(t, ok)
	require.Equal(t, AgentDir{}, got)
}

func TestDetectAgentDirSkipsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "somewhere")
	require.NoError(t, os.Mkdir(target, 0o755))
	require.NoError(t, os.Symlink(target, filepath.Join(root, ".claude")))

	got, ok, err := DetectAgentDir(root, defaultCandidates)
	require.NoError(t, err)
	require.False(t, ok, "symlinked candidates must not be detected (slice 3 safety case)")
	require.Equal(t, AgentDir{}, got)
}

func TestDetectAgentDirFallsThroughToLaterCandidate(t *testing.T) {
	root := t.TempDir()
	// only .cursor exists. detection must skip the missing .claude/.agents
	// without erroring and return .cursor.
	require.NoError(t, os.Mkdir(filepath.Join(root, ".cursor"), 0o755))

	got, ok, err := DetectAgentDir(root, defaultCandidates)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, ".cursor", got.Name)
}
