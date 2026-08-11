package output

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/nathanclairmonte/lore/internal/config"
)

// PullDirStatus mirrors project.PullDirStatus.
type PullDirStatus string

const (
	// PullDirMaterialized means the directory was created from central.
	PullDirMaterialized PullDirStatus = "materialized"
	// PullDirUpdated means the directory received newer central content.
	PullDirUpdated PullDirStatus = "updated"
	// PullDirUnchanged means the directory already matched central.
	PullDirUnchanged PullDirStatus = "unchanged"
	// PullDirProjectAhead means the directory holds changes central lacks.
	PullDirProjectAhead PullDirStatus = "project-ahead"
	// PullDirMerged means text differences merged without conflicts.
	PullDirMerged PullDirStatus = "merged"
	// PullDirConflicted means text conflict markers were written.
	PullDirConflicted PullDirStatus = "conflicted"
	// PullDirKeepBoth means a binary or non-text sibling was written.
	PullDirKeepBoth PullDirStatus = "keep-both"
	// PullDirCentralForced means explicit central selection replaced the directory.
	PullDirCentralForced PullDirStatus = "central-forced"
	// PullDirProjectForced means explicit project selection retained the directory.
	PullDirProjectForced PullDirStatus = "project-forced"
)

// PullCheckoutStatus mirrors project.PullCheckoutStatus. Values are identical
// to the project package's; the cmd layer converts.
type PullCheckoutStatus string

const (
	// PullCheckoutMaterialized means the copy was created from central.
	PullCheckoutMaterialized PullCheckoutStatus = "materialized"
	// PullCheckoutUpdated means the copy received newer central content.
	PullCheckoutUpdated PullCheckoutStatus = "updated"
	// PullCheckoutAdopted means a matching copy was recorded at central HEAD.
	PullCheckoutAdopted PullCheckoutStatus = "adopted"
	// PullCheckoutUnchanged means the copy already matched at a live base.
	PullCheckoutUnchanged PullCheckoutStatus = "unchanged"
	// PullCheckoutProjectAhead means the copy holds changes central lacks.
	PullCheckoutProjectAhead PullCheckoutStatus = "project-ahead"
	// PullCheckoutRefused means a safety gate blocked the checkout.
	PullCheckoutRefused PullCheckoutStatus = "refused"
	// PullCheckoutFailed means the checkout was attempted and errored.
	PullCheckoutFailed PullCheckoutStatus = "failed"
	// PullCheckoutMerged means every divergence merged cleanly.
	PullCheckoutMerged PullCheckoutStatus = "merged"
	// PullCheckoutConflicted means unresolved artifacts were published.
	PullCheckoutConflicted PullCheckoutStatus = "conflicted"
	// PullCheckoutCentralForced means central was explicitly selected.
	PullCheckoutCentralForced PullCheckoutStatus = "central-forced"
	// PullCheckoutProjectForced means project was explicitly selected.
	PullCheckoutProjectForced PullCheckoutStatus = "project-forced"
)

// PullProjectStatus mirrors project.PullProjectStatus.
type PullProjectStatus string

const (
	// PullProjectPulled means at least one checkout received content.
	PullProjectPulled PullProjectStatus = "pulled"
	// PullProjectUnchanged means every checkout already matched.
	PullProjectUnchanged PullProjectStatus = "unchanged"
	// PullProjectMissing means no checkout was found in any configured root.
	PullProjectMissing PullProjectStatus = "missing"
	// PullProjectAmbiguous means several roots hold a candidate checkout.
	PullProjectAmbiguous PullProjectStatus = "ambiguous"
	// PullProjectRefused means a safety gate blocked the project.
	PullProjectRefused PullProjectStatus = "refused"
	// PullProjectFailed means the project was attempted and errored.
	PullProjectFailed PullProjectStatus = "failed"
	// PullProjectConflicted means at least one checkout needs resolution.
	PullProjectConflicted PullProjectStatus = "conflicted"
)

// PullConflictResult is one public unresolved merge artifact.
type PullConflictResult struct {
	Path        string `json:"path"`
	Kind        string `json:"kind"`
	SiblingPath string `json:"sibling_path"`
}

// PullDirResult is one public agent-directory outcome.
type PullDirResult struct {
	Name      string               `json:"name"`
	Path      string               `json:"path"`
	Status    PullDirStatus        `json:"status"`
	Unignored bool                 `json:"unignored"`
	Conflicts []PullConflictResult `json:"conflicts"`
}

// PullCheckoutResult is one public checkout outcome.
type PullCheckoutResult struct {
	Path     string             `json:"path"`
	Status   PullCheckoutStatus `json:"status"`
	SyncBase string             `json:"sync_base"`
	Dirs     []PullDirResult    `json:"dirs"`
	Error    string             `json:"error"`
}

// PullProjectResult is one public project outcome.
type PullProjectResult struct {
	Name      string               `json:"name"`
	Status    PullProjectStatus    `json:"status"`
	Matches   []string             `json:"matches"`
	Checkouts []PullCheckoutResult `json:"checkouts"`
	Error     string               `json:"error"`
}

// PullResult is the public pull output model.
type PullResult struct {
	CentralRepo  string              `json:"central_repo"`
	CentralHead  string              `json:"central_head"`
	ScannedRoots []string            `json:"scanned_roots"`
	Projects     []PullProjectResult `json:"projects"`
}

// PrintPull renders pull outcomes in human or JSON form.
func PrintPull(result PullResult) {
	if mode == ModeJSON {
		_ = json.NewEncoder(Out).Encode(result)
		return
	}
	if len(result.Projects) == 0 {
		Success("No tracked projects in the central repo")
		return
	}
	printScannedRoots(result.ScannedRoots)

	g := activeGlyphs()
	pathStyle := stylePath()
	for _, project := range result.Projects {
		switch project.Status {
		case PullProjectMissing:
			Warn("%s was not found in any configured root", project.Name)
			Hint("run `lore pull %s --path <path>`", project.Name)
			continue
		case PullProjectAmbiguous:
			Warn("%s was found in multiple configured roots", project.Name)
			for _, match := range project.Matches {
				Hint("run `lore pull %s --path %s`", project.Name, config.Contract(match))
			}
			continue
		}
		for _, checkout := range project.Checkouts {
			switch checkout.Status {
			case PullCheckoutMaterialized:
				_, _ = fmt.Fprintf(Out, "%s Materialized %s  %s\n", pathStyle.Render(g.arrowLeft), project.Name, config.Contract(checkout.Path))
			case PullCheckoutUpdated:
				_, _ = fmt.Fprintf(Out, "%s Pulled %s  %s\n", pathStyle.Render(g.arrowLeft), project.Name, config.Contract(checkout.Path))
			case PullCheckoutAdopted:
				Success("Adopted matching checkout: %s", config.Contract(checkout.Path))
			case PullCheckoutProjectAhead:
				Success("Kept project-ahead checkout: %s", config.Contract(checkout.Path))
				Hint("run `lore push --project-from-cwd` from that checkout to capture its changes")
			case PullCheckoutUnchanged:
				Success("Already up to date: %s", config.Contract(checkout.Path))
			case PullCheckoutMerged:
				_, _ = fmt.Fprintf(Out, "%s Merged %s  %s\n", pathStyle.Render(g.arrowLeft), project.Name, config.Contract(checkout.Path))
				Hint("run `lore push --project-from-cwd` from that checkout to capture the merge")
			case PullCheckoutConflicted:
				Warn("Pulled %s with unresolved conflicts: %s", project.Name, config.Contract(checkout.Path))
				printPullConflicts(checkout)
				Hint("resolve the files above, then run `lore push --project-from-cwd` from that checkout")
			case PullCheckoutCentralForced:
				Warn("Selected central content for %s: %s", project.Name, config.Contract(checkout.Path))
			case PullCheckoutProjectForced:
				Warn("Kept project content for %s: %s", project.Name, config.Contract(checkout.Path))
				Hint("run `lore push --project-from-cwd` from that checkout to capture its content")
			case PullCheckoutRefused, PullCheckoutFailed:
				Warn("Could not pull %s: %s", config.Contract(checkout.Path), checkout.Error)
			default:
				Warn("Could not render pull result for %s: unknown status %q", config.Contract(checkout.Path), checkout.Status)
			}
		}
	}
}

func printPullConflicts(checkout PullCheckoutResult) {
	for _, dir := range checkout.Dirs {
		for _, conflict := range dir.Conflicts {
			path := filepath.Join(dir.Name, conflict.Path)
			if conflict.Kind == "keep-both" {
				Warn("binary conflict: %s and %s", path, filepath.Join(dir.Name, conflict.SiblingPath))
				continue
			}
			Warn("text conflict: %s contains conflict markers", path)
		}
	}
}

// PrintPullGitignoreWarnings prints warnings after the interactive picker.
func PrintPullGitignoreWarnings(result PullResult) {
	if mode == ModeJSON {
		return
	}
	for _, project := range result.Projects {
		for _, checkout := range project.Checkouts {
			for _, dir := range checkout.Dirs {
				if dir.Unignored {
					Warn("%s is not gitignored in %s", dir.Name, config.Contract(checkout.Path))
				}
			}
		}
	}
}
