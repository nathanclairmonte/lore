package output

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/stretchr/testify/require"
)

// updateGolden, when set, rewrites the .golden fixtures under testdata
// with the test's actual output. Run `go test ./internal/output -update`
// after intentionally changing a printer's formatting; otherwise tests
// fail on a diff. Conventional Go pattern (e.g. gofmt, gopls).
var updateGolden = flag.Bool("update", false, "rewrite golden fixture files")

// goldenPath resolves a fixture name to the absolute testdata path.
func goldenPath(name string) string {
	return filepath.Join("testdata", name+".golden")
}

// assertGolden compares got against the named golden fixture. With
// -update, writes got and passes.
func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	path := goldenPath(name)
	if *updateGolden {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoErrorf(t, err, "missing fixture %s - run `go test ./internal/output -update`", path)
	require.Equal(t, string(want), got, "fixture %s out of date - run `go test ./internal/output -update` if the change is intentional", path)
}

// captureBoth swaps Out and Err for buffers for the duration of t and
// returns them. Mirrors the pattern in cmd/lore/path_test.go's
// capturedOut helper.
func captureBoth(t *testing.T) (out, errBuf *bytes.Buffer) {
	t.Helper()
	out = &bytes.Buffer{}
	errBuf = &bytes.Buffer{}
	prevOut, prevErr := Out, Err
	Out, Err = out, errBuf
	t.Cleanup(func() { Out, Err = prevOut, prevErr })
	return out, errBuf
}

// withCleanState pins all four output knobs (Out, Err, mode, color,
// unicode) to deterministic values for the duration of t. Goldens are
// captured with color OFF so fixtures stay plain UTF-8 / ASCII text
// rather than embedding lipgloss's ANSI escapes (which would make
// fixtures brittle against lipgloss updates).
func withCleanState(t *testing.T, unicode bool) (out, errBuf *bytes.Buffer) {
	t.Helper()
	out, errBuf = captureBoth(t)
	SetModeForTest(t, ModeHuman)
	SetColorForTest(t, false)
	SetUnicodeForTest(t, unicode)
	return out, errBuf
}

// --- glyph fallback golden tests -------------------------------------

func TestGolden_Success_Unicode(t *testing.T) {
	out, _ := withCleanState(t, true)
	Success("project added at %s", "/tmp/x")
	assertGolden(t, "success_unicode", out.String())
}

func TestGolden_Success_ASCII(t *testing.T) {
	out, _ := withCleanState(t, false)
	Success("project added at %s", "/tmp/x")
	assertGolden(t, "success_ascii", out.String())
}

func TestGolden_Warn_Unicode(t *testing.T) {
	out, _ := withCleanState(t, true)
	Warn("no remote configured")
	assertGolden(t, "warn_unicode", out.String())
}

func TestGolden_Warn_ASCII(t *testing.T) {
	out, _ := withCleanState(t, false)
	Warn("no remote configured")
	assertGolden(t, "warn_ascii", out.String())
}

func TestGolden_Hint(t *testing.T) {
	// hint never uses a glyph; one fixture covers both modes.
	out, _ := withCleanState(t, true)
	Hint("run `lore init` first")
	assertGolden(t, "hint", out.String())
}

func TestGolden_PrintError_NoHint_Unicode(t *testing.T) {
	_, errBuf := withCleanState(t, true)
	PrintError(lerr.New(lerr.CodeNotFound, "central repo not found"))
	assertGolden(t, "error_nohint_unicode", errBuf.String())
}

func TestGolden_PrintError_NoHint_ASCII(t *testing.T) {
	_, errBuf := withCleanState(t, false)
	PrintError(lerr.New(lerr.CodeNotFound, "central repo not found"))
	assertGolden(t, "error_nohint_ascii", errBuf.String())
}

func TestGolden_PrintError_WithHint_Unicode(t *testing.T) {
	_, errBuf := withCleanState(t, true)
	PrintError(lerr.New(lerr.CodeNotFound, "central repo not found").
		WithHint("run `lore init` first"))
	assertGolden(t, "error_withhint_unicode", errBuf.String())
}

func TestGolden_PrintError_WithHint_ASCII(t *testing.T) {
	_, errBuf := withCleanState(t, false)
	PrintError(lerr.New(lerr.CodeNotFound, "central repo not found").
		WithHint("run `lore init` first"))
	assertGolden(t, "error_withhint_ascii", errBuf.String())
}

// --- composite-printer golden tests ----------------------------------
//
// These exercise PrintAddResult / PrintInitResult, which compose Success
// + Blank + raw Fprintf for the arrows. The arrow row is the place glyph
// degradation matters most (it's the only line that uses → ←).

func sampleAddResult() AddResult {
	return AddResult{
		ProjectName:   "my-app",
		ProjectPath:   "/home/u/code/my-app",
		CentralRepo:   "/home/u/agent-context",
		CommitMessage: "feat: add my-app",
		CommitSHA:     "0123456789abcdef0123456789abcdef01234567",
		SyncBase:      "0123456789abcdef0123456789abcdef01234567",
		Dirs: []AddDirResult{{
			Name:        ".claude",
			Status:      StatusAdded,
			CentralPath: "/home/u/agent-context/projects/my-app/.claude",
		}},
	}
}

// sampleMultiAddResult covers the slice-5 multi-dir case (two dirs
// added in one run). golden locks the per-dir block iteration in place.
func sampleMultiAddResult() AddResult {
	return AddResult{
		ProjectName:   "my-app",
		ProjectPath:   "/home/u/code/my-app",
		CentralRepo:   "/home/u/agent-context",
		CommitMessage: "feat: add my-app",
		CommitSHA:     "0123456789abcdef0123456789abcdef01234567",
		SyncBase:      "0123456789abcdef0123456789abcdef01234567",
		Dirs: []AddDirResult{
			{
				Name:        ".claude",
				Status:      StatusAdded,
				CentralPath: "/home/u/agent-context/projects/my-app/.claude",
			},
			{
				Name:        ".cursor",
				Status:      StatusAdded,
				CentralPath: "/home/u/agent-context/projects/my-app/.cursor",
			},
		},
	}
}

// samplePartialAddResult covers the slice-5 partial-failure case: one
// dir landed, one was refused for safety. golden locks the layout: the
// added block renders normally, the skipped block follows with a "warn"
// header and the violation message indented under it.
func samplePartialAddResult() AddResult {
	return AddResult{
		ProjectName:   "my-app",
		ProjectPath:   "/home/u/code/my-app",
		CentralRepo:   "/home/u/agent-context",
		CommitMessage: "feat: add my-app",
		CommitSHA:     "0123456789abcdef0123456789abcdef01234567",
		SyncBase:      "0123456789abcdef0123456789abcdef01234567",
		Dirs: []AddDirResult{
			{
				Name:        ".cursor",
				Status:      StatusAdded,
				CentralPath: "/home/u/agent-context/projects/my-app/.cursor",
			},
			{
				Name:   ".claude",
				Status: StatusSkippedSafety,
				SafetyViolations: []AddSafetyViolation{
					{Kind: "tracked-files", Message: ".claude/ contains files tracked by this repo's git."},
				},
			},
		},
	}
}

func sampleSyncResult() SyncResult {
	return SyncResult{
		CentralRepo: "/home/u/agent-context",
		CentralHead: "89abcdef0123456789abcdef0123456789abcdef",
		Projects: []SyncProjectResult{{
			Name: "my-app",
			Checkouts: []SyncCheckoutResult{{
				Path: "/home/u/code/my-app", SyncBase: "0123456789abcdef0123456789abcdef01234567",
				State: "diverged", Dirs: []SyncDirResult{{
					Name: ".claude", State: "diverged", Files: []SyncFileResult{
						{
							Path: "CLAUDE.md", State: "diverged", ProjectChange: "modified", CentralChange: "modified",
							Project: &SyncEntry{Type: "file", Data: []byte("project\n")},
							Central: &SyncEntry{Type: "file", Data: []byte("central\n")},
							Base:    &SyncEntry{Type: "file", Data: []byte("base\n")},
						},
						{
							Path: "notes.md", State: "project-ahead", ProjectChange: "added", CentralChange: "none",
							Project: &SyncEntry{Type: "file", Data: []byte("notes\n")},
						},
					}, Issues: []SyncIssue{{Kind: SyncIssueConflictMarkers, Path: "CLAUDE.md", Message: "file contains unresolved conflict markers"}},
				}}, Issues: []SyncIssue{},
			}},
		}},
	}
}

func TestGolden_PrintStatus_Unicode(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	out, _ := withCleanState(t, true)
	PrintStatus(sampleSyncResult())
	assertGolden(t, "status_unicode", out.String())
}

func TestGolden_PrintStatus_ASCII(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	out, _ := withCleanState(t, false)
	PrintStatus(sampleSyncResult())
	assertGolden(t, "status_ascii", out.String())
}

func TestGolden_PrintStatus_JSON(t *testing.T) {
	out, _ := captureBoth(t)
	SetModeForTest(t, ModeJSON)
	PrintStatus(sampleSyncResult())
	assertGolden(t, "status_json", out.String())
}

func TestGolden_PrintDiff_Human(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	out, _ := withCleanState(t, false)
	PrintDiff(sampleSyncResult())
	assertGolden(t, "diff_human", out.String())
}

func TestGolden_PrintDiff_JSON(t *testing.T) {
	out, _ := captureBoth(t)
	SetModeForTest(t, ModeJSON)
	PrintDiff(sampleSyncResult())
	assertGolden(t, "diff_json", out.String())
}

func TestPrintDiffReportsEmptyAndBinaryFilesWithoutPatches(t *testing.T) {
	out, _ := withCleanState(t, false)
	result := SyncResult{Projects: []SyncProjectResult{{
		Name: "my-app", Checkouts: []SyncCheckoutResult{{
			Path: "/work/my-app", Dirs: []SyncDirResult{{Name: ".claude", Files: []SyncFileResult{
				{Path: "empty", State: "project-ahead", Project: &SyncEntry{Type: "file"}},
				{Path: "binary", State: "diverged", Project: &SyncEntry{Type: "file", Data: []byte{0, 1}}, Central: &SyncEntry{Type: "file", Data: []byte{0, 2}}},
			}}},
		}},
	}}}
	PrintDiff(result)
	require.Contains(t, out.String(), "project-ahead  .claude/empty")
	require.Contains(t, out.String(), "diverged  .claude/binary")
	require.NotContains(t, out.String(), "--- central/")
}

func TestPrintDiffProducesApplicableAddedFilePatch(t *testing.T) {
	out, _ := withCleanState(t, false)
	result := SyncResult{Projects: []SyncProjectResult{{
		Name: "my-app", Checkouts: []SyncCheckoutResult{{
			Path: "/work/my-app", Dirs: []SyncDirResult{{Name: ".claude", Files: []SyncFileResult{{
				Path: "notes.md", State: SyncProjectAhead,
				Project: &SyncEntry{Type: SyncEntryFile, Data: []byte("notes\n")},
			}}}},
		}},
	}}}
	PrintDiff(result)
	require.Contains(t, out.String(), "@@ -0,0 +1 @@")

	worktree := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(worktree, ".claude"), 0o755))
	cmd := exec.Command("git", "apply", "--check", "-")
	cmd.Dir = worktree
	cmd.Stdin = strings.NewReader(out.String())
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
}

func TestPrintDiffMarksMissingFinalNewline(t *testing.T) {
	out, _ := withCleanState(t, false)
	result := SyncResult{Projects: []SyncProjectResult{{
		Name: "my-app", Checkouts: []SyncCheckoutResult{{
			Path: "/work/my-app", Dirs: []SyncDirResult{{Name: ".claude", Files: []SyncFileResult{{
				Path: "notes.md", State: SyncDiverged,
				Project: &SyncEntry{Type: SyncEntryFile, Data: []byte("project")},
				Central: &SyncEntry{Type: SyncEntryFile, Data: []byte("central")},
			}}}},
		}},
	}}}
	PrintDiff(result)
	require.Equal(t, 2, strings.Count(out.String(), "\\ No newline at end of file"))

	worktree := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(worktree, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(worktree, ".claude", "notes.md"), []byte("central"), 0o644))
	cmd := exec.Command("git", "apply", "--check", "-")
	cmd.Dir = worktree
	cmd.Stdin = strings.NewReader(out.String())
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
}

func TestGolden_PrintAddResult_Fresh_Unicode(t *testing.T) {
	t.Setenv("HOME", "/home/u") // so config.Contract turns /home/u/... into ~/...
	out, _ := withCleanState(t, true)
	PrintAddResult(sampleAddResult())
	assertGolden(t, "add_fresh_unicode", out.String())
}

func TestGolden_PrintAddResult_Fresh_ASCII(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	out, _ := withCleanState(t, false)
	PrintAddResult(sampleAddResult())
	assertGolden(t, "add_fresh_ascii", out.String())
}

func TestGolden_PrintAddResult_MultiDir(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	out, _ := withCleanState(t, true)
	PrintAddResult(sampleMultiAddResult())
	assertGolden(t, "add_multi_dir", out.String())
}

func TestGolden_PrintAddResult_PartialFailure(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	out, _ := withCleanState(t, true)
	PrintAddResult(samplePartialAddResult())
	assertGolden(t, "add_partial_failure", out.String())
}

func TestGolden_PrintAddResult_JSON(t *testing.T) {
	out, _ := captureBoth(t)
	SetModeForTest(t, ModeJSON)
	PrintAddResult(sampleAddResult())
	assertGolden(t, "add_fresh_json", out.String())
}

func TestGolden_PrintAddResult_AlreadyTracked(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	out, _ := withCleanState(t, true)
	r := sampleAddResult()
	r.Dirs[0].Status = StatusAlreadyTracked
	PrintAddResult(r)
	assertGolden(t, "add_already_tracked", out.String())
}

func TestGolden_PrintAddResult_AlreadyTrackedMultiDir(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	out, _ := withCleanState(t, true)
	r := sampleMultiAddResult()
	r.Dirs[0].Status = StatusAlreadyTracked
	r.Dirs[1].Status = StatusAlreadyTracked
	r.CommitMessage = ""
	PrintAddResult(r)
	assertGolden(t, "add_already_tracked_multi", out.String())
}

func sampleInitResult(hasRemote, recreated bool) InitResult {
	return InitResult{
		CentralPath: "/home/u/agent-context",
		ConfigPath:  "/home/u/.config/lore/config.toml",
		HasRemote:   hasRemote,
		Recreated:   recreated,
	}
}

func TestGolden_PrintInitResult_Fresh_NoRemote(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	out, _ := withCleanState(t, true)
	PrintInitResult(sampleInitResult(false, false))
	assertGolden(t, "init_fresh_no_remote", out.String())
}

func TestGolden_PrintInitResult_Fresh_WithRemote(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	out, _ := withCleanState(t, true)
	PrintInitResult(sampleInitResult(true, false))
	assertGolden(t, "init_fresh_with_remote", out.String())
}

func TestGolden_PrintInitResult_Recreated(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	out, _ := withCleanState(t, true)
	PrintInitResult(sampleInitResult(true, true))
	assertGolden(t, "init_recreated", out.String())
}

// --- PrintPath: human + JSON envelope --------------------------------

func TestGolden_PrintPath_Human(t *testing.T) {
	out, _ := withCleanState(t, true)
	PrintPath("/home/u/agent-context/projects/my-app/.claude")
	assertGolden(t, "path_human", out.String())
}

func TestPrintPath_JSON(t *testing.T) {
	// schema-assertion test, not golden: assert structure rather than
	// exact bytes so future schema additions (additive) don't churn a
	// fixture file. Goes hand-in-hand with slice 21's broader schema work.
	out := &bytes.Buffer{}
	prev := Out
	Out = out
	t.Cleanup(func() { Out = prev })
	SetModeForTest(t, ModeJSON)

	PrintPath("/home/u/agent-context/projects/my-app/.claude")

	var got struct {
		Path string `json:"path"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	require.Equal(t, "/home/u/agent-context/projects/my-app/.claude", got.Path)
}

// --- JSON-mode suppression of human printers -------------------------
//
// Critical invariant for slice 21: under --json, the human printers must
// not leak even a stray "Wrote config to ..." line into stdout, or a
// parser would have to peel pre-amble lines off the envelope.

func TestJSONMode_SuppressesHumanPrinters(t *testing.T) {
	out, errBuf := captureBoth(t)
	SetModeForTest(t, ModeJSON)
	SetColorForTest(t, false)
	SetUnicodeForTest(t, true)

	Success("anything")
	Warn("anything")
	Hint("anything")
	Blank()
	PrintInitResult(sampleInitResult(false, false))
	PrintError(lerr.New(lerr.CodeGenericError, "x"))

	require.Empty(t, out.String(), "human printers must be silent in JSON mode")
	require.Empty(t, errBuf.String(), "PrintError must be silent in JSON mode")
}

func TestJSONMode_PrintAddResult(t *testing.T) {
	out, _ := captureBoth(t)
	SetModeForTest(t, ModeJSON)
	PrintAddResult(sampleAddResult())
	var got addJSONEnvelope
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	require.Equal(t, "my-app", got.Project.Name)
	require.Equal(t, sampleAddResult().SyncBase, got.SyncBase)
	require.NotNil(t, got.Commit)
	require.Equal(t, sampleAddResult().CommitSHA, got.Commit.SHA)
	require.Len(t, got.Dirs, 1)
	require.Empty(t, got.Dirs[0].Violations)
}

// --- color/no-color toggling -----------------------------------------
//
// Goldens above are captured with color OFF to keep fixtures plain
// text. The color path needs its own coverage; testing the presence of
// ANSI escapes (vs. exact escape sequences, which lipgloss may evolve)
// keeps the test resilient.

func TestColorEnabled_PrintsAnsiEscapes(t *testing.T) {
	out, _ := captureBoth(t)
	SetModeForTest(t, ModeHuman)
	SetColorForTest(t, true)
	SetUnicodeForTest(t, true)

	Success("hello")
	require.Contains(t, out.String(), "\x1b[",
		"color enabled must produce at least one ANSI escape sequence")
}

func TestColorDisabled_NoAnsiEscapes(t *testing.T) {
	out, _ := captureBoth(t)
	SetModeForTest(t, ModeHuman)
	SetColorForTest(t, false)
	SetUnicodeForTest(t, true)

	Success("hello")
	require.NotContains(t, out.String(), "\x1b[",
		"color disabled must produce zero ANSI escape sequences")
}

func TestNoColorEnvHonored(t *testing.T) {
	// NO_COLOR env var must disable color even when no --no-color flag
	// or override is in play. Mirrors no-color.org's contract.
	t.Setenv("NO_COLOR", "1")
	out, _ := captureBoth(t)
	SetModeForTest(t, ModeHuman)
	// no SetColorForTest call: we want the real ColorEnabled path to
	// observe NO_COLOR.
	SetUnicodeForTest(t, true)

	Success("hello")
	require.NotContains(t, out.String(), "\x1b[",
		"NO_COLOR must suppress ANSI escapes without an explicit override")
}

func TestColorEnabled_RespectsOverride(t *testing.T) {
	// the override (set by --no-color flag) must win over the env var
	// in the inverse direction too: forcing color on when NO_COLOR is
	// set would be wrong, but turning color off when NO_COLOR is unset
	// must work cleanly (that's the --no-color flag path).
	out, _ := captureBoth(t)
	SetModeForTest(t, ModeHuman)
	SetColorForTest(t, false) // simulates --no-color
	SetUnicodeForTest(t, true)
	Success("hello")
	require.NotContains(t, out.String(), "\x1b[")
}

// --- defensive: PrintError on nil is a no-op -------------------------

func TestPrintError_NilIsNoOp(t *testing.T) {
	out, errBuf := captureBoth(t)
	SetModeForTest(t, ModeHuman)
	SetColorForTest(t, false)
	SetUnicodeForTest(t, true)

	PrintError(nil)
	require.Empty(t, out.String())
	require.Empty(t, errBuf.String())
	// also confirms no panic on the nil-error path.
	_ = strings.TrimSpace(out.String())
}
