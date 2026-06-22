#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
cd "${REPO_ROOT}"

VALIDATION_TEST="${SCRIPT_DIR}/validation_runtime_test.go"
trap 'rm -f "${VALIDATION_TEST}"' EXIT

cat > "${VALIDATION_TEST}" <<'GO'
package validation_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gitcode-mcp/internal/gitcode"
	live "gitcode-mcp/internal/provider/live"
)

func TestTask009LiveAdapterRuntimePath(t *testing.T) {
	provider, err := live.NewLiveProvider(live.Config{Mode: gitcode.ProviderModeLive, LiveAllowed: true, Token: "test-token"})
	if err != nil {
		t.Fatalf("NewLiveProvider returned error: %v", err)
	}
	if provider == nil {
		t.Fatalf("NewLiveProvider returned nil")
	}
	var _ live.Provider = provider

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("expected bearer auth header, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v5/repos/example-owner/example-repo/issues":
			fmt.Fprint(w, `[{"id":"REMOTE-ISSUE-1","number":1,"title":"remote issue","state":"open"}]`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v5/repos/example-owner/example-repo/issues/1":
			fmt.Fprint(w, `{"id":"REMOTE-ISSUE-1","number":1,"title":"remote issue","body":"remote body","state":"open"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v5/repos/example-owner/example-repo/issues/1/comments":
			fmt.Fprint(w, `[{"id":"REMOTE-COMMENT-1","issue_id":"REMOTE-ISSUE-1","body":"remote comment","author":"remote-user"}]`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v5/repos/example-owner/example-repo/wiki":
			fmt.Fprint(w, `[{"id":"REMOTE-WIKI-1","slug":"Home","title":"remote wiki","revision":"remote-rev-1"}]`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v5/repos/example-owner/example-repo/wiki/Home":
			fmt.Fprint(w, `{"id":"REMOTE-WIKI-1","slug":"Home","title":"remote wiki","body":"remote wiki body","revision":"remote-rev-1"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v5/repos/example-owner/example-repo/issues":
			if r.Header.Get("Idempotency-Key") != "validation-key-1" {
				t.Fatalf("missing idempotency key")
			}
			var payload gitcode.CreateIssueRequest
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode create issue payload: %v", err)
			}
			if payload.Title != "created remotely" {
				t.Fatalf("unexpected create issue payload: %+v", payload)
			}
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"REMOTE-ISSUE-2","number":2,"title":"created remotely","body":"created body"}`)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client, err := live.NewHTTPClient(live.HTTPClientConfig{BaseURL: server.URL, Token: "test-token", MaxRetries: 0})
	if err != nil {
		t.Fatalf("NewHTTPClient returned error: %v", err)
	}

	issues, err := client.ListIssues(context.Background(), gitcode.IssueListRequest{Owner: "example-owner", Repo: "example-repo"})
	if err != nil || len(issues.Items) != 1 || issues.Items[0].ID != "REMOTE-ISSUE-1" || issues.Items[0].ID == "ISSUE-42" {
		t.Fatalf("live issue list did not use remote-shaped response: page=%+v err=%v", issues, err)
	}
	issue, err := client.GetIssue(context.Background(), gitcode.IssueRequest{Owner: "example-owner", Repo: "example-repo", Number: 1})
	if err != nil || issue.ID != "REMOTE-ISSUE-1" {
		t.Fatalf("GetIssue did not return remote issue: issue=%+v err=%v", issue, err)
	}
	comments, err := client.ListIssueComments(context.Background(), gitcode.IssueRequest{Owner: "example-owner", Repo: "example-repo", Number: 1})
	if err != nil || len(comments.Items) != 1 || comments.Items[0].ID != "REMOTE-COMMENT-1" {
		t.Fatalf("ListIssueComments did not return remote comment: page=%+v err=%v", comments, err)
	}
	wikiPages, err := client.ListWikiPages(context.Background(), gitcode.WikiListRequest{Owner: "example-owner", Repo: "example-repo"})
	if err != nil || len(wikiPages.Items) != 1 || wikiPages.Items[0].ID != "REMOTE-WIKI-1" || wikiPages.Items[0].ID == "WIKI-HOME" {
		t.Fatalf("ListWikiPages did not return remote wiki: page=%+v err=%v", wikiPages, err)
	}
	wiki, err := client.GetWikiPage(context.Background(), gitcode.WikiPageRequest{Owner: "example-owner", Repo: "example-repo", Slug: "Home"})
	if err != nil || wiki.ID != "REMOTE-WIKI-1" {
		t.Fatalf("GetWikiPage did not return remote wiki: wiki=%+v err=%v", wiki, err)
	}
	created, err := client.CreateIssue(context.Background(), gitcode.CreateIssueRequest{Owner: "example-owner", Repo: "example-repo", Title: "created remotely", Body: "created body"}, gitcode.WriteOptions{IdempotencyKey: "validation-key-1"})
	if err != nil || !created.Confirmed || created.RemoteID != "REMOTE-ISSUE-2" || strings.Contains(fmt.Sprintf("%+v", err), "fixture client is read-only") {
		t.Fatalf("CreateIssue did not use live write path: result=%+v err=%v", created, err)
	}
}

func TestTask009LiveAdapterAdmissionGates(t *testing.T) {
	cases := []live.Config{
		{Mode: gitcode.ProviderModeLive, LiveAllowed: false, Token: "test-token"},
		{Mode: gitcode.ProviderModeLive, LiveAllowed: true, Token: ""},
		{Mode: gitcode.ProviderModeFixture, LiveAllowed: true, Token: "test-token"},
	}
	for _, tc := range cases {
		_, err := live.NewLiveProvider(tc)
		if !live.IsProviderUnavailable(err) {
			t.Fatalf("expected provider unavailable for %+v, got %T %v", tc, err, err)
		}
	}
	if live.IsProviderUnavailable(errors.New("unrelated")) {
		t.Fatalf("IsProviderUnavailable returned true for unrelated error")
	}
}

func TestTask009LiveAdapterTypedErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		check  func(error) bool
	}{
		{"rate-limit", http.StatusTooManyRequests, func(err error) bool { var target gitcode.ErrRateLimited; return errors.As(err, &target) }},
		{"auth-expired", http.StatusUnauthorized, func(err error) bool { var target gitcode.ErrAuthExpired; return errors.As(err, &target) }},
		{"forbidden", http.StatusForbidden, func(err error) bool { var target gitcode.ErrForbidden; return errors.As(err, &target) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.status == http.StatusTooManyRequests {
					w.Header().Set("Retry-After", "0")
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, `{"message":"diagnostic"}`)
			}))
			defer server.Close()
			client, err := live.NewHTTPClient(live.HTTPClientConfig{BaseURL: server.URL, Token: "test-token", MaxRetries: 0})
			if err != nil {
				t.Fatalf("NewHTTPClient returned error: %v", err)
			}
			_, err = client.GetIssue(context.Background(), gitcode.IssueRequest{Owner: "example-owner", Repo: "example-repo", Number: 1})
			if !tc.check(err) {
				t.Fatalf("expected typed error for status %d, got %T %v", tc.status, err, err)
			}
		})
	}
}
GO

go test ./tests/design_package/009-internal-provider-live-task-1-add-live-gitcode-rest-api-adapter-package -run TestTask009 -count=1 -v
go test ./internal/provider/live/... -count=1 -v
go test ./internal/gitcode/... -run 'TestContract|TestWriteIdempotency|TestWriteNegativeScenariosDoNotConfirm|TestConfirmedWriteOperations|TestReadRetry' -count=1 -v
go build ./internal/provider/live/...
go vet ./internal/provider/live/...
go test ./internal/provider/live/... ./internal/gitcode/... ./internal/provider/... ./internal/service/... ./internal/cache/... ./cmd/gitcode-mcp/... ./internal/index/... ./internal/mcp/... ./internal/config/... ./internal/cli/... ./internal/testnet/... -count=1
