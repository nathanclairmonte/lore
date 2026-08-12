// Package state loads and saves lore's per-machine local state file.
package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	lerr "github.com/nathanclairmonte/lore/internal/errors"
)

// CurrentSchemaVersion is the schema_version value written by this build.
const CurrentSchemaVersion = 3

const stateFileRelPath = "lore/state.toml"
const syncErrorFileName = "last-sync-error.txt"

// CheckoutEntry records one project copy on this machine.
type CheckoutEntry struct {
	Path     string `toml:"path"`
	SyncBase string `toml:"sync_base"`
}

// ProjectEntry records shared project identity and per-checkout state.
type ProjectEntry struct {
	AgentDirs []string        `toml:"agent_dirs"`
	Checkouts []CheckoutEntry `toml:"checkouts"`
}

// State is the typed representation of state.toml.
type State struct {
	SchemaVersion int                     `toml:"schema_version"`
	Projects      map[string]ProjectEntry `toml:"projects"`
}

type validationIssueKind uint8

const (
	missingAgentDirs validationIssueKind = iota
	missingCheckouts
	invalidCheckout
	duplicateCheckout
)

type validationIssue struct {
	kind     validationIssueKind
	project  string
	other    string
	checkout CheckoutEntry
	err      error
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

// SyncErrorPath returns the deferred automatic-sync error record path.
func SyncErrorPath() (string, error) {
	path, err := Path()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(path), syncErrorFileName), nil
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
// to remove the stale file and rebuild current state with pull.
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
	// decode into a zero-value State so a file missing
	// schema_version doesn't silently inherit CurrentSchemaVersion and
	// slip past the version check below.
	if _, err := toml.Decode(string(data), &st); err != nil {
		return State{}, false, lerr.Wrap(lerr.CodeGenericError, err, "malformed state file at "+path)
	}
	if st.SchemaVersion != CurrentSchemaVersion {
		return State{}, false, lerr.Newf(lerr.CodeInconsistency,
			"state file at %s is schema version %d, but this lore expects %d",
			path, st.SchemaVersion, CurrentSchemaVersion).
			WithHint("delete the stale state file, replace any legacy top-level links with real directories, then run `lore pull --all`")
	}
	// enforce invariants at the trust boundary so invalid state fails here.
	if issue := validateProjects(st.Projects); issue != nil {
		return State{}, false, issue.loadError(path)
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

// PathFor returns the sole checkout path for name.
func (s *State) PathFor(name string) (string, bool) {
	e, ok := s.ProjectEntryFor(name)
	if !ok || len(e.Checkouts) != 1 {
		return "", false
	}
	return e.Checkouts[0].Path, true
}

// CheckoutForPath returns the checkout recorded at projectPath.
func (s *State) CheckoutForPath(name, projectPath string) (CheckoutEntry, bool) {
	entry, ok := s.ProjectEntryFor(name)
	if !ok {
		return CheckoutEntry{}, false
	}
	for _, checkout := range entry.Checkouts {
		if checkout.Path == projectPath {
			return checkout, true
		}
	}
	return CheckoutEntry{}, false
}

// CheckoutOwner returns the project that owns an exact checkout path.
func (s *State) CheckoutOwner(projectPath string) (string, bool) {
	for name, entry := range s.Projects {
		for _, checkout := range entry.Checkouts {
			if checkout.Path == projectPath {
				return name, true
			}
		}
	}
	return "", false
}

// Clone returns a copy of s safe to mutate independently. Projects is a map
// (reference type), so a plain struct copy would alias it; orchestrators
// that snapshot pre-mutation state for a rollback (lore rm / mv) need this.
func (s *State) Clone() State {
	out := State{
		SchemaVersion: s.SchemaVersion,
		Projects:      make(map[string]ProjectEntry, len(s.Projects)),
	}
	for name, entry := range s.Projects {
		entry.AgentDirs = slices.Clone(entry.AgentDirs)
		entry.Checkouts = slices.Clone(entry.Checkouts)
		out.Projects[name] = entry
	}
	return out
}

// AddAgentDir records an agent directory in the project identity.
func (s *State) AddAgentDir(name, dirName string) error {
	if name == "" {
		return lerr.New(lerr.CodeGenericError, "project name cannot be empty")
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
			AgentDirs: []string{dirName},
		}
		return nil
	}
	if slices.Contains(existing.AgentDirs, dirName) {
		return nil
	}
	existing.AgentDirs = append(existing.AgentDirs, dirName)
	s.Projects[name] = existing
	return nil
}

// RecordCheckout records or updates one checkout and its sync-base.
func (s *State) RecordCheckout(name, projectPath, syncBase string) error {
	if name == "" {
		return lerr.New(lerr.CodeGenericError, "project name cannot be empty")
	}
	checkout := CheckoutEntry{Path: projectPath, SyncBase: syncBase}
	if err := validateCheckout(checkout); err != nil {
		return lerr.New(lerr.CodeGenericError, err.Error())
	}
	entry, ok := s.Projects[name]
	if !ok || len(entry.AgentDirs) == 0 {
		return lerr.Newf(lerr.CodeInconsistency,
			"project %q has no agent directories recorded", name)
	}
	if owner, exists := s.CheckoutOwner(projectPath); exists && owner != name {
		return lerr.Newf(lerr.CodeConflict,
			"checkout %s is already recorded for project %q", projectPath, owner)
	}
	for i, existing := range entry.Checkouts {
		if existing.Path == projectPath {
			entry.Checkouts[i] = checkout
			s.Projects[name] = entry
			return nil
		}
	}
	entry.Checkouts = append(entry.Checkouts, checkout)
	s.Projects[name] = entry
	return nil
}

// RemoveCheckout drops one checkout from a project.
func (s *State) RemoveCheckout(name, projectPath string) {
	entry, ok := s.Projects[name]
	if !ok {
		return
	}
	entry.Checkouts = slices.DeleteFunc(entry.Checkouts, func(c CheckoutEntry) bool {
		return c.Path == projectPath
	})
	if len(entry.Checkouts) == 0 {
		delete(s.Projects, name)
		return
	}
	s.Projects[name] = entry
}

// RenameProject moves a project entry without changing its checkout data.
func (s *State) RenameProject(oldName, newName string) error {
	entry, ok := s.Projects[oldName]
	if !ok {
		return lerr.Newf(lerr.CodeNotFound, "project %q is not recorded in local state", oldName)
	}
	if _, exists := s.Projects[newName]; exists {
		return lerr.Newf(lerr.CodeConflict, "project %q is already recorded in local state", newName)
	}
	delete(s.Projects, oldName)
	s.Projects[newName] = entry
	return nil
}

// RemoveProject drops every checkout record for a project.
func (s *State) RemoveProject(name string) {
	delete(s.Projects, name)
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

func validateCheckout(checkout CheckoutEntry) error {
	if checkout.Path == "" {
		return errors.New("checkout path is empty")
	}
	if !filepath.IsAbs(checkout.Path) {
		return fmt.Errorf("checkout path must be absolute, got %q", checkout.Path)
	}
	if len(checkout.SyncBase) != 40 || strings.IndexFunc(checkout.SyncBase, func(r rune) bool {
		return (r < '0' || r > '9') && (r < 'a' || r > 'f')
	}) != -1 {
		return fmt.Errorf("sync-base must be a 40-character lowercase Git SHA")
	}
	return nil
}

func validateProjects(projects map[string]ProjectEntry) *validationIssue {
	seen := map[string]string{}
	names := make([]string, 0, len(projects))
	for name := range projects {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		entry := projects[name]
		if len(entry.AgentDirs) == 0 {
			return &validationIssue{kind: missingAgentDirs, project: name}
		}
		if len(entry.Checkouts) == 0 {
			return &validationIssue{kind: missingCheckouts, project: name}
		}
		for _, checkout := range entry.Checkouts {
			if err := validateCheckout(checkout); err != nil {
				return &validationIssue{
					kind: invalidCheckout, project: name, checkout: checkout, err: err,
				}
			}
			if other, ok := seen[checkout.Path]; ok {
				return &validationIssue{
					kind: duplicateCheckout, project: name, other: other, checkout: checkout,
				}
			}
			seen[checkout.Path] = name
		}
	}
	return nil
}

func (issue *validationIssue) loadError(path string) error {
	switch issue.kind {
	case missingAgentDirs:
		return lerr.Newf(lerr.CodeInconsistency,
			"state file at %s has no agent_dirs for project %q", path, issue.project).
			WithHint("delete the file and run `lore pull --all`")
	case missingCheckouts:
		return lerr.Newf(lerr.CodeInconsistency,
			"state file at %s has no checkouts for project %q", path, issue.project).
			WithHint("delete the file and run `lore pull --all`")
	case invalidCheckout:
		return lerr.Newf(lerr.CodeInconsistency,
			"state file at %s has an invalid checkout for project %q: %s",
			path, issue.project, issue.err).
			WithHint("delete the file and run `lore pull --all`")
	case duplicateCheckout:
		if issue.other == issue.project {
			return lerr.Newf(lerr.CodeInconsistency,
				"state file at %s records checkout %s more than once for project %q",
				path, issue.checkout.Path, issue.project).
				WithHint("delete the duplicate checkout and retry")
		}
		return lerr.Newf(lerr.CodeInconsistency,
			"state file at %s records checkout %s for both %q and %q",
			path, issue.checkout.Path, issue.other, issue.project).
			WithHint("delete the duplicate checkout and retry")
	default:
		panic("unknown state validation issue")
	}
}

func (issue *validationIssue) saveError() error {
	switch issue.kind {
	case missingAgentDirs, missingCheckouts:
		return lerr.Newf(lerr.CodeInconsistency,
			"cannot save incomplete state for project %q", issue.project)
	case invalidCheckout:
		return lerr.Newf(lerr.CodeInconsistency,
			"cannot save invalid checkout for project %q: %s", issue.project, issue.err)
	case duplicateCheckout:
		if issue.other == issue.project {
			return lerr.Newf(lerr.CodeInconsistency,
				"cannot save duplicate checkout %s for project %q",
				issue.checkout.Path, issue.project)
		}
		return lerr.Newf(lerr.CodeInconsistency,
			"cannot save checkout %s for both %q and %q",
			issue.checkout.Path, issue.other, issue.project)
	default:
		panic("unknown state validation issue")
	}
}

// Save writes the state file atomically, creating its parent directory
// if needed. Writes to a sibling tempfile then renames into place so a
// crash or encode failure can never leave a truncated state.toml on
// disk. lore add's rollback path leans on this: if Save errors out the
// previous state file is still intact.
func Save(s State) error {
	if s.SchemaVersion != CurrentSchemaVersion {
		return lerr.Newf(lerr.CodeInconsistency,
			"cannot save state schema version %d with lore schema version %d",
			s.SchemaVersion, CurrentSchemaVersion)
	}
	if issue := validateProjects(s.Projects); issue != nil {
		return issue.saveError()
	}
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
