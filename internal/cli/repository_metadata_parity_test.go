package cli

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
	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/service"
)

func TestIssue198MetadataTransportFieldWithdrawal(t *testing.T) {
	for _, visibility := range []string{"true", "false", "null", `"unsupported"`, "omitted"} {
		t.Run(visibility, func(t *testing.T) {
			var requests atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != "GET" || r.URL.Path != "/api/v5/repos/owner/repo" {
					t.Error("metadata reached an unexpected provider operation")
				}
				field := ""
				if visibility != "omitted" {
					field = `,"private":` + visibility
				}
				fmt.Fprintf(w, `{"id":17,"owner":{"login":"owner"},"name":"repo","full_name":"owner/repo","default_branch":"main"%s}`, field)
			}))
			defer api.Close()
			store, err := cache.NewInMemorySQLiteStore(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := store.AddRepository(context.Background(), cache.RepositoryBinding{RepoID: "fixture", Owner: "owner", Name: "repo", APIBaseURL: api.URL, Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
				t.Fatal("binding setup failed")
			}
			svc, err := service.NewWithMode(store, gitcode.ProviderModeLive, "fixture-token", service.ServiceConfig{BaseURL: api.URL})
			if err != nil {
				t.Fatal("live adapter fixture setup failed")
			}
			cliResult, exit := parityCLI(t, svc, []string{"repo-metadata"})
			mcpResult, code := parityMCP(t, svc, "get_repo_metadata", map[string]any{})
			if exit != 0 || code != "" {
				t.Fatal("withdrawn visibility blocked identity read")
			}
			for _, data := range []json.RawMessage{cliResult, mcpResult} {
				var result map[string]any
				if json.Unmarshal(data, &result) != nil {
					t.Fatal("invalid transport metadata")
				}
				if result["repo_id"] != "fixture" || result["provider_id"] != "17" || result["full_name"] != "owner/repo" || result["default_branch"] != "main" {
					t.Fatal("canonical identity/default-branch changed")
				}
				for _, key := range []string{"private", "public", "visibility", "visibility_level"} {
					if _, exists := result[key]; exists {
						t.Fatal("transport exposed withdrawn visibility")
					}
				}
			}
			text, exit := parityCLIFormat(t, svc, []string{"repo-metadata"}, "text")
			if exit != 0 || strings.Contains(string(text), "private=") || !strings.Contains(string(text), "provider_id=17 default_branch=main") {
				t.Fatal("CLI text metadata retained visibility or lost identity")
			}
			if requests.Load() != 3 {
				t.Fatal("metadata read did not remain one explicit GET per transport call")
			}
		})
	}
}
