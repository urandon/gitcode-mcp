package cache

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

func EmbeddingNamespaceID(identity EmbeddingNamespaceIdentity) string {
	parts := []string{
		identity.ProviderID,
		identity.ProviderType,
		identity.ModelID,
		identity.ModelRevision,
		fmt.Sprintf("%d", identity.Dimensions),
		identity.DType,
		identity.Normalization,
		identity.DocumentInstructionID,
		identity.QueryInstructionID,
		identity.ChunkPolicyID,
		identity.LanguagePolicyID,
		identity.ConfigHash,
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "embns-" + hex.EncodeToString(sum[:16])
}

func (s *SQLiteStore) UpsertEmbeddingNamespace(ctx context.Context, namespace EmbeddingNamespace) (EmbeddingNamespace, error) {
	normalized, err := normalizeEmbeddingNamespace(namespace)
	if err != nil {
		return EmbeddingNamespace{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO embedding_namespaces (repo_id, namespace_id, profile_id, provider_id, provider_type, model_id, model_revision, dimensions, dtype, normalization, document_instruction_id, query_instruction_id, chunk_policy_id, language_policy_id, config_hash, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(repo_id, namespace_id) DO UPDATE SET profile_id = excluded.profile_id, provider_id = excluded.provider_id, provider_type = excluded.provider_type, model_id = excluded.model_id, model_revision = excluded.model_revision, dimensions = excluded.dimensions, dtype = excluded.dtype, normalization = excluded.normalization, document_instruction_id = excluded.document_instruction_id, query_instruction_id = excluded.query_instruction_id, chunk_policy_id = excluded.chunk_policy_id, language_policy_id = excluded.language_policy_id, config_hash = excluded.config_hash, updated_at = excluded.updated_at`,
		normalized.RepoID, normalized.ID, normalized.ProfileID, normalized.ProviderID, normalized.ProviderType, normalized.ModelID, normalized.ModelRevision, normalized.Dimensions, normalized.DType, normalized.Normalization, normalized.DocumentInstructionID, normalized.QueryInstructionID, normalized.ChunkPolicyID, normalized.LanguagePolicyID, normalized.ConfigHash, normalized.CreatedAt.Format(time.RFC3339Nano), normalized.UpdatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return EmbeddingNamespace{}, err
	}
	return normalized, nil
}

func (s *SQLiteStore) ResolveEmbeddingNamespace(ctx context.Context, identity EmbeddingNamespaceIdentity) (EmbeddingNamespace, bool, error) {
	query := `SELECT repo_id, namespace_id, profile_id, provider_id, provider_type, model_id, model_revision, dimensions, dtype, normalization, document_instruction_id, query_instruction_id, chunk_policy_id, language_policy_id, config_hash, created_at, updated_at
FROM embedding_namespaces
WHERE repo_id = ? AND provider_id = ? AND provider_type = ? AND model_id = ? AND model_revision = ? AND dimensions = ? AND dtype = ? AND normalization = ? AND document_instruction_id = ? AND query_instruction_id = ? AND chunk_policy_id = ? AND language_policy_id = ? AND config_hash = ?
ORDER BY namespace_id LIMIT 1`
	row := s.db.QueryRowContext(ctx, query, identity.RepoID, identity.ProviderID, identity.ProviderType, identity.ModelID, identity.ModelRevision, identity.Dimensions, identity.DType, identity.Normalization, identity.DocumentInstructionID, identity.QueryInstructionID, identity.ChunkPolicyID, identity.LanguagePolicyID, identity.ConfigHash)
	namespace, err := scanEmbeddingNamespaceRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return EmbeddingNamespace{}, false, nil
		}
		return EmbeddingNamespace{}, false, err
	}
	return namespace, true, nil
}

func (s *SQLiteStore) GetEmbeddingNamespace(ctx context.Context, repoID, namespaceID string) (EmbeddingNamespace, error) {
	row := s.db.QueryRowContext(ctx, `SELECT repo_id, namespace_id, profile_id, provider_id, provider_type, model_id, model_revision, dimensions, dtype, normalization, document_instruction_id, query_instruction_id, chunk_policy_id, language_policy_id, config_hash, created_at, updated_at FROM embedding_namespaces WHERE repo_id = ? AND namespace_id = ?`, repoID, namespaceID)
	namespace, err := scanEmbeddingNamespaceRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return EmbeddingNamespace{}, ErrNotFound
	}
	return namespace, err
}

func (s *SQLiteStore) ListEmbeddingNamespaces(ctx context.Context, repoID string) ([]EmbeddingNamespace, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT repo_id, namespace_id, profile_id, provider_id, provider_type, model_id, model_revision, dimensions, dtype, normalization, document_instruction_id, query_instruction_id, chunk_policy_id, language_policy_id, config_hash, created_at, updated_at FROM embedding_namespaces WHERE (? = '' OR repo_id = ?) ORDER BY repo_id, profile_id, namespace_id`, repoID, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	namespaces := []EmbeddingNamespace{}
	for rows.Next() {
		namespace, err := scanEmbeddingNamespaceScanner(rows)
		if err != nil {
			return nil, err
		}
		namespaces = append(namespaces, namespace)
	}
	return namespaces, rows.Err()
}

func (s *SQLiteStore) UpsertChunkEmbedding(ctx context.Context, embedding ChunkEmbedding) error {
	normalized, err := normalizeChunkEmbedding(ctx, s.db, embedding)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO chunk_embeddings (repo_id, namespace_id, chunk_id, source_id, record_id, snapshot_id, chunk_content_hash, vector, dimensions, dtype, vector_hash, embedded_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(repo_id, namespace_id, chunk_id) DO UPDATE SET source_id = excluded.source_id, record_id = excluded.record_id, snapshot_id = excluded.snapshot_id, chunk_content_hash = excluded.chunk_content_hash, vector = excluded.vector, dimensions = excluded.dimensions, dtype = excluded.dtype, vector_hash = excluded.vector_hash, embedded_at = excluded.embedded_at`,
		normalized.RepoID, normalized.NamespaceID, normalized.ChunkID, normalized.SourceID, normalized.RecordID, normalized.SnapshotID, normalized.ChunkContentHash, normalized.Vector, normalized.Dimensions, normalized.DType, normalized.VectorHash, normalized.EmbeddedAt.Format(time.RFC3339Nano))
	return err
}

func (s *SQLiteStore) ListChunkEmbeddings(ctx context.Context, filter ChunkEmbeddingFilter) ([]ChunkEmbedding, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT repo_id, namespace_id, chunk_id, source_id, record_id, snapshot_id, chunk_content_hash, vector, dimensions, dtype, vector_hash, embedded_at FROM chunk_embeddings WHERE (? = '' OR repo_id = ?) AND (? = '' OR namespace_id = ?) AND (? = '' OR chunk_id = ?) AND (? = '' OR source_id = ?) AND (? = '' OR record_id = ?) AND (? = '' OR snapshot_id = ?) ORDER BY repo_id, namespace_id, source_id, record_id, chunk_id`,
		filter.RepoID, filter.RepoID, filter.NamespaceID, filter.NamespaceID, filter.ChunkID, filter.ChunkID, filter.SourceID, filter.SourceID, filter.RecordID, filter.RecordID, filter.SnapshotID, filter.SnapshotID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	embeddings := []ChunkEmbedding{}
	for rows.Next() {
		embedding, err := scanChunkEmbedding(rows)
		if err != nil {
			return nil, err
		}
		embeddings = append(embeddings, embedding)
	}
	return embeddings, rows.Err()
}

func (s *SQLiteStore) UpsertRAGIndexRun(ctx context.Context, run RAGIndexRun) error {
	if run.RepoID == "" || run.ID == "" || run.NamespaceID == "" || run.ProfileID == "" || run.Status == "" {
		return fmt.Errorf("cache: rag index run requires repo id, run id, namespace id, profile id, and status")
	}
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Unix(0, 0).UTC()
	}
	if run.UpdatedAt.IsZero() {
		run.UpdatedAt = run.StartedAt
	}
	metadata, err := marshalJSON(run.Metadata)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO rag_index_runs (repo_id, run_id, namespace_id, profile_id, status, total_chunks, embedded_chunks, skipped_chunks, failed_chunks, started_at, updated_at, completed_at, error_class, message, metadata_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(repo_id, run_id) DO UPDATE SET namespace_id = excluded.namespace_id, profile_id = excluded.profile_id, status = excluded.status, total_chunks = excluded.total_chunks, embedded_chunks = excluded.embedded_chunks, skipped_chunks = excluded.skipped_chunks, failed_chunks = excluded.failed_chunks, updated_at = excluded.updated_at, completed_at = excluded.completed_at, error_class = excluded.error_class, message = excluded.message, metadata_json = excluded.metadata_json`,
		run.RepoID, run.ID, run.NamespaceID, run.ProfileID, run.Status, run.TotalChunks, run.EmbeddedChunks, run.SkippedChunks, run.FailedChunks, run.StartedAt.Format(time.RFC3339Nano), run.UpdatedAt.Format(time.RFC3339Nano), formatTimeOrEmpty(run.CompletedAt), run.ErrorClass, run.Message, metadata)
	return err
}

func (s *SQLiteStore) GetRAGIndexRun(ctx context.Context, repoID, runID string) (RAGIndexRun, error) {
	row := s.db.QueryRowContext(ctx, `SELECT repo_id, run_id, namespace_id, profile_id, status, total_chunks, embedded_chunks, skipped_chunks, failed_chunks, started_at, updated_at, completed_at, error_class, message, metadata_json FROM rag_index_runs WHERE repo_id = ? AND run_id = ?`, repoID, runID)
	run, err := scanRAGIndexRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return RAGIndexRun{}, ErrNotFound
	}
	return run, err
}

func normalizeEmbeddingNamespace(namespace EmbeddingNamespace) (EmbeddingNamespace, error) {
	if namespace.RepoID == "" || namespace.ProfileID == "" || namespace.ProviderID == "" || namespace.ProviderType == "" || namespace.ModelID == "" || namespace.Dimensions <= 0 || namespace.DType == "" || namespace.Normalization == "" || namespace.ChunkPolicyID == "" || namespace.LanguagePolicyID == "" || namespace.ConfigHash == "" {
		return EmbeddingNamespace{}, fmt.Errorf("cache: embedding namespace identity is incomplete")
	}
	if namespace.ID == "" {
		namespace.ID = EmbeddingNamespaceID(namespace.EmbeddingNamespaceIdentity)
	}
	if namespace.CreatedAt.IsZero() {
		namespace.CreatedAt = time.Unix(0, 0).UTC()
	}
	if namespace.UpdatedAt.IsZero() {
		namespace.UpdatedAt = namespace.CreatedAt
	}
	return namespace, nil
}

func normalizeChunkEmbedding(ctx context.Context, db *sql.DB, embedding ChunkEmbedding) (ChunkEmbedding, error) {
	if embedding.RepoID == "" || embedding.NamespaceID == "" || embedding.ChunkID == "" || len(embedding.Vector) == 0 || embedding.Dimensions <= 0 || embedding.DType == "" {
		return ChunkEmbedding{}, fmt.Errorf("cache: chunk embedding requires repo id, namespace id, chunk id, vector, dimensions, and dtype")
	}
	if embedding.EmbeddedAt.IsZero() {
		embedding.EmbeddedAt = time.Unix(0, 0).UTC()
	}
	if embedding.VectorHash == "" {
		sum := sha256.Sum256(embedding.Vector)
		embedding.VectorHash = hex.EncodeToString(sum[:])
	}
	if embedding.SourceID == "" || embedding.RecordID == "" || embedding.SnapshotID == "" || embedding.ChunkContentHash == "" {
		var sourceID, recordID, snapshotID, contentHash string
		err := db.QueryRowContext(ctx, `SELECT source_id, record_id, snapshot_id, content_hash FROM chunks WHERE repo_id = ? AND id = ?`, embedding.RepoID, embedding.ChunkID).Scan(&sourceID, &recordID, &snapshotID, &contentHash)
		if err != nil {
			return ChunkEmbedding{}, err
		}
		if embedding.SourceID == "" {
			embedding.SourceID = sourceID
		}
		if embedding.RecordID == "" {
			embedding.RecordID = recordID
		}
		if embedding.SnapshotID == "" {
			embedding.SnapshotID = snapshotID
		}
		if embedding.ChunkContentHash == "" {
			embedding.ChunkContentHash = contentHash
		}
	}
	return embedding, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanEmbeddingNamespaceRow(row *sql.Row) (EmbeddingNamespace, error) {
	return scanEmbeddingNamespaceScanner(row)
}

func scanEmbeddingNamespaceScanner(scanner rowScanner) (EmbeddingNamespace, error) {
	var namespace EmbeddingNamespace
	var createdRaw, updatedRaw string
	err := scanner.Scan(&namespace.RepoID, &namespace.ID, &namespace.ProfileID, &namespace.ProviderID, &namespace.ProviderType, &namespace.ModelID, &namespace.ModelRevision, &namespace.Dimensions, &namespace.DType, &namespace.Normalization, &namespace.DocumentInstructionID, &namespace.QueryInstructionID, &namespace.ChunkPolicyID, &namespace.LanguagePolicyID, &namespace.ConfigHash, &createdRaw, &updatedRaw)
	if err != nil {
		return EmbeddingNamespace{}, err
	}
	namespace.CreatedAt = parseTimeOrZero(createdRaw)
	namespace.UpdatedAt = parseTimeOrZero(updatedRaw)
	return namespace, nil
}

func scanChunkEmbedding(scanner rowScanner) (ChunkEmbedding, error) {
	var embedding ChunkEmbedding
	var embeddedRaw string
	err := scanner.Scan(&embedding.RepoID, &embedding.NamespaceID, &embedding.ChunkID, &embedding.SourceID, &embedding.RecordID, &embedding.SnapshotID, &embedding.ChunkContentHash, &embedding.Vector, &embedding.Dimensions, &embedding.DType, &embedding.VectorHash, &embeddedRaw)
	if err != nil {
		return ChunkEmbedding{}, err
	}
	embedding.EmbeddedAt = parseTimeOrZero(embeddedRaw)
	return embedding, nil
}

func scanRAGIndexRun(scanner rowScanner) (RAGIndexRun, error) {
	var run RAGIndexRun
	var startedRaw, updatedRaw, completedRaw, metadataRaw string
	err := scanner.Scan(&run.RepoID, &run.ID, &run.NamespaceID, &run.ProfileID, &run.Status, &run.TotalChunks, &run.EmbeddedChunks, &run.SkippedChunks, &run.FailedChunks, &startedRaw, &updatedRaw, &completedRaw, &run.ErrorClass, &run.Message, &metadataRaw)
	if err != nil {
		return RAGIndexRun{}, err
	}
	run.StartedAt = parseTimeOrZero(startedRaw)
	run.UpdatedAt = parseTimeOrZero(updatedRaw)
	run.CompletedAt = parseTimeOrZero(completedRaw)
	run.Metadata, _ = unmarshalJSON[map[string]string](metadataRaw)
	return run, nil
}
