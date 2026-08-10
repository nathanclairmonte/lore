package central

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nathanclairmonte/lore/internal/git"
	"github.com/stretchr/testify/require"
)

func committedAgentDir(t *testing.T) (*Central, string) {
	t.Helper()
	c := initCentralWithIdentity(t)
	source := makeProjectDir(t, ".claude")
	_, _, err := c.AddAgentDir("my-app", ".claude", source)
	require.NoError(t, err)
	require.NoError(t, c.Commit("feat: add my-app"))
	return c, source
}

func TestReplacementSetRollbackRestoresContentAndIndex(t *testing.T) {
	c, source := committedAgentDir(t)
	require.NoError(t, os.WriteFile(filepath.Join(source, "CLAUDE.md"), []byte("changed"), 0o644))

	set, err := c.StageAgentDirReplacements([]AgentDirReplacement{{Project: "my-app", Dir: ".claude", Source: source}})
	require.NoError(t, err)
	require.NoError(t, set.Rollback())

	data, err := os.ReadFile(c.ProjectPath("my-app", ".claude") + "/CLAUDE.md")
	require.NoError(t, err)
	require.Equal(t, "context", string(data))
	status, err := git.Run(c.Path, "status", "--porcelain", "--", "projects/my-app")
	require.NoError(t, err)
	require.Empty(t, status)
}

func TestRecoverReplacementsRestoresInterruptedUncommittedContent(t *testing.T) {
	c, source := committedAgentDir(t)
	require.NoError(t, os.WriteFile(filepath.Join(source, "CLAUDE.md"), []byte("changed"), 0o644))
	_, err := c.StageAgentDirReplacements([]AgentDirReplacement{{Project: "my-app", Dir: ".claude", Source: source}})
	require.NoError(t, err)

	require.NoError(t, c.RecoverReplacements())
	data, err := os.ReadFile(filepath.Join(c.ProjectPath("my-app", ".claude"), "CLAUDE.md"))
	require.NoError(t, err)
	require.Equal(t, "context", string(data))
	status, err := git.Run(c.Path, "status", "--porcelain", "--", "projects/my-app")
	require.NoError(t, err)
	require.Empty(t, status)
}

func TestRecoverReplacementsKeepsInterruptedCommittedContent(t *testing.T) {
	c, source := committedAgentDir(t)
	require.NoError(t, os.WriteFile(filepath.Join(source, "CLAUDE.md"), []byte("changed"), 0o644))
	set, err := c.StageAgentDirReplacements([]AgentDirReplacement{{Project: "my-app", Dir: ".claude", Source: source}})
	require.NoError(t, err)
	require.NoError(t, c.CommitPaths("chore: sync (my-app)", set.Paths()...))
	require.NoError(t, os.WriteFile(filepath.Join(c.ProjectPath("my-app", ".claude"), "CLAUDE.md"), []byte("post-commit edit"), 0o644))

	require.NoError(t, c.RecoverReplacements())
	data, err := os.ReadFile(filepath.Join(c.ProjectPath("my-app", ".claude"), "CLAUDE.md"))
	require.NoError(t, err)
	require.Equal(t, "post-commit edit", string(data))
	require.NoDirExists(t, filepath.Join(c.Path, ".git", "lore-replacements", filepath.Base(set.transactions[0].root)))
}

func TestMutationLockSerializesCallers(t *testing.T) {
	c := initCentralWithIdentity(t)
	unlockFirst, err := c.LockMutations()
	require.NoError(t, err)
	acquired := make(chan func() error, 1)
	go func() {
		unlock, lockErr := c.LockMutations()
		if lockErr == nil {
			acquired <- unlock
		}
	}()

	select {
	case <-acquired:
		t.Fatal("second mutation lock acquired before the first was released")
	case <-time.After(50 * time.Millisecond):
	}
	require.NoError(t, unlockFirst())
	select {
	case unlockSecond := <-acquired:
		require.NoError(t, unlockSecond())
	case <-time.After(2 * time.Second):
		t.Fatal("second mutation lock did not acquire after release")
	}
}
