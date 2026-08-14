package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/nathanclairmonte/lore/internal/output"
	"github.com/stretchr/testify/require"
)

func TestRootVersionFlag(t *testing.T) {
	stdout := capturedOut(t)
	require.Equal(t, 0, run([]string{"--version"}))
	require.Equal(t, "lore version devel\n", stdout.String())
}

func TestRootVersionFlagJSON(t *testing.T) {
	// run() sets the package-global output mode and never restores it, so
	// without this the JSON mode leaks into every later test in the package
	output.SetModeForTest(t, output.ModeHuman)
	stdout := capturedOut(t)
	require.Equal(t, 0, run([]string{"--version", "--json"}))

	var got output.VersionEnvelope
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &got))
	require.Equal(t, "devel", got.Version)
}

func TestRootSuggestsCommandsForTypos(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"stauts"})

	err := root.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "Did you mean this?")
	require.Contains(t, err.Error(), "status")
}

func TestBareRootPreservesPendingPushFailure(t *testing.T) {
	sandbox(t)
	recordPushFailure("lore push --auto", "all", errors.New("push failed"))
	failurePath, err := pushFailurePath()
	require.NoError(t, err)
	require.FileExists(t, failurePath)

	stderr := capturedErr(t)
	root := newRootCmd()
	root.SetOut(&bytes.Buffer{})
	require.NoError(t, root.Execute())

	require.Empty(t, stderr.String())
	require.FileExists(t, failurePath)
}
