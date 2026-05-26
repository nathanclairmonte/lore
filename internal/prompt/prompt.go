// Package prompt wraps charmbracelet/huh for the small number of
// interactive confirms slice 1 needs. The wizard slice (14) will reuse
// these helpers and add richer forms.
//
// All prompts are TTY-gated: if stdin isn't a terminal the call returns
// a CodeInconsistency error instead of hanging or silently picking a
// default. Scripted callers should pass explicit flags rather than rely
// on prompt defaults.
package prompt

import (
	"os"

	"github.com/charmbracelet/huh"
	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"golang.org/x/term"
)

// isTTY is var (not const) so tests can swap it.
var isTTY = func() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// Confirm asks a yes/no question. Returns the user's choice.
//
// If stdin isn't a TTY, returns (false, *errors.Error{Code:
// CodeInconsistency}) so non-interactive callers see a deterministic
// refuse and a meaningful error message rather than a silent default.
func Confirm(question string, defaultYes bool) (bool, error) {
	if !isTTY() {
		return false, lerr.New(
			lerr.CodeInconsistency,
			"interactive confirmation required but stdin is not a terminal",
		).WithHint("re-run from an interactive shell, or pass explicit flags")
	}

	value := defaultYes
	form := huh.NewConfirm().
		Title(question).
		Affirmative("Yes").
		Negative("No").
		Value(&value)

	if err := form.Run(); err != nil {
		return false, lerr.Wrap(lerr.CodeGenericError, err, "prompt failed")
	}
	return value, nil
}
