package cache

import (
	"context"
	"testing"
	"time"
)

func TestRepairIssueProviderPlaceholdersMergesCommentsAndQueue(t *testing.T) {
	ctx := context.Background()
	store, err := NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, RepositoryBinding{RepoID: "repair", Owner: "owner", Name: "repo", Scopes: []RepositoryScope{RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	canonical := Record{RepoID: "repair", ID: "ISSUE-88", Type: "issue", Path: "issues/88.md", Title: "Canonical", Body: "body", Status: "open", ContentHash: "canonical", Provenance: ProvenanceRemote, RemoteType: "issue", RemoteID: "88", CreatedAt: now, UpdatedAt: now}
	if err := store.UpsertRecordGraph(ctx, RecordGraph{Record: canonical, Identities: []Identity{
		{RepoID: "repair", SourceID: canonical.ID, AliasType: "issue", Alias: "88", Remote: RemoteAlias{Type: "issue", ID: "88"}},
		{RepoID: "repair", SourceID: canonical.ID, AliasType: "gitcode_issue_id", Alias: "4277473", Remote: RemoteAlias{Type: "gitcode_issue_id", ID: "4277473"}},
	}}); err != nil {
		t.Fatal(err)
	}
	canonicalNewer := now.Add(time.Hour)
	if err := store.UpsertRecordGraph(ctx, RecordGraph{Record: canonical, Comments: []RecordComment{{RepoID: "repair", RecordID: canonical.ID, CommentID: "comment-1", Body: "new canonical body", ContentHash: "canonical-comment", CreatedAt: now, UpdatedAt: canonicalNewer}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertIssueCommentSync(ctx, IssueCommentSync{RepoID: "repair", SourceID: canonical.ID, IssueNumber: 88, RemoteID: "88", ProviderID: "4277473", ExpectedCount: 1, Status: "complete", UpdatedAt: canonicalNewer}); err != nil {
		t.Fatal(err)
	}
	placeholder := Record{RepoID: "repair", ID: "ISSUE-4277473", Type: "issue", Path: "issues/4277473.md", Title: "Issue 4277473", Status: "open", ContentHash: "placeholder", Provenance: ProvenanceRemote, RemoteType: "issue", RemoteID: "4277473", CreatedAt: now, UpdatedAt: now}
	if err := store.UpsertRecordGraph(ctx, RecordGraph{Record: placeholder, Comments: []RecordComment{{RepoID: "repair", RecordID: placeholder.ID, CommentID: "comment-1", Body: "dogfood", ContentHash: "comment", CreatedAt: now, UpdatedAt: now}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordCacheConfirmation(ctx, CacheConfirmationRecord{RepoID: "repair", Command: "add-comment", RecordID: placeholder.ID, RecordType: "issue", RemoteType: "issue_comment", RemoteID: "comment-1", IdempotencyKey: "repair-confirmation", Status: "succeeded", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	child := Source{RepoID: "repair", ID: "ISSUECOMMENT-88-1", Kind: "issue_comment", Path: "issues/88/comments/1.md", Title: "Comment", Body: "dogfood", Status: "published", ContentHash: "child", CreatedAt: now, UpdatedAt: now}
	if err := store.UpsertSourceGraph(ctx, SourceGraph{Source: child, Links: []Link{{RepoID: "repair", SourceID: child.ID, TargetID: placeholder.ID, Kind: "parent"}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertIssueCommentSync(ctx, IssueCommentSync{RepoID: "repair", SourceID: placeholder.ID, IssueNumber: 4277473, RemoteID: "4277473", Status: "pending", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}

	repaired, err := store.RepairIssueProviderPlaceholders(ctx, "repair")
	if err != nil || repaired != 1 {
		t.Fatalf("repaired=%d err=%v", repaired, err)
	}
	record, err := store.GetRecord(ctx, "repair", canonical.ID)
	if err != nil || len(record.Comments) != 1 || record.Comments[0].CommentID != "comment-1" || record.Comments[0].Body != "new canonical body" {
		t.Fatalf("canonical=%#v err=%v", record, err)
	}
	if _, err := store.GetRecord(ctx, "repair", placeholder.ID); err == nil {
		t.Fatal("placeholder record still exists")
	}
	if _, ok, err := store.GetIssueCommentSync(ctx, "repair", placeholder.ID); err != nil || ok {
		t.Fatalf("placeholder queue exists=%t err=%v", ok, err)
	}
	canonicalQueue, ok, err := store.GetIssueCommentSync(ctx, "repair", canonical.ID)
	if err != nil || !ok || canonicalQueue.Status != "pending" || canonicalQueue.IssueNumber != 88 || canonicalQueue.ProviderID != "4277473" {
		t.Fatalf("canonical queue=%#v exists=%t err=%v", canonicalQueue, ok, err)
	}
	confirmation, err := store.GetCacheConfirmationByKey(ctx, "repair", "repair-confirmation")
	if err != nil || confirmation == nil || confirmation.RecordID != canonical.ID {
		t.Fatalf("confirmation=%#v err=%v", confirmation, err)
	}
	links, err := store.ListLinks(ctx, LinkFilter{RepoID: "repair", SourceID: child.ID})
	if err != nil || len(links) != 1 || links[0].TargetID != canonical.ID {
		t.Fatalf("links=%#v err=%v", links, err)
	}
	identity, err := store.ResolveAliasScoped(ctx, "repair", RemoteAlias{Type: "gitcode_issue_id", ID: "4277473"})
	if err != nil || identity.SourceID != canonical.ID {
		t.Fatalf("identity=%#v err=%v", identity, err)
	}
	repaired, err = store.RepairIssueProviderPlaceholders(ctx, "repair")
	if err != nil || repaired != 0 {
		t.Fatalf("second repaired=%d err=%v", repaired, err)
	}
}

func TestRepairIssueProviderPlaceholdersPreservesIdentifiedLargeIssue(t *testing.T) {
	ctx := context.Background()
	store, err := NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, RepositoryBinding{RepoID: "repair", Owner: "owner", Name: "repo", Scopes: []RepositoryScope{RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	canonical := Record{RepoID: "repair", ID: "ISSUE-88", Type: "issue", Path: "issues/88.md", Title: "Canonical", Body: "body", Status: "open", ContentHash: "canonical", Provenance: ProvenanceRemote, RemoteType: "issue", RemoteID: "88", CreatedAt: now, UpdatedAt: now}
	large := Record{RepoID: "repair", ID: "ISSUE-4277473", Type: "issue", Path: "issues/4277473.md", Title: "Issue 4277473", Status: "open", ContentHash: "large", Provenance: ProvenanceRemote, RemoteType: "issue", RemoteID: "4277473", CreatedAt: now, UpdatedAt: now}
	if err := store.UpsertRecordGraph(ctx, RecordGraph{Record: canonical, Identities: []Identity{{RepoID: "repair", SourceID: canonical.ID, AliasType: "gitcode_issue_id", Alias: "4277473", Remote: RemoteAlias{Type: "gitcode_issue_id", ID: "4277473"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRecordGraph(ctx, RecordGraph{Record: large, Identities: []Identity{{RepoID: "repair", SourceID: large.ID, AliasType: "issue", Alias: "4277473", Remote: RemoteAlias{Type: "issue", ID: "4277473"}}}}); err != nil {
		t.Fatal(err)
	}
	repaired, err := store.RepairIssueProviderPlaceholders(ctx, "repair")
	if err != nil || repaired != 0 {
		t.Fatalf("repaired=%d err=%v", repaired, err)
	}
	if _, err := store.GetRecord(ctx, "repair", large.ID); err != nil {
		t.Fatalf("identified large issue removed: %v", err)
	}
}

func TestRepairIssueProviderPlaceholdersMergesExactHistoricalDuplicate(t *testing.T) {
	ctx := context.Background()
	store, err := NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, RepositoryBinding{RepoID: "repair", Owner: "owner", Name: "repo", Scopes: []RepositoryScope{RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	canonical := Record{RepoID: "repair", ID: "ISSUE-77", Type: "issue", Path: "issues/77.md", Title: "Hybrid search", Body: "same body", Status: "open", ContentHash: "newer", Provenance: ProvenanceRemote, RemoteType: "issue", RemoteID: "77", CreatedAt: now, UpdatedAt: now}
	duplicate := Record{RepoID: "repair", ID: "ISSUE-4243027", Type: "issue", Path: "issues/4243027.md", Title: canonical.Title, Body: canonical.Body, Status: "closed", ContentHash: "older", Provenance: ProvenanceRemote, RemoteType: "issue", RemoteID: "4243027", CreatedAt: now, UpdatedAt: now}
	if err := store.UpsertRecordGraph(ctx, RecordGraph{Record: canonical, Identities: []Identity{
		{RepoID: "repair", SourceID: canonical.ID, AliasType: "issue", Alias: "77", Remote: RemoteAlias{Type: "issue", ID: "77"}},
		{RepoID: "repair", SourceID: canonical.ID, AliasType: "gitcode_issue_id", Alias: "4243027", Remote: RemoteAlias{Type: "gitcode_issue_id", ID: "4243027"}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRecordGraph(ctx, RecordGraph{Record: duplicate, Identities: []Identity{{RepoID: "repair", SourceID: duplicate.ID, AliasType: "issue", Alias: "4243027", Remote: RemoteAlias{Type: "issue", ID: "4243027"}}}}); err != nil {
		t.Fatal(err)
	}
	repaired, err := store.RepairIssueProviderPlaceholders(ctx, "repair")
	if err != nil || repaired != 1 {
		t.Fatalf("repaired=%d err=%v", repaired, err)
	}
	if _, err := store.GetRecord(ctx, "repair", duplicate.ID); err == nil {
		t.Fatal("historical duplicate still exists")
	}
	if _, err := store.ResolveAliasScoped(ctx, "repair", RemoteAlias{Type: "issue", ID: "4243027"}); err == nil {
		t.Fatal("mistaken repository-local issue alias still exists")
	}
}
