package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/service"
)

func backlinksParityCache(t *testing.T, incoming int) *service.Service {
	t.Helper()
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, repo := range []string{"fixture", "other"} {
		if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: repo, Owner: "owner", Name: repo, Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
			t.Fatal(err)
		}
		count := incoming
		if repo == "other" {
			count = 4
		}
		for i := count; i >= 0; i-- {
			id := fmt.Sprintf("ISSUE-%02d", i)
			if err := store.UpsertSource(ctx, cache.Source{RepoID: repo, ID: id, Kind: "issue", Path: id + ".md", Title: repo, Provenance: cache.ProvenanceFixture}); err != nil {
				t.Fatal(err)
			}
		}
		for i := count; i > 0; i-- {
			id := fmt.Sprintf("ISSUE-%02d", i)
			if repo == "fixture" || i >= 3 {
				if err := store.UpsertLink(ctx, cache.Link{RepoID: repo, SourceID: id, TargetID: "ISSUE-00", Kind: "reference"}); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := store.UpsertIdentity(ctx, cache.Identity{RepoID: repo, SourceID: "ISSUE-00", AliasType: "remote", Alias: "target", Remote: cache.RemoteAlias{Type: "remote", ID: "target"}}); err != nil {
			t.Fatal(err)
		}
	}
	return service.New(store)
}

func TestBacklinksPaginationParity(t *testing.T) {
	svc := backlinksParityCache(t, 3)
	for _, tc := range []struct {
		name, id      string
		limit, offset int
		want          []string
	}{
		{"omitted", "ISSUE-00", 0, 0, []string{"ISSUE-01", "ISSUE-02", "ISSUE-03"}},
		{"middle", "ISSUE-00", 1, 1, []string{"ISSUE-02"}},
		{"last", "ISSUE-00", 1, 2, []string{"ISSUE-03"}},
		{"exhausted", "ISSUE-00", 1, 3, []string{}},
		{"far beyond end", "ISSUE-00", 20, int(^uint(0)>>1) - 20, []string{}},
		{"maximum", "ISSUE-00", 100, 0, []string{"ISSUE-01", "ISSUE-02", "ISSUE-03"}},
		{"alias", "remote:target", 1, 1, []string{"ISSUE-02"}},
		{"no incoming links", "ISSUE-01", 0, 0, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flags := []string{"backlinks", tc.id, "--offset", fmt.Sprint(tc.offset)}
			args := map[string]any{"id": tc.id, "offset": tc.offset}
			if tc.limit != 0 {
				flags = append(flags, "--limit", fmt.Sprint(tc.limit))
				args["limit"] = tc.limit
			}
			a, exit := parityCLI(t, svc, flags)
			b, code := parityMCP(t, svc, "source_backlinks", args)
			if exit != 0 || code != "" {
				t.Fatal("backlinks dispatch failed")
			}
			var cli, mcp service.BacklinksResult
			if json.Unmarshal(a, &cli) != nil || json.Unmarshal(b, &mcp) != nil {
				t.Fatal("invalid backlinks result")
			}
			direct, err := svc.GetBacklinks(context.Background(), service.GetBacklinksRequest{RepoID: "fixture", ID: tc.id, Limit: tc.limit, Offset: tc.offset})
			if err != nil || !reflect.DeepEqual(cli, mcp) || !reflect.DeepEqual(cli, direct) {
				t.Fatal("service/CLI/MCP structured backlinks differ")
			}
			limit := tc.limit
			if limit == 0 {
				limit = 20
			}
			if cli.Limit != limit || cli.Offset != tc.offset || len(cli.Backlinks) != len(tc.want) {
				t.Fatal("actual backlinks page or metadata is incorrect")
			}
			for i, id := range tc.want {
				if cli.Backlinks[i].ID != id || cli.Backlinks[i].TargetID != "ISSUE-00" || cli.Backlinks[i].RepoID != "fixture" || cli.Backlinks[i].Title != "fixture" {
					t.Fatal("page ordering, stable target or repository isolation was lost")
				}
			}
		})
	}
	other, err := svc.GetBacklinks(context.Background(), service.GetBacklinksRequest{RepoID: "other", ID: "remote:target", Limit: 1, Offset: 1})
	if err != nil || len(other.Backlinks) != 1 || other.ID != "ISSUE-00" || other.Backlinks[0].ID != "ISSUE-04" || other.Backlinks[0].RepoID != "other" {
		t.Fatal("alias/offset escaped repository isolation")
	}
}

func TestBacklinksInvalidPaginationParity(t *testing.T) {
	svc := backlinksParityCache(t, 3)
	for _, tc := range []struct {
		flag, key string
		value     int
	}{
		{"--limit", "limit", 0}, {"--limit", "limit", -1}, {"--limit", "limit", 101}, {"--offset", "offset", -1}, {"--offset", "offset", int(^uint(0) >> 1)},
	} {
		a, exit := parityCLI(t, svc, []string{"backlinks", "ISSUE-00", tc.flag, fmt.Sprint(tc.value)})
		_, code := parityMCP(t, svc, "source_backlinks", map[string]any{"id": "ISSUE-00", tc.key: tc.value})
		if exit == 0 || !strings.Contains(string(a), "invalid_query") || code != "invalid_query" {
			t.Fatal("typed pagination validation differs or invalid request succeeded")
		}
		if tc.value != 0 {
			req := service.GetBacklinksRequest{RepoID: "fixture", ID: "ISSUE-00"}
			if tc.key == "limit" {
				req.Limit = tc.value
			} else {
				req.Offset = tc.value
			}
			_, err := svc.GetBacklinks(context.Background(), req)
			var invalid service.ErrInvalidQuery
			if !errors.As(err, &invalid) {
				t.Fatal("direct service accepted an invalid page")
			}
		}
	}
}

func TestBacklinksDefaultDoesNotTruncateEmbeddedSourceRecord(t *testing.T) {
	svc := backlinksParityCache(t, 25)
	page, err := svc.GetBacklinks(context.Background(), service.GetBacklinksRequest{RepoID: "fixture", AliasType: "remote", AliasID: "target"})
	if err != nil || len(page.Backlinks) != 20 || page.Limit != 20 {
		t.Fatal("public backlinks omission is not paged")
	}
	a, exit := parityCLI(t, svc, []string{"backlinks", "ISSUE-00"})
	b, code := parityMCP(t, svc, "source_backlinks", map[string]any{"id": "ISSUE-00"})
	var cli, mcp service.BacklinksResult
	if exit != 0 || code != "" || json.Unmarshal(a, &cli) != nil || json.Unmarshal(b, &mcp) != nil || !reflect.DeepEqual(cli, page) || !reflect.DeepEqual(mcp, page) {
		t.Fatal("transport omission did not return the actual 20-source page")
	}
	record, err := svc.GetSource(context.Background(), service.GetSourceRequest{RepoID: "fixture", ID: "ISSUE-00"})
	if err != nil || len(record.Backlinks) != 25 {
		t.Fatal("paged query default truncated the embedded source record")
	}
}
