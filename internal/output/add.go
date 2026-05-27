package output

import (
	"fmt"

	"github.com/nathanclairmonte/lore/internal/config"
)

// AddResult is the data lore add hands to the output layer. Mirrors
// project.AddResult; cmd/lore/add.go converts the orchestrator's return
// into this shape so the output package stays decoupled from project.
type AddResult struct {
	ProjectName    string
	ProjectPath    string
	AgentDirName   string
	CentralPath    string
	CentralRepo    string
	CommitMessage  string
	AlreadyTracked bool
}

// PrintAddResult renders the happy-path summary defined in SPEC.md's
// `lore add` section. Two variants:
//
//   - Fresh add: three-block layout - the three ✓ confirmations, two →
//     filesystem ops, the ✓ commit line, and the "Next:" hint pointing at
//     `git push` on the central repo.
//
//   - Already-tracked re-run: single ✓ line. Idempotency is meant to be
//     quiet rather than re-printing the full block.
//
// Suppressed in JSON mode: slice 21 will wire a typed envelope; for now
// the human block is just skipped so script callers don't get noise.
func PrintAddResult(r AddResult) {
	if mode == ModeJSON {
		return
	}
	if r.AlreadyTracked {
		Success("Project %s is already tracked at %s",
			r.ProjectName, config.Contract(r.CentralPath))
		return
	}

	Success("Project:   %s (%s)", r.ProjectName, config.Contract(r.ProjectPath))
	Success("Agent dir: %s", r.AgentDirName)
	// "Gitignored" without "globally": slice 4's picker lets the user
	// place the entry in one of three locations (repo .gitignore, global
	// excludes, .git/info/exclude). Only one of those is truly global,
	// so the older "Globally gitignored: yes" wording was a lie in two
	// of three picker outcomes. The picker-write line printed upstream
	// already names the exact location when relevant.
	Success("Gitignored: yes")
	Blank()
	g := activeGlyphs()
	pathStyle := stylePath()
	_, _ = fmt.Fprintf(Out, "%s Moved  %s  %s  %s\n",
		pathStyle.Render(g.arrowRight), r.AgentDirName,
		pathStyle.Render(g.arrowRight), config.Contract(r.CentralPath))
	_, _ = fmt.Fprintf(Out, "%s Linked %s  %s  %s\n",
		pathStyle.Render(g.arrowRight), r.AgentDirName,
		pathStyle.Render(g.arrowLeft), config.Contract(r.CentralPath))
	Success("Committed to central repo: %q", r.CommitMessage)
	Blank()
	path := shellQuoteForCD(config.Contract(r.CentralRepo), r.CentralRepo)
	_, _ = fmt.Fprintf(Out, "Next: cd %s && git push\n", path)
}
