package cache

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestCacheIdentityIsStableAndContentGenerationTracksHashes(t *testing.T) {
	ctx := context.Background()
	store, err := NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, RepositoryBinding{RepoID: "repo", Owner: "owner", Name: "repo"}); err != nil {
		t.Fatal(err)
	}
	first, err := store.CacheIdentity(ctx)
	if err != nil || first.UUID == "" {
		t.Fatalf("identity=%+v err=%v", first, err)
	}
	second, err := store.CacheIdentity(ctx)
	if err != nil || second.UUID != first.UUID {
		t.Fatalf("second identity=%+v err=%v", second, err)
	}
	source := Source{RepoID: "repo", ID: "ISSUE-1", Kind: "issue", Path: "issues/1.md", Title: "one", ContentHash: "hash-1"}
	if err := store.UpsertSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	state1, err := store.GetRepoContentState(ctx, "repo")
	if err != nil || state1.ContentGeneration == 0 || state1.ContentChangedAt.IsZero() {
		t.Fatalf("state1=%+v err=%v", state1, err)
	}
	if err := store.UpsertSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	state2, _ := store.GetRepoContentState(ctx, "repo")
	if state2.ContentGeneration != state1.ContentGeneration {
		t.Fatalf("unchanged hash advanced generation: %d -> %d", state1.ContentGeneration, state2.ContentGeneration)
	}
	source.ContentHash = "hash-2"
	if err := store.UpsertSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	state3, _ := store.GetRepoContentState(ctx, "repo")
	if state3.ContentGeneration <= state2.ContentGeneration {
		t.Fatalf("changed hash did not advance generation: %d -> %d", state2.ContentGeneration, state3.ContentGeneration)
	}
}

func TestMaintenanceFrontiersKeepHeadAndTailIndependent(t *testing.T) {
	ctx := context.Background()
	store, err := NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, RepositoryBinding{RepoID: "repo", Owner: "owner", Name: "repo"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, frontier := range []MaintenanceFrontier{
		{RepoID: "repo", RemoteType: "issue", Ordering: "updated_at_desc", FilterKey: "state=all", Lane: "head", Status: "fresh", PagesListed: 2, UpdatedAt: now},
		{RepoID: "repo", RemoteType: "issue", Ordering: "updated_at_desc", FilterKey: "state=all", Lane: "tail", Status: "backfilling", PagesListed: 10, Checkpoint: "page:11", UpdatedAt: now},
	} {
		if err := store.UpsertMaintenanceFrontier(ctx, frontier); err != nil {
			t.Fatal(err)
		}
	}
	frontiers, err := store.ListMaintenanceFrontiers(ctx, "repo")
	if err != nil || len(frontiers) != 2 {
		t.Fatalf("frontiers=%+v err=%v", frontiers, err)
	}
	if frontiers[0].Lane == frontiers[1].Lane {
		t.Fatalf("head and tail collapsed: %+v", frontiers)
	}
}

func TestMaintenanceFrontierPreservesLastSuccessAcrossSameLaneDegradation(t *testing.T) {
	ctx := context.Background()
	store, err := NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, RepositoryBinding{RepoID: "repo", Owner: "owner", Name: "repo"}); err != nil {
		t.Fatal(err)
	}
	succeededAt := time.Now().UTC().Add(-time.Hour)
	key := MaintenanceFrontier{RepoID: "repo", RemoteType: "wiki", Ordering: "updated_at_desc", FilterKey: "all", Lane: "head"}
	success := key
	success.Status, success.UpdatedAt = "fresh", succeededAt
	if err := store.UpsertMaintenanceFrontier(ctx, success); err != nil {
		t.Fatal(err)
	}
	degraded := key
	degraded.Status, degraded.LastErrorClass, degraded.UpdatedAt = "degraded", "network_timeout", succeededAt.Add(time.Hour)
	if err := store.UpsertMaintenanceFrontier(ctx, degraded); err != nil {
		t.Fatal(err)
	}
	frontiers, err := store.ListMaintenanceFrontiers(ctx, "repo")
	if err != nil || len(frontiers) != 1 || !frontiers[0].LastSuccessAt.Equal(succeededAt) {
		t.Fatalf("degraded frontier=%+v err=%v", frontiers, err)
	}
}

func TestVersion21MigrationBackfillsLegacySuccessBeforeFirstDegradation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache-v20.db")
	store, err := NewSQLiteStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, RepositoryBinding{RepoID: "repo", Owner: "owner", Name: "repo"}); err != nil {
		t.Fatal(err)
	}
	succeededAt := time.Now().UTC().Add(-time.Hour)
	key := MaintenanceFrontier{RepoID: "repo", RemoteType: "wiki", Ordering: "updated_at_desc", FilterKey: "all", Lane: "head"}
	success := key
	success.Status, success.UpdatedAt = "fresh", succeededAt
	if err := store.UpsertMaintenanceFrontier(ctx, success); err != nil {
		t.Fatal(err)
	}
	// Recreate the state visible to a version-20 binary: the row has a
	// successful updated_at but no durable last_success_at value.
	if _, err := store.db.ExecContext(ctx, `UPDATE maintenance_frontiers SET last_success_at = ''`); err != nil {
		t.Fatal(err)
	}
	setSchemaVersion(t, ctx, store.db, 20)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateCache(ctx, path, false); err != nil {
		t.Fatal(err)
	}
	store, err = NewSQLiteStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	degraded := key
	degraded.Status, degraded.LastErrorClass, degraded.UpdatedAt = "degraded", "network_timeout", succeededAt.Add(time.Hour)
	if err := store.UpsertMaintenanceFrontier(ctx, degraded); err != nil {
		t.Fatal(err)
	}
	frontiers, err := store.ListMaintenanceFrontiers(ctx, "repo")
	if err != nil || len(frontiers) != 1 || !frontiers[0].LastSuccessAt.Equal(succeededAt) {
		t.Fatalf("migrated degraded frontier=%+v err=%v", frontiers, err)
	}
}

func TestVersion17MigrationSeedsExistingContentGeneration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")
	store, err := NewSQLiteStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSource(ctx, Source{RepoID: "owner/repo", ID: "ISSUE-1", Kind: "issue", Path: "issues/1.md", Title: "one", ContentHash: "hash-1"}); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DROP TRIGGER trg_sources_content_insert`, `DROP TRIGGER trg_sources_content_update`, `DROP TRIGGER trg_sources_content_delete`,
		`DROP TRIGGER trg_chunks_content_insert`, `DROP TRIGGER trg_chunks_content_update`, `DROP TRIGGER trg_chunks_content_delete`,
		`DROP TABLE maintenance_frontiers`, `DROP TABLE rag_coverage_state`, `DROP TABLE repo_content_state`, `DROP TABLE cache_identity`,
	} {
		if _, err := store.db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	setSchemaVersion(t, ctx, store.db, 16)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateCache(ctx, path, false); err != nil {
		t.Fatal(err)
	}
	store, err = NewSQLiteStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state, err := store.GetRepoContentState(ctx, "owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	if state.ContentGeneration != 1 || state.LastProjectionID != "migration-v17" {
		t.Fatalf("state=%+v", state)
	}
}
