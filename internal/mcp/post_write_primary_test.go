package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/service"
	"gitcode-mcp/internal/testnet"
)

func TestMCPImmediateCreatedPrimaryReadback(t *testing.T) {
	for _, kind := range []string{"issue", "pull_request"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			body := "## Created description\n\nExact Markdown.\n"
			api := testnet.NewCreationAPI(t, kind, "Created primary", body)
			store, err := cache.NewInMemorySQLiteStore(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "created-primary", Owner: "owner", Name: "repo", APIBaseURL: api.URL, Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
				t.Fatal(err)
			}
			svc, err := service.NewWithMode(store, gitcode.ProviderModeLive, "offline-test-token", service.ServiceConfig{BaseURL: api.URL, LockPath: filepath.Join(t.TempDir(), "writer.lock")})
			if err != nil {
				t.Fatal(err)
			}
			srv, r, w, _ := newPipeServerWithToolAccess(svc, ToolAccessWrite)
			var wg sync.WaitGroup
			wg.Add(1)
			go func() { defer wg.Done(); _ = srv.Serve() }()
			defer func() { _ = r.Close(); wg.Wait() }()
			call := func(id int, tool string, args map[string]any, dst any) {
				t.Helper()
				request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := r.Write(append(request, '\n')); err != nil {
					t.Fatal(err)
				}
				line, err := readLine(w)
				if err != nil {
					t.Fatal(err)
				}
				decodeStructured(t, decodeToolCallResult(t, line), dst)
			}
			tool := "create_issue"
			if kind == "pull_request" {
				tool = "create_pr"
			}
			args := map[string]any{"repo_id": "created-primary", "write_mode": "live", "title": "Created primary", "body": body, "idempotency_key": "created-primary"}
			if kind == "pull_request" {
				args["head"] = "topic"
				args["base"] = "main"
			}
			var created, replay service.WriteCommandResult
			call(1, tool, args, &created)
			var cached service.SourceRecord
			call(2, "get_source", map[string]any{"repo_id": "created-primary", "id": created.ID}, &cached)
			if cached.Body != body || cached.Status != "open" || cached.Provenance != "live" {
				t.Fatalf("cache body_match=%v state=%s provenance=%s", cached.Body == body, cached.Status, cached.Provenance)
			}
			call(3, tool, args, &replay)
			if replay.Status != "already_applied" || replay.ID != created.ID || api.Posts.Load() != 1 {
				t.Fatalf("replay status=%s posts=%d", replay.Status, api.Posts.Load())
			}
			wantReads := int32(0)
			if kind == "pull_request" {
				wantReads = 1
			}
			if api.Reads.Load() != wantReads {
				t.Fatalf("unexpected read traffic=%d", api.Reads.Load())
			}
		})
	}
}

func TestMCPImmediateLivePRCommentReadback(t *testing.T) {
	ctx := context.Background()
	body := "## Comment\n\nExact Markdown.\n"
	api := testnet.NewPRCommentAPI(t, body)
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "comment-origin", Owner: "owner", Name: "repo", APIBaseURL: api.URL, Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	testnet.SeedPRCommentParent(t, store, "comment-origin")
	svc, err := service.NewWithMode(store, gitcode.ProviderModeLive, "offline-test-token", service.ServiceConfig{BaseURL: api.URL, LockPath: filepath.Join(t.TempDir(), "writer.lock")})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewRPCHandlerWithToolAccess(svc, ToolAccessWrite)
	call := func(tool string, args map[string]any, dst any) {
		t.Helper()
		params, _ := json.Marshal(map[string]any{"name": tool, "arguments": args})
		raw := json.RawMessage(params)
		id := json.RawMessage(`1`)
		resp, ok := handler.Handle(ctx, request{JSONRPC: "2.0", ID: &id, Method: "tools/call", Params: &raw})
		if !ok || resp == nil || resp.Error != nil {
			t.Fatalf("MCP response=%+v", resp)
		}
		encoded, _ := json.Marshal(resp)
		decodeStructured(t, decodeToolCallResult(t, encoded), dst)
	}
	args := map[string]any{"repo_id": "comment-origin", "number": 7, "body": body, "write_mode": "live", "idempotency_key": "one-comment"}
	var created, replay service.WriteCommandResult
	call("add_pr_comment", args, &created)
	var cached service.SourceRecord
	call("get_source", map[string]any{"repo_id": "comment-origin", "id": created.ID}, &cached)
	call("add_pr_comment", args, &replay)
	if cached.Provenance != "live" || cached.Body != body || replay.Status != "already_applied" || api.Posts.Load() != 1 {
		t.Fatalf("origin=%s body_match=%v replay=%s posts=%d", cached.Provenance, cached.Body == body, replay.Status, api.Posts.Load())
	}
}

func TestMCPExhaustedTargetedWriterWaitIsActionable(t *testing.T) {
	data := classifyDomainError(cache.ErrLockContention{Operation: "sync", RepoID: "other", Path: "/private/cache.lock", WaitExhausted: true}, domainErrorContext{Operation: "sync_live", RepoID: "target"})
	if data.Code != "cache_owned" || !strings.Contains(data.Message, "bounded writer wait exhausted") || !strings.Contains(data.Remediation, "do not repeat") {
		t.Fatalf("wait diagnostic=%+v", data)
	}
	encoded, _ := json.Marshal(data)
	if strings.Contains(string(encoded), "/private/") {
		t.Fatal("private lock location leaked")
	}
}

type observedCommentWriterStore struct {
	cache.Store
	contended chan struct{}
}

func (s *observedCommentWriterStore) AcquireWriter(ctx context.Context, req cache.WriterRequest) (*cache.WriterLease, error) {
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

func TestMCPTargetedPRCommentsWaitForSharedWriter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	api := testnet.NewPRCommentAPI(t, "read once")
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "comment-origin", Owner: "owner", Name: "repo", APIBaseURL: api.URL, Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	testnet.SeedPRCommentParent(t, store, "comment-origin")
	observed := &observedCommentWriterStore{Store: store, contended: make(chan struct{}, 1)}
	lockPath := filepath.Join(t.TempDir(), "writer.lock")
	svc, err := service.NewWithMode(observed, gitcode.ProviderModeLive, "offline-test-token", service.ServiceConfig{BaseURL: api.URL, LockPath: lockPath})
	if err != nil {
		t.Fatal(err)
	}
	held, err := store.AcquireWriter(ctx, cache.WriterRequest{Operation: "sync", RepoID: "other", LockPath: lockPath})
	if err != nil {
		t.Fatal(err)
	}
	defer store.ReleaseWriter(context.Background(), held)
	handler := NewRPCHandlerWithToolAccess(svc, ToolAccessWrite)
	params := json.RawMessage(`{"name":"sync_live","arguments":{"repo_id":"comment-origin","pr_comments":true,"remote_alias":"pr:7","idempotency_key":"canonical-read"}}`)
	id := json.RawMessage(`1`)
	done := make(chan *response, 1)
	go func() {
		resp, _ := handler.Handle(ctx, request{JSONRPC: "2.0", ID: &id, Method: "tools/call", Params: &params})
		done <- resp
	}()
	select {
	case <-observed.contended:
	case <-ctx.Done():
		t.Fatal("no admission observation")
	}
	if api.Reads.Load() != 0 || api.Posts.Load() != 0 {
		t.Fatal("provider contacted before admission")
	}
	if err := store.ReleaseWriter(ctx, held); err != nil {
		t.Fatal(err)
	}
	resp := <-done
	if resp == nil || resp.Error != nil {
		t.Fatalf("MCP response=%+v", resp)
	}
	encoded, _ := json.Marshal(resp)
	var result syncLiveResult
	decodeStructured(t, decodeToolCallResult(t, encoded), &result)
	if result.FailureCount != 0 || result.SuccessCount != 1 || api.Reads.Load() != 1 || api.Posts.Load() != 0 {
		t.Fatalf("result=%+v reads=%d posts=%d", result, api.Reads.Load(), api.Posts.Load())
	}
}
