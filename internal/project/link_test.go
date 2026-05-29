package project

import (
	"os"
	"path/filepath"
	"testing"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/git"
	"github.com/nathanclairmonte/lore/internal/prompt"
	"github.com/stretchr/testify/require"
)

// linkUnignoredSandbox onboards my-app, then reproduces the exact post-clone
// situation the gitignore re-check exists for: the agent dir is no longer
// ignored (a clone does not carry the original .git/info/exclude choice) and
// its symlink is gone. Returns the project root, primed for a Link call.
func linkUnignoredSandbox(t *testing.T) (projectRoot string) {
	t.Helper()
	_, projectRoot = addSandbox(t)
	_, err := Add(AddOptions{ProjectPath: projectRoot})
	require.NoError(t, err)
	unignoreSandbox(t, projectRoot)
	require.NoError(t, os.Remove(filepath.Join(projectRoot, ".claude")))
	return projectRoot
}

func TestLink_FlagsUnignoredDir(t *testing.T) {
	projectRoot := linkUnignoredSandbox(t)

	res, err := Link(LinkOptions{Name: "my-app"})
	require.NoError(t, err, "an un-ignored dir is advisory and must not fail the run")

	require.Len(t, res.Dirs, 1)
	require.Equal(t, LinkStatusLinked, res.Dirs[0].Status)
	require.True(t, res.Dirs[0].Unignored,
		"a linked-but-un-ignored dir must be flagged for the caller")

	// detection only: the symlink is recreated but nothing is written to a
	// gitignore (that is OfferGitignoreFixes' job, deferred to the cmd layer).
	ignored, err := git.CheckIgnore(projectRoot, ".claude")
	require.NoError(t, err)
	require.False(t, ignored, "Link must not silently re-ignore the dir on its own")
}

func TestOfferGitignoreFixes_IgnoresAndClearsFlag(t *testing.T) {
	projectRoot := linkUnignoredSandbox(t)

	res, err := Link(LinkOptions{Name: "my-app"})
	require.NoError(t, err)
	require.True(t, res.Dirs[0].Unignored)

	// the picker chooses the repo .gitignore; the dir ends up ignored again.
	stubPicker(t, prompt.TargetRepoGitignore, nil)
	OfferGitignoreFixes(&res)

	require.False(t, res.Dirs[0].Unignored,
		"a successful picker fix must clear the advisory flag")
	ignored, err := git.CheckIgnore(projectRoot, ".claude")
	require.NoError(t, err)
	require.True(t, ignored, "the dir should be gitignored after the fix")
}

func TestOfferGitignoreFixes_DeclinedLeavesFlag(t *testing.T) {
	projectRoot := linkUnignoredSandbox(t)

	res, err := Link(LinkOptions{Name: "my-app"})
	require.NoError(t, err)
	require.True(t, res.Dirs[0].Unignored)

	// a dismissed picker (non-TTY, or Ctrl-C) leaves the dir as-is so the
	// caller can surface the advisory warning instead.
	stubPicker(t, 0, lerr.New(lerr.CodeRefusedSafety, "dismissed"))
	OfferGitignoreFixes(&res)

	require.True(t, res.Dirs[0].Unignored,
		"a declined picker must leave the advisory flag set")
	ignored, err := git.CheckIgnore(projectRoot, ".claude")
	require.NoError(t, err)
	require.False(t, ignored, "nothing should have been written on decline")
}
