package prompt

import (
	"testing"

	"github.com/charmbracelet/huh"
	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/output"
	"github.com/stretchr/testify/require"
)

// stubTTY forces isTTY to return v for the duration of t. Same pattern
// the Confirm tests would use; the picker reuses prompt's isTTY var.
func stubTTY(t *testing.T, v bool) {
	t.Helper()
	prev := isTTY
	isTTY = func() bool { return v }
	t.Cleanup(func() { isTTY = prev })
}

// stubPicker forces pickerRunner to return err without driving huh
// through a real terminal. The happy-path case relies on the picker's
// own default selection (set via Value() before Run() is called), so
// the stub only needs to control the error return.
func stubPicker(t *testing.T, err error) {
	t.Helper()
	prev := pickerRunner
	pickerRunner = func(_ *huh.Select[GitignoreTarget]) error {
		return err
	}
	t.Cleanup(func() { pickerRunner = prev })
}

func TestPickGitignoreTargetRefusesWhenNoTTY(t *testing.T) {
	stubTTY(t, false)
	// JSON mode off; only the TTY gate should fire.
	output.SetModeForTest(t, output.ModeHuman)

	_, err := PickGitignoreTarget(".claude")
	require.Error(t, err)
	require.Equal(t, lerr.CodeInconsistency, lerr.CodeOf(err))
	require.Contains(t, err.Error(), "stdin is not a terminal")
}

func TestPickGitignoreTargetRefusesInJSONMode(t *testing.T) {
	// TTY available but JSON mode active: picker still must refuse so
	// the JSON stream stays clean.
	stubTTY(t, true)
	output.SetModeForTest(t, output.ModeJSON)

	_, err := PickGitignoreTarget(".claude")
	require.Error(t, err)
	require.Equal(t, lerr.CodeInconsistency, lerr.CodeOf(err))
	require.Contains(t, err.Error(), "--json")
}

func TestPickGitignoreTargetMapsAbortToRefusedSafety(t *testing.T) {
	stubTTY(t, true)
	output.SetModeForTest(t, output.ModeHuman)
	stubPicker(t, huh.ErrUserAborted)

	_, err := PickGitignoreTarget(".claude")
	require.Error(t, err)
	require.Equal(t, lerr.CodeRefusedSafety, lerr.CodeOf(err))
	require.Contains(t, err.Error(), "must be gitignored")
}

func TestPickGitignoreTargetHappyPathReturnsDefault(t *testing.T) {
	// happy path: stub Run() returning nil leaves the bound value at
	// its initial setting (TargetRepoGitignore). exercises the success
	// return path even though we don't drive a real key-press selection.
	stubTTY(t, true)
	output.SetModeForTest(t, output.ModeHuman)
	stubPicker(t, nil)

	got, err := PickGitignoreTarget(".claude")
	require.NoError(t, err)
	require.Equal(t, TargetRepoGitignore, got)
}
