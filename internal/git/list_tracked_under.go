package git

import (
	"bytes"
	stderrors "errors"
	"fmt"
	"os/exec"
	"strings"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
)

// ListTrackedUnder returns paths inside relPath that are tracked by the
// git repo containing workdir. Used by slice 3's safety gate: moving
// tracked content into central and replacing it with a symlink would
// silently rewrite git's view of the project, so `lore add` refuses when
// the agent dir contains any tracked file.
//
// Semantics of `git ls-files -- <relPath>`:
//   - exit 0, empty stdout  -> nothing tracked under relPath          -> ([], nil)
//   - exit 0, paths emitted -> tracked files inside relPath           -> (paths, nil)
//   - exit 128              -> workdir is not inside a git repo       -> ([], nil)
//   - other                 -> real failure                           -> (nil, *lerr.Error{CodeGitNetwork})
//
// The "not a git repo" case folds into an empty result, mirroring
// CheckIgnore's convention: a non-git project trivially has no tracked
// files, and the safety gate concerns itself with what git currently
// tracks rather than what the user might track later.
//
// A missing git binary maps to CodeSetupMissing for consistency with Run
// and CheckIgnore.
func ListTrackedUnder(workdir, relPath string) ([]string, error) {
	cmd := exec.Command("git", "ls-files", "--", relPath)
	if workdir != "" {
		cmd.Dir = workdir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err == nil {
		return splitPaths(stdout.String()), nil
	}
	if stderrors.Is(err, exec.ErrNotFound) {
		return nil, lerr.New(lerr.CodeSetupMissing, "git is not installed or not on PATH").
			WithHint("install git and re-run; on macOS try `xcode-select --install`")
	}
	var exitErr *exec.ExitError
	if stderrors.As(err, &exitErr) && exitErr.ExitCode() == 128 {
		// same as CheckIgnore: absorb the not-a-git-repo fatal but let
		// other fatals (corrupt index, locked .git, etc.) bubble up.
		if strings.Contains(stderr.String(), "not a git repository") {
			return nil, nil
		}
	}
	msg := fmt.Sprintf("git ls-files %s failed", relPath)
	if s := strings.TrimSpace(stderr.String()); s != "" {
		msg = msg + ": " + s
	}
	return nil, lerr.Wrap(lerr.CodeGitNetwork, err, msg)
}

// splitPaths turns ls-files's newline-delimited output into a slice,
// dropping the trailing empty line and any stray blanks.
func splitPaths(out string) []string {
	out = strings.TrimRight(out, "\n")
	if out == "" {
		return nil
	}
	lines := strings.Split(out, "\n")
	result := lines[:0]
	for _, l := range lines {
		if l != "" {
			result = append(result, l)
		}
	}
	return result
}
