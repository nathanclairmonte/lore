package git

import (
	"bytes"
	stderrors "errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
)

// GlobalExcludesPath returns the path of the user's global gitignore.
// Resolution order:
//
//  1. `core.excludesFile` from git's GLOBAL config scope only. Local
//     repo config is intentionally NOT consulted: slice 4's picker
//     labels this option "machine-wide", so honouring a per-repo
//     `core.excludesFile` override here would mislead the user into
//     writing a rule that doesn't apply elsewhere. System scope is
//     also skipped; the picker is about the user's own global file.
//  2. $XDG_CONFIG_HOME/git/ignore, when XDG_CONFIG_HOME is set.
//  3. $HOME/.config/git/ignore.
//
// Does not create the file. The caller writes via AppendIgnoreEntry,
// which creates parents and the file as needed.
func GlobalExcludesPath() (string, error) {
	if v, ok, err := gitConfigGetGlobal("core.excludesFile"); err != nil {
		return "", err
	} else if ok && v != "" {
		return expandTilde(v)
	}

	// XDG takes precedence over the ~/.config default, but only when
	// XDG_CONFIG_HOME is explicitly set. Mirrors git's own logic.
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "git", "ignore"), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", lerr.Wrap(lerr.CodeGenericError, err,
			"cannot determine HOME for global gitignore")
	}
	return filepath.Join(home, ".config", "git", "ignore"), nil
}

// RepoToplevel returns the absolute path to the worktree root that
// contains workdir. Refuses with CodeRefusedSafety when workdir is not
// inside a git repo: callers (slice 4's picker, slice 10's link) lean
// on this as their "is this a git project?" gate so they don't try to
// land a gitignore entry, an exclude rule, or a check-ignore in a
// directory that has no git at all.
//
// Used over a bare `git rev-parse --is-inside-work-tree` so the caller
// also gets the toplevel path (needed by slice 4's TargetRepoGitignore
// option, which must write to the repo root's .gitignore even when the
// project is onboarded from a subdirectory of a monorepo).
func RepoToplevel(workdir string) (string, error) {
	out, err := Run(workdir, "rev-parse", "--show-toplevel")
	if err != nil {
		// Run wraps the non-git-repo case as CodeGitNetwork with git's
		// "not a git repository" stderr in the message. Re-class as
		// CodeRefusedSafety so the user sees the right exit code and a
		// hint that's specific to what slice 4 needs.
		if strings.Contains(err.Error(), "not a git repository") {
			return "", lerr.Newf(lerr.CodeRefusedSafety,
				"%s is not inside a git repository", workdir).
				WithHint("run `git init` in the project root and re-run `lore add`")
		}
		return "", err
	}
	return out, nil
}

// RepoExcludePath returns the absolute path to .git/info/exclude for the
// repo containing workdir. Uses `git rev-parse --git-path info/exclude`
// so worktrees (where .git is a file pointing into the main repo's
// worktrees/ directory) and submodules resolve correctly without us
// parsing the .git file ourselves.
func RepoExcludePath(workdir string) (string, error) {
	out, err := Run(workdir, "rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return "", err
	}
	// rev-parse returns a path relative to workdir when the repo lives
	// inline (most common); join so callers always see an absolute path.
	if !filepath.IsAbs(out) {
		out = filepath.Join(workdir, out)
	}
	return out, nil
}

// AppendIgnoreEntry appends entry to path on its own line. Creates the
// parent directory and the file if either is missing. If the file
// exists and does not end with a newline, prepends one so entry isn't
// concatenated onto the previous line.
func AppendIgnoreEntry(path, entry string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return lerr.Wrap(lerr.CodeGenericError, err,
			fmt.Sprintf("cannot create parent dir for %s", path))
	}

	// peek at the existing tail so we know whether to prepend a newline.
	// done before Open(O_APPEND) so we don't have to seek backwards on
	// the writable handle.
	needsLeadingNewline := false
	info, statErr := os.Stat(path)
	switch {
	case statErr == nil && info.Size() > 0:
		f, openErr := os.Open(path)
		if openErr != nil {
			return lerr.Wrap(lerr.CodeGenericError, openErr,
				fmt.Sprintf("cannot read %s", path))
		}
		var last [1]byte
		_, readErr := f.ReadAt(last[:], info.Size()-1)
		_ = f.Close()
		if readErr != nil {
			return lerr.Wrap(lerr.CodeGenericError, readErr,
				fmt.Sprintf("cannot read tail of %s", path))
		}
		needsLeadingNewline = last[0] != '\n'
	case statErr != nil && !os.IsNotExist(statErr):
		// non-NotExist Stat error (EACCES, etc.) is surfaced here rather
		// than left to fall through to OpenFile below: a successful create
		// after a failed Stat would silently skip the leading-newline
		// fixup, producing a malformed gitignore line.
		return lerr.Wrap(lerr.CodeGenericError, statErr,
			fmt.Sprintf("cannot stat %s", path))
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return lerr.Wrap(lerr.CodeGenericError, err,
			fmt.Sprintf("cannot open %s for appending", path))
	}

	payload := entry + "\n"
	if needsLeadingNewline {
		payload = "\n" + payload
	}
	if _, werr := f.WriteString(payload); werr != nil {
		// best-effort close on the failure path; we'd rather surface
		// the write error than mask it with a close error.
		_ = f.Close()
		return lerr.Wrap(lerr.CodeGenericError, werr,
			fmt.Sprintf("cannot append to %s", path))
	}
	// on the success path the close error matters: buffered data can
	// still fail to flush, leaving the entry truncated on disk.
	if cerr := f.Close(); cerr != nil {
		return lerr.Wrap(lerr.CodeGenericError, cerr,
			fmt.Sprintf("cannot close %s after appending", path))
	}
	return nil
}

// gitConfigGetGlobal reads a single git config key from the GLOBAL
// scope only. Used by GlobalExcludesPath so the picker's "machine-wide"
// label can't be silently subverted by a per-repo override. Returns
// (value, true, nil) when set, ("", false, nil) when unset (git's
// exit 1), or (_, _, err) on real failure (missing binary, etc.).
//
// Intentionally bypasses Run: `git config --get` exits 1 when the key
// isn't set, which is a normal answer here, but Run would class that as
// CodeGitNetwork. Keeping the missing-binary detection duplicated is
// the smaller cost than threading an allowed-exit-codes parameter
// through Run for a single caller.
func gitConfigGetGlobal(key string) (string, bool, error) {
	cmd := exec.Command("git", "config", "--global", "--get", key)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return strings.TrimSpace(stdout.String()), true, nil
	}
	if stderrors.Is(err, exec.ErrNotFound) {
		return "", false, lerr.New(lerr.CodeSetupMissing,
			"git is not installed or not on PATH").
			WithHint("install git and re-run; on macOS try `xcode-select --install`")
	}
	var exitErr *exec.ExitError
	if stderrors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		// git config --get returns 1 when the key isn't set. normal answer.
		return "", false, nil
	}
	msg := fmt.Sprintf("git config --get %s failed", key)
	if s := strings.TrimSpace(stderr.String()); s != "" {
		msg = msg + ": " + s
	}
	return "", false, lerr.Wrap(lerr.CodeGitNetwork, err, msg)
}

// expandTilde resolves a leading ~ or ~/ to the user's home dir. Does not
// support ~user (different user's home); leaves those untouched so the
// caller's error mentions the literal path the user configured.
func expandTilde(path string) (string, error) {
	if !strings.HasPrefix(path, "~") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", lerr.Wrap(lerr.CodeGenericError, err,
			"cannot expand ~ in path")
	}
	if path == "~" {
		return home, nil
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}
