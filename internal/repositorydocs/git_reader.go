package repositorydocs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

const DefaultMaxDocumentBytes int64 = 1 << 20

var (
	ErrGitObjectUnavailable = errors.New("git object is unavailable")
	ErrWorktreeUnavailable  = errors.New("worktree content is unavailable")
)

type GitObjectError struct {
	Object string
	Reason string
}

func (e *GitObjectError) Error() string {
	if e == nil {
		return ErrGitObjectUnavailable.Error()
	}
	return fmt.Sprintf("%s: %s", ErrGitObjectUnavailable, e.Reason)
}

func (e *GitObjectError) Unwrap() error { return ErrGitObjectUnavailable }

func (e *GitObjectError) DiagnosticCode() string { return "git_object_unavailable" }

type WorktreeError struct {
	Path   string
	Reason string
}

func (e *WorktreeError) Error() string {
	if e == nil {
		return ErrWorktreeUnavailable.Error()
	}
	return fmt.Sprintf("%s: %s: %s", ErrWorktreeUnavailable, e.Path, e.Reason)
}

func (e *WorktreeError) Unwrap() error { return ErrWorktreeUnavailable }

func (e *WorktreeError) DiagnosticCode() string { return "worktree_overlay_unavailable" }

type Repository struct {
	root         string
	commonDir    string
	GitStoreRef  string `json:"git_store_ref"`
	WorktreeRef  string `json:"worktree_ref"`
	ObjectFormat string `json:"object_format"`
}

type TreeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	OID  string `json:"oid"`
	Size int64  `json:"size"`
}

type WorktreeChange struct {
	Path       string `json:"path"`
	OldPath    string `json:"old_path,omitempty"`
	IndexState byte   `json:"index_state"`
	TreeState  byte   `json:"worktree_state"`
	Deleted    bool   `json:"deleted"`
	Renamed    bool   `json:"renamed"`
	Digest     string `json:"digest,omitempty"`
	Size       int64  `json:"size,omitempty"`
}

func OpenRepository(ctx context.Context, start string) (*Repository, error) {
	if strings.TrimSpace(start) == "" {
		start = "."
	}
	root, err := gitOutput(ctx, start, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("repository docs: resolve Git worktree: %w", err)
	}
	root, err = filepath.Abs(strings.TrimSpace(root))
	if err != nil {
		return nil, err
	}
	common, err := gitOutput(ctx, root, "rev-parse", "--git-common-dir")
	if err != nil {
		return nil, fmt.Errorf("repository docs: resolve Git object store: %w", err)
	}
	common = strings.TrimSpace(common)
	if !filepath.IsAbs(common) {
		common = filepath.Join(root, common)
	}
	common, err = filepath.Abs(common)
	if err != nil {
		return nil, err
	}
	format, err := gitOutput(ctx, root, "rev-parse", "--show-object-format")
	if err != nil {
		// Git versions before --show-object-format only support SHA-1 repositories.
		format = "sha1"
	}
	format = strings.TrimSpace(format)
	if format == "" {
		format = "sha1"
	}
	return &Repository{
		root:         root,
		commonDir:    common,
		ObjectFormat: format,
		GitStoreRef:  opaqueRef("git-store", common+"\x00"+format),
		WorktreeRef:  opaqueRef("worktree", root+"\x00"+common),
	}, nil
}

func (r *Repository) ResolveRevision(ctx context.Context, revision string) (string, error) {
	if r == nil {
		return "", fmt.Errorf("repository docs: Git repository is required")
	}
	if strings.TrimSpace(revision) == "" {
		revision = "HEAD"
	}
	oid, err := gitOutput(ctx, r.root, "rev-parse", "--verify", revision+"^{commit}")
	if err != nil {
		return "", &GitObjectError{Object: revision, Reason: "revision cannot be resolved from the local object database"}
	}
	oid = strings.TrimSpace(oid)
	if !validOID(oid, r.ObjectFormat) {
		return "", &GitObjectError{Object: revision, Reason: "resolved object id has an unexpected format"}
	}
	return oid, nil
}

func (r *Repository) ListTree(ctx context.Context, commitOID string) ([]TreeEntry, error) {
	if !validOID(commitOID, r.ObjectFormat) {
		return nil, &GitObjectError{Object: commitOID, Reason: "invalid commit object id"}
	}
	cmd := exec.CommandContext(ctx, "git", "-C", r.root, "ls-tree", "-r", "-l", "-z", "--full-tree", commitOID)
	data, err := cmd.Output()
	if err != nil {
		return nil, &GitObjectError{Object: commitOID, Reason: "commit tree is not available locally"}
	}
	parts := bytes.Split(data, []byte{0})
	entries := make([]TreeEntry, 0, len(parts))
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		header, rawPath, ok := bytes.Cut(part, []byte{'\t'})
		if !ok {
			return nil, fmt.Errorf("repository docs: malformed git ls-tree entry")
		}
		fields := strings.Fields(string(header))
		if len(fields) != 4 {
			return nil, fmt.Errorf("repository docs: malformed git ls-tree header")
		}
		size := int64(-1)
		if fields[3] != "-" {
			parsed, parseErr := strconv.ParseInt(fields[3], 10, 64)
			if parseErr != nil {
				return nil, fmt.Errorf("repository docs: malformed git object size")
			}
			size = parsed
		}
		entries = append(entries, TreeEntry{Path: string(rawPath), Mode: fields[0], Type: fields[1], OID: fields[2], Size: size})
	}
	return entries, nil
}

func (r *Repository) OpenBlob(ctx context.Context, oid string) (io.ReadCloser, error) {
	if !validOID(oid, r.ObjectFormat) {
		return nil, &GitObjectError{Object: oid, Reason: "invalid blob object id"}
	}
	cmd := exec.CommandContext(ctx, "git", "-C", r.root, "cat-file", "blob", oid)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, &GitObjectError{Object: oid, Reason: "blob cannot be opened"}
	}
	return &commandReadCloser{reader: stdout, cmd: cmd, object: oid}, nil
}

func (r *Repository) ReadBlob(ctx context.Context, oid string, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxDocumentBytes
	}
	reader, err := r.OpenBlob(ctx, oid)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	closeErr := reader.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if int64(len(data)) > maxBytes {
		return nil, &GitObjectError{Object: oid, Reason: fmt.Sprintf("blob exceeds the %d-byte indexing limit", maxBytes)}
	}
	return data, nil
}

func readBoundedFile(path string, maxBytes int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("file exceeds the %d-byte indexing limit", maxBytes)
	}
	return data, nil
}

func (r *Repository) ReadFileAtCommit(ctx context.Context, commitOID, repoPath string, maxBytes int64) ([]byte, bool, error) {
	entries, err := r.ListTree(ctx, commitOID)
	if err != nil {
		return nil, false, err
	}
	repoPath = normalizeRepoPath(repoPath)
	for _, entry := range entries {
		if entry.Path != repoPath {
			continue
		}
		if entry.Type != "blob" || entry.Mode == "120000" {
			return nil, false, nil
		}
		data, err := r.ReadBlob(ctx, entry.OID, maxBytes)
		return data, true, err
	}
	return nil, false, nil
}

func (r *Repository) TrackedChanges(ctx context.Context, maxBytes int64) ([]WorktreeChange, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", r.root, "status", "--porcelain=v1", "-z", "--untracked-files=no")
	data, err := cmd.Output()
	if err != nil {
		return nil, &WorktreeError{Reason: "tracked worktree status is unavailable"}
	}
	parts := bytes.Split(data, []byte{0})
	changes := make([]WorktreeChange, 0, len(parts))
	for idx := 0; idx < len(parts); idx++ {
		part := parts[idx]
		if len(part) == 0 {
			continue
		}
		if len(part) < 4 || part[2] != ' ' {
			return nil, fmt.Errorf("repository docs: malformed git status entry")
		}
		change := WorktreeChange{IndexState: part[0], TreeState: part[1], Path: normalizeRepoPath(string(part[3:]))}
		change.Deleted = change.IndexState == 'D' || change.TreeState == 'D'
		change.Renamed = change.IndexState == 'R' || change.TreeState == 'R'
		if change.Renamed {
			idx++
			if idx >= len(parts) || len(parts[idx]) == 0 {
				return nil, fmt.Errorf("repository docs: malformed rename status entry")
			}
			change.OldPath = normalizeRepoPath(string(parts[idx]))
		}
		if !change.Deleted {
			content, err := r.ReadTrackedWorktreeFile(ctx, change.Path, maxBytes)
			if err != nil {
				return nil, err
			}
			change.Digest = digestBytes(content)
			change.Size = int64(len(content))
		}
		changes = append(changes, change)
	}
	return changes, nil
}

func (r *Repository) ReadTrackedWorktreeFile(ctx context.Context, repoPath string, maxBytes int64) ([]byte, error) {
	repoPath = normalizeRepoPath(repoPath)
	if repoPath == "" || strings.HasPrefix(repoPath, "../") || strings.Contains(repoPath, "/../") {
		return nil, &WorktreeError{Path: repoPath, Reason: "unsafe repository-relative path"}
	}
	if err := exec.CommandContext(ctx, "git", "-C", r.root, "ls-files", "--error-unmatch", "--", repoPath).Run(); err != nil {
		return nil, &WorktreeError{Path: repoPath, Reason: "path is not tracked"}
	}
	fullPath := filepath.Join(r.root, filepath.FromSlash(repoPath))
	rel, err := filepath.Rel(r.root, fullPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, &WorktreeError{Path: repoPath, Reason: "path escapes the selected worktree"}
	}
	info, err := os.Lstat(fullPath)
	if err != nil {
		return nil, &WorktreeError{Path: repoPath, Reason: "tracked file is unavailable"}
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, &WorktreeError{Path: repoPath, Reason: "symlink and non-regular content is excluded"}
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxDocumentBytes
	}
	if info.Size() > maxBytes {
		return nil, &WorktreeError{Path: repoPath, Reason: fmt.Sprintf("file exceeds the %d-byte indexing limit", maxBytes)}
	}
	data, err := readBoundedFile(fullPath, maxBytes)
	if err != nil {
		return nil, &WorktreeError{Path: repoPath, Reason: "tracked file cannot be read"}
	}
	return data, nil
}

func ResolvePolicy(ctx context.Context, repo *Repository, revision string, includeWorktree bool) (string, PolicyResolution, error) {
	commitOID, err := repo.ResolveRevision(ctx, revision)
	if err != nil {
		return "", PolicyResolution{}, err
	}
	data, found, err := repo.ReadFileAtCommit(ctx, commitOID, PolicyConfigPath, DefaultMaxDocumentBytes)
	if err != nil {
		return "", PolicyResolution{}, err
	}
	source := PolicySourceCommitted
	if !found {
		data = nil
	}
	if includeWorktree {
		changes, err := repo.TrackedChanges(ctx, DefaultMaxDocumentBytes)
		if err != nil {
			return "", PolicyResolution{}, err
		}
		for _, change := range changes {
			if change.Path != PolicyConfigPath {
				continue
			}
			if change.Deleted {
				data = nil
			} else {
				data, err = repo.ReadTrackedWorktreeFile(ctx, PolicyConfigPath, DefaultMaxDocumentBytes)
				if err != nil {
					return "", PolicyResolution{}, err
				}
			}
			source = PolicySourceWorktree
			break
		}
	}
	resolved, err := ParsePolicy(data, source)
	return commitOID, resolved, err
}

type commandReadCloser struct {
	reader io.ReadCloser
	cmd    *exec.Cmd
	object string
	once   sync.Once
	err    error
}

func (r *commandReadCloser) Read(p []byte) (int, error) { return r.reader.Read(p) }

func (r *commandReadCloser) Close() error {
	r.once.Do(func() {
		_ = r.reader.Close()
		r.err = r.cmd.Wait()
		if r.err != nil {
			r.err = &GitObjectError{Object: r.object, Reason: "blob read did not complete"}
		}
	})
	return r.err
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmdArgs := append([]string{"-C", dir}, args...)
	data, err := exec.CommandContext(ctx, "git", cmdArgs...).Output()
	return string(data), err
}

func opaqueRef(prefix, value string) string {
	sum := sha256.Sum256([]byte(value))
	return prefix + "-" + hex.EncodeToString(sum[:16])
}

func validOID(oid, objectFormat string) bool {
	want := 40
	if objectFormat == "sha256" {
		want = 64
	}
	if len(oid) != want {
		return false
	}
	_, err := hex.DecodeString(oid)
	return err == nil
}

func normalizeRepoPath(value string) string {
	return strings.TrimPrefix(filepath.ToSlash(filepath.Clean(value)), "./")
}
