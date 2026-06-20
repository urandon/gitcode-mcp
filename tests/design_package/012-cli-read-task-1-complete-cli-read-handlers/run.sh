#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SCENARIO_DIR="$ROOT/tests/design_package/012-cli-read-task-1-complete-cli-read-handlers"
TEST_FILE="$SCENARIO_DIR/cli_read_product_test.go"
trap 'rm -f "$TEST_FILE"' EXIT

cat > "$TEST_FILE" <<'GOEOF'
package cli_read_product_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
)

type cliResult struct {
	code   int
	stdout string
	stderr string
}

func TestCompleteCLIReadHandlersProductScenarios(t *testing.T) {
	ctx := context.Background()
	cachePath := filepath.Join(t.TempDir(), "fixture-cache.db")
	seedCLIReadFixture(t, ctx, cachePath, true)

	t.Run("012-cli-read-task-1-complete-cli-read-handlers-scenario-1", func(t *testing.T) {
		commands := [][]string{
			{"list", "--repo", "fixture-a", "--format", "json"},
			{"search", "--repo", "fixture-a", "offline", "--format", "json"},
			{"get", "--repo", "fixture-a", "ISSUE-42", "--format", "json"},
			{"get-snippet", "--repo", "fixture-a", "ISSUE-42", "--line-start", "1", "--line-end", "3", "--format", "json"},
			{"backlinks", "--repo", "fixture-a", "WIKI-Home", "--format", "json"},
			{"recent", "--repo", "fixture-a", "--format", "json"},
			{"link-check", "--repo", "fixture-a", "--format", "json"},
			{"stale-index", "--repo", "fixture-a", "--format", "json"},
			{"cache-status", "--repo", "fixture-a", "--format", "json"},
			{"list-chunks", "--repo", "fixture-a", "--format", "json"},
			{"export", "--repo", "fixture-a", "--format", "json"},
			{"diff", "--repo", "fixture-a", "--format", "json"},
		}
		for _, args := range commands {
			first := runCLI(t, cachePath, args...)
			requireSuccess(t, args, first)
			second := runCLI(t, cachePath, args...)
			requireSuccess(t, args, second)
			if first.stdout != second.stdout {
				t.Fatalf("%v JSON output is not deterministic\nfirst=%s\nsecond=%s", args, first.stdout, second.stdout)
			}
			assertJSONContainsRepo(t, args, first.stdout, "fixture-a")
			assertNoNetworkFailure(t, first)
		}

		textCommands := [][]string{
			{"list", "--repo", "fixture-a"},
			{"search", "--repo", "fixture-a", "offline"},
			{"get", "--repo", "fixture-a", "WIKI-Home"},
			{"list-chunks", "--repo", "fixture-a"},
			{"cache-status", "--repo", "fixture-a"},
		}
		for _, args := range textCommands {
			first := runCLI(t, cachePath, args...)
			requireSuccess(t, args, first)
			second := runCLI(t, cachePath, args...)
			requireSuccess(t, args, second)
			if first.stdout != second.stdout {
				t.Fatalf("%v text output is not deterministic\nfirst=%s\nsecond=%s", args, first.stdout, second.stdout)
			}
			assertContains(t, first.stdout, "fixture-a")
		}
	})

	t.Run("012-cli-read-task-1-complete-cli-read-handlers-scenario-2", func(t *testing.T) {
		canonical := runCLI(t, cachePath, "get-snippet", "--repo", "fixture-a", "ISSUE-42", "--line-start", "1", "--line-end", "3", "--format", "json")
		requireSuccess(t, []string{"get-snippet"}, canonical)
		for _, alias := range []string{"snippet", "snippets"} {
			got := runCLI(t, cachePath, alias, "--repo", "fixture-a", "ISSUE-42", "--line-start", "1", "--line-end", "3", "--format", "json")
			requireSuccess(t, []string{alias}, got)
			if got.stdout != canonical.stdout {
				t.Fatalf("alias %s differs from get-snippet\ngot=%s\nwant=%s", alias, got.stdout, canonical.stdout)
			}
		}
	})

	t.Run("012-cli-read-task-1-complete-cli-read-handlers-scenario-3", func(t *testing.T) {
		list := runCLI(t, cachePath, "list", "--repo", "fixture-a", "--format", "json")
		requireSuccess(t, []string{"list"}, list)
		assertContainsAll(t, list.stdout, []string{"ISSUE-42", "WIKI-Home", "issue", "wiki"})

		search := runCLI(t, cachePath, "search", "--repo", "fixture-a", "offline", "--format", "json")
		requireSuccess(t, []string{"search"}, search)
		assertContainsAll(t, search.stdout, []string{"ISSUE-42", "WIKI-Home"})

		getIssue := runCLI(t, cachePath, "get", "--repo", "fixture-a", "ISSUE-42", "--format", "json")
		requireSuccess(t, []string{"get ISSUE-42"}, getIssue)
		assertContainsAll(t, getIssue.stdout, []string{"fixture-a", "ISSUE-42", "issue", "offline fixture issue"})
		getWiki := runCLI(t, cachePath, "get", "--repo", "fixture-a", "WIKI-Home", "--format", "json")
		requireSuccess(t, []string{"get WIKI-Home"}, getWiki)
		assertContainsAll(t, getWiki.stdout, []string{"fixture-a", "WIKI-Home", "wiki", "offline fixture wiki"})

		snippet := runCLI(t, cachePath, "get-snippet", "--repo", "fixture-a", "ISSUE-42", "--chunk-id", "chunk-issue-1", "--format", "json")
		requireSuccess(t, []string{"get-snippet chunk"}, snippet)
		assertContainsAll(t, snippet.stdout, []string{"chunk-issue-1", "ISSUE-42", "line_start", "line_end", "content_hash"})

		chunks := runCLI(t, cachePath, "list-chunks", "--repo", "fixture-a", "--format", "json")
		requireSuccess(t, []string{"list-chunks"}, chunks)
		assertContainsAll(t, chunks.stdout, []string{"chunk-issue-1", "chunk-wiki-1", "source_id", "line_start", "line_end", "content_hash"})

		status := runCLI(t, cachePath, "cache-status", "--repo", "fixture-a", "--format", "json")
		requireSuccess(t, []string{"cache-status"}, status)
		assertContainsAll(t, status.stdout, []string{"records", "chunks", "sync_events", "index_freshness_warnings"})

		exportPath := filepath.Join(t.TempDir(), "snapshot.json")
		exported := runCLI(t, cachePath, "export", "--repo", "fixture-a", "--format", "json", "--output", exportPath)
		if exported.code != 0 {
			t.Fatalf("export failed: code=%d stdout=%s stderr=%s", exported.code, exported.stdout, exported.stderr)
		}
		if _, err := os.Stat(exportPath); err != nil {
			t.Fatalf("export did not write stored snapshot: %v", err)
		}
		snapshotBytes, err := os.ReadFile(exportPath)
		if err != nil {
			t.Fatal(err)
		}
		assertContainsAll(t, string(snapshotBytes), []string{"chunks", "chunk-issue-1", "line_start", "line_end", "content_hash"})

		diff := runCLI(t, cachePath, "diff", "--repo", "fixture-a", "--format", "json", "--base", exportPath)
		requireSuccess(t, []string{"diff"}, diff)
		assertContainsAll(t, diff.stdout, []string{"base_snapshot_id", "changed_source_ids", exportPath})

		missingIndexPath := filepath.Join(t.TempDir(), "missing-index.db")
		seedCLIReadFixture(t, ctx, missingIndexPath, false)
		for _, args := range [][]string{
			{"stale-index", "--repo", "fixture-a", "--format", "json"},
			{"cache-status", "--repo", "fixture-a", "--format", "json"},
			{"list-chunks", "--repo", "fixture-a", "--format", "json"},
			{"get-snippet", "--repo", "fixture-a", "ISSUE-42", "--line-start", "1", "--line-end", "2", "--format", "json"},
			{"export", "--repo", "fixture-a", "--format", "json"},
		} {
			got := runCLI(t, missingIndexPath, args...)
			requireSuccess(t, args, got)
			assertContains(t, got.stdout, "missing_index")
		}
	})

	t.Run("012-cli-read-task-1-complete-cli-read-handlers-scenario-4", func(t *testing.T) {
		missingRepo := runCLI(t, cachePath, "list", "--format", "json")
		if missingRepo.code == 0 || !(strings.Contains(missingRepo.stderr, "validation_failed") || strings.Contains(missingRepo.stderr, "repo_required")) {
			t.Fatalf("missing --repo did not return typed validation failure: code=%d stderr=%s", missingRepo.code, missingRepo.stderr)
		}

		unknownRepo := runCLI(t, cachePath, "list", "--repo", "missing-repo", "--format", "json")
		if unknownRepo.code == 0 || !strings.Contains(unknownRepo.stderr, "not_found") {
			t.Fatalf("unknown repo did not return not-found: code=%d stderr=%s", unknownRepo.code, unknownRepo.stderr)
		}

		fixtureA := runCLI(t, cachePath, "get", "--repo", "fixture-a", "issue:42", "--format", "json")
		requireSuccess(t, []string{"get fixture-a issue:42"}, fixtureA)
		fixtureB := runCLI(t, cachePath, "get", "--repo", "fixture-b", "issue:42", "--format", "json")
		requireSuccess(t, []string{"get fixture-b issue:42"}, fixtureB)
		assertContainsAll(t, fixtureA.stdout, []string{"fixture-a", "Fixture A Issue"})
		assertContainsAll(t, fixtureB.stdout, []string{"fixture-b", "Fixture B Issue"})
		if strings.Contains(fixtureA.stdout, "Fixture B Issue") || strings.Contains(fixtureB.stdout, "Fixture A Issue") {
			t.Fatalf("cross-repo alias collision leaked across scopes\nA=%s\nB=%s", fixtureA.stdout, fixtureB.stdout)
		}

		unscoped := runCLI(t, cachePath, "get", "issue:42", "--format", "json")
		if unscoped.code == 0 || !(strings.Contains(unscoped.stderr, "validation_failed") || strings.Contains(unscoped.stderr, "repo_required")) {
			t.Fatalf("unscoped alias lookup was not rejected: code=%d stderr=%s stdout=%s", unscoped.code, unscoped.stderr, unscoped.stdout)
		}
	})
}

func seedCLIReadFixture(t *testing.T, ctx context.Context, path string, includeChunks bool) {
	t.Helper()
	store, err := cache.NewSQLiteStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	for _, repo := range []cache.RepositoryBinding{
		{RepoID: "fixture-a", Owner: "owner-a", Name: "repo-a", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}, Aliases: []string{"fixture"}, CreatedAt: now, UpdatedAt: now},
		{RepoID: "fixture-b", Owner: "owner-b", Name: "repo-b", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}, Aliases: []string{"fixture-b"}, CreatedAt: now, UpdatedAt: now},
	} {
		if err := store.AddRepository(ctx, repo); err != nil {
			t.Fatal(err)
		}
	}

	issueBody := "offline fixture issue\nThis issue cites WIKI-Home.\nLine three has deterministic snippet text.\n"
	wikiBody := "offline fixture wiki\nThe wiki is linked from ISSUE-42.\nLine three has citation data.\n"
	issueChunks := []cache.Chunk{}
	wikiChunks := []cache.Chunk{}
	if includeChunks {
		issueChunks = []cache.Chunk{{RepoID: "fixture-a", ID: "chunk-issue-1", SourceID: "ISSUE-42", RecordID: "ISSUE-42", ContentHash: "hash-issue-chunk", ByteStart: 0, ByteEnd: len(issueBody), LineStart: 1, LineEnd: 3, HeadingPath: []string{"Issue"}, Text: issueBody, NormalizedText: issueBody, Policy: "heading"}}
		wikiChunks = []cache.Chunk{{RepoID: "fixture-a", ID: "chunk-wiki-1", SourceID: "WIKI-Home", RecordID: "WIKI-Home", ContentHash: "hash-wiki-chunk", ByteStart: 0, ByteEnd: len(wikiBody), LineStart: 1, LineEnd: 3, HeadingPath: []string{"Wiki"}, Text: wikiBody, NormalizedText: wikiBody, Policy: "heading"}}
	}
	graphs := []cache.SourceGraph{
		{
			Source: cache.Source{RepoID: "fixture-a", ID: "WIKI-Home", Kind: "wiki", Path: "wiki/Home.md", Title: "Fixture A Wiki", Body: wikiBody, Status: "active", Labels: []string{"wiki"}, ContentHash: "hash-wiki", CreatedAt: now, UpdatedAt: now.Add(time.Minute)},
			Identities: []cache.Identity{{RepoID: "fixture-a", SourceID: "WIKI-Home", AliasType: "wiki", Alias: "Home", Remote: cache.RemoteAlias{Type: "wiki", ID: "Home"}}},
			Chunks: wikiChunks,
			SyncStatus: &cache.SyncStatus{RepoID: "fixture-a", SourceID: "WIKI-Home", RemoteType: "wiki", RemoteID: "Home", RemoteRevision: "rev-wiki", Status: "fresh", LastFetchedAt: now},
			SyncEvents: []cache.SyncEvent{{RepoID: "fixture-a", ID: "sync-wiki", SourceID: "WIKI-Home", RemoteType: "wiki", RemoteID: "Home", RemoteRevision: "rev-wiki", Status: "succeeded", IdempotencyKey: "sync-wiki", Message: "fixture wiki", CreatedAt: now}},
		},
		{
			Source: cache.Source{RepoID: "fixture-a", ID: "ISSUE-42", Kind: "issue", Path: "issues/42.md", Title: "Fixture A Issue", Body: issueBody, Status: "open", Labels: []string{"bug"}, ContentHash: "hash-issue", CreatedAt: now, UpdatedAt: now.Add(2 * time.Minute)},
			Identities: []cache.Identity{{RepoID: "fixture-a", SourceID: "ISSUE-42", AliasType: "issue", Alias: "42", Remote: cache.RemoteAlias{Type: "issue", ID: "42"}}},
			Links: []cache.Link{{RepoID: "fixture-a", SourceID: "ISSUE-42", TargetID: "WIKI-Home", Kind: "mentions", Text: "WIKI-Home"}},
			Chunks: issueChunks,
			SyncStatus: &cache.SyncStatus{RepoID: "fixture-a", SourceID: "ISSUE-42", RemoteType: "issue", RemoteID: "42", RemoteRevision: "rev-issue", Status: "fresh", LastFetchedAt: now},
			SyncEvents: []cache.SyncEvent{{RepoID: "fixture-a", ID: "sync-issue", SourceID: "ISSUE-42", RemoteType: "issue", RemoteID: "42", RemoteRevision: "rev-issue", Status: "succeeded", IdempotencyKey: "sync-issue", Message: "fixture issue", CreatedAt: now}},
		},
		{
			Source: cache.Source{RepoID: "fixture-b", ID: "ISSUE-42", Kind: "issue", Path: "issues/42.md", Title: "Fixture B Issue", Body: "fixture-b scoped offline issue", Status: "open", ContentHash: "hash-b", CreatedAt: now, UpdatedAt: now},
			Identities: []cache.Identity{{RepoID: "fixture-b", SourceID: "ISSUE-42", AliasType: "issue", Alias: "42", Remote: cache.RemoteAlias{Type: "issue", ID: "42"}}},
			SyncStatus: &cache.SyncStatus{RepoID: "fixture-b", SourceID: "ISSUE-42", RemoteType: "issue", RemoteID: "42", RemoteRevision: "rev-b", Status: "fresh", LastFetchedAt: now},
		},
	}
	for _, graph := range graphs {
		if err := store.UpsertSourceGraph(ctx, graph); err != nil {
			t.Fatal(err)
		}
	}
}

func runCLI(t *testing.T, cachePath string, args ...string) cliResult {
	t.Helper()
	cmdArgs := append([]string{"run", "./cmd/gitcode-mcp", "--cache-path", cachePath}, args...)
	cmd := exec.Command("go", cmdArgs...)
	cmd.Dir = rootDir(t)
	cmd.Env = cleanEnv(cachePath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			code = 1
		}
	}
	return cliResult{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func cleanEnv(cachePath string) []string {
	keep := []string{}
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "PATH=") || strings.HasPrefix(entry, "HOME=") || strings.HasPrefix(entry, "TMPDIR=") || strings.HasPrefix(entry, "GOCACHE=") || strings.HasPrefix(entry, "GOMODCACHE=") || strings.HasPrefix(entry, "GOPATH=") {
			keep = append(keep, entry)
		}
	}
	keep = append(keep,
		"GITCODE_TOKEN=",
		"GITCODE_API_URL=https://offline.invalid/api",
		"GITCODE_MCP_CONFIG=",
		"GITCODE_CONFIG=",
		"GITCODE_MCP_CACHE_DIR="+filepath.Dir(cachePath),
	)
	return keep
}

func requireSuccess(t *testing.T, args []string, result cliResult) {
	t.Helper()
	if result.code != 0 {
		t.Fatalf("%v failed: code=%d stdout=%s stderr=%s", args, result.code, result.stdout, result.stderr)
	}
	if strings.TrimSpace(result.stdout) == "" {
		t.Fatalf("%v produced empty stdout", args)
	}
}

func assertJSONContainsRepo(t *testing.T, args []string, stdout string, repoID string) {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(stdout), &value); err != nil {
		t.Fatalf("%v invalid JSON: %v\n%s", args, err, stdout)
	}
	if !strings.Contains(stdout, `"repo_id"`) || !strings.Contains(stdout, repoID) {
		t.Fatalf("%v JSON output is not repo-scoped to %s: %s", args, repoID, stdout)
	}
}

func assertNoNetworkFailure(t *testing.T, result cliResult) {
	t.Helper()
	combined := result.stdout + result.stderr
	for _, forbidden := range []string{"offline.invalid", "GITCODE_TOKEN", "connection refused", "no such host", "network is unreachable"} {
		if strings.Contains(strings.ToLower(combined), strings.ToLower(forbidden)) {
			t.Fatalf("read command exposed network/credential dependency %q in output: %s", forbidden, combined)
		}
	}
}

func assertContains(t *testing.T, got string, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("output missing %q in %s", want, got)
	}
}

func assertContainsAll(t *testing.T, got string, wants []string) {
	t.Helper()
	for _, want := range wants {
		assertContains(t, got, want)
	}
}

func rootDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
			return wd
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			t.Fatal("go.mod not found")
		}
		wd = parent
	}
}
GOEOF

go test "$SCENARIO_DIR" -run TestCompleteCLIReadHandlersProductScenarios -count=1 -v
