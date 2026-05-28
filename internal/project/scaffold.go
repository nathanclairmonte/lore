package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/prompt"
)

// pickScaffoldDir and pickScaffoldFile are vars (not consts) so tests
// can stub the interactive pickers with fixed choices, mirroring the
// pickGitignoreTarget test-seam pattern.
var (
	pickScaffoldDir  = prompt.PickScaffoldDir
	pickScaffoldFile = prompt.PickScaffoldFile
)

// scaffoldDefaultFile is the silent default when --dir is passed (no
// interactive prompt fires). AGENTS.md is the emerging cross-tool
// convention.
const scaffoldDefaultFile = "AGENTS.md"

// scaffoldAgentDir creates dirName/ and dirName/fileName at root if and
// only if dirName/ doesn't already exist. Returns true when the dir was
// freshly created (and the file written), false when an existing dir
// caused a skip. Never clobbers an existing file: if dirName/ exists
// the function leaves whatever's inside it alone and returns (false,
// nil).
//
// Refuses (CodeRefusedSafety) when the path at dirName exists as a
// regular file or as a symlink: scaffolding over a regular file would
// silently clobber user content, and a symlink there is a state lore
// can't safely take ownership of without breaking whatever the link
// currently points at. Cleans up the freshly-created directory if the
// file write fails so a partial scaffold doesn't leave cruft behind.
func scaffoldAgentDir(root, dirName, fileName string) (bool, error) {
	target := filepath.Join(root, dirName)
	info, err := os.Lstat(target)
	if err == nil {
		// path exists - classify and decide.
		if info.Mode()&os.ModeSymlink != 0 {
			return false, lerr.Newf(lerr.CodeRefusedSafety,
				"%s exists as a symlink; --create cannot scaffold over a symlink", target).
				WithHint("remove the symlink or drop --create and rerun")
		}
		if !info.IsDir() {
			return false, lerr.Newf(lerr.CodeRefusedSafety,
				"%s exists as a regular file; --create cannot scaffold over a file", target).
				WithHint("move the file aside and rerun")
		}
		// dir already present: idempotent skip.
		return false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return false, lerr.Wrap(lerr.CodeGenericError, err,
			fmt.Sprintf("cannot stat %s", target))
	}

	if mkdirErr := os.Mkdir(target, 0o755); mkdirErr != nil {
		return false, lerr.Wrap(lerr.CodeGenericError, mkdirErr,
			fmt.Sprintf("cannot create %s", target))
	}
	filePath := filepath.Join(target, fileName)
	// O_EXCL guards the "dir was created fresh but file somehow already
	// exists" race; the empty starter file should be a brand-new write.
	f, openErr := os.OpenFile(filePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if openErr != nil {
		// roll back the fresh directory so a partial scaffold doesn't
		// leave a half-state for the user to clean up. Matches project.go's
		// rollback convention of surfacing cleanup failures via stderr
		// rather than swallowing them.
		if rmErr := os.Remove(target); rmErr != nil {
			fmt.Fprintf(os.Stderr, "warning: cannot remove %s after scaffold failure: %v\n", target, rmErr)
		}
		return false, lerr.Wrap(lerr.CodeGenericError, openErr,
			fmt.Sprintf("cannot create %s", filePath))
	}
	if closeErr := f.Close(); closeErr != nil {
		if rmErr := os.Remove(filePath); rmErr != nil {
			fmt.Fprintf(os.Stderr, "warning: cannot remove %s after scaffold failure: %v\n", filePath, rmErr)
		}
		if rmErr := os.Remove(target); rmErr != nil {
			fmt.Fprintf(os.Stderr, "warning: cannot remove %s after scaffold failure: %v\n", target, rmErr)
		}
		return false, lerr.Wrap(lerr.CodeGenericError, closeErr,
			fmt.Sprintf("cannot close %s", filePath))
	}
	return true, nil
}

// scaffoldChoice captures the (dirName, fileName) pair the orchestrator
// will hand to scaffoldAgentDir, sourced from either --dir + silent
// default or the interactive pickers.
type scaffoldChoice struct {
	DirName  string
	FileName string
}

// scaffoldChoicesFromOptions builds the scaffolding plan when --create
// is set. When --dir was passed, each named dir scaffolds with the
// silent default filename (no prompts fire, so scripted callers stay
// scripted). When --dir was empty, the interactive pickers run once to
// collect a single (dir, file) pair.
//
// The function does not touch disk; it only returns the planned set.
// scaffoldAgentDir is what actually writes.
func scaffoldChoicesFromOptions(dirs []string, configuredDirs []string) ([]scaffoldChoice, error) {
	if len(dirs) > 0 {
		out := make([]scaffoldChoice, 0, len(dirs))
		for _, name := range dirs {
			if err := validateScaffoldName(name, "--dir"); err != nil {
				return nil, err
			}
			out = append(out, scaffoldChoice{DirName: name, FileName: scaffoldDefaultFile})
		}
		return out, nil
	}
	dirName, err := pickScaffoldDir(configuredDirs)
	if err != nil {
		return nil, err
	}
	if err := validateScaffoldName(dirName, "scaffold dir picker"); err != nil {
		return nil, err
	}
	fileName, err := pickScaffoldFile()
	if err != nil {
		return nil, err
	}
	if err := validateScaffoldName(fileName, "scaffold file picker"); err != nil {
		return nil, err
	}
	return []scaffoldChoice{{DirName: dirName, FileName: fileName}}, nil
}

// validateScaffoldName rejects the obvious filesystem-illegal cases for
// a typed agent-dir name or filename: empty, "." / "..", anything with
// "/". Wider than nameSlug because agent-dir names conventionally start
// with "." and filenames carry extensions; this is the loose "won't
// break the filesystem" gate, not the project-name strict-slug gate.
func validateScaffoldName(name, source string) error {
	if name == "" {
		return lerr.Newf(lerr.CodeGenericError,
			"%s produced an empty name", source).
			WithHint("type a non-empty name and rerun")
	}
	if name == "." || name == ".." {
		return lerr.Newf(lerr.CodeGenericError,
			"%s %q is not a valid name", source, name)
	}
	if strings.ContainsRune(name, '/') || strings.ContainsRune(name, os.PathSeparator) {
		return lerr.Newf(lerr.CodeGenericError,
			"%s %q must not contain a path separator", source, name)
	}
	return nil
}
