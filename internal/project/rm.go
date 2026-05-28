package project

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/nathanclairmonte/lore/internal/central"
	"github.com/nathanclairmonte/lore/internal/config"
	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/state"
)

// RmPlan is what PreflightRm produces and Remove consumes. The exported
// fields are what cmd/lore/rm.go reads to render the confirmation
// prompt; the unexported fields carry the loaded environment so Remove
// doesn't have to re-load config / state / central. cmd interacts with
// the plan only through its exported surface.
//
// If RefusalErr is non-nil, Remove will refuse with the same error.
// Per-dir refusal reasons live in Dirs entries with status
// UnlinkStatusRefused so the prompt can show every problematic dir at
// once.
type RmPlan struct {
	ProjectName  string
	ProjectPath  string // empty when the project isn't tracked locally
	CentralRepo  string
	Dirs         []UnlinkDirResult
	CommitMsg    string
	RecoveryCmd  string // materialised recovery command
	RefusalErr   error  // joined per-dir refusals, nil when none
	NotInCentral bool   // project missing from central too: cmd layer treats as not-found

	cfg       config.Config
	st        state.State
	origState state.State
	central   *central.Central
}

// RmResult is the structured outcome of a Remove. cmd/lore/rm.go
// converts it to the output package's render type.
type RmResult struct {
	ProjectName   string
	ProjectPath   string
	CentralRepo   string
	CommitMessage string
	Dirs          []UnlinkDirResult
	RecoveryCmd   string
}

// PreflightRm runs the read-only portion of Remove: loads the
// environment, validates the project exists, classifies every tracked
// dir's drift state, and computes the materialised commit + recovery
// commands. The returned RmPlan carries everything Remove needs, so
// Remove never re-loads.
//
// Does not mutate state or disk. When a drift gate would refuse,
// RefusalErr is populated and per-dir verdicts land in Dirs.
func PreflightRm(name string) (RmPlan, error) {
	cfg, _, err := config.Load()
	if err != nil {
		return RmPlan{}, err
	}

	st, _, err := state.Load()
	if err != nil {
		return RmPlan{}, err
	}

	c, err := central.Open(cfg.CentralRepo)
	if err != nil {
		if lerr.CodeOf(err) == lerr.CodeNotFound {
			return RmPlan{}, lerr.Newf(lerr.CodeSetupMissing,
				"central repo not found at %s", config.Contract(cfg.CentralRepo)).
				WithHint("run `lore init` first")
		}
		return RmPlan{}, err
	}

	// snapshot pre-mutation state so Remove's post-Save rollback can
	// restore it. State.Projects is a map (reference type), so a
	// shallow State copy isn't enough; copy the entries.
	origState := state.State{
		SchemaVersion: st.SchemaVersion,
		Projects:      make(map[string]state.ProjectEntry, len(st.Projects)),
	}
	maps.Copy(origState.Projects, st.Projects)

	plan := RmPlan{
		ProjectName: name,
		CentralRepo: c.Path,
		CommitMsg:   renderRmTemplate(cfg.Commit.RmTemplate, name),
		RecoveryCmd: rmRecoveryCommand(c.Path, name),

		cfg:       cfg,
		st:        st,
		origState: origState,
		central:   c,
	}

	entry, tracked := st.ProjectEntryFor(name)
	if tracked {
		plan.ProjectPath = entry.Path
	}

	if !tracked {
		// not in state and not in central → surface a clean not-found
		// before letting RemoveProjectStaged fail later.
		if _, statErr := os.Stat(c.ProjectPath(name, "")); statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				plan.NotInCentral = true
				return plan, nil
			}
			return RmPlan{}, lerr.Wrap(lerr.CodeGenericError, statErr,
				fmt.Sprintf("cannot stat %s", c.ProjectPath(name, "")))
		}
		// in central but not in state: nothing to unlink, just show the
		// central removal in the prompt.
		return plan, nil
	}

	dirs, refusal := previewUnlinkDirs(c, entry.Path, name, entry.AgentDirs)
	plan.Dirs = dirs
	plan.RefusalErr = refusal
	return plan, nil
}

// Remove stops tracking the project named in plan. Calls unlinkProject
// to clean up symlinks and scrub local state in memory, then `git rm
// -r projects/<name>/` in central, commits using plan.CommitMsg, and
// saves state. The commit is the success boundary; any failure before
// it unwinds every side effect via LIFO rollbacks.
//
// Confirmation lives in the cmd layer: this orchestrator runs the
// destructive sequence on a plan it trusts. cmd/lore/rm.go calls
// PreflightRm, renders the prompt, gets a y/N, then calls Remove.
func Remove(plan RmPlan) (RmResult, error) {
	// destructure the plan's execution environment. plan.st gets
	// mutated by unlinkProject; plan.origState is the snapshot used
	// by the post-Save rollback to restore disk state.
	cfg, st, c, origState := plan.cfg, plan.st, plan.central, plan.origState
	name := plan.ProjectName

	success := false
	var rollbacks []func()
	defer func() {
		if success {
			return
		}
		for i := len(rollbacks) - 1; i >= 0; i-- {
			rollbacks[i]()
		}
	}()

	helperRollbacks, unlinkRes, err := unlinkProject(&st, c, name)
	rollbacks = append(rollbacks, helperRollbacks...)
	if err != nil {
		return RmResult{}, err
	}

	undo, err := c.RemoveProjectStaged(name)
	if err != nil {
		return RmResult{}, err
	}
	rollbacks = append(rollbacks, func() {
		if undoErr := undo(); undoErr != nil {
			fmt.Fprintf(os.Stderr, "warning: %v\n", undoErr)
		}
	})

	// persist the state mutation unlinkProject made in-memory. the
	// rollback writes origState (the pre-mutation snapshot), not the
	// current st: at rollback time st still holds the mutated map
	// because LIFO drain runs this rollback before unlinkProject's
	// in-memory restore closure fires.
	if err := state.Save(st); err != nil {
		return RmResult{}, err
	}
	rollbacks = append(rollbacks, func() {
		if saveErr := state.Save(origState); saveErr != nil {
			fmt.Fprintf(os.Stderr, "warning: cannot revert state file: %v\n", saveErr)
		}
	})

	// success boundary. on success no rollback fires; on failure every
	// step above unwinds in reverse.
	commitMessage := plan.CommitMsg
	if commitMessage == "" {
		commitMessage = renderRmTemplate(cfg.Commit.RmTemplate, name)
	}
	if err := c.Commit(commitMessage); err != nil {
		return RmResult{}, err
	}
	success = true

	return RmResult{
		ProjectName:   name,
		ProjectPath:   unlinkRes.ProjectPath,
		CentralRepo:   c.Path,
		CommitMessage: commitMessage,
		Dirs:          unlinkRes.Dirs,
		RecoveryCmd:   plan.RecoveryCmd,
	}, nil
}

// renderRmTemplate expands {project} in commit.rm_template. The empty-
// template case is handled by config defaulting (the embedded default
// config supplies "chore: remove {project}"); the renderer assumes a
// non-empty template.
func renderRmTemplate(tmpl, projectName string) string {
	return strings.ReplaceAll(tmpl, "{project}", projectName)
}

// rmRecoveryCommand returns the materialised recovery command the user
// can run to restore a removed project. Includes the cd into the
// central repo so the snippet is copy-pasteable from the prompt or the
// success output without additional context.
func rmRecoveryCommand(centralPath, projectName string) string {
	return fmt.Sprintf("cd %s && git checkout HEAD~1 -- projects/%s/",
		config.Contract(centralPath), projectName)
}

// previewUnlinkDirs runs the same drift classification as the
// unlinkProject helper but without mutating state or removing anything.
// Used by PreflightRm to build the per-dir verdict list shown in the
// confirmation prompt. Aggregates every refusal so the prompt can show
// every problematic dir in one pass.
//
// Shares classifyAgentDirDrift with unlinkProject so the prompt's
// verdict can't drift from what the actual run would do.
func previewUnlinkDirs(c *central.Central, projectPath, name string, dirs []string) ([]UnlinkDirResult, error) {
	out := make([]UnlinkDirResult, 0, len(dirs))
	var refusals []error
	for _, dirName := range dirs {
		linkPath := filepath.Join(projectPath, dirName)
		expected := c.ProjectPath(name, dirName)
		status, classifyErr := classifyAgentDirDrift(linkPath, expected)
		if classifyErr != nil {
			out = append(out, UnlinkDirResult{
				Name:     dirName,
				Status:   UnlinkStatusRefused,
				LinkPath: linkPath,
				Err:      classifyErr,
			})
			refusals = append(refusals, classifyErr)
			continue
		}
		out = append(out, UnlinkDirResult{
			Name:     dirName,
			Status:   status,
			LinkPath: linkPath,
		})
	}
	if len(refusals) > 0 {
		return out, errors.Join(refusals...)
	}
	return out, nil
}
