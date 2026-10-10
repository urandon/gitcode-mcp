package mcp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"

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
