package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
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

func TestCLIImmediateLivePRCommentReadback(t *testing.T) {
	ctx := context.Background()
	body := "## Comment\n\nExact Markdown.\n"
	api := testnet.NewPRCommentAPI(t, body)
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "comment-origin", Owner: "owner", Name: "repo", APIBaseURL: api.URL, Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	testnet.SeedPRCommentParent(t, store, "comment-origin")
	src := &repoInitLocalSource{env: map[string]string{"GITCODE_TOKEN": "offline-test-token"}, cwd: dir, homeDir: dir, configDir: filepath.Join(dir, "config"), cacheDir: filepath.Join(dir, "cache")}
	args := []string{"add-pr-comment", "--cache-path", cachePath, "--repo", "comment-origin", "--number", "7", "--body", body, "--idempotency-key", "one-comment", "--format", "json"}
	var out, stderr bytes.Buffer
	for attempt := 0; attempt < 2; attempt++ {
		out.Reset()
		stderr.Reset()
		if code := ExecuteWithSourceContext(ctx, args, &out, &stderr, src); code != 0 {
			t.Fatalf("write code=%d error=%s", code, stderr.String())
		}
		var result service.WriteCommandResult
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if attempt == 1 && result.Status != "already_applied" {
			t.Fatalf("replay status=%s", result.Status)
		}
	}
	delete(src.env, "GITCODE_TOKEN")
	out.Reset()
	stderr.Reset()
	if code := ExecuteWithSourceContext(ctx, []string{"get", "PRCOMMENT-7-301", "--cache-path", cachePath, "--repo", "comment-origin", "--format", "json"}, &out, &stderr, src); code != 0 {
		t.Fatalf("read code=%d error=%s", code, stderr.String())
	}
	var cached service.SourceRecord
	if err := json.Unmarshal(out.Bytes(), &cached); err != nil {
		t.Fatal(err)
	}
	if cached.Provenance != "live" || cached.Body != body || api.Posts.Load() != 1 {
		t.Fatalf("origin=%s body_match=%v posts=%d", cached.Provenance, cached.Body == body, api.Posts.Load())
	}
}

func TestCLIExhaustedTargetedWriterWaitIsActionable(t *testing.T) {
	for _, format := range []string{"json", "text"} {
		var out bytes.Buffer
		code := writeCommandError(&out, format, startupPlan{ProviderMode: "live-http"}, cache.ErrLockContention{Operation: "sync", RepoID: "other", Path: "/private/cache.lock", WaitExhausted: true})
		if code != 1 || !strings.Contains(out.String(), "bounded writer wait exhausted") || !strings.Contains(out.String(), "not the preceding comment write") || strings.Contains(out.String(), "/private/") {
			t.Fatalf("wait diagnostic code=%d output=%s", code, out.String())
		}
		if format == "json" {
			var payload map[string]any
			if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if payload["writer_wait_exhausted"] != true || payload["failure_class"] != "cache_busy" || payload["http_attempted"] != false {
				t.Fatalf("diagnostic=%+v", payload)
			}
		}
	}
}
