#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
WORKDIR="${TMPDIR:-/tmp}/gitcode-mcp-validation-018-$$"
cleanup() {
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

mkdir -p "$WORKDIR"
rsync -a --exclude '.git' --exclude 'ai/artifacts' "$ROOT/" "$WORKDIR/"

cat > "$WORKDIR/internal/service/design018_validation_test.go" <<'GOEOF'
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/index"
)

func design018Service(t *testing.T, ctx context.Context, repoID string) (*Service, *cache.SQLiteStore) {
	t.Helper()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: repoID, Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	svc := New(store)
	svc.now = func() time.Time { return now }
	return svc, store
}

func design018SnapshotID(t *testing.T, result OperationResult) string {
	t.Helper()
	parts := strings.SplitN(result.Evidence, "=", 2)
	if len(parts) != 2 || parts[0] != "snapshot_id" || parts[1] == "" {
		t.Fatalf("index did not return snapshot_id evidence: %#v", result)
	}
	return parts[1]
}

func TestDesign018Scenario1CreateExportStoredSnapshot(t *testing.T) {
	ctx := context.Background()
	svc, store := design018Service(t, ctx, "fixture-repo")
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	if err := store.UpsertSourceGraph(ctx, cache.SourceGraph{
		Source:     cache.Source{RepoID: "fixture-repo", ID: "DOC-1", Kind: "doc", Path: "docs/doc1.md", Title: "Doc One", Body: "# Heading\n\nparagraph one body", Status: "ready", Labels: []string{"design"}, ContentHash: "h1", CreatedAt: now, UpdatedAt: now},
		Identities: []cache.Identity{{RepoID: "fixture-repo", SourceID: "DOC-1", AliasType: "path", Alias: "docs/doc1.md"}},
		Links:      []cache.Link{{RepoID: "fixture-repo", SourceID: "DOC-1", TargetID: "DOC-1", Kind: "self", Text: "self"}},
		SyncStatus: &cache.SyncStatus{RepoID: "fixture-repo", SourceID: "DOC-1", RemoteType: "wiki", RemoteID: "Doc1", RemoteRevision: "rev-1", Status: "fresh", LastFetchedAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSourceGraph(ctx, cache.SourceGraph{
		Source:     cache.Source{RepoID: "fixture-repo", ID: "DOC-2", Kind: "doc", Path: "docs/doc2.md", Title: "Doc Two", Body: "plain text body no headings", Status: "ready", ContentHash: "h2", CreatedAt: now, UpdatedAt: now},
		SyncStatus: &cache.SyncStatus{RepoID: "fixture-repo", SourceID: "DOC-2", RemoteType: "wiki", RemoteID: "Doc2", RemoteRevision: "rev-2", Status: "fresh", LastFetchedAt: now},
	}); err != nil {
		t.Fatal(err)
	}

	indexResult, err := svc.Index(ctx, OperationRequest{RepoID: "fixture-repo"})
	if err != nil {
		t.Fatalf("Index error: %v", err)
	}
	snapshotID := design018SnapshotID(t, indexResult)

	export, err := svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-repo", SnapshotID: snapshotID, Format: "json", IncludeBody: true})
	if err != nil {
		t.Fatalf("ExportSnapshot error: %v", err)
	}

	var snapshot Snapshot
	if err := json.Unmarshal([]byte(export.InlineContent), &snapshot); err != nil {
		t.Fatalf("invalid snapshot json: %v", err)
	}

	if snapshot.RepoID != "fixture-repo" || export.RepoID != "fixture-repo" {
		t.Fatalf("missing repo_id: snapshot=%q export=%q", snapshot.RepoID, export.RepoID)
	}
	if export.SnapshotID != snapshotID {
		t.Fatalf("snapshot_id mismatch: export=%q want=%q", export.SnapshotID, snapshotID)
	}
	if len(snapshot.Sources) != 2 {
		t.Fatalf("source summaries count=%d want 2: %#v", len(snapshot.Sources), snapshot)
	}
	for _, s := range snapshot.Sources {
		if s.ID == "" || s.Kind == "" || s.Title == "" || s.ContentHash == "" {
			t.Fatalf("source missing required fields: %#v", s)
		}
	}

	if len(snapshot.Chunks) == 0 {
		t.Fatalf("snapshot has no stored chunks")
	}
	for _, chunk := range snapshot.Chunks {
		if chunk.ID == "" || chunk.SourceID == "" || chunk.Text == "" || chunk.ContentHash == "" {
			t.Fatalf("chunk missing provenance: %#v", chunk)
		}
		if chunk.ByteStart < 0 || chunk.ByteEnd <= chunk.ByteStart {
			t.Fatalf("chunk byte range invalid: %#v", chunk)
		}
		if chunk.LineStart <= 0 || chunk.LineEnd <= 0 {
			t.Fatalf("chunk line range invalid: %#v", chunk)
		}
	}

	if export.RecordCount != len(snapshot.Sources) {
		t.Fatalf("record_count=%d want=%d", export.RecordCount, len(snapshot.Sources))
	}

	secondExport, err := svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-repo", SnapshotID: snapshotID, Format: "json", IncludeBody: true})
	if err != nil {
		t.Fatalf("second export error: %v", err)
	}
	if secondExport.InlineContent != export.InlineContent {
		t.Fatalf("export not deterministic:\nfirst: %s\nsecond: %s", export.InlineContent, secondExport.InlineContent)
	}
}

func TestDesign018Scenario2WarningPersistence(t *testing.T) {
	ctx := context.Background()
	svc, store := design018Service(t, ctx, "fixture-repo")
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)

	staleBody := "# Stale\n\noriginal body"
	normalBody := "# Normal\n\nnormal indexed body"
	if err := store.UpsertSourceGraph(ctx, cache.SourceGraph{
		Source:     cache.Source{RepoID: "fixture-repo", ID: "DOC-STALE", Kind: "doc", Path: "docs/stale.md", Title: "Stale Doc", Body: staleBody, Status: "ready", ContentHash: index.ContentHash(staleBody), CreatedAt: now, UpdatedAt: now},
		SyncStatus: &cache.SyncStatus{RepoID: "fixture-repo", SourceID: "DOC-STALE", RemoteType: "wiki", RemoteID: "stale", RemoteRevision: "rev-1", Status: "fresh", LastFetchedAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSourceGraph(ctx, cache.SourceGraph{
		Source:     cache.Source{RepoID: "fixture-repo", ID: "DOC-NORMAL", Kind: "doc", Path: "docs/normal.md", Title: "Normal Doc", Body: normalBody, Status: "ready", ContentHash: index.ContentHash(normalBody), CreatedAt: now, UpdatedAt: now},
		SyncStatus: &cache.SyncStatus{RepoID: "fixture-repo", SourceID: "DOC-NORMAL", RemoteType: "wiki", RemoteID: "normal", RemoteRevision: "rev-3", Status: "fresh", LastFetchedAt: now},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Index(ctx, OperationRequest{RepoID: "fixture-repo"}); err != nil {
		t.Fatalf("Index error: %v", err)
	}

	mutatedBody := "# Stale\n\nmutated body without reindex"
	if err := store.UpsertSource(ctx, cache.Source{RepoID: "fixture-repo", ID: "DOC-STALE", Kind: "doc", Path: "docs/stale.md", Title: "Stale Doc Mutated", Body: mutatedBody, Status: "ready", ContentHash: index.ContentHash(mutatedBody), CreatedAt: now, UpdatedAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	missingBody := "# No Index\n\nno chunks here"
	if err := store.UpsertSourceGraph(ctx, cache.SourceGraph{
		Source:     cache.Source{RepoID: "fixture-repo", ID: "DOC-MISSING", Kind: "doc", Path: "docs/missing.md", Title: "Missing Chunks Doc", Body: missingBody, Status: "ready", ContentHash: index.ContentHash(missingBody), CreatedAt: now, UpdatedAt: now},
		SyncStatus: &cache.SyncStatus{RepoID: "fixture-repo", SourceID: "DOC-MISSING", RemoteType: "wiki", RemoteID: "missing", RemoteRevision: "rev-2", Status: "fresh", LastFetchedAt: now},
	}); err != nil {
		t.Fatal(err)
	}

	export, err := svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-repo", Format: "json", IncludeBody: true})
	if err != nil {
		t.Fatalf("ExportSnapshot error: %v", err)
	}
	snapshotID := export.SnapshotID

	var snapshot Snapshot
	if err := json.Unmarshal([]byte(export.InlineContent), &snapshot); err != nil {
		t.Fatalf("invalid snapshot json: %v", err)
	}

	hasStale := false
	hasMissing := false
	for _, w := range snapshot.Warnings {
		if w.Code == "stale_index" && w.SourceID == "DOC-STALE" {
			hasStale = true
		}
		if w.Code == "missing_index" && w.SourceID == "DOC-MISSING" {
			hasMissing = true
		}
	}
	if !hasStale {
		t.Fatalf("stored stale_index warning missing; warnings=%+v", snapshot.Warnings)
	}
	if !hasMissing {
		t.Fatalf("stored missing_index warning missing for source with no chunks; warnings=%+v", snapshot.Warnings)
	}

	for _, w := range export.Warnings {
		if w == "stale_index" || w == "missing_index" {
			continue
		}
	}

	if err := store.UpsertSource(ctx, cache.Source{RepoID: "fixture-repo", ID: "DOC-MISSING", Kind: "doc", Path: "docs/missing.md", Title: "Now Has Chunks", Body: "now indexed", Status: "ready", ContentHash: "hash-now", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	source := cache.Source{RepoID: "fixture-repo", ID: "DOC-MISSING", ContentHash: "hash-now", Body: "now indexed"}
	chunks := index.ChunkSource(indexSourceRecord(source), index.ParseSource(indexSourceRecord(source)))
	for _, chunk := range chunks {
		if _, err := store.UpsertChunk(ctx, cacheChunk(chunk)); err != nil {
			t.Fatal(err)
		}
	}

	reexport, err := svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-repo", SnapshotID: snapshotID, Format: "json", IncludeBody: true})
	if err != nil {
		t.Fatalf("Re-export error: %v", err)
	}
	if reexport.InlineContent != export.InlineContent {
		t.Fatalf("stored export changed after cache mutation - should be immutable:\nfirst: %s\nsecond: %s", export.InlineContent, reexport.InlineContent)
	}
}

func TestDesign018Scenario3StoredOnlyDiff(t *testing.T) {
	ctx := context.Background()
	svc, store := design018Service(t, ctx, "fixture-repo")
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)

	if err := store.UpsertSourceGraph(ctx, cache.SourceGraph{
		Source:     cache.Source{RepoID: "fixture-repo", ID: "DOC-A", Kind: "doc", Path: "docs/a.md", Title: "Source A", Body: "# A\n\nSource A body original", Status: "ready", ContentHash: "hash-a-orig", CreatedAt: now, UpdatedAt: now},
		SyncStatus: &cache.SyncStatus{RepoID: "fixture-repo", SourceID: "DOC-A", RemoteType: "wiki", RemoteID: "a", RemoteRevision: "rev-a-1", Status: "fresh", LastFetchedAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSourceGraph(ctx, cache.SourceGraph{
		Source:     cache.Source{RepoID: "fixture-repo", ID: "DOC-B", Kind: "doc", Path: "docs/b.md", Title: "Source B", Body: "plain body kept stable", Status: "ready", ContentHash: "hash-b", CreatedAt: now, UpdatedAt: now},
		SyncStatus: &cache.SyncStatus{RepoID: "fixture-repo", SourceID: "DOC-B", RemoteType: "wiki", RemoteID: "b", RemoteRevision: "rev-b", Status: "fresh", LastFetchedAt: now},
	}); err != nil {
		t.Fatal(err)
	}

	indexResult1, err := svc.Index(ctx, OperationRequest{RepoID: "fixture-repo"})
	if err != nil {
		t.Fatalf("Index 1 error: %v", err)
	}
	baseID := design018SnapshotID(t, indexResult1)

	if err := store.UpsertSourceGraph(ctx, cache.SourceGraph{
		Source:     cache.Source{RepoID: "fixture-repo", ID: "DOC-C", Kind: "doc", Path: "docs/c.md", Title: "Source C New", Body: "# C\n\nnew source added", Status: "ready", ContentHash: "hash-c", CreatedAt: now, UpdatedAt: now},
		SyncStatus: &cache.SyncStatus{RepoID: "fixture-repo", SourceID: "DOC-C", RemoteType: "wiki", RemoteID: "c", RemoteRevision: "rev-c", Status: "fresh", LastFetchedAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSource(ctx, cache.Source{RepoID: "fixture-repo", ID: "DOC-A", Kind: "doc", Path: "docs/a.md", Title: "Source A Modified", Body: "# A Modified\n\nSource A body changed", Status: "ready", ContentHash: "hash-a-mod", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}

	indexResult2, err := svc.Index(ctx, OperationRequest{RepoID: "fixture-repo"})
	if err != nil {
		t.Fatalf("Index 2 error: %v", err)
	}
	headID := design018SnapshotID(t, indexResult2)

	diff, err := svc.DiffSnapshot(ctx, DiffSnapshotRequest{RepoID: "fixture-repo", BaseSnapshotID: baseID, HeadSnapshotID: headID, Format: "json"})
	if err != nil {
		t.Fatalf("DiffSnapshot error: %v", err)
	}

	foundAdded := false
	for _, s := range diff.AddedSources {
		if s.ID == "DOC-C" {
			foundAdded = true
		}
	}
	if !foundAdded || len(diff.AddedSources) == 0 {
		t.Fatalf("diff missing added sources; added=%#v", diff.AddedSources)
	}

	foundModified := false
	for _, c := range diff.ChangedSources {
		if c.ID == "DOC-A" && c.BeforeContentHash == "hash-a-orig" && c.AfterContentHash == "hash-a-mod" {
			foundModified = true
		}
	}
	if !foundModified || len(diff.ChangedSources) == 0 {
		t.Fatalf("diff missing modified sources; changed=%#v", diff.ChangedSources)
	}

	if diff.BaseSnapshotID != baseID || diff.HeadSnapshotID != headID {
		t.Fatalf("diff ids mismatch: base=%q want=%q head=%q want=%q", diff.BaseSnapshotID, baseID, diff.HeadSnapshotID, headID)
	}

	if err := store.UpsertSource(ctx, cache.Source{RepoID: "fixture-repo", ID: "DOC-A", Kind: "doc", Path: "docs/a.md", Title: "Post-diff mutation", Body: "this should not affect diff", Status: "ready", ContentHash: "hash-post-diff", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}

	rediff, err := svc.DiffSnapshot(ctx, DiffSnapshotRequest{RepoID: "fixture-repo", BaseSnapshotID: baseID, HeadSnapshotID: headID, Format: "json"})
	if err != nil {
		t.Fatalf("rediff error: %v", err)
	}

	rediffJSON, _ := json.Marshal(rediff)
	diffJSON, _ := json.Marshal(diff)
	if !bytes.Equal(diffJSON, rediffJSON) {
		t.Fatalf("diff changed after cache mutation:\nbefore: %s\nafter:  %s", string(diffJSON), string(rediffJSON))
	}
}

func TestDesign018Scenario3CitationRangeChanged(t *testing.T) {
	ctx := context.Background()
	svc, store := design018Service(t, ctx, "fixture-repo")
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)

	if err := store.UpsertSourceGraph(ctx, cache.SourceGraph{
		Source:     cache.Source{RepoID: "fixture-repo", ID: "DOC-RANGE", Kind: "doc", Path: "docs/range.md", Title: "Range Doc", Body: "# Section\n\nthis is a test of range shift\n\nextra content for range change", Status: "ready", ContentHash: "hash-range", CreatedAt: now, UpdatedAt: now},
		SyncStatus: &cache.SyncStatus{RepoID: "fixture-repo", SourceID: "DOC-RANGE", RemoteType: "wiki", RemoteID: "range", RemoteRevision: "rev-r-1", Status: "fresh", LastFetchedAt: now},
	}); err != nil {
		t.Fatal(err)
	}

	indexResult1, err := svc.Index(ctx, OperationRequest{RepoID: "fixture-repo"})
	if err != nil {
		t.Fatalf("Index 1 error: %v", err)
	}
	baseID := design018SnapshotID(t, indexResult1)

	chunks, err := store.GetChunksScoped(ctx, "fixture-repo", "DOC-RANGE")
	if err != nil || len(chunks) == 0 {
		t.Fatalf("chunks missing: err=%v len=%d", err, len(chunks))
	}
	firstChunk := chunks[0]
	modifiedChunk := firstChunk
	modifiedChunk.ContentHash = firstChunk.ContentHash
	modifiedChunk.ByteStart = firstChunk.ByteStart + 100
	modifiedChunk.ByteEnd = firstChunk.ByteEnd + 100
	modifiedChunk.LineStart = firstChunk.LineStart + 5
	modifiedChunk.LineEnd = firstChunk.LineEnd + 5
	modifiedChunk.Text = firstChunk.Text
	modifiedChunk.NormalizedText = firstChunk.NormalizedText

	if err := store.UpsertSource(ctx, cache.Source{RepoID: "fixture-repo", ID: "DOC-RANGE", Kind: "doc", Path: "docs/range.md", Title: "Range Doc Shifted", Body: "# Section\n\nthis is a test of range shift\n\nextra content for range change", Status: "ready", ContentHash: "hash-range", CreatedAt: now, UpdatedAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertChunk(ctx, modifiedChunk); err != nil {
		t.Fatal(err)
	}

	headExport, err := svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-repo", Format: "json", IncludeBody: true})
	if err != nil {
		t.Fatalf("head export error: %v", err)
	}
	headID := headExport.SnapshotID

	diff, err := svc.DiffSnapshot(ctx, DiffSnapshotRequest{RepoID: "fixture-repo", BaseSnapshotID: baseID, HeadSnapshotID: headID, Format: "json"})
	if err != nil {
		t.Fatalf("DiffSnapshot error: %v", err)
	}

	foundCitationRangeChanged := false
	for _, c := range diff.ChangedChunks {
		for _, f := range c.ChangedFields {
			if f == "citation_range_changed" {
				foundCitationRangeChanged = true
			}
		}
	}
	if !foundCitationRangeChanged {
		t.Fatalf("diff missing citation_range_changed; changed chunks: %#v", diff.ChangedChunks)
	}
}

func TestDesign018Scenario4NotFoundNoFallback(t *testing.T) {
	ctx := context.Background()
	svc, store := design018Service(t, ctx, "fixture-repo")
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)

	if err := store.UpsertSourceGraph(ctx, cache.SourceGraph{
		Source:     cache.Source{RepoID: "fixture-repo", ID: "DOC-X", Kind: "doc", Path: "docs/x.md", Title: "Doc X", Body: "# X\n\nbody x", Status: "ready", ContentHash: "hash-x", CreatedAt: now, UpdatedAt: now},
		SyncStatus: &cache.SyncStatus{RepoID: "fixture-repo", SourceID: "DOC-X", RemoteType: "wiki", RemoteID: "x", RemoteRevision: "rev-x", Status: "fresh", LastFetchedAt: now},
	}); err != nil {
		t.Fatal(err)
	}

	indexResult, err := svc.Index(ctx, OperationRequest{RepoID: "fixture-repo"})
	if err != nil {
		t.Fatalf("Index error: %v", err)
	}
	headID := design018SnapshotID(t, indexResult)

	_, err = svc.DiffSnapshot(ctx, DiffSnapshotRequest{RepoID: "fixture-repo", BaseSnapshotID: "missing-nonexistent", HeadSnapshotID: headID, Format: "json"})
	var notFound ErrNotFound
	if !errors.As(err, &notFound) {
		t.Fatalf("diff error = %T %[1]v, want ErrNotFound", err)
	}
	if notFound.Kind != "base_id" || notFound.ID != "missing-nonexistent" {
		t.Fatalf("not-found kind=%q id=%q want kind=base_id id=missing-nonexistent", notFound.Kind, notFound.ID)
	}

	_, err = svc.DiffSnapshot(ctx, DiffSnapshotRequest{RepoID: "fixture-repo", BaseSnapshotID: headID, HeadSnapshotID: "missing-head-id", Format: "json"})
	if !errors.As(err, &notFound) {
		t.Fatalf("diff error = %T %[1]v, want ErrNotFound for head_id", err)
	}
	if notFound.Kind != "head_id" || notFound.ID != "missing-head-id" {
		t.Fatalf("not-found kind=%q id=%q want kind=head_id id=missing-head-id", notFound.Kind, notFound.ID)
	}
}

func TestDesign018Scenario4NoCurrentCurrentFallback(t *testing.T) {
	ctx := context.Background()
	svc, store := design018Service(t, ctx, "fixture-repo")
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)

	if err := store.UpsertSourceGraph(ctx, cache.SourceGraph{
		Source:     cache.Source{RepoID: "fixture-repo", ID: "DOC-Y", Kind: "doc", Path: "docs/y.md", Title: "Doc Y", Body: "# Y\n\nbody y", Status: "ready", ContentHash: "hash-y", CreatedAt: now, UpdatedAt: now},
		SyncStatus: &cache.SyncStatus{RepoID: "fixture-repo", SourceID: "DOC-Y", RemoteType: "wiki", RemoteID: "y", RemoteRevision: "rev-y", Status: "fresh", LastFetchedAt: now},
	}); err != nil {
		t.Fatal(err)
	}

	indexResult, err := svc.Index(ctx, OperationRequest{RepoID: "fixture-repo"})
	if err != nil {
		t.Fatalf("Index error: %v", err)
	}
	headID := design018SnapshotID(t, indexResult)

	result, err := svc.DiffSnapshot(ctx, DiffSnapshotRequest{RepoID: "fixture-repo", BaseSnapshotID: "missing", HeadSnapshotID: headID, Format: "json"})
	if err == nil {
		t.Fatalf("diff with missing base_id unexpectedly succeeded; result=%#v", result)
	}
	if result.DiffText != "" || result.BaseSnapshotID != "" || result.HeadSnapshotID != "" {
		t.Fatalf("diff with missing base_id returned partial result: %#v", result)
	}

	result2, err2 := svc.DiffSnapshot(ctx, DiffSnapshotRequest{RepoID: "fixture-repo", BaseSnapshotID: headID, HeadSnapshotID: "missing", Format: "json"})
	if err2 == nil {
		t.Fatalf("diff with missing head_id unexpectedly succeeded; result=%#v", result2)
	}
	if result2.DiffText != "" || result2.BaseSnapshotID != "" || result2.HeadSnapshotID != "" {
		t.Fatalf("diff with missing head_id returned partial result: %#v", result2)
	}
}

func TestDesign018ConsistencyError(t *testing.T) {
	ctx := context.Background()
	svc, store := design018Service(t, ctx, "fixture-repo")
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)

	if err := store.UpsertSourceGraph(ctx, cache.SourceGraph{
		Source:     cache.Source{RepoID: "fixture-repo", ID: "DOC-Z", Kind: "doc", Path: "docs/z.md", Title: "Doc Z", Body: "# Z\n\nbody z for consistency", Status: "ready", ContentHash: "hash-z", CreatedAt: now, UpdatedAt: now},
		SyncStatus: &cache.SyncStatus{RepoID: "fixture-repo", SourceID: "DOC-Z", RemoteType: "wiki", RemoteID: "z", RemoteRevision: "rev-z", Status: "fresh", LastFetchedAt: now},
	}); err != nil {
		t.Fatal(err)
	}

	indexResult, err := svc.Index(ctx, OperationRequest{RepoID: "fixture-repo"})
	if err != nil {
		t.Fatalf("Index error: %v", err)
	}
	snapshotID := design018SnapshotID(t, indexResult)

	stored, err := store.GetSnapshot(ctx, "fixture-repo", snapshotID)
	if err != nil {
		t.Fatal(err)
	}
	tampered := stored
	tampered.ChunkCount = stored.ChunkCount + 1
	if err := store.UpsertSnapshot(ctx, tampered); err != nil {
		t.Fatal(err)
	}

	_, err = svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-repo", SnapshotID: snapshotID, Format: "json", IncludeBody: true})
	var consistencyErr ErrSnapshotConsistency
	if !errors.As(err, &consistencyErr) {
		t.Fatalf("tampered chunk_count did not produce consistency error; got %T %[1]v", err)
	}
	if consistencyErr.Expectation != "chunk_count" {
		t.Fatalf("consistency error expectation=%q want chunk_count", consistencyErr.Expectation)
	}
}

func TestDesign018IndexBasedWarningCategories(t *testing.T) {
	ctx := context.Background()
	svc, store := design018Service(t, ctx, "fixture-repo")
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	body := "# No Citation\n\nbody with no valid citation"
	bodyHash := index.ContentHash(body)

	if err := store.UpsertSourceGraph(ctx, cache.SourceGraph{
		Source:     cache.Source{RepoID: "fixture-repo", ID: "DOC-MISSING-CITE", Kind: "doc", Path: "docs/nocite.md", Title: "No Citation Doc", Body: body, Status: "ready", ContentHash: bodyHash, CreatedAt: now, UpdatedAt: now},
		SyncStatus: &cache.SyncStatus{RepoID: "fixture-repo", SourceID: "DOC-MISSING-CITE", RemoteType: "wiki", RemoteID: "nocite", RemoteRevision: "rev-nc", Status: "fresh", LastFetchedAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertChunk(ctx, cache.Chunk{RepoID: "fixture-repo", ID: "bad-citation", SourceID: "DOC-MISSING-CITE", RecordID: "DOC-MISSING-CITE", ContentHash: bodyHash, ByteStart: 0, ByteEnd: 0, LineStart: 0, LineEnd: 0, Text: body}); err != nil {
		t.Fatal(err)
	}

	export, err := svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-repo", Format: "json", IncludeBody: true})
	if err != nil {
		t.Fatalf("ExportSnapshot error: %v", err)
	}

	var snapshot Snapshot
	if err := json.Unmarshal([]byte(export.InlineContent), &snapshot); err != nil {
		t.Fatalf("invalid snapshot json: %v", err)
	}

	if len(snapshot.Warnings) == 0 {
		t.Fatalf("expected warnings for missing chunks/citations: %#v", snapshot)
	}

	hasMissingCitation := false
	for _, w := range snapshot.Warnings {
		if w.Code == "missing_citation" {
			hasMissingCitation = true
		}
		if w.Code == "stale_index" {
			t.Fatalf("unexpected stale_index before mutation; warnings=%+v", snapshot.Warnings)
		}
	}
	if !hasMissingCitation {
		t.Fatalf("expected missing_citation warning; warnings=%+v", snapshot.Warnings)
	}
}

func TestDesign018ExportContainsRepoIDAndSnapshotID(t *testing.T) {
	ctx := context.Background()
	svc, store := design018Service(t, ctx, "fixture-repo")
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)

	body := "# ID\n\nverifying ids propagate"
	if err := store.UpsertSourceGraph(ctx, cache.SourceGraph{
		Source:     cache.Source{RepoID: "fixture-repo", ID: "DOC-E", Kind: "doc", Path: "docs/e.md", Title: "Export ID Test", Body: body, Status: "ready", ContentHash: index.ContentHash(body), CreatedAt: now, UpdatedAt: now},
		Identities: []cache.Identity{{RepoID: "fixture-repo", SourceID: "DOC-E", AliasType: "path", Alias: "docs/e.md"}},
		SyncStatus: &cache.SyncStatus{RepoID: "fixture-repo", SourceID: "DOC-E", RemoteType: "wiki", RemoteID: "e", RemoteRevision: "rev-e", Status: "fresh", LastFetchedAt: now},
	}); err != nil {
		t.Fatal(err)
	}

	indexResult, err := svc.Index(ctx, OperationRequest{RepoID: "fixture-repo"})
	if err != nil {
		t.Fatalf("Index error: %v", err)
	}
	snapshotID := design018SnapshotID(t, indexResult)

	export, err := svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-repo", SnapshotID: snapshotID, Format: "json", IncludeBody: true})
	if err != nil {
		t.Fatalf("ExportSnapshot error: %v", err)
	}

	if export.RepoID != "fixture-repo" {
		t.Fatalf("export.RepoID=%q want fixture-repo", export.RepoID)
	}
	if export.SnapshotID != snapshotID {
		t.Fatalf("export.SnapshotID=%q want %q", export.SnapshotID, snapshotID)
	}

	var snapshot Snapshot
	if err := json.Unmarshal([]byte(export.InlineContent), &snapshot); err != nil {
		t.Fatalf("invalid snapshot json: %v", err)
	}

	if snapshot.RepoID != "fixture-repo" {
		t.Fatalf("snapshot.RepoID=%q want fixture-repo", snapshot.RepoID)
	}

	if len(snapshot.Sources) == 0 || len(snapshot.Aliases) == 0 || len(snapshot.SyncStatus) == 0 {
		t.Fatalf("snapshot missing required manifest sections: %#v", snapshot)
	}

	for _, chunk := range snapshot.Chunks {
		if chunk.ByteStart < 0 || chunk.ByteEnd == 0 || chunk.LineStart == 0 || chunk.LineEnd == 0 {
			t.Fatalf("chunk citation ranges invalid or missing: %#v", chunk)
		}
	}
}

func TestDesign018RepoScopedSnapshotIsolation(t *testing.T) {
	ctx := context.Background()
	svc, store := design018Service(t, ctx, "fixture-a")
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)

	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-b", Owner: "owner-b", Name: "repo-b", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSourceGraph(ctx, cache.SourceGraph{
		Source:     cache.Source{RepoID: "fixture-a", ID: "DOC-A1", Kind: "doc", Path: "docs/a1.md", Title: "Doc A1", Body: "# A1\n\nrepo a source", Status: "ready", ContentHash: "hash-a1", CreatedAt: now, UpdatedAt: now},
		SyncStatus: &cache.SyncStatus{RepoID: "fixture-a", SourceID: "DOC-A1", RemoteType: "wiki", RemoteID: "a1", RemoteRevision: "rev-a1", Status: "fresh", LastFetchedAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSourceGraph(ctx, cache.SourceGraph{
		Source:     cache.Source{RepoID: "fixture-b", ID: "DOC-B1", Kind: "doc", Path: "docs/b1.md", Title: "Doc B1", Body: "# B1\n\nrepo b source", Status: "ready", ContentHash: "hash-b1", CreatedAt: now, UpdatedAt: now},
		SyncStatus: &cache.SyncStatus{RepoID: "fixture-b", SourceID: "DOC-B1", RemoteType: "wiki", RemoteID: "b1", RemoteRevision: "rev-b1", Status: "fresh", LastFetchedAt: now},
	}); err != nil {
		t.Fatal(err)
	}

	idxA, err := svc.Index(ctx, OperationRequest{RepoID: "fixture-a"})
	if err != nil {
		t.Fatalf("Index fixture-a error: %v", err)
	}
	snapIDa := design018SnapshotID(t, idxA)

	idxB, err := svc.Index(ctx, OperationRequest{RepoID: "fixture-b"})
	if err != nil {
		t.Fatalf("Index fixture-b error: %v", err)
	}
	snapIDb := design018SnapshotID(t, idxB)

	exportA, err := svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-a", SnapshotID: snapIDa, Format: "json", IncludeBody: true})
	if err != nil {
		t.Fatalf("ExportSnapshot fixture-a error: %v", err)
	}
	var snapshotA Snapshot
	if err := json.Unmarshal([]byte(exportA.InlineContent), &snapshotA); err != nil {
		t.Fatal(err)
	}
	for _, s := range snapshotA.Sources {
		if s.ID == "DOC-B1" {
			t.Fatalf("fixture-a snapshot contains fixture-b source: %#v", s)
		}
	}

	exportB, err := svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-b", SnapshotID: snapIDb, Format: "json", IncludeBody: true})
	if err != nil {
		t.Fatalf("ExportSnapshot fixture-b error: %v", err)
	}
	var snapshotB Snapshot
	if err := json.Unmarshal([]byte(exportB.InlineContent), &snapshotB); err != nil {
		t.Fatal(err)
	}
	for _, s := range snapshotB.Sources {
		if s.ID == "DOC-A1" {
			t.Fatalf("fixture-b snapshot contains fixture-a source: %#v", s)
		}
	}
}
GOEOF

cat > "$WORKDIR/internal/cli/design018_validation_test.go" <<'GOEOF'
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitcode-mcp/internal/service"
)

func design018SharedFactory(t *testing.T) serviceFactory {
	t.Helper()
	store := populatedStore(t)
	t.Cleanup(func() { _ = store.Close() })
	return func(context.Context, string) (queryService, func() error, error) {
		return service.New(store), nil, nil
	}
}

func TestDesign018CLIIndexThenExportWithID(t *testing.T) {
	factory := design018SharedFactory(t)

	var indexOut, indexErr bytes.Buffer
	if code := executeWithFactory([]string{"index", "--repo", "fixture-a"}, &indexOut, &indexErr, factory); code != 0 {
		t.Fatalf("index code=%d stderr=%q", code, indexErr.String())
	}
	snapshotID := snapshotIDFromIndexOutput(t, indexOut.String())

	var exportOut, exportErr bytes.Buffer
	exportArgs := []string{"export-snapshot", "--repo", "fixture-a", "--id", snapshotID, "--format", "json"}
	if code := executeWithFactory(exportArgs, &exportOut, &exportErr, factory); code != 0 {
		t.Fatalf("export-snapshot code=%d stderr=%q args=%v", code, exportErr.String(), exportArgs)
	}

	var snapshot service.Snapshot
	if err := json.Unmarshal(exportOut.Bytes(), &snapshot); err != nil {
		t.Fatalf("export-snapshot json invalid: %v content=%s", err, exportOut.String())
	}

	if snapshot.RepoID != "fixture-a" {
		t.Fatalf("snapshot repo_id=%q want fixture-a", snapshot.RepoID)
	}
	if len(snapshot.Sources) == 0 {
		t.Fatalf("snapshot has no sources")
	}
}

func TestDesign018CLIDiffWithBaseAndHeadIDs(t *testing.T) {
	factory := design018SharedFactory(t)

	var i1Out, i1Err bytes.Buffer
	if code := executeWithFactory([]string{"index", "--repo", "fixture-a"}, &i1Out, &i1Err, factory); code != 0 {
		t.Fatalf("index 1 code=%d stderr=%q", code, i1Err.String())
	}
	baseID := snapshotIDFromIndexOutput(t, i1Out.String())

	var i2Out, i2Err bytes.Buffer
	if code := executeWithFactory([]string{"index", "--repo", "fixture-a"}, &i2Out, &i2Err, factory); code != 0 {
		t.Fatalf("index 2 code=%d stderr=%q", code, i2Err.String())
	}
	headID := snapshotIDFromIndexOutput(t, i2Out.String())

	var diffOut, diffErr bytes.Buffer
	diffArgs := []string{"diff-snapshot", "--repo", "fixture-a", "--base-id", baseID, "--head-id", headID, "--format", "json"}
	if code := executeWithFactory(diffArgs, &diffOut, &diffErr, factory); code != 0 {
		t.Fatalf("diff-snapshot code=%d stderr=%q", code, diffErr.String())
	}

	var result service.DiffSnapshotResult
	if err := json.Unmarshal(diffOut.Bytes(), &result); err != nil {
		t.Fatalf("diff-snapshot json invalid: %v content=%s", err, diffOut.String())
	}

	if result.RepoID != "fixture-a" {
		t.Fatalf("diff repo_id=%q want fixture-a", result.RepoID)
	}
	if result.BaseSnapshotID != baseID {
		t.Fatalf("diff base_snapshot_id=%q want %q", result.BaseSnapshotID, baseID)
	}
	if result.HeadSnapshotID != headID {
		t.Fatalf("diff head_snapshot_id=%q want %q", result.HeadSnapshotID, headID)
	}
}

func TestDesign018CLIDiffMissingBaseIDNotFound(t *testing.T) {
	factory := design018SharedFactory(t)

	var i2Out, i2Err bytes.Buffer
	if code := executeWithFactory([]string{"index", "--repo", "fixture-a"}, &i2Out, &i2Err, factory); code != 0 {
		t.Fatalf("index code=%d stderr=%q", code, i2Err.String())
	}
	headID := snapshotIDFromIndexOutput(t, i2Out.String())

	var diffOut, diffErr bytes.Buffer
	diffArgs := []string{"diff-snapshot", "--repo", "fixture-a", "--base-id", "missing-nonexistent-zzz", "--head-id", headID, "--format", "json"}
	code := executeWithFactory(diffArgs, &diffOut, &diffErr, factory)
	if code == 0 {
		t.Fatalf("diff-snapshot with missing base_id unexpectedly succeeded; output=%q", diffOut.String())
	}
	if !strings.Contains(diffErr.String(), "not_found") || !strings.Contains(diffErr.String(), "base_id") || !strings.Contains(diffErr.String(), "missing-nonexistent-zzz") {
		t.Fatalf("stderr missing not_found/base_id detail: stderr=%q stdout=%q", diffErr.String(), diffOut.String())
	}
	if strings.Contains(strings.ToLower(diffOut.String()), "changed") && strings.Contains(strings.ToLower(diffOut.String()), "false") {
		t.Fatalf("diff produced changed:false output for missing base_id: %q", diffOut.String())
	}
}

func TestDesign018CLIExportSnapshotPersistsImmutable(t *testing.T) {
	factory := design018SharedFactory(t)

	var iOut, iErr bytes.Buffer
	if code := executeWithFactory([]string{"index", "--repo", "fixture-a"}, &iOut, &iErr, factory); code != 0 {
		t.Fatalf("index code=%d stderr=%q", code, iErr.String())
	}
	snapshotID := snapshotIDFromIndexOutput(t, iOut.String())

	var e1Out, e1Err bytes.Buffer
	if code := executeWithFactory([]string{"export-snapshot", "--repo", "fixture-a", "--id", snapshotID, "--format", "json"}, &e1Out, &e1Err, factory); code != 0 {
		t.Fatalf("export 1 code=%d stderr=%q", code, e1Err.String())
	}

	var e2Out, e2Err bytes.Buffer
	if code := executeWithFactory([]string{"export-snapshot", "--repo", "fixture-a", "--id", snapshotID, "--format", "json"}, &e2Out, &e2Err, factory); code != 0 {
		t.Fatalf("export 2 code=%d stderr=%q", code, e2Err.String())
	}

	if e1Out.String() != e2Out.String() {
		t.Fatalf("export not deterministic:\n1: %s\n2: %s", e1Out.String(), e2Out.String())
	}
}

func TestDesign018CLIExportToFile(t *testing.T) {
	factory := design018SharedFactory(t)
	outPath := filepath.Join(t.TempDir(), "snapshot.json")

	var iOut, iErr bytes.Buffer
	if code := executeWithFactory([]string{"index", "--repo", "fixture-a"}, &iOut, &iErr, factory); code != 0 {
		t.Fatalf("index code=%d stderr=%q", code, iErr.String())
	}
	snapshotID := snapshotIDFromIndexOutput(t, iOut.String())

	var eOut, eErr bytes.Buffer
	if code := executeWithFactory([]string{"export-snapshot", "--repo", "fixture-a", "--id", snapshotID, "--format", "json", "--output", outPath}, &eOut, &eErr, factory); code != 0 {
		t.Fatalf("export code=%d stderr=%q", code, eErr.String())
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("output file not written: %v", err)
	}
}

func snapshotIDFromIndexOutput(t *testing.T, output string) string {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for _, line := range lines {
		var result service.OperationResult
		if err := json.Unmarshal([]byte(line), &result); err == nil && strings.HasPrefix(result.Evidence, "snapshot_id=") {
			return strings.TrimPrefix(result.Evidence, "snapshot_id=")
		}
		marker := "evidence=snapshot_id="
		if idx := strings.Index(line, marker); idx >= 0 {
			id := strings.TrimSpace(line[idx+len(marker):])
			if id != "" {
				return id
			}
		}
	}
	t.Fatalf("no snapshot_id evidence found in index output: %q", output)
	return ""
}

func TestDesign018ExportDiffUseServiceOnly(t *testing.T) {
	spy := &spyService{}
	factory := func(context.Context, string) (queryService, func() error, error) { return spy, nil, nil }

	var out, errBuf bytes.Buffer
	if code := executeWithFactory([]string{"export-snapshot", "--repo", "fixture-a", "--format", "json"}, &out, &errBuf, factory); code != 0 {
		t.Fatalf("export code=%d stderr=%q", code, errBuf.String())
	}
	if spy.calls["ExportSnapshot"] != 1 {
		t.Fatalf("export did not call ExportSnapshot once; calls=%v", spy.calls)
	}

	spy = &spyService{}
	factory = func(context.Context, string) (queryService, func() error, error) { return spy, nil, nil }
	var dOut, dErr bytes.Buffer
	if code := executeWithFactory([]string{"diff-snapshot", "--repo", "fixture-a", "--format", "json", "--base-id", "a", "--head-id", "b"}, &dOut, &dErr, factory); code != 0 {
		t.Fatalf("diff code=%d stderr=%q", code, dErr.String())
	}
	if spy.calls["DiffSnapshot"] != 1 {
		t.Fatalf("diff did not call DiffSnapshot once; calls=%v", spy.calls)
	}
}
GOEOF

cd "$WORKDIR"
go test ./internal/service -run 'TestDesign018' -count=1 -v
go test ./internal/cli -run 'TestDesign018' -count=1 -v
go test ./...
git -C "$ROOT" diff --check
