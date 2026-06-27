package cache

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func TestConcurrentSearchSources(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")
	store, err := NewSQLiteStore(ctx, path)
	if err != nil {
		t.Fatalf("NewSQLiteStore returned error: %v", err)
	}
	defer store.Close()
	mustAddTestRepo(t, ctx, store, "fixture-a")
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("DOC-SCN1-%02d", i)
		mustUpsertGraph(t, ctx, store, SourceGraph{Source: testSource(id, "doc", "Search Doc "+id)})
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for j := 0; j < 2; j++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results, searchErr := store.SearchSources(ctx, SearchQuery{RepoID: "fixture-a", Query: "Search", Limit: 20})
			if searchErr != nil {
				errs <- searchErr
				return
			}
			if len(results) == 0 {
				errs <- fmt.Errorf("concurrent SearchSources returned 0 results")
			}
		}()
	}
	wg.Wait()
	close(errs)

	for e := range errs {
		t.Errorf("concurrent SearchSources error: %v", e)
	}
}

func TestWriterHoldReadersUnblocked(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")
	store, err := NewSQLiteStore(ctx, path)
	if err != nil {
		t.Fatalf("NewSQLiteStore returned error: %v", err)
	}
	defer store.Close()
	mustAddTestRepo(t, ctx, store, "fixture-a")
	mustUpsertGraph(t, ctx, store, SourceGraph{Source: testSource("DOC-WHR", "doc", "Writer Hold Reader Doc")})

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
		source, readErr := reader.GetSourceScoped(ctx, "fixture-a", "DOC-WHR")
		if readErr != nil {
			t.Errorf("reader %d GetSourceScoped returned error: %v", i+1, readErr)
		}
		if source.RepoID != "fixture-a" || source.ID != "DOC-WHR" {
			t.Errorf("reader %d source = %#v", i+1, source)
		}
		results, searchErr := reader.SearchSources(ctx, SearchQuery{RepoID: "fixture-a", Query: "Writer Hold", Limit: 10})
		if searchErr != nil {
			t.Errorf("reader %d SearchSources returned error: %v", i+1, searchErr)
		}
		if len(results) == 0 {
			t.Errorf("reader %d SearchSources returned 0 results", i+1)
		}
		sources, listErr := reader.ListSources(ctx, SourceFilter{RepoID: "fixture-a", Kind: "doc"})
		if listErr != nil {
			t.Errorf("reader %d ListSources returned error: %v", i+1, listErr)
		}
		if len(sources) == 0 {
			t.Errorf("reader %d ListSources returned 0 sources", i+1)
		}
	}
}

func TestConcurrentCurrentSchemaOpensWhileWriterHeld(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")
	store, err := NewSQLiteStore(ctx, path)
	if err != nil {
		t.Fatalf("NewSQLiteStore returned error: %v", err)
	}
	defer store.Close()
	mustAddTestRepo(t, ctx, store, "fixture-a")
	mustUpsertGraph(t, ctx, store, SourceGraph{Source: testSource("DOC-OPEN", "doc", "Concurrent Open Doc")})

	lease, err := store.AcquireWriter(ctx, WriterRequest{Operation: "sync-index", RepoID: "fixture-a"})
	if err != nil {
		t.Fatalf("AcquireWriter returned error: %v", err)
	}
	defer store.ReleaseWriter(ctx, lease)

	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reader, openErr := NewSQLiteStore(ctx, path)
			if openErr != nil {
				errs <- openErr
				return
			}
			defer reader.Close()
			source, readErr := reader.GetSourceScoped(ctx, "fixture-a", "DOC-OPEN")
			if readErr != nil {
				errs <- readErr
				return
			}
			if source.ID != "DOC-OPEN" {
				errs <- fmt.Errorf("reader got unexpected source: %#v", source)
			}
		}()
	}
	wg.Wait()
	close(errs)

	for e := range errs {
		t.Errorf("concurrent current-schema open error: %v", e)
	}
}

func TestTwoWritersContentionCacheBusy(t *testing.T) {
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
	if err == nil {
		t.Fatalf("second AcquireWriter succeeded, want ErrLockContention")
	}
	var contention ErrLockContention
	if !errors.As(err, &contention) {
		t.Fatalf("second AcquireWriter error = %T %[1]v, want ErrLockContention", err)
	}
	if contention.DiagnosticCode() != "cache_busy" {
		t.Fatalf("DiagnosticCode() = %q, want cache_busy", contention.DiagnosticCode())
	}
	if contention.Operation != "sync" {
		t.Fatalf("contention.Operation = %q, want sync", contention.Operation)
	}
	if contention.StartedAt.IsZero() {
		t.Fatalf("contention.StartedAt is zero, want non-zero timestamp")
	}
	if contention.PID == 0 {
		t.Fatalf("contention.PID = 0, want non-zero PID")
	}
}

func TestThreeRoutinesTwoReadersOneWriter(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")
	store, err := NewSQLiteStore(ctx, path)
	if err != nil {
		t.Fatalf("NewSQLiteStore returned error: %v", err)
	}
	defer store.Close()
	mustAddTestRepo(t, ctx, store, "fixture-a")
	mustUpsertGraph(t, ctx, store, SourceGraph{Source: testSource("DOC-3G", "doc", "Three Goroutines Doc")})

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

	var wg sync.WaitGroup
	readErrors := make(chan error, 2)

	wg.Add(1)
	go func(r *SQLiteStore) {
		defer wg.Done()
		source, readErr := r.GetSourceScoped(ctx, "fixture-a", "DOC-3G")
		if readErr != nil {
			readErrors <- readErr
			return
		}
		if source.RepoID != "fixture-a" || source.ID != "DOC-3G" {
			readErrors <- fmt.Errorf("reader got unexpected source: %#v", source)
		}
	}(readerOne)

	wg.Add(1)
	go func(r *SQLiteStore) {
		defer wg.Done()
		results, searchErr := r.SearchSources(ctx, SearchQuery{RepoID: "fixture-a", Query: "Goroutines", Limit: 10})
		if searchErr != nil {
			readErrors <- searchErr
			return
		}
		if len(results) == 0 {
			readErrors <- fmt.Errorf("reader SearchSources returned 0 results")
		}
	}(readerTwo)

	wg.Wait()
	close(readErrors)

	for e := range readErrors {
		t.Errorf("reader goroutine error: %v", e)
	}

	_, writeErr := readerOne.AcquireWriter(ctx, WriterRequest{Operation: "write", RepoID: "fixture-a"})
	if writeErr == nil {
		t.Fatalf("second AcquireWriter succeeded, want ErrLockContention")
	}
	var contention ErrLockContention
	if !errors.As(writeErr, &contention) {
		t.Fatalf("second AcquireWriter error = %T %[1]v, want ErrLockContention", writeErr)
	}
	if contention.DiagnosticCode() != "cache_busy" {
		t.Fatalf("DiagnosticCode() = %q, want cache_busy", contention.DiagnosticCode())
	}
}

func TestFutureSchemaIncompatibleDiagnostic(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "future.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open temp db: %v", err)
	}
	setSchemaVersion(t, ctx, db, currentSchemaVersion+1)
	if err := db.Close(); err != nil {
		t.Fatalf("close temp db: %v", err)
	}

	store, err := NewSQLiteStore(ctx, path)
	if err == nil {
		store.Close()
		t.Fatalf("NewSQLiteStore returned nil error for future schema")
	}
	if !errors.Is(err, ErrSchemaVersionIncompatible) {
		t.Fatalf("NewSQLiteStore error = %v, want ErrSchemaVersionIncompatible", err)
	}

	var schemaErr *SchemaVersionError
	if !errors.As(err, &schemaErr) {
		t.Fatalf("error is not *SchemaVersionError: %T %v", err, err)
	}
	if !errors.Is(err, ErrSchemaVersionIncompatible) {
		t.Fatalf("error does not unwrap to ErrSchemaVersionIncompatible: %v", err)
	}
}
