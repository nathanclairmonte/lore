package project

// AddOptions configures a single Add() call.
type AddOptions struct {
	// ProjectPath is the project root. May be relative or contain a `~`;
	// Add resolves to an absolute, symlink-evaluated canonical path
	// before doing anything else. Empty string means "use cwd".
	ProjectPath string

	// Dirs scopes which agent dirs to add. Empty slice (the default)
	// means "all detected agent dirs at the project root"; a non-empty
	// slice restricts the run to the named dirs. Names that don't exist
	// at the project root surface as CodeNotFound; names already tracked
	// for this project are reported as AlreadyTracked rather than added
	// again (extending a tracked project is supported via a fresh
	// `--dir` invocation).
	Dirs []string
}
