package cache

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"time"
)

func TestInMemoryStoreRetainsCommittedFrontierAfterCancelledTransaction(t *testing.T) {
	ctx := context.Background()
	store, err := NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const repoID = "memory-cancel"
	if err := store.AddRepository(ctx, RepositoryBinding{RepoID: repoID, Owner: "example", Name: "repo"}); err != nil {
		t.Fatal(err)
	}
	want := SyncFrontier{RepoID: repoID, RemoteType: "issue", Ordering: "updated_at_desc", FilterKey: "state=all", Status: "complete", PagesListed: 3, RecordsListed: 30, UpdatedAt: time.Unix(100, 0).UTC()}
	if err := store.UpsertSyncFrontier(ctx, want); err != nil {
		t.Fatal(err)
	}
	txCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	tx, err := store.db.BeginTx(txCtx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(txCtx, `UPDATE sync_frontiers SET status = 'uncommitted' WHERE repo_id = ?`, repoID); err != nil {
		t.Fatal(err)
	}
	cancel()
	// Wait for database/sql's cancellation-driven rollback, not an explicit
	// Rollback which could win the race and leave the connection reusable.
	deadline := time.Now().Add(5 * time.Second)
	for store.db.Stats().InUse != 0 {
		if time.Now().After(deadline) {
			t.Fatal("cancelled transaction did not release its worker connection")
		}
		time.Sleep(time.Millisecond)
	}
	got, ok, err := store.GetSyncFrontier(ctx, repoID, want.RemoteType, want.Ordering, want.FilterKey)
	if err != nil || !ok {
		t.Fatalf("committed frontier readback after cancellation: ok=%v err=%v", ok, err)
	}
	if got != want {
		t.Fatalf("frontier after cancellation = %#v, want %#v", got, want)
	}
	assertMemoryConnectionPragmas(t, store)
}

func TestInMemoryStoreSurvivesDiscardedWorkerWithoutSharingState(t *testing.T) {
	ctx := context.Background()
	first, err := NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if first.cacheRef == second.cacheRef || first.lockPath == second.lockPath {
		t.Fatal("independent memory stores share cache or writer identity")
	}
	if err := first.AddRepository(ctx, RepositoryBinding{RepoID: "memory-only-first", Owner: "example", Name: "repo"}); err != nil {
		t.Fatal(err)
	}
	conn, err := first.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Raw(func(any) error { return driver.ErrBadConn }); err != driver.ErrBadConn {
		t.Fatalf("discard worker = %v, want ErrBadConn", err)
	}
	_ = conn.Close()
	if _, err := first.GetRepository(ctx, "memory-only-first"); err != nil {
		t.Fatalf("committed repository lost after worker replacement: %v", err)
	}
	if repositories, err := second.ListRepositories(ctx); err != nil || len(repositories) != 0 {
		t.Fatalf("second store repositories=%v err=%v, want empty isolated store", repositories, err)
	}
	assertMemoryConnectionPragmas(t, first)
}

func TestInMemoryStoreCloseReleasesLifetimeAnchor(t *testing.T) {
	store, err := NewInMemorySQLiteStore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if store.memoryAnchorConn == nil || store.memoryAnchorDB == nil {
		t.Fatal("memory store has no lifetime anchor")
	}
	anchor := store.memoryAnchorConn
	anchorDB := store.memoryAnchorDB
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := anchor.ExecContext(context.Background(), "SELECT 1"); !errors.Is(err, sql.ErrConnDone) {
		t.Fatalf("closed lifetime anchor = %v, want ErrConnDone", err)
	}
	if err := anchorDB.Ping(); err == nil || anchorDB.Stats().OpenConnections != 0 {
		t.Fatalf("anchor pool remains open: err=%v connections=%d", err, anchorDB.Stats().OpenConnections)
	}
	if err := store.db.Ping(); err == nil || store.db.Stats().OpenConnections != 0 {
		t.Fatalf("worker pool remains open: err=%v connections=%d", err, store.db.Stats().OpenConnections)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("repeated Close = %v", err)
	}
}

func TestInMemoryStoreCancelledInitialization(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store, err := NewInMemorySQLiteStore(ctx)
	if store != nil {
		_ = store.Close()
		t.Fatal("cancelled initialization returned a store")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled initialization = %v, want context.Canceled", err)
	}
}

func assertMemoryConnectionPragmas(t *testing.T, store *SQLiteStore) {
	t.Helper()
	for pragma, want := range map[string]int{"foreign_keys": 1, "busy_timeout": 5000} {
		var got int
		if err := store.db.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil || got != want {
			t.Fatalf("replacement worker %s=%d err=%v, want %d", pragma, got, err, want)
		}
	}
	if _, err := store.db.Exec(`INSERT INTO repo_aliases (alias, repo_id, created_at) VALUES ('orphan', 'missing-repository', '2026-01-01T00:00:00Z')`); err == nil {
		t.Fatal("replacement worker accepted a foreign-key violation")
	}
}
