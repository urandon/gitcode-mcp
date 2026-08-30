package repositorydocs

import (
	"bufio"
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
	"unicode/utf8"
)

const (
	DefaultMaxDocumentBytes int64 = 1 << 20
	maxGitTreeRecordBytes         = 1 << 20
	maxLFSPointerBytes            = 1024
)

var (
	ErrGitObjectUnavailable = errors.New("git object is unavailable")
	ErrDocumentTooLarge     = errors.New("repository document exceeds the configured size limit")
	ErrMalformedGitTree     = errors.New("malformed git tree output")
	ErrWorktreeUnavailable  = errors.New("worktree content is unavailable")
	ErrWorktreeOverlayStale = errors.New("worktree overlay changed during the operation")
	ErrIndexSnapshotStale   = errors.New("repository documentation index snapshot changed before execution")
)

type DocumentTooLargeError struct {
	Object      string
	LimitBytes  int64
	ActualBytes int64
}

func (e *DocumentTooLargeError) Error() string {
	if e == nil {
		return ErrDocumentTooLarge.Error()
	}
	return fmt.Sprintf("%s: limit=%d bytes", ErrDocumentTooLarge, e.LimitBytes)
}

func (e *DocumentTooLargeError) Unwrap() error { return ErrDocumentTooLarge }

func (e *DocumentTooLargeError) DiagnosticCode() string { return "repository_document_too_large" }

type GitObjectError struct {
	Object       string                `json:"-"`
	Reason       string                `json:"reason"`
	Availability GitObjectAvailability `json:"availability"`
	Recovery     GitObjectRecovery     `json:"recovery"`
}

type GitObjectAvailability string

const (
	GitObjectUnavailable GitObjectAvailability = "unavailable"
	GitObjectShallow     GitObjectAvailability = "shallow"
	GitObjectPartial     GitObjectAvailability = "partial_clone"
	GitObjectPromisor    GitObjectAvailability = "promisor"
)

type GitObjectRecovery struct {
	Action  string `json:"action"`
	Message string `json:"message"`
}

func (e *GitObjectError) Error() string {
	if e == nil {
		return ErrGitObjectUnavailable.Error()
	}
	return fmt.Sprintf("%s: %s", ErrGitObjectUnavailable, e.Reason)
}

func (e *GitObjectError) Unwrap() error { return ErrGitObjectUnavailable }

func (e *GitObjectError) DiagnosticCode() string {
	if e == nil {
		return "git_object_unavailable"
	}
	switch e.Availability {
	case GitObjectShallow:
		return "git_object_shallow"
	case GitObjectPartial:
		return "git_object_partial_clone"
	case GitObjectPromisor:
		return "git_object_promisor_unavailable"
	default:
		return "git_object_unavailable"
	}
}

func (e *GitObjectError) Remediation() string {
	if e == nil {
		return ""
	}
	return e.Recovery.Message
}

type WorktreeError struct {
	Path   string
	Reason string
}

func (e *WorktreeError) Error() string {
	if e == nil {
		return ErrWorktreeUnavailable.Error()
	}
	return fmt.Sprintf("%s: %s", ErrWorktreeUnavailable, e.Reason)
}

func (e *WorktreeError) Unwrap() error { return ErrWorktreeUnavailable }

func (e *WorktreeError) DiagnosticCode() string { return "worktree_overlay_unavailable" }

type WorktreeOverlayStaleError struct{}

func (e *WorktreeOverlayStaleError) Error() string { return ErrWorktreeOverlayStale.Error() }

func (e *WorktreeOverlayStaleError) Unwrap() error { return ErrWorktreeOverlayStale }

func (e *WorktreeOverlayStaleError) DiagnosticCode() string { return "worktree_overlay_stale" }

type IndexSnapshotStaleError struct{}

func (e *IndexSnapshotStaleError) Error() string { return ErrIndexSnapshotStale.Error() }

func (e *IndexSnapshotStaleError) Unwrap() error { return ErrIndexSnapshotStale }

func (e *IndexSnapshotStaleError) DiagnosticCode() string { return "repository_docs_snapshot_stale" }

type Repository struct {
	root         string
	commonDir    string
	GitStoreRef  string `json:"git_store_ref"`
	WorktreeRef  string `json:"worktree_ref"`
	ObjectFormat string `json:"object_format"`
}

func invalidGitObjectError(object, reason string) *GitObjectError {
	return &GitObjectError{Object: object, Reason: reason, Availability: GitObjectUnavailable}
}

func (r *Repository) unavailableGitObjectError(ctx context.Context, object, reason string, revisionObject bool) *GitObjectError {
	availability := r.gitObjectAvailability(ctx, revisionObject)
	recovery := GitObjectRecovery{Action: "fetch_git_object", Message: "fetch the missing object into the selected local Git authority, then retry"}
	switch availability {
	case GitObjectShallow:
		recovery = GitObjectRecovery{Action: "deepen_git_history", Message: "deepen or fetch the selected local Git authority, then retry"}
	case GitObjectPartial:
		recovery = GitObjectRecovery{Action: "materialize_partial_clone_object", Message: "materialize the missing object in the selected local partial clone, then retry"}
	case GitObjectPromisor:
		recovery = GitObjectRecovery{Action: "materialize_promisor_object", Message: "materialize the missing promised object in the selected local Git authority, then retry"}
	}
	return &GitObjectError{Object: object, Reason: reason, Availability: availability, Recovery: recovery}
}

func (r *Repository) gitObjectAvailability(ctx context.Context, revisionObject bool) GitObjectAvailability {
	if r == nil || strings.TrimSpace(r.root) == "" {
		return GitObjectUnavailable
	}
	isShallow := func() bool {
		value, err := gitOutput(ctx, r.root, "rev-parse", "--is-shallow-repository")
		return err == nil && strings.TrimSpace(value) == "true"
	}
	isPartial := func() bool {
		if value, err := gitOutput(ctx, r.root, "config", "--local", "--get", "extensions.partialClone"); err == nil && strings.TrimSpace(value) != "" {
			return true
		}
		value, err := gitOutput(ctx, r.root, "config", "--local", "--get-regexp", `^remote\..*\.partialclonefilter$`)
		return err == nil && strings.TrimSpace(value) != ""
	}
	isPromisor := func() bool {
		value, err := gitOutput(ctx, r.root, "config", "--local", "--get-regexp", `^remote\..*\.promisor$`)
		if err != nil {
			return false
		}
		for _, line := range strings.Split(value, "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && strings.EqualFold(fields[len(fields)-1], "true") {
				return true
			}
		}
		return false
	}
	if revisionObject && isShallow() {
		return GitObjectShallow
	}
	if isPartial() {
		return GitObjectPartial
	}
	if isPromisor() {
		return GitObjectPromisor
	}
	if isShallow() {
		return GitObjectShallow
	}
	return GitObjectUnavailable
}

type TreeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	OID  string `json:"oid"`
	Size int64  `json:"size"`
}

type DocumentContentClass string

const (
	DocumentContentRegular     DocumentContentClass = "regular"
	DocumentContentSymlink     DocumentContentClass = "symlink"
	DocumentContentSubmodule   DocumentContentClass = "submodule"
	DocumentContentLFSPointer  DocumentContentClass = "lfs_pointer"
	DocumentContentNUL         DocumentContentClass = "nul_content"
	DocumentContentInvalidUTF8 DocumentContentClass = "invalid_utf8"
)

func ClassifyDocumentContent(entry TreeEntry, content []byte) DocumentContentClass {
	if entry.Mode == "160000" || entry.Type == "commit" {
		return DocumentContentSubmodule
	}
	if entry.Mode == "120000" {
		return DocumentContentSymlink
	}
	if isLFSPointer(content) {
		return DocumentContentLFSPointer
	}
	if bytes.IndexByte(content, 0) >= 0 {
		return DocumentContentNUL
	}
	if !utf8.Valid(content) {
		return DocumentContentInvalidUTF8
	}
	return DocumentContentRegular
}

func isLFSPointer(content []byte) bool {
	if len(content) == 0 || len(content) > maxLFSPointerBytes || !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 || bytes.IndexByte(content, '\r') >= 0 {
		return false
	}
	text := string(content)
	if strings.HasSuffix(text, "\n") {
		text = strings.TrimSuffix(text, "\n")
	}
	lines := strings.Split(text, "\n")
	if len(lines) < 3 || lines[0] != "version https://git-lfs.github.com/spec/v1" {
		return false
	}
	line := 1
	priorities := make(map[uint64]struct{})
	for line < len(lines) && strings.HasPrefix(lines[line], "ext-") {
		priority, ok := parseLFSExtensionLine(lines[line])
		if !ok {
			return false
		}
		if _, duplicate := priorities[priority]; duplicate {
			return false
		}
		priorities[priority] = struct{}{}
		line++
	}
	if line >= len(lines) || !validLFSOIDLine(lines[line]) {
		return false
	}
	line++
	if line >= len(lines) || !validLFSSizeLine(lines[line]) {
		return false
	}
	return line+1 == len(lines)
}

func parseLFSExtensionLine(line string) (uint64, bool) {
	fields := strings.Fields(line)
	if len(fields) != 2 || strings.Join(fields, " ") != line || !strings.HasPrefix(fields[0], "ext-") {
		return 0, false
	}
	order, name, ok := strings.Cut(strings.TrimPrefix(fields[0], "ext-"), "-")
	if !ok || order == "" || name == "" || !validLFSExtensionName(name) {
		return 0, false
	}
	priority, err := strconv.ParseUint(order, 10, 64)
	if err != nil {
		return 0, false
	}
	algorithm, digest, ok := strings.Cut(fields[1], ":")
	return priority, ok && algorithm == "sha256" && len(digest) == 64 && digest == strings.ToLower(digest) && isHex(digest)
}

func validLFSExtensionName(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '.' && char != '_' && char != '-' {
			return false
		}
	}
	return true
}

func validLFSOIDLine(line string) bool {
	const prefix = "oid sha256:"
	digest := strings.TrimPrefix(line, prefix)
	return strings.HasPrefix(line, prefix) && len(digest) == 64 && digest == strings.ToLower(digest) && isHex(digest)
}

func validLFSSizeLine(line string) bool {
	const prefix = "size "
	value := strings.TrimPrefix(line, prefix)
	if !strings.HasPrefix(line, prefix) || value == "" || strings.ContainsAny(value, " \t") {
		return false
	}
	_, err := strconv.ParseUint(value, 10, 64)
	return err == nil
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
	isBare := false
	if err != nil {
		bare, bareErr := gitOutput(ctx, start, "rev-parse", "--is-bare-repository")
		if bareErr != nil || strings.TrimSpace(bare) != "true" {
			return nil, fmt.Errorf("repository docs: resolve Git worktree: %w", err)
		}
		isBare = true
		root = start
	}
	root, err = filepath.Abs(strings.TrimSpace(root))
	if err != nil {
		return nil, err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(root); resolveErr == nil {
		root = resolved
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
	if resolved, resolveErr := filepath.EvalSymlinks(common); resolveErr == nil {
		common = resolved
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
	repository := &Repository{
		root:         root,
		commonDir:    common,
		ObjectFormat: format,
		GitStoreRef:  opaqueRef("git-store", common+"\x00"+format),
	}
	if !isBare {
		repository.WorktreeRef = opaqueRef("worktree", root+"\x00"+common)
	}
	return repository, nil
}

func (r *Repository) ResolveRevision(ctx context.Context, revision string) (string, error) {
	if r == nil {
		return "", fmt.Errorf("repository docs: Git repository is required")
	}
	if strings.TrimSpace(revision) == "" {
		revision = "HEAD"
	}
	oid, err := gitOutput(ctx, r.root, "rev-parse", "--verify", "--end-of-options", revision+"^{commit}")
	if err != nil {
		return "", r.unavailableGitObjectError(ctx, revision, "revision cannot be resolved from the local object database", true)
	}
	oid = strings.TrimSpace(oid)
	if !validOID(oid, r.ObjectFormat) {
		return "", invalidGitObjectError(revision, "resolved object id has an unexpected format")
	}
	return oid, nil
}

func (r *Repository) ListTree(ctx context.Context, commitOID string) ([]TreeEntry, error) {
	entries := make([]TreeEntry, 0)
	err := r.WalkTree(ctx, commitOID, func(entry TreeEntry) error {
		entries = append(entries, entry)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

// WalkTree visits the committed tree in Git's deterministic output order
// without buffering the complete ls-tree response in memory.
func (r *Repository) WalkTree(ctx context.Context, commitOID string, visit func(TreeEntry) error) error {
	if !validOID(commitOID, r.ObjectFormat) {
		return invalidGitObjectError(commitOID, "invalid commit object id")
	}
	if visit == nil {
		return fmt.Errorf("repository docs: tree visitor is required")
	}
	childCtx, cancelChild := context.WithCancel(ctx)
	defer cancelChild()
	cmd := gitCommandContext(childCtx, "-C", r.root, "ls-tree", "-r", "-l", "-z", "--full-tree", commitOID)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return r.unavailableGitObjectError(ctx, commitOID, "commit tree cannot be opened", true)
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return r.unavailableGitObjectError(ctx, commitOID, "commit tree cannot be opened", true)
	}
	walkErr := walkTreeRecords(ctx, stdout, visit)
	if walkErr != nil {
		cancelChild()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = stdout.Close()
	}
	waitErr := cmd.Wait()
	if walkErr != nil {
		return walkErr
	}
	if waitErr != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		return r.unavailableGitObjectError(ctx, commitOID, "commit tree is not available locally", true)
	}
	return nil
}

func walkTreeRecords(ctx context.Context, reader io.Reader, visit func(TreeEntry) error) error {
	buffered := bufio.NewReaderSize(reader, maxGitTreeRecordBytes)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		record, err := buffered.ReadSlice(0)
		if err != nil {
			if errors.Is(err, io.EOF) && len(record) == 0 {
				return nil
			}
			if errors.Is(err, io.EOF) {
				return fmt.Errorf("%w: truncated record", ErrMalformedGitTree)
			}
			if errors.Is(err, bufio.ErrBufferFull) {
				return fmt.Errorf("%w: record exceeds safe parser limit", ErrMalformedGitTree)
			}
			return fmt.Errorf("repository docs: read git tree: %w", err)
		}
		entry, err := parseTreeRecord(record[:len(record)-1])
		if err != nil {
			return err
		}
		if err := visit(entry); err != nil {
			return err
		}
	}
}

func parseTreeRecord(record []byte) (TreeEntry, error) {
	header, rawPath, ok := bytes.Cut(record, []byte{'\t'})
	if !ok || len(rawPath) == 0 {
		return TreeEntry{}, fmt.Errorf("%w: missing path separator", ErrMalformedGitTree)
	}
	fields := strings.Fields(string(header))
	if len(fields) != 4 {
		return TreeEntry{}, fmt.Errorf("%w: invalid header", ErrMalformedGitTree)
	}
	if (len(fields[2]) != 40 && len(fields[2]) != 64) || !isHex(fields[2]) {
		return TreeEntry{}, fmt.Errorf("%w: invalid object id", ErrMalformedGitTree)
	}
	size := int64(-1)
	if fields[3] != "-" {
		parsed, err := strconv.ParseInt(fields[3], 10, 64)
		if err != nil || parsed < 0 {
			return TreeEntry{}, fmt.Errorf("%w: invalid object size", ErrMalformedGitTree)
		}
		size = parsed
	}
	return TreeEntry{Path: string(rawPath), Mode: fields[0], Type: fields[1], OID: fields[2], Size: size}, nil
}

func (r *Repository) OpenBlob(ctx context.Context, oid string) (io.ReadCloser, error) {
	if !validOID(oid, r.ObjectFormat) {
		return nil, invalidGitObjectError(oid, "invalid blob object id")
	}
	cmd := gitCommandContext(ctx, "-C", r.root, "cat-file", "blob", oid)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, r.unavailableGitObjectError(ctx, oid, "blob cannot be opened", false)
	}
	return &commandReadCloser{reader: stdout, cmd: cmd, object: oid, repo: r, ctx: ctx}, nil
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
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		return nil, r.unavailableGitObjectError(ctx, oid, "blob read did not complete", false)
	}
	if int64(len(data)) > maxBytes {
		return nil, &DocumentTooLargeError{Object: oid, LimitBytes: maxBytes, ActualBytes: int64(len(data))}
	}
	if closeErr != nil {
		return nil, closeErr
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
		return nil, &DocumentTooLargeError{LimitBytes: maxBytes, ActualBytes: int64(len(data))}
	}
	return data, nil
}

func (r *Repository) ReadFileAtCommit(ctx context.Context, commitOID, repoPath string, maxBytes int64) ([]byte, bool, error) {
	if !validOID(commitOID, r.ObjectFormat) {
		return nil, false, invalidGitObjectError(commitOID, "invalid commit object id")
	}
	repoPath = normalizeRepoPath(repoPath)
	if repoPath == "" || repoPath == "." || strings.HasPrefix(repoPath, "../") || strings.Contains(repoPath, "/../") {
		return nil, false, nil
	}
	pathspec := ":(top,literal)" + repoPath
	cmd := gitCommandContext(ctx, "-C", r.root, "ls-tree", "-l", "-z", "--full-tree", commitOID, "--", pathspec)
	cmd.Stderr = io.Discard
	data, err := cmd.Output()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, false, ctxErr
		}
		return nil, false, r.unavailableGitObjectError(ctx, commitOID, "commit tree is not available locally", true)
	}
	if len(data) == 0 {
		return nil, false, nil
	}
	if data[len(data)-1] != 0 || bytes.IndexByte(data[:len(data)-1], 0) >= 0 {
		return nil, false, fmt.Errorf("%w: exact-path lookup returned an unexpected record count", ErrMalformedGitTree)
	}
	entry, err := parseTreeRecord(data[:len(data)-1])
	if err != nil {
		return nil, false, err
	}
	if entry.Path != repoPath || entry.Type != "blob" || entry.Mode == "120000" {
		return nil, false, nil
	}
	body, err := r.ReadBlob(ctx, entry.OID, maxBytes)
	return body, true, err
}

func (r *Repository) TrackedChanges(ctx context.Context, maxBytes int64) ([]WorktreeChange, error) {
	commitOID, err := r.ResolveRevision(ctx, "HEAD")
	if err != nil {
		return nil, err
	}
	return r.TrackedChangesAt(ctx, commitOID, maxBytes)
}

// TrackedChangesAt returns the tracked worktree overlay relative to the exact
// commit that the caller will use as its base. This differs intentionally from
// `git status`, whose comparison base is always the current HEAD.
func (r *Repository) TrackedChangesAt(ctx context.Context, commitOID string, maxBytes int64) ([]WorktreeChange, error) {
	return r.TrackedChangesAtFiltered(ctx, commitOID, maxBytes, nil)
}

// TrackedChangesAtFiltered limits content reads and overlay identity to paths
// relevant to the caller's corpus. Git still computes one local name-status
// diff, but an unrelated large tracked file cannot fail documentation work.
func (r *Repository) TrackedChangesAtFiltered(ctx context.Context, commitOID string, maxBytes int64, include func(string) bool) ([]WorktreeChange, error) {
	if !validOID(commitOID, r.ObjectFormat) {
		return nil, invalidGitObjectError(commitOID, "invalid overlay base commit")
	}
	combined, err := r.diffNameStatus(ctx, commitOID, false)
	if err != nil {
		return nil, &WorktreeError{Reason: "tracked worktree comparison is unavailable"}
	}
	indexed, err := r.diffNameStatus(ctx, commitOID, true)
	if err != nil {
		return nil, &WorktreeError{Reason: "tracked index comparison is unavailable"}
	}
	unstaged, err := r.diffNameStatus(ctx, "", false)
	if err != nil {
		return nil, &WorktreeError{Reason: "tracked worktree comparison is unavailable"}
	}
	indexStates := worktreeChangeStates(indexed)
	treeStates := worktreeChangeStates(unstaged)
	changes := make([]WorktreeChange, 0, len(combined))
	for _, raw := range combined {
		change := raw
		change.IndexState = stateForWorktreeChange(indexStates, change)
		change.TreeState = stateForWorktreeChange(treeStates, change)
		if include != nil && !include(change.Path) && (change.OldPath == "" || !include(change.OldPath)) {
			continue
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

func (r *Repository) diffNameStatus(ctx context.Context, baseCommit string, cached bool) ([]WorktreeChange, error) {
	args := []string{"-C", r.root, "diff"}
	if cached {
		args = append(args, "--cached")
	}
	args = append(args, "--name-status", "-z", "--find-renames", "--no-ext-diff", "--no-textconv")
	if baseCommit != "" {
		args = append(args, baseCommit)
	}
	args = append(args, "--")
	data, err := gitCommandContext(ctx, args...).Output()
	if err != nil {
		return nil, err
	}
	parts := bytes.Split(data, []byte{0})
	changes := make([]WorktreeChange, 0, len(parts)/2)
	for idx := 0; idx < len(parts); idx++ {
		part := parts[idx]
		if len(part) == 0 {
			continue
		}
		status := string(part)
		if len(status) == 0 {
			return nil, fmt.Errorf("repository docs: malformed git diff status entry")
		}
		idx++
		if idx >= len(parts) || len(parts[idx]) == 0 {
			return nil, fmt.Errorf("repository docs: malformed git diff path entry")
		}
		change := WorktreeChange{Path: normalizeRepoPath(string(parts[idx]))}
		change.Deleted = status[0] == 'D'
		change.Renamed = status[0] == 'R'
		change.IndexState = status[0]
		if change.Renamed {
			idx++
			if idx >= len(parts) || len(parts[idx]) == 0 {
				return nil, fmt.Errorf("repository docs: malformed rename diff entry")
			}
			change.OldPath = change.Path
			change.Path = normalizeRepoPath(string(parts[idx]))
		}
		changes = append(changes, change)
	}
	return changes, nil
}

func worktreeChangeStates(changes []WorktreeChange) map[string]byte {
	states := make(map[string]byte, len(changes)*2)
	for _, change := range changes {
		states[change.Path] = change.IndexState
		if change.OldPath != "" {
			states[change.OldPath] = change.IndexState
		}
	}
	return states
}

func stateForWorktreeChange(states map[string]byte, change WorktreeChange) byte {
	if state := states[change.Path]; state != 0 {
		return state
	}
	if state := states[change.OldPath]; state != 0 {
		return state
	}
	return ' '
}

func (r *Repository) ReadTrackedWorktreeFile(ctx context.Context, repoPath string, maxBytes int64) ([]byte, error) {
	if filepath.IsAbs(repoPath) || filepath.VolumeName(repoPath) != "" {
		return nil, &WorktreeError{Reason: "unsafe repository-relative path"}
	}
	repoPath = normalizeRepoPath(repoPath)
	if repoPath == "" || strings.HasPrefix(repoPath, "../") || strings.Contains(repoPath, "/../") {
		return nil, &WorktreeError{Path: repoPath, Reason: "unsafe repository-relative path"}
	}
	if err := gitCommandContext(ctx, "-C", r.root, "ls-files", "--error-unmatch", "--", repoPath).Run(); err != nil {
		return nil, &WorktreeError{Path: repoPath, Reason: "path is not tracked"}
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxDocumentBytes
	}
	data, actualBytes, err := readWorktreeFileNoFollow(ctx, r.root, repoPath, maxBytes)
	if err != nil {
		if errors.Is(err, ErrDocumentTooLarge) {
			return nil, &DocumentTooLargeError{Object: repoPath, LimitBytes: maxBytes, ActualBytes: actualBytes}
		}
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
		changes, err := repo.TrackedChangesAtFiltered(ctx, commitOID, DefaultMaxDocumentBytes, func(repoPath string) bool {
			return repoPath == PolicyConfigPath
		})
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
	repo   *Repository
	ctx    context.Context
	once   sync.Once
	err    error
}

func (r *commandReadCloser) Read(p []byte) (int, error) { return r.reader.Read(p) }

func (r *commandReadCloser) Close() error {
	r.once.Do(func() {
		_ = r.reader.Close()
		r.err = r.cmd.Wait()
		if r.err != nil {
			r.err = r.repo.unavailableGitObjectError(r.ctx, r.object, "blob read did not complete", false)
		}
	})
	return r.err
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmdArgs := append([]string{"-C", dir}, args...)
	data, err := gitCommandContext(ctx, cmdArgs...).Output()
	return string(data), err
}

func gitCommandContext(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0")
	return cmd
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
	return isHex(oid)
}

func isHex(value string) bool {
	_, err := hex.DecodeString(value)
	return err == nil
}

func normalizeRepoPath(value string) string {
	return strings.TrimPrefix(filepath.ToSlash(filepath.Clean(value)), "./")
}
