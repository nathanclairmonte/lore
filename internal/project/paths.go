package project

import (
	"os"
	"path/filepath"
	"strings"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/state"
)

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func promoteWorst(worst, err error) error {
	if worst == nil {
		return err
	}
	if lerr.CodeOf(err) == lerr.CodeRefusedSafety && lerr.CodeOf(worst) != lerr.CodeRefusedSafety {
		return err
	}
	return worst
}

func pathUnderAnyRoot(dir string, roots []string) bool {
	for _, root := range roots {
		canonicalRoot := root
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			canonicalRoot = resolved
		}
		if isSelfOrAncestor(canonicalRoot, dir) {
			return true
		}
	}
	return false
}

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
