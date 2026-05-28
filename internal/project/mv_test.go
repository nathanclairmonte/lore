package project

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRenderMvTemplate_Expands(t *testing.T) {
	got := renderMvTemplate("chore: move {old} -> {new}", "foo", "bar")
	require.Equal(t, "chore: move foo -> bar", got)
}

func TestRenderMvTemplate_BlankFallsBackToDefault(t *testing.T) {
	// a user can explicitly set mv_template = "" in config; rather than hand
	// git an empty commit message, the renderer falls back to the built-in
	// default so the rename still commits cleanly.
	for _, blank := range []string{"", "   ", "\t\n"} {
		got := renderMvTemplate(blank, "foo", "bar")
		require.Equal(t, "refactor: rename foo to bar", got,
			"blank template %q must fall back to the default", blank)
	}
}
