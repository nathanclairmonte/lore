// Package project orchestrates the multi-step use cases behind lore's
// project subcommands (add, path, and in later slices rm/mv/unlink/link).
// It composes the deep modules - config, state, central, git, detect,
// link - so the cmd/lore/* files stay thin Cobra adapters.
//
// Slice 2 ships Add, Path, PathRepo. Slices 3-6 extend these and add new
// orchestrators alongside.
package project

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nathanclairmonte/lore/internal/central"
	"github.com/nathanclairmonte/lore/internal/config"
	"github.com/nathanclairmonte/lore/internal/detect"
	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/git"
	"github.com/nathanclairmonte/lore/internal/link"
	"github.com/nathanclairmonte/lore/internal/state"
)

// AddResult is the structured outcome of an Add. cmd/lore/add.go converts
// it to the output package's render type. Kept here as the orchestrator's
// return value so the cmd/output dependency stays one-way.
type AddResult struct {
	ProjectName    string
	ProjectPath    string // resolved, absolute
	AgentDirName   string
	CentralPath    string // absolute path inside the central repo
	CentralRepo    string // central repo root (for the Next hint)
	CommitMessage  string
	AlreadyTracked bool // re-run on a clean tracked project; renderer picks a different message
}

// Add onboards a project: detects an agent dir, refuses if not globally
// gitignored, moves the dir into the central repo, symlinks it back,
// records the path in state, and commits with commit.add_template.
//
// Idempotent: re-running on a project where state and the on-disk
// symlink agree returns AddResult{AlreadyTracked: true} with nil error.
// Any mismatch between state and symlink surfaces as CodeConflict;
// silently "succeeding" on inconsistent state would mask drift.
//
// Failures partway through fully roll back: source dir restored, no
// symlink left behind, no state entry, no commit on central.
func Add(opts AddOptions) (AddResult, error) {
	projectPath, err := resolveProjectPath(opts.ProjectPath)
	if err != nil {
		return AddResult{}, err
	}

	cfg, _, err := config.Load()
	if err != nil {
		return AddResult{}, err
	}

	st, _, err := state.Load()
	if err != nil {
		return AddResult{}, err
	}

	c, err := central.Open(cfg.CentralRepo)
	if err != nil {
		// surface central-not-set-up as setup-missing so the user sees
		// the right exit code class.
		if lerr.CodeOf(err) == lerr.CodeNotFound {
			return AddResult{}, lerr.Newf(lerr.CodeSetupMissing,
				"central repo not found at %s", config.Contract(cfg.CentralRepo)).
				WithHint("run `lore init` first")
		}
		return AddResult{}, err
	}

	name := filepath.Base(projectPath)

	// already-tracked detection BEFORE any side effects, and BEFORE the
	// fresh-detection step. detect.DetectAgentDir intentionally skips
	// symlinks (slice 3 safety), so it would miss a successfully-added
	// project whose agent dir is now a symlink into central. cross-check
	// state against central instead: ask central what dir it has for
	// this project, then validate the symlink at <projectPath>/<dirName>.
	if existing, tracked := st.PathFor(name); tracked {
		if existing != projectPath {
			return AddResult{}, lerr.Newf(lerr.CodeConflict,
				"project %q is already tracked at %s",
				name, config.Contract(existing))
		}
		dirName, dirErr := c.SingleAgentDirName(name)
		if dirErr != nil {
			return AddResult{}, lerr.Newf(lerr.CodeConflict,
				"state says %q is tracked but central has no agent dir for it", name).
				WithHint("remove the stale state entry and re-run, or run `lore doctor` once it ships")
		}
		expectedTarget := c.ProjectPath(name, dirName)
		linkPath := filepath.Join(projectPath, dirName)
		if validErr := link.Validate(linkPath, expectedTarget); validErr == nil {
			return AddResult{
				ProjectName:    name,
				ProjectPath:    projectPath,
				AgentDirName:   dirName,
				CentralPath:    expectedTarget,
				CentralRepo:    c.Path,
				AlreadyTracked: true,
			}, nil
		}
		return AddResult{}, lerr.Newf(lerr.CodeConflict,
			"state says %q is tracked but the symlink at %s does not point to %s",
			name, linkPath, expectedTarget).
			WithHint("remove the stale symlink and re-run, or run `lore doctor` once it ships")
	}

	// safety gate runs BEFORE DetectAgentDir. CheckSafety picks the
	// same candidate DetectAgentDir would pick (first existing dir or
	// symlink in priority order) and checks only that one - lore polices
	// what it would manage, not what it ignores. Running it before
	// DetectAgentDir lets the agent-dir-is-symlink case surface as a
	// proper refusal: DetectAgentDir intentionally skips symlinks (the
	// post-add steady-state is a symlink into central), so a stale
	// symlinked .claude would otherwise produce a confusing "no agent
	// dir found".
	violations, err := detect.CheckSafety(projectPath, cfg.AgentDirs)
	if err != nil {
		return AddResult{}, err
	}
	if len(violations) > 0 {
		return AddResult{}, buildSafetyError(violations)
	}

	agentDir, found, err := detect.DetectAgentDir(projectPath, cfg.AgentDirs)
	if err != nil {
		return AddResult{}, err
	}
	if !found {
		return AddResult{}, lerr.Newf(lerr.CodeNotFound,
			"no agent directory found at %s (looked for: %s)",
			config.Contract(projectPath), strings.Join(cfg.AgentDirs, ", "))
	}

	// gitignore precheck. slice 4 replaces this with the interactive
	// picker; slice 2 refuses with a forward-pointing message.
	ignored, err := git.CheckIgnore(projectPath, agentDir.Name)
	if err != nil {
		return AddResult{}, err
	}
	if !ignored {
		return AddResult{}, lerr.Newf(lerr.CodeRefusedSafety,
			"%s is not globally gitignored", agentDir.Name).
			WithHint("add it to your global .gitignore and re-run; an interactive picker is coming in a future release")
	}

	commitMessage := renderAddTemplate(cfg.Commit.AddTemplate, name)

	// success-gated unwind, same pattern as central.Initialize. each
	// step that lands a side effect registers a defer that reverts it
	// when !success. defers fire LIFO so unwind order is the reverse of
	// the order side effects happened in.
	destPath, commit, undo, err := c.AddProject(name, agentDir.Name, agentDir.Path)
	if err != nil {
		return AddResult{}, err
	}
	success := false
	defer func() {
		if success {
			return
		}
		if undoErr := undo(); undoErr != nil {
			fmt.Fprintf(os.Stderr, "warning: %v\n", undoErr)
		}
	}()

	if err := link.Create(destPath, agentDir.Path); err != nil {
		return AddResult{}, err
	}
	defer func() {
		if success {
			return
		}
		_ = link.Remove(agentDir.Path)
	}()

	if err := st.AddPath(name, projectPath); err != nil {
		// in-memory mutation only; st is a local that goes out of scope
		// when we return, so no cleanup defer needed yet.
		return AddResult{}, err
	}
	if err := state.Save(st); err != nil {
		return AddResult{}, err
	}
	defer func() {
		if success {
			return
		}
		st.RemovePath(name)
		if saveErr := state.Save(st); saveErr != nil {
			fmt.Fprintf(os.Stderr, "warning: cannot revert state file: %v\n", saveErr)
		}
	}()

	if err := commit(commitMessage); err != nil {
		return AddResult{}, err
	}
	success = true
	return AddResult{
		ProjectName:   name,
		ProjectPath:   projectPath,
		AgentDirName:  agentDir.Name,
		CentralPath:   destPath,
		CentralRepo:   c.Path,
		CommitMessage: commitMessage,
	}, nil
}

// Path returns the absolute on-disk path of the (single) agent directory
// tracked for name inside the central repo. Slice 5 will add a dirName
// parameter for the multi-dir case; slice 2 always returns the single
// tracked dir.
func Path(name string) (string, error) {
	st, _, err := state.Load()
	if err != nil {
		return "", err
	}
	if _, tracked := st.PathFor(name); !tracked {
		return "", lerr.Newf(lerr.CodeNotFound, "project %q is not tracked", name)
	}

	cfg, _, err := config.Load()
	if err != nil {
		return "", err
	}
	c, err := central.Open(cfg.CentralRepo)
	if err != nil {
		return "", err
	}
	dirName, err := c.SingleAgentDirName(name)
	if err != nil {
		return "", err
	}
	return c.ProjectPath(name, dirName), nil
}

// PathRepo returns the absolute path of the central repo.
func PathRepo() (string, error) {
	cfg, _, err := config.Load()
	if err != nil {
		return "", err
	}
	return cfg.CentralRepo, nil
}

// resolveProjectPath normalises opts.ProjectPath to an absolute,
// symlink-evaluated path. Empty input means cwd.
func resolveProjectPath(in string) (string, error) {
	raw := in
	if raw == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", lerr.Wrap(lerr.CodeGenericError, err, "cannot determine current working directory")
		}
		raw = cwd
	} else {
		expanded, err := config.Expand(raw)
		if err == nil {
			raw = expanded
		}
		if !filepath.IsAbs(raw) {
			abs, err := filepath.Abs(raw)
			if err != nil {
				return "", lerr.Wrap(lerr.CodeGenericError, err,
					fmt.Sprintf("cannot resolve %s", in))
			}
			raw = abs
		}
	}
	// EvalSymlinks doubles as an existence check; we want state to
	// store the canonical path so intermediate-symlink moves don't drift.
	resolved, err := filepath.EvalSymlinks(raw)
	if err != nil {
		if os.IsNotExist(err) {
			return "", lerr.Newf(lerr.CodeNotFound,
				"project path %s does not exist", config.Contract(raw))
		}
		return "", lerr.Wrap(lerr.CodeGenericError, err,
			fmt.Sprintf("cannot resolve symlinks in %s", raw))
	}
	return resolved, nil
}

// buildSafetyError collapses one or more safety violations into a single
// CodeRefusedSafety error. Each violation's full message becomes a bullet
// under a short header so the user sees every gate that fired in one go
// rather than fixing them one-at-a-time across multiple `lore add` retries.
// No hint is attached: each violation message already contains its own
// recovery instruction, and a synthetic top-level hint would either repeat
// one of them or paper over the others.
func buildSafetyError(vs []detect.Violation) error {
	var b strings.Builder
	b.WriteString("refused for safety:")
	for _, v := range vs {
		b.WriteString("\n  - ")
		b.WriteString(v.Message)
	}
	return lerr.New(lerr.CodeRefusedSafety, b.String())
}

// renderAddTemplate expands the {project} variable in commit.add_template.
// Intentionally a one-variable substituter for slice 2: other variables
// ({date}, {hostname}, {projects}, {old}, {new}) land with the slices
// that consume them. Replace with a generic helper if/when a second
// consumer arrives.
func renderAddTemplate(tmpl, projectName string) string {
	if tmpl == "" {
		tmpl = "feat: add {project}"
	}
	return strings.ReplaceAll(tmpl, "{project}", projectName)
}
