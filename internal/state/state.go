// Package state loads and saves lore's per-machine local state file
// (~/.local/state/lore/state.toml). State is never committed; it's a path
// mapping (project name -> on-disk path) regeneratable by `lore link`.
//
// Slice 1 ships the typed module + Load/Save only. Query methods (AddPath,
// PathFor) and the multi-path schema land in later slices that need them.
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
