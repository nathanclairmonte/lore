package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nathanclairmonte/lore/internal/central"
	"github.com/nathanclairmonte/lore/internal/config"
	"github.com/nathanclairmonte/lore/internal/detect"
	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/git"
	"github.com/nathanclairmonte/lore/internal/output"
	"github.com/nathanclairmonte/lore/internal/reconcile"
	"github.com/nathanclairmonte/lore/internal/state"
)

var saveAddState = state.Save

// Add onboards a checkout with real project and central copies.
func Add(opts AddOptions) (AddResult, error) {
	projectPath, err := resolveProjectPath(opts.ProjectPath)
	if err != nil {
		return AddResult{}, err
	}
	ctx, err := openMutationContext()
	if err != nil {
		return AddResult{}, err
	}
	defer ctx.close()
	cfg := ctx.config
	st := ctx.state
	c := ctx.central
	if err := validateAgentDirNames(opts.Dirs, "--dir"); err != nil {
		return AddResult{}, err
	}
	if err := validateAgentDirNames(cfg.AgentDirs, "configured agent_dirs"); err != nil {
		return AddResult{}, err
	}
	projectName := opts.Name
	nameSource := "--name"
	if projectName == "" {
		projectName = filepath.Base(projectPath)
		nameSource = "project path basename"
	}
	if err := ValidateProjectName(projectName, nameSource); err != nil {
		return AddResult{}, err
	}
	if owner, exists := st.CheckoutOwner(projectPath); exists && owner != projectName {
		return AddResult{}, lerr.Newf(lerr.CodeConflict,
			"checkout %s is already recorded for project %q", config.Contract(projectPath), owner)
	}

	trackedEntry, projectTracked := st.ProjectEntryFor(projectName)
	if err := validateAgentDirNames(trackedEntry.AgentDirs, "recorded agent_dirs"); err != nil {
		return AddResult{}, err
	}
	trackedCheckout, checkoutTracked := st.CheckoutForPath(projectName, projectPath)
	centralExists, err := centralProjectDirExists(c, projectName)
	if err != nil {
		return AddResult{}, err
	}
	if projectTracked && !centralExists {
		return AddResult{}, lerr.Newf(lerr.CodeInconsistency,
			"project %q is recorded locally but is missing from central", projectName).
			WithHint("restore the central copy from Git history before retrying")
	}

	success := false
	commitLanded := false
	var rollbacks []func()
	defer func() {
		if success || commitLanded {
			return
		}
		for i := len(rollbacks) - 1; i >= 0; i-- {
			rollbacks[i]()
		}
	}()

	scaffoldedSet := map[string]bool{}
	if opts.Create {
		choices, err := scaffoldChoicesFromOptions(opts.Dirs, cfg.AgentDirs)
		if err != nil {
			return AddResult{}, err
		}
		for _, choice := range choices {
			created, err := scaffoldAgentDir(projectPath, choice.DirName, choice.FileName)
			if err != nil {
				return AddResult{}, err
			}
			if created {
				scaffoldedSet[choice.DirName] = true
				scaffoldedPath := filepath.Join(projectPath, choice.DirName)
				rollbacks = append(rollbacks, func() { _ = os.RemoveAll(scaffoldedPath) })
				output.Success("Scaffolded %s/ with empty %s", choice.DirName, choice.FileName)
			} else {
				output.Hint("%s already exists; skipping scaffold", choice.DirName)
			}
		}
	}

	detected, err := detect.DetectAgentDirs(projectPath, cfg.AgentDirs)
	if err != nil {
		return AddResult{}, err
	}
	centralDirs := []string(nil)
	if centralExists {
		centralDirs, err = c.ProjectAgentDirs(projectName)
		if err != nil {
			return AddResult{}, err
		}
	}
	knownDirs := trackedEntry.AgentDirs
	adoptingCheckout := centralExists && !checkoutTracked
	if adoptingCheckout {
		knownDirs = centralDirs
	}
	symlinkedNames, err := untrackedSymlinkCandidates(projectPath, cfg.AgentDirs, knownDirs)
	if err != nil {
		return AddResult{}, err
	}
	selectedNames, err := resolveSelection(opts.Dirs, detected, knownDirs, symlinkedNames)
	if err != nil {
		return AddResult{}, err
	}
	if adoptingCheckout {
		selectedNames = slices.Clone(centralDirs)
	}
	if len(selectedNames) == 0 {
		return AddResult{}, lerr.Newf(lerr.CodeNotFound,
			"no agent directory found at %s (looked for: %s)",
			config.Contract(projectPath), strings.Join(cfg.AgentDirs, ", ")).
			WithHint("pass --create to scaffold an empty agent dir, or create one manually and retry")
	}

	var alreadyTracked []string
	var toCheck []detect.AgentDir
	for _, name := range selectedNames {
		path := filepath.Join(projectPath, name)
		info, statErr := os.Lstat(path)
		if statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				if checkoutTracked && slices.Contains(trackedEntry.AgentDirs, name) {
					return AddResult{}, lerr.Newf(lerr.CodeInconsistency,
						"recorded project copy %s is missing", path).
						WithHint("run `lore pull` to restore the project copy")
				}
				return AddResult{}, missingDirError(name, projectPath, detected, knownDirs)
			}
			return AddResult{}, lerr.Wrap(lerr.CodeGenericError, statErr,
				fmt.Sprintf("cannot inspect %s", path))
		}
		if info.Mode()&os.ModeSymlink == 0 && !info.IsDir() {
			return AddResult{}, missingDirError(name, projectPath, detected, knownDirs)
		}
		if checkoutTracked && slices.Contains(trackedEntry.AgentDirs, name) {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return AddResult{}, lerr.Newf(lerr.CodeInconsistency,
					"recorded project copy %s is not a real directory", path).
					WithHint("run `lore pull` to restore the project copy")
			}
			centralPath := c.ProjectPath(projectName, name)
			centralInfo, centralErr := os.Stat(centralPath)
			if centralErr != nil || !centralInfo.IsDir() {
				return AddResult{}, lerr.Newf(lerr.CodeInconsistency,
					"recorded central copy %s is missing or invalid", centralPath)
			}
			alreadyTracked = append(alreadyTracked, name)
			continue
		}
		toCheck = append(toCheck, detect.AgentDir{Name: name, Path: path})
	}

	violationsByDir, err := detect.CheckSafetyAll(projectPath, namesOf(toCheck))
	if err != nil {
		return AddResult{}, err
	}
	var cleanCandidates []detect.AgentDir
	var skipped []AgentDirResult
	for _, candidate := range toCheck {
		if violations := violationsByDir[candidate.Name]; len(violations) > 0 {
			skipped = append(skipped, AgentDirResult{
				Name: candidate.Name, Status: StatusSkippedSafety, Violations: violations,
			})
			continue
		}
		cleanCandidates = append(cleanCandidates, candidate)
	}
	if adoptingCheckout && len(skipped) > 0 {
		result := AddResult{
			ProjectName: projectName, ProjectPath: projectPath, CentralRepo: c.Path,
			Dirs: skipped, ScaffoldRequested: opts.Create,
		}
		return result, buildSafetyError(skipped)
	}
	if err := ensureAddDirsIgnored(projectPath, cleanCandidates); err != nil {
		return AddResult{}, err
	}

	if adoptingCheckout {
		return adoptCheckout(st, c, projectName, projectPath, centralDirs, opts.Create)
	}

	var toOnboard []detect.AgentDir
	var recovered []AgentDirResult
	for _, candidate := range cleanCandidates {
		centralPath := c.ProjectPath(projectName, candidate.Name)
		if _, statErr := os.Stat(centralPath); statErr == nil {
			if err := requireCentralDirsAtHead(c, projectName, []string{candidate.Name}); err != nil {
				return AddResult{}, err
			}
			equal, err := reconcile.TreesEqual(candidate.Path, centralPath)
			if err != nil {
				return AddResult{}, err
			}
			if !equal {
				return AddResult{}, lerr.Newf(lerr.CodeConflict,
					"project copy for %q differs from central", projectName).
					WithHint("run `lore pull` to choose how to reconcile the copies")
			}
			recovered = append(recovered, AgentDirResult{
				Name: candidate.Name, Status: StatusAdopted, CentralPath: centralPath,
			})
			continue
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return AddResult{}, lerr.Wrap(lerr.CodeGenericError, statErr,
				fmt.Sprintf("cannot inspect %s", centralPath))
		}
		toOnboard = append(toOnboard, candidate)
	}

	if (len(toOnboard) > 0 || len(recovered) > 0) && checkoutTracked {
		if err := requireCentralDirsAtHead(c, projectName, trackedEntry.AgentDirs); err != nil {
			return AddResult{}, err
		}
		if err := requireExistingDirsEqual(c, projectName, projectPath, trackedEntry.AgentDirs); err != nil {
			return AddResult{}, err
		}
	}

	added := make([]AgentDirResult, 0, len(toOnboard))
	commitPaths := make([]string, 0, len(toOnboard))
	for _, candidate := range toOnboard {
		destination, undo, err := c.AddAgentDir(projectName, candidate.Name, candidate.Path)
		if err != nil {
			return AddResult{}, err
		}
		rollbacks = append(rollbacks, func() {
			if err := undo(); err != nil {
				fmt.Fprintf(os.Stderr, "warning: %v\n", err)
			}
		})
		if err := st.AddAgentDir(projectName, candidate.Name); err != nil {
			return AddResult{}, err
		}
		status := StatusAdded
		if scaffoldedSet[candidate.Name] {
			status = StatusScaffolded
		}
		added = append(added, AgentDirResult{
			Name: candidate.Name, Status: status, CentralPath: destination,
		})
		commitPaths = append(commitPaths, filepath.Join("projects", projectName, candidate.Name))
	}
	for _, dir := range recovered {
		if err := st.AddAgentDir(projectName, dir.Name); err != nil {
			return AddResult{}, err
		}
	}

	commitMessage := ""
	commitSHA := ""
	syncBase := trackedCheckout.SyncBase
	if len(toOnboard) > 0 {
		commitMessage = renderAddTemplate(cfg.Commit.AddTemplate, projectName, namesOf(toOnboard))
		if err := c.CommitPaths(commitMessage, commitPaths...); err != nil {
			return AddResult{}, err
		}
		commitLanded = true
		commitSHA, err = c.HeadSHA()
		if err != nil {
			return AddResult{}, lerr.Wrap(lerr.CodeInconsistency, err,
				"central commit succeeded but its SHA could not be read")
		}
		syncBase = commitSHA
	}
	if len(recovered) > 0 && commitSHA == "" {
		syncBase, err = c.HeadSHA()
		if err != nil {
			return AddResult{}, err
		}
	}

	allDirs := make([]AgentDirResult, 0, len(alreadyTracked)+len(added)+len(recovered)+len(skipped))
	for _, name := range alreadyTracked {
		allDirs = append(allDirs, AgentDirResult{
			Name: name, Status: StatusAlreadyTracked, CentralPath: c.ProjectPath(projectName, name),
		})
	}
	allDirs = append(allDirs, added...)
	allDirs = append(allDirs, recovered...)
	allDirs = append(allDirs, skipped...)
	result := AddResult{
		ProjectName: projectName, ProjectPath: projectPath, CentralRepo: c.Path,
		CommitMessage: commitMessage, CommitSHA: commitSHA, SyncBase: syncBase,
		Dirs: allDirs, ScaffoldRequested: opts.Create,
	}

	if len(toOnboard) > 0 || len(recovered) > 0 {
		if err := st.RecordCheckout(projectName, projectPath, syncBase); err != nil {
			return result, err
		}
		if err := saveAddState(st); err != nil {
			return result, lerr.Wrap(lerr.CodeInconsistency, err,
				"central copy is committed but local state could not be saved").
				WithHint("re-run `lore add`; identical copies will be adopted safely")
		}
	}
	success = true
	if len(skipped) > 0 {
		return result, buildSafetyError(skipped)
	}
	return result, nil
}

func adoptCheckout(st state.State, c *central.Central, projectName, projectPath string, centralDirs []string, scaffoldRequested bool) (AddResult, error) {
	if err := requireCentralDirsAtHead(c, projectName, centralDirs); err != nil {
		return AddResult{}, err
	}
	if err := requireExistingDirsEqual(c, projectName, projectPath, centralDirs); err != nil {
		if lerr.CodeOf(err) == lerr.CodeRefusedSafety {
			return AddResult{}, lerr.Newf(lerr.CodeConflict,
				"project copy for %q differs from central", projectName).
				WithHint("run `lore pull` to choose how to reconcile the copies")
		}
		return AddResult{}, err
	}
	base, err := c.HeadSHA()
	if err != nil {
		return AddResult{}, err
	}
	for _, name := range centralDirs {
		if err := st.AddAgentDir(projectName, name); err != nil {
			return AddResult{}, err
		}
	}
	if err := st.RecordCheckout(projectName, projectPath, base); err != nil {
		return AddResult{}, err
	}
	if err := saveAddState(st); err != nil {
		return AddResult{}, err
	}
	dirs := make([]AgentDirResult, 0, len(centralDirs))
	for _, name := range centralDirs {
		dirs = append(dirs, AgentDirResult{
			Name: name, Status: StatusAdopted, CentralPath: c.ProjectPath(projectName, name),
		})
	}
	return AddResult{
		ProjectName: projectName, ProjectPath: projectPath, CentralRepo: c.Path,
		SyncBase: base, Dirs: dirs, ScaffoldRequested: scaffoldRequested,
	}, nil
}

func requireCentralDirsAtHead(c *central.Central, projectName string, dirNames []string) error {
	dirty, err := c.ScopedDirty(projectName, dirNames)
	if err != nil {
		return err
	}
	if dirty {
		return lerr.Newf(lerr.CodeRefusedSafety,
			"uncommitted central changes prevent a valid sync-base for %q", projectName).
			WithHint("commit, stash, or discard the central changes, then re-run `lore add`")
	}
	return nil
}

func requireExistingDirsEqual(c *central.Central, projectName, projectPath string, dirNames []string) error {
	for _, name := range dirNames {
		equal, err := reconcile.TreesEqual(filepath.Join(projectPath, name), c.ProjectPath(projectName, name))
		if err != nil {
			return err
		}
		if !equal {
			return lerr.Newf(lerr.CodeRefusedSafety,
				"cannot advance the sync-base while %s differs from central", name).
				WithHint("run `lore push` or `lore pull` first")
		}
	}
	return nil
}

func ensureAddDirsIgnored(projectPath string, dirs []detect.AgentDir) error {
	var repoTop string
	for _, dir := range dirs {
		ignored, err := git.CheckIgnore(projectPath, dir.Name)
		if err != nil {
			return err
		}
		if ignored {
			continue
		}
		if repoTop == "" {
			repoTop, err = git.RepoToplevel(projectPath)
			if err != nil {
				return err
			}
		}
		target, err := pickGitignoreTarget(dir.Name)
		if err != nil {
			return err
		}
		targetPath, err := resolveGitignoreTarget(target, repoTop)
		if err != nil {
			return err
		}
		pattern := ignorePatternFor(target, dir.Name, projectPath, repoTop)
		if err := git.AppendIgnoreEntry(targetPath, pattern); err != nil {
			return err
		}
		ignored, err = git.CheckIgnore(projectPath, dir.Name)
		if err != nil {
			return err
		}
		if !ignored {
			return lerr.Newf(lerr.CodeInconsistency,
				"wrote %s to %s but git still reports it as not ignored",
				dir.Name, config.Contract(targetPath))
		}
		output.Success("added %s to %s", pattern, config.Contract(targetPath))
	}
	return nil
}
