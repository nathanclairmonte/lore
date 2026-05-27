package detect

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

var defaultCandidates = []string{".claude", ".agents", ".cursor", ".aider"}

func namesOf(dirs []AgentDir) []string {
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		out = append(out, d.Name)
	}
	return out
}

func TestDetectAgentDirsSingleMatch(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".claude"), 0o755))

	got, err := DetectAgentDirs(root, defaultCandidates)
	require.NoError(t, err)
	require.Equal(t, []string{".claude"}, namesOf(got))
	require.Equal(t, filepath.Join(root, ".claude"), got[0].Path)
}

func TestDetectAgentDirsNoMatch(t *testing.T) {
	root := t.TempDir()
	got, err := DetectAgentDirs(root, defaultCandidates)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestDetectAgentDirsReturnsAllMatches(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".claude"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(root, ".cursor"), 0o755))

	got, err := DetectAgentDirs(root, defaultCandidates)
	require.NoError(t, err)
	require.Equal(t, []string{".claude", ".cursor"}, namesOf(got),
		"multi-match returns every existing real-directory candidate in candidates order")
}

func TestDetectAgentDirsHonoursCandidatesOrder(t *testing.T) {
	root := t.TempDir()
	// create .cursor and .claude; the candidates list places .claude
	// before .cursor, so the returned slice must mirror that order
	// regardless of filesystem creation order.
	require.NoError(t, os.Mkdir(filepath.Join(root, ".cursor"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(root, ".claude"), 0o755))

	got, err := DetectAgentDirs(root, defaultCandidates)
	require.NoError(t, err)
	require.Equal(t, []string{".claude", ".cursor"}, namesOf(got))
}

func TestDetectAgentDirsRespectsCustomOrder(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".claude"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(root, ".cursor"), 0o755))

	// caller-supplied order puts .cursor first.
	got, err := DetectAgentDirs(root, []string{".cursor", ".claude"})
	require.NoError(t, err)
	require.Equal(t, []string{".cursor", ".claude"}, namesOf(got))
}

func TestDetectAgentDirsSkipsRegularFile(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude"), []byte(""), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(root, ".cursor"), 0o755))

	got, err := DetectAgentDirs(root, defaultCandidates)
	require.NoError(t, err)
	require.Equal(t, []string{".cursor"}, namesOf(got),
		"regular files at candidate names are skipped")
}

func TestDetectAgentDirsSkipsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "somewhere")
	require.NoError(t, os.Mkdir(target, 0o755))
	require.NoError(t, os.Symlink(target, filepath.Join(root, ".claude")))
	require.NoError(t, os.Mkdir(filepath.Join(root, ".cursor"), 0o755))

	got, err := DetectAgentDirs(root, defaultCandidates)
	require.NoError(t, err)
	require.Equal(t, []string{".cursor"}, namesOf(got),
		"symlinked candidates are skipped (post-add steady state); .cursor still detected")
}
