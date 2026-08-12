package git

import (
	"os"
	"path/filepath"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
)

// MergeFile performs a three-way text merge with stable project and central labels.
func MergeFile(project, base, central []byte) ([]byte, bool, error) {
	temporary, err := os.MkdirTemp("", "lore-merge-file-")
	if err != nil {
		return nil, false, lerr.Wrap(lerr.CodeGenericError, err, "cannot create merge workspace")
	}
	defer func() { _ = os.RemoveAll(temporary) }()

	paths := []string{
		filepath.Join(temporary, "project"),
		filepath.Join(temporary, "base"),
		filepath.Join(temporary, "central"),
	}
	for i, data := range [][]byte{project, base, central} {
		if err := os.WriteFile(paths[i], data, 0o600); err != nil {
			return nil, false, lerr.Wrap(lerr.CodeGenericError, err, "cannot prepare merge input")
		}
	}

	result, err := RunRaw("", "merge-file", "--stdout",
		"-L", "project", "-L", "base", "-L", "central",
		paths[0], paths[1], paths[2])
	if err != nil {
		return nil, false, err
	}
	switch {
	case result.ExitCode == 0:
		return result.Stdout, false, nil
	// git reports the conflict count as its exit status, capped at 127, and
	// reserves negative statuses for real failure. treating anything above 1 as
	// an error would discard a correct merge of a file with several conflicts.
	case result.ExitCode >= 1 && result.ExitCode <= 127:
		return result.Stdout, true, nil
	default:
		return nil, false, lerr.New(lerr.CodeGenericError, "git could not merge file content")
	}
}
