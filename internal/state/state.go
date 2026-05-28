// Package state loads and saves lore's per-machine local state file
// (~/.local/state/lore/state.toml). State is never committed; it's a
// per-project record of on-disk path + tracked agent dirs, regeneratable
// by `lore link`.
package state

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/BurntSushi/toml"
	lerr "github.com/nathanclairmonte/lore/internal/errors"
)

// CurrentSchemaVersion is the schema_version value written by this lore
// build. Bumped to 2 in slice 5 to record the set of tracked agent dirs
// per project. Older versions are rejected at Load time: lore is still
// pre-1.0 with effectively one user, so the cost of carrying migration
// code outweighs the convenience.
const CurrentSchemaVersion = 2

const stateFileRelPath = "lore/state.toml"

// ProjectEntry is the per-project record. AgentDirs is the set of agent
// directories tracked under projects/<name>/ in the central repo, in the
// order they were added. Always non-empty for a tracked project:
// RemoveAgentDir collapses the entry when the last dir leaves.
type ProjectEntry struct {
	Path      string   `toml:"path"`
	AgentDirs []string `toml:"agent_dirs"`
}

// State is the typed representation of state.toml.
type State struct {
	SchemaVersion int                     `toml:"schema_version"`
	Projects      map[string]ProjectEntry `toml:"projects"`
}

// Path returns the on-disk location of the state file. Honors
// XDG_STATE_HOME; falls back to ~/.local/state/lore/state.toml.
func Path() (string, error) {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, stateFileRelPath), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", lerr.Wrap(lerr.CodeGenericError, err, "cannot determine home directory")
	}
	return filepath.Join(home, ".local", "state", stateFileRelPath), nil
}

// Defaults returns an empty State at the current schema version.
func Defaults() State {
	return State{
		SchemaVersion: CurrentSchemaVersion,
		Projects:      map[string]ProjectEntry{},
	}
}

// Load reads the state file. If absent, returns Defaults() with
// exists=false (nil err). Files at schema versions other than the
// current one are rejected with CodeInconsistency; the user is expected
// to remove the stale file (a future `lore link` will regenerate it).
func Load() (st State, exists bool, err error) {
	path, err := Path()
	if err != nil {
		return State{}, false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Defaults(), false, nil
		}
		return State{}, false, lerr.Wrap(lerr.CodeGenericError, err, "cannot read state file")
	}
	// decode into a zero-value State (NOT Defaults()) so a file missing
	// schema_version doesn't silently inherit CurrentSchemaVersion and
	// slip past the version check below.
	if _, err := toml.Decode(string(data), &st); err != nil {
		return State{}, false, lerr.Wrap(lerr.CodeGenericError, err, "malformed state file at "+path)
	}
	if st.SchemaVersion != CurrentSchemaVersion {
		return State{}, false, lerr.Newf(lerr.CodeInconsistency,
			"state file at %s is schema version %d, but this lore expects %d",
			path, st.SchemaVersion, CurrentSchemaVersion).
			WithHint("delete the file and re-run `lore add` (or `lore link` once it ships)")
	}
	// enforce the ProjectEntry invariant at the trust boundary: every
	// tracked project has at least one agent dir. AddAgentDir and
	// RemoveAgentDir maintain this in memory, but a hand-edited state
	// file could skip the agent_dirs key entirely and slip through
	// otherwise, leaving Add to fail with a confusing central-side
	// CodeConflict instead of a clear "your state file is wrong."
	for name, entry := range st.Projects {
		if len(entry.AgentDirs) == 0 {
			return State{}, false, lerr.Newf(lerr.CodeInconsistency,
				"state file at %s has no agent_dirs for project %q",
				path, name).
				WithHint("delete the file and re-run `lore add` (or `lore link` once it ships)")
		}
	}
	if st.Projects == nil {
		st.Projects = map[string]ProjectEntry{}
	}
	return st, true, nil
}

// ProjectEntryFor returns the entry recorded for name. Second return is
// false when the project isn't tracked.
func (s *State) ProjectEntryFor(name string) (ProjectEntry, bool) {
	if s.Projects == nil {
		return ProjectEntry{}, false
	}
	e, ok := s.Projects[name]
	return e, ok
}

// PathFor returns the absolute path recorded for name. Thin wrapper for
// callers that only care about the project path, not its agent dirs.
func (s *State) PathFor(name string) (string, bool) {
	e, ok := s.ProjectEntryFor(name)
	if !ok {
		return "", false
	}
	return e.Path, true
}

// Clone returns a copy of s safe to mutate independently. Projects is a map
// (reference type), so a plain struct copy would alias it; orchestrators
// that snapshot pre-mutation state for a rollback (lore rm / mv) need this.
// Per-entry AgentDirs slices are shared, which is safe because callers
// replace whole entries rather than mutating a slice in place.
func (s *State) Clone() State {
	out := State{
		SchemaVersion: s.SchemaVersion,
		Projects:      make(map[string]ProjectEntry, len(s.Projects)),
	}
	maps.Copy(out.Projects, s.Projects)
	return out
}

// AddAgentDir records (name, projectPath, dirName) on the in-memory
// state. Idempotent on a triple already present; rejects the same name
// at a different projectPath (callers must remove the old entry first).
//
// Rejects empty names, empty paths, non-absolute paths, and empty
// dirNames so a misbehaving caller can't poison state with values that
// would later fail to round-trip or resolve.
//
// Does NOT persist; callers control when to Save so multi-step flows
// (lore add) can roll back without leaving a half-written state file.
func (s *State) AddAgentDir(name, projectPath, dirName string) error {
	if name == "" {
		return lerr.New(lerr.CodeGenericError, "project name cannot be empty")
	}
	if projectPath == "" {
		return lerr.New(lerr.CodeGenericError, "project path cannot be empty")
	}
	if !filepath.IsAbs(projectPath) {
		return lerr.Newf(lerr.CodeGenericError,
			"project path must be absolute, got %q", projectPath)
	}
	if dirName == "" {
		return lerr.New(lerr.CodeGenericError, "agent dir name cannot be empty")
	}
	if s.Projects == nil {
		s.Projects = map[string]ProjectEntry{}
	}
	existing, ok := s.Projects[name]
	if !ok {
		s.Projects[name] = ProjectEntry{
			Path:      projectPath,
			AgentDirs: []string{dirName},
		}
		return nil
	}
	if existing.Path != projectPath {
		return lerr.Newf(lerr.CodeConflict,
			"project %q is already tracked at %s", name, existing.Path)
	}
	if slices.Contains(existing.AgentDirs, dirName) {
		return nil
	}
	existing.AgentDirs = append(existing.AgentDirs, dirName)
	s.Projects[name] = existing
	return nil
}

// RemoveAgentDir drops dirName from name's tracked set. If the removal
// empties the project's agent dir list, the project entry itself is
// removed (a project with zero tracked dirs is not a tracked project).
// No-op when the project or the dir isn't tracked.
//
// Does NOT persist; see AddAgentDir for the rationale.
func (s *State) RemoveAgentDir(name, dirName string) {
	if s.Projects == nil {
		return
	}
	entry, ok := s.Projects[name]
	if !ok {
		return
	}
	// fresh slice rather than entry.AgentDirs[:0]: in-place filtering
	// would alias the existing backing array, so any caller still
	// holding the old slice (or a ProjectEntry value copied earlier in
	// the same call) would see corrupted data.
	filtered := make([]string, 0, len(entry.AgentDirs))
	for _, d := range entry.AgentDirs {
		if d != dirName {
			filtered = append(filtered, d)
		}
	}
	if len(filtered) == 0 {
		delete(s.Projects, name)
		return
	}
	entry.AgentDirs = filtered
	s.Projects[name] = entry
}

// Save writes the state file atomically, creating its parent directory
// if needed. Writes to a sibling tempfile then renames into place so a
// crash or encode failure can never leave a truncated state.toml on
// disk. lore add's rollback path leans on this: if Save errors out the
// previous state file is still intact.
func Save(s State) error {
	path, err := Path()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return lerr.Wrap(lerr.CodeGenericError, err, "cannot create state directory")
	}
	// tempfile in the SAME directory so os.Rename is an atomic move on
	// POSIX (cross-device renames would silently fall back to copy+remove).
	tmp, err := os.CreateTemp(dir, "state-*.toml.tmp")
	if err != nil {
		return lerr.Wrap(lerr.CodeGenericError, err, "cannot create temporary state file")
	}
	tmpPath := tmp.Name()
	// cleanup defer: removes the tempfile if anything below this point
	// errors out before the rename. once rename succeeds, the tempfile
	// no longer exists at tmpPath, so Remove returns ErrNotExist (ignored).
	defer func() { _ = os.Remove(tmpPath) }()

	if err := toml.NewEncoder(tmp).Encode(s); err != nil {
		_ = tmp.Close()
		return lerr.Wrap(lerr.CodeGenericError, err, "cannot encode state")
	}
	if err := tmp.Close(); err != nil {
		return lerr.Wrap(lerr.CodeGenericError, err, "cannot close temporary state file")
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return lerr.Wrap(lerr.CodeGenericError, err, "cannot set permissions on state file")
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return lerr.Wrap(lerr.CodeGenericError, err, "cannot replace state file")
	}
	return nil
}
