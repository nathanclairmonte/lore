package detect

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/git"
)

// ViolationKind names one of the slice-3 safety refusals. Stable string
// values so callers (notably project.Add's error builder and any future
// `lore doctor` check) can branch on a specific kind without depending on
// the user-facing message wording.
type ViolationKind string

const (
	ViolationTrackedFiles    ViolationKind = "tracked-files"
	ViolationNestedGit       ViolationKind = "nested-git"
	ViolationAgentDirSymlink ViolationKind = "agent-dir-symlink"
)

// Violation describes one safety-gate failure. Message is the full
// user-facing line including the recovery instruction, ready to be
// surfaced verbatim by project.Add.
type Violation struct {
	Kind    ViolationKind
	Name    string // candidate name that tripped the check (e.g. ".claude")
	Message string
}

// CheckSafetyAll runs the slice-3 refusals against every name in names
// independently. Returns a map keyed by name; entries are present only
// when violations fired (an absent key means that candidate is clean OR
// doesn't exist as a directory/symlink at root).
//
// Each candidate is policed in isolation so multi-dir onboarding can
// skip just the offending dir and proceed with the rest, matching the
// slice-5 acceptance criterion. The slice-3 "polices what it manages"
// principle survives in the orchestrator's selection logic: only names
// the orchestrator intends to onboard are passed in.
//
// A symlinked candidate produces exactly one violation (AgentDirSymlink)
// and the dir-only checks are skipped: those checks read through the
// symlink and would report against the resolved target rather than the
// candidate name the user controls. A directory candidate can produce
// up to two violations (tracked + nested .git) so the user fixes both
// in one round-trip.
//
// Pure inspection: no mutation, no network. Only `git ls-files` is
// consulted, and only for directory candidates.
func CheckSafetyAll(root string, names []string) (map[string][]Violation, error) {
	out := map[string][]Violation{}
	for _, name := range names {
		vs, err := checkOne(root, name)
		if err != nil {
			return nil, err
		}
		if len(vs) > 0 {
			out[name] = vs
		}
	}
	return out, nil
}

// checkOne returns the violations for a single candidate name at root.
// Missing entries and regular files yield no violations (and no error):
// the orchestrator wouldn't onboard them anyway, and `--dir` validation
// upstream rejects user-supplied names that don't exist at all.
func checkOne(root, name string) ([]Violation, error) {
	path := filepath.Join(root, name)
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, lerr.Wrap(lerr.CodeGenericError, err,
			fmt.Sprintf("cannot stat %s", path))
	}

	if info.Mode()&os.ModeSymlink != 0 {
		target, readErr := os.Readlink(path)
		if readErr != nil {
			// Lstat succeeded but Readlink didn't - surface the message
			// without the target rather than failing the whole check.
			target = "(unreadable)"
		}
		return []Violation{{
			Kind: ViolationAgentDirSymlink,
			Name: name,
			Message: fmt.Sprintf(
				"%s/ is already a symlink to %s. Resolve it manually "+
					"(replace with a real directory containing what you "+
					"want lore to manage) before retrying.",
				name, target),
		}}, nil
	}

	if !info.IsDir() {
		// regular file at this candidate name: nothing to police.
		return nil, nil
	}

	var violations []Violation

	// nested .git check. Stat (not Lstat) so a .git that's itself a
	// symlink into a real git repo still counts. A non-NotExist stat
	// error (permission denied, etc.) is deliberately swallowed here:
	// it shouldn't poison the whole check -- the tracked-file probe
	// below still runs so the user gets every signal we CAN produce.
	if _, statErr := os.Stat(filepath.Join(path, ".git")); statErr == nil {
		violations = append(violations, Violation{
			Kind: ViolationNestedGit,
			Name: name,
			Message: fmt.Sprintf(
				"%s/ contains a nested git repository. Either remove "+
					"the nested .git first (after backing up its history "+
					"if you want to keep it) and retry, or skip lore for "+
					"this project.",
				name),
		})
	}

	// tracked-file check. ListTrackedUnder folds the not-a-git-repo
	// case into an empty result; missing-binary surfaces as
	// CodeSetupMissing, which we bubble up rather than coerce to a
	// safety violation (setup-missing is a different class of failure).
	tracked, err := git.ListTrackedUnder(root, name)
	if err != nil {
		return nil, err
	}
	if len(tracked) > 0 {
		violations = append(violations, Violation{
			Kind: ViolationTrackedFiles,
			Name: name,
			Message: fmt.Sprintf(
				"%s/ contains files tracked by this repo's git. lore "+
					"can't move tracked content into central without "+
					"rewriting your project's git history. Either "+
					"untrack them first (`git rm --cached -r %s && git "+
					"commit`) or skip lore for this project.",
				name, name),
		})
	}

	return violations, nil
}
