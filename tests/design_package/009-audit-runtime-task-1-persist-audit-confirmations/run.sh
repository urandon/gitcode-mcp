#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/gitcode-mcp-scenario-009.XXXXXX")"
trap 'rm -rf "${WORKDIR}"' EXIT

rsync -a --delete \
  --exclude '.git' \
  --exclude 'ai/artifacts' \
  --exclude 'tests/design_package/009-audit-runtime-task-1-persist-audit-confirmations' \
  "${REPO_ROOT}/" "${WORKDIR}/"

cat > "${WORKDIR}/internal/service/scenario009_design_validation_test.go" <<'GOEOF'
package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/gitcode"
)

func TestDesignPackageScenario009LiveCreateIssuePersistsSanitizedAuditConfirmation(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var createRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v5/repos/owner-a/repo-a/issues" {
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		createRequests++
		if r.Header.Get("Authorization") != "Bearer valid-scenario-009-token" {
			http.Error(w, "missing authorization", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Idempotency-Key") != "scenario-009-live-key" {
			http.Error(w, "missing idempotency key", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"remote-900","number":900,"title":"Scenario 009 Live","body":"body with private input","state":"open","created_at":"2026-06-23T12:00:00Z","updated_at":"2026-06-23T12:00:00Z"}`))
	}))
	defer server.Close()

	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner-a", Name: "repo-a", APIBaseURL: server.URL, Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITCODE_TOKEN", "")
	svc, err := NewWithMode(store, gitcode.ProviderModeLive, "valid-scenario-009-token", ServiceConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewWithMode live returned error: %v", err)
	}

	result, err := svc.CreateIssue(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Title: "Scenario 009 Live", Body: "body with private input", IdempotencyKey: " scenario-009-live-key "})
	if err != nil {
		if strings.Contains(err.Error(), "fixture client is read-only") {
			t.Fatalf("fixture fallback leaked through live write: %v", err)
		}
		t.Fatalf("CreateIssue live returned error: %v", err)
	}
	if createRequests != 1 {
		t.Fatalf("mock create requests=%d want 1", createRequests)
	}
	if result.Command != "create-issue" || result.Status != "succeeded" || result.RemoteID != "remote-900" || result.IdempotencyKey != "scenario-009-live-key" {
		t.Fatalf("result=%#v", result)
	}
	resultBytes, _ := json.Marshal(result)
	if strings.Contains(string(resultBytes), "fixture client is read-only") || strings.Contains(string(resultBytes), "valid-scenario-009-token") {
		t.Fatalf("unsafe command result: %s", resultBytes)
	}

	entry, err := store.GetAuditEventByKey(ctx, "fixture-a", "scenario-009-live-key")
	if err != nil {
		t.Fatalf("GetAuditEventByKey returned error: %v", err)
	}
	if entry == nil {
		t.Fatal("audit confirmation missing")
	}
	if entry.Operation != "create-issue" || entry.Command != "create-issue" || entry.Mode != "live" || entry.IdempotencyKey != "scenario-009-live-key" || entry.Status != "succeeded" || entry.PayloadHash == "" || entry.RemoteID != "remote-900" {
		t.Fatalf("audit entry=%#v", entry)
	}
	if entry.CreatedAt.IsZero() {
		t.Fatalf("audit timestamp=%s", entry.CreatedAt)
	}
	if entry.RequestMetadata["method"] != "POST" || entry.RequestMetadata["provider_mode"] != "live" || entry.RequestMetadata["idempotency_key"] != "scenario-009-live-key" || entry.RequestMetadata["remote_alias"] != "remote-900" || entry.RequestMetadata["source_fingerprint"] != entry.PayloadHash {
		t.Fatalf("audit metadata=%#v payload_hash=%q", entry.RequestMetadata, entry.PayloadHash)
	}

	entryBytes, _ := json.Marshal(entry)
	lowerEntry := strings.ToLower(string(entryBytes))
	for _, forbidden := range []string{"valid-scenario-009-token", "authorization", "bearer ", "cookie", "session=", server.URL, "private.example", "raw_body", "body with private input", "fixture client is read-only"} {
		if strings.Contains(lowerEntry, strings.ToLower(forbidden)) {
			t.Fatalf("forbidden audit data %q found in %s", forbidden, entryBytes)
		}
	}

	replay, err := svc.CreateIssue(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Title: "Scenario 009 Live", Body: "body with private input", IdempotencyKey: "scenario-009-live-key"})
	if err != nil {
		t.Fatalf("CreateIssue replay returned error: %v", err)
	}
	if !replay.Replayed || replay.Status != "already_applied" || createRequests != 1 {
		t.Fatalf("replay=%#v requests=%d", replay, createRequests)
	}

	_, err = svc.CreateIssue(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Title: "Changed", Body: "body with private input", IdempotencyKey: "scenario-009-live-key"})
	if err == nil || !strings.Contains(err.Error(), "write_idempotency_conflict") || createRequests != 1 {
		t.Fatalf("conflict err=%v requests=%d", err, createRequests)
	}
}
GOEOF

cd "${WORKDIR}"
go test ./...
