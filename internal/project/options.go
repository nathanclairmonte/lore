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

	// Name overrides the project name lore records (and uses to key
	// central/projects/<name>/). Empty means "use filepath.Base of the
	// resolved project path". Useful when two projects share a basename
	// in different parents, or when the user wants a different label in
	// central than the on-disk directory name.
	Name string

	// Create asks Add to scaffold an agent dir at the project root when
	// none would otherwise be found. With --dir, each named dir that
	// doesn't already exist is scaffolded (silent default filename). Without
	// --dir, an interactive picker collects a (dir, file) pair. Existing
	// dirs are left untouched (idempotent skip). Non-TTY without --dir is
	// CodeInconsistency.
	Create bool
}
