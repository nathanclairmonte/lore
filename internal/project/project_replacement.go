package project

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/reconcile"
	"github.com/nathanclairmonte/lore/internal/state"
)

const projectReplacementVersion = 2

type projectReplacementRecord struct {
	Version      int                      `json:"version"`
	Project      string                   `json:"project"`
	Checkout     string                   `json:"checkout"`
	SyncBase     string                   `json:"sync_base"`
	ReplacePaths []string                 `json:"replace_paths"`
	AgentDirs    []string                 `json:"agent_dirs"`
	Items        []projectReplacementItem `json:"items"`
	Committed    bool                     `json:"committed"`
}

type projectReplacementItem struct {
	Destination string `json:"destination"`
	Staged      string `json:"staged"`
	Backup      string `json:"backup"`
	HadOriginal bool   `json:"had_original"`
	Copied      bool   `json:"copied"`
	Published   bool   `json:"published"`
}

type projectReplacementTransaction struct {
	path   string
	record projectReplacementRecord
}

type projectReplacementPublication struct {
	project      string
	checkout     string
	syncBase     string
	replacePaths []string
	agentDirs    []string
	replacements []reconcile.TreeReplacement
}

func projectReplacementPath() (string, error) {
	statePath, err := state.Path()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(statePath), "project-replacement.json"), nil
}

func publishProjectReplacements(publication projectReplacementPublication) (*projectReplacementTransaction, error) {
	if len(publication.replacements) == 0 {
		return nil, nil
	}
	path, err := projectReplacementPath()
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); err == nil {
		return nil, lerr.New(lerr.CodeInconsistency, "an interrupted project replacement must be recovered before updating project copies")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, lerr.Wrap(lerr.CodeGenericError, err, "cannot inspect project replacement record")
	}

	record := projectReplacementRecord{
		Version: projectReplacementVersion, Project: publication.project,
		Checkout: publication.checkout, SyncBase: publication.syncBase,
		ReplacePaths: append([]string(nil), publication.replacePaths...),
		AgentDirs:    append([]string(nil), publication.agentDirs...), Items: []projectReplacementItem{},
	}
	transaction := &projectReplacementTransaction{path: path, record: record}
	for _, replacement := range publication.replacements {
		staged, err := reserveProjectSibling(replacement.Destination, reconcile.StagePathPrefix)
		if err != nil {
			return nil, transaction.fail(err)
		}
		transaction.record.Items = append(transaction.record.Items, projectReplacementItem{
			Destination: replacement.Destination, Staged: staged,
		})
		if err := transaction.save(); err != nil {
			return nil, transaction.fail(err)
		}
		if err := reconcile.CopyTree(replacement.Source, staged); err != nil {
			return nil, transaction.fail(err)
		}
		transaction.record.Items[len(transaction.record.Items)-1].Copied = true
		if err := transaction.save(); err != nil {
			return nil, transaction.fail(err)
		}
	}
	for i := range transaction.record.Items {
		item := &transaction.record.Items[i]
		info, err := os.Lstat(item.Destination)
		switch {
		case err == nil:
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return nil, transaction.fail(lerr.Newf(lerr.CodeRefusedSafety, "project copy %s is not a real directory", item.Destination))
			}
			item.Backup, err = reserveProjectSibling(item.Destination, reconcile.BackupPathPrefix)
			if err != nil {
				return nil, transaction.fail(err)
			}
			if err := transaction.save(); err != nil {
				return nil, transaction.fail(err)
			}
			if err := os.Rename(item.Destination, item.Backup); err != nil {
				return nil, transaction.fail(lerr.Wrap(lerr.CodeGenericError, err, "cannot back up project copy"))
			}
			item.HadOriginal = true
		case errors.Is(err, os.ErrNotExist):
		default:
			return nil, transaction.fail(lerr.Wrap(lerr.CodeGenericError, err, "cannot inspect project copy"))
		}
		if err := transaction.save(); err != nil {
			return nil, transaction.fail(err)
		}
		if err := os.Rename(item.Staged, item.Destination); err != nil {
			return nil, transaction.fail(lerr.Wrap(lerr.CodeGenericError, err, "cannot publish project copy"))
		}
		item.Published = true
		if err := transaction.save(); err != nil {
			return nil, transaction.fail(err)
		}
	}
	transaction.record.Committed = true
	if err := transaction.save(); err != nil {
		return nil, lerr.Wrap(lerr.CodeInconsistency, err, "project copies were published but their transaction could not be committed")
	}
	return transaction, nil
}

func reserveProjectSibling(destination, prefix string) (string, error) {
	path, err := os.MkdirTemp(filepath.Dir(destination), prefix)
	if err != nil {
		return "", lerr.Wrap(lerr.CodeGenericError, err, "cannot reserve project replacement path")
	}
	if err := os.Remove(path); err != nil {
		return "", lerr.Wrap(lerr.CodeGenericError, err, "cannot prepare project replacement path")
	}
	return path, nil
}

func (t *projectReplacementTransaction) save() error {
	if err := os.MkdirAll(filepath.Dir(t.path), 0o755); err != nil {
		return lerr.Wrap(lerr.CodeGenericError, err, "cannot create project replacement state directory")
	}
	data, err := json.Marshal(t.record)
	if err != nil {
		return lerr.Wrap(lerr.CodeGenericError, err, "cannot encode project replacement record")
	}
	temporary := t.path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return lerr.Wrap(lerr.CodeGenericError, err, "cannot write project replacement record")
	}
	if err := os.Rename(temporary, t.path); err != nil {
		return lerr.Wrap(lerr.CodeGenericError, err, "cannot publish project replacement record")
	}
	return nil
}

func (t *projectReplacementTransaction) finish() error {
	var cleanupErrors []error
	for _, item := range t.record.Items {
		if item.Backup != "" {
			cleanupErrors = appendIfError(cleanupErrors, os.RemoveAll(item.Backup))
		}
		if item.Staged != "" {
			cleanupErrors = appendIfError(cleanupErrors, os.RemoveAll(item.Staged))
		}
	}
	cleanupErrors = appendIfError(cleanupErrors, os.Remove(t.path))
	if err := errors.Join(cleanupErrors...); err != nil {
		return lerr.Wrap(lerr.CodeInconsistency, err, "project copy updated but transaction cleanup failed")
	}
	return nil
}

func (t *projectReplacementTransaction) fail(cause error) error {
	if err := t.rollback(); err != nil {
		return errors.Join(cause, lerr.Wrap(lerr.CodeInconsistency, err, "project replacement rollback failed"))
	}
	return cause
}

func (t *projectReplacementTransaction) rollback() error {
	var rollbackErrors []error
	for i := len(t.record.Items) - 1; i >= 0; i-- {
		item := t.record.Items[i]
		backupExists := item.Backup != "" && !pathMissing(item.Backup)
		needsDestinationRemoval := item.Published || item.Copied && pathMissing(item.Staged)
		if needsDestinationRemoval && (item.Backup == "" || backupExists) {
			if err := os.RemoveAll(item.Destination); err != nil {
				rollbackErrors = append(rollbackErrors, lerr.Wrap(lerr.CodeInconsistency, err, "cannot remove published project copy"))
			}
		}
		if backupExists {
			if err := os.Rename(item.Backup, item.Destination); err != nil {
				rollbackErrors = append(rollbackErrors, lerr.Wrap(lerr.CodeInconsistency, err, fmt.Sprintf("cannot restore project copy from backup %s", item.Backup)))
			}
		}
	}
	if err := errors.Join(rollbackErrors...); err != nil {
		return err
	}
	for _, item := range t.record.Items {
		if err := os.RemoveAll(item.Staged); err != nil {
			return lerr.Wrap(lerr.CodeInconsistency, err, "cannot remove staged project copy")
		}
	}
	if t.path != "" {
		if err := os.Remove(t.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return lerr.Wrap(lerr.CodeInconsistency, err, "cannot remove project replacement record")
		}
	}
	return nil
}

func recoverProjectReplacement(st *state.State) error {
	path, err := projectReplacementPath()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return lerr.Wrap(lerr.CodeInconsistency, err, "cannot read interrupted project replacement")
	}
	var record projectReplacementRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return lerr.Wrap(lerr.CodeInconsistency, err, "cannot decode interrupted project replacement")
	}
	if record.Version == 1 {
		for i := range record.Items {
			record.Items[i].Copied = true
		}
	}
	if err := validateProjectReplacement(record); err != nil {
		return err
	}
	transaction := &projectReplacementTransaction{path: path, record: record}
	if !record.Committed && !publicationLooksComplete(record.Items) {
		if err := transaction.rollback(); err != nil {
			return lerr.Wrap(lerr.CodeInconsistency, err, "cannot roll back interrupted project replacement")
		}
		return nil
	}
	for _, replaced := range record.ReplacePaths {
		st.RemoveCheckout(record.Project, replaced)
	}
	for _, dir := range record.AgentDirs {
		if err := st.AddAgentDir(record.Project, dir); err != nil {
			return err
		}
	}
	if err := st.RecordCheckout(record.Project, record.Checkout, record.SyncBase); err != nil {
		return err
	}
	if err := state.Save(*st); err != nil {
		return lerr.Wrap(lerr.CodeInconsistency, err, "cannot save recovered project sync-base")
	}
	return transaction.finish()
}

func detectOrphanProjectReplacementPaths(paths []string) error {
	for _, path := range paths {
		children, err := os.ReadDir(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return lerr.Wrap(lerr.CodeGenericError, err, "cannot inspect checkout for replacement artifacts")
		}
		for _, child := range children {
			if strings.HasPrefix(child.Name(), reconcile.StagePathPrefix) || strings.HasPrefix(child.Name(), reconcile.BackupPathPrefix) {
				return lerr.Newf(lerr.CodeInconsistency, "unrecorded project replacement artifact at %s", filepath.Join(path, child.Name())).
					WithHint("inspect and remove or relocate the artifact before running lore")
			}
		}
	}
	return nil
}

func validateProjectReplacement(record projectReplacementRecord) error {
	if (record.Version != 1 && record.Version != projectReplacementVersion) || record.Project == "" || record.Checkout == "" || record.SyncBase == "" || len(record.Items) == 0 {
		return lerr.New(lerr.CodeInconsistency, "invalid interrupted project replacement record")
	}
	for _, item := range record.Items {
		parent := filepath.Dir(item.Destination)
		if !filepath.IsAbs(item.Destination) || filepath.Dir(item.Staged) != parent || item.Backup != "" && filepath.Dir(item.Backup) != parent ||
			!strings.HasPrefix(filepath.Base(item.Staged), reconcile.StagePathPrefix) || item.Backup != "" && !strings.HasPrefix(filepath.Base(item.Backup), reconcile.BackupPathPrefix) {
			return lerr.Newf(lerr.CodeInconsistency, "invalid interrupted project replacement path for %s", item.Destination)
		}
	}
	return nil
}

func publicationLooksComplete(items []projectReplacementItem) bool {
	for _, item := range items {
		if !item.Copied || !pathMissing(item.Staged) || pathMissing(item.Destination) {
			return false
		}
	}
	return true
}

func pathMissing(path string) bool {
	_, err := os.Lstat(path)
	return errors.Is(err, os.ErrNotExist)
}

func appendIfError(existing []error, err error) []error {
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return append(existing, err)
	}
	return existing
}
