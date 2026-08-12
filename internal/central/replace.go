package central

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
	"github.com/nathanclairmonte/lore/internal/git"
	"github.com/nathanclairmonte/lore/internal/reconcile"
	"golang.org/x/sys/unix"
)

const replacementRecordFile = "record.json"

// AgentDirReplacement describes one project copy that replaces central.
type AgentDirReplacement struct {
	Project string
	Dir     string
	Source  string
}

// ReplacementSet owns staged central replacements until commit or rollback.
type ReplacementSet struct {
	central      *Central
	transactions []replacementTransaction
	paths        []string
	closed       bool
}

type replacementTransaction struct {
	root    string
	current string
	old     string
	relPath string
}

type replacementRecord struct {
	Path         string `json:"path"`
	PreviousHead string `json:"previous_head"`
}

// LockMutations serializes central mutations until the returned function runs.
func (c *Central) LockMutations() (func() error, error) {
	path := filepath.Join(c.Path, ".git", "lore-mutation.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, lerr.Wrap(lerr.CodeGenericError, err, "cannot open central mutation lock")
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, lerr.Wrap(lerr.CodeGenericError, err, "cannot lock central mutations")
	}
	return func() error {
		unlockErr := unix.Flock(int(file.Fd()), unix.LOCK_UN)
		closeErr := file.Close()
		if err := errors.Join(unlockErr, closeErr); err != nil {
			return lerr.Wrap(lerr.CodeGenericError, err, "cannot release central mutation lock")
		}
		return nil
	}, nil
}

// RecoverReplacements repairs or clears replacements left by an interrupted push.
func (c *Central) RecoverReplacements() error {
	root := filepath.Join(c.Path, ".git", "lore-replacements")
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return lerr.Wrap(lerr.CodeGenericError, err, "cannot inspect interrupted central replacements")
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		transactionRoot := filepath.Join(root, entry.Name())
		if err := c.recoverReplacement(transactionRoot); err != nil {
			return err
		}
	}
	return nil
}

func (c *Central) recoverReplacement(root string) error {
	data, err := os.ReadFile(filepath.Join(root, replacementRecordFile))
	if errors.Is(err, os.ErrNotExist) {
		return os.RemoveAll(root)
	}
	if err != nil {
		return lerr.Wrap(lerr.CodeInconsistency, err, "cannot read interrupted replacement record")
	}
	var record replacementRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return lerr.Wrap(lerr.CodeInconsistency, err, "cannot decode interrupted replacement record")
	}
	relPath := filepath.Clean(record.Path)
	projectsPrefix := "projects" + string(filepath.Separator)
	if filepath.IsAbs(relPath) || relPath == "projects" || len(relPath) <= len(projectsPrefix) || relPath[:len(projectsPrefix)] != projectsPrefix {
		return lerr.Newf(lerr.CodeInconsistency, "invalid interrupted replacement path %q", relPath)
	}
	previousExists, err := c.CommitExists(record.PreviousHead)
	if err != nil {
		return err
	}
	if !previousExists {
		return lerr.Newf(lerr.CodeInconsistency, "interrupted replacement has invalid previous HEAD %q", record.PreviousHead)
	}
	head, err := c.HeadSHA()
	if err != nil {
		return err
	}
	old := filepath.Join(root, "old")
	if _, err := os.Lstat(old); errors.Is(err, os.ErrNotExist) {
		return os.RemoveAll(root)
	} else if err != nil {
		return lerr.Wrap(lerr.CodeInconsistency, err, "cannot inspect interrupted replacement backup")
	}
	current := filepath.Join(c.Path, relPath)
	_, currentErr := os.Lstat(current)
	if currentErr != nil && !errors.Is(currentErr, os.ErrNotExist) {
		return lerr.Wrap(lerr.CodeInconsistency, currentErr, "cannot inspect interrupted central replacement")
	}
	if head != record.PreviousHead {
		if errors.Is(currentErr, os.ErrNotExist) {
			if _, err := git.Run(c.Path, "checkout", "HEAD", "--", relPath); err != nil {
				return lerr.Wrap(lerr.CodeInconsistency, err, "cannot restore committed central replacement from HEAD")
			}
		}
		return os.RemoveAll(root)
	}
	if currentErr == nil {
		if err := os.RemoveAll(current); err != nil {
			return lerr.Wrap(lerr.CodeInconsistency, err, "cannot remove interrupted central replacement")
		}
	}
	if err := os.MkdirAll(filepath.Dir(current), 0o755); err != nil {
		return lerr.Wrap(lerr.CodeInconsistency, err, "cannot prepare interrupted replacement recovery")
	}
	if err := os.Rename(old, current); err != nil {
		return lerr.Wrap(lerr.CodeInconsistency, err, "cannot restore interrupted central replacement")
	}
	if _, err := git.Run(c.Path, "reset", "HEAD", "--", relPath); err != nil {
		return lerr.Wrap(lerr.CodeInconsistency, err, "central content was restored but its index could not be reset")
	}
	return os.RemoveAll(root)
}

// StageAgentDirReplacements publishes and stages a recoverable replacement set.
func (c *Central) StageAgentDirReplacements(replacements []AgentDirReplacement) (*ReplacementSet, error) {
	if len(replacements) == 0 {
		return nil, lerr.New(lerr.CodeGenericError, "central replacement set is empty")
	}
	previousHead, err := c.HeadSHA()
	if err != nil {
		return nil, err
	}
	set := &ReplacementSet{central: c, paths: make([]string, 0, len(replacements))}
	for _, replacement := range replacements {
		transaction, err := c.publishReplacement(replacement, previousHead)
		if err != nil {
			return nil, set.fail(err)
		}
		set.transactions = append(set.transactions, transaction)
		set.paths = append(set.paths, transaction.relPath)
	}
	args := append([]string{"-c", "core.fileMode=true", "add", "-A", "-f", "--"}, set.paths...)
	if _, err := git.Run(c.Path, args...); err != nil {
		return nil, set.fail(err)
	}
	return set, nil
}

func (c *Central) publishReplacement(replacement AgentDirReplacement, previousHead string) (replacementTransaction, error) {
	rootParent := filepath.Join(c.Path, ".git", "lore-replacements")
	if err := os.MkdirAll(rootParent, 0o700); err != nil {
		return replacementTransaction{}, lerr.Wrap(lerr.CodeGenericError, err, "cannot create replacement transaction directory")
	}
	root, err := os.MkdirTemp(rootParent, "replace-*")
	if err != nil {
		return replacementTransaction{}, lerr.Wrap(lerr.CodeGenericError, err, "cannot create replacement transaction")
	}
	relPath := filepath.Join("projects", replacement.Project, replacement.Dir)
	transaction := replacementTransaction{
		root: root, current: c.ProjectPath(replacement.Project, replacement.Dir),
		old: filepath.Join(root, "old"), relPath: relPath,
	}
	record, err := json.Marshal(replacementRecord{Path: relPath, PreviousHead: previousHead})
	if err != nil {
		_ = os.RemoveAll(root)
		return replacementTransaction{}, lerr.Wrap(lerr.CodeGenericError, err, "cannot encode replacement transaction")
	}
	if err := os.WriteFile(filepath.Join(root, replacementRecordFile), record, 0o600); err != nil {
		_ = os.RemoveAll(root)
		return replacementTransaction{}, lerr.Wrap(lerr.CodeGenericError, err, "cannot record replacement transaction")
	}
	if err := reconcile.CopyTree(replacement.Source, filepath.Join(root, "new")); err != nil {
		_ = os.RemoveAll(root)
		return replacementTransaction{}, err
	}
	if err := os.Rename(transaction.current, transaction.old); err != nil {
		_ = os.RemoveAll(root)
		return replacementTransaction{}, lerr.Wrap(lerr.CodeGenericError, err, "cannot back up central copy")
	}
	if err := os.Rename(filepath.Join(root, "new"), transaction.current); err != nil {
		restoreErr := os.Rename(transaction.old, transaction.current)
		_ = os.RemoveAll(root)
		if restoreErr != nil {
			return replacementTransaction{}, lerr.Wrap(lerr.CodeInconsistency, err,
				fmt.Sprintf("cannot publish central replacement and cannot restore its backup: %v", restoreErr))
		}
		return replacementTransaction{}, lerr.Wrap(lerr.CodeGenericError, err, "cannot publish central replacement")
	}
	return transaction, nil
}

// Paths returns the repo-relative pathspecs owned by this replacement set.
func (s *ReplacementSet) Paths() []string {
	return append([]string(nil), s.paths...)
}

// Rollback restores central content and its index to HEAD.
func (s *ReplacementSet) Rollback() error {
	if s == nil || s.closed {
		return nil
	}
	s.closed = true
	var rollbackErrors []error
	if len(s.paths) > 0 {
		if _, err := git.Run(s.central.Path, append([]string{"reset", "HEAD", "--"}, s.paths...)...); err != nil {
			rollbackErrors = append(rollbackErrors, err)
		}
	}
	for i := len(s.transactions) - 1; i >= 0; i-- {
		transaction := s.transactions[i]
		if err := os.RemoveAll(transaction.current); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("remove %s: %w", transaction.current, err))
			continue
		}
		if err := os.Rename(transaction.old, transaction.current); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("restore %s: %w", transaction.current, err))
			continue
		}
		if err := os.RemoveAll(transaction.root); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("remove transaction %s: %w", transaction.root, err))
		}
	}
	if err := errors.Join(rollbackErrors...); err != nil {
		return lerr.Wrap(lerr.CodeInconsistency, err, "cannot fully roll back central replacements")
	}
	return nil
}

// Finish discards committed replacement backups.
func (s *ReplacementSet) Finish() error {
	if s == nil || s.closed {
		return nil
	}
	s.closed = true
	var cleanupErrors []error
	for _, transaction := range s.transactions {
		if err := os.RemoveAll(transaction.root); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	if err := errors.Join(cleanupErrors...); err != nil {
		return lerr.Wrap(lerr.CodeInconsistency, err, "central commit succeeded but replacement backups could not be removed")
	}
	return nil
}

func (s *ReplacementSet) fail(primary error) error {
	if rollbackErr := s.Rollback(); rollbackErr != nil {
		return lerr.Wrap(lerr.CodeInconsistency, primary, fmt.Sprintf("central replacement failed and rollback also failed: %v", rollbackErr))
	}
	return primary
}
