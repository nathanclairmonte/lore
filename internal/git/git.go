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

// Result is the unmodified output of a Git process.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	cause    error
}

// RunRaw executes Git without trimming or converting stdout. It returns
// non-zero exits to the caller so read APIs can distinguish an absent object
// from a broken Git invocation.
func RunRaw(workdir string, args ...string) (Result, error) {
	return runRawInput(workdir, nil, args...)
}

func runRawInput(workdir string, input []byte, args ...string) (Result, error) {
	cmd := exec.Command("git", args...)
	if workdir != "" {
		cmd.Dir = workdir
	}
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if err == nil {
		return result, nil
	}
	// a missing `git` binary is a setup problem, not a network/git failure.
	// distinguish so scripts and agents reacting to exit codes per the global
	// table see the right class.
	if stderrors.Is(err, exec.ErrNotFound) {
		return Result{}, lerr.New(lerr.CodeSetupMissing, "git is not installed or not on PATH").
			WithHint("install git and re-run; on macOS try `xcode-select --install`")
	}
	var exitErr *exec.ExitError
	if stderrors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		result.cause = err
		return result, nil
	}
	return Result{}, lerr.Wrap(lerr.CodeGitNetwork, err,
		fmt.Sprintf("cannot run git %s", strings.Join(args, " ")))
}

// Run executes `git args...` in workdir. Returns trimmed stdout. Non-zero
// exits are wrapped as *errors.Error with CodeGitNetwork; the error message
// includes the git stderr so callers don't need to construct it themselves.
// It shares its process execution path with RunRaw.
func Run(workdir string, args ...string) (string, error) {
	result, err := RunRaw(workdir, args...)
	if err != nil {
		return "", err
	}
	stdout := strings.TrimSpace(string(result.Stdout))
	if result.ExitCode == 0 {
		return stdout, nil
	}
	msg := fmt.Sprintf("git %s failed", strings.Join(args, " "))
	if s := strings.TrimSpace(string(result.Stderr)); s != "" {
		msg = msg + ": " + s
	}
	return stdout, lerr.Wrap(lerr.CodeGitNetwork, result.cause, msg)
}

// Init runs `git init` in dir. dir must exist.
func Init(dir string) error {
	_, err := Run(dir, "init")
	return err
}
