// Package scan locates project checkouts on disk by walking the
// configured projects_roots to a bounded depth and matching directory
// basenames against a set of project names. Pure filesystem logic with no
// config or state knowledge: callers pass the roots, depth, and names in.
//
// Distinct from package detect, which inspects one already-known project
// root for agent dirs. scan searches many roots to answer "where does
// project X live on this machine?" - the core of `lore link --all`'s
// new-machine recovery sweep.
//
// Linux + macOS only per ADR 0001.
package scan

import (
	"os"
	"path/filepath"
	"strings"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
)

// ScanRoots walks each root up to depth levels deep and returns, per
// wanted project name, every absolute directory path whose basename
// matches. depth counts levels below a root: depth 1 visits a root's
// direct children, depth 2 their children too. The root itself is level 0
// and never a match.
//
// Only real directories are descended into or matched. A symlink reports
// false from DirEntry.IsDir() (the entry type is read without following),
// so symlinked dirs are skipped: this both avoids walk loops and stops
// lore's own agent-dir symlinks from matching. Dot-prefixed directories
// (.git, .cache, ...) are skipped wholesale, since real checkouts are not
// dot-named and descending into them is pure waste. A root that doesn't
// exist on this machine is skipped silently (a fresh machine won't have
// every configured root).
//
// The returned map only holds names that matched at least one path; names
// with zero matches are absent. Match order within a name follows root
// order then walk order, deterministic across runs for a fixed tree.
func ScanRoots(roots []string, depth int, names []string) (map[string][]string, error) {
	if len(names) == 0 {
		return map[string][]string{}, nil
	}
	want := make(map[string]struct{}, len(names))
	for _, n := range names {
		want[n] = struct{}{}
	}

	matches := make(map[string][]string)
	for _, root := range roots {
		if err := walk(root, depth, want, matches); err != nil {
			return nil, err
		}
	}
	return matches, nil
}

// walk descends dir while remaining > 0, recording any child directory
// whose name is in want. remaining is the number of levels still allowed
// below dir, reaching 0 at the depth limit.
func walk(dir string, remaining int, want map[string]struct{}, matches map[string][]string) error {
	if remaining <= 0 {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		// a configured root (or subdir) absent on this machine is fine: the
		// caller surfaces unfound projects via the "missing" summary.
		if os.IsNotExist(err) {
			return nil
		}
		return lerr.Wrap(lerr.CodeGenericError, err, "cannot read "+dir)
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		child := filepath.Join(dir, e.Name())
		if _, ok := want[e.Name()]; ok {
			matches[e.Name()] = append(matches[e.Name()], child)
		}
		if err := walk(child, remaining-1, want, matches); err != nil {
			return err
		}
	}
	return nil
}
