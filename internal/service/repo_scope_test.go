package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/gitcode"
)

func TestRepoScopedAliasResolverDuplicateIssueAlias(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, repoID := range []string{"fixture-a", "fixture-b"} {
		if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: repoID, Owner: "owner", Name: repoID, APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	graphs := []cache.SourceGraph{
		{Source: cache.Source{RepoID: "fixture-a", ID: "DOC-42", Kind: "issue", Path: "issues/42-a.md", Title: "Issue A", Body: "fixture a", Status: "open", ContentHash: "ha", CreatedAt: now, UpdatedAt: now}, Identities: []cache.Identity{{RepoID: "fixture-a", AliasType: "issue", Alias: "42", Remote: cache.RemoteAlias{Type: "issue", ID: "42"}}}},
		{Source: cache.Source{RepoID: "fixture-b", ID: "DOC-42", Kind: "issue", Path: "issues/42-b.md", Title: "Issue B", Body: "fixture b", Status: "open", ContentHash: "hb", CreatedAt: now, UpdatedAt: now}, Identities: []cache.Identity{{RepoID: "fixture-b", AliasType: "issue", Alias: "42", Remote: cache.RemoteAlias{Type: "issue", ID: "42"}}}},
	}
	for _, graph := range graphs {
		if err := store.UpsertSourceGraph(ctx, graph); err != nil {
			t.Fatal(err)
		}
	}
	svc := New(store)
	a, err := svc.GetSource(ctx, GetSourceRequest{RepoID: "fixture-a", ID: "issue:42"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.GetSource(ctx, GetSourceRequest{RepoID: "fixture-b", ID: "issue:42"})
	if err != nil {
		t.Fatal(err)
	}
	if a.RepoID != "fixture-a" || a.Body != "fixture a" || b.RepoID != "fixture-b" || b.Body != "fixture b" {
		t.Fatalf("scoped results crossed repos: a=%+v b=%+v", a, b)
	}
	if err := svc.DiagnoseUnscopedAlias(ctx, "issue", "42"); err == nil {
		t.Fatalf("unscoped duplicate alias resolved without diagnostic")
	} else {
		var ambiguous ErrAmbiguousAlias
		if !errors.As(err, &ambiguous) {
			t.Fatalf("unscoped duplicate alias err=%T %[1]v, want ErrAmbiguousAlias", err)
		}
	}
}

func TestSyncRejectsDisabledWikiScopeBeforeAdapter(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "issues-only", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	client := &fakeGitCodeClient{wiki: gitcode.WikiPage{Slug: "Home", Title: "Home", Body: "body"}}
	svc := NewWithClient(store, client)
	_, err = svc.SyncToCache(ctx, SyncRequest{RepoID: "issues-only", RemoteAlias: "wiki:Home", IdempotencyKey: "disabled-wiki"})
	if err == nil {
		t.Fatalf("SyncToCache succeeded with disabled wiki scope")
	}
	if client.wikiCalls != 0 || client.issueCalls != 0 {
		t.Fatalf("adapter was called before disabled-scope rejection: wiki=%d issue=%d", client.wikiCalls, client.issueCalls)
	}
}

func TestBuildAdapterRouteValidatesRepoScope(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner-a", Name: "repo-a", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	svc := New(store)
	route, err := svc.BuildAdapterRoute(ctx, "fixture-a", RepositoryScopeIssues)
	if err != nil {
		t.Fatal(err)
	}
	if route.RepoID != "fixture-a" || route.Owner != "owner-a" || route.Name != "repo-a" || route.APIBaseURL != "https://example.invalid/api" {
		t.Fatalf("route = %#v", route)
	}
	_, err = svc.BuildAdapterRoute(ctx, "fixture-a", RepositoryScopeWiki)
	var invalid ErrInvalidQuery
	if !errors.As(err, &invalid) {
		t.Fatalf("disabled route err=%v want ErrInvalidQuery", err)
	}
}
