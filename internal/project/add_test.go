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
	"github.com/nathanclairmonte/lore/internal/prompt"
	"github.com/nathanclairmonte/lore/internal/state"
	"github.com/stretchr/testify/require"
)

// stubPicker swaps the package-level pickGitignoreTarget var so picker
// tests can exercise integration paths without driving huh through a
// real terminal. Returns a call counter so tests can assert the picker
// was (or wasn't) invoked.
func stubPicker(t *testing.T, choice prompt.GitignoreTarget, err error) *int {
	t.Helper()
	count := 0
	prev := pickGitignoreTarget
	pickGitignoreTarget = func(string) (prompt.GitignoreTarget, error) {
		count++
		return choice, err
	}
	t.Cleanup(func() { pickGitignoreTarget = prev })
	return &count
}

// unignoreSandbox clears the .git/info/exclude entry addSandbox plants
// so check-ignore reports .claude as not ignored, forcing Add into the
// picker path.
func unignoreSandbox(t *testing.T, projectRoot string) {
	t.Helper()
	require.NoError(t, os.WriteFile(
		filepath.Join(projectRoot, ".git", "info", "exclude"),
		[]byte(""), 0o644))
}

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

	// hermetic git config. without these, a CI runner or dev machine
	// with a system-level core.excludesFile (Homebrew on macOS sets
	// /usr/local/etc/gitconfig that way) would leak into the slice 4
	// picker tests: GlobalExcludesPath would resolve to the system
	// path, AppendIgnoreEntry would either fail with EACCES or, worse,
	// pollute the host's shared file. GIT_CONFIG_GLOBAL pins the
	// "global" scope to the temp HOME's .gitconfig; GIT_CONFIG_SYSTEM
	// disables system scope entirely.
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")

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
	require.Equal(t, centralPath, res.CentralRepo)
	require.Equal(t, "feat: add my-app", res.CommitMessage)
	require.False(t, res.AllAlreadyTracked())

	require.Len(t, res.Dirs, 1)
	require.Equal(t, ".claude", res.Dirs[0].Name)
	require.Equal(t, StatusAdded, res.Dirs[0].Status)
	require.Equal(t,
		filepath.Join(centralPath, "projects", "my-app", ".claude"),
		res.Dirs[0].CentralPath)

	// symlink is in place and points where we expect.
	require.NoError(t, link.Validate(
		filepath.Join(projectRoot, ".claude"),
		res.Dirs[0].CentralPath))

	// central contents survived the move.
	require.FileExists(t, filepath.Join(res.Dirs[0].CentralPath, "CLAUDE.md"))

	// state recorded with the agent dir set.
	st, _, err := state.Load()
	require.NoError(t, err)
	entry, ok := st.ProjectEntryFor("my-app")
	require.True(t, ok)
	require.Equal(t, projectRoot, entry.Path)
	require.Equal(t, []string{".claude"}, entry.AgentDirs)

	// commit landed on central with the templated message.
	msg, err := git.Run(centralPath, "log", "-1", "--pretty=%s")
	require.NoError(t, err)
	require.Equal(t, "feat: add my-app", msg)
}

func TestAddRefusesWhenNotGitignoredAndNoTTY(t *testing.T) {
	_, projectRoot := addSandbox(t)
	unignoreSandbox(t, projectRoot)
	// no stubPicker: tests run without a TTY, so the picker's own
	// gate fires and returns CodeInconsistency. exercises the
	// real picker -> orchestrator integration end-to-end.

	_, err := Add(AddOptions{ProjectPath: projectRoot})
	require.Error(t, err)
	require.Equal(t, lerr.CodeInconsistency, lerr.CodeOf(err))
	require.Contains(t, err.Error(), "stdin is not a terminal")

	// nothing on disk has changed: source dir still there, no symlink.
	require.DirExists(t, filepath.Join(projectRoot, ".claude"))
	isLink, _ := link.IsSymlink(filepath.Join(projectRoot, ".claude"))
	require.False(t, isLink)

	// no state recorded.
	st, _, _ := state.Load()
	_, ok := st.PathFor("my-app")
	require.False(t, ok)
}

func TestAddPickerWritesRepoGitignoreAndProceeds(t *testing.T) {
	centralPath, projectRoot := addSandbox(t)
	unignoreSandbox(t, projectRoot)
	calls := stubPicker(t, prompt.TargetRepoGitignore, nil)

	res, err := Add(AddOptions{ProjectPath: projectRoot})
	require.NoError(t, err)
	require.Equal(t, 1, *calls, "picker should fire once when not ignored")

	// .gitignore at repo root got the entry. Leading slash (slice-5
	// followup fold-in) anchors the pattern to the repo root rather
	// than matching .claude/ inside arbitrary subdirs. No trailing slash
	// because the post-move symlink wouldn't match a dir-only pattern.
	got, err := os.ReadFile(filepath.Join(projectRoot, ".gitignore"))
	require.NoError(t, err)
	require.Equal(t, "/.claude\n", string(got))

	// the rest of Add ran: central + symlink + state + commit.
	require.Len(t, res.Dirs, 1)
	require.Equal(t,
		filepath.Join(centralPath, "projects", "my-app", ".claude"),
		res.Dirs[0].CentralPath)
	require.NoError(t, link.Validate(
		filepath.Join(projectRoot, ".claude"), res.Dirs[0].CentralPath))
}

func TestAddPickerWritesGlobalExcludesAndProceeds(t *testing.T) {
	_, projectRoot := addSandbox(t)
	unignoreSandbox(t, projectRoot)
	calls := stubPicker(t, prompt.TargetGlobalExcludes, nil)

	_, err := Add(AddOptions{ProjectPath: projectRoot})
	require.NoError(t, err)
	require.Equal(t, 1, *calls)

	// addSandbox set HOME and XDG_CONFIG_HOME to temp dirs; with no
	// core.excludesFile configured, the helper falls through to
	// $XDG_CONFIG_HOME/git/ignore. assert it landed there. Bare pattern
	// (no leading slash): global patterns shouldn't be path-anchored or
	// they'd defeat the point of a machine-wide rule.
	xdg := os.Getenv("XDG_CONFIG_HOME")
	require.NotEmpty(t, xdg)
	got, err := os.ReadFile(filepath.Join(xdg, "git", "ignore"))
	require.NoError(t, err, "global excludes file should have been created")
	require.Equal(t, ".claude\n", string(got))
}

func TestAddPickerWritesRepoExcludeAndProceeds(t *testing.T) {
	_, projectRoot := addSandbox(t)
	unignoreSandbox(t, projectRoot)
	calls := stubPicker(t, prompt.TargetRepoExclude, nil)

	_, err := Add(AddOptions{ProjectPath: projectRoot})
	require.NoError(t, err)
	require.Equal(t, 1, *calls)

	// .git/info/exclude contains the entry now. unignoreSandbox emptied
	// the file at setup time, so the exact-match assertion catches a
	// missing-anchor regression cleanly. Leading slash anchors the
	// pattern to the repo root, same policy as the repo .gitignore case.
	got, err := os.ReadFile(filepath.Join(projectRoot, ".git", "info", "exclude"))
	require.NoError(t, err)
	require.Equal(t, "/.claude\n", string(got))
}

func TestAddPickerDismissAbortsWithNoSideEffects(t *testing.T) {
	centralPath, projectRoot := addSandbox(t)
	unignoreSandbox(t, projectRoot)

	// snapshot disk + central HEAD so we can prove the dismissal
	// leaves them untouched.
	projectBefore := snapshotDir(t, projectRoot)
	centralBefore := snapshotDir(t, centralPath)
	commitBefore, err := git.Run(centralPath, "rev-parse", "HEAD")
	require.NoError(t, err)

	stubPicker(t, 0, lerr.New(lerr.CodeRefusedSafety,
		"agent directory must be gitignored before `lore add` can proceed"))

	_, err = Add(AddOptions{ProjectPath: projectRoot})
	require.Error(t, err)
	require.Equal(t, lerr.CodeRefusedSafety, lerr.CodeOf(err))

	// no .gitignore written (picker errored before any write).
	_, statErr := os.Stat(filepath.Join(projectRoot, ".gitignore"))
	require.True(t, os.IsNotExist(statErr))
	// project and central byte-identical, no new commit.
	require.Equal(t, projectBefore, snapshotDir(t, projectRoot))
	require.Equal(t, centralBefore, snapshotDir(t, centralPath))
	commitAfter, err := git.Run(centralPath, "rev-parse", "HEAD")
	require.NoError(t, err)
	require.Equal(t, commitBefore, commitAfter)
	// state empty.
	st, _, _ := state.Load()
	_, ok := st.PathFor("my-app")
	require.False(t, ok)
}

func TestAddRefusesNonGitProjectBeforePickerFires(t *testing.T) {
	// non-git project with .claude/ but no `git init`. The slice 4
	// gating step (RepoToplevel) must refuse BEFORE the picker fires,
	// so a stubbed picker returning an error would prove the gate
	// short-circuited correctly even if the picker would otherwise
	// "succeed". CheckIgnore folds not-a-repo into "not ignored", so
	// without the gate this branch would reach the picker, write a
	// stray .gitignore, and only fail on re-verify.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local/state"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	centralPath := filepath.Join(home, "agent-context")
	_, err := central.Initialize(centralPath)
	require.NoError(t, err)
	require.NoError(t, config.WriteDefaults())

	projectRoot := filepath.Join(home, "code", "nogit")
	require.NoError(t, os.MkdirAll(filepath.Join(projectRoot, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(projectRoot, ".claude", "CLAUDE.md"),
		[]byte("c"), 0o644))

	calls := stubPicker(t, prompt.TargetRepoGitignore,
		lerr.New(lerr.CodeGenericError, "picker must not be called"))

	_, err = Add(AddOptions{ProjectPath: projectRoot})
	require.Error(t, err)
	require.Equal(t, lerr.CodeRefusedSafety, lerr.CodeOf(err))
	require.Contains(t, err.Error(), "not inside a git repository")
	require.Equal(t, 0, *calls, "picker must not fire when the project isn't a git repo")
	// no stray .gitignore from a partial picker write.
	_, statErr := os.Stat(filepath.Join(projectRoot, ".gitignore"))
	require.True(t, os.IsNotExist(statErr))
}

func TestAddPickerWritesToRepoToplevelForMonorepoSubdir(t *testing.T) {
	// the project is onboarded from a subdir of a larger repo. The
	// repo's actual root .gitignore must receive the entry, not a new
	// .gitignore stranded in the subdir. addSandbox makes the project
	// itself the repo root, so this test builds its own layout where
	// the git repo wraps the project as a subdir.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local/state"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Setenv("GIT_AUTHOR_NAME", "Test Author")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Author")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")
	centralPath := filepath.Join(home, "agent-context")
	_, err := central.Initialize(centralPath)
	require.NoError(t, err)
	require.NoError(t, config.WriteDefaults())

	repoRoot := filepath.Join(home, "monorepo")
	subProject := filepath.Join(repoRoot, "services", "foo")
	require.NoError(t, os.MkdirAll(subProject, 0o755))
	_, err = git.Run(repoRoot, "init")
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(subProject, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(subProject, ".claude", "CLAUDE.md"),
		[]byte("c"), 0o644))

	stubPicker(t, prompt.TargetRepoGitignore, nil)

	_, err = Add(AddOptions{ProjectPath: subProject})
	require.NoError(t, err)

	// entry must land in the repo's root .gitignore, not in the subdir.
	// Pattern is anchored to the SUBDIR's location relative to the repo
	// root so the ignore rule actually fires for the project's .claude
	// (a bare ".claude" at the repo root would only match ./.claude, not
	// services/foo/.claude). The leading "/" anchors to the .gitignore
	// file's directory; the relative path scopes to the subdir.
	gotRoot, err := os.ReadFile(filepath.Join(repoRoot, ".gitignore"))
	require.NoError(t, err)
	require.Equal(t, "/services/foo/.claude\n", string(gotRoot))
	_, subStatErr := os.Stat(filepath.Join(subProject, ".gitignore"))
	require.True(t, os.IsNotExist(subStatErr),
		"no stray .gitignore at the subdir level")
}

func TestAddPickerNotInvokedOnAlreadyIgnored(t *testing.T) {
	_, projectRoot := addSandbox(t)
	// sandbox already wires .git/info/exclude, so the picker has no
	// reason to fire. counts must stay at zero.
	calls := stubPicker(t, prompt.TargetRepoGitignore,
		lerr.New(lerr.CodeGenericError, "picker should not have been called"))

	_, err := Add(AddOptions{ProjectPath: projectRoot})
	require.NoError(t, err)
	require.Equal(t, 0, *calls)
}

func TestAddPickerNotReinvokedAfterSuccessfulRun(t *testing.T) {
	_, projectRoot := addSandbox(t)
	unignoreSandbox(t, projectRoot)
	calls := stubPicker(t, prompt.TargetRepoGitignore, nil)

	_, err := Add(AddOptions{ProjectPath: projectRoot})
	require.NoError(t, err)
	require.Equal(t, 1, *calls, "first run fires the picker")

	// second run: project is already tracked, idempotent return path
	// fires BEFORE the gitignore check, so the picker should never be
	// consulted again even if check-ignore behaviour were to change.
	_, err = Add(AddOptions{ProjectPath: projectRoot})
	require.NoError(t, err)
	require.Equal(t, 1, *calls, "second run must not re-invoke the picker")
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
	require.False(t, first.AllAlreadyTracked())
	require.Equal(t, StatusAdded, first.Dirs[0].Status)

	second, err := Add(AddOptions{ProjectPath: projectRoot})
	require.NoError(t, err)
	require.True(t, second.AllAlreadyTracked())
	require.Equal(t, first.Dirs[0].CentralPath, second.Dirs[0].CentralPath)
	require.Empty(t, second.CommitMessage, "no commit fires on idempotent re-run")
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
		renderAddTemplate("feat: add {project}", "my-app", []string{".claude"}))
	// empty template falls back to a safe default so a misconfigured
	// commit.add_template doesn't break Add with a blank commit message.
	require.Equal(t, "feat: add my-app",
		renderAddTemplate("", "my-app", []string{".claude"}))
	// no placeholder -> template returned verbatim.
	require.Equal(t, "static message",
		renderAddTemplate("static message", "my-app", []string{".claude"}))
	// {dirs} expands to a comma-joined list, the slice-5 multi-dir
	// case. Order in the slice is preserved so the user sees a
	// deterministic message.
	require.Equal(t, "feat: add my-app (.claude, .cursor)",
		renderAddTemplate("feat: add {project} ({dirs})", "my-app",
			[]string{".claude", ".cursor"}))
}

func TestPathReturnsCentralAgentDir(t *testing.T) {
	centralPath, projectRoot := addSandbox(t)
	_, err := Add(AddOptions{ProjectPath: projectRoot})
	require.NoError(t, err)

	got, err := Path("my-app", "")
	require.NoError(t, err)
	require.Equal(t,
		filepath.Join(centralPath, "projects", "my-app", ".claude"),
		got)
}

func TestPathUntracked(t *testing.T) {
	addSandbox(t)
	_, err := Path("never-added", "")
	require.Error(t, err)
	require.Equal(t, lerr.CodeNotFound, lerr.CodeOf(err))
}

func TestPathRepo(t *testing.T) {
	centralPath, _ := addSandbox(t)
	got, err := PathRepo()
	require.NoError(t, err)
	require.Equal(t, centralPath, got)
}

// --- slice 5 multi-dir tests -------------------------------------------

// makeAgentDir creates an agent dir with a single placeholder file
// inside projectRoot. Helper for multi-dir setup.
func makeAgentDir(t *testing.T, projectRoot, name string) {
	t.Helper()
	dir := filepath.Join(projectRoot, name)
	require.NoError(t, os.Mkdir(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "CONTEXT.md"),
		[]byte("ctx"), 0o644))
}

func TestAddMultiDirOnboardsAllByDefault(t *testing.T) {
	centralPath, projectRoot := addSandbox(t)
	// addSandbox already puts .claude/ in place; layer on .cursor/.
	makeAgentDir(t, projectRoot, ".cursor")
	// ignore .cursor too so the picker doesn't fire for either dir.
	require.NoError(t, os.WriteFile(
		filepath.Join(projectRoot, ".git", "info", "exclude"),
		[]byte(".claude/\n.cursor/\n"), 0o644))

	res, err := Add(AddOptions{ProjectPath: projectRoot})
	require.NoError(t, err)
	require.Len(t, res.Dirs, 2)

	gotNames := map[string]DirStatus{}
	for _, d := range res.Dirs {
		gotNames[d.Name] = d.Status
	}
	require.Equal(t, StatusAdded, gotNames[".claude"])
	require.Equal(t, StatusAdded, gotNames[".cursor"])

	// state records both dirs under one project entry.
	st, _, err := state.Load()
	require.NoError(t, err)
	entry, ok := st.ProjectEntryFor("my-app")
	require.True(t, ok)
	require.ElementsMatch(t, []string{".claude", ".cursor"}, entry.AgentDirs)

	// ONE commit covers both dirs.
	count, err := git.Run(centralPath, "rev-list", "--count", "HEAD")
	require.NoError(t, err)
	require.Equal(t, "2", count, "central HEAD should have advanced by exactly one commit")
}

func TestAddDirFlagScopesToSubset(t *testing.T) {
	_, projectRoot := addSandbox(t)
	makeAgentDir(t, projectRoot, ".cursor")
	require.NoError(t, os.WriteFile(
		filepath.Join(projectRoot, ".git", "info", "exclude"),
		[]byte(".claude/\n.cursor/\n"), 0o644))

	// only .cursor; .claude must remain a real dir on disk and not in state.
	res, err := Add(AddOptions{ProjectPath: projectRoot, Dirs: []string{".cursor"}})
	require.NoError(t, err)
	require.Len(t, res.Dirs, 1)
	require.Equal(t, ".cursor", res.Dirs[0].Name)
	require.Equal(t, StatusAdded, res.Dirs[0].Status)

	// .claude still a regular directory (not a symlink) at the project root.
	info, statErr := os.Lstat(filepath.Join(projectRoot, ".claude"))
	require.NoError(t, statErr)
	require.True(t, info.IsDir())
	require.Zero(t, info.Mode()&os.ModeSymlink)

	st, _, err := state.Load()
	require.NoError(t, err)
	entry, _ := st.ProjectEntryFor("my-app")
	require.Equal(t, []string{".cursor"}, entry.AgentDirs)
}

func TestAddDirFlagExtendsTrackedProject(t *testing.T) {
	// onboard .claude first, then run again with --dir .cursor to extend.
	_, projectRoot := addSandbox(t)

	first, err := Add(AddOptions{ProjectPath: projectRoot})
	require.NoError(t, err)
	require.Len(t, first.Dirs, 1)

	makeAgentDir(t, projectRoot, ".cursor")
	// .cursor wasn't ignored at sandbox setup; add the rule so the picker doesn't fire.
	require.NoError(t, os.WriteFile(
		filepath.Join(projectRoot, ".git", "info", "exclude"),
		[]byte(".claude/\n.cursor/\n"), 0o644))

	res, err := Add(AddOptions{ProjectPath: projectRoot, Dirs: []string{".cursor"}})
	require.NoError(t, err)
	require.Len(t, res.Dirs, 1)
	require.Equal(t, ".cursor", res.Dirs[0].Name)
	require.Equal(t, StatusAdded, res.Dirs[0].Status)

	// state now lists both dirs under my-app.
	st, _, err := state.Load()
	require.NoError(t, err)
	entry, _ := st.ProjectEntryFor("my-app")
	require.ElementsMatch(t, []string{".claude", ".cursor"}, entry.AgentDirs)
}

func TestAddDirFlagErrorsOnMissing(t *testing.T) {
	_, projectRoot := addSandbox(t)
	// only .claude exists; --dir .nope should hard-error.
	_, err := Add(AddOptions{ProjectPath: projectRoot, Dirs: []string{".nope"}})
	require.Error(t, err)
	require.Equal(t, lerr.CodeNotFound, lerr.CodeOf(err))
	require.Contains(t, err.Error(), ".nope")
	require.Contains(t, err.Error(), ".claude",
		"error must list what WAS detected so the user can pick a real candidate")
}

func TestAddPartialFailureOneCleanOneSafetyViolation(t *testing.T) {
	centralPath, projectRoot := addSandbox(t)
	makeAgentDir(t, projectRoot, ".cursor")
	require.NoError(t, os.WriteFile(
		filepath.Join(projectRoot, ".git", "info", "exclude"),
		[]byte(".claude/\n.cursor/\n"), 0o644))

	// trip .claude's tracked-files safety gate; .cursor stays clean.
	_, err := git.Run(projectRoot, "add", "-f", ".claude/CLAUDE.md")
	require.NoError(t, err)

	res, err := Add(AddOptions{ProjectPath: projectRoot})
	// per-dir semantics: error code reflects "something was refused",
	// but the result still carries the successful add.
	require.Error(t, err)
	require.Equal(t, lerr.CodeRefusedSafety, lerr.CodeOf(err))
	require.Len(t, res.Dirs, 2)

	statusByName := map[string]DirStatus{}
	for _, d := range res.Dirs {
		statusByName[d.Name] = d.Status
	}
	require.Equal(t, StatusSkippedSafety, statusByName[".claude"])
	require.Equal(t, StatusAdded, statusByName[".cursor"])

	// .cursor IS tracked in state; .claude is NOT.
	st, _, _ := state.Load()
	entry, _ := st.ProjectEntryFor("my-app")
	require.Equal(t, []string{".cursor"}, entry.AgentDirs)

	// one commit covers .cursor only (HEAD advanced by 1).
	count, err := git.Run(centralPath, "rev-list", "--count", "HEAD")
	require.NoError(t, err)
	require.Equal(t, "2", count)
}

func TestAddPickerSkipsAlreadyIgnoredDir(t *testing.T) {
	_, projectRoot := addSandbox(t)
	makeAgentDir(t, projectRoot, ".cursor")
	// .claude is ignored by sandbox; .cursor is NOT. Only one picker prompt.
	calls := stubPicker(t, prompt.TargetRepoGitignore, nil)

	_, err := Add(AddOptions{ProjectPath: projectRoot})
	require.NoError(t, err)
	require.Equal(t, 1, *calls,
		"picker fires once per non-ignored dir, not once per Add()")
}

func TestAddPickerFiresOncePerDirWhenNoneIgnored(t *testing.T) {
	_, projectRoot := addSandbox(t)
	makeAgentDir(t, projectRoot, ".cursor")
	// neither dir is ignored: blank info/exclude.
	unignoreSandbox(t, projectRoot)

	calls := stubPicker(t, prompt.TargetRepoGitignore, nil)
	_, err := Add(AddOptions{ProjectPath: projectRoot})
	require.NoError(t, err)
	require.Equal(t, 2, *calls,
		"two non-ignored dirs means two picker prompts")
}

func TestAddPickerDismissalMidLoopAbortsWithoutOnboarding(t *testing.T) {
	// design lock-in: when the picker is dismissed for a later dir in a
	// multi-dir run, the whole Add aborts (no central / state / commit
	// side effects) but the .gitignore entry from any earlier picker
	// write persists. The user's prior pick is a deliberate decision and
	// retrying shouldn't make them re-pick it.
	centralPath, projectRoot := addSandbox(t)
	makeAgentDir(t, projectRoot, ".cursor")
	unignoreSandbox(t, projectRoot)

	// pick repo-gitignore for .claude; dismiss when called for .cursor.
	// keying off the dir name (not call count) keeps the test honest if
	// cfg.AgentDirs's default order ever flips - we're asserting
	// behavior about which dir gets dismissed, not which call index.
	prev := pickGitignoreTarget
	pickGitignoreTarget = func(dirName string) (prompt.GitignoreTarget, error) {
		if dirName == ".cursor" {
			return 0, lerr.New(lerr.CodeRefusedSafety, "picker dismissed")
		}
		return prompt.TargetRepoGitignore, nil
	}
	t.Cleanup(func() { pickGitignoreTarget = prev })

	headBefore, err := git.Run(centralPath, "rev-parse", "HEAD")
	require.NoError(t, err)

	_, err = Add(AddOptions{ProjectPath: projectRoot})
	require.Error(t, err)
	require.Equal(t, lerr.CodeRefusedSafety, lerr.CodeOf(err))

	// no project tracked, no symlinks planted, no commit landed.
	st, _, err := state.Load()
	require.NoError(t, err)
	_, ok := st.ProjectEntryFor("my-app")
	require.False(t, ok, "dismissal must abort before state is touched")

	headAfter, err := git.Run(centralPath, "rev-parse", "HEAD")
	require.NoError(t, err)
	require.Equal(t, headBefore, headAfter, "no commit on central")

	for _, name := range []string{".claude", ".cursor"} {
		info, statErr := os.Lstat(filepath.Join(projectRoot, name))
		require.NoError(t, statErr)
		require.True(t, info.IsDir())
		require.Zero(t, info.Mode()&os.ModeSymlink,
			name+" should still be a real dir, not a symlink into central")
	}

	// but the gitignore entry the user actively picked for the first dir
	// IS persisted - that decision survives the abort so a retry doesn't
	// re-prompt for it.
	got, err := os.ReadFile(filepath.Join(projectRoot, ".gitignore"))
	require.NoError(t, err)
	require.Contains(t, string(got), ".claude",
		"earlier picker write must persist even when a later dir aborts the run")
}

func TestPathErrorsWhenMultipleDirsAndNoFlag(t *testing.T) {
	_, projectRoot := addSandbox(t)
	makeAgentDir(t, projectRoot, ".cursor")
	require.NoError(t, os.WriteFile(
		filepath.Join(projectRoot, ".git", "info", "exclude"),
		[]byte(".claude/\n.cursor/\n"), 0o644))
	_, err := Add(AddOptions{ProjectPath: projectRoot})
	require.NoError(t, err)

	_, err = Path("my-app", "")
	require.Error(t, err)
	require.Equal(t, lerr.CodeConflict, lerr.CodeOf(err))
	require.Contains(t, err.Error(), "--dir")
	require.Contains(t, err.Error(), ".claude")
	require.Contains(t, err.Error(), ".cursor")
}

func TestPathSelectsSpecificDir(t *testing.T) {
	centralPath, projectRoot := addSandbox(t)
	makeAgentDir(t, projectRoot, ".cursor")
	require.NoError(t, os.WriteFile(
		filepath.Join(projectRoot, ".git", "info", "exclude"),
		[]byte(".claude/\n.cursor/\n"), 0o644))
	_, err := Add(AddOptions{ProjectPath: projectRoot})
	require.NoError(t, err)

	got, err := Path("my-app", ".cursor")
	require.NoError(t, err)
	require.Equal(t,
		filepath.Join(centralPath, "projects", "my-app", ".cursor"),
		got)
}

func TestPathDirNotTracked(t *testing.T) {
	_, projectRoot := addSandbox(t)
	_, err := Add(AddOptions{ProjectPath: projectRoot})
	require.NoError(t, err)

	_, err = Path("my-app", ".cursor")
	require.Error(t, err)
	require.Equal(t, lerr.CodeNotFound, lerr.CodeOf(err))
	require.Contains(t, err.Error(), ".cursor")
	require.Contains(t, err.Error(), ".claude",
		"error must list what IS tracked so the user picks a real dir")
}

func TestAddRejectsOldSchemaState(t *testing.T) {
	// v1 state files are no longer supported. The user is expected to
	// delete the stale file and re-run; the migration code is gone.
	_, projectRoot := addSandbox(t)

	statePath, err := state.Path()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(statePath), 0o755))
	v1 := "schema_version = 1\n\n[paths]\nmy-app = \"" + projectRoot + "\"\n"
	require.NoError(t, os.WriteFile(statePath, []byte(v1), 0o644))

	_, err = Add(AddOptions{ProjectPath: projectRoot})
	require.Error(t, err)
	require.Equal(t, lerr.CodeInconsistency, lerr.CodeOf(err))
}
