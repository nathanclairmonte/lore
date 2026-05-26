package output

import (
	"fmt"
	"strings"

	"github.com/nathanclairmonte/lore/internal/config"
)

// InitResult is the data lore init hands to the output layer.
type InitResult struct {
	CentralPath string // absolute path to the central repo
	ConfigPath  string // absolute path to the config file
	HasRemote   bool   // whether the central repo has a git remote configured
	Recreated   bool   // true if we re-created a missing dir (vs. fresh init)
}

// PrintInitResult prints the success summary plus the "no remote configured"
// hint when applicable. Paths are contracted back to `~/` for readability.
// Suppressed in JSON mode (slice 21 will wire a typed envelope).
func PrintInitResult(r InitResult) {
	if mode == ModeJSON {
		return
	}
	verb := "Initialized"
	if r.Recreated {
		verb = "Re-created"
	}
	Success("%s central repository at %s", verb, config.Contract(r.CentralPath))

	// recreate path leaves config untouched, so "wrote config" would be
	// a lie. say what actually happened instead.
	if r.Recreated {
		Success("Using existing config at %s", config.Contract(r.ConfigPath))
	} else {
		Success("Wrote config to %s", config.Contract(r.ConfigPath))
	}

	if !r.HasRemote {
		Blank()
		Warn("No remote configured. Set one with:")
		// print the contracted central path literally rather than
		// `$(lore path --repo)`: `lore path` arrives in slice 2; until
		// then the hint should be a working command the user can run.
		path := shellQuoteForCD(config.Contract(r.CentralPath), r.CentralPath)
		_, _ = fmt.Fprintf(Out, "    cd %s && git remote add origin <url>\n", path)
	}
}

// shellQuoteForCD picks a form of the central-repo path that is safe to
// paste after `cd` in any POSIX shell.
//
// If the contracted form has no shell-unsafe characters it is returned
// unchanged, so the user sees the friendly `~/agent-context` shape and
// the shell still expands the leading `~`. If the contracted form
// contains anything that would break unquoted use (spaces, quotes, shell
// metacharacters) we fall back to the absolute path single-quoted, which
// always works but loses the `~` aesthetic. Correctness wins over polish
// in the latter case.
func shellQuoteForCD(contracted, abs string) string {
	// `~` is intentionally NOT in this set: unquoted `~/foo` expands
	// correctly to the user's home dir, which is what we want here.
	const unsafe = " \t\n\"'\\$`<>|&;()*?#![]{}"
	if !strings.ContainsAny(contracted, unsafe) {
		return contracted
	}
	return "'" + strings.ReplaceAll(abs, "'", `'\''`) + "'"
}
