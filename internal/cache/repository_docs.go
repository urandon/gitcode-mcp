package cache

import (
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

type RepositoryDocRevisionSet struct {
	RepoID            string
	ID                string
	GitStoreRef       string
	WorktreeRef       string
	ObjectFormat      string
	CommitOID         string
	RequestedRevision string
	PolicyHash        string
	PolicySource      string
	ConfigDigest      string
	OverlayDigest     string
	ChunkPolicyID     string
	NamespaceID       string
	State             string
	EligibleFiles     int
	EligibleChunks    int
	EmbeddedChunks    int
	ReusedChunks      int
	FailedChunks      int
	ExcludedFiles     int
	MissingObjects    int
	CreatedAt         time.Time
	UpdatedAt         time.Time
	CompletedAt       time.Time
	LastErrorClass    string
}

type RepositoryDocRevisionSetFilter struct {
	RepoID        string
	GitStoreRef   string
	CommitOID     string
	PolicyHash    string
	OverlayDigest string
	ExactOverlay  bool
	ChunkPolicyID string
	NamespaceID   string
	State         string
	Limit         int
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
	ContentDigest string
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
	_, err = s.db.ExecContext(ctx, `INSERT INTO repo_doc_revision_sets (repo_id, revision_set_id, git_store_ref, worktree_ref, object_format, commit_oid, requested_revision, policy_hash, policy_source, config_digest, overlay_digest, chunk_policy_id, namespace_id, state, eligible_files, eligible_chunks, embedded_chunks, reused_chunks, failed_chunks, excluded_files, missing_objects, created_at, updated_at, completed_at, last_error_class)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(repo_id, revision_set_id) DO UPDATE SET git_store_ref=excluded.git_store_ref, worktree_ref=excluded.worktree_ref, object_format=excluded.object_format, commit_oid=excluded.commit_oid, requested_revision=excluded.requested_revision, policy_hash=excluded.policy_hash, policy_source=excluded.policy_source, config_digest=excluded.config_digest, overlay_digest=excluded.overlay_digest, chunk_policy_id=excluded.chunk_policy_id, namespace_id=excluded.namespace_id, state=excluded.state, eligible_files=excluded.eligible_files, eligible_chunks=excluded.eligible_chunks, embedded_chunks=excluded.embedded_chunks, reused_chunks=excluded.reused_chunks, failed_chunks=excluded.failed_chunks, excluded_files=excluded.excluded_files, missing_objects=excluded.missing_objects, updated_at=excluded.updated_at, completed_at=excluded.completed_at, last_error_class=excluded.last_error_class`,
		normalized.RepoID, normalized.ID, normalized.GitStoreRef, normalized.WorktreeRef, normalized.ObjectFormat, normalized.CommitOID, normalized.RequestedRevision, normalized.PolicyHash, normalized.PolicySource, normalized.ConfigDigest, normalized.OverlayDigest, normalized.ChunkPolicyID, normalized.NamespaceID, normalized.State, normalized.EligibleFiles, normalized.EligibleChunks, normalized.EmbeddedChunks, normalized.ReusedChunks, normalized.FailedChunks, normalized.ExcludedFiles, normalized.MissingObjects, formatTime(normalized.CreatedAt), formatTime(normalized.UpdatedAt), formatOptionalTime(normalized.CompletedAt), normalized.LastErrorClass)
	return err
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
	}{{"git_store_ref", filter.GitStoreRef}, {"commit_oid", filter.CommitOID}, {"policy_hash", filter.PolicyHash}, {"overlay_digest", filter.OverlayDigest}, {"chunk_policy_id", filter.ChunkPolicyID}, {"namespace_id", filter.NamespaceID}, {"state", filter.State}} {
		if item.value != "" {
			query += ` AND ` + item.column + ` = ?`
			args = append(args, item.value)
		}
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
	_, err = s.db.ExecContext(ctx, `INSERT INTO repo_doc_chunks (repo_id, chunk_id, object_format, blob_oid, worktree_ref, content_digest, byte_start, byte_end, line_start, line_end, raw_slice_digest, embedding_input_digest, chunk_policy_id, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(repo_id, chunk_id) DO UPDATE SET object_format=excluded.object_format, blob_oid=excluded.blob_oid, worktree_ref=excluded.worktree_ref, content_digest=excluded.content_digest, byte_start=excluded.byte_start, byte_end=excluded.byte_end, line_start=excluded.line_start, line_end=excluded.line_end, raw_slice_digest=excluded.raw_slice_digest, embedding_input_digest=excluded.embedding_input_digest, chunk_policy_id=excluded.chunk_policy_id, updated_at=excluded.updated_at`,
		normalized.RepoID, normalized.ID, normalized.ObjectFormat, normalized.BlobOID, normalized.WorktreeRef, normalized.ContentDigest, normalized.ByteStart, normalized.ByteEnd, normalized.LineStart, normalized.LineEnd, normalized.RawSliceDigest, normalized.EmbeddingInputDigest, normalized.ChunkPolicyID, formatTime(normalized.CreatedAt), formatTime(normalized.UpdatedAt))
	return err
}

func (s *SQLiteStore) ReplaceRepositoryDocMembership(ctx context.Context, repoID, setID string, memberships []RepositoryDocMembership) (err error) {
	if strings.TrimSpace(repoID) == "" || strings.TrimSpace(setID) == "" {
		return fmt.Errorf("cache: repository document membership requires repo and revision set ids")
	}
	sort.SliceStable(memberships, func(i, j int) bool {
		if memberships[i].Path != memberships[j].Path {
			return memberships[i].Path < memberships[j].Path
		}
		if memberships[i].Ordinal != memberships[j].Ordinal {
			return memberships[i].Ordinal < memberships[j].Ordinal
		}
		return memberships[i].ChunkID < memberships[j].ChunkID
	})
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer txRollbackOnError(tx, &err)
	if _, err = tx.ExecContext(ctx, `DELETE FROM repo_doc_membership WHERE repo_id = ? AND revision_set_id = ?`, repoID, setID); err != nil {
		return err
	}
	for _, membership := range memberships {
		if membership.RepoID == "" {
			membership.RepoID = repoID
		}
		if membership.RevisionSetID == "" {
			membership.RevisionSetID = setID
		}
		if membership.RepoID != repoID || membership.RevisionSetID != setID || membership.Path == "" || membership.ChunkID == "" || membership.Authority == "" || membership.ContentDigest == "" {
			return fmt.Errorf("cache: invalid repository document membership")
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO repo_doc_membership (repo_id, revision_set_id, path, chunk_id, authority, ordinal, blob_oid, content_digest) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, membership.RepoID, membership.RevisionSetID, membership.Path, membership.ChunkID, membership.Authority, membership.Ordinal, membership.BlobOID, membership.ContentDigest); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLiteStore) ListRepositoryDocMembership(ctx context.Context, repoID, setID string) ([]RepositoryDocMembership, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT repo_id, revision_set_id, path, chunk_id, authority, ordinal, blob_oid, content_digest FROM repo_doc_membership WHERE repo_id = ? AND revision_set_id = ? ORDER BY path, ordinal, chunk_id`, repoID, setID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var memberships []RepositoryDocMembership
	for rows.Next() {
		var membership RepositoryDocMembership
		if err := rows.Scan(&membership.RepoID, &membership.RevisionSetID, &membership.Path, &membership.ChunkID, &membership.Authority, &membership.Ordinal, &membership.BlobOID, &membership.ContentDigest); err != nil {
			return nil, err
		}
		memberships = append(memberships, membership)
	}
	return memberships, rows.Err()
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
	_, err := s.db.ExecContext(ctx, `INSERT INTO repo_doc_vectors (repo_id, namespace_id, chunk_id, vector, dimensions, dtype, vector_hash, embedded_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(repo_id, namespace_id, chunk_id) DO UPDATE SET vector=excluded.vector, dimensions=excluded.dimensions, dtype=excluded.dtype, vector_hash=excluded.vector_hash, embedded_at=excluded.embedded_at`, vector.RepoID, vector.NamespaceID, vector.ChunkID, vector.Vector, vector.Dimensions, vector.DType, vector.VectorHash, formatTime(vector.EmbeddedAt))
	return err
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
	rows, err := s.db.QueryContext(ctx, `SELECT m.repo_id, m.revision_set_id, m.path, m.chunk_id, m.authority, m.ordinal, m.blob_oid, m.content_digest, c.object_format, c.worktree_ref, c.byte_start, c.byte_end, c.line_start, c.line_end, c.raw_slice_digest, c.embedding_input_digest, c.chunk_policy_id, COALESCE(v.namespace_id, ''), COALESCE(v.vector, X''), COALESCE(v.dimensions, 0), COALESCE(v.dtype, ''), COALESCE(v.vector_hash, '')
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
		if err := rows.Scan(&candidate.RepoID, &candidate.RevisionSetID, &candidate.Path, &candidate.ChunkID, &candidate.Authority, &candidate.Ordinal, &candidate.BlobOID, &candidate.ContentDigest, &candidate.ObjectFormat, &candidate.WorktreeRef, &candidate.ByteStart, &candidate.ByteEnd, &candidate.LineStart, &candidate.LineEnd, &candidate.RawSliceDigest, &candidate.EmbeddingInputDigest, &candidate.ChunkPolicyID, &candidate.NamespaceID, &candidate.Vector, &candidate.Dimensions, &candidate.DType, &candidate.VectorHash); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

func (s *SQLiteStore) DeleteRepositoryDocRevisionSet(ctx context.Context, repoID, setID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM repo_doc_revision_sets WHERE repo_id = ? AND revision_set_id = ?`, repoID, setID)
	return err
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

const repositoryDocRevisionSetSelect = `SELECT repo_id, revision_set_id, git_store_ref, worktree_ref, object_format, commit_oid, requested_revision, policy_hash, policy_source, config_digest, overlay_digest, chunk_policy_id, namespace_id, state, eligible_files, eligible_chunks, embedded_chunks, reused_chunks, failed_chunks, excluded_files, missing_objects, created_at, updated_at, completed_at, last_error_class FROM repo_doc_revision_sets`

func scanRepositoryDocRevisionSet(scanner rowScanner) (RepositoryDocRevisionSet, error) {
	var set RepositoryDocRevisionSet
	var createdRaw, updatedRaw, completedRaw string
	err := scanner.Scan(&set.RepoID, &set.ID, &set.GitStoreRef, &set.WorktreeRef, &set.ObjectFormat, &set.CommitOID, &set.RequestedRevision, &set.PolicyHash, &set.PolicySource, &set.ConfigDigest, &set.OverlayDigest, &set.ChunkPolicyID, &set.NamespaceID, &set.State, &set.EligibleFiles, &set.EligibleChunks, &set.EmbeddedChunks, &set.ReusedChunks, &set.FailedChunks, &set.ExcludedFiles, &set.MissingObjects, &createdRaw, &updatedRaw, &completedRaw, &set.LastErrorClass)
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
