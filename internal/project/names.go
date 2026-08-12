package project

import (
	"regexp"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
)

// nameSlug pins the legal shape for a project name. The name becomes a
// directory inside central and a key in local state; restricting it to a
// conservative slug avoids
// shell-escaping, JSON-edge-case, and cross-platform path surprises.
// Leading dot is excluded so central/projects/<name>/ never looks like a
// hidden directory; leading dash is excluded so the name can't be
// mistaken for a CLI flag.
var nameSlug = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidateProjectName checks name against the strict-slug rule. source
// describes where the name came from (e.g. "--name" or "project path
// basename") so the returned error names a fix the user can act on.
func ValidateProjectName(name, source string) error {
	if name == "" {
		return lerr.Newf(lerr.CodeGenericError,
			"%s produced an empty project name", source).
			WithHint("pass --name <slug> with a non-empty value")
	}
	if !nameSlug.MatchString(name) {
		return lerr.Newf(lerr.CodeGenericError,
			"%s %q is not a valid project name (allowed: letters, digits, dots, dashes; must start with letter or digit)",
			source, name).
			WithHint("pass --name <slug> with a valid name")
	}
	return nil
}
