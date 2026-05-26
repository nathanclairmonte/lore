// Package state loads and saves lore's per-machine local state file
// (~/.local/state/lore/state.toml). State is never committed; it's a path
// mapping (project name -> on-disk path) regeneratable by `lore link`.
package state

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	lerr "github.com/nathanclairmonte/lore/internal/errors"
)

// CurrentSchemaVersion is the schema_version value written by this lore
// build. Bump when the on-disk schema changes; Load is responsible for
// future migration logic.
const CurrentSchemaVersion = 1

const stateFileRelPath = "lore/state.toml"

// State is the typed representation of state.toml.
type State struct {
	SchemaVersion int               `toml:"schema_version"`
	Paths         map[string]string `toml:"paths"` // project name -> absolute path
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
		Paths:         map[string]string{},
	}
}

// Load reads the state file. If absent, returns Defaults() with
// exists=false (nil err).
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
	st = Defaults()
	if _, err := toml.Decode(string(data), &st); err != nil {
		return State{}, false, lerr.Wrap(lerr.CodeGenericError, err, "malformed state file at "+path)
	}
	if st.Paths == nil {
		st.Paths = map[string]string{}
	}
	return st, true, nil
}

// AddPath records project name -> absolute path on the in-memory state.
// Idempotent on same (name, path); rejects same name with a different
// path so callers don't silently clobber tracked locations.
//
// Does NOT persist; callers control when to Save so multi-step flows
// (lore add) can roll back without leaving a half-written state file.
func (s *State) AddPath(name, path string) error {
	if s.Paths == nil {
		s.Paths = map[string]string{}
	}
	if existing, ok := s.Paths[name]; ok {
		if existing == path {
			return nil
		}
		return lerr.Newf(lerr.CodeConflict,
			"project %q is already tracked at %s", name, existing)
	}
	s.Paths[name] = path
	return nil
}

// PathFor returns the absolute path recorded for name. Second return is
// false when the project isn't tracked.
func (s *State) PathFor(name string) (string, bool) {
	if s.Paths == nil {
		return "", false
	}
	p, ok := s.Paths[name]
	return p, ok
}

// RemovePath drops the mapping for name. No-op when not tracked.
//
// Does NOT persist; see AddPath for the rationale.
func (s *State) RemovePath(name string) {
	if s.Paths == nil {
		return
	}
	delete(s.Paths, name)
}

// Save writes the state file, creating its parent directory if needed.
func Save(s State) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return lerr.Wrap(lerr.CodeGenericError, err, "cannot create state directory")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return lerr.Wrap(lerr.CodeGenericError, err, "cannot open state file for writing")
	}
	defer func() { _ = f.Close() }()
	if err := toml.NewEncoder(f).Encode(s); err != nil {
		return lerr.Wrap(lerr.CodeGenericError, err, "cannot encode state")
	}
	return nil
}
