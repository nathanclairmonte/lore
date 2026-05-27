// Package state loads and saves lore's per-machine local state file
// (~/.local/state/lore/state.toml). State is never committed; it's a
// per-project record of on-disk path + tracked agent dirs, regeneratable
// by `lore link`.
package state

import (
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/BurntSushi/toml"
	lerr "github.com/nathanclairmonte/lore/internal/errors"
)

// CurrentSchemaVersion is the schema_version value written by this lore
// build. Bumped to 2 in slice 5 to record the set of tracked agent dirs
// per project (the slice 1-4 schema only carried project name -> path).
// Load auto-migrates older versions; Save always writes the current one.
const CurrentSchemaVersion = 2

const stateFileRelPath = "lore/state.toml"

// ProjectEntry is the per-project record. AgentDirs is the set of agent
// directories tracked under projects/<name>/ in the central repo, in the
// order they were added.
//
// AgentDirs == nil is the documented v1->v2 migration sentinel: state
// loaded from a v1 file has no per-dir info and stays nil until a
// project-level command backfills it from central. AgentDirs == [] means
// the user explicitly cleared every dir (shouldn't happen in practice
// since RemoveAgentDir collapses the entry when the last dir leaves).
type ProjectEntry struct {
	Path      string   `toml:"path"`
	AgentDirs []string `toml:"agent_dirs,omitempty"`
}

// State is the typed representation of state.toml.
type State struct {
	SchemaVersion int                     `toml:"schema_version"`
	Projects      map[string]ProjectEntry `toml:"projects"`
}

// rawState is the on-disk shape used only for decode. Carrying both the
// v1 (`paths`) and v2 (`projects`) keys lets Load read either without two
// passes; the in-memory State only ever exposes the v2 shape.
type rawState struct {
	SchemaVersion int                     `toml:"schema_version"`
	Paths         map[string]string       `toml:"paths"`
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
// exists=false (nil err). v1 state files (with a `paths` table and no
// `projects`) are migrated in-memory: each path becomes a ProjectEntry
// with AgentDirs=nil so the orchestrator knows to backfill from central
// on first use. The new shape isn't persisted until the next Save.
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
	var raw rawState
	if _, err := toml.Decode(string(data), &raw); err != nil {
		return State{}, false, lerr.Wrap(lerr.CodeGenericError, err, "malformed state file at "+path)
	}
	st = Defaults()
	if len(raw.Projects) > 0 {
		st.Projects = raw.Projects
	} else if len(raw.Paths) > 0 {
		// v1 migration: keep AgentDirs nil so callers know this entry
		// hasn't been reconciled with central yet.
		for name, p := range raw.Paths {
			st.Projects[name] = ProjectEntry{Path: p}
		}
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
	filtered := entry.AgentDirs[:0]
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

// SetAgentDirs replaces a project's tracked agent dirs wholesale.
// Intended for the v1->v2 backfill path: once project-level code learns
// from central what dirs a migrated entry should reference, it calls
// SetAgentDirs so the next Save persists the v2 shape. No-op when the
// project isn't tracked.
func (s *State) SetAgentDirs(name string, dirs []string) {
	if s.Projects == nil {
		return
	}
	entry, ok := s.Projects[name]
	if !ok {
		return
	}
	entry.AgentDirs = append([]string(nil), dirs...)
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
