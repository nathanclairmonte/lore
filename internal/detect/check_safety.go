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

// CheckSafety runs the three slice-3 refusals against the single candidate
// at root that lore would actually manage: the first entry in candidates
// (priority order) that exists as either a directory or a symlink. Missing
// candidates and regular files are skipped, matching DetectAgentDir's
// precedence rules so the policy and detection layers agree on which
// candidate is in play.
//
// Returning violations only for the "intended" candidate avoids the false-
// positive shape where a clean .claude (which would be adopted) is refused
// because an unrelated .cursor (which lore would never touch) happens to
// have a nested .git. Lore polices what it manages, not what it ignores.
//
// A symlinked candidate produces exactly one violation (AgentDirSymlink)
// and the dir-only checks are skipped: those checks read through the
// symlink and would re-report against the resolved target rather than the
// candidate name the user controls. A directory candidate can produce up
// to two violations (tracked + nested .git) so the user fixes both in one
// round-trip.
//
// Pure inspection: no mutation, no network. Safe to call before any side
// effects in project.Add. Only `git ls-files` is consulted, and only when
// a directory candidate has been selected.
//
// Run as a pre-check before DetectAgentDir rather than after: DetectAgentDir
// intentionally skips symlinked candidates (the post-add steady-state is a
// symlink into central, so re-detecting them would re-onboard tracked
// projects). That skip would hide the slice-3 symlink violation from any
// post-detect check, leaving the user with a confusing "no agent directory
// found" when they have a pre-existing symlinked .claude.
func CheckSafety(root string, candidates []string) ([]Violation, error) {
	name, path, isSymlink, ok, err := firstManagedCandidate(root, candidates)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}

	if isSymlink {
		target, readErr := os.Readlink(path)
		if readErr != nil {
			// Lstat succeeded but Readlink didn't - surface the message
			// without the target rather than failing the whole check.
			// the user still gets the actionable refusal.
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

	var violations []Violation

	// nested .git check. Stat (not Lstat) so a .git that's itself a
	// symlink into a real git repo still counts. A non-NotExist stat
	// error (permission denied, etc.) is deliberately swallowed here:
	// it shouldn't poison the whole check -- the tracked-file probe
	// below still runs, mirroring the Readlink fallback above, so the
	// user gets every signal we CAN produce.
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

// firstManagedCandidate walks candidates in priority order and returns the
// first one that exists as a directory or a symlink (the candidate
// DetectAgentDir would consider, plus symlinks for safety reporting).
// Regular files and missing entries are skipped. ok=false means no
// candidate is relevant: safety has nothing to check, and DetectAgentDir
// will surface "no agent dir found" downstream.
func firstManagedCandidate(root string, candidates []string) (name, path string, isSymlink, ok bool, err error) {
	for _, n := range candidates {
		p := filepath.Join(root, n)
		info, statErr := os.Lstat(p)
		if statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			return "", "", false, false, lerr.Wrap(lerr.CodeGenericError, statErr,
				fmt.Sprintf("cannot stat %s", p))
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return n, p, true, true, nil
		}
		if info.IsDir() {
			return n, p, false, true, nil
		}
		// regular file at this name: DetectAgentDir would skip it and
		// move on, so we do the same.
	}
	return "", "", false, false, nil
}
