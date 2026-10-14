package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"

	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/localgit"
	"gitcode-mcp/internal/service"
)

func TestLocalProjectSearchCLIAndMCPRealGitAndExplicitProvider(t *testing.T) {
	base := newTestSource(t)
	short, err := os.MkdirTemp(".", "g5-")
	if err != nil {
		t.Fatal("short runtime fixture unavailable")
	}
	t.Cleanup(func() { os.RemoveAll(short) })
	base.cacheDir = filepath.Join(short, "cache")
	base.env[config.EnvMCPConfigPath] = filepath.Join(base.configDir, "local.yaml")
	var calls, documentTransfers atomic.Int64
	var providerFailure atomic.Bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if providerFailure.Load() {
			w.WriteHeader(503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/tags":
			json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]any{"name": "fixture-embedding", "digest": "fixture-revision"}}})
		case "/api/embed":
			var req struct {
				Input []string `json:"input"`
			}
			if json.NewDecoder(r.Body).Decode(&req) != nil {
				t.Error("invalid bounded fixture embed request")
				w.WriteHeader(400)
				return
			}
			vectors := make([][]float64, len(req.Input))
			for i, input := range req.Input {
				if bytes.Contains([]byte(input), []byte("GitOnlyTransportFixture")) {
					documentTransfers.Add(1)
				}
				vectors[i] = []float64{1, 0, 0, 0, 0, 0, 0, 0}
			}
			json.NewEncoder(w).Encode(map[string]any{"model": "fixture-embedding", "embeddings": vectors})
		default:
			t.Error("local search attempted unexpected provider or GitCode operation")
			w.WriteHeader(500)
		}
	}))
	defer provider.Close()
	base.env[config.EnvAPIURL] = provider.URL + "/api/v5"
	src := localProjectSource{base}
	localProjectCLI(t, src, "", "init", "--project", "lab", "--project-id", "lab", "--storage-ref", "data", "--rag-profile", "fixture", "--confirm")
	data, err := os.ReadFile(base.env[config.EnvMCPConfigPath])
	if err != nil || bytes.Count(data, []byte("rag_profile: fixture")) != 1 {
		t.Fatal("local setup fixture invalid")
	}
	data = append(data, []byte("\nrag:\n  providers:\n    fixture:\n      type: ollama\n      data_boundary: local_network\n      endpoint: "+strconv.Quote(provider.URL)+"\n  profiles:\n    fixture:\n      provider: fixture\n      model: fixture-embedding\n      dimensions: 8\n      batch_size: 2\n      max_input_tokens: 512\n")...)
	if os.WriteFile(base.env[config.EnvMCPConfigPath], data, 0600) != nil {
		t.Fatal("explicit profile fixture unavailable")
	}
	_, stop := startLocalProjectDaemon(t, src)
	defer stop()
	var status service.LocalProjectsStatus
	json.Unmarshal(localProjectCLI(t, src, "", "status"), &status)
	var issue localgit.MutationResult
	json.Unmarshal(localProjectCLI(t, src, "", "mutate", "--project", "lab", "--operation", "create_issue", "--title", "Question", "--body", "oldneedle GitOnlyTransportFixture", "--expected-head", status.Projects[0].Commit, "--write-mode", "local", "--idempotency-key", "question"), &issue)
	var cliSearch, mcpSearch service.LocalProjectSearchResult
	json.Unmarshal(localProjectCLI(t, src, "", "search", "--project", "lab", "--query", "oldneedle"), &cliSearch)
	json.Unmarshal(localProjectMCP(t, src, "tools/call", projectTool("local_project_search", service.LocalProjectSearchRequest{Project: "lab", Query: "oldneedle"}), ""), &mcpSearch)
	if calls.Load() != 0 || cliSearch.EffectiveMode != "fulltext" || len(cliSearch.Hits) != 1 || len(mcpSearch.Hits) != 1 || cliSearch.Hits[0].Citation != issue.Entity.Citation || cliSearch.Hits[0].Citation != mcpSearch.Hits[0].Citation {
		t.Fatal("default transports contacted provider or lost exact citation")
	}
	localProjectMCP(t, localReadOnlySource{src}, "tools/call", projectTool("local_project_index", service.LocalProjectIndexRequest{Project: "lab", WriteMode: "local"}), "tool_disabled_by_policy")
	if calls.Load() != 0 {
		t.Fatal("read policy performed provider work")
	}
	var indexed service.LocalProjectIndexResult
	json.Unmarshal(localProjectCLI(t, src, "", "index", "--project", "lab", "--write-mode", "local", "--max-chunks", "8"), &indexed)
	if indexed.State != "ready" || documentTransfers.Load() != 1 {
		t.Fatal("explicit bounded index did not embed document once")
	}
	before := documentTransfers.Load()
	json.Unmarshal(localProjectMCP(t, src, "tools/call", projectTool("local_project_search", service.LocalProjectSearchRequest{Project: "lab", Query: "oldneedle", Mode: "hybrid"}), ""), &mcpSearch)
	if mcpSearch.EffectiveMode != "hybrid" || mcpSearch.IndexState != "ready" || len(mcpSearch.Hits) != 1 || documentTransfers.Load() != before {
		t.Fatal("hybrid query repaired index or sent cached text")
	}
	update := service.LocalProjectMutation{Project: "lab", Mutation: localgit.Mutation{Operation: "update_issue", ID: "ISSUE-1", Title: "Question", State: "open", Body: "newneedle GitOnlyTransportFixture", ExpectedRevision: issue.Entity.Citation.Blob, WriteMode: "local", IdempotencyKey: "edit"}}
	localProjectMCP(t, src, "tools/call", projectTool("local_project_mutate", update), "")
	json.Unmarshal(localProjectCLI(t, src, "", "search", "--project", "lab", "--query", "oldneedle", "--mode", "hybrid"), &cliSearch)
	if cliSearch.EffectiveMode != "fulltext" || cliSearch.IndexState != "stale" || len(cliSearch.Hits) != 0 || documentTransfers.Load() != before {
		t.Fatal("transport used or repaired stale semantic set")
	}
	json.Unmarshal(localProjectMCP(t, src, "tools/call", projectTool("local_project_index", service.LocalProjectIndexRequest{Project: "lab", WriteMode: "local", MaxChunks: 8}), ""), &indexed)
	if indexed.State != "ready" || indexed.EmbeddedChunks != 1 {
		t.Fatal("MCP index failed to use shared engine")
	}
	before = documentTransfers.Load()
	providerFailure.Store(true)
	json.Unmarshal(localProjectMCP(t, src, "tools/call", projectTool("local_project_search", service.LocalProjectSearchRequest{Project: "lab", Query: "newneedle", Mode: "hybrid"}), ""), &mcpSearch)
	if mcpSearch.EffectiveMode != "fulltext" || mcpSearch.Fallback != "semantic_unavailable" || len(mcpSearch.Hits) != 1 || documentTransfers.Load() != before {
		t.Fatal("provider failure hid lexical results or repaired documents")
	}
	localProjectCLI(t, src, "local_git_invalid_argument", "search", "--project", "lab", "--query", "x", "--write-mode", "local")
	localProjectMCP(t, src, "tools/call", projectTool("local_project_search", map[string]any{"project": "lab", "query": "x", "cache_path": "private-coordinate"}), "local_git_invalid_argument")
}
