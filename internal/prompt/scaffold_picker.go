package prompt

import (
	stderrors "errors"
	"strings"

	"github.com/charmbracelet/huh"
	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/output"
)

// scaffoldOther is the sentinel selection value returned by the picker
// when the user wants to type a free-text name instead of choosing one
// of the offered options. The caller spots this and drops to the input
// step. The literal is namespaced so it can't collide with a real
// agent-dir name a user might one day configure (e.g. an empty entry in
// agent_dirs would silently match a bare "" sentinel).
const scaffoldOther = "__lore_scaffold_other__"

// scaffoldSelectRunner is a var so tests can stub the select form
// without driving huh through a real terminal.
var scaffoldSelectRunner = func(form *huh.Select[string]) error {
	return form.Run()
}

// scaffoldInputRunner is the input-form analog used for the "other"
// free-text branch.
var scaffoldInputRunner = func(form *huh.Input) error {
	return form.Run()
}

// PickScaffoldDir asks the user which agent dir to scaffold. options
// drives the picker order (sourced from config.AgentDirs in production,
// so a user who customised their detection list sees the same names
// they configured). A free-text "other" choice lets the user type a
// name not in the list.
//
// Returns:
//
//   - CodeInconsistency if stdin isn't a TTY or output is in JSON mode
//   - CodeRefusedSafety on user dismiss
//   - (dirName, nil) on selection
//
// The returned name is not validated here; callers apply their own
// constraints (e.g. reject "/" or empty) before passing it into the
// scaffolder.
func PickScaffoldDir(options []string) (string, error) {
	if err := requireInteractive("scaffold agent dir picker"); err != nil {
		return "", err
	}
	opts := make([]huh.Option[string], 0, len(options)+1)
	for _, name := range options {
		opts = append(opts, huh.NewOption(name, name))
	}
	opts = append(opts, huh.NewOption("other (type a name)", scaffoldOther))

	choice := ""
	if len(options) > 0 {
		choice = options[0]
	}
	form := huh.NewSelect[string]().
		Title("No agent dir found. Which one should lore scaffold?").
		Options(opts...).
		Value(&choice)
	if err := scaffoldSelectRunner(form); err != nil {
		return "", scaffoldDismissOrWrap(err, "scaffold dir picker failed")
	}
	if choice != scaffoldOther {
		return choice, nil
	}
	typed := ""
	input := huh.NewInput().
		Title("Agent dir name (e.g. .claude):").
		Value(&typed)
	if err := scaffoldInputRunner(input); err != nil {
		return "", scaffoldDismissOrWrap(err, "scaffold dir input failed")
	}
	return strings.TrimSpace(typed), nil
}

// PickScaffoldFile asks which filename to write inside the scaffolded
// agent dir. AGENTS.md is the default (cross-tool convention); CLAUDE.md
// is offered for Claude-specific projects; "other" drops to free-text.
func PickScaffoldFile() (string, error) {
	if err := requireInteractive("scaffold filename picker"); err != nil {
		return "", err
	}
	choice := "AGENTS.md"
	form := huh.NewSelect[string]().
		Title("What filename should the empty starter file use?").
		Options(
			huh.NewOption("AGENTS.md (cross-tool convention, default)", "AGENTS.md"),
			huh.NewOption("CLAUDE.md (Claude-specific)", "CLAUDE.md"),
			huh.NewOption("other (type a name)", scaffoldOther),
		).
		Value(&choice)
	if err := scaffoldSelectRunner(form); err != nil {
		return "", scaffoldDismissOrWrap(err, "scaffold file picker failed")
	}
	if choice != scaffoldOther {
		return choice, nil
	}
	typed := ""
	input := huh.NewInput().
		Title("Filename:").
		Value(&typed)
	if err := scaffoldInputRunner(input); err != nil {
		return "", scaffoldDismissOrWrap(err, "scaffold file input failed")
	}
	return strings.TrimSpace(typed), nil
}

// requireInteractive returns a CodeInconsistency error when stdin
// isn't a TTY or output is in JSON mode. Mirrors the gate in
// PickGitignoreTarget so the two pickers fail the same way for
// scripted callers.
func requireInteractive(label string) error {
	if !isTTY() {
		return lerr.Newf(lerr.CodeInconsistency,
			"%s required but stdin is not a terminal", label).
			WithHint("use --dir <name> to specify an agent dir in non-interactive mode")
	}
	if output.GetMode() == output.ModeJSON {
		return lerr.Newf(lerr.CodeInconsistency,
			"%s cannot run in --json mode", label).
			WithHint("use --dir <name> to specify an agent dir, or drop --json")
	}
	return nil
}

// scaffoldDismissOrWrap maps huh.ErrUserAborted (Ctrl+C / ESC) to a
// CodeRefusedSafety error - the user saw the prompt and walked away,
// same as the gitignore-picker dismiss path. Everything else wraps as
// CodeGenericError.
func scaffoldDismissOrWrap(err error, label string) error {
	if stderrors.Is(err, huh.ErrUserAborted) {
		return lerr.New(lerr.CodeRefusedSafety,
			"scaffold cancelled").
			WithHint("re-run with --dir <name> to scaffold non-interactively")
	}
	return lerr.Wrap(lerr.CodeGenericError, err, label)
}
