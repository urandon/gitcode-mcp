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

func TestListSourcesHydratesAliasesInBatchesAndKeepsRepoScope(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()
	mustAddTestRepo(t, ctx, store, "fixture-b")

	for _, repoID := range []string{"fixture-a", "fixture-b"} {
		source := testSource("ISSUE-SHARED", "issue", repoID+" issue")
		source.RepoID = repoID
		alias := "41"
		if repoID == "fixture-b" {
			alias = "42"
		}
		mustUpsertGraph(t, ctx, store, SourceGraph{
			Source: source,
			Identities: []Identity{{
				RepoID:    repoID,
				SourceID:  source.ID,
				AliasType: "issue",
				Alias:     alias,
				Remote:    RemoteAlias{Type: "issue", ID: alias},
			}},
		})
	}

	sources, err := store.ListSources(ctx, SourceFilter{Kind: "issue"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 2 {
		t.Fatalf("ListSources returned %d sources, want 2", len(sources))
	}
	wantAlias := map[string]string{"fixture-a": "41", "fixture-b": "42"}
	for _, source := range sources {
		if len(source.Aliases) != 1 || source.Aliases[0].RepoID != source.RepoID || source.Aliases[0].Alias != wantAlias[source.RepoID] {
			t.Fatalf("source %s aliases = %#v", source.RepoID, source.Aliases)
		}
	}
}

func TestSourceKindCountsAggregatesWithoutCrossingRepositoryScope(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()
	mustAddTestRepo(t, ctx, store, "fixture-b")

	for _, source := range []Source{
		testSource("ISSUE-1", "issue", "Issue one"),
		testSource("ISSUE-2", "issue", "Issue two"),
		testSource("WIKI-1", "wiki", "Wiki one"),
	} {
		mustUpsertGraph(t, ctx, store, SourceGraph{Source: source})
	}
	other := testSource("ISSUE-3", "issue", "Other repository")
	other.RepoID = "fixture-b"
	mustUpsertGraph(t, ctx, store, SourceGraph{Source: other})

	counts, err := store.SourceKindCounts(ctx, "fixture-a")
	if err != nil {
		t.Fatal(err)
	}
	want := []SourceKindCount{{Kind: "issue", Count: 2}, {Kind: "wiki", Count: 1}}
	if !reflect.DeepEqual(counts, want) {
		t.Fatalf("SourceKindCounts = %#v, want %#v", counts, want)
	}
}

func TestSyncFrontierRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()
	if err := store.AddRepository(ctx, RepositoryBinding{RepoID: "frontier-repo", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []RepositoryScope{RepositoryScopeIssues}}); err != nil {
		t.Fatalf("AddRepository returned error: %v", err)
	}
	high := time.Date(2026, 6, 30, 8, 0, 0, 0, time.UTC)
	updated := high.Add(time.Minute)
	want := SyncFrontier{RepoID: "frontier-repo", RemoteType: "issue", Ordering: "updated_at_desc", FilterKey: "state=all", Status: "complete", HighUpdatedAt: high, HighRemoteID: "42", HighNumber: 42, StopReason: "end_of_collection", PagesListed: 3, RecordsListed: 250, UpdatedAt: updated}
	if err := store.UpsertSyncFrontier(ctx, want); err != nil {
		t.Fatalf("UpsertSyncFrontier returned error: %v", err)
	}
	got, ok, err := store.GetSyncFrontier(ctx, "frontier-repo", "issue", "updated_at_desc", "state=all")
	if err != nil || !ok {
		t.Fatalf("GetSyncFrontier ok=%v err=%v", ok, err)
	}
	if got.Status != want.Status || got.HighRemoteID != want.HighRemoteID || got.HighNumber != want.HighNumber || got.StopReason != want.StopReason || got.PagesListed != want.PagesListed || got.RecordsListed != want.RecordsListed {
		t.Fatalf("frontier = %#v, want %#v", got, want)
	}
	if !got.HighUpdatedAt.Equal(high) || !got.UpdatedAt.Equal(updated) {
		t.Fatalf("frontier times = %s/%s, want %s/%s", got.HighUpdatedAt, got.UpdatedAt, high, updated)
	}
}

func TestCommitSyncBatchRollsBackGraphsAndFrontierTogether(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()
	now := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	valid := SyncGraph{RepoID: "fixture-a", Record: Record{RepoID: "fixture-a", ID: "ISSUE-ATOMIC-1", Type: "issue", Path: "issues/atomic-1.md", Title: "Atomic one", Body: "body", Status: "open", ContentHash: "atomic-1", Provenance: ProvenanceRemote, CreatedAt: now, UpdatedAt: now}}
	frontier := SyncFrontier{RepoID: "fixture-a", RemoteType: "issue", Ordering: "updated_at_desc", FilterKey: "state=all", Status: "complete", HighUpdatedAt: now, HighRemoteID: "2", HighNumber: 2, StopReason: "end_of_collection", PagesListed: 1, RecordsListed: 2, UpdatedAt: now}
	invalidFrontier := frontier
	invalidFrontier.RepoID = "missing-repository"
	clearTarget := SyncGraph{RepoID: "fixture-a", Record: Record{RepoID: "fixture-a", ID: "ISSUE-ATOMIC-CLEAR", Type: "issue", Path: "issues/atomic-clear.md", Title: "Preserve comment", Status: "open", ContentHash: "atomic-clear", Provenance: ProvenanceRemote, CreatedAt: now, UpdatedAt: now}, Comments: []RecordComment{{CommentID: "keep-me", Body: "preserved", CreatedAt: now, UpdatedAt: now}}}
	if err := store.UpsertSyncGraph(ctx, clearTarget); err != nil {
		t.Fatal(err)
	}
	child := SyncGraph{RepoID: "fixture-a", Record: Record{RepoID: "fixture-a", ID: "ISSUECOMMENT-ATOMIC-KEEP", Type: "issue_comment", Path: "issues/atomic-clear/comments/keep.md", Title: "Preserve projection", Body: "preserved", Status: "current", ContentHash: "atomic-child", Provenance: ProvenanceProjection, CreatedAt: now, UpdatedAt: now}, Links: []Link{{RepoID: "fixture-a", SourceID: "ISSUECOMMENT-ATOMIC-KEEP", TargetID: clearTarget.Record.ID, Kind: "parent"}}}
	if err := store.UpsertSyncGraph(ctx, child); err != nil {
		t.Fatal(err)
	}

	queueItem := IssueCommentSync{RepoID: "fixture-a", SourceID: valid.Record.ID, IssueNumber: 1, RemoteID: "1", RemoteRevision: "rev-1", Status: "pending", UpdatedAt: now}
	maintenance := MaintenanceFrontier{RepoID: "fixture-a", RemoteType: "issue", Ordering: "updated_at_desc", FilterKey: "all", Lane: "head", Status: "fresh", UpdatedAt: now}
	receipt := SyncCommitReceipt{StageID: "stage-atomic", Checksum: "checksum", RepoID: "fixture-a", Collection: "issues", CommittedAt: now}
	if err := store.CommitSyncBatch(ctx, SyncBatch{Graphs: []SyncGraph{valid}, Frontier: &invalidFrontier, MaintenanceFrontier: &maintenance, Receipt: &receipt, IssueCommentSyncs: []IssueCommentSync{queueItem}, ClearRecordCommentRefs: []RecordRef{{RepoID: "fixture-a", RecordID: clearTarget.Record.ID}}, ReplaceRecordComments: []RecordCommentsReplacement{{RepoID: "fixture-a", RecordID: clearTarget.Record.ID, Comments: []RecordComment{{CommentID: "replace-me", Body: "must roll back", CreatedAt: now, UpdatedAt: now}}}}, ReconcileChildren: []ChildSourceReconciliation{{RepoID: "fixture-a", ParentID: clearTarget.Record.ID, Kind: "issue_comment"}}}); err == nil {
		t.Fatal("CommitSyncBatch succeeded with an invalid terminal frontier")
	}
	if _, err := store.GetSourceScoped(ctx, "fixture-a", valid.Record.ID); err == nil {
		t.Fatal("first graph survived a failed atomic batch")
	}
	if _, ok, err := store.GetSyncFrontier(ctx, "fixture-a", "issue", "updated_at_desc", "state=all"); err != nil || ok {
		t.Fatalf("frontier ok=%v err=%v after failed atomic batch", ok, err)
	}
	if _, ok, err := store.GetIssueCommentSync(ctx, "fixture-a", valid.Record.ID); err != nil || ok {
		t.Fatalf("comment queue ok=%v err=%v after failed atomic batch", ok, err)
	}
	if frontiers, err := store.ListMaintenanceFrontiers(ctx, "fixture-a"); err != nil || len(frontiers) != 0 {
		t.Fatalf("maintenance frontiers=%+v err=%v after failed atomic batch", frontiers, err)
	}
	if _, ok, err := store.GetSyncCommitReceipt(ctx, receipt.StageID); err != nil || ok {
		t.Fatalf("receipt ok=%v err=%v after failed atomic batch", ok, err)
	}
	preserved, err := store.GetRecord(ctx, "fixture-a", clearTarget.Record.ID)
	if err != nil || len(preserved.Comments) != 1 || preserved.Comments[0].CommentID != "keep-me" {
		t.Fatalf("preserved record=%+v err=%v after failed atomic batch", preserved, err)
	}
	if _, err := store.GetSourceScoped(ctx, "fixture-a", child.Record.ID); err != nil {
		t.Fatalf("child reconciliation escaped failed atomic batch: %v", err)
	}
}

func TestCommitSyncBatchPublishesGraphFrontierAndReceiptTogether(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()
	now := time.Date(2026, 9, 1, 8, 30, 0, 0, time.UTC)
	graph := SyncGraph{RepoID: "fixture-a", Record: Record{RepoID: "fixture-a", ID: "ISSUE-ATOMIC-RECEIPT", Type: "issue", Path: "issues/atomic-receipt.md", Title: "Atomic receipt", Status: "open", ContentHash: "atomic-receipt", Provenance: ProvenanceRemote, CreatedAt: now, UpdatedAt: now}}
	maintenance := MaintenanceFrontier{RepoID: "fixture-a", RemoteType: "issue", Ordering: "updated_at_desc", FilterKey: "all", Lane: "head", Status: "fresh", Checkpoint: "next_page:2", UpdatedAt: now}
	receipt := SyncCommitReceipt{StageID: "stage-atomic-receipt", Checksum: "checksum-atomic", RepoID: "fixture-a", Collection: "issues", CommittedAt: now}
	if err := store.CommitSyncBatch(ctx, SyncBatch{Graphs: []SyncGraph{graph}, MaintenanceFrontier: &maintenance, Receipt: &receipt}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetSourceScoped(ctx, "fixture-a", graph.Record.ID); err != nil {
		t.Fatal(err)
	}
	frontiers, err := store.ListMaintenanceFrontiers(ctx, "fixture-a")
	if err != nil || len(frontiers) != 1 || frontiers[0].Checkpoint != "next_page:2" {
		t.Fatalf("frontiers=%+v err=%v", frontiers, err)
	}
	got, ok, err := store.GetSyncCommitReceipt(ctx, receipt.StageID)
	if err != nil || !ok || got.Checksum != receipt.Checksum || !got.CommittedAt.Equal(now) {
		t.Fatalf("receipt=%+v ok=%v err=%v", got, ok, err)
	}
}

func TestRecentCompletedSyncEventsAreBoundedAndNewestFirst(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	mustUpsertGraph(t, ctx, store, SourceGraph{Source: testSource("ISSUE-SYNC", "issue", "Sync target")})
	for i := 1; i <= 3; i++ {
		event := SyncEvent{RepoID: "fixture-a", ID: fmt.Sprintf("sync-%d", i), SourceID: "ISSUE-SYNC", RemoteType: "issue", Status: "succeeded", IdempotencyKey: fmt.Sprintf("ik-sync-%d", i), CreatedAt: now.Add(time.Duration(i) * time.Minute), CompletedAt: now.Add(time.Duration(i) * time.Minute)}
		if err := store.RecordSyncEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	events, err := store.RecentSyncEventSummaries(ctx, "fixture-a", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].ID != "sync-3" || events[1].ID != "sync-2" {
		t.Fatalf("recent events=%+v", events)
	}
}

func TestIssueCommentSyncQueueRoundTripAndReplaceComments(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	if err := store.UpsertRecordGraph(ctx, RecordGraph{Record: Record{RepoID: "fixture-a", ID: "ISSUE-51", Type: "issue", Path: "issues/51.md", Title: "Deferred comments", Body: "body", Status: "open", ContentHash: "hash", Provenance: ProvenanceRemote, RemoteType: "issue", RemoteID: "51", RemoteRevision: "rev-1", CreatedAt: now, UpdatedAt: now}}); err != nil {
		t.Fatalf("UpsertRecordGraph returned error: %v", err)
	}
	item := IssueCommentSync{RepoID: "fixture-a", SourceID: "ISSUE-51", IssueNumber: 51, RemoteID: "51", RemoteRevision: "rev-1", ExpectedCount: 2, Status: "pending", UpdatedAt: now}
	if err := store.UpsertIssueCommentSync(ctx, item); err != nil {
		t.Fatalf("UpsertIssueCommentSync returned error: %v", err)
	}
	got, ok, err := store.GetIssueCommentSync(ctx, "fixture-a", "ISSUE-51")
	if err != nil || !ok || got.Status != "pending" || got.ExpectedCount != 2 {
		t.Fatalf("GetIssueCommentSync ok=%v err=%v item=%#v", ok, err, got)
	}
	if err := store.ReplaceRecordComments(ctx, "fixture-a", "ISSUE-51", []RecordComment{{CommentID: "c1", Body: "one", CreatedAt: now, UpdatedAt: now}, {CommentID: "c2", Body: "two", CreatedAt: now, UpdatedAt: now}}); err != nil {
		t.Fatalf("ReplaceRecordComments returned error: %v", err)
	}
	if err := store.ReplaceRecordComments(ctx, "fixture-a", "ISSUE-51", []RecordComment{{CommentID: "c2", Body: "updated", CreatedAt: now, UpdatedAt: now.Add(time.Minute)}}); err != nil {
		t.Fatalf("ReplaceRecordComments second returned error: %v", err)
	}
	record, err := store.GetRecord(ctx, "fixture-a", "ISSUE-51")
	if err != nil || len(record.Comments) != 1 || record.Comments[0].CommentID != "c2" || record.Comments[0].Body != "updated" {
		t.Fatalf("record=%#v err=%v", record, err)
	}
	summary, err := store.IssueCommentSyncSummary(ctx, "fixture-a")
	if err != nil || summary.Pending != 1 || summary.Total != 1 {
		t.Fatalf("summary=%#v err=%v", summary, err)
	}
}

func TestRecordGraphReplacesLinksByKind(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()
	now := time.Date(2026, 7, 28, 18, 30, 0, 0, time.UTC)
	issue := Record{RepoID: "fixture-a", ID: "ISSUE-1", Type: "issue", Path: "issues/1.md", Title: "Issue", Status: "open", ContentHash: "issue-1", Provenance: ProvenanceRemote, RemoteType: "issue", RemoteID: "1", CreatedAt: now, UpdatedAt: now}
	milestone := Record{RepoID: "fixture-a", ID: "MILESTONE-1", Type: "milestone", Path: "milestones/1.md", Title: "Release 1", Status: "open", ContentHash: "milestone-1", Provenance: ProvenanceRemote, RemoteType: "milestone", RemoteID: "1", CreatedAt: now, UpdatedAt: now}
	if err := store.UpsertRecordGraph(ctx, RecordGraph{
		Record:           issue,
		RelatedRecords:   []Record{milestone},
		ReplaceLinkKinds: []string{"milestone"},
		Links:            []Link{{TargetID: "MILESTONE-1", Kind: "milestone", Text: "Release 1"}},
	}); err != nil {
		t.Fatal(err)
	}
	links, err := store.ListLinks(ctx, LinkFilter{RepoID: "fixture-a", SourceID: "ISSUE-1"})
	if err != nil || len(links) != 1 || links[0].TargetID != "MILESTONE-1" {
		t.Fatalf("links=%#v err=%v", links, err)
	}
	issue.ContentHash = "issue-cleared"
	if err := store.UpsertRecordGraph(ctx, RecordGraph{Record: issue, ReplaceLinkKinds: []string{"milestone"}}); err != nil {
		t.Fatal(err)
	}
	links, err = store.ListLinks(ctx, LinkFilter{RepoID: "fixture-a", SourceID: "ISSUE-1"})
	if err != nil || len(links) != 0 {
		t.Fatalf("cleared links=%#v err=%v", links, err)
	}
}

func TestRecordCommentsPageUpsertAndSuccessfulReconciliation(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()
	now := time.Date(2026, 7, 13, 13, 0, 0, 0, time.UTC)
	if err := store.UpsertRecordGraph(ctx, RecordGraph{Record: Record{RepoID: "fixture-a", ID: "ISSUE-59", Type: "issue", Path: "issues/59.md", Title: "Aggregate comments", Status: "open", ContentHash: "hash", Provenance: ProvenanceRemote, RemoteType: "issue", RemoteID: "59", CreatedAt: now, UpdatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRecordComments(ctx, "fixture-a", "ISSUE-59", []RecordComment{{CommentID: "c1", Body: "first", CreatedAt: now, UpdatedAt: now}, {CommentID: "stale", Body: "stale", CreatedAt: now, UpdatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRecordComments(ctx, "fixture-a", "ISSUE-59", []RecordComment{{CommentID: "c1", Body: "updated", CreatedAt: now, UpdatedAt: now.Add(time.Minute)}, {CommentID: "c2", Body: "second", CreatedAt: now, UpdatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReconcileRecordComments(ctx, "fixture-a", "ISSUE-59", []string{"c1", "c2"}); err != nil {
		t.Fatal(err)
	}
	record, err := store.GetRecord(ctx, "fixture-a", "ISSUE-59")
	if err != nil || len(record.Comments) != 2 || record.Comments[0].CommentID != "c1" || record.Comments[0].Body != "updated" || record.Comments[1].CommentID != "c2" {
		t.Fatalf("record=%#v err=%v", record, err)
	}
}

func TestReconcileChildSourcesRemovesOnlyStaleChildrenForParent(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

	for _, number := range []int{7, 8} {
		if err := store.UpsertSyncGraph(ctx, SyncGraph{
			RepoID:     "fixture-a",
			Provenance: ProvenanceLive,
			Record:     Record{ID: fmt.Sprintf("ISSUE-%d", number), Type: "issue", Path: fmt.Sprintf("issues/%d.md", number), Title: fmt.Sprintf("Issue %d", number), Status: "open", ContentHash: fmt.Sprintf("issue-%d", number), RemoteType: "issue", RemoteID: fmt.Sprintf("%d", number), CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, graph := range []SyncGraph{
		{
			RepoID:     "fixture-a",
			Provenance: ProvenanceLive,
			Record:     Record{ID: "ISSUECOMMENT-7-c1", Type: "issue_comment", Path: "issues/7/comments/c1.md", Title: "Stale", Body: "stale child token", Status: "current", ContentHash: "c1", RemoteType: "issue_comment", RemoteID: "7:c1", CreatedAt: now, UpdatedAt: now},
			Links:      []Link{{SourceID: "ISSUECOMMENT-7-c1", TargetID: "ISSUE-7", Kind: "parent", Text: "issue"}},
		},
		{
			RepoID:     "fixture-a",
			Provenance: ProvenanceLive,
			Record:     Record{ID: "ISSUECOMMENT-7-c2", Type: "issue_comment", Path: "issues/7/comments/c2.md", Title: "Current", Body: "current child token", Status: "current", ContentHash: "c2", RemoteType: "issue_comment", RemoteID: "7:c2", CreatedAt: now, UpdatedAt: now},
			Links:      []Link{{SourceID: "ISSUECOMMENT-7-c2", TargetID: "ISSUE-7", Kind: "parent", Text: "issue"}},
		},
		{
			RepoID:     "fixture-a",
			Provenance: ProvenanceLive,
			Record:     Record{ID: "ISSUECOMMENT-8-c1", Type: "issue_comment", Path: "issues/8/comments/c1.md", Title: "Other parent", Body: "other child token", Status: "current", ContentHash: "other", RemoteType: "issue_comment", RemoteID: "8:c1", CreatedAt: now, UpdatedAt: now},
			Links:      []Link{{SourceID: "ISSUECOMMENT-8-c1", TargetID: "ISSUE-8", Kind: "parent", Text: "issue"}},
		},
	} {
		if err := store.UpsertSyncGraph(ctx, graph); err != nil {
			t.Fatal(err)
		}
	}

	if err := store.ReconcileChildSources(ctx, "fixture-a", "ISSUE-7", "issue_comment", []string{"ISSUECOMMENT-7-c2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetSourceScoped(ctx, "fixture-a", "ISSUECOMMENT-7-c1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale child source error = %v, want ErrNotFound", err)
	}
	if _, err := store.GetRecord(ctx, "fixture-a", "ISSUECOMMENT-7-c1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale child record error = %v, want ErrNotFound", err)
	}
	for _, id := range []string{"ISSUECOMMENT-7-c2", "ISSUECOMMENT-8-c1"} {
		if _, err := store.GetSourceScoped(ctx, "fixture-a", id); err != nil {
			t.Fatalf("preserved child %s: %v", id, err)
		}
	}
	results, err := store.SearchSources(ctx, SearchQuery{RepoID: "fixture-a", Query: "stale child token", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("stale child remained searchable: %#v", results)
	}
}

func TestRAGEmbeddingSchemaRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()
	now := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	mustUpsertGraph(t, ctx, store, SourceGraph{
		Source: testSource("ISSUE-40", "issue", "RAG schema"),
		Chunks: []Chunk{{
			ID:             "chunk-40-1",
			SourceID:       "ISSUE-40",
			RecordID:       "ISSUE-40",
			ContentHash:    "chunk-hash-1",
			ByteStart:      0,
			ByteEnd:        32,
			LineStart:      1,
			LineEnd:        2,
			HeadingPath:    []string{"RAG"},
			Text:           "русский 中文 English",
			NormalizedText: "русский 中文 english",
			Policy:         "heading-v1",
		}},
	})
	identity := EmbeddingNamespaceIdentity{
		RepoID:                "fixture-a",
		ProfileID:             "qwen3-ollama-0_6b-1024",
		ProviderID:            "ollama-local",
		ProviderType:          "ollama",
		ModelID:               "qwen3-embedding:0.6b",
		ModelRevision:         "sha256:one",
		Dimensions:            1024,
		DType:                 "float32",
		Normalization:         "l2",
		DocumentInstructionID: "doc-default",
		QueryInstructionID:    "query-default",
		ChunkPolicyID:         "heading-v1",
		LanguagePolicyID:      "ru-zh-en-v1",
		ConfigHash:            "config-hash-1",
	}
	namespace, err := store.UpsertEmbeddingNamespace(ctx, EmbeddingNamespace{EmbeddingNamespaceIdentity: identity, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatalf("UpsertEmbeddingNamespace returned error: %v", err)
	}
	if namespace.ID == "" {
		t.Fatalf("namespace ID is empty")
	}
	resolved, ok, err := store.ResolveEmbeddingNamespace(ctx, identity)
	if err != nil || !ok {
		t.Fatalf("ResolveEmbeddingNamespace ok=%v err=%v", ok, err)
	}
	if resolved.ID != namespace.ID || resolved.LanguagePolicyID != "ru-zh-en-v1" {
		t.Fatalf("resolved namespace=%#v, want id=%q", resolved, namespace.ID)
	}
	aliasIdentity := identity
	aliasIdentity.ProfileID = "same-model-alias"
	aliasNamespace, err := store.UpsertEmbeddingNamespace(ctx, EmbeddingNamespace{EmbeddingNamespaceIdentity: aliasIdentity, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatalf("UpsertEmbeddingNamespace alias returned error: %v", err)
	}
	if aliasNamespace.ID != namespace.ID {
		t.Fatalf("same model identity produced namespace id %q, want %q", aliasNamespace.ID, namespace.ID)
	}
	changedIdentity := identity
	changedIdentity.ModelRevision = "sha256:two"
	changedNamespace, err := store.UpsertEmbeddingNamespace(ctx, EmbeddingNamespace{EmbeddingNamespaceIdentity: changedIdentity, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatalf("UpsertEmbeddingNamespace changed returned error: %v", err)
	}
	if changedNamespace.ID == namespace.ID {
		t.Fatalf("changed model revision reused namespace id %q", namespace.ID)
	}
	if err := store.UpsertChunkEmbedding(ctx, ChunkEmbedding{RepoID: "fixture-a", NamespaceID: namespace.ID, ChunkID: "chunk-40-1", Vector: []byte{1, 2, 3, 4}, Dimensions: 1024, DType: "float32", EmbeddedAt: now}); err != nil {
		t.Fatalf("UpsertChunkEmbedding returned error: %v", err)
	}
	embeddings, err := store.ListChunkEmbeddings(ctx, ChunkEmbeddingFilter{RepoID: "fixture-a", NamespaceID: namespace.ID})
	if err != nil {
		t.Fatalf("ListChunkEmbeddings returned error: %v", err)
	}
	if len(embeddings) != 1 || embeddings[0].SourceID != "ISSUE-40" || embeddings[0].ChunkContentHash != "chunk-hash-1" || embeddings[0].VectorHash == "" {
		t.Fatalf("embeddings=%#v", embeddings)
	}
	run := RAGIndexRun{RepoID: "fixture-a", ID: "rag-run-1", NamespaceID: namespace.ID, ProfileID: identity.ProfileID, Status: "running", TotalChunks: 1, EmbeddedChunks: 1, StartedAt: now, UpdatedAt: now, Metadata: map[string]string{"provider": "ollama"}}
	if err := store.UpsertRAGIndexRun(ctx, run); err != nil {
		t.Fatalf("UpsertRAGIndexRun returned error: %v", err)
	}
	run.Status = "succeeded"
	run.CompletedAt = now.Add(time.Second)
	if err := store.UpsertRAGIndexRun(ctx, run); err != nil {
		t.Fatalf("UpsertRAGIndexRun update returned error: %v", err)
	}
	gotRun, err := store.GetRAGIndexRun(ctx, "fixture-a", "rag-run-1")
	if err != nil {
		t.Fatalf("GetRAGIndexRun returned error: %v", err)
	}
	if gotRun.Status != "succeeded" || gotRun.Metadata["provider"] != "ollama" || gotRun.EmbeddedChunks != 1 {
		t.Fatalf("run=%#v", gotRun)
	}
	counts, err := store.RecordCounts(ctx, "fixture-a")
	if err != nil {
		t.Fatalf("RecordCounts returned error: %v", err)
	}
	if counts.Chunks != 1 || counts.RAGNamespaces != 2 || counts.RAGEmbeddings != 1 || counts.RAGIndexRuns != 1 {
		t.Fatalf("RecordCounts = %#v", counts)
	}
}

func TestResetLiveClearsRemoteRecordsAndFrontiers(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()
	mustAddTestRepo(t, ctx, store, "other-repo")
	now := time.Date(2026, 6, 30, 9, 0, 0, 0, time.UTC)
	graph := SyncGraph{
		RepoID: "fixture-a",
		Record: Record{
			ID:             "ISSUE-100",
			Type:           "issue",
			Path:           "issues/100.md",
			Title:          "Remote",
			Body:           "remote body",
			Status:         "open",
			ContentHash:    "remote-hash",
			Provenance:     ProvenanceRemote,
			RemoteType:     "issue",
			RemoteID:       "100",
			RemoteRevision: "rev-100",
			CreatedAt:      now,
			UpdatedAt:      now,
		},
		Comments:        []RecordComment{{CommentID: "c100", Author: "alice", Body: "comment", ContentHash: "comment-hash", CreatedAt: now, UpdatedAt: now}},
		RemoteRevisions: []RemoteRevision{{RecordID: "ISSUE-100", RemoteType: "issue", RemoteID: "100", RemoteRevision: "rev-100", Status: "fresh", LastFetchedAt: now}},
		SyncEvents:      []SyncEvent{{ID: "sync-100", SourceID: "ISSUE-100", RemoteType: "issue", RemoteID: "100", RemoteRevision: "rev-100", Status: "succeeded", IdempotencyKey: "sync-100", Message: "ok", CreatedAt: now, StartedAt: now, CompletedAt: now}},
		PRReviewDiscussions: []PRReviewDiscussion{{
			RepoID: "fixture-a", PRNumber: 100, DiscussionID: "discussion-100", Kind: "inline", CreatedAt: now, UpdatedAt: now,
		}},
		PRReviewPositions: []PRReviewPosition{{
			RepoID: "fixture-a", PRNumber: 100, CommentID: "c100", PositionKind: "current", DiscussionID: "discussion-100", NewPath: "x.go", NewLine: 10, CreatedAt: now, UpdatedAt: now,
		}},
	}
	if err := store.UpsertSyncGraph(ctx, graph); err != nil {
		t.Fatalf("UpsertSyncGraph returned error: %v", err)
	}
	if err := store.UpsertRecordGraph(ctx, RecordGraph{Record: Record{RepoID: "other-repo", ID: "ISSUE-200", Type: "issue", Path: "issues/200.md", Title: "Other", Body: "other body", Status: "open", ContentHash: "other-hash", Provenance: ProvenanceRemote, RemoteType: "issue", RemoteID: "200", CreatedAt: now, UpdatedAt: now}}); err != nil {
		t.Fatalf("UpsertRecordGraph other repo returned error: %v", err)
	}
	if err := store.UpsertSyncFrontier(ctx, SyncFrontier{RepoID: "fixture-a", RemoteType: "issue", Ordering: "updated_at_desc", FilterKey: "state=all", Status: "complete", HighUpdatedAt: now, HighRemoteID: "100", HighNumber: 100, StopReason: "end_of_collection", UpdatedAt: now}); err != nil {
		t.Fatalf("UpsertSyncFrontier returned error: %v", err)
	}
	if err := store.UpsertSyncFrontier(ctx, SyncFrontier{RepoID: "other-repo", RemoteType: "issue", Ordering: "updated_at_desc", FilterKey: "state=all", Status: "complete", HighUpdatedAt: now, HighRemoteID: "200", HighNumber: 200, StopReason: "end_of_collection", UpdatedAt: now}); err != nil {
		t.Fatalf("UpsertSyncFrontier other repo returned error: %v", err)
	}
	if err := store.UpsertIssueCommentSync(ctx, IssueCommentSync{RepoID: "fixture-a", SourceID: "ISSUE-100", IssueNumber: 100, RemoteID: "100", RemoteRevision: "rev-100", ExpectedCount: 1, Status: "pending", UpdatedAt: now}); err != nil {
		t.Fatalf("UpsertIssueCommentSync returned error: %v", err)
	}
	if err := store.UpsertIssueCommentSync(ctx, IssueCommentSync{RepoID: "other-repo", SourceID: "ISSUE-200", IssueNumber: 200, RemoteID: "200", RemoteRevision: "rev-200", ExpectedCount: 1, Status: "pending", UpdatedAt: now}); err != nil {
		t.Fatalf("UpsertIssueCommentSync other repo returned error: %v", err)
	}

	if err := store.ResetLive(ctx, "fixture-a"); err != nil {
		t.Fatalf("ResetLive returned error: %v", err)
	}

	for _, table := range []string{"sources", "records", "record_comments", "remote_revisions", "sync_events", "pr_review_discussions", "pr_review_positions", "sync_frontiers", "issue_comment_sync"} {
		if got := countRepoRows(t, ctx, store, table, "fixture-a"); got != 0 {
			t.Fatalf("%s fixture-a rows=%d, want 0", table, got)
		}
	}
	if store.useFTS {
		if got := countRepoRows(t, ctx, store, "fts_index", "fixture-a"); got != 0 {
			t.Fatalf("fts_index fixture-a rows=%d, want 0", got)
		}
	}
	if got := countRepoRows(t, ctx, store, "records", "other-repo"); got != 1 {
		t.Fatalf("records other-repo rows=%d, want 1", got)
	}
	if got := countRepoRows(t, ctx, store, "sync_frontiers", "other-repo"); got != 1 {
		t.Fatalf("sync_frontiers other-repo rows=%d, want 1", got)
	}
	if got := countRepoRows(t, ctx, store, "issue_comment_sync", "other-repo"); got != 1 {
		t.Fatalf("issue_comment_sync other-repo rows=%d, want 1", got)
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

func TestAuditClaimIsAtomicAndOnlyFailedAttemptCanRetry(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()
	entry := AuditTrailEntry{
		RepoID:         "fixture-a",
		ID:             "write-mirror-key",
		Operation:      "trigger-push-mirror",
		Command:        "trigger-push-mirror",
		Mode:           "live",
		RecordID:       "PUSHMIRROR-17",
		RemoteType:     "push_remote_mirror",
		RemoteID:       "17",
		IdempotencyKey: "mirror-key",
		Status:         "in_progress",
		PayloadHash:    "payload-hash",
		CreatedAt:      time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC),
	}

	claimed, err := store.ClaimAuditEvent(ctx, entry)
	if err != nil || !claimed {
		t.Fatalf("first claim=%t err=%v", claimed, err)
	}
	claimed, err = store.ClaimAuditEvent(ctx, entry)
	if err != nil || claimed {
		t.Fatalf("duplicate in-progress claim=%t err=%v", claimed, err)
	}

	failed := entry
	failed.Status = "failed"
	if err := store.RecordAuditEvent(ctx, failed); err != nil {
		t.Fatal(err)
	}
	claimed, err = store.ClaimAuditEvent(ctx, entry)
	if err != nil || !claimed {
		t.Fatalf("failed retry claim=%t err=%v", claimed, err)
	}

	succeeded := entry
	succeeded.Status = "succeeded"
	if err := store.RecordAuditEvent(ctx, succeeded); err != nil {
		t.Fatal(err)
	}
	claimed, err = store.ClaimAuditEvent(ctx, entry)
	if err != nil || claimed {
		t.Fatalf("succeeded claim=%t err=%v", claimed, err)
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

func TestUpsertSyncGraphReplacesStaleChunksAndEmbeddings(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	record := Record{ID: "ISSUE-79", Type: "issue", Path: "issues/79.md", Title: "Lifecycle", Body: "old body", Status: "open", ContentHash: "old-record", Provenance: ProvenanceRemote, RemoteType: "issue", RemoteID: "79", CreatedAt: now, UpdatedAt: now}
	oldChunk := Chunk{ID: "chunk-old", SourceID: record.ID, RecordID: record.ID, ContentHash: "old-chunk", Text: "old body", NormalizedText: "old body", Policy: "heading-v1"}
	if err := store.UpsertSyncGraph(ctx, SyncGraph{RepoID: "fixture-a", Record: record, Chunks: []Chunk{oldChunk}, ReplaceChunks: true}); err != nil {
		t.Fatal(err)
	}
	identity := EmbeddingNamespaceIdentity{RepoID: "fixture-a", ProfileID: "profile", ProviderID: "provider", ProviderType: "test", ModelID: "model", ModelRevision: "rev-1", Dimensions: 2, DType: "float32", Normalization: "l2", DocumentInstructionID: "doc", QueryInstructionID: "query", ChunkPolicyID: "heading-v1", LanguagePolicyID: "default", ConfigHash: "config"}
	namespace, err := store.UpsertEmbeddingNamespace(ctx, EmbeddingNamespace{EmbeddingNamespaceIdentity: identity, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertChunkEmbedding(ctx, ChunkEmbedding{RepoID: "fixture-a", NamespaceID: namespace.ID, ChunkID: oldChunk.ID, Vector: []byte{1, 2}, Dimensions: 2, DType: "float32", EmbeddedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSyncGraph(ctx, SyncGraph{RepoID: "fixture-a", Record: record, Chunks: []Chunk{oldChunk}, ReplaceChunks: true}); err != nil {
		t.Fatal(err)
	}
	embeddings, err := store.ListChunkEmbeddings(ctx, ChunkEmbeddingFilter{RepoID: "fixture-a", NamespaceID: namespace.ID})
	if err != nil || len(embeddings) != 1 {
		t.Fatalf("unchanged replacement discarded embedding: %#v err=%v", embeddings, err)
	}
	record.Body = "new body"
	record.ContentHash = "new-record"
	record.UpdatedAt = now.Add(time.Minute)
	newChunk := Chunk{ID: "chunk-new", SourceID: record.ID, RecordID: record.ID, ContentHash: "new-chunk", Text: "new body", NormalizedText: "new body", Policy: "heading-v1"}
	if err := store.UpsertSyncGraph(ctx, SyncGraph{RepoID: "fixture-a", Record: record, Chunks: []Chunk{newChunk}, ReplaceChunks: true}); err != nil {
		t.Fatal(err)
	}
	chunks, err := store.GetChunks(ctx, record.ID)
	if err != nil || len(chunks) != 1 || chunks[0].ID != newChunk.ID {
		t.Fatalf("replacement chunks=%#v err=%v", chunks, err)
	}
	embeddings, err = store.ListChunkEmbeddings(ctx, ChunkEmbeddingFilter{RepoID: "fixture-a", NamespaceID: namespace.ID})
	if err != nil || len(embeddings) != 0 {
		t.Fatalf("stale embeddings=%#v err=%v", embeddings, err)
	}
	if err := store.UpsertSyncGraph(ctx, SyncGraph{RepoID: "fixture-a", Record: record, ReplaceChunks: true}); err != nil {
		t.Fatal(err)
	}
	chunks, err = store.GetChunks(ctx, record.ID)
	if err != nil || len(chunks) != 0 {
		t.Fatalf("empty replacement chunks=%#v err=%v", chunks, err)
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

func countRepoRows(t *testing.T, ctx context.Context, store *SQLiteStore, table string, repoID string) int {
	t.Helper()
	var count int
	if err := store.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT count(*) FROM %s WHERE repo_id = ?`, table), repoID).Scan(&count); err != nil {
		t.Fatalf("count %s rows returned error: %v", table, err)
	}
	return count
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
