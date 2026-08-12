package git

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	lerr "github.com/nathanclairmonte/lore/internal/errors"
)

// objectIDWidth is the hex length of a SHA-1 object id, used only to presize
// the cat-file batch input.
const objectIDWidth = 40

// TreeEntry is one file or symlink stored in a Git tree.
type TreeEntry struct {
	Path       string
	Mode       string
	ObjectType string
	ObjectID   string
	Data       []byte
}

// CommitExists reports whether rev resolves to a commit object.
func CommitExists(repo, rev string) (bool, error) {
	args := []string{"rev-parse", "--verify", "--quiet", rev + "^{commit}"}
	result, err := RunRaw(repo, args...)
	if err != nil {
		return false, err
	}
	if result.ExitCode == 0 {
		return true, nil
	}
	if result.ExitCode == 1 {
		return false, nil
	}
	return false, rawError(result, args...)
}

// ReadTree reads every blob under prefix at rev without changing its bytes.
func ReadTree(repo, rev, prefix string) ([]TreeEntry, error) {
	args := []string{"ls-tree", "-rz", "--full-tree", rev, "--", prefix}
	result, err := RunRaw(repo, args...)
	if err != nil {
		return nil, err
	}
	if result.ExitCode != 0 {
		return nil, rawError(result, args...)
	}
	records := bytes.Split(result.Stdout, []byte{0})
	entries := make([]TreeEntry, 0, len(records))
	batchIndexes := make([]int, 0, len(records))
	var batchInput bytes.Buffer
	// object ids are fixed width, so the batch stdin size is known up front.
	batchInput.Grow(len(records) * (objectIDWidth + 1))
	for _, record := range records {
		if len(record) == 0 {
			continue
		}
		header, pathBytes, ok := bytes.Cut(record, []byte{'\t'})
		if !ok {
			return nil, lerr.New(lerr.CodeGitNetwork, "git ls-tree returned malformed output")
		}
		fields := strings.Fields(string(header))
		if len(fields) != 3 {
			return nil, lerr.New(lerr.CodeGitNetwork, "git ls-tree returned malformed metadata")
		}
		entries = append(entries, TreeEntry{
			Path: string(pathBytes), Mode: fields[0], ObjectType: fields[1],
			ObjectID: fields[2],
		})
		if fields[1] == "blob" {
			batchIndexes = append(batchIndexes, len(entries)-1)
			_, _ = batchInput.WriteString(fields[2])
			_ = batchInput.WriteByte('\n')
		}
	}
	if len(batchIndexes) == 0 {
		return entries, nil
	}
	batch, err := runRawInput(repo, batchInput.Bytes(), "cat-file", "--batch")
	if err != nil {
		return nil, err
	}
	if batch.ExitCode != 0 {
		return nil, rawError(batch, "cat-file", "--batch")
	}
	remaining := batch.Stdout
	for _, entryIndex := range batchIndexes {
		newline := bytes.IndexByte(remaining, '\n')
		if newline < 0 {
			return nil, lerr.New(lerr.CodeGitNetwork, "git cat-file returned a truncated batch header")
		}
		header := strings.Fields(string(remaining[:newline]))
		if len(header) != 3 || header[0] != entries[entryIndex].ObjectID || header[1] != "blob" {
			return nil, lerr.New(lerr.CodeGitNetwork, "git cat-file returned malformed batch metadata")
		}
		size, err := strconv.Atoi(header[2])
		if err != nil || size < 0 || len(remaining) < newline+1+size+1 {
			return nil, lerr.New(lerr.CodeGitNetwork, "git cat-file returned an invalid batch size")
		}
		start := newline + 1
		end := start + size
		if remaining[end] != '\n' {
			return nil, lerr.New(lerr.CodeGitNetwork, "git cat-file returned malformed batch content")
		}
		entries[entryIndex].Data = append([]byte(nil), remaining[start:end]...)
		remaining = remaining[end+1:]
	}
	if len(remaining) != 0 {
		return nil, lerr.New(lerr.CodeGitNetwork, "git cat-file returned unexpected batch content")
	}
	return entries, nil
}

// StatusRaw returns NUL-delimited porcelain records for paths.
func StatusRaw(repo string, paths ...string) ([]byte, error) {
	args := []string{"status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignored=matching", "--"}
	args = append(args, paths...)
	result, err := RunRaw(repo, args...)
	if err != nil {
		return nil, err
	}
	if result.ExitCode != 0 {
		return nil, rawError(result, args...)
	}
	return append([]byte(nil), result.Stdout...), nil
}

func rawError(result Result, args ...string) error {
	message := fmt.Sprintf("git %s failed with exit code %d", strings.Join(args, " "), result.ExitCode)
	if detail := strings.TrimSpace(string(result.Stderr)); detail != "" {
		message += ": " + detail
	}
	return lerr.New(lerr.CodeGitNetwork, message)
}
