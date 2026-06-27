package cache

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestBacklinks(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()

	mustUpsertGraph(t, ctx, store, SourceGraph{Source: testSource("DOC-123", "doc", "Design Doc"), Identities: []Identity{{AliasType: "path", Alias: "docs/DOC-123.md"}, {AliasType: "remote", Alias: "issue/123"}}})
	mustUpsertGraph(t, ctx, store, SourceGraph{Source: testSource("TASK-001", "task", "Task"), Links: []Link{{TargetID: "DOC-123", Kind: "references", Text: "DOC-123"}}})

	backlinks, err := store.GetBacklinks(ctx, "DOC-123")
	if err != nil {
		t.Fatalf("GetBacklinks returned error: %v", err)
	}
	if len(backlinks) != 1 {
		t.Fatalf("GetBacklinks returned %d records, want 1", len(backlinks))
	}
	if backlinks[0].ID != "TASK-001" {
		t.Fatalf("backlink source id = %q, want TASK-001", backlinks[0].ID)
	}
	if backlinks[0].Path != "project/task-001.md" {
		t.Fatalf("backlink path = %q, want project/task-001.md", backlinks[0].Path)
	}

	source, err := store.GetSource(ctx, "DOC-123")
	if err != nil {
		t.Fatalf("GetSource returned error: %v", err)
	}
	if len(source.Aliases) != 2 {
		t.Fatalf("GetSource aliases = %d, want 2", len(source.Aliases))
	}
}

func TestRecordSyncEventTimestamps(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()

	mustUpsertGraph(t, ctx, store, SourceGraph{Source: testSource("DOC-123", "doc", "Design Doc")})
	startedAt := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	completedAt := startedAt.Add(2 * time.Second)
	event := SyncEvent{
		RepoID:         "fixture-a",
		ID:             "sync-event-timestamps",
		SourceID:       "DOC-123",
		RemoteType:     "issue",
		RemoteID:       "123",
		RemoteRevision: "rev-1",
		Status:         "succeeded",
		IdempotencyKey: "sync-event-timestamps-key",
		Message:        "{}",
		CreatedAt:      completedAt,
		StartedAt:      startedAt,
		CompletedAt:    completedAt,
	}
	if err := store.RecordSyncEvent(ctx, event); err != nil {
		t.Fatalf("RecordSyncEvent returned error: %v", err)
	}
	got, err := store.GetSyncEventByKey(ctx, "sync-event-timestamps-key")
	if err != nil {
		t.Fatalf("GetSyncEventByKey returned error: %v", err)
	}
	if got == nil {
		t.Fatal("GetSyncEventByKey returned nil")
	}
	if !got.StartedAt.Equal(startedAt) {
		t.Fatalf("StartedAt = %s, want %s", got.StartedAt, startedAt)
	}
	if !got.CompletedAt.Equal(completedAt) {
		t.Fatalf("CompletedAt = %s, want %s", got.CompletedAt, completedAt)
	}
}

func TestScenario009AuditConfirmationPersistsInspectableMetadata(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()
	now := time.Date(2026, 6, 23, 11, 0, 0, 0, time.UTC)
	entry := AuditTrailEntry{RepoID: "fixture-a", ID: "write-scenario-009-key", Operation: "create-issue", Command: "create-issue", Mode: "live", RecordID: "ISSUE-100", RemoteType: "issue", RemoteID: "100", IdempotencyKey: "scenario-009-key", Status: "succeeded", PayloadHash: "payload-hash", RequestMetadata: map[string]string{"method": "POST", "remote_alias": "100", "source_fingerprint": "payload-hash"}, CreatedAt: now}
	if err := store.RecordAuditEvent(ctx, entry); err != nil {
		t.Fatalf("RecordAuditEvent returned error: %v", err)
	}
	got, err := store.GetAuditEventByKey(ctx, "fixture-a", "scenario-009-key")
	if err != nil {
		t.Fatalf("GetAuditEventByKey returned error: %v", err)
	}
	if got == nil || got.Command != "create-issue" || got.Mode != "live" || got.RemoteID != "100" || got.PayloadHash != "payload-hash" || !got.CreatedAt.Equal(now) {
		t.Fatalf("audit entry=%#v", got)
	}
	if got.RequestMetadata["method"] != "POST" || got.RequestMetadata["remote_alias"] != "100" || got.RequestMetadata["source_fingerprint"] != "payload-hash" {
		t.Fatalf("metadata=%#v", got.RequestMetadata)
	}
}

func TestScenario008CacheConfirmationIdempotentUpsert(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()
	now := time.Date(2026, 6, 23, 10, 0, 0, 0, time.UTC)
	record := Record{RepoID: "fixture-a", ID: "ISSUE-100", Type: "issue", Path: "issues/100.md", Title: "Live mock", Body: "body", Status: "open", ContentHash: "hash-100", Provenance: ProvenanceRemote, RemoteType: "issue", RemoteID: "100", RemoteRevision: "rev-100", CreatedAt: now, UpdatedAt: now}
	if err := store.UpsertRecordGraph(ctx, RecordGraph{Record: record}); err != nil {
		t.Fatalf("UpsertRecordGraph returned error: %v", err)
	}
	first := CacheConfirmationRecord{RepoID: "fixture-a", Command: "create-issue", RecordID: "ISSUE-100", RecordType: "issue", RemoteType: "issue", RemoteID: "100", IdempotencyKey: "scenario-008-key", Status: "succeeded", SourceFingerprint: "fingerprint-1", CreatedAt: now}
	if err := store.RecordCacheConfirmation(ctx, first); err != nil {
		t.Fatalf("RecordCacheConfirmation first returned error: %v", err)
	}
	second := first
	second.ID = "custom-confirmation-id"
	second.SourceFingerprint = "fingerprint-2"
	if err := store.RecordCacheConfirmation(ctx, second); err != nil {
		t.Fatalf("RecordCacheConfirmation second returned error: %v", err)
	}
	got, err := store.GetCacheConfirmationByKey(ctx, "fixture-a", "scenario-008-key")
	if err != nil {
		t.Fatalf("GetCacheConfirmationByKey returned error: %v", err)
	}
	if got == nil || got.ID != "custom-confirmation-id" || got.RemoteID != "100" || got.SourceFingerprint != "fingerprint-2" {
		t.Fatalf("confirmation=%#v", got)
	}
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM cache_confirmations WHERE repo_id = ? AND idempotency_key = ?`, "fixture-a", "scenario-008-key").Scan(&count); err != nil {
		t.Fatalf("count cache confirmations: %v", err)
	}
	if count != 1 {
		t.Fatalf("confirmation rows=%d want 1", count)
	}
}

func TestScenario008CacheConfirmationRequiresRecord(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()
	base := CacheConfirmationRecord{RepoID: "fixture-a", Command: "create-issue", RecordID: "ISSUE-404", RecordType: "issue", RemoteType: "issue", RemoteID: "404", IdempotencyKey: "missing-record", Status: "succeeded", CreatedAt: time.Date(2026, 6, 23, 10, 0, 0, 0, time.UTC)}
	if err := store.RecordCacheConfirmation(ctx, base); err == nil {
		t.Fatalf("missing record confirmation was accepted")
	}
	for name, mutate := range map[string]func(*CacheConfirmationRecord){
		"repo_id":         func(c *CacheConfirmationRecord) { c.RepoID = "" },
		"record_id":       func(c *CacheConfirmationRecord) { c.RecordID = "" },
		"remote_type":     func(c *CacheConfirmationRecord) { c.RemoteType = "" },
		"remote_id":       func(c *CacheConfirmationRecord) { c.RemoteID = "" },
		"idempotency_key": func(c *CacheConfirmationRecord) { c.IdempotencyKey = "" },
	} {
		confirmation := base
		mutate(&confirmation)
		if err := store.RecordCacheConfirmation(ctx, confirmation); err == nil {
			t.Fatalf("%s empty confirmation was accepted", name)
		}
	}
}

func TestScenario008LiveSyncCacheEvidence(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()
	now := time.Date(2026, 6, 23, 10, 0, 0, 0, time.UTC)
	graph := SyncGraph{RepoID: "fixture-a", Record: Record{ID: "ISSUE-MOCK-100", Type: "issue", Path: "issues/100.md", Title: "Mock issue", Body: "mock body", Status: "open", ContentHash: "hash-mock-100", Provenance: ProvenanceRemote, RemoteType: "issue", RemoteID: "100", RemoteRevision: "rev-100", CreatedAt: now, UpdatedAt: now}, Comments: []RecordComment{{CommentID: "comment-100", Author: "mock-user", Body: "mock comment", ContentHash: "hash-comment", RemoteRevision: "comment-rev", CreatedAt: now, UpdatedAt: now}}, Identities: []Identity{{AliasType: "issue", Alias: "100", Remote: RemoteAlias{Type: "issue", ID: "100"}}}, RemoteRevisions: []RemoteRevision{{RemoteType: "issue", RemoteID: "100", RemoteRevision: "rev-100", Status: "fresh", LastFetchedAt: now}}, SyncEvents: []SyncEvent{{ID: "sync-mock-100", RemoteType: "issue", RemoteID: "100", RemoteRevision: "rev-100", Status: "succeeded", IdempotencyKey: "scenario-008-sync", Message: "mock sync", CreatedAt: now, StartedAt: now, CompletedAt: now}}}
	if err := store.UpsertSyncGraph(ctx, graph); err != nil {
		t.Fatalf("UpsertSyncGraph returned error: %v", err)
	}
	record, err := store.GetRecord(ctx, "fixture-a", "ISSUE-MOCK-100")
	if err != nil {
		t.Fatalf("GetRecord returned error: %v", err)
	}
	if record.Provenance != ProvenanceRemote || record.RemoteID != "100" || len(record.Comments) != 1 || len(record.Aliases) != 1 {
		t.Fatalf("record=%#v", record)
	}
	if event, err := store.GetSyncEventByKey(ctx, "scenario-008-sync"); err != nil || event == nil {
		t.Fatalf("sync event=%#v err=%v", event, err)
	}
	counts, err := store.RecordCounts(ctx, "fixture-a")
	if err != nil {
		t.Fatalf("RecordCounts returned error: %v", err)
	}
	if counts.RemoteRevisions != 1 || counts.Comments != 1 || counts.IdentityAliases != 1 || counts.SyncEvents != 1 {
		t.Fatalf("counts=%#v", counts)
	}
	for _, id := range []string{"ISSUE-42", "WIKI-HOME"} {
		if _, err := store.GetRecord(ctx, "fixture-a", id); err == nil {
			t.Fatalf("fixture record %s present in live cache evidence", id)
		}
	}
}

func TestChunkSchemaEmbeddingColumn(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()

	var columnType string
	var defaultValue sql.NullString
	var found bool
	rows, err := store.db.QueryContext(ctx, `PRAGMA table_info(chunks)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info returned error: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name string
		var notNull int
		var pk int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		if name == "embedding" {
			found = true
			if columnType != "BLOB" || (defaultValue.Valid && defaultValue.String != "NULL") || notNull != 0 {
				t.Fatalf("embedding column type/default/notnull = %q/%v/%d, want BLOB/NULL/0", columnType, defaultValue, notNull)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info rows error: %v", err)
	}
	if !found {
		t.Fatalf("chunks table missing embedding column")
	}

	contentHash := "hash-doc-123"
	mustUpsertGraph(t, ctx, store, SourceGraph{Source: testSourceWithHash("DOC-123", "doc", "Design Doc", contentHash)})
	first := Chunk{SourceID: "DOC-123", ContentHash: contentHash, ByteStart: 0, ByteEnd: 20, LineStart: 1, LineEnd: 2, HeadingPath: []string{"Design"}, Text: "first chunk", NormalizedText: "first chunk"}
	second := Chunk{SourceID: "DOC-123", ContentHash: contentHash, ByteStart: 21, ByteEnd: 40, LineStart: 3, LineEnd: 4, HeadingPath: []string{"Design", "Details"}, Text: "second chunk", NormalizedText: "second chunk"}
	if _, err := store.UpsertChunk(ctx, first); err != nil {
		t.Fatalf("UpsertChunk first returned error: %v", err)
	}
	if _, err := store.UpsertChunk(ctx, second); err != nil {
		t.Fatalf("UpsertChunk second returned error: %v", err)
	}
	chunks, err := store.GetChunks(ctx, "DOC-123")
	if err != nil {
		t.Fatalf("GetChunks returned error: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("GetChunks returned %d records, want 2", len(chunks))
	}
	for _, chunk := range chunks {
		if chunk.Embedding != nil {
			t.Fatalf("chunk embedding = %v, want nil", chunk.Embedding)
		}
	}
	duplicate := first
	duplicate.ID = "different-id"
	duplicate.ByteEnd = 30
	if _, err := store.UpsertChunk(ctx, duplicate); err == nil {
		t.Fatalf("duplicate source_id/content_hash/byte_start was accepted")
	}
}

func TestChunkIdentity(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()

	contentHash := "hash-doc-123"
	graph := SourceGraph{
		Source: testSourceWithHash("DOC-123", "doc", "Design Doc", contentHash),
		Chunks: []Chunk{
			{ContentHash: contentHash, ByteStart: 0, ByteEnd: 20, LineStart: 1, LineEnd: 2, HeadingPath: []string{"Design"}, Text: "first chunk", NormalizedText: "first chunk"},
			{ContentHash: contentHash, ByteStart: 21, ByteEnd: 40, LineStart: 3, LineEnd: 4, HeadingPath: []string{"Design", "Details"}, Text: "second chunk", NormalizedText: "second chunk"},
		},
	}
	mustUpsertGraph(t, ctx, store, graph)
	mustUpsertGraph(t, ctx, store, graph)

	chunks, err := store.GetChunks(ctx, "DOC-123")
	if err != nil {
		t.Fatalf("GetChunks returned error: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("GetChunks returned %d records, want 2", len(chunks))
	}
	for _, chunk := range chunks {
		want := deterministicChunkID(chunk)
		if chunk.ID != want {
			t.Fatalf("chunk id = %q, want deterministic %q", chunk.ID, want)
		}
	}
	if chunks[0].ContentHash != chunks[1].ContentHash {
		t.Fatalf("chunks should share content hash")
	}
}

func TestIdentityResolution(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()

	mustUpsertGraph(t, ctx, store, SourceGraph{
		Source: testSource("DOC-123", "doc", "Design Doc"),
		Identities: []Identity{
			{AliasType: "path", Alias: "docs/design.md"},
			{AliasType: "remote", Alias: "wiki/design-doc"},
		},
	})

	identities, err := store.GetIdentityMap(ctx, "DOC-123")
	if err != nil {
		t.Fatalf("GetIdentityMap returned error: %v", err)
	}
	if len(identities) != 2 {
		t.Fatalf("GetIdentityMap returned %d identities, want 2", len(identities))
	}
	resolved, err := store.ResolveAliasScoped(ctx, "fixture-a", RemoteAlias{Type: "path", ID: "docs/design.md"})
	if err != nil {
		t.Fatalf("ResolveAliasScoped(path) returned error: %v", err)
	}
	if resolved.SourceID != "DOC-123" {
		t.Fatalf("ResolveAliasScoped(path) = %q, want DOC-123", resolved.SourceID)
	}
	resolved, err = store.ResolveAliasScoped(ctx, "fixture-a", RemoteAlias{Type: "remote", ID: "wiki/design-doc"})
	if err != nil {
		t.Fatalf("ResolveAliasScoped(remote) returned error: %v", err)
	}
	if resolved.SourceID != "DOC-123" {
		t.Fatalf("ResolveAliasScoped(remote) = %q, want DOC-123", resolved.SourceID)
	}
	if _, err := store.ResolveAlias(ctx, RemoteAlias{Type: "remote", ID: "wiki/design-doc"}); err == nil {
		t.Fatalf("ResolveAlias(remote) succeeded without repo_id")
	}
}

func TestRepoScopedRecordGraphCountsSnapshotsAndAliases(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()
	mustAddTestRepo(t, ctx, store, "fixture-b")
	now := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)

	if err := store.UpsertRecordGraph(ctx, RecordGraph{
		Record:          Record{RepoID: "fixture-a", ID: "ISSUE-42", Type: "issue", Path: "issues/42.md", Title: "Issue A", Body: "remote issue body", Status: "open", ContentHash: "ha", Provenance: ProvenanceRemote, RemoteType: "issue", RemoteID: "42", RemoteRevision: "r1", CreatedAt: now, UpdatedAt: now},
		Comments:        []RecordComment{{CommentID: "c1", Author: "fixture-user", Body: "comment", ContentHash: "hc", CreatedAt: now, UpdatedAt: now}},
		Identities:      []Identity{{AliasType: "issue", Alias: "42", Remote: RemoteAlias{Type: "issue", ID: "42"}}},
		RemoteRevisions: []RemoteRevision{{RemoteType: "issue", RemoteID: "42", RemoteRevision: "r1", Status: "fresh", LastFetchedAt: now}},
		SyncEvents:      []SyncEvent{{ID: "sync-42", RemoteType: "issue", RemoteID: "42", RemoteRevision: "r1", Status: "fresh", IdempotencyKey: "sync-a-42", Message: "fixture", CreatedAt: now}},
		AuditTrail:      []AuditTrailEntry{{ID: "audit-42", Operation: "sync", Status: "success", Message: "fixture", CreatedAt: now}},
		Snapshots:       []Snapshot{{ID: "snap-1", Format: "json", ContentHash: "snap-h", RecordCount: 1, CreatedAt: now, Chunks: []SnapshotChunk{{ChunkID: "chunk-1", RecordID: "ISSUE-42", ByteStart: 0, ByteEnd: 5, LineStart: 1, LineEnd: 1, Citation: "issues/42.md:1", ContentHash: "chunk-h"}}}},
	}); err != nil {
		t.Fatalf("UpsertRecordGraph fixture-a returned error: %v", err)
	}
	if err := store.UpsertRecordGraph(ctx, RecordGraph{Record: Record{RepoID: "fixture-b", ID: "ISSUE-42", Type: "issue", Path: "issues/42.md", Title: "Issue B", Body: "other repo", Status: "open", ContentHash: "hb", Provenance: ProvenanceRemote, RemoteType: "issue", RemoteID: "42"}, Identities: []Identity{{AliasType: "issue", Alias: "42", Remote: RemoteAlias{Type: "issue", ID: "42"}}}}); err != nil {
		t.Fatalf("UpsertRecordGraph fixture-b returned error: %v", err)
	}

	identityA, err := store.ResolveRepoAlias(ctx, "fixture-a", RemoteAlias{Type: "issue", ID: "42"})
	if err != nil || identityA.RepoID != "fixture-a" || identityA.SourceID != "ISSUE-42" {
		t.Fatalf("ResolveRepoAlias fixture-a = %#v, %v", identityA, err)
	}
	if _, err := store.ResolveAlias(ctx, RemoteAlias{Type: "issue", ID: "42"}); err == nil {
		t.Fatalf("unscoped ResolveAlias succeeded for colliding issue:42")
	} else {
		var conflict ErrAliasConflict
		if !errors.As(err, &conflict) {
			t.Fatalf("unscoped ResolveAlias error = %T %[1]v, want ErrAliasConflict", err)
		}
	}
	record, err := store.GetRecord(ctx, "fixture-a", "ISSUE-42")
	if err != nil || record.Provenance != ProvenanceRemote || len(record.Comments) != 1 || len(record.Aliases) != 1 {
		t.Fatalf("GetRecord = %#v, %v", record, err)
	}
	counts, err := store.RecordCounts(ctx, "fixture-a")
	if err != nil {
		t.Fatalf("RecordCounts returned error: %v", err)
	}
	if counts.Records != 1 || counts.Comments != 1 || counts.IdentityAliases != 1 || counts.SyncEvents != 1 || counts.AuditRows != 1 || counts.Snapshots != 1 || counts.SnapshotChunks != 1 || counts.RemoteRevisions != 1 {
		t.Fatalf("RecordCounts = %#v", counts)
	}
	chunks, err := store.ListSnapshotChunks(ctx, "fixture-a", "snap-1")
	if err != nil || len(chunks) != 1 || chunks[0].Citation != "issues/42.md:1" {
		t.Fatalf("ListSnapshotChunks = %#v, %v", chunks, err)
	}
}

func TestUpsertSyncGraphIdempotentRepeat(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()
	now := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	graph := SyncGraph{RepoID: "fixture-a", Record: Record{ID: "ISSUE-7", Type: "issue", Path: "issues/7.md", Title: "Issue", Body: "body", Status: "open", ContentHash: "h7", Provenance: ProvenanceRemote, RemoteType: "issue", RemoteID: "7", RemoteRevision: "rev-7", CreatedAt: now, UpdatedAt: now}, Comments: []RecordComment{{CommentID: "c1", Author: "fixture-user", Body: "comment", ContentHash: "hc", CreatedAt: now, UpdatedAt: now}}, Identities: []Identity{{AliasType: "issue", Alias: "7", Remote: RemoteAlias{Type: "issue", ID: "7"}}}, RemoteRevisions: []RemoteRevision{{RemoteType: "issue", RemoteID: "7", RemoteRevision: "rev-7", Status: "fresh", LastFetchedAt: now}}, SyncEvents: []SyncEvent{{ID: "sync-7", RemoteType: "issue", RemoteID: "7", RemoteRevision: "rev-7", Status: "succeeded", IdempotencyKey: "sync-issue-7", Message: "fixture", CreatedAt: now}}, Chunks: []Chunk{{ID: "chunk-7", SourceID: "ISSUE-7", ContentHash: "h7", ByteStart: 0, ByteEnd: 4, LineStart: 1, LineEnd: 1, Text: "body", NormalizedText: "body"}}}
	if err := store.UpsertSyncGraph(ctx, graph); err != nil {
		t.Fatalf("UpsertSyncGraph first returned error: %v", err)
	}
	if err := store.UpsertSyncGraph(ctx, graph); err != nil {
		t.Fatalf("UpsertSyncGraph replay returned error: %v", err)
	}
	counts, err := store.RecordCounts(ctx, "fixture-a")
	if err != nil {
		t.Fatal(err)
	}
	if counts.Records != 1 || counts.Comments != 1 || counts.IdentityAliases != 1 || counts.SyncEvents != 1 || counts.RemoteRevisions != 1 || counts.Chunks != 1 {
		t.Fatalf("RecordCounts = %#v", counts)
	}
}

func TestUpsertSyncGraphProjectionThenRemotePreservesProjectionAliasBoundary(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()
	now := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	if err := store.UpsertRecordGraph(ctx, RecordGraph{Record: Record{RepoID: "fixture-a", ID: "LOCAL-1", Type: "wiki", Path: "local/doc.md", Title: "Local", Body: "projection", Status: "draft", ContentHash: "projection", Provenance: ProvenanceProjection, CreatedAt: now, UpdatedAt: now}, Identities: []Identity{{AliasType: "projection", Alias: "local-doc"}}}); err != nil {
		t.Fatalf("projection upsert returned error: %v", err)
	}
	if err := store.UpsertSyncGraph(ctx, SyncGraph{RepoID: "fixture-a", Record: Record{ID: "WIKI-HOME", Type: "wiki", Path: "wiki/Home.md", Title: "Home", Body: "remote", Status: "fresh", ContentHash: "remote", Provenance: ProvenanceRemote, RemoteType: "wiki", RemoteID: "Home", RemoteRevision: "rev-home", CreatedAt: now, UpdatedAt: now}, Identities: []Identity{{AliasType: "wiki", Alias: "Home", Remote: RemoteAlias{Type: "wiki", ID: "Home"}}}, RemoteRevisions: []RemoteRevision{{RemoteType: "wiki", RemoteID: "Home", RemoteRevision: "rev-home", Status: "fresh", LastFetchedAt: now}}, SyncEvents: []SyncEvent{{ID: "sync-home", RemoteType: "wiki", RemoteID: "Home", RemoteRevision: "rev-home", Status: "succeeded", IdempotencyKey: "sync-home", Message: "fixture", CreatedAt: now}}}); err != nil {
		t.Fatalf("remote sync upsert returned error: %v", err)
	}
	projectionAlias, err := store.ResolveRepoAlias(ctx, "fixture-a", RemoteAlias{Type: "projection", ID: "local-doc"})
	if err != nil || projectionAlias.SourceID != "LOCAL-1" {
		t.Fatalf("projection alias = %#v, %v", projectionAlias, err)
	}
	remoteAlias, err := store.ResolveRepoAlias(ctx, "fixture-a", RemoteAlias{Type: "wiki", ID: "Home"})
	if err != nil || remoteAlias.SourceID != "WIKI-HOME" {
		t.Fatalf("remote alias = %#v, %v", remoteAlias, err)
	}
}

func TestRecordProvenanceRemoteCanonical(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()

	if err := store.UpsertRecordGraph(ctx, RecordGraph{Record: Record{RepoID: "fixture-a", ID: "DOC-1", Type: "wiki", Path: "wiki/doc-1.md", Title: "Remote", Body: "remote", Status: "current", ContentHash: "remote", Provenance: ProvenanceRemote, RemoteType: "wiki", RemoteID: "DOC-1"}}); err != nil {
		t.Fatalf("remote upsert returned error: %v", err)
	}
	if err := store.UpsertRecordGraph(ctx, RecordGraph{Record: Record{RepoID: "fixture-a", ID: "DOC-1", Type: "wiki", Path: "wiki/doc-1.md", Title: "Projection", Body: "projection", Status: "current", ContentHash: "projection", Provenance: ProvenanceProjection, RemoteType: "", RemoteID: ""}}); err != nil {
		t.Fatalf("projection upsert returned error: %v", err)
	}
	record, err := store.GetRecord(ctx, "fixture-a", "DOC-1")
	if err != nil {
		t.Fatalf("GetRecord returned error: %v", err)
	}
	if record.Provenance != ProvenanceRemote || record.RemoteType != "wiki" || record.RemoteID != "DOC-1" {
		t.Fatalf("remote identity was overwritten by projection: %#v", record)
	}
}

func TestSourceGraphRollback(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()

	mustUpsertGraph(t, ctx, store, SourceGraph{Source: testSource("DOC-123", "doc", "Design Doc")})
	graph := SourceGraph{
		Source:     testSource("TASK-001", "task", "Task"),
		Identities: []Identity{{AliasType: "path", Alias: "project/task-001.md"}},
		Links:      []Link{{TargetID: "MISSING-999", Kind: "references", Text: "missing target"}},
		Chunks:     []Chunk{{ContentHash: "hash-task-001", ByteStart: 0, ByteEnd: 10, LineStart: 1, LineEnd: 1, Text: "task", NormalizedText: "task"}},
		SyncEvents: []SyncEvent{{ID: "sync-task-001", IdempotencyKey: "key-1", Message: "ingest", Status: "started"}},
		SyncStatus: &SyncStatus{RemoteType: "issue", RemoteID: "1", RemoteRevision: "rev-1", Status: "fresh"},
		Conflicts:  []Conflict{{ID: "conflict-task-001", Kind: "test", LocalPayload: "local", RemotePayload: "remote"}},
	}

	if err := store.UpsertSourceGraph(ctx, graph); err == nil {
		t.Fatalf("UpsertSourceGraph succeeded, want foreign key failure")
	}
	if _, err := store.GetSource(ctx, "TASK-001"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetSource after rollback error = %v, want ErrNotFound", err)
	}
	if identities, err := store.GetIdentityMap(ctx, "TASK-001"); err != nil || len(identities) != 0 {
		t.Fatalf("identities after rollback = %v, %v; want none", identities, err)
	}
	if chunks, err := store.GetChunks(ctx, "TASK-001"); err != nil || len(chunks) != 0 {
		t.Fatalf("chunks after rollback = %v, %v; want none", chunks, err)
	}
	if _, err := store.GetSyncStatus(ctx, "TASK-001"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetSyncStatus after rollback error = %v, want ErrNotFound", err)
	}
	if conflicts, err := store.GetConflicts(ctx, "TASK-001"); err != nil || len(conflicts) != 0 {
		t.Fatalf("conflicts after rollback = %v, %v; want none", conflicts, err)
	}
	backlinks, err := store.GetBacklinks(ctx, "MISSING-999")
	if err != nil {
		t.Fatalf("GetBacklinks after rollback returned error: %v", err)
	}
	if len(backlinks) != 0 {
		t.Fatalf("backlinks after rollback = %d, want none", len(backlinks))
	}
}

func TestMinimumReplacementCacheState(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()

	createdAt := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	updatedAt := time.Date(2026, 6, 18, 12, 30, 0, 0, time.UTC)
	fetchedAt := time.Date(2026, 6, 18, 13, 0, 0, 0, time.UTC)

	mustUpsertGraph(t, ctx, store, SourceGraph{
		Source: Source{
			ID:          "DOC-123",
			Kind:        "doc",
			Path:        "docs/DOC-123.md",
			Title:       "Coordinator Backlog Guide",
			Body:        "Coordinator intake uses the backlog cache state for task lookup and handoff review.",
			Status:      "current",
			Labels:      []string{"coordinator", "backlog"},
			ContentHash: "hash-doc-123-minimum",
			CreatedAt:   createdAt,
			UpdatedAt:   updatedAt,
		},
		Identities: []Identity{
			{AliasType: "path", Alias: "docs/DOC-123.md", Remote: RemoteAlias{Type: "wiki", ID: "DOC-123"}},
			{AliasType: "remote", Alias: "wiki/DOC-123", Remote: RemoteAlias{Type: "wiki", ID: "DOC-123"}},
		},
		SyncStatus: &SyncStatus{RemoteType: "wiki", RemoteID: "DOC-123", RemoteRevision: "rev-doc-123", Status: "fresh", LastFetchedAt: fetchedAt},
		SyncEvents: []SyncEvent{{ID: "sync-doc-123", RemoteType: "wiki", RemoteID: "DOC-123", RemoteRevision: "rev-doc-123", Status: "fresh", IdempotencyKey: "minimum-doc-123", Message: "fixture ingest", CreatedAt: fetchedAt}},
	})
	mustUpsertGraph(t, ctx, store, SourceGraph{
		Source: Source{
			ID:          "TASK-015",
			Kind:        "task",
			Path:        "project/tasks/TASK-015.md",
			Title:       "Add minimum cache state test",
			Body:        "Ready task for coordinator intake references DOC-123 without querying markdown indexes.",
			Status:      "ready",
			Labels:      []string{"cache-store", "day-7"},
			ContentHash: "hash-task-015-minimum",
			CreatedAt:   createdAt,
			UpdatedAt:   updatedAt,
		},
		Identities: []Identity{{AliasType: "path", Alias: "project/tasks/TASK-015.md", Remote: RemoteAlias{Type: "issue", ID: "15"}}},
		Links:      []Link{{TargetID: "DOC-123", Kind: "references", Text: "DOC-123"}},
		SyncStatus: &SyncStatus{RemoteType: "issue", RemoteID: "15", RemoteRevision: "rev-task-015", Status: "fresh", LastFetchedAt: fetchedAt},
	})
	mustUpsertGraph(t, ctx, store, SourceGraph{
		Source: Source{
			ID:          "HANDOFF-001",
			Kind:        "handoff",
			Path:        "project/handoffs/HANDOFF-001.md",
			Title:       "Cache handoff review",
			Body:        "Handoff review confirms the Day 7 route remains offline after ingest.",
			Status:      "accepted",
			Labels:      []string{"handoff"},
			ContentHash: "hash-handoff-001-minimum",
			CreatedAt:   createdAt,
			UpdatedAt:   updatedAt,
		},
		Links: []Link{{TargetID: "DOC-123", Kind: "reviews", Text: "DOC-123"}},
	})

	results, err := store.SearchSources(ctx, SearchQuery{Query: "backlog", Limit: 5})
	if err != nil {
		t.Fatalf("SearchSources returned error: %v", err)
	}
	if len(results) == 0 || results[0].ID != "DOC-123" || results[0].Path != "docs/DOC-123.md" || results[0].Title != "Coordinator Backlog Guide" || results[0].Snippet == "" {
		t.Fatalf("SearchSources(backlog) = %#v, want DOC-123 with path/title/snippet", results)
	}
	missing, err := store.SearchSources(ctx, SearchQuery{Query: "NONEXISTENT", Limit: 5})
	if err != nil {
		t.Fatalf("SearchSources(NONEXISTENT) returned error: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("SearchSources(NONEXISTENT) returned %d results, want 0", len(missing))
	}
	if store.useFTS {
		fallbackStore, err := newSQLiteStore(ctx, ":memory:", true)
		if err != nil {
			t.Fatalf("new fallback store returned error: %v", err)
		}
		defer fallbackStore.Close()
		mustAddTestRepo(t, ctx, fallbackStore, "fixture-a")
		mustUpsertGraph(t, ctx, fallbackStore, SourceGraph{Source: Source{ID: "DOC-123", Kind: "doc", Path: "docs/DOC-123.md", Title: "Coordinator Backlog Guide", Body: "Coordinator intake uses the backlog cache state for task lookup and handoff review.", Status: "current", Labels: []string{"coordinator", "backlog"}, ContentHash: "hash-doc-123-minimum", CreatedAt: createdAt, UpdatedAt: updatedAt}})
		mustUpsertGraph(t, ctx, fallbackStore, SourceGraph{Source: Source{ID: "TASK-015", Kind: "task", Path: "project/tasks/TASK-015.md", Title: "Add minimum cache state test", Body: "Ready task for coordinator intake references DOC-123 without querying markdown indexes.", Status: "ready", Labels: []string{"cache-store", "day-7"}, ContentHash: "hash-task-015-minimum", CreatedAt: createdAt, UpdatedAt: updatedAt}})
		mustUpsertGraph(t, ctx, fallbackStore, SourceGraph{Source: Source{ID: "HANDOFF-001", Kind: "handoff", Path: "project/handoffs/HANDOFF-001.md", Title: "Cache handoff review", Body: "Handoff review confirms the Day 7 route remains offline after ingest.", Status: "accepted", Labels: []string{"handoff"}, ContentHash: "hash-handoff-001-minimum", CreatedAt: createdAt, UpdatedAt: updatedAt}})
		fallbackResults, err := fallbackStore.SearchSources(ctx, SearchQuery{Query: "backlog", Limit: 5})
		if err != nil {
			t.Fatalf("fallback SearchSources returned error: %v", err)
		}
		if !reflect.DeepEqual(visibleSearchResults(results), visibleSearchResults(fallbackResults)) {
			t.Fatalf("visible search results differ\nfts=%#v\nfallback=%#v", visibleSearchResults(results), visibleSearchResults(fallbackResults))
		}
		if _, err := store.db.ExecContext(ctx, `DELETE FROM fts_index WHERE repo_id = ?`, "fixture-a"); err != nil {
			t.Fatalf("delete fts_index returned error: %v", err)
		}
		repaired, err := store.SearchSources(ctx, SearchQuery{Query: "backlog", Limit: 5})
		if err != nil {
			t.Fatalf("repaired SearchSources returned error: %v", err)
		}
		if !reflect.DeepEqual(visibleSearchResults(results), visibleSearchResults(repaired)) {
			t.Fatalf("repaired visible search results differ\nbefore=%#v\nafter=%#v", visibleSearchResults(results), visibleSearchResults(repaired))
		}
	}

	readyTasks, err := store.ListSources(ctx, SourceFilter{Kind: "task", Status: "ready"})
	if err != nil {
		t.Fatalf("ListSources ready tasks returned error: %v", err)
	}
	if len(readyTasks) != 1 || readyTasks[0].ID != "TASK-015" || readyTasks[0].Path != "project/tasks/TASK-015.md" {
		t.Fatalf("ListSources ready tasks = %#v, want TASK-015", readyTasks)
	}

	doc, err := store.GetSource(ctx, "DOC-123")
	if err != nil {
		t.Fatalf("GetSource(DOC-123) returned error: %v", err)
	}
	if doc.Kind != "doc" || doc.Body == "" || doc.ContentHash != "hash-doc-123-minimum" || len(doc.Labels) != 2 || !doc.CreatedAt.Equal(createdAt) || !doc.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("GetSource(DOC-123) = %#v, want persisted metadata/body/hash/timestamps", doc)
	}
	if len(doc.Aliases) != 2 {
		t.Fatalf("GetSource(DOC-123) aliases = %d, want 2", len(doc.Aliases))
	}

	resolved, err := store.ResolveAliasScoped(ctx, "fixture-a", RemoteAlias{Type: "wiki", ID: "DOC-123"})
	if err != nil {
		t.Fatalf("ResolveAliasScoped(wiki:DOC-123) returned error: %v", err)
	}
	if resolved.SourceID != "DOC-123" {
		t.Fatalf("ResolveAliasScoped(wiki:DOC-123) = %q, want DOC-123", resolved.SourceID)
	}

	links, err := store.ListLinks(ctx, LinkFilter{TargetID: "DOC-123"})
	if err != nil {
		t.Fatalf("ListLinks(DOC-123) returned error: %v", err)
	}
	if len(links) != 2 {
		t.Fatalf("ListLinks(DOC-123) = %#v, want two stable-id links", links)
	}
	for _, link := range links {
		if link.TargetID != "DOC-123" {
			t.Fatalf("link target = %q, want stable id DOC-123", link.TargetID)
		}
	}

	backlinks, err := store.GetBacklinks(ctx, "DOC-123")
	if err != nil {
		t.Fatalf("GetBacklinks(DOC-123) returned error: %v", err)
	}
	if len(backlinks) != 2 || backlinks[0].ID != "HANDOFF-001" || backlinks[1].ID != "TASK-015" {
		t.Fatalf("GetBacklinks(DOC-123) = %#v, want HANDOFF-001 and TASK-015", backlinks)
	}

	syncStatus, err := store.GetSyncStatus(ctx, "DOC-123")
	if err != nil {
		t.Fatalf("GetSyncStatus(DOC-123) returned error: %v", err)
	}
	if syncStatus.RemoteType != "wiki" || syncStatus.RemoteID != "DOC-123" || syncStatus.RemoteRevision != "rev-doc-123" || syncStatus.Status != "fresh" || !syncStatus.LastFetchedAt.Equal(fetchedAt) {
		t.Fatalf("GetSyncStatus(DOC-123) = %#v, want persisted fresh wiki status", syncStatus)
	}
}

func TestLockContention(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()
	lockPath := filepath.Join(t.TempDir(), "cache.lock")

	first, err := store.AcquireLock(ctx, lockPath)
	if err != nil {
		t.Fatalf("AcquireLock first returned error: %v", err)
	}
	second, err := store.AcquireLock(ctx, lockPath)
	if err == nil {
		_ = store.ReleaseLock(ctx, second)
		t.Fatalf("AcquireLock second succeeded, want ErrLockContention")
	}
	var contention ErrLockContention
	if !errors.As(err, &contention) {
		t.Fatalf("AcquireLock second error = %T %[1]v, want ErrLockContention", err)
	}
	if contention.Path != lockPath {
		t.Fatalf("ErrLockContention path = %q, want %q", contention.Path, lockPath)
	}
	if err := store.ReleaseLock(ctx, first); err != nil {
		t.Fatalf("ReleaseLock first returned error: %v", err)
	}
	if err := store.ReleaseLock(ctx, first); err != nil {
		t.Fatalf("ReleaseLock second returned error: %v", err)
	}
	reacquired, err := store.AcquireLock(ctx, lockPath)
	if err != nil {
		t.Fatalf("AcquireLock after release returned error: %v", err)
	}
	if err := store.ReleaseLock(ctx, reacquired); err != nil {
		t.Fatalf("ReleaseLock reacquired returned error: %v", err)
	}
	if err := store.ReleaseLock(ctx, nil); err != nil {
		t.Fatalf("ReleaseLock nil returned error: %v", err)
	}
}

func TestWriterAdmissionWALOwnershipRuntime(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")
	store, err := NewSQLiteStore(ctx, path)
	if err != nil {
		t.Fatalf("NewSQLiteStore returned error: %v", err)
	}
	defer store.Close()
	mustAddTestRepo(t, ctx, store, "fixture-a")
	mustUpsertGraph(t, ctx, store, SourceGraph{Source: testSource("DOC-WAL", "doc", "WAL Doc")})

	readerOne, err := NewSQLiteStore(ctx, path)
	if err != nil {
		t.Fatalf("NewSQLiteStore readerOne returned error: %v", err)
	}
	defer readerOne.Close()
	readerTwo, err := NewSQLiteStore(ctx, path)
	if err != nil {
		t.Fatalf("NewSQLiteStore readerTwo returned error: %v", err)
	}
	defer readerTwo.Close()

	lease, err := store.AcquireWriter(ctx, WriterRequest{Operation: "sync-index", RepoID: "fixture-a"})
	if err != nil {
		t.Fatalf("AcquireWriter returned error: %v", err)
	}
	defer store.ReleaseWriter(ctx, lease)

	for i, reader := range []*SQLiteStore{readerOne, readerTwo} {
		source, err := reader.GetSourceScoped(ctx, "fixture-a", "DOC-WAL")
		if err != nil {
			t.Fatalf("reader %d GetSourceScoped returned error: %v", i+1, err)
		}
		if source.RepoID != "fixture-a" || source.ID != "DOC-WAL" {
			t.Fatalf("reader %d source = %#v", i+1, source)
		}
	}

	_, err = readerOne.AcquireWriter(ctx, WriterRequest{Operation: "sync", RepoID: "fixture-a"})
	var contention ErrLockContention
	if !errors.As(err, &contention) {
		t.Fatalf("second AcquireWriter error = %T %[1]v, want ErrLockContention", err)
	}
	if contention.Operation != "sync-index" || contention.StartedAt.IsZero() || contention.PID == 0 {
		t.Fatalf("contention metadata = %#v", contention)
	}

	migrationStore, err := NewSQLiteStore(ctx, path)
	if err != nil {
		t.Fatalf("NewSQLiteStore current schema open returned error while writer lease held: %v", err)
	}
	source, err := migrationStore.GetSourceScoped(ctx, "fixture-a", "DOC-WAL")
	if err != nil {
		_ = migrationStore.Close()
		t.Fatalf("new store GetSourceScoped returned error while writer lease held: %v", err)
	}
	if source.RepoID != "fixture-a" || source.ID != "DOC-WAL" {
		_ = migrationStore.Close()
		t.Fatalf("new store source = %#v", source)
	}
	_ = migrationStore.Close()

	if err := store.ReleaseWriter(ctx, lease); err != nil {
		t.Fatalf("ReleaseWriter returned error: %v", err)
	}
	lease = nil
	reacquired, err := readerTwo.AcquireWriter(ctx, WriterRequest{Operation: "sync", RepoID: "fixture-a"})
	if err != nil {
		t.Fatalf("AcquireWriter after release returned error: %v", err)
	}
	if err := readerTwo.ReleaseWriter(ctx, reacquired); err != nil {
		t.Fatalf("ReleaseWriter reacquired returned error: %v", err)
	}
	migrationStore, err = NewSQLiteStore(ctx, path)
	if err != nil {
		t.Fatalf("NewSQLiteStore after release returned error: %v", err)
	}
	_ = migrationStore.Close()
}

func TestCheckpointAfterWriteHeavySync(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")
	store, err := NewSQLiteStore(ctx, path)
	if err != nil {
		t.Fatalf("NewSQLiteStore returned error: %v", err)
	}
	defer store.Close()
	mustAddTestRepo(t, ctx, store, "fixture-a")
	for i := 0; i < 25; i++ {
		mustUpsertGraph(t, ctx, store, SourceGraph{Source: testSource(fmt.Sprintf("DOC-CP-%02d", i), "doc", "Checkpoint Doc")})
	}
	if err := store.Checkpoint(ctx, "sync-complete"); err != nil {
		var contention ErrLockContention
		if !errors.As(err, &contention) {
			t.Fatalf("Checkpoint returned error = %T %[1]v, want nil or ErrLockContention", err)
		}
	}
	reader, err := NewSQLiteStore(ctx, path)
	if err != nil {
		t.Fatalf("NewSQLiteStore reader returned error: %v", err)
	}
	defer reader.Close()
	if _, err := reader.GetSourceScoped(ctx, "fixture-a", "DOC-CP-00"); err != nil {
		t.Fatalf("reader GetSourceScoped after checkpoint returned error: %v", err)
	}
}

func TestLockContentionBlocksSimulatedSync(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()
	lockPath := filepath.Join(t.TempDir(), "cache.lock")

	held, err := store.AcquireLock(ctx, lockPath)
	if err != nil {
		t.Fatalf("AcquireLock held returned error: %v", err)
	}
	defer store.ReleaseLock(ctx, held)

	called := false
	err = simulateLockBeforeMutate(ctx, store, lockPath, func() error {
		called = true
		return store.UpsertSourceGraph(ctx, SourceGraph{
			Source:     testSource("DOC-LOCK", "doc", "Should Not Write"),
			SyncStatus: &SyncStatus{RemoteType: "wiki", RemoteID: "lock", RemoteRevision: "rev-lock", Status: "fresh"},
		})
	})
	var contention ErrLockContention
	if !errors.As(err, &contention) {
		t.Fatalf("simulateLockBeforeMutate error = %T %[1]v, want ErrLockContention", err)
	}
	if called {
		t.Fatalf("mutation was called while lock contention was active")
	}
	if sources, err := store.ListSources(ctx, SourceFilter{}); err != nil || len(sources) != 0 {
		t.Fatalf("sources after contention = %v, %v; want none", sources, err)
	}
	if _, err := store.GetSyncStatus(ctx, "DOC-LOCK"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetSyncStatus after contention error = %v, want ErrNotFound", err)
	}
}

func simulateLockBeforeMutate(ctx context.Context, store *SQLiteStore, lockPath string, mutate func() error) error {
	lock, err := store.AcquireLock(ctx, lockPath)
	if err != nil {
		return err
	}
	defer store.ReleaseLock(ctx, lock)
	return mutate()
}

func TestCacheBusyDiagnosticCodeOnLockContention(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")
	store, err := NewSQLiteStore(ctx, path)
	if err != nil {
		t.Fatalf("NewSQLiteStore returned error: %v", err)
	}
	defer store.Close()
	mustAddTestRepo(t, ctx, store, "fixture-a")

	lease, err := store.AcquireWriter(ctx, WriterRequest{Operation: "sync", RepoID: "fixture-a"})
	if err != nil {
		t.Fatalf("AcquireWriter returned error: %v", err)
	}
	defer store.ReleaseWriter(ctx, lease)

	_, err = store.AcquireWriter(ctx, WriterRequest{Operation: "write", RepoID: "fixture-a"})
	var contention ErrLockContention
	if !errors.As(err, &contention) {
		t.Fatalf("second AcquireWriter error = %T %[1]v, want ErrLockContention", err)
	}
	if contention.DiagnosticCode() != "cache_busy" {
		t.Fatalf("DiagnosticCode() = %q, want cache_busy", contention.DiagnosticCode())
	}
}

func TestThreeReadersOneWriterConcurrency(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")
	store, err := NewSQLiteStore(ctx, path)
	if err != nil {
		t.Fatalf("NewSQLiteStore returned error: %v", err)
	}
	defer store.Close()
	mustAddTestRepo(t, ctx, store, "fixture-a")
	mustUpsertGraph(t, ctx, store, SourceGraph{Source: testSource("DOC-R3W1", "doc", "R3W1 Doc")})

	readerOne, err := NewSQLiteStore(ctx, path)
	if err != nil {
		t.Fatalf("NewSQLiteStore readerOne returned error: %v", err)
	}
	defer readerOne.Close()
	readerTwo, err := NewSQLiteStore(ctx, path)
	if err != nil {
		t.Fatalf("NewSQLiteStore readerTwo returned error: %v", err)
	}
	defer readerTwo.Close()

	lease, err := store.AcquireWriter(ctx, WriterRequest{Operation: "sync-index", RepoID: "fixture-a"})
	if err != nil {
		t.Fatalf("AcquireWriter returned error: %v", err)
	}
	defer store.ReleaseWriter(ctx, lease)

	for i, reader := range []*SQLiteStore{readerOne, readerTwo} {
		source, err := reader.GetSourceScoped(ctx, "fixture-a", "DOC-R3W1")
		if err != nil {
			t.Fatalf("reader %d GetSourceScoped returned error: %v", i+1, err)
		}
		if source.RepoID != "fixture-a" || source.ID != "DOC-R3W1" {
			t.Fatalf("reader %d source = %#v", i+1, source)
		}
	}

	_, err = readerOne.AcquireWriter(ctx, WriterRequest{Operation: "sync", RepoID: "fixture-a"})
	var contention ErrLockContention
	if !errors.As(err, &contention) {
		t.Fatalf("second AcquireWriter error = %T %[1]v, want ErrLockContention", err)
	}
	if contention.DiagnosticCode() != "cache_busy" {
		t.Fatalf("DiagnosticCode() = %q, want cache_busy", contention.DiagnosticCode())
	}
}

func newTestStore(t *testing.T, ctx context.Context) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore(ctx, ":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore returned error: %v", err)
	}
	mustAddTestRepo(t, ctx, store, "fixture-a")
	return store
}

func mustAddTestRepo(t *testing.T, ctx context.Context, store *SQLiteStore, repoID string) {
	t.Helper()
	err := store.AddRepository(ctx, RepositoryBinding{RepoID: repoID, Owner: "owner", Name: repoID, APIBaseURL: "https://example.invalid/api", Scopes: []RepositoryScope{RepositoryScopeIssues, RepositoryScopeWiki}, DisplayName: repoID})
	if err != nil {
		t.Fatalf("AddRepository returned error: %v", err)
	}
}

func mustUpsertGraph(t *testing.T, ctx context.Context, store *SQLiteStore, graph SourceGraph) {
	t.Helper()
	graph = withTestRepo(graph)
	if err := store.UpsertSourceGraph(ctx, graph); err != nil {
		t.Fatalf("UpsertSourceGraph returned error: %v", err)
	}
}

func withTestRepo(graph SourceGraph) SourceGraph {
	if graph.Source.RepoID == "" {
		graph.Source.RepoID = "fixture-a"
	}
	for i := range graph.Identities {
		if graph.Identities[i].RepoID == "" {
			graph.Identities[i].RepoID = graph.Source.RepoID
		}
	}
	for i := range graph.Links {
		if graph.Links[i].RepoID == "" {
			graph.Links[i].RepoID = graph.Source.RepoID
		}
	}
	for i := range graph.Chunks {
		if graph.Chunks[i].RepoID == "" {
			graph.Chunks[i].RepoID = graph.Source.RepoID
		}
	}
	if graph.SyncStatus != nil && graph.SyncStatus.RepoID == "" {
		graph.SyncStatus.RepoID = graph.Source.RepoID
	}
	for i := range graph.SyncEvents {
		if graph.SyncEvents[i].RepoID == "" {
			graph.SyncEvents[i].RepoID = graph.Source.RepoID
		}
	}
	for i := range graph.Conflicts {
		if graph.Conflicts[i].RepoID == "" {
			graph.Conflicts[i].RepoID = graph.Source.RepoID
		}
	}
	return graph
}

func testSource(id string, kind string, title string) Source {
	return testSourceWithHash(id, kind, title, "hash-"+id)
}

func testSourceWithHash(id string, kind string, title string, contentHash string) Source {
	path := "docs/" + id + ".md"
	if kind == "task" {
		path = "project/task-001.md"
	}
	return Source{
		RepoID:      "fixture-a",
		ID:          id,
		Kind:        kind,
		Title:       title,
		Path:        path,
		Body:        "This source body mentions backlog and cache-first design.",
		Status:      "ready",
		Labels:      []string{"cache"},
		ContentHash: contentHash,
		CreatedAt:   time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2026, 6, 18, 12, 30, 0, 0, time.UTC),
	}
}
