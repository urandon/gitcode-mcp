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

func TestMCPExactLiveIssue42(t *testing.T) {
	ctx := context.Background()
	api := testnet.NewExactIssueAPI(t, 420042, 42)
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "provider-issue", Owner: "owner", Name: "repo", APIBaseURL: api.URL, Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	svc, err := service.NewWithMode(store, gitcode.ProviderModeLive, "offline-test-token", service.ServiceConfig{BaseURL: api.URL, LockPath: filepath.Join(t.TempDir(), "sync.lock")})
	if err != nil {
		t.Fatal(err)
	}
	srv, r, w, _ := newPipeServerWithToolAccess(svc, ToolAccessWrite)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = srv.Serve() }()
	defer func() { _ = r.Close(); wg.Wait() }()
	request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "sync_live", "arguments": map[string]any{"repo_id": "provider-issue", "issues": true, "remote_alias": "issue:42"}}})
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
	var result syncLiveResult
	decodeStructured(t, decodeToolCallResult(t, line), &result)
	if result.FreshCount != 1 || result.FailureCount != 0 || len(result.Results) != 1 || result.Results[0].Record.ID != "ISSUE-42" || api.Details.Load() != 1 || api.Comments.Load() != 1 {
		t.Fatalf("exact MCP refresh=%+v", result)
	}
	cached, err := svc.GetSource(ctx, service.GetSourceRequest{RepoID: "provider-issue", ID: "ISSUE-42"})
	if err != nil || cached.IssueNumber != 42 || cached.Provenance != "live" {
		t.Fatalf("canonical readback=%+v err=%v", cached, err)
	}
}
