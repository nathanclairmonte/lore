package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nathanclairmonte/lore/internal/config"
	"github.com/nathanclairmonte/lore/internal/git"
	"github.com/nathanclairmonte/lore/internal/output"
	"github.com/nathanclairmonte/lore/internal/reconcile"
	"github.com/nathanclairmonte/lore/internal/state"
	"github.com/stretchr/testify/require"
)

func TestLorePushCapturesChangesAndDeletion(t *testing.T) {
	_, projectRoot, centralRoot := trackedCopySandbox(t)
	projectFile := filepath.Join(projectRoot, ".claude", "CLAUDE.md")
	newFile := filepath.Join(projectRoot, ".claude", "new.md")
	require.NoError(t, os.Remove(projectFile))
	require.NoError(t, os.WriteFile(newFile, []byte("new context\n"), 0o755))

	require.Equal(t, 0, run([]string{"push", "--project", "my-app"}))
	require.NoFileExists(t, filepath.Join(centralRoot, "projects", "my-app", ".claude", "CLAUDE.md"))
	centralNew := filepath.Join(centralRoot, "projects", "my-app", ".claude", "new.md")
	info, err := os.Stat(centralNew)
	require.NoError(t, err)
	require.NotZero(t, info.Mode().Perm()&0o111)
	message, err := git.Run(centralRoot, "log", "-1", "--pretty=%s")
	require.NoError(t, err)
	require.Equal(t, "chore: sync (my-app)", message)
	head, err := git.Run(centralRoot, "rev-parse", "HEAD")
	require.NoError(t, err)
	st, _, err := state.Load()
	require.NoError(t, err)
	entry, ok := st.ProjectEntryFor("my-app")
	require.True(t, ok)
	require.Equal(t, head, entry.Checkouts[0].SyncBase)
}

func TestLorePushRefusesCentralAheadWithoutMutation(t *testing.T) {
	_, projectRoot, centralRoot := trackedCopySandbox(t)
	centralFile := filepath.Join(centralRoot, "projects", "my-app", ".claude", "CLAUDE.md")
	require.NoError(t, os.WriteFile(centralFile, []byte("central edit\n"), 0o644))
	commitCentralChange(t, centralRoot)
	headBefore, err := git.Run(centralRoot, "rev-parse", "HEAD")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, ".claude", "local.md"), []byte("local\n"), 0o644))

	require.Equal(t, 5, run([]string{"push", "--project", "my-app"}))
	headAfter, err := git.Run(centralRoot, "rev-parse", "HEAD")
	require.NoError(t, err)
	require.Equal(t, headBefore, headAfter)
	data, err := os.ReadFile(centralFile)
	require.NoError(t, err)
	require.Equal(t, "central edit\n", string(data))
}

func TestLorePushAutoNoopIsSilent(t *testing.T) {
	trackedCopySandbox(t)
	buffer := capturedOut(t)
	require.Equal(t, 0, run([]string{"push", "--auto", "--project", "my-app"}))
	require.Empty(t, buffer.String())
}

func TestLorePushFromCWDAndJSON(t *testing.T) {
	_, projectRoot, centralRoot := trackedCopySandbox(t)
	buffer := capturedOut(t)
	require.NoError(t, os.MkdirAll(filepath.Join(projectRoot, "nested"), 0o755))
	previous, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(filepath.Join(projectRoot, "nested")))
	t.Cleanup(func() { _ = os.Chdir(previous) })
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, ".claude", "CLAUDE.md"), []byte("changed\n"), 0o644))

	require.Equal(t, 0, run([]string{"--json", "push", "--project-from-cwd"}))
	var got output.PushResult
	require.NoError(t, json.Unmarshal(buffer.Bytes(), &got))
	require.Len(t, got.CommitSHA, 40)
	require.Equal(t, output.PushPushed, got.Projects[0].Checkouts[0].Status)
	require.FileExists(t, filepath.Join(centralRoot, "projects", "my-app", ".claude", "CLAUDE.md"))
}

func TestLorePushAutoFailureSurfacesOnNextInteractiveCommand(t *testing.T) {
	home, _, centralRoot := trackedCopySandbox(t)
	require.NoError(t, os.WriteFile(filepath.Join(centralRoot, "projects", "my-app", ".claude", "CLAUDE.md"), []byte("central\n"), 0o644))
	commitCentralChange(t, centralRoot)
	require.Equal(t, 5, run([]string{"push", "--auto", "--project", "my-app"}))
	require.FileExists(t, filepath.Join(home, ".local", "state", "lore", "last-sync-error.txt"))

	errBuffer := capturedErr(t)
	require.Equal(t, 0, run([]string{"status", "my-app"}))
	require.Contains(t, errBuffer.String(), "Previous automatic push failed")
	require.NoFileExists(t, filepath.Join(home, ".local", "state", "lore", "last-sync-error.txt"))
}

func TestLorePushRefusesCompetingCheckoutChanges(t *testing.T) {
	_, projectRoot, centralRoot := trackedCopySandbox(t)
	secondRoot := filepath.Join(filepath.Dir(projectRoot), "my-app-worktree")
	require.NoError(t, os.MkdirAll(filepath.Join(secondRoot, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(secondRoot, ".claude", "CLAUDE.md"), []byte("second\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, ".claude", "CLAUDE.md"), []byte("first\n"), 0o644))
	head, err := git.Run(centralRoot, "rev-parse", "HEAD")
	require.NoError(t, err)
	st, _, err := state.Load()
	require.NoError(t, err)
	require.NoError(t, st.RecordCheckout("my-app", secondRoot, head))
	require.NoError(t, state.Save(st))

	require.Equal(t, 5, run([]string{"push", "--project", "my-app"}))
	headAfter, err := git.Run(centralRoot, "rev-parse", "HEAD")
	require.NoError(t, err)
	require.Equal(t, head, headAfter)
}

func TestLorePushBroadPreflightIsAtomic(t *testing.T) {
	home, projectRoot, centralRoot := trackedCopySandbox(t)
	secondRoot := filepath.Join(home, "code", "second-app")
	require.NoError(t, os.MkdirAll(filepath.Join(secondRoot, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(secondRoot, ".claude", "CLAUDE.md"), []byte("second base\n"), 0o644))
	require.NoError(t, git.Init(secondRoot))
	require.NoError(t, os.WriteFile(filepath.Join(secondRoot, ".git", "info", "exclude"), []byte(".claude/\n"), 0o644))
	require.Equal(t, 0, run([]string{"add", secondRoot}))
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, ".claude", "local.md"), []byte("safe change\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(centralRoot, "projects", "second-app", ".claude", "CLAUDE.md"), []byte("central advance\n"), 0o644))
	commitCentralChange(t, centralRoot)
	head, err := git.Run(centralRoot, "rev-parse", "HEAD")
	require.NoError(t, err)

	require.Equal(t, 5, run([]string{"push"}))
	headAfter, err := git.Run(centralRoot, "rev-parse", "HEAD")
	require.NoError(t, err)
	require.Equal(t, head, headAfter)
	require.NoFileExists(t, filepath.Join(centralRoot, "projects", "my-app", ".claude", "local.md"))
}

func TestLorePushCommitFailureRestoresCentralAndIndex(t *testing.T) {
	_, projectRoot, centralRoot := trackedCopySandbox(t)
	centralFile := filepath.Join(centralRoot, "projects", "my-app", ".claude", "CLAUDE.md")
	before, err := os.ReadFile(centralFile)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, ".claude", "CLAUDE.md"), []byte("local change\n"), 0o644))
	hook := filepath.Join(centralRoot, ".git", "hooks", "pre-commit")
	require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755))
	head, err := git.Run(centralRoot, "rev-parse", "HEAD")
	require.NoError(t, err)

	require.NotEqual(t, 0, run([]string{"push", "--project", "my-app"}))
	after, err := os.ReadFile(centralFile)
	require.NoError(t, err)
	require.Equal(t, before, after)
	headAfter, err := git.Run(centralRoot, "rev-parse", "HEAD")
	require.NoError(t, err)
	require.Equal(t, head, headAfter)
	status, err := git.Run(centralRoot, "status", "--porcelain", "--", "projects/my-app")
	require.NoError(t, err)
	require.Empty(t, status)
}

func TestLorePushCopiesOnlyProjectAheadAgentDirs(t *testing.T) {
	_, projectRoot, centralRoot := trackedCopySandbox(t)
	require.NoError(t, os.Mkdir(filepath.Join(projectRoot, ".cursor"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, ".cursor", "RULES.md"), []byte("cursor base\n"), 0o644))
	exclude := filepath.Join(projectRoot, ".git", "info", "exclude")
	require.NoError(t, os.WriteFile(exclude, []byte(".claude/\n.cursor/\n"), 0o644))
	require.Equal(t, 0, run([]string{"add", "--dir", ".cursor", projectRoot}))
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, ".claude", "CLAUDE.md"), []byte("claude change\n"), 0o644))

	require.Equal(t, 0, run([]string{"push", "--project", "my-app"}))
	paths, err := git.Run(centralRoot, "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD")
	require.NoError(t, err)
	require.Contains(t, paths, "projects/my-app/.claude/CLAUDE.md")
	require.NotContains(t, paths, "projects/my-app/.cursor/")
}

func TestLorePushUsesCentralAgentDirAuthorityWhenStateCacheIsStale(t *testing.T) {
	_, projectRoot, centralRoot := trackedCopySandbox(t)
	centralCursor := filepath.Join(centralRoot, "projects", "my-app", ".cursor")
	projectCursor := filepath.Join(projectRoot, ".cursor")
	require.NoError(t, os.Mkdir(centralCursor, 0o755))
	require.NoError(t, os.Mkdir(projectCursor, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(centralCursor, "RULES.md"), []byte("cursor base\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(projectCursor, "RULES.md"), []byte("cursor base\n"), 0o644))
	commitCentralChange(t, centralRoot)
	head, err := git.Run(centralRoot, "rev-parse", "HEAD")
	require.NoError(t, err)
	st, _, err := state.Load()
	require.NoError(t, err)
	entry, ok := st.ProjectEntryFor("my-app")
	require.True(t, ok)
	require.Equal(t, []string{".claude"}, entry.AgentDirs)
	require.NoError(t, st.RecordCheckout("my-app", entry.Checkouts[0].Path, head))
	require.NoError(t, state.Save(st))
	require.NoError(t, os.WriteFile(filepath.Join(projectCursor, "RULES.md"), []byte("local cursor change\n"), 0o644))

	require.Equal(t, 0, run([]string{"push", "--project", "my-app"}))
	data, err := os.ReadFile(filepath.Join(centralCursor, "RULES.md"))
	require.NoError(t, err)
	require.Equal(t, "local cursor change\n", string(data))
}

func TestLorePushFromCWDDoesNotInspectOtherProjects(t *testing.T) {
	home, projectRoot, _ := trackedCopySandbox(t)
	secondRoot := filepath.Join(home, "code", "second-app")
	require.NoError(t, os.MkdirAll(filepath.Join(secondRoot, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(secondRoot, ".claude", "CLAUDE.md"), []byte("second\n"), 0o644))
	require.NoError(t, git.Init(secondRoot))
	require.NoError(t, os.WriteFile(filepath.Join(secondRoot, ".git", "info", "exclude"), []byte(".claude/\n"), 0o644))
	require.Equal(t, 0, run([]string{"add", secondRoot}))
	require.NoError(t, os.RemoveAll(filepath.Join(secondRoot, ".claude")))
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, ".claude", "CLAUDE.md"), []byte("first changed\n"), 0o644))
	previous, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(projectRoot))
	t.Cleanup(func() { _ = os.Chdir(previous) })

	require.Equal(t, 0, run([]string{"push", "--project-from-cwd"}))
}

func TestLorePushFromCWDLeavesSiblingCheckoutStateUntouched(t *testing.T) {
	_, projectRoot, centralRoot := trackedCopySandbox(t)
	secondRoot := filepath.Join(filepath.Dir(projectRoot), "my-app-worktree")
	require.NoError(t, os.MkdirAll(secondRoot, 0o755))
	require.NoError(t, reconcile.CopyTree(filepath.Join(projectRoot, ".claude"), filepath.Join(secondRoot, ".claude")))
	head, err := git.Run(centralRoot, "rev-parse", "HEAD")
	require.NoError(t, err)
	st, _, err := state.Load()
	require.NoError(t, err)
	require.NoError(t, st.RecordCheckout("my-app", secondRoot, head))
	require.NoError(t, state.Save(st))
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, ".claude", "CLAUDE.md"), []byte("first changed\n"), 0o644))
	previous, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(projectRoot))
	t.Cleanup(func() { _ = os.Chdir(previous) })

	require.Equal(t, 0, run([]string{"push", "--project-from-cwd"}))
	st, _, err = state.Load()
	require.NoError(t, err)
	entry, ok := st.ProjectEntryFor("my-app")
	require.True(t, ok)
	require.Len(t, entry.Checkouts, 2)
	second, ok := st.CheckoutForPath("my-app", secondRoot)
	require.True(t, ok)
	require.Equal(t, head, second.SyncBase)
}

func TestLorePushForceAddsIgnoredFiles(t *testing.T) {
	_, projectRoot, centralRoot := trackedCopySandbox(t)
	require.NoError(t, os.WriteFile(filepath.Join(centralRoot, ".git", "info", "exclude"), []byte("*.log\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, ".claude", "capture.log"), []byte("important\n"), 0o644))

	require.Equal(t, 0, run([]string{"push", "--project", "my-app"}))
	tracked, err := git.Run(centralRoot, "ls-tree", "-r", "--name-only", "HEAD")
	require.NoError(t, err)
	require.Contains(t, tracked, "projects/my-app/.claude/capture.log")
}

func TestLorePushUsesDefaultForBlankSyncTemplate(t *testing.T) {
	_, projectRoot, centralRoot := trackedCopySandbox(t)
	cfg, _, err := config.Load()
	require.NoError(t, err)
	cfg.Commit.SyncTemplate = ""
	require.NoError(t, config.Save(cfg))
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, ".claude", "CLAUDE.md"), []byte("changed\n"), 0o644))

	require.Equal(t, 0, run([]string{"push", "--project", "my-app"}))
	message, err := git.Run(centralRoot, "log", "-1", "--pretty=%s")
	require.NoError(t, err)
	require.Equal(t, "chore: sync (my-app)", message)
}

func TestLorePushRecordsExecutableChangesWhenCentralDisablesFileMode(t *testing.T) {
	_, projectRoot, centralRoot := trackedCopySandbox(t)
	require.NoError(t, os.Chmod(filepath.Join(projectRoot, ".claude", "CLAUDE.md"), 0o755))
	_, err := git.Run(centralRoot, "config", "core.fileMode", "false")
	require.NoError(t, err)

	require.Equal(t, 0, run([]string{"push", "--project", "my-app"}))
	tree, err := git.Run(centralRoot, "ls-tree", "HEAD", "projects/my-app/.claude/CLAUDE.md")
	require.NoError(t, err)
	require.Contains(t, tree, "100755 blob")
}

func TestLorePushKeepsAdoptedSiblingAtPrePushHead(t *testing.T) {
	_, firstRoot, centralRoot := trackedCopySandbox(t)
	secondRoot := filepath.Join(filepath.Dir(firstRoot), "my-app-worktree")
	require.NoError(t, os.MkdirAll(secondRoot, 0o755))
	require.NoError(t, reconcile.CopyTree(filepath.Join(firstRoot, ".claude"), filepath.Join(secondRoot, ".claude")))
	base, err := git.Run(centralRoot, "rev-parse", "HEAD")
	require.NoError(t, err)
	st, _, err := state.Load()
	require.NoError(t, err)
	require.NoError(t, st.RecordCheckout("my-app", secondRoot, base))
	require.NoError(t, state.Save(st))

	common := []byte("later shared value\n")
	require.NoError(t, os.WriteFile(filepath.Join(centralRoot, "projects", "my-app", ".claude", "CLAUDE.md"), common, 0o644))
	commitCentralChange(t, centralRoot)
	prePushHead, err := git.Run(centralRoot, "rev-parse", "HEAD")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(firstRoot, ".claude", "CLAUDE.md"), common, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(secondRoot, ".claude", "CLAUDE.md"), common, 0o644))
	st, _, err = state.Load()
	require.NoError(t, err)
	require.NoError(t, st.RecordCheckout("my-app", secondRoot, prePushHead))
	require.NoError(t, state.Save(st))
	require.NoError(t, os.WriteFile(filepath.Join(secondRoot, ".claude", "CLAUDE.md"), []byte("second checkout capture\n"), 0o644))

	require.Equal(t, 0, run([]string{"push", "--project", "my-app"}))
	postPushHead, err := git.Run(centralRoot, "rev-parse", "HEAD")
	require.NoError(t, err)
	require.NotEqual(t, prePushHead, postPushHead)
	st, _, err = state.Load()
	require.NoError(t, err)
	entry, ok := st.ProjectEntryFor("my-app")
	require.True(t, ok)
	require.Len(t, entry.Checkouts, 2)
	firstPath := entry.Checkouts[0].Path
	if firstPath == secondRoot {
		firstPath = entry.Checkouts[1].Path
	}
	first, ok := st.CheckoutForPath("my-app", firstPath)
	require.True(t, ok)
	second, ok := st.CheckoutForPath("my-app", secondRoot)
	require.True(t, ok)
	require.Equal(t, prePushHead, first.SyncBase)
	require.Equal(t, postPushHead, second.SyncBase)

	previous, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(firstRoot))
	t.Cleanup(func() { _ = os.Chdir(previous) })
	require.Equal(t, 5, run([]string{"push", "--project-from-cwd"}))
}

func TestLorePushJSONDoesNotConsumeDeferredFailure(t *testing.T) {
	home, projectRoot, centralRoot := trackedCopySandbox(t)
	centralFile := filepath.Join(centralRoot, "projects", "my-app", ".claude", "CLAUDE.md")
	require.NoError(t, os.WriteFile(centralFile, []byte("central\n"), 0o644))
	commitCentralChange(t, centralRoot)
	require.Equal(t, 5, run([]string{"push", "--auto", "--project", "my-app"}))
	record := filepath.Join(home, ".local", "state", "lore", "last-sync-error.txt")
	require.FileExists(t, record)

	require.Equal(t, 0, run([]string{"--json", "status", "my-app"}))
	require.FileExists(t, record)
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, ".claude", "CLAUDE.md"), []byte("central\n"), 0o644))
	require.Equal(t, 0, run([]string{"push", "--auto", "--project", "my-app"}))
	require.NoFileExists(t, record)
}

func TestLorePushFromCWDFailureRecordsResolvedProject(t *testing.T) {
	home, projectRoot, centralRoot := trackedCopySandbox(t)
	require.NoError(t, os.WriteFile(filepath.Join(centralRoot, "projects", "my-app", ".claude", "CLAUDE.md"), []byte("central\n"), 0o644))
	commitCentralChange(t, centralRoot)
	previous, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(projectRoot))
	t.Cleanup(func() { _ = os.Chdir(previous) })

	require.Equal(t, 5, run([]string{"push", "--auto", "--project-from-cwd"}))
	data, err := os.ReadFile(filepath.Join(home, ".local", "state", "lore", "last-sync-error.txt"))
	require.NoError(t, err)
	require.Contains(t, string(data), "scope: my-app\n")
}
