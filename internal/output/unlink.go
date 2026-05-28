package output

import (
	"fmt"

	"github.com/nathanclairmonte/lore/internal/config"
)

// UnlinkDirStatus mirrors project.UnlinkDirStatus so this package
// doesn't import project. The dependency only ever points one way:
// cmd/lore/unlink.go converts the orchestrator's verdicts into this
// shape.
type UnlinkDirStatus string

const (
	UnlinkStatusUnlinked        UnlinkDirStatus = "unlinked"
	UnlinkStatusAlreadyUnlinked UnlinkDirStatus = "already-unlinked"
)

// UnlinkDirResult mirrors project.UnlinkDirResult.
type UnlinkDirResult struct {
	Name     string
	Status   UnlinkDirStatus
	LinkPath string
}

// UnlinkResult is the data lore unlink hands to the output layer.
type UnlinkResult struct {
	ProjectName     string
	ProjectPath     string
	Dirs            []UnlinkDirResult
	AlreadyUnlinked bool
}

// PrintUnlinkResult renders the per-dir verdicts of a single-project
// unlink. Three variants:
//
//   - AlreadyUnlinked: terse "Project X was already unlinked" line so
//     idempotent re-runs stay quiet.
//   - All dirs unlinked cleanly: header (project + path) plus one line
//     per removed symlink.
//   - Mixed dirs (some unlinked, some drift-A "already-unlinked"): the
//     drift-A lines render as warn lines so the user sees the drift
//     was observed and bookkeeping was scrubbed regardless.
//
// Suppressed in JSON mode.
func PrintUnlinkResult(r UnlinkResult) {
	if mode == ModeJSON {
		return
	}
	if r.AlreadyUnlinked {
		Success("Project %s was already unlinked on this machine", r.ProjectName)
		return
	}

	Success("Project: %s (%s)", r.ProjectName, config.Contract(r.ProjectPath))
	Blank()

	g := activeGlyphs()
	pathStyle := stylePath()

	for _, d := range r.Dirs {
		switch d.Status {
		case UnlinkStatusUnlinked:
			_, _ = fmt.Fprintf(Out, "%s Removed symlink: %s\n",
				pathStyle.Render(g.arrowLeft), config.Contract(d.LinkPath))
		case UnlinkStatusAlreadyUnlinked:
			Warn("Drift: %s was not present on disk (state scrubbed anyway)",
				config.Contract(d.LinkPath))
		}
	}
}

// UnlinkProjectResult mirrors project.UnlinkProjectResult: a per-
// project verdict inside a --all run.
type UnlinkProjectResult struct {
	UnlinkResult
	Err error
}

// UnlinkAllResult is the data lore unlink --all hands to the output
// layer.
type UnlinkAllResult struct {
	Projects []UnlinkProjectResult
}

// PrintUnlinkAllResult renders a per-project summary of an --all run.
// Projects that unlinked cleanly render with a single check; projects
// that refused render with a warn line and the underlying error; drift
// observations propagate from each per-project result.
func PrintUnlinkAllResult(r UnlinkAllResult) {
	if mode == ModeJSON {
		return
	}
	if len(r.Projects) == 0 {
		Success("No tracked projects on this machine; nothing to unlink")
		return
	}
	for _, p := range r.Projects {
		if p.Err != nil {
			Warn("%s: %s", p.ProjectName, p.Err.Error())
			continue
		}
		if p.AlreadyUnlinked {
			Success("%s: already unlinked", p.ProjectName)
			continue
		}
		// summarise verdicts for the project: count clean unlinks and
		// drift-A observations. avoids printing a verbose multi-line
		// block per project when the run touches many projects.
		clean, drift := 0, 0
		for _, d := range p.Dirs {
			switch d.Status {
			case UnlinkStatusUnlinked:
				clean++
			case UnlinkStatusAlreadyUnlinked:
				drift++
			}
		}
		if drift == 0 {
			Success("%s: unlinked %d symlink(s)", p.ProjectName, clean)
		} else {
			Success("%s: unlinked %d symlink(s), %d already absent", p.ProjectName, clean, drift)
		}
	}
}
