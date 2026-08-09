package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nathanclairmonte/lore/internal/git"
	"github.com/stretchr/testify/require"
)

// claudeTarget is the central agent-dir path my-app's .claude symlink is
// expected to point at, given a sandbox whose HOME is home.
func claudeTarget(home string) string {
	return filepath.Join(home, "agent-context", "projects", "my-app", ".claude")
}

func TestLoreLink_RecreatesMissingSymlinkByName(t *testing.T) {
	home, projectRoot := postAddCmdSandbox(t)
	link := filepath.Join(projectRoot, ".claude")

	// the symlink went missing (fresh clone / lost state simulation).
	require.NoError(t, os.Remove(link))

	require.Equal(t, 0, run([]string{"link", "my-app"}))

	target, err := os.Readlink(link)
	require.NoError(t, err)
	require.Equal(t, claudeTarget(home), target)
}

func TestLoreLink_AlreadyLinkedIsIdempotent(t *testing.T) {
	_, _ = postAddCmdSandbox(t)

	// the symlink add created is already correct: link is a no-op.
	require.Equal(t, 0, run([]string{"link", "my-app"}))
	require.Equal(t, 0, run([]string{"link", "my-app"}),
		"idempotent re-run must exit 0")
}

func TestLoreLink_NamedWithPathRelocatesAndRecords(t *testing.T) {
	home, _ := postAddCmdSandbox(t)

	relocated := filepath.Join(home, "code", "relocated")
	require.NoError(t, os.MkdirAll(relocated, 0o755))

	require.Equal(t, 0, run([]string{"link", "my-app", "--path", relocated}))

	// symlink created at the new path, pointing into central.
	target, err := os.Readlink(filepath.Join(relocated, ".claude"))
	require.NoError(t, err)
	require.Equal(t, claudeTarget(home), target)

	// local state now records the new path.
	stateBytes, _ := os.ReadFile(filepath.Join(home, ".local/state", "lore", "state.toml"))
	require.Contains(t, string(stateBytes), relocated,
		"state.toml should record the relocated path")
}

func TestLoreLink_NotTrackedExits3(t *testing.T) {
	postAddCmdSandbox(t)
	require.Equal(t, 3, run([]string{"link", "ghost"}))
}

func TestLoreLink_NoRecordedPathNoFlagExits3(t *testing.T) {
	_, _ = postAddCmdSandbox(t)

	// unlink drops local state but leaves the project tracked in central.
	require.Equal(t, 0, run([]string{"unlink", "my-app"}))

	// with no recorded path and no --path, link can't know where to link.
	require.Equal(t, 3, run([]string{"link", "my-app"}))
}

func TestLoreLink_DriftBRealDirectoryRefuses(t *testing.T) {
	home, projectRoot := postAddCmdSandbox(t)
	link := filepath.Join(projectRoot, ".claude")

	// a real directory now sits where the symlink belongs (drift B).
	require.NoError(t, os.Remove(link))
	require.NoError(t, os.Mkdir(link, 0o755))

	require.Equal(t, 5, run([]string{"link", "my-app"}))

	// central is untouched; the real dir is not clobbered.
	require.DirExists(t, claudeTarget(home))
	info, err := os.Lstat(link)
	require.NoError(t, err)
	require.Zero(t, info.Mode()&os.ModeSymlink, "real dir must be left in place")
}

func TestLoreLink_DriftCWrongTargetRefuses(t *testing.T) {
	_, projectRoot := postAddCmdSandbox(t)
	link := filepath.Join(projectRoot, ".claude")

	// a symlink pointing somewhere unexpected (drift C).
	require.NoError(t, os.Remove(link))
	require.NoError(t, os.Symlink(filepath.Join(projectRoot, "elsewhere"), link))

	require.Equal(t, 5, run([]string{"link", "my-app"}))

	// the foreign symlink is left exactly as it was.
	target, err := os.Readlink(link)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(projectRoot, "elsewhere"), target)
}

func TestLoreLink_PathRequiresNameExits1(t *testing.T) {
	_, projectRoot := postAddCmdSandbox(t)
	require.Equal(t, 1, run([]string{"link", "--path", projectRoot}))
}

func TestLoreLink_BareFromCwdAncestryMatch(t *testing.T) {
	_, projectRoot := postAddCmdSandbox(t)
	link := filepath.Join(projectRoot, ".claude")
	require.NoError(t, os.Remove(link))

	t.Chdir(projectRoot)

	// recorded path equals cwd: ancestry match resolves the project.
	require.Equal(t, 0, run([]string{"link"}))
	info, err := os.Lstat(link)
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeSymlink)
}

func TestLoreLink_BareFromCwdBasenameMatch(t *testing.T) {
	home, projectRoot := postAddCmdSandbox(t)

	// drop local state so ancestry can't match; central still tracks my-app
	// and cwd's basename is "my-app", so basename matching resolves it.
	require.Equal(t, 0, run([]string{"unlink", "my-app"}))

	t.Chdir(projectRoot)
	require.Equal(t, 0, run([]string{"link"}))

	target, err := os.Readlink(filepath.Join(projectRoot, ".claude"))
	require.NoError(t, err)
	require.Equal(t, claudeTarget(home), target)
}

func TestLoreLink_BareUnrelatedBasenameRefuses(t *testing.T) {
	home, _ := postAddCmdSandbox(t)

	// drop local state so ancestry can't match; central still tracks my-app.
	require.Equal(t, 0, run([]string{"unlink", "my-app"}))

	// a directory that merely shares the project's basename but lives OUTSIDE
	// any configured projects_root (~/code). a bare link must not guess this
	// unrelated dir is the project and drop symlinks into it.
	unrelated := filepath.Join(home, "scratch", "my-app")
	require.NoError(t, os.MkdirAll(unrelated, 0o755))
	t.Chdir(unrelated)

	require.Equal(t, 3, run([]string{"link"}))

	_, err := os.Lstat(filepath.Join(unrelated, ".claude"))
	require.True(t, os.IsNotExist(err),
		"a name-matching dir outside projects_roots must not be linked")
}

// postAddMultiDirCmdSandbox onboards my-app with two agent dirs (.claude
// and .cursor) so multi-dir link behavior can be exercised end to end.
func postAddMultiDirCmdSandbox(t *testing.T) (home, projectRoot string) {
	t.Helper()
	home = sandbox(t)
	t.Setenv("GIT_AUTHOR_NAME", "Test Author")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Author")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")

	require.Equal(t, 0, run([]string{"init"}))

	projectRoot = filepath.Join(home, "code", "my-app")
	require.NoError(t, os.MkdirAll(projectRoot, 0o755))
	_, err := git.Run(projectRoot, "init")
	require.NoError(t, err)
	for _, dir := range []string{".claude", ".cursor"} {
		require.NoError(t, os.Mkdir(filepath.Join(projectRoot, dir), 0o755))
		require.NoError(t, os.WriteFile(
			filepath.Join(projectRoot, dir, "notes.md"), []byte("x"), 0o644))
	}
	require.NoError(t, os.WriteFile(
		filepath.Join(projectRoot, ".git", "info", "exclude"),
		[]byte(".claude/\n.cursor/\n"), 0o644))
	require.Equal(t, 0, run([]string{"add", projectRoot}))
	for _, dir := range []string{".claude", ".cursor"} {
		makeLegacySymlink(t, home, projectRoot, dir)
	}
	return home, projectRoot
}

func TestLoreLink_MultiDirLinksAll(t *testing.T) {
	home, projectRoot := postAddMultiDirCmdSandbox(t)

	for _, dir := range []string{".claude", ".cursor"} {
		require.NoError(t, os.Remove(filepath.Join(projectRoot, dir)))
	}

	require.Equal(t, 0, run([]string{"link", "my-app"}))

	for _, dir := range []string{".claude", ".cursor"} {
		target, err := os.Readlink(filepath.Join(projectRoot, dir))
		require.NoError(t, err)
		require.Equal(t,
			filepath.Join(home, "agent-context", "projects", "my-app", dir), target)
	}
}

func TestLoreLink_MultiDirPartialRefusalLinksSafeDir(t *testing.T) {
	home, projectRoot := postAddMultiDirCmdSandbox(t)

	// .claude is missing (linkable); .cursor is blocked by a real dir (drift B).
	require.NoError(t, os.Remove(filepath.Join(projectRoot, ".claude")))
	require.NoError(t, os.Remove(filepath.Join(projectRoot, ".cursor")))
	require.NoError(t, os.Mkdir(filepath.Join(projectRoot, ".cursor"), 0o755))

	// aggregated refusal exits 5, but the safe dir still links.
	require.Equal(t, 5, run([]string{"link", "my-app"}))

	target, err := os.Readlink(filepath.Join(projectRoot, ".claude"))
	require.NoError(t, err)
	require.Equal(t, claudeTarget(home), target)

	info, err := os.Lstat(filepath.Join(projectRoot, ".cursor"))
	require.NoError(t, err)
	require.Zero(t, info.Mode()&os.ModeSymlink, "drift-B dir must be left in place")
}

// --- lore link --all (slice 11) ---

func TestLoreLinkAll_RelinksFromRecordedPath(t *testing.T) {
	home, projectRoot := postAddCmdSandbox(t)
	link := filepath.Join(projectRoot, ".claude")
	require.NoError(t, os.Remove(link))

	require.Equal(t, 0, run([]string{"link", "--all"}))

	target, err := os.Readlink(link)
	require.NoError(t, err)
	require.Equal(t, claudeTarget(home), target)
}

func TestLoreLinkAll_AlreadyLinkedExit0(t *testing.T) {
	_, _ = postAddCmdSandbox(t)
	require.Equal(t, 0, run([]string{"link", "--all"}))
}

func TestLoreLinkAll_FoundByScanRecordsPath(t *testing.T) {
	home, projectRoot := postAddCmdSandbox(t)

	// drop local state but keep the project tracked in central and its
	// checkout on disk under ~/code: the scan must relocate it.
	require.Equal(t, 0, run([]string{"unlink", "my-app"}))

	require.Equal(t, 0, run([]string{"link", "--all"}))

	target, err := os.Readlink(filepath.Join(projectRoot, ".claude"))
	require.NoError(t, err)
	require.Equal(t, claudeTarget(home), target)

	stateBytes, _ := os.ReadFile(filepath.Join(home, ".local/state", "lore", "state.toml"))
	require.Contains(t, string(stateBytes), projectRoot,
		"scan-found project should be recorded back into state")
}

func TestLoreLinkAll_MissingIsNotAFailure(t *testing.T) {
	home, projectRoot := postAddCmdSandbox(t)

	// tracked in central, no state, and no checkout anywhere on disk.
	require.Equal(t, 0, run([]string{"unlink", "my-app"}))
	require.NoError(t, os.RemoveAll(projectRoot))

	// missing is information, not failure: exit 0.
	require.Equal(t, 0, run([]string{"link", "--all"}))

	stateBytes, _ := os.ReadFile(filepath.Join(home, ".local/state", "lore", "state.toml"))
	require.NotContains(t, string(stateBytes), "my-app",
		"a project that couldn't be located must not be recorded")
}

func TestLoreLinkAll_AmbiguousIsNotAFailure(t *testing.T) {
	home, projectRoot := postAddCmdSandbox(t)

	// drop state; the original checkout stays under ~/code/my-app.
	require.Equal(t, 0, run([]string{"unlink", "my-app"}))

	// a second checkout with the same basename under a different root.
	rootB := filepath.Join(home, "work")
	require.NoError(t, os.MkdirAll(filepath.Join(rootB, "my-app"), 0o755))

	// point config at both roots so the scan finds two candidates.
	writeConfigRoots(t, home, filepath.Join(home, "code"), rootB)

	// ambiguous is reported, not failed: exit 0, and nothing is linked.
	require.Equal(t, 0, run([]string{"link", "--all"}))

	_, err := os.Lstat(filepath.Join(projectRoot, ".claude"))
	require.True(t, os.IsNotExist(err),
		"an ambiguous project must not be linked unattended")
}

func TestLoreLinkAll_DriftRefusalExits5(t *testing.T) {
	_, projectRoot := postAddCmdSandbox(t)
	link := filepath.Join(projectRoot, ".claude")

	// recorded path is valid but a real dir blocks the symlink (drift B).
	require.NoError(t, os.Remove(link))
	require.NoError(t, os.Mkdir(link, 0o755))

	require.Equal(t, 5, run([]string{"link", "--all"}))
}

func TestLoreLinkAll_NameMutuallyExclusive(t *testing.T) {
	postAddCmdSandbox(t)
	require.Equal(t, 1, run([]string{"link", "--all", "my-app"}))
}

func TestLoreLinkAll_PathMutuallyExclusive(t *testing.T) {
	home, _ := postAddCmdSandbox(t)
	require.Equal(t, 1, run([]string{"link", "--all", "--path", home}))
}

func TestLoreLinkAll_EmptyCentralExit0(t *testing.T) {
	sandbox(t)
	require.Equal(t, 0, run([]string{"init"}))
	require.Equal(t, 0, run([]string{"link", "--all"}))
}

// writeConfigRoots overwrites the sandbox config so projects_roots points
// at the given roots (absolute). central_repo is kept at the sandbox
// default; every other key falls back to the embedded defaults on load.
func writeConfigRoots(t *testing.T, home string, roots ...string) {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "central_repo = %q\n", filepath.Join(home, "agent-context"))
	b.WriteString("projects_roots = [\n")
	for _, r := range roots {
		fmt.Fprintf(&b, "  %q,\n", r)
	}
	b.WriteString("]\n")
	b.WriteString("projects_root_depth = 2\n")
	cfgPath := filepath.Join(home, ".config", "lore", "config.toml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(b.String()), 0o644))
}
