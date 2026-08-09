package central

import (
	"bytes"
	"path/filepath"
	"strings"

	"github.com/nathanclairmonte/lore/internal/git"
	"github.com/nathanclairmonte/lore/internal/reconcile"
)

// Snapshot reads one agent directory exactly as stored at a commit.
func (c *Central) Snapshot(commit, projectName, dirName string) (reconcile.Snapshot, []reconcile.Issue, error) {
	prefix := filepath.ToSlash(filepath.Join("projects", projectName, dirName))
	entries, err := git.ReadTree(c.Path, commit, prefix)
	if err != nil {
		return nil, nil, err
	}
	snapshot := reconcile.Snapshot{}
	issues := []reconcile.Issue{}
	strip := prefix + "/"
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Path, strip) {
			continue
		}
		rel := strings.TrimPrefix(entry.Path, strip)
		if entry.ObjectType != "blob" {
			issues = append(issues, reconcile.Issue{
				Kind: reconcile.IssueUnsupportedObject, Path: rel,
				Message: "committed path is not a regular file or symlink",
			})
			continue
		}
		switch entry.Mode {
		case "100644", "100755":
			snapshot[rel] = reconcile.Entry{
				Kind: reconcile.KindFile, Data: entry.Data, Executable: entry.Mode == "100755",
			}
		case "120000":
			snapshot[rel] = reconcile.Entry{Kind: reconcile.KindSymlink, Data: entry.Data}
		default:
			issues = append(issues, reconcile.Issue{
				Kind: reconcile.IssueUnsupportedObject, Path: rel,
				Message: "committed path has unsupported Git mode " + entry.Mode,
			})
		}
	}
	reconcile.SortIssues(issues)
	return snapshot, issues, nil
}

// CommitExists reports whether a SHA remains resolvable as a commit.
func (c *Central) CommitExists(sha string) (bool, error) {
	return git.CommitExists(c.Path, sha)
}

// ScopedDirty reports whether central has working-tree content under the
// selected agent directories that differs from committed HEAD.
func (c *Central) ScopedDirty(projectName string, dirNames []string) (bool, error) {
	paths := make([]string, 0, len(dirNames))
	for _, name := range dirNames {
		paths = append(paths, filepath.ToSlash(filepath.Join("projects", projectName, name)))
	}
	status, err := git.StatusRaw(c.Path, paths...)
	if err != nil {
		return false, err
	}
	return len(bytes.Trim(status, "\x00")) > 0, nil
}
