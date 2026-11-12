package repositorydocs

import (
	"context"
	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/rag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBADSizeParser(t *testing.T) {
	x, e := parseTreeRecord([]byte("100644 blob 843b7360f9eb7c2edacd872c815af85595eb7b90 BAD\tdocs/missing.md"))
	if e != nil || x.Size != -1 {
		t.Fatalf("%+v %v", x, e)
	}
	if _, e := parseTreeRecord([]byte("100644 blob 843b7360f9eb7c2edacd872c815af85595eb7b90 garbage\tdocs/missing.md")); e == nil {
		t.Fatal("invalid size accepted")
	}
}
func TestMissingBlobCoverage(t *testing.T) {
	for _, included := range []bool{false, true} {
		t.Run(map[bool]string{false: "outside-policy", true: "included"}[included], func(t *testing.T) {
			ctx := context.Background()
			root := initTestRepository(t)
			path := "src/lost.c"
			if included {
				path = "docs/lost.md"
			}
			writeTestFile(t, root, "docs/good.md", "# good\n\nlocal documentation\n")
			writeTestFile(t, root, path, "unique missing blob\n")
			rev := commitTestRepository(t, root, "fixture")
			oid := strings.TrimSpace(runGit(t, root, "rev-parse", rev+":"+path))
			if e := os.Remove(filepath.Join(root, ".git", "objects", oid[:2], oid[2:])); e != nil {
				t.Fatal(e)
			}
			repo, e := OpenRepository(ctx, root)
			if e != nil {
				t.Fatal(e)
			}
			plan, e := InspectPlan(ctx, repo, PlanRequest{RepoID: "owner/repo", Revision: rev})
			if e != nil {
				t.Fatal(e)
			}
			want := 0
			if included {
				want = 1
			}
			if plan.MissingObjects != want || plan.EligibleFiles != 1 {
				t.Fatalf("plan %+v", plan)
			}
			store, e := cache.NewSQLiteStore(ctx, ":memory:")
			if e != nil {
				t.Fatal(e)
			}
			defer store.Close()
			e = store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}})
			if e != nil {
				t.Fatal(e)
			}
			provider, e := rag.NewFakeProvider(rag.EmbeddingProviderProfile{ProfileID: "repo-docs", ProviderID: "fake", ProviderType: "fake", Model: "fake-embedding", Dimensions: 8, BatchSize: 2, Timeout: time.Second})
			if e != nil {
				t.Fatal(e)
			}
			result, e := NewIndexer(store, provider).Run(ctx, IndexRequest{RepoID: "owner/repo", Repository: repo, Revision: rev})
			if e != nil {
				t.Fatal(e)
			}
			if result.MissingObjects != want {
				t.Fatalf("result %+v", result)
			}
			if included && result.State != cache.RepoDocSetPartial {
				t.Fatalf("missing document published ready: %+v", result)
			}
			if !included && result.State != cache.RepoDocSetReady {
				t.Fatalf("unrelated missing blob blocks ready: %+v", result)
			}
			hit, e := NewRetriever(store, provider).Search(ctx, SearchRequest{RepoID: "owner/repo", Repository: repo, Revision: rev, Query: "documentation", Mode: SearchModeHybrid, Limit: 5})
			if e != nil {
				t.Fatal(e)
			}
			if included && hit.EffectiveMode == SearchModeHybrid {
				t.Fatal("partial corpus falsely hybrid")
			}
		})
	}
}

func TestMissingPolicyFailsClosed(t *testing.T) {
	ctx := context.Background()
	root := initTestRepository(t)
	writeTestFile(t, root, PolicyConfigPath, "repository_docs:\n  preset: none\n")
	rev := commitTestRepository(t, root, "missing policy fixture")
	oid := strings.TrimSpace(runGit(t, root, "rev-parse", rev+":"+PolicyConfigPath))
	if e := os.Remove(filepath.Join(root, ".git", "objects", oid[:2], oid[2:])); e != nil {
		t.Fatal(e)
	}
	repo, e := OpenRepository(ctx, root)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := InspectPlan(ctx, repo, PlanRequest{RepoID: "owner/repo", Revision: rev}); e == nil {
		t.Fatal("missing committed policy must not fall back to default")
	}
}
