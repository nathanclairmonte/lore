package project

import (
	"path/filepath"
	"strings"

	"github.com/nathanclairmonte/lore/internal/state"
)

// longestRecordedAncestor returns the tracked project whose recorded path
// is dir itself or the nearest ancestor of dir. The longest match wins so a
// project nested inside another resolves to the inner one.
func longestRecordedAncestor(st *state.State, dir string) (name, path string) {
	for projectName, entry := range st.Projects {
		for _, checkout := range entry.Checkouts {
			if !isSelfOrAncestor(checkout.Path, dir) {
				continue
			}
			if len(checkout.Path) > len(path) {
				name, path = projectName, checkout.Path
			}
		}
	}
	return name, path
}

// isSelfOrAncestor reports whether descendant is ancestor itself or lives
// underneath it. A cleaned relative path prevents parent escapes from matching.
func isSelfOrAncestor(ancestor, descendant string) bool {
	relative, err := filepath.Rel(ancestor, descendant)
	if err != nil {
		return false
	}
	if relative == "." {
		return true
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
