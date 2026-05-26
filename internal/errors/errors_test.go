package errors

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodeOf(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want ExitCode
	}{
		{"nil error", nil, CodeSuccess},
		{"plain error", fmt.Errorf("boom"), CodeGenericError},
		{"lore error", New(CodeConflict, "already configured"), CodeConflict},
		{"wrapped lore error", fmt.Errorf("outer: %w", New(CodeNotFound, "missing")), CodeNotFound},
		{"double wrapped", fmt.Errorf("a: %w", fmt.Errorf("b: %w", New(CodeRefusedSafety, "nope"))), CodeRefusedSafety},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, CodeOf(c.err))
		})
	}
}

func TestWrapPreservesCause(t *testing.T) {
	cause := fmt.Errorf("disk full")
	err := Wrap(CodeGenericError, cause, "failed to write")

	require.Equal(t, CodeGenericError, err.Code)
	require.ErrorIs(t, err, cause)
	require.Contains(t, err.Error(), "failed to write")
	require.Contains(t, err.Error(), "disk full")
}

func TestWithHint(t *testing.T) {
	err := New(CodeConflict, "already exists").WithHint("try lore use instead")
	require.Equal(t, "try lore use instead", err.Hint)
	require.Equal(t, "try lore use instead", HintOf(err))
}

func TestHintOfWrapped(t *testing.T) {
	inner := New(CodeNotFound, "missing").WithHint("run lore init")
	outer := fmt.Errorf("context: %w", inner)
	require.Equal(t, "run lore init", HintOf(outer))
}

func TestNewf(t *testing.T) {
	err := Newf(CodeConflict, "project %q already tracked", "foo")
	require.Equal(t, `project "foo" already tracked`, err.Message)
	require.Equal(t, CodeConflict, err.Code)
}
