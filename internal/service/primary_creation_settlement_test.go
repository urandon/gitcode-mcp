package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"gitcode-mcp/internal/audit"
	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/gitcode"
)

type creationPauseStore struct {
	*cache.SQLiteStore
	calls            atomic.Int32
	entered, release chan struct{}
	failLate         bool
}

func (s *creationPauseStore) SettleWriteGraphGeneration(ctx context.Context, graph cache.RecordGraph, complete, pending cache.AuditTrailEntry, generation time.Time) (bool, error) {
	if s.calls.Add(1) == 1 {
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
			return false, ctx.Err()
		}
		if s.failLate {
			return false, errors.New("injected delayed publication failure")
		}
	}
	return s.SQLiteStore.SettleWriteGraphGeneration(ctx, graph, complete, pending, generation)
}

func TestLiveCreateLatePublicationCannotOverwriteRecovery(t *testing.T) {
	for _, kind := range []string{"issue", "pull_request"} {
		for _, failLate := range []bool{false, true} {
			name := kind + "/late-success"
			if failLate {
				name = kind + "/late-failure"
			}
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				var posts, gets atomic.Int32
				api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					body := "original description"
					if r.Method == http.MethodPost {
						posts.Add(1)
						w.WriteHeader(http.StatusCreated)
					} else if gets.Add(1) > 1 || kind == "issue" {
						body = "newer canonical description"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"id": 9001, "number": 7, "title": "Title", "body": body, "state": "open", "head": map[string]string{"ref": "topic"}, "base": map[string]string{"ref": "main"}})
				}))
				defer api.Close()
				db, err := cache.NewInMemorySQLiteStore(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				if err := db.AddRepository(ctx, cache.RepositoryBinding{RepoID: "created-primary", Owner: "owner", Name: "repo", APIBaseURL: api.URL, Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
					t.Fatal(err)
				}
				store := &creationPauseStore{SQLiteStore: db, entered: make(chan struct{}), release: make(chan struct{}), failLate: failLate}
				svc, err := NewWithMode(store, gitcode.ProviderModeLive, "offline-test-token", ServiceConfig{BaseURL: api.URL, LockPath: filepath.Join(t.TempDir(), "writer.lock")})
				if err != nil {
					t.Fatal(err)
				}
				req := WriteCommandRequest{RepoID: "created-primary", Mode: WriteModeLive, Title: "Title", Body: "original description", Head: "topic", Base: "main", IdempotencyKey: "creation-late"}
				create := svc.CreatePR
				if kind == "issue" {
					create = svc.CreateIssue
				}
				done := make(chan error, 1)
				go func() { _, err := create(ctx, req); done <- err }()
				released, drained := false, false
				defer func() {
					if !released {
						close(store.release)
					}
					if !drained {
						<-done
					}
				}()
				select {
				case <-store.entered:
				case <-ctx.Done():
					t.Fatal("pending publication not reached")
				}
				if kind == "issue" {
					confirmation, err := db.GetCacheConfirmationByKey(ctx, req.RepoID, req.IdempotencyKey)
					if err != nil || confirmation != nil {
						t.Fatal("cache confirmation preceded atomic publication")
					}
				}
				newer, err := create(ctx, req)
				if err != nil {
					t.Fatal(err)
				}
				before, err := svc.GetSource(ctx, GetSourceRequest{RepoID: req.RepoID, ID: newer.ID})
				if err != nil || before.Body != "newer canonical description" {
					t.Fatal("canonical recovery did not publish newer body")
				}
				close(store.release)
				released = true
				firstErr := <-done
				drained = true
				if firstErr != nil {
					t.Fatalf("settled recovery was downgraded: %v", firstErr)
				}
				after, err := svc.GetSource(ctx, GetSourceRequest{RepoID: req.RepoID, ID: newer.ID})
				if err != nil || after.Body != before.Body || after.Provenance != "live" || posts.Load() != 1 {
					t.Fatalf("late publication changed recovery body_match=%v provenance=%s posts=%d err=%v", after.Body == before.Body, after.Provenance, posts.Load(), err)
				}
				receipt, err := db.GetAuditEventByKey(ctx, req.RepoID, req.IdempotencyKey)
				if err != nil || receipt == nil || receipt.Status != audit.StatusSucceeded {
					t.Fatal("late outcome downgraded audit")
				}
				if kind == "issue" {
					confirmation, err := db.GetCacheConfirmationByKey(ctx, req.RepoID, req.IdempotencyKey)
					if err != nil || confirmation == nil || confirmation.RecordID != newer.ID {
						t.Fatal("atomic cache confirmation missing")
					}
				}
			})
		}
	}
}

func TestLiveCreateRecoveryRejectsProviderIdentityDrift(t *testing.T) {
	for _, kind := range []string{"issue", "pull_request"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			var posts, gets atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				id := 9001
				if r.Method == http.MethodPost {
					posts.Add(1)
					w.WriteHeader(http.StatusCreated)
				} else if gets.Add(1) > 1 || kind == "issue" {
					id = 9002
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "number": 7, "title": "Title", "body": "description", "state": "open", "head": map[string]string{"ref": "topic"}, "base": map[string]string{"ref": "main"}})
			}))
			defer api.Close()
			db, err := cache.NewInMemorySQLiteStore(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := db.AddRepository(ctx, cache.RepositoryBinding{RepoID: "created-primary", Owner: "owner", Name: "repo", APIBaseURL: api.URL, Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
				t.Fatal(err)
			}
			svc, err := NewWithMode(&writeRefreshFailStore{Store: db, failNextRefresh: true}, gitcode.ProviderModeLive, "offline-test-token", ServiceConfig{BaseURL: api.URL, LockPath: filepath.Join(t.TempDir(), "writer.lock")})
			if err != nil {
				t.Fatal(err)
			}
			req := WriteCommandRequest{RepoID: "created-primary", Mode: WriteModeLive, Title: "Title", Body: "description", Head: "topic", Base: "main", IdempotencyKey: "creation-drift"}
			create := svc.CreatePR
			if kind == "issue" {
				create = svc.CreateIssue
			}
			if _, err := create(ctx, req); err == nil {
				t.Fatal("expected publication failure")
			}
			receipt, err := db.GetAuditEventByKey(ctx, req.RepoID, req.IdempotencyKey)
			if err != nil || receipt.RequestMetadata["provider_id"] != "9001" {
				t.Fatal("provider identity not retained")
			}
			if _, err := create(ctx, req); err == nil {
				t.Fatal("provider identity drift accepted")
			}
			if posts.Load() != 1 {
				t.Fatal("identity drift repeated mutation")
			}
			if _, err := svc.GetSource(ctx, GetSourceRequest{RepoID: req.RepoID, ID: receipt.RecordID}); err == nil {
				t.Fatal("drifted identity published")
			}
		})
	}
}

func TestLiveCreateLegacyPartialReceiptFailsClosed(t *testing.T) {
	for _, command := range []string{"create-issue", "create-pr"} {
		t.Run(command, func(t *testing.T) {
			ctx := context.Background()
			var requests atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(http.StatusNotFound) }))
			defer api.Close()
			db, err := cache.NewInMemorySQLiteStore(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := db.AddRepository(ctx, cache.RepositoryBinding{RepoID: "created-primary", Owner: "owner", Name: "repo", APIBaseURL: api.URL, Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
				t.Fatal(err)
			}
			req := WriteCommandRequest{RepoID: "created-primary", Mode: WriteModeLive, Title: "Title", Body: "description", Head: "topic", Base: "main", IdempotencyKey: "legacy-creation"}
			key, fingerprint := writeIdempotency(command, req)
			prior := audit.WithRequestMetadata(audit.RemoteConfirmedCacheRefreshPending(req.RepoID, key, command, "PRIMARY-7", "primary", "7", fingerprint, "legacy confirmation", time.Now().UTC()), map[string]string{"remote_number": "7"})
			if err := db.RecordAuditEvent(ctx, prior); err != nil {
				t.Fatal(err)
			}
			svc, err := NewWithMode(db, gitcode.ProviderModeLive, "offline-test-token", ServiceConfig{BaseURL: api.URL, LockPath: filepath.Join(t.TempDir(), "writer.lock")})
			if err != nil {
				t.Fatal(err)
			}
			create := svc.CreatePR
			if command == "create-issue" {
				create = svc.CreateIssue
			}
			if _, err := create(ctx, req); err == nil {
				t.Fatal("unproven legacy identity reported success")
			}
			if requests.Load() != 0 {
				t.Fatal("legacy receipt issued provider traffic")
			}
			current, err := db.GetAuditEventByKey(ctx, req.RepoID, key)
			if err != nil || current.Status != prior.Status {
				t.Fatal("legacy receipt lost its fence")
			}
		})
	}
}
