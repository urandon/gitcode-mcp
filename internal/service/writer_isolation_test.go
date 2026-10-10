package service

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
)

func writerFixtureService(t *testing.T, dataSource string, cfg ServiceConfig) *Service {
	t.Helper()
	store, err := cache.NewSQLiteStore(context.Background(), dataSource)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.GetRepository(context.Background(), "writer-fixture"); err != nil {
		if err := store.AddRepository(context.Background(), cache.RepositoryBinding{RepoID: "writer-fixture", Owner: "example-owner", Name: "example-repo", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
			t.Fatal(err)
		}
	}
	svc := New(store)
	if cfg.LockPath != "" {
		svc = NewWithClientConfig(store, sanitizedFixtureClient{}, cfg)
	}
	return svc
}

func requireWriterContention(t *testing.T, err error) {
	t.Helper()
	var busy cache.ErrLockContention
	if !errors.As(err, &busy) {
		t.Fatalf("writer admission error type=%T, want cache.ErrLockContention", err)
	}
}

func TestDefaultWriterIndependentStores(t *testing.T) {
	for _, memory := range []bool{true, false} {
		name := "file"
		if memory {
			name = "memory"
		}
		t.Run(name, func(t *testing.T) {
			first, second := ":memory:", ":memory:"
			if !memory {
				first = filepath.Join(t.TempDir(), "first.db")
				second = filepath.Join(t.TempDir(), "second.db")
			}
			a := writerFixtureService(t, first, ServiceConfig{})
			b := writerFixtureService(t, second, ServiceConfig{})
			ctx := context.Background()
			batch, err := b.FetchIssueSyncBatch(ctx, BulkSyncRequest{RepoID: "writer-fixture", PerPage: 100, Bounds: &SyncBounds{MaxPages: 1}})
			if err != nil {
				t.Fatal(err)
			}
			_, release, err := a.acquireBulkWriter(ctx, "writer-fixture", "fixture-holder")
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			result, err := b.CommitIssueSyncBatch(ctx, batch, nil)
			if err != nil {
				t.Fatalf("independent store commit blocked: %T", err)
			}
			if result.SuccessCount != 1 {
				t.Fatalf("committed=%d, want 1", result.SuccessCount)
			}
		})
	}
}

func TestDefaultWriterSameCacheStillContends(t *testing.T) {
	for _, separateHandle := range []bool{false, true} {
		name := "same-store"
		if separateHandle {
			name = "separate-handles"
		}
		t.Run(name, func(t *testing.T) {
			dataSource := filepath.Join(t.TempDir(), "shared.db")
			a := writerFixtureService(t, dataSource, ServiceConfig{})
			b := New(a.store)
			if separateHandle {
				b = writerFixtureService(t, dataSource, ServiceConfig{})
			}
			ctx, release, err := a.acquireBulkWriter(context.Background(), "writer-fixture", "fixture-holder")
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			_, otherRelease, err := b.acquireBulkWriter(ctx, "writer-fixture", "fixture-contender")
			if otherRelease != nil {
				otherRelease()
			}
			requireWriterContention(t, err)
		})
	}
}

func TestBulkWriterAdmissionLifetimeAndExplicitSharing(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "shared.lock")
	a := writerFixtureService(t, ":memory:", ServiceConfig{LockPath: lockPath})
	b := writerFixtureService(t, ":memory:", ServiceConfig{LockPath: lockPath})
	ctx, release, err := a.acquireBulkWriter(context.Background(), "writer-fixture", "fixture-holder")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	_, nestedRelease, err := a.acquireBulkWriter(ctx, "writer-fixture", "nested")
	if err != nil {
		t.Fatal("same-service active admission was not reused")
	}
	nestedRelease()
	_, otherRelease, err := b.acquireBulkWriter(ctx, "writer-fixture", "foreign-context")
	if otherRelease != nil {
		otherRelease()
	}
	requireWriterContention(t, err)
	release()
	_, holdAgain, err := b.acquireBulkWriter(context.Background(), "writer-fixture", "next-holder")
	if err != nil {
		t.Fatal(err)
	}
	defer holdAgain()
	_, staleRelease, err := a.acquireBulkWriter(ctx, "writer-fixture", "released-context")
	if staleRelease != nil {
		staleRelease()
	}
	requireWriterContention(t, err)
}

// A pipe handshake proves that the child has acquired admission before the
// parent attempts work. No external service, scheduler timing or sleep is used.
func TestDefaultWriterIndependentProcesses(t *testing.T) {
	for _, mode := range []string{"memory", "distinct-files", "same-file"} {
		t.Run(mode, func(t *testing.T) {
			childSource, parentSource := ":memory:", ":memory:"
			if mode != "memory" {
				childSource = filepath.Join(t.TempDir(), "child.db")
				parentSource = childSource
				if mode == "distinct-files" {
					parentSource = filepath.Join(t.TempDir(), "parent.db")
				}
			}
			// Initialize both handles before holding the shared-cache lease.
			parent := writerFixtureService(t, parentSource, ServiceConfig{})
			childCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			t.Cleanup(cancel)
			cmd := exec.CommandContext(childCtx, os.Args[0], "-test.run=^TestDefaultWriterSubprocessHelper$", "-test.count=1")
			cmd.Env = append(os.Environ(), "GITCODE_MCP_WRITER_CHILD=1", "GITCODE_MCP_WRITER_SOURCE="+childSource)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			in, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal("child start failed")
			}
			t.Cleanup(func() {
				_ = in.Close()
				if err := cmd.Wait(); err != nil {
					t.Error("child writer process failed")
				}
			})
			line, err := bufio.NewReader(out).ReadString('\n')
			if err != nil || line != "writer-ready\n" {
				t.Fatal("child did not reach writer admission barrier")
			}
			batch, err := parent.FetchIssueSyncBatch(context.Background(), BulkSyncRequest{RepoID: "writer-fixture", PerPage: 100, Bounds: &SyncBounds{MaxPages: 1}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := parent.CommitIssueSyncBatch(context.Background(), batch, nil)
			if mode == "same-file" {
				requireWriterContention(t, err)
			} else if err != nil {
				t.Fatalf("independent process commit blocked: %T", err)
			} else if result.SuccessCount != 1 {
				t.Fatalf("committed=%d, want 1", result.SuccessCount)
			}
		})
	}
}

func TestDefaultWriterSubprocessHelper(t *testing.T) {
	if os.Getenv("GITCODE_MCP_WRITER_CHILD") != "1" {
		return
	}
	svc := writerFixtureService(t, os.Getenv("GITCODE_MCP_WRITER_SOURCE"), ServiceConfig{})
	_, release, err := svc.acquireBulkWriter(context.Background(), "writer-fixture", "child-holder")
	if err != nil {
		t.Fatal("child admission failed")
	}
	defer release()
	if _, err := io.WriteString(os.Stdout, "writer-ready\n"); err != nil {
		t.Fatal("child readiness failed")
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
}
