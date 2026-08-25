package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/audit"
	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/diagnostics"
	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/index"
	"gitcode-mcp/internal/rag"
)

func TestNewDelegatesToFixture(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	svc := New(store)
	if got := svc.ProviderMode(); got != gitcode.ProviderModeFixture {
		t.Fatalf("ProviderMode() = %q, want %q", got, gitcode.ProviderModeFixture)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner-a", Name: "repo-a", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SyncToCache(ctx, SyncRequest{RepoID: "fixture-a", RemoteAlias: "issue:42", IdempotencyKey: "new-fixture-sync"}); err != nil {
		t.Fatalf("SyncToCache returned error: %v", err)
	}
	if _, err := store.GetSourceScoped(ctx, "fixture-a", "ISSUE-42"); err != nil {
		t.Fatalf("fixture source missing: %v", err)
	}
}

func TestNewWithModeFixture(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	svc, err := NewWithMode(store, gitcode.ProviderModeFixture, "", ServiceConfig{})
	if err != nil {
		t.Fatalf("NewWithMode fixture returned error: %v", err)
	}
	if got := svc.ProviderMode(); got != gitcode.ProviderModeFixture {
		t.Fatalf("ProviderMode() = %q, want %q", got, gitcode.ProviderModeFixture)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner-a", Name: "repo-a", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SyncToCache(ctx, SyncRequest{RepoID: "fixture-a", RemoteAlias: "wiki:Home", IdempotencyKey: "mode-fixture-sync"}); err != nil {
		t.Fatalf("SyncToCache returned error: %v", err)
	}
	if _, err := store.GetSourceScoped(ctx, "fixture-a", "WIKI-HOME"); err != nil {
		t.Fatalf("fixture wiki source missing: %v", err)
	}
	if _, err := svc.Index(ctx, OperationRequest{RepoID: "fixture-a", Mode: "full"}); err != nil {
		t.Fatalf("Index returned error: %v", err)
	}
	results, err := svc.SearchSources(ctx, SearchSourcesRequest{RepoID: "fixture-a", Query: "test", Limit: 20})
	if err != nil {
		t.Fatalf("fixture search_sources test returned error: %v", err)
	}
	if len(results.Results) == 0 {
		t.Fatalf("fixture search_sources test returned no results")
	}
}

func TestScenario005ServiceSanitizedFixtureBoundary(t *testing.T) {
	client := sanitizedFixtureClient{}
	if !gitcode.IsFixtureBoundary(client) {
		t.Fatalf("sanitized fixture client does not expose fixture boundary")
	}
	markers := client.FixtureMarkerIDs()
	if len(markers) != 2 || markers[0] != gitcode.FixtureIssueMarker || markers[1] != gitcode.FixtureWikiMarker {
		t.Fatalf("fixture markers = %#v", markers)
	}
	ctx := context.Background()
	tests := []struct {
		name string
		run  func() error
	}{
		{name: "create-issue", run: func() error {
			_, err := client.CreateIssue(ctx, gitcode.CreateIssueRequest{}, gitcode.WriteOptions{})
			return err
		}},
		{name: "update-issue", run: func() error {
			_, err := client.UpdateIssue(ctx, gitcode.UpdateIssueRequest{}, gitcode.WriteOptions{})
			return err
		}},
		{name: "create-comment", run: func() error {
			_, err := client.CreateIssueComment(ctx, gitcode.CreateIssueCommentRequest{}, gitcode.WriteOptions{})
			return err
		}},
		{name: "create-pr", run: func() error {
			_, err := client.CreatePR(ctx, gitcode.CreatePRRequest{}, gitcode.WriteOptions{})
			return err
		}},
		{name: "update-pr", run: func() error {
			_, err := client.UpdatePR(ctx, gitcode.UpdatePRRequest{}, gitcode.WriteOptions{})
			return err
		}},
		{name: "merge-pr", run: func() error {
			_, err := client.MergePR(ctx, gitcode.MergePRRequest{}, gitcode.WriteOptions{})
			return err
		}},
		{name: "create-pr-comment", run: func() error {
			_, err := client.CreatePRComment(ctx, gitcode.CreatePRCommentRequest{}, gitcode.WriteOptions{})
			return err
		}},
		{name: "create-wiki", run: func() error {
			_, err := client.CreateWikiPage(ctx, gitcode.CreateWikiPageRequest{}, gitcode.WriteOptions{})
			return err
		}},
		{name: "update-wiki", run: func() error {
			_, err := client.UpdateWikiPage(ctx, gitcode.UpdateWikiPageRequest{}, gitcode.WriteOptions{})
			return err
		}},
		{name: "add-label", run: func() error {
			_, err := client.AddLabel(ctx, gitcode.LabelRequest{}, gitcode.WriteOptions{})
			return err
		}},
		{name: "remove-label", run: func() error {
			_, err := client.RemoveLabel(ctx, gitcode.LabelRequest{}, gitcode.WriteOptions{})
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.run(); !gitcode.IsFixtureReadOnly(err) {
				t.Fatalf("expected fixture read-only classification, got %T %v", err, err)
			}
		})
	}
}

func TestNewWithModeLive(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	base := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "missing authorization", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v5/repos/owner-a/repo-a/issues/42":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"remote-42","number":42,"title":"Live Issue","body":"live issue body","state":"open","created_at":"` + base.Format(time.RFC3339) + `","updated_at":"` + base.Format(time.RFC3339) + `"}`))
		case "/api/v5/repos/owner-a/repo-a/issues/42/comments":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		default:
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	defer server.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner-a", Name: "repo-a", APIBaseURL: server.URL, Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	svc, err := NewWithMode(store, gitcode.ProviderModeLive, "test-token", ServiceConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewWithMode live returned error: %v", err)
	}
	if got := svc.ProviderMode(); got != gitcode.ProviderModeLive {
		t.Fatalf("ProviderMode() = %q, want %q", got, gitcode.ProviderModeLive)
	}
	if _, err := svc.SyncToCache(ctx, SyncRequest{RepoID: "fixture-a", RemoteAlias: "issue:42", IdempotencyKey: "mode-live-sync"}); err != nil {
		t.Fatalf("SyncToCache returned error: %v", err)
	}
	source, err := store.GetSourceScoped(ctx, "fixture-a", "ISSUE-REMOTE-42")
	if err != nil {
		t.Fatalf("live source missing: %v", err)
	}
	if source.Title != "Live Issue" {
		t.Fatalf("source title = %q, want Live Issue", source.Title)
	}
}

func TestS018LiveWriteUsesConstructedLiveClientWithoutEnv(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost || r.URL.Path != "/api/v5/repos/owner-a/repo-a/issues" {
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer live-token" || r.Header.Get("Idempotency-Key") != "live-key-1" {
			http.Error(w, "missing live headers", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"remote-77","number":77,"title":"Live Create","body":"body","state":"open"}`))
	}))
	defer server.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner-a", Name: "repo-a", APIBaseURL: server.URL, Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITCODE_TOKEN", "")
	svc, err := NewWithMode(store, gitcode.ProviderModeLive, "live-token", ServiceConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewWithMode live returned error: %v", err)
	}
	result, err := svc.CreateIssue(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Title: "Live Create", Body: "body", IdempotencyKey: "live-key-1"})
	if err != nil {
		t.Fatalf("CreateIssue live returned error: %v", err)
	}
	if result.Status != "succeeded" || result.RemoteID != "remote-77" || requests != 1 {
		t.Fatalf("result=%#v requests=%d", result, requests)
	}
	if _, err := store.GetRecord(ctx, "fixture-a", "ISSUE-77"); err != nil {
		if _, fallbackErr := store.GetRecord(ctx, "fixture-a", "ISSUE-77"); fallbackErr != nil {
			t.Fatalf("live write did not refresh cache: %v", err)
		}
	}
}

func TestS018LiveWriteConflictMaps409(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"message":"conflict","remote":"existing"}`))
	}))
	defer server.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner-a", Name: "repo-a", APIBaseURL: server.URL, Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	svc, err := NewWithMode(store, gitcode.ProviderModeLive, "live-token", ServiceConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewWithMode live returned error: %v", err)
	}
	_, err = svc.CreateIssue(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Title: "Conflict", IdempotencyKey: "conflict-key-1"})
	var writeErr ErrWriteFailure
	if !errors.As(err, &writeErr) || writeErr.Code != "write_conflict" {
		t.Fatalf("err=%v want write_conflict", err)
	}
	if requests != 1 {
		t.Fatalf("requests=%d want 1", requests)
	}
	counts, err := store.RecordCounts(ctx, "fixture-a")
	if err != nil {
		t.Fatal(err)
	}
	if counts.AuditRows != 1 {
		t.Fatalf("audit rows=%d want 1", counts.AuditRows)
	}
	entry, err := store.GetAuditEventByKey(ctx, "fixture-a", "conflict-key-1")
	if err != nil {
		t.Fatal(err)
	}
	if entry == nil || entry.Status != "failed" || entry.Message != "write_conflict" {
		t.Fatalf("audit entry=%#v", entry)
	}
}

func TestNewWithModeUnavailable(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if svc, err := NewWithMode(store, gitcode.ProviderModeUnavailable, "", ServiceConfig{}); svc != nil || !gitcode.IsProviderUnavailable(err) {
		t.Fatalf("NewWithMode unavailable svc=%#v err=%v, want provider unavailable", svc, err)
	}
	if svc, err := NewWithMode(store, gitcode.ProviderModeLive, "", ServiceConfig{}); svc != nil || !gitcode.IsProviderUnavailable(err) {
		t.Fatalf("NewWithMode live without token svc=%#v err=%v, want provider unavailable", svc, err)
	}
}

func TestNewWithClientSetsProviderMode(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	svc := NewWithClient(store, &fakeGitCodeClient{})
	if got := svc.ProviderMode(); got != gitcode.ProviderMode("custom") {
		t.Fatalf("ProviderMode() = %q, want custom", got)
	}
}

func TestRepositoryRegistry(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	svc := New(store)
	svc.now = func() time.Time { return time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC) }
	repo, err := svc.AddRepository(ctx, AddRepositoryRequest{RepoID: "fixture-a", Owner: "owner-a", Name: "repo-a", APIBaseURL: "https://user:pass@example.invalid/api?token=secret&safe=1", Scopes: []string{"issues,wiki", "issues"}, DisplayName: "Fixture A", Aliases: []string{"proj", "proj"}})
	if err != nil {
		t.Fatalf("AddRepository returned error: %v", err)
	}
	if repo.RepoID != "fixture-a" || !reflect.DeepEqual(repo.Scopes, []RepositoryScope{RepositoryScopeIssues, RepositoryScopeWiki}) || !reflect.DeepEqual(repo.Aliases, []string{"proj"}) {
		t.Fatalf("repo = %#v", repo)
	}
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	if err := store.UpsertRecordGraph(ctx, cache.RecordGraph{Record: cache.Record{RepoID: "fixture-a", ID: "ISSUE-7", Type: "issue", Path: "issues/7.md", Title: "Issue 7", Status: "open", ContentHash: "issue-7", Provenance: cache.ProvenanceLive, RemoteType: "issue", RemoteID: "7", CreatedAt: now, UpdatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceRecordComments(ctx, "fixture-a", "ISSUE-7", []cache.RecordComment{{CommentID: "c7", Body: "cached comment", CreatedAt: now, UpdatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertIssueCommentSync(ctx, cache.IssueCommentSync{RepoID: "fixture-a", SourceID: "ISSUE-7", IssueNumber: 7, RemoteID: "7", ExpectedCount: 1, Status: "complete", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	status, err := svc.RepositoryStatus(ctx, RepositoryStatusRequest{RepoID: "fixture-a"})
	if err != nil {
		t.Fatalf("RepositoryStatus returned error: %v", err)
	}
	if status.APIBaseURL != "https://example.invalid/api?safe=1" || status.BindingState != "ready" || status.CacheState != "ready" || status.IndexState != "unknown" {
		t.Fatalf("status = %#v", status)
	}
	if status.BinaryVersion == "" || status.BinaryVersionSource == "" || status.CacheSchemaVersion != cache.CurrentSchemaVersion() || status.ExpectedCacheSchemaVersion != cache.CurrentSchemaVersion() || status.IssueRecords != 1 || status.IssueComments != 1 || status.IssueCommentQueueState != "available" || status.IssueCommentQueue == nil || status.IssueCommentQueue.Complete != 1 || status.IssueCommentQueue.Total != 1 {
		t.Fatalf("status = %#v", status)
	}
	_, err = svc.AddRepository(ctx, AddRepositoryRequest{RepoID: "fixture-a", Owner: "owner-a", Name: "repo-a", APIBaseURL: "https://example.invalid/api", Scopes: []string{"issues"}})
	var conflict ErrConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("duplicate err=%v want ErrConflict", err)
	}
	_, err = svc.AddRepository(ctx, AddRepositoryRequest{RepoID: "fixture-b", Owner: "owner-b", Name: "repo-b", APIBaseURL: "https://example.invalid/api", Scopes: []string{"issues"}, Aliases: []string{"proj"}})
	if !errors.As(err, &conflict) {
		t.Fatalf("alias err=%v want ErrConflict", err)
	}
	missing, err := svc.RepositoryStatus(ctx, RepositoryStatusRequest{RepoID: "repo-a"})
	var notFound ErrNotFound
	if !errors.As(err, &notFound) || notFound.Kind != "repository" {
		t.Fatalf("missing err=%v want repository ErrNotFound", err)
	}
	if missing.BindingState != "missing" || missing.RepoID != "repo-a" || missing.SuggestedRepoID != "fixture-a" || !reflect.DeepEqual(missing.AvailableBindings, []string{"fixture-a"}) {
		t.Fatalf("missing status = %#v", missing)
	}
}

func TestRepositoryBindingHintsAreBoundedAndUnambiguous(t *testing.T) {
	repos := []cache.RepositoryBinding{
		{RepoID: "org/z", Name: "shared"},
		{RepoID: "org/a", Name: "shared"},
		{RepoID: "org/b", Name: "other", Aliases: []string{"short"}},
		{RepoID: "org/c", Name: "c"},
	}
	available, suggested := repositoryBindingHints("shared", repos, 3)
	if suggested != "" {
		t.Fatalf("ambiguous suggestion = %q", suggested)
	}
	if !reflect.DeepEqual(available, []string{"org/a", "org/b", "org/c"}) {
		t.Fatalf("bounded bindings = %#v", available)
	}
	_, suggested = repositoryBindingHints("SHORT", repos, 8)
	if suggested != "org/b" {
		t.Fatalf("alias suggestion = %q", suggested)
	}
}

func TestRepositoryStatusPreservesBindingRegistryFailure(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	want := cache.ErrCacheCorruption{Path: "/private/cache.db", Detail: "aliases table is malformed"}
	svc := New(&listRepositoriesFailStore{Store: store, err: want})
	status, err := svc.RepositoryStatus(ctx, RepositoryStatusRequest{RepoID: "missing"})
	if status.BindingState != "missing" {
		t.Fatalf("status=%+v", status)
	}
	var corruption cache.ErrCacheCorruption
	if !errors.As(err, &corruption) {
		t.Fatalf("err=%v, want cache corruption", err)
	}
	if IsNotFound(err) {
		t.Fatalf("registry failure was collapsed to not found: %v", err)
	}
}

func TestRepositoryCacheState(t *testing.T) {
	for _, tt := range []struct {
		name     string
		detected int
		expected int
		want     string
	}{
		{name: "current", detected: 16, expected: 16, want: "ready"},
		{name: "older", detected: 15, expected: 16, want: "migration_required"},
		{name: "newer", detected: 17, expected: 16, want: "binary_upgrade_required"},
		{name: "unknown", detected: 0, expected: 16, want: "unknown"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := repositoryCacheState(tt.detected, tt.expected); got != tt.want {
				t.Fatalf("repositoryCacheState(%d, %d) = %q, want %q", tt.detected, tt.expected, got, tt.want)
			}
		})
	}
}

func TestWriteDryRunNoMutation(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedStore(t, ctx, store)
	client := &fakeGitCodeClient{}
	svc := NewWithClient(store, client)
	result, err := svc.CreateIssue(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeDryRun, Title: "T", Body: "B"})
	if err != nil {
		t.Fatalf("CreateIssue dry-run returned error: %v", err)
	}
	if result.Status != "dry_run_valid" || result.RepoID != "fixture-a" || result.IdempotencyKey == "" || client.createIssueCalls != 0 {
		t.Fatalf("dry-run result=%#v calls=%d", result, client.createIssueCalls)
	}
	counts, err := store.RecordCounts(ctx, "fixture-a")
	if err != nil {
		t.Fatal(err)
	}
	if counts.AuditRows != 0 {
		t.Fatalf("audit rows=%d want 0", counts.AuditRows)
	}
}

func TestScenario007WriteLiveCreateAuditCacheConfirmation(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedStore(t, ctx, store)
	client := &fakeGitCodeClient{createIssueResult: gitcode.WriteResult[gitcode.Issue]{Record: gitcode.Issue{ID: "remote-77", Number: 77, Title: "Live Create", Body: "body", State: "open"}, Confirmed: true, Operation: "CreateIssue", RemoteID: "remote-77", RemoteNumber: 77, ConfirmedAt: time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)}}
	service := &Service{store: store, client: client, now: func() time.Time { return time.Now().UTC() }, providerMode: gitcode.ProviderModeLive, writeCredentialPresent: true}
	t.Setenv("GITCODE_TOKEN", "")
	result, err := service.CreateIssue(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Title: "Live Create", Body: "body", IdempotencyKey: "scenario-007-key"})
	if err != nil {
		t.Fatalf("CreateIssue live returned error: %v", err)
	}
	if result.Status != "succeeded" || result.RemoteID != "remote-77" || client.createIssueCalls != 1 {
		t.Fatalf("result=%#v calls=%d", result, client.createIssueCalls)
	}
	if client.lastCreateIssueRequest.Owner != "owner-a" || client.lastCreateIssueRequest.Repo != "repo-a" || client.lastCreateIssueRequest.Title != "Live Create" || client.lastWriteOptions.IdempotencyKey != "scenario-007-key" {
		t.Fatalf("request=%#v opts=%#v", client.lastCreateIssueRequest, client.lastWriteOptions)
	}
	entry, err := store.GetAuditEventByKey(ctx, "fixture-a", "scenario-007-key")
	if err != nil {
		t.Fatal(err)
	}
	if entry == nil || entry.Status != "succeeded" || entry.Command != "create-issue" || entry.Mode != "live" || entry.RemoteID != "remote-77" || entry.PayloadHash == "" || strings.Contains(entry.Message, "test-token") {
		t.Fatalf("audit entry=%#v", entry)
	}
	if entry.RequestMetadata["method"] != "POST" || entry.RequestMetadata["provider_mode"] != "live" || entry.RequestMetadata["idempotency_key"] != "scenario-007-key" {
		t.Fatalf("audit metadata=%#v", entry.RequestMetadata)
	}
	confirmation, err := store.GetCacheConfirmationByKey(ctx, "fixture-a", "scenario-007-key")
	if err != nil {
		t.Fatal(err)
	}
	if confirmation == nil || confirmation.RecordID != "ISSUE-77" || confirmation.RemoteID != "remote-77" || confirmation.IdempotencyKey != "scenario-007-key" {
		t.Fatalf("cache confirmation=%#v", confirmation)
	}
	if _, err := store.GetRecord(ctx, "fixture-a", "ISSUE-77"); err != nil {
		t.Fatalf("cache confirmation missing: %v", err)
	}
}

func TestScenario007WriteLiveCreateIdempotentReplay(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedStore(t, ctx, store)
	client := &fakeGitCodeClient{createIssueResult: gitcode.WriteResult[gitcode.Issue]{Record: gitcode.Issue{ID: "remote-78", Number: 78, Title: "Replay", Body: "body", State: "open"}, Confirmed: true, Operation: "CreateIssue", RemoteID: "78", RemoteNumber: 78, ConfirmedAt: time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)}}
	service := &Service{store: store, client: client, now: func() time.Time { return time.Now().UTC() }, providerMode: gitcode.ProviderModeLive, writeCredentialPresent: true}
	req := WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Title: "Replay", Body: "body", IdempotencyKey: "scenario-007-replay"}
	if _, err := service.CreateIssue(ctx, req); err != nil {
		t.Fatalf("CreateIssue live returned error: %v", err)
	}
	replay, err := service.CreateIssue(ctx, req)
	if err != nil {
		t.Fatalf("CreateIssue replay returned error: %v", err)
	}
	if replay.Status != "already_applied" || !replay.Replayed || client.createIssueCalls != 1 {
		t.Fatalf("replay=%#v calls=%d", replay, client.createIssueCalls)
	}
	confirmation, err := store.GetCacheConfirmationByKey(ctx, "fixture-a", "scenario-007-replay")
	if err != nil {
		t.Fatal(err)
	}
	if confirmation == nil || confirmation.RecordID != "ISSUE-78" || confirmation.RemoteID != "78" {
		t.Fatalf("cache confirmation=%#v", confirmation)
	}
}

func TestScenario007WriteLiveCreateIdempotencyConflict(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedStore(t, ctx, store)
	client := &fakeGitCodeClient{createIssueResult: gitcode.WriteResult[gitcode.Issue]{Record: gitcode.Issue{ID: "remote-79", Number: 79, Title: "Conflict", Body: "body", State: "open"}, Confirmed: true, Operation: "CreateIssue", RemoteID: "79", RemoteNumber: 79, ConfirmedAt: time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)}}
	service := &Service{store: store, client: client, now: func() time.Time { return time.Now().UTC() }, providerMode: gitcode.ProviderModeLive, writeCredentialPresent: true}
	if _, err := service.CreateIssue(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Title: "Conflict", Body: "body", IdempotencyKey: "scenario-007-conflict"}); err != nil {
		t.Fatalf("CreateIssue live returned error: %v", err)
	}
	_, err = service.CreateIssue(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Title: "Changed", Body: "body", IdempotencyKey: "scenario-007-conflict"})
	var writeErr ErrWriteFailure
	if !errors.As(err, &writeErr) || writeErr.Code != "write_idempotency_conflict" || client.createIssueCalls != 1 {
		t.Fatalf("err=%v calls=%d", err, client.createIssueCalls)
	}
}

func TestScenario007WriteLiveFixtureFallbackDetected(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedStore(t, ctx, store)
	service := &Service{store: store, client: sanitizedFixtureClient{}, now: func() time.Time { return time.Now().UTC() }, providerMode: gitcode.ProviderModeLive, writeCredentialPresent: true}
	_, err = service.CreateIssue(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Title: "Fixture fallback", IdempotencyKey: "scenario-007-fixture"})
	var writeErr ErrWriteFailure
	if !errors.As(err, &writeErr) || writeErr.Code != "write_fixture_fallback_detected" {
		t.Fatalf("err=%v", err)
	}
	if strings.Contains(err.Error(), "fixture client is read-only") {
		t.Fatalf("forbidden fixture text leaked: %v", err)
	}
	entry, err := store.GetAuditEventByKey(ctx, "fixture-a", "scenario-007-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if entry == nil || entry.Message != "write_fixture_fallback_detected" {
		t.Fatalf("audit entry=%#v", entry)
	}
}

func TestWriteLiveSuccessAuditCacheAndReplay(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedStore(t, ctx, store)
	client := &fakeGitCodeClient{createIssueResult: gitcode.WriteResult[gitcode.Issue]{Record: gitcode.Issue{ID: "remote-42", Number: 42, Title: "T", Body: "B", State: "open"}, Confirmed: true, Operation: "CreateIssue", RemoteID: "42", RemoteNumber: 42, ConfirmedAt: time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)}}
	svc := NewWithClient(store, client)
	t.Setenv("GITCODE_TOKEN", "test-token")
	request := WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Title: "T", Body: "B", IdempotencyKey: "key-1"}
	result, err := svc.CreateIssue(ctx, request)
	if err != nil {
		t.Fatalf("CreateIssue live returned error: %v", err)
	}
	if result.Status != "succeeded" || result.RemoteID != "42" || result.ID != "ISSUE-42" || result.StableSourceID != "ISSUE-42" || result.IssueNumber != 42 || client.createIssueCalls != 1 {
		t.Fatalf("live result=%#v calls=%d", result, client.createIssueCalls)
	}
	counts, err := store.RecordCounts(ctx, "fixture-a")
	if err != nil {
		t.Fatal(err)
	}
	if counts.AuditRows != 1 {
		t.Fatalf("audit rows=%d want 1", counts.AuditRows)
	}
	confirmation, err := store.GetCacheConfirmationByKey(ctx, "fixture-a", "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if confirmation == nil || confirmation.RecordID != "ISSUE-42" || confirmation.RemoteID != "42" {
		t.Fatalf("cache confirmation=%#v", confirmation)
	}
	if _, err := store.GetRecord(ctx, "fixture-a", "ISSUE-42"); err != nil {
		t.Fatalf("refreshed record missing: %v", err)
	}
	replay, err := svc.CreateIssue(ctx, request)
	if err != nil {
		t.Fatalf("CreateIssue replay returned error: %v", err)
	}
	if replay.Status != "already_applied" || !replay.Replayed || replay.StableSourceID != "ISSUE-42" || replay.IssueNumber != 42 || client.createIssueCalls != 1 {
		t.Fatalf("replay=%#v calls=%d", replay, client.createIssueCalls)
	}
}

func TestIssueWriteTargetResolutionIsExplicitAndCacheFirst(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "identity-write", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	issue := cache.Source{RepoID: "identity-write", ID: "ISSUE-4226802", Kind: "issue", Path: "issues/76.md", Title: "Identity", Body: "body", Status: "open", ContentHash: "identity"}
	identities := []cache.Identity{
		{RepoID: "identity-write", SourceID: issue.ID, AliasType: "issue", Alias: "76", Remote: cache.RemoteAlias{Type: "issue", ID: "76"}},
		{RepoID: "identity-write", SourceID: issue.ID, AliasType: "gitcode_issue_id", Alias: "4226802", Remote: cache.RemoteAlias{Type: "gitcode_issue_id", ID: "4226802"}},
	}
	if err := store.UpsertSourceGraph(ctx, cache.SourceGraph{Source: issue, Identities: identities}); err != nil {
		t.Fatal(err)
	}
	client := &fakeGitCodeClient{}
	svc := NewWithClient(store, client)

	for _, selector := range []string{"ISSUE-4226802", "issue:76", "gitcode_issue_id:4226802"} {
		t.Run(selector, func(t *testing.T) {
			result, err := svc.AddComment(ctx, WriteCommandRequest{RepoID: "identity-write", Mode: WriteModeDryRun, IssueID: selector, Body: "comment", IdempotencyKey: "resolve-" + strings.ReplaceAll(selector, ":", "-")})
			if err != nil {
				t.Fatal(err)
			}
			if result.IssueNumber != 76 || result.StableSourceID != "ISSUE-4226802" || result.ID != "ISSUE-4226802" {
				t.Fatalf("result=%#v", result)
			}
		})
	}

	result, err := svc.AddComment(ctx, WriteCommandRequest{RepoID: "identity-write", Mode: WriteModeDryRun, Number: 76, Body: "comment", IdempotencyKey: "number-76"})
	if err != nil || result.IssueNumber != 76 || result.StableSourceID != "ISSUE-4226802" {
		t.Fatalf("number result=%#v err=%v", result, err)
	}

	tests := []struct {
		name string
		req  WriteCommandRequest
		want string
	}{
		{name: "provider id passed as number", req: WriteCommandRequest{Number: 4226802}, want: "cached GitCode provider id"},
		{name: "conflicting selectors", req: WriteCommandRequest{Number: 77, IssueID: "ISSUE-4226802"}, want: "resolve to different issues"},
		{name: "unknown stable id", req: WriteCommandRequest{IssueID: "ISSUE-999"}, want: "sync the issue first"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.req.RepoID = "identity-write"
			test.req.Mode = WriteModeDryRun
			test.req.Body = "comment"
			test.req.IdempotencyKey = "invalid-" + test.name
			_, err := svc.AddComment(ctx, test.req)
			var invalid ErrInvalidQuery
			if !errors.As(err, &invalid) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err=%T %v, want invalid_query containing %q", err, err, test.want)
			}
		})
	}

	confirmedAt := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	client.createIssueCommentResult = gitcode.WriteResult[gitcode.Comment]{
		Record:            gitcode.Comment{ID: "comment-76", IssueID: "4226802", Body: "same comment", Author: "agent", CreatedAt: confirmedAt, UpdatedAt: confirmedAt},
		Confirmed:         true,
		Operation:         "CreateIssueComment",
		RemoteID:          "comment-76",
		ParentIssueNumber: 76,
		ParentIssueID:     "4226802",
		ConfirmedAt:       confirmedAt,
	}
	svc.providerMode = gitcode.ProviderModeLive
	svc.writeCredentialPresent = true
	legacy, err := svc.AddComment(ctx, WriteCommandRequest{RepoID: "identity-write", Mode: WriteModeLive, ID: "ISSUE-4226802", Body: "same comment", IdempotencyKey: "selector-form-replay"})
	if err != nil {
		t.Fatalf("legacy selector write: %v", err)
	}
	canonical, err := svc.AddComment(ctx, WriteCommandRequest{RepoID: "identity-write", Mode: WriteModeLive, IssueID: "ISSUE-4226802", Body: "same comment", IdempotencyKey: "selector-form-replay"})
	if err != nil {
		t.Fatalf("canonical selector replay: %v", err)
	}
	if legacy.SourceFingerprint != canonical.SourceFingerprint || canonical.Status != "already_applied" || !canonical.Replayed || client.createIssueCommentCalls != 1 {
		t.Fatalf("legacy=%#v canonical=%#v adapter_calls=%d", legacy, canonical, client.createIssueCommentCalls)
	}

	legitimate := cache.Source{RepoID: "identity-write", ID: "ISSUE-LARGE-NUMBER", Kind: "issue", Path: "issues/4226802.md", Title: "Large number", Body: "body", Status: "open", ContentHash: "large-number"}
	if err := store.UpsertSourceGraph(ctx, cache.SourceGraph{Source: legitimate, Identities: []cache.Identity{{RepoID: "identity-write", SourceID: legitimate.ID, AliasType: "issue", Alias: "4226802", Remote: cache.RemoteAlias{Type: "issue", ID: "4226802"}}}}); err != nil {
		t.Fatal(err)
	}
	largeNumber, err := svc.AddComment(ctx, WriteCommandRequest{RepoID: "identity-write", Mode: WriteModeDryRun, Number: 4226802, Body: "comment", IdempotencyKey: "legitimate-large-number"})
	if err != nil || largeNumber.IssueNumber != 4226802 || largeNumber.StableSourceID != "ISSUE-LARGE-NUMBER" {
		t.Fatalf("legitimate large issue number result=%#v err=%v", largeNumber, err)
	}

	partialStore := &writeRefreshFailStore{Store: store, failNextRefresh: true}
	partialClient := &fakeGitCodeClient{createIssueCommentResult: gitcode.WriteResult[gitcode.Comment]{
		Record:            gitcode.Comment{ID: "partial-comment-76", IssueID: "4226802", Body: "partial replay", CreatedAt: confirmedAt, UpdatedAt: confirmedAt},
		Confirmed:         true,
		Operation:         "CreateIssueComment",
		RemoteID:          "partial-comment-76",
		ParentIssueNumber: 76,
		ParentIssueID:     "4226802",
		ConfirmedAt:       confirmedAt,
	}}
	partialSvc := NewWithClient(partialStore, partialClient)
	partialSvc.providerMode = gitcode.ProviderModeLive
	partialSvc.writeCredentialPresent = true
	partialReq := WriteCommandRequest{RepoID: "identity-write", Mode: WriteModeLive, Number: 76, Body: "partial replay", IdempotencyKey: "partial-comment-replay"}
	if _, err := partialSvc.AddComment(ctx, partialReq); err == nil {
		t.Fatal("first partial comment write unexpectedly succeeded")
	}
	if result, err := partialSvc.AddComment(ctx, partialReq); err != nil || result.Status != "succeeded" || !result.Replayed || partialClient.createIssueCommentCalls != 1 {
		t.Fatalf("partial replay result=%#v calls=%d err=%v", result, partialClient.createIssueCommentCalls, err)
	}
	if _, err := store.ResolveAliasScoped(ctx, "identity-write", cache.RemoteAlias{Type: "gitcode_issue_id", ID: "ISSUE-4226802"}); err == nil {
		t.Fatal("partial replay created a stable id as a provider alias")
	}
	identity, err := store.ResolveAliasScoped(ctx, "identity-write", cache.RemoteAlias{Type: "gitcode_issue_id", ID: "4226802"})
	if err != nil || identity.SourceID != "ISSUE-4226802" {
		t.Fatalf("canonical provider identity=%#v err=%v", identity, err)
	}
}

func TestWritePartialCacheRefreshRetryUsesAuditWithoutSecondAdapterCall(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedStore(t, ctx, store)
	wrapped := &writeRefreshFailStore{Store: store, failNextRefresh: true}
	client := &fakeGitCodeClient{createIssueResult: gitcode.WriteResult[gitcode.Issue]{Record: gitcode.Issue{ID: "remote-45", Number: 45, Title: "T", Body: "B", State: "open"}, Confirmed: true, Operation: "CreateIssue", RemoteID: "45", RemoteNumber: 45, RemoteRevision: "rev-45", ConfirmedAt: time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)}}
	svc := NewWithClient(wrapped, client)
	t.Setenv("GITCODE_TOKEN", "test-token")
	req := WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Title: "T", Body: "B", IdempotencyKey: "partial-cache"}
	_, err = svc.CreateIssue(ctx, req)
	var writeErr ErrWriteFailure
	if !errors.As(err, &writeErr) || writeErr.Code != "write_partial_cache_refresh_failed" {
		t.Fatalf("first write err=%v want partial cache failure", err)
	}
	result, err := svc.CreateIssue(ctx, req)
	if err != nil {
		t.Fatalf("retry returned error: %v", err)
	}
	if result.Status != "succeeded" || !result.Replayed || client.createIssueCalls != 1 {
		t.Fatalf("retry result=%#v calls=%d want replay success without adapter", result, client.createIssueCalls)
	}
	if _, err := store.GetRecord(ctx, "fixture-a", "ISSUE-45"); err != nil {
		t.Fatalf("retry did not refresh record: %v", err)
	}
}

func TestWriteLiveMissingToken(t *testing.T) {
	ctx := context.Background()
	svc := seededSyncService(t, ctx, &fakeGitCodeClient{})
	_, err := svc.CreateIssue(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Title: "T"})
	var writeErr ErrWriteFailure
	if !errors.As(err, &writeErr) || writeErr.Code != "write_missing_credential" {
		t.Fatalf("missing token err=%v", err)
	}
}

func TestUpdateIssueValidatesPublicStateBeforeDryRun(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedStore(t, ctx, store)
	client := &fakeGitCodeClient{}
	svc := NewWithClient(store, client)

	_, err = svc.UpdateIssue(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeDryRun, Number: 1, State: "close"})
	var invalid ErrInvalidQuery
	if !errors.As(err, &invalid) || invalid.Field != "state" {
		t.Fatalf("error=%T %v, want state ErrInvalidQuery", err, err)
	}
	if client.updateIssueCalls != 0 || client.issueCalls != 0 {
		t.Fatalf("invalid dry-run called adapter: update=%d get=%d", client.updateIssueCalls, client.issueCalls)
	}
}

func TestUpdateIssueStateLiveCachesReadbackAndReplaysIdempotently(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedStore(t, ctx, store)
	updatedAt := time.Date(2026, 7, 13, 23, 40, 14, 0, time.UTC)
	client := &fakeGitCodeClient{
		updateIssueResult: gitcode.WriteResult[gitcode.Issue]{
			Record:    gitcode.Issue{ID: "1", Number: 1, Title: "Issue 1", Body: "Preserved body", State: "closed", Labels: []string{"bug"}, UpdatedAt: updatedAt},
			Confirmed: true, Operation: "UpdateIssue", RemoteID: "1", RemoteNumber: 1, RemoteRevision: updatedAt.Format(time.RFC3339Nano), ConfirmedAt: updatedAt,
		},
	}
	svc := NewWithClient(store, client)
	t.Setenv("GITCODE_TOKEN", "test-token")
	request := WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Number: 1, State: "closed", IdempotencyKey: "issue-state-close-key"}

	result, err := svc.UpdateIssue(ctx, request)
	if err != nil {
		t.Fatalf("UpdateIssue live: %v", err)
	}
	if result.Status != "succeeded" || result.RemoteNumber != 1 || client.updateIssueCalls != 1 {
		t.Fatalf("result=%#v calls=%d", result, client.updateIssueCalls)
	}
	if client.lastUpdateIssueRequest.State != "closed" || client.lastUpdateIssueRequest.Title != "" || client.lastUpdateIssueRequest.Body != "" || len(client.lastUpdateIssueRequest.Labels) != 0 {
		t.Fatalf("service update request=%#v", client.lastUpdateIssueRequest)
	}
	record, err := store.GetRecord(ctx, "fixture-a", "ISSUE-1")
	if err != nil {
		t.Fatalf("get cached issue: %v", err)
	}
	if record.Status != "closed" || record.Title != "Issue 1" || record.Body != "Preserved body" || !reflect.DeepEqual(record.Labels, []string{"bug"}) {
		t.Fatalf("cached record=%#v", record)
	}

	replay, err := svc.UpdateIssue(ctx, request)
	if err != nil {
		t.Fatalf("UpdateIssue replay: %v", err)
	}
	if replay.Status != "already_applied" || !replay.Replayed || client.updateIssueCalls != 1 {
		t.Fatalf("replay=%#v calls=%d", replay, client.updateIssueCalls)
	}
}

func TestAddLabelDryRunNoMutation(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedStore(t, ctx, store)
	client := &fakeGitCodeClient{}
	svc := NewWithClient(store, client)
	result, err := svc.AddLabel(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeDryRun, Number: 1, Label: "bug"})
	if err != nil {
		t.Fatalf("AddLabel dry-run: %v", err)
	}
	if result.Status != "dry_run_valid" {
		t.Fatalf("AddLabel dry-run status=%q, want dry_run_valid", result.Status)
	}
	if client.addLabelCalls != 0 || client.updateIssueCalls != 0 || client.issueCalls != 0 {
		t.Fatalf("dry-run should not call client, got add=%d update=%d get=%d", client.addLabelCalls, client.updateIssueCalls, client.issueCalls)
	}
}

func TestAddLabelLiveUsesUpdateIssueWithMergedLabels(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedStore(t, ctx, store)
	updatedAt := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		issue: gitcode.Issue{ID: "1", Number: 1, Title: "Issue 1", Body: "B", State: "open", Labels: []string{"bug"}, UpdatedAt: updatedAt},
		updateIssueResult: gitcode.WriteResult[gitcode.Issue]{
			Record:       gitcode.Issue{ID: "1", Number: 1, Title: "Issue 1", Body: "B", State: "open", Labels: []string{"bug", "triage"}, UpdatedAt: updatedAt},
			Confirmed:    true,
			Operation:    "UpdateIssue",
			RemoteID:     "1",
			RemoteNumber: 1,
			ConfirmedAt:  updatedAt,
		},
	}
	svc := NewWithClient(store, client)
	t.Setenv("GITCODE_TOKEN", "test-token")
	request := WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Number: 1, Label: "triage", IdempotencyKey: "label-key-1"}
	result, err := svc.AddLabel(ctx, request)
	if err != nil {
		t.Fatalf("AddLabel live: %v", err)
	}
	if result.Status != "succeeded" || result.RemoteNumber != 1 {
		t.Fatalf("AddLabel live result=%#v", result)
	}
	if client.issueCalls != 1 || client.updateIssueCalls != 1 || client.addLabelCalls != 0 {
		t.Fatalf("calls get=%d update=%d add=%d", client.issueCalls, client.updateIssueCalls, client.addLabelCalls)
	}
	var labels string
	if err := json.Unmarshal(client.lastUpdateIssueRequest.Labels, &labels); err != nil {
		t.Fatalf("decode update labels: %v", err)
	}
	if labels != "bug,triage" {
		t.Fatalf("labels=%q, want bug,triage", labels)
	}
	if client.lastWriteOptions.IdempotencyKey != "label-key-1" {
		t.Fatalf("idempotency key=%q", client.lastWriteOptions.IdempotencyKey)
	}
}

func TestAddLabelLiveNoopWhenLabelAlreadyExists(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedStore(t, ctx, store)
	updatedAt := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		issue: gitcode.Issue{ID: "1", Number: 1, Title: "Issue 1", Body: "B", State: "open", Labels: []string{"bug", "triage"}, UpdatedAt: updatedAt},
	}
	svc := NewWithClient(store, client)
	t.Setenv("GITCODE_TOKEN", "test-token")
	result, err := svc.AddLabel(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Number: 1, Label: "triage", IdempotencyKey: "label-key-noop"})
	if err != nil {
		t.Fatalf("AddLabel no-op: %v", err)
	}
	if result.Status != "succeeded" || result.RemoteNumber != 1 {
		t.Fatalf("AddLabel no-op result=%#v", result)
	}
	if client.issueCalls != 1 || client.updateIssueCalls != 0 || client.addLabelCalls != 0 {
		t.Fatalf("calls get=%d update=%d add=%d", client.issueCalls, client.updateIssueCalls, client.addLabelCalls)
	}
	record, err := store.GetRecord(ctx, "fixture-a", "ISSUE-1")
	if err != nil {
		t.Fatalf("get cached issue: %v", err)
	}
	if !reflect.DeepEqual(record.Labels, []string{"bug", "triage"}) {
		t.Fatalf("cached labels=%v", record.Labels)
	}
}

func TestListPushRemoteMirrorsSortsAndRefreshesSanitizedCache(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "example-owner", Name: "example-repo", APIBaseURL: "https://api.example.invalid/api/v5", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	client := &fakeGitCodeClient{pushMirrorsResult: []gitcode.PushMirror{
		{RemoteID: "10", ProjectID: "101", URL: "https://mirror.example.invalid/ten.git", UpdateStatus: "failed", NumberOfFailures: 2, Message: "remote unavailable", CreatedAt: "2026-07-29T09:00:00Z", LastUpdateAt: "2026-07-29T10:00:00Z"},
		{RemoteID: "2", ProjectID: "101", URL: "https://mirror.example.invalid/two.git", Force: true, Private: true, UpdateStatus: "finished", CreatedAt: "2026-07-29T08:00:00Z", LastSuccessfulUpdateAt: "2026-07-29T11:00:00Z"},
	}}
	svc := NewWithClient(store, client)
	svc.now = func() time.Time { return time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC) }

	result, err := svc.ListPushRemoteMirrors(ctx, PushMirrorListRequest{RepoID: "fixture-a"})
	if err != nil {
		t.Fatalf("ListPushRemoteMirrors: %v", err)
	}
	if client.lastPushMirrorsRequest.Owner != "example-owner" || client.lastPushMirrorsRequest.Repo != "example-repo" {
		t.Fatalf("request=%#v", client.lastPushMirrorsRequest)
	}
	if result.Count != 2 || result.Mirrors[0].RemoteID != "2" || result.Mirrors[1].RemoteID != "10" {
		t.Fatalf("result=%#v", result)
	}
	if result.Evidence != "adapter-confirmed read with sanitized cache refresh" {
		t.Fatalf("evidence=%q", result.Evidence)
	}
	record, err := store.GetRecord(ctx, "fixture-a", "PUSHMIRROR-2")
	if err != nil {
		t.Fatalf("GetRecord: %v", err)
	}
	if record.Type != "push_remote_mirror" || record.RemoteType != "push_remote_mirror" || record.RemoteID != "2" {
		t.Fatalf("record=%#v", record)
	}
	for _, forbidden := range []string{"@", "access_token", "credential", "secret"} {
		if strings.Contains(record.Body, forbidden) {
			t.Fatalf("cached body contains %q: %s", forbidden, record.Body)
		}
	}
	if !strings.Contains(record.Body, "destination: https://mirror.example.invalid/two.git") {
		t.Fatalf("cached body=%q", record.Body)
	}
}

func TestListPushRemoteMirrorsPropagatesProviderFailure(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "example-owner", Name: "example-repo", APIBaseURL: "https://api.example.invalid/api/v5", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	client := &fakeGitCodeClient{pushMirrorsErr: gitcode.ErrForbidden{Endpoint: "/api/v5/repos/example-owner/example-repo/push_remote_mirrors", Status: http.StatusForbidden}}
	svc := NewWithClient(store, client)
	_, err = svc.ListPushRemoteMirrors(ctx, PushMirrorListRequest{RepoID: "fixture-a"})
	var forbidden gitcode.ErrForbidden
	if !errors.As(err, &forbidden) {
		t.Fatalf("error=%T %v", err, err)
	}
}

func TestTriggerPushRemoteMirrorAuditsCachesAndReplays(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "example-owner", Name: "example-repo", APIBaseURL: "https://api.example.invalid/api/v5", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	triggeredAt := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	before := gitcode.PushMirror{RemoteID: "17", ProjectID: "101", URL: "https://mirror.example.invalid/repo.git", UpdateStatus: "finished", LastSuccessfulUpdateAt: "2026-07-30T11:00:00Z"}
	readback := before
	readback.UpdateStatus = "processing"
	readback.LastUpdateAt = "2026-07-30T12:00:00Z"
	client := &fakeGitCodeClient{
		pushMirrorsResult: beforeSlice(before),
		triggerPushMirrorResult: gitcode.WriteResult[gitcode.PushMirror]{
			Record:         readback,
			Confirmed:      true,
			Operation:      "TriggerPushRemoteMirror",
			ProviderStatus: "200-sanitized-readback",
			RemoteID:       "17",
			RemoteRevision: "revision-17",
			ConfirmedAt:    triggeredAt,
		},
	}
	svc := NewWithClient(store, client)
	svc.now = func() time.Time { return triggeredAt }
	t.Setenv("GITCODE_TOKEN", "test-token")
	req := WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, IdempotencyKey: "trigger-17-key"}

	result, err := svc.TriggerPushRemoteMirror(ctx, req)
	if err != nil {
		t.Fatalf("TriggerPushRemoteMirror: %v", err)
	}
	if result.Status != "succeeded" || result.RemoteID != "17" || result.PushMirror == nil {
		t.Fatalf("result=%#v", result)
	}
	if result.PushMirror.PreviousStatus != "finished" || result.PushMirror.ReadbackStatus != "processing" || !result.PushMirror.TriggeredAt.Equal(triggeredAt) {
		t.Fatalf("receipt=%#v", result.PushMirror)
	}
	if client.triggerPushMirrorCalls != 1 || client.lastTriggerPushMirrorRequest.MirrorID != "17" || client.lastWriteOptions.IdempotencyKey != "trigger-17-key" {
		t.Fatalf("calls=%d request=%#v opts=%#v", client.triggerPushMirrorCalls, client.lastTriggerPushMirrorRequest, client.lastWriteOptions)
	}
	entry, err := store.GetAuditEventByKey(ctx, "fixture-a", "trigger-17-key")
	if err != nil {
		t.Fatal(err)
	}
	if entry == nil || entry.Status != audit.StatusSucceeded || entry.RemoteID != "17" || entry.RequestMetadata["push_mirror_previous_status"] != "finished" {
		t.Fatalf("audit=%#v", entry)
	}
	record, err := store.GetRecord(ctx, "fixture-a", "PUSHMIRROR-17")
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != "processing" || strings.Contains(record.Body, "@") || strings.Contains(record.Body, "?") {
		t.Fatalf("record=%#v", record)
	}

	replay, err := svc.TriggerPushRemoteMirror(ctx, req)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !replay.Replayed || replay.Status != "already_applied" || replay.PushMirror == nil || replay.PushMirror.MirrorID != "17" {
		t.Fatalf("replay=%#v", replay)
	}
	if client.triggerPushMirrorCalls != 1 {
		t.Fatalf("trigger calls=%d want 1", client.triggerPushMirrorCalls)
	}
}

func TestTriggerPushRemoteMirrorRequiresSelectionAndKnownMirror(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "example-owner", Name: "example-repo", APIBaseURL: "https://api.example.invalid/api/v5", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	client := &fakeGitCodeClient{pushMirrorsResult: []gitcode.PushMirror{{RemoteID: "1"}, {RemoteID: "2"}}}
	svc := NewWithClient(store, client)
	t.Setenv("GITCODE_TOKEN", "test-token")

	_, err = svc.TriggerPushRemoteMirror(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, IdempotencyKey: "selection-key"})
	var selection ErrPushMirrorSelectionRequired
	if !errors.As(err, &selection) || selection.Count != 2 {
		t.Fatalf("error=%T %v", err, err)
	}
	_, err = svc.TriggerPushRemoteMirror(ctx, WriteCommandRequest{RepoID: "fixture-a", ID: "99", Mode: WriteModeLive, IdempotencyKey: "missing-key"})
	var missing ErrPushMirrorNotFound
	if !errors.As(err, &missing) || missing.MirrorID != "99" {
		t.Fatalf("error=%T %v", err, err)
	}
	if client.triggerPushMirrorCalls != 0 {
		t.Fatalf("trigger calls=%d", client.triggerPushMirrorCalls)
	}
}

func TestTriggerPushRemoteMirrorAmbiguousFailureBlocksSameKeyReplay(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "example-owner", Name: "example-repo", APIBaseURL: "https://api.example.invalid/api/v5", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	client := &fakeGitCodeClient{
		pushMirrorsResult:    []gitcode.PushMirror{{RemoteID: "17", URL: "https://mirror.example.invalid/repo.git", UpdateStatus: "finished"}},
		triggerPushMirrorErr: gitcode.ErrNetworkUnavailable{Endpoint: "/push_remote_mirrors/17", Attempts: 1},
	}
	svc := NewWithClient(store, client)
	t.Setenv("GITCODE_TOKEN", "test-token")
	req := WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, IdempotencyKey: "ambiguous-key"}

	_, err = svc.TriggerPushRemoteMirror(ctx, req)
	var first ErrWriteFailure
	if !errors.As(err, &first) || first.Code != "write_ambiguous_remote" {
		t.Fatalf("first error=%T %v", err, err)
	}
	_, err = svc.TriggerPushRemoteMirror(ctx, req)
	var replay ErrWriteFailure
	if !errors.As(err, &replay) || replay.Code != "write_idempotency_in_progress" {
		t.Fatalf("replay error=%T %v", err, err)
	}
	if client.triggerPushMirrorCalls != 1 {
		t.Fatalf("trigger calls=%d want 1", client.triggerPushMirrorCalls)
	}
	entry, err := store.GetAuditEventByKey(ctx, "fixture-a", "ambiguous-key")
	if err != nil {
		t.Fatal(err)
	}
	if entry == nil || entry.Status != audit.StatusInProgress {
		t.Fatalf("audit=%#v", entry)
	}
}

func TestWaitPushRemoteMirrorIgnoresStaleTerminalAndReturnsFreshSuccess(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "example-owner", Name: "example-repo", APIBaseURL: "https://api.example.invalid/api/v5", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	client := &fakeGitCodeClient{pushMirrorsResults: [][]gitcode.PushMirror{
		{{RemoteID: "17", URL: "https://mirror.example.invalid/repo.git", UpdateStatus: "finished", LastUpdateAt: "2026-07-30T11:59:00Z", LastSuccessfulUpdateAt: "2026-07-30T11:59:00Z"}},
		{{RemoteID: "17", URL: "https://mirror.example.invalid/repo.git", UpdateStatus: "processing", LastUpdateAt: "2026-07-30T12:00:01Z"}},
		{{RemoteID: "17", URL: "https://mirror.example.invalid/repo.git", UpdateStatus: "finished", LastUpdateAt: "2026-07-30T12:00:02Z", LastSuccessfulUpdateAt: "2026-07-30T12:00:02Z"}},
	}}
	svc := NewWithClient(store, client)
	after := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	result, err := svc.WaitPushRemoteMirror(ctx, PushMirrorWaitRequest{RepoID: "fixture-a", MirrorID: "17", After: after, TimeoutSeconds: 1, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("WaitPushRemoteMirror: %v", err)
	}
	if result.Status != "finished" || result.UpdateStatus != "finished" || result.LastSuccessfulUpdateAt.Before(after) {
		t.Fatalf("result=%#v", result)
	}
}

func TestWaitPushRemoteMirrorReturnsTimeoutWithoutDestination(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "example-owner", Name: "example-repo", APIBaseURL: "https://api.example.invalid/api/v5", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	client := &fakeGitCodeClient{pushMirrorsResult: []gitcode.PushMirror{{RemoteID: "17", URL: "https://mirror.example.invalid/repo.git", UpdateStatus: "processing", LastUpdateAt: "2026-07-30T12:00:00Z"}}}
	svc := NewWithClient(store, client)
	result, err := svc.WaitPushRemoteMirror(ctx, PushMirrorWaitRequest{RepoID: "fixture-a", MirrorID: "17", Timeout: 10 * time.Millisecond, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("WaitPushRemoteMirror: %v", err)
	}
	if result.Status != "timeout" || result.MirrorID != "17" {
		t.Fatalf("result=%#v", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "destination") || strings.Contains(string(encoded), "example.invalid") {
		t.Fatalf("result leaked destination: %s", encoded)
	}
}

func beforeSlice(mirror gitcode.PushMirror) []gitcode.PushMirror {
	return []gitcode.PushMirror{mirror}
}

func TestSetIssueMilestoneVerifiesReadbackWhenPatchResponseIsNull(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedStore(t, ctx, store)
	updatedAt := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	milestone := &gitcode.Milestone{RemoteID: "576480", SourceID: "MILESTONE-576480", Title: "RAG indexer MVP", Status: "open", DueOn: "2026-07-15"}
	client := &fakeGitCodeClient{
		listMilestonesResult: gitcode.Page[gitcode.Milestone]{Items: []gitcode.Milestone{*milestone}},
		updateIssueResult:    gitcode.WriteResult[gitcode.Issue]{Record: gitcode.Issue{ID: "1", Number: 1, Title: "Issue 1", Body: "B", State: "open", UpdatedAt: updatedAt}, Confirmed: true, Operation: "UpdateIssue", RemoteID: "1", RemoteNumber: 1, ConfirmedAt: updatedAt},
		issuesByNumber:       map[int]gitcode.Issue{1: {ID: "1", Number: 1, Title: "Issue 1", Body: "B", State: "open", Milestone: milestone, UpdatedAt: updatedAt}},
	}
	svc := NewWithClient(store, client)
	t.Setenv("GITCODE_TOKEN", "test-token")
	result, err := svc.SetIssueMilestone(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Number: 1, Milestone: "RAG indexer MVP", IdempotencyKey: "set-milestone-key"})
	if err != nil {
		t.Fatalf("SetIssueMilestone: %v", err)
	}
	if result.Status != "succeeded" || result.RemoteNumber != 1 {
		t.Fatalf("result=%#v", result)
	}
	if result.Milestone == nil || result.Milestone.ID != "MILESTONE-576480" || result.Milestone.RemoteID != "576480" || result.Milestone.Title != "RAG indexer MVP" {
		t.Fatalf("milestone receipt=%#v", result.Milestone)
	}
	if client.lastListMilestonesRequest.PerPage != 100 {
		t.Fatalf("list milestone request=%#v", client.lastListMilestonesRequest)
	}
	if string(client.lastUpdateIssueRequest.Milestone) != "576480" {
		t.Fatalf("milestone payload=%s", string(client.lastUpdateIssueRequest.Milestone))
	}
	if client.issueCalls != 1 {
		t.Fatalf("readback calls=%d, want 1", client.issueCalls)
	}
}

func TestClearIssueMilestoneSendsNullAndVerifiesReadback(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedStore(t, ctx, store)
	updatedAt := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		updateIssueResult: gitcode.WriteResult[gitcode.Issue]{Record: gitcode.Issue{ID: "1", Number: 1, Title: "Issue 1", Body: "B", State: "open", UpdatedAt: updatedAt}, Confirmed: true, Operation: "UpdateIssue", RemoteID: "1", RemoteNumber: 1, ConfirmedAt: updatedAt},
		issuesByNumber:    map[int]gitcode.Issue{1: {ID: "1", Number: 1, Title: "Issue 1", Body: "B", State: "open", UpdatedAt: updatedAt}},
	}
	svc := NewWithClient(store, client)
	t.Setenv("GITCODE_TOKEN", "test-token")
	result, err := svc.ClearIssueMilestone(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Number: 1, IdempotencyKey: "clear-milestone-key"})
	if err != nil {
		t.Fatalf("ClearIssueMilestone: %v", err)
	}
	if result.Status != "succeeded" || result.RemoteNumber != 1 {
		t.Fatalf("result=%#v", result)
	}
	if result.Milestone == nil || !result.Milestone.Cleared {
		t.Fatalf("milestone receipt=%#v", result.Milestone)
	}
	if string(client.lastUpdateIssueRequest.Milestone) != "null" {
		t.Fatalf("milestone payload=%s", string(client.lastUpdateIssueRequest.Milestone))
	}
	if client.issueCalls != 1 {
		t.Fatalf("readback calls=%d, want 1", client.issueCalls)
	}
}

func TestCreateIssueWithMilestoneValidatesBeforeMutationAndReplaysReceipt(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedStore(t, ctx, store)
	updatedAt := time.Date(2026, 7, 28, 18, 0, 0, 0, time.UTC)
	milestone := gitcode.Milestone{RemoteID: "1", SourceID: "MILESTONE-1", Title: "Release 1", Status: "open"}
	client := &fakeGitCodeClient{
		milestonesByID: map[int]gitcode.Milestone{1: milestone},
		createIssueResult: gitcode.WriteResult[gitcode.Issue]{
			Record:       gitcode.Issue{ID: "101", Number: 101, Title: "Milestoned issue", State: "open", UpdatedAt: updatedAt},
			Confirmed:    true,
			Operation:    "CreateIssue",
			RemoteID:     "101",
			RemoteNumber: 101,
			ConfirmedAt:  updatedAt,
		},
		issuesByNumber: map[int]gitcode.Issue{
			101: {ID: "101", Number: 101, Title: "Milestoned issue", State: "open", Milestone: &milestone, UpdatedAt: updatedAt},
		},
	}
	svc := NewWithClient(store, client)
	t.Setenv("GITCODE_TOKEN", "test-token")
	req := WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Title: "Milestoned issue", Milestone: "MILESTONE-1", IdempotencyKey: "create-issue-milestone-key"}

	result, err := svc.CreateIssue(ctx, req)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if client.getMilestoneCalls != 1 || client.createIssueCalls != 1 || client.issueCalls != 1 {
		t.Fatalf("calls milestone=%d create=%d issue=%d", client.getMilestoneCalls, client.createIssueCalls, client.issueCalls)
	}
	if string(client.lastCreateIssueRequest.Milestone) != "1" {
		t.Fatalf("create milestone payload=%s", string(client.lastCreateIssueRequest.Milestone))
	}
	if result.Milestone == nil || result.Milestone.ID != "MILESTONE-1" || result.Milestone.RemoteID != "1" || result.Milestone.Title != "Release 1" || result.Milestone.Cleared {
		t.Fatalf("milestone receipt=%#v", result.Milestone)
	}
	if _, err := store.GetRecord(ctx, "fixture-a", "ISSUE-101"); err != nil {
		t.Fatalf("cached issue: %v", err)
	}
	links, err := store.ListLinks(ctx, cache.LinkFilter{RepoID: "fixture-a", SourceID: "ISSUE-101"})
	if err != nil || len(links) != 1 || links[0].TargetID != "MILESTONE-1" || links[0].Kind != "milestone" {
		t.Fatalf("milestone links=%#v err=%v", links, err)
	}

	replay, err := svc.CreateIssue(ctx, req)
	if err != nil {
		t.Fatalf("CreateIssue replay: %v", err)
	}
	if !replay.Replayed || replay.Status != "already_applied" || replay.Milestone == nil || replay.Milestone.ID != "MILESTONE-1" || replay.Milestone.RemoteID != "1" || replay.Milestone.Title != "Release 1" {
		t.Fatalf("replay=%#v", replay)
	}
	if client.getMilestoneCalls != 1 || client.createIssueCalls != 1 || client.issueCalls != 1 {
		t.Fatalf("replay performed remote calls: milestone=%d create=%d issue=%d", client.getMilestoneCalls, client.createIssueCalls, client.issueCalls)
	}
}

func TestCreateIssueInvalidMilestoneFailsBeforeMutation(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedStore(t, ctx, store)
	client := &fakeGitCodeClient{getMilestoneErr: gitcode.ErrRemoteNotFound{Alias: "milestone:999", Endpoint: "/milestones/999"}}
	svc := NewWithClient(store, client)
	t.Setenv("GITCODE_TOKEN", "test-token")

	_, err = svc.CreateIssue(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Title: "Invalid milestone", Milestone: "999", IdempotencyKey: "invalid-milestone-key"})
	var writeErr ErrWriteFailure
	if !errors.As(err, &writeErr) {
		t.Fatalf("error=%T %v, want ErrWriteFailure", err, err)
	}
	if client.getMilestoneCalls != 1 || client.createIssueCalls != 0 {
		t.Fatalf("invalid milestone calls milestone=%d create=%d", client.getMilestoneCalls, client.createIssueCalls)
	}
}

func TestCreateIssueAmbiguousMilestoneTitleFailsBeforeMutation(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedStore(t, ctx, store)
	client := &fakeGitCodeClient{listMilestonesResult: gitcode.Page[gitcode.Milestone]{Items: []gitcode.Milestone{
		{RemoteID: "1", Title: "Release"},
		{RemoteID: "2", Title: "release"},
	}}}
	svc := NewWithClient(store, client)
	t.Setenv("GITCODE_TOKEN", "test-token")

	_, err = svc.CreateIssue(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Title: "Ambiguous milestone", Milestone: "Release", IdempotencyKey: "ambiguous-milestone-key"})
	var writeErr ErrWriteFailure
	if !errors.As(err, &writeErr) || client.createIssueCalls != 0 {
		t.Fatalf("error=%T %v create_calls=%d", err, err, client.createIssueCalls)
	}
}

func TestCreateIssueMilestoneReadbackMismatchFailsConfirmation(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedStore(t, ctx, store)
	milestone := gitcode.Milestone{RemoteID: "1", SourceID: "MILESTONE-1", Title: "Release 1"}
	client := &fakeGitCodeClient{
		milestonesByID: map[int]gitcode.Milestone{1: milestone},
		createIssueResult: gitcode.WriteResult[gitcode.Issue]{
			Record:    gitcode.Issue{ID: "102", Number: 102, Title: "Mismatch"},
			Confirmed: true, Operation: "CreateIssue", RemoteID: "102", RemoteNumber: 102,
		},
		issuesByNumber: map[int]gitcode.Issue{102: {ID: "102", Number: 102, Title: "Mismatch"}},
	}
	svc := NewWithClient(store, client)
	t.Setenv("GITCODE_TOKEN", "test-token")

	_, err = svc.CreateIssue(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Title: "Mismatch", Milestone: "1", IdempotencyKey: "mismatch-milestone-key"})
	var writeErr ErrWriteFailure
	if !errors.As(err, &writeErr) || writeErr.Code != "write_provider_error" {
		t.Fatalf("error=%T %v", err, err)
	}
	if client.createIssueCalls != 1 || client.issueCalls != 1 {
		t.Fatalf("calls create=%d readback=%d", client.createIssueCalls, client.issueCalls)
	}
}

func TestUpdateIssueClearMilestoneReturnsClearedReceipt(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedStore(t, ctx, store)
	updatedAt := time.Date(2026, 7, 28, 18, 15, 0, 0, time.UTC)
	if err := store.UpsertRecordGraph(ctx, cache.RecordGraph{
		Record:           cache.Record{RepoID: "fixture-a", ID: "ISSUE-1", Type: "issue", Path: "issues/1.md", Title: "Issue 1", Status: "open", ContentHash: "with-milestone", Provenance: cache.ProvenanceRemote, RemoteType: "issue", RemoteID: "1", CreatedAt: updatedAt, UpdatedAt: updatedAt},
		RelatedRecords:   []cache.Record{{RepoID: "fixture-a", ID: "MILESTONE-1", Type: "milestone", Path: "milestones/1.md", Title: "Release 1", Status: "open", ContentHash: "milestone-1", Provenance: cache.ProvenanceRemote, RemoteType: "milestone", RemoteID: "1", CreatedAt: updatedAt, UpdatedAt: updatedAt}},
		ReplaceLinkKinds: []string{"milestone"},
		Links:            []cache.Link{{TargetID: "MILESTONE-1", Kind: "milestone", Text: "Release 1"}},
	}); err != nil {
		t.Fatal(err)
	}
	client := &fakeGitCodeClient{
		updateIssueResult: gitcode.WriteResult[gitcode.Issue]{Record: gitcode.Issue{ID: "1", Number: 1, Title: "Issue 1", State: "open", UpdatedAt: updatedAt}, Confirmed: true, Operation: "UpdateIssue", RemoteID: "1", RemoteNumber: 1, ConfirmedAt: updatedAt},
		issuesByNumber:    map[int]gitcode.Issue{1: {ID: "1", Number: 1, Title: "Issue 1", State: "open", UpdatedAt: updatedAt}},
	}
	svc := NewWithClient(store, client)
	t.Setenv("GITCODE_TOKEN", "test-token")

	result, err := svc.UpdateIssue(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Number: 1, ClearMilestone: true, IdempotencyKey: "update-clear-milestone-key"})
	if err != nil {
		t.Fatalf("UpdateIssue clear milestone: %v", err)
	}
	if string(client.lastUpdateIssueRequest.Milestone) != "null" || result.Milestone == nil || !result.Milestone.Cleared {
		t.Fatalf("request=%#v result=%#v", client.lastUpdateIssueRequest, result)
	}
	links, err := store.ListLinks(ctx, cache.LinkFilter{RepoID: "fixture-a", SourceID: "ISSUE-1"})
	if err != nil || len(links) != 0 {
		t.Fatalf("cleared milestone links=%#v err=%v", links, err)
	}
	entry, err := store.GetAuditEventByKey(ctx, "fixture-a", "update-clear-milestone-key")
	if err != nil || entry == nil || entry.RequestMetadata["method"] != "PATCH" || entry.RequestMetadata["milestone_cleared"] != "true" {
		t.Fatalf("audit entry=%#v err=%v", entry, err)
	}
	replay, err := svc.UpdateIssue(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Number: 1, ClearMilestone: true, IdempotencyKey: "update-clear-milestone-key"})
	if err != nil || !replay.Replayed || replay.Milestone == nil || !replay.Milestone.Cleared || client.updateIssueCalls != 1 {
		t.Fatalf("replay=%#v err=%v update_calls=%d", replay, err, client.updateIssueCalls)
	}
}

func TestUpdateIssueRejectsMilestoneAndClearMilestone(t *testing.T) {
	ctx := context.Background()
	svc := seededSyncService(t, ctx, &fakeGitCodeClient{})
	_, err := svc.UpdateIssue(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeDryRun, Number: 1, Milestone: "1", ClearMilestone: true})
	var invalid ErrInvalidQuery
	if !errors.As(err, &invalid) || invalid.Field != "milestone" {
		t.Fatalf("error=%T %v, want milestone ErrInvalidQuery", err, err)
	}
}

func TestScenario017AddCommentLiveShapeCachesComment(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner-a", Name: "repo-a", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	parent := cache.Record{RepoID: "fixture-a", ID: "ISSUE-42", Type: "issue", Path: "issues/42.md", Title: "Canonical issue", Body: "body", Status: "open", ContentHash: "issue-42", Provenance: cache.ProvenanceRemote, RemoteType: "issue", RemoteID: "42", CreatedAt: created, UpdatedAt: created}
	if err := store.UpsertRecordGraph(ctx, cache.RecordGraph{Record: parent, Identities: []cache.Identity{
		{RepoID: "fixture-a", SourceID: parent.ID, AliasType: "issue", Alias: "42", Remote: cache.RemoteAlias{Type: "issue", ID: "42"}},
		{RepoID: "fixture-a", SourceID: parent.ID, AliasType: "gitcode_issue_id", Alias: "420042", Remote: cache.RemoteAlias{Type: "gitcode_issue_id", ID: "420042"}},
	}}); err != nil {
		t.Fatal(err)
	}
	client := &fakeGitCodeClient{
		createIssueCommentResult: gitcode.WriteResult[gitcode.Comment]{Record: gitcode.Comment{ID: "2002", IssueID: "420042", Body: "live comment", Author: "commenter", CreatedAt: created}, Confirmed: true, Operation: "CreateIssueComment", RemoteID: "2002", ParentIssueNumber: 42, ParentIssueID: "420042", ConfirmedAt: created},
	}
	svc := NewWithClient(store, client)
	svc.providerMode = gitcode.ProviderModeLive
	svc.writeCredentialPresent = true
	result, err := svc.AddComment(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Number: 42, Body: "live comment", IdempotencyKey: "comment-key-live-shape"})
	if err != nil {
		t.Fatalf("AddComment live returned error: %v", err)
	}
	if result.Status != "succeeded" || result.RemoteID != "2002" || client.createIssueCommentCalls != 1 {
		t.Fatalf("unexpected result=%+v calls=%d", result, client.createIssueCommentCalls)
	}
	record, err := store.GetRecord(ctx, "fixture-a", "ISSUE-42")
	if err != nil {
		t.Fatalf("GetRecord: %v", err)
	}
	if len(record.Comments) != 1 || record.Comments[0].CommentID != "2002" || record.Comments[0].Author != "commenter" || record.Comments[0].Body != "live comment" {
		t.Fatalf("comments=%#v", record.Comments)
	}
	if _, err := store.GetRecord(ctx, "fixture-a", "ISSUE-420042"); err == nil {
		t.Fatal("add-comment created a provider-id placeholder parent")
	}
	identity, err := store.ResolveAliasScoped(ctx, "fixture-a", cache.RemoteAlias{Type: "gitcode_issue_id", ID: "420042"})
	if err != nil || identity.SourceID != "ISSUE-42" {
		t.Fatalf("provider identity=%#v err=%v", identity, err)
	}
	projection, err := store.GetSourceScoped(ctx, "fixture-a", "ISSUECOMMENT-42-2002")
	if err != nil || projection.Body != "live comment" {
		t.Fatalf("projection=%#v err=%v", projection, err)
	}
	links, err := store.ListLinks(ctx, cache.LinkFilter{RepoID: "fixture-a", SourceID: projection.ID})
	if err != nil || len(links) != 1 || links[0].TargetID != "ISSUE-42" {
		t.Fatalf("projection links=%#v err=%v", links, err)
	}
	queue, ok, err := store.GetIssueCommentSync(ctx, "fixture-a", "ISSUE-42")
	if err != nil || !ok || queue.IssueNumber != 42 || queue.ProviderID != "420042" {
		t.Fatalf("queue=%#v ok=%t err=%v", queue, ok, err)
	}
	found, err := svc.SearchSources(ctx, SearchSourcesRequest{RepoID: "fixture-a", Query: "live comment", Kind: "issue_comment"})
	if err != nil || len(found.Results) != 1 || found.Results[0].ID != projection.ID {
		t.Fatalf("search=%#v err=%v", found, err)
	}
	replayed, err := svc.AddComment(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Number: 42, Body: "live comment", IdempotencyKey: "comment-key-live-shape"})
	if err != nil || replayed.Status != "already_applied" || client.createIssueCommentCalls != 1 {
		t.Fatalf("replay=%#v calls=%d err=%v", replayed, client.createIssueCommentCalls, err)
	}
	record, err = store.GetRecord(ctx, "fixture-a", "ISSUE-42")
	if err != nil || len(record.Comments) != 1 {
		t.Fatalf("record after replay=%#v err=%v", record, err)
	}
}

func TestScenario007UpdateCommentLiveShapeUpsertsCachedComment(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner-a", Name: "repo-a", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	updated := created.Add(time.Minute)
	client := &fakeGitCodeClient{
		createIssueCommentResult: gitcode.WriteResult[gitcode.Comment]{Record: gitcode.Comment{ID: "2002", IssueID: "42", Body: "initial comment", Author: "commenter", CreatedAt: created, UpdatedAt: created}, Confirmed: true, Operation: "CreateIssueComment", RemoteID: "2002", ParentIssueNumber: 42, ParentIssueID: "42", ConfirmedAt: created},
		updateIssueCommentResult: gitcode.WriteResult[gitcode.Comment]{Record: gitcode.Comment{ID: "2002", IssueID: "42", Body: "updated comment\nline two", Author: "commenter", CreatedAt: created, UpdatedAt: updated}, Confirmed: true, Operation: "UpdateIssueComment", RemoteID: "2002", ParentIssueNumber: 42, ParentIssueID: "42", ConfirmedAt: updated},
	}
	svc := NewWithClient(store, client)
	svc.providerMode = gitcode.ProviderModeLive
	svc.writeCredentialPresent = true
	if _, err := svc.AddComment(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Number: 42, Body: "initial comment", IdempotencyKey: "comment-key-create"}); err != nil {
		t.Fatalf("AddComment live returned error: %v", err)
	}
	result, err := svc.UpdateComment(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Number: 42, CommentID: "2002", Body: "updated comment\nline two", IdempotencyKey: "comment-key-update"})
	if err != nil {
		t.Fatalf("UpdateComment live returned error: %v", err)
	}
	if result.Status != "succeeded" || result.Command != "update-comment" || result.RemoteID != "2002" || client.updateIssueCommentCalls != 1 {
		t.Fatalf("unexpected result=%+v updateCalls=%d", result, client.updateIssueCommentCalls)
	}
	if client.lastUpdateIssueCommentReq.CommentID != "2002" || client.lastUpdateIssueCommentReq.Number != 42 || client.lastUpdateIssueCommentReq.Body != "updated comment\nline two" {
		t.Fatalf("last update request=%#v", client.lastUpdateIssueCommentReq)
	}
	record, err := store.GetRecord(ctx, "fixture-a", "ISSUE-42")
	if err != nil {
		t.Fatalf("GetRecord: %v", err)
	}
	if len(record.Comments) != 1 || record.Comments[0].CommentID != "2002" || record.Comments[0].Body != "updated comment\nline two" {
		t.Fatalf("comments=%#v", record.Comments)
	}
}

func TestScenario016MCPWriteLifecycleCreatePRAndComment(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner-a", Name: "repo-a", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		createPRResult:              gitcode.WriteResult[gitcode.PullRequest]{Record: gitcode.PullRequest{ID: "9001", Number: 7, Title: "Add MCP writes", Body: "body", State: "open", Base: "main", Head: "topic", CreatedAt: created, UpdatedAt: created}, Confirmed: true, Operation: "CreatePR", RemoteID: "9001", RemoteNumber: 7, ConfirmedAt: created},
		createPRCommentResult:       gitcode.WriteResult[gitcode.PRComment]{Record: gitcode.PRComment{ID: "301", Body: "tested", Author: "bot", CreatedAt: created}, Confirmed: true, Operation: "CreatePRComment", RemoteID: "301", ParentIssueNumber: 7, ParentIssueID: "7", ConfirmedAt: created},
		createPRReviewCommentResult: gitcode.WriteResult[gitcode.PRComment]{Record: gitcode.PRComment{ID: "302", Body: "inline", Author: "bot", DiscussionID: "D7", ReviewKind: "inline", Path: "internal/service/service.go", Line: 42, Position: 42, Positions: []gitcode.PRCommentPosition{{PositionKind: "current", PositionType: "text", BaseSHA: "base-sha", StartSHA: "base-sha", HeadSHA: "head-sha", OldPath: "internal/service/service.go", NewPath: "internal/service/service.go", NewLine: 42, LineCode: "line-code", Side: "new"}}, CreatedAt: created, UpdatedAt: created}, Confirmed: true, Operation: "CreatePRReviewComment", RemoteID: "302", ParentIssueNumber: 7, ParentIssueID: "D7", ConfirmedAt: created},
		replyPRReviewCommentResult:  gitcode.WriteResult[gitcode.PRComment]{Record: gitcode.PRComment{ID: "303", Body: "confirmed", Author: "bot", DiscussionID: "D7", ParentID: "302", ReviewKind: "inline", Path: "internal/service/service.go", Line: 42, Positions: []gitcode.PRCommentPosition{{PositionKind: "current", PositionType: "text", BaseSHA: "base-sha", StartSHA: "base-sha", HeadSHA: "head-sha", NewPath: "internal/service/service.go", NewLine: 42, Side: "new"}}, CreatedAt: created, UpdatedAt: created}, Confirmed: true, Operation: "ReplyPRReviewComment", RemoteID: "303", ParentIssueNumber: 7, ParentIssueID: "D7", ConfirmedAt: created},
	}
	client.replyPRReviewCommentResult.Record.Thread = []gitcode.PRComment{{ID: "302", Body: "inline", Author: "bot", DiscussionID: "D7", ReviewKind: "inline", Path: "internal/service/service.go", Line: 42, Positions: []gitcode.PRCommentPosition{{PositionKind: "current", PositionType: "text", BaseSHA: "base-sha", StartSHA: "base-sha", HeadSHA: "head-sha", NewPath: "internal/service/service.go", NewLine: 42, Side: "new"}}, CreatedAt: created, UpdatedAt: created}}
	svc := NewWithClient(store, client)
	svc.providerMode = gitcode.ProviderModeLive
	svc.writeCredentialPresent = true

	pr, err := svc.CreatePR(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Title: "Add MCP writes", Body: "body", Head: "topic", Base: "main", IdempotencyKey: "create-pr-key"})
	if err != nil {
		t.Fatalf("CreatePR live returned error: %v", err)
	}
	if pr.Status != "succeeded" || pr.ID != "PR-7" || pr.RemoteID != "7" || client.createPRCalls != 1 {
		t.Fatalf("unexpected PR result=%+v calls=%d", pr, client.createPRCalls)
	}
	if client.lastCreatePRRequest.Head != "topic" || client.lastCreatePRRequest.Base != "main" || client.lastWriteOptions.IdempotencyKey != "create-pr-key" {
		t.Fatalf("create request=%#v opts=%#v", client.lastCreatePRRequest, client.lastWriteOptions)
	}
	record, err := store.GetRecord(ctx, "fixture-a", "PR-7")
	if err != nil {
		t.Fatalf("PR cache refresh missing: %v", err)
	}
	if record.Type != "pull_request" || record.Title != "Add MCP writes" || record.RemoteID != "7" {
		t.Fatalf("record=%#v", record)
	}

	comment, err := svc.AddPRComment(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Number: 7, Body: "tested", IdempotencyKey: "pr-comment-key"})
	if err != nil {
		t.Fatalf("AddPRComment live returned error: %v", err)
	}
	if comment.Status != "succeeded" || comment.ID != "PRCOMMENT-7-301" || comment.RemoteID != "301" || client.createPRCommentCalls != 1 {
		t.Fatalf("unexpected PR comment result=%+v calls=%d", comment, client.createPRCommentCalls)
	}
	commentRecord, err := store.GetRecord(ctx, "fixture-a", "PRCOMMENT-7-301")
	if err != nil {
		t.Fatalf("PR comment cache refresh missing: %v", err)
	}
	if commentRecord.Type != "pr_comment" || commentRecord.Body != "tested" {
		t.Fatalf("comment record=%#v", commentRecord)
	}

	reviewComment, err := svc.AddPRReviewComment(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Number: 7, Body: "inline", Path: "internal/service/service.go", Line: 42, Position: 42, IdempotencyKey: "pr-review-comment-key"})
	if err != nil {
		t.Fatalf("AddPRReviewComment live returned error: %v", err)
	}
	if reviewComment.Status != "succeeded" || reviewComment.ID != "PRCOMMENT-7-302" || reviewComment.RemoteID != "302" || client.createPRReviewCommentCalls != 1 {
		t.Fatalf("unexpected PR review comment result=%+v calls=%d", reviewComment, client.createPRReviewCommentCalls)
	}
	if client.lastCreatePRReviewCommentReq.Path != "internal/service/service.go" || client.lastCreatePRReviewCommentReq.Line != 42 || client.lastCreatePRReviewCommentReq.Position != 42 {
		t.Fatalf("review comment request=%#v", client.lastCreatePRReviewCommentReq)
	}
	reviewRows, err := store.ListPRReviewComments(ctx, cache.PRReviewCommentFilter{RepoID: "fixture-a", PRNumber: 7, SourceID: "PRCOMMENT-7-302"})
	if err != nil {
		t.Fatalf("list review comments: %v", err)
	}
	if len(reviewRows) != 1 || reviewRows[0].ReviewKind != "inline" || reviewRows[0].Path != "internal/service/service.go" || reviewRows[0].Line != 42 || reviewRows[0].Position != 42 || reviewRows[0].DiscussionID != "D7" {
		t.Fatalf("review rows=%+v", reviewRows)
	}
	positionRows, err := store.ListPRReviewPositions(ctx, cache.PRReviewPositionFilter{RepoID: "fixture-a", PRNumber: 7, CommentID: "302"})
	if err != nil {
		t.Fatalf("list review positions: %v", err)
	}
	if len(positionRows) != 1 || positionRows[0].NewPath != "internal/service/service.go" || positionRows[0].NewLine != 42 || positionRows[0].LineCode != "line-code" {
		t.Fatalf("position rows=%+v", positionRows)
	}
	reply, err := svc.ReplyPRReviewComment(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Number: 7, DiscussionID: "D7", ParentID: "302", Body: "confirmed", IdempotencyKey: "pr-review-reply-key"})
	if err != nil {
		t.Fatalf("ReplyPRReviewComment live returned error: %v", err)
	}
	if reply.Status != "succeeded" || reply.ID != "PRCOMMENT-7-303" || reply.RemoteID != "303" || client.replyPRReviewCommentCalls != 1 {
		t.Fatalf("unexpected PR review reply=%+v calls=%d", reply, client.replyPRReviewCommentCalls)
	}
	if client.lastReplyPRReviewCommentReq.DiscussionID != "D7" || client.lastReplyPRReviewCommentReq.ParentCommentID != "302" || client.lastWriteOptions.IdempotencyKey != "pr-review-reply-key" {
		t.Fatalf("reply request=%#v opts=%#v", client.lastReplyPRReviewCommentReq, client.lastWriteOptions)
	}
	replyRows, err := store.ListPRReviewComments(ctx, cache.PRReviewCommentFilter{RepoID: "fixture-a", PRNumber: 7, SourceID: "PRCOMMENT-7-303"})
	if err != nil {
		t.Fatalf("list reply comments: %v", err)
	}
	if len(replyRows) != 1 || replyRows[0].DiscussionID != "D7" || replyRows[0].ParentID != "302" {
		t.Fatalf("reply rows=%+v", replyRows)
	}
	replyRecord, err := store.GetRecord(ctx, "fixture-a", "PRCOMMENT-7-303")
	if err != nil || replyRecord.Body != "confirmed" {
		t.Fatalf("reply record=%+v err=%v", replyRecord, err)
	}
	discussions, err := svc.ListPRDiscussions(ctx, PRDiscussionRequest{RepoID: "fixture-a", Number: 7})
	if err != nil {
		t.Fatalf("list cached reply discussion: %v", err)
	}
	var reviewDiscussion *PRDiscussion
	for i := range discussions.Discussions {
		if discussions.Discussions[i].ID == "D7" {
			reviewDiscussion = &discussions.Discussions[i]
		}
	}
	if reviewDiscussion == nil || len(reviewDiscussion.Comments) != 2 || reviewDiscussion.Comments[0].ID != "302" || reviewDiscussion.Comments[1].ID != "303" {
		t.Fatalf("cached discussions=%+v", discussions.Discussions)
	}
}

func TestAddPRReviewCommentRejectsConflictingAnchorBeforeWriteLifecycle(t *testing.T) {
	svc := &Service{}
	_, err := svc.AddPRReviewComment(context.Background(), WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Number: 7, Body: "inline", Path: "internal/service/service.go", Line: 42, Position: 9, IdempotencyKey: "conflicting-anchor"})
	var invalid ErrInvalidQuery
	if !errors.As(err, &invalid) || invalid.Field != "position" {
		t.Fatalf("error=%#v, want position invalid query", err)
	}
}

func TestPRReviewCommentAnchorChangesIdempotencyFingerprint(t *testing.T) {
	base := WriteCommandRequest{RepoID: "fixture-a", Number: 7, Body: "inline", Path: "x.go", Line: 42, Position: 42, StartLine: 41, EndLine: 42, IdempotencyKey: "review-key"}
	key, fingerprint := writeIdempotency("add-pr-review-comment", base)
	changed := base
	changed.Line = 43
	changed.Position = 43
	changed.EndLine = 43
	changedKey, changedFingerprint := writeIdempotency("add-pr-review-comment", changed)
	if key != changedKey || fingerprint == changedFingerprint {
		t.Fatalf("keys=(%q,%q) fingerprints=(%q,%q), want stable explicit key and anchor-sensitive fingerprint", key, changedKey, fingerprint, changedFingerprint)
	}
}

func TestPRReviewAnchorMismatchIsAuditedAndIdempotentlyBlocked(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "anchor-mismatch", Owner: "owner-a", Name: "repo-a", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	seedCachedPullRequest(t, ctx, store, "anchor-mismatch", 7)
	client := &fakeGitCodeClient{errors: []error{gitcode.ErrPRReviewAnchorMismatch{Endpoint: "/pulls/7/comments", CommentID: "301", ExpectedPath: "x.go", ActualPath: "x.go", ExpectedLine: 42, ActualLine: 41}}}
	svc := NewWithClient(store, client)
	svc.providerMode = gitcode.ProviderModeLive
	svc.writeCredentialPresent = true
	req := WriteCommandRequest{RepoID: "anchor-mismatch", Mode: WriteModeLive, Number: 7, Body: "inline", Path: "x.go", Line: 42, IdempotencyKey: "anchor-mismatch-key"}

	_, err = svc.AddPRReviewComment(ctx, req)
	var writeErr ErrWriteFailure
	if !errors.As(err, &writeErr) || writeErr.Code != "pr_review_anchor_mismatch" || writeErr.RemoteID != "301" {
		t.Fatalf("first error=%#v, want typed remote mismatch", err)
	}
	entry, err := store.GetAuditEventByKey(ctx, "anchor-mismatch", "anchor-mismatch-key")
	if err != nil || entry == nil || entry.Status != audit.StatusRemoteConfirmedUnsafe || entry.RemoteID != "301" {
		t.Fatalf("audit entry=%#v err=%v", entry, err)
	}

	_, err = svc.AddPRReviewComment(ctx, req)
	if !errors.As(err, &writeErr) || writeErr.Code != "pr_review_anchor_mismatch" || writeErr.RemoteID != "301" {
		t.Fatalf("replay error=%#v, want blocked mismatch", err)
	}
	if client.createPRReviewCommentCalls != 1 {
		t.Fatalf("provider writes=%d, want one after replay", client.createPRReviewCommentCalls)
	}
}

func TestAddPRReviewCommentRequiresCachedParentBeforeProviderWrite(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const repoID = "empty-review"
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: repoID, Owner: "owner-a", Name: "repo-a", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		prsByNumber: map[int]gitcode.PullRequest{7: {ID: "9001", Number: 7, Title: "Review target", State: "open", CreatedAt: now, UpdatedAt: now}},
		createPRReviewCommentResult: gitcode.WriteResult[gitcode.PRComment]{
			Record:    gitcode.PRComment{ID: "302", Body: "inline", PRNumber: 7, Path: "x.go", Line: 42, Position: 42, ReviewKind: "inline", DiscussionID: "D7", CreatedAt: now, UpdatedAt: now},
			Confirmed: true, Operation: "CreatePRReviewComment", RemoteID: "302", ParentIssueNumber: 7, ConfirmedAt: now,
		},
	}
	svc := NewWithClient(store, client)
	svc.providerMode = gitcode.ProviderModeLive
	svc.writeCredentialPresent = true
	req := WriteCommandRequest{RepoID: repoID, Mode: WriteModeLive, Number: 7, Body: "inline", Path: "x.go", Line: 42, Position: 42, IdempotencyKey: "review-parent-key"}

	_, err = svc.AddPRReviewComment(ctx, req)
	var parentMissing ErrParentPRNotCached
	if !errors.As(err, &parentMissing) || parentMissing.RepoID != repoID || parentMissing.Number != 7 {
		t.Fatalf("error=%#v, want typed missing parent", err)
	}
	if parentMissing.DiagnosticCode() != "parent_pr_not_cached" || parentMissing.Remediation() != "gitcode-mcp sync --repo 'empty-review' --pulls --input 'pr:7'" {
		t.Fatalf("parent error=%#v remediation=%q", parentMissing, parentMissing.Remediation())
	}
	if client.createPRReviewCommentCalls != 0 {
		t.Fatalf("provider writes=%d, want zero before parent sync", client.createPRReviewCommentCalls)
	}
	entry, err := store.GetAuditEventByKey(ctx, repoID, req.IdempotencyKey)
	if err != nil || entry != nil {
		t.Fatalf("audit entry=%#v err=%v, want no claim before preflight", entry, err)
	}

	if _, err := svc.SyncToCache(ctx, SyncRequest{RepoID: repoID, RemoteAlias: "pr:7", IdempotencyKey: "review-parent-sync"}); err != nil {
		t.Fatalf("targeted parent sync: %v", err)
	}
	result, err := svc.AddPRReviewComment(ctx, req)
	if err != nil || result.Status != "succeeded" || client.createPRReviewCommentCalls != 1 {
		t.Fatalf("result=%#v err=%v calls=%d", result, err, client.createPRReviewCommentCalls)
	}
	replayed, err := svc.AddPRReviewComment(ctx, req)
	if err != nil || !replayed.Replayed || client.createPRReviewCommentCalls != 1 {
		t.Fatalf("replay=%#v err=%v calls=%d", replayed, err, client.createPRReviewCommentCalls)
	}
}

func TestAddPRReviewCommentUsesAliasedParentStableID(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const repoID = "custom-parent"
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: repoID, Owner: "owner-a", Name: "repo-a", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	if err := store.UpsertRecordGraph(ctx, cache.RecordGraph{
		Record:     cache.Record{RepoID: repoID, ID: "DOC-123", Type: "pull_request", Path: "pulls/7.md", Title: "Custom stable parent", Status: "open", ContentHash: "doc-123", Provenance: cache.ProvenanceLive, RemoteType: "pull_request", RemoteID: "7", CreatedAt: now, UpdatedAt: now},
		Identities: []cache.Identity{{RepoID: repoID, SourceID: "DOC-123", AliasType: "pull_request", Alias: "7", Remote: cache.RemoteAlias{Type: "pull_request", ID: "7"}}},
	}); err != nil {
		t.Fatal(err)
	}
	client := &fakeGitCodeClient{createPRReviewCommentResult: gitcode.WriteResult[gitcode.PRComment]{
		Record:    gitcode.PRComment{ID: "302", Body: "inline", PRNumber: 7, Path: "x.go", Line: 42, ReviewKind: "inline", CreatedAt: now, UpdatedAt: now},
		Confirmed: true, Operation: "CreatePRReviewComment", RemoteID: "302", ParentIssueNumber: 7, ConfirmedAt: now,
	}}
	svc := NewWithClient(store, client)
	svc.providerMode = gitcode.ProviderModeLive
	svc.writeCredentialPresent = true
	result, err := svc.AddPRReviewComment(ctx, WriteCommandRequest{RepoID: repoID, Mode: WriteModeLive, Number: 7, Body: "inline", Path: "x.go", Line: 42, IdempotencyKey: "custom-parent-review"})
	if err != nil || result.Status != "succeeded" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	links, err := store.ListLinks(ctx, cache.LinkFilter{RepoID: repoID, SourceID: "PRCOMMENT-7-302"})
	if err != nil || len(links) != 1 || links[0].TargetID != "DOC-123" {
		t.Fatalf("links=%#v err=%v, want custom parent stable id", links, err)
	}
}

func TestAddPRReviewCommentClaimAndWriterLeasePreventConcurrentUnsafeWrites(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const repoID = "concurrent-review"
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: repoID, Owner: "owner-a", Name: "repo-a", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	seedCachedPullRequest(t, ctx, store, repoID, 7)
	started := make(chan struct{})
	release := make(chan struct{})
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		createPRReviewCommentResult: gitcode.WriteResult[gitcode.PRComment]{Record: gitcode.PRComment{ID: "302", Body: "inline", PRNumber: 7, Path: "x.go", Line: 42, ReviewKind: "inline", CreatedAt: now, UpdatedAt: now}, Confirmed: true, Operation: "CreatePRReviewComment", RemoteID: "302", ParentIssueNumber: 7, ConfirmedAt: now},
		onCreatePRReviewComment: func(gitcode.CreatePRReviewCommentRequest, gitcode.WriteOptions) {
			close(started)
			<-release
		},
	}
	svc := NewWithClient(store, client)
	svc.providerMode = gitcode.ProviderModeLive
	svc.writeCredentialPresent = true
	req := WriteCommandRequest{RepoID: repoID, Mode: WriteModeLive, Number: 7, Body: "inline", Path: "x.go", Line: 42, IdempotencyKey: "concurrent-review-key"}
	firstDone := make(chan error, 1)
	go func() {
		_, err := svc.AddPRReviewComment(context.Background(), req)
		firstDone <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first provider call did not start")
	}
	_, secondErr := svc.AddPRReviewComment(ctx, req)
	var writeErr ErrWriteFailure
	if !errors.As(secondErr, &writeErr) || writeErr.Code != "write_idempotency_in_progress" {
		t.Fatalf("second error=%T %v", secondErr, secondErr)
	}
	_, resetErr := svc.ResetLiveCache(ctx, ResetLiveCacheRequest{RepoID: repoID})
	var lockErr cache.ErrLockContention
	if !errors.As(resetErr, &lockErr) {
		t.Fatalf("reset error=%T %v, want writer lock contention", resetErr, resetErr)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first write: %v", err)
	}
	if client.createPRReviewCommentCalls != 1 {
		t.Fatalf("provider writes=%d, want one", client.createPRReviewCommentCalls)
	}
}

func TestParentPRRemediationShellQuotesRepositoryID(t *testing.T) {
	err := ErrParentPRNotCached{RepoID: "repo; $(unsafe) 'quoted'", Number: 7}
	want := `gitcode-mcp sync --repo 'repo; $(unsafe) '"'"'quoted'"'"'' --pulls --input 'pr:7'`
	if got := err.Remediation(); got != want {
		t.Fatalf("remediation=%q want %q", got, want)
	}
}

func seedCachedPullRequest(t *testing.T, ctx context.Context, store cache.Store, repoID string, number int) {
	t.Helper()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	if err := store.UpsertRecordGraph(ctx, cache.RecordGraph{Record: cache.Record{
		RepoID: repoID, ID: fmt.Sprintf("PR-%d", number), Type: "pull_request", Path: fmt.Sprintf("pulls/%d.md", number),
		Title: "Review target", Status: "open", ContentHash: fmt.Sprintf("pr-%d", number), Provenance: cache.ProvenanceLive,
		RemoteType: "pull_request", RemoteID: strconv.Itoa(number), CreatedAt: now, UpdatedAt: now,
	}}); err != nil {
		t.Fatal(err)
	}
}

func TestPRReviewReplyIncompleteReadbackIsAuditedAndIdempotentlyBlocked(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "reply-incomplete", Owner: "owner-a", Name: "repo-a", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	client := &fakeGitCodeClient{errors: []error{gitcode.ErrWriteConfirmationIncomplete{Endpoint: "/discussions/D7/comments", RemoteID: "303", Message: "reply readback unavailable"}}}
	svc := NewWithClient(store, client)
	svc.providerMode = gitcode.ProviderModeLive
	svc.writeCredentialPresent = true
	req := WriteCommandRequest{RepoID: "reply-incomplete", Mode: WriteModeLive, Number: 7, DiscussionID: "comment:301", ParentID: "301", Body: "reply", IdempotencyKey: "reply-incomplete-key"}

	_, err = svc.ReplyPRReviewComment(ctx, req)
	var writeErr ErrWriteFailure
	if !errors.As(err, &writeErr) || writeErr.Code != "write_confirmation_incomplete" || writeErr.RemoteID != "303" {
		t.Fatalf("first error=%#v", err)
	}
	entry, err := store.GetAuditEventByKey(ctx, "reply-incomplete", "reply-incomplete-key")
	if err != nil || entry == nil || entry.Status != audit.StatusRemoteConfirmedUnsafe || entry.RemoteID != "303" {
		t.Fatalf("audit entry=%#v err=%v", entry, err)
	}
	_, err = svc.ReplyPRReviewComment(ctx, req)
	if !errors.As(err, &writeErr) || writeErr.Code != "write_confirmation_incomplete" || client.replyPRReviewCommentCalls != 1 {
		t.Fatalf("replay error=%#v calls=%d", err, client.replyPRReviewCommentCalls)
	}
}

func TestPRReviewReplyPartialCacheReplayPreservesResolvedDiscussionID(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "reply-partial", Owner: "owner-a", Name: "repo-a", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		listPRPages:                []gitcode.Page[gitcode.PullRequest]{{Items: []gitcode.PullRequest{{ID: "9001", Number: 7, Title: "PR", State: "open", CreatedAt: now, UpdatedAt: now}}}},
		replyPRReviewCommentResult: gitcode.WriteResult[gitcode.PRComment]{Record: gitcode.PRComment{ID: "303", Body: "reply", PRNumber: 7, DiscussionID: "D7", ParentID: "301", ReviewKind: "inline", CreatedAt: now, UpdatedAt: now}, Confirmed: true, Operation: "ReplyPRReviewComment", RemoteID: "303", ParentIssueNumber: 7, ParentIssueID: "D7", ConfirmedAt: now},
	}
	seedSvc := NewWithClient(store, client)
	if _, err := seedSvc.BulkSyncPullRequests(ctx, BulkSyncRequest{RepoID: "reply-partial"}); err != nil {
		t.Fatal(err)
	}
	wrapped := &writeRefreshFailStore{Store: store, failNextRefresh: true}
	svc := NewWithClient(wrapped, client)
	svc.providerMode = gitcode.ProviderModeLive
	svc.writeCredentialPresent = true
	req := WriteCommandRequest{RepoID: "reply-partial", Mode: WriteModeLive, Number: 7, DiscussionID: "comment:301", ParentID: "301", Body: "reply", IdempotencyKey: "reply-partial-key"}

	_, err = svc.ReplyPRReviewComment(ctx, req)
	var writeErr ErrWriteFailure
	if !errors.As(err, &writeErr) || writeErr.Code != "write_partial_cache_refresh_failed" {
		t.Fatalf("first error=%#v", err)
	}
	entry, err := store.GetAuditEventByKey(ctx, "reply-partial", "reply-partial-key")
	if err != nil || entry == nil || entry.RequestMetadata["pr_discussion_id"] != "D7" {
		t.Fatalf("audit entry=%#v err=%v", entry, err)
	}
	result, err := svc.ReplyPRReviewComment(ctx, req)
	if err != nil || !result.Replayed || client.replyPRReviewCommentCalls != 1 {
		t.Fatalf("replay=%#v err=%v calls=%d", result, err, client.replyPRReviewCommentCalls)
	}
	rows, err := store.ListPRReviewComments(ctx, cache.PRReviewCommentFilter{RepoID: "reply-partial", PRNumber: 7, SourceID: "PRCOMMENT-7-303"})
	if err != nil || len(rows) != 1 || rows[0].DiscussionID != "D7" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
}

func TestMergePRWritesAuditAndRefreshesCachedPR(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner-a", Name: "repo-a", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{mergePRResult: gitcode.WriteResult[gitcode.PullRequest]{Record: gitcode.PullRequest{ID: "9001", Number: 103, Title: "Release batch", State: "merged", HeadSHA: "head-sha", CreatedAt: now, UpdatedAt: now}, Confirmed: true, Operation: "MergePR", ProviderStatus: "200", RemoteID: "9001", RemoteNumber: 103, RemoteRevision: "merge-sha", IdempotencyKey: "merge-103", ResponseHash: "response-hash", ProviderPayloadFingerprint: "payload-hash", ConfirmedAt: now}}
	svc := NewWithClient(store, client)
	svc.providerMode = gitcode.ProviderModeLive
	svc.writeCredentialPresent = true

	result, err := svc.MergePR(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Number: 103, Sha: "head-sha", Strategy: "squash", IdempotencyKey: "merge-103"})
	if err != nil {
		t.Fatalf("MergePR: %v", err)
	}
	if result.Status != "succeeded" || result.RemoteNumber != 103 || result.RemoteRevision != "merge-sha" || client.mergePRCalls != 1 || client.lastMergePRRequest.HeadSHA != "head-sha" || client.lastMergePRRequest.MergeMethod != "squash" {
		t.Fatalf("result=%+v calls=%d request=%+v", result, client.mergePRCalls, client.lastMergePRRequest)
	}
	entry, err := store.GetAuditEventByKey(ctx, "fixture-a", "merge-103")
	if err != nil || entry == nil || entry.Status != audit.StatusSucceeded || entry.RequestMetadata["method"] != "PUT" || entry.RequestMetadata["remote_type"] != "pull_request" {
		t.Fatalf("audit=%+v err=%v", entry, err)
	}
	replayed, err := svc.MergePR(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Number: 103, Sha: "head-sha", Strategy: "squash", IdempotencyKey: "merge-103"})
	if err != nil || !replayed.Replayed || client.mergePRCalls != 1 {
		t.Fatalf("replayed=%+v err=%v calls=%d", replayed, err, client.mergePRCalls)
	}
}

func TestScenario004MCPWriteLifecycleLinkPRIssueRelationAPI(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner-a", Name: "repo-a", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		linkPRIssueResult: gitcode.WriteResult[[]gitcode.Issue]{Record: []gitcode.Issue{{ID: "4119896", Number: 16, Title: "Issue 16", State: "open"}}, Confirmed: true, Operation: "LinkPRIssue", RemoteID: "7", RemoteNumber: 7, ResponseHash: "relation-rev", ConfirmedAt: created},
	}
	svc := NewWithClient(store, client)
	svc.providerMode = gitcode.ProviderModeLive
	svc.writeCredentialPresent = true

	result, err := svc.LinkPRIssue(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Number: 7, IssueNumber: 16, Strategy: "auto", IdempotencyKey: "link-pr-issue-key"})
	if err != nil {
		t.Fatalf("LinkPRIssue live returned error: %v", err)
	}
	if result.Status != "succeeded" || result.RemoteID != "7" || result.RemoteNumber != 7 || result.RemoteRevision != "relation-rev" {
		t.Fatalf("result=%#v", result)
	}
	if client.linkPRIssueCalls != 1 || client.prCalls != 0 || client.updatePRCalls != 0 {
		t.Fatalf("linkCalls=%d prCalls=%d updateCalls=%d", client.linkPRIssueCalls, client.prCalls, client.updatePRCalls)
	}
	if client.lastLinkPRIssueRequest.Number != 7 || client.lastLinkPRIssueRequest.IssueNumber != 16 {
		t.Fatalf("link request=%#v", client.lastLinkPRIssueRequest)
	}
	record, err := store.GetRecord(ctx, "fixture-a", "PR-7")
	if err != nil {
		t.Fatalf("PR link cache refresh missing: %v", err)
	}
	if record.RemoteType != "pull_request" || record.RemoteID != "7" || record.RemoteRevision != "relation-rev" {
		t.Fatalf("record=%#v", record)
	}
}

func TestScenario004MCPWriteLifecycleLinkPRIssueUnsupportedFallsBackToDescription(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner-a", Name: "repo-a", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		prsByNumber:    map[int]gitcode.PullRequest{7: {ID: "9001", Number: 7, Title: "PR", Body: "existing body", State: "open", CreatedAt: created, UpdatedAt: created}},
		updatePRResult: gitcode.WriteResult[gitcode.PullRequest]{Record: gitcode.PullRequest{ID: "9001", Number: 7, Title: "PR", Body: "existing body\n\n<!-- gitcode-mcp-link:issue:16 -->\nFixes #16", State: "open", CreatedAt: created, UpdatedAt: created}, Confirmed: true, Operation: "UpdatePR", RemoteID: "9001", RemoteNumber: 7, ConfirmedAt: created},
		errors:         []error{gitcode.ErrUnsupportedCapability{CapabilityKey: "pr_issue_relation", Message: "unsupported in test"}},
	}
	svc := NewWithClient(store, client)
	svc.providerMode = gitcode.ProviderModeLive
	svc.writeCredentialPresent = true

	result, err := svc.LinkPRIssue(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Number: 7, IssueNumber: 16, IdempotencyKey: "link-pr-issue-key"})
	if err != nil {
		t.Fatalf("LinkPRIssue live returned error: %v", err)
	}
	if result.Status != "succeeded" || client.linkPRIssueCalls != 1 || client.prCalls != 1 || client.updatePRCalls != 1 {
		t.Fatalf("result=%#v linkCalls=%d prCalls=%d updateCalls=%d", result, client.linkPRIssueCalls, client.prCalls, client.updatePRCalls)
	}
	if strings.Count(client.lastUpdatePRRequest.Body, "gitcode-mcp-link:issue:16") != 1 || !strings.Contains(client.lastUpdatePRRequest.Body, "Fixes #16") {
		t.Fatalf("link body=%q", client.lastUpdatePRRequest.Body)
	}
}

func TestScenario004MCPWriteLifecycleLinkPRIssueForcedDescriptionFallback(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner-a", Name: "repo-a", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		prsByNumber:    map[int]gitcode.PullRequest{7: {ID: "9001", Number: 7, Title: "PR", Body: "existing body", State: "open", CreatedAt: created, UpdatedAt: created}},
		updatePRResult: gitcode.WriteResult[gitcode.PullRequest]{Record: gitcode.PullRequest{ID: "9001", Number: 7, Title: "PR", Body: "existing body\n\n<!-- gitcode-mcp-link:issue:16 -->\nFixes #16", State: "open", CreatedAt: created, UpdatedAt: created}, Confirmed: true, Operation: "UpdatePR", RemoteID: "9001", RemoteNumber: 7, ConfirmedAt: created},
	}
	svc := NewWithClient(store, client)
	svc.providerMode = gitcode.ProviderModeLive
	svc.writeCredentialPresent = true

	result, err := svc.LinkPRIssue(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Number: 7, IssueNumber: 16, Strategy: "description_fallback", IdempotencyKey: "link-pr-issue-key-fallback"})
	if err != nil {
		t.Fatalf("LinkPRIssue live returned error: %v", err)
	}
	if result.Status != "succeeded" || client.linkPRIssueCalls != 0 || client.prCalls != 1 || client.updatePRCalls != 1 {
		t.Fatalf("result=%#v linkCalls=%d prCalls=%d updateCalls=%d", result, client.linkPRIssueCalls, client.prCalls, client.updatePRCalls)
	}
}

func TestScenario017AddCommentMalformedBodyDiagnosticHTTPAttempted(t *testing.T) {
	err := ErrWriteFailure{Code: "schema_decode", RepoID: "fixture-a", PayloadSource: "schema_decode", Cause: &gitcode.ErrSchemaDecode{Field: "comment", Message: "malformed"}}
	ctx := diagnostics.CommandContext{ProviderMode: "live-http", HTTPAttempted: err.Code == "schema_decode", SchemaDecodeFailure: true, FailureSource: err.PayloadSource}
	diagnostic := diagnostics.Classify(err, ctx)
	if diagnostic.Code != diagnostics.CodeSchemaDecode || !diagnostic.HTTPAttempted {
		t.Fatalf("diagnostic=%+v", diagnostic)
	}
}

func TestS018LiveWriteConfirmedRefreshesCommentAndWiki(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedStore(t, ctx, store)
	client := &fakeGitCodeClient{
		createIssueCommentResult: gitcode.WriteResult[gitcode.Comment]{Record: gitcode.Comment{ID: "comment-9", IssueID: "42", Body: "confirmed comment"}, Confirmed: true, Operation: "CreateIssueComment", RemoteID: "comment-9", ParentIssueNumber: 42, ParentIssueID: "42", ConfirmedAt: time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)},
		createWikiPageResult:     gitcode.WriteResult[gitcode.WikiPage]{Record: gitcode.WikiPage{ID: "wiki-9", Slug: "Home", Title: "Home", Body: "confirmed wiki", Revision: "rev-9"}, Confirmed: true, Operation: "CreateWikiPage", RemoteID: "wiki-9", RemoteSlug: "Home", RemoteRevision: "rev-9", ConfirmedAt: time.Date(2026, 6, 20, 12, 1, 0, 0, time.UTC)},
	}
	svc := NewWithClient(store, client)
	t.Setenv("GITCODE_TOKEN", "test-token")
	comment, err := svc.AddComment(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Number: 42, Body: "confirmed comment", IdempotencyKey: "comment-key-1"})
	if err != nil {
		t.Fatalf("AddComment live returned error: %v", err)
	}
	wiki, err := svc.CreatePage(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Title: "Home", Body: "confirmed wiki", IdempotencyKey: "wiki-key-1"})
	if err != nil {
		t.Fatalf("CreatePage live returned error: %v", err)
	}
	if comment.Status != "succeeded" || wiki.Status != "succeeded" || client.createIssueCommentCalls != 1 || client.createWikiPageCalls != 1 {
		t.Fatalf("comment=%#v wiki=%#v client=%#v", comment, wiki, client)
	}
	record, err := store.GetRecord(ctx, "fixture-a", comment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(record.Comments) != 1 || record.Comments[0].CommentID != "comment-9" {
		t.Fatalf("comments=%#v", record.Comments)
	}
	if _, err := store.GetRecord(ctx, "fixture-a", wiki.ID); err != nil {
		t.Fatalf("wiki cache refresh missing: %v", err)
	}
}

func TestWriteIdempotencyConflictDetection(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedStore(t, ctx, store)
	client := &fakeGitCodeClient{createIssueResult: gitcode.WriteResult[gitcode.Issue]{Record: gitcode.Issue{ID: "remote-42", Number: 42, Title: "T", Body: "B", State: "open"}, Confirmed: true, Operation: "CreateIssue", RemoteID: "42", RemoteNumber: 42, ConfirmedAt: time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)}}
	svc := NewWithClient(store, client)
	t.Setenv("GITCODE_TOKEN", "test-token")
	_, err = svc.CreateIssue(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Title: "T", Body: "B", IdempotencyKey: "same-key"})
	if err != nil {
		t.Fatalf("CreateIssue live returned error: %v", err)
	}
	_, err = svc.CreateIssue(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Title: "Different", Body: "B", IdempotencyKey: "same-key"})
	var writeErr ErrWriteFailure
	if !errors.As(err, &writeErr) || writeErr.Code != "write_idempotency_conflict" {
		t.Fatalf("conflict err=%v", err)
	}
	if client.createIssueCalls != 1 {
		t.Fatalf("calls=%d want 1", client.createIssueCalls)
	}
}

func TestWriteFailureAuditAllowsRetry(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedStore(t, ctx, store)
	client := &fakeGitCodeClient{errors: []error{gitcode.ErrNetworkUnavailable{Endpoint: "/issues", Attempts: 1}}, createIssueResult: gitcode.WriteResult[gitcode.Issue]{Record: gitcode.Issue{ID: "remote-43", Number: 43, Title: "T", Body: "B", State: "open"}, Confirmed: true, Operation: "CreateIssue", RemoteID: "43", RemoteNumber: 43, ConfirmedAt: time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)}}
	svc := NewWithClient(store, client)
	t.Setenv("GITCODE_TOKEN", "test-token")
	req := WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Title: "T", Body: "B", IdempotencyKey: "retry-key"}
	_, err = svc.CreateIssue(ctx, req)
	var writeErr ErrWriteFailure
	if !errors.As(err, &writeErr) || writeErr.Code != "write_network_unavailable" {
		t.Fatalf("first err=%v want write_network_unavailable", err)
	}
	entry, err := store.GetAuditEventByKey(ctx, "fixture-a", "retry-key")
	if err != nil {
		t.Fatal(err)
	}
	if entry == nil || entry.Status != "failed" || entry.PayloadHash == "" || entry.RemoteID != "" || entry.Message != "write_network_unavailable" {
		t.Fatalf("failure audit entry=%#v", entry)
	}
	result, err := svc.CreateIssue(ctx, req)
	if err != nil {
		t.Fatalf("retry returned error: %v", err)
	}
	if result.Status != "succeeded" || result.RemoteID != "43" || client.createIssueCalls != 2 {
		t.Fatalf("retry result=%#v calls=%d", result, client.createIssueCalls)
	}
}

func TestWriteUnconfirmedRemoteAuditAllowsRetry(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedStore(t, ctx, store)
	client := &fakeGitCodeClient{createIssueResults: []gitcode.WriteResult[gitcode.Issue]{
		{Record: gitcode.Issue{ID: "remote-43", Number: 43, Title: "T", Body: "B", State: "open"}, Confirmed: false, Operation: "CreateIssue"},
		{Record: gitcode.Issue{ID: "remote-43", Number: 43, Title: "T", Body: "B", State: "open"}, Confirmed: true, Operation: "CreateIssue", RemoteID: "43", RemoteNumber: 43, ConfirmedAt: time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)},
	}}
	svc := NewWithClient(store, client)
	t.Setenv("GITCODE_TOKEN", "test-token")
	req := WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Title: "T", Body: "B", IdempotencyKey: "unconfirmed-key"}
	_, err = svc.CreateIssue(ctx, req)
	var writeErr ErrWriteFailure
	if !errors.As(err, &writeErr) || writeErr.Code != "write_unconfirmed_remote" {
		t.Fatalf("first err=%v want write_unconfirmed_remote", err)
	}
	entry, err := store.GetAuditEventByKey(ctx, "fixture-a", "unconfirmed-key")
	if err != nil {
		t.Fatal(err)
	}
	if entry == nil || entry.Status != "failed" || entry.Message != "write_unconfirmed_remote" || entry.RemoteID != "" {
		t.Fatalf("unconfirmed audit entry=%#v", entry)
	}
	result, err := svc.CreateIssue(ctx, req)
	if err != nil {
		t.Fatalf("retry returned error: %v", err)
	}
	if result.Status != "succeeded" || result.RemoteID != "43" || client.createIssueCalls != 2 {
		t.Fatalf("retry result=%#v calls=%d", result, client.createIssueCalls)
	}
}

func TestWriteIdempotencyScopedByRepo(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedStore(t, ctx, store)
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-b", Owner: "owner-b", Name: "repo-b", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}, DisplayName: "Fixture B", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	client := &fakeGitCodeClient{createIssueResult: gitcode.WriteResult[gitcode.Issue]{Record: gitcode.Issue{ID: "remote-44", Number: 44, Title: "T", Body: "B", State: "open"}, Confirmed: true, Operation: "CreateIssue", RemoteID: "44", RemoteNumber: 44, ConfirmedAt: time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)}}
	svc := NewWithClient(store, client)
	t.Setenv("GITCODE_TOKEN", "test-token")
	if _, err := svc.CreateIssue(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Title: "T", Body: "B", IdempotencyKey: "shared-key"}); err != nil {
		t.Fatal(err)
	}
	client.createIssueResult.Record.ID = "remote-45"
	client.createIssueResult.Record.Number = 45
	client.createIssueResult.RemoteID = "45"
	client.createIssueResult.RemoteNumber = 45
	if _, err := svc.CreateIssue(ctx, WriteCommandRequest{RepoID: "fixture-b", Mode: WriteModeLive, Title: "T", Body: "B", IdempotencyKey: "shared-key"}); err != nil {
		t.Fatal(err)
	}
	if client.createIssueCalls != 2 {
		t.Fatalf("calls=%d want 2", client.createIssueCalls)
	}
}

func TestProjectPendingIssueCommentCacheMakesRepairedCommentsSearchable(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "repair", Owner: "owner", Name: "repo", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	record := cache.Record{RepoID: "repair", ID: "ISSUE-88", Type: "issue", Path: "issues/88.md", Title: "Issue 88", Status: "open", ContentHash: "issue", Provenance: cache.ProvenanceRemote, RemoteType: "issue", RemoteID: "88", CreatedAt: now, UpdatedAt: now}
	comment := cache.RecordComment{RepoID: "repair", RecordID: record.ID, CommentID: "comment-1", Body: "repaired comment searchable", ContentHash: "comment", CreatedAt: now, UpdatedAt: now}
	if err := store.UpsertRecordGraph(ctx, cache.RecordGraph{Record: record, Comments: []cache.RecordComment{comment}}); err != nil {
		t.Fatal(err)
	}
	item := cache.IssueCommentSync{RepoID: "repair", SourceID: record.ID, IssueNumber: 88, RemoteID: "88", ExpectedCount: 1, Status: "pending", UpdatedAt: now}
	svc := New(store)
	if err := svc.projectPendingIssueCommentCache(ctx, []cache.IssueCommentSync{item}); err != nil {
		t.Fatal(err)
	}
	projection, err := store.GetSourceScoped(ctx, "repair", "ISSUECOMMENT-88-comment-1")
	if err != nil || projection.Body != comment.Body {
		t.Fatalf("projection=%#v err=%v", projection, err)
	}
}

func TestProjectRepairedIssueCommentCacheLeavesUnrelatedQueueUntouched(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "repair", Owner: "owner", Name: "repo", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	for _, item := range []struct {
		id     string
		number int
		status string
	}{
		{id: "ISSUE-88", number: 88, status: "pending"},
		{id: "ISSUE-89", number: 89, status: "deferred"},
		{id: "ISSUE-90", number: 90, status: "pending"},
	} {
		record := cache.Record{RepoID: "repair", ID: item.id, Type: "issue", Path: fmt.Sprintf("issues/%d.md", item.number), Title: item.id, Status: "open", ContentHash: item.id, Provenance: cache.ProvenanceRemote, RemoteType: "issue", RemoteID: fmt.Sprint(item.number), CreatedAt: now, UpdatedAt: now}
		comment := cache.RecordComment{RepoID: "repair", RecordID: item.id, CommentID: "comment-1", Body: "comment " + item.id, ContentHash: item.id + "-comment", CreatedAt: now, UpdatedAt: now}
		if err := store.UpsertRecordGraph(ctx, cache.RecordGraph{Record: record, Comments: []cache.RecordComment{comment}}); err != nil {
			t.Fatal(err)
		}
		if err := store.UpsertIssueCommentSync(ctx, cache.IssueCommentSync{RepoID: "repair", SourceID: item.id, IssueNumber: item.number, RemoteID: fmt.Sprint(item.number), Status: item.status, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	svc := New(store)
	if err := svc.projectRepairedIssueCommentCache(ctx, "repair", []string{"ISSUE-88", "ISSUE-89"}); err != nil {
		t.Fatal(err)
	}
	for _, sourceID := range []string{"ISSUECOMMENT-88-comment-1", "ISSUECOMMENT-89-comment-1"} {
		if _, err := store.GetSourceScoped(ctx, "repair", sourceID); err != nil {
			t.Fatalf("repaired projection %s missing: %v", sourceID, err)
		}
	}
	if _, err := store.GetSourceScoped(ctx, "repair", "ISSUECOMMENT-90-comment-1"); err == nil {
		t.Fatal("unrelated pending issue comment was projected")
	}
	queue, ok, err := store.GetIssueCommentSync(ctx, "repair", "ISSUE-90")
	if err != nil || !ok || queue.Status != "pending" {
		t.Fatalf("unrelated queue=%+v ok=%t err=%v", queue, ok, err)
	}
}

func TestSearchSources(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	results, err := svc.SearchSources(ctx, SearchSourcesRequest{RepoID: "fixture-a", Query: "backlog", Limit: 10})
	if err != nil {
		t.Fatalf("SearchSources returned error: %v", err)
	}
	if len(results.Results) != 2 {
		t.Fatalf("SearchSources returned %d results, want 2", len(results.Results))
	}
	if results.RequestedMode != SearchModeHybrid || results.EffectiveMode != SearchModeFullText || results.FallbackReason != "rag_disabled" {
		t.Fatalf("default fallback contract=%#v", results)
	}
	if results.RepoID != "fixture-a" || results.Query != "backlog" || results.Results[0].ID == "" || results.Results[0].Path == "" || results.Results[0].Title == "" || results.Results[0].Kind == "" || results.Results[0].Status == "" || results.Results[0].Snippet == "" || results.Results[0].LineStart == nil || results.Results[0].LineEnd == nil {
		t.Fatalf("SearchSources result missing contract fields: %#v", results)
	}

	missing, err := svc.SearchSources(ctx, SearchSourcesRequest{RepoID: "fixture-a", Query: "NONEXISTENT", Limit: 10})
	if err != nil {
		t.Fatalf("SearchSources missing query returned error: %v", err)
	}
	if len(missing.Results) != 0 {
		t.Fatalf("SearchSources missing query returned %d results, want 0", len(missing.Results))
	}
}

func TestSearchSourcesFullTextNeverCallsRAG(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	calls := 0
	svc.SetRAGSearchProvider(func(context.Context, rag.SearchRequest) (rag.SearchResult, error) {
		calls++
		return rag.SearchResult{}, nil
	})
	result, err := svc.SearchSources(ctx, SearchSourcesRequest{RepoID: "fixture-a", Query: "backlog", Mode: "full-text", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 || result.RequestedMode != SearchModeFullText || result.EffectiveMode != SearchModeFullText || result.RAGState != "not_requested" || len(result.Results) != 2 {
		t.Fatalf("calls=%d result=%#v", calls, result)
	}
	exact, err := svc.SearchSources(ctx, SearchSourcesRequest{RepoID: "fixture-a", Query: "DOC-123", Mode: SearchModeFullText, Limit: 10})
	if err != nil || calls != 0 || len(exact.Results) != 1 || exact.Results[0].ID != "DOC-123" || !exact.Results[0].Match.ExactMatch {
		t.Fatalf("exact=%#v calls=%d err=%v", exact, calls, err)
	}
}

func TestSearchSourcesHybridGroupsSemanticChunksBySource(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	svc.SetRAGSearchProvider(func(_ context.Context, req rag.SearchRequest) (rag.SearchResult, error) {
		if req.TopK < 40 || req.Limit < 80 {
			t.Fatalf("bounded semantic candidates=%#v", req)
		}
		return rag.SearchResult{
			Status:    rag.RAGSearchStatusReady,
			Namespace: rag.NamespaceStatus{ID: "namespace-1", Exists: true, Current: true},
			Coverage:  rag.CoverageStatus{TotalChunks: 4, EmbeddedChunks: 3, MissingChunks: 1},
			Results: []rag.SearchContext{
				{ChunkID: "doc-a", SourceID: "DOC-123", LineStart: 2, LineEnd: 2, Snippet: "русский концептуальный фрагмент", Score: rag.ScoreBreakdown{Semantic: .99}},
				{ChunkID: "doc-b", SourceID: "DOC-123", LineStart: 3, LineEnd: 3, Snippet: "中文语义片段", Score: rag.ScoreBreakdown{Semantic: .98}},
				{ChunkID: "task-a", SourceID: "TASK-001", LineStart: 1, LineEnd: 1, Snippet: "English semantic citation", Score: rag.ScoreBreakdown{Semantic: .80}},
			},
		}, nil
	})
	result, err := svc.SearchSources(ctx, SearchSourcesRequest{RepoID: "fixture-a", Query: "как искать по смыслу", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if result.RequestedMode != SearchModeHybrid || result.EffectiveMode != SearchModeHybrid || result.RAGState != "partial" || result.FallbackReason != "" || result.Coverage.Ratio != .75 || result.Repair.State != "needed" {
		t.Fatalf("hybrid state=%#v", result)
	}
	if len(result.Results) != 2 || result.Results[0].ID != "DOC-123" || len(result.Results[0].Citations) != 2 || result.Results[1].ID != "TASK-001" {
		t.Fatalf("grouped results=%#v", result.Results)
	}
	if result.Results[0].Match.SemanticRank != 1 || result.Results[0].Match.SemanticScore != .99 || result.Results[0].Match.LexicalRank != 0 || result.Results[0].Snippet != "русский концептуальный фрагмент" {
		t.Fatalf("top result=%#v", result.Results[0])
	}
}

func TestSearchSourcesHybridAppliesFiltersBeforeSemanticTopK(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	svc.SetRAGSearchProvider(func(_ context.Context, req rag.SearchRequest) (rag.SearchResult, error) {
		if strings.Join(req.SourceIDs, ",") != "TASK-001" {
			t.Fatalf("semantic source filter=%#v, want TASK-001", req.SourceIDs)
		}
		return rag.SearchResult{Status: rag.RAGSearchStatusReady, Namespace: rag.NamespaceStatus{ID: "namespace-1", Exists: true, Current: true}, Coverage: rag.CoverageStatus{TotalChunks: 1, EmbeddedChunks: 1}, Results: []rag.SearchContext{
			{ChunkID: "task", SourceID: "TASK-001", Snippet: "filtered semantic result", Score: rag.ScoreBreakdown{Semantic: .5}},
		}}, nil
	})
	result, err := svc.SearchSources(ctx, SearchSourcesRequest{RepoID: "fixture-a", Query: "semantic only", Kind: "task", Provenance: "fixture", Limit: 1})
	if err != nil || len(result.Results) != 1 || result.Results[0].ID != "TASK-001" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestSearchSourcesHybridExactIDBoostAndProviderFallback(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	svc.SetRAGSearchProvider(func(_ context.Context, req rag.SearchRequest) (rag.SearchResult, error) {
		if req.Query == "backlog" {
			return rag.SearchResult{Status: rag.RAGSearchStatusProviderNotReady, FailureClass: rag.ProviderFailureUnavailable}, nil
		}
		if req.Query == "same" {
			return rag.SearchResult{Status: rag.RAGSearchStatusNoNamespace}, nil
		}
		return rag.SearchResult{Status: rag.RAGSearchStatusReady, Namespace: rag.NamespaceStatus{ID: "namespace-1", Exists: true, Current: true}, Coverage: rag.CoverageStatus{TotalChunks: 2, EmbeddedChunks: 2}, Results: []rag.SearchContext{
			{ChunkID: "task", SourceID: "TASK-001", Snippet: "broad match", Score: rag.ScoreBreakdown{Semantic: .99}},
			{ChunkID: "doc", SourceID: "DOC-123", Snippet: "exact source", Score: rag.ScoreBreakdown{Semantic: .50}},
		}}, nil
	})
	exact, err := svc.SearchSources(ctx, SearchSourcesRequest{RepoID: "fixture-a", Query: "DOC-123", Limit: 10})
	if err != nil || len(exact.Results) != 2 || exact.Results[0].ID != "DOC-123" || !exact.Results[0].Match.ExactMatch {
		t.Fatalf("exact=%#v err=%v", exact, err)
	}
	fallback, err := svc.SearchSources(ctx, SearchSourcesRequest{RepoID: "fixture-a", Query: "backlog", Limit: 10})
	if err != nil || fallback.EffectiveMode != SearchModeFullText || fallback.FallbackReason != "provider_unavailable" || len(fallback.Results) != 2 {
		t.Fatalf("fallback=%#v err=%v", fallback, err)
	}
	missing, err := svc.SearchSources(ctx, SearchSourcesRequest{RepoID: "fixture-a", Query: "same", Limit: 10})
	if err != nil || missing.EffectiveMode != SearchModeFullText || missing.FallbackReason != "rag_namespace_missing" || len(missing.Results) != 2 {
		t.Fatalf("missing namespace=%#v err=%v", missing, err)
	}
}

func TestSearchSourcesHybridPropagatesCancellationAndInternalErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	svc := seededService(t, ctx)
	svc.SetRAGSearchProvider(func(context.Context, rag.SearchRequest) (rag.SearchResult, error) {
		cancel()
		return rag.SearchResult{}, &rag.ProviderError{Class: rag.ProviderFailureTimeout, Message: "cancelled provider"}
	})
	if _, err := svc.SearchSources(ctx, SearchSourcesRequest{RepoID: "fixture-a", Query: "backlog"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation err=%v, want context.Canceled", err)
	}

	internalErr := errors.New("cache invariant failed")
	svc = seededService(t, context.Background())
	svc.SetRAGSearchProvider(func(context.Context, rag.SearchRequest) (rag.SearchResult, error) {
		return rag.SearchResult{}, internalErr
	})
	if _, err := svc.SearchSources(context.Background(), SearchSourcesRequest{RepoID: "fixture-a", Query: "backlog"}); !errors.Is(err, internalErr) {
		t.Fatalf("internal err=%v, want propagation", err)
	}

	svc.SetRAGSearchProvider(func(context.Context, rag.SearchRequest) (rag.SearchResult, error) {
		return rag.SearchResult{}, &rag.ProviderError{Class: rag.ProviderFailureUnavailable, Message: "provider down"}
	})
	fallback, err := svc.SearchSources(context.Background(), SearchSourcesRequest{RepoID: "fixture-a", Query: "backlog"})
	if err != nil || fallback.EffectiveMode != SearchModeFullText || fallback.FallbackReason != "provider_unavailable" {
		t.Fatalf("typed provider fallback=%#v err=%v", fallback, err)
	}
}

func TestSearchSourcesHybridMultilingualSemanticOnly(t *testing.T) {
	for _, query := range []string{"фоновый процесс завершается", "后台进程提前退出", "background process exits early"} {
		t.Run(query, func(t *testing.T) {
			ctx := context.Background()
			svc := seededService(t, ctx)
			svc.SetRAGSearchProvider(func(context.Context, rag.SearchRequest) (rag.SearchResult, error) {
				return rag.SearchResult{Status: rag.RAGSearchStatusReady, Namespace: rag.NamespaceStatus{ID: "namespace-1", Exists: true, Current: true}, Coverage: rag.CoverageStatus{TotalChunks: 1, EmbeddedChunks: 1}, Results: []rag.SearchContext{{ChunkID: "doc", SourceID: "DOC-123", Snippet: "daemon lifetime", Score: rag.ScoreBreakdown{Semantic: .9}}}}, nil
			})
			result, err := svc.SearchSources(ctx, SearchSourcesRequest{RepoID: "fixture-a", Query: query, Limit: 5})
			if err != nil || result.EffectiveMode != SearchModeHybrid || len(result.Results) != 1 || result.Results[0].ID != "DOC-123" {
				t.Fatalf("result=%#v err=%v", result, err)
			}
		})
	}
}

func TestSearchSourcesFiltersProvenance(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	if err := svc.store.UpsertSource(ctx, cache.Source{RepoID: "fixture-a", ID: "LIVE-1", Kind: "doc", Path: "docs/live.md", Title: "Live Backlog", Body: "backlog live-only", Status: "ready", ContentHash: "live-hash", Provenance: cache.ProvenanceLive, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}

	results, err := svc.SearchSources(ctx, SearchSourcesRequest{RepoID: "fixture-a", Query: "backlog", Provenance: "live", Limit: 10})
	if err != nil {
		t.Fatalf("SearchSources returned error: %v", err)
	}
	if len(results.Results) != 1 || results.Results[0].ID != "LIVE-1" || results.Results[0].Provenance != "live" {
		t.Fatalf("SearchSources provenance filter = %#v", results.Results)
	}
}

func TestGetSource(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	record, err := svc.GetSource(ctx, GetSourceRequest{RepoID: "fixture-a", ID: "DOC-123"})
	if err != nil {
		t.Fatalf("GetSource returned error: %v", err)
	}
	if record.ID != "DOC-123" || record.RemoteAlias != "remote:wiki/design" || len(record.Links) != 1 || len(record.Backlinks) != 1 || len(record.Labels) != 2 {
		t.Fatalf("GetSource returned incomplete record: %#v", record)
	}
}

func TestListSources(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	results, err := svc.ListSources(ctx, ListSourcesRequest{RepoID: "fixture-a", Kind: "task", Status: "ready", Limit: 1})
	if err != nil {
		t.Fatalf("ListSources returned error: %v", err)
	}
	if len(results.Results) != 1 || results.Results[0].ID != "TASK-001" || results.RepoID != "fixture-a" {
		t.Fatalf("ListSources = %#v, want TASK-001", results)
	}
}

func TestListSourcesFiltersProvenance(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	if err := svc.store.UpsertSource(ctx, cache.Source{RepoID: "fixture-a", ID: "LIVE-1", Kind: "doc", Path: "docs/live.md", Title: "Live Backlog", Body: "backlog live-only", Status: "ready", ContentHash: "live-hash", Provenance: cache.ProvenanceLive, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}

	results, err := svc.ListSources(ctx, ListSourcesRequest{RepoID: "fixture-a", Provenance: "live"})
	if err != nil {
		t.Fatalf("ListSources returned error: %v", err)
	}
	if len(results.Results) != 1 || results.Results[0].ID != "LIVE-1" || results.Results[0].Provenance != "live" {
		t.Fatalf("ListSources provenance filter = %#v", results.Results)
	}
}

func TestGetBacklinks(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	results, err := svc.GetBacklinks(ctx, GetBacklinksRequest{RepoID: "fixture-a", AliasType: "remote", AliasID: "wiki/design"})
	if err != nil {
		t.Fatalf("GetBacklinks returned error: %v", err)
	}
	if len(results.Backlinks) != 1 || results.Backlinks[0].ID != "TASK-001" || results.Backlinks[0].TargetID != "DOC-123" || results.ID != "DOC-123" {
		t.Fatalf("GetBacklinks = %#v", results)
	}
}

func TestResolveID(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	resolved, err := svc.ResolveID(ctx, ResolveIDRequest{RepoID: "fixture-a", AliasType: "path", AliasID: "docs/design.md"})
	if err != nil {
		t.Fatalf("ResolveID returned error: %v", err)
	}
	if resolved.ID != "DOC-123" || resolved.Path != "docs/design.md" || resolved.RemoteAlias != "remote:wiki/design" {
		t.Fatalf("ResolveID = %#v", resolved)
	}
}

func TestIssueReadResultsExposeStableIDAndIssueNumber(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSourceGraph(ctx, cache.SourceGraph{
		Source:     cache.Source{RepoID: "fixture-a", ID: "ISSUE-42", Kind: "issue", Path: "issues/42.md", Title: "Issue", Body: "body", Status: "open", ContentHash: "issue-42"},
		Identities: []cache.Identity{{RepoID: "fixture-a", SourceID: "ISSUE-42", AliasType: "issue", Alias: "42", Remote: cache.RemoteAlias{Type: "issue", ID: "42"}}},
	}); err != nil {
		t.Fatal(err)
	}
	svc := New(store)

	record, err := svc.GetSource(ctx, GetSourceRequest{RepoID: "fixture-a", ID: "ISSUE-42"})
	if err != nil {
		t.Fatal(err)
	}
	if record.StableSourceID != "ISSUE-42" || record.IssueNumber != 42 {
		t.Fatalf("record=%#v", record)
	}
	listed, err := svc.ListSources(ctx, ListSourcesRequest{RepoID: "fixture-a", Kind: "issue", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Results) == 0 || listed.Results[0].StableSourceID != "ISSUE-42" || listed.Results[0].IssueNumber != 42 {
		t.Fatalf("listed=%#v", listed)
	}
	recent, err := svc.RecentChanges(ctx, RecentChangesRequest{RepoID: "fixture-a", Kind: "issue", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(recent.Results) == 0 || recent.Results[0].StableSourceID != "ISSUE-42" || recent.Results[0].IssueNumber != 42 {
		t.Fatalf("recent=%#v", recent)
	}
	resolved, err := svc.ResolveID(ctx, ResolveIDRequest{RepoID: "fixture-a", ID: "issue:42"})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.StableSourceID != "ISSUE-42" || resolved.IssueNumber != 42 {
		t.Fatalf("resolved=%#v", resolved)
	}
}

func TestGetSnippet(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	snippet, err := svc.GetSnippet(ctx, SnippetRequest{RepoID: "fixture-a", ID: "DOC-123", LineStart: 2, LineEnd: 99})
	if err != nil {
		t.Fatalf("GetSnippet returned error: %v", err)
	}
	if snippet.LineStart != 2 || snippet.LineEnd != 3 || len(snippet.Warnings) != 1 || snippet.Text == "" {
		t.Fatalf("GetSnippet did not clamp as expected: %#v", snippet)
	}
	chunk, err := svc.GetSnippet(ctx, SnippetRequest{RepoID: "fixture-a", ID: "DOC-123", ChunkID: "chunk-doc"})
	if err != nil {
		t.Fatalf("GetSnippet chunk returned error: %v", err)
	}
	if chunk.Text != "backlog chunk" || chunk.LineStart != 2 {
		t.Fatalf("GetSnippet chunk = %#v", chunk)
	}
}

func TestGetSyncStatus(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	status, err := svc.GetSyncStatus(ctx, SyncStatusRequest{RepoID: "fixture-a", ID: "DOC-123"})
	if err != nil {
		t.Fatalf("GetSyncStatus returned error: %v", err)
	}
	if status.Status != "fresh" || status.RemoteID != "wiki/design" {
		t.Fatalf("GetSyncStatus = %#v", status)
	}
}

func TestRepoScopedAliasResolution(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	svc := New(store)
	for _, repoID := range []string{"fixture-a", "fixture-b"} {
		if _, err := svc.AddRepository(ctx, AddRepositoryRequest{RepoID: repoID, Owner: "owner", Name: repoID, APIBaseURL: "https://example.invalid/api", Scopes: []string{"issues,wiki"}}); err != nil {
			t.Fatal(err)
		}
		if err := store.UpsertSourceGraph(ctx, cache.SourceGraph{Source: cache.Source{RepoID: repoID, ID: "ISSUE-42", Kind: "issue", Path: repoID + "/issues/42.md", Title: repoID + " issue", Body: repoID + " body", Status: "open", ContentHash: repoID + "-hash"}, Identities: []cache.Identity{{RepoID: repoID, AliasType: "issue", Alias: "42", Remote: cache.RemoteAlias{Type: "issue", ID: "42"}}}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.UpsertSourceGraph(ctx, cache.SourceGraph{Source: cache.Source{RepoID: "fixture-a", ID: "WIKI-HOME", Kind: "wiki", Path: "wiki/Home.md", Title: "Home", Body: "home", Status: "fresh", ContentHash: "home-hash"}, Identities: []cache.Identity{{RepoID: "fixture-a", AliasType: "wiki", Alias: "Home", Remote: cache.RemoteAlias{Type: "wiki", ID: "Home"}}}}); err != nil {
		t.Fatal(err)
	}
	a, err := svc.GetSource(ctx, GetSourceRequest{RepoID: "fixture-a", ID: "issue:42"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.GetSource(ctx, GetSourceRequest{RepoID: "fixture-b", ID: "issue:42"})
	if err != nil {
		t.Fatal(err)
	}
	if a.RepoID != "fixture-a" || b.RepoID != "fixture-b" || a.Body == b.Body {
		t.Fatalf("scoped records crossed repos: a=%#v b=%#v", a, b)
	}
	if err := svc.DiagnoseUnscopedAlias(ctx, "issue", "42"); !errors.As(err, &ErrAmbiguousAlias{}) {
		t.Fatalf("DiagnoseUnscopedAlias error=%v want ambiguous", err)
	}
	wiki, err := svc.ResolveID(ctx, ResolveIDRequest{RepoID: "fixture-a", ID: "wiki:Home"})
	if err != nil || wiki.ID != "WIKI-HOME" || wiki.RepoID != "fixture-a" {
		t.Fatalf("wiki resolve=%#v err=%v", wiki, err)
	}
	_, err = svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-a", SourceIDs: []string{"ISSUE-42"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-a", SourceIDs: []string{"fixture-b:ISSUE-42"}})
	var empty ErrCacheEmpty
	if !errors.As(err, &empty) {
		t.Fatalf("cross repo export err=%v want cache empty", err)
	}
}

func TestDisabledWikiScopeRejectedBeforeClient(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "issues-only", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	client := &fakeGitCodeClient{wiki: gitcode.WikiPage{Slug: "Home", Title: "Home", Body: "body"}}
	svc := NewWithClient(store, client)
	svc.lockPath = filepath.Join(t.TempDir(), "sync.lock")
	_, err = svc.SyncToCache(ctx, SyncRequest{RepoID: "issues-only", RemoteAlias: "wiki:Home", IdempotencyKey: "wiki-disabled"})
	var invalid ErrInvalidQuery
	if !errors.As(err, &invalid) || client.wikiCalls != 0 {
		t.Fatalf("sync err=%v wikiCalls=%d want local validation before client", err, client.wikiCalls)
	}
	_, err = svc.CreatePage(ctx, WriteCommandRequest{Repo: "issues-only", Title: "Home", Body: "body"})
	if !errors.As(err, &invalid) {
		t.Fatalf("write err=%v want invalid disabled scope", err)
	}
}

func TestSyncGraphFixtureOfflineReadsIssueWikiCommentsAndChunks(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner-a", Name: "repo-a", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	svc := NewWithClient(store, &fakeGitCodeClient{issue: gitcode.Issue{Number: 42, Title: "Fixture Issue", Body: "# Issue\n\nremote issue body", State: "open", CreatedAt: base, UpdatedAt: base}, comments: []gitcode.Comment{{ID: "c1", Author: "fixture-user", Body: "comment", CreatedAt: base, UpdatedAt: base}}, wiki: gitcode.WikiPage{Slug: "Home", Title: "Fixture Wiki", Body: "# Wiki\n\nremote wiki body", Revision: "rev-home", CreatedAt: base, UpdatedAt: base}})
	svc.lockPath = filepath.Join(t.TempDir(), "sync.lock")
	if _, err := svc.SyncToCache(ctx, SyncRequest{RepoID: "fixture-a", RemoteAlias: "issue:42", IdempotencyKey: "sync-issue-42"}); err != nil {
		t.Fatalf("issue sync returned error: %v", err)
	}
	if _, err := svc.SyncToCache(ctx, SyncRequest{RepoID: "fixture-a", RemoteAlias: "wiki:Home", IdempotencyKey: "sync-wiki-home"}); err != nil {
		t.Fatalf("wiki sync returned error: %v", err)
	}
	results, err := svc.SearchSources(ctx, SearchSourcesRequest{RepoID: "fixture-a", Query: "remote"})
	if err != nil || len(results.Results) < 2 {
		t.Fatalf("search results = %#v, %v", results, err)
	}
	issue, err := svc.GetSource(ctx, GetSourceRequest{RepoID: "fixture-a", ID: "ISSUE-42"})
	if err != nil || issue.Kind != "issue" {
		t.Fatalf("issue get = %#v, %v", issue, err)
	}
	wiki, err := svc.GetSource(ctx, GetSourceRequest{RepoID: "fixture-a", ID: "WIKI-HOME"})
	if err != nil || wiki.Kind != "wiki" {
		t.Fatalf("wiki get = %#v, %v", wiki, err)
	}
	comment, err := svc.GetSource(ctx, GetSourceRequest{RepoID: "fixture-a", ID: "ISSUECOMMENT-42-c1"})
	if err != nil || comment.Kind != "issue_comment" || comment.Body != "comment" || len(comment.Links) != 1 || comment.Links[0].TargetID != "ISSUE-42" {
		t.Fatalf("comment get = %#v, %v", comment, err)
	}
	snippet, err := svc.GetSnippet(ctx, SnippetRequest{RepoID: "fixture-a", ID: "ISSUE-42", LineStart: 1, LineEnd: 2})
	if err != nil || snippet.Text == "" {
		t.Fatalf("snippet = %#v, %v", snippet, err)
	}
	record, err := store.GetRecord(ctx, "fixture-a", "ISSUE-42")
	if err != nil || len(record.Comments) != 1 {
		t.Fatalf("record = %#v, %v", record, err)
	}
	counts, err := store.RecordCounts(ctx, "fixture-a")
	if err != nil {
		t.Fatal(err)
	}
	if counts.Records != 3 || counts.Comments != 1 || counts.IdentityAliases != 3 || counts.SyncEvents != 2 || counts.RemoteRevisions != 3 || counts.Chunks != 3 {
		t.Fatalf("counts = %#v", counts)
	}
}

func TestScenario006LiveGraphValidStagesIssueWikiComments(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "live-a", Owner: "owner-a", Name: "repo-a", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	svc := NewWithClient(store, &fakeGitCodeClient{
		issue:    gitcode.Issue{ID: "MOCK-ISSUE-100", Number: 100, Title: "Mock Issue", Body: "mock issue body", State: "open", CreatedAt: base, UpdatedAt: base},
		comments: []gitcode.Comment{{ID: "MOCK-COMMENT-1", IssueID: "MOCK-ISSUE-100", Author: "mock-user", Body: "mock comment", CreatedAt: base, UpdatedAt: base}},
		wiki:     gitcode.WikiPage{ID: "MOCK-WIKI-LIVE", Slug: "Live", Title: "Mock Wiki", Body: "mock wiki body", Revision: "rev-live", CreatedAt: base, UpdatedAt: base},
	})
	svc.providerMode = gitcode.ProviderModeLive
	svc.lockPath = filepath.Join(t.TempDir(), "sync.lock")
	if _, err := svc.SyncToCache(ctx, SyncRequest{RepoID: "live-a", RemoteAlias: "issue:100", IdempotencyKey: "sc-006-live-issue"}); err != nil {
		t.Fatalf("live issue sync returned error: %v", err)
	}
	if _, err := svc.SyncToCache(ctx, SyncRequest{RepoID: "live-a", RemoteAlias: "wiki:Live", IdempotencyKey: "sc-006-live-wiki"}); err != nil {
		t.Fatalf("live wiki sync returned error: %v", err)
	}
	issue, err := store.GetRecord(ctx, "live-a", "ISSUE-MOCK-ISSUE-100")
	if err != nil || len(issue.Comments) != 1 || issue.Comments[0].CommentID != "MOCK-COMMENT-1" {
		t.Fatalf("live issue record = %#v err=%v", issue, err)
	}
	coverage, ok, err := store.GetIssueCommentSync(ctx, "live-a", "ISSUE-MOCK-ISSUE-100")
	if err != nil || !ok || coverage.Status != "complete" || coverage.ExpectedCount != 1 {
		t.Fatalf("targeted issue comment coverage = %#v ok=%v err=%v", coverage, ok, err)
	}
	if _, err := store.GetRecord(ctx, "live-a", "WIKI-MOCK-WIKI-LIVE"); err != nil {
		t.Fatalf("live wiki missing: %v", err)
	}
	if _, err := store.GetRecord(ctx, "live-a", "ISSUE-42"); err == nil {
		t.Fatal("fixture issue marker committed in live sync")
	}
	if _, err := store.GetRecord(ctx, "live-a", "WIKI-HOME"); err == nil {
		t.Fatal("fixture wiki marker committed in live sync")
	}
}

func TestScenario006LiveGraphInvalidRejectedBeforeCommit(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "live-invalid", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		issue    gitcode.Issue
		comments []gitcode.Comment
	}{
		{name: "missing-comment-id", issue: gitcode.Issue{ID: "MOCK-ISSUE-100", Number: 100, Title: "Mock Issue", Body: "body", State: "open", CreatedAt: base, UpdatedAt: base}, comments: []gitcode.Comment{{IssueID: "MOCK-ISSUE-100", Body: "comment", CreatedAt: base, UpdatedAt: base}}},
		{name: "unreconciled-parent", issue: gitcode.Issue{ID: "MOCK-ISSUE-100", Number: 100, Title: "Mock Issue", Body: "body", State: "open", CreatedAt: base, UpdatedAt: base}, comments: []gitcode.Comment{{ID: "MOCK-COMMENT-1", IssueID: "OTHER-ISSUE", Body: "comment", CreatedAt: base, UpdatedAt: base}}},
		{name: "fixture-marker", issue: gitcode.Issue{ID: "42", Number: 42, Title: "Fixture Issue", Body: "body", State: "open", CreatedAt: base, UpdatedAt: base}, comments: []gitcode.Comment{{ID: "MOCK-COMMENT-1", IssueID: "42", Body: "comment", CreatedAt: base, UpdatedAt: base}}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewWithClient(store, &fakeGitCodeClient{issue: tt.issue, comments: tt.comments})
			svc.providerMode = gitcode.ProviderModeLive
			svc.lockPath = filepath.Join(t.TempDir(), "sync.lock")
			_, err := svc.SyncToCache(ctx, SyncRequest{RepoID: "live-invalid", RemoteAlias: fmt.Sprintf("issue:%d", tt.issue.Number), IdempotencyKey: "sc-006-" + tt.name})
			var failure ErrSyncFailure
			if !errors.As(err, &failure) || failure.Mode != "live_graph_invalid" {
				t.Fatalf("error = %T %v, want live_graph_invalid", err, err)
			}
		})
	}
	counts, err := store.RecordCounts(ctx, "live-invalid")
	if err != nil {
		t.Fatal(err)
	}
	if counts.Records != 0 || counts.Comments != 0 {
		t.Fatalf("invalid live graph committed counts = %#v", counts)
	}
}

func TestScenario006LiveAuthFailureNormalized(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "live-auth", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, &fakeGitCodeClient{errors: []error{gitcode.ErrAuthExpired{Endpoint: "/issues/100", Status: 401, Message: "invalid token"}}})
	svc.providerMode = gitcode.ProviderModeLive
	svc.lockPath = filepath.Join(t.TempDir(), "sync.lock")
	_, err = svc.SyncToCache(ctx, SyncRequest{RepoID: "live-auth", RemoteAlias: "issue:100", IdempotencyKey: "sc-006-auth"})
	var failure ErrSyncFailure
	if !errors.As(err, &failure) || failure.Mode != "live_auth_failure" {
		t.Fatalf("error = %T %v, want live_auth_failure", err, err)
	}
}

func TestSyncStateMachine(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner-a", Name: "repo-a", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSourceGraph(ctx, cache.SourceGraph{Source: cache.Source{RepoID: "fixture-a", ID: "DOC-123", Kind: "wiki", Path: "wiki/design.md", Title: "Old", Body: "old", Status: "fresh", ContentHash: "old-hash", CreatedAt: base, UpdatedAt: base.Add(time.Hour)}, Identities: []cache.Identity{{RepoID: "fixture-a", AliasType: "remote", Alias: "wiki/design", Remote: cache.RemoteAlias{Type: "remote", ID: "wiki/design"}}}, SyncStatus: &cache.SyncStatus{RepoID: "fixture-a", RemoteType: "remote", RemoteID: "wiki/design", RemoteRevision: "old", Status: "fresh", LastFetchedAt: base}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, &fakeGitCodeClient{wiki: gitcode.WikiPage{Slug: "wiki/design", Title: "Design", Body: "new body", Revision: "rev-2", CreatedAt: base, UpdatedAt: base.Add(2 * time.Hour)}})
	svc.lockPath = filepath.Join(t.TempDir(), "sync.lock")
	before, err := svc.GetSyncStatus(ctx, SyncStatusRequest{RepoID: "fixture-a", ID: "DOC-123"})
	if err != nil {
		t.Fatal(err)
	}
	if before.Freshness != FreshnessStale {
		t.Fatalf("before freshness=%s want stale", before.Freshness)
	}
	result, err := svc.SyncToCache(ctx, SyncRequest{RepoID: "fixture-a", StableID: "DOC-123", IdempotencyKey: "sync-key"})
	if err != nil {
		t.Fatalf("SyncToCache returned error: %v", err)
	}
	if result.Status != "succeeded" || result.IdempotencyKey != "sync-key" || result.Counts.Updated != 1 {
		t.Fatalf("SyncToCache result = %#v", result)
	}
	after, err := svc.GetSyncStatus(ctx, SyncStatusRequest{RepoID: "fixture-a", ID: "DOC-123"})
	if err != nil {
		t.Fatal(err)
	}
	if after.Freshness != FreshnessFresh || after.RemoteRevision != "rev-2" {
		t.Fatalf("after status = %#v", after)
	}
	event, err := store.GetSyncEventByKey(ctx, "sync-key")
	if err != nil {
		t.Fatal(err)
	}
	if event == nil || event.Status != "succeeded" || event.IdempotencyKey != "sync-key" || event.Message == "" {
		t.Fatalf("sync event = %#v", event)
	}
	source, err := store.GetSource(ctx, "DOC-123")
	if err != nil {
		t.Fatal(err)
	}
	if source.Body != "new body" || source.Title != "Design" {
		t.Fatalf("source not updated: %#v", source)
	}
	record, err := store.GetRecord(ctx, "fixture-a", "DOC-123")
	if err != nil {
		t.Fatal(err)
	}
	if record.RemoteType != "remote" || record.RemoteID != "wiki/design" {
		t.Fatalf("record = %#v", record)
	}
	chunks, err := store.GetChunksScoped(ctx, "fixture-a", "DOC-123")
	if err != nil || len(chunks) == 0 {
		t.Fatalf("chunks = %#v, %v", chunks, err)
	}
}

func TestSyncLockContention(t *testing.T) {
	ctx := context.Background()
	svc := seededSyncService(t, ctx, nil)
	svc.lockPath = filepath.Join(t.TempDir(), "sync.lock")
	lock, err := svc.store.AcquireLock(ctx, svc.lockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.store.ReleaseLock(ctx, lock)
	client := &fakeGitCodeClient{wiki: gitcode.WikiPage{Slug: "wiki/design", Title: "Design", Body: "new"}}
	svc.client = client
	_, err = svc.SyncToCache(ctx, SyncRequest{RepoID: "fixture-a", StableID: "DOC-123", IdempotencyKey: "lock-key"})
	var contention cache.ErrLockContention
	if !errors.As(err, &contention) {
		t.Fatalf("error=%v want ErrLockContention", err)
	}
	if client.wikiCalls != 0 {
		t.Fatalf("remote calls=%d want 0", client.wikiCalls)
	}
	source, err := svc.store.GetSource(ctx, "DOC-123")
	if err != nil {
		t.Fatal(err)
	}
	if source.Body != "intro same\nbacklog design same\nfinal" {
		t.Fatalf("source mutated during lock contention: %#v", source)
	}
}

func TestBulkSyncWriterAdmissionFailsBeforeProviderOrCacheWork(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.db")
	lockPath := cachePath + ".lock"
	holderStore, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	defer holderStore.Close()
	if err := holderStore.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner-a", Name: "repo-a", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	contenderStore, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	defer contenderStore.Close()
	client := &fakeGitCodeClient{}
	svc := NewWithClientConfig(contenderStore, client, ServiceConfig{LockPath: lockPath})
	before, err := contenderStore.RecordCounts(ctx, "fixture-a")
	if err != nil {
		t.Fatal(err)
	}
	held, err := holderStore.AcquireWriter(ctx, cache.WriterRequest{Operation: "other-sync", RepoID: "fixture-a", LockPath: lockPath})
	if err != nil {
		t.Fatal(err)
	}
	defer holderStore.ReleaseWriter(ctx, held)

	operations := []struct {
		name string
		call func() (*SyncResourcesResult, error)
	}{
		{name: "issues", call: func() (*SyncResourcesResult, error) {
			return svc.BulkSyncIssues(ctx, BulkSyncRequest{RepoID: "fixture-a"})
		}},
		{name: "issue-comments", call: func() (*SyncResourcesResult, error) {
			return svc.BulkSyncIssueComments(ctx, BulkSyncRequest{RepoID: "fixture-a"})
		}},
		{name: "pulls", call: func() (*SyncResourcesResult, error) {
			return svc.BulkSyncPullRequests(ctx, BulkSyncRequest{RepoID: "fixture-a"})
		}},
		{name: "pr-comments", call: func() (*SyncResourcesResult, error) {
			return svc.BulkSyncPRComments(ctx, BulkSyncRequest{RepoID: "fixture-a"})
		}},
		{name: "wiki", call: func() (*SyncResourcesResult, error) {
			return svc.BulkSyncWiki(ctx, BulkSyncRequest{RepoID: "fixture-a"})
		}},
		{name: "all", call: func() (*SyncResourcesResult, error) {
			return svc.BulkSyncAll(ctx, BulkSyncRequest{RepoID: "fixture-a"})
		}},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			result, err := operation.call()
			if result != nil {
				t.Fatalf("result=%+v, want nil admission failure", result)
			}
			var contention cache.ErrLockContention
			if !errors.As(err, &contention) {
				t.Fatalf("err=%T %v, want ErrLockContention", err, err)
			}
			var partial *PartialSyncError
			if errors.As(err, &partial) {
				t.Fatalf("admission failure wrapped as partial sync: %v", err)
			}
			if contention.Operation != "other-sync" || contention.RepoID != "fixture-a" {
				t.Fatalf("contention=%+v", contention)
			}
		})
	}
	if len(client.listIssueRequests) != 0 || client.listPRCalls != 0 || client.prCommentCalls != 0 || client.listWikiPagesCallCount != 0 || client.commentCalls != 0 {
		t.Fatalf("provider called during admission failure: %+v", client)
	}
	after, err := contenderStore.RecordCounts(ctx, "fixture-a")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("cache mutated during admission failure: before=%+v after=%+v", before, after)
	}
}

func TestBulkSyncAllReusesWriterAdmissionForNestedCollections(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewSQLiteStore(ctx, filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner-a", Name: "repo-a", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	client := &fakeGitCodeClient{}
	svc := NewWithClientConfig(store, client, ServiceConfig{LockPath: filepath.Join(t.TempDir(), "sync.lock")})
	result, err := svc.BulkSyncAll(ctx, BulkSyncRequest{RepoID: "fixture-a"})
	if err != nil {
		t.Fatalf("BulkSyncAll returned error: %v", err)
	}
	if result == nil || client.listWikiPagesCallCount != 1 || len(client.listIssueRequests) != 1 {
		t.Fatalf("result=%+v client=%+v", result, client)
	}
}

func TestBulkSyncWriterAdmissionIsIndependentPerCache(t *testing.T) {
	ctx := context.Background()
	firstPath := filepath.Join(t.TempDir(), "first.db")
	secondPath := filepath.Join(t.TempDir(), "second.db")
	first, err := cache.NewSQLiteStore(ctx, firstPath)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := cache.NewSQLiteStore(ctx, secondPath)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := second.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner-a", Name: "repo-a", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	held, err := first.AcquireWriter(ctx, cache.WriterRequest{Operation: "first-cache-sync", RepoID: "fixture-a", LockPath: firstPath + ".lock"})
	if err != nil {
		t.Fatal(err)
	}
	defer first.ReleaseWriter(ctx, held)
	client := &fakeGitCodeClient{}
	svc := NewWithClientConfig(second, client, ServiceConfig{LockPath: secondPath + ".lock"})
	if _, err := svc.BulkSyncIssues(ctx, BulkSyncRequest{RepoID: "fixture-a"}); err != nil {
		t.Fatalf("independent cache sync returned error: %v", err)
	}
	if len(client.listIssueRequests) != 1 {
		t.Fatalf("independent cache provider calls=%d", len(client.listIssueRequests))
	}
}

func TestSyncIdempotencyReplay(t *testing.T) {
	ctx := context.Background()
	client := &fakeGitCodeClient{wiki: gitcode.WikiPage{Slug: "wiki/design", Title: "Design", Body: "new body", Revision: "rev-2", UpdatedAt: time.Now().UTC()}}
	svc := seededSyncService(t, ctx, client)
	svc.lockPath = filepath.Join(t.TempDir(), "sync.lock")
	first, err := svc.SyncToCache(ctx, SyncRequest{RepoID: "fixture-a", StableID: "DOC-123", IdempotencyKey: "replay-key"})
	if err != nil {
		t.Fatal(err)
	}
	lock, err := svc.store.AcquireLock(ctx, svc.lockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.store.ReleaseLock(ctx, lock)
	second, err := svc.SyncToCache(ctx, SyncRequest{RepoID: "fixture-a", StableID: "DOC-123", IdempotencyKey: "replay-key"})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Replayed || second.SyncEventID != first.SyncEventID {
		t.Fatalf("replay result=%#v first=%#v", second, first)
	}
	if client.wikiCalls != 1 {
		t.Fatalf("remote calls=%d want 1", client.wikiCalls)
	}
}

func TestSyncBoundedStaging(t *testing.T) {
	ctx := context.Background()
	client := &fakeGitCodeClient{wiki: gitcode.WikiPage{Slug: "wiki/design", Title: "Design", Body: "body too large for limit", Revision: "rev-2"}}
	svc := seededSyncService(t, ctx, client)
	svc.lockPath = filepath.Join(t.TempDir(), "sync.lock")
	_, err := svc.SyncToCache(ctx, SyncRequest{RepoID: "fixture-a", StableID: "DOC-123", IdempotencyKey: "large-key", MaxSize: 5})
	var tooLarge gitcode.ErrPayloadTooLarge
	if !errors.As(err, &tooLarge) {
		t.Fatalf("error=%v want ErrPayloadTooLarge", err)
	}
	source, err := svc.store.GetSource(ctx, "DOC-123")
	if err != nil {
		t.Fatal(err)
	}
	if source.Body != "intro same\nbacklog design same\nfinal" || source.ContentHash != index.ContentHash(source.Body) {
		t.Fatalf("source mutated after staging failure: %#v", source)
	}
	event, err := svc.store.GetSyncEventByKey(ctx, "large-key")
	if err != nil {
		t.Fatal(err)
	}
	if event == nil || event.Status != "failed" {
		t.Fatalf("failed event = %#v", event)
	}
}

func TestSyncRetry(t *testing.T) {
	ctx := context.Background()
	client := &fakeGitCodeClient{wiki: gitcode.WikiPage{Slug: "wiki/design", Title: "Design", Body: "new body", Revision: "rev-2", UpdatedAt: time.Now().UTC()}, errors: []error{gitcode.ErrRateLimited{RetryAfter: time.Nanosecond, Endpoint: "/wiki", Attempts: 1}}}
	svc := seededSyncService(t, ctx, client)
	svc.lockPath = filepath.Join(t.TempDir(), "sync.lock")
	result, err := svc.SyncToCache(ctx, SyncRequest{RepoID: "fixture-a", StableID: "DOC-123", IdempotencyKey: "retry-key", MaxAttempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "succeeded" || client.wikiCalls != 2 || result.Counts.Updated != 1 {
		t.Fatalf("result=%#v calls=%d", result, client.wikiCalls)
	}
	event, err := svc.store.GetSyncEventByKey(ctx, "retry-key")
	if err != nil {
		t.Fatal(err)
	}
	if event == nil || event.Status != "succeeded" {
		t.Fatalf("event=%#v", event)
	}
}

func TestProductPathWrappedGitCodeErrorsClassify(t *testing.T) {
	badCodes := map[diagnostics.Code]bool{diagnostics.CodeLiveTransportFailure: true, diagnostics.CodeConfigurationError: true, diagnostics.CodeLiveAPIFailure: true, diagnostics.CodeLiveAuthFailure: true, diagnostics.CodeUnsupportedMockPayload: true}
	tests := []struct {
		name string
		err  error
		ctx  diagnostics.CommandContext
		want diagnostics.Code
	}{
		{name: "SCN-DIAG-PRODUCT-WRAP-01 direct not found", err: gitcode.ErrNotFound{Endpoint: "/issues/404"}, ctx: diagnostics.CommandContext{ProviderMode: "live-http", HTTPStatus: http.StatusNotFound, HTTPAttempted: true}, want: diagnostics.CodeAPIFailure},
		{name: "SCN-DIAG-PRODUCT-WRAP-02 sync conflict", err: ErrSyncFailure{Mode: "conflict", Target: "issue:7", Endpoint: "/issues/7", Cause: gitcode.ErrConflict{Endpoint: "/issues/7", Status: http.StatusConflict}}, ctx: diagnostics.CommandContext{ProviderMode: "live-http", HTTPStatus: http.StatusConflict, HTTPAttempted: true}, want: diagnostics.CodeAPIFailure},
		{name: "SCN-DIAG-PRODUCT-WRAP-03 sync remote collision", err: ErrSyncFailure{Mode: "remote_collision", Target: "issue:7", Endpoint: "/issues/7", Cause: gitcode.ErrRemoteCollision{Endpoint: "/issues/7", Alias: "issue:7", ExistingID: "ISSUE-7", NewID: "ISSUE-8"}}, ctx: diagnostics.CommandContext{ProviderMode: "live-http", HTTPStatus: http.StatusConflict, HTTPAttempted: true}, want: diagnostics.CodeAPIFailure},
		{name: "SCN-DIAG-PRODUCT-WRAP-04 sync remote not found", err: ErrSyncFailure{Mode: "remote_not_found", Target: "issue:404", Endpoint: "/issues/404", Cause: gitcode.ErrRemoteNotFound{Endpoint: "/issues/404", Alias: "issue:404"}}, ctx: diagnostics.CommandContext{ProviderMode: "live-http", HTTPStatus: http.StatusNotFound, HTTPAttempted: true}, want: diagnostics.CodeAPIFailure},
		{name: "SCN-DIAG-PRODUCT-WRAP-05 sync remote payload too large", err: ErrSyncFailure{Mode: "payload_too_large", Target: "issue:*", Endpoint: "/issues", PayloadSource: "remote_status", Cause: gitcode.ErrPayloadTooLarge{Endpoint: "/issues", Limit: 10, Size: 20, Source: "remote_status"}}, ctx: diagnostics.CommandContext{ProviderMode: "live-http", HTTPStatus: http.StatusRequestEntityTooLarge, HTTPAttempted: true, FailureSource: "remote_status"}, want: diagnostics.CodeAPIFailure},
		{name: "SCN-DIAG-PRODUCT-WRAP-06 sync local payload too large", err: ErrSyncFailure{Mode: "payload_too_large", Target: "issue:*", Endpoint: "/issues", PayloadSource: "local_body_limit", Cause: gitcode.ErrPayloadTooLarge{Endpoint: "/issues", Limit: 10, Size: 20, Source: "local_body_limit"}}, ctx: diagnostics.CommandContext{ProviderMode: "live-http", HTTPAttempted: true, FailureSource: "local_body_limit", LocalPayloadTooLarge: true}, want: diagnostics.CodeSchemaDecode},
		{name: "SCN-DIAG-PRODUCT-WRAP-07 sync partial response", err: ErrSyncFailure{Mode: "partial_response", Target: "issue:*", Endpoint: "/issues", Cause: gitcode.ErrPartialResponse{Endpoint: "/issues", Expected: 10, Got: 5}}, ctx: diagnostics.CommandContext{ProviderMode: "live-http", HTTPAttempted: true, FailureSource: "partial_response", SchemaDecodeFailure: true}, want: diagnostics.CodeSchemaDecode},
		{name: "SCN-DIAG-PRODUCT-WRAP-08 sync rate limited", err: ErrSyncFailure{Mode: "rate_limited", Target: "issue:*", Endpoint: "/issues", RetryAfter: time.Second, Cause: gitcode.ErrRateLimited{Endpoint: "/issues", RetryAfter: time.Second, Attempts: 1}}, ctx: diagnostics.CommandContext{ProviderMode: "live-http", HTTPStatus: http.StatusTooManyRequests, HTTPAttempted: true}, want: diagnostics.CodeAPIFailure},
		{name: "SCN-DIAG-PRODUCT-WRAP-09 write local payload too large", err: ErrWriteFailure{Code: "write_provider_error", RepoID: "fixture-a", PayloadSource: "local_body_limit", Cause: gitcode.ErrPayloadTooLarge{Endpoint: "/issues", Limit: 10, Size: 20, Source: "local_body_limit"}}, ctx: diagnostics.CommandContext{ProviderMode: "live-http", HTTPAttempted: true, FailureSource: "local_body_limit", LocalPayloadTooLarge: true}, want: diagnostics.CodeSchemaDecode},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := diagnostics.Classify(tt.err, tt.ctx)
			if got.Code != tt.want {
				t.Fatalf("got %s want %s", got.Code, tt.want)
			}
			if badCodes[got.Code] && (tt.want == diagnostics.CodeAPIFailure || tt.want == diagnostics.CodeSchemaDecode) {
				t.Fatalf("decommissioned visible class returned: %s", got.Code)
			}
		})
	}
}

func TestFailureModes(t *testing.T) {
	ctx := context.Background()
	baseWiki := gitcode.WikiPage{Slug: "wiki/design", Title: "Design", Body: "new body", Revision: "rev-2", UpdatedAt: time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)}
	tests := []struct {
		name              string
		client            *fakeGitCodeClient
		request           SyncRequest
		prelock           bool
		corrupt           bool
		wantMode          string
		wantErrAs         func(error) bool
		wantMessage       string
		wantRemote        int
		wantPayloadSource string
		wantNotFound      bool
	}{
		{name: "failure-timeout-network-unavailable", client: &fakeGitCodeClient{errors: []error{gitcode.ErrNetworkUnavailable{Endpoint: "/wiki", Attempts: 1}}}, request: SyncRequest{RepoID: "fixture-a", StableID: "DOC-123", IdempotencyKey: "failure-timeout-network-unavailable"}, wantMode: "network_timeout", wantErrAs: func(err error) bool { var target gitcode.ErrNetworkUnavailable; return errors.As(err, &target) }, wantMessage: "sync: network timeout for record DOC-123: retry with --timeout to increase deadline or check connectivity", wantRemote: 1},
		{name: "failure-rate-limited-retry-after", client: &fakeGitCodeClient{errors: []error{gitcode.ErrRateLimited{RetryAfter: time.Second, Endpoint: "/wiki", Attempts: 1}}}, request: SyncRequest{RepoID: "fixture-a", StableID: "DOC-123", IdempotencyKey: "failure-rate-limited-retry-after", MaxAttempts: 1}, wantMode: "rate_limited", wantErrAs: func(err error) bool {
			var target gitcode.ErrRateLimited
			return errors.As(err, &target) && target.RetryAfter == time.Second
		}, wantMessage: "sync: rate limited. Retry after 1 seconds.", wantRemote: 1},
		{name: "failure-partial-response", client: &fakeGitCodeClient{errors: []error{gitcode.ErrPartialResponse{Endpoint: "/wiki", Expected: 100, Got: 40}}}, request: SyncRequest{RepoID: "fixture-a", StableID: "DOC-123", IdempotencyKey: "failure-partial-response"}, wantMode: "partial_response", wantErrAs: func(err error) bool { var target gitcode.ErrPartialResponse; return errors.As(err, &target) }, wantMessage: "sync: received a truncated response for /wiki after 1 attempt(s)", wantRemote: 1},
		{name: "failure-malformed-response", client: &fakeGitCodeClient{errors: []error{gitcode.ErrMalformedJSON{Endpoint: "/wiki", ResponseSize: 41, Offset: 17, Attempts: 3}}}, request: SyncRequest{RepoID: "fixture-a", StableID: "DOC-123", IdempotencyKey: "failure-malformed-response"}, wantMode: "malformed_response", wantErrAs: func(err error) bool { var target gitcode.ErrMalformedJSON; return errors.As(err, &target) }, wantMessage: "sync: provider returned malformed JSON for /wiki after 3 attempt(s)", wantRemote: 1},
		{name: "failure-unexpected-content-type", client: &fakeGitCodeClient{errors: []error{gitcode.ErrUnexpectedContentType{Endpoint: "/wiki", ContentType: "text/html", ResponseSize: 73, Attempts: 3}}}, request: SyncRequest{RepoID: "fixture-a", StableID: "DOC-123", IdempotencyKey: "failure-unexpected-content-type"}, wantMode: "unexpected_content_type", wantErrAs: func(err error) bool { var target gitcode.ErrUnexpectedContentType; return errors.As(err, &target) }, wantMessage: "sync: provider returned unexpected content type text/html for /wiki after 3 attempt(s)", wantRemote: 1},
		{name: "failure-schema-decode", client: &fakeGitCodeClient{errors: []error{&gitcode.ErrSchemaDecode{Endpoint: "/wiki", Field: "milestone.id", Expected: "number", Received: "object"}}}, request: SyncRequest{RepoID: "fixture-a", StableID: "DOC-123", IdempotencyKey: "failure-schema-decode"}, wantMode: "schema_decode", wantErrAs: func(err error) bool { var target *gitcode.ErrSchemaDecode; return errors.As(err, &target) }, wantMessage: "sync: provider response schema is incompatible for /wiki", wantRemote: 1},
		{name: "failure-auth-expired", client: &fakeGitCodeClient{errors: []error{gitcode.ErrAuthExpired{Endpoint: "/wiki", Status: 401}}}, request: SyncRequest{RepoID: "fixture-a", StableID: "DOC-123", IdempotencyKey: "failure-auth-expired"}, wantMode: "auth_expired", wantErrAs: func(err error) bool { var target gitcode.ErrAuthExpired; return errors.As(err, &target) }, wantMessage: "sync: authentication expired. Renew your GITCODE_TOKEN and try again.", wantRemote: 1},
		{name: "failure-remote-id-collision", client: &fakeGitCodeClient{wiki: baseWiki}, request: SyncRequest{RepoID: "fixture-a", StableID: "TASK-001", RemoteAlias: "remote:wiki/design", IdempotencyKey: "failure-remote-id-collision"}, wantMode: "remote_collision", wantErrAs: func(err error) bool {
			var target gitcode.ErrRemoteCollision
			return errors.As(err, &target) && target.ExistingID == "DOC-123" && target.NewID == "TASK-001"
		}, wantMessage: "sync: remote id remote:wiki/design already maps to local id DOC-123; cannot map to TASK-001. Run link-check for guidance.", wantRemote: 1},
		{name: "failure-cache-corruption", client: &fakeGitCodeClient{wiki: baseWiki}, request: SyncRequest{RepoID: "fixture-a", StableID: "DOC-123", IdempotencyKey: "failure-cache-corruption"}, corrupt: true, wantMode: "cache_corruption", wantErrAs: func(err error) bool { var target cache.ErrCacheCorruption; return errors.As(err, &target) }, wantMessage: "cache: integrity check failed at memory. Recover from backup or re-ingest with gitcode-mcp sync --full.", wantRemote: 0},
		{name: "failure-lock-contention", client: &fakeGitCodeClient{wiki: baseWiki}, request: SyncRequest{RepoID: "fixture-a", StableID: "DOC-123", IdempotencyKey: "failure-lock-contention"}, prelock: true, wantErrAs: func(err error) bool { var target cache.ErrLockContention; return errors.As(err, &target) }, wantRemote: 0},
		{name: "failure-missing-remote-record", client: &fakeGitCodeClient{errors: []error{gitcode.ErrRemoteNotFound{Endpoint: "/wiki", Alias: "remote:wiki/design"}}}, request: SyncRequest{RepoID: "fixture-a", StableID: "DOC-123", IdempotencyKey: "failure-missing-remote-record"}, wantMode: "remote_not_found", wantErrAs: func(err error) bool { var target gitcode.ErrRemoteNotFound; return errors.As(err, &target) }, wantMessage: "sync: remote record for alias remote:wiki/design not found. It may have been deleted or moved. Run link-check to find affected references.", wantRemote: 1, wantNotFound: true},
		{name: "failure-oversized-payload", client: &fakeGitCodeClient{errors: []error{gitcode.ErrPayloadTooLarge{Endpoint: "/wiki", Limit: 5, Size: 50, Source: "local_body_limit"}}}, request: SyncRequest{RepoID: "fixture-a", StableID: "DOC-123", IdempotencyKey: "failure-oversized-payload"}, wantMode: "payload_too_large", wantErrAs: func(err error) bool {
			var target gitcode.ErrPayloadTooLarge
			return errors.As(err, &target) && target.Limit == 5 && target.Source == "local_body_limit"
		}, wantMessage: "sync: record DOC-123 exceeds maximum size 5 bytes. Use --max-size to increase limit or skip with --skip-large.", wantRemote: 1, wantPayloadSource: "local_body_limit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := seededSyncService(t, ctx, tt.client)
			svc.lockPath = filepath.Join(t.TempDir(), "sync.lock")
			if tt.corrupt {
				svc.store = corruptingStore{Store: svc.store}
			}
			var lock *cache.LockHandle
			if tt.prelock {
				var err error
				lock, err = svc.store.AcquireLock(ctx, svc.lockPath)
				if err != nil {
					t.Fatal(err)
				}
				defer svc.store.ReleaseLock(ctx, lock)
			}
			_, err := svc.SyncToCache(ctx, tt.request)
			if err == nil {
				t.Fatal("SyncToCache succeeded, want failure")
			}
			if !tt.wantErrAs(err) {
				t.Fatalf("error=%T %[1]v did not match typed expectation", err)
			}
			if tt.wantMode != "" {
				var failure ErrSyncFailure
				if !errors.As(err, &failure) || failure.Mode != tt.wantMode || failure.RecoveryAction == "" {
					t.Fatalf("failure=%#v err=%v", failure, err)
				}
				if failure.PayloadSource != tt.wantPayloadSource {
					t.Fatalf("payload source=%q want %q", failure.PayloadSource, tt.wantPayloadSource)
				}
			}
			if tt.wantMessage != "" && err.Error() != tt.wantMessage {
				t.Fatalf("message=%q want %q", err.Error(), tt.wantMessage)
			}
			if tt.client.wikiCalls != tt.wantRemote {
				t.Fatalf("remote calls=%d want %d", tt.client.wikiCalls, tt.wantRemote)
			}
			source, err := svc.store.GetSource(ctx, "DOC-123")
			if err != nil {
				t.Fatal(err)
			}
			if source.Body != "intro same\nbacklog design same\nfinal" || source.ContentHash != index.ContentHash(source.Body) {
				t.Fatalf("source mutated after failure: %#v", source)
			}
			if tt.prelock {
				if event, err := svc.store.GetSyncEventByKey(ctx, tt.request.IdempotencyKey); err != nil || event != nil {
					t.Fatalf("lock contention event=%#v err=%v", event, err)
				}
				return
			}
			event, err := svc.store.GetSyncEventByKey(ctx, tt.request.IdempotencyKey)
			if err != nil {
				t.Fatal(err)
			}
			if event == nil || event.Status != "failed" || event.Message == "" {
				t.Fatalf("failed event=%#v", event)
			}
			status, err := svc.store.GetSyncStatus(ctx, "DOC-123")
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantNotFound {
				if status.Status != "not_found" {
					t.Fatalf("status=%#v want not_found", status)
				}
			} else if status.RemoteRevision != "rev-1" || status.Status != "fresh" {
				t.Fatalf("sync status mutated after failure: %#v", status)
			}
			conflicts, err := svc.store.GetConflicts(ctx, "DOC-123")
			if err != nil {
				t.Fatal(err)
			}
			if len(conflicts) != 0 {
				t.Fatalf("conflicts written after failure: %#v", conflicts)
			}
		})
	}
}

func TestResourceErrorIncludesSanitizedResponseDiagnostics(t *testing.T) {
	cause := gitcode.ErrMalformedJSON{Endpoint: "/api/v5/repos/example/repo/issues", ContentType: "application/json", ResponseSize: 2048, Offset: 1024, Attempts: 3}
	failure := ErrSyncFailure{
		Mode:           "malformed_response",
		Target:         "issue:*",
		Endpoint:       cause.Endpoint,
		ResponseBytes:  cause.ResponseSize,
		ContentType:    cause.ContentType,
		DecodeOffset:   cause.Offset,
		Attempts:       cause.Attempts,
		RecoveryAction: "inspect provider status or adapter contract",
		Cause:          cause,
	}
	resource := newResourceError("issue:*", "issues", failure)
	if resource.FailureClass != "malformed_response" || resource.Endpoint != cause.Endpoint || resource.ResponseBytes != 2048 || resource.ContentType != "application/json" || resource.DecodeOffset != 1024 || resource.Attempts != 3 {
		t.Fatalf("resource=%+v", resource)
	}
	encoded, err := json.Marshal(resource)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if strings.Contains(text, "Authorization") || strings.Contains(text, "Cookie") || strings.Contains(text, "response_body") {
		t.Fatalf("unsafe response diagnostics: %s", text)
	}
}

func TestRecentChanges(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	recent, err := svc.RecentChanges(ctx, RecentChangesRequest{RepoID: "fixture-a", Limit: 2})
	if err != nil {
		t.Fatalf("RecentChanges returned error: %v", err)
	}
	if len(recent.Results) != 2 || recent.Results[0].ID != "TASK-001" || recent.RepoID != "fixture-a" {
		t.Fatalf("RecentChanges = %#v", recent)
	}
}

func TestLinkCheck(t *testing.T) {
	ctx := context.Background()
	svc := New(fakeBrokenStore())
	result, err := svc.LinkCheck(ctx, LinkCheckRequest{RepoID: "fixture-a"})
	if err != nil {
		t.Fatalf("LinkCheck returned error: %v", err)
	}
	if result.BrokenCount != 1 || result.BrokenLinks[0].TargetID != "MISSING-1" {
		t.Fatalf("LinkCheck = %#v", result)
	}
	_, err = svc.LinkCheck(ctx, LinkCheckRequest{RepoID: "fixture-a", Strict: true})
	var failed ErrLinkCheckFailed
	if !errors.As(err, &failed) {
		t.Fatalf("LinkCheck strict error = %v, want ErrLinkCheckFailed", err)
	}
}

func TestStaleIndex(t *testing.T) {
	ctx := context.Background()
	svc := New(fakeBrokenStore())
	result, err := svc.StaleIndex(ctx, StaleIndexRequest{RepoID: "fixture-a"})
	if err != nil {
		t.Fatalf("StaleIndex returned error: %v", err)
	}
	if result.StaleCount != 1 || result.AffectedSourceIDs[0] != "DOC-1" || result.MissingTargetIDs[0] != "MISSING-1" || result.Warnings[0].Code != "missing_index" {
		t.Fatalf("StaleIndex = %#v", result)
	}
	_, err = svc.StaleIndex(ctx, StaleIndexRequest{RepoID: "fixture-a", Strict: true})
	var stale ErrStaleIndex
	if !errors.As(err, &stale) {
		t.Fatalf("StaleIndex strict error = %v, want ErrStaleIndex", err)
	}
}

func TestIndexFreshnessWarningsOnReadSurfaces(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	source := cache.Source{RepoID: "fixture-a", ID: "DOC-MISSING", Kind: "doc", Path: "docs/missing.md", Title: "Missing", Body: "body", Status: "ready", ContentHash: "hash-missing", CreatedAt: base, UpdatedAt: base}
	if err := store.UpsertSourceGraph(ctx, cache.SourceGraph{Source: source, SyncStatus: &cache.SyncStatus{RepoID: "fixture-a", SourceID: "DOC-MISSING", RemoteType: "wiki", RemoteID: "missing", RemoteRevision: "rev-1", Status: "fresh", LastFetchedAt: base}}); err != nil {
		t.Fatal(err)
	}
	svc := New(store)
	lineSnippet, err := svc.GetSnippet(ctx, SnippetRequest{RepoID: "fixture-a", ID: "DOC-MISSING", LineStart: 1, LineEnd: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(lineSnippet.Warnings) != 1 || lineSnippet.Warnings[0] != "missing_index" {
		t.Fatalf("GetSnippet line warnings = %+v", lineSnippet)
	}
	chunks, err := svc.GetChunkSnippet(ctx, SnippetQuery{RepoID: "fixture-a", SourceID: "DOC-MISSING"})
	if err != nil {
		t.Fatal(err)
	}
	if chunks.Total != 0 || len(chunks.Warnings) != 1 || chunks.Warnings[0].Code != "missing_index" {
		t.Fatalf("GetChunkSnippet warnings = %+v", chunks)
	}
	status, err := svc.CacheStatus(ctx, CacheStatusRequest{RepoID: "fixture-a"})
	if err != nil {
		t.Fatal(err)
	}
	if status.IndexFreshnessWarnings != 1 || status.IndexFreshnessByWarning["missing_index"] != 1 {
		t.Fatalf("CacheStatus = %+v", status)
	}
	export, err := svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-a", Format: "json"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(export.InlineContent, "missing_index") || len(export.Warnings) != 1 || export.Warnings[0] != "missing_index" {
		t.Fatalf("ExportSnapshot warnings missing: %+v", export)
	}
	filtered, err := svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-a", SourceIDs: []string{"DOC-MISSING"}, Format: "json"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(filtered.InlineContent, "missing_index") || len(filtered.Warnings) != 1 || filtered.Warnings[0] != "missing_index" {
		t.Fatalf("filtered ExportSnapshot warnings missing: %+v", filtered)
	}
}

func TestExportSnapshot(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	svc.now = func() time.Time { return time.Unix(100, 0).UTC() }
	first, err := svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-a", Format: "text"})
	if err != nil {
		t.Fatalf("ExportSnapshot returned error: %v", err)
	}
	second, err := svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-a", Format: "text"})
	if err != nil {
		t.Fatalf("ExportSnapshot second returned error: %v", err)
	}
	if first.ContentHash != second.ContentHash || first.InlineContent != second.InlineContent || first.RecordCount != 2 {
		t.Fatalf("ExportSnapshot not deterministic: %#v %#v", first, second)
	}
}

func TestExportDeterminism(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	svc.now = func() time.Time { return time.Unix(100, 0).UTC() }
	jsonFirst, err := svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-a", Format: "json", IncludeBody: true})
	if err != nil {
		t.Fatalf("ExportSnapshot json returned error: %v", err)
	}
	jsonSecond, err := svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-a", Format: "json", IncludeBody: true})
	if err != nil {
		t.Fatalf("ExportSnapshot json second returned error: %v", err)
	}
	markdownFirst, err := svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-a", Format: "markdown", IncludeBody: true})
	if err != nil {
		t.Fatalf("ExportSnapshot markdown returned error: %v", err)
	}
	markdownSecond, err := svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-a", Format: "markdown", IncludeBody: true})
	if err != nil {
		t.Fatalf("ExportSnapshot markdown second returned error: %v", err)
	}
	if jsonFirst.InlineContent != jsonSecond.InlineContent || markdownFirst.InlineContent != markdownSecond.InlineContent {
		t.Fatalf("exports are not byte-identical")
	}
}

func TestGoldenExport(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner-a", Name: "repo-a", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	bodyHash := index.ContentHash("body")
	if err := store.UpsertSourceGraph(ctx, cache.SourceGraph{
		Source:     cache.Source{RepoID: "fixture-a", ID: "DOC-123", Kind: "doc", Path: "docs/design.md", Title: "Design Doc", Body: "body", Status: "ready", Labels: []string{"zeta", "design"}, ContentHash: bodyHash, CreatedAt: base, UpdatedAt: base},
		Identities: []cache.Identity{{RepoID: "fixture-a", AliasType: "path", Alias: "docs/design.md", Remote: cache.RemoteAlias{Type: "remote", ID: "wiki/design"}}},
		Links:      []cache.Link{{RepoID: "fixture-a", TargetID: "DOC-123", Kind: "mentions", Text: "doc"}},
		Chunks:     []cache.Chunk{{RepoID: "fixture-a", ID: "chunk-doc", ContentHash: bodyHash, ByteStart: 0, ByteEnd: 4, LineStart: 1, LineEnd: 1, HeadingPath: []string{"Design"}, Text: "body", NormalizedText: "body"}},
		SyncStatus: &cache.SyncStatus{RepoID: "fixture-a", RemoteType: "remote", RemoteID: "wiki/design", RemoteRevision: "rev-1", Status: "fresh", LastFetchedAt: base},
	}); err != nil {
		t.Fatal(err)
	}
	svc := New(store)
	svc.now = func() time.Time { return base }
	result, err := svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-a", Format: "markdown", IncludeBody: true})
	if err != nil {
		t.Fatalf("ExportSnapshot markdown returned error: %v", err)
	}
	want, err := os.ReadFile("testdata/golden_export.md")
	if err != nil {
		t.Fatal(err)
	}
	if result.InlineContent != string(want) {
		t.Fatalf("golden export mismatch\n got: %q\nwant: %q", result.InlineContent, string(want))
	}
}

func TestExportIncludesChunkProvenance(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	result, err := svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-a", Format: "json", IncludeBody: true})
	if err != nil {
		t.Fatalf("ExportSnapshot returned error: %v", err)
	}
	var snapshot Snapshot
	if err := json.Unmarshal([]byte(result.InlineContent), &snapshot); err != nil {
		t.Fatalf("snapshot json invalid: %v", err)
	}
	if len(snapshot.Chunks) == 0 {
		t.Fatalf("snapshot has no chunks: %#v", snapshot)
	}
	chunk := snapshot.Chunks[0]
	if chunk.ID == "" || chunk.SourceID == "" || chunk.ContentHash == "" || chunk.ByteStart < 0 || chunk.ByteEnd == 0 || chunk.LineStart == 0 || chunk.LineEnd == 0 || chunk.Text == "" {
		t.Fatalf("chunk missing provenance: %#v", chunk)
	}
	if chunk.InheritedMetadata["owner"] != "docs" || len(chunk.OutboundLinks) == 0 || chunk.ResolvedAliases["TASK-001"] != "task:1" {
		t.Fatalf("chunk missing nested provenance: %#v", chunk)
	}
}

func TestDiffSnapshot(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	base, err := svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-a", Format: "json", IncludeBody: true})
	if err != nil {
		t.Fatalf("base export returned error: %v", err)
	}
	if err := svc.store.UpsertSource(ctx, cache.Source{RepoID: "fixture-a", ID: "TASK-001", Kind: "task", Path: "project/tasks/task.md", Title: "Task Backlog Changed", Body: "task changed", Status: "ready", Labels: []string{"task"}, ContentHash: "hash-task-2", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 1, 1, 2, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	if err := svc.store.UpsertSourceGraph(ctx, cache.SourceGraph{Source: cache.Source{RepoID: "fixture-a", ID: "DOC-999", Kind: "doc", Path: "docs/new.md", Title: "New", Body: "new", Status: "ready", ContentHash: "hash-new"}, Chunks: []cache.Chunk{{RepoID: "fixture-a", ID: "chunk-new", SourceID: "DOC-999", ContentHash: "hash-new", ByteStart: 0, ByteEnd: 3, LineStart: 1, LineEnd: 1, Text: "new"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.store.UpsertChunk(ctx, cache.Chunk{RepoID: "fixture-a", ID: "chunk-doc", SourceID: "DOC-123", ContentHash: "hash-doc", ByteStart: 0, ByteEnd: 14, LineStart: 2, LineEnd: 2, HeadingPath: []string{"Design"}, Text: "backlog chunk!", NormalizedText: "backlog chunk", InheritedMetadata: map[string]string{"owner": "docs"}, OutboundLinks: []string{"TASK-001"}, ResolvedAliases: map[string]string{"TASK-001": "task:1"}}); err != nil {
		t.Fatal(err)
	}
	diff, err := svc.DiffSnapshot(ctx, DiffSnapshotRequest{RepoID: "fixture-a", Base: SnapshotRef{Kind: "bytes", Bytes: []byte(base.InlineContent), Format: "json"}, Head: SnapshotRef{Kind: "current", Format: "json"}, Format: "json"})
	if err != nil {
		t.Fatalf("DiffSnapshot returned error: %v", err)
	}
	if len(diff.AddedSources) == 0 || len(diff.ChangedSources) == 0 || len(diff.AddedChunks) == 0 || len(diff.ChangedChunks) == 0 || !strings.Contains(strings.Join(diff.ChangedSources[0].ChangedFields, ","), "content_hash") {
		t.Fatalf("DiffSnapshot = %#v", diff)
	}
}

func TestStoredSnapshotDiffNotFoundNoFallback(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	head, err := svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-a", Format: "json", IncludeBody: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.DiffSnapshot(ctx, DiffSnapshotRequest{RepoID: "fixture-a", BaseSnapshotID: "missing", HeadSnapshotID: head.SnapshotID, Format: "json"})
	var notFound ErrNotFound
	if !errors.As(err, &notFound) || notFound.Kind != "base_id" {
		t.Fatalf("diff error = %#v, want base_id not found", err)
	}
}

func TestStoredSnapshotDiffUsesStoredRowsOnly(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	base, err := svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-a", Format: "json", IncludeBody: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.store.UpsertSource(ctx, cache.Source{RepoID: "fixture-a", ID: "DOC-123", Kind: "doc", Path: "docs/design.md", Title: "Changed", Body: "changed", Status: "ready", ContentHash: "hash-doc-current", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	head, err := svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-a", Format: "json", IncludeBody: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.store.UpsertSource(ctx, cache.Source{RepoID: "fixture-a", ID: "DOC-123", Kind: "doc", Path: "docs/design.md", Title: "Current Mutation", Body: "current", Status: "ready", ContentHash: "hash-doc-later", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 1, 1, 4, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	diff, err := svc.DiffSnapshot(ctx, DiffSnapshotRequest{RepoID: "fixture-a", BaseSnapshotID: base.SnapshotID, HeadSnapshotID: head.SnapshotID, Format: "json"})
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.ChangedSources) == 0 || diff.ChangedSources[0].AfterContentHash != "hash-doc-current" {
		t.Fatalf("diff used current cache or missed stored change: %#v", diff)
	}
}

func TestMCPToolDTOContract(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	if _, err := svc.SearchSources(ctx, SearchSourcesRequest{RepoID: "fixture-a", Query: "backlog"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetSource(ctx, GetSourceRequest{RepoID: "fixture-a", ID: "DOC-123"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ListSources(ctx, ListSourcesRequest{RepoID: "fixture-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetBacklinks(ctx, GetBacklinksRequest{RepoID: "fixture-a", ID: "DOC-123"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResolveID(ctx, ResolveIDRequest{RepoID: "fixture-a", ID: "DOC-123"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetSyncStatus(ctx, SyncStatusRequest{RepoID: "fixture-a", ID: "DOC-123"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DiffSnapshot(ctx, DiffSnapshotRequest{RepoID: "fixture-a"}); err != nil {
		t.Fatal(err)
	}
}

func TestQueryEdgeCases(t *testing.T) {
	ctx := context.Background()
	empty, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer empty.Close()
	if err := empty.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner-a", Name: "repo-a", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	_, err = New(empty).ListSources(ctx, ListSourcesRequest{RepoID: "fixture-a"})
	var cacheEmpty ErrCacheEmpty
	if !errors.As(err, &cacheEmpty) {
		t.Fatalf("empty cache error = %v, want ErrCacheEmpty", err)
	}
	svc := seededService(t, ctx)
	_, err = svc.GetSource(ctx, GetSourceRequest{RepoID: "fixture-a", ID: "NOPE"})
	var notFound ErrNotFound
	if !errors.As(err, &notFound) {
		t.Fatalf("not found error = %v, want ErrNotFound", err)
	}
	search, err := New(empty).SearchSources(ctx, SearchSourcesRequest{RepoID: "fixture-a", Query: "backlog"})
	if err != nil {
		t.Fatalf("empty cache search error = %v, want nil", err)
	}
	if len(search.Results) != 0 {
		t.Fatalf("empty cache search results = %d, want 0", len(search.Results))
	}
	_, err = svc.SearchSources(ctx, SearchSourcesRequest{RepoID: "fixture-a"})
	var invalid ErrInvalidQuery
	if !errors.As(err, &invalid) {
		t.Fatalf("invalid query error = %v, want ErrInvalidQuery", err)
	}
	results, err := svc.SearchSources(ctx, SearchSourcesRequest{RepoID: "fixture-a", Query: "same"})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{results.Results[0].ID, results.Results[1].ID}
	if !reflect.DeepEqual(ids, []string{"DOC-123", "TASK-001"}) {
		t.Fatalf("equal score ordering ids = %v", ids)
	}
}

func TestQueryMethodsDoNotUseLiveNetwork(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	calls := []func() error{
		func() error {
			_, err := svc.SearchSources(ctx, SearchSourcesRequest{RepoID: "fixture-a", Query: "backlog"})
			return err
		},
		func() error {
			_, err := svc.GetSource(ctx, GetSourceRequest{RepoID: "fixture-a", ID: "DOC-123"})
			return err
		},
		func() error { _, err := svc.ListSources(ctx, ListSourcesRequest{RepoID: "fixture-a"}); return err },
		func() error {
			_, err := svc.GetBacklinks(ctx, GetBacklinksRequest{RepoID: "fixture-a", ID: "DOC-123"})
			return err
		},
		func() error {
			_, err := svc.ResolveID(ctx, ResolveIDRequest{RepoID: "fixture-a", ID: "DOC-123"})
			return err
		},
		func() error {
			_, err := svc.GetSnippet(ctx, SnippetRequest{RepoID: "fixture-a", ID: "DOC-123", LineStart: 1, LineEnd: 1})
			return err
		},
		func() error {
			_, err := svc.GetSyncStatus(ctx, SyncStatusRequest{RepoID: "fixture-a", ID: "DOC-123"})
			return err
		},
		func() error { _, err := svc.RecentChanges(ctx, RecentChangesRequest{RepoID: "fixture-a"}); return err },
		func() error { _, err := svc.LinkCheck(ctx, LinkCheckRequest{RepoID: "fixture-a"}); return err },
		func() error { _, err := svc.StaleIndex(ctx, StaleIndexRequest{RepoID: "fixture-a"}); return err },
		func() error {
			_, err := svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-a"})
			return err
		},
		func() error { _, err := svc.DiffSnapshot(ctx, DiffSnapshotRequest{RepoID: "fixture-a"}); return err },
	}
	for i, call := range calls {
		if err := call(); err != nil {
			t.Fatalf("query call %d returned error: %v", i, err)
		}
	}
}

func seededService(t *testing.T, ctx context.Context) *Service {
	t.Helper()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	seedStore(t, ctx, store)
	return New(store)
}

func seedStore(t *testing.T, ctx context.Context, store cache.Store) {
	t.Helper()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner-a", Name: "repo-a", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	docBody := "intro same\nbacklog design same\nfinal"
	docHash := index.ContentHash(docBody)
	taskBody := "task same\nbacklog item same"
	taskHash := index.ContentHash(taskBody)
	err := store.UpsertSource(ctx, cache.Source{RepoID: "fixture-a", ID: "DOC-123", Kind: "doc", Path: "docs/design.md", Title: "Design Doc", Body: docBody, Status: "ready", Labels: []string{"zeta", "design"}, ContentHash: docHash, CreatedAt: base, UpdatedAt: base})
	if err != nil {
		t.Fatal(err)
	}
	err = store.UpsertSource(ctx, cache.Source{RepoID: "fixture-a", ID: "TASK-001", Kind: "task", Path: "project/tasks/task.md", Title: "Task Backlog", Body: taskBody, Status: "ready", Labels: []string{"task"}, ContentHash: taskHash, CreatedAt: base, UpdatedAt: base.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	err = store.UpsertSourceGraph(ctx, cache.SourceGraph{
		Source:     cache.Source{RepoID: "fixture-a", ID: "DOC-123", Kind: "doc", Path: "docs/design.md", Title: "Design Doc", Body: docBody, Status: "ready", Labels: []string{"zeta", "design"}, ContentHash: docHash, CreatedAt: base, UpdatedAt: base},
		Identities: []cache.Identity{{RepoID: "fixture-a", AliasType: "path", Alias: "docs/design.md", Remote: cache.RemoteAlias{Type: "remote", ID: "wiki/design"}}},
		Links:      []cache.Link{{RepoID: "fixture-a", TargetID: "TASK-001", Kind: "mentions", Text: "task"}},
		Chunks:     []cache.Chunk{{RepoID: "fixture-a", ID: "chunk-doc", ContentHash: docHash, ByteStart: 0, ByteEnd: 13, LineStart: 2, LineEnd: 2, HeadingPath: []string{"Design"}, Text: "backlog chunk", NormalizedText: "backlog chunk", InheritedMetadata: map[string]string{"owner": "docs"}, OutboundLinks: []string{"TASK-001"}, ResolvedAliases: map[string]string{"TASK-001": "task:1"}}},
		SyncStatus: &cache.SyncStatus{RepoID: "fixture-a", RemoteType: "remote", RemoteID: "wiki/design", RemoteRevision: "rev-1", Status: "fresh", LastFetchedAt: base},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = store.UpsertLink(ctx, cache.Link{RepoID: "fixture-a", SourceID: "TASK-001", TargetID: "DOC-123", Kind: "mentions", Text: "doc"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestTargetedIssueSyncRejectsMismatchedProviderIdentityBeforeCacheMutation(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "identity-check", Owner: "owner-a", Name: "repo-a", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	client := &fakeGitCodeClient{issue: gitcode.Issue{ID: "provider-71", Number: 71, Title: "Unrelated issue", State: "open"}}
	svc := NewWithClient(store, client)
	svc.lockPath = filepath.Join(t.TempDir(), "sync.lock")

	_, err = svc.SyncToCache(ctx, SyncRequest{RepoID: "identity-check", RemoteAlias: "issue:42", IdempotencyKey: "identity-mismatch"})
	var syncErr ErrSyncFailure
	if !errors.As(err, &syncErr) || syncErr.Mode != "remote_identity_mismatch" || syncErr.ExpectedID != "42" || syncErr.ActualID != "71" {
		t.Fatalf("sync error=%#v, want remote_identity_mismatch 42 != 71", err)
	}
	if client.issueCalls != 1 || client.commentCalls != 0 || len(client.listIssueRequests) != 0 {
		t.Fatalf("provider calls: get=%d comments=%d list=%d", client.issueCalls, client.commentCalls, len(client.listIssueRequests))
	}
	if _, err := store.GetSourceScoped(ctx, "identity-check", "ISSUE-42"); err == nil {
		t.Fatal("requested issue was written despite identity mismatch")
	}
	if _, err := store.GetSourceScoped(ctx, "identity-check", "ISSUE-71"); err == nil {
		t.Fatal("unrelated issue was written despite identity mismatch")
	}
}

func TestSyncResourcesAllSuccess(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		issue:    gitcode.Issue{Number: 42, Title: "Test Issue", Body: "issue body", State: "open", CreatedAt: base, UpdatedAt: base},
		comments: []gitcode.Comment{{ID: "c1", Author: "author", Body: "comment", CreatedAt: base, UpdatedAt: base}},
		wiki:     gitcode.WikiPage{Slug: "Home", Title: "Home", Body: "wiki body", Revision: "rev-1", CreatedAt: base, UpdatedAt: base},
	}
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "sync-resources-a", Owner: "owner-a", Name: "repo-a", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)
	reqs := []SyncRequest{
		{RepoID: "sync-resources-a", RemoteAlias: "issue:42", IdempotencyKey: "all-success-issue"},
		{RepoID: "sync-resources-a", RemoteAlias: "wiki:Home", IdempotencyKey: "all-success-wiki"},
	}
	result, err := svc.SyncResources(ctx, reqs)
	if err != nil {
		t.Fatalf("SyncResources returned unexpected error: %v", err)
	}
	if result.SuccessCount != 2 {
		t.Fatalf("SuccessCount = %d, want 2", result.SuccessCount)
	}
	if result.FailureCount != 0 {
		t.Fatalf("FailureCount = %d, want 0", result.FailureCount)
	}
	if len(result.Failures) != 0 {
		t.Fatalf("Failures length = %d, want 0", len(result.Failures))
	}
	if len(result.Results) != 2 {
		t.Fatalf("Results length = %d, want 2", len(result.Results))
	}
	if result.Results[0].Counts.Fetched <= 0 {
		t.Fatalf("Results[0].Counts.Fetched = %d, want > 0", result.Results[0].Counts.Fetched)
	}
	if result.Results[1].Counts.Fetched <= 0 {
		t.Fatalf("Results[1].Counts.Fetched = %d, want > 0", result.Results[1].Counts.Fetched)
	}
	if _, err := store.GetSourceScoped(ctx, "sync-resources-a", "ISSUE-42"); err != nil {
		t.Fatalf("issue source not committed to cache: %v", err)
	}
	if _, err := store.GetSourceScoped(ctx, "sync-resources-a", "WIKI-HOME"); err != nil {
		t.Fatalf("wiki source not committed to cache: %v", err)
	}
}

func TestSyncResourcesPartialFailure(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		issue:    gitcode.Issue{Number: 42, Title: "Test Issue", Body: "issue body", State: "open", CreatedAt: base, UpdatedAt: base},
		comments: []gitcode.Comment{{ID: "c1", Author: "author", Body: "comment", CreatedAt: base, UpdatedAt: base}},
		errors:   []error{nil, gitcode.ErrNotFound{Endpoint: "/wiki", ID: "Home", Message: "not found"}},
	}
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "sync-resources-b", Owner: "owner-b", Name: "repo-b", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)
	reqs := []SyncRequest{
		{RepoID: "sync-resources-b", RemoteAlias: "issue:42", IdempotencyKey: "partial-issue"},
		{RepoID: "sync-resources-b", RemoteAlias: "wiki:Home", IdempotencyKey: "partial-wiki"},
	}
	result, err := svc.SyncResources(ctx, reqs)
	if err == nil {
		t.Fatal("SyncResources expected PartialSyncError, got nil")
	}
	var partial *PartialSyncError
	if !errors.As(err, &partial) {
		t.Fatalf("SyncResources error is not *PartialSyncError: %T %v", err, err)
	}
	if result.SuccessCount != 1 {
		t.Fatalf("SuccessCount = %d, want 1", result.SuccessCount)
	}
	if result.FailureCount != 1 {
		t.Fatalf("FailureCount = %d, want 1", result.FailureCount)
	}
	if len(result.Failures) != 1 {
		t.Fatalf("Failures length = %d, want 1", len(result.Failures))
	}
	if len(result.Results) != 1 {
		t.Fatalf("Results length = %d, want 1", len(result.Results))
	}
	if result.Results[0].Counts.Fetched <= 0 {
		t.Fatalf("Results[0].Counts.Fetched = %d, want > 0", result.Results[0].Counts.Fetched)
	}
	if result.Failures[0].Err == nil {
		t.Fatal("Failures[0].Err is nil")
	}
	if !strings.Contains(partial.Error(), "1 succeeded") || !strings.Contains(partial.Error(), "1 failed") {
		t.Fatalf("PartialSyncError.Error() = %q, want contains success/failure counts", partial.Error())
	}
	if _, err := store.GetSourceScoped(ctx, "sync-resources-b", "ISSUE-42"); err != nil {
		t.Fatalf("issue source not committed to cache: %v", err)
	}
}

func TestSyncResourcesAllFailure(t *testing.T) {
	ctx := context.Background()
	client := &fakeGitCodeClient{
		errors: []error{
			gitcode.ErrNotFound{Endpoint: "/issue", ID: "42", Message: "not found"},
			gitcode.ErrNotFound{Endpoint: "/wiki", ID: "Home", Message: "not found"},
		},
	}
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "sync-resources-c", Owner: "owner-c", Name: "repo-c", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)
	reqs := []SyncRequest{
		{RepoID: "sync-resources-c", RemoteAlias: "issue:42", IdempotencyKey: "all-fail-issue"},
		{RepoID: "sync-resources-c", RemoteAlias: "wiki:Home", IdempotencyKey: "all-fail-wiki"},
	}
	result, err := svc.SyncResources(ctx, reqs)
	if err == nil {
		t.Fatal("SyncResources expected PartialSyncError, got nil")
	}
	var partial *PartialSyncError
	if !errors.As(err, &partial) {
		t.Fatalf("SyncResources error is not *PartialSyncError: %T %v", err, err)
	}
	if result.SuccessCount != 0 {
		t.Fatalf("SuccessCount = %d, want 0", result.SuccessCount)
	}
	if result.FailureCount != 2 {
		t.Fatalf("FailureCount = %d, want 2", result.FailureCount)
	}
	if len(result.Failures) != 2 {
		t.Fatalf("Failures length = %d, want 2", len(result.Failures))
	}
	if len(result.Results) != 0 {
		t.Fatalf("Results length = %d, want 0", len(result.Results))
	}
}

func TestZeroDeltaPersistentEvent(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 6, 22, 13, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		issue:    gitcode.Issue{Number: 42, Title: "Zero Delta Issue", Body: "unchanged body", State: "open", CreatedAt: base, UpdatedAt: base},
		comments: []gitcode.Comment{{ID: "c1", Author: "author", Body: "comment", CreatedAt: base, UpdatedAt: base}},
	}
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "zero-delta", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)
	first, err := svc.SyncToCache(ctx, SyncRequest{RepoID: "zero-delta", RemoteAlias: "issue:42", IdempotencyKey: "zero-delta-first"})
	if err != nil {
		t.Fatalf("first SyncToCache returned error: %v", err)
	}
	if first.ZeroDelta {
		t.Fatal("first SyncToCache ZeroDelta = true, want false")
	}
	second, err := svc.SyncToCache(ctx, SyncRequest{RepoID: "zero-delta", RemoteAlias: "issue:42", IdempotencyKey: "zero-delta-second"})
	if err != nil {
		t.Fatalf("second SyncToCache returned error: %v", err)
	}
	if !second.ZeroDelta {
		t.Fatal("second SyncToCache ZeroDelta = false, want true")
	}
	if second.Counts.Fetched == 0 || second.Counts.Skipped != second.Counts.Fetched {
		t.Fatalf("second counts = %#v, want fetched > 0 and skipped == fetched", second.Counts)
	}
	if second.Counts.Updated != 0 || second.Counts.Inserted != 0 || second.Counts.Conflicts != 0 {
		t.Fatalf("second counts = %#v, want no mutations", second.Counts)
	}
	stored, err := store.GetSyncEventByKey(ctx, "zero-delta-second")
	if err != nil {
		t.Fatalf("GetSyncEventByKey returned error: %v", err)
	}
	if stored == nil || !stored.ZeroDelta {
		t.Fatalf("stored zero-delta event = %#v, want ZeroDelta true", stored)
	}
	var storedCounts SyncCounts
	if err := json.Unmarshal([]byte(stored.Message), &storedCounts); err != nil {
		t.Fatalf("stored sync event counts JSON invalid: %v", err)
	}
	if storedCounts.Fetched != second.Counts.Fetched || storedCounts.Skipped != second.Counts.Skipped {
		t.Fatalf("stored counts = %#v, want %#v", storedCounts, second.Counts)
	}
	events, err := store.ListCompletedSyncEventsScoped(ctx, "zero-delta")
	if err != nil {
		t.Fatalf("ListCompletedSyncEventsScoped returned error: %v", err)
	}
	completedForSource := 0
	for _, event := range events {
		if event.SourceID == "ISSUE-42" && event.Status == "succeeded" {
			completedForSource++
		}
	}
	if completedForSource != 2 {
		t.Fatalf("completed sync events for ISSUE-42 = %d, want 2", completedForSource)
	}
	replayed, err := svc.SyncToCache(ctx, SyncRequest{RepoID: "zero-delta", RemoteAlias: "issue:42", IdempotencyKey: "zero-delta-second"})
	if err != nil {
		t.Fatalf("replayed SyncToCache returned error: %v", err)
	}
	if !replayed.Replayed || !replayed.ZeroDelta {
		t.Fatalf("replayed result = %#v, want replayed zero-delta", replayed)
	}
	summary, err := svc.SyncStatus(ctx, ListSourcesRequest{RepoID: "zero-delta"})
	if err != nil {
		t.Fatalf("SyncStatus returned error: %v", err)
	}
	if !summary.ZeroDelta {
		t.Fatalf("SyncStatus ZeroDelta = false, want true: %#v", summary)
	}
}

func TestZeroDeltaFalseWhenContentChanges(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 6, 22, 14, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{issue: gitcode.Issue{Number: 42, Title: "Changing Issue", Body: "first body", State: "open", CreatedAt: base, UpdatedAt: base}}
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "zero-delta-change", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)
	if _, err := svc.SyncToCache(ctx, SyncRequest{RepoID: "zero-delta-change", RemoteAlias: "issue:42", IdempotencyKey: "zero-delta-change-first"}); err != nil {
		t.Fatalf("first SyncToCache returned error: %v", err)
	}
	client.issue.Body = "second body"
	client.issue.UpdatedAt = base.Add(time.Minute)
	result, err := svc.SyncToCache(ctx, SyncRequest{RepoID: "zero-delta-change", RemoteAlias: "issue:42", IdempotencyKey: "zero-delta-change-second"})
	if err != nil {
		t.Fatalf("second SyncToCache returned error: %v", err)
	}
	if result.ZeroDelta {
		t.Fatalf("ZeroDelta = true, want false for changed content: %#v", result)
	}
	if result.Counts.Updated != 1 {
		t.Fatalf("counts = %#v, want Updated 1", result.Counts)
	}
}

func TestSyncEventTimestamps(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		issue:    gitcode.Issue{Number: 42, Title: "Timestamp Issue", Body: "issue body", State: "open", CreatedAt: base, UpdatedAt: base},
		comments: []gitcode.Comment{{ID: "c1", Author: "author", Body: "comment", CreatedAt: base, UpdatedAt: base}},
	}
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "sync-timestamps", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)
	times := []time.Time{base, base.Add(time.Second), base.Add(2 * time.Second)}
	next := 0
	svc.now = func() time.Time {
		if next >= len(times) {
			return times[len(times)-1]
		}
		t := times[next]
		next++
		return t
	}
	result, err := svc.SyncToCache(ctx, SyncRequest{RepoID: "sync-timestamps", RemoteAlias: "issue:42", IdempotencyKey: "sync-timestamps-key"})
	if err != nil {
		t.Fatalf("SyncToCache returned error: %v", err)
	}
	if result.StartedAt.IsZero() {
		t.Fatal("SyncResult.StartedAt is zero")
	}
	if result.CompletedAt.IsZero() {
		t.Fatal("SyncResult.CompletedAt is zero")
	}
	if !result.CompletedAt.After(result.StartedAt) {
		t.Fatalf("CompletedAt = %s, want after StartedAt = %s", result.CompletedAt, result.StartedAt)
	}
	stored, err := store.GetSyncEventByKey(ctx, "sync-timestamps-key")
	if err != nil {
		t.Fatalf("GetSyncEventByKey returned error: %v", err)
	}
	if stored == nil || stored.StartedAt.IsZero() || stored.CompletedAt.IsZero() {
		t.Fatalf("stored sync event timestamps not populated: %#v", stored)
	}
}

func TestSyncEventTimestampsFailure(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 6, 22, 11, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{errors: []error{gitcode.ErrNotFound{Endpoint: "/issues/42", ID: "42", Message: "not found"}}}
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "sync-timestamps-failure", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSource(ctx, cache.Source{RepoID: "sync-timestamps-failure", ID: "ISSUE-42", Kind: "issue", Path: "issues/42.md", Title: "Issue", Body: "body", Status: "open", ContentHash: "old", CreatedAt: base, UpdatedAt: base}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)
	times := []time.Time{base, base.Add(time.Second)}
	next := 0
	svc.now = func() time.Time {
		if next >= len(times) {
			return times[len(times)-1]
		}
		t := times[next]
		next++
		return t
	}
	_, err = svc.SyncToCache(ctx, SyncRequest{RepoID: "sync-timestamps-failure", RemoteAlias: "issue:42", IdempotencyKey: "sync-timestamps-failure-key"})
	if err == nil {
		t.Fatal("SyncToCache returned nil error, want failure")
	}
	stored, err := store.GetSyncEventByKey(ctx, "sync-timestamps-failure-key")
	if err != nil {
		t.Fatalf("GetSyncEventByKey returned error: %v", err)
	}
	if stored == nil {
		t.Fatal("failed sync event was not persisted")
	}
	if stored.Status != "failed" {
		t.Fatalf("Status = %q, want failed", stored.Status)
	}
	if !stored.StartedAt.Equal(base) {
		t.Fatalf("StartedAt = %s, want %s", stored.StartedAt, base)
	}
	if !stored.CompletedAt.Equal(base.Add(time.Second)) {
		t.Fatalf("CompletedAt = %s, want %s", stored.CompletedAt, base.Add(time.Second))
	}
}

func TestSyncStatusSummaryCompletedAt(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "sync-status-completed", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	graph := cache.SourceGraph{
		Source:     cache.Source{RepoID: "sync-status-completed", ID: "ISSUE-42", Kind: "issue", Path: "issues/42.md", Title: "Issue", Body: "body", Status: "open", ContentHash: "hash", CreatedAt: base, UpdatedAt: base},
		SyncStatus: &cache.SyncStatus{RepoID: "sync-status-completed", SourceID: "ISSUE-42", RemoteType: "issue", RemoteID: "42", RemoteRevision: "rev", Status: "fresh", LastFetchedAt: base},
	}
	if err := store.UpsertSourceGraph(ctx, graph); err != nil {
		t.Fatal(err)
	}
	incompleteStarted := base.Add(time.Hour)
	if err := store.RecordSyncEvent(ctx, cache.SyncEvent{RepoID: "sync-status-completed", ID: "incomplete", SourceID: "ISSUE-42", RemoteType: "issue", RemoteID: "42", Status: "in_progress", IdempotencyKey: "incomplete", Message: "sync started", CreatedAt: incompleteStarted, StartedAt: incompleteStarted}); err != nil {
		t.Fatal(err)
	}
	completedStarted := base.Add(10 * time.Minute)
	completedAt := base.Add(11 * time.Minute)
	if err := store.RecordSyncEvent(ctx, cache.SyncEvent{RepoID: "sync-status-completed", ID: "completed", SourceID: "ISSUE-42", RemoteType: "issue", RemoteID: "42", Status: "succeeded", IdempotencyKey: "completed", Message: "{}", CreatedAt: completedAt, StartedAt: completedStarted, CompletedAt: completedAt}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, &fakeGitCodeClient{})
	result, err := svc.SyncStatus(ctx, ListSourcesRequest{RepoID: "sync-status-completed"})
	if err != nil {
		t.Fatalf("SyncStatus returned error: %v", err)
	}
	if !result.LastSyncStartedAt.Equal(completedStarted) {
		t.Fatalf("LastSyncStartedAt = %s, want %s", result.LastSyncStartedAt, completedStarted)
	}
	if !result.LastSyncCompletedAt.Equal(completedAt) {
		t.Fatalf("LastSyncCompletedAt = %s, want %s", result.LastSyncCompletedAt, completedAt)
	}
	if result.LastSyncCompletedAt.Equal(incompleteStarted) {
		t.Fatal("incomplete sync event was selected as completed")
	}
}

func seededSyncService(t *testing.T, ctx context.Context, client gitcode.Client) *Service {
	t.Helper()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	seedStore(t, ctx, store)
	return NewWithClient(store, client)
}

type fakeGitCodeClient struct {
	wiki                         gitcode.WikiPage
	issue                        gitcode.Issue
	comments                     []gitcode.Comment
	errors                       []error
	wikiCalls                    int
	issueCalls                   int
	commentCalls                 int
	createIssueCalls             int
	createIssueCommentCalls      int
	updateIssueCommentCalls      int
	createPRCalls                int
	updatePRCalls                int
	mergePRCalls                 int
	linkPRIssueCalls             int
	createPRCommentCalls         int
	createPRReviewCommentCalls   int
	replyPRReviewCommentCalls    int
	createWikiPageCalls          int
	addLabelCalls                int
	getReleaseCalls              int
	createReleaseCalls           int
	updateReleaseCalls           int
	createIssueResult            gitcode.WriteResult[gitcode.Issue]
	createIssueResults           []gitcode.WriteResult[gitcode.Issue]
	updateIssueCalls             int
	updateIssueResult            gitcode.WriteResult[gitcode.Issue]
	createIssueCommentResult     gitcode.WriteResult[gitcode.Comment]
	updateIssueCommentResult     gitcode.WriteResult[gitcode.Comment]
	createPRResult               gitcode.WriteResult[gitcode.PullRequest]
	updatePRResult               gitcode.WriteResult[gitcode.PullRequest]
	mergePRResult                gitcode.WriteResult[gitcode.PullRequest]
	linkPRIssueResult            gitcode.WriteResult[[]gitcode.Issue]
	listMilestonesResult         gitcode.Page[gitcode.Milestone]
	createMilestoneResult        gitcode.WriteResult[gitcode.Milestone]
	updateMilestoneResult        gitcode.WriteResult[gitcode.Milestone]
	milestonesByID               map[int]gitcode.Milestone
	getMilestoneErr              error
	getMilestoneCalls            int
	lastGetMilestoneRequest      gitcode.MilestoneRequest
	createPRCommentResult        gitcode.WriteResult[gitcode.PRComment]
	createPRReviewCommentResult  gitcode.WriteResult[gitcode.PRComment]
	replyPRReviewCommentResult   gitcode.WriteResult[gitcode.PRComment]
	createWikiPageResult         gitcode.WriteResult[gitcode.WikiPage]
	addLabelResult               gitcode.WriteResult[gitcode.Issue]
	release                      gitcode.Release
	getReleaseErr                error
	createReleaseResult          gitcode.WriteResult[gitcode.Release]
	updateReleaseResult          gitcode.WriteResult[gitcode.Release]
	listIssuesPages              []gitcode.Page[gitcode.IssueSummary]
	listIssuesErrorPages         []gitcode.Page[gitcode.IssueSummary]
	listIssueRequests            []gitcode.IssueListRequest
	listIssuesErrors             []error
	listWikiPages                []gitcode.Page[gitcode.WikiPage]
	listWikiErrors               []error
	listWikiPagesCallCount       int
	onWikiCall                   func(int)
	listPRPages                  []gitcode.Page[gitcode.PullRequest]
	listPRRequests               []gitcode.PRListRequest
	prsByNumber                  map[int]gitcode.PullRequest
	prCommentsByPR               map[int][]gitcode.PRComment
	listPRCalls                  int
	prCalls                      int
	prCommentCalls               int
	issuesByNumber               map[int]gitcode.Issue
	wikiBySlug                   map[string]gitcode.WikiPage
	commentsByIssue              map[int][]gitcode.Comment
	listIssueCommentsErr         error
	lastCreateIssueRequest       gitcode.CreateIssueRequest
	lastUpdateIssueRequest       gitcode.UpdateIssueRequest
	lastUpdateIssueCommentReq    gitcode.UpdateIssueCommentRequest
	lastCreatePRRequest          gitcode.CreatePRRequest
	lastUpdatePRRequest          gitcode.UpdatePRRequest
	lastMergePRRequest           gitcode.MergePRRequest
	lastLinkPRIssueRequest       gitcode.LinkPRIssueRequest
	lastListMilestonesRequest    gitcode.MilestoneListRequest
	pushMirrorsResult            []gitcode.PushMirror
	pushMirrorsResults           [][]gitcode.PushMirror
	pushMirrorsErr               error
	lastPushMirrorsRequest       gitcode.PushMirrorListRequest
	triggerPushMirrorResult      gitcode.WriteResult[gitcode.PushMirror]
	triggerPushMirrorErr         error
	triggerPushMirrorCalls       int
	lastTriggerPushMirrorRequest gitcode.PushMirrorTriggerRequest
	lastCreateMilestoneRequest   gitcode.MilestoneWriteRequest
	lastUpdateMilestoneRequest   gitcode.MilestoneWriteRequest
	lastCreatePRCommentReq       gitcode.CreatePRCommentRequest
	lastCreatePRReviewCommentReq gitcode.CreatePRReviewCommentRequest
	lastReplyPRReviewCommentReq  gitcode.ReplyPRReviewCommentRequest
	lastWriteOptions             gitcode.WriteOptions
	lastCreateReleaseReq         gitcode.ReleaseWriteRequest
	lastUpdateReleaseReq         gitcode.ReleaseWriteRequest
	onCreateIssue                func(gitcode.CreateIssueRequest, gitcode.WriteOptions)
	onCreatePRReviewComment      func(gitcode.CreatePRReviewCommentRequest, gitcode.WriteOptions)
}

func (f *fakeGitCodeClient) nextError() error {
	if len(f.errors) == 0 {
		return nil
	}
	err := f.errors[0]
	f.errors = f.errors[1:]
	return err
}

func (f *fakeGitCodeClient) ListIssues(_ context.Context, req gitcode.IssueListRequest) (gitcode.Page[gitcode.IssueSummary], error) {
	f.listIssueRequests = append(f.listIssueRequests, req)
	if len(f.listIssuesErrors) > 0 {
		err := f.listIssuesErrors[0]
		f.listIssuesErrors = f.listIssuesErrors[1:]
		if len(f.listIssuesErrorPages) > 0 {
			page := f.listIssuesErrorPages[0]
			f.listIssuesErrorPages = f.listIssuesErrorPages[1:]
			return page, err
		}
		return gitcode.Page[gitcode.IssueSummary]{}, err
	}
	if len(f.listIssuesPages) > 0 {
		page := f.listIssuesPages[0]
		f.listIssuesPages = f.listIssuesPages[1:]
		return page, nil
	}
	return gitcode.Page[gitcode.IssueSummary]{}, nil
}
func (f *fakeGitCodeClient) GetIssue(_ context.Context, req gitcode.IssueRequest) (gitcode.Issue, error) {
	f.issueCalls++
	if err := f.nextError(); err != nil {
		return gitcode.Issue{}, err
	}
	if f.issuesByNumber != nil {
		if issue, ok := f.issuesByNumber[req.Number]; ok {
			return issue, nil
		}
	}
	return f.issue, nil
}
func (f *fakeGitCodeClient) ListIssueComments(_ context.Context, req gitcode.IssueRequest) (gitcode.Page[gitcode.Comment], error) {
	f.commentCalls++
	if f.listIssueCommentsErr != nil {
		return gitcode.Page[gitcode.Comment]{}, f.listIssueCommentsErr
	}
	if f.commentsByIssue != nil {
		return gitcode.Page[gitcode.Comment]{Items: f.commentsByIssue[req.Number]}, nil
	}
	return gitcode.Page[gitcode.Comment]{Items: f.comments}, nil
}
func (f *fakeGitCodeClient) ListPRs(_ context.Context, req gitcode.PRListRequest) (gitcode.Page[gitcode.PullRequest], error) {
	f.listPRCalls++
	f.listPRRequests = append(f.listPRRequests, req)
	if len(f.listPRPages) > 0 {
		page := f.listPRPages[0]
		f.listPRPages = f.listPRPages[1:]
		return page, nil
	}
	return gitcode.Page[gitcode.PullRequest]{}, nil
}
func (f *fakeGitCodeClient) GetPR(_ context.Context, req gitcode.PRRequest) (gitcode.PullRequest, error) {
	f.prCalls++
	if f.prsByNumber != nil {
		if pr, ok := f.prsByNumber[req.Number]; ok {
			return pr, nil
		}
	}
	return gitcode.PullRequest{}, nil
}
func (f *fakeGitCodeClient) ListPRComments(_ context.Context, req gitcode.PRRequest) (gitcode.Page[gitcode.PRComment], error) {
	f.prCommentCalls++
	if f.prCommentsByPR != nil {
		return gitcode.Page[gitcode.PRComment]{Items: f.prCommentsByPR[req.Number]}, nil
	}
	return gitcode.Page[gitcode.PRComment]{}, nil
}
func (f *fakeGitCodeClient) GetWikiPage(_ context.Context, req gitcode.WikiPageRequest) (gitcode.WikiPage, error) {
	f.wikiCalls++
	if f.onWikiCall != nil {
		f.onWikiCall(f.wikiCalls)
	}
	if err := f.nextError(); err != nil {
		return gitcode.WikiPage{}, err
	}
	if f.wikiBySlug != nil {
		if page, ok := f.wikiBySlug[req.Slug]; ok {
			return page, nil
		}
	}
	return f.wiki, nil
}
func (f *fakeGitCodeClient) ListWikiPages(context.Context, gitcode.WikiListRequest) (gitcode.Page[gitcode.WikiPage], error) {
	f.listWikiPagesCallCount++
	if len(f.listWikiErrors) > 0 {
		err := f.listWikiErrors[0]
		f.listWikiErrors = f.listWikiErrors[1:]
		return gitcode.Page[gitcode.WikiPage]{}, err
	}
	if len(f.listWikiPages) > 0 {
		page := f.listWikiPages[0]
		f.listWikiPages = f.listWikiPages[1:]
		return page, nil
	}
	return gitcode.Page[gitcode.WikiPage]{}, nil
}
func (f *fakeGitCodeClient) Search(context.Context, gitcode.SearchRequest) (gitcode.Page[gitcode.SearchResult], error) {
	return gitcode.Page[gitcode.SearchResult]{}, nil
}
func (f *fakeGitCodeClient) ListIssueAttachments(context.Context, gitcode.IssueRequest) (gitcode.Page[gitcode.AttachmentSummary], error) {
	return gitcode.Page[gitcode.AttachmentSummary]{}, nil
}
func (f *fakeGitCodeClient) GetAttachment(context.Context, gitcode.AttachmentRequest) (gitcode.AttachmentBody, error) {
	return gitcode.AttachmentBody{}, nil
}
func (f *fakeGitCodeClient) CreateIssue(_ context.Context, req gitcode.CreateIssueRequest, opts gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Issue], error) {
	f.createIssueCalls++
	f.lastCreateIssueRequest = req
	f.lastWriteOptions = opts
	if f.onCreateIssue != nil {
		f.onCreateIssue(req, opts)
	}
	if err := f.nextError(); err != nil {
		return gitcode.WriteResult[gitcode.Issue]{}, err
	}
	if len(f.createIssueResults) > 0 {
		result := f.createIssueResults[0]
		f.createIssueResults = f.createIssueResults[1:]
		return result, nil
	}
	return f.createIssueResult, nil
}
func (f *fakeGitCodeClient) UpdateIssue(_ context.Context, req gitcode.UpdateIssueRequest, opts gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Issue], error) {
	f.updateIssueCalls++
	f.lastUpdateIssueRequest = req
	f.lastWriteOptions = opts
	if err := f.nextError(); err != nil {
		return gitcode.WriteResult[gitcode.Issue]{}, err
	}
	return f.updateIssueResult, nil
}
func (f *fakeGitCodeClient) CreateIssueComment(context.Context, gitcode.CreateIssueCommentRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Comment], error) {
	f.createIssueCommentCalls++
	if err := f.nextError(); err != nil {
		return gitcode.WriteResult[gitcode.Comment]{}, err
	}
	return f.createIssueCommentResult, nil
}
func (f *fakeGitCodeClient) UpdateIssueComment(_ context.Context, req gitcode.UpdateIssueCommentRequest, opts gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Comment], error) {
	f.updateIssueCommentCalls++
	f.lastUpdateIssueCommentReq = req
	f.lastWriteOptions = opts
	if err := f.nextError(); err != nil {
		return gitcode.WriteResult[gitcode.Comment]{}, err
	}
	return f.updateIssueCommentResult, nil
}
func (f *fakeGitCodeClient) CreatePRComment(_ context.Context, req gitcode.CreatePRCommentRequest, opts gitcode.WriteOptions) (gitcode.WriteResult[gitcode.PRComment], error) {
	f.createPRCommentCalls++
	f.lastCreatePRCommentReq = req
	f.lastWriteOptions = opts
	if err := f.nextError(); err != nil {
		return gitcode.WriteResult[gitcode.PRComment]{}, err
	}
	return f.createPRCommentResult, nil
}
func (f *fakeGitCodeClient) CreatePRReviewComment(_ context.Context, req gitcode.CreatePRReviewCommentRequest, opts gitcode.WriteOptions) (gitcode.WriteResult[gitcode.PRComment], error) {
	f.createPRReviewCommentCalls++
	f.lastCreatePRReviewCommentReq = req
	f.lastWriteOptions = opts
	if f.onCreatePRReviewComment != nil {
		f.onCreatePRReviewComment(req, opts)
	}
	if err := f.nextError(); err != nil {
		return gitcode.WriteResult[gitcode.PRComment]{}, err
	}
	return f.createPRReviewCommentResult, nil
}
func (f *fakeGitCodeClient) ReplyPRReviewComment(_ context.Context, req gitcode.ReplyPRReviewCommentRequest, opts gitcode.WriteOptions) (gitcode.WriteResult[gitcode.PRComment], error) {
	f.replyPRReviewCommentCalls++
	f.lastReplyPRReviewCommentReq = req
	f.lastWriteOptions = opts
	if err := f.nextError(); err != nil {
		return gitcode.WriteResult[gitcode.PRComment]{}, err
	}
	return f.replyPRReviewCommentResult, nil
}
func (f *fakeGitCodeClient) CreatePR(_ context.Context, req gitcode.CreatePRRequest, opts gitcode.WriteOptions) (gitcode.WriteResult[gitcode.PullRequest], error) {
	f.createPRCalls++
	f.lastCreatePRRequest = req
	f.lastWriteOptions = opts
	if err := f.nextError(); err != nil {
		return gitcode.WriteResult[gitcode.PullRequest]{}, err
	}
	return f.createPRResult, nil
}
func (f *fakeGitCodeClient) UpdatePR(_ context.Context, req gitcode.UpdatePRRequest, opts gitcode.WriteOptions) (gitcode.WriteResult[gitcode.PullRequest], error) {
	f.updatePRCalls++
	f.lastUpdatePRRequest = req
	f.lastWriteOptions = opts
	if err := f.nextError(); err != nil {
		return gitcode.WriteResult[gitcode.PullRequest]{}, err
	}
	return f.updatePRResult, nil
}

func (f *fakeGitCodeClient) MergePR(_ context.Context, req gitcode.MergePRRequest, opts gitcode.WriteOptions) (gitcode.WriteResult[gitcode.PullRequest], error) {
	f.mergePRCalls++
	f.lastMergePRRequest = req
	f.lastWriteOptions = opts
	if err := f.nextError(); err != nil {
		return gitcode.WriteResult[gitcode.PullRequest]{}, err
	}
	return f.mergePRResult, nil
}
func (f *fakeGitCodeClient) LinkPRIssue(_ context.Context, req gitcode.LinkPRIssueRequest, opts gitcode.WriteOptions) (gitcode.WriteResult[[]gitcode.Issue], error) {
	f.linkPRIssueCalls++
	f.lastLinkPRIssueRequest = req
	f.lastWriteOptions = opts
	if err := f.nextError(); err != nil {
		return gitcode.WriteResult[[]gitcode.Issue]{}, err
	}
	return f.linkPRIssueResult, nil
}
func (f *fakeGitCodeClient) CreateWikiPage(context.Context, gitcode.CreateWikiPageRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.WikiPage], error) {
	f.createWikiPageCalls++
	if err := f.nextError(); err != nil {
		return gitcode.WriteResult[gitcode.WikiPage]{}, err
	}
	return f.createWikiPageResult, nil
}
func (f *fakeGitCodeClient) UpdateWikiPage(context.Context, gitcode.UpdateWikiPageRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.WikiPage], error) {
	return gitcode.WriteResult[gitcode.WikiPage]{}, nil
}
func (f *fakeGitCodeClient) DeleteWikiPage(context.Context, gitcode.DeleteWikiPageRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.WikiPage], error) {
	return gitcode.WriteResult[gitcode.WikiPage]{}, nil
}
func (f *fakeGitCodeClient) AddLabel(context.Context, gitcode.LabelRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Issue], error) {
	f.addLabelCalls++
	if err := f.nextError(); err != nil {
		return gitcode.WriteResult[gitcode.Issue]{}, err
	}
	return f.addLabelResult, nil
}
func (f *fakeGitCodeClient) RemoveLabel(context.Context, gitcode.LabelRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Issue], error) {
	return gitcode.WriteResult[gitcode.Issue]{}, nil
}

func (f *fakeGitCodeClient) ListMilestones(_ context.Context, req gitcode.MilestoneListRequest) (gitcode.Page[gitcode.Milestone], error) {
	f.lastListMilestonesRequest = req
	if err := f.nextError(); err != nil {
		return gitcode.Page[gitcode.Milestone]{}, err
	}
	return f.listMilestonesResult, nil
}

func (f *fakeGitCodeClient) ListPushRemoteMirrors(_ context.Context, req gitcode.PushMirrorListRequest) ([]gitcode.PushMirror, error) {
	f.lastPushMirrorsRequest = req
	if f.pushMirrorsErr != nil {
		return nil, f.pushMirrorsErr
	}
	if len(f.pushMirrorsResults) > 0 {
		result := f.pushMirrorsResults[0]
		f.pushMirrorsResults = f.pushMirrorsResults[1:]
		return append([]gitcode.PushMirror(nil), result...), nil
	}
	return append([]gitcode.PushMirror(nil), f.pushMirrorsResult...), nil
}

func (f *fakeGitCodeClient) TriggerPushRemoteMirror(_ context.Context, req gitcode.PushMirrorTriggerRequest, opts gitcode.WriteOptions) (gitcode.WriteResult[gitcode.PushMirror], error) {
	f.triggerPushMirrorCalls++
	f.lastTriggerPushMirrorRequest = req
	f.lastWriteOptions = opts
	if f.triggerPushMirrorErr != nil {
		return gitcode.WriteResult[gitcode.PushMirror]{}, f.triggerPushMirrorErr
	}
	return f.triggerPushMirrorResult, nil
}

func (f *fakeGitCodeClient) GetMilestone(_ context.Context, req gitcode.MilestoneRequest) (gitcode.Milestone, error) {
	f.getMilestoneCalls++
	f.lastGetMilestoneRequest = req
	if f.getMilestoneErr != nil {
		return gitcode.Milestone{}, f.getMilestoneErr
	}
	if milestone, ok := f.milestonesByID[req.ID]; ok {
		return milestone, nil
	}
	for _, milestone := range f.listMilestonesResult.Items {
		if milestone.RemoteID == strconv.Itoa(req.ID) {
			return milestone, nil
		}
	}
	return gitcode.Milestone{RemoteID: strconv.Itoa(req.ID), SourceID: "MILESTONE-" + strconv.Itoa(req.ID), Title: "Milestone " + strconv.Itoa(req.ID)}, nil
}

func (f *fakeGitCodeClient) CreateMilestone(_ context.Context, req gitcode.MilestoneWriteRequest, opts gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Milestone], error) {
	f.lastCreateMilestoneRequest = req
	f.lastWriteOptions = opts
	if err := f.nextError(); err != nil {
		return gitcode.WriteResult[gitcode.Milestone]{}, err
	}
	return f.createMilestoneResult, nil
}

func (f *fakeGitCodeClient) UpdateMilestone(_ context.Context, req gitcode.MilestoneWriteRequest, opts gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Milestone], error) {
	f.lastUpdateMilestoneRequest = req
	f.lastWriteOptions = opts
	if err := f.nextError(); err != nil {
		return gitcode.WriteResult[gitcode.Milestone]{}, err
	}
	return f.updateMilestoneResult, nil
}

func (f *fakeGitCodeClient) GetRelease(context.Context, gitcode.ReleaseRequest) (gitcode.Release, error) {
	f.getReleaseCalls++
	if f.getReleaseErr != nil {
		return gitcode.Release{}, f.getReleaseErr
	}
	return f.release, nil
}

func (f *fakeGitCodeClient) CreateRelease(_ context.Context, req gitcode.ReleaseWriteRequest, opts gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Release], error) {
	f.createReleaseCalls++
	f.lastCreateReleaseReq = req
	f.lastWriteOptions = opts
	if err := f.nextError(); err != nil {
		return gitcode.WriteResult[gitcode.Release]{}, err
	}
	if !f.createReleaseResult.Confirmed {
		return gitcode.WriteResult[gitcode.Release]{Record: gitcode.Release{TagName: req.TagName, Name: req.Name, Description: req.Description, ReleaseStatus: req.ReleaseStatus}, Confirmed: true, RemoteID: req.TagName, RemoteSlug: req.TagName, ConfirmedAt: time.Now()}, nil
	}
	return f.createReleaseResult, nil
}

func (f *fakeGitCodeClient) UpdateRelease(_ context.Context, req gitcode.ReleaseWriteRequest, opts gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Release], error) {
	f.updateReleaseCalls++
	f.lastUpdateReleaseReq = req
	f.lastWriteOptions = opts
	if err := f.nextError(); err != nil {
		return gitcode.WriteResult[gitcode.Release]{}, err
	}
	if !f.updateReleaseResult.Confirmed {
		return gitcode.WriteResult[gitcode.Release]{Record: gitcode.Release{TagName: req.TagName, Name: req.Name, Description: req.Description, ReleaseStatus: req.ReleaseStatus}, Confirmed: true, RemoteID: req.TagName, RemoteSlug: req.TagName, ConfirmedAt: time.Now()}, nil
	}
	return f.updateReleaseResult, nil
}

var _ gitcode.Client = (*fakeGitCodeClient)(nil)

type writeRefreshFailStore struct {
	cache.Store
	failNextRefresh bool
}

type listRepositoriesFailStore struct {
	cache.Store
	err error
}

func (s *listRepositoriesFailStore) ListRepositories(context.Context) ([]cache.RepositoryBinding, error) {
	return nil, s.err
}

func (s *writeRefreshFailStore) UpsertRecordGraph(ctx context.Context, graph cache.RecordGraph) error {
	if s.failNextRefresh {
		s.failNextRefresh = false
		return errors.New("injected cache refresh failure")
	}
	return s.Store.UpsertRecordGraph(ctx, graph)
}

type corruptingStore struct {
	cache.Store
}

func (s corruptingStore) IntegrityCheck(context.Context) error {
	return cache.ErrCacheCorruption{Path: "memory", Detail: "test corruption"}
}

type brokenStore struct {
	sources map[string]cache.Source
	links   []cache.Link
}

func fakeBrokenStore() *brokenStore {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return &brokenStore{sources: map[string]cache.Source{"DOC-1": {RepoID: "fixture-a", ID: "DOC-1", Kind: "doc", Path: "doc.md", Title: "Doc", Body: "body", Status: "ready", UpdatedAt: now}}, links: []cache.Link{{RepoID: "fixture-a", SourceID: "DOC-1", TargetID: "MISSING-1", Kind: "mentions", Text: "missing"}}}
}

func (f *brokenStore) AddRepository(context.Context, cache.RepositoryBinding) error { return nil }
func (f *brokenStore) UpsertRepo(context.Context, cache.RepositoryBinding) error    { return nil }
func (f *brokenStore) GetRepository(context.Context, string) (cache.RepositoryBinding, error) {
	return cache.RepositoryBinding{RepoID: "fixture-a", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}, nil
}
func (f *brokenStore) GetRepo(ctx context.Context, repoID string) (cache.RepositoryBinding, error) {
	return f.GetRepository(ctx, repoID)
}
func (f *brokenStore) ListRepositories(context.Context) ([]cache.RepositoryBinding, error) {
	return nil, nil
}
func (f *brokenStore) SchemaVersion(context.Context) (int, error) {
	return cache.CurrentSchemaVersion(), nil
}
func (f *brokenStore) UpsertSourceGraph(context.Context, cache.SourceGraph) error { return nil }
func (f *brokenStore) UpsertRecordGraph(context.Context, cache.RecordGraph) error { return nil }
func (f *brokenStore) UpsertSyncGraph(context.Context, cache.SyncGraph) error     { return nil }
func (f *brokenStore) UpsertSource(context.Context, cache.Source) error           { return nil }
func (f *brokenStore) GetSource(_ context.Context, id string) (cache.Source, error) {
	if source, ok := f.sources[id]; ok {
		return source, nil
	}
	return cache.Source{}, cache.ErrNotFound
}
func (f *brokenStore) GetSourceScoped(_ context.Context, repoID, id string) (cache.Source, error) {
	if source, ok := f.sources[id]; ok && source.RepoID == repoID {
		return source, nil
	}
	return cache.Source{}, cache.ErrNotFound
}
func (f *brokenStore) ListSources(context.Context, cache.SourceFilter) ([]cache.Source, error) {
	out := make([]cache.Source, 0, len(f.sources))
	for _, source := range f.sources {
		out = append(out, source)
	}
	return out, nil
}
func (f *brokenStore) ReconcileChildSources(context.Context, string, string, string, []string) error {
	return nil
}
func (f *brokenStore) UpsertPRReviewComment(context.Context, cache.PRReviewComment) error {
	return nil
}
func (f *brokenStore) ListPRReviewComments(context.Context, cache.PRReviewCommentFilter) ([]cache.PRReviewComment, error) {
	return nil, nil
}
func (f *brokenStore) UpsertPRReviewDiscussion(context.Context, cache.PRReviewDiscussion) error {
	return nil
}
func (f *brokenStore) ListPRReviewDiscussions(context.Context, cache.PRReviewDiscussionFilter) ([]cache.PRReviewDiscussion, error) {
	return nil, nil
}
func (f *brokenStore) UpsertPRReviewPosition(context.Context, cache.PRReviewPosition) error {
	return nil
}
func (f *brokenStore) ListPRReviewPositions(context.Context, cache.PRReviewPositionFilter) ([]cache.PRReviewPosition, error) {
	return nil, nil
}
func (f *brokenStore) SearchSources(context.Context, cache.SearchQuery) ([]cache.SearchResult, error) {
	return nil, nil
}
func (f *brokenStore) GetRecord(context.Context, string, string) (cache.Record, error) {
	return cache.Record{}, cache.ErrNotFound
}
func (f *brokenStore) ListRecords(context.Context, cache.RecordFilter) ([]cache.Record, error) {
	return nil, nil
}
func (f *brokenStore) SearchRecords(context.Context, cache.SearchQuery) ([]cache.SearchResult, error) {
	return nil, nil
}
func (f *brokenStore) UpsertIdentity(context.Context, cache.Identity) error { return nil }
func (f *brokenStore) GetIdentityMap(context.Context, string) ([]cache.Identity, error) {
	return nil, nil
}
func (f *brokenStore) GetIdentityMapScoped(context.Context, string, string) ([]cache.Identity, error) {
	return nil, nil
}
func (f *brokenStore) ResolveAlias(context.Context, cache.RemoteAlias) (cache.Identity, error) {
	return cache.Identity{}, cache.ErrNotFound
}
func (f *brokenStore) ResolveAliasScoped(context.Context, string, cache.RemoteAlias) (cache.Identity, error) {
	return cache.Identity{}, cache.ErrNotFound
}
func (f *brokenStore) ResolveRepoAlias(context.Context, string, cache.RemoteAlias) (cache.Identity, error) {
	return cache.Identity{}, cache.ErrNotFound
}
func (f *brokenStore) DiagnoseAlias(context.Context, cache.RemoteAlias) ([]cache.Identity, error) {
	return nil, nil
}
func (f *brokenStore) UpsertLink(context.Context, cache.Link) error { return nil }
func (f *brokenStore) ListLinks(context.Context, cache.LinkFilter) ([]cache.Link, error) {
	return f.links, nil
}
func (f *brokenStore) GetBacklinks(context.Context, string) ([]cache.Source, error) { return nil, nil }
func (f *brokenStore) GetBacklinksScoped(context.Context, string, string) ([]cache.Source, error) {
	return nil, nil
}
func (f *brokenStore) UpsertChunk(context.Context, cache.Chunk) (cache.Chunk, error) {
	return cache.Chunk{}, nil
}
func (f *brokenStore) GetChunks(context.Context, string) ([]cache.Chunk, error) { return nil, nil }
func (f *brokenStore) GetChunksScoped(context.Context, string, string) ([]cache.Chunk, error) {
	return nil, nil
}
func (f *brokenStore) ListChunks(context.Context, cache.ChunkFilter) ([]cache.Chunk, error) {
	return nil, nil
}
func (f *brokenStore) UpsertEmbeddingNamespace(context.Context, cache.EmbeddingNamespace) (cache.EmbeddingNamespace, error) {
	return cache.EmbeddingNamespace{}, nil
}
func (f *brokenStore) ResolveEmbeddingNamespace(context.Context, cache.EmbeddingNamespaceIdentity) (cache.EmbeddingNamespace, bool, error) {
	return cache.EmbeddingNamespace{}, false, nil
}
func (f *brokenStore) GetEmbeddingNamespace(context.Context, string, string) (cache.EmbeddingNamespace, error) {
	return cache.EmbeddingNamespace{}, cache.ErrNotFound
}
func (f *brokenStore) ListEmbeddingNamespaces(context.Context, string) ([]cache.EmbeddingNamespace, error) {
	return nil, nil
}
func (f *brokenStore) UpsertChunkEmbedding(context.Context, cache.ChunkEmbedding) error {
	return nil
}
func (f *brokenStore) ListChunkEmbeddings(context.Context, cache.ChunkEmbeddingFilter) ([]cache.ChunkEmbedding, error) {
	return nil, nil
}
func (f *brokenStore) UpsertRAGIndexRun(context.Context, cache.RAGIndexRun) error {
	return nil
}
func (f *brokenStore) GetRAGIndexRun(context.Context, string, string) (cache.RAGIndexRun, error) {
	return cache.RAGIndexRun{}, cache.ErrNotFound
}
func (f *brokenStore) ListRAGIndexRuns(context.Context, cache.RAGIndexRunFilter) ([]cache.RAGIndexRun, error) {
	return nil, nil
}
func (f *brokenStore) GetRepoContentState(context.Context, string) (cache.RepoContentState, error) {
	return cache.RepoContentState{}, nil
}
func (f *brokenStore) GetRAGCoverageState(context.Context, string, string) (cache.RAGCoverageState, bool, error) {
	return cache.RAGCoverageState{}, false, nil
}
func (f *brokenStore) UpsertRAGCoverageState(context.Context, cache.RAGCoverageState) error {
	return nil
}
func (f *brokenStore) RecordSyncEvent(context.Context, cache.SyncEvent) error { return nil }
func (f *brokenStore) GetSyncEventByKey(ctx context.Context, key string) (*cache.SyncEvent, error) {
	return nil, nil
}
func (f *brokenStore) ListCompletedSyncEventsScoped(context.Context, string) ([]cache.SyncEvent, error) {
	return nil, nil
}
func (f *brokenStore) UpsertSyncFrontier(context.Context, cache.SyncFrontier) error { return nil }
func (f *brokenStore) GetSyncFrontier(context.Context, string, string, string, string) (cache.SyncFrontier, bool, error) {
	return cache.SyncFrontier{}, false, nil
}
func (f *brokenStore) UpsertIssueCommentSync(context.Context, cache.IssueCommentSync) error {
	return nil
}
func (f *brokenStore) GetIssueCommentSync(context.Context, string, string) (cache.IssueCommentSync, bool, error) {
	return cache.IssueCommentSync{}, false, nil
}
func (f *brokenStore) ListIssueCommentSync(context.Context, cache.IssueCommentSyncFilter) ([]cache.IssueCommentSync, error) {
	return nil, nil
}
func (f *brokenStore) IssueCommentSyncSummary(context.Context, string) (cache.IssueCommentSyncSummary, error) {
	return cache.IssueCommentSyncSummary{}, nil
}
func (f *brokenStore) UpsertRecordComments(context.Context, string, string, []cache.RecordComment) error {
	return nil
}
func (f *brokenStore) ReconcileRecordComments(context.Context, string, string, []string) error {
	return nil
}
func (f *brokenStore) ReplaceRecordComments(context.Context, string, string, []cache.RecordComment) error {
	return nil
}
func (f *brokenStore) RecordAuditEvent(context.Context, cache.AuditTrailEntry) error { return nil }
func (f *brokenStore) GetAuditEventByKey(context.Context, string, string) (*cache.AuditTrailEntry, error) {
	return nil, nil
}
func (f *brokenStore) RecordCacheConfirmation(context.Context, cache.CacheConfirmationRecord) error {
	return nil
}
func (f *brokenStore) GetCacheConfirmationByKey(context.Context, string, string) (*cache.CacheConfirmationRecord, error) {
	return nil, nil
}
func (f *brokenStore) GetSyncStatus(context.Context, string) (cache.SyncStatus, error) {
	return cache.SyncStatus{}, nil
}
func (f *brokenStore) GetSyncStatusScoped(context.Context, string, string) (cache.SyncStatus, error) {
	return cache.SyncStatus{}, nil
}
func (f *brokenStore) UpsertConflict(context.Context, cache.Conflict) error { return nil }
func (f *brokenStore) GetConflicts(context.Context, string) ([]cache.Conflict, error) {
	return nil, nil
}
func (f *brokenStore) RecordCounts(context.Context, string) (cache.RecordCounts, error) {
	return cache.RecordCounts{}, nil
}
func (f *brokenStore) WALCapable(context.Context) (bool, string, error)     { return true, "memory", nil }
func (f *brokenStore) UpsertSnapshot(context.Context, cache.Snapshot) error { return nil }
func (f *brokenStore) GetSnapshot(context.Context, string, string) (cache.Snapshot, error) {
	return cache.Snapshot{}, cache.ErrNotFound
}
func (f *brokenStore) ListSnapshotChunks(context.Context, string, string) ([]cache.SnapshotChunk, error) {
	return nil, nil
}
func (f *brokenStore) IntegrityCheck(context.Context) error    { return nil }
func (f *brokenStore) ResetLive(context.Context, string) error { return nil }
func (f *brokenStore) AcquireLock(context.Context, string) (*cache.LockHandle, error) {
	return nil, nil
}
func (f *brokenStore) ReleaseLock(context.Context, *cache.LockHandle) error { return nil }
func (f *brokenStore) AcquireWriter(context.Context, cache.WriterRequest) (*cache.WriterLease, error) {
	return nil, nil
}
func (f *brokenStore) ReleaseWriter(context.Context, *cache.WriterLease) error { return nil }
func (f *brokenStore) Checkpoint(context.Context, string) error                { return nil }
func (f *brokenStore) Close() error                                            { return nil }

func TestScenario013009AddLabelDryRunValidatesWithoutMutation(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedStore(t, ctx, store)
	client := &fakeGitCodeClient{}
	svc := NewWithClient(store, client)
	result, err := svc.AddLabel(ctx, WriteCommandRequest{
		RepoID: "fixture-a",
		Mode:   WriteModeDryRun,
		Number: 42,
		Label:  "bug",
	})
	if err != nil {
		t.Fatalf("AddLabel dry-run: %v", err)
	}
	if result.Status != "dry_run_valid" {
		t.Fatalf("AddLabel dry-run status=%q", result.Status)
	}
	if client.addLabelCalls != 0 || client.updateIssueCalls != 0 || client.issueCalls != 0 {
		t.Fatalf("dry-run should not call client, got add=%d update=%d get=%d", client.addLabelCalls, client.updateIssueCalls, client.issueCalls)
	}
}

func TestScenario013004AddLabelLiveUsesReadModifyUpdate(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedStore(t, ctx, store)
	updatedAt := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		issue: gitcode.Issue{ID: "42", Number: 42, Title: "Issue 42", Body: "B", State: "open", Labels: []string{"bug"}, UpdatedAt: updatedAt},
		updateIssueResult: gitcode.WriteResult[gitcode.Issue]{
			Record:       gitcode.Issue{ID: "42", Number: 42, Title: "Issue 42", Body: "B", State: "open", Labels: []string{"bug", "triage"}, UpdatedAt: updatedAt},
			Confirmed:    true,
			Operation:    "UpdateIssue",
			RemoteID:     "42",
			RemoteNumber: 42,
			ConfirmedAt:  updatedAt,
		},
	}
	svc := NewWithClient(store, client)
	t.Setenv("GITCODE_TOKEN", "test-token")
	result, err := svc.AddLabel(ctx, WriteCommandRequest{
		RepoID: "fixture-a",
		Mode:   WriteModeLive,
		Number: 42,
		Label:  "triage",
	})
	if err != nil {
		t.Fatalf("AddLabel live: %v", err)
	}
	if result.Status != "succeeded" || result.RemoteNumber != 42 {
		t.Fatalf("AddLabel live result=%#v", result)
	}
	if client.issueCalls != 1 || client.updateIssueCalls != 1 || client.addLabelCalls != 0 {
		t.Fatalf("calls get=%d update=%d add=%d", client.issueCalls, client.updateIssueCalls, client.addLabelCalls)
	}
}

func TestPublishReleaseDryRun(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedStore(t, ctx, store)
	client := &fakeGitCodeClient{}
	svc := NewWithClient(store, client)

	result, err := svc.PublishRelease(ctx, PublishReleaseRequest{RepoID: "fixture-a", Mode: WriteModeDryRun, Tag: "v0.1.0", Title: "gitcode-mcp v0.1.0", Body: "body", Assets: []PublishAssetLink{{Name: "checksums.txt", URL: "https://example.invalid/checksums.txt"}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "dry_run_valid" || result.Tag != "v0.1.0" || result.ReleaseStatus != gitcode.ReleaseStatusLatest || len(result.AssetLinks) != 1 {
		t.Fatalf("result=%#v", result)
	}
	if client.getReleaseCalls != 0 || client.createReleaseCalls != 0 || client.updateReleaseCalls != 0 {
		t.Fatalf("dry-run called client: get=%d create=%d update=%d", client.getReleaseCalls, client.createReleaseCalls, client.updateReleaseCalls)
	}
}

func TestPublishReleaseCreatesMissingRelease(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedStore(t, ctx, store)
	client := &fakeGitCodeClient{getReleaseErr: gitcode.ErrNotFound{Endpoint: "/api/v5/repos/owner-a/repo-a/releases/tags/v0.1.0"}}
	svc := NewWithClient(store, client)
	t.Setenv("GITCODE_TOKEN", "test-token")

	result, err := svc.PublishRelease(ctx, PublishReleaseRequest{RepoID: "fixture-a", Mode: WriteModeLive, Tag: "v0.1.0", Ref: "main", Title: "gitcode-mcp v0.1.0", Body: "body", Status: "latest", Assets: []PublishAssetLink{{Name: "checksums.txt", URL: "https://example.invalid/checksums.txt"}}, IdempotencyKey: "release-v0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "succeeded" || result.RemoteID != "v0.1.0" {
		t.Fatalf("result=%#v", result)
	}
	if client.getReleaseCalls != 1 || client.createReleaseCalls != 1 || client.updateReleaseCalls != 0 {
		t.Fatalf("calls get=%d create=%d update=%d", client.getReleaseCalls, client.createReleaseCalls, client.updateReleaseCalls)
	}
	if client.lastCreateReleaseReq.Owner != "owner-a" || client.lastCreateReleaseReq.Repo != "repo-a" || client.lastCreateReleaseReq.TagName != "v0.1.0" || client.lastCreateReleaseReq.Ref != "main" || client.lastCreateReleaseReq.ReleaseStatus != gitcode.ReleaseStatusLatest {
		t.Fatalf("create request=%#v", client.lastCreateReleaseReq)
	}
	if len(client.lastCreateReleaseReq.Links) != 1 || client.lastCreateReleaseReq.Links[0].Name != "checksums.txt" {
		t.Fatalf("links=%#v", client.lastCreateReleaseReq.Links)
	}
	if !strings.Contains(client.lastCreateReleaseReq.Description, "## Assets") || !strings.Contains(client.lastCreateReleaseReq.Description, "https://example.invalid/checksums.txt") {
		t.Fatalf("description=%q", client.lastCreateReleaseReq.Description)
	}
	if client.lastWriteOptions.IdempotencyKey != "release-v0.1.0" {
		t.Fatalf("idempotency=%#v", client.lastWriteOptions)
	}
}

func TestPublishReleaseUpdatesExistingRelease(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedStore(t, ctx, store)
	client := &fakeGitCodeClient{release: gitcode.Release{TagName: "v0.1.0", Name: "old"}}
	svc := NewWithClient(store, client)
	t.Setenv("GITCODE_TOKEN", "test-token")

	result, err := svc.PublishRelease(ctx, PublishReleaseRequest{RepoID: "fixture-a", Mode: WriteModeLive, Tag: "v0.1.0", Title: "gitcode-mcp v0.1.0", Body: "body", Status: "prerelease"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "succeeded" || result.ReleaseStatus != gitcode.ReleaseStatusPreRelease {
		t.Fatalf("result=%#v", result)
	}
	if client.getReleaseCalls != 1 || client.createReleaseCalls != 0 || client.updateReleaseCalls != 1 {
		t.Fatalf("calls get=%d create=%d update=%d", client.getReleaseCalls, client.createReleaseCalls, client.updateReleaseCalls)
	}
	if client.lastUpdateReleaseReq.TagName != "v0.1.0" || client.lastUpdateReleaseReq.ReleaseStatus != gitcode.ReleaseStatusPreRelease {
		t.Fatalf("update request=%#v", client.lastUpdateReleaseReq)
	}
}

func TestNormalizeWikiCachePath(t *testing.T) {
	tests := []struct {
		name     string
		remoteID string
		want     string
	}{
		{"slug with .md extension", "Home.md", "wiki/Home.md"},
		{"slug without extension", "Home", "wiki/Home.md"},
		{"slug with .markdown extension", "Guide.markdown", "wiki/Guide.md"},
		{"slug with .mdown extension", "FAQ.mdown", "wiki/FAQ.md"},
		{"slug with .mkd extension", "README.mkd", "wiki/README.md"},
		{"nested subdirectory with .md extension", "dir/Sub.md", "wiki/dir/Sub.md"},
		{"nested subdirectory without extension", "dir/Sub", "wiki/dir/Sub.md"},
		{"non-markdown extension slug", "README.txt", "wiki/README.txt.md"},
		{"empty slug", "", "wiki/Home.md"},
		{"slug with existing wiki prefix", "wiki/Home.md", "wiki/Home.md"},
		{"slug with path separators and extension", "docs/api/Overview.md", "wiki/docs/api/Overview.md"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeWikiCachePath(tt.remoteID)
			if got != tt.want {
				t.Errorf("normalizeWikiCachePath(%q) = %q, want %q", tt.remoteID, got, tt.want)
			}
		})
	}
}

func TestWikiPathNormalizationInSync(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)

	client := &fakeGitCodeClient{
		wiki: gitcode.WikiPage{
			Slug:      "Home.md",
			Title:     "Home",
			Body:      "# Home\n\nWelcome to the wiki.",
			Revision:  "rev-home-v1",
			CreatedAt: base,
			UpdatedAt: base,
		},
	}
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{
		RepoID:     "wiki-path-test",
		Owner:      "owner",
		Name:       "repo",
		APIBaseURL: "https://example.invalid/api",
		Scopes:     []cache.RepositoryScope{cache.RepositoryScopeWiki},
	}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)

	result, err := svc.SyncToCache(ctx, SyncRequest{
		RepoID:    "wiki-path-test",
		AliasType: "remote",
		AliasID:   "Home.md",
	})
	if err != nil {
		t.Fatalf("SyncToCache failed: %v", err)
	}

	if result.Record.Path != "wiki/Home.md" {
		t.Errorf("cached wiki path = %q, want %q", result.Record.Path, "wiki/Home.md")
	}
}
