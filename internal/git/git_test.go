package git

import (
	"testing"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/stretchr/testify/require"
)

// TestRunMissingGitMapsToSetupMissing locks in the contract that a missing
// `git` binary is treated as a setup problem (exit 2) rather than a
// network/git failure (exit 6). matters because main.go's exit-code
// translation hinges on this mapping, and the rest of the codebase only
// exercises it indirectly through central.Initialize.
func TestRunMissingGitMapsToSetupMissing(t *testing.T) {
	t.Setenv("PATH", "")

	_, err := Run("", "rev-parse", "HEAD")
	require.Error(t, err)
	require.Equal(t, lerr.CodeSetupMissing, lerr.CodeOf(err))
	require.NotEmpty(t, lerr.HintOf(err), "missing-git error should carry an install hint")
}
