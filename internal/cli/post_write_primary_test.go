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

func TestCLIImmediateCreatedPrimaryReadback(t *testing.T) {
	for _, kind := range []string{"issue", "pull_request"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			body := "## Created description\n\nExact Markdown.\n"
			api := testnet.NewCreationAPI(t, kind, "Created primary", body)
			dir := t.TempDir()
			cachePath := filepath.Join(dir, "cache.db")
			store, err := cache.NewSQLiteStore(ctx, cachePath)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "created-primary", Owner: "owner", Name: "repo", APIBaseURL: api.URL, Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
				t.Fatal(err)
			}
			src := &repoInitLocalSource{env: map[string]string{"GITCODE_TOKEN": "offline-test-token"}, cwd: dir, homeDir: dir, configDir: filepath.Join(dir, "config"), cacheDir: filepath.Join(dir, "cache")}
			command := "create-issue"
			if kind == "pull_request" {
				command = "create-pr"
			}
			args := []string{command, "--cache-path", cachePath, "--repo", "created-primary", "--title", "Created primary", "--body", body, "--live", "--idempotency-key", "created-primary", "--format", "json"}
			if kind == "pull_request" {
				args = append(args, "--head", "topic", "--base", "main")
			}
			var out, stderr bytes.Buffer
			if code := ExecuteWithSourceContext(ctx, args, &out, &stderr, src); code != 0 {
				t.Fatalf("create code=%d stderr=%s", code, stderr.String())
			}
			var created service.WriteCommandResult
			if err := json.Unmarshal(out.Bytes(), &created); err != nil {
				t.Fatal(err)
			}
			out.Reset()
			stderr.Reset()
			// Cache-only get must remain usable without the write credential.
			delete(src.env, "GITCODE_TOKEN")
			if code := ExecuteWithSourceContext(ctx, []string{"get", created.ID, "--cache-path", cachePath, "--repo", "created-primary", "--format", "json"}, &out, &stderr, src); code != 0 {
				t.Fatalf("get code=%d stderr=%s", code, stderr.String())
			}
			var cached service.SourceRecord
			if err := json.Unmarshal(out.Bytes(), &cached); err != nil {
				t.Fatal(err)
			}
			if cached.Body != body || cached.Status != "open" || cached.Provenance != "live" || api.Posts.Load() != 1 {
				t.Fatalf("cache body_match=%v state=%s provenance=%s posts=%d", cached.Body == body, cached.Status, cached.Provenance, api.Posts.Load())
			}
			wantReads := int32(0)
			if kind == "pull_request" {
				wantReads = 1
			}
			if api.Reads.Load() != wantReads {
				t.Fatalf("unexpected read traffic=%d", api.Reads.Load())
			}
		})
	}
}
