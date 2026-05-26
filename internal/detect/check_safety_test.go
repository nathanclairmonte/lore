package detect

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nathanclairmonte/lore/internal/git"
	"github.com/stretchr/testify/require"
)

// safetySandbox sets up an isolated git environment so check-ignore /
// ls-files don't accidentally pick up the developer's global config.
// Returns a fresh git repo rooted at the returned dir, with .claude/
// present as a real directory (callers mutate it per-scenario).
func safetySandbox(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	dir := t.TempDir()
	require.NoError(t, git.Init(dir))
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".claude"), 0o755))
	return dir
}

// kinds extracts the Kind set from a violations slice for assertion
// against ElementsMatch. The Kind is the stable identity of a check;
// Message wording is allowed to drift.
func kinds(vs []Violation) []ViolationKind {
	out := make([]ViolationKind, 0, len(vs))
	for _, v := range vs {
		out = append(out, v.Kind)
	}
	return out
}

func TestCheckSafetyNoViolations(t *testing.T) {
	dir := safetySandbox(t)
	// .claude exists as a real, empty, untracked dir with no nested .git.
	vs, err := CheckSafety(dir, []string{".claude"})
	require.NoError(t, err)
	require.Empty(t, vs)
}

func TestCheckSafetyTrackedFile(t *testing.T) {
	dir := safetySandbox(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".claude", "CLAUDE.md"),
		[]byte("ctx"), 0o644))
	_, err := git.Run(dir, "add", ".claude/CLAUDE.md")
	require.NoError(t, err)

	vs, err := CheckSafety(dir, []string{".claude"})
	require.NoError(t, err)
	require.ElementsMatch(t, []ViolationKind{ViolationTrackedFiles}, kinds(vs))
	require.Contains(t, vs[0].Message, "git rm --cached -r .claude",
		"recovery hint must surface the exact untrack command")
}

func TestCheckSafetyNestedGit(t *testing.T) {
	dir := safetySandbox(t)
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".claude", ".git"), 0o755))

	vs, err := CheckSafety(dir, []string{".claude"})
	require.NoError(t, err)
	require.ElementsMatch(t, []ViolationKind{ViolationNestedGit}, kinds(vs))
	require.Contains(t, vs[0].Message, "nested git repository")
}

func TestCheckSafetyAgentDirSymlink(t *testing.T) {
	dir := safetySandbox(t)
	// replace the real .claude with a symlink to a different dir.
	require.NoError(t, os.RemoveAll(filepath.Join(dir, ".claude")))
	target := filepath.Join(t.TempDir(), "somewhere-else")
	require.NoError(t, os.Mkdir(target, 0o755))
	require.NoError(t, os.Symlink(target, filepath.Join(dir, ".claude")))

	vs, err := CheckSafety(dir, []string{".claude"})
	require.NoError(t, err)
	require.ElementsMatch(t, []ViolationKind{ViolationAgentDirSymlink}, kinds(vs))
	require.Contains(t, vs[0].Message, target,
		"symlink violation must name the target so the user knows what they pointed at")
}

func TestCheckSafetyAggregatesViolationsForOneCandidate(t *testing.T) {
	dir := safetySandbox(t)
	// .claude is a real dir with a tracked file AND a nested .git: both
	// dir-only checks must fire so the user fixes everything in one
	// round-trip rather than playing whack-a-mole across retries.
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".claude", "CLAUDE.md"),
		[]byte("ctx"), 0o644))
	_, err := git.Run(dir, "add", ".claude/CLAUDE.md")
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".claude", ".git"), 0o755))

	vs, err := CheckSafety(dir, []string{".claude"})
	require.NoError(t, err)
	require.ElementsMatch(t,
		[]ViolationKind{ViolationTrackedFiles, ViolationNestedGit},
		kinds(vs),
		"all applicable dir-only violations must surface in one pass")
}

func TestCheckSafetyOnlyChecksFirstManagedCandidate(t *testing.T) {
	// the key new-design invariant: a clean higher-priority candidate
	// is what lore will adopt, so a problematic lower-priority candidate
	// must NOT trigger a refusal - lore would never touch it. otherwise
	// users with a stale .cursor would be blocked from onboarding a
	// fresh .claude they actually care about.
	dir := safetySandbox(t)
	// .claude is the clean, untracked dir created by safetySandbox.
	// .cursor has a nested .git (would fire if it were checked).
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".cursor"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".cursor", ".git"), 0o755))

	vs, err := CheckSafety(dir, []string{".claude", ".cursor"})
	require.NoError(t, err)
	require.Empty(t, vs, "lower-priority candidates must not be probed when a higher-priority one is selected")
}

func TestCheckSafetyFallsThroughToLaterCandidate(t *testing.T) {
	// when the highest-priority candidate doesn't exist (or is a regular
	// file, which DetectAgentDir also skips), the next existing one is
	// the candidate lore would manage, and that's what safety checks.
	dir := safetySandbox(t)
	require.NoError(t, os.RemoveAll(filepath.Join(dir, ".claude")))
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".cursor"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".cursor", ".git"), 0o755))

	vs, err := CheckSafety(dir, []string{".claude", ".cursor"})
	require.NoError(t, err)
	require.ElementsMatch(t, []ViolationKind{ViolationNestedGit}, kinds(vs))
	require.Equal(t, ".cursor", vs[0].Name, "violation must point at the candidate actually selected")
}

func TestCheckSafetySymlinkSkipsNestedAndTrackedChecks(t *testing.T) {
	// a symlinked candidate must not get its target probed for nested
	// .git or tracked files. otherwise we'd report violations against
	// the symlink target instead of the candidate the user controls.
	dir := safetySandbox(t)
	require.NoError(t, os.RemoveAll(filepath.Join(dir, ".claude")))

	// build a target that WOULD fire nested-git if traversed.
	target := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(target, ".git"), 0o755))
	require.NoError(t, os.Symlink(target, filepath.Join(dir, ".claude")))

	vs, err := CheckSafety(dir, []string{".claude"})
	require.NoError(t, err)
	require.ElementsMatch(t, []ViolationKind{ViolationAgentDirSymlink}, kinds(vs))
}

func TestCheckSafetyIgnoresMissingCandidates(t *testing.T) {
	dir := safetySandbox(t)
	// .cursor / .agents / .aider don't exist on this project. CheckSafety
	// must not invent violations for them; only real candidates count.
	vs, err := CheckSafety(dir, []string{".claude", ".cursor", ".agents", ".aider"})
	require.NoError(t, err)
	require.Empty(t, vs)
}

func TestCheckSafetyIgnoresRegularFile(t *testing.T) {
	dir := safetySandbox(t)
	// a candidate that's a regular file (not a dir, not a symlink) is
	// none of CheckSafety's business - DetectAgentDir will already skip
	// it, and there's nothing to refuse.
	require.NoError(t, os.RemoveAll(filepath.Join(dir, ".claude")))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".claude"), []byte(""), 0o644))

	vs, err := CheckSafety(dir, []string{".claude"})
	require.NoError(t, err)
	require.Empty(t, vs)
}
