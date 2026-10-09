package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/service"
	"gitcode-mcp/internal/testnet"
)

func TestCLIExactLiveIssue42(t *testing.T) {
	ctx := context.Background()
	api := testnet.NewExactIssueAPI(t, 420042, 42)
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "provider-issue", Owner: "owner", Name: "repo", APIBaseURL: api.URL, Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	src := &repoInitLocalSource{env: map[string]string{"GITCODE_TOKEN": "offline-test-token"}, cwd: dir, homeDir: dir, configDir: filepath.Join(dir, "config"), cacheDir: filepath.Join(dir, "cache")}
	var out, stderr bytes.Buffer
	if code := ExecuteWithSourceContext(ctx, []string{"sync", "--cache-path", cachePath, "--repo", "provider-issue", "--issues", "--input", "issue:42", "--details", "--format", "json"}, &out, &stderr, src); code != 0 {
		t.Fatalf("sync code=%d stderr=%s", code, stderr.String())
	}
	var result service.SyncResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Record.ID != "ISSUE-42" || api.Details.Load() != 1 || api.Comments.Load() != 1 {
		t.Fatalf("exact CLI refresh=%+v", result)
	}
	cached, err := service.New(store).GetSource(ctx, service.GetSourceRequest{RepoID: "provider-issue", ID: "ISSUE-42"})
	if err != nil || cached.IssueNumber != 42 || cached.Provenance != "live" {
		t.Fatalf("canonical readback=%+v err=%v", cached, err)
	}
}
