// Package git is a narrow shell-out wrapper around the `git` binary. Every
// lore subcommand that touches git funnels through here so error handling
// and exit-code mapping live in one place.
package git

import (
	"bytes"
	stderrors "errors"
	"fmt"
	"os/exec"
	"strings"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
)

// Run executes `git args...` in workdir. Returns trimmed stdout. Non-zero
// exits are wrapped as *errors.Error with CodeGitNetwork; the error message
// includes the git stderr so callers don't need to construct it themselves.
func Run(workdir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if workdir != "" {
		cmd.Dir = workdir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// a missing `git` binary is a setup problem, not a network/git
		// failure. distinguish so scripts and agents reacting to exit
		// codes per the global table see the right class.
		if stderrors.Is(err, exec.ErrNotFound) {
			return "", lerr.New(lerr.CodeSetupMissing, "git is not installed or not on PATH").
				WithHint("install git and re-run; on macOS try `xcode-select --install`")
		}
		msg := fmt.Sprintf("git %s failed", strings.Join(args, " "))
		if s := strings.TrimSpace(stderr.String()); s != "" {
			msg = msg + ": " + s
		}
		return strings.TrimSpace(stdout.String()), lerr.Wrap(lerr.CodeGitNetwork, err, msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// Init runs `git init` in dir. dir must exist.
func Init(dir string) error {
	_, err := Run(dir, "init")
	return err
}
