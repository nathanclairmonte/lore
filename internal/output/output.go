// Package output formats lore's human and JSON output. All command handlers route through this
// package so styling, glyph fallbacks, and mode selection live in one
// place.
//
// Three degradation rules govern human output:
//
//  1. Color: emitted only when Out is a real terminal AND neither NO_COLOR
//     nor --no-color is in effect. Redirecting stdout to a file therefore
//     produces a clean ASCII transcript with no escape residue.
//  2. Unicode glyphs: ✓ ⚠ ✗ → ← degrade to [ok] [!] [x] -> <- when Out is
//     not a terminal. Decoupled from color so a user on a UTF-8 terminal
//     with NO_COLOR set still gets nice glyphs; a user piping to `less`
//     or a CI log still avoids mojibake.
//  3. JSON mode (--json): human-mode printers (Success, Warn, Hint,
//     PrintAddResult, PrintInitResult, PrintError) suppress all human
//     output. Data-returning commands emit a documented JSON envelope
//     instead.
//
// Concurrency: mode, colorOverride, unicodeOverride, Out, and Err are
// package-level mutable globals. This is intentional for a single-process
// CLI where the cobra pre-run sets them once before any subcommand RunE
// fires. If a future command ever fans out goroutines that print (e.g. a
// parallel `lore status` across many projects), these need a mutex or a
// per-call context-carried renderer; today they don't.
package output

import (
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/nathanclairmonte/lore/internal/config"
	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"golang.org/x/term"
)

// Mode names the output shape - human-readable lines, or a stable JSON
// envelope suitable for scripts and agents.
type Mode int

const (
	ModeHuman Mode = iota
	ModeJSON
)

// Out is the writer for normal output. Tests swap it for a buffer;
// defaults to stdout. Reassigning it changes the TTY check that governs
// color and glyph degradation, since the check inspects whatever Out
// currently points at.
var Out io.Writer = os.Stdout

// Err is the writer for error output. Tests swap it; defaults to stderr.
var Err io.Writer = os.Stderr

// mode is the active output shape. Mutated by SetMode (called by the
// root cobra pre-run hook from the --json flag) and SetModeForTest.
var mode = ModeHuman

// colorOverride / unicodeOverride: nil means "auto-detect from Out +
// env"; non-nil forces the answer regardless. Used by --no-color and
// by tests that need deterministic output to compare against goldens.
var (
	colorOverride   *bool
	unicodeOverride *bool
)

// glyphSet bundles the five symbols the human printers use. Two
// instances are defined so the picker can swap between them whole rather
// than scattering branches through every Printf.
type glyphSet struct {
	ok, warn, err, arrowRight, arrowLeft string
}

var (
	unicodeGlyphs = glyphSet{"✓", "⚠", "✗", "→", "←"}
	asciiGlyphs   = glyphSet{"[ok]", "[!]", "[x]", "->", "<-"}
)

// SetMode sets the active output mode. Called by the root cobra pre-run
// hook after parsing --json. Tests should use SetModeForTest instead so
// the mode resets on cleanup.
func SetMode(m Mode) { mode = m }

// GetMode returns the active mode. Commands consult this to choose
// between PrintXxx and emitJSON envelopes.
func GetMode() Mode { return mode }

// SetNoColor disables (or re-enables) color emission regardless of TTY
// detection. Called by the root pre-run hook after parsing --no-color.
// disabled=false clears the override so live env / TTY detection takes
// over again.
func SetNoColor(disabled bool) {
	if disabled {
		f := false
		colorOverride = &f
	} else {
		colorOverride = nil
	}
}

// ColorEnabled reports whether ANSI styling is currently emitted. False
// when --no-color is set, when NO_COLOR is in the environment (per the
// no-color.org de-facto standard), or when Out is not a terminal.
func ColorEnabled() bool {
	if colorOverride != nil {
		return *colorOverride
	}
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	return writerIsTerminal(Out)
}

// UnicodeEnabled reports whether unicode glyphs are emitted (versus the
// ASCII fallback set). Governed only by the TTY check on Out: a user
// piping to a logfile or a CI buffer gets ASCII even with NO_COLOR
// unset, because the logfile encoding is the actual concern.
func UnicodeEnabled() bool {
	if unicodeOverride != nil {
		return *unicodeOverride
	}
	return writerIsTerminal(Out)
}

// writerIsTerminal reports whether w is a real terminal. Returns false
// for *bytes.Buffer, *strings.Builder, or any non-*os.File writer (which
// is what tests use). When called against os.Stdout / os.Stderr it
// consults term.IsTerminal on the underlying fd.
func writerIsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// activeGlyphs returns the glyph set to use for this call based on
// UnicodeEnabled. Computed per-call rather than cached because Out can
// be swapped between calls (tests, future programmatic reconfiguration).
func activeGlyphs() glyphSet {
	if UnicodeEnabled() {
		return unicodeGlyphs
	}
	return asciiGlyphs
}

// colorRenderer is a lipgloss renderer locked to TrueColor. We own the
// "should we emit color" decision via ColorEnabled (which checks Out for
// TTY-ness and honors NO_COLOR / --no-color); lipgloss's default
// renderer would try to make that decision too, and would say "no" in
// any test or piped context. Locking the profile here means once we
// decide yes, lipgloss actually emits the escape codes.
//
// The writer (io.Discard) is only consulted for the renderer's
// initial color-capability detection, which we then override via
// SetColorProfile. The renderer doesn't actually write to it - all
// printed bytes flow through fmt.Fprintf(Out, ...) at the call site.
var colorRenderer = func() *lipgloss.Renderer {
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.TrueColor)
	return r
}()

// styleSuccess / styleWarn / styleError / styleHint / stylePath return
// the lipgloss style for each named role. When color is disabled they
// collapse to a no-op style (zero-value lipgloss.Style emits no escape
// codes), so .Render(s) returns s verbatim. Per-call construction is
// cheap and avoids the "Out swapped after init" staleness problem.
func styleSuccess() lipgloss.Style {
	if !ColorEnabled() {
		return lipgloss.NewStyle()
	}
	return colorRenderer.NewStyle().Foreground(lipgloss.Color("2")).Bold(true)
}
func styleWarn() lipgloss.Style {
	if !ColorEnabled() {
		return lipgloss.NewStyle()
	}
	return colorRenderer.NewStyle().Foreground(lipgloss.Color("3"))
}
func styleError() lipgloss.Style {
	if !ColorEnabled() {
		return lipgloss.NewStyle()
	}
	return colorRenderer.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
}
func styleHint() lipgloss.Style {
	if !ColorEnabled() {
		return lipgloss.NewStyle()
	}
	return colorRenderer.NewStyle().Foreground(lipgloss.Color("8"))
}
func stylePath() lipgloss.Style {
	if !ColorEnabled() {
		return lipgloss.NewStyle()
	}
	return colorRenderer.NewStyle().Foreground(lipgloss.Color("4"))
}

// Success prints a ✓-prefixed line. No-op in JSON mode so a script-
// oriented `--json` run sees only the documented envelope on stdout.
func Success(format string, args ...any) {
	if mode == ModeJSON {
		return
	}
	g := activeGlyphs()
	_, _ = fmt.Fprintf(Out, "%s %s\n", styleSuccess().Render(g.ok), fmt.Sprintf(format, args...))
}

// Warn prints a ⚠-prefixed line to Out. Warnings are part of normal
// output, not errors. Suppressed in JSON mode.
func Warn(format string, args ...any) {
	if mode == ModeJSON {
		return
	}
	g := activeGlyphs()
	_, _ = fmt.Fprintf(Out, "%s %s\n", styleWarn().Render(g.warn), fmt.Sprintf(format, args...))
}

// Hint prints a dimmed informational line. Suppressed in JSON mode.
func Hint(format string, args ...any) {
	if mode == ModeJSON {
		return
	}
	_, _ = fmt.Fprintln(Out, styleHint().Render(fmt.Sprintf(format, args...)))
}

// Blank prints an empty line. Suppressed in JSON mode so the envelope
// stays single-line-parseable.
func Blank() {
	if mode == ModeJSON {
		return
	}
	_, _ = fmt.Fprintln(Out)
}

// PrintDeferredFailure writes a deferred background failure to stderr.
func PrintDeferredFailure(data []byte) error {
	_, err := fmt.Fprintf(Err, "Previous automatic push failed:\n%s", data)
	return err
}

// PrintError renders a lore error to Err with its hint, if any.
// Suppressed in JSON mode: the JSON envelope is the only thing the
// caller should be parsing on a failure (slice 21 polishes the failure
// envelope shape; slice 03 just ensures human noise doesn't leak).
func PrintError(err error) {
	if err == nil {
		return
	}
	if mode == ModeJSON {
		return
	}
	g := activeGlyphs()
	_, _ = fmt.Fprintf(Err, "%s %s\n", styleError().Render(g.err), err.Error())
	if hint := lerr.HintOf(err); hint != "" {
		_, _ = fmt.Fprintln(Err, styleHint().Render(hint))
	}
}

// --- Test helpers (exported so cmd-level tests can use them too) -----

// SetModeForTest forces mode for the duration of t. Restores the prior
// value on t.Cleanup so tests don't leak global state into each other.
func SetModeForTest(t *testing.T, m Mode) {
	t.Helper()
	prev := mode
	mode = m
	t.Cleanup(func() { mode = prev })
}

// SetColorForTest forces color on/off for the duration of t. Used by
// golden tests that must produce deterministic output regardless of the
// developer's TTY.
func SetColorForTest(t *testing.T, enabled bool) {
	t.Helper()
	prev := colorOverride
	colorOverride = &enabled
	t.Cleanup(func() { colorOverride = prev })
}

// SetUnicodeForTest forces glyph mode for the duration of t. Same
// rationale as SetColorForTest.
func SetUnicodeForTest(t *testing.T, enabled bool) {
	t.Helper()
	prev := unicodeOverride
	unicodeOverride = &enabled
	t.Cleanup(func() { unicodeOverride = prev })
}

// printScannedRoots announces a configured-roots sweep. Only announce a scan
// when one actually ran: when every project resolved from its recorded path
// roots is empty, and claiming we swept the roots would be misleading.
func printScannedRoots(roots []string) {
	if len(roots) == 0 {
		return
	}
	_, _ = fmt.Fprintln(Out, "Scanning configured roots for tracked projects...")
	for _, root := range roots {
		_, _ = fmt.Fprintf(Out, "  %s\n", config.Contract(root))
	}
	Blank()
}
