package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/mcp"
	"gitcode-mcp/internal/service"
)

type budgetLiveService struct{ *service.Service }

func (s *budgetLiveService) ProviderMode() gitcode.ProviderMode { return gitcode.ProviderModeLive }

func TestForegroundSyncHTTPBudgetParity(t *testing.T) {
	for _, transport := range []string{"CLI", "MCP"} {
		for _, collection := range []string{"issues", "pulls"} {
			t.Run(transport+"/"+collection, func(t *testing.T) {
				ctx := context.Background()
				store, err := cache.NewInMemorySQLiteStore(ctx)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = store.Close() })
				calls := []int{}
				var callsMu sync.Mutex
				var failTail atomic.Bool
				observed := func() []int { callsMu.Lock(); defer callsMu.Unlock(); return append([]int(nil), calls...) }
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodGet || r.URL.Path != "/api/v5/repos/owner/repo/"+collection {
						t.Error("unexpected provider operation")
						w.WriteHeader(500)
						return
					}
					page, _ := strconv.Atoi(r.URL.Query().Get("page"))
					perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
					if perPage != 100 {
						t.Error("provider page size differs from shared default")
					}
					callsMu.Lock()
					calls = append(calls, page)
					callsMu.Unlock()
					if page == 2 && failTail.Load() {
						w.WriteHeader(http.StatusForbidden)
						return
					}
					items := []map[string]any{}
					for n := (page-1)*perPage + 1; n <= page*perPage && n <= 120; n++ {
						items = append(items, map[string]any{"id": fmt.Sprintf("provider-%d", n), "number": n, "title": fmt.Sprintf("Issue %d", n), "body": "bounded fixture", "state": "open", "comments": 0, "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-02T00:00:00Z", "labels": []string{}, "head": map[string]any{"ref": "topic"}, "base": map[string]any{"ref": "main"}})
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(items)
				}))
				t.Cleanup(server.Close)
				if store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture", Owner: "owner", Name: "repo", APIBaseURL: server.URL, Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}) != nil {
					t.Fatal("binding setup failed")
				}
				client, err := gitcode.NewHTTPClient(gitcode.Config{BaseURL: server.URL, Timeout: time.Second, MaxRetries: 0})
				if err != nil {
					t.Fatal("adapter setup failed")
				}
				svc := &budgetLiveService{service.NewWithClient(store, client)}
				for _, step := range []struct {
					name     string
					expand   bool
					fail     bool
					calls    int
					pages    int
					coverage string
					sources  int
				}{
					{"preview", false, false, 1, 1, "bounded", 100},
					{"cached prefix preview", false, false, 1, 1, "bounded", 100},
					{"failed expansion retains prefix", true, true, 2, 1, "partial", 100},
					{"explicit expansion", true, false, 2, 2, "complete", 120},
				} {
					failTail.Store(step.fail)
					before := len(observed())
					var raw json.RawMessage
					if transport == "CLI" {
						args := []string{"sync", "--" + collection, "--progress", "off"}
						if step.expand {
							args = append(args, "--max-pages", "2")
						}
						var exit int
						raw, exit = parityCLI(t, svc, args)
						if (exit != 0) != step.fail {
							t.Fatalf("%s CLI dispatch failed with exit%d", step.name, exit)
						}
					} else {
						args := map[string]any{collection: true}
						if step.expand {
							args["max_pages"] = 2
						}
						var code string
						raw, code = parityMCPCall(t, "sync_live", args, func(in io.Reader, out io.Writer) *mcp.Server {
							return mcp.NewWithToolAccess(in, out, io.Discard, svc, nil, mcp.ToolAccessWrite)
						})
						if code != "" {
							t.Fatalf("%s MCP dispatch failed: %s", step.name, code)
						}
					}
					var result struct {
						TraversalStatus string `json:"traversal_status"`
						StopReason      string `json:"stop_reason"`
						PagesListed     int    `json:"pages_listed"`
						RecordsListed   int    `json:"records_listed"`
						Continuation    string `json:"continuation"`
					}
					if json.Unmarshal(raw, &result) != nil {
						t.Fatal("invalid sync result")
					}
					requests := observed()
					if len(requests)-before != step.calls || result.PagesListed != step.pages || result.TraversalStatus != step.coverage {
						t.Fatalf("%s calls=%d pages=%d coverage=%s", step.name, len(requests)-before, result.PagesListed, result.TraversalStatus)
					}
					if step.coverage != "complete" && (result.Continuation == "" || result.StopReason == "end_of_collection") {
						t.Fatal("bounded preview omitted continuation or claimed complete coverage")
					}
					kind, remote := "issue", "issue"
					if collection == "pulls" {
						kind, remote = "pull_request", "pull_request"
					}
					sources, err := store.ListSources(ctx, cache.SourceFilter{RepoID: "fixture", Kind: kind})
					if err != nil || len(sources) != step.sources {
						t.Fatal("committed prefix/tail was not preserved")
					}
					frontier, found, err := store.GetSyncFrontier(ctx, "fixture", remote, "updated_at_desc", "state=all")
					if err != nil || !found || frontier.Status != step.coverage {
						t.Fatal("frontier coverage does not match proven traversal")
					}
					if !step.expand && requests[before] != 1 {
						t.Fatal("preview used an unsafe provider page cursor")
					}
				}
			})
		}
	}
}

func TestForegroundSyncInvalidBoundsParity(t *testing.T) {
	for _, tc := range []struct {
		flag, key, value string
		number           int
	}{{"--max-pages", "max_pages", "-1", -1}, {"--max-records", "max_records", "-1", -1}, {"--per-page", "per_page", "101", 101}} {
		spy := &paritySyncSpy{Service: parityCache(t)}
		raw, exit := parityCLI(t, spy, []string{"sync", "--issues", tc.flag, tc.value})
		_, code := parityMCPCall(t, "sync_live", map[string]any{"issues": true, tc.key: tc.number}, func(in io.Reader, out io.Writer) *mcp.Server {
			return mcp.NewWithToolAccess(in, out, io.Discard, spy, nil, mcp.ToolAccessWrite)
		})
		if exit == 0 || code != "invalid_query" || !strings.Contains(string(raw), "invalid_query") || len(spy.requests) != 0 {
			t.Fatal("invalid bounds reached sync or typed validation diverged")
		}
	}
}
