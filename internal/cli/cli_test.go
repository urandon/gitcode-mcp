package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/capability"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/rag"
	"gitcode-mcp/internal/service"
	"gitcode-mcp/internal/servicectl"
)

func TestHelpReturnsSuccess(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Execute(nil, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("Execute(nil) code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "gitcode-mcp") {
		t.Fatalf("help output did not include program name: %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestCLIWriteCapabilitiesComeFromRegistry(t *testing.T) {
	known := map[string]bool{}
	for _, command := range commands {
		known[command] = true
	}
	for _, cap := range capability.WriteCapabilities() {
		if !cap.CLI.Enabled {
			if cap.CLI.DisabledReason == "" {
				t.Fatalf("%s is CLI-disabled without a reason", cap.ID)
			}
			continue
		}
		if known[cap.CLIName] {
			continue
		}
		foundAlias := false
		for _, alias := range cap.CLIAliases {
			if known[alias] {
				foundAlias = true
				break
			}
		}
		if !foundAlias {
			t.Fatalf("CLI-enabled capability %s missing command %q or aliases %v", cap.ID, cap.CLIName, cap.CLIAliases)
		}
	}
}

func TestCLIRAGCapabilitiesComeFromRegistry(t *testing.T) {
	known := map[string]bool{}
	for _, command := range commands {
		known[command] = true
	}
	for _, cap := range capability.RAGCapabilities() {
		if !cap.CLI.Enabled {
			if cap.CLI.DisabledReason == "" {
				t.Fatalf("%s is CLI-disabled without a reason", cap.ID)
			}
			continue
		}
		if !known[cap.CLIName] {
			t.Fatalf("CLI-enabled RAG capability %s missing command %q", cap.ID, cap.CLIName)
		}
	}
}

func TestRootHelpDoesNotAdvertiseGetIDFlag(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Execute([]string{"--help"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	if strings.Contains(out, "--id ID") {
		t.Fatalf("root help advertises command-local --id flag: %q", out)
	}
	if !strings.Contains(out, "record IDs are positional") {
		t.Fatalf("root help missing positional ID guidance: %q", out)
	}
}

func TestWriteErrorClassifiesCacheLockContention(t *testing.T) {
	var stderr bytes.Buffer
	err := cache.ErrLockContention{Path: "cache.db.writer.lock", Operation: "sync", RepoID: "fixture-a"}

	code := writeError(&stderr, "text", err)

	if code != 1 {
		t.Fatalf("writeError code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "failure_class: cache_busy") {
		t.Fatalf("stderr = %q, want cache_busy failure_class", stderr.String())
	}
	if strings.Contains(stderr.String(), "failure_class: internal_error") {
		t.Fatalf("stderr = %q, want no internal_error classification", stderr.String())
	}
}

func TestLivePushMirrorCooldownReportsRetryableProviderResponse(t *testing.T) {
	var stderr bytes.Buffer
	err := service.ErrWriteFailure{
		Code:           "push_mirror_sync_in_progress",
		RepoID:         "fixture-a",
		IdempotencyKey: "mirror-key",
		Cause:          gitcode.ErrPushMirrorSyncInProgress{Endpoint: "/api/v5/repos/example/repo/push_remote_mirrors/17"},
	}

	code := writeCommandError(&stderr, "json", startupPlan{ProviderMode: "live-http", Command: "trigger-push-mirror", APIBaseURL: "https://api.gitcode.com/api/v5"}, err)

	if code != 1 {
		t.Fatalf("writeCommandError code = %d, want 1", code)
	}
	var payload map[string]any
	if decodeErr := json.Unmarshal(stderr.Bytes(), &payload); decodeErr != nil {
		t.Fatalf("decode stderr: %v (%s)", decodeErr, stderr.String())
	}
	if payload["failure_class"] != "push_mirror_sync_in_progress" || payload["http_attempted"] != true || payload["retryable"] != true {
		t.Fatalf("payload=%#v", payload)
	}
}

func TestAddLabelDryRunValidates(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := executeWithFactory([]string{"add-label", "--repo", "fixture-a", "--number", "1", "--label", "triage", "--dry-run"}, &stdout, &stderr, cacheBackedFactory(t))

	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "add-label: dry_run_valid") {
		t.Fatalf("stdout missing dry_run_valid: %q", stdout.String())
	}
}

func TestDoctorRejectsConflictingProviderFlags(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Execute([]string{"doctor", "--live", "--offline"}, &stdout, &stderr)

	if code == 0 {
		t.Fatalf("code=0 stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "invalid_query") || !strings.Contains(stderr.String(), "--live conflicts with --offline/--fixture") {
		t.Fatalf("stderr missing provider conflict: %q", stderr.String())
	}
}

func TestCLIProvenanceFiltersListAndSearch(t *testing.T) {
	store := populatedStore(t)
	defer store.Close()
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	if err := store.UpsertSource(context.Background(), cache.Source{RepoID: "fixture-a", ID: "LIVE-1", Kind: "doc", Path: "docs/live.md", Title: "Live Backlog", Body: "backlog live-only", Status: "active", ContentHash: "live-hash", Provenance: cache.ProvenanceLive, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	factory := func(context.Context, string) (queryService, func() error, error) {
		return service.New(store), nil, nil
	}

	var listOut bytes.Buffer
	var listErr bytes.Buffer
	if code := executeWithFactory([]string{"list", "--repo", "fixture-a", "--provenance", "live", "--format", "json"}, &listOut, &listErr, factory); code != 0 {
		t.Fatalf("list code=%d stderr=%q", code, listErr.String())
	}
	var listed service.ListSourcesResult
	if err := json.Unmarshal(listOut.Bytes(), &listed); err != nil {
		t.Fatalf("list json: %v", err)
	}
	if len(listed.Results) != 1 || listed.Results[0].ID != "LIVE-1" || listed.Results[0].Provenance != "live" {
		t.Fatalf("list provenance filter = %#v", listed.Results)
	}

	var searchOut bytes.Buffer
	var searchErr bytes.Buffer
	if code := executeWithFactory([]string{"search", "--repo", "fixture-a", "--provenance", "live", "--format", "json", "backlog"}, &searchOut, &searchErr, factory); code != 0 {
		t.Fatalf("search code=%d stderr=%q", code, searchErr.String())
	}
	var searched service.SearchSourcesResult
	if err := json.Unmarshal(searchOut.Bytes(), &searched); err != nil {
		t.Fatalf("search json: %v", err)
	}
	if len(searched.Results) != 1 || searched.Results[0].ID != "LIVE-1" || searched.Results[0].Provenance != "live" {
		t.Fatalf("search provenance filter = %#v", searched.Results)
	}
}

func TestMinimumReplacementBar(t *testing.T) {
	factory := cacheBackedFactory(t)
	cases := [][]string{
		{"search", "--repo", "fixture-a", "backlog"},
		{"list", "--repo", "fixture-a", "--kind", "task", "--status", "ready"},
		{"get", "--repo", "fixture-a", "DOC-123"},
		{"backlinks", "--repo", "fixture-a", "DOC-123"},
		{"get-snippet", "--repo", "fixture-a", "DOC-123", "--line-start", "1", "--line-end", "1"},
		{"list-chunks", "--repo", "fixture-a"},
		{"recent", "--repo", "fixture-a"},
		{"link-check", "--repo", "fixture-a"},
		{"stale-index", "--repo", "fixture-a"},
		{"cache-status", "--repo", "fixture-a"},
		{"export", "--repo", "fixture-a"},
		{"diff", "--repo", "fixture-a"},
	}
	for _, args := range cases {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		code := executeWithFactory(args, &stdout, &stderr, factory)
		if code != 0 {
			t.Fatalf("%v code = %d stderr=%q stdout=%q", args, code, stderr.String(), stdout.String())
		}
		if stdout.Len() == 0 && args[0] != "link-check" {
			t.Fatalf("%v produced no output", args)
		}
	}
}

func TestCLIRepoScopedDuplicateAlias(t *testing.T) {
	store := populatedStore(t)
	defer store.Close()
	ctx := context.Background()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-b", Owner: "owner-b", Name: "repo-b", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSourceGraph(ctx, cache.SourceGraph{Source: cache.Source{RepoID: "fixture-a", ID: "ISSUE-42", Kind: "issue", Path: "fixture-a/issues/42.md", Title: "Fixture A", Body: "fixture-a scoped body", Status: "open", ContentHash: "a42"}, Identities: []cache.Identity{{RepoID: "fixture-a", AliasType: "issue", Alias: "42", Remote: cache.RemoteAlias{Type: "issue", ID: "42"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSourceGraph(ctx, cache.SourceGraph{Source: cache.Source{RepoID: "fixture-b", ID: "ISSUE-42", Kind: "issue", Path: "fixture-b/issues/42.md", Title: "Fixture B", Body: "fixture-b scoped body", Status: "open", ContentHash: "b42"}, Identities: []cache.Identity{{RepoID: "fixture-b", AliasType: "issue", Alias: "42", Remote: cache.RemoteAlias{Type: "issue", ID: "42"}}}}); err != nil {
		t.Fatal(err)
	}
	factory := func(context.Context, string) (queryService, func() error, error) { return service.New(store), nil, nil }
	var outA, errA bytes.Buffer
	if code := executeWithFactory([]string{"get", "--repo", "fixture-a", "issue:42"}, &outA, &errA, factory); code != 0 {
		t.Fatalf("fixture-a code=%d err=%q", code, errA.String())
	}
	var outB, errB bytes.Buffer
	if code := executeWithFactory([]string{"get", "--repo", "fixture-b", "issue:42"}, &outB, &errB, factory); code != 0 {
		t.Fatalf("fixture-b code=%d err=%q", code, errB.String())
	}
	if !strings.Contains(outA.String(), "repo_id: fixture-a") || strings.Contains(outA.String(), "fixture-b scoped body") {
		t.Fatalf("fixture-a output crossed scope: %q", outA.String())
	}
	if !strings.Contains(outB.String(), "repo_id: fixture-b") || strings.Contains(outB.String(), "fixture-a scoped body") {
		t.Fatalf("fixture-b output crossed scope: %q", outB.String())
	}
	var unscopedOut, unscopedErr bytes.Buffer
	if code := executeWithFactory([]string{"get", "issue:42"}, &unscopedOut, &unscopedErr, factory); code != 4 || !strings.Contains(unscopedErr.String(), "repo_required") {
		t.Fatalf("unscoped code=%d err=%q", code, unscopedErr.String())
	}
}

func TestCacheStatusJSON(t *testing.T) {
	store := populatedStore(t)
	defer store.Close()
	now := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	if err := store.UpsertRecordGraph(context.Background(), cache.RecordGraph{
		Record:     cache.Record{RepoID: "fixture-a", ID: "ISSUE-1", Type: "issue", Path: "issues/1.md", Title: "Issue", Body: "body", Status: "open", ContentHash: "h", Provenance: cache.ProvenanceRemote, RemoteType: "issue", RemoteID: "1", CreatedAt: now, UpdatedAt: now},
		Comments:   []cache.RecordComment{{CommentID: "c1", Author: "fixture-user", Body: "comment", ContentHash: "hc", CreatedAt: now, UpdatedAt: now}},
		Identities: []cache.Identity{{AliasType: "issue", Alias: "1", Remote: cache.RemoteAlias{Type: "issue", ID: "1"}}},
		SyncEvents: []cache.SyncEvent{{ID: "sync-1", RemoteType: "issue", RemoteID: "1", RemoteRevision: "r1", Status: "fresh", IdempotencyKey: "sync-1", Message: "fixture", CreatedAt: now}},
		AuditTrail: []cache.AuditTrailEntry{{ID: "audit-1", Operation: "sync", Status: "success", CreatedAt: now}},
		Snapshots:  []cache.Snapshot{{ID: "snap-1", Format: "json", ContentHash: "sh", RecordCount: 1, CreatedAt: now, Chunks: []cache.SnapshotChunk{{ChunkID: "chunk-1", RecordID: "ISSUE-1", LineStart: 1, LineEnd: 1}}}},
	}); err != nil {
		t.Fatal(err)
	}
	factory := func(context.Context, string) (queryService, func() error, error) { return service.New(store), nil, nil }
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := executeWithFactory([]string{"cache-status", "--repo", "fixture-a", "--format", "json"}, &stdout, &stderr, factory)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	var result service.CacheStatusResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !result.WALCapable || result.Records != 1 || result.Comments != 1 || result.IdentityAliases != 1 || result.SyncEvents != 1 || result.AuditRows != 1 || result.Snapshots != 1 || result.SnapshotChunks != 1 {
		t.Fatalf("cache-status result = %#v", result)
	}
}

func TestSearchJSON(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := executeWithFactory([]string{"search", "--repo", "fixture-a", "backlog", "--format", "json"}, &stdout, &stderr, cacheBackedFactory(t))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	var results service.SearchSourcesResult
	if err := json.Unmarshal(stdout.Bytes(), &results); err != nil {
		t.Fatalf("invalid json: %v: %q", err, stdout.String())
	}
	if results.RepoID != "fixture-a" || results.Query != "backlog" || results.SearchMode != service.SearchModeFullText || len(results.Results) == 0 || results.Results[0].ID == "" || results.Results[0].Path == "" || results.Results[0].Title == "" || results.Results[0].Snippet == "" {
		t.Fatalf("missing fields: %#v", results)
	}
}

func TestSearchTextShowsMode(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := executeWithFactory([]string{"search", "--repo", "fixture-a", "backlog"}, &stdout, &stderr, cacheBackedFactory(t))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "search_mode: full_text") {
		t.Fatalf("text search output missing mode: %q", stdout.String())
	}
}

func TestSearchSourcesCommandJSON(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := executeWithFactory([]string{"search_sources", "--repo", "fixture-a", "backlog", "--format", "json"}, &stdout, &stderr, cacheBackedFactory(t))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	var results service.SearchSourcesResult
	if err := json.Unmarshal(stdout.Bytes(), &results); err != nil {
		t.Fatalf("invalid json: %v: %q", err, stdout.String())
	}
	if results.RepoID != "fixture-a" || results.Query != "backlog" || results.SearchMode != service.SearchModeFullText || len(results.Results) == 0 {
		t.Fatalf("missing search_sources results: %#v", results)
	}
}

func TestSearchSourcesCommandEmptyJSON(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := executeWithFactory([]string{"search_sources", "--repo", "fixture-a", "NONEXISTENT", "--format", "json"}, &stdout, &stderr, cacheBackedFactory(t))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr=%q, want empty", stderr.String())
	}
	var results service.SearchSourcesResult
	if err := json.Unmarshal(stdout.Bytes(), &results); err != nil {
		t.Fatalf("invalid json: %v: %q", err, stdout.String())
	}
	if results.RepoID != "fixture-a" || results.Query != "NONEXISTENT" || results.SearchMode != service.SearchModeFullText || len(results.Results) != 0 {
		t.Fatalf("unexpected empty search_sources results: %#v", results)
	}
}

func TestSearchHelpStatesFullTextNotFuzzy(t *testing.T) {
	for _, command := range []string{"search", "search_sources"} {
		t.Run(command, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := Execute([]string{command, "--help"}, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
			out := stdout.String()
			for _, want := range []string{"full-text", "not fuzzy", "not fuzzy or semantic"} {
				if !strings.Contains(out, want) {
					t.Fatalf("%s help missing %q in %q", command, want, out)
				}
			}
		})
	}
}

func TestGetSource(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := executeWithFactory([]string{"get", "--repo", "fixture-a", "DOC-123"}, &stdout, &stderr, cacheBackedFactory(t))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	for _, want := range []string{"id: DOC-123", "path: docs/backlog.md", "title: Backlog", "body:", "status: active"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("get output missing %q in %q", want, stdout.String())
		}
	}
}

func TestSnippetAliasesMatchCanonical(t *testing.T) {
	factory := cacheBackedFactory(t)
	var canonical bytes.Buffer
	var canonicalErr bytes.Buffer
	if code := executeWithFactory([]string{"get-snippet", "--repo", "fixture-a", "DOC-123", "--line-start", "1", "--line-end", "1", "--format", "json"}, &canonical, &canonicalErr, factory); code != 0 {
		t.Fatalf("canonical code=%d stderr=%q", code, canonicalErr.String())
	}
	for _, command := range []string{"snippet", "snippets"} {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if code := executeWithFactory([]string{command, "--repo", "fixture-a", "DOC-123", "--line-start", "1", "--line-end", "1", "--format", "json"}, &stdout, &stderr, factory); code != 0 {
			t.Fatalf("%s code=%d stderr=%q", command, code, stderr.String())
		}
		if stdout.String() != canonical.String() {
			t.Fatalf("%s output differs\n got: %q\nwant: %q", command, stdout.String(), canonical.String())
		}
	}
}

func TestSnippetRejectsChunkAndLineAddressing(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := executeWithFactory([]string{"get-snippet", "--repo", "fixture-a", "DOC-123", "--chunk-id", "chunk-1", "--line-start", "1", "--format", "json"}, &stdout, &stderr, spyFactory())
	if code != 4 || !strings.Contains(stderr.String(), "invalid_query") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestExportJSONDeterministic(t *testing.T) {
	factory := cacheBackedFactory(t)
	var firstOut bytes.Buffer
	var firstErr bytes.Buffer
	if code := executeWithFactory([]string{"export", "--repo", "fixture-a", "--format", "json"}, &firstOut, &firstErr, factory); code != 0 {
		t.Fatalf("first export code=%d stderr=%q", code, firstErr.String())
	}
	var secondOut bytes.Buffer
	var secondErr bytes.Buffer
	if code := executeWithFactory([]string{"export", "--repo", "fixture-a", "--format", "json"}, &secondOut, &secondErr, factory); code != 0 {
		t.Fatalf("second export code=%d stderr=%q", code, secondErr.String())
	}
	if firstOut.String() != secondOut.String() {
		t.Fatalf("export output not deterministic")
	}
	var snapshot service.Snapshot
	if err := json.Unmarshal(firstOut.Bytes(), &snapshot); err != nil {
		t.Fatalf("invalid snapshot json: %v", err)
	}
	if len(snapshot.Sources) == 0 || len(snapshot.Chunks) != 0 {
		t.Fatalf("unexpected snapshot content: %#v", snapshot)
	}
}

func TestDiffLoadsSnapshotPaths(t *testing.T) {
	factory := cacheBackedFactory(t)
	basePath := filepath.Join(t.TempDir(), "base.json")
	var exportOut bytes.Buffer
	var exportErr bytes.Buffer
	if code := executeWithFactory([]string{"export", "--repo", "fixture-a", "--format", "json", "--output", basePath}, &exportOut, &exportErr, factory); code != 0 {
		t.Fatalf("export code=%d stderr=%q", code, exportErr.String())
	}
	if _, err := os.Stat(basePath); err != nil {
		t.Fatalf("base snapshot not written: %v", err)
	}
	var diffOut bytes.Buffer
	var diffErr bytes.Buffer
	if code := executeWithFactory([]string{"diff", "--repo", "fixture-a", "--format", "json", "--base", basePath}, &diffOut, &diffErr, factory); code != 0 {
		t.Fatalf("diff code=%d stderr=%q", code, diffErr.String())
	}
	var result service.DiffSnapshotResult
	if err := json.Unmarshal(diffOut.Bytes(), &result); err != nil {
		t.Fatalf("invalid diff json: %v", err)
	}
	if result.BaseSnapshotID != basePath {
		t.Fatalf("base id=%q want %q", result.BaseSnapshotID, basePath)
	}
}

func TestAllCommandsRegistered(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Execute([]string{"--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	for _, want := range []string{"ingest", "index", "search", "search_sources", "list", "get", "get-snippet", "snippet", "snippets", "backlinks", "list-chunks", "link-check", "stale-index", "recent", "cache", "cache-status", "sync-status", "sync_status", "sync", "export", "diff", "create-issue", "update-issue", "create-pr", "create-mr", "update-pr", "milestones", "list-push-mirrors", "push-mirrors", "trigger-push-mirror", "wait-push-mirror", "create-milestone", "update-milestone", "set-issue-milestone", "clear-issue-milestone", "create-page", "update-page", "delete-page", "add-comment", "add-pr-review-comment", "reply-pr-review-comment", "update-comment", "add-label", "publish-release", "config", "auth", "service", "rag", "rag-status", "rag-search", "doctor", "migrate-cache", "repo"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("help missing command %q in %q", want, stdout.String())
		}
	}
}

func TestServiceCommandStatusAndInstallUseUserGlobalPaths(t *testing.T) {
	root := t.TempDir()
	src := &repoInitLocalSource{
		env:       map[string]string{},
		cwd:       root,
		homeDir:   filepath.Join(root, "h"),
		configDir: filepath.Join(root, "f"),
		cacheDir:  filepath.Join(root, "c"),
	}

	var statusOut bytes.Buffer
	var statusErr bytes.Buffer
	code := executeWithFactoryAndDeps([]string{"service", "status", "--format", "json"}, &statusOut, &statusErr, nil, localCommandDeps{Source: src})
	if code != 0 {
		t.Fatalf("service status code=%d stderr=%q", code, statusErr.String())
	}
	var status servicectl.Status
	if err := json.Unmarshal(statusOut.Bytes(), &status); err != nil {
		t.Fatalf("invalid status json: %v\n%s", err, statusOut.String())
	}
	if status.Status != servicectl.StatusNotInstalled || status.Installed || status.Running {
		t.Fatalf("initial service status = %#v", status)
	}
	if !strings.HasPrefix(status.RuntimeDir, src.cacheDir) || !strings.HasPrefix(status.LogDir, src.cacheDir) {
		t.Fatalf("service paths are not cache-global: %#v", status)
	}

	var installOut bytes.Buffer
	var installErr bytes.Buffer
	code = executeWithFactoryAndDeps([]string{"service", "install", "--overwrite", "--format", "json"}, &installOut, &installErr, nil, localCommandDeps{Source: src})
	if code != 0 {
		t.Fatalf("service install code=%d stderr=%q", code, installErr.String())
	}
	if err := json.Unmarshal(installOut.Bytes(), &status); err != nil {
		t.Fatalf("invalid install json: %v\n%s", err, installOut.String())
	}
	if status.Status != servicectl.StatusInstalledStopped || !status.Installed || status.Running {
		t.Fatalf("installed service status = %#v", status)
	}
	if _, err := os.Stat(status.InstallPath); err != nil {
		t.Fatalf("install path was not written: %v", err)
	}
	if !strings.HasPrefix(status.InstallPath, src.homeDir) && !strings.HasPrefix(status.InstallPath, src.configDir) {
		t.Fatalf("install path is not user-global: %#v", status)
	}
}

func TestServiceHelpShowsLifecycleSubcommands(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Execute([]string{"service", "--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	for _, want := range []string{"install", "uninstall", "start", "stop", "status", "doctor", "run"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("service help missing %q in %q", want, stdout.String())
		}
	}

	stdout.Reset()
	stderr.Reset()
	code = Execute([]string{"service", "status", "--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("status help code=%d stderr=%q", code, stderr.String())
	}
	for _, want := range []string{"service status", "runtime", "socket"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("service status help missing %q in %q", want, stdout.String())
		}
	}
}

func TestServiceCLIControlsFakeJobOverIPC(t *testing.T) {
	root, err := shortCLITestRoot(t, "cli-svc-")
	if err != nil {
		t.Fatal(err)
	}
	src := &repoInitLocalSource{
		env:       map[string]string{"GITCODE_MCP_SERVICE_NETWORK": "mem", "GITCODE_MCP_SERVICE_ADDRESS": "test-cli-service-jobs"},
		cwd:       root,
		homeDir:   filepath.Join(root, "h"),
		configDir: filepath.Join(root, "f"),
		cacheDir:  filepath.Join(root, "c"),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runCode := make(chan int, 1)
	go func() {
		var runOut bytes.Buffer
		var runErr bytes.Buffer
		runCode <- executeWithFactoryAndDepsContext(ctx, []string{"service", "run"}, &runOut, &runErr, nil, localCommandDeps{Source: src})
	}()
	waitForServiceSocket(t, src)

	var jobOut bytes.Buffer
	var jobErr bytes.Buffer
	code := executeWithFactoryAndDeps([]string{"service", "fake-job", "--steps", "2", "--interval-ms", "1", "--format", "json"}, &jobOut, &jobErr, nil, localCommandDeps{Source: src})
	if code != 0 {
		t.Fatalf("fake-job code=%d stderr=%q", code, jobErr.String())
	}
	if !strings.Contains(jobErr.String(), "job progress:") {
		t.Fatalf("fake-job attach did not emit progress: %q", jobErr.String())
	}
	var job servicectl.Job
	if err := json.Unmarshal(jobOut.Bytes(), &job); err != nil {
		t.Fatalf("invalid fake-job json: %v\n%s", err, jobOut.String())
	}
	if job.Status != servicectl.JobStatusSucceeded || job.Completed != 2 {
		t.Fatalf("fake-job result = %#v", job)
	}

	var listOut bytes.Buffer
	var listErr bytes.Buffer
	code = executeWithFactoryAndDeps([]string{"service", "jobs", "--format", "json"}, &listOut, &listErr, nil, localCommandDeps{Source: src})
	if code != 0 {
		t.Fatalf("jobs code=%d stderr=%q", code, listErr.String())
	}
	var list servicectl.JobListResult
	if err := json.Unmarshal(listOut.Bytes(), &list); err != nil {
		t.Fatalf("invalid jobs json: %v\n%s", err, listOut.String())
	}
	if len(list.Jobs) != 1 || list.Jobs[0].ID != job.ID {
		t.Fatalf("jobs result = %#v", list)
	}

	var detachOut bytes.Buffer
	var detachErr bytes.Buffer
	code = executeWithFactoryAndDeps([]string{"service", "fake-job", "--steps", "50", "--interval-ms", "20", "--detach", "--format", "json"}, &detachOut, &detachErr, nil, localCommandDeps{Source: src})
	if code != 0 {
		t.Fatalf("detached fake-job code=%d stderr=%q", code, detachErr.String())
	}
	var detached servicectl.Job
	if err := json.Unmarshal(detachOut.Bytes(), &detached); err != nil {
		t.Fatalf("invalid detached json: %v\n%s", err, detachOut.String())
	}

	var cancelOut bytes.Buffer
	var cancelErr bytes.Buffer
	code = executeWithFactoryAndDeps([]string{"service", "cancel", detached.ID, "--format", "json"}, &cancelOut, &cancelErr, nil, localCommandDeps{Source: src})
	if code != 0 {
		t.Fatalf("cancel code=%d stderr=%q", code, cancelErr.String())
	}
	var cancelled servicectl.Job
	if err := json.Unmarshal(cancelOut.Bytes(), &cancelled); err != nil {
		t.Fatalf("invalid cancel json: %v\n%s", err, cancelOut.String())
	}
	if cancelled.Status != servicectl.JobStatusCancelled {
		t.Fatalf("cancelled job = %#v", cancelled)
	}

	cancel()
	if code := <-runCode; code != 0 {
		t.Fatalf("service run code=%d", code)
	}
}

func TestRAGIndexCLIStartsDaemonJobOverIPC(t *testing.T) {
	root, err := shortCLITestRoot(t, "cli-rag-")
	if err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(root, "cache.db")
	store, err := cache.NewSQLiteStore(context.Background(), cachePath)
	if err != nil {
		t.Fatalf("NewSQLiteStore returned error: %v", err)
	}
	if err := store.AddRepository(context.Background(), cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatalf("AddRepository returned error: %v", err)
	}
	if err := store.UpsertSourceGraph(context.Background(), cache.SourceGraph{
		Source: cache.Source{RepoID: "fixture-a", ID: "ISSUE-1", Kind: "issue", Path: "issues/1.md", Title: "RAG", Body: "русский 中文 English", Status: "open", ContentHash: "source-hash"},
		Chunks: []cache.Chunk{{
			RepoID:         "fixture-a",
			ID:             "chunk-1",
			SourceID:       "ISSUE-1",
			RecordID:       "ISSUE-1",
			ContentHash:    "chunk-hash",
			ByteStart:      0,
			ByteEnd:        32,
			LineStart:      1,
			LineEnd:        1,
			Text:           "русский 中文 English",
			NormalizedText: "русский 中文 english",
			Policy:         rag.DefaultChunkPolicyID,
		}},
	}); err != nil {
		t.Fatalf("UpsertSourceGraph returned error: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	configPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configPath, []byte(strings.Join([]string{
		"cache_path: " + cachePath,
		"rag:",
		"  default_profile: fake-rag",
		"  providers:",
		"    fake:",
		"      type: fake",
		"  profiles:",
		"    fake-rag:",
		"      provider: fake",
		"      model: fake-embedding",
		"      dimensions: 2",
		"      batch_size: 1",
		"  indexing:",
		"    profile: fake-rag",
		"",
	}, "\n")), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	src := &repoInitLocalSource{
		env:       map[string]string{"GITCODE_MCP_SERVICE_NETWORK": "mem", "GITCODE_MCP_SERVICE_ADDRESS": "test-cli-rag-index", config.EnvMCPConfigPath: configPath},
		cwd:       root,
		homeDir:   filepath.Join(root, "h"),
		configDir: filepath.Join(root, "f"),
		cacheDir:  filepath.Join(root, "c"),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runCode := make(chan int, 1)
	go func() {
		var runOut bytes.Buffer
		var runErr bytes.Buffer
		runCode <- executeWithFactoryAndDepsContext(ctx, []string{"service", "run"}, &runOut, &runErr, nil, localCommandDeps{Source: src})
	}()
	waitForServiceSocket(t, src)

	var out bytes.Buffer
	var errOut bytes.Buffer
	code := executeWithFactoryAndDeps([]string{"rag", "index", "--repo", "fixture-a", "--progress", "lines", "--format", "json"}, &out, &errOut, nil, localCommandDeps{Source: src})
	if code != 0 {
		t.Fatalf("rag index code=%d stderr=%q", code, errOut.String())
	}
	var job servicectl.Job
	if err := json.Unmarshal(out.Bytes(), &job); err != nil {
		t.Fatalf("invalid job json: %v\n%s", err, out.String())
	}
	if job.Type != servicectl.RAGIndexJobType || job.RepoID != "fixture-a" {
		t.Fatalf("job=%#v", job)
	}
	if job.Status != servicectl.JobStatusSucceeded {
		t.Fatalf("rag index job did not finish: %#v", job)
	}
	if job.Completed != 1 || job.Steps != 1 {
		t.Fatalf("completed job=%#v", job)
	}
	progress := errOut.String()
	for _, want := range []string{"rag index progress:", "1/1", "100.0%", "chunks/s", "elapsed="} {
		if !strings.Contains(progress, want) {
			t.Fatalf("rag index progress missing %q: %q", want, progress)
		}
	}
	cancel()
	if code := <-runCode; code != 0 {
		t.Fatalf("service run code=%d", code)
	}
}

func TestSyncCLIStartsDaemonJobOverIPC(t *testing.T) {
	root, err := shortCLITestRoot(t, "cli-sync-")
	if err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(root, "cache.db")
	store, err := cache.NewSQLiteStore(context.Background(), cachePath)
	if err != nil {
		t.Fatalf("NewSQLiteStore returned error: %v", err)
	}
	if err := store.AddRepository(context.Background(), cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatalf("AddRepository returned error: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	configPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configPath, []byte("cache_path: "+cachePath+"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	src := &repoInitLocalSource{
		env:       map[string]string{"GITCODE_MCP_SERVICE_NETWORK": "mem", "GITCODE_MCP_SERVICE_ADDRESS": "test-cli-sync-job", config.EnvMCPConfigPath: configPath},
		cwd:       root,
		homeDir:   filepath.Join(root, "h"),
		configDir: filepath.Join(root, "f"),
		cacheDir:  filepath.Join(root, "c"),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runCode := make(chan int, 1)
	go func() {
		var runOut bytes.Buffer
		var runErr bytes.Buffer
		runCode <- executeWithFactoryAndDepsContext(ctx, []string{"service", "run"}, &runOut, &runErr, nil, localCommandDeps{Source: src})
	}()
	waitForServiceSocket(t, src)

	var out bytes.Buffer
	var errOut bytes.Buffer
	code := executeWithFactoryAndDeps([]string{"sync", "--offline", "--repo", "fixture-a", "--issues", "--daemon", "--progress", "lines", "--format", "json"}, &out, &errOut, nil, localCommandDeps{Source: src})
	if code != 0 {
		t.Fatalf("sync daemon code=%d stderr=%q", code, errOut.String())
	}
	var job servicectl.Job
	if err := json.Unmarshal(out.Bytes(), &job); err != nil {
		t.Fatalf("invalid job json: %v\n%s", err, out.String())
	}
	if job.Type != servicectl.SyncJobType || job.RepoID != "fixture-a" || job.Status != servicectl.JobStatusSucceeded {
		t.Fatalf("sync job=%#v", job)
	}
	if job.Completed == 0 || job.Steps == 0 {
		t.Fatalf("sync job progress counters not populated: %#v", job)
	}
	if progress := errOut.String(); !strings.Contains(progress, "sync progress:") || !strings.Contains(progress, "collection=issues") {
		t.Fatalf("sync daemon progress missing expected output: %q", progress)
	}

	cancel()
	if code := <-runCode; code != 0 {
		t.Fatalf("service run code=%d", code)
	}
}

func TestRAGStatusCLIReportsCoverage(t *testing.T) {
	root := t.TempDir()
	cachePath := filepath.Join(root, "cache.db")
	store, err := cache.NewSQLiteStore(context.Background(), cachePath)
	if err != nil {
		t.Fatalf("NewSQLiteStore returned error: %v", err)
	}
	if err := store.AddRepository(context.Background(), cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatalf("AddRepository returned error: %v", err)
	}
	if err := store.UpsertSourceGraph(context.Background(), cache.SourceGraph{
		Source: cache.Source{RepoID: "fixture-a", ID: "ISSUE-1", Kind: "issue", Path: "issues/1.md", Title: "RAG status", Body: "русский 中文 English", Status: "open", ContentHash: "source-hash"},
		Chunks: []cache.Chunk{{
			RepoID:         "fixture-a",
			ID:             "chunk-1",
			SourceID:       "ISSUE-1",
			RecordID:       "ISSUE-1",
			ContentHash:    "chunk-hash",
			ByteStart:      0,
			ByteEnd:        32,
			LineStart:      1,
			LineEnd:        1,
			Text:           "русский 中文 English",
			NormalizedText: "русский 中文 english",
			Policy:         rag.DefaultChunkPolicyID,
		}},
	}); err != nil {
		t.Fatalf("UpsertSourceGraph returned error: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	configPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configPath, []byte(strings.Join([]string{
		"cache_path: " + cachePath,
		"rag:",
		"  default_profile: fake-rag",
		"  providers:",
		"    fake:",
		"      type: fake",
		"  profiles:",
		"    fake-rag:",
		"      provider: fake",
		"      model: fake-embedding",
		"      dimensions: 2",
		"      batch_size: 1",
		"  indexing:",
		"    profile: fake-rag",
		"",
	}, "\n")), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	src := &repoInitLocalSource{
		env:       map[string]string{config.EnvMCPConfigPath: configPath},
		cwd:       root,
		homeDir:   filepath.Join(root, "h"),
		configDir: filepath.Join(root, "f"),
		cacheDir:  filepath.Join(root, "c"),
	}
	var out bytes.Buffer
	var errOut bytes.Buffer
	code := executeWithFactoryAndDeps([]string{"rag", "status", "--repo", "fixture-a", "--format", "json"}, &out, &errOut, nil, localCommandDeps{Source: src})
	if code != 0 {
		t.Fatalf("rag status code=%d stderr=%q", code, errOut.String())
	}
	var payload struct {
		Status   string `json:"status"`
		Coverage struct {
			TotalChunks   int `json:"total_chunks"`
			MissingChunks int `json:"missing_chunks"`
		} `json:"coverage"`
		Provider struct {
			Ready bool `json:"ready"`
		} `json:"provider"`
		Namespace struct {
			Exists bool `json:"exists"`
		} `json:"namespace"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("invalid status json: %v\n%s", err, out.String())
	}
	if payload.Status != "no_namespace" || payload.Coverage.TotalChunks != 1 || payload.Coverage.MissingChunks != 1 || !payload.Provider.Ready || payload.Namespace.Exists {
		t.Fatalf("payload=%#v", payload)
	}
}

func TestRAGSearchCLIReportsNoNamespace(t *testing.T) {
	root := t.TempDir()
	cachePath := filepath.Join(root, "cache.db")
	store, err := cache.NewSQLiteStore(context.Background(), cachePath)
	if err != nil {
		t.Fatalf("NewSQLiteStore returned error: %v", err)
	}
	if err := store.AddRepository(context.Background(), cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatalf("AddRepository returned error: %v", err)
	}
	if err := store.UpsertSourceGraph(context.Background(), cache.SourceGraph{
		Source: cache.Source{RepoID: "fixture-a", ID: "ISSUE-1", Kind: "issue", Path: "issues/1.md", Title: "RAG search", Body: "rate limits and cited context", Status: "open", ContentHash: "source-hash"},
		Chunks: []cache.Chunk{{
			RepoID:         "fixture-a",
			ID:             "chunk-1",
			SourceID:       "ISSUE-1",
			RecordID:       "ISSUE-1",
			ContentHash:    "chunk-hash",
			ByteStart:      0,
			ByteEnd:        29,
			LineStart:      1,
			LineEnd:        1,
			Text:           "rate limits and cited context",
			NormalizedText: "rate limits and cited context",
			Policy:         rag.DefaultChunkPolicyID,
		}},
	}); err != nil {
		t.Fatalf("UpsertSourceGraph returned error: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	configPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configPath, []byte(strings.Join([]string{
		"cache_path: " + cachePath,
		"rag:",
		"  default_profile: fake-rag",
		"  providers:",
		"    fake:",
		"      type: fake",
		"  profiles:",
		"    fake-rag:",
		"      provider: fake",
		"      model: fake-embedding",
		"      dimensions: 2",
		"      batch_size: 1",
		"",
	}, "\n")), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	src := &repoInitLocalSource{
		env:       map[string]string{config.EnvMCPConfigPath: configPath},
		cwd:       root,
		homeDir:   filepath.Join(root, "h"),
		configDir: filepath.Join(root, "f"),
		cacheDir:  filepath.Join(root, "c"),
	}
	var out bytes.Buffer
	var errOut bytes.Buffer
	code := executeWithFactoryAndDeps([]string{"rag-search", "--repo", "fixture-a", "--format", "json", "rate limits"}, &out, &errOut, nil, localCommandDeps{Source: src})
	if code != 0 {
		t.Fatalf("rag-search code=%d stderr=%q", code, errOut.String())
	}
	var payload struct {
		SearchMode string `json:"search_mode"`
		Status     string `json:"status"`
		Warnings   []string
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("invalid search json: %v\n%s", err, out.String())
	}
	if payload.SearchMode != rag.SearchModeHybridRAG || payload.Status != rag.RAGSearchStatusNoNamespace || len(payload.Warnings) == 0 {
		t.Fatalf("payload=%#v", payload)
	}
}

func shortCLITestRoot(t *testing.T, pattern string) (string, error) {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	root := cwd
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			return "", os.ErrNotExist
		}
		root = parent
	}
	base := filepath.Join(root, ".t")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(base, pattern)
	if err != nil {
		return "", err
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir, nil
}

func TestPublicDocsDoNotAdvertiseReinitCache(t *testing.T) {
	stale := "reinit" + "-cache"
	paths := []string{
		filepath.Join("..", "..", "README.md"),
		filepath.Join("..", "cache", "schema.go"),
		filepath.Join("..", "cli", "cli.go"),
	}
	err := filepath.WalkDir(filepath.Join("..", "..", "docs"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".md" {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk docs returned error: %v", err)
	}
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s returned error: %v", path, err)
		}
		if strings.Contains(string(content), stale) {
			t.Fatalf("%s advertises stale command %q", path, stale)
		}
	}
}

func TestRecentJSON(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := executeWithFactory([]string{"recent", "--repo", "fixture-a", "--format", "json"}, &stdout, &stderr, cacheBackedFactory(t))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	var results service.RecentChangesResult
	if err := json.Unmarshal(stdout.Bytes(), &results); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if results.RepoID != "fixture-a" || len(results.Results) == 0 || results.Results[0].UpdatedAt.IsZero() {
		t.Fatalf("missing recent fields: %#v", results)
	}
}

func TestSyncStatusJSONAndAlias(t *testing.T) {
	factory := cacheBackedFactory(t)
	var perRecord bytes.Buffer
	var perRecordErr bytes.Buffer
	if code := executeWithFactory([]string{"sync-status", "--repo", "fixture-a", "DOC-123", "--format", "json"}, &perRecord, &perRecordErr, factory); code != 0 {
		t.Fatalf("sync-status per-record code=%d stderr=%q", code, perRecordErr.String())
	}
	var status service.SyncStatusResult
	if err := json.Unmarshal(perRecord.Bytes(), &status); err != nil {
		t.Fatalf("invalid per-record json: %v", err)
	}
	if status.RepoID != "fixture-a" || status.SourceID != "DOC-123" || status.Freshness != service.FreshnessFresh {
		t.Fatalf("sync-status per-record = %#v", status)
	}
	var aggregate bytes.Buffer
	var aggregateErr bytes.Buffer
	if code := executeWithFactory([]string{"sync_status", "--repo", "fixture-a", "--format", "json"}, &aggregate, &aggregateErr, factory); code != 0 {
		t.Fatalf("sync_status aggregate code=%d stderr=%q", code, aggregateErr.String())
	}
	var summary service.SyncStatusSummaryResult
	if err := json.Unmarshal(aggregate.Bytes(), &summary); err != nil {
		t.Fatalf("invalid aggregate json: %v", err)
	}
	if summary.RepoID != "fixture-a" || summary.FreshCount != 1 || summary.CacheEmpty || len(summary.Results) != 0 || summary.IssueComments == nil {
		t.Fatalf("sync-status aggregate = %#v", summary)
	}
	var detailed bytes.Buffer
	var detailedErr bytes.Buffer
	if code := executeWithFactory([]string{"sync_status", "--repo", "fixture-a", "--format", "json", "--details"}, &detailed, &detailedErr, factory); code != 0 {
		t.Fatalf("sync_status detailed code=%d stderr=%q", code, detailedErr.String())
	}
	var detailedSummary service.SyncStatusSummaryResult
	if err := json.Unmarshal(detailed.Bytes(), &detailedSummary); err != nil {
		t.Fatalf("invalid detailed aggregate json: %v", err)
	}
	if len(detailedSummary.Results) != 1 {
		t.Fatalf("sync-status detailed aggregate = %#v", detailedSummary)
	}
}

func TestSyncJSONDefaultsToCompactSummaryAndDetailsRestoresRecords(t *testing.T) {
	var compactOut bytes.Buffer
	var compactErr bytes.Buffer
	if code := executeWithFactory([]string{"sync", "--offline", "--repo", "fixture-a", "--issues", "--format", "json"}, &compactOut, &compactErr, spyFactory()); code != 0 {
		t.Fatalf("compact sync code=%d stderr=%q", code, compactErr.String())
	}
	var compact map[string]any
	if err := json.Unmarshal(compactOut.Bytes(), &compact); err != nil {
		t.Fatalf("invalid compact sync json: %v\n%s", err, compactOut.String())
	}
	if compact["status"] != "succeeded" || compact["success_count"].(float64) != 1 {
		t.Fatalf("compact sync summary=%#v", compact)
	}
	if _, ok := compact["results"]; ok {
		t.Fatalf("compact sync should omit per-record results: %#v", compact)
	}
	if !strings.Contains(compactErr.String(), "sync progress: type=records collection=issues page=1 records=1") {
		t.Fatalf("missing progress stderr: %q", compactErr.String())
	}

	var detailsOut bytes.Buffer
	var detailsErr bytes.Buffer
	if code := executeWithFactory([]string{"sync", "--offline", "--repo", "fixture-a", "--issues", "--format", "json", "--details"}, &detailsOut, &detailsErr, spyFactory()); code != 0 {
		t.Fatalf("details sync code=%d stderr=%q", code, detailsErr.String())
	}
	var detailed service.SyncResourcesResult
	if err := json.Unmarshal(detailsOut.Bytes(), &detailed); err != nil {
		t.Fatalf("invalid details sync json: %v\n%s", err, detailsOut.String())
	}
	if len(detailed.Results) != 1 || detailed.SuccessCount != 1 {
		t.Fatalf("details sync result=%#v", detailed)
	}
}

func TestSyncSummaryAndProgressExposeDeferredRecords(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	result := &service.SyncResourcesResult{
		Results: []service.SyncResult{
			{Status: "succeeded", Counts: service.SyncCounts{Fetched: 1, Listed: 1, FetchedDetail: 1, Deferred: 1}, GeneratedAt: now, StartedAt: now, CompletedAt: now},
		},
		SuccessCount: 1,
	}
	summary := syncResourcesSummary(result, nil, now)
	if summary.Counts.Deferred != 1 {
		t.Fatalf("summary deferred = %d, want 1", summary.Counts.Deferred)
	}
	var text bytes.Buffer
	renderSyncResourcesSummaryText(&text, summary)
	if !strings.Contains(text.String(), "deferred=1") {
		t.Fatalf("summary text missing deferred count: %q", text.String())
	}
	var progress bytes.Buffer
	renderSyncProgressLine(&progress, service.ProgressEvent{Collection: "issues", Page: 1, RecordsFetched: 1, RecordsDeferred: 1}, now)
	if !strings.Contains(progress.String(), "deferred=1") {
		t.Fatalf("progress text missing deferred count: %q", progress.String())
	}
}

func TestSyncCommentSurfaceRouting(t *testing.T) {
	run := func(args []string) (*spyService, int, string) {
		t.Helper()
		spy := &spyService{}
		factory := func(context.Context, string) (queryService, func() error, error) {
			return spy, nil, nil
		}
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		code := executeWithFactory(args, &stdout, &stderr, factory)
		return spy, code, stderr.String()
	}

	spy, code, stderr := run([]string{"sync", "--offline", "--repo", "fixture-a", "--comments", "--input", "issue:42"})
	if code != 0 {
		t.Fatalf("legacy issue comments sync code=%d stderr=%q", code, stderr)
	}
	if spy.calls["SyncToCache"] != 1 || spy.calls["BulkSyncPRComments"] != 0 {
		t.Fatalf("legacy issue comments calls=%+v, want SyncToCache only", spy.calls)
	}

	spy, code, stderr = run([]string{"sync", "--offline", "--repo", "fixture-a", "--issue-comments", "--input", "issue:42"})
	if code != 0 {
		t.Fatalf("issue comments sync code=%d stderr=%q", code, stderr)
	}
	if spy.calls["SyncToCache"] != 1 || spy.calls["BulkSyncPRComments"] != 0 {
		t.Fatalf("issue comments calls=%+v, want SyncToCache only", spy.calls)
	}

	spy, code, stderr = run([]string{"sync", "--offline", "--repo", "fixture-a", "--issue-comments"})
	if code != 0 {
		t.Fatalf("bulk issue comments sync code=%d stderr=%q", code, stderr)
	}
	if spy.calls["BulkSyncIssueComments"] != 1 || spy.calls["BulkSyncIssues"] != 0 {
		t.Fatalf("bulk issue comments calls=%+v, want queue drain only", spy.calls)
	}

	spy, code, stderr = run([]string{"sync", "--offline", "--repo", "fixture-a", "--issues", "--issue-comments"})
	if code != 0 {
		t.Fatalf("combined parent and issue comments sync code=%d stderr=%q", code, stderr)
	}
	if spy.calls["BulkSyncIssues"] != 1 || spy.calls["BulkSyncIssueComments"] != 1 {
		t.Fatalf("combined parent and issue comments calls=%+v, want parent then queue drain", spy.calls)
	}

	spy, code, stderr = run([]string{"sync", "--offline", "--repo", "fixture-a", "--pr-comments"})
	if code != 0 {
		t.Fatalf("pr comments sync code=%d stderr=%q", code, stderr)
	}
	if spy.calls["BulkSyncPRComments"] != 1 || spy.calls["SyncToCache"] != 0 {
		t.Fatalf("pr comments calls=%+v, want BulkSyncPRComments only", spy.calls)
	}

	spy, code, stderr = run([]string{"sync", "--offline", "--repo", "fixture-a", "--pr-comments", "--input", "issue:42"})
	if code != 4 || !strings.Contains(stderr, "--pr-comments cannot target issue aliases") {
		t.Fatalf("invalid pr comments target code=%d stderr=%q", code, stderr)
	}
	if len(spy.calls) != 0 {
		t.Fatalf("invalid pr comments target called service: %+v", spy.calls)
	}

	spy, code, stderr = run([]string{"sync", "--offline", "--repo", "fixture-a", "--comments", "--input", "pr:7"})
	if code != 4 || !strings.Contains(stderr, "targeted pull request comment sync is not supported") {
		t.Fatalf("targeted pr comments code=%d stderr=%q", code, stderr)
	}
	if len(spy.calls) != 0 {
		t.Fatalf("targeted pr comments called service: %+v", spy.calls)
	}
}

func TestSyncProgressModes(t *testing.T) {
	t.Run("off suppresses progress", func(t *testing.T) {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		code := executeWithFactory([]string{"sync", "--offline", "--repo", "fixture-a", "--issues", "--format", "json", "--progress", "off"}, &stdout, &stderr, spyFactory())
		if code != 0 {
			t.Fatalf("sync code=%d stderr=%q", code, stderr.String())
		}
		if strings.Contains(stderr.String(), "sync progress:") {
			t.Fatalf("progress stderr not suppressed: %q", stderr.String())
		}
		var compact map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &compact); err != nil {
			t.Fatalf("invalid compact sync json: %v\n%s", err, stdout.String())
		}
	})

	t.Run("quiet suppresses progress", func(t *testing.T) {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		code := executeWithFactory([]string{"sync", "--offline", "--repo", "fixture-a", "--issues", "--format", "json", "--quiet"}, &stdout, &stderr, spyFactory())
		if code != 0 {
			t.Fatalf("sync code=%d stderr=%q", code, stderr.String())
		}
		if stderr.Len() != 0 {
			t.Fatalf("quiet stderr=%q, want empty", stderr.String())
		}
	})

	t.Run("jsonl writes progress events to stderr", func(t *testing.T) {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		code := executeWithFactory([]string{"sync", "--offline", "--repo", "fixture-a", "--issues", "--format", "json", "--progress", "jsonl"}, &stdout, &stderr, spyFactory())
		if code != 0 {
			t.Fatalf("sync code=%d stderr=%q", code, stderr.String())
		}
		if err := json.Unmarshal(stdout.Bytes(), &map[string]any{}); err != nil {
			t.Fatalf("invalid stdout json: %v\n%s", err, stdout.String())
		}
		lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
		if len(lines) != 1 {
			t.Fatalf("jsonl progress lines=%d stderr=%q", len(lines), stderr.String())
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(lines[0]), &event); err != nil {
			t.Fatalf("invalid jsonl progress: %v line=%q", err, lines[0])
		}
		if event["type"] != "records" || event["collection"] != "issues" || event["records_fetched"].(float64) != 1 {
			t.Fatalf("unexpected progress event=%#v", event)
		}
		if _, ok := event["elapsed_ms"]; !ok {
			t.Fatalf("progress event missing elapsed_ms=%#v", event)
		}
	})

	t.Run("invalid mode fails validation", func(t *testing.T) {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		code := executeWithFactory([]string{"sync", "--offline", "--repo", "fixture-a", "--issues", "--progress", "sparkles"}, &stdout, &stderr, spyFactory())
		if code == 0 {
			t.Fatalf("invalid progress mode succeeded stdout=%q stderr=%q", stdout.String(), stderr.String())
		}
		if !strings.Contains(stderr.String(), "progress must be auto, spinner, lines, jsonl, or off") {
			t.Fatalf("stderr missing validation message: %q", stderr.String())
		}
	})

	t.Run("auto uses lines for non terminal stderr", func(t *testing.T) {
		if got := syncProgressMode(options{progress: "auto"}, &bytes.Buffer{}); got != "lines" {
			t.Fatalf("syncProgressMode auto non-terminal=%q, want lines", got)
		}
	})

	t.Run("spinner renders one terminal line", func(t *testing.T) {
		state := syncProgressSpinnerState{Started: time.Now()}
		state.Apply(service.ProgressEvent{Collection: "issues", Page: 2, RecordsFetched: 3})
		var stderr bytes.Buffer
		renderSyncProgressSpinnerFrame(&stderr, &state)
		line := stderr.String()
		for _, want := range []string{"\r\033[K", "sync", "issues", "p2", "3 rec"} {
			if !strings.Contains(line, want) {
				t.Fatalf("spinner line missing %q: %q", want, line)
			}
		}
		for _, unwanted := range []string{"type=", "collection=", "page=", "records="} {
			if strings.Contains(line, unwanted) {
				t.Fatalf("spinner line should stay compact and omit %q: %q", unwanted, line)
			}
		}
	})

	t.Run("spinner renders rate limit compactly", func(t *testing.T) {
		state := syncProgressSpinnerState{Started: time.Now()}
		state.Apply(service.ProgressEvent{Collection: "issues", Page: 2, RecordsFetched: 3})
		state.Apply(service.ProgressEvent{Type: "rate_limit", RateLimitState: "throttle_wait_started", RetryAfter: "250ms"})
		var stderr bytes.Buffer
		renderSyncProgressSpinnerFrame(&stderr, &state)
		line := stderr.String()
		for _, want := range []string{"issues", "p2", "3 rec", "wait 250ms"} {
			if !strings.Contains(line, want) {
				t.Fatalf("spinner rate-limit line missing %q: %q", want, line)
			}
		}
		if strings.Contains(line, "rate_limit=") || strings.Contains(line, "retry_after=") {
			t.Fatalf("spinner rate-limit line should stay compact: %q", line)
		}
	})

	t.Run("lines include rate limiter state", func(t *testing.T) {
		var stderr bytes.Buffer
		renderSyncProgressLine(&stderr, service.ProgressEvent{
			Type:           "rate_limit",
			RateLimitState: "throttle_wait_started",
			RateLimitRPS:   "4",
			RateLimitBurst: 4,
			RetryAfter:     "250ms",
			Endpoint:       "/api/v5/repos/example/repo/issues",
			Attempt:        1,
		}, time.Now())
		line := stderr.String()
		for _, want := range []string{"type=rate_limit", "rate_limit=throttle_wait_started", "rps=4", "burst=4", "retry_after=250ms", "attempt=1"} {
			if !strings.Contains(line, want) {
				t.Fatalf("progress line missing %q: %q", want, line)
			}
		}
	})
}

func TestRAGIndexProgressRendering(t *testing.T) {
	started := time.Date(2026, 7, 2, 14, 31, 28, 0, time.UTC)
	now := started.Add(32 * time.Second)
	state := ragIndexProgressState{Started: started, JobID: "job-3", Status: servicectl.JobStatusRunning}
	state.ApplyEvent(service.ProgressEvent{RecordsListed: 4033, RecordsFetched: 336, RecordsFailed: 16})

	t.Run("spinner is compact", func(t *testing.T) {
		var stderr bytes.Buffer
		renderRAGIndexProgressSpinnerFrame(&stderr, &state, now)
		line := stderr.String()
		for _, want := range []string{"\r\033[K", "rag index running", "336/4033", "8.3%", "10.5 chunks/s", "failed=16", "elapsed=32s"} {
			if !strings.Contains(line, want) {
				t.Fatalf("rag spinner line missing %q: %q", want, line)
			}
		}
		for _, unwanted := range []string{"records=", "collection=", "type="} {
			if strings.Contains(line, unwanted) {
				t.Fatalf("rag spinner line should stay compact and omit %q: %q", unwanted, line)
			}
		}
	})

	t.Run("json event includes speed", func(t *testing.T) {
		event := ragIndexProgressJSONEvent(state, now)
		if event.Completed != 336 || event.Total != 4033 || event.Failed != 16 {
			t.Fatalf("event counts = %#v", event)
		}
		if event.ChunksPerS < 10.4 || event.ChunksPerS > 10.6 {
			t.Fatalf("event speed = %#v", event)
		}
	})
}

func TestRenderSyncResourcesPartialSummaryGroupsFailures(t *testing.T) {
	result := &service.SyncResourcesResult{
		Results:      []service.SyncResult{{Status: "succeeded", Counts: service.SyncCounts{Fetched: 1}, GeneratedAt: time.Now()}},
		SuccessCount: 1,
		FailureCount: 2,
		Failures: []service.ResourceError{
			{SourceID: "PR-1", RemoteType: "pr_comment", FailureClass: "api_validation", Endpoint: "/api/v5/repos/example/repo/pulls/1/comments", StatusCode: 400, Message: "one"},
			{SourceID: "PR-2", RemoteType: "pr_comment", FailureClass: "api_validation", Endpoint: "/api/v5/repos/example/repo/pulls/1/comments", StatusCode: 400, Message: "two"},
		},
	}
	partial := &service.PartialSyncError{Errors: result.Failures, SuccessCount: 1, FailureCount: 2, Diagnostic: service.SyncDiagnosticTimeout, TotalRequested: 3}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := renderSyncResources(&stdout, &stderr, "json", false, result, partial, startupPlan{}, time.Now().UTC())
	if code != 1 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var summary map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &summary); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if summary["status"] != "partial" || summary["diagnostic"] != string(service.SyncDiagnosticTimeout) || summary["failure_count"].(float64) != 2 {
		t.Fatalf("partial summary=%#v", summary)
	}
	if _, ok := summary["results"]; ok {
		t.Fatalf("partial summary should omit results: %#v", summary)
	}
	groups, ok := summary["failure_groups"].([]any)
	if !ok || len(groups) != 1 {
		t.Fatalf("failure groups=%#v", summary["failure_groups"])
	}
	group := groups[0].(map[string]any)
	if group["remote_type"] != "pr_comment" || group["failure_class"] != "api_validation" || group["endpoint"] != "/api/v5/repos/example/repo/pulls/1/comments" || group["status_code"].(float64) != 400 || group["count"].(float64) != 2 {
		t.Fatalf("failure group=%#v", group)
	}
}

func TestLinkCheckJSON(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := executeWithFactory([]string{"link-check", "--repo", "fixture-a", "--format", "json"}, &stdout, &stderr, spyFactory())
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	var result service.LinkCheckResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if result.CheckedCount == 0 || result.BrokenCount == 0 || len(result.BrokenLinks) == 0 || result.SuggestedAliases == nil {
		t.Fatalf("missing link-check fields: %#v", result)
	}
}

func TestStaleIndexJSON(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := executeWithFactory([]string{"stale-index", "--repo", "fixture-a", "--format", "json"}, &stdout, &stderr, spyFactory())
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	var result service.StaleIndexResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if result.StaleCount == 0 || len(result.AffectedSourceIDs) == 0 || len(result.MissingTargetIDs) == 0 {
		t.Fatalf("missing stale-index fields: %#v", result)
	}
}

func TestHelpDocumentsShellMapping(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Execute([]string{"--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	for _, want := range []string{"find -> list", "rg -n -> search", "rg --files -> list", "sed -n -> get-snippet", "handoff/review inspection -> recent", "broken pointer search -> link-check", "stale derived data search -> stale-index", "sync -> search -> list -> get -> backlinks"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("help missing %q in %q", want, stdout.String())
		}
	}
}

func TestUnknownRepoIsNotFound(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := executeWithFactory([]string{"list", "--repo", "missing-repo", "--format", "json"}, &stdout, &stderr, cacheBackedFactory(t))
	if code != 3 || !strings.Contains(stderr.String(), "not_found") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestQueryCommandErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"empty cache", []string{"list", "--repo", "fixture-a"}, 2},
		{"not found", []string{"get", "--repo", "fixture-a", "MISSING"}, 3},
		{"invalid snippet", []string{"get-snippet", "--repo", "fixture-a", "--line-start", "5", "--line-end", "1", "DOC-123"}, 4},
		{"clamped snippet", []string{"get-snippet", "--repo", "fixture-a", "--line-start", "1", "--line-end", "50", "DOC-123"}, 0},
		{"stale strict", []string{"stale-index", "--repo", "fixture-a", "--strict"}, 5},
		{"link strict", []string{"link-check", "--repo", "fixture-a", "--strict"}, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			factory := cacheBackedFactory(t)
			if tc.name == "empty cache" {
				factory = emptyFactory(t)
			}
			if tc.name == "stale strict" || tc.name == "link strict" {
				factory = spyFactory()
			}
			code := executeWithFactory(tc.args, &stdout, &stderr, factory)
			if code != tc.want {
				t.Fatalf("code=%d want=%d stdout=%q stderr=%q", code, tc.want, stdout.String(), stderr.String())
			}
			if tc.name == "clamped snippet" && (stdout.Len() == 0 || stderr.Len() == 0) {
				t.Fatalf("clamped snippet should write stdout and warning stderr")
			}
		})
	}
}

func TestRepoRegistryCLI(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.db")
	factory := func(ctx context.Context, path string) (queryService, func() error, error) {
		store, err := cache.NewSQLiteStore(ctx, path)
		if err != nil {
			return nil, nil, err
		}
		return service.New(store), store.Close, nil
	}
	var addOut, addErr bytes.Buffer
	code := executeWithFactory([]string{"repo", "add", "--cache-path", cachePath, "--repo", "fixture-a", "--owner", "owner-a", "--name", "repo-a", "--api-base-url", "https://user:pass@example.invalid/api?access_token=secret&safe=1", "--scopes", "issues,wiki,pulls,comments,issues", "--alias", "proj"}, &addOut, &addErr, factory)
	if code != 0 {
		t.Fatalf("repo add code=%d stderr=%q", code, addErr.String())
	}
	var statusOut, statusErr bytes.Buffer
	code = executeWithFactory([]string{"repo", "status", "--cache-path", cachePath, "--repo", "fixture-a"}, &statusOut, &statusErr, factory)
	if code != 0 {
		t.Fatalf("repo status code=%d stderr=%q", code, statusErr.String())
	}
	out := statusOut.String()
	for _, want := range []string{"repo_id: fixture-a", "owner: owner-a", "name: repo-a", "api_base_url: https://example.invalid/api?safe=1", "scopes: issues,wiki", "aliases: proj", "binding_state: ready", "alias_conflict_state: none", "cache_state: ready", "index_state: unknown", "binary_version:", "binary_version_source:", "cache_schema_version: 17", "expected_cache_schema_version: 17", "issue_records: 0", "issue_comments: 0", "issue_comment_queue_state: available", "issue_comment_queue: pending=0 deferred=0 complete=0 total=0"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status output missing %q in %q", want, out)
		}
	}
	if strings.Contains(out, "secret") || strings.Contains(out, "user:pass") {
		t.Fatalf("status output leaked sensitive URL parts: %q", out)
	}
	var dupOut, dupErr bytes.Buffer
	code = executeWithFactory([]string{"repo", "add", "--cache-path", cachePath, "--repo", "fixture-a", "--owner", "owner-a", "--name", "repo-a", "--api-base-url", "https://example.invalid/api", "--scopes", "issues"}, &dupOut, &dupErr, factory)
	if code == 0 || !strings.Contains(dupErr.String(), "conflict") {
		t.Fatalf("duplicate repo code=%d stderr=%q", code, dupErr.String())
	}
	var aliasOut, aliasErr bytes.Buffer
	code = executeWithFactory([]string{"repo", "add", "--cache-path", cachePath, "--repo", "fixture-b", "--owner", "owner-b", "--name", "repo-b", "--api-base-url", "https://example.invalid/api", "--scopes", "issues", "--alias", "proj"}, &aliasOut, &aliasErr, factory)
	if code == 0 || !strings.Contains(aliasErr.String(), "conflict") {
		t.Fatalf("alias conflict code=%d stderr=%q", code, aliasErr.String())
	}
	var missingOut, missingErr bytes.Buffer
	code = executeWithFactory([]string{"repo", "status", "--cache-path", cachePath, "--repo", "missing-repo"}, &missingOut, &missingErr, factory)
	if code != 3 || !strings.Contains(missingErr.String(), "repository") || !strings.Contains(missingErr.String(), "not found") {
		t.Fatalf("missing status code=%d stderr=%q", code, missingErr.String())
	}
}

func TestRepoStatusReadsCompatibleOlderSchemaForDiagnostics(t *testing.T) {
	ctx := context.Background()
	cachePath := filepath.Join(t.TempDir(), "older-cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner-a", Name: "repo-a", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DROP TABLE issue_comment_sync`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE schema_version SET version = 15`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := Execute([]string{"repo", "status", "--cache-path", cachePath, "--repo", "fixture-a", "--format", "json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	for _, want := range []string{`"cache_state": "migration_required"`, `"cache_schema_version": 15`, `"expected_cache_schema_version": 17`, `"binary_version"`, `"issue_comment_queue_state": "schema_unavailable"`} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("repo status missing %q in %q", want, stdout.String())
		}
	}
}

func TestRepoAddAPIBaseURLPrecedence(t *testing.T) {
	tests := []struct {
		name          string
		configuredURL string
		explicitURL   string
		wantURL       string
		wantError     string
	}{
		{
			name:    "built-in GitCode v5 default",
			wantURL: "https://api.gitcode.com/api/v5",
		},
		{
			name:          "effective config",
			configuredURL: "https://configured.example/api/v5",
			wantURL:       "https://configured.example/api/v5",
		},
		{
			name:          "explicit override",
			configuredURL: "https://configured.example/api/v5",
			explicitURL:   "https://explicit.example/api/v5",
			wantURL:       "https://explicit.example/api/v5",
		},
		{
			name:          "invalid explicit value does not fall back",
			configuredURL: "https://configured.example/api/v5",
			explicitURL:   "not-a-url",
			wantError:     "valid api base url is required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			cachePath := filepath.Join(root, "cache.db")
			env := map[string]string{}
			if tc.configuredURL != "" {
				configPath := filepath.Join(root, "config.yaml")
				if err := os.WriteFile(configPath, []byte("gitcode_base_url: "+tc.configuredURL+"\n"), 0o600); err != nil {
					t.Fatalf("write config: %v", err)
				}
				env[config.EnvMCPConfigPath] = configPath
			}
			src := &repoInitLocalSource{
				env:       env,
				cwd:       root,
				homeDir:   filepath.Join(root, "home"),
				configDir: filepath.Join(root, "config"),
				cacheDir:  filepath.Join(root, "cache"),
			}
			factory := func(ctx context.Context, path string) (queryService, func() error, error) {
				store, err := cache.NewSQLiteStore(ctx, path)
				if err != nil {
					return nil, nil, err
				}
				return service.New(store), store.Close, nil
			}
			args := []string{"repo", "add", "--cache-path", cachePath, "--repo", "example-owner/example-repo", "--owner", "example-owner", "--name", "example-repo", "--scopes", "issues,wiki"}
			if tc.explicitURL != "" {
				args = append(args, "--api-base-url", tc.explicitURL)
			}
			var stdout, stderr bytes.Buffer
			code := executeWithFactoryAndDeps(args, &stdout, &stderr, factory, localCommandDeps{Source: src})
			if tc.wantError != "" {
				if code == 0 || !strings.Contains(stderr.String(), tc.wantError) {
					t.Fatalf("code=%d stderr=%q, want error containing %q", code, stderr.String(), tc.wantError)
				}
				store, err := cache.NewSQLiteStore(context.Background(), cachePath)
				if err != nil {
					t.Fatalf("open cache: %v", err)
				}
				defer store.Close()
				if _, err := store.GetRepository(context.Background(), "example-owner/example-repo"); err == nil {
					t.Fatal("invalid explicit URL unexpectedly created a repository binding")
				}
				return
			}
			if code != 0 {
				t.Fatalf("code=%d stderr=%q stdout=%q", code, stderr.String(), stdout.String())
			}
			store, err := cache.NewSQLiteStore(context.Background(), cachePath)
			if err != nil {
				t.Fatalf("open cache: %v", err)
			}
			repo, err := store.GetRepository(context.Background(), "example-owner/example-repo")
			if closeErr := store.Close(); closeErr != nil {
				t.Fatalf("close cache: %v", closeErr)
			}
			if err != nil {
				t.Fatalf("get repository: %v", err)
			}
			if repo.APIBaseURL != tc.wantURL {
				t.Fatalf("api base URL=%q want %q", repo.APIBaseURL, tc.wantURL)
			}
		})
	}
}

func TestBindCompatibilityAliasCreatesRepository(t *testing.T) {
	root := t.TempDir()
	cachePath := filepath.Join(root, "cache.db")
	src := &repoInitLocalSource{
		env:       map[string]string{},
		cwd:       root,
		homeDir:   filepath.Join(root, "home"),
		configDir: filepath.Join(root, "config"),
		cacheDir:  filepath.Join(root, "cache"),
	}
	factory := func(ctx context.Context, path string) (queryService, func() error, error) {
		store, err := cache.NewSQLiteStore(ctx, path)
		if err != nil {
			return nil, nil, err
		}
		return service.New(store), store.Close, nil
	}
	var stdout, stderr bytes.Buffer
	code := executeWithFactoryAndDeps([]string{"bind", "--cache-path", cachePath, "--repo-owner", "example-owner", "--repo", "example-repo", "--format", "json"}, &stdout, &stderr, factory, localCommandDeps{Source: src})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	store, err := cache.NewSQLiteStore(context.Background(), cachePath)
	if err != nil {
		t.Fatalf("open cache: %v", err)
	}
	repo, err := store.GetRepository(context.Background(), "example-owner/example-repo")
	if closeErr := store.Close(); closeErr != nil {
		t.Fatalf("close cache: %v", closeErr)
	}
	if err != nil {
		t.Fatalf("get repository: %v", err)
	}
	if repo.Owner != "example-owner" || repo.Name != "example-repo" || repo.APIBaseURL != "https://api.gitcode.com/api/v5" {
		t.Fatalf("unexpected compatibility binding: %#v", repo)
	}
	if len(repo.Scopes) != 2 || repo.Scopes[0] != cache.RepositoryScopeIssues || repo.Scopes[1] != cache.RepositoryScopeWiki {
		t.Fatalf("scopes=%v want [issues wiki]", repo.Scopes)
	}
}

func TestQueryCommandsUseServiceOnly(t *testing.T) {
	spy := &spyService{}
	factory := func(context.Context, string) (queryService, func() error, error) { return spy, nil, nil }
	commands := [][]string{
		{"ingest"}, {"index", "--repo", "fixture-a", "--full"}, {"search", "--repo", "fixture-a", "backlog"}, {"search_sources", "--repo", "fixture-a", "backlog"}, {"list", "--repo", "fixture-a"}, {"get", "--repo", "fixture-a", "DOC-123"}, {"backlinks", "--repo", "fixture-a", "DOC-123"}, {"get-snippet", "--repo", "fixture-a", "DOC-123", "--line-start", "1", "--line-end", "1"}, {"snippet", "--repo", "fixture-a", "DOC-123", "--line-start", "1", "--line-end", "1"}, {"snippets", "--repo", "fixture-a", "DOC-123", "--line-start", "1", "--line-end", "1"}, {"list-chunks", "--repo", "fixture-a"}, {"recent", "--repo", "fixture-a"}, {"link-check", "--repo", "fixture-a"}, {"stale-index", "--repo", "fixture-a"}, {"pr-discussions", "--repo", "fixture-a", "--number", "7", "--unresolved-only"}, {"sync", "--offline", "--repo", "fixture-a", "--input", "issue:42"}, {"cache", "reset", "--live", "--repo", "fixture-a"}, {"cache-status", "--repo", "fixture-a"}, {"sync-status", "--repo", "fixture-a", "DOC-123"}, {"sync_status", "--repo", "fixture-a"}, {"export", "--repo", "fixture-a"}, {"diff", "--repo", "fixture-a"}, {"repo", "add", "--repo", "fixture-a", "--owner", "owner", "--name", "repo", "--api-base-url", "https://example.invalid/api", "--scopes", "issues"}, {"repo", "status", "--repo", "fixture-a"}, {"create-issue", "--repo", "fixture-a", "--title", "t", "--dry-run"}, {"update-issue", "--repo", "fixture-a", "--number", "1", "--dry-run"}, {"create-pr", "--repo", "fixture-a", "--title", "pr", "--head", "topic", "--base", "main", "--dry-run"}, {"create-mr", "--repo", "fixture-a", "--title", "mr", "--head", "topic", "--base", "main", "--dry-run"}, {"update-pr", "--repo", "fixture-a", "--number", "1", "--body", "line 1\nline 2", "--dry-run"}, {"milestones", "--repo", "fixture-a", "--dry-run"}, {"list-push-mirrors", "--repo", "fixture-a", "--offline"}, {"push-mirrors", "--repo", "fixture-a", "--offline"}, {"trigger-push-mirror", "--repo", "fixture-a", "--mirror-id", "17", "--idempotency-key", "trigger-key", "--dry-run"}, {"wait-push-mirror", "--repo", "fixture-a", "--mirror-id", "17", "--offline"}, {"create-milestone", "--repo", "fixture-a", "--title", "M1", "--due-on", "2026-07-15", "--dry-run"}, {"update-milestone", "--repo", "fixture-a", "--milestone", "1", "--title", "M1b", "--dry-run"}, {"set-issue-milestone", "--repo", "fixture-a", "--number", "1", "--milestone", "1", "--dry-run"}, {"clear-issue-milestone", "--repo", "fixture-a", "--number", "1", "--dry-run"}, {"create-page", "--repo", "fixture-a", "--title", "t", "--body", "b", "--dry-run"}, {"update-page", "--repo", "fixture-a", "--slug", "s", "--dry-run"}, {"add-comment", "--repo", "fixture-a", "--number", "1", "--body", "b", "--dry-run"}, {"add-pr-review-comment", "--repo", "fixture-a", "--number", "1", "--body", "b", "--path", "internal/service/service.go", "--line", "42", "--dry-run"}, {"reply-pr-review-comment", "--repo", "fixture-a", "--number", "1", "--discussion-id", "D1", "--parent-comment-id", "C1", "--body", "b", "--dry-run"}, {"update-comment", "--repo", "fixture-a", "--comment-id", "c1", "--body", "b", "--dry-run"}, {"add-label", "--repo", "fixture-a", "--number", "1", "--label", "l", "--dry-run"}, {"publish-release", "--repo", "fixture-a", "--tag", "v0.1.0", "--title", "t", "--body", "b", "--dry-run"},
	}
	for _, args := range commands {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if code := executeWithFactory(args, &stdout, &stderr, factory); code != 0 {
			t.Fatalf("%v code=%d stderr=%q", args, code, stderr.String())
		}
	}
	wantCalls := map[string]int{"Ingest": 1, "Index": 1, "SearchSources": 2, "ListSources": 1, "GetSource": 1, "GetBacklinks": 1, "GetSnippet": 3, "ListChunks": 1, "RecentChanges": 1, "LinkCheck": 1, "StaleIndex": 1, "ListPRDiscussions": 1, "SyncToCache": 1, "ResetLiveCache": 1, "CacheStatus": 1, "GetSyncStatus": 1, "SyncStatus": 1, "ExportSnapshot": 1, "DiffSnapshot": 1, "AddRepository": 1, "RepositoryStatus": 1, "CreateIssue": 1, "UpdateIssue": 1, "CreatePR": 2, "UpdatePR": 1, "ListMilestones": 1, "ListPushRemoteMirrors": 2, "TriggerPushRemoteMirror": 1, "WaitPushRemoteMirror": 1, "CreateMilestone": 1, "UpdateMilestone": 1, "SetIssueMilestone": 1, "ClearIssueMilestone": 1, "CreatePage": 1, "UpdatePage": 1, "AddComment": 1, "ReplyPRReviewComment": 1, "AddLabel": 1, "PublishRelease": 1}
	for method, want := range wantCalls {
		if spy.calls[method] != want {
			t.Fatalf("%s calls=%d want %d", method, spy.calls[method], want)
		}
	}
	replyReq := spy.lastWriteRequest["ReplyPRReviewComment"]
	if replyReq.DiscussionID != "D1" || replyReq.ParentID != "C1" || replyReq.Number != 1 || replyReq.Body != "b" {
		t.Fatalf("reply request=%#v", replyReq)
	}
}

func TestBulkSyncRequestUsesTraversalBoundsByDefault(t *testing.T) {
	req := bulkSyncRequest(options{repo: "fixture-a"})
	if req.Bounds == nil {
		t.Fatal("Bounds is nil, want default traversal bounds for collection sync")
	}
	if req.Bounds.MaxPages != 0 || req.Bounds.MaxRecords != 0 {
		t.Fatalf("Bounds = %#v, want unlimited traversal bounds", req.Bounds)
	}
	if req.PerPage != 100 {
		t.Fatalf("PerPage = %d, want default 100", req.PerPage)
	}

	limited := bulkSyncRequest(options{repo: "fixture-a", perPage: 25, maxPages: 2, maxRecords: 40})
	if limited.Bounds == nil || limited.Bounds.MaxPages != 2 || limited.Bounds.MaxRecords != 40 || limited.PerPage != 25 {
		t.Fatalf("limited request = %#v", limited)
	}
}

func TestDispatchUsesProvidedContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	spy := &spyService{}
	factory := func(context.Context, string) (queryService, func() error, error) { return spy, nil, nil }
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := executeWithFactoryAndDepsContext(ctx, []string{"search", "--repo", "fixture-a", "backlog"}, &stdout, &stderr, factory, localCommandDeps{Source: config.OSSource{}})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if spy.lastContextErr != context.Canceled {
		t.Fatalf("search context error = %v, want context.Canceled", spy.lastContextErr)
	}
}

func TestCreatePRAliasDispatchesWriteRequest(t *testing.T) {
	spy := &spyService{}
	factory := func(context.Context, string) (queryService, func() error, error) { return spy, nil, nil }
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := executeWithFactory([]string{"create-mr", "--repo", "fixture-a", "--title", "Open PR", "--body", "Body", "--head", "topic", "--base", "main", "--dry-run", "--idempotency-key", "pr-key"}, &stdout, &stderr, factory)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	req := spy.lastWriteRequest["CreatePR"]
	if req.RepoID != "fixture-a" || req.Mode != service.WriteModeDryRun || req.Title != "Open PR" || req.Body != "Body" || req.Head != "topic" || req.Base != "main" || req.IdempotencyKey != "pr-key" {
		t.Fatalf("CreatePR request=%#v", req)
	}
	if !strings.Contains(stdout.String(), "create-pr: dry_run_valid") {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestUpdatePRDispatchesWriteRequest(t *testing.T) {
	spy := &spyService{}
	factory := func(context.Context, string) (queryService, func() error, error) { return spy, nil, nil }
	body := "Line one\nLine two"
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := executeWithFactory([]string{"update-pr", "--repo", "fixture-a", "--number", "42", "--title", "Updated PR", "--body", body, "--state", "closed", "--dry-run", "--idempotency-key", "update-pr-key"}, &stdout, &stderr, factory)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	req := spy.lastWriteRequest["UpdatePR"]
	if req.RepoID != "fixture-a" || req.Mode != service.WriteModeDryRun || req.Number != 42 || req.Title != "Updated PR" || req.Body != body || req.State != "closed" || req.IdempotencyKey != "update-pr-key" {
		t.Fatalf("UpdatePR request=%#v", req)
	}
	if !strings.Contains(stdout.String(), "update-pr: dry_run_valid") {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestSetIssueMilestoneDispatchesWriteRequest(t *testing.T) {
	spy := &spyService{}
	factory := func(context.Context, string) (queryService, func() error, error) { return spy, nil, nil }
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := executeWithFactory([]string{"set-issue-milestone", "--repo", "fixture-a", "--number", "42", "--milestone", "RAG indexer MVP", "--dry-run", "--idempotency-key", "issue-milestone-key"}, &stdout, &stderr, factory)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	req := spy.lastWriteRequest["SetIssueMilestone"]
	if req.RepoID != "fixture-a" || req.Mode != service.WriteModeDryRun || req.Number != 42 || req.Milestone != "RAG indexer MVP" || req.IdempotencyKey != "issue-milestone-key" {
		t.Fatalf("SetIssueMilestone request=%#v", req)
	}
	if !strings.Contains(stdout.String(), "set-issue-milestone: dry_run_valid") {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestIssueWritesDispatchMilestoneFlags(t *testing.T) {
	spy := &spyService{}
	factory := func(context.Context, string) (queryService, func() error, error) { return spy, nil, nil }
	var stdout, stderr bytes.Buffer
	code := executeWithFactory([]string{"create-issue", "--repo", "fixture-a", "--title", "Milestoned", "--milestone", "MILESTONE-1", "--dry-run", "--idempotency-key", "create-milestone-key"}, &stdout, &stderr, factory)
	if code != 0 {
		t.Fatalf("create code=%d stderr=%q", code, stderr.String())
	}
	createReq := spy.lastWriteRequest["CreateIssue"]
	if createReq.Milestone != "MILESTONE-1" || createReq.ClearMilestone {
		t.Fatalf("CreateIssue request=%#v", createReq)
	}

	stdout.Reset()
	stderr.Reset()
	code = executeWithFactory([]string{"update-issue", "--repo", "fixture-a", "--number", "42", "--clear-milestone", "--dry-run", "--idempotency-key", "clear-milestone-key"}, &stdout, &stderr, factory)
	if code != 0 {
		t.Fatalf("update code=%d stderr=%q", code, stderr.String())
	}
	updateReq := spy.lastWriteRequest["UpdateIssue"]
	if updateReq.Number != 42 || !updateReq.ClearMilestone || updateReq.Milestone != "" {
		t.Fatalf("UpdateIssue request=%#v", updateReq)
	}
}

func TestPublishReleaseParsesBodyFileAndAssets(t *testing.T) {
	bodyPath := filepath.Join(t.TempDir(), "release.md")
	if err := os.WriteFile(bodyPath, []byte("release body"), 0o600); err != nil {
		t.Fatal(err)
	}
	spy := &spyService{}
	factory := func(context.Context, string) (queryService, func() error, error) { return spy, nil, nil }
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := executeWithFactory([]string{"publish-release", "--repo", "fixture-a", "--tag", "v0.1.0", "--title", "gitcode-mcp v0.1.0", "--input", bodyPath, "--status", "latest", "--asset", "checksums.txt=https://example.invalid/checksums.txt", "--dry-run", "--idempotency-key", "release-v0.1.0"}, &stdout, &stderr, factory)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	req := spy.lastReleaseRequest
	if req.RepoID != "fixture-a" || req.Tag != "v0.1.0" || req.Title != "gitcode-mcp v0.1.0" || req.Body != "release body" || req.Status != "latest" || req.Mode != service.WriteModeDryRun || req.IdempotencyKey != "release-v0.1.0" {
		t.Fatalf("PublishRelease request=%#v", req)
	}
	if len(req.Assets) != 1 || req.Assets[0].Name != "checksums.txt" || req.Assets[0].URL != "https://example.invalid/checksums.txt" {
		t.Fatalf("assets=%#v", req.Assets)
	}
	if !strings.Contains(stdout.String(), "publish-release: dry_run_valid") {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestPRDiscussionsCommandReturnsJSON(t *testing.T) {
	spy := &spyService{}
	factory := func(context.Context, string) (queryService, func() error, error) { return spy, nil, nil }
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := executeWithFactory([]string{"pr-discussions", "--repo", "fixture-a", "--number", "7", "--unresolved-only", "--format", "json"}, &stdout, &stderr, factory)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	var result service.PRDiscussionsResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode json: %v\n%s", err, stdout.String())
	}
	if result.RepoID != "fixture-a" || result.Number != 7 || !result.UnresolvedOnly || len(result.Discussions) != 1 || result.Discussions[0].ID != "D7" {
		t.Fatalf("result=%+v", result)
	}
}

func cacheBackedFactory(t *testing.T) serviceFactory {
	t.Helper()
	return func(context.Context, string) (queryService, func() error, error) {
		store := populatedStore(t)
		return service.New(store), store.Close, nil
	}
}

func emptyFactory(t *testing.T) serviceFactory {
	t.Helper()
	return func(context.Context, string) (queryService, func() error, error) {
		store, err := cache.NewInMemorySQLiteStore(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := store.AddRepository(context.Background(), cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner-a", Name: "repo-a", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
			t.Fatal(err)
		}
		return service.New(store), store.Close, nil
	}
}

func populatedStore(t *testing.T) *cache.SQLiteStore {
	t.Helper()
	store, err := cache.NewInMemorySQLiteStore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	if err := store.AddRepository(context.Background(), cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner-a", Name: "repo-a", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	graphs := []cache.SourceGraph{
		{Source: cache.Source{RepoID: "fixture-a", ID: "DOC-123", Kind: "doc", Path: "docs/backlog.md", Title: "Backlog", Body: "backlog overview\nready task details\nmore context", Status: "active", Labels: []string{"knowledge"}, ContentHash: "h1", CreatedAt: now, UpdatedAt: now}, SyncStatus: &cache.SyncStatus{RepoID: "fixture-a", RemoteType: "issue", RemoteID: "100", RemoteRevision: "r1", Status: "fresh", LastFetchedAt: now}},
		{Source: cache.Source{RepoID: "fixture-a", ID: "TASK-1", Kind: "task", Path: "project/tasks/task-1.md", Title: "Ready Task", Body: "task references DOC-123", Status: "ready", ContentHash: "h2", CreatedAt: now.Add(time.Minute), UpdatedAt: now.Add(time.Minute)}, Links: []cache.Link{{RepoID: "fixture-a", TargetID: "DOC-123", Kind: "mentions", Text: "DOC-123"}}},
	}
	for _, graph := range graphs {
		if err := store.UpsertSourceGraph(context.Background(), graph); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

type spyService struct {
	calls              map[string]int
	lastWriteRequest   map[string]service.WriteCommandRequest
	lastReleaseRequest service.PublishReleaseRequest
	lastContextErr     error
}

func (s *spyService) called(name string) {
	if s.calls == nil {
		s.calls = map[string]int{}
	}
	s.calls[name]++
}
func (s *spyService) Ingest(context.Context, service.OperationRequest) (service.OperationResult, error) {
	s.called("Ingest")
	return service.OperationResult{Command: "ingest", Status: "ok", ProcessedCount: 1, GeneratedAt: time.Now()}, nil
}
func (s *spyService) Index(context.Context, service.OperationRequest) (service.OperationResult, error) {
	s.called("Index")
	return service.OperationResult{Command: "index", Status: "ok", ProcessedCount: 1, GeneratedAt: time.Now()}, nil
}
func (s *spyService) SearchSources(ctx context.Context, req service.SearchSourcesRequest) (service.SearchSourcesResult, error) {
	s.called("SearchSources")
	s.lastContextErr = ctx.Err()
	line := 1
	return service.SearchSourcesResult{RepoID: req.RepoID, Query: req.Query, Results: []service.SearchSourceResult{{ID: "DOC-123", Path: "docs/backlog.md", Title: "Backlog", Kind: "doc", Status: "active", Snippet: "backlog", LineStart: &line, LineEnd: &line, Score: 1}}}, nil
}
func (s *spyService) ListSources(context.Context, service.ListSourcesRequest) (service.ListSourcesResult, error) {
	s.called("ListSources")
	return service.ListSourcesResult{RepoID: "fixture-a", Results: []service.SourceSummary{{ID: "DOC-123", Path: "docs/backlog.md", Title: "Backlog"}}}, nil
}
func (s *spyService) GetSource(context.Context, service.GetSourceRequest) (service.SourceRecord, error) {
	s.called("GetSource")
	return service.SourceRecord{ID: "DOC-123", Path: "docs/backlog.md", Title: "Backlog", Body: "body"}, nil
}
func (s *spyService) GetBacklinks(context.Context, service.GetBacklinksRequest) (service.BacklinksResult, error) {
	s.called("GetBacklinks")
	return service.BacklinksResult{RepoID: "fixture-a", ID: "DOC-123", Backlinks: []service.BacklinkResult{{SourceSummary: service.SourceSummary{ID: "TASK-1", Path: "project/tasks/task-1.md"}, TargetID: "DOC-123"}}}, nil
}
func (s *spyService) GetSnippet(context.Context, service.SnippetRequest) (service.SnippetResult, error) {
	s.called("GetSnippet")
	return service.SnippetResult{ID: "DOC-123", Path: "docs/backlog.md", Text: "body", LineStart: 1, LineEnd: 1}, nil
}
func (s *spyService) ListChunks(context.Context, service.ChunkQuery) (service.ChunkQueryResult, error) {
	s.called("ListChunks")
	return service.ChunkQueryResult{Chunks: []service.ChunkResult{{ID: "chunk-1", SourceID: "DOC-123", Policy: "heading", Text: "body"}}, Total: 1}, nil
}
func (s *spyService) SearchChunks(context.Context, service.ChunkSearchQuery) (service.ChunkQueryResult, error) {
	s.called("SearchChunks")
	return service.ChunkQueryResult{Chunks: []service.ChunkResult{{ID: "chunk-1", SourceID: "DOC-123", Policy: "heading", Text: "body"}}, Total: 1}, nil
}
func (s *spyService) GetChunkSnippet(context.Context, service.SnippetQuery) (service.ChunkQueryResult, error) {
	s.called("GetChunkSnippet")
	return service.ChunkQueryResult{Chunks: []service.ChunkResult{{ID: "chunk-1", SourceID: "DOC-123", Policy: "heading", SnippetText: "body"}}, Total: 1}, nil
}
func (s *spyService) GetSyncStatus(context.Context, service.SyncStatusRequest) (service.SyncStatusResult, error) {
	s.called("GetSyncStatus")
	return service.SyncStatusResult{RepoID: "fixture-a", SourceID: "DOC-123", Status: "fresh", LastFetchedAt: time.Now()}, nil
}
func (s *spyService) SyncStatus(context.Context, service.ListSourcesRequest) (service.SyncStatusSummaryResult, error) {
	s.called("SyncStatus")
	return service.SyncStatusSummaryResult{RepoID: "fixture-a", FreshCount: 1, Results: []service.SyncStatusResult{{RepoID: "fixture-a", SourceID: "DOC-123", Status: "fresh", LastFetchedAt: time.Now()}}}, nil
}
func (s *spyService) RecentChanges(context.Context, service.RecentChangesRequest) (service.RecentChangesResult, error) {
	s.called("RecentChanges")
	return service.RecentChangesResult{RepoID: "fixture-a", Results: []service.RecentChangeResult{{ID: "DOC-123", Path: "docs/backlog.md", UpdatedAt: time.Now()}}}, nil
}
func (s *spyService) LinkCheck(_ context.Context, req service.LinkCheckRequest) (service.LinkCheckResult, error) {
	s.called("LinkCheck")
	result := service.LinkCheckResult{CheckedCount: 1, BrokenCount: 1, BrokenLinks: []service.BrokenLinkResult{{SourceID: "DOC-123", TargetID: "MISSING", Kind: "mentions", Text: "MISSING"}}, SuggestedAliases: map[string][]string{}}
	if req.Strict {
		return result, service.ErrLinkCheckFailed{BrokenCount: 1}
	}
	return result, nil
}
func (s *spyService) StaleIndex(_ context.Context, req service.StaleIndexRequest) (service.StaleIndexResult, error) {
	s.called("StaleIndex")
	result := service.StaleIndexResult{StaleCount: 1, AffectedSourceIDs: []string{"DOC-123"}, MissingTargetIDs: []string{"MISSING"}}
	if req.Strict {
		return result, service.ErrStaleIndex{StaleCount: 1}
	}
	return result, nil
}
func (s *spyService) SyncToCache(context.Context, service.SyncRequest) (service.SyncResult, error) {
	s.called("SyncToCache")
	return service.SyncResult{Status: "succeeded", Counts: service.SyncCounts{Fetched: 1}, IdempotencyKey: "key", GeneratedAt: time.Now()}, nil
}
func (s *spyService) SyncResources(_ context.Context, reqs []service.SyncRequest) (*service.SyncResourcesResult, error) {
	s.called("SyncResources")
	results := make([]service.SyncResult, len(reqs))
	for i := range results {
		results[i] = service.SyncResult{Status: "succeeded", Counts: service.SyncCounts{Fetched: 1}, IdempotencyKey: reqs[i].IdempotencyKey, GeneratedAt: time.Now()}
	}
	return &service.SyncResourcesResult{Results: results, SuccessCount: len(results)}, nil
}
func (s *spyService) BulkSyncIssues(_ context.Context, req service.BulkSyncRequest) (*service.SyncResourcesResult, error) {
	s.called("BulkSyncIssues")
	return spyBulkSyncResult(req, "issues"), nil
}
func (s *spyService) BulkSyncIssueComments(_ context.Context, req service.BulkSyncRequest) (*service.SyncResourcesResult, error) {
	s.called("BulkSyncIssueComments")
	return spyBulkSyncResult(req, "issue_comments"), nil
}
func (s *spyService) BulkSyncWiki(_ context.Context, req service.BulkSyncRequest) (*service.SyncResourcesResult, error) {
	s.called("BulkSyncWiki")
	return spyBulkSyncResult(req, "wiki"), nil
}
func (s *spyService) BulkSyncPullRequests(_ context.Context, req service.BulkSyncRequest) (*service.SyncResourcesResult, error) {
	s.called("BulkSyncPullRequests")
	return spyBulkSyncResult(req, "pulls"), nil
}
func (s *spyService) BulkSyncPRComments(_ context.Context, req service.BulkSyncRequest) (*service.SyncResourcesResult, error) {
	s.called("BulkSyncPRComments")
	return spyBulkSyncResult(req, "pr_comments"), nil
}
func (s *spyService) BulkSyncAll(_ context.Context, req service.BulkSyncRequest) (*service.SyncResourcesResult, error) {
	s.called("BulkSyncAll")
	return spyBulkSyncResult(req, "all"), nil
}

func spyBulkSyncResult(req service.BulkSyncRequest, collection string) *service.SyncResourcesResult {
	if req.ProgressChan != nil {
		req.ProgressChan <- service.ProgressEvent{Collection: collection, Page: 1, RecordsFetched: 1}
	}
	if req.Bounds != nil && req.Bounds.ProgressChan != nil && req.Bounds.ProgressChan != req.ProgressChan {
		req.Bounds.ProgressChan <- service.ProgressEvent{Collection: collection, Page: 1, RecordsFetched: 1}
	}
	now := time.Now()
	return &service.SyncResourcesResult{
		Results:       []service.SyncResult{{Status: "succeeded", Counts: service.SyncCounts{Fetched: 1, Listed: 1}, GeneratedAt: now, StartedAt: now, CompletedAt: now, ZeroDelta: true}},
		SuccessCount:  1,
		PagesListed:   1,
		RecordsListed: 1,
		Ordering:      "updated_at_desc",
	}
}
func (s *spyService) ListPRDiscussions(_ context.Context, req service.PRDiscussionRequest) (service.PRDiscussionsResult, error) {
	s.called("ListPRDiscussions")
	resolved := false
	return service.PRDiscussionsResult{RepoID: req.RepoID, Number: req.Number, UnresolvedOnly: req.UnresolvedOnly, Discussions: []service.PRDiscussion{{ID: "D7", Kind: "inline", Resolved: &resolved, Comments: []service.PRReviewComment{{ID: "301", Body: "review"}}}}, GeneratedAt: time.Now()}, nil
}
func (s *spyService) ResetLiveCache(context.Context, service.ResetLiveCacheRequest) (service.ResetLiveCacheResult, error) {
	s.called("ResetLiveCache")
	return service.ResetLiveCacheResult{RepoID: "fixture-a", Reset: "live"}, nil
}
func (s *spyService) CacheStatus(context.Context, service.CacheStatusRequest) (service.CacheStatusResult, error) {
	s.called("CacheStatus")
	return service.CacheStatusResult{RepoID: "fixture-a", WALCapable: true, JournalMode: "wal", Records: 1}, nil
}
func (s *spyService) ExportSnapshot(context.Context, service.ExportSnapshotRequest) (service.ExportSnapshotResult, error) {
	s.called("ExportSnapshot")
	return service.ExportSnapshotResult{SnapshotID: "snap", Format: "text", RecordCount: 1, GeneratedAt: time.Now(), ContentHash: "hash", InlineContent: "DOC-123\n"}, nil
}
func (s *spyService) DiffSnapshot(context.Context, service.DiffSnapshotRequest) (service.DiffSnapshotResult, error) {
	s.called("DiffSnapshot")
	return service.DiffSnapshotResult{BaseSnapshotID: "base", HeadSnapshotID: "head", Format: "text", ChangedSourceIDs: []string{"DOC-123"}, DiffText: "changed\n"}, nil
}
func (s *spyService) AddRepository(context.Context, service.AddRepositoryRequest) (service.RepositoryBinding, error) {
	s.called("AddRepository")
	return service.RepositoryBinding{RepoID: "fixture-a", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []service.RepositoryScope{service.RepositoryScopeIssues}}, nil
}
func (s *spyService) RepositoryStatus(context.Context, service.RepositoryStatusRequest) (service.RepositoryStatus, error) {
	s.called("RepositoryStatus")
	return service.RepositoryStatus{RepoID: "fixture-a", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []service.RepositoryScope{service.RepositoryScopeIssues}, BindingState: "ready", AliasConflictState: "none", CacheState: "unknown", IndexState: "unknown"}, nil
}
func (s *spyService) CreateIssue(_ context.Context, req service.WriteCommandRequest) (service.WriteCommandResult, error) {
	s.called("CreateIssue")
	if s.lastWriteRequest == nil {
		s.lastWriteRequest = map[string]service.WriteCommandRequest{}
	}
	s.lastWriteRequest["CreateIssue"] = req
	return service.WriteCommandResult{Command: "create-issue", Status: "dry_run_valid", IdempotencyKey: firstNonEmpty(req.IdempotencyKey, "key"), GeneratedAt: time.Now()}, nil
}
func (s *spyService) UpdateIssue(_ context.Context, req service.WriteCommandRequest) (service.WriteCommandResult, error) {
	s.called("UpdateIssue")
	if s.lastWriteRequest == nil {
		s.lastWriteRequest = map[string]service.WriteCommandRequest{}
	}
	s.lastWriteRequest["UpdateIssue"] = req
	return service.WriteCommandResult{Command: "update-issue", Status: "dry_run_valid", IdempotencyKey: firstNonEmpty(req.IdempotencyKey, "key"), GeneratedAt: time.Now()}, nil
}
func (s *spyService) CreatePR(_ context.Context, req service.WriteCommandRequest) (service.WriteCommandResult, error) {
	s.called("CreatePR")
	if s.lastWriteRequest == nil {
		s.lastWriteRequest = map[string]service.WriteCommandRequest{}
	}
	s.lastWriteRequest["CreatePR"] = req
	return service.WriteCommandResult{Command: "create-pr", Status: "dry_run_valid", IdempotencyKey: firstNonEmpty(req.IdempotencyKey, "key"), GeneratedAt: time.Now()}, nil
}
func (s *spyService) UpdatePR(_ context.Context, req service.WriteCommandRequest) (service.WriteCommandResult, error) {
	s.called("UpdatePR")
	if s.lastWriteRequest == nil {
		s.lastWriteRequest = map[string]service.WriteCommandRequest{}
	}
	s.lastWriteRequest["UpdatePR"] = req
	return service.WriteCommandResult{Command: "update-pr", Status: "dry_run_valid", IdempotencyKey: firstNonEmpty(req.IdempotencyKey, "key"), GeneratedAt: time.Now()}, nil
}
func (s *spyService) ListMilestones(context.Context, service.MilestoneListRequest) (service.MilestoneListResult, error) {
	s.called("ListMilestones")
	return service.MilestoneListResult{RepoID: "fixture-a", Count: 1, Milestones: []service.MilestoneRecord{{ID: "MILESTONE-1", RemoteID: "1", Title: "M1", State: "open"}}, Evidence: "adapter-confirmed read with cache refresh", GeneratedAt: time.Now()}, nil
}
func (s *spyService) ListPushRemoteMirrors(context.Context, service.PushMirrorListRequest) (service.PushMirrorListResult, error) {
	s.called("ListPushRemoteMirrors")
	return service.PushMirrorListResult{RepoID: "fixture-a", Count: 1, Mirrors: []service.PushMirrorRecord{{ID: "PUSHMIRROR-1", RemoteID: "1", Destination: "https://example.invalid/mirror.git", UpdateStatus: "finished"}}, Evidence: "adapter-confirmed read with sanitized cache refresh", GeneratedAt: time.Now()}, nil
}
func (s *spyService) TriggerPushRemoteMirror(_ context.Context, req service.WriteCommandRequest) (service.WriteCommandResult, error) {
	s.called("TriggerPushRemoteMirror")
	if s.lastWriteRequest == nil {
		s.lastWriteRequest = map[string]service.WriteCommandRequest{}
	}
	s.lastWriteRequest["TriggerPushRemoteMirror"] = req
	return service.WriteCommandResult{Command: "trigger-push-mirror", Status: "succeeded", IdempotencyKey: req.IdempotencyKey, PushMirror: &service.WritePushMirrorReceipt{MirrorID: req.ID, Status: "triggered", TriggeredAt: time.Now()}, GeneratedAt: time.Now()}, nil
}
func (s *spyService) WaitPushRemoteMirror(context.Context, service.PushMirrorWaitRequest) (service.PushMirrorWaitResult, error) {
	s.called("WaitPushRemoteMirror")
	return service.PushMirrorWaitResult{RepoID: "fixture-a", MirrorID: "1", Status: "finished", UpdateStatus: "finished", GeneratedAt: time.Now()}, nil
}
func (s *spyService) CreateMilestone(_ context.Context, req service.WriteCommandRequest) (service.WriteCommandResult, error) {
	s.called("CreateMilestone")
	if s.lastWriteRequest == nil {
		s.lastWriteRequest = map[string]service.WriteCommandRequest{}
	}
	s.lastWriteRequest["CreateMilestone"] = req
	return service.WriteCommandResult{Command: "create-milestone", Status: "dry_run_valid", IdempotencyKey: firstNonEmpty(req.IdempotencyKey, "key"), GeneratedAt: time.Now()}, nil
}
func (s *spyService) UpdateMilestone(_ context.Context, req service.WriteCommandRequest) (service.WriteCommandResult, error) {
	s.called("UpdateMilestone")
	if s.lastWriteRequest == nil {
		s.lastWriteRequest = map[string]service.WriteCommandRequest{}
	}
	s.lastWriteRequest["UpdateMilestone"] = req
	return service.WriteCommandResult{Command: "update-milestone", Status: "dry_run_valid", IdempotencyKey: firstNonEmpty(req.IdempotencyKey, "key"), GeneratedAt: time.Now()}, nil
}
func (s *spyService) SetIssueMilestone(_ context.Context, req service.WriteCommandRequest) (service.WriteCommandResult, error) {
	s.called("SetIssueMilestone")
	if s.lastWriteRequest == nil {
		s.lastWriteRequest = map[string]service.WriteCommandRequest{}
	}
	s.lastWriteRequest["SetIssueMilestone"] = req
	return service.WriteCommandResult{Command: "set-issue-milestone", Status: "dry_run_valid", IdempotencyKey: firstNonEmpty(req.IdempotencyKey, "key"), GeneratedAt: time.Now()}, nil
}
func (s *spyService) ClearIssueMilestone(_ context.Context, req service.WriteCommandRequest) (service.WriteCommandResult, error) {
	s.called("ClearIssueMilestone")
	if s.lastWriteRequest == nil {
		s.lastWriteRequest = map[string]service.WriteCommandRequest{}
	}
	s.lastWriteRequest["ClearIssueMilestone"] = req
	return service.WriteCommandResult{Command: "clear-issue-milestone", Status: "dry_run_valid", IdempotencyKey: firstNonEmpty(req.IdempotencyKey, "key"), GeneratedAt: time.Now()}, nil
}
func (s *spyService) CreatePage(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error) {
	s.called("CreatePage")
	return service.WriteCommandResult{Command: "create-page", Status: "dry_run_valid", IdempotencyKey: "key", GeneratedAt: time.Now()}, nil
}
func (s *spyService) UpdatePage(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error) {
	s.called("UpdatePage")
	return service.WriteCommandResult{Command: "update-page", Status: "dry_run_valid", IdempotencyKey: "key", GeneratedAt: time.Now()}, nil
}
func (s *spyService) DeletePage(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error) {
	s.called("DeletePage")
	return service.WriteCommandResult{Command: "delete-page", Status: "dry_run_valid", IdempotencyKey: "key", GeneratedAt: time.Now()}, nil
}
func (s *spyService) AddComment(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error) {
	s.called("AddComment")
	return service.WriteCommandResult{Command: "add-comment", Status: "dry_run_valid", IdempotencyKey: "key", GeneratedAt: time.Now()}, nil
}
func (s *spyService) AddPRReviewComment(_ context.Context, req service.WriteCommandRequest) (service.WriteCommandResult, error) {
	s.called("AddPRReviewComment")
	if s.lastWriteRequest == nil {
		s.lastWriteRequest = map[string]service.WriteCommandRequest{}
	}
	s.lastWriteRequest["AddPRReviewComment"] = req
	return service.WriteCommandResult{Command: "add-pr-review-comment", Status: "dry_run_valid", IdempotencyKey: "key", GeneratedAt: time.Now()}, nil
}
func (s *spyService) ReplyPRReviewComment(_ context.Context, req service.WriteCommandRequest) (service.WriteCommandResult, error) {
	s.called("ReplyPRReviewComment")
	if s.lastWriteRequest == nil {
		s.lastWriteRequest = map[string]service.WriteCommandRequest{}
	}
	s.lastWriteRequest["ReplyPRReviewComment"] = req
	return service.WriteCommandResult{Command: "reply-pr-review-comment", Status: "dry_run_valid", IdempotencyKey: "key", GeneratedAt: time.Now()}, nil
}
func (s *spyService) UpdateComment(_ context.Context, req service.WriteCommandRequest) (service.WriteCommandResult, error) {
	s.called("UpdateComment")
	s.lastWriteRequest["UpdateComment"] = req
	return service.WriteCommandResult{Command: "update-comment", Status: "dry_run_valid", IdempotencyKey: "key", GeneratedAt: time.Now()}, nil
}
func (s *spyService) AddLabel(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error) {
	s.called("AddLabel")
	return service.WriteCommandResult{Command: "add-label", Status: "dry_run_valid", IdempotencyKey: "key", GeneratedAt: time.Now()}, nil
}
func (s *spyService) PublishRelease(_ context.Context, req service.PublishReleaseRequest) (service.PublishReleaseResult, error) {
	s.called("PublishRelease")
	s.lastReleaseRequest = req
	return service.PublishReleaseResult{Command: "publish-release", Status: "dry_run_valid", RepoID: req.RepoID, Tag: req.Tag, ReleaseStatus: 2, AssetLinks: req.Assets, IdempotencyKey: firstNonEmpty(req.IdempotencyKey, "key"), GeneratedAt: time.Now()}, nil
}

func spyFactory() serviceFactory {
	return func(context.Context, string) (queryService, func() error, error) { return &spyService{}, nil, nil }
}

var _ queryService = (*spyService)(nil)

func TestCommandHelpExitsZero(t *testing.T) {
	commands := []string{
		"sync", "index", "search", "search_sources", "list", "get",
		"get-snippet", "snippet", "snippets", "backlinks", "list-chunks",
		"recent", "link-check", "stale-index", "cache", "cache-status",
		"sync-status", "sync_status", "export", "export-snapshot",
		"diff", "diff-snapshot",
		"create-issue", "update-issue", "create-pr", "create-mr", "update-pr",
		"milestones", "list-push-mirrors", "push-mirrors", "trigger-push-mirror", "wait-push-mirror", "create-milestone", "update-milestone", "set-issue-milestone", "clear-issue-milestone",
		"create-page", "update-page",
		"add-comment", "add-pr-review-comment", "reply-pr-review-comment", "update-comment", "add-label", "publish-release",
		"ingest",
	}
	for _, command := range commands {
		t.Run(command+" --help", func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Execute([]string{command, "--help"}, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
			if !strings.Contains(stdout.String(), command) {
				t.Fatalf("help output missing command name %q in %q", command, stdout.String())
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr must be empty, got %q", stderr.String())
			}
			if strings.Contains(stdout.String(), "invalid_query") {
				t.Fatalf("help output contains invalid_query: %q", stdout.String())
			}
		})
	}
}

func TestCommandHelpShortForm(t *testing.T) {
	commands := []string{"sync", "index", "search"}
	for _, command := range commands {
		t.Run(command+" -h", func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Execute([]string{command, "-h"}, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
			if !strings.Contains(stdout.String(), command) {
				t.Fatalf("help output missing command name %q in %q", command, stdout.String())
			}
		})
	}
}

func TestLocalCommandHelpExitsZero(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"auth --help", []string{"auth", "--help"}},
		{"auth -h", []string{"auth", "-h"}},
		{"config --help", []string{"config", "--help"}},
		{"rag --help", []string{"rag", "--help"}},
		{"rag-status --help", []string{"rag-status", "--help"}},
		{"rag-search --help", []string{"rag-search", "--help"}},
		{"doctor --help", []string{"doctor", "--help"}},
		{"migrate-cache --help", []string{"migrate-cache", "--help"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Execute(tc.args, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
			if !strings.Contains(stdout.String(), tc.args[0]) {
				t.Fatalf("help output missing command name %q in %q", tc.args[0], stdout.String())
			}
		})
	}
}

func TestLocalSubcommandHelpExitsZero(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"config init --help", []string{"config", "init", "--help"}},
		{"config locate --help", []string{"config", "locate", "--help"}},
		{"config show --help", []string{"config", "show", "--help"}},
		{"auth status --help", []string{"auth", "status", "--help"}},
		{"repo add --help", []string{"repo", "add", "--help"}},
		{"repo status --help", []string{"repo", "status", "--help"}},
		{"rag setup --help", []string{"rag", "setup", "--help"}},
		{"rag index --help", []string{"rag", "index", "--help"}},
		{"rag status --help", []string{"rag", "status", "--help"}},
		{"rag search --help", []string{"rag", "search", "--help"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Execute(tc.args, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
			if !strings.Contains(stdout.String(), "Usage") {
				t.Fatalf("help output missing Usage line in %q", stdout.String())
			}
		})
	}
}

func TestRepoAddHelpShowsFlagsAndSupportedScopes(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Execute([]string{"repo", "add", "--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"--owner OWNER", "--name NAME", "[--api-base-url URL]", "defaults to effective config", "--scopes SCOPES", "--alias ALIAS", "issues, wiki, pulls, comments"} {
		if !strings.Contains(out, want) {
			t.Fatalf("repo add help missing %q in %q", want, out)
		}
	}
	if strings.Contains(out, "API base URL (required)") {
		t.Fatalf("repo add help still marks API base URL required: %q", out)
	}
}

func TestBindHelpDocumentsWorkingCompatibilityAlias(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Execute([]string{"bind", "--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"Deprecated compatibility alias", "repo add", "--repo-owner OWNER", "--repo REPO", "defaults to effective config", "defaults to issues,wiki,pulls,comments"} {
		if !strings.Contains(out, want) {
			t.Fatalf("bind help missing %q in %q", want, out)
		}
	}
	if strings.Contains(out, "non-operational") {
		t.Fatalf("bind help still claims the compatibility route is non-operational: %q", out)
	}
}

func TestRepoInitLocalHelpShowsBootstrapFlags(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Execute([]string{"repo", "init-local", "--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"repo init-local", "--owner OWNER", "--name NAME", "--api-base-url URL", "--scopes SCOPES", "--overwrite", "without syncing"} {
		if !strings.Contains(out, want) {
			t.Fatalf("repo init-local help missing %q in %q", want, out)
		}
	}
}

type repoInitLocalSource struct {
	env       map[string]string
	cwd       string
	homeDir   string
	configDir string
	cacheDir  string
}

func (s *repoInitLocalSource) Env(key string) string          { return s.env[key] }
func (s *repoInitLocalSource) UserHomeDir() (string, error)   { return s.homeDir, nil }
func (s *repoInitLocalSource) UserConfigDir() (string, error) { return s.configDir, nil }
func (s *repoInitLocalSource) UserCacheDir() (string, error)  { return s.cacheDir, nil }
func (s *repoInitLocalSource) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}
func (s *repoInitLocalSource) WorkingDir() (string, error) { return s.cwd, nil }
func (s *repoInitLocalSource) Stat(path string) (os.FileInfo, error) {
	return os.Stat(path)
}

func waitForServiceSocket(t *testing.T, src config.Source) {
	t.Helper()
	manager := servicectl.Manager{Source: src}
	client, err := manager.Client()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		var status servicectl.Status
		err := client.Call(context.Background(), "Service.Status", nil, &status)
		if err == nil && status.Status == servicectl.StatusRunning {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("service socket did not become ready: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRepoInitLocalBootstrapsWorktreeCache(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "packages", "agent")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("build/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := &repoInitLocalSource{
		env:       map[string]string{},
		cwd:       nested,
		homeDir:   filepath.Join(root, "home"),
		configDir: filepath.Join(root, "config"),
		cacheDir:  filepath.Join(root, "cache"),
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := executeWithFactoryAndDeps([]string{"repo", "init-local", "--repo", "example-owner/example-repo", "--owner", "example-owner", "--name", "example-repo", "--display-name", "Example Repository", "--api-base-url", "https://api.gitcode.com/api/v5", "--alias", "example"}, &stdout, &stderr, nil, localCommandDeps{Source: src})
	if code != 0 {
		t.Fatalf("code=%d stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	out := stdout.String()
	for _, want := range []string{"config_status: created", "gitignore_updated: true", "binding_status: created", "cache_path: " + filepath.Join(root, ".gitcode", "mcp", "cache.db")} {
		if !strings.Contains(out, want) {
			t.Fatalf("init-local output missing %q in %q", want, out)
		}
	}
	configBytes, err := os.ReadFile(filepath.Join(root, ".gitcode", "gitcode-mcp.yaml"))
	if err != nil {
		t.Fatalf("repo-local config not written: %v", err)
	}
	if strings.TrimSpace(string(configBytes)) != "cache_mode: repo-local" {
		t.Fatalf("unexpected repo-local config: %q", string(configBytes))
	}
	gitignoreBytes, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatalf("gitignore not readable: %v", err)
	}
	if !strings.Contains(string(gitignoreBytes), ".gitcode/mcp/") {
		t.Fatalf("gitignore missing repo-local cache rule: %q", string(gitignoreBytes))
	}
	cachePath := filepath.Join(root, ".gitcode", "mcp", "cache.db")
	store, err := cache.NewSQLiteStore(context.Background(), cachePath)
	if err != nil {
		t.Fatalf("open repo-local cache: %v", err)
	}
	repo, err := store.GetRepository(context.Background(), "example-owner/example-repo")
	if closeErr := store.Close(); closeErr != nil {
		t.Fatalf("close repo-local cache: %v", closeErr)
	}
	if err != nil {
		t.Fatalf("repository binding missing: %v", err)
	}
	if repo.Owner != "example-owner" || repo.Name != "example-repo" || repo.DisplayName != "Example Repository" {
		t.Fatalf("unexpected repository binding: %#v", repo)
	}

	stdout.Reset()
	stderr.Reset()
	code = executeWithFactoryAndDeps([]string{"repo", "init-local", "--repo", "example-owner/example-repo", "--owner", "example-owner", "--name", "example-repo", "--api-base-url", "https://api.gitcode.com/api/v5"}, &stdout, &stderr, nil, localCommandDeps{Source: src})
	if code != 0 {
		t.Fatalf("second init-local code=%d stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), "binding_status: existing") || !strings.Contains(stdout.String(), "gitignore_updated: false") {
		t.Fatalf("second init-local should be idempotent, stdout=%q", stdout.String())
	}
}

func TestAliasCommandHelpExitsZero(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"snippet --help", []string{"snippet", "--help"}},
		{"search_sources --help", []string{"search_sources", "--help"}},
		{"sync_status --help", []string{"sync_status", "--help"}},
		{"export-snapshot --help", []string{"export-snapshot", "--help"}},
		{"diff-snapshot --help", []string{"diff-snapshot", "--help"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Execute(tc.args, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
			if stdout.Len() == 0 {
				t.Fatalf("empty help output")
			}
		})
	}
}

func TestHelpDoesNotCreateService(t *testing.T) {
	factoryCalls := 0
	factory := func(ctx context.Context, path string) (queryService, func() error, error) {
		factoryCalls++
		return &spyService{}, nil, nil
	}
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"sync --help", []string{"sync", "--help"}},
		{"index --help", []string{"index", "--help"}},
		{"search --help", []string{"search", "--help"}},
		{"list --help", []string{"list", "--help"}},
		{"get --help", []string{"get", "--help"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			factoryCalls = 0
			var stdout, stderr bytes.Buffer
			code := executeWithFactory(tc.args, &stdout, &stderr, factory)
			if code != 0 {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
			if factoryCalls != 0 {
				t.Fatalf("service factory was called %d times, want 0", factoryCalls)
			}
		})
	}
}

func TestUnknownCommandErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{"nonexistent", "--help"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("code=%d want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("expected unknown command error, got stderr=%q", stderr.String())
	}
}
