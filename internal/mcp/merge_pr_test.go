package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/capability"
	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/service"
)

func TestIssue160MergePRCapabilityAndSchema(t *testing.T) {
	cap, ok := capability.LookupByMCPName("merge_pr")
	if !ok || !cap.MCP.Enabled || !cap.CLI.Enabled || cap.CLIName != "merge-pr" || cap.ServiceCommand != "merge-pr" || cap.Safety != capability.SafetyAuditedWrite {
		t.Fatal("audited shared merge capability is missing")
	}
	if cap.UI.Enabled || cap.UI.DisabledReason == "" {
		t.Fatal("Admin remote-write boundary must be explicit")
	}
	schema := writeToolInputSchema("merge_pr")
	for _, field := range []string{"repo_id", "number", "write_mode", "idempotency_key", "sha"} {
		if !containsString(schema.Required, field) {
			t.Fatalf("required merge field missing: %s", field)
		}
	}
	for _, strategy := range []string{"merge", "squash", "rebase"} {
		if !containsString(schema.Properties["strategy"].Enum, strategy) {
			t.Fatalf("merge strategy missing: %s", strategy)
		}
	}
}

const mergeExpectedSHA = "0123456789abcdef0123456789abcdef01234567"

type mergePRSpyService struct {
	serviceInterface
	calls []service.WriteCommandRequest
}

func (s *mergePRSpyService) MergePR(_ context.Context, req service.WriteCommandRequest) (service.WriteCommandResult, error) {
	s.calls = append(s.calls, req)
	return service.WriteCommandResult{Command: "merge-pr", Status: "succeeded", RemoteNumber: req.Number, IdempotencyKey: req.IdempotencyKey}, nil
}

func mergeRPC(t *testing.T, h *RPCHandler, name string, args any) *response {
	t.Helper()
	params, err := json.Marshal(map[string]any{"name": name, "arguments": args})
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(params)
	id := json.RawMessage(`1`)
	resp, ok := h.Handle(context.Background(), request{JSONRPC: "2.0", ID: &id, Method: "tools/call", Params: &raw})
	if !ok || resp == nil {
		t.Fatal("missing JSON-RPC response")
	}
	return resp
}

func mergeArgs() map[string]any {
	return map[string]any{"repo_id": "merge-parity", "number": 7, "sha": mergeExpectedSHA, "write_mode": "live", "idempotency_key": "one-merge"}
}

func TestIssue160MergePRValidatesBeforeService(t *testing.T) {
	for _, tc := range []struct {
		field string
		value any
	}{
		{"repo_id", " "}, {"write_mode", ""}, {"write_mode", "dry-run"},
		{"number", 0}, {"number", -1}, {"number", 1.5}, {"number", "7"},
		{"sha", ""}, {"sha", "abc123"}, {"sha", strings.Repeat("g", 40)},
		{"idempotency_key", " "}, {"strategy", "auto"}, {"strategy", "force"},
		{"force", true}, {"url", "https://example.invalid/destination"},
	} {
		t.Run(fmt.Sprintf("%s-%v", tc.field, tc.value), func(t *testing.T) {
			spy := &mergePRSpyService{}
			args := mergeArgs()
			args[tc.field] = tc.value
			resp := mergeRPC(t, NewRPCHandlerWithToolAccess(spy, ToolAccessWrite), "merge_pr", args)
			if resp.Error == nil || resp.Error.Code != -32602 || resp.Error.Data == nil || resp.Error.Data.Code != "invalid_arguments" || len(spy.calls) != 0 {
				t.Fatal("invalid merge input reached service or lost validation category")
			}
		})
	}
	for _, strategy := range []string{"", "merge", "squash", "rebase"} {
		spy := &mergePRSpyService{}
		args := mergeArgs()
		args["strategy"] = strategy
		args["sha"] = strings.ToUpper(mergeExpectedSHA)
		resp := mergeRPC(t, NewRPCHandlerWithToolAccess(spy, ToolAccessWrite), "merge_pr", args)
		if resp.Error != nil || len(spy.calls) != 1 {
			t.Fatalf("valid strategy %q failed", strategy)
		}
		req := spy.calls[0]
		if req.RepoID != "merge-parity" || req.Number != 7 || req.Sha != mergeExpectedSHA || req.Strategy != strategy || req.Mode != service.WriteModeLive || req.IdempotencyKey != "one-merge" {
			t.Fatal("merge request mapping changed")
		}
	}
	// Operation-specific validation must not enable merge strategies for links.
	resp := mergeRPC(t, NewRPCHandlerWithToolAccess(&mergePRSpyService{}, ToolAccessWrite), "link_pr_issue", map[string]any{"repo_id": "merge-parity", "write_mode": "live", "strategy": "merge"})
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Fatal("link strategy validation was weakened")
	}
}

func TestIssue160MergePRReadPolicyAndStdioDiscovery(t *testing.T) {
	for _, access := range []ToolAccess{ToolAccessRead, ToolAccessWrite} {
		t.Run(string(access), func(t *testing.T) {
			spy := &mergePRSpyService{}
			srv, r, w, _ := newPipeServerWithToolAccess(spy, access)
			var wg sync.WaitGroup
			wg.Add(1)
			go func() { defer wg.Done(); _ = srv.Serve() }()
			defer func() { _ = r.Close(); wg.Wait() }()
			_, err := r.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n"))
			if err != nil {
				t.Fatal(err)
			}
			line, err := readLine(w)
			if err != nil {
				t.Fatal(err)
			}
			var listed response
			if err := json.Unmarshal(line, &listed); err != nil {
				t.Fatal(err)
			}
			var tools struct {
				Tools []toolDefinition `json:"tools"`
			}
			if err := json.Unmarshal(listed.Result, &tools); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, tool := range tools.Tools {
				if tool.Name == "merge_pr" {
					found = true
				}
			}
			if found != (access == ToolAccessWrite) {
				t.Fatal("merge discovery policy mismatch")
			}
			args := mergeArgs()
			if access == ToolAccessRead {
				args = map[string]any{"force": true}
			}
			payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "merge_pr", "arguments": args}})
			if _, err := r.Write(append(payload, '\n')); err != nil {
				t.Fatal(err)
			}
			line, err = readLine(w)
			if err != nil {
				t.Fatal(err)
			}
			var called response
			if err := json.Unmarshal(line, &called); err != nil {
				t.Fatal(err)
			}
			if access == ToolAccessRead {
				if called.Error == nil || called.Error.Data == nil || called.Error.Data.Code != "tool_disabled_by_policy" || len(spy.calls) != 0 {
					t.Fatal("read-only call did not fail before validation/service")
				}
			} else if called.Error != nil || len(spy.calls) != 1 {
				t.Fatal("stdio merge dispatch failed")
			}
		})
	}
}

func TestIntegrationIssue160MCPMergeLifecycle(t *testing.T) {
	for _, scenario := range []string{"confirmed", "stale_head", "forbidden", "ambiguous"} {
		t.Run(scenario, func(t *testing.T) {
			var puts, gets atomic.Int32
			var merged atomic.Bool
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet && r.URL.Path == "/api/v5/repos/owner/repo/pulls/7" {
					gets.Add(1)
					state := "open"
					if merged.Load() {
						state = "merged"
					}
					fmt.Fprintf(w, `{"id":9001,"number":7,"title":"merge parity","body":"original body","state":%q,"head":{"ref":"topic","sha":%q},"base":{"ref":"main"}}`, state, mergeExpectedSHA)
					return
				}
				if r.Method == http.MethodPut && r.URL.Path == "/api/v5/repos/owner/repo/pulls/7/merge" {
					puts.Add(1)
					var payload struct {
						Method string `json:"merge_method"`
					}
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Method != "squash" {
						t.Error("unexpected merge payload")
					}
					if scenario == "forbidden" {
						w.WriteHeader(http.StatusForbidden)
						fmt.Fprint(w, `{"message":"protected branch; fixture-provider-body-sentinel"}`)
						return
					}
					merged.Store(true)
					if scenario == "ambiguous" {
						w.WriteHeader(http.StatusBadGateway)
						fmt.Fprint(w, `{"message":"fixture-provider-body-sentinel"}`)
						return
					}
					fmt.Fprintf(w, `{"sha":%q,"merged":true}`, strings.Repeat("a", 40))
					return
				}
				t.Error("unexpected provider request")
				w.WriteHeader(http.StatusNotFound)
			}))
			defer api.Close()
			ctx := context.Background()
			store, err := cache.NewInMemorySQLiteStore(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "merge-parity", Owner: "owner", Name: "repo", APIBaseURL: api.URL, Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
				t.Fatal(err)
			}
			svc, err := service.NewWithMode(store, gitcode.ProviderModeLive, "offline-test-token", service.ServiceConfig{BaseURL: api.URL})
			if err != nil {
				t.Fatal(err)
			}
			h := NewRPCHandlerWithToolAccess(svc, ToolAccessWrite)
			args := mergeArgs()
			args["strategy"] = "squash"
			if scenario == "stale_head" {
				args["sha"] = strings.Repeat("b", 40)
			}
			resp := mergeRPC(t, h, "merge_pr", args)
			if scenario == "stale_head" || scenario == "forbidden" {
				if resp.Error == nil || resp.Error.Code != -32000 || resp.Error.Data == nil || resp.Error.Data.Code == "internal_error" {
					t.Fatal("provider guard lost typed failure")
				}
				wantPuts := int32(1)
				if scenario == "stale_head" {
					wantPuts = 0
				}
				if puts.Load() != wantPuts {
					t.Fatal("provider guard was bypassed")
				}
				encoded, _ := json.Marshal(resp)
				if strings.Contains(string(encoded), "fixture-provider-body-sentinel") || strings.Contains(string(encoded), "offline-test-token") {
					t.Fatal("raw provider evidence leaked")
				}
				return
			}
			if scenario == "ambiguous" {
				if resp.Error == nil {
					t.Fatal("ambiguous PUT falsely succeeded")
				}
				resp = mergeRPC(t, h, "merge_pr", args)
			}
			if resp.Error != nil {
				t.Fatalf("merge failed: code=%s", resp.Error.Data.Code)
			}
			var receipt service.WriteCommandResult
			encoded, _ := json.Marshal(resp)
			decodeStructured(t, decodeToolCallResult(t, encoded), &receipt)
			wantStatus := "succeeded"
			if scenario == "ambiguous" {
				wantStatus = "recovered_after_ambiguous_write"
			}
			if receipt.Command != "merge-pr" || receipt.Status != wantStatus || receipt.ID != "PR-7" || receipt.RemoteNumber != 7 || receipt.RemoteID != "7" || receipt.IdempotencyKey != "one-merge" || receipt.Evidence == "" {
				t.Fatalf("canonical receipt: command=%s status=%s number=%d remote_id=%s key_match=%t evidence=%t", receipt.Command, receipt.Status, receipt.RemoteNumber, receipt.RemoteID, receipt.IdempotencyKey == "one-merge", receipt.Evidence != "")
			}
			if scenario == "confirmed" && receipt.RemoteRevision != strings.Repeat("a", 40) {
				t.Fatal("merge revision lost")
			}
			read := mergeRPC(t, h, "get_source", map[string]any{"repo_id": "merge-parity", "id": "PR-7"})
			encoded, _ = json.Marshal(read)
			var source service.SourceRecord
			decodeStructured(t, decodeToolCallResult(t, encoded), &source)
			if source.Status != "merged" || source.Provenance != "live" || source.Body != "original body" {
				t.Fatal("immediate canonical cache graph mismatch")
			}
			reads := gets.Load()
			replayed := mergeRPC(t, h, "merge_pr", args)
			encoded, _ = json.Marshal(replayed)
			decodeStructured(t, decodeToolCallResult(t, encoded), &receipt)
			if receipt.Status != "already_applied" || puts.Load() != 1 || gets.Load() != reads {
				t.Fatal("settled replay repeated provider traffic")
			}
			entry, err := store.GetAuditEventByKey(ctx, "merge-parity", "one-merge")
			if err != nil || entry == nil || entry.Status != "succeeded" || entry.RequestMetadata["merge_preimage_head_sha_hash"] == "" {
				t.Fatal("durable merge fence missing")
			}
		})
	}
}
