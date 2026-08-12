// Package detect inspects a project root and decides which agent
// directory(ies) lore should onboard. Pure filesystem logic; no config or
// state knowledge.
package detect

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
)

// AgentDir names one detected agent directory at a project root.
type AgentDir struct {
	Name string // bare directory name, e.g. ".claude"
	Path string // absolute path on disk
}

// DetectAgentDirs scans root for every entry in candidates that is a
// real directory (not a symlink, not a regular file). Iteration follows
// the order in candidates so callers control precedence via
// config.AgentDirs and `lore add` preserves a stable per-run order.
//
// Symlinked candidates are intentionally skipped. A top-level link cannot
// satisfy the project-copy contract. CheckSafetyAll handles it when the user
// requests that candidate explicitly through `--dir`.
//
// Returns an empty slice when nothing matches (never a nil-vs-empty
// distinction). Errors only on unexpected stat failures (permission etc).
func DetectAgentDirs(root string, candidates []string) ([]AgentDir, error) {
	dirs := make([]AgentDir, 0, len(candidates))
	for _, name := range candidates {
		path := filepath.Join(root, name)
		info, err := os.Lstat(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, lerr.Wrap(lerr.CodeGenericError, err,
				fmt.Sprintf("cannot stat %s", path))
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if !info.IsDir() {
			continue
		}
		dirs = append(dirs, AgentDir{Name: name, Path: path})
	}
	return dirs, nil
}
