package cache

import (
	"context"
	"testing"
)

func TestUpdateRepositoryIsAtomicWhenAliasConflicts(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteStore(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first := RepositoryBinding{RepoID: "owner/first", Owner: "owner", Name: "first", APIBaseURL: "https://api.example", Scopes: []RepositoryScope{RepositoryScopeIssues}, DisplayName: "First", Aliases: []string{"old/first"}}
	second := RepositoryBinding{RepoID: "owner/second", Owner: "owner", Name: "second", APIBaseURL: "https://api.example", Scopes: []RepositoryScope{RepositoryScopeIssues}, Aliases: []string{"taken/alias"}}
	if err := store.AddRepository(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, second); err != nil {
		t.Fatal(err)
	}
	changed := first
	changed.DisplayName = "Changed"
	changed.Aliases = []string{"taken/alias"}
	if err := store.UpdateRepository(ctx, changed); err == nil || !IsConstraintError(err) {
		t.Fatalf("conflicting update err=%v", err)
	}
	got, err := store.GetRepository(ctx, first.RepoID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DisplayName != "First" || len(got.Aliases) != 1 || got.Aliases[0] != "old/first" {
		t.Fatalf("partial update escaped rollback: %+v", got)
	}
}
