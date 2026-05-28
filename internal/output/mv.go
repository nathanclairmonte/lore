package output

import (
	"fmt"

	"github.com/nathanclairmonte/lore/internal/config"
)

// MoveDirStatus mirrors project.MoveDirStatus so this package doesn't
// import project. cmd/lore/mv.go translates the orchestrator's verdicts
// into this shape.
type MoveDirStatus string

const (
	MoveStatusRetargeted  MoveDirStatus = "retargeted"
	MoveStatusMovedToPath MoveDirStatus = "moved"
	MoveStatusRecreated   MoveDirStatus = "recreated"
	MoveStatusSkippedOld  MoveDirStatus = "skipped-old"
	MoveStatusLeftInPlace MoveDirStatus = "left-in-place"
)

// MoveDirResult mirrors project.MoveDirResult.
type MoveDirResult struct {
	Name    string
	Status  MoveDirStatus
	OldLink string
	NewLink string
	Note    string
}

// MoveResult is the data lore mv hands to the output layer.
type MoveResult struct {
	OldName       string
	ProjectName   string
	OldPath       string
	NewPath       string
	CentralRepo   string
	CommitMessage string
	Dirs          []MoveDirResult
	NotLinkedHere bool
}

// PrintMoveResult renders the post-mv success summary: the rename
// headline, one line per agent dir describing what happened to its
// symlink, the lenient "left in place" / "already absent" notes from
// --path mode, and the central commit line.
//
// Suppressed in JSON mode.
func PrintMoveResult(r MoveResult) {
	if mode == ModeJSON {
		return
	}

	g := activeGlyphs()
	arrow := g.arrowRight

	if r.NotLinkedHere {
		Success("Renamed projects/%s %s projects/%s in central (no symlinks on this machine)",
			r.OldName, arrow, r.ProjectName)
		Blank()
		Success("Committed to central repo: %q", r.CommitMessage)
		return
	}

	Success("Renamed %s %s %s (%s)", r.OldName, arrow, r.ProjectName,
		config.Contract(r.NewPath))
	for _, d := range r.Dirs {
		switch d.Status {
		case MoveStatusRetargeted:
			_, _ = fmt.Fprintf(Out, "  - retargeted symlink: %s\n", config.Contract(d.NewLink))
		case MoveStatusMovedToPath:
			_, _ = fmt.Fprintf(Out, "  - moved symlink: %s %s %s\n",
				config.Contract(d.OldLink), arrow, config.Contract(d.NewLink))
		case MoveStatusRecreated:
			_, _ = fmt.Fprintf(Out, "  - created symlink (was missing): %s\n",
				config.Contract(d.NewLink))
		case MoveStatusSkippedOld, MoveStatusLeftInPlace:
			_, _ = fmt.Fprintf(Out, "  - created symlink: %s\n", config.Contract(d.NewLink))
			if d.Note != "" {
				Warn("%s", d.Note)
			}
		}
	}
	Blank()
	Success("Committed to central repo: %q", r.CommitMessage)
}
