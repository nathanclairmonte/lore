// Package detect inspects a project root and decides which agent
// directory(ies) lore should onboard. Pure filesystem logic; no config or
// state knowledge.
//
// Slice 2 returns only the first match in the candidates list to keep the
// single-agent-dir use case shippable. Slice 5 expands to multi-dir,
// where this package will return every matching dir.
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

// DetectAgentDir scans root for the first entry in candidates that is a
// real directory (not a symlink, not a regular file). Iteration follows
// the order in candidates so callers control precedence via
// config.AgentDirs.
//
// Symlinked candidates are intentionally skipped in slice 2: an agent dir
// that's already a symlink is a slice-3 safety case, and detecting it as
// a "valid agent dir" would let `lore add` re-onboard a tracked project
// without surfacing the conflict. Skipping means the user gets a clear
// "no agent dir found" if every candidate is either absent or a symlink.
//
// Returns (AgentDir{}, false, nil) when nothing matches. Errors only on
// unexpected stat failures (permission, etc.).
func DetectAgentDir(root string, candidates []string) (AgentDir, bool, error) {
	for _, name := range candidates {
		path := filepath.Join(root, name)
		info, err := os.Lstat(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return AgentDir{}, false, lerr.Wrap(lerr.CodeGenericError, err,
				fmt.Sprintf("cannot stat %s", path))
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if !info.IsDir() {
			continue
		}
		return AgentDir{Name: name, Path: path}, true, nil
	}
	return AgentDir{}, false, nil
}
