package output

import (
	"fmt"

	"github.com/nathanclairmonte/lore/internal/config"
)

// AddDirStatus mirrors project.DirStatus so this package doesn't have to
// import project (the dependency only ever points the other way:
// cmd/lore/add.go converts the orchestrator's result into this shape).
type AddDirStatus string

const (
	StatusAdded          AddDirStatus = "added"
	StatusAlreadyTracked AddDirStatus = "already-tracked"
	StatusSkippedSafety  AddDirStatus = "skipped-safety"
	StatusScaffolded     AddDirStatus = "scaffolded"
)

// AddSafetyViolation mirrors detect.Violation in shape (Kind + Message)
// so this package can carry the violation kind through to slice 21's
// typed JSON envelope without importing detect. The human renderer only
// uses Message today; Kind is preserved for the structured output path.
type AddSafetyViolation struct {
	Kind    string
	Message string
}

// AddDirResult is the per-dir outcome inside an AddResult. Mirrors the
// shape of project.AgentDirResult.
type AddDirResult struct {
	Name        string
	Status      AddDirStatus
	CentralPath string
	// SafetyViolations carries one entry per safety-gate failure. Populated
	// only for StatusSkippedSafety. The slice (not a pre-joined string)
	// lets the JSON path emit structured violation kinds while the human
	// path still iterates Message lines.
	SafetyViolations []AddSafetyViolation
}

// AddResult is the data lore add hands to the output layer.
type AddResult struct {
	ProjectName   string
	ProjectPath   string
	CentralRepo   string
	CommitMessage string
	Dirs          []AddDirResult
	// ScaffoldRequested mirrors the orchestrator's flag: true when the
	// user passed --create, even when no scaffolding actually happened.
	ScaffoldRequested bool
}

// allAlreadyTracked reports whether every per-dir result has
// AlreadyTracked status; mirrors project.AddResult.AllAlreadyTracked.
func (r AddResult) allAlreadyTracked() bool {
	if len(r.Dirs) == 0 {
		return false
	}
	for _, d := range r.Dirs {
		if d.Status != StatusAlreadyTracked {
			return false
		}
	}
	return true
}

// PrintAddResult renders the per-dir summary defined in SPEC.md's
// `lore add` section. Three variants:
//
//   - Every dir already tracked: terse "Project X is already tracked"
//     line; idempotency is meant to be quiet.
//   - At least one dir freshly added: header (project + path), per-dir
//     blocks for each added/already-tracked dir, then the commit line
//     and the "Next:" hint pointing at `git push` on central.
//   - At least one dir skipped: per-dir safety messages rendered below
//     the added blocks (or alone, when no dir landed). The cmd layer
//     emits a non-zero exit via the error path; this function focuses
//     on showing the user what was added vs refused.
//
// Suppressed in JSON mode: slice 21 will wire a typed envelope; for now
// the human block is just skipped so script callers don't get noise.
func PrintAddResult(r AddResult) {
	if mode == ModeJSON {
		return
	}
	if r.allAlreadyTracked() {
		if len(r.Dirs) == 1 {
			Success("Project %s is already tracked at %s",
				r.ProjectName, config.Contract(r.Dirs[0].CentralPath))
			return
		}
		Success("Project %s is already tracked (%d agent dirs)",
			r.ProjectName, len(r.Dirs))
		return
	}

	Success("Project:   %s (%s)", r.ProjectName, config.Contract(r.ProjectPath))
	Blank()

	g := activeGlyphs()
	pathStyle := stylePath()

	addedCount := 0
	for _, d := range r.Dirs {
		switch d.Status {
		case StatusAdded, StatusScaffolded:
			// scaffolded dirs go through the same onboard flow once
			// they're written to disk, so the Moved/Linked block is
			// identical. The "Scaffolded ... with empty ..." line was
			// already printed by the orchestrator at scaffold time so
			// the user sees the create event in real time alongside the
			// gitignore picker writes.
			addedCount++
			_, _ = fmt.Fprintf(Out, "%s Moved  %s  %s  %s\n",
				pathStyle.Render(g.arrowRight), d.Name,
				pathStyle.Render(g.arrowRight), config.Contract(d.CentralPath))
			_, _ = fmt.Fprintf(Out, "%s Linked %s  %s  %s\n",
				pathStyle.Render(g.arrowRight), d.Name,
				pathStyle.Render(g.arrowLeft), config.Contract(d.CentralPath))
		case StatusAlreadyTracked:
			Success("%s already tracked at %s", d.Name, config.Contract(d.CentralPath))
		}
	}

	skippedCount := 0
	for _, d := range r.Dirs {
		if d.Status != StatusSkippedSafety {
			continue
		}
		if skippedCount == 0 {
			Blank()
			Warn("Skipped (safety):")
		}
		skippedCount++
		for _, v := range d.SafetyViolations {
			_, _ = fmt.Fprintf(Out, "  - %s\n", v.Message)
		}
	}

	if addedCount > 0 {
		// blank separates the skipped block from the commit line.
		if skippedCount > 0 {
			Blank()
		}
		Success("Committed to central repo: %q", r.CommitMessage)
		Blank()
		path := shellQuoteForCD(config.Contract(r.CentralRepo), r.CentralRepo)
		_, _ = fmt.Fprintf(Out, "Next: cd %s && git push\n", path)
	}
}
