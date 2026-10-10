package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"gitcode-mcp/internal/audit"
	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/testnet"
)

func TestLiveCreatePrimaryImmediateReadback(t *testing.T) {
	for _, kind := range []string{"issue", "pull_request"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			var posts, gets atomic.Int32
			body := "## Confirmed description\n\nExact Markdown.\n"
			canonical := map[string]any{"id": 9001, "number": 7, "title": "Created primary", "body": body, "state": "open", "base": map[string]string{"ref": "main"}, "head": map[string]string{"ref": "topic"}}
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodPost && kind == "pull_request" && r.URL.Path == "/api/v5/repos/owner/repo/pulls":
					posts.Add(1)
					w.WriteHeader(http.StatusCreated)
					_ = json.NewEncoder(w).Encode(map[string]any{"id": 9001, "number": 7, "title": "Created primary", "state": "opened", "base": map[string]string{"ref": "main"}, "head": map[string]string{"ref": "topic"}})
				case r.Method == http.MethodPost && kind == "issue" && r.URL.Path == "/api/v5/repos/owner/repo/issues":
					posts.Add(1)
					w.WriteHeader(http.StatusCreated)
					_ = json.NewEncoder(w).Encode(canonical)
				case r.Method == http.MethodGet && r.URL.Path == "/api/v5/repos/owner/repo/pulls/7":
					gets.Add(1)
					_ = json.NewEncoder(w).Encode(canonical)
				default:
					t.Errorf("unexpected route %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer api.Close()
			store, err := cache.NewInMemorySQLiteStore(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "created-primary", Owner: "owner", Name: "repo", APIBaseURL: api.URL, Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
				t.Fatal(err)
			}
			svc, err := NewWithMode(store, gitcode.ProviderModeLive, "offline-test-token", ServiceConfig{BaseURL: api.URL, LockPath: filepath.Join(t.TempDir(), "writer.lock")})
			if err != nil {
				t.Fatal(err)
			}
			req := WriteCommandRequest{RepoID: "created-primary", Mode: WriteModeLive, Title: "Created primary", Body: body, Head: "topic", Base: "main", IdempotencyKey: "create-primary"}
			create := svc.CreatePR
			if kind == "issue" {
				create = svc.CreateIssue
			}
			result, err := create(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			cached, err := svc.GetSource(ctx, GetSourceRequest{RepoID: req.RepoID, ID: result.ID})
			if err != nil {
				t.Fatal(err)
			}
			if cached.Body != body || cached.Status != "open" || cached.Provenance != "live" {
				t.Fatalf("immediate primary body_match=%v state=%s provenance=%s", cached.Body == body, cached.Status, cached.Provenance)
			}
			replay, err := create(ctx, req)
			if err != nil || replay.Status != "already_applied" || replay.ID != result.ID || posts.Load() != 1 {
				t.Fatalf("replay=%+v err=%v posts=%d", replay, err, posts.Load())
			}
			if kind == "pull_request" && gets.Load() != 1 {
				t.Fatalf("canonical reads=%d", gets.Load())
			}
			record, err := store.GetRecord(ctx, req.RepoID, result.ID)
			if err != nil || record.Provenance != cache.ProvenanceRemote {
				t.Fatalf("storage role changed: %s err=%v", record.Provenance, err)
			}
		})
	}
}

func TestLiveCreatePRReadbackAndCacheFailureNeverRepeatsPOST(t *testing.T) {
	for _, failure := range []string{"readback", "body-mismatch", "id-mismatch", "number-mismatch", "title-mismatch", "head-mismatch", "base-mismatch", "state-mismatch", "post-ambiguous", "post-redirect", "post-malformed", "cache"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			var posts, gets atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodPost && r.URL.Path == "/api/v5/repos/owner/repo/pulls" {
					posts.Add(1)
					if failure == "post-ambiguous" {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					if failure == "post-redirect" {
						w.Header().Set("Location", "/api/v5/repos/owner/repo/pulls")
						w.WriteHeader(http.StatusTemporaryRedirect)
						return
					}
					w.WriteHeader(http.StatusCreated)
					if failure == "post-malformed" {
						_, _ = w.Write([]byte(`{"number":7}`))
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"id": 9001, "number": 7, "title": "Created primary"})
					return
				}
				if r.Method == http.MethodGet && r.URL.Path == "/api/v5/repos/owner/repo/pulls/7" {
					gets.Add(1)
					if failure == "readback" {
						w.WriteHeader(http.StatusForbidden)
						return
					}
					body := "description"
					if failure == "body-mismatch" {
						body = "different provider content"
					}
					canonical := map[string]any{"id": 9001, "number": 7, "title": "Created primary", "body": body, "state": "open", "base": map[string]string{"ref": "main"}, "head": map[string]string{"ref": "topic"}}
					switch failure {
					case "id-mismatch":
						canonical["id"] = 9002
					case "number-mismatch":
						canonical["number"] = 8
					case "title-mismatch":
						canonical["title"] = "Different"
					case "head-mismatch":
						canonical["head"] = map[string]string{"ref": "other"}
					case "base-mismatch":
						canonical["base"] = map[string]string{"ref": "other"}
					case "state-mismatch":
						canonical["state"] = "closed"
					}
					_ = json.NewEncoder(w).Encode(canonical)
					return
				}
				t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			}))
			defer api.Close()
			store, err := cache.NewInMemorySQLiteStore(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "created-primary", Owner: "owner", Name: "repo", APIBaseURL: api.URL, Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
				t.Fatal(err)
			}
			wrapped := &writeRefreshFailStore{Store: store, failNextRefresh: failure == "cache"}
			svc, err := NewWithMode(wrapped, gitcode.ProviderModeLive, "offline-test-token", ServiceConfig{BaseURL: api.URL, LockPath: filepath.Join(t.TempDir(), "writer.lock")})
			if err != nil {
				t.Fatal(err)
			}
			req := WriteCommandRequest{RepoID: "created-primary", Mode: WriteModeLive, Title: "Created primary", Body: "description", Head: "topic", Base: "main", IdempotencyKey: "create-failure"}
			if _, err := svc.CreatePR(ctx, req); err == nil {
				t.Fatal("failed confirmation/publication reported success")
			}
			entry, err := store.GetAuditEventByKey(ctx, req.RepoID, req.IdempotencyKey)
			if err != nil || entry == nil || entry.Status == audit.StatusSucceeded {
				t.Fatalf("failure audit=%+v err=%v", entry, err)
			}
			if _, err := svc.GetSource(ctx, GetSourceRequest{RepoID: req.RepoID, ID: "PR-7"}); err == nil {
				t.Fatal("unconfirmed primary published")
			}
			result, err := svc.CreatePR(ctx, req)
			if posts.Load() != 1 {
				t.Fatalf("unsafe duplicate posts=%d", posts.Load())
			}
			if failure == "cache" {
				if err != nil || result.Status != "succeeded" || result.ID != "PR-7" || result.RemoteNumber != 7 || gets.Load() != 2 {
					t.Fatalf("cache repair result=%+v err=%v gets=%d", result, err, gets.Load())
				}
				cached, err := svc.GetSource(ctx, GetSourceRequest{RepoID: req.RepoID, ID: result.ID})
				if err != nil || cached.Body != req.Body || cached.Provenance != "live" {
					t.Fatalf("cache repair body_match=%v provenance=%s err=%v", cached.Body == req.Body, cached.Provenance, err)
				}
			} else if err == nil {
				t.Fatal("ambiguous creation replay reported success")
			}
		})
	}
}

func TestLiveCreateIssueCacheRepairUsesCanonicalNumberAndOrigin(t *testing.T) {
	ctx := context.Background()
	api := testnet.NewCreationAPI(t, "issue", "Created primary", "description")
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "created-primary", Owner: "owner", Name: "repo", APIBaseURL: api.URL, Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	wrapped := &writeRefreshFailStore{Store: store, failNextRefresh: true}
	svc, err := NewWithMode(wrapped, gitcode.ProviderModeLive, "offline-test-token", ServiceConfig{BaseURL: api.URL, LockPath: filepath.Join(t.TempDir(), "writer.lock")})
	if err != nil {
		t.Fatal(err)
	}
	req := WriteCommandRequest{RepoID: "created-primary", Mode: WriteModeLive, Title: "Created primary", Body: "description", IdempotencyKey: "create-issue-cache"}
	if _, err := svc.CreateIssue(ctx, req); err == nil {
		t.Fatal("failed cache publication reported success")
	}
	result, err := svc.CreateIssue(ctx, req)
	if err != nil || result.ID != "ISSUE-7" || result.IssueNumber != 7 || api.Posts.Load() != 1 || api.Reads.Load() != 1 {
		t.Fatalf("repair id=%s number=%d posts=%d reads=%d err=%v", result.ID, result.IssueNumber, api.Posts.Load(), api.Reads.Load(), err)
	}
	source, err := svc.GetSource(ctx, GetSourceRequest{RepoID: req.RepoID, ID: result.ID})
	if err != nil || source.Body != req.Body || source.Provenance != "live" {
		t.Fatalf("repair body_match=%v provenance=%s err=%v", source.Body == req.Body, source.Provenance, err)
	}
}

func TestLiveCreatePRConcurrentClaimSendsOnePOST(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started, release := make(chan struct{}), make(chan struct{})
	var posts atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			if posts.Add(1) == 1 {
				close(started)
			}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			w.WriteHeader(http.StatusCreated)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 9001, "number": 7, "title": "Created primary", "body": "description", "state": "open", "base": map[string]string{"ref": "main"}, "head": map[string]string{"ref": "topic"}})
	}))
	defer api.Close()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "created-primary", Owner: "owner", Name: "repo", APIBaseURL: api.URL, Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	svc, err := NewWithMode(store, gitcode.ProviderModeLive, "offline-test-token", ServiceConfig{BaseURL: api.URL, LockPath: filepath.Join(t.TempDir(), "writer.lock")})
	if err != nil {
		t.Fatal(err)
	}
	req := WriteCommandRequest{RepoID: "created-primary", Mode: WriteModeLive, Title: "Created primary", Body: "description", Head: "topic", Base: "main", IdempotencyKey: "create-concurrent"}
	done := make(chan error, 1)
	go func() { _, err := svc.CreatePR(ctx, req); done <- err }()
	defer func() { cancel(); close(release); <-done }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("creation did not reach POST")
	}
	if _, err := svc.CreatePR(ctx, req); err == nil {
		t.Fatal("concurrent pending creation reported success")
	}
	if posts.Load() != 1 {
		t.Fatalf("concurrent posts=%d", posts.Load())
	}
}

func TestFixtureIssueAndPRWriteGraphsRemainFixture(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-origin", Owner: "owner", Name: "repo", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, &fakeGitCodeClient{})
	svc.providerMode = gitcode.ProviderModeFixture
	_, issue := svc.issueWriteGraph("fixture-origin", gitcode.Issue{ID: "9001", Number: 7, Title: "Fixture"}, gitcode.WriteResult[gitcode.Issue]{Confirmed: true}, svc.now())
	_, pr, err := svc.pullRequestWriteGraph(ctx, "fixture-origin", gitcode.PullRequest{ID: "9002", Number: 8, Title: "Fixture"}, gitcode.WriteResult[gitcode.PullRequest]{Confirmed: true}, svc.now())
	if err != nil {
		t.Fatal(err)
	}
	for _, graph := range []cache.RecordGraph{issue, pr} {
		if err := store.UpsertRecordGraph(ctx, graph); err != nil {
			t.Fatal(err)
		}
		source, err := store.GetSourceScoped(ctx, "fixture-origin", graph.Record.ID)
		if err != nil || source.Provenance != cache.ProvenanceFixture {
			t.Fatalf("fixture relabeled=%s err=%v", source.Provenance, err)
		}
	}
}
