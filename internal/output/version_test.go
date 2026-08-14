package output

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrintVersionHuman(t *testing.T) {
	out, _ := withCleanState(t, false)
	PrintVersion("0.1.0")
	require.Equal(t, "lore version 0.1.0\n", out.String())
}

func TestPrintVersionJSON(t *testing.T) {
	out, _ := captureBoth(t)
	SetModeForTest(t, ModeJSON)
	PrintVersion("0.1.0")

	var got VersionEnvelope
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	require.Equal(t, VersionEnvelope{Version: "0.1.0"}, got)
}
