package project

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nathanclairmonte/lore/internal/central"
	"github.com/nathanclairmonte/lore/internal/config"
	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/git"
	"github.com/nathanclairmonte/lore/internal/link"
	"github.com/nathanclairmonte/lore/internal/state"
	"github.com/stretchr/testify/require"
)

// addSandbox stands up a full lore environment in temp dirs: HOME,
// XDG_CONFIG_HOME, XDG_STATE_HOME isolated, central repo initialized,
// config.toml pointing at it, git identity wired so AddProject commits
// succeed without leaning on the developer's ~/.gitconfig. Returns
// (central path, project root). The project root is a fresh git repo
// with .claude/ present and ignored via .git/info/exclude.
func addSandbox(t *testing.T) (centralPath, projectRoot string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local/state"))

	// git identity via env (no ~/.gitconfig writes). ADR 0003: user
	// commits use the user's identity, NOT lore@localhost.
	t.Setenv("GIT_AUTHOR_NAME", "Test Author")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Author")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")

	centralPath = filepath.Join(home, "agent-context")
	_, err := central.Initialize(centralPath)
	require.NoError(t, err)
	require.NoError(t, config.WriteDefaults())

	projectRoot = filepath.Join(home, "code", "my-app")
	require.NoError(t, os.MkdirAll(projectRoot, 0o755))
	_, err = git.Run(projectRoot, "init")
	require.NoError(t, err)

	agentDir := filepath.Join(projectRoot, ".claude")
	require.NoError(t, os.Mkdir(agentDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(agentDir, "CLAUDE.md"),
		[]byte("context"), 0o644))
	// .git/info/exclude is the per-repo source check-ignore consults;
	// avoids depending on the developer's global excludesfile.
	require.NoError(t, os.WriteFile(
		filepath.Join(projectRoot, ".git", "info", "exclude"),
		[]byte(".claude/\n"), 0o644))

	return centralPath, projectRoot
}

func TestAddHappyPath(t *testing.T) {
	centralPath, projectRoot := addSandbox(t)

	res, err := Add(AddOptions{ProjectPath: projectRoot})
	require.NoError(t, err)

	require.Equal(t, "my-app", res.ProjectName)
	require.Equal(t, projectRoot, res.ProjectPath)
	require.Equal(t, ".claude", res.AgentDirName)
	require.Equal(t, centralPath, res.CentralRepo)
	require.Equal(t,
		filepath.Join(centralPath, "projects", "my-app", ".claude"),
		res.CentralPath)
	require.Equal(t, "feat: add my-app", res.CommitMessage)
	require.False(t, res.AlreadyTracked)

	// symlink is in place and points where we expect.
	require.NoError(t, link.Validate(
		filepath.Join(projectRoot, ".claude"),
		res.CentralPath))

	// central contents survived the move.
	require.FileExists(t, filepath.Join(res.CentralPath, "CLAUDE.md"))

	// state recorded.
	st, _, err := state.Load()
	require.NoError(t, err)
	got, ok := st.PathFor("my-app")
	require.True(t, ok)
	require.Equal(t, projectRoot, got)

	// commit landed on central with the templated message.
	msg, err := git.Run(centralPath, "log", "-1", "--pretty=%s")
	require.NoError(t, err)
	require.Equal(t, "feat: add my-app", msg)
}

func TestAddRefusesWhenNotGitignored(t *testing.T) {
	_, projectRoot := addSandbox(t)

	// nuke the exclude rule we set up in the sandbox so check-ignore
	// returns "not ignored".
	require.NoError(t, os.WriteFile(
		filepath.Join(projectRoot, ".git", "info", "exclude"),
		[]byte(""), 0o644))

	_, err := Add(AddOptions{ProjectPath: projectRoot})
	require.Error(t, err)
	require.Equal(t, lerr.CodeRefusedSafety, lerr.CodeOf(err))
	require.Contains(t, err.Error(), ".claude is not globally gitignored")

	// nothing on disk has changed: source dir still there, no symlink.
	require.DirExists(t, filepath.Join(projectRoot, ".claude"))
	isLink, _ := link.IsSymlink(filepath.Join(projectRoot, ".claude"))
	require.False(t, isLink)

	// no state recorded.
	st, _, _ := state.Load()
	_, ok := st.PathFor("my-app")
	require.False(t, ok)
}

func TestAddNoAgentDirFound(t *testing.T) {
	_, projectRoot := addSandbox(t)
	require.NoError(t, os.RemoveAll(filepath.Join(projectRoot, ".claude")))

	_, err := Add(AddOptions{ProjectPath: projectRoot})
	require.Error(t, err)
	require.Equal(t, lerr.CodeNotFound, lerr.CodeOf(err))
	require.Contains(t, err.Error(), "no agent directory found")
}

func TestAddIdempotentReRun(t *testing.T) {
	_, projectRoot := addSandbox(t)

	first, err := Add(AddOptions{ProjectPath: projectRoot})
	require.NoError(t, err)
	require.False(t, first.AlreadyTracked)

	second, err := Add(AddOptions{ProjectPath: projectRoot})
	require.NoError(t, err)
	require.True(t, second.AlreadyTracked)
	require.Equal(t, first.CentralPath, second.CentralPath)
}

func TestAddDriftedStateConflicts(t *testing.T) {
	_, projectRoot := addSandbox(t)

	_, err := Add(AddOptions{ProjectPath: projectRoot})
	require.NoError(t, err)

	// simulate drift: user manually removes the symlink. state still
	// claims the project is tracked, so the next add must surface this
	// as a conflict rather than silently no-op or re-onboard.
	require.NoError(t, link.Remove(filepath.Join(projectRoot, ".claude")))

	_, err = Add(AddOptions{ProjectPath: projectRoot})
	require.Error(t, err)
	require.Equal(t, lerr.CodeConflict, lerr.CodeOf(err))
}

func TestAddUsesCwdWhenPathEmpty(t *testing.T) {
	_, projectRoot := addSandbox(t)

	cwd, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	require.NoError(t, os.Chdir(projectRoot))

	res, err := Add(AddOptions{})
	require.NoError(t, err)
	require.Equal(t, "my-app", res.ProjectName)
}

func TestAddResolvesSymlinkInProjectPath(t *testing.T) {
	_, projectRoot := addSandbox(t)

	// plant a symlink that points at the real project root; calling
	// Add with the symlink path must resolve to the real path so state
	// stores the canonical location.
	linkAtTop := filepath.Join(t.TempDir(), "alias")
	require.NoError(t, os.Symlink(projectRoot, linkAtTop))

	res, err := Add(AddOptions{ProjectPath: linkAtTop})
	require.NoError(t, err)
	require.Equal(t, projectRoot, res.ProjectPath,
		"state must store the symlink-resolved path, not the alias")
}

func TestAddMissingProjectPath(t *testing.T) {
	addSandbox(t)
	_, err := Add(AddOptions{ProjectPath: "/this/path/does/not/exist"})
	require.Error(t, err)
	require.Equal(t, lerr.CodeNotFound, lerr.CodeOf(err))
}

// snapshotDir lists every entry under root (recursively, by relative
// path) so a refusal test can assert "nothing changed". Returning a sorted
// slice keeps the comparison deterministic across filesystems.
func snapshotDir(t *testing.T, root string) []string {
	t.Helper()
	var entries []string
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		entries = append(entries, rel)
		return nil
	})
	require.NoError(t, err)
	return entries
}

func TestAddRefusesWhenAgentDirHasTrackedFiles(t *testing.T) {
	_, projectRoot := addSandbox(t)

	// move .claude into the project's index so check-safety's tracked-
	// files gate fires. addSandbox already wired .git/info/exclude to
	// ignore .claude, so we use -f to force it past the ignore rule.
	_, err := git.Run(projectRoot, "add", "-f", ".claude/CLAUDE.md")
	require.NoError(t, err)

	_, err = Add(AddOptions{ProjectPath: projectRoot})
	require.Error(t, err)
	require.Equal(t, lerr.CodeRefusedSafety, lerr.CodeOf(err))
	require.Contains(t, err.Error(), "tracked by this repo's git")
	require.Contains(t, err.Error(), "git rm --cached -r .claude")
}

func TestAddRefusesWhenAgentDirHasNestedGit(t *testing.T) {
	_, projectRoot := addSandbox(t)
	require.NoError(t, os.Mkdir(filepath.Join(projectRoot, ".claude", ".git"), 0o755))

	_, err := Add(AddOptions{ProjectPath: projectRoot})
	require.Error(t, err)
	require.Equal(t, lerr.CodeRefusedSafety, lerr.CodeOf(err))
	require.Contains(t, err.Error(), "nested git repository")
}

func TestAddRefusesWhenAgentDirIsSymlink(t *testing.T) {
	_, projectRoot := addSandbox(t)
	// replace the real .claude with a symlink pointing somewhere else.
	require.NoError(t, os.RemoveAll(filepath.Join(projectRoot, ".claude")))
	target := filepath.Join(t.TempDir(), "somewhere")
	require.NoError(t, os.Mkdir(target, 0o755))
	require.NoError(t, os.Symlink(target, filepath.Join(projectRoot, ".claude")))

	_, err := Add(AddOptions{ProjectPath: projectRoot})
	require.Error(t, err)
	require.Equal(t, lerr.CodeRefusedSafety, lerr.CodeOf(err))
	require.Contains(t, err.Error(), "already a symlink to")
	require.Contains(t, err.Error(), target)
}

func TestAddRefusesReportsAllViolationsAtOnce(t *testing.T) {
	_, projectRoot := addSandbox(t)
	// .claude trips two checks at once: tracked file + nested .git.
	_, err := git.Run(projectRoot, "add", "-f", ".claude/CLAUDE.md")
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(projectRoot, ".claude", ".git"), 0o755))

	_, err = Add(AddOptions{ProjectPath: projectRoot})
	require.Error(t, err)
	require.Equal(t, lerr.CodeRefusedSafety, lerr.CodeOf(err))
	// both messages must surface so the user fixes everything in one round.
	require.Contains(t, err.Error(), "tracked by this repo's git")
	require.Contains(t, err.Error(), "nested git repository")
}

func TestAddRefusalLeavesFilesystemUntouched(t *testing.T) {
	centralPath, projectRoot := addSandbox(t)

	// arrange a refusal (nested .git is cheap to set up).
	require.NoError(t, os.Mkdir(filepath.Join(projectRoot, ".claude", ".git"), 0o755))

	projectBefore := snapshotDir(t, projectRoot)
	centralBefore := snapshotDir(t, centralPath)
	commitBefore, err := git.Run(centralPath, "rev-parse", "HEAD")
	require.NoError(t, err)

	_, err = Add(AddOptions{ProjectPath: projectRoot})
	require.Error(t, err)
	require.Equal(t, lerr.CodeRefusedSafety, lerr.CodeOf(err))

	// every entry list and the central HEAD commit must be byte-identical.
	require.Equal(t, projectBefore, snapshotDir(t, projectRoot),
		"project dir must not change on refusal (no move, no symlink)")
	require.Equal(t, centralBefore, snapshotDir(t, centralPath),
		"central repo dir must not change on refusal")
	commitAfter, err := git.Run(centralPath, "rev-parse", "HEAD")
	require.NoError(t, err)
	require.Equal(t, commitBefore, commitAfter,
		"no new commit must land on central when add is refused")

	// state has not gained an entry for this project either.
	st, _, _ := state.Load()
	_, ok := st.PathFor("my-app")
	require.False(t, ok)
}

func TestRenderAddTemplate(t *testing.T) {
	require.Equal(t, "feat: add my-app",
		renderAddTemplate("feat: add {project}", "my-app"))
	// empty template falls back to a safe default so a misconfigured
	// commit.add_template doesn't break Add with a blank commit message.
	require.Equal(t, "feat: add my-app",
		renderAddTemplate("", "my-app"))
	// no {project} placeholder -> template returned verbatim.
	require.Equal(t, "static message",
		renderAddTemplate("static message", "my-app"))
}

func TestPathReturnsCentralAgentDir(t *testing.T) {
	centralPath, projectRoot := addSandbox(t)
	_, err := Add(AddOptions{ProjectPath: projectRoot})
	require.NoError(t, err)

	got, err := Path("my-app")
	require.NoError(t, err)
	require.Equal(t,
		filepath.Join(centralPath, "projects", "my-app", ".claude"),
		got)
}

func TestPathUntracked(t *testing.T) {
	addSandbox(t)
	_, err := Path("never-added")
	require.Error(t, err)
	require.Equal(t, lerr.CodeNotFound, lerr.CodeOf(err))
}

func TestPathRepo(t *testing.T) {
	centralPath, _ := addSandbox(t)
	got, err := PathRepo()
	require.NoError(t, err)
	require.Equal(t, centralPath, got)
}
