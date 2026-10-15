package service

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
)

func TestSetRepositoryScopesPreservesBindingAndCachedData(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	before := cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", APIBaseURL: "https://custom.example.invalid/api/v5?route=one", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}, DisplayName: "Fixture", Aliases: []string{"short"}, CreatedAt: now, UpdatedAt: now}
	if err := store.AddRepository(ctx, before); err != nil {
		t.Fatal(err)
	}
	other := before
	other.RepoID, other.Name, other.Aliases = "owner/other", "other", []string{"other"}
	if err := store.AddRepository(ctx, other); err != nil {
		t.Fatal(err)
	}
	record := cache.Record{RepoID: before.RepoID, ID: "ISSUE-42", Type: "issue", Path: "issues/42.md", Title: "Fixture", Body: "Cached content", Status: "open", ContentHash: "fixture", Provenance: cache.ProvenanceRemote, CreatedAt: now, UpdatedAt: now}
	if err := store.UpsertRecordGraph(ctx, cache.RecordGraph{Record: record}); err != nil {
		t.Fatal(err)
	}
	record, err = store.GetRecord(ctx, before.RepoID, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := store.CacheIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	svc := New(store)
	for _, scopes := range []string{"issues,wiki,pulls,comments", "issues,wiki", "wiki", "pr,comment", "issues"} {
		result, err := svc.SetRepositoryScopes(ctx, before.RepoID, []string{scopes})
		if err != nil {
			t.Fatalf("scopes %s: %v", scopes, err)
		}
		got, err := store.GetRepository(ctx, before.RepoID)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Scopes) != len(got.Scopes) {
			t.Fatal("receipt did not read back scopes")
		}
		for i := range got.Scopes {
			if string(result.Scopes[i]) != string(got.Scopes[i]) {
				t.Fatal("receipt disagrees with stored scopes")
			}
		}
		wantScopes, err := normalizeRepositoryScopes([]string{scopes})
		if err != nil || !reflect.DeepEqual(wantScopes, result.Scopes) {
			t.Fatal("incorrect shared scope normalization")
		}
		got.Scopes, got.UpdatedAt = before.Scopes, before.UpdatedAt
		if !reflect.DeepEqual(got, before) {
			t.Fatal("scope update replaced unrelated binding metadata")
		}
	}
	gotRecord, err := store.GetRecord(ctx, before.RepoID, record.ID)
	if err != nil || !reflect.DeepEqual(gotRecord, record) {
		t.Fatal("scope reduction destroyed cached records")
	}
	gotOther, err := store.GetRepository(ctx, other.RepoID)
	if err != nil || !reflect.DeepEqual(gotOther, other) {
		t.Fatal("scope update changed another binding")
	}
	gotIdentity, err := store.CacheIdentity(ctx)
	if err != nil || gotIdentity != identity {
		t.Fatal("cache identity changed")
	}
	for _, tc := range []struct{ id, scopes string }{{"", "issues"}, {"owner/repo", ""}, {"owner/repo", "issues,unknown"}, {"short", "wiki"}, {"missing", "wiki"}} {
		_, err := svc.SetRepositoryScopes(ctx, tc.id, []string{tc.scopes})
		var invalid ErrInvalidQuery
		var missing ErrNotFound
		if tc.id == "short" || tc.id == "missing" {
			if !errors.As(err, &missing) {
				t.Fatal("alias/missing target did not return typed not-found")
			}
		} else if !errors.As(err, &invalid) {
			t.Fatal("invalid scope request did not return typed validation failure")
		}
	}
	got, err := store.GetRepository(ctx, before.RepoID)
	if err != nil || !reflect.DeepEqual(got.Scopes, before.Scopes) {
		t.Fatal("refused request changed scopes")
	}
}

func TestSetRepositoryScopesRespectsCacheWriter(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "repo", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireWriter(ctx, cache.WriterRequest{Operation: "fixture-holder"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.ReleaseWriter(ctx, lease)
	_, err = New(store).SetRepositoryScopes(ctx, "repo", []string{"wiki"})
	var locked cache.ErrLockContention
	if !errors.As(err, &locked) {
		t.Fatalf("scope writer crossed held lease: %T", err)
	}
	got, err := store.GetRepository(ctx, "repo")
	if err != nil || len(got.Scopes) != 1 || got.Scopes[0] != cache.RepositoryScopeIssues {
		t.Fatal("blocked writer changed scopes")
	}
}

type scopeInterleavingStore struct {
	*cache.SQLiteStore
	change func()
}

func (s scopeInterleavingStore) SetRepositoryScopes(ctx context.Context, id string, scopes []cache.RepositoryScope, now time.Time) error {
	s.change()
	return s.SQLiteStore.SetRepositoryScopes(ctx, id, scopes, now)
}

func TestSetRepositoryScopesDoesNotReplaceConcurrentMetadata(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repo := cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", APIBaseURL: "https://custom.example.invalid/api/v5", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}, Aliases: []string{"old"}}
	if err := store.AddRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}
	wrapped := scopeInterleavingStore{SQLiteStore: store, change: func() {
		repo.DisplayName, repo.Aliases = "Updated by another binding writer", []string{"new"}
		if err := store.UpdateRepository(ctx, repo); err != nil {
			t.Fatal(err)
		}
	}}
	result, err := New(wrapped).SetRepositoryScopes(ctx, repo.RepoID, []string{"wiki"})
	if err != nil || result.DisplayName != repo.DisplayName || !reflect.DeepEqual(result.Aliases, repo.Aliases) {
		t.Fatal("scope update overwrote interleaved binding metadata")
	}
}
