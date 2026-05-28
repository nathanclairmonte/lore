package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/nathanclairmonte/lore/internal/git"
	"github.com/nathanclairmonte/lore/internal/output"
	"github.com/stretchr/testify/require"
)

// capturedErr swaps output.Err for a buffer so tests can assert the
// human-mode error text PrintError writes to stderr.
func capturedErr(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := output.Err
	output.Err = buf
	t.Cleanup(func() { output.Err = prev })
	return buf
}

// addCmdProject onboards a second tracked project named name under
// HOME/code/<name> so conflict tests have something to collide with.
func addCmdProject(t *testing.T, home, name string) string {
	t.Helper()
	root := filepath.Join(home, "code", name)
	require.NoError(t, os.MkdirAll(root, 0o755))
	_, err := git.Run(root, "init")
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(root, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(root, ".claude", "CLAUDE.md"), []byte("context"), 0o644))
	require.NoError(t, os.WriteFile(
		filepath.Join(root, ".git", "info", "exclude"), []byte(".claude/\n"), 0o644))
	require.Equal(t, 0, run([]string{"add", root}))
	return root
}

// addCmdSecondDir extends the my-app project with a .cursor agent dir.
func addCmdSecondDir(t *testing.T, projectRoot string) {
	t.Helper()
	require.NoError(t, os.Mkdir(filepath.Join(projectRoot, ".cursor"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(projectRoot, ".cursor", "rules.md"), []byte("rules"), 0o644))
	require.NoError(t, os.WriteFile(
		filepath.Join(projectRoot, ".git", "info", "exclude"),
		[]byte(".claude/\n.cursor/\n"), 0o644))
	require.Equal(t, 0, run([]string{"add", "--dir", ".cursor", projectRoot}))
}

func centralDir(home string) string { return filepath.Join(home, "agent-context") }

func readState(t *testing.T, home string) string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(home, ".local/state", "lore", "state.toml"))
	return string(b)
}

func TestLoreMv_DefaultSingleDir(t *testing.T) {
	home, projectRoot := postAddCmdSandbox(t)

	require.Equal(t, 0, run([]string{"mv", "my-app", "bar"}))

	// central dir renamed.
	require.DirExists(t, filepath.Join(centralDir(home), "projects", "bar"))
	require.NoDirExists(t, filepath.Join(centralDir(home), "projects", "my-app"))

	// symlink repointed in place at the unchanged recorded path.
	target, err := os.Readlink(filepath.Join(projectRoot, ".claude"))
	require.NoError(t, err)
	require.Equal(t,
		filepath.Join(centralDir(home), "projects", "bar", ".claude"), target)

	// state key renamed; path unchanged.
	st := readState(t, home)
	require.Contains(t, st, "[projects.bar]")
	require.NotContains(t, st, "[projects.my-app]")

	// commit landed with the mv template.
	msg, err := git.Run(centralDir(home), "log", "-1", "--pretty=%s")
	require.NoError(t, err)
	require.Equal(t, "refactor: rename my-app to bar", msg)
}

func TestLoreMv_DefaultMultiAgentDir(t *testing.T) {
	home, projectRoot := postAddCmdSandbox(t)
	addCmdSecondDir(t, projectRoot)

	require.Equal(t, 0, run([]string{"mv", "my-app", "bar"}))

	for _, dir := range []string{".claude", ".cursor"} {
		target, err := os.Readlink(filepath.Join(projectRoot, dir))
		require.NoError(t, err)
		require.Equal(t,
			filepath.Join(centralDir(home), "projects", "bar", dir), target,
			"every agent-dir symlink must repoint to projects/bar")
	}
}

func TestLoreMv_PathModeRelocates(t *testing.T) {
	home, projectRoot := postAddCmdSandbox(t)
	newPath := filepath.Join(home, "code", "bar")
	require.NoError(t, os.MkdirAll(newPath, 0o755))

	require.Equal(t, 0, run([]string{"mv", "my-app", "bar", "--path", newPath}))

	// old symlink gone, new one created under the new path.
	_, err := os.Lstat(filepath.Join(projectRoot, ".claude"))
	require.True(t, os.IsNotExist(err), "old-path symlink must be removed")
	target, err := os.Readlink(filepath.Join(newPath, ".claude"))
	require.NoError(t, err)
	require.Equal(t,
		filepath.Join(centralDir(home), "projects", "bar", ".claude"), target)

	// state path updated.
	require.Contains(t, readState(t, home), filepath.Join("code", "bar"))
	require.DirExists(t, filepath.Join(centralDir(home), "projects", "bar"))
}

func TestLoreMv_PathModeLeavesForeignOldPathWithNote(t *testing.T) {
	home, projectRoot := postAddCmdSandbox(t)

	// drift B at the old path: a real directory where the symlink was.
	oldLink := filepath.Join(projectRoot, ".claude")
	require.NoError(t, os.Remove(oldLink))
	require.NoError(t, os.MkdirAll(oldLink, 0o755))

	newPath := filepath.Join(home, "code", "bar")
	require.NoError(t, os.MkdirAll(newPath, 0o755))

	buf := capturedOut(t)
	require.Equal(t, 0, run([]string{"mv", "my-app", "bar", "--path", newPath}),
		"--path is lenient at the old path: a foreign dir must not refuse")

	// old real dir untouched (still a real dir, not a symlink).
	info, err := os.Lstat(oldLink)
	require.NoError(t, err)
	require.Zero(t, info.Mode()&os.ModeSymlink, "old real dir must be left in place")

	// new symlink still created.
	target, err := os.Readlink(filepath.Join(newPath, ".claude"))
	require.NoError(t, err)
	require.Equal(t,
		filepath.Join(centralDir(home), "projects", "bar", ".claude"), target)

	require.Contains(t, buf.String(), "left in place")
}

func TestLoreMv_PathModeNewPathOccupiedExits5(t *testing.T) {
	home, projectRoot := postAddCmdSandbox(t)

	newPath := filepath.Join(home, "code", "bar")
	require.NoError(t, os.MkdirAll(filepath.Join(newPath, ".claude"), 0o755))

	require.Equal(t, 5, run([]string{"mv", "my-app", "bar", "--path", newPath}),
		"a real dir in the way at the new path must refuse with exit 5")

	// all-or-nothing: central not renamed, old symlink intact.
	require.DirExists(t, filepath.Join(centralDir(home), "projects", "my-app"))
	require.NoDirExists(t, filepath.Join(centralDir(home), "projects", "bar"))
	info, err := os.Lstat(filepath.Join(projectRoot, ".claude"))
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeSymlink)
}

func TestLoreMv_OldAbsentExits3(t *testing.T) {
	postAddCmdSandbox(t)
	require.Equal(t, 3, run([]string{"mv", "nonexistent", "bar"}))
}

func TestLoreMv_NewConflictExits4(t *testing.T) {
	home, _ := postAddCmdSandbox(t)
	addCmdProject(t, home, "second-app")

	require.Equal(t, 4, run([]string{"mv", "my-app", "second-app"}))
}

func TestLoreMv_SameNameExits4(t *testing.T) {
	postAddCmdSandbox(t)
	errBuf := capturedErr(t)

	require.Equal(t, 4, run([]string{"mv", "my-app", "my-app"}))
	require.Contains(t, errBuf.String(), "already named")
}

func TestLoreMv_InvalidNewNameExits1(t *testing.T) {
	postAddCmdSandbox(t)
	// leading dot is rejected by ValidateProjectName (CodeGenericError → 1).
	require.Equal(t, 1, run([]string{"mv", "my-app", ".bad"}))
}

func TestLoreMv_DefaultDriftBExits5(t *testing.T) {
	home, projectRoot := postAddCmdSandbox(t)

	// drift B: real dir where the symlink was.
	link := filepath.Join(projectRoot, ".claude")
	require.NoError(t, os.Remove(link))
	require.NoError(t, os.MkdirAll(link, 0o755))

	buf := capturedOut(t)
	require.Equal(t, 5, run([]string{"mv", "my-app", "bar"}))

	// a refusal must NOT print a "Renamed" success headline.
	require.NotContains(t, buf.String(), "Renamed")

	// central + state untouched (refusal before any side effect).
	require.DirExists(t, filepath.Join(centralDir(home), "projects", "my-app"))
	require.NoDirExists(t, filepath.Join(centralDir(home), "projects", "bar"))
	require.Contains(t, readState(t, home), "[projects.my-app]")
}

func TestLoreMv_DefaultDriftCExits5(t *testing.T) {
	home, projectRoot := postAddCmdSandbox(t)

	// drift C: symlink pointing somewhere unexpected.
	link := filepath.Join(projectRoot, ".claude")
	require.NoError(t, os.Remove(link))
	bogus := filepath.Join(home, "elsewhere")
	require.NoError(t, os.MkdirAll(bogus, 0o755))
	require.NoError(t, os.Symlink(bogus, link))

	require.Equal(t, 5, run([]string{"mv", "my-app", "bar"}))
	require.DirExists(t, filepath.Join(centralDir(home), "projects", "my-app"))
	require.Contains(t, readState(t, home), "[projects.my-app]")
}

func TestLoreMv_DefaultDriftARecreates(t *testing.T) {
	home, projectRoot := postAddCmdSandbox(t)

	// drift A: symlink missing. mv should recover by creating the new one.
	require.NoError(t, os.Remove(filepath.Join(projectRoot, ".claude")))

	require.Equal(t, 0, run([]string{"mv", "my-app", "bar"}))

	target, err := os.Readlink(filepath.Join(projectRoot, ".claude"))
	require.NoError(t, err)
	require.Equal(t,
		filepath.Join(centralDir(home), "projects", "bar", ".claude"), target)
	require.DirExists(t, filepath.Join(centralDir(home), "projects", "bar"))
	require.Contains(t, readState(t, home), "[projects.bar]")
}

func TestLoreMv_DirtyCentralExits5(t *testing.T) {
	home, projectRoot := postAddCmdSandbox(t)

	// uncommitted edit under the central project dir → refuse.
	dirty := filepath.Join(centralDir(home), "projects", "my-app", ".claude", "CLAUDE.md")
	require.NoError(t, os.WriteFile(dirty, []byte("edited locally"), 0o644))

	require.Equal(t, 5, run([]string{"mv", "my-app", "bar"}))

	// nothing renamed; edit survives; local side untouched.
	require.DirExists(t, filepath.Join(centralDir(home), "projects", "my-app"))
	require.NoDirExists(t, filepath.Join(centralDir(home), "projects", "bar"))
	info, err := os.Lstat(filepath.Join(projectRoot, ".claude"))
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeSymlink)
	got, err := os.ReadFile(dirty)
	require.NoError(t, err)
	require.Equal(t, "edited locally", string(got))
}

func TestLoreMv_NotLinkedHereCentralOnly(t *testing.T) {
	home, projectRoot := postAddCmdSandbox(t)

	// unlink first: central still has projects/my-app, but local state has
	// no entry. mv must still rename in central.
	require.Equal(t, 0, run([]string{"unlink", "my-app"}))

	buf := capturedOut(t)
	require.Equal(t, 0, run([]string{"mv", "my-app", "bar"}))

	require.DirExists(t, filepath.Join(centralDir(home), "projects", "bar"))
	require.NoDirExists(t, filepath.Join(centralDir(home), "projects", "my-app"))
	msg, err := git.Run(centralDir(home), "log", "-1", "--pretty=%s")
	require.NoError(t, err)
	require.Equal(t, "refactor: rename my-app to bar", msg)
	require.Contains(t, buf.String(), "no symlinks on this machine")

	// no symlink work happened on disk.
	_, err = os.Lstat(filepath.Join(projectRoot, ".claude"))
	require.True(t, os.IsNotExist(err))
}

func TestLoreMv_NeverRenamesOnDiskProjectDir(t *testing.T) {
	// default mode: the on-disk project dir keeps its basename.
	home, projectRoot := postAddCmdSandbox(t)
	require.Equal(t, 0, run([]string{"mv", "my-app", "bar"}))
	require.Equal(t, "my-app", filepath.Base(projectRoot))
	require.DirExists(t, projectRoot)

	// --path mode: the OLD project dir still exists with its basename too;
	// only the symlink inside it moved.
	newPath := filepath.Join(home, "code", "elsewhere")
	require.NoError(t, os.MkdirAll(newPath, 0o755))
	require.Equal(t, 0, run([]string{"mv", "bar", "baz", "--path", newPath}))
	require.DirExists(t, projectRoot)
	require.Equal(t, "my-app", filepath.Base(projectRoot))
}

func TestLoreMv_CommitFailureRollsBackEverything(t *testing.T) {
	home, projectRoot := postAddCmdSandbox(t)

	// failing pre-commit hook: c.Commit fails after the destructive
	// sequence landed. the LIFO drain must restore every side effect.
	hooksDir := filepath.Join(centralDir(home), ".git", "hooks")
	require.NoError(t, os.MkdirAll(hooksDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(hooksDir, "pre-commit"),
		[]byte("#!/bin/sh\nexit 1\n"), 0o755))

	require.NotEqual(t, 0, run([]string{"mv", "my-app", "bar"}))

	// central rename reversed.
	require.DirExists(t, filepath.Join(centralDir(home), "projects", "my-app"))
	require.NoDirExists(t, filepath.Join(centralDir(home), "projects", "bar"))

	// symlink restored and pointing back at projects/my-app.
	target, err := os.Readlink(filepath.Join(projectRoot, ".claude"))
	require.NoError(t, err)
	require.Equal(t,
		filepath.Join(centralDir(home), "projects", "my-app", ".claude"), target)

	// state restored.
	st := readState(t, home)
	require.Contains(t, st, "[projects.my-app]")
	require.NotContains(t, st, "[projects.bar]")
}

func TestLoreMv_PathModeMissingDirExits3(t *testing.T) {
	home, projectRoot := postAddCmdSandbox(t)

	// --path at a directory that doesn't exist: lore plants a symlink inside
	// the new path, it never creates the user's project tree, so this refuses
	// with CodeNotFound (exit 3) before any side effect.
	missing := filepath.Join(home, "code", "does-not-exist")
	require.Equal(t, 3, run([]string{"mv", "my-app", "bar", "--path", missing}))

	// all-or-nothing: central not renamed, old symlink intact.
	require.DirExists(t, filepath.Join(centralDir(home), "projects", "my-app"))
	require.NoDirExists(t, filepath.Join(centralDir(home), "projects", "bar"))
	info, err := os.Lstat(filepath.Join(projectRoot, ".claude"))
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeSymlink)
}

func TestLoreMv_PathModeNotADirExits5(t *testing.T) {
	home, _ := postAddCmdSandbox(t)

	// --path pointing at a regular file: refuse with CodeRefusedSafety (5).
	file := filepath.Join(home, "code", "a-file")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))

	require.Equal(t, 5, run([]string{"mv", "my-app", "bar", "--path", file}))

	require.DirExists(t, filepath.Join(centralDir(home), "projects", "my-app"))
	require.NoDirExists(t, filepath.Join(centralDir(home), "projects", "bar"))
}

func TestLoreMv_PathModeMultiAgentDir(t *testing.T) {
	home, projectRoot := postAddCmdSandbox(t)
	addCmdSecondDir(t, projectRoot)

	newPath := filepath.Join(home, "code", "bar")
	require.NoError(t, os.MkdirAll(newPath, 0o755))

	require.Equal(t, 0, run([]string{"mv", "my-app", "bar", "--path", newPath}))

	for _, dir := range []string{".claude", ".cursor"} {
		// every old-path symlink removed.
		_, err := os.Lstat(filepath.Join(projectRoot, dir))
		require.True(t, os.IsNotExist(err), "old-path symlink must be removed: "+dir)
		// every new-path symlink repoints to projects/bar/<dir>.
		target, err := os.Readlink(filepath.Join(newPath, dir))
		require.NoError(t, err)
		require.Equal(t,
			filepath.Join(centralDir(home), "projects", "bar", dir), target,
			"every relocated symlink must point at projects/bar")
	}
	require.Contains(t, readState(t, home), filepath.Join("code", "bar"))
}

func TestLoreMv_DoesNotSweepUnrelatedStagedChanges(t *testing.T) {
	home, _ := postAddCmdSandbox(t)

	// stage an unrelated file in central, outside projects/my-app. the
	// scoped commit must not fold it into the rename commit.
	stray := filepath.Join(centralDir(home), "STRAY.md")
	require.NoError(t, os.WriteFile(stray, []byte("unrelated"), 0o644))
	_, err := git.Run(centralDir(home), "add", "STRAY.md")
	require.NoError(t, err)

	require.Equal(t, 0, run([]string{"mv", "my-app", "bar"}))

	// the HEAD commit touches only the project paths, never STRAY.md.
	files, err := git.Run(centralDir(home), "show", "--name-only", "--pretty=format:", "HEAD")
	require.NoError(t, err)
	require.NotContains(t, files, "STRAY.md",
		"unrelated staged file must not ride the rename commit")
	require.Contains(t, files, filepath.Join("projects", "bar"))

	// STRAY.md is still staged, waiting for its own commit.
	status, err := git.Run(centralDir(home), "status", "--porcelain", "--", "STRAY.md")
	require.NoError(t, err)
	require.Contains(t, status, "STRAY.md")
}
