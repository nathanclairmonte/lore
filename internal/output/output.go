// Package output formats lore's human-mode output. JSON formatting will
// land alongside the data-returning commands in slice 21; for slice 1 we
// only need human-mode printers.
//
// All output goes through this package so styling is consistent across
// commands and a single place owns the look and feel.
package output

import (
	"fmt"
	"io"
	"os"

	"github.com/charmbracelet/lipgloss"
	lerr "github.com/nathanclairmonte/lore/internal/errors"
)

// Shared styles. Pulled out as package-level vars so individual printers
// don't redefine colors on every call.
var (
	StyleSuccess = lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Bold(true)
	StyleWarn    = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	StyleError   = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	StyleHint    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	StylePath    = lipgloss.NewStyle().Foreground(lipgloss.Color("4"))
)

// Out is the writer for normal output. Tests can swap it; defaults to stdout.
var Out io.Writer = os.Stdout

// Err is the writer for error output. Tests can swap it; defaults to stderr.
var Err io.Writer = os.Stderr

// Success prints a green ✓ line.
func Success(format string, args ...any) {
	_, _ = fmt.Fprintf(Out, "%s %s\n", StyleSuccess.Render("✓"), fmt.Sprintf(format, args...))
}

// Warn prints a yellow ⚠ line to stdout (warnings are part of normal output,
// not errors).
func Warn(format string, args ...any) {
	_, _ = fmt.Fprintf(Out, "%s %s\n", StyleWarn.Render("⚠"), fmt.Sprintf(format, args...))
}

// Hint prints a dimmed informational line to stdout.
func Hint(format string, args ...any) {
	_, _ = fmt.Fprintln(Out, StyleHint.Render(fmt.Sprintf(format, args...)))
}

// Blank prints an empty line.
func Blank() {
	_, _ = fmt.Fprintln(Out)
}

// PrintError renders a lore error to stderr with its hint, if any.
func PrintError(err error) {
	if err == nil {
		return
	}
	_, _ = fmt.Fprintf(Err, "%s %s\n", StyleError.Render("✗"), err.Error())
	if hint := lerr.HintOf(err); hint != "" {
		_, _ = fmt.Fprintln(Err, StyleHint.Render(hint))
	}
}
