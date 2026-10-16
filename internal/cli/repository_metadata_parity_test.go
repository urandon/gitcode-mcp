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

func TestIssue198MetadataProviderPrivateParity(t *testing.T) {
	for _, visibility := range []string{"true", "false", "null", `"unsupported"`, "0", "{}", "omitted"} {
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
			hasPrivate := visibility == "true" || visibility == "false"
			if !hasPrivate && visibility != "omitted" && visibility != "null" {
				var failure struct {
					FailureClass string `json:"failure_class"`
				}
				if exit == 0 || json.Unmarshal(cliResult, &failure) != nil || failure.FailureClass != "schema_decode" || code != "schema_decode" {
					t.Fatalf("invalid provider boolean did not fail closed: CLI=%d MCP=%s", exit, code)
				}
				if requests.Load() != 2 {
					t.Fatal("invalid metadata caused extra provider operations")
				}
				return
			}
			if exit != 0 || code != "" {
				t.Fatal("valid provider boolean blocked metadata read")
			}
			for _, data := range []json.RawMessage{cliResult, mcpResult} {
				var result map[string]any
				if json.Unmarshal(data, &result) != nil {
					t.Fatal("invalid transport metadata")
				}
				if result["repo_id"] != "fixture" || result["provider_id"] != "17" || result["full_name"] != "owner/repo" || result["default_branch"] != "main" {
					t.Fatal("canonical identity/default-branch changed")
				}
				if hasPrivate {
					if value, ok := result["private"].(bool); !ok || value != (visibility == "true") {
						t.Fatal("transport did not preserve provider private boolean")
					}
				} else if _, exists := result["private"]; exists {
					t.Fatal("transport invented a missing provider private value")
				}
				if !strings.Contains(fmt.Sprint(result["evidence"]), "provider-reported private") || !strings.Contains(fmt.Sprint(result["evidence"]), "not independent accessibility or authorization evidence") {
					t.Fatal("provider observation lacks its evidence boundary")
				}
				for _, key := range []string{"public", "visibility", "visibility_level"} {
					if _, exists := result[key]; exists {
						t.Fatal("transport invented a visibility projection")
					}
				}
			}
			text, exit := parityCLIFormat(t, svc, []string{"repo-metadata"}, "text")
			if exit != 0 || !strings.Contains(string(text), "provider_id=17") || !strings.Contains(string(text), "default_branch=main") {
				t.Fatal("CLI text metadata lost provider private or identity")
			}
			if hasPrivate && !strings.Contains(string(text), "private="+visibility) || !hasPrivate && strings.Contains(string(text), "private=") {
				t.Fatal("CLI text did not preserve optional private semantics")
			}
			if requests.Load() != 3 {
				t.Fatal("metadata read did not remain one explicit GET per transport call")
			}
		})
	}
}
