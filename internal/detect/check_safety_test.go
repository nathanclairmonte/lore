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

// kindsFor extracts the Kind set for a single candidate's violations.
// Lets tests assert against ElementsMatch without depending on message
// wording.
func kindsFor(m map[string][]Violation, name string) []ViolationKind {
	out := make([]ViolationKind, 0, len(m[name]))
	for _, v := range m[name] {
		out = append(out, v.Kind)
	}
	return out
}

func TestCheckSafetyAllNoViolations(t *testing.T) {
	dir := safetySandbox(t)
	// .claude exists as a real, empty, untracked dir with no nested .git.
	vs, err := CheckSafetyAll(dir, []string{".claude"})
	require.NoError(t, err)
	require.Empty(t, vs)
}

func TestCheckSafetyAllTrackedFile(t *testing.T) {
	dir := safetySandbox(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".claude", "CLAUDE.md"),
		[]byte("ctx"), 0o644))
	_, err := git.Run(dir, "add", ".claude/CLAUDE.md")
	require.NoError(t, err)

	vs, err := CheckSafetyAll(dir, []string{".claude"})
	require.NoError(t, err)
	require.ElementsMatch(t, []ViolationKind{ViolationTrackedFiles}, kindsFor(vs, ".claude"))
	require.Contains(t, vs[".claude"][0].Message, "git rm --cached -r .claude",
		"recovery hint must surface the exact untrack command")
}

func TestCheckSafetyAllNestedGit(t *testing.T) {
	dir := safetySandbox(t)
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".claude", ".git"), 0o755))

	vs, err := CheckSafetyAll(dir, []string{".claude"})
	require.NoError(t, err)
	require.ElementsMatch(t, []ViolationKind{ViolationNestedGit}, kindsFor(vs, ".claude"))
	require.Contains(t, vs[".claude"][0].Message, "nested git repository")
}

func TestCheckSafetyAllAgentDirSymlink(t *testing.T) {
	dir := safetySandbox(t)
	require.NoError(t, os.RemoveAll(filepath.Join(dir, ".claude")))
	target := filepath.Join(t.TempDir(), "somewhere-else")
	require.NoError(t, os.Mkdir(target, 0o755))
	require.NoError(t, os.Symlink(target, filepath.Join(dir, ".claude")))

	vs, err := CheckSafetyAll(dir, []string{".claude"})
	require.NoError(t, err)
	require.ElementsMatch(t, []ViolationKind{ViolationAgentDirSymlink}, kindsFor(vs, ".claude"))
	require.Contains(t, vs[".claude"][0].Message, target,
		"symlink violation must name the target so the user knows what they pointed at")
}

func TestCheckSafetyAllAggregatesViolationsForOneCandidate(t *testing.T) {
	dir := safetySandbox(t)
	// .claude is a real dir with a tracked file AND a nested .git: both
	// dir-only checks must fire so the user fixes everything in one
	// round-trip rather than playing whack-a-mole across retries.
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".claude", "CLAUDE.md"),
		[]byte("ctx"), 0o644))
	_, err := git.Run(dir, "add", ".claude/CLAUDE.md")
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".claude", ".git"), 0o755))

	vs, err := CheckSafetyAll(dir, []string{".claude"})
	require.NoError(t, err)
	require.ElementsMatch(t,
		[]ViolationKind{ViolationTrackedFiles, ViolationNestedGit},
		kindsFor(vs, ".claude"),
		"all applicable dir-only violations must surface in one pass")
}

func TestCheckSafetyAllPolicesEachCandidateIndependently(t *testing.T) {
	// slice 5 invariant: each candidate is policed in isolation. .claude
	// is clean; .cursor has a nested .git. Multi-dir add should adopt
	// .claude and skip .cursor with a violation, so per-dir results must
	// reflect that split rather than the old "first managed candidate"
	// short-circuit.
	dir := safetySandbox(t)
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".cursor"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".cursor", ".git"), 0o755))

	vs, err := CheckSafetyAll(dir, []string{".claude", ".cursor"})
	require.NoError(t, err)
	require.NotContains(t, vs, ".claude", "clean candidate has no map entry")
	require.ElementsMatch(t, []ViolationKind{ViolationNestedGit}, kindsFor(vs, ".cursor"))
	require.Equal(t, ".cursor", vs[".cursor"][0].Name)
}

func TestCheckSafetyAllSymlinkSkipsNestedAndTrackedChecks(t *testing.T) {
	// a symlinked candidate must not get its target probed for nested
	// .git or tracked files. otherwise we'd report violations against
	// the symlink target instead of the candidate the user controls.
	dir := safetySandbox(t)
	require.NoError(t, os.RemoveAll(filepath.Join(dir, ".claude")))

	// build a target that WOULD fire nested-git if traversed.
	target := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(target, ".git"), 0o755))
	require.NoError(t, os.Symlink(target, filepath.Join(dir, ".claude")))

	vs, err := CheckSafetyAll(dir, []string{".claude"})
	require.NoError(t, err)
	require.ElementsMatch(t, []ViolationKind{ViolationAgentDirSymlink}, kindsFor(vs, ".claude"))
}

func TestCheckSafetyAllIgnoresMissingCandidates(t *testing.T) {
	dir := safetySandbox(t)
	// caller may pass a name that doesn't exist (orchestrator filters
	// upstream, but defense in depth). No violations, no error.
	vs, err := CheckSafetyAll(dir, []string{".claude", ".never-there"})
	require.NoError(t, err)
	require.Empty(t, vs)
}

func TestCheckSafetyAllIgnoresRegularFile(t *testing.T) {
	dir := safetySandbox(t)
	require.NoError(t, os.RemoveAll(filepath.Join(dir, ".claude")))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".claude"), []byte(""), 0o644))

	vs, err := CheckSafetyAll(dir, []string{".claude"})
	require.NoError(t, err)
	require.Empty(t, vs)
}
