package project

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nathanclairmonte/lore/internal/config"
	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/git"
	"github.com/nathanclairmonte/lore/internal/reconcile"
	"github.com/nathanclairmonte/lore/internal/state"
)

// MoveCheckoutResult describes one checkout retained by a rename.
type MoveCheckoutResult struct {
	OldPath  string
	Path     string
	SyncBase string
}

// MoveResult is the structured outcome of a project rename.
type MoveResult struct {
	OldName       string
	ProjectName   string
	CentralRepo   string
	CommitMessage string
	Commit        string
	Checkouts     []MoveCheckoutResult
	CentralOnly   bool
}

var saveMoveState = state.Save

// Move renames one committed central project and its local-state key.
// Project copies never move. The optional path updates one checkout record.
func Move(old, newName, newPath string) (MoveResult, error) {
	ctx, err := openMutationContext()
	if err != nil {
		return MoveResult{}, err
	}
	defer ctx.close()
	if err := ValidateProjectName(old, "<old>"); err != nil {
		return MoveResult{}, err
	}
	if err := ValidateProjectName(newName, "<new>"); err != nil {
		return MoveResult{}, err
	}
	if old == newName {
		return MoveResult{}, lerr.Newf(lerr.CodeConflict, "project is already named %q", old)
	}
	oldExists, err := centralProjectDirExists(ctx.central, old)
	if err != nil {
		return MoveResult{}, err
	}
	newExists, err := centralProjectDirExists(ctx.central, newName)
	if err != nil {
		return MoveResult{}, err
	}
	_, oldRecorded := ctx.state.ProjectEntryFor(old)
	_, newRecorded := ctx.state.ProjectEntryFor(newName)
	if !oldExists {
		if newExists && oldRecorded && !newRecorded {
			return recoverCommittedMove(ctx, old, newName, newPath)
		}
		return MoveResult{}, lerr.Newf(lerr.CodeNotFound, "projects/%s does not exist in the central repo", old)
	}
	if newExists {
		return MoveResult{}, lerr.Newf(lerr.CodeConflict, "projects/%s already exists in the central repo", newName)
	}
	if newRecorded {
		return MoveResult{}, lerr.Newf(lerr.CodeConflict, "project %q is already recorded in local state", newName)
	}
	working := ctx.state.Clone()
	recorded := oldRecorded
	if !recorded && newPath != "" {
		return MoveResult{}, lerr.New(lerr.CodeRefusedSafety, "--path cannot update a project with no local checkout record").
			WithHint("rename without --path, then use `lore pull <new> --path <path>` to adopt the checkout")
	}
	oldPaths := map[string]string{}
	if !recorded {
		centralDirs, err := ctx.central.ProjectAgentDirs(old)
		if err != nil {
			return MoveResult{}, err
		}
		dirty, err := ctx.central.ScopedDirty(old, centralDirs)
		if err != nil {
			return MoveResult{}, err
		}
		if dirty {
			return MoveResult{}, lerr.New(lerr.CodeRefusedSafety, "central copy has uncommitted or ignored content").
				WithHint("commit, stash, or discard the central changes before renaming the project")
		}
	} else {
		oldPaths, err = applyPathUpdate(&working, old, newPath)
		if err != nil {
			return MoveResult{}, err
		}
		status, statusErr := statusFromState(working, ctx.central, old, "")
		if statusErr != nil {
			return MoveResult{}, statusErr
		}
		if err := validateMoveStatus(status); err != nil {
			return MoveResult{}, err
		}
	}

	message := renderMvTemplate(ctx.config.Commit.MvTemplate, old, newName)
	undo, err := ctx.central.RenameProjectStaged(old, newName)
	if err != nil {
		return MoveResult{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = undo()
		}
	}()
	if err := ctx.central.CommitPaths(message, filepath.Join("projects", old), filepath.Join("projects", newName)); err != nil {
		return MoveResult{}, err
	}
	committed = true
	head, err := ctx.central.HeadSHA()
	if err != nil {
		return MoveResult{}, err
	}

	result := MoveResult{OldName: old, ProjectName: newName, CentralRepo: ctx.central.Path, CommitMessage: message, Commit: head, Checkouts: []MoveCheckoutResult{}, CentralOnly: !recorded}
	if recorded {
		wrapSave := func(err error) error {
			return lerr.Wrap(lerr.CodeInconsistency, err, "central rename committed but local state could not be updated").
				WithHint("retry after repairing the local state file")
		}
		if err := finishMove(working, old, newName, head, oldPaths, &result, wrapSave); err != nil {
			return result, err
		}
	}
	return result, nil
}

// applyPathUpdate re-points the selected checkout at newPath and returns the
// pre-rename path of every checkout, keyed by its post-rename path.
func applyPathUpdate(working *state.State, name, newPath string) (map[string]string, error) {
	entry, _ := working.ProjectEntryFor(name)
	oldPaths := map[string]string{}
	for _, checkout := range entry.Checkouts {
		oldPaths[checkout.Path] = checkout.Path
	}
	if newPath == "" {
		return oldPaths, nil
	}
	destination, err := resolveProjectPath(newPath)
	if err != nil {
		return nil, err
	}
	if owner, exists := working.CheckoutOwner(destination); exists && owner != name {
		return nil, lerr.Newf(lerr.CodeConflict,
			"checkout %s is already recorded for project %q", config.Contract(destination), owner)
	}
	selected, err := selectMoveCheckout(entry.Checkouts)
	if err != nil {
		return nil, err
	}
	for _, checkout := range entry.Checkouts {
		if checkout.Path == destination && checkout.Path != selected {
			return nil, lerr.Newf(lerr.CodeConflict, "checkout %s is already recorded for project %q", config.Contract(destination), name)
		}
	}
	for i := range entry.Checkouts {
		if entry.Checkouts[i].Path == selected {
			oldPaths[destination] = selected
			entry.Checkouts[i].Path = destination
		}
	}
	working.Projects[name] = entry
	return oldPaths, nil
}

// finishMove renames the state key, stamps every checkout onto the rename
// commit, fills in result.Checkouts, and persists the result. wrapSave labels a
// save failure, which strands a committed central rename against stale state.
func finishMove(working state.State, old, newName, head string, oldPaths map[string]string, result *MoveResult, wrapSave func(error) error) error {
	if err := working.RenameProject(old, newName); err != nil {
		return err
	}
	entry := working.Projects[newName]
	for i := range entry.Checkouts {
		entry.Checkouts[i].SyncBase = head
		result.Checkouts = append(result.Checkouts, MoveCheckoutResult{OldPath: oldPaths[entry.Checkouts[i].Path], Path: entry.Checkouts[i].Path, SyncBase: head})
	}
	working.Projects[newName] = entry
	sort.Slice(result.Checkouts, func(i, j int) bool { return result.Checkouts[i].Path < result.Checkouts[j].Path })
	if err := saveMoveState(working); err != nil {
		return wrapSave(err)
	}
	return nil
}

func recoverCommittedMove(ctx *mutationContext, old, newName, newPath string) (MoveResult, error) {
	working := ctx.state.Clone()
	oldPaths, err := applyPathUpdate(&working, old, newPath)
	if err != nil {
		return MoveResult{}, err
	}
	dirs, err := ctx.central.ProjectAgentDirs(newName)
	if err != nil {
		return MoveResult{}, err
	}
	dirty, err := ctx.central.ScopedDirty(newName, dirs)
	if err != nil {
		return MoveResult{}, err
	}
	if dirty {
		return MoveResult{}, lerr.New(lerr.CodeRefusedSafety, "central copy has uncommitted or ignored content")
	}
	head, err := ctx.central.HeadSHA()
	if err != nil {
		return MoveResult{}, err
	}
	if err := validateCommittedRename(ctx.central.Path, head, old, newName); err != nil {
		return MoveResult{}, err
	}
	entry, _ := working.ProjectEntryFor(old)
	for _, checkout := range entry.Checkouts {
		if err := validateRecoveredCheckout(ctx, head, old, newName, checkout, dirs); err != nil {
			return MoveResult{}, err
		}
	}
	result := MoveResult{
		OldName: old, ProjectName: newName, CentralRepo: ctx.central.Path,
		CommitMessage: renderMvTemplate(ctx.config.Commit.MvTemplate, old, newName), Commit: head,
		Checkouts: []MoveCheckoutResult{},
	}
	wrapSave := func(err error) error {
		return lerr.Wrap(lerr.CodeInconsistency, err, "central rename is committed but local state could not be recovered")
	}
	if err := finishMove(working, old, newName, head, oldPaths, &result, wrapSave); err != nil {
		return result, err
	}
	return result, nil
}

func validateCommittedRename(centralPath, head, oldName, newName string) error {
	parent, err := git.Run(centralPath, "rev-parse", head+"^")
	if err != nil {
		return lerr.New(lerr.CodeRefusedSafety, "cannot verify the interrupted rename commit")
	}
	oldPrefix := filepath.ToSlash(filepath.Join("projects", oldName))
	newPrefix := filepath.ToSlash(filepath.Join("projects", newName))
	oldBefore, err := git.ReadTree(centralPath, parent, oldPrefix)
	if err != nil {
		return err
	}
	newAfter, err := git.ReadTree(centralPath, head, newPrefix)
	if err != nil {
		return err
	}
	oldAfter, err := git.ReadTree(centralPath, head, oldPrefix)
	if err != nil {
		return err
	}
	newBefore, err := git.ReadTree(centralPath, parent, newPrefix)
	if err != nil {
		return err
	}
	if len(oldBefore) == 0 || len(oldBefore) != len(newAfter) || len(oldAfter) != 0 || len(newBefore) != 0 {
		return lerr.New(lerr.CodeRefusedSafety, "central history does not prove the requested interrupted rename").
			WithHint("inspect central Git history and repair local state before retrying")
	}
	for i := range oldBefore {
		oldPath := strings.TrimPrefix(oldBefore[i].Path, oldPrefix)
		newPath := strings.TrimPrefix(newAfter[i].Path, newPrefix)
		if oldPath != newPath || oldBefore[i].Mode != newAfter[i].Mode || oldBefore[i].ObjectType != newAfter[i].ObjectType || oldBefore[i].ObjectID != newAfter[i].ObjectID {
			return lerr.New(lerr.CodeRefusedSafety, "central history does not prove the requested interrupted rename").
				WithHint("inspect central Git history and repair local state before retrying")
		}
	}
	return nil
}

// validateRecoveredCheckout applies the normal rename safety gate while the
// state key still uses oldName and committed central content uses newName.
func validateRecoveredCheckout(ctx *mutationContext, head, oldName, newName string, checkout state.CheckoutEntry, dirs []string) error {
	baseExists, err := ctx.central.CommitExists(checkout.SyncBase)
	if err != nil {
		return err
	}
	if !baseExists {
		return lerr.Newf(lerr.CodeRefusedSafety, "checkout %s has an unreachable sync-base", config.Contract(checkout.Path))
	}
	missing := 0
	for _, dir := range dirs {
		projectSnapshot, issues, err := reconcile.InspectTree(filepath.Join(checkout.Path, dir))
		if err != nil {
			return err
		}
		if onlyCopyMissing(issues) {
			missing++
			continue
		}
		if len(issues) > 0 {
			return lerr.Newf(lerr.CodeRefusedSafety, "checkout %s has unresolved health issues", config.Contract(checkout.Path))
		}
		centralSnapshot, centralIssues, err := ctx.central.Snapshot(head, newName, dir)
		if err != nil {
			return err
		}
		baseSnapshot, baseIssues, err := ctx.central.Snapshot(checkout.SyncBase, oldName, dir)
		if err != nil {
			return err
		}
		classified := reconcile.Classify(projectSnapshot, centralSnapshot, baseSnapshot, mergeIssues(centralIssues, baseIssues))
		if len(classified.Issues) > 0 || classified.State == reconcile.StateCentralAhead || classified.State == reconcile.StateDiverged {
			return lerr.Newf(lerr.CodeRefusedSafety, "checkout %s is %s or unhealthy", config.Contract(checkout.Path), classified.State)
		}
	}
	if missing > 0 && missing < len(dirs) {
		return lerr.Newf(lerr.CodeRefusedSafety, "checkout %s is only partially present", config.Contract(checkout.Path))
	}
	return nil
}

func selectMoveCheckout(checkouts []state.CheckoutEntry) (string, error) {
	if len(checkouts) == 1 {
		return checkouts[0].Path, nil
	}
	missing := []string{}
	for _, checkout := range checkouts {
		if _, err := os.Stat(checkout.Path); errors.Is(err, os.ErrNotExist) {
			missing = append(missing, checkout.Path)
		}
	}
	if len(missing) == 1 {
		return missing[0], nil
	}
	cwd, err := resolveProjectPath("")
	if err == nil {
		matches := []string{}
		for _, checkout := range checkouts {
			if isSelfOrAncestor(checkout.Path, cwd) {
				matches = append(matches, checkout.Path)
			}
		}
		if len(matches) == 1 {
			return matches[0], nil
		}
	}
	return "", lerr.New(lerr.CodeRefusedSafety, "cannot determine which checkout --path should update").
		WithHint("run from the checkout to update, or ensure exactly one recorded checkout path is missing")
}

func validateMoveStatus(result StatusResult) error {
	for _, project := range result.Projects {
		if len(project.Issues) > 0 {
			return lerr.New(lerr.CodeRefusedSafety, "cannot rename while the central copy is unhealthy or dirty")
		}
		for _, checkout := range project.Checkouts {
			missing := 0
			for _, dir := range checkout.Dirs {
				if len(dir.Issues) == 0 {
					continue
				}
				if !onlyCopyMissing(dir.Issues) {
					return lerr.Newf(lerr.CodeRefusedSafety, "checkout %s has unresolved health issues", config.Contract(checkout.Path))
				}
				missing++
			}
			// a wholly absent checkout is renameable; a partial one is not.
			if len(checkout.Issues) > 0 || (missing > 0 && missing < len(checkout.Dirs)) {
				return lerr.Newf(lerr.CodeRefusedSafety, "checkout %s has unresolved health issues", config.Contract(checkout.Path))
			}
			if missing == len(checkout.Dirs) && len(checkout.Dirs) > 0 {
				continue
			}
			if checkout.State == reconcile.StateCentralAhead || checkout.State == reconcile.StateDiverged {
				return lerr.Newf(lerr.CodeRefusedSafety, "checkout %s is %s", config.Contract(checkout.Path), checkout.State).
					WithHint("run `lore pull` before renaming the project")
			}
		}
	}
	return nil
}

func renderMvTemplate(tmpl, old, newName string) string {
	if strings.TrimSpace(tmpl) == "" {
		tmpl = "refactor: rename {old} to {new}"
	}
	result := strings.ReplaceAll(tmpl, "{old}", old)
	return strings.ReplaceAll(result, "{new}", newName)
}
