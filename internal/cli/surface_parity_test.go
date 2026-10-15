package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/mcp"
	"gitcode-mcp/internal/service"
)

func parityCache(t *testing.T) *service.Service {
	t.Helper()
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture", Owner: "owner", Name: "repo", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 1; i <= 24; i++ {
		provenance := cache.ProvenanceLive
		if i%2 == 0 {
			provenance = cache.ProvenanceFixture
		}
		id := fmt.Sprintf("ISSUE-%02d", i)
		kind := "issue"
		if i == 24 {
			kind = "task"
		}
		if err := store.UpsertSource(ctx, cache.Source{RepoID: "fixture", ID: id, Kind: kind, Path: fmt.Sprintf("issues/%d.md", i), Title: "Parity fixture", Body: "Parity evidence", Status: "open", ContentHash: id, Provenance: provenance, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	return service.New(store)
}

func parityCLI(t *testing.T, svc queryService, args []string) (json.RawMessage, int) {
	t.Helper()
	dir := t.TempDir()
	src := &repoInitLocalSource{env: map[string]string{}, cwd: dir, homeDir: dir, configDir: filepath.Join(dir, "config"), cacheDir: filepath.Join(dir, "cache")}
	factory := func(context.Context, string) (queryService, func() error, error) { return svc, nil, nil }
	var out, stderr bytes.Buffer
	args = append(args, "--repo", "fixture", "--format", "json", "--offline")
	code := executeWithFactoryAndDeps(args, &out, &stderr, factory, localCommandDeps{Source: src})
	if code != 0 && out.Len() == 0 {
		return append(json.RawMessage(nil), stderr.Bytes()...), code
	}
	return append(json.RawMessage(nil), out.Bytes()...), code
}

func parityMCP(t *testing.T, svc *service.Service, tool string, args map[string]any) (json.RawMessage, string) {
	return parityMCPCall(t, tool, args, func(in io.Reader, out io.Writer) *mcp.Server { return mcp.New(in, out, io.Discard, svc, nil) })
}

func parityMCPCall(t *testing.T, tool string, args map[string]any, create func(io.Reader, io.Writer) *mcp.Server) (json.RawMessage, string) {
	t.Helper()
	args["repo_id"] = "fixture"
	frame, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	srv := create(strings.NewReader(string(frame)+"\n"), &out)
	if err := srv.Serve(); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Result struct {
			StructuredContent json.RawMessage `json:"structuredContent"`
		} `json:"result"`
		Error *struct {
			Data struct {
				Code string `json:"code"`
			} `json:"data"`
		} `json:"error"`
	}
	if json.Unmarshal(out.Bytes(), &response) != nil {
		t.Fatal("invalid MCP parity response")
	}
	if response.Error != nil {
		return nil, response.Error.Data.Code
	}
	return response.Result.StructuredContent, ""
}

func TestCachedReadBehavioralParity(t *testing.T) {
	for _, tc := range []struct {
		name, tool string
		cli        []string
		mcp        map[string]any
		want       int
		provenance string
	}{
		{"list default", "list_sources", []string{"list"}, map[string]any{}, 20, ""},
		{"list provenance", "list_sources", []string{"list", "--provenance", "live"}, map[string]any{"provenance": "live"}, 12, "live"},
		{"search provenance", "search_sources", []string{"search", "Parity", "--mode", "full_text", "--provenance", "live"}, map[string]any{"query": "Parity", "mode": "full_text", "provenance": "live"}, 12, "live"},
		{"list explicit page", "list_sources", []string{"list", "--limit", "3", "--offset", "21"}, map[string]any{"limit": 3, "offset": 21}, 3, ""},
		{"list max", "list_sources", []string{"list", "--limit", "100"}, map[string]any{"limit": 100}, 24, ""},
		{"list legacy kind", "list_sources", []string{"list", "--kind", "task"}, map[string]any{"kind": "task"}, 1, "fixture"},
		{"search legacy kind", "search_sources", []string{"search", "Parity", "--kind", "task", "--mode", "full_text"}, map[string]any{"query": "Parity", "kind": "task", "mode": "full_text"}, 1, "fixture"},
		{"search alias default hybrid", "search_sources", []string{"search_sources", "Parity"}, map[string]any{"query": "Parity"}, 20, ""},
		{"search paged filter", "search_sources", []string{"search", "Parity", "--mode", "full-text", "--provenance", "fixture", "--limit", "2", "--offset", "3"}, map[string]any{"query": "Parity", "mode": "full_text", "provenance": "fixture", "limit": 2, "offset": 3}, 2, "fixture"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := parityCache(t)
			cli, code := parityCLI(t, svc, tc.cli)
			remote, failure := parityMCP(t, svc, tc.tool, tc.mcp)
			if code != 0 || failure != "" {
				t.Fatalf("parity call failed: CLI=%d MCP=%s", code, failure)
			}
			var cliResult, mcpResult any
			if json.Unmarshal(cli, &cliResult) != nil || json.Unmarshal(remote, &mcpResult) != nil || !reflect.DeepEqual(cliResult, mcpResult) {
				t.Fatal("complete structured service result diverged")
			}
			var a, b struct {
				Results []struct{ ID, Provenance string }
			}
			if json.Unmarshal(cli, &a) != nil || json.Unmarshal(remote, &b) != nil {
				t.Fatal("invalid parity result")
			}
			if len(a.Results) != tc.want || len(b.Results) != tc.want {
				t.Fatalf("result count: CLI=%d MCP=%d want=%d", len(a.Results), len(b.Results), tc.want)
			}
			for i := range a.Results {
				if a.Results[i] != b.Results[i] || tc.provenance != "" && a.Results[i].Provenance != tc.provenance {
					t.Fatal("source identity/provenance diverged")
				}
			}
		})
	}
}

func TestCachedReadErrorParity(t *testing.T) {
	for _, tc := range []struct {
		name, tool, want string
		cli              []string
		args             map[string]any
	}{
		{"list negative", "list_sources", "invalid_query", []string{"list", "--limit", "-1"}, map[string]any{"limit": -1}},
		{"list zero", "list_sources", "invalid_query", []string{"list", "--limit", "0"}, map[string]any{"limit": 0}},
		{"list excessive", "list_sources", "invalid_query", []string{"list", "--limit", "101"}, map[string]any{"limit": 101}},
		{"list offset", "list_sources", "invalid_query", []string{"list", "--offset", "-1"}, map[string]any{"offset": -1}},
		{"search excessive", "search_sources", "invalid_query", []string{"search", "Parity", "--limit", "101"}, map[string]any{"query": "Parity", "limit": 101}},
		{"search zero", "search_sources", "invalid_query", []string{"search", "Parity", "--limit", "0"}, map[string]any{"query": "Parity", "limit": 0}},
		{"search mode", "search_sources", "invalid_query", []string{"search", "Parity", "--mode", "unsupported"}, map[string]any{"query": "Parity", "mode": "unsupported"}},
		{"search empty", "search_sources", "invalid_query", []string{"search", " "}, map[string]any{"query": " "}},
		{"list missing kind", "list_sources", "cache_empty", []string{"list", "--kind", "unknown"}, map[string]any{"kind": "unknown"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := parityCache(t)
			raw, exit := parityCLI(t, svc, tc.cli)
			_, code := parityMCP(t, svc, tc.tool, tc.args)
			var envelope struct {
				Code string `json:"failure_class"`
			}
			if json.Unmarshal(raw, &envelope) != nil || exit == 0 || envelope.Code != tc.want || code != tc.want {
				t.Fatalf("typed errors differ: CLI exit=%d code=%s MCP=%s want=%s", exit, envelope.Code, code, tc.want)
			}
		})
	}
}

// Only the dispatch boundary is exercised here: no provider write/credential
// startup is run. Real offline CLI factory and MCP stdio are tested above, and
// write lifecycle/readback/replay is covered by the adapter/service fixtures.
type parityWriteSpy struct {
	*service.Service
	requests []service.WriteCommandRequest
}

type paritySyncSpy struct {
	*service.Service
	requests []service.BulkSyncRequest
}

func (s *paritySyncSpy) ProviderMode() gitcode.ProviderMode { return gitcode.ProviderModeLive }
func (s *paritySyncSpy) BulkSyncIssues(_ context.Context, req service.BulkSyncRequest) (*service.SyncResourcesResult, error) {
	s.requests = append(s.requests, req)
	return &service.SyncResourcesResult{}, nil
}

func TestBoundedSyncDelegationParity(t *testing.T) {
	for _, tc := range []struct{ pages, records, perPage int }{{1, 5, 25}, {2, 7, 100}} {
		spy := &paritySyncSpy{Service: parityCache(t)}
		_, exit := parityCLI(t, spy, []string{"sync", "--issues", "--max-pages", fmt.Sprint(tc.pages), "--max-records", fmt.Sprint(tc.records), "--per-page", fmt.Sprint(tc.perPage), "--progress", "off"})
		_, failure := parityMCPCall(t, "sync_live", map[string]any{"issues": true, "max_pages": tc.pages, "max_records": tc.records, "per_page": tc.perPage}, func(in io.Reader, out io.Writer) *mcp.Server {
			return mcp.NewWithToolAccess(in, out, io.Discard, spy, nil, mcp.ToolAccessWrite)
		})
		if exit != 0 || failure != "" || len(spy.requests) != 2 {
			t.Fatal("bounded sync dispatch failed")
		}
		for _, req := range spy.requests {
			if req.RepoID != "fixture" || req.PerPage != tc.perPage || req.Bounds == nil || req.Bounds.MaxPages != tc.pages || req.Bounds.MaxRecords != tc.records {
				t.Fatal("bounded sync semantics diverged")
			}
		}
	}
}

func TestSyncDefaultDelegationParity(t *testing.T) {
	spy := &paritySyncSpy{Service: parityCache(t)}
	_, exit := parityCLI(t, spy, []string{"sync", "--issues", "--progress", "off"})
	_, failure := parityMCPCall(t, "sync_live", map[string]any{"issues": true}, func(in io.Reader, out io.Writer) *mcp.Server {
		return mcp.NewWithToolAccess(in, out, io.Discard, spy, nil, mcp.ToolAccessWrite)
	})
	if exit != 0 || failure != "" || len(spy.requests) != 2 {
		t.Fatal("sync default triage failed to delegate")
	}
	for _, req := range spy.requests {
		if req.PerPage != 100 || req.Bounds == nil || req.Bounds.MaxPages != 1 || req.Bounds.MaxRecords != 100 || req.Bounds.MaxListPages != 1 {
			t.Fatal("foreground sync omission must delegate the same finite collection slice")
		}
	}
}

func TestListDefaultDoesNotTruncateAggregateSyncStatus(t *testing.T) {
	svc := parityCache(t)
	listed, err := svc.ListSources(context.Background(), service.ListSourcesRequest{RepoID: "fixture"})
	if err != nil || len(listed.Results) != 20 || listed.Limit != 20 {
		t.Fatal("public list default differs")
	}
	status, err := svc.SyncStatus(context.Background(), service.ListSourcesRequest{RepoID: "fixture"})
	if err != nil || len(status.Results)+len(status.Warnings) != 24 || status.Limit != 0 {
		t.Fatalf("aggregate coverage was truncated by public query pagination: results=%d warnings=%d limit=%d error=%v", len(status.Results), len(status.Warnings), status.Limit, err)
	}
}

func (s *parityWriteSpy) UpdateIssue(_ context.Context, req service.WriteCommandRequest) (service.WriteCommandResult, error) {
	s.requests = append(s.requests, req)
	return service.WriteCommandResult{Command: "update-issue", Status: "succeeded"}, nil
}

func TestIssueWriteOmissionClearDelegationParity(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
		args  map[string]any
	}{
		{"omitted", nil, map[string]any{}},
		{"set", []string{"--milestone", "MILESTONE-1"}, map[string]any{"milestone": "MILESTONE-1"}},
		{"clear", []string{"--clear-milestone"}, map[string]any{"clear_milestone": true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := &parityWriteSpy{Service: parityCache(t)}
			opts, rest, err := parseOptions("update-issue", append([]string{"--repo", "fixture", "--number", "7", "--live", "--idempotency-key", "parity-write", "--format", "json"}, tc.flags...))
			if err != nil {
				t.Fatal(err)
			}
			var out, stderr bytes.Buffer
			if code := dispatch(context.Background(), spy, "update-issue", rest, opts, &out, &stderr, startupPlan{}, localCommandDeps{}); code != 0 {
				t.Fatalf("CLI dispatch=%d", code)
			}
			tc.args["number"], tc.args["write_mode"], tc.args["idempotency_key"] = 7, "live", "parity-write"
			_, failure := parityMCPCall(t, "update_issue", tc.args, func(in io.Reader, out io.Writer) *mcp.Server {
				return mcp.NewWithToolAccess(in, out, io.Discard, spy, nil, mcp.ToolAccessWrite)
			})
			if failure != "" || len(spy.requests) != 2 {
				t.Fatal("write delegation failed")
			}
			// Labels nil/empty both mean omitted in this request contract (and
			// have the same omitempty JSON). Never normalize clear_milestone.
			a, _ := json.Marshal(spy.requests[0])
			b, _ := json.Marshal(spy.requests[1])
			if !bytes.Equal(a, b) {
				t.Fatal("write omission/clear/delegation semantics diverged")
			}
		})
	}
}
