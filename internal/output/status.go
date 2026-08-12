package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nathanclairmonte/lore/internal/config"
	"github.com/pmezard/go-difflib/difflib"
)

// SyncState mirrors reconcile.SyncState so this package doesn't import
// reconcile. cmd/lore/status.go translates the classifier's verdicts into it.
type SyncState string

const (
	SyncInSync       SyncState = "in-sync"
	SyncProjectAhead SyncState = "project-ahead"
	SyncCentralAhead SyncState = "central-ahead"
	SyncDiverged     SyncState = "diverged"
)

// SyncEntryType mirrors reconcile.EntryKind.
type SyncEntryType string

const (
	SyncEntryFile    SyncEntryType = "file"
	SyncEntrySymlink SyncEntryType = "symlink"
)

// SyncIssueKind mirrors reconcile.IssueKind.
type SyncIssueKind string

const (
	SyncIssueCopyMissing       SyncIssueKind = "copy-missing"
	SyncIssueCopyInvalid       SyncIssueKind = "copy-invalid"
	SyncIssueUnsupportedObject SyncIssueKind = "unsupported-object"
	SyncIssueConflictMarkers   SyncIssueKind = "conflict-markers"
	SyncIssueConflictSibling   SyncIssueKind = "conflict-sibling"
	SyncIssueCentralDirty      SyncIssueKind = "central-dirty"
	SyncIssueBaseUnreachable   SyncIssueKind = "base-unreachable"
	SyncIssueCentralMissing    SyncIssueKind = "central-missing"
	SyncIssueInspectionFailed  SyncIssueKind = "inspection-failed"
)

// SyncEntry is one side's file metadata and content.
type SyncEntry struct {
	Type       SyncEntryType
	Executable bool
	Data       []byte
}

// SyncFileResult is one path's three-way classification.
type SyncFileResult struct {
	Path          string
	State         SyncState
	ProjectChange string
	CentralChange string
	Project       *SyncEntry
	Central       *SyncEntry
	Base          *SyncEntry
}

// SyncIssue is one health condition outside ordinary drift.
type SyncIssue struct {
	Kind    SyncIssueKind `json:"kind"`
	Path    string        `json:"path"`
	Message string        `json:"message"`
}

// SyncDirResult is one tracked agent directory.
type SyncDirResult struct {
	Name      string
	State     SyncState
	BaseStale bool
	Files     []SyncFileResult
	Issues    []SyncIssue
}

// SyncCheckoutResult is one independently tracked checkout.
type SyncCheckoutResult struct {
	Path      string
	SyncBase  string
	State     SyncState
	BaseStale bool
	Dirs      []SyncDirResult
	Issues    []SyncIssue
}

// SyncProjectResult groups a project's checkouts.
type SyncProjectResult struct {
	Name      string
	Checkouts []SyncCheckoutResult
	Issues    []SyncIssue
}

// SyncResult is the shared status and diff rendering model.
type SyncResult struct {
	CentralRepo string
	CentralHead string
	Projects    []SyncProjectResult
}

// PrintStatus renders the complete sync-state report.
func PrintStatus(result SyncResult) {
	if mode == ModeJSON {
		_ = json.NewEncoder(Out).Encode(syncEnvelope(result))
		return
	}
	if len(result.Projects) == 0 {
		Success("No tracked projects")
		return
	}
	for projectIndex, project := range result.Projects {
		if projectIndex > 0 {
			Blank()
		}
		_, _ = fmt.Fprintf(Out, "%s\n", project.Name)
		printIssues(project.Issues, "  ")
		for _, checkout := range project.Checkouts {
			_, _ = fmt.Fprintf(Out, "  %s  %s", stylePath().Render(config.Contract(checkout.Path)), checkout.State)
			if checkout.BaseStale {
				_, _ = fmt.Fprint(Out, "  (base stale)")
			}
			_, _ = fmt.Fprintln(Out)
			for _, dir := range checkout.Dirs {
				counts := countStates(dir.Files)
				_, _ = fmt.Fprintf(Out, "    %s  %s  P:%d C:%d D:%d\n",
					dir.Name, dir.State, counts.ProjectAhead, counts.CentralAhead, counts.Diverged)
				for _, file := range dir.Files {
					if file.State == SyncInSync {
						continue
					}
					_, _ = fmt.Fprintf(Out, "      %s  %s\n", file.State, file.Path)
				}
				printDirIssueGroup(dir, "      ")
			}
			printIssues(checkout.Issues, "    ")
		}
	}
}

// PrintDiff renders project-copy changes against committed central HEAD.
func PrintDiff(result SyncResult) {
	if mode == ModeJSON {
		_ = json.NewEncoder(Out).Encode(syncEnvelope(result))
		return
	}
	for projectIndex, project := range result.Projects {
		if projectIndex > 0 {
			Blank()
		}
		if hasIssue(project.Issues, SyncIssueCentralDirty) {
			_, _ = fmt.Fprintf(Out, "%s\n", project.Name)
			_, _ = fmt.Fprintln(Out, "  central copy is dirty; patches are suppressed")
			printIssues(project.Issues, "  ", SyncIssueCentralDirty)
			for _, checkout := range project.Checkouts {
				printDirIssues(checkout.Dirs, "  ")
				printIssues(checkout.Issues, "  ")
			}
			continue
		}
		for checkoutIndex, checkout := range project.Checkouts {
			if checkoutIndex > 0 {
				Blank()
			}
			_, _ = fmt.Fprintf(Out, "%s  %s\n", project.Name, stylePath().Render(config.Contract(checkout.Path)))
			fileCount := 0
			for _, dir := range checkout.Dirs {
				for _, file := range dir.Files {
					fileCount++
					printFileDiff(dir.Name, file)
				}
			}
			if fileCount == 0 && !hasCheckoutIssues(checkout) {
				_, _ = fmt.Fprintln(Out, "  no differences")
			}
			printDirIssues(checkout.Dirs, "  ")
			printIssues(checkout.Issues, "  ", SyncIssueCentralDirty)
		}
	}
}

func hasCheckoutIssues(checkout SyncCheckoutResult) bool {
	if len(checkout.Issues) > 0 {
		return true
	}
	for _, dir := range checkout.Dirs {
		if len(dir.Issues) > 0 {
			return true
		}
	}
	return false
}

// devNull names an absent patch side, matching git's own diff headers.
const devNull = "/dev/null"

func printFileDiff(dirName string, file SyncFileResult) {
	filePath := dirName + "/" + file.Path
	oldLines, oldName := patchSide(file.Central, "central/"+filePath)
	newLines, newName := patchSide(file.Project, "project/"+filePath)
	// with both sides present a patch needs both to be text files; with one
	// side absent the present side alone is patched against /dev/null.
	bothPresent := file.Project != nil && file.Central != nil
	patchable := oldName != devNull && newName != devNull ||
		!bothPresent && (oldName != devNull || newName != devNull)

	patch := ""
	if patchable {
		patch, _ = difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
			A: oldLines, B: newLines,
			FromFile: oldName, ToFile: newName, Context: 3,
		})
		if patch != "" {
			_, _ = fmt.Fprint(Out, patch)
		}
	}
	// an executable-bit flip produces no patch text, so name it outright.
	if bothPresent && patchable && file.Project.Executable != file.Central.Executable {
		_, _ = fmt.Fprintf(Out, "mode changed: %s executable %t -> %t\n", filePath, file.Central.Executable, file.Project.Executable)
		return
	}
	if patch != "" {
		return
	}
	_, _ = fmt.Fprintf(Out, "%s  %s (project: %s, central: %s)\n",
		file.State, filePath, entryDescription(file.Project), entryDescription(file.Central))
}

// patchSide returns one side's patch input. Anything Git cannot render as a
// text hunk - an absent entry, a symlink, or binary content - reads as absent
// so the caller falls back to a metadata record.
func patchSide(entry *SyncEntry, name string) ([]string, string) {
	if entry == nil || entry.Type != SyncEntryFile || !isText(entry.Data) {
		return nil, devNull
	}
	return patchLines(entry.Data), name
}

func patchLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	lines := strings.SplitAfter(string(data), "\n")
	if lines[len(lines)-1] == "" {
		return lines[:len(lines)-1]
	}
	lines[len(lines)-1] += "\n\\ No newline at end of file\n"
	return lines
}

func isText(data []byte) bool {
	return !bytes.Contains(data, []byte{0}) && utf8.Valid(data)
}

// printIssues renders health issues at the caller's indent level, omitting any
// kind the caller has already reported in a dedicated line.
func printIssues(issues []SyncIssue, indent string, skip ...SyncIssueKind) {
	for _, issue := range issues {
		if slices.Contains(skip, issue.Kind) {
			continue
		}
		printIssue(issue, indent)
	}
}

func printIssue(issue SyncIssue, indent string) {
	_, _ = fmt.Fprintf(Out, "%s%s %s  %s: %s\n",
		indent, styleWarn().Render(activeGlyphs().warn), issue.Kind,
		stylePath().Render(config.Contract(issue.Path)), issue.Message)
}

func printDirIssues(dirs []SyncDirResult, indent string) {
	for _, dir := range dirs {
		printDirIssueGroup(dir, indent)
	}
}

func printDirIssueGroup(dir SyncDirResult, indent string) {
	for _, issue := range dir.Issues {
		if !filepath.IsAbs(issue.Path) {
			issue.Path = path.Join(dir.Name, issue.Path)
		}
		printIssue(issue, indent)
	}
}

func entryDescription(entry *SyncEntry) string {
	if entry == nil {
		return "absent"
	}
	if entry.Type == SyncEntryFile && entry.Executable {
		return "executable file"
	}
	return string(entry.Type)
}

// SyncCounts contains stable file totals for every sync state.
type SyncCounts struct {
	InSync       int `json:"in_sync"`
	ProjectAhead int `json:"project_ahead"`
	CentralAhead int `json:"central_ahead"`
	Diverged     int `json:"diverged"`
}

func countStates(files []SyncFileResult) SyncCounts {
	counts := SyncCounts{}
	for _, file := range files {
		switch file.State {
		case SyncInSync:
			counts.InSync++
		case SyncProjectAhead:
			counts.ProjectAhead++
		case SyncCentralAhead:
			counts.CentralAhead++
		case SyncDiverged:
			counts.Diverged++
		}
	}
	return counts
}

func hasIssue(issues []SyncIssue, kind SyncIssueKind) bool {
	for _, issue := range issues {
		if issue.Kind == kind {
			return true
		}
	}
	return false
}

type syncJSONEntry struct {
	Type       string `json:"type"`
	Executable bool   `json:"executable"`
}

type syncJSONFile struct {
	Path          string         `json:"path"`
	State         string         `json:"state"`
	ProjectChange string         `json:"project_change"`
	CentralChange string         `json:"central_change"`
	Project       *syncJSONEntry `json:"project"`
	Central       *syncJSONEntry `json:"central"`
	Base          *syncJSONEntry `json:"base"`
}

type syncJSONDir struct {
	Name      string         `json:"name"`
	State     string         `json:"state"`
	BaseStale bool           `json:"base_stale"`
	Counts    SyncCounts     `json:"counts"`
	Files     []syncJSONFile `json:"files"`
	Issues    []SyncIssue    `json:"issues"`
}

type syncJSONCheckout struct {
	Path      string        `json:"path"`
	SyncBase  string        `json:"sync_base"`
	State     string        `json:"state"`
	BaseStale bool          `json:"base_stale"`
	Dirs      []syncJSONDir `json:"dirs"`
	Issues    []SyncIssue   `json:"issues"`
}

type syncJSONProject struct {
	Name      string             `json:"name"`
	Checkouts []syncJSONCheckout `json:"checkouts"`
	Issues    []SyncIssue        `json:"issues"`
}

type syncJSONEnvelope struct {
	CentralRepo string            `json:"central_repo"`
	CentralHead string            `json:"central_head"`
	Projects    []syncJSONProject `json:"projects"`
}

func syncEnvelope(result SyncResult) syncJSONEnvelope {
	envelope := syncJSONEnvelope{CentralRepo: result.CentralRepo, CentralHead: result.CentralHead, Projects: make([]syncJSONProject, 0, len(result.Projects))}
	for _, project := range result.Projects {
		jsonProject := syncJSONProject{
			Name: project.Name, Checkouts: make([]syncJSONCheckout, 0, len(project.Checkouts)),
			Issues: nonNilIssues(project.Issues),
		}
		for _, checkout := range project.Checkouts {
			jsonCheckout := syncJSONCheckout{
				Path: checkout.Path, SyncBase: checkout.SyncBase, State: string(checkout.State), BaseStale: checkout.BaseStale,
				Dirs: make([]syncJSONDir, 0, len(checkout.Dirs)), Issues: nonNilIssues(checkout.Issues),
			}
			for _, dir := range checkout.Dirs {
				jsonDir := syncJSONDir{
					Name: dir.Name, State: string(dir.State), BaseStale: dir.BaseStale, Counts: countStates(dir.Files),
					Files: make([]syncJSONFile, 0, len(dir.Files)), Issues: nonNilIssues(dir.Issues),
				}
				for _, file := range dir.Files {
					jsonDir.Files = append(jsonDir.Files, syncJSONFile{
						Path: file.Path, State: string(file.State), ProjectChange: file.ProjectChange, CentralChange: file.CentralChange,
						Project: jsonEntry(file.Project), Central: jsonEntry(file.Central), Base: jsonEntry(file.Base),
					})
				}
				jsonCheckout.Dirs = append(jsonCheckout.Dirs, jsonDir)
			}
			jsonProject.Checkouts = append(jsonProject.Checkouts, jsonCheckout)
		}
		envelope.Projects = append(envelope.Projects, jsonProject)
	}
	return envelope
}

func jsonEntry(entry *SyncEntry) *syncJSONEntry {
	if entry == nil {
		return nil
	}
	return &syncJSONEntry{Type: string(entry.Type), Executable: entry.Executable}
}

func nonNilIssues(issues []SyncIssue) []SyncIssue {
	if issues == nil {
		return []SyncIssue{}
	}
	return issues
}
