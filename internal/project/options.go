package project

// AddOptions configures a single Add() call. Slice 2 carries only the
// project path; --name, --create, --no-commit, --dir land in slices 5/6
// and will extend this struct.
type AddOptions struct {
	// ProjectPath is the project root. May be relative or contain a `~`;
	// Add resolves to an absolute, symlink-evaluated canonical path
	// before doing anything else. Empty string means "use cwd".
	ProjectPath string
}
