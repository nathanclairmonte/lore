package reconcile

import (
	"sort"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/git"
)

// ConflictKind identifies the unresolved representation written by pull.
type ConflictKind string

const (
	ConflictText          ConflictKind = "text"
	ConflictKeepBoth      ConflictKind = "keep-both"
	ConflictSiblingSuffix              = ".sync-conflict"
)

// MergeConflict describes one unresolved path and its optional sibling.
type MergeConflict struct {
	Path        string
	Kind        ConflictKind
	SiblingPath string
}

// MergeResult contains the project-side tree produced by reconciliation.
type MergeResult struct {
	Snapshot  Snapshot
	Conflicts []MergeConflict
}

// MergeSnapshots reconciles project and central snapshots against base.
func MergeSnapshots(project, central, base Snapshot) (MergeResult, error) {
	return MergeFiles(Classify(project, central, base, nil).Files)
}

// MergeFiles reconciles an existing three-way file classification.
func MergeFiles(files []FileResult) (MergeResult, error) {
	merged := Snapshot{}
	occupied := make(map[string]struct{}, len(files))
	for _, file := range files {
		occupied[file.Path] = struct{}{}
		if file.Project != nil {
			merged[file.Path] = cloneEntry(*file.Project)
		}
	}
	result := MergeResult{Snapshot: merged, Conflicts: []MergeConflict{}}

	for _, file := range files {
		switch file.State {
		case StateInSync, StateProjectAhead:
			continue
		case StateCentralAhead:
			setSnapshotEntry(merged, file.Path, file.Central)
			continue
		case StateDiverged:
			conflict, err := mergeDivergedFile(merged, occupied, file)
			if err != nil {
				return MergeResult{}, err
			}
			if conflict != nil {
				result.Conflicts = append(result.Conflicts, *conflict)
			}
		}
	}
	sort.Slice(result.Conflicts, func(i, j int) bool {
		return result.Conflicts[i].Path < result.Conflicts[j].Path
	})
	if err := validateSnapshot(result.Snapshot); err != nil {
		return MergeResult{}, err
	}
	return result, nil
}

func mergeDivergedFile(merged Snapshot, occupied map[string]struct{}, file FileResult) (*MergeConflict, error) {
	if file.Project == nil || file.Central == nil {
		return nil, lerr.Newf(lerr.CodeRefusedSafety,
			"path %s has a modify/delete conflict", file.Path)
	}
	if file.Project.Kind != file.Central.Kind || file.Base != nil && file.Base.Kind != file.Project.Kind {
		return nil, lerr.Newf(lerr.CodeRefusedSafety,
			"path %s has an incompatible file-type conflict", file.Path)
	}

	if file.Project.Kind == KindFile && mergeableText(file.Project, file.Base, file.Central) {
		baseData := []byte{}
		if file.Base != nil {
			baseData = file.Base.Data
		}
		data, conflicted, err := git.MergeFile(file.Project.Data, baseData, file.Central.Data)
		if err != nil {
			return nil, err
		}
		merged[file.Path] = Entry{
			Kind: KindFile, Data: data,
			Executable: mergeExecutable(file.Project, file.Base, file.Central),
		}
		if conflicted {
			return &MergeConflict{Path: file.Path, Kind: ConflictText}, nil
		}
		return nil, nil
	}

	sibling := file.Path + ConflictSiblingSuffix
	if _, exists := occupied[sibling]; exists {
		return nil, lerr.Newf(lerr.CodeRefusedSafety,
			"cannot preserve conflict for %s because %s already exists", file.Path, sibling)
	}
	occupied[sibling] = struct{}{}
	merged[sibling] = cloneEntry(*file.Central)
	return &MergeConflict{Path: file.Path, Kind: ConflictKeepBoth, SiblingPath: sibling}, nil
}

func mergeableText(entries ...*Entry) bool {
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		if entry.Kind != KindFile || !isTextData(entry.Data) {
			return false
		}
	}
	return true
}

func mergeExecutable(project, base, central *Entry) bool {
	if project.Executable == central.Executable {
		return project.Executable
	}
	if base == nil || project.Executable == base.Executable {
		return central.Executable
	}
	return project.Executable
}

func cloneEntry(entry Entry) Entry {
	entry.Data = append([]byte(nil), entry.Data...)
	return entry
}

func setSnapshotEntry(snapshot Snapshot, path string, entry *Entry) {
	if entry == nil {
		delete(snapshot, path)
		return
	}
	snapshot[path] = cloneEntry(*entry)
}
