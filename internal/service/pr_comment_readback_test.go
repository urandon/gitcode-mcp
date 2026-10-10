package service

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/gitcode"
)

func TestPRCommentWriteGraphPreservesProviderOrigin(t *testing.T) {
	for _, mode := range []gitcode.ProviderMode{gitcode.ProviderModeLive, gitcode.ProviderModeFixture} {
		t.Run(string(mode), func(t *testing.T) {
			ctx := context.Background()
			store, err := cache.NewInMemorySQLiteStore(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "origin", Owner: "owner", Name: "repo", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
				t.Fatal(err)
			}
			svc := NewWithClient(store, &fakeGitCodeClient{})
			svc.providerMode = mode
			now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
			if err := store.UpsertRecordGraph(ctx, cache.RecordGraph{Record: cache.Record{RepoID: "origin", ID: "PR-7", Type: "pull_request", Path: "pulls/7.md", Title: "parent", ContentHash: "parent", Provenance: cache.ProvenanceRemote, RemoteType: "pull_request", RemoteID: "7", CreatedAt: now, UpdatedAt: now}}); err != nil {
				t.Fatal(err)
			}
			root := gitcode.PRComment{ID: "301", PRNumber: 7, Body: "confirmed root", CreatedAt: now, UpdatedAt: now}
			root.Thread = []gitcode.PRComment{{ID: "302", PRNumber: 7, Body: "confirmed reply", CreatedAt: now, UpdatedAt: now}}
			_, graph, err := svc.prCommentThreadWriteGraph(ctx, "origin", 7, gitcode.WriteResult[gitcode.PRComment]{Record: root, RemoteID: root.ID, Confirmed: true, ConfirmedAt: now}, now)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.UpsertRecordGraph(ctx, graph); err != nil {
				t.Fatal(err)
			}
			want := cache.ProvenanceFixture
			if mode == gitcode.ProviderModeLive {
				want = cache.ProvenanceLive
			}
			for _, id := range []string{"PRCOMMENT-7-301", "PRCOMMENT-7-302"} {
				source, err := store.GetSourceScoped(ctx, "origin", id)
				if err != nil || source.Provenance != want {
					t.Fatalf("%s origin=%s want=%s err=%v", id, source.Provenance, want, err)
				}
				record, err := store.GetRecord(ctx, "origin", id)
				if err != nil || record.Provenance != cache.ProvenanceRemote {
					t.Fatalf("remote role changed: %s err=%v", record.Provenance, err)
				}
			}
		})
	}
}

func TestTargetedPRCommentWriterWaitBoundaries(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	observed := &observedPRWriterStore{Store: store, contended: make(chan struct{}, 1)}
	svc := NewWithClientConfig(observed, &fakeGitCodeClient{}, ServiceConfig{LockPath: filepath.Join(t.TempDir(), "writer.lock")})
	held, err := store.AcquireWriter(ctx, cache.WriterRequest{Operation: "sync", RepoID: "other", LockPath: svc.lockPath})
	if err != nil {
		t.Fatal(err)
	}
	defer store.ReleaseWriter(ctx, held)
	_, _, err = svc.acquireTargetedPRCommentWriter(ctx, "target", 10*time.Millisecond)
	var busy cache.ErrLockContention
	if !errors.As(err, &busy) || !busy.WaitExhausted || busy.RepoID != "other" || !strings.Contains(err.Error(), "retry the same targeted sync") || strings.Contains(err.Error(), svc.lockPath) {
		t.Fatalf("bounded admission=%v", err)
	}
	for _, deadline := range []bool{false, true} {
		var callCtx context.Context
		var cancel context.CancelFunc
		if deadline {
			callCtx, cancel = context.WithTimeout(ctx, 10*time.Millisecond)
		} else {
			callCtx, cancel = context.WithCancel(ctx)
			cancel()
		}
		_, _, err = svc.acquireTargetedPRCommentWriter(callCtx, "target", time.Second)
		cancel()
		want := context.Canceled
		if deadline {
			want = context.DeadlineExceeded
		}
		if !errors.Is(err, want) {
			t.Fatalf("caller cancellation=%v want=%v", err, want)
		}
	}
	// Collections remain fail-fast; missing exact parents are rejected before
	// waiting for this unrelated holder. Neither path contacts the provider.
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "target", Owner: "owner", Name: "repo", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	_, err = svc.BulkSyncPRComments(ctx, BulkSyncRequest{RepoID: "target"})
	if !errors.As(err, &busy) || busy.WaitExhausted {
		t.Fatalf("collection admission changed: %v", err)
	}
	_, err = svc.BulkSyncPRComments(ctx, BulkSyncRequest{RepoID: "target", RemoteAlias: "pr:99"})
	var invalid ErrInvalidQuery
	if !errors.As(err, &invalid) || invalid.Field != "remote_alias" {
		t.Fatalf("missing parent=%v", err)
	}
}

type observedPRWriterStore struct {
	cache.Store
	contended chan struct{}
}

func (s *observedPRWriterStore) AcquireWriter(ctx context.Context, req cache.WriterRequest) (*cache.WriterLease, error) {
	lease, err := s.Store.AcquireWriter(ctx, req)
	var busy cache.ErrLockContention
	if errors.As(err, &busy) {
		select {
		case s.contended <- struct{}{}:
		default:
		}
	}
	return lease, err
}

func TestTargetedPRCommentsWaitForOtherRepositoryWriter(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "target", Owner: "owner", Name: "repo", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSourceGraph(ctx, cache.SourceGraph{Source: cache.Source{RepoID: "target", ID: "TRACKER-PR-SEVEN", Kind: "pull_request", Path: "pulls/7.md", Title: "parent", ContentHash: "parent"}, Identities: []cache.Identity{{RepoID: "target", SourceID: "TRACKER-PR-SEVEN", AliasType: "pull_request", Alias: "7", Remote: cache.RemoteAlias{Type: "pull_request", ID: "7"}}}}); err != nil {
		t.Fatal(err)
	}
	observed := &observedPRWriterStore{Store: store, contended: make(chan struct{}, 1)}
	client := &fakeGitCodeClient{prCommentsByPR: map[int][]gitcode.PRComment{7: {{ID: "301", Body: "read once"}}}}
	svc := NewWithClientConfig(observed, client, ServiceConfig{LockPath: filepath.Join(t.TempDir(), "writer.lock")})
	svc.providerMode = gitcode.ProviderModeLive
	held, err := store.AcquireWriter(ctx, cache.WriterRequest{Operation: "sync", RepoID: "other", LockPath: svc.lockPath})
	if err != nil {
		t.Fatal(err)
	}
	defer store.ReleaseWriter(ctx, held)
	callCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	type outcome struct {
		result *SyncResourcesResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := svc.BulkSyncPRComments(callCtx, BulkSyncRequest{RepoID: "target", RemoteAlias: "pr:7", IdempotencyKey: "canonical-read"})
		done <- outcome{result, err}
	}()
	select {
	case <-observed.contended:
	case <-callCtx.Done():
		t.Fatal("no contention observation")
	}
	if err := store.ReleaseWriter(ctx, held); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil || got.result == nil || got.result.SuccessCount != 1 || client.prCommentCalls != 1 {
		t.Fatalf("targeted read result=%+v err=%v calls=%d", got.result, got.err, client.prCommentCalls)
	}
	links, err := store.ListLinks(ctx, cache.LinkFilter{RepoID: "target", SourceID: "PRCOMMENT-7-301"})
	if err != nil || len(links) != 1 || links[0].TargetID != "TRACKER-PR-SEVEN" {
		t.Fatalf("canonical links=%+v err=%v", links, err)
	}
	if got.result.Results[0].Record.Provenance != "live" {
		t.Fatalf("read origin=%s", got.result.Results[0].Record.Provenance)
	}
}
