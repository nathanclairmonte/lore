package git

import (
	"bytes"
	stderrors "errors"
	"fmt"
	"os/exec"
	"strings"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
)

// CheckIgnore reports whether path is ignored by gitignore rules as seen
// from workdir. Lives outside Run because `git check-ignore`'s exit 1 is
// a normal "not ignored" answer rather than a failure; Run would map that
// to CodeGitNetwork.
//
// Semantics of `git check-ignore -q <path>`:
//   - exit 0      -> path matches a gitignore rule          -> (true, nil)
//   - exit 1      -> path does not match any gitignore rule -> (false, nil)
//   - exit 128    -> workdir is not inside a git repo       -> (false, nil)
//   - other       -> real failure                           -> (false, *lerr.Error{CodeGitNetwork})
//
// The "not a git repo" case is folded into the not-ignored answer so
// callers (notably `lore add`'s gitignore precheck) surface the standard
// CodeRefusedSafety message and the "add it to your global .gitignore"
// hint, rather than a confusing CodeGitNetwork. Conservatively reporting
// "not ignored" preserves the safety property: a non-git project is
// only safe to onboard when its agent dir is globally gitignored anyway,
// since the user may later `git init` and commit.
//
// A missing git binary maps to CodeSetupMissing for consistency with Run.
func CheckIgnore(workdir, path string) (bool, error) {
	cmd := exec.Command("git", "check-ignore", "-q", path)
	if workdir != "" {
		cmd.Dir = workdir
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	if stderrors.Is(err, exec.ErrNotFound) {
		return false, lerr.New(lerr.CodeSetupMissing, "git is not installed or not on PATH").
			WithHint("install git and re-run; on macOS try `xcode-select --install`")
	}
	var exitErr *exec.ExitError
	if stderrors.As(err, &exitErr) {
		switch exitErr.ExitCode() {
		case 1:
			return false, nil
		case 128:
			// git's universal "fatal:" exit code. The only case we want
			// to absorb here is "not a git repository"; anything else
			// (corrupt repo, locked index, etc.) is a real failure.
			if strings.Contains(stderr.String(), "not a git repository") {
				return false, nil
			}
		}
	}
	msg := fmt.Sprintf("git check-ignore %s failed", path)
	if s := strings.TrimSpace(stderr.String()); s != "" {
		msg = msg + ": " + s
	}
	return false, lerr.Wrap(lerr.CodeGitNetwork, err, msg)
}
