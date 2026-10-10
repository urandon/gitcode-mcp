package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/capability"
	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/service"
)

type bootstrapSpyService struct {
	serviceInterface
	writes, reads int
	request       service.WriteCommandRequest
}

func (s *bootstrapSpyService) GetRepositoryMetadata(context.Context, string) (service.RepositoryMetadataResult, error) {
	s.reads++
	return service.RepositoryMetadataResult{}, nil
}
func (s *bootstrapSpyService) ListRepositoryLabels(context.Context, string) (service.RepositoryLabelsResult, error) {
	s.reads++
	return service.RepositoryLabelsResult{}, nil
}
func (s *bootstrapSpyService) CreateRepositoryLabel(_ context.Context, r service.WriteCommandRequest) (service.WriteCommandResult, error) {
	s.writes++
	s.request = r
	return service.WriteCommandResult{Status: "succeeded"}, nil
}
func labelArgs() map[string]any {
	return map[string]any{"repo_id": "bootstrap", "write_mode": "live", "name": "state:ready", "color": "#AABBCC", "idempotency_key": "one-label"}
}
func TestIssue157MCPValidationAndReadPolicy(t *testing.T) {
	for _, tc := range []struct {
		field string
		value any
	}{{"repo_id", ""}, {"write_mode", "dry_run"}, {"name", " "}, {"name", " edge"}, {"color", "red"}, {"idempotency_key", ""}, {"description", "not supported"}, {"number", 42}, {"url", "https://example.invalid"}} {
		spy := &bootstrapSpyService{}
		args := labelArgs()
		args[tc.field] = tc.value
		resp := mergeRPC(t, NewRPCHandlerWithToolAccess(spy, ToolAccessWrite), "create_repo_label", args)
		if resp.Error == nil || resp.Error.Code != -32602 || spy.writes != 0 {
			t.Fatal("invalid bootstrap reached service")
		}
	}
	for _, access := range []ToolAccess{ToolAccessRead, ToolAccessWrite} {
		spy := &bootstrapSpyService{}
		h := NewRPCHandlerWithToolAccess(spy, access)
		for _, tool := range []string{"get_repo_metadata", "list_repo_labels"} {
			resp := mergeRPC(t, h, tool, map[string]any{"repo_id": "bootstrap"})
			if resp.Error != nil {
				t.Fatal("explicit live read unavailable under read policy")
			}
			cap, ok := capability.LookupByMCPName(tool)
			if !ok || cap.Safety != capability.SafetyReadOnly {
				t.Fatal("read capability missing")
			}
		}
		resp := mergeRPC(t, h, "create_repo_label", labelArgs())
		if access == ToolAccessRead {
			if resp.Error == nil || resp.Error.Data.Code != "tool_disabled_by_policy" || spy.writes != 0 {
				t.Fatal("policy failed before handler")
			}
		} else if resp.Error != nil || spy.request.Label != "state:ready" || spy.request.Color != "#AABBCC" || spy.request.IdempotencyKey != "one-label" {
			t.Fatal("valid mapping failed")
		}
	}
	schema := writeToolInputSchema("create_repo_label")
	for _, field := range []string{"repo_id", "write_mode", "name", "color", "idempotency_key"} {
		if !containsString(schema.Required, field) {
			t.Fatal("required bootstrap schema missing")
		}
	}
}
func TestIntegrationIssue157MCPBootstrapLifecycle(t *testing.T) {
	for _, scenario := range []string{"confirmed", "ambiguous", "forbidden"} {
		t.Run(scenario, func(t *testing.T) {
			var posts atomic.Int32
			var created atomic.Bool
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/v5/repos/owner/repo" {
					fmt.Fprint(w, `{"id":17,"owner":{"login":"owner"},"name":"repo","full_name":"owner/repo","private":true,"default_branch":"main"}`)
					return
				}
				if r.URL.Path != "/api/v5/repos/owner/repo/labels" {
					t.Error("unexpected provider operation (no issue assignments permitted)")
					w.WriteHeader(404)
					return
				}
				if r.Method == "POST" {
					posts.Add(1)
					_ = r.ParseForm()
					if len(r.PostForm) != 2 || r.PostForm.Get("name") != "state:ready" || r.PostForm.Get("color") != "#aabbcc" {
						t.Error("incorrect standalone label form")
					}
					if scenario == "forbidden" {
						w.WriteHeader(403)
						fmt.Fprint(w, `{"message":"fixture-provider-body-sentinel"}`)
						return
					}
					created.Store(true)
					if scenario == "ambiguous" {
						w.WriteHeader(502)
						return
					}
					fmt.Fprint(w, `{"id":7,"name":"state:ready","color":"#aabbcc","repository_id":17}`)
					return
				}
				if created.Load() && r.URL.Query().Get("page") == "1" {
					fmt.Fprint(w, `[{"id":7,"name":"state:ready","color":"#aabbcc","repository_id":17,"description":""}]`)
				} else {
					fmt.Fprint(w, `[]`)
				}
			}))
			defer api.Close()
			ctx := context.Background()
			store, err := cache.NewInMemorySQLiteStore(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "bootstrap", Owner: "owner", Name: "repo", APIBaseURL: api.URL, Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
				t.Fatal(err)
			}
			svc, err := service.NewWithMode(store, gitcode.ProviderModeLive, "offline-test-token", service.ServiceConfig{BaseURL: api.URL, MaxRetries: 3})
			if err != nil {
				t.Fatal(err)
			}
			h := NewRPCHandlerWithToolAccess(svc, ToolAccessWrite)
			metadata := mergeRPC(t, h, "get_repo_metadata", map[string]any{"repo_id": "bootstrap"})
			if metadata.Error != nil {
				t.Fatal("metadata unavailable")
			}
			var m toolCallResult
			if err := json.Unmarshal(metadata.Result, &m); err != nil {
				t.Fatal(err)
			}
			resp := mergeRPC(t, h, "create_repo_label", labelArgs())
			if scenario != "confirmed" {
				if resp.Error == nil || resp.Error.Code != -32000 || strings.Contains(fmt.Sprint(resp.Error), "fixture-provider-body-sentinel") {
					t.Fatal("provider failure not safely typed")
				}
			}
			if scenario == "forbidden" {
				_ = mergeRPC(t, h, "create_repo_label", labelArgs())
				if posts.Load() != 1 {
					t.Fatal("forbidden request repeated POST")
				}
				return
			}
			if scenario == "ambiguous" {
				resp = mergeRPC(t, h, "create_repo_label", labelArgs())
			}
			if resp.Error != nil {
				t.Fatal("canonical label not recovered")
			}
			cached, err := svc.GetSource(ctx, service.GetSourceRequest{RepoID: "bootstrap", ID: "LABEL-7"})
			if err != nil || cached.Kind != "label" || cached.Provenance != "live" {
				t.Fatal("cache source missing")
			}
			replayed := mergeRPC(t, h, "create_repo_label", labelArgs())
			if replayed.Error != nil || posts.Load() != 1 {
				t.Fatal("settled MCP replay duplicated POST")
			}
		})
	}
}
