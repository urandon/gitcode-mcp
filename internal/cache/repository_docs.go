package cache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	RepoDocSetBuilding    = "building"
	RepoDocSetPartial     = "partial"
	RepoDocSetReady       = "ready"
	RepoDocSetBlocked     = "blocked"
	RepoDocSetSuperseded  = "superseded"
	RepoDocSetUnavailable = "unavailable"
	RepoDocSetEvicted     = "evicted"
)

var (
	ErrRepositoryDocRevisionSetPublished = errors.New("cache: repository document revision set is already published")
	ErrRepositoryDocVectorConflict       = errors.New("cache: repository document vector conflicts with immutable identity")
)

type RepositoryDocRevisionSet struct {
	RepoID                       string
	ID                           string
	SourceRegistrationID         string
	SourceRegistrationGeneration int64
	GitStoreRef                  string
	WorktreeRef                  string
	ObjectFormat                 string
	CommitOID                    string
	RequestedRevision            string
	PolicyHash                   string
	PolicySource                 string
	ConfigDigest                 string
	OverlayDigest                string
	ChunkPolicyID                string
	ProcessingPolicyID           string
	NamespaceID                  string
	State                        string
	EligibleFiles                int
	EligibleChunks               int
	EmbeddedChunks               int
	ReusedChunks                 int
	FailedChunks                 int
	ExcludedFiles                int
	MissingObjects               int
	CreatedAt                    time.Time
	UpdatedAt                    time.Time
	CompletedAt                  time.Time
	LastErrorClass               string
}

type RepositoryDocRevisionSetFilter struct {
	RepoID                       string
	SourceRegistrationID         string
	SourceRegistrationGeneration int64
	GitStoreRef                  string
	CommitOID                    string
	PolicyHash                   string
	OverlayDigest                string
	ExactOverlay                 bool
	ChunkPolicyID                string
	NamespaceID                  string
	State                        string
	Limit                        int
}

type RepositoryDocChunk struct {
	RepoID               string
	ID                   string
	ObjectFormat         string
	BlobOID              string
	WorktreeRef          string
	ContentDigest        string
	ByteStart            int
	ByteEnd              int
	LineStart            int
	LineEnd              int
	RawSliceDigest       string
	EmbeddingInputDigest string
	ChunkPolicyID        string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type RepositoryDocMembership struct {
	RepoID        string
	RevisionSetID string
	Path          string
	ChunkID       string
	Authority     string
	Ordinal       int
	BlobOID       string
	WorktreeRef   string
	ContentDigest string
}

// RepositoryDocExclusion is a typed, path-safe explanation for a document that
// was deliberately omitted from a revision set. Git remains the byte authority;
// this record stores only identity metadata and the stable reason code.
type RepositoryDocExclusion struct {
	RepoID        string
	RevisionSetID string
	Path          string
	Authority     string
	BlobOID       string
	ReasonCode    string
}

type RepositoryDocVector struct {
	RepoID      string
	NamespaceID string
	ChunkID     string
	Vector      []byte
	Dimensions  int
	DType       string
	VectorHash  string
	EmbeddedAt  time.Time
}

// RepositoryDocPendingVectorIdentity names a metadata-backed chunk that may
// have a durable provider checkpoint but does not yet have a published vector.
type RepositoryDocPendingVectorIdentity struct {
	RepoID      string
	NamespaceID string
	ChunkID     string
}

type RepositoryDocCandidate struct {
	RepositoryDocMembership
	ObjectFormat         string
	WorktreeRef          string
	ByteStart            int
	ByteEnd              int
	LineStart            int
	LineEnd              int
	RawSliceDigest       string
	EmbeddingInputDigest string
	ChunkPolicyID        string
	NamespaceID          string
	Vector               []byte
	Dimensions           int
	DType                string
	VectorHash           string
}

// RepositoryDocSearchSnapshot is an exact, transactionally consistent view of
// one published revision set and its derived search candidates. Git remains the
// byte authority; the snapshot contains metadata and vectors only.
type RepositoryDocSearchSnapshot struct {
	RevisionSet RepositoryDocRevisionSet
	Candidates  []RepositoryDocCandidate
}

type RepositoryDocRetentionPolicy struct {
	RetainCommittedPerIdentity int
	RetainOverlaysPerIdentity  int
	CommittedCutoff            time.Time
	OverlayCutoff              time.Time
	TerminalCutoff             time.Time
	MaxVectorBytes             int64
	ProtectedSetIDs            []string
}

type RepositoryDocGCResult struct {
	RevisionSetsDeleted int64 `json:"revision_sets_deleted"`
	ChunksDeleted       int64 `json:"chunks_deleted"`
	VectorsDeleted      int64 `json:"vectors_deleted"`
	VectorBytesBefore   int64 `json:"vector_bytes_before"`
	VectorBytesAfter    int64 `json:"vector_bytes_after"`
}

func (s *SQLiteStore) UpsertRepositoryDocRevisionSet(ctx context.Context, set RepositoryDocRevisionSet) error {
	normalized, err := normalizeRepositoryDocRevisionSet(set)
	if err != nil {
		return err
	}
	if normalized.State == RepoDocSetReady || normalized.State == RepoDocSetPartial {
		return fmt.Errorf("cache: ready and partial repository document sets require atomic publication")
	}
	return upsertRepositoryDocRevisionSet(ctx, s.db, normalized)
}

type repositoryDocExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func upsertRepositoryDocRevisionSet(ctx context.Context, exec repositoryDocExecutor, normalized RepositoryDocRevisionSet) error {
	result, err := exec.ExecContext(ctx, `INSERT INTO repo_doc_revision_sets (repo_id, revision_set_id, source_registration_id, source_registration_generation, git_store_ref, worktree_ref, object_format, commit_oid, requested_revision, policy_hash, policy_source, config_digest, overlay_digest, chunk_policy_id, processing_policy_id, namespace_id, state, eligible_files, eligible_chunks, embedded_chunks, reused_chunks, failed_chunks, excluded_files, missing_objects, created_at, updated_at, completed_at, last_error_class)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(repo_id, revision_set_id) DO UPDATE SET source_registration_id=excluded.source_registration_id, source_registration_generation=excluded.source_registration_generation, git_store_ref=excluded.git_store_ref, worktree_ref=excluded.worktree_ref, object_format=excluded.object_format, commit_oid=excluded.commit_oid, requested_revision=excluded.requested_revision, policy_hash=excluded.policy_hash, policy_source=excluded.policy_source, config_digest=excluded.config_digest, overlay_digest=excluded.overlay_digest, chunk_policy_id=excluded.chunk_policy_id, processing_policy_id=excluded.processing_policy_id, namespace_id=excluded.namespace_id, state=excluded.state, eligible_files=excluded.eligible_files, eligible_chunks=excluded.eligible_chunks, embedded_chunks=excluded.embedded_chunks, reused_chunks=excluded.reused_chunks, failed_chunks=excluded.failed_chunks, excluded_files=excluded.excluded_files, missing_objects=excluded.missing_objects, updated_at=excluded.updated_at, completed_at=excluded.completed_at, last_error_class=excluded.last_error_class
WHERE repo_doc_revision_sets.state != 'ready'`,
		normalized.RepoID, normalized.ID, normalized.SourceRegistrationID, normalized.SourceRegistrationGeneration, normalized.GitStoreRef, normalized.WorktreeRef, normalized.ObjectFormat, normalized.CommitOID, normalized.RequestedRevision, normalized.PolicyHash, normalized.PolicySource, normalized.ConfigDigest, normalized.OverlayDigest, normalized.ChunkPolicyID, normalized.ProcessingPolicyID, normalized.NamespaceID, normalized.State, normalized.EligibleFiles, normalized.EligibleChunks, normalized.EmbeddedChunks, normalized.ReusedChunks, normalized.FailedChunks, normalized.ExcludedFiles, normalized.MissingObjects, formatTime(normalized.CreatedAt), formatTime(normalized.UpdatedAt), formatOptionalTime(normalized.CompletedAt), normalized.LastErrorClass)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrRepositoryDocRevisionSetPublished
	}
	return nil
}

func (s *SQLiteStore) GetRepositoryDocRevisionSet(ctx context.Context, repoID, setID string) (RepositoryDocRevisionSet, error) {
	row := s.db.QueryRowContext(ctx, repositoryDocRevisionSetSelect+` WHERE repo_id = ? AND revision_set_id = ?`, repoID, setID)
	set, err := scanRepositoryDocRevisionSet(row)
	if errors.Is(err, sql.ErrNoRows) {
		return RepositoryDocRevisionSet{}, ErrNotFound
	}
	return set, err
}

func (s *SQLiteStore) ListRepositoryDocRevisionSets(ctx context.Context, filter RepositoryDocRevisionSetFilter) ([]RepositoryDocRevisionSet, error) {
	query := repositoryDocRevisionSetSelect + ` WHERE repo_id = ?`
	args := []any{filter.RepoID}
	for _, item := range []struct {
		column string
		value  string
	}{{"source_registration_id", filter.SourceRegistrationID}, {"git_store_ref", filter.GitStoreRef}, {"commit_oid", filter.CommitOID}, {"policy_hash", filter.PolicyHash}, {"overlay_digest", filter.OverlayDigest}, {"chunk_policy_id", filter.ChunkPolicyID}, {"namespace_id", filter.NamespaceID}, {"state", filter.State}} {
		if item.value != "" {
			query += ` AND ` + item.column + ` = ?`
			args = append(args, item.value)
		}
	}
	if filter.SourceRegistrationGeneration > 0 {
		query += ` AND source_registration_generation = ?`
		args = append(args, filter.SourceRegistrationGeneration)
	}
	if filter.ExactOverlay && filter.OverlayDigest == "" {
		query += ` AND overlay_digest = ''`
	}
	query += ` ORDER BY updated_at DESC, revision_set_id`
	if filter.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, filter.Limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sets []RepositoryDocRevisionSet
	for rows.Next() {
		set, err := scanRepositoryDocRevisionSet(rows)
		if err != nil {
			return nil, err
		}
		sets = append(sets, set)
	}
	return sets, rows.Err()
}

func (s *SQLiteStore) UpsertRepositoryDocChunk(ctx context.Context, chunk RepositoryDocChunk) error {
	normalized, err := normalizeRepositoryDocChunk(chunk)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO repo_doc_chunks (repo_id, chunk_id, object_format, blob_oid, worktree_ref, content_digest, byte_start, byte_end, line_start, line_end, raw_slice_digest, embedding_input_digest, chunk_policy_id, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(repo_id, chunk_id) DO UPDATE SET updated_at=excluded.updated_at
WHERE repo_doc_chunks.content_digest=excluded.content_digest
  AND repo_doc_chunks.byte_start=excluded.byte_start AND repo_doc_chunks.byte_end=excluded.byte_end
  AND repo_doc_chunks.line_start=excluded.line_start AND repo_doc_chunks.line_end=excluded.line_end
  AND repo_doc_chunks.raw_slice_digest=excluded.raw_slice_digest
  AND repo_doc_chunks.embedding_input_digest=excluded.embedding_input_digest
  AND repo_doc_chunks.chunk_policy_id=excluded.chunk_policy_id`,
		normalized.RepoID, normalized.ID, normalized.ObjectFormat, normalized.BlobOID, normalized.WorktreeRef, normalized.ContentDigest, normalized.ByteStart, normalized.ByteEnd, normalized.LineStart, normalized.LineEnd, normalized.RawSliceDigest, normalized.EmbeddingInputDigest, normalized.ChunkPolicyID, formatTime(normalized.CreatedAt), formatTime(normalized.UpdatedAt))
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("cache: repository document chunk identity conflicts with immutable metadata")
	}
	return nil
}

func (s *SQLiteStore) ReplaceRepositoryDocMembership(ctx context.Context, repoID, setID string, memberships []RepositoryDocMembership) (err error) {
	memberships, err = normalizeRepositoryDocMembership(repoID, setID, memberships)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer txRollbackOnError(tx, &err)
	if err = requireBuildingRepositoryDocRevisionSet(ctx, tx, repoID, setID); err != nil {
		return err
	}
	if err = replaceRepositoryDocMembership(ctx, tx, repoID, setID, memberships); err != nil {
		return err
	}
	return tx.Commit()
}

// PublishRepositoryDocRevisionSet atomically replaces the exact membership and
// publishes its terminal counters/state. Readers therefore cannot observe a
// ready or partial set with membership from another publication attempt.
func (s *SQLiteStore) PublishRepositoryDocRevisionSet(ctx context.Context, set RepositoryDocRevisionSet, memberships []RepositoryDocMembership) (err error) {
	normalized, err := normalizeRepositoryDocRevisionSet(set)
	if err != nil {
		return err
	}
	if normalized.State != RepoDocSetReady && normalized.State != RepoDocSetPartial {
		return fmt.Errorf("cache: repository document publication requires ready or partial state")
	}
	memberships, err = normalizeRepositoryDocMembership(normalized.RepoID, normalized.ID, memberships)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer txRollbackOnError(tx, &err)
	var currentState string
	err = tx.QueryRowContext(ctx, `SELECT state FROM repo_doc_revision_sets WHERE repo_id = ? AND revision_set_id = ?`, normalized.RepoID, normalized.ID).Scan(&currentState)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if currentState == RepoDocSetReady {
		return ErrRepositoryDocRevisionSetPublished
	}
	if err = replaceRepositoryDocMembership(ctx, tx, normalized.RepoID, normalized.ID, memberships); err != nil {
		return err
	}
	if err = validateStagedRepositoryDocPublication(ctx, tx, normalized); err != nil {
		return err
	}
	if err = upsertRepositoryDocRevisionSet(ctx, tx, normalized); err != nil {
		return err
	}
	return tx.Commit()
}

// PublishStagedRepositoryDocRevisionSet atomically exposes metadata and
// membership accumulated incrementally while the set was building. This keeps
// indexing memory bounded without making an incomplete set visible to readers.
func (s *SQLiteStore) PublishStagedRepositoryDocRevisionSet(ctx context.Context, set RepositoryDocRevisionSet) (err error) {
	normalized, err := normalizeRepositoryDocRevisionSet(set)
	if err != nil {
		return err
	}
	if normalized.State != RepoDocSetReady && normalized.State != RepoDocSetPartial {
		return fmt.Errorf("cache: repository document publication requires ready or partial state")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer txRollbackOnError(tx, &err)
	var currentState string
	err = tx.QueryRowContext(ctx, `SELECT state FROM repo_doc_revision_sets WHERE repo_id = ? AND revision_set_id = ?`, normalized.RepoID, normalized.ID).Scan(&currentState)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if currentState == RepoDocSetReady {
		return ErrRepositoryDocRevisionSetPublished
	}
	if err = validateStagedRepositoryDocPublication(ctx, tx, normalized); err != nil {
		return err
	}
	if err = upsertRepositoryDocRevisionSet(ctx, tx, normalized); err != nil {
		return err
	}
	return tx.Commit()
}

func validateStagedRepositoryDocPublication(ctx context.Context, tx *sql.Tx, set RepositoryDocRevisionSet) error {
	var membershipCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM repo_doc_membership WHERE repo_id = ? AND revision_set_id = ?`, set.RepoID, set.ID).Scan(&membershipCount); err != nil {
		return err
	}
	if membershipCount != set.EligibleChunks {
		return fmt.Errorf("cache: repository document publication membership count does not match eligible chunks")
	}
	var exclusionCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM repo_doc_exclusions WHERE repo_id = ? AND revision_set_id = ?`, set.RepoID, set.ID).Scan(&exclusionCount); err != nil {
		return err
	}
	if exclusionCount != set.ExcludedFiles {
		return fmt.Errorf("cache: repository document publication exclusion count does not match excluded files")
	}
	if set.EmbeddedChunks < 0 || set.ReusedChunks < 0 || set.FailedChunks < 0 || set.MissingObjects < 0 || set.EmbeddedChunks+set.ReusedChunks > set.EligibleChunks {
		return fmt.Errorf("cache: repository document publication counters are inconsistent")
	}
	if set.State != RepoDocSetReady {
		return nil
	}
	if set.FailedChunks != 0 || set.MissingObjects != 0 || set.EmbeddedChunks+set.ReusedChunks != set.EligibleChunks {
		return fmt.Errorf("cache: ready repository document publication has incomplete coverage")
	}
	var missingVectors int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM (
		SELECT DISTINCT m.chunk_id
		FROM repo_doc_membership m
		LEFT JOIN repo_doc_vectors v ON v.repo_id = m.repo_id AND v.namespace_id = ? AND v.chunk_id = m.chunk_id
		WHERE m.repo_id = ? AND m.revision_set_id = ? AND v.chunk_id IS NULL
	)`, set.NamespaceID, set.RepoID, set.ID).Scan(&missingVectors); err != nil {
		return err
	}
	if missingVectors != 0 {
		return fmt.Errorf("cache: ready repository document publication is missing an exact-namespace vector")
	}
	var namespaceDimensions int
	var namespaceDType string
	if err := tx.QueryRowContext(ctx, `SELECT dimensions, dtype FROM embedding_namespaces WHERE repo_id = ? AND namespace_id = ?`, set.RepoID, set.NamespaceID).Scan(&namespaceDimensions, &namespaceDType); err != nil {
		return fmt.Errorf("cache: ready repository document publication namespace is unavailable: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT v.vector, v.dimensions, v.dtype, v.vector_hash
		FROM repo_doc_membership m
		JOIN repo_doc_vectors v ON v.repo_id = m.repo_id AND v.namespace_id = ? AND v.chunk_id = m.chunk_id
		WHERE m.repo_id = ? AND m.revision_set_id = ?`, set.NamespaceID, set.RepoID, set.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var encoded []byte
		var dimensions int
		var dtype, vectorHash string
		if err := rows.Scan(&encoded, &dimensions, &dtype, &vectorHash); err != nil {
			return err
		}
		sum := sha256.Sum256(encoded)
		if dimensions != namespaceDimensions || dtype != namespaceDType || dtype != "float32" || len(encoded) != dimensions*4 || vectorHash != hex.EncodeToString(sum[:]) {
			return fmt.Errorf("cache: ready repository document publication vector violates namespace contract")
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return nil
}

func replaceRepositoryDocMembership(ctx context.Context, tx *sql.Tx, repoID, setID string, memberships []RepositoryDocMembership) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM repo_doc_membership WHERE repo_id = ? AND revision_set_id = ?`, repoID, setID); err != nil {
		return err
	}
	for _, membership := range memberships {
		if _, err := tx.ExecContext(ctx, `INSERT INTO repo_doc_membership (repo_id, revision_set_id, path, chunk_id, authority, ordinal, blob_oid, worktree_ref, content_digest) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, membership.RepoID, membership.RevisionSetID, membership.Path, membership.ChunkID, membership.Authority, membership.Ordinal, membership.BlobOID, membership.WorktreeRef, membership.ContentDigest); err != nil {
			return err
		}
	}
	return nil
}

func normalizeRepositoryDocMembership(repoID, setID string, memberships []RepositoryDocMembership) ([]RepositoryDocMembership, error) {
	if strings.TrimSpace(repoID) == "" || strings.TrimSpace(setID) == "" {
		return nil, fmt.Errorf("cache: repository document membership requires repo and revision set ids")
	}
	normalized := append([]RepositoryDocMembership(nil), memberships...)
	for idx := range normalized {
		membership := &normalized[idx]
		if membership.RepoID == "" {
			membership.RepoID = repoID
		}
		if membership.RevisionSetID == "" {
			membership.RevisionSetID = setID
		}
		if membership.RepoID != repoID || membership.RevisionSetID != setID || membership.Path == "" || membership.ChunkID == "" || membership.Authority == "" || membership.ContentDigest == "" {
			return nil, fmt.Errorf("cache: invalid repository document membership")
		}
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		if normalized[i].Path != normalized[j].Path {
			return normalized[i].Path < normalized[j].Path
		}
		if normalized[i].Ordinal != normalized[j].Ordinal {
			return normalized[i].Ordinal < normalized[j].Ordinal
		}
		return normalized[i].ChunkID < normalized[j].ChunkID
	})
	return normalized, nil
}

func (s *SQLiteStore) ListRepositoryDocMembership(ctx context.Context, repoID, setID string) ([]RepositoryDocMembership, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT repo_id, revision_set_id, path, chunk_id, authority, ordinal, blob_oid, worktree_ref, content_digest FROM repo_doc_membership WHERE repo_id = ? AND revision_set_id = ? ORDER BY path, ordinal, chunk_id`, repoID, setID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var memberships []RepositoryDocMembership
	for rows.Next() {
		var membership RepositoryDocMembership
		if err := rows.Scan(&membership.RepoID, &membership.RevisionSetID, &membership.Path, &membership.ChunkID, &membership.Authority, &membership.Ordinal, &membership.BlobOID, &membership.WorktreeRef, &membership.ContentDigest); err != nil {
			return nil, err
		}
		memberships = append(memberships, membership)
	}
	return memberships, rows.Err()
}

func (s *SQLiteStore) UpsertRepositoryDocMembership(ctx context.Context, membership RepositoryDocMembership) error {
	normalized, err := normalizeRepositoryDocMembership(membership.RepoID, membership.RevisionSetID, []RepositoryDocMembership{membership})
	if err != nil {
		return err
	}
	membership = normalized[0]
	result, err := s.db.ExecContext(ctx, `INSERT INTO repo_doc_membership (repo_id, revision_set_id, path, chunk_id, authority, ordinal, blob_oid, worktree_ref, content_digest)
SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?
WHERE EXISTS (SELECT 1 FROM repo_doc_revision_sets WHERE repo_id = ? AND revision_set_id = ? AND state = 'building')
ON CONFLICT(repo_id, revision_set_id, path, chunk_id) DO UPDATE SET authority=excluded.authority, ordinal=excluded.ordinal, blob_oid=excluded.blob_oid, worktree_ref=excluded.worktree_ref, content_digest=excluded.content_digest`, membership.RepoID, membership.RevisionSetID, membership.Path, membership.ChunkID, membership.Authority, membership.Ordinal, membership.BlobOID, membership.WorktreeRef, membership.ContentDigest, membership.RepoID, membership.RevisionSetID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrRepositoryDocRevisionSetPublished
	}
	return nil
}

func requireBuildingRepositoryDocRevisionSet(ctx context.Context, queryer repositoryDocQuerier, repoID, setID string) error {
	var state string
	err := queryer.QueryRowContext(ctx, `SELECT state FROM repo_doc_revision_sets WHERE repo_id = ? AND revision_set_id = ?`, repoID, setID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if state != RepoDocSetBuilding {
		return ErrRepositoryDocRevisionSetPublished
	}
	return nil
}

func (s *SQLiteStore) ReplaceRepositoryDocExclusions(ctx context.Context, repoID, setID string, exclusions []RepositoryDocExclusion) (err error) {
	exclusions, err = normalizeRepositoryDocExclusions(repoID, setID, exclusions)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer txRollbackOnError(tx, &err)
	if err = requireBuildingRepositoryDocRevisionSet(ctx, tx, repoID, setID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM repo_doc_exclusions WHERE repo_id = ? AND revision_set_id = ?`, repoID, setID); err != nil {
		return err
	}
	for _, exclusion := range exclusions {
		if _, err = tx.ExecContext(ctx, `INSERT INTO repo_doc_exclusions (repo_id, revision_set_id, path, authority, blob_oid, reason_code) VALUES (?, ?, ?, ?, ?, ?)`, exclusion.RepoID, exclusion.RevisionSetID, exclusion.Path, exclusion.Authority, exclusion.BlobOID, exclusion.ReasonCode); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLiteStore) UpsertRepositoryDocExclusion(ctx context.Context, exclusion RepositoryDocExclusion) error {
	normalized, err := normalizeRepositoryDocExclusions(exclusion.RepoID, exclusion.RevisionSetID, []RepositoryDocExclusion{exclusion})
	if err != nil {
		return err
	}
	exclusion = normalized[0]
	result, err := s.db.ExecContext(ctx, `INSERT INTO repo_doc_exclusions (repo_id, revision_set_id, path, authority, blob_oid, reason_code)
SELECT ?, ?, ?, ?, ?, ?
WHERE EXISTS (SELECT 1 FROM repo_doc_revision_sets WHERE repo_id = ? AND revision_set_id = ? AND state = 'building')
ON CONFLICT(repo_id, revision_set_id, path, reason_code) DO UPDATE SET authority=excluded.authority, blob_oid=excluded.blob_oid`, exclusion.RepoID, exclusion.RevisionSetID, exclusion.Path, exclusion.Authority, exclusion.BlobOID, exclusion.ReasonCode, exclusion.RepoID, exclusion.RevisionSetID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrRepositoryDocRevisionSetPublished
	}
	return nil
}

func (s *SQLiteStore) ListRepositoryDocExclusions(ctx context.Context, repoID, setID string) ([]RepositoryDocExclusion, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT repo_id, revision_set_id, path, authority, blob_oid, reason_code FROM repo_doc_exclusions WHERE repo_id = ? AND revision_set_id = ? ORDER BY path, reason_code`, repoID, setID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var exclusions []RepositoryDocExclusion
	for rows.Next() {
		var exclusion RepositoryDocExclusion
		if err := rows.Scan(&exclusion.RepoID, &exclusion.RevisionSetID, &exclusion.Path, &exclusion.Authority, &exclusion.BlobOID, &exclusion.ReasonCode); err != nil {
			return nil, err
		}
		exclusions = append(exclusions, exclusion)
	}
	return exclusions, rows.Err()
}

func normalizeRepositoryDocExclusions(repoID, setID string, exclusions []RepositoryDocExclusion) ([]RepositoryDocExclusion, error) {
	if strings.TrimSpace(repoID) == "" || strings.TrimSpace(setID) == "" {
		return nil, fmt.Errorf("cache: repository document exclusions require repo and revision set ids")
	}
	normalized := append([]RepositoryDocExclusion(nil), exclusions...)
	for idx := range normalized {
		exclusion := &normalized[idx]
		if exclusion.RepoID == "" {
			exclusion.RepoID = repoID
		}
		if exclusion.RevisionSetID == "" {
			exclusion.RevisionSetID = setID
		}
		if exclusion.RepoID != repoID || exclusion.RevisionSetID != setID || strings.TrimSpace(exclusion.Path) == "" || strings.TrimSpace(exclusion.Authority) == "" || strings.TrimSpace(exclusion.ReasonCode) == "" {
			return nil, fmt.Errorf("cache: invalid repository document exclusion")
		}
	}
	return normalized, nil
}

func (s *SQLiteStore) UpsertRepositoryDocVector(ctx context.Context, vector RepositoryDocVector) error {
	if vector.RepoID == "" || vector.NamespaceID == "" || vector.ChunkID == "" || len(vector.Vector) == 0 || vector.Dimensions <= 0 || vector.DType == "" {
		return fmt.Errorf("cache: repository document vector identity is incomplete")
	}
	if vector.VectorHash == "" {
		sum := sha256.Sum256(vector.Vector)
		vector.VectorHash = hex.EncodeToString(sum[:])
	}
	if vector.EmbeddedAt.IsZero() {
		vector.EmbeddedAt = time.Now().UTC()
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO repo_doc_vectors (repo_id, namespace_id, chunk_id, vector, dimensions, dtype, vector_hash, embedded_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(repo_id, namespace_id, chunk_id) DO NOTHING`, vector.RepoID, vector.NamespaceID, vector.ChunkID, vector.Vector, vector.Dimensions, vector.DType, vector.VectorHash, formatTime(vector.EmbeddedAt))
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil || inserted == 1 {
		return err
	}
	current, err := s.GetRepositoryDocVector(ctx, vector.RepoID, vector.NamespaceID, vector.ChunkID)
	if err != nil {
		return err
	}
	if current.Dimensions != vector.Dimensions || current.DType != vector.DType || current.VectorHash != vector.VectorHash || !bytes.Equal(current.Vector, vector.Vector) {
		return ErrRepositoryDocVectorConflict
	}
	return nil
}

func (s *SQLiteStore) GetRepositoryDocVector(ctx context.Context, repoID, namespaceID, chunkID string) (RepositoryDocVector, error) {
	var vector RepositoryDocVector
	var embeddedRaw string
	err := s.db.QueryRowContext(ctx, `SELECT repo_id, namespace_id, chunk_id, vector, dimensions, dtype, vector_hash, embedded_at FROM repo_doc_vectors WHERE repo_id = ? AND namespace_id = ? AND chunk_id = ?`, repoID, namespaceID, chunkID).Scan(&vector.RepoID, &vector.NamespaceID, &vector.ChunkID, &vector.Vector, &vector.Dimensions, &vector.DType, &vector.VectorHash, &embeddedRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return RepositoryDocVector{}, ErrNotFound
	}
	if err != nil {
		return RepositoryDocVector{}, err
	}
	vector.EmbeddedAt, err = time.Parse(time.RFC3339Nano, embeddedRaw)
	return vector, err
}

func (s *SQLiteStore) ListRepositoryDocCandidates(ctx context.Context, repoID, setID, namespaceID string) ([]RepositoryDocCandidate, error) {
	return listRepositoryDocCandidates(ctx, s.db, repoID, setID, namespaceID)
}

type repositoryDocQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func listRepositoryDocCandidates(ctx context.Context, queryer repositoryDocQuerier, repoID, setID, namespaceID string) ([]RepositoryDocCandidate, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT m.repo_id, m.revision_set_id, m.path, m.chunk_id, m.authority, m.ordinal, m.blob_oid, m.worktree_ref, m.content_digest, c.object_format, c.byte_start, c.byte_end, c.line_start, c.line_end, c.raw_slice_digest, c.embedding_input_digest, c.chunk_policy_id, COALESCE(v.namespace_id, ''), COALESCE(v.vector, X''), COALESCE(v.dimensions, 0), COALESCE(v.dtype, ''), COALESCE(v.vector_hash, '')
FROM repo_doc_membership m
JOIN repo_doc_chunks c ON c.repo_id = m.repo_id AND c.chunk_id = m.chunk_id
LEFT JOIN repo_doc_vectors v ON v.repo_id = m.repo_id AND v.chunk_id = m.chunk_id AND v.namespace_id = ?
WHERE m.repo_id = ? AND m.revision_set_id = ?
ORDER BY m.path, m.ordinal, m.chunk_id`, namespaceID, repoID, setID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var candidates []RepositoryDocCandidate
	for rows.Next() {
		var candidate RepositoryDocCandidate
		if err := rows.Scan(&candidate.RepoID, &candidate.RevisionSetID, &candidate.Path, &candidate.ChunkID, &candidate.Authority, &candidate.Ordinal, &candidate.BlobOID, &candidate.WorktreeRef, &candidate.ContentDigest, &candidate.ObjectFormat, &candidate.ByteStart, &candidate.ByteEnd, &candidate.LineStart, &candidate.LineEnd, &candidate.RawSliceDigest, &candidate.EmbeddingInputDigest, &candidate.ChunkPolicyID, &candidate.NamespaceID, &candidate.Vector, &candidate.Dimensions, &candidate.DType, &candidate.VectorHash); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

// LoadRepositoryDocSearchSnapshot loads an exact published set and all of its
// candidates under one SQLite read transaction. It intentionally accepts a set
// ID rather than a loose filter so callers must resolve the full semantic
// identity before hydrating candidates.
func (s *SQLiteStore) LoadRepositoryDocSearchSnapshot(ctx context.Context, repoID, setID, namespaceID string) (snapshot RepositoryDocSearchSnapshot, err error) {
	if strings.TrimSpace(repoID) == "" || strings.TrimSpace(setID) == "" || strings.TrimSpace(namespaceID) == "" {
		return RepositoryDocSearchSnapshot{}, fmt.Errorf("cache: repository document search snapshot identity is incomplete")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return RepositoryDocSearchSnapshot{}, err
	}
	defer txRollbackOnError(tx, &err)
	snapshot.RevisionSet, err = scanRepositoryDocRevisionSet(tx.QueryRowContext(ctx, repositoryDocRevisionSetSelect+` WHERE repo_id = ? AND revision_set_id = ?`, repoID, setID))
	if errors.Is(err, sql.ErrNoRows) {
		return RepositoryDocSearchSnapshot{}, ErrNotFound
	}
	if err != nil {
		return RepositoryDocSearchSnapshot{}, err
	}
	if snapshot.RevisionSet.NamespaceID != namespaceID {
		return RepositoryDocSearchSnapshot{}, ErrNotFound
	}
	if snapshot.RevisionSet.State != RepoDocSetReady {
		return RepositoryDocSearchSnapshot{}, ErrNotFound
	}
	snapshot.Candidates, err = listRepositoryDocCandidates(ctx, tx, repoID, setID, namespaceID)
	if err != nil {
		return RepositoryDocSearchSnapshot{}, err
	}
	if err = tx.Commit(); err != nil {
		return RepositoryDocSearchSnapshot{}, err
	}
	return snapshot, nil
}

func (s *SQLiteStore) DeleteRepositoryDocRevisionSet(ctx context.Context, repoID, setID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM repo_doc_revision_sets WHERE repo_id = ? AND revision_set_id = ?`, repoID, setID)
	return err
}

// ListRepositoryDocPendingVectorIdentities returns the exact resumable
// checkpoint set. It deliberately excludes terminal superseded/unavailable
// sets so their provider handoffs are treated as orphans by checkpoint GC.
func (s *SQLiteStore) ListRepositoryDocPendingVectorIdentities(ctx context.Context) ([]RepositoryDocPendingVectorIdentity, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT DISTINCT rs.repo_id, rs.namespace_id, m.chunk_id
FROM repo_doc_revision_sets rs
JOIN repo_doc_membership m
  ON m.repo_id = rs.repo_id AND m.revision_set_id = rs.revision_set_id
LEFT JOIN repo_doc_vectors v
  ON v.repo_id = rs.repo_id AND v.namespace_id = rs.namespace_id AND v.chunk_id = m.chunk_id
WHERE rs.namespace_id <> ''
  AND rs.state IN (?, ?, ?)
  AND v.chunk_id IS NULL
ORDER BY rs.repo_id, rs.namespace_id, m.chunk_id`, RepoDocSetBuilding, RepoDocSetPartial, RepoDocSetBlocked)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []RepositoryDocPendingVectorIdentity
	for rows.Next() {
		var value RepositoryDocPendingVectorIdentity
		if err := rows.Scan(&value.RepoID, &value.NamespaceID, &value.ChunkID); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *SQLiteStore) DeleteUnreferencedRepositoryDocData(ctx context.Context, repoID string) (chunks, vectors int64, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer txRollbackOnError(tx, &err)
	vectorResult, err := tx.ExecContext(ctx, `DELETE FROM repo_doc_vectors WHERE repo_id = ? AND chunk_id NOT IN (SELECT DISTINCT chunk_id FROM repo_doc_membership WHERE repo_id = ?)`, repoID, repoID)
	if err != nil {
		return 0, 0, err
	}
	chunkResult, err := tx.ExecContext(ctx, `DELETE FROM repo_doc_chunks WHERE repo_id = ? AND chunk_id NOT IN (SELECT DISTINCT chunk_id FROM repo_doc_membership WHERE repo_id = ?)`, repoID, repoID)
	if err != nil {
		return 0, 0, err
	}
	vectors, _ = vectorResult.RowsAffected()
	chunks, _ = chunkResult.RowsAffected()
	return chunks, vectors, tx.Commit()
}

// PruneRepositoryDocRevisionSets applies deterministic metadata retention.
// Building sets are never removed. Ready committed and overlay sets are
// retained independently so an explicit worktree cohort cannot evict durable
// commit history. Document bytes are not involved: Git remains their authority.
func (s *SQLiteStore) PruneRepositoryDocRevisionSets(ctx context.Context, repoID string, policy RepositoryDocRetentionPolicy) (RepositoryDocGCResult, error) {
	if strings.TrimSpace(repoID) == "" {
		return RepositoryDocGCResult{}, fmt.Errorf("cache: repository document GC requires repo id")
	}
	if policy.RetainCommittedPerIdentity <= 0 {
		policy.RetainCommittedPerIdentity = 8
	}
	if policy.RetainOverlaysPerIdentity < 0 {
		policy.RetainOverlaysPerIdentity = 0
	}
	sets, err := s.ListRepositoryDocRevisionSets(ctx, RepositoryDocRevisionSetFilter{RepoID: repoID})
	if err != nil {
		return RepositoryDocGCResult{}, err
	}
	protected := make(map[string]bool, len(policy.ProtectedSetIDs))
	for _, setID := range policy.ProtectedSetIDs {
		if setID = strings.TrimSpace(setID); setID != "" {
			protected[setID] = true
		}
	}
	// ListRepositoryDocRevisionSets is newest-first. Protect the newest ready
	// committed set for every exact local identity so byte/age GC cannot hide
	// current searchable coverage. An active overlay is protected explicitly by
	// the caller; inactive overlays remain eligible for their short grace period.
	latestReady := map[string]bool{}
	for _, set := range sets {
		if set.State != RepoDocSetReady {
			continue
		}
		kind := "commit"
		if set.OverlayDigest != "" {
			continue
		}
		key := strings.Join([]string{set.GitStoreRef, set.PolicyHash, set.ChunkPolicyID, set.NamespaceID, kind}, "\x00")
		if !latestReady[key] {
			latestReady[key] = true
			protected[set.ID] = true
		}
	}
	counts := map[string]int{}
	var deleteIDs []string
	for _, set := range sets {
		terminalExpired := !policy.TerminalCutoff.IsZero() && set.UpdatedAt.Before(policy.TerminalCutoff) && set.State != RepoDocSetReady
		if set.State == RepoDocSetBuilding && !terminalExpired {
			continue
		}
		kind := "commit"
		limit := policy.RetainCommittedPerIdentity
		ageExpired := !policy.CommittedCutoff.IsZero() && set.UpdatedAt.Before(policy.CommittedCutoff)
		if set.OverlayDigest != "" {
			kind = "overlay"
			limit = policy.RetainOverlaysPerIdentity
			ageExpired = !policy.OverlayCutoff.IsZero() && set.UpdatedAt.Before(policy.OverlayCutoff)
		}
		key := strings.Join([]string{set.GitStoreRef, set.PolicyHash, set.ChunkPolicyID, set.NamespaceID, kind}, "\x00")
		counts[key]++
		if protected[set.ID] {
			continue
		}
		if terminalExpired || ageExpired || (limit > 0 && counts[key] > limit) {
			deleteIDs = append(deleteIDs, set.ID)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RepositoryDocGCResult{}, err
	}
	defer tx.Rollback()
	var result RepositoryDocGCResult
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(vector)), 0) FROM repo_doc_vectors WHERE repo_id = ?`, repoID).Scan(&result.VectorBytesBefore); err != nil {
		return RepositoryDocGCResult{}, err
	}
	deletedSet := map[string]bool{}
	for _, setID := range deleteIDs {
		deleted, execErr := tx.ExecContext(ctx, `DELETE FROM repo_doc_revision_sets WHERE repo_id = ? AND revision_set_id = ?`, repoID, setID)
		if execErr != nil {
			return RepositoryDocGCResult{}, execErr
		}
		rows, _ := deleted.RowsAffected()
		result.RevisionSetsDeleted += rows
		deletedSet[setID] = rows > 0
	}
	cleanup := func() error {
		vectors, cleanupErr := tx.ExecContext(ctx, `DELETE FROM repo_doc_vectors WHERE repo_id = ? AND chunk_id NOT IN (SELECT DISTINCT chunk_id FROM repo_doc_membership WHERE repo_id = ?)`, repoID, repoID)
		if cleanupErr != nil {
			return cleanupErr
		}
		chunks, cleanupErr := tx.ExecContext(ctx, `DELETE FROM repo_doc_chunks WHERE repo_id = ? AND chunk_id NOT IN (SELECT DISTINCT chunk_id FROM repo_doc_membership WHERE repo_id = ?)`, repoID, repoID)
		if cleanupErr != nil {
			return cleanupErr
		}
		vectorsDeleted, _ := vectors.RowsAffected()
		chunksDeleted, _ := chunks.RowsAffected()
		result.VectorsDeleted += vectorsDeleted
		result.ChunksDeleted += chunksDeleted
		return nil
	}
	if err := cleanup(); err != nil {
		return RepositoryDocGCResult{}, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(vector)), 0) FROM repo_doc_vectors WHERE repo_id = ?`, repoID).Scan(&result.VectorBytesAfter); err != nil {
		return RepositoryDocGCResult{}, err
	}
	if policy.MaxVectorBytes > 0 && result.VectorBytesAfter > policy.MaxVectorBytes {
		// The source list is newest-first, so byte-pressure eviction walks it in
		// reverse order. Shared vectors are removed only after their last set
		// membership disappears; recalculation keeps the decision deterministic.
		for index := len(sets) - 1; index >= 0 && result.VectorBytesAfter > policy.MaxVectorBytes; index-- {
			set := sets[index]
			if deletedSet[set.ID] || protected[set.ID] || set.State == RepoDocSetBuilding {
				continue
			}
			deleted, deleteErr := tx.ExecContext(ctx, `DELETE FROM repo_doc_revision_sets WHERE repo_id = ? AND revision_set_id = ?`, repoID, set.ID)
			if deleteErr != nil {
				return RepositoryDocGCResult{}, deleteErr
			}
			rows, _ := deleted.RowsAffected()
			if rows == 0 {
				continue
			}
			deletedSet[set.ID] = true
			result.RevisionSetsDeleted += rows
			if err := cleanup(); err != nil {
				return RepositoryDocGCResult{}, err
			}
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(vector)), 0) FROM repo_doc_vectors WHERE repo_id = ?`, repoID).Scan(&result.VectorBytesAfter); err != nil {
				return RepositoryDocGCResult{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return RepositoryDocGCResult{}, err
	}
	return result, nil
}

const repositoryDocRevisionSetSelect = `SELECT repo_id, revision_set_id, source_registration_id, source_registration_generation, git_store_ref, worktree_ref, object_format, commit_oid, requested_revision, policy_hash, policy_source, config_digest, overlay_digest, chunk_policy_id, processing_policy_id, namespace_id, state, eligible_files, eligible_chunks, embedded_chunks, reused_chunks, failed_chunks, excluded_files, missing_objects, created_at, updated_at, completed_at, last_error_class FROM repo_doc_revision_sets`

func scanRepositoryDocRevisionSet(scanner rowScanner) (RepositoryDocRevisionSet, error) {
	var set RepositoryDocRevisionSet
	var createdRaw, updatedRaw, completedRaw string
	err := scanner.Scan(&set.RepoID, &set.ID, &set.SourceRegistrationID, &set.SourceRegistrationGeneration, &set.GitStoreRef, &set.WorktreeRef, &set.ObjectFormat, &set.CommitOID, &set.RequestedRevision, &set.PolicyHash, &set.PolicySource, &set.ConfigDigest, &set.OverlayDigest, &set.ChunkPolicyID, &set.ProcessingPolicyID, &set.NamespaceID, &set.State, &set.EligibleFiles, &set.EligibleChunks, &set.EmbeddedChunks, &set.ReusedChunks, &set.FailedChunks, &set.ExcludedFiles, &set.MissingObjects, &createdRaw, &updatedRaw, &completedRaw, &set.LastErrorClass)
	if err != nil {
		return RepositoryDocRevisionSet{}, err
	}
	set.CreatedAt, err = time.Parse(time.RFC3339Nano, createdRaw)
	if err != nil {
		return RepositoryDocRevisionSet{}, err
	}
	set.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedRaw)
	if err != nil {
		return RepositoryDocRevisionSet{}, err
	}
	if completedRaw != "" {
		set.CompletedAt, err = time.Parse(time.RFC3339Nano, completedRaw)
	}
	return set, err
}

func normalizeRepositoryDocRevisionSet(set RepositoryDocRevisionSet) (RepositoryDocRevisionSet, error) {
	if set.RepoID == "" || set.ID == "" || set.GitStoreRef == "" || set.ObjectFormat == "" || set.CommitOID == "" || set.PolicyHash == "" || set.PolicySource == "" || set.ChunkPolicyID == "" || set.State == "" {
		return RepositoryDocRevisionSet{}, fmt.Errorf("cache: repository document revision set identity is incomplete")
	}
	if set.SourceRegistrationGeneration < 0 {
		return RepositoryDocRevisionSet{}, fmt.Errorf("cache: repository document source registration generation is invalid")
	}
	if set.ProcessingPolicyID == "" {
		set.ProcessingPolicyID = set.ChunkPolicyID
	}
	if set.CreatedAt.IsZero() {
		set.CreatedAt = time.Now().UTC()
	}
	if set.UpdatedAt.IsZero() {
		set.UpdatedAt = set.CreatedAt
	}
	return set, nil
}

func normalizeRepositoryDocChunk(chunk RepositoryDocChunk) (RepositoryDocChunk, error) {
	if chunk.RepoID == "" || chunk.ID == "" || chunk.ObjectFormat == "" || chunk.ContentDigest == "" || chunk.RawSliceDigest == "" || chunk.EmbeddingInputDigest == "" || chunk.ChunkPolicyID == "" || chunk.ByteStart < 0 || chunk.ByteEnd <= chunk.ByteStart || chunk.LineStart <= 0 || chunk.LineEnd < chunk.LineStart {
		return RepositoryDocChunk{}, fmt.Errorf("cache: repository document chunk identity is incomplete")
	}
	if chunk.BlobOID == "" && chunk.WorktreeRef == "" {
		return RepositoryDocChunk{}, fmt.Errorf("cache: repository document chunk requires blob or worktree authority")
	}
	if chunk.CreatedAt.IsZero() {
		chunk.CreatedAt = time.Now().UTC()
	}
	if chunk.UpdatedAt.IsZero() {
		chunk.UpdatedAt = chunk.CreatedAt
	}
	return chunk, nil
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func formatOptionalTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return formatTime(value)
}
