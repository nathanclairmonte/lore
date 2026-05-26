package config

import (
	"os"
	"path/filepath"
	"strings"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
)

// configFileRelPath is the path under XDG_CONFIG_HOME (or ~/.config).
const configFileRelPath = "lore/config.toml"

// Path returns the on-disk location of the config file. Honors
// XDG_CONFIG_HOME; falls back to ~/.config/lore/config.toml.
func Path() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, configFileRelPath), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", lerr.Wrap(lerr.CodeGenericError, err, "cannot determine home directory")
	}
	return filepath.Join(home, ".config", configFileRelPath), nil
}

// Expand resolves a leading `~/` to the user's home directory. A bare `~`
// also expands. Returns p unchanged if it doesn't start with `~`.
func Expand(p string) (string, error) {
	if p == "" || (p[0] != '~') {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", lerr.Wrap(lerr.CodeGenericError, err, "cannot determine home directory")
	}
	if p == "~" {
		return home, nil
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:]), nil
	}
	// `~user/...` form is not supported; leave it alone rather than guessing.
	return p, nil
}

// Contract is the inverse of Expand: if p is under HOME, rewrite the prefix
// back to `~`. Otherwise returns p unchanged. Best-effort: on any error
// determining HOME, returns p unchanged.
func Contract(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	homeWithSep := home + string(filepath.Separator)
	if strings.HasPrefix(p, homeWithSep) {
		return "~" + string(filepath.Separator) + p[len(homeWithSep):]
	}
	return p
}
