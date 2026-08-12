// Package config loads, saves, and validates lore's TOML config file.
//
// On-disk paths are stored with `~/` so the file is portable across machines
// (e.g. synced via dotfiles). In-memory Config always holds absolute paths;
// expansion happens once at Load and Save handles contraction on the way out.
package config

import (
	_ "embed"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	lerr "github.com/nathanclairmonte/lore/internal/errors"
)

//go:embed default_config.toml
var defaultConfigTOML []byte

// DefaultConfigTOML returns the embedded commented template byte-for-byte.
// Used by callers (tests, future `lore reset` etc.) that need the raw file.
func DefaultConfigTOML() []byte {
	out := make([]byte, len(defaultConfigTOML))
	copy(out, defaultConfigTOML)
	return out
}

// Config is the typed representation of ~/.config/lore/config.toml.
type Config struct {
	CentralRepo   string       `toml:"central_repo"`
	ProjectsRoots []string     `toml:"projects_roots"`
	ProjectsDepth int          `toml:"projects_root_depth"`
	AgentDirs     []string     `toml:"agent_dirs"`
	Commit        CommitConfig `toml:"commit"`
}

// CommitConfig holds commit-related preferences.
type CommitConfig struct {
	Auto         bool   `toml:"auto"`
	AddTemplate  string `toml:"add_template"`
	RmTemplate   string `toml:"rm_template"`
	MvTemplate   string `toml:"mv_template"`
	SyncTemplate string `toml:"sync_template"`
}

// rawDefaults returns the defaults parsed from the embedded template,
// without path expansion. Used by Defaults() and by tests that need the
// template-derived view of defaults.
func rawDefaults() (Config, error) {
	var cfg Config
	if _, err := toml.Decode(string(defaultConfigTOML), &cfg); err != nil {
		// the embedded template is a compile-time asset; a parse failure
		// is a programmer error worth surfacing loudly.
		return Config{}, lerr.Wrap(lerr.CodeGenericError, err, "embedded default config is malformed")
	}
	return cfg, nil
}

// Defaults returns the in-memory Config with all defaults applied and path
// fields expanded to absolute paths.
func Defaults() Config {
	cfg, err := rawDefaults()
	if err != nil {
		// keep a panic here: any failure means the binary itself is broken.
		panic(err)
	}
	expandPaths(&cfg)
	return cfg
}

// expandPaths rewrites every path field on cfg in-place from `~/...` form
// to its absolute form. Failures are tolerated (path left as-is); paths in
// config files are user-controlled and we'd rather load best-effort than
// refuse on a weird HOME setup.
func expandPaths(cfg *Config) {
	if p, err := Expand(cfg.CentralRepo); err == nil {
		cfg.CentralRepo = p
	}
	for i, root := range cfg.ProjectsRoots {
		if p, err := Expand(root); err == nil {
			cfg.ProjectsRoots[i] = p
		}
	}
}

// contractPaths rewrites every path field on cfg in-place from absolute
// form back to `~/...` if under HOME.
func contractPaths(cfg *Config) {
	cfg.CentralRepo = Contract(cfg.CentralRepo)
	for i, root := range cfg.ProjectsRoots {
		cfg.ProjectsRoots[i] = Contract(root)
	}
}

// Load reads the config file, fills in any missing keys from defaults, and
// expands `~/` in path fields. If the file doesn't exist, returns Defaults()
// with exists=false (and a nil error).
func Load() (cfg Config, exists bool, err error) {
	path, err := Path()
	if err != nil {
		return Config{}, false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Defaults(), false, nil
		}
		return Config{}, false, lerr.Wrap(lerr.CodeGenericError, err, "cannot read config file")
	}

	// Start from defaults and decode on top so missing keys keep their
	// default values. BurntSushi/toml leaves struct fields untouched when
	// the corresponding TOML key is absent, so this gives us a clean
	// merge.
	cfg, defErr := rawDefaults()
	if defErr != nil {
		return Config{}, false, defErr
	}
	if _, err := toml.Decode(string(data), &cfg); err != nil {
		return Config{}, false, lerr.Wrap(lerr.CodeGenericError, err, "malformed config file at "+path)
	}
	expandPaths(&cfg)

	// reject obviously-broken configs at the boundary rather than letting
	// an empty central_repo flow downstream and surface as a confusing
	// "central path is empty" deep inside central.Initialize. only checked
	// on the file-present branch: Defaults() always has a non-empty value.
	if strings.TrimSpace(cfg.CentralRepo) == "" {
		return Config{}, false, lerr.Newf(
			lerr.CodeInconsistency,
			"config at %s has empty central_repo",
			path,
		).WithHint("set central_repo to a path, or remove the file to restore defaults")
	}
	return cfg, true, nil
}

// Save writes cfg to disk. Path fields are contracted back to `~/...` form.
// Comments from the embedded template are NOT preserved; this round-trip
// path is for programmatic config edits in later slices. Slice 1 uses
// WriteDefaults to lay down the commented template instead.
func Save(cfg Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return lerr.Wrap(lerr.CodeGenericError, err, "cannot create config directory")
	}

	// deep-copy so we don't mutate the caller's struct (slices share backing
	// arrays under a plain struct copy).
	out := cfg
	out.ProjectsRoots = append([]string(nil), cfg.ProjectsRoots...)
	out.AgentDirs = append([]string(nil), cfg.AgentDirs...)
	contractPaths(&out)

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return lerr.Wrap(lerr.CodeGenericError, err, "cannot open config file for writing")
	}
	defer func() { _ = f.Close() }()
	enc := toml.NewEncoder(f)
	if err := enc.Encode(out); err != nil {
		return lerr.Wrap(lerr.CodeGenericError, err, "cannot encode config")
	}
	return nil
}

// WriteDefaults writes the embedded commented template verbatim to the
// config file location. Returns CodeConflict if the file already exists; the
// caller is responsible for the higher-level "already configured" check.
//
// On any failure after the file is opened (write error, sync error), the
// partial file is removed so a retry isn't permanently blocked by the
// O_EXCL guard. ENOSPC mid-write is the motivating real-world case.
func WriteDefaults() error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return lerr.Wrap(lerr.CodeGenericError, err, "cannot create config directory")
	}
	// O_EXCL ensures we never clobber an existing file.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return lerr.Newf(lerr.CodeConflict, "config file already exists at %s", path)
		}
		return lerr.Wrap(lerr.CodeGenericError, err, "cannot create config file")
	}
	success := false
	defer func() {
		_ = f.Close()
		if !success {
			_ = os.Remove(path)
		}
	}()
	if _, err := f.Write(defaultConfigTOML); err != nil {
		return lerr.Wrap(lerr.CodeGenericError, err, "cannot write config file")
	}
	success = true
	return nil
}
