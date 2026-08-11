package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nathanclairmonte/lore/internal/git"
	"github.com/nathanclairmonte/lore/internal/project"
	"github.com/nathanclairmonte/lore/internal/reconcile"
	"github.com/nathanclairmonte/lore/internal/state"
	"github.com/stretchr/testify/require"
)

func TestLorePullUpdatesCentralAheadCopy(t *testing.T) {
	_, projectRoot, centralRoot := trackedCopySandbox(t)
	centralFile := filepath.Join(centralRoot, "projects", "my-app", ".claude", "CLAUDE.md")
	require.NoError(t, os.WriteFile(centralFile, []byte("central edit\n"), 0o644))
	commitCentralChange(t, centralRoot)

	require.Equal(t, 0, run([]string{"pull", "my-app"}))
	projectData, err := os.ReadFile(filepath.Join(projectRoot, ".claude", "CLAUDE.md"))
	require.NoError(t, err)
	require.Equal(t, "central edit\n", string(projectData))

	st, _, err := state.Load()
	require.NoError(t, err)
	canonicalRoot, err := filepath.EvalSymlinks(projectRoot)
	require.NoError(t, err)
	checkout, ok := st.CheckoutForPath("my-app", canonicalRoot)
	require.True(t, ok)
	head, err := git.Run(centralRoot, "rev-parse", "HEAD")
	require.NoError(t, err)
	require.Equal(t, head, checkout.SyncBase)
}

func TestLorePullAllMaterializesFreshMachineAndRecordsBase(t *testing.T) {
	home, projectRoot, centralRoot := trackedCopySandbox(t)
	require.NoError(t, os.RemoveAll(filepath.Join(projectRoot, ".claude")))
	require.NoError(t, os.Remove(filepath.Join(home, ".local", "state", "lore", "state.toml")))

	require.Equal(t, 0, run([]string{"pull", "--all"}))
	require.FileExists(t, filepath.Join(projectRoot, ".claude", "CLAUDE.md"))
	info, err := os.Lstat(filepath.Join(projectRoot, ".claude"))
	require.NoError(t, err)
	require.Zero(t, info.Mode()&os.ModeSymlink)

	st, _, err := state.Load()
	require.NoError(t, err)
	canonicalRoot, err := filepath.EvalSymlinks(projectRoot)
	require.NoError(t, err)
	checkout, ok := st.CheckoutForPath("my-app", canonicalRoot)
	require.True(t, ok)
	head, err := git.Run(centralRoot, "rev-parse", "HEAD")
	require.NoError(t, err)
	require.Equal(t, head, checkout.SyncBase)
}

func TestLorePullAdoptsIdenticalCopyWithUnreachableBase(t *testing.T) {
	_, projectRoot, centralRoot := trackedCopySandbox(t)
	st, _, err := state.Load()
	require.NoError(t, err)
	canonicalRoot, err := filepath.EvalSymlinks(projectRoot)
	require.NoError(t, err)
	require.NoError(t, st.RecordCheckout("my-app", canonicalRoot, "0123456789abcdef0123456789abcdef01234567"))
	require.NoError(t, state.Save(st))

	require.Equal(t, 0, run([]string{"pull", "my-app"}))
	st, _, err = state.Load()
	require.NoError(t, err)
	checkout, ok := st.CheckoutForPath("my-app", canonicalRoot)
	require.True(t, ok)
	head, err := git.Run(centralRoot, "rev-parse", "HEAD")
	require.NoError(t, err)
	require.Equal(t, head, checkout.SyncBase)
}

func TestLorePullKeepsProjectAheadCopy(t *testing.T) {
	_, projectRoot, _ := trackedCopySandbox(t)
	projectFile := filepath.Join(projectRoot, ".claude", "CLAUDE.md")
	require.NoError(t, os.WriteFile(projectFile, []byte("project edit\n"), 0o644))

	require.Equal(t, 0, run([]string{"pull", "my-app"}))
	data, err := os.ReadFile(projectFile)
	require.NoError(t, err)
	require.Equal(t, "project edit\n", string(data))
}

func TestLorePullWritesTextConflictAndAdvancesBase(t *testing.T) {
	_, projectRoot, centralRoot := trackedCopySandbox(t)
	projectFile := filepath.Join(projectRoot, ".claude", "CLAUDE.md")
	centralFile := filepath.Join(centralRoot, "projects", "my-app", ".claude", "CLAUDE.md")
	require.NoError(t, os.WriteFile(projectFile, []byte("project edit\n"), 0o644))
	require.NoError(t, os.WriteFile(centralFile, []byte("central edit\n"), 0o644))
	commitCentralChange(t, centralRoot)

	require.Equal(t, 7, run([]string{"pull", "my-app"}))
	data, err := os.ReadFile(projectFile)
	require.NoError(t, err)
	require.Equal(t, "<<<<<<< project\nproject edit\n=======\ncentral edit\n>>>>>>> central\n", string(data))

	st, _, err := state.Load()
	require.NoError(t, err)
	canonicalRoot, err := filepath.EvalSymlinks(projectRoot)
	require.NoError(t, err)
	checkout, ok := st.CheckoutForPath("my-app", canonicalRoot)
	require.True(t, ok)
	head, err := git.Run(centralRoot, "rev-parse", "HEAD")
	require.NoError(t, err)
	require.Equal(t, head, checkout.SyncBase)

	require.NoError(t, os.WriteFile(projectFile, []byte("resolved\n"), 0o644))
	require.Equal(t, 0, run([]string{"push", "--project", "my-app"}))
}

func TestLorePullCleanlyMergesNonOverlappingEdits(t *testing.T) {
	_, projectRoot, centralRoot := trackedCopySandbox(t)
	projectFile := filepath.Join(projectRoot, ".claude", "CLAUDE.md")
	centralFile := filepath.Join(centralRoot, "projects", "my-app", ".claude", "CLAUDE.md")
	require.NoError(t, os.WriteFile(projectFile, []byte("one\ntwo\nthree\n"), 0o644))
	require.Equal(t, 0, run([]string{"push", "--project", "my-app"}))
	require.NoError(t, os.WriteFile(projectFile, []byte("ONE\ntwo\nthree\n"), 0o644))
	require.NoError(t, os.WriteFile(centralFile, []byte("one\ntwo\nTHREE\n"), 0o644))
	commitCentralChange(t, centralRoot)

	require.Equal(t, 0, run([]string{"pull", "my-app"}))
	data, err := os.ReadFile(projectFile)
	require.NoError(t, err)
	require.Equal(t, "ONE\ntwo\nTHREE\n", string(data))
	require.Equal(t, 0, run([]string{"push", "--project", "my-app"}))
}

func TestLorePullKeepsBothBinaryConflicts(t *testing.T) {
	_, projectRoot, centralRoot := trackedCopySandbox(t)
	projectFile := filepath.Join(projectRoot, ".claude", "asset.bin")
	centralFile := filepath.Join(centralRoot, "projects", "my-app", ".claude", "asset.bin")
	require.NoError(t, os.WriteFile(projectFile, []byte{0, 0}, 0o644))
	require.Equal(t, 0, run([]string{"push", "--project", "my-app"}))
	require.NoError(t, os.WriteFile(projectFile, []byte{0, 1}, 0o644))
	require.NoError(t, os.WriteFile(centralFile, []byte{0, 2}, 0o644))
	commitCentralChange(t, centralRoot)

	require.Equal(t, 7, run([]string{"pull", "my-app"}))
	projectData, err := os.ReadFile(projectFile)
	require.NoError(t, err)
	centralData, err := os.ReadFile(projectFile + ".sync-conflict")
	require.NoError(t, err)
	require.Equal(t, []byte{0, 1}, projectData)
	require.Equal(t, []byte{0, 2}, centralData)
	require.Equal(t, 0, run([]string{"pull", "my-app"}))
	require.Equal(t, 0, run([]string{"pull", "my-app", "--project"}))
	require.Equal(t, 7, run([]string{"status", "my-app"}))
	require.Equal(t, 7, run([]string{"push", "--project", "my-app"}))
}

func TestLorePullForceDirections(t *testing.T) {
	t.Run("central", func(t *testing.T) {
		_, projectRoot, centralRoot := trackedCopySandbox(t)
		projectFile := filepath.Join(projectRoot, ".claude", "CLAUDE.md")
		centralFile := filepath.Join(centralRoot, "projects", "my-app", ".claude", "CLAUDE.md")
		require.NoError(t, os.WriteFile(projectFile, []byte("project\n"), 0o644))
		require.NoError(t, os.WriteFile(centralFile, []byte("central\n"), 0o644))
		commitCentralChange(t, centralRoot)
		require.Equal(t, 0, run([]string{"pull", "my-app", "--central"}))
		data, err := os.ReadFile(projectFile)
		require.NoError(t, err)
		require.Equal(t, "central\n", string(data))
	})

	t.Run("project", func(t *testing.T) {
		_, projectRoot, centralRoot := trackedCopySandbox(t)
		projectFile := filepath.Join(projectRoot, ".claude", "CLAUDE.md")
		centralFile := filepath.Join(centralRoot, "projects", "my-app", ".claude", "CLAUDE.md")
		require.NoError(t, os.WriteFile(projectFile, []byte("project\n"), 0o644))
		require.NoError(t, os.WriteFile(centralFile, []byte("central\n"), 0o644))
		commitCentralChange(t, centralRoot)
		require.Equal(t, 0, run([]string{"pull", "my-app", "--project"}))
		data, err := os.ReadFile(projectFile)
		require.NoError(t, err)
		require.Equal(t, "project\n", string(data))
		require.Equal(t, 0, run([]string{"push", "--project", "my-app"}))
	})
}

func TestLorePullForceDirectionsRecoverDifferingBaselessCopy(t *testing.T) {
	t.Run("project", func(t *testing.T) {
		home, projectRoot, _ := trackedCopySandbox(t)
		projectFile := filepath.Join(projectRoot, ".claude", "CLAUDE.md")
		require.NoError(t, os.WriteFile(projectFile, []byte("recovered project\n"), 0o644))
		require.NoError(t, os.Remove(filepath.Join(home, ".local", "state", "lore", "state.toml")))

		require.Equal(t, 0, run([]string{"pull", "my-app", "--path", projectRoot, "--project"}))
		data, err := os.ReadFile(projectFile)
		require.NoError(t, err)
		require.Equal(t, "recovered project\n", string(data))
		require.Equal(t, 0, run([]string{"push", "--project", "my-app"}))
	})

	t.Run("central", func(t *testing.T) {
		home, projectRoot, _ := trackedCopySandbox(t)
		projectFile := filepath.Join(projectRoot, ".claude", "CLAUDE.md")
		require.NoError(t, os.WriteFile(projectFile, []byte("discarded project\n"), 0o644))
		require.NoError(t, os.Remove(filepath.Join(home, ".local", "state", "lore", "state.toml")))

		require.Equal(t, 0, run([]string{"pull", "my-app", "--path", projectRoot, "--central"}))
		data, err := os.ReadFile(projectFile)
		require.NoError(t, err)
		require.Equal(t, "context", string(data))
	})
}

func TestLorePullNamedUpdatesEveryRecordedCheckout(t *testing.T) {
	_, projectRoot, centralRoot := trackedCopySandbox(t)
	secondRoot := filepath.Join(filepath.Dir(projectRoot), "my-app-worktree")
	require.NoError(t, os.MkdirAll(secondRoot, 0o755))
	require.NoError(t, reconcile.CopyTree(filepath.Join(projectRoot, ".claude"), filepath.Join(secondRoot, ".claude")))
	st, _, err := state.Load()
	require.NoError(t, err)
	head, err := git.Run(centralRoot, "rev-parse", "HEAD")
	require.NoError(t, err)
	require.NoError(t, st.RecordCheckout("my-app", secondRoot, head))
	require.NoError(t, state.Save(st))

	centralFile := filepath.Join(centralRoot, "projects", "my-app", ".claude", "CLAUDE.md")
	require.NoError(t, os.WriteFile(centralFile, []byte("central edit\n"), 0o644))
	commitCentralChange(t, centralRoot)
	require.Equal(t, 0, run([]string{"pull", "my-app"}))

	for _, root := range []string{projectRoot, secondRoot} {
		data, err := os.ReadFile(filepath.Join(root, ".claude", "CLAUDE.md"))
		require.NoError(t, err)
		require.Equal(t, "central edit\n", string(data))
	}
}

func TestLorePullPathPrunesEveryMissingRecordedCheckout(t *testing.T) {
	home, _, centralRoot := trackedCopySandbox(t)
	st, _, err := state.Load()
	require.NoError(t, err)
	head, err := git.Run(centralRoot, "rev-parse", "HEAD")
	require.NoError(t, err)
	missingOne := filepath.Join(home, "code", "missing-one")
	missingTwo := filepath.Join(home, "code", "missing-two")
	require.NoError(t, st.RecordCheckout("my-app", missingOne, head))
	require.NoError(t, st.RecordCheckout("my-app", missingTwo, head))
	require.NoError(t, state.Save(st))

	replacement := filepath.Join(home, "code", "replacement")
	require.NoError(t, os.MkdirAll(replacement, 0o755))
	require.Equal(t, 0, run([]string{"pull", "my-app", "--path", replacement}))

	st, _, err = state.Load()
	require.NoError(t, err)
	entry, ok := st.ProjectEntryFor("my-app")
	require.True(t, ok)
	for _, checkout := range entry.Checkouts {
		require.NotEqual(t, missingOne, checkout.Path)
		require.NotEqual(t, missingTwo, checkout.Path)
	}
}

func TestLorePullJSONReportsUnignoredWithoutPrompt(t *testing.T) {
	home, _, _ := trackedCopySandbox(t)
	newRoot := filepath.Join(home, "code", "new-checkout")
	require.NoError(t, os.MkdirAll(newRoot, 0o755))
	_, err := git.Run(newRoot, "init")
	require.NoError(t, err)
	buffer := capturedOut(t)

	require.Equal(t, 0, run([]string{"--json", "pull", "my-app", "--path", newRoot}))
	var result struct {
		Projects []struct {
			Checkouts []struct {
				Dirs []struct {
					Unignored bool `json:"unignored"`
				} `json:"dirs"`
			} `json:"checkouts"`
		} `json:"projects"`
	}
	require.NoError(t, json.Unmarshal(buffer.Bytes(), &result))
	require.True(t, result.Projects[0].Checkouts[0].Dirs[0].Unignored)
}

func TestLorePullAllLeavesAmbiguousUnrecordedMatchesUntouched(t *testing.T) {
	home, projectRoot, _ := trackedCopySandbox(t)
	require.NoError(t, os.RemoveAll(filepath.Join(projectRoot, ".claude")))
	require.NoError(t, os.Remove(filepath.Join(home, ".local", "state", "lore", "state.toml")))
	second := filepath.Join(home, "code", "nested", "my-app")
	require.NoError(t, os.MkdirAll(second, 0o755))

	require.Equal(t, 0, run([]string{"pull", "--all"}))
	_, firstErr := os.Lstat(filepath.Join(projectRoot, ".claude"))
	_, secondErr := os.Lstat(filepath.Join(second, ".claude"))
	require.True(t, os.IsNotExist(firstErr))
	require.True(t, os.IsNotExist(secondErr))
}

func TestLorePullAllContinuesPastDivergedCheckout(t *testing.T) {
	home := sandbox(t)
	t.Setenv("GIT_AUTHOR_NAME", "Test Author")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Author")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")
	require.Equal(t, 0, run([]string{"init"}))

	projectRoots := map[string]string{}
	for _, name := range []string{"first", "second"} {
		root := filepath.Join(home, "code", name)
		projectRoots[name] = root
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0o755))
		_, err := git.Run(root, "init")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(root, ".git", "info", "exclude"), []byte(".claude/\n"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "CLAUDE.md"), []byte("base\n"), 0o644))
		require.Equal(t, 0, run([]string{"add", root}))
	}

	centralRoot := filepath.Join(home, "agent-context")
	for _, name := range []string{"first", "second"} {
		centralFile := filepath.Join(centralRoot, "projects", name, ".claude", "CLAUDE.md")
		require.NoError(t, os.WriteFile(centralFile, []byte("central "+name+"\n"), 0o644))
	}
	commitCentralChange(t, centralRoot)
	require.NoError(t, os.WriteFile(filepath.Join(projectRoots["first"], ".claude", "CLAUDE.md"), []byte("project first\n"), 0o644))

	require.Equal(t, 7, run([]string{"pull", "--all"}))
	firstData, err := os.ReadFile(filepath.Join(projectRoots["first"], ".claude", "CLAUDE.md"))
	require.NoError(t, err)
	require.Contains(t, string(firstData), "<<<<<<< project")
	secondData, err := os.ReadFile(filepath.Join(projectRoots["second"], ".claude", "CLAUDE.md"))
	require.NoError(t, err)
	require.Equal(t, "central second\n", string(secondData))
}

func TestPullResultForOutputKeepsArraysNonNil(t *testing.T) {
	result := pullResultForOutput(project.PullResult{
		Projects: []project.PullProjectResult{{
			Checkouts: []project.PullCheckoutResult{{}},
		}},
	})

	require.NotNil(t, result.ScannedRoots)
	require.NotNil(t, result.Projects)
	require.NotNil(t, result.Projects[0].Matches)
	require.NotNil(t, result.Projects[0].Checkouts)
	require.NotNil(t, result.Projects[0].Checkouts[0].Dirs)
	result = pullResultForOutput(project.PullResult{
		Projects: []project.PullProjectResult{{
			Checkouts: []project.PullCheckoutResult{{Dirs: []project.PullDirResult{{}}}},
		}},
	})
	require.NotNil(t, result.Projects[0].Checkouts[0].Dirs[0].Conflicts)
}

func TestLorePullRejectsForceDirectionWithAll(t *testing.T) {
	trackedCopySandbox(t)
	require.Equal(t, 4, run([]string{"pull", "--all", "--central"}))
}
