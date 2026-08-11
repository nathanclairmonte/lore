package reconcile

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func file(value string) Entry { return Entry{Kind: KindFile, Data: []byte(value)} }

func TestClassifyThreeWayStatesAndRollup(t *testing.T) {
	base := Snapshot{"same": file("a"), "project": file("a"), "central": file("a"), "conflict": file("a")}
	project := Snapshot{"same": file("a"), "project": file("p"), "central": file("a"), "conflict": file("p")}
	central := Snapshot{"same": file("a"), "project": file("a"), "central": file("c"), "conflict": file("c")}

	result := Classify(project, central, base, nil)
	require.Equal(t, StateDiverged, result.State)
	require.Equal(t, []SyncState{StateCentralAhead, StateDiverged, StateProjectAhead, StateInSync}, []SyncState{
		result.Files[0].State, result.Files[1].State, result.Files[2].State, result.Files[3].State,
	})
}

func TestClassifyOppositeFilesRollUpAsDiverged(t *testing.T) {
	base := Snapshot{"a": file("0"), "b": file("0")}
	project := Snapshot{"a": file("1"), "b": file("0")}
	central := Snapshot{"a": file("0"), "b": file("1")}
	require.Equal(t, StateDiverged, Classify(project, central, base, nil).State)
}

func TestClassifyEqualChangesAreInSyncWithStaleBase(t *testing.T) {
	base := Snapshot{"a": file("0")}
	current := Snapshot{"a": file("1")}
	result := Classify(current, current, base, nil)
	require.Equal(t, StateInSync, result.State)
	require.True(t, result.BaseStale)
}

func TestClassifyAddsDeletesModesAndTypes(t *testing.T) {
	base := Snapshot{
		"deleted-project": file("base"),
		"deleted-central": file("base"),
		"mode":            file("same"),
		"type":            file("target"),
	}
	project := Snapshot{
		"deleted-central": file("base"),
		"mode":            {Kind: KindFile, Data: []byte("same"), Executable: true},
		"type":            {Kind: KindSymlink, Data: []byte("target")},
		"project-added":   file("new"),
	}
	central := Snapshot{
		"deleted-project": file("base"),
		"mode":            file("same"),
		"type":            file("target"),
		"central-added":   file("new"),
	}

	result := Classify(project, central, base, nil)
	require.Equal(t, StateDiverged, result.State)
	byPath := map[string]FileResult{}
	for _, file := range result.Files {
		byPath[file.Path] = file
	}
	require.Equal(t, ChangeAdded, byPath["project-added"].ProjectChange)
	require.Equal(t, ChangeDeleted, byPath["deleted-project"].ProjectChange)
	require.Equal(t, ChangeDeleted, byPath["deleted-central"].CentralChange)
	require.Equal(t, ChangeModified, byPath["mode"].ProjectChange)
	require.Equal(t, ChangeModified, byPath["type"].ProjectChange)
}

func TestInspectTreeFindsCompleteConflictMarkersAndSpecialFiles(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "conflict.md"), []byte("<<<<<<< project\na\n=======\nb\n>>>>>>> central\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "partial.md"), []byte("<<<<<<< example\n"), 0o644))
	require.NoError(t, syscall.Mkfifo(filepath.Join(root, "pipe"), 0o600))

	snapshot, issues, err := InspectTree(root)
	require.NoError(t, err)
	require.Contains(t, snapshot, "conflict.md")
	require.Contains(t, snapshot, "partial.md")
	require.Len(t, issues, 2)
	require.Equal(t, IssueConflictMarkers, issues[0].Kind)
	require.Equal(t, IssueUnsupportedObject, issues[1].Kind)
}

func TestInspectTreeIgnoresMarkerBytesInBinaryFile(t *testing.T) {
	root := t.TempDir()
	data := []byte("\x00<<<<<<< project\n=======\n>>>>>>> central\n")
	require.NoError(t, os.WriteFile(filepath.Join(root, "binary"), data, 0o644))

	_, issues, err := InspectTree(root)
	require.NoError(t, err)
	require.Empty(t, issues)
}

func TestInspectTreeDoesNotFlagConflictSuffixWithoutMergeContext(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "asset.bin.sync-conflict"), []byte{0, 2}, 0o644))

	_, issues, err := InspectTree(root)
	require.NoError(t, err)
	require.Empty(t, issues)
}

func TestClassifyFlagsKeepBothConflictSibling(t *testing.T) {
	central := Entry{Kind: KindFile, Data: []byte{0, 2}}
	project := Snapshot{
		"asset.bin":               {Kind: KindFile, Data: []byte{0, 1}},
		"asset.bin.sync-conflict": central,
	}

	result := Classify(project, Snapshot{"asset.bin": central}, Snapshot{"asset.bin": central}, nil)
	require.Equal(t, []Issue{{
		Kind: IssueConflictSibling, Path: "asset.bin.sync-conflict",
		Message: "path is an unresolved keep-both conflict sibling",
	}}, result.Issues)
}

func TestInspectTreeReportsUnreadablePathAndContinues(t *testing.T) {
	root := t.TempDir()
	unreadable := filepath.Join(root, "unreadable")
	require.NoError(t, os.WriteFile(unreadable, []byte("secret"), 0o600))
	require.NoError(t, os.Chmod(unreadable, 0))
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o600) })
	require.NoError(t, os.WriteFile(filepath.Join(root, "readable"), []byte("visible"), 0o600))

	snapshot, issues, err := InspectTree(root)
	require.NoError(t, err)
	if len(issues) == 0 {
		t.Skip("current user can read mode-zero files")
	}
	require.Contains(t, snapshot, "readable")
	require.NotContains(t, snapshot, "unreadable")
	require.Equal(t, IssueInspectionFailed, issues[0].Kind)
	require.Equal(t, "unreadable", issues[0].Path)
}
