#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/gitcode-mcp-028-validation.XXXXXX")"
cleanup() {
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

cp -R "$ROOT/." "$WORKDIR/"
rm -rf "$WORKDIR/.git" "$WORKDIR/ai/artifacts" 2>/dev/null || true

(
  cd "$WORKDIR"
  env -u GITCODE_TOKEN -u GITCODE_E2E_OWNER -u GITCODE_E2E_REPO -u GITCODE_E2E_BASE_URL -u GITCODE_E2E_API_BASE_URL \
    go test -run TestE2ELiveTwoCache -tags=e2e ./internal/e2e/ -count=1 -v > "$WORKDIR/skip.out" 2>&1
)

python3 - "$WORKDIR/skip.out" <<'PY'
import sys
text = open(sys.argv[1], encoding="utf-8").read()
if "missing required env: GITCODE_TOKEN" not in text:
    raise SystemExit("missing-env scenario did not skip on GITCODE_TOKEN name")
if "offline-e2e-token" in text or "Authorization:" in text:
    raise SystemExit("skip output exposed token-like or Authorization text")
PY

cat > "$WORKDIR/internal/e2e/offline_stub_test.go" <<'GOEOF'
//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

const offlineValidationToken = "offline-e2e-token-028-secret"

var offlineServer *httptest.Server
var createdIssue = map[string]any{}

func TestMain(m *testing.M) {
	offlineServer = httptest.NewServer(http.HandlerFunc(handleOfflineGitCodeAPI))
	os.Setenv("GITCODE_TOKEN", offlineValidationToken)
	os.Setenv("GITCODE_E2E_OWNER", "offline-owner")
	os.Setenv("GITCODE_E2E_REPO", "offline-repo")
	os.Setenv("GITCODE_E2E_BASE_URL", offlineServer.URL)
	code := m.Run()
	offlineServer.Close()
	os.Exit(code)
}

func handleOfflineGitCodeAPI(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+offlineValidationToken {
		http.Error(w, `{"message":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Page", "1")
	w.Header().Set("X-Per-Page", "100")
	now := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v5/repos/offline-owner/offline-repo/issues":
		writeJSON(w, []map[string]any{{"id": "1", "number": 1, "title": "Initial issue", "status": "open", "state": "open", "created_at": now, "updated_at": now}})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v5/repos/offline-owner/offline-repo/issues/1":
		writeJSON(w, map[string]any{"id": "1", "number": 1, "title": "Initial issue", "body": "initial body\n", "status": "open", "state": "open", "created_at": now, "updated_at": now})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v5/repos/offline-owner/offline-repo/issues/1/comments":
		writeJSON(w, []map[string]any{{"id": "c1", "issue_id": "1", "body": "comment one", "author": "validator", "created_at": now, "updated_at": now}})
	case r.Method == http.MethodPost && r.URL.Path == "/api/v5/repos/offline-owner/offline-repo/issues":
		var payload struct{ Title, Body string }
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || strings.TrimSpace(payload.Title) == "" {
			http.Error(w, `{"message":"bad request"}`, http.StatusBadRequest)
			return
		}
		createdIssue = map[string]any{"id": "3", "number": 3, "title": payload.Title, "body": payload.Body, "status": "open", "state": "open", "created_at": now, "updated_at": now}
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, createdIssue)
	case r.Method == http.MethodGet && r.URL.Path == "/api/v5/repos/offline-owner/offline-repo/issues/3":
		if len(createdIssue) == 0 {
			http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
			return
		}
		writeJSON(w, createdIssue)
	case r.Method == http.MethodGet && r.URL.Path == "/api/v5/repos/offline-owner/offline-repo/issues/3/comments":
		writeJSON(w, []map[string]any{})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v5/repos/offline-owner/offline-repo/wiki":
		writeJSON(w, []map[string]any{{"id": "w1", "slug": "Home", "title": "Home", "body": "home body", "revision": "rev-home", "created_at": now, "updated_at": now}})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v5/repos/offline-owner/offline-repo/wiki/Home":
		writeJSON(w, map[string]any{"id": "w1", "slug": "Home", "title": "Home", "body": "home body\r\n", "revision": "rev-home", "created_at": now, "updated_at": now})
	default:
		http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
	}
}

func writeJSON(w http.ResponseWriter, value any) {
	if err := json.NewEncoder(w).Encode(value); err != nil {
		panic(err)
	}
}
GOEOF

(
  cd "$WORKDIR"
  go test ./internal/e2e/ -run TestE2ELiveTwoCache -tags=e2e -count=1 -v > "$WORKDIR/live.out" 2>&1
)

python3 - "$WORKDIR/live.out" <<'PY'
import re
import sys
text = open(sys.argv[1], encoding="utf-8").read()
if "PASS" not in text:
    raise SystemExit("e2e stubbed API run did not pass")
if "offline-e2e-token-028-secret" in text:
    raise SystemExit("raw token appeared in e2e output")
if re.search(r"Authorization:\s*\S+", text, re.I):
    raise SystemExit("Authorization header pattern appeared in e2e output")
required = ["discovered aliases", "cache A initial sync succeeded", "cache A post-write sync succeeded", "cache B sync succeeded"]
missing = [item for item in required if item not in text]
if missing:
    raise SystemExit("e2e output missing expected product-path evidence: " + ", ".join(missing))
PY

(
  cd "$WORKDIR"
  go test ./... -count=1
)
