package cli

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/service"
)

func TestMetadataCLIAndMCPParity(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture", Owner: "owner", Name: "repo", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"ISSUE-1", "ISSUE-2", "ISSUE-3"} {
		status := "open"
		if i == 0 {
			status = "closed"
		}
		if err := store.UpsertSource(ctx, cache.Source{RepoID: "fixture", ID: id, Kind: "issue", Path: id + ".md", Title: "Bootstrap 中文%_", Body: "needle", Labels: []string{"bug", "area, 中文"}, Status: status, ContentHash: id, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	svc := service.New(store)
	for _, command := range []string{"list", "search", "search_sources"} {
		t.Run(command, func(t *testing.T) {
			tool := "search_sources"
			if command == "list" {
				tool = "list_sources"
			}
			cli, code := parityCLI(t, svc, []string{command, "--kind", "issue", "--status", "open", "--filter-label", "bug", "--filter-label", "area, 中文", "--labels-match", "all", "--title-contains", "中文%_", "--limit", "1", "--offset", "1"})
			mcp, failure := parityMCP(t, svc, tool, map[string]any{"kind": "issue", "status": "open", "labels": []string{"bug", "area, 中文"}, "labels_match": "all", "title_contains": "中文%_", "limit": 1, "offset": 1})
			var a, b map[string]any
			if code != 0 || failure != "" || json.Unmarshal(cli, &a) != nil || json.Unmarshal(mcp, &b) != nil || !reflect.DeepEqual(a, b) {
				t.Fatalf("CLI/MCP drift: code=%d failure=%s cli=%s mcp=%s", code, failure, cli, mcp)
			}
			r := a["results"].([]any)
			if len(r) != 1 || r[0].(map[string]any)["id"] != "ISSUE-3" {
				t.Fatalf("wrong page: %s", cli)
			}
		})
	}
	cli, code := parityCLI(t, svc, []string{"search", "needle", "--mode", "full_text", "--status", "open", "--filter-label", "unknown"})
	if code != 0 {
		t.Fatalf("unknown label not valid empty: %s", cli)
	}
}
