package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nathanclairmonte/lore/internal/output"
	"github.com/stretchr/testify/require"
)

// capturedOut swaps output.Out for a buffer and restores the original on
// test cleanup. Used to assert exactly what `lore path` writes to stdout.
func capturedOut(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := output.Out
	output.Out = buf
	t.Cleanup(func() { output.Out = prev })
	return buf
}

func TestLorePath_Project(t *testing.T) {
	home, projectRoot := addCmdSandbox(t)
	require.Equal(t, 0, run([]string{"add", projectRoot}))

	buf := capturedOut(t)
	require.Equal(t, 0, run([]string{"path", "my-app"}))

	expected := filepath.Join(home, "agent-context", "projects", "my-app", ".claude")
	require.Equal(t, expected, strings.TrimRight(buf.String(), "\n"))
	// no styling: the only content is the path + a trailing newline.
	require.Equal(t, expected+"\n", buf.String())
}

func TestLorePath_Repo(t *testing.T) {
	home, projectRoot := addCmdSandbox(t)
	require.Equal(t, 0, run([]string{"add", projectRoot}))

	buf := capturedOut(t)
	require.Equal(t, 0, run([]string{"path", "--repo"}))

	require.Equal(t,
		filepath.Join(home, "agent-context")+"\n",
		buf.String())
}

func TestLorePath_NotFound(t *testing.T) {
	addCmdSandbox(t)
	// no Add called: the project name is unknown.
	require.Equal(t, 3, run([]string{"path", "never-added"}))
}

func TestLorePath_RepoRejectsExtraArgs(t *testing.T) {
	addCmdSandbox(t)
	require.Equal(t, 1, run([]string{"path", "--repo", "extra"}))
}

func TestLorePath_NoArgsErrors(t *testing.T) {
	addCmdSandbox(t)
	require.Equal(t, 1, run([]string{"path"}))
}
