package main

import (
	"bytes"
	"testing"

	"github.com/nathanclairmonte/lore/internal/output"
)

func capturedErr(t *testing.T) *bytes.Buffer {
	t.Helper()
	buffer := &bytes.Buffer{}
	previous := output.Err
	output.Err = buffer
	t.Cleanup(func() { output.Err = previous })
	return buffer
}
