package git

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadTreePreservesBytesModesAndSymlinkTargets(t *testing.T) {
	repo := t.TempDir()
	require.NoError(t, Init(repo))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "tree"), 0o755))
	data := []byte("trailing whitespace  \n\x00binary\n\n")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "tree", "file"), data, 0o755))
	require.NoError(t, os.Symlink("file", filepath.Join(repo, "tree", "link")))
	_, err := Run(repo, "add", "tree")
	require.NoError(t, err)
	_, err = Run(repo, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "test")
	require.NoError(t, err)

	entries, err := ReadTree(repo, "HEAD", "tree")
	require.NoError(t, err)
	require.Len(t, entries, 2)
	require.Equal(t, "100755", entries[0].Mode)
	require.Equal(t, data, entries[0].Data)
	require.Equal(t, "120000", entries[1].Mode)
	require.Equal(t, []byte("file"), entries[1].Data)
}

func TestCommitExistsDistinguishesMissingObject(t *testing.T) {
	repo := t.TempDir()
	require.NoError(t, Init(repo))
	exists, err := CommitExists(repo, "0123456789abcdef0123456789abcdef01234567")
	require.NoError(t, err)
	require.False(t, exists)
}

func TestReadTreeReturnsGitlinksWithoutReadingThemAsBlobs(t *testing.T) {
	repo := t.TempDir()
	require.NoError(t, Init(repo))
	_, err := Run(repo, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "nested")
	require.NoError(t, err)
	sha, err := Run(repo, "rev-parse", "HEAD")
	require.NoError(t, err)
	_, err = Run(repo, "update-index", "--add", "--cacheinfo", "160000,"+sha+",tree/nested")
	require.NoError(t, err)
	_, err = Run(repo, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-m", "gitlink")
	require.NoError(t, err)

	entries, err := ReadTree(repo, "HEAD", "tree")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "160000", entries[0].Mode)
	require.Equal(t, "commit", entries[0].ObjectType)
	require.Empty(t, entries[0].Data)
}

func TestCommitExistsDoesNotTreatCorruptObjectAsMissing(t *testing.T) {
	repo := t.TempDir()
	require.NoError(t, Init(repo))
	_, err := Run(repo, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "base")
	require.NoError(t, err)
	sha, err := Run(repo, "rev-parse", "HEAD")
	require.NoError(t, err)
	objectPath := filepath.Join(repo, ".git", "objects", sha[:2], sha[2:])
	require.NoError(t, os.Chmod(objectPath, 0o644))
	require.NoError(t, os.WriteFile(objectPath, []byte("corrupt"), 0o444))

	exists, err := CommitExists(repo, sha)
	require.Error(t, err)
	require.False(t, exists)
}
