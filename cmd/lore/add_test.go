package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nathanclairmonte/lore/internal/git"
	"github.com/nathanclairmonte/lore/internal/output"
	"github.com/stretchr/testify/require"
)

// addCmdSandbox is the CLI-level equivalent of project.addSandbox. Wires
// HOME/XDG, an initialized central repo, a config file pointing at it, a
// project under HOME/code/my-app with .claude/ ignored via
// .git/info/exclude, and git identity env vars (no developer ~/.gitconfig).
func addCmdSandbox(t *testing.T) (home, projectRoot string) {
	t.Helper()
	home = sandbox(t)

	// identity for `lore add`'s user-authored commit on the central repo.
	t.Setenv("GIT_AUTHOR_NAME", "Test Author")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Author")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")

	require.Equal(t, 0, run([]string{"init"}))

	projectRoot = filepath.Join(home, "code", "my-app")
	require.NoError(t, os.MkdirAll(projectRoot, 0o755))
	_, err := git.Run(projectRoot, "init")
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(projectRoot, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(projectRoot, ".claude", "CLAUDE.md"),
		[]byte("context"), 0o644))
	require.NoError(t, os.WriteFile(
		filepath.Join(projectRoot, ".git", "info", "exclude"),
		[]byte(".claude/\n"), 0o644))
	return home, projectRoot
}

func TestLoreAdd_HappyPath(t *testing.T) {
	home, projectRoot := addCmdSandbox(t)

	require.Equal(t, 0, run([]string{"add", projectRoot}))

	// project copy remains a real directory.
	linkInfo, err := os.Lstat(filepath.Join(projectRoot, ".claude"))
	require.NoError(t, err)
	require.Zero(t, linkInfo.Mode()&os.ModeSymlink)
	require.True(t, linkInfo.IsDir())
	require.FileExists(t, filepath.Join(projectRoot, ".claude", "CLAUDE.md"))
	require.FileExists(t, filepath.Join(home, "agent-context", "projects", "my-app", ".claude", "CLAUDE.md"))

	// commit landed on central with the templated message
	msg, err := git.Run(filepath.Join(home, "agent-context"), "log", "-1", "--pretty=%s")
	require.NoError(t, err)
	require.Equal(t, "feat: add my-app", msg)
}

func TestLoreAdd_NameFlagRecordsOverride(t *testing.T) {
	home, projectRoot := addCmdSandbox(t)

	require.Equal(t, 0, run([]string{"add", "--name", "foo-alt", projectRoot}))

	// central project lives under the override, not the basename.
	require.DirExists(t,
		filepath.Join(home, "agent-context", "projects", "foo-alt", ".claude"))
	require.NoDirExists(t,
		filepath.Join(home, "agent-context", "projects", "my-app"))

	// project copy remains in place.
	require.FileExists(t, filepath.Join(projectRoot, ".claude", "CLAUDE.md"))

	// commit message uses the override.
	msg, err := git.Run(filepath.Join(home, "agent-context"), "log", "-1", "--pretty=%s")
	require.NoError(t, err)
	require.Equal(t, "feat: add foo-alt", msg)
}

func TestLoreAdd_JSONIncludesSyncBase(t *testing.T) {
	_, projectRoot := addCmdSandbox(t)
	var outputBuffer strings.Builder
	previous := output.Out
	output.Out = &outputBuffer
	t.Cleanup(func() { output.Out = previous })

	require.Equal(t, 0, run([]string{"--json", "add", projectRoot}))
	var got struct {
		Commit *struct {
			SHA string `json:"sha"`
		} `json:"commit"`
		SyncBase string `json:"sync_base"`
	}
	require.NoError(t, json.Unmarshal([]byte(outputBuffer.String()), &got))
	require.NotNil(t, got.Commit)
	require.Len(t, got.Commit.SHA, 40)
	require.Equal(t, got.Commit.SHA, got.SyncBase)
}

func TestLoreAdd_CreateWithoutDirIsInconsistency(t *testing.T) {
	_, projectRoot := addCmdSandbox(t)
	// remove the pre-existing .claude so --create has work to do.
	require.NoError(t, os.RemoveAll(filepath.Join(projectRoot, ".claude")))

	// no --dir + no TTY: the scaffold dir picker bails with exit 7
	// before any disk side effects land.
	require.Equal(t, 7, run([]string{"add", "--create", projectRoot}),
		"--create without --dir in non-interactive mode must exit 7")
}

func TestLoreAdd_RefusesNotGitignoredAndNoTTY(t *testing.T) {
	_, projectRoot := addCmdSandbox(t)
	// blank out the exclude rule so check-ignore says "not ignored".
	require.NoError(t, os.WriteFile(
		filepath.Join(projectRoot, ".git", "info", "exclude"),
		[]byte(""), 0o644))

	// `go test` runs without a TTY, so the slice 4 picker's stdin gate
	// fires and Add bails with CodeInconsistency (7). The picker itself
	// would map an interactive dismissal to CodeRefusedSafety (5);
	// non-interactive callers see CodeInconsistency. Both paths refuse
	// to onboard so the underlying contract (lore add will not move a
	// non-gitignored agent dir) is preserved.
	require.Equal(t, 7, run([]string{"add", projectRoot}),
		"no-TTY non-gitignored add must exit 7 (inconsistency)")
}
