package cache

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (s *SQLiteStore) UpsertSourceGraph(ctx context.Context, graph SourceGraph) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer txRollbackOnError(tx, &err)
	if err = upsertSourceTx(ctx, tx, graph.Source); err != nil {
		return err
	}
	if err = upsertSearchProjectionTx(ctx, tx, graph.Source, s.useFTS); err != nil {
		return err
	}
	for _, identity := range graph.Identities {
		if identity.RepoID == "" {
			identity.RepoID = graph.Source.RepoID
		}
		if identity.SourceID == "" {
			identity.SourceID = graph.Source.ID
		}
		if err = upsertIdentityTx(ctx, tx, identity); err != nil {
			return err
		}
	}
	for _, link := range graph.Links {
		if link.RepoID == "" {
			link.RepoID = graph.Source.RepoID
		}
		if link.SourceID == "" {
			link.SourceID = graph.Source.ID
		}
		if err = upsertLinkTx(ctx, tx, link); err != nil {
			return err
		}
	}
	for _, chunk := range graph.Chunks {
		if chunk.RepoID == "" {
			chunk.RepoID = graph.Source.RepoID
		}
		if chunk.SourceID == "" {
			chunk.SourceID = graph.Source.ID
		}
		if _, err = upsertChunkTx(ctx, tx, chunk); err != nil {
			return err
		}
	}
	if graph.SyncStatus != nil {
		status := *graph.SyncStatus
		if status.RepoID == "" {
			status.RepoID = graph.Source.RepoID
		}
		if status.SourceID == "" {
			status.SourceID = graph.Source.ID
		}
		if err = upsertSyncStatusTx(ctx, tx, status); err != nil {
			return err
		}
	}
	for _, event := range graph.SyncEvents {
		if event.RepoID == "" {
			event.RepoID = graph.Source.RepoID
		}
		if event.SourceID == "" {
			event.SourceID = graph.Source.ID
		}
		if err = recordSyncEventTx(ctx, tx, event); err != nil {
			return err
		}
	}
	for _, conflict := range graph.Conflicts {
		if conflict.RepoID == "" {
			conflict.RepoID = graph.Source.RepoID
		}
		if conflict.SourceID == "" {
			conflict.SourceID = graph.Source.ID
		}
		if err = upsertConflictTx(ctx, tx, conflict); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLiteStore) UpsertSource(ctx context.Context, source Source) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer txRollbackOnError(tx, &err)
	if err = upsertSourceTx(ctx, tx, source); err != nil {
		return err
	}
	if err = upsertSearchProjectionTx(ctx, tx, source, s.useFTS); err != nil {
		return err
	}
	return tx.Commit()
}

func upsertSourceTx(ctx context.Context, tx *sql.Tx, source Source) error {
	labels, err := marshalJSON(source.Labels)
	if err != nil {
		return err
	}
	createdAt := source.CreatedAt
	updatedAt := source.UpdatedAt
	if createdAt.IsZero() {
		createdAt = time.Unix(0, 0).UTC()
	}
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}
	return execTx(ctx, tx, `INSERT INTO sources (repo_id, id, kind, path, title, body, status, labels, content_hash, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(repo_id, id) DO UPDATE SET kind = excluded.kind, path = excluded.path, title = excluded.title, body = excluded.body, status = excluded.status, labels = excluded.labels, content_hash = excluded.content_hash, updated_at = excluded.updated_at`,
		source.RepoID, source.ID, source.Kind, source.Path, source.Title, source.Body, source.Status, labels, source.ContentHash, createdAt.Format(time.RFC3339Nano), updatedAt.Format(time.RFC3339Nano))
}

func upsertSearchProjectionTx(ctx context.Context, tx *sql.Tx, source Source, useFTS bool) error {
	if !useFTS {
		return nil
	}
	if err := execTx(ctx, tx, `DELETE FROM fts_index WHERE repo_id = ? AND source_id = ?`, source.RepoID, source.ID); err != nil {
		return err
	}
	return execTx(ctx, tx, `INSERT INTO fts_index (repo_id, source_id, path, title, body) VALUES (?, ?, ?, ?, ?)`, source.RepoID, source.ID, source.Path, source.Title, source.Body)
}

func (s *SQLiteStore) GetSource(ctx context.Context, id string) (Source, error) {
	source, err := s.scanSource(ctx, `SELECT repo_id, id, kind, path, title, body, status, labels, content_hash, created_at, updated_at FROM sources WHERE id = ? ORDER BY repo_id LIMIT 1`, id)
	if err != nil {
		return Source{}, err
	}
	aliases, err := s.GetIdentityMapScoped(ctx, source.RepoID, id)
	if err != nil {
		return Source{}, err
	}
	source.Aliases = aliases
	return source, nil
}

func (s *SQLiteStore) GetSourceScoped(ctx context.Context, repoID, id string) (Source, error) {
	source, err := s.scanSource(ctx, `SELECT repo_id, id, kind, path, title, body, status, labels, content_hash, created_at, updated_at FROM sources WHERE repo_id = ? AND id = ?`, repoID, id)
	if err != nil {
		return Source{}, err
	}
	aliases, err := s.GetIdentityMapScoped(ctx, repoID, id)
	if err != nil {
		return Source{}, err
	}
	source.Aliases = aliases
	return source, nil
}

func (s *SQLiteStore) ListSources(ctx context.Context, filter SourceFilter) ([]Source, error) {
	query := `SELECT repo_id, id, kind, path, title, body, status, labels, content_hash, created_at, updated_at FROM sources WHERE (? = '' OR repo_id = ?) AND (? = '' OR kind = ?) AND (? = '' OR status = ?) ORDER BY repo_id, id`
	args := []any{filter.RepoID, filter.RepoID, filter.Kind, filter.Kind, filter.Status, filter.Status}
	if filter.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, filter.Limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSources(rows)
}

func (s *SQLiteStore) scanSource(ctx context.Context, query string, args ...any) (Source, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return Source{}, err
	}
	defer rows.Close()
	sources, err := scanSources(rows)
	if err != nil {
		return Source{}, err
	}
	if len(sources) == 0 {
		return Source{}, notFoundErr("source", "")
	}
	return sources[0], nil
}

func scanSources(rows *sql.Rows) ([]Source, error) {
	var sources []Source
	for rows.Next() {
		var source Source
		var labelsRaw, createdRaw, updatedRaw string
		if err := rows.Scan(&source.RepoID, &source.ID, &source.Kind, &source.Path, &source.Title, &source.Body, &source.Status, &labelsRaw, &source.ContentHash, &createdRaw, &updatedRaw); err != nil {
			return nil, err
		}
		labels, err := unmarshalJSON[[]string](labelsRaw)
		if err != nil {
			return nil, err
		}
		source.Labels = labels
		source.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdRaw)
		source.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedRaw)
		sources = append(sources, source)
	}
	return sources, rows.Err()
}

func (s *SQLiteStore) SearchSources(ctx context.Context, query SearchQuery) ([]SearchResult, error) {
	if s.useFTS {
		return s.searchSourcesFTS(ctx, query)
	}
	return s.searchSourcesFallback(ctx, query)
}

func (s *SQLiteStore) searchSourcesFallback(ctx context.Context, query SearchQuery) ([]SearchResult, error) {
	needle := normalizeSearchQuery(query.Query)
	rows, err := s.db.QueryContext(ctx, `SELECT repo_id, id, path, title, body FROM sources WHERE (? = '' OR repo_id = ?) AND (? = '' OR kind = ?) AND (lower(title) LIKE ? OR lower(body) LIKE ?) ORDER BY repo_id, id, path`, query.RepoID, query.RepoID, query.Kind, query.Kind, "%"+needle+"%", "%"+needle+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSearchResults(rows, needle, query.Limit)
}

func (s *SQLiteStore) searchSourcesFTS(ctx context.Context, query SearchQuery) ([]SearchResult, error) {
	if err := s.repairSearchProjection(ctx, query.RepoID); err != nil {
		return nil, err
	}
	needle := normalizeSearchQuery(query.Query)
	match := ftsMatchQuery(needle)
	rows, err := s.db.QueryContext(ctx, `SELECT s.repo_id, s.id, s.path, s.title, s.body
FROM fts_index f
JOIN sources s ON s.repo_id = f.repo_id AND s.id = f.source_id
WHERE (? = '' OR s.repo_id = ?) AND (? = '' OR s.kind = ?) AND fts_index MATCH ?
ORDER BY s.repo_id, s.id, s.path`, query.RepoID, query.RepoID, query.Kind, query.Kind, match)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSearchResults(rows, needle, query.Limit)
}

func (s *SQLiteStore) repairSearchProjection(ctx context.Context, repoID string) (err error) {
	var missing int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*)
FROM sources s
WHERE (? = '' OR s.repo_id = ?)
  AND NOT EXISTS (SELECT 1 FROM fts_index f WHERE f.repo_id = s.repo_id AND f.source_id = s.id)`, repoID, repoID).Scan(&missing); err != nil {
		return err
	}
	if missing == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer txRollbackOnError(tx, &err)
	rows, err := tx.QueryContext(ctx, `SELECT repo_id, id, path, title, body FROM sources s
WHERE (? = '' OR s.repo_id = ?)
  AND NOT EXISTS (SELECT 1 FROM fts_index f WHERE f.repo_id = s.repo_id AND f.source_id = s.id)`, repoID, repoID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var source Source
		if err = rows.Scan(&source.RepoID, &source.ID, &source.Path, &source.Title, &source.Body); err != nil {
			return err
		}
		if err = upsertSearchProjectionTx(ctx, tx, source, true); err != nil {
			return err
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	return tx.Commit()
}

func scanSearchResults(rows *sql.Rows, needle string, limit int) ([]SearchResult, error) {
	var results []SearchResult
	for rows.Next() {
		var repoID, id, path, title, body string
		if err := rows.Scan(&repoID, &id, &path, &title, &body); err != nil {
			return nil, err
		}
		results = append(results, SearchResult{RepoID: repoID, ID: id, Path: path, Title: title, Snippet: snippet(title+"\n"+body, needle), Score: searchScore(title, body, needle), Line: lineFor(body, needle)})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		if results[i].ID != results[j].ID {
			return results[i].ID < results[j].ID
		}
		return results[i].Path < results[j].Path
	})
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func normalizeSearchQuery(query string) string {
	return strings.ToLower(strings.TrimSpace(query))
}

func ftsMatchQuery(query string) string {
	parts := strings.Fields(query)
	if len(parts) == 0 {
		return `""`
	}
	for i, part := range parts {
		parts[i] = `"` + strings.ReplaceAll(part, `"`, `""`) + `"`
	}
	return strings.Join(parts, " AND ")
}

func searchScore(title, body, needle string) float64 {
	titleLower := strings.ToLower(title)
	bodyLower := strings.ToLower(body)
	score := 0.0
	if strings.Contains(titleLower, needle) {
		score += 10
	}
	score += float64(strings.Count(titleLower, needle) + strings.Count(bodyLower, needle))
	return score
}

func snippet(text, needle string) string {
	lower := strings.ToLower(text)
	idx := strings.Index(lower, needle)
	if idx < 0 {
		if len(text) > 80 {
			return text[:80]
		}
		return text
	}
	start := idx - 30
	if start < 0 {
		start = 0
	}
	end := idx + len(needle) + 30
	if end > len(text) {
		end = len(text)
	}
	return text[start:end]
}

func lineFor(body, needle string) int {
	idx := strings.Index(strings.ToLower(body), needle)
	if idx < 0 {
		return 1
	}
	return strings.Count(body[:idx], "\n") + 1
}

func deterministicChunkID(chunk Chunk) string {
	sum := sha256.Sum256([]byte(chunk.SourceID + "\x00" + chunk.ContentHash + "\x00" + strconv.Itoa(chunk.ByteStart)))
	return hex.EncodeToString(sum[:])
}
