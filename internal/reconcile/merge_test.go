package reconcile

import (
	"testing"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/stretchr/testify/require"
)

func TestMergeSnapshotsCombinesNonOverlappingTextEdits(t *testing.T) {
	base := Snapshot{"notes.md": textEntry("one\ntwo\nthree\n")}
	project := Snapshot{"notes.md": textEntry("ONE\ntwo\nthree\n")}
	central := Snapshot{"notes.md": textEntry("one\ntwo\nTHREE\n")}

	result, err := MergeSnapshots(project, central, base)
	require.NoError(t, err)
	require.Empty(t, result.Conflicts)
	require.Equal(t, "ONE\ntwo\nTHREE\n", string(result.Snapshot["notes.md"].Data))
}

func TestMergeSnapshotsWritesTextConflictMarkers(t *testing.T) {
	base := Snapshot{"notes.md": textEntry("base\n")}
	project := Snapshot{"notes.md": textEntry("project\n")}
	central := Snapshot{"notes.md": textEntry("central\n")}

	result, err := MergeSnapshots(project, central, base)
	require.NoError(t, err)
	require.Equal(t, []MergeConflict{{Path: "notes.md", Kind: ConflictText}}, result.Conflicts)
	require.Equal(t, "<<<<<<< project\nproject\n=======\ncentral\n>>>>>>> central\n",
		string(result.Snapshot["notes.md"].Data))
}

func TestMergeSnapshotsKeepsBothBinaryValues(t *testing.T) {
	base := Snapshot{"asset.bin": binaryEntry(0)}
	project := Snapshot{"asset.bin": binaryEntry(1)}
	central := Snapshot{"asset.bin": binaryEntry(2)}

	result, err := MergeSnapshots(project, central, base)
	require.NoError(t, err)
	require.Equal(t, []MergeConflict{{
		Path: "asset.bin", Kind: ConflictKeepBoth, SiblingPath: "asset.bin.sync-conflict",
	}}, result.Conflicts)
	require.Equal(t, []byte{0, 1}, result.Snapshot["asset.bin"].Data)
	require.Equal(t, []byte{0, 2}, result.Snapshot["asset.bin.sync-conflict"].Data)
}

func TestMergeSnapshotsRefusesOccupiedConflictSibling(t *testing.T) {
	base := Snapshot{"asset.bin": binaryEntry(0)}
	project := Snapshot{"asset.bin": binaryEntry(1), "asset.bin.sync-conflict": textEntry("older\n")}
	central := Snapshot{"asset.bin": binaryEntry(2), "asset.bin.sync-conflict": textEntry("older\n")}

	_, err := MergeSnapshots(project, central, base)
	require.Error(t, err)
	require.Equal(t, lerr.CodeRefusedSafety, lerr.CodeOf(err))
}

func TestMergeSnapshotsRefusesModifyDeleteConflict(t *testing.T) {
	base := Snapshot{"notes.md": textEntry("base\n")}
	project := Snapshot{}
	central := Snapshot{"notes.md": textEntry("central\n")}

	_, err := MergeSnapshots(project, central, base)
	require.Error(t, err)
	require.Equal(t, lerr.CodeRefusedSafety, lerr.CodeOf(err))
}

func TestMergeFilesRefusesFileAndChildPathCollision(t *testing.T) {
	_, err := MergeFiles([]FileResult{
		{Path: "notes", State: StateInSync, Project: entryPointer(textEntry("file\n"), true)},
		{Path: "notes/x.md", State: StateCentralAhead, Central: entryPointer(textEntry("child\n"), true)},
	})
	require.Error(t, err)
	require.Equal(t, lerr.CodeRefusedSafety, lerr.CodeOf(err))
}

func textEntry(data string) Entry {
	return Entry{Kind: KindFile, Data: []byte(data)}
}

func binaryEntry(value byte) Entry {
	return Entry{Kind: KindFile, Data: []byte{0, value}}
}
