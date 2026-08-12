package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nathanclairmonte/lore/internal/git"
	"github.com/nathanclairmonte/lore/internal/reconcile"
	"github.com/nathanclairmonte/lore/internal/state"
	"github.com/stretchr/testify/require"
)

func trackedCopySandbox(t *testing.T) (home, projectRoot, centralRoot string) {
	t.Helper()
	home, projectRoot = addCmdSandbox(t)
	require.Equal(t, 0, run([]string{"add", projectRoot}))
	centralRoot = filepath.Join(home, "agent-context")
	return home, projectRoot, centralRoot
}

func commitCentralChange(t *testing.T, centralRoot string) {
	t.Helper()
	_, err := git.Run(centralRoot, "add", "-A")
	require.NoError(t, err)
	_, err = git.Run(centralRoot, "commit", "-m", "test: central drift")
	require.NoError(t, err)
}

func TestLoreStatusReportsInSyncAndProjectAhead(t *testing.T) {
	_, projectRoot, _ := trackedCopySandbox(t)
	buffer := capturedOut(t)

	require.Equal(t, 0, run([]string{"status", "my-app"}))
	require.Contains(t, buffer.String(), "in-sync")

	buffer.Reset()
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, ".claude", "CLAUDE.md"), []byte("project edit\n"), 0o644))
	require.Equal(t, 0, run([]string{"status", "my-app"}))
	require.Contains(t, buffer.String(), "project-ahead")
}

func TestLoreStatusReportsCentralAhead(t *testing.T) {
	_, _, centralRoot := trackedCopySandbox(t)
	buffer := capturedOut(t)
	require.NoError(t, os.WriteFile(filepath.Join(centralRoot, "projects", "my-app", ".claude", "CLAUDE.md"), []byte("central edit\n"), 0o644))
	commitCentralChange(t, centralRoot)

	require.Equal(t, 0, run([]string{"status", "my-app"}))
	require.Contains(t, buffer.String(), "central-ahead")
}

func TestLoreStatusEqualChangedCopiesAreInSyncWithStaleBase(t *testing.T) {
	_, projectRoot, centralRoot := trackedCopySandbox(t)
	buffer := capturedOut(t)
	data := []byte("same later value\n")
	require.NoError(t, os.WriteFile(filepath.Join(centralRoot, "projects", "my-app", ".claude", "CLAUDE.md"), data, 0o644))
	commitCentralChange(t, centralRoot)
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, ".claude", "CLAUDE.md"), data, 0o644))

	require.Equal(t, 0, run([]string{"status", "my-app"}))
	require.Contains(t, buffer.String(), "in-sync")
	require.Contains(t, buffer.String(), "base stale")
}

func TestLoreStatusAndDiffInspectEveryCheckout(t *testing.T) {
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
	require.NoError(t, os.WriteFile(filepath.Join(secondRoot, ".claude", "CLAUDE.md"), []byte("second checkout edit\n"), 0o644))
	buffer := capturedOut(t)

	require.Equal(t, 0, run([]string{"--json", "status", "my-app"}))
	require.Contains(t, buffer.String(), projectRoot)
	require.Contains(t, buffer.String(), secondRoot)
	buffer.Reset()
	require.Equal(t, 0, run([]string{"--json", "diff", "my-app"}))
	require.Contains(t, buffer.String(), projectRoot)
	require.Contains(t, buffer.String(), secondRoot)
}

func TestLoreStatusDivergenceAndConflictMarkersExitSeven(t *testing.T) {
	_, projectRoot, centralRoot := trackedCopySandbox(t)
	buffer := capturedOut(t)
	projectFile := filepath.Join(projectRoot, ".claude", "CLAUDE.md")
	centralFile := filepath.Join(centralRoot, "projects", "my-app", ".claude", "CLAUDE.md")
	require.NoError(t, os.WriteFile(centralFile, []byte("central edit\n"), 0o644))
	commitCentralChange(t, centralRoot)
	require.NoError(t, os.WriteFile(projectFile, []byte("project edit\n"), 0o644))

	require.Equal(t, 7, run([]string{"status", "my-app"}))
	require.Contains(t, buffer.String(), "diverged")

	buffer.Reset()
	require.NoError(t, os.WriteFile(projectFile, []byte("<<<<<<< project\na\n=======\nb\n>>>>>>> central\n"), 0o644))
	require.Equal(t, 7, run([]string{"status", "my-app"}))
	require.Contains(t, buffer.String(), "conflict-markers")
}

func TestLoreStatusMissingCopyAndUnreachableBaseExitSeven(t *testing.T) {
	_, projectRoot, _ := trackedCopySandbox(t)
	buffer := capturedOut(t)
	require.NoError(t, os.RemoveAll(filepath.Join(projectRoot, ".claude")))

	require.Equal(t, 7, run([]string{"status", "my-app"}))
	require.Contains(t, buffer.String(), "copy-missing")
	buffer.Reset()
	require.Equal(t, 7, run([]string{"diff", "my-app"}))
	require.Contains(t, buffer.String(), "copy-missing")
	require.NotContains(t, buffer.String(), "no differences")

	buffer.Reset()
	require.NoError(t, os.MkdirAll(filepath.Join(projectRoot, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, ".claude", "CLAUDE.md"), []byte("context"), 0o644))
	st, _, err := state.Load()
	require.NoError(t, err)
	require.NoError(t, st.RecordCheckout("my-app", projectRoot, "0123456789abcdef0123456789abcdef01234567"))
	require.NoError(t, state.Save(st))
	require.Equal(t, 7, run([]string{"status", "my-app"}))
	require.Contains(t, buffer.String(), "base-unreachable")
}

func TestLoreStatusUnknownProjectExitsThree(t *testing.T) {
	trackedCopySandbox(t)
	require.Equal(t, 3, run([]string{"status", "unknown"}))
	require.Equal(t, 3, run([]string{"diff", "unknown"}))
}

func TestLoreStatusJSONEmitsResultBeforeUnhealthyExit(t *testing.T) {
	_, projectRoot, centralRoot := trackedCopySandbox(t)
	buffer := capturedOut(t)
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, ".claude", "CLAUDE.md"),
		[]byte("<<<<<<< project\na\n=======\nb\n>>>>>>> central\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(centralRoot, "projects", "my-app", ".claude", "CLAUDE.md"), []byte("central edit\n"), 0o644))
	commitCentralChange(t, centralRoot)

	require.Equal(t, 7, run([]string{"--json", "status", "my-app"}))
	var envelope struct {
		Projects []struct {
			Checkouts []struct {
				State  string `json:"state"`
				Issues []any  `json:"issues"`
				Dirs   []struct {
					Issues []struct {
						Kind string `json:"kind"`
					} `json:"issues"`
				} `json:"dirs"`
			} `json:"checkouts"`
		} `json:"projects"`
	}
	require.NoError(t, json.Unmarshal(buffer.Bytes(), &envelope))
	require.Equal(t, "diverged", envelope.Projects[0].Checkouts[0].State)
	require.Empty(t, envelope.Projects[0].Checkouts[0].Issues)
	require.Equal(t, "conflict-markers", envelope.Projects[0].Checkouts[0].Dirs[0].Issues[0].Kind)
}

func TestLoreDiffShowsUnifiedPatchAndDifferencesExitZero(t *testing.T) {
	_, projectRoot, _ := trackedCopySandbox(t)
	buffer := capturedOut(t)
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, ".claude", "CLAUDE.md"), []byte("project edit\n"), 0o644))

	require.Equal(t, 0, run([]string{"diff", "my-app"}))
	require.Contains(t, buffer.String(), "--- central/.claude/CLAUDE.md")
	require.Contains(t, buffer.String(), "+++ project/.claude/CLAUDE.md")
	require.Contains(t, buffer.String(), "+project edit")
}

func TestLoreDiffDirtyCentralSuppressesPatch(t *testing.T) {
	_, projectRoot, centralRoot := trackedCopySandbox(t)
	buffer := capturedOut(t)
	require.NoError(t, os.WriteFile(filepath.Join(projectRoot, ".claude", "CLAUDE.md"), []byte("project edit\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(centralRoot, "projects", "my-app", ".claude", "CLAUDE.md"), []byte("uncommitted central edit\n"), 0o644))

	require.Equal(t, 7, run([]string{"diff", "my-app"}))
	require.Contains(t, buffer.String(), "patches are suppressed")
	require.NotContains(t, buffer.String(), "--- central/")

	buffer.Reset()
	require.Equal(t, 7, run([]string{"--json", "status", "my-app"}))
	var envelope struct {
		Projects []struct {
			Issues []struct {
				Kind string `json:"kind"`
			} `json:"issues"`
			Checkouts []struct {
				Issues []any `json:"issues"`
			} `json:"checkouts"`
		} `json:"projects"`
	}
	require.NoError(t, json.Unmarshal(buffer.Bytes(), &envelope))
	require.Equal(t, "central-dirty", envelope.Projects[0].Issues[0].Kind)
	require.Empty(t, envelope.Projects[0].Checkouts[0].Issues)
}

func TestLoreStatusReadsNewAgentDirsFromCentral(t *testing.T) {
	_, _, centralRoot := trackedCopySandbox(t)
	buffer := capturedOut(t)
	newDir := filepath.Join(centralRoot, "projects", "my-app", ".cursor")
	require.NoError(t, os.MkdirAll(newDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(newDir, "RULES.md"), []byte("central rules\n"), 0o644))
	commitCentralChange(t, centralRoot)

	require.Equal(t, 7, run([]string{"--json", "status", "my-app"}))
	var envelope struct {
		Projects []struct {
			Checkouts []struct {
				Dirs []struct {
					Name string `json:"name"`
				} `json:"dirs"`
			} `json:"checkouts"`
		} `json:"projects"`
	}
	require.NoError(t, json.Unmarshal(buffer.Bytes(), &envelope))
	require.Equal(t, []string{".claude", ".cursor"}, []string{
		envelope.Projects[0].Checkouts[0].Dirs[0].Name,
		envelope.Projects[0].Checkouts[0].Dirs[1].Name,
	})
}

func TestLoreStatusReportsGitlinkAsScopedIssue(t *testing.T) {
	_, _, centralRoot := trackedCopySandbox(t)
	buffer := capturedOut(t)
	nested := filepath.Join(centralRoot, "projects", "my-app", ".claude", "nested")
	require.NoError(t, os.MkdirAll(nested, 0o755))
	require.NoError(t, git.Init(nested))
	_, err := git.Run(nested, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "nested")
	require.NoError(t, err)
	_, err = git.Run(centralRoot, "add", "projects/my-app/.claude/nested")
	require.NoError(t, err)
	_, err = git.Run(centralRoot, "commit", "-m", "test: add gitlink")
	require.NoError(t, err)

	require.Equal(t, 7, run([]string{"--json", "status", "my-app"}))
	var envelope struct {
		Projects []struct {
			Checkouts []struct {
				Dirs []struct {
					Issues []struct {
						Kind string `json:"kind"`
						Path string `json:"path"`
					} `json:"issues"`
				} `json:"dirs"`
			} `json:"checkouts"`
		} `json:"projects"`
	}
	require.NoError(t, json.Unmarshal(buffer.Bytes(), &envelope))
	issues := envelope.Projects[0].Checkouts[0].Dirs[0].Issues
	require.Contains(t, issues, struct {
		Kind string `json:"kind"`
		Path string `json:"path"`
	}{Kind: "unsupported-object", Path: "nested"})
}

func TestLoreStatusAndDiffDoNotMutateStateOrCentralHead(t *testing.T) {
	home, _, centralRoot := trackedCopySandbox(t)
	statePath := filepath.Join(home, ".local", "state", "lore", "state.toml")
	stateBefore, err := os.ReadFile(statePath)
	require.NoError(t, err)
	headBefore, err := git.Run(centralRoot, "rev-parse", "HEAD")
	require.NoError(t, err)

	require.Equal(t, 0, run([]string{"status"}))
	require.Equal(t, 0, run([]string{"diff", "my-app"}))
	stateAfter, err := os.ReadFile(statePath)
	require.NoError(t, err)
	headAfter, err := git.Run(centralRoot, "rev-parse", "HEAD")
	require.NoError(t, err)
	require.Equal(t, stateBefore, stateAfter)
	require.Equal(t, headBefore, headAfter)
}
