package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/mcp"
	"gitcode-mcp/internal/service"
)

type coverageClient struct{ gitcode.Client }

var coverageTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func (coverageClient) ListIssues(context.Context, gitcode.IssueListRequest) (gitcode.Page[gitcode.IssueSummary], error) {
	return gitcode.Page[gitcode.IssueSummary]{Items: []gitcode.IssueSummary{{ID: "provider-1", Number: 1, Title: "Fixture issue", State: "open", CreatedAt: coverageTime, UpdatedAt: coverageTime}}}, nil
}

func (coverageClient) ListWikiPages(context.Context, gitcode.WikiListRequest) (gitcode.Page[gitcode.WikiPage], error) {
	return gitcode.Page[gitcode.WikiPage]{}, errors.New("synthetic wiki failure")
}

func (coverageClient) ListPRs(context.Context, gitcode.PRListRequest) (gitcode.Page[gitcode.PullRequest], error) {
	return gitcode.Page[gitcode.PullRequest]{Items: []gitcode.PullRequest{{ID: "provider-pr-1", Number: 1, Title: "Fixture PR", State: "open", CreatedAt: coverageTime, UpdatedAt: coverageTime}}}, nil
}

func (coverageClient) ListPRComments(context.Context, gitcode.PRRequest) (gitcode.Page[gitcode.PRComment], error) {
	items := make([]gitcode.PRComment, 101)
	for i := range items {
		items[i] = gitcode.PRComment{ID: fmt.Sprint(i + 1), Body: "Fixture comment", CreatedAt: coverageTime, UpdatedAt: coverageTime}
	}
	return gitcode.Page[gitcode.PRComment]{Items: items}, nil
}

func TestForegroundSyncIncompleteCoverageParity(t *testing.T) {
	for _, transport := range []string{"CLI", "MCP"} {
		for _, cohort := range []string{"comment preview", "failed wiki sibling"} {
			t.Run(transport+"/"+cohort, func(t *testing.T) {
				ctx := context.Background()
				store, err := cache.NewInMemorySQLiteStore(ctx)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = store.Close() })
				if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture", Owner: "owner", Name: "repo", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
					t.Fatal(err)
				}
				svc := &budgetLiveService{service.NewWithClient(store, coverageClient{})}
				comments := cohort == "comment preview"
				if comments {
					if _, err := svc.BulkSyncPullRequests(ctx, service.BulkSyncRequest{RepoID: "fixture"}); err != nil {
						t.Fatal(err)
					}
				}
				for _, expand := range []bool{false, true} {
					if expand && !comments {
						break
					}
					args := map[string]any{"issues": true, "wiki": true}
					flags := []string{"sync", "--issues", "--wiki", "--progress", "off"}
					if comments {
						args = map[string]any{"pr_comments": true}
						flags = []string{"sync", "--pr-comments", "--progress", "off"}
					}
					if expand {
						args["max_records"] = 200
						flags = append(flags, "--max-records", "200")
					}
					var raw json.RawMessage
					if transport == "CLI" {
						var exit int
						raw, exit = parityCLI(t, svc, flags)
						if (exit != 0) == comments {
							t.Fatal("unexpected CLI outcome")
						}
					} else {
						var code string
						raw, code = parityMCPCall(t, "sync_live", args, func(in io.Reader, out io.Writer) *mcp.Server {
							return mcp.NewWithToolAccess(in, out, io.Discard, svc, nil, mcp.ToolAccessWrite)
						})
						if code != "" {
							t.Fatal("MCP collection dispatch failed")
						}
					}
					var result struct {
						TraversalStatus string `json:"traversal_status"`
						StopReason      string `json:"stop_reason"`
						Continuation    string `json:"continuation"`
						SuccessCount    int    `json:"success_count"`
						FailureCount    int    `json:"failure_count"`
					}
					if json.Unmarshal(raw, &result) != nil {
						t.Fatal("invalid sync receipt")
					}
					if expand {
						if result.TraversalStatus != "complete" || result.SuccessCount != 101 || result.Continuation != "" {
							t.Fatal("explicit expansion did not recover all comments")
						}
					} else if result.TraversalStatus == "complete" || result.StopReason == "end_of_collection" || result.Continuation == "" {
						t.Fatal("unfinished collection falsely claimed complete coverage")
					}
					if comments && !expand && (result.SuccessCount != 100 || result.TraversalStatus != "bounded" || result.StopReason != "max_records") {
						t.Fatal("comment preview did not report its record bound")
					}
					if !comments && (result.SuccessCount != 1 || result.FailureCount != 1) {
						t.Fatal("sibling failure lost committed success or failure evidence")
					}
					if comments {
						sources, err := store.ListSources(ctx, cache.SourceFilter{RepoID: "fixture", Kind: "pr_comment"})
						want := 100
						if expand {
							want = 101
						}
						if err != nil || len(sources) != want {
							t.Fatal("committed comment prefix was lost")
						}
					}
				}
			})
		}
	}
}
