package output

import (
	"fmt"

	"github.com/nathanclairmonte/lore/internal/config"
)

// LinkDirStatus mirrors project.LinkDirStatus so this package doesn't
// import project. cmd/lore/link.go translates the orchestrator's verdicts
// into this shape.
type LinkDirStatus string

const (
	LinkStatusLinked        LinkDirStatus = "linked"
	LinkStatusAlreadyLinked LinkDirStatus = "already-linked"
	LinkStatusRefused       LinkDirStatus = "refused"
)

// LinkDirResult mirrors project.LinkDirResult. Err is populated only when
// Status is LinkStatusRefused; Unignored flags an advisory gitignore
// warning for a dir that ended up not ignored.
type LinkDirResult struct {
	Name      string
	Status    LinkDirStatus
	LinkPath  string
	Err       error
	Unignored bool
}

// LinkResult is the data lore link hands to the output layer.
type LinkResult struct {
	ProjectName string
	ProjectPath string
	Dirs        []LinkDirResult
}

// PrintLinkResult renders the per-dir verdicts of a single-project link.
// Variants:
//
//   - Every dir already linked: terse "already linked" line so idempotent
//     re-runs stay quiet.
//   - Otherwise: header (project + path) plus one line per dir describing
//     what happened (linked / already linked / refused), then an advisory
//     gitignore warning for any dir that came up un-ignored.
//
// Suppressed in JSON mode.
func PrintLinkResult(r LinkResult) {
	if mode == ModeJSON {
		return
	}

	if allAlreadyLinked(r.Dirs) {
		Success("Project %s is already linked (%s)",
			r.ProjectName, config.Contract(r.ProjectPath))
		printGitignoreWarnings(r)
		return
	}

	Success("Project: %s (%s)", r.ProjectName, config.Contract(r.ProjectPath))
	Blank()

	g := activeGlyphs()
	pathStyle := stylePath()

	for _, d := range r.Dirs {
		switch d.Status {
		case LinkStatusLinked:
			_, _ = fmt.Fprintf(Out, "%s Linked: %s\n",
				pathStyle.Render(g.arrowRight), config.Contract(d.LinkPath))
		case LinkStatusAlreadyLinked:
			_, _ = fmt.Fprintf(Out, "  - already linked: %s\n",
				config.Contract(d.LinkPath))
		case LinkStatusRefused:
			reason := "drift refusal"
			if d.Err != nil {
				reason = d.Err.Error()
			}
			Warn("Refused %s: %s", config.Contract(d.LinkPath), reason)
		}
	}

	printGitignoreWarnings(r)
}

// allAlreadyLinked reports whether every dir was already correctly linked
// before this run. False for an empty dir set.
func allAlreadyLinked(dirs []LinkDirResult) bool {
	if len(dirs) == 0 {
		return false
	}
	for _, d := range dirs {
		if d.Status != LinkStatusAlreadyLinked {
			return false
		}
	}
	return true
}

// printGitignoreWarnings emits an advisory warning per un-ignored dir,
// nudging the user toward an interactive re-run that offers the picker.
func printGitignoreWarnings(r LinkResult) {
	for _, d := range r.Dirs {
		if !d.Unignored {
			continue
		}
		Warn("%s is not gitignored here; re-run `lore link %s` in an interactive shell to fix, or add it manually",
			d.Name, r.ProjectName)
	}
}

// LinkProjectStatus mirrors project.LinkProjectStatus.
type LinkProjectStatus string

const (
	LinkProjectLinked        LinkProjectStatus = "linked"
	LinkProjectAlreadyLinked LinkProjectStatus = "already-linked"
	LinkProjectRefused       LinkProjectStatus = "refused"
	LinkProjectFailed        LinkProjectStatus = "failed"
	LinkProjectAmbiguous     LinkProjectStatus = "ambiguous"
	LinkProjectMissing       LinkProjectStatus = "missing"
)

// LinkProjectResult mirrors project.LinkProjectResult.
type LinkProjectResult struct {
	Name    string
	Status  LinkProjectStatus
	Path    string
	Matches []string
	Link    LinkResult
	Err     error
}

// LinkAllResult is the data lore link --all hands to the output layer.
type LinkAllResult struct {
	ScannedRoots []string
	Projects     []LinkProjectResult
}

// PrintLinkAllResult renders the new-machine recovery summary: a header
// listing the scanned roots, then one line per tracked project (linked /
// already linked / not found / ambiguous / refused), and finally a footer
// flagging any linked-but-un-ignored dirs with a re-run hint. The sweep is
// non-interactive, so every problem is reported with the exact follow-up
// command rather than prompting.
//
// Suppressed in JSON mode.
func PrintLinkAllResult(r LinkAllResult) {
	if mode == ModeJSON {
		return
	}

	_, _ = fmt.Fprintln(Out, "Scanning configured roots for tracked projects...")
	for _, root := range r.ScannedRoots {
		_, _ = fmt.Fprintf(Out, "  %s\n", config.Contract(root))
	}

	if len(r.Projects) == 0 {
		Blank()
		Success("No tracked projects in the central repo")
		return
	}
	Blank()

	g := activeGlyphs()
	okStyle := styleSuccess()
	warnStyle := styleWarn()
	pathStyle := stylePath()
	hintStyle := styleHint()

	type unignoredDir struct{ project, dir string }
	var unignored []unignoredDir

	for _, p := range r.Projects {
		switch p.Status {
		case LinkProjectLinked:
			_, _ = fmt.Fprintf(Out, "%s %s  found at %s  %s linked\n",
				okStyle.Render(g.ok), p.Name,
				pathStyle.Render(config.Contract(p.Path)), g.arrowRight)
		case LinkProjectAlreadyLinked:
			_, _ = fmt.Fprintf(Out, "%s %s  already linked\n", okStyle.Render(g.ok), p.Name)
		case LinkProjectMissing:
			_, _ = fmt.Fprintf(Out, "%s %s  not found in any root\n", warnStyle.Render(g.warn), p.Name)
			_, _ = fmt.Fprintf(Out, "%s\n",
				hintStyle.Render(fmt.Sprintf("    run: lore link %s --path <path>", p.Name)))
		case LinkProjectAmbiguous:
			_, _ = fmt.Fprintf(Out, "%s %s  found in multiple roots\n", warnStyle.Render(g.warn), p.Name)
			for _, m := range p.Matches {
				_, _ = fmt.Fprintf(Out, "%s\n",
					hintStyle.Render(fmt.Sprintf("    run: lore link %s --path %s", p.Name, config.Contract(m))))
			}
		case LinkProjectRefused, LinkProjectFailed:
			reason := "failed"
			if p.Err != nil {
				reason = p.Err.Error()
			}
			_, _ = fmt.Fprintf(Out, "%s %s  %s\n", warnStyle.Render(g.warn), p.Name, reason)
		}

		for _, d := range p.Link.Dirs {
			if d.Unignored {
				unignored = append(unignored, unignoredDir{p.Name, d.Name})
			}
		}
	}

	if len(unignored) > 0 {
		Blank()
		Warn("Some linked directories are not gitignored on this machine:")
		for _, u := range unignored {
			Hint("  %s in %s (re-run `lore link %s` interactively to fix)", u.dir, u.project, u.project)
		}
	}
}
