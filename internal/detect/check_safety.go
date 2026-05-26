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

// CheckSafety runs the three slice-3 refusals against root for every name
// in candidates. Each applicable check that fires produces one Violation;
// callers surface all violations together so the user sees the full
// picture rather than one fix-and-retry cycle per gate.
//
// Pure inspection: no mutation, no network. Safe to call before any side
// effects in project.Add. Only `git ls-files` is consulted, and only for
// candidates that exist as real directories (a non-existent or
// non-directory candidate is silently ignored - it's not lore's job to
// police things the user hasn't actually set up).
//
// Run as a pre-check before DetectAgentDir rather than after: DetectAgentDir
// intentionally skips symlinked candidates (the post-add steady-state is a
// symlink into central, so re-detecting them would re-onboard tracked
// projects). That skip would also hide the slice-3 symlink violation from
// us, leaving the user with a confusing "no agent directory found" when
// they have a pre-existing symlinked .claude. Iterating candidates here
// keeps the policy layer in one place and lets DetectAgentDir keep doing
// one thing.
func CheckSafety(root string, candidates []string) ([]Violation, error) {
	var violations []Violation
	for _, name := range candidates {
		path := filepath.Join(root, name)
		info, err := os.Lstat(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, lerr.Wrap(lerr.CodeGenericError, err,
				fmt.Sprintf("cannot stat %s", path))
		}

		if info.Mode()&os.ModeSymlink != 0 {
			target, readErr := os.Readlink(path)
			if readErr != nil {
				// Lstat succeeded but Readlink didn't - surface the message
				// without the target rather than failing the whole check.
				// The user still gets the actionable refusal.
				target = "(unreadable)"
			}
			violations = append(violations, Violation{
				Kind: ViolationAgentDirSymlink,
				Name: name,
				Message: fmt.Sprintf(
					"%s/ is already a symlink to %s. Resolve it manually "+
						"(replace with a real directory containing what you "+
						"want lore to manage) before retrying.",
					name, target),
			})
			// a candidate that's a symlink can't ALSO be tested for
			// tracked files or nested .git in a meaningful way: those
			// checks read through the symlink and would re-report the
			// resolved target. skip the further checks for this candidate.
			continue
		}
		if !info.IsDir() {
			// regular file at the candidate name - DetectAgentDir will
			// skip it too. nothing to refuse here.
			continue
		}

		// nested .git check. Stat (not Lstat) so a .git that's itself a
		// symlink into a real git repo still counts.
		if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
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
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, lerr.Wrap(lerr.CodeGenericError, err,
				fmt.Sprintf("cannot stat %s/.git", path))
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
	}
	return violations, nil
}
