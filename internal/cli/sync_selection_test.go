package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/mcp"
	"gitcode-mcp/internal/service"
)

type selectionClient struct {
	coverageClient
	calls    []string
	failWiki bool
}

func (c *selectionClient) ListIssues(ctx context.Context, req gitcode.IssueListRequest) (gitcode.Page[gitcode.IssueSummary], error) {
	c.calls = append(c.calls, "issues")
	return c.coverageClient.ListIssues(ctx, req)
}
func (c *selectionClient) ListWikiPages(context.Context, gitcode.WikiListRequest) (gitcode.Page[gitcode.WikiPage], error) {
	c.calls = append(c.calls, "wiki")
	if c.failWiki {
		return gitcode.Page[gitcode.WikiPage]{}, errors.New("synthetic wiki failure")
	}
	return gitcode.Page[gitcode.WikiPage]{Items: []gitcode.WikiPage{{Slug: "guide", Title: "Fixture guide", Body: "Fixture", CreatedAt: coverageTime, UpdatedAt: coverageTime}}}, nil
}
func (c *selectionClient) GetWikiPage(context.Context, gitcode.WikiPageRequest) (gitcode.WikiPage, error) {
	c.calls = append(c.calls, "wiki_body")
	return gitcode.WikiPage{Slug: "guide", Title: "Fixture guide", Body: "Fixture", Revision: "revision-1", CreatedAt: coverageTime, UpdatedAt: coverageTime}, nil
}

func TestSyncSelectionTransportParity(t *testing.T) {
	for _, transport := range []string{"CLI", "MCP"} {
		for _, tc := range []struct {
			name                                  string
			scopes                                []cache.RepositoryScope
			explicit, wikiOnly, failWiki, invalid bool
			calls, collections                    []string
		}{
			{"issues omission", []cache.RepositoryScope{cache.RepositoryScopeIssues}, false, false, false, false, []string{"issues"}, []string{"issues"}},
			{"wiki omission", []cache.RepositoryScope{cache.RepositoryScopeWiki}, false, false, false, false, []string{"wiki", "wiki_body"}, []string{"wiki"}},
			{"both omission", []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}, false, false, false, false, []string{"issues", "wiki", "wiki_body"}, []string{"issues", "wiki"}},
			{"both explicit", []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}, true, false, false, false, []string{"issues", "wiki", "wiki_body"}, []string{"issues", "wiki"}},
			{"wiki explicit subset", []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}, true, true, false, false, []string{"wiki", "wiki_body"}, []string{"wiki"}},
			{"unavailable explicit wiki", []cache.RepositoryScope{cache.RepositoryScopeIssues}, true, false, false, true, nil, nil},
			{"unavailable explicit issues", []cache.RepositoryScope{cache.RepositoryScopeWiki}, true, false, false, true, nil, nil},
			{"empty scopes", nil, false, false, false, true, nil, nil},
			{"partial defaults", []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}, false, false, true, false, []string{"issues", "wiki"}, []string{"issues", "wiki"}},
		} {
			t.Run(transport+"/"+tc.name, func(t *testing.T) {
				store, err := cache.NewInMemorySQLiteStore(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = store.Close() })
				if store.AddRepository(context.Background(), cache.RepositoryBinding{RepoID: "fixture", Owner: "owner", Name: "repo", Scopes: tc.scopes}) != nil {
					t.Fatal("binding setup")
				}
				client := &selectionClient{failWiki: tc.failWiki}
				svc := &budgetLiveService{service.NewWithClient(store, client)}
				flags := []string{"sync", "--progress", "off"}
				args := map[string]any{}
				if tc.explicit {
					flags = append(flags, "--wiki")
					args["wiki"] = true
					if !tc.wikiOnly {
						flags = append(flags, "--issues")
						args["issues"] = true
					}
				}
				var raw json.RawMessage
				if transport == "CLI" {
					var exit int
					raw, exit = parityCLI(t, svc, flags)
					if (exit != 0) != (tc.invalid || tc.failWiki) {
						t.Fatal("unexpected CLI exit")
					}
					if tc.invalid && !strings.Contains(string(raw), "invalid_query") {
						t.Fatal("missing typed CLI scope error")
					}
				} else {
					var code string
					raw, code = parityMCPCall(t, "sync_live", args, func(in io.Reader, out io.Writer) *mcp.Server {
						return mcp.NewWithToolAccess(in, out, io.Discard, svc, nil, mcp.ToolAccessWrite)
					})
					if (tc.invalid && code != "invalid_query") || (!tc.invalid && code != "") {
						t.Fatal("unexpected MCP scope outcome")
					}
				}
				if !reflect.DeepEqual(client.calls, tc.calls) {
					t.Fatalf("selected provider work=%v want=%v", client.calls, tc.calls)
				}
				if tc.invalid {
					return
				}
				var result struct {
					Collections     []string `json:"collections"`
					TraversalStatus string   `json:"traversal_status"`
					Continuation    string   `json:"continuation"`
					SuccessCount    int      `json:"success_count"`
					FailureCount    int      `json:"failure_count"`
				}
				if json.Unmarshal(raw, &result) != nil {
					t.Fatal("invalid result")
				}
				if transport == "MCP" && !reflect.DeepEqual(result.Collections, tc.collections) {
					t.Fatal("MCP selected-collection metadata differs from actual work")
				}
				if tc.failWiki {
					if result.SuccessCount != 1 || result.FailureCount != 1 || result.TraversalStatus == "complete" || result.Continuation == "" {
						t.Fatal("partial default sync lost coverage/failure evidence")
					}
					return
				}
				if result.SuccessCount != len(tc.collections) || result.TraversalStatus != "complete" {
					t.Fatal("valid selected work did not complete")
				}
			})
		}
	}
}

func TestSyncSelectionMissingBindingHasNoProviderWork(t *testing.T) {
	for _, transport := range []string{"CLI", "MCP"} {
		t.Run(transport, func(t *testing.T) {
			store, err := cache.NewInMemorySQLiteStore(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			client := &selectionClient{}
			svc := &budgetLiveService{service.NewWithClient(store, client)}
			_, err = svc.ResolveSyncSelection(context.Background(), "fixture", service.SyncSelection{})
			var notFound service.ErrNotFound
			if !errors.As(err, &notFound) {
				t.Fatal("missing binding lost typed service error")
			}
			if transport == "CLI" {
				if _, exit := parityCLI(t, svc, []string{"sync", "--progress", "off"}); exit == 0 {
					t.Fatal("missing binding reported CLI success")
				}
			} else {
				_, code := parityMCPCall(t, "sync_live", map[string]any{}, func(in io.Reader, out io.Writer) *mcp.Server {
					return mcp.NewWithToolAccess(in, out, io.Discard, svc, nil, mcp.ToolAccessWrite)
				})
				if code == "" {
					t.Fatal("missing binding reported MCP success")
				}
			}
			if len(client.calls) != 0 {
				t.Fatal("missing binding contacted the provider")
			}
		})
	}
}
