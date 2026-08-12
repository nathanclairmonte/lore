package prompt

import (
	stderrors "errors"
	"fmt"

	"github.com/charmbracelet/huh"
	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/output"
)

// GitignoreTarget is the user's choice for where to record an agent dir
// gitignore entry. Display order matches Slice 4's spec: repo .gitignore
// is the default because it survives clones and is scoped to the repo.
type GitignoreTarget int

const (
	// TargetRepoGitignore appends to <repo>/.gitignore. Survives clones,
	// scoped to this repo. Default selection.
	TargetRepoGitignore GitignoreTarget = iota
	// TargetGlobalExcludes appends to git's core.excludesFile (or the
	// XDG default). Machine-wide, applies to every repo.
	TargetGlobalExcludes
	// TargetRepoExclude appends to .git/info/exclude. Repo-local and
	// does not survive clones; lore pull re-runs the picker after fresh
	// clones (slice 10).
	TargetRepoExclude
)

// pickerRunner is a var so tests can stub huh entirely. Production
// always calls form.Run(); tests inject a func that returns a fixed
// choice (or huh.ErrUserAborted) without touching a terminal.
var pickerRunner = func(form *huh.Select[GitignoreTarget]) error {
	return form.Run()
}

// PickGitignoreTarget shows a three-option picker asking the user how to
// gitignore agentDirName. Returns:
//
//   - CodeInconsistency if stdin isn't a TTY OR output is in JSON mode.
//     Picker can't run without a human at the keyboard or with stdout
//     reserved for a JSON envelope; both cases need explicit setup, not
//     an interactive fallback.
//   - CodeRefusedSafety on user dismiss (huh.ErrUserAborted from Ctrl+C
//     or ESC). The user saw the choice and walked away; lore respects
//     that and refuses to proceed.
//   - (choice, nil) on a selection.
//
// agentDirName is rendered into the picker title so the prompt is
// concrete ("Gitignore .claude/ where?" rather than a generic question).
func PickGitignoreTarget(agentDirName string) (GitignoreTarget, error) {
	if !isTTY() {
		return 0, lerr.New(
			lerr.CodeInconsistency,
			"interactive gitignore picker required but stdin is not a terminal",
		).WithHint("gitignore the agent directory manually (repo .gitignore, global, or .git/info/exclude) and re-run")
	}
	if output.GetMode() == output.ModeJSON {
		return 0, lerr.New(
			lerr.CodeInconsistency,
			"interactive gitignore picker cannot run in --json mode",
		).WithHint("gitignore the agent directory manually and re-run, or drop --json for the initial add")
	}

	choice := TargetRepoGitignore
	form := huh.NewSelect[GitignoreTarget]().
		Title(fmt.Sprintf("%s is not gitignored. Where should lore add the entry?", agentDirName)).
		Options(
			huh.NewOption("Append to repo .gitignore (survives clones)", TargetRepoGitignore),
			huh.NewOption("Append to global gitignore (machine-wide)", TargetGlobalExcludes),
			huh.NewOption("Append to .git/info/exclude (repo-local, no clones)", TargetRepoExclude),
		).
		Value(&choice)

	if err := pickerRunner(form); err != nil {
		if stderrors.Is(err, huh.ErrUserAborted) {
			return 0, lerr.New(
				lerr.CodeRefusedSafety,
				"agent directory must be gitignored before `lore add` can proceed",
			).WithHint("re-run and choose an option, or set up your gitignore manually first")
		}
		return 0, lerr.Wrap(lerr.CodeGenericError, err, "gitignore picker failed")
	}
	return choice, nil
}
