package index

import (
	"context"
	"testing"
	"time"
)

func TestFreshnessReportClassifications(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sources := []SourceRecord{
		freshnessSource("fixture-a", "DOC-FRESH", "fresh body", "rev-1", base),
		freshnessSource("fixture-a", "DOC-MISSING", "missing body", "rev-1", base),
		freshnessSource("fixture-a", "DOC-CONTENT", "new body", "rev-1", base),
		freshnessSource("fixture-a", "DOC-REV", "revision body", "rev-2", base.Add(time.Hour)),
		freshnessSource("fixture-a", "DOC-LINK", "link body", "rev-1", base),
	}
	chunks := []Chunk{
		freshnessChunk(sources[0], ContentHash(sources[0].Body), "rev-1", base),
		freshnessChunk(sources[2], "old-hash", "rev-1", base),
		freshnessChunk(sources[3], ContentHash(sources[3].Body), "rev-1", base),
		freshnessChunk(sources[4], ContentHash(sources[4].Body), "rev-1", base),
	}
	linkReport := StaleReport{TotalStaleBacklinks: 1, AffectedSourceIDs: []string{"DOC-LINK"}, UnresolvedTargets: []string{"MISSING-TARGET"}}
	report := BuildFreshnessReport(ctx, sources, nil, chunks, nil, linkReport, ChunkQuery{RepoID: "fixture-a"})
	states := map[string]IndexFreshnessRecord{}
	for _, record := range report.Records {
		states[record.SourceID] = record
	}
	assertFreshness(t, states["DOC-FRESH"], IndexFreshnessFresh, "")
	assertFreshness(t, states["DOC-MISSING"], IndexFreshnessMissingIndex, WarningMissingIndex)
	assertFreshness(t, states["DOC-CONTENT"], IndexFreshnessStaleByContent, WarningStaleIndex)
	assertFreshness(t, states["DOC-REV"], IndexFreshnessStaleByRevision, WarningStaleIndexRevision)
	assertFreshness(t, states["DOC-LINK"], IndexFreshnessLinkStaleOnly, WarningLinkStaleOnly)
	if len(report.Warnings) != 4 {
		t.Fatalf("warnings = %+v", report.Warnings)
	}
	wantOrder := []string{"DOC-CONTENT", "DOC-FRESH", "DOC-LINK", "DOC-MISSING", "DOC-REV"}
	for i, want := range wantOrder {
		if report.Records[i].SourceID != want {
			t.Fatalf("record order[%d] = %s, want %s; report=%+v", i, report.Records[i].SourceID, want, report.Records)
		}
	}
}

func TestFreshnessReportFiltersCitationCountsByScope(t *testing.T) {
	ctx := context.Background()
	source := freshnessSource("fixture-a", "DOC-1", "body", "rev-1", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	source.SnapshotID = "snap-a"
	chunk := freshnessChunk(source, ContentHash(source.Body), "rev-1", source.UpdatedAt)
	anchors := []CitationAnchor{
		{RepoID: "fixture-a", SourceID: "DOC-1", RecordID: "DOC-1", SnapshotID: "snap-a", Policy: ChunkPolicyHeading, ID: "a1"},
		{RepoID: "fixture-a", SourceID: "DOC-1", RecordID: "DOC-1", SnapshotID: "snap-b", Policy: ChunkPolicyHeading, ID: "a2"},
		{RepoID: "fixture-b", SourceID: "DOC-1", RecordID: "DOC-1", SnapshotID: "snap-a", Policy: ChunkPolicyHeading, ID: "a3"},
		{RepoID: "fixture-a", SourceID: "DOC-1", RecordID: "DOC-1", SnapshotID: "snap-a", Policy: ChunkPolicySlidingWindow, ID: "a4"},
	}
	report := BuildFreshnessReport(ctx, []SourceRecord{source}, nil, []Chunk{chunk}, anchors, StaleReport{}, ChunkQuery{RepoID: "fixture-a", SourceID: "DOC-1", RecordID: "DOC-1", SnapshotID: "snap-a", Policy: ChunkPolicyHeading})
	if len(report.Records) != 1 || report.Records[0].CitationCount != 1 {
		t.Fatalf("citation count = %+v, want exactly scoped citation", report.Records)
	}
}

func TestChunkQueryFreshnessWarnings(t *testing.T) {
	ctx := context.Background()
	chunk := Chunk{RepoID: "fixture-a", SourceID: "DOC-1", RecordID: "DOC-1", ContentHash: "hash", ByteStart: 0, ByteEnd: 4, LineStart: 1, LineEnd: 1, Text: "body", NormalizedText: "body", Policy: ChunkPolicyHeading}
	chunk.ID = "chunk-1"
	reader := NewMemoryChunkIndex([]Chunk{chunk})
	warnings := []IndexWarning{
		{RepoID: "fixture-a", SourceID: "DOC-1", Code: WarningStaleIndex, State: IndexFreshnessStaleByContent, Message: "stale"},
		{RepoID: "fixture-a", SourceID: "DOC-OTHER", Code: WarningMissingIndex, State: IndexFreshnessMissingIndex, Message: "missing"},
	}
	listed, err := reader.ListChunksWithWarnings(ctx, ChunkQuery{RepoID: "fixture-a", SourceID: "DOC-1"}, warnings)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Warnings) != 1 || listed.Warnings[0].Code != WarningStaleIndex {
		t.Fatalf("list warnings = %+v", listed.Warnings)
	}
	snippet, err := reader.GetSnippetWithWarnings(ctx, SnippetQuery{RepoID: "fixture-a", SourceID: "DOC-MISSING"}, []IndexWarning{{RepoID: "fixture-a", SourceID: "DOC-MISSING", Code: WarningMissingIndex, State: IndexFreshnessMissingIndex}, {RepoID: "fixture-a", SourceID: "DOC-OTHER", Code: WarningStaleIndex, State: IndexFreshnessStaleByContent}})
	if err != nil {
		t.Fatal(err)
	}
	if snippet.Total != 0 || len(snippet.Warnings) != 1 || snippet.Warnings[0].Code != WarningMissingIndex {
		t.Fatalf("snippet missing warnings = %+v", snippet)
	}
}

func freshnessSource(repoID, sourceID, body, revision string, updated time.Time) SourceRecord {
	return SourceRecord{RepoID: repoID, ID: sourceID, RecordID: sourceID, Body: body, UpdatedAt: updated, RemoteRevision: revision, SyncRevision: revision, Metadata: map[string]string{"content_hash": ContentHash(body)}}
}

func freshnessChunk(source SourceRecord, hash, revision string, updated time.Time) Chunk {
	return Chunk{ID: "chunk-" + source.ID, RepoID: source.RepoID, SourceID: source.ID, RecordID: source.RecordID, ContentHash: hash, ByteStart: 0, ByteEnd: len(source.Body), LineStart: 1, LineEnd: 1, Text: source.Body, NormalizedText: normalizeChunkText(source.Body), Policy: ChunkPolicyHeading, InheritedMetadata: map[string]string{"remote_revision": revision, "sync_revision": revision, "source_updated_at": updated.Format(time.RFC3339Nano)}}
}

func assertFreshness(t *testing.T, record IndexFreshnessRecord, state IndexFreshnessState, warning string) {
	t.Helper()
	if record.State != state || record.WarningCode != warning {
		t.Fatalf("record = %+v, want state=%s warning=%s", record, state, warning)
	}
}
