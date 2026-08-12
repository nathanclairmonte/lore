package reconcile

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
)

// EntryKind identifies Git-reproducible filesystem object types.
type EntryKind string

const (
	KindFile    EntryKind = "file"
	KindSymlink EntryKind = "symlink"
)

// Entry contains the Git-reproducible value of one path.
type Entry struct {
	Kind       EntryKind
	Data       []byte
	Executable bool
}

// Snapshot is a relative-path map of files and symlinks.
type Snapshot map[string]Entry

// IssueKind identifies a condition outside the four sync states.
type IssueKind string

const (
	IssueCopyMissing       IssueKind = "copy-missing"
	IssueCopyInvalid       IssueKind = "copy-invalid"
	IssueUnsupportedObject IssueKind = "unsupported-object"
	IssueConflictMarkers   IssueKind = "conflict-markers"
	IssueConflictSibling   IssueKind = "conflict-sibling"
	IssueCentralDirty      IssueKind = "central-dirty"
	IssueBaseUnreachable   IssueKind = "base-unreachable"
	IssueCentralMissing    IssueKind = "central-missing"
	IssueInspectionFailed  IssueKind = "inspection-failed"
)

// Issue is one typed health problem found during inspection.
type Issue struct {
	Kind    IssueKind
	Path    string
	Message string
}

// SyncState is the public four-state classification.
type SyncState string

const (
	StateInSync       SyncState = "in-sync"
	StateProjectAhead SyncState = "project-ahead"
	StateCentralAhead SyncState = "central-ahead"
	StateDiverged     SyncState = "diverged"
)

// Change describes how one side differs from the base.
type Change string

const (
	ChangeNone     Change = "none"
	ChangeAdded    Change = "added"
	ChangeModified Change = "modified"
	ChangeDeleted  Change = "deleted"
)

// FileResult is the three-way verdict for one path.
type FileResult struct {
	Path          string
	State         SyncState
	ProjectChange Change
	CentralChange Change
	Project       *Entry
	Central       *Entry
	Base          *Entry
}

// Result is the read-only classification of one agent directory.
type Result struct {
	State     SyncState
	BaseStale bool
	Files     []FileResult
	Issues    []Issue
}

// InspectTree snapshots a real directory without following symlinks.
func InspectTree(root string) (Snapshot, []Issue, error) {
	info, err := os.Lstat(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Snapshot{}, []Issue{{Kind: IssueCopyMissing, Path: root, Message: "project copy is missing"}}, nil
		}
		return Snapshot{}, []Issue{{Kind: IssueInspectionFailed, Path: root, Message: err.Error()}}, nil
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Snapshot{}, []Issue{{Kind: IssueCopyInvalid, Path: root, Message: "project copy is not a real directory"}}, nil
	}

	snapshot := Snapshot{}
	issues := []Issue{}
	err = filepath.WalkDir(root, func(path string, dirEntry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			issues = append(issues, Issue{Kind: IssueInspectionFailed, Path: issuePath(root, path), Message: walkErr.Error()})
			if dirEntry != nil && dirEntry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if path == root || dirEntry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		info, err := dirEntry.Info()
		if err != nil {
			issues = append(issues, Issue{Kind: IssueInspectionFailed, Path: rel, Message: err.Error()})
			return nil
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				issues = append(issues, Issue{Kind: IssueInspectionFailed, Path: rel, Message: err.Error()})
				return nil
			}
			snapshot[rel] = Entry{Kind: KindSymlink, Data: []byte(target)}
		case info.Mode().IsRegular():
			data, err := os.ReadFile(path)
			if err != nil {
				issues = append(issues, Issue{Kind: IssueInspectionFailed, Path: rel, Message: err.Error()})
				return nil
			}
			snapshot[rel] = Entry{Kind: KindFile, Data: data, Executable: info.Mode().Perm()&0o111 != 0}
			if hasConflictMarkers(data) {
				issues = append(issues, Issue{Kind: IssueConflictMarkers, Path: rel, Message: "file contains unresolved conflict markers"})
			}
		default:
			issues = append(issues, Issue{Kind: IssueUnsupportedObject, Path: rel, Message: "path is not a regular file or symlink"})
		}
		return nil
	})
	if err != nil {
		return nil, nil, lerr.Wrap(lerr.CodeGenericError, err, fmt.Sprintf("cannot inspect %s", root))
	}
	SortIssues(issues)
	return snapshot, issues, nil
}

func issuePath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." {
		return path
	}
	return filepath.ToSlash(rel)
}

// Classify compares project and central snapshots against their base. Paths
// flagged with IssueUnsupportedObject are excluded from the file records
// because Git cannot reproduce them on either side.
func Classify(project, central, base Snapshot, issues []Issue) Result {
	issues = append(append([]Issue(nil), issues...), conflictSiblingIssues(project, central, base)...)
	unsupported := map[string]struct{}{}
	for _, issue := range issues {
		if issue.Kind == IssueUnsupportedObject {
			unsupported[issue.Path] = struct{}{}
		}
	}
	paths := map[string]struct{}{}
	for _, snapshot := range []Snapshot{project, central, base} {
		for path := range snapshot {
			if _, skip := unsupported[path]; !skip {
				paths[path] = struct{}{}
			}
		}
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)

	result := Result{State: StateInSync, Files: make([]FileResult, 0, len(ordered)), Issues: append([]Issue(nil), issues...)}
	states := make([]SyncState, 0, len(ordered))
	for _, path := range ordered {
		projectEntry, projectOK := project[path]
		centralEntry, centralOK := central[path]
		baseEntry, baseOK := base[path]
		state := classifyEntry(projectEntry, projectOK, centralEntry, centralOK, baseEntry, baseOK)
		file := FileResult{
			Path: path, State: state,
			ProjectChange: changeFromBase(projectEntry, projectOK, baseEntry, baseOK),
			CentralChange: changeFromBase(centralEntry, centralOK, baseEntry, baseOK),
			Project:       entryPointer(projectEntry, projectOK), Central: entryPointer(centralEntry, centralOK),
			Base: entryPointer(baseEntry, baseOK),
		}
		result.Files = append(result.Files, file)
		states = append(states, state)
		if state == StateInSync && file.ProjectChange != ChangeNone && file.CentralChange != ChangeNone {
			result.BaseStale = true
		}
	}
	result.State = Rollup(states)
	SortIssues(result.Issues)
	return result
}

// Rollup combines child sync states into the state of their container. It is
// the single definition of the precedence rule the spec documents, shared by
// the file-to-directory and directory-to-checkout folds.
func Rollup(states []SyncState) SyncState {
	hasProjectAhead := false
	hasCentralAhead := false
	for _, state := range states {
		switch state {
		case StateDiverged:
			return StateDiverged
		case StateProjectAhead:
			hasProjectAhead = true
		case StateCentralAhead:
			hasCentralAhead = true
		}
	}
	switch {
	case hasProjectAhead && hasCentralAhead:
		return StateDiverged
	case hasProjectAhead:
		return StateProjectAhead
	case hasCentralAhead:
		return StateCentralAhead
	default:
		return StateInSync
	}
}

func classifyEntry(project Entry, projectOK bool, central Entry, centralOK bool, base Entry, baseOK bool) SyncState {
	if entriesEqual(project, projectOK, central, centralOK) {
		return StateInSync
	}
	if entriesEqual(project, projectOK, base, baseOK) {
		return StateCentralAhead
	}
	if entriesEqual(central, centralOK, base, baseOK) {
		return StateProjectAhead
	}
	return StateDiverged
}

func changeFromBase(entry Entry, ok bool, base Entry, baseOK bool) Change {
	if entriesEqual(entry, ok, base, baseOK) {
		return ChangeNone
	}
	if !baseOK {
		return ChangeAdded
	}
	if !ok {
		return ChangeDeleted
	}
	return ChangeModified
}

func entriesEqual(left Entry, leftOK bool, right Entry, rightOK bool) bool {
	return leftOK == rightOK && (!leftOK || left.Kind == right.Kind && left.Executable == right.Executable && bytes.Equal(left.Data, right.Data))
}

// entryPointer returns an addressable view of a snapshot entry. Snapshot data
// is never mutated after it is built, so the pointee shares the same bytes.
func entryPointer(entry Entry, ok bool) *Entry {
	if !ok {
		return nil
	}
	pointee := entry
	return &pointee
}

func hasConflictMarkers(data []byte) bool {
	if !isTextData(data) {
		return false
	}
	markers := [3][]byte{[]byte("<<<<<<<"), []byte("======="), []byte(">>>>>>>")}
	stage := 0
	for remaining := data; ; {
		line, rest, more := bytes.Cut(remaining, []byte{'\n'})
		if bytes.HasPrefix(line, markers[stage]) {
			if stage == 2 {
				return true
			}
			stage++
		}
		if !more {
			return false
		}
		remaining = rest
	}
}

func isTextData(data []byte) bool {
	return !bytes.Contains(data, []byte{0}) && utf8.Valid(data)
}

func conflictSiblingIssues(project, central, base Snapshot) []Issue {
	issues := []Issue{}
	for siblingPath := range project {
		if !strings.HasSuffix(siblingPath, ConflictSiblingSuffix) {
			continue
		}
		path := strings.TrimSuffix(siblingPath, ConflictSiblingSuffix)
		_, projectExists := project[path]
		_, centralExists := central[path]
		_, baseExists := base[path]
		// a conflict sibling remains unresolved until it is removed, even if
		// either conflict value is edited or deleted during resolution.
		if projectExists || centralExists || baseExists {
			issues = append(issues, Issue{Kind: IssueConflictSibling, Path: siblingPath, Message: "path is an unresolved keep-both conflict sibling"})
		}
	}
	return issues
}

// SortIssues orders issues by path then kind so output stays stable.
func SortIssues(issues []Issue) {
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Path == issues[j].Path {
			return issues[i].Kind < issues[j].Kind
		}
		return issues[i].Path < issues[j].Path
	})
}
