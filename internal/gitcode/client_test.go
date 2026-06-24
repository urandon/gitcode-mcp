package gitcode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gitcode-mcp/internal/testnet"
)

func TestScenario004ReadRouteContract(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if got := r.Header.Get("Authorization"); got != "Bearer selected-token" {
			t.Fatalf("auth header not applied: %q", got)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Fatalf("accept header not applied: %q", got)
		}
		switch r.URL.Path {
		case "/api/v5/repos/example-owner/example-repo/issues":
			fmt.Fprint(w, `[{"id":"MOCK-ISSUE-7","number":7,"title":"mock issue"}]`)
		case "/api/v5/repos/example-owner/example-repo/issues/7":
			fmt.Fprint(w, `{"id":"MOCK-ISSUE-7","number":7,"title":"mock issue","body":"mock body"}`)
		case "/api/v5/repos/example-owner/example-repo/issues/7/comments":
			fmt.Fprint(w, `[{"id":"MOCK-COMMENT-1","issue_id":"MOCK-ISSUE-7","body":"mock comment"}]`)
		case "/api/v5/repos/example-owner/example-repo/wiki":
			fmt.Fprint(w, `[{"id":"MOCK-WIKI-1","slug":"MockHome","title":"Mock Home","revision":"mock-rev-1"}]`)
		case "/api/v5/repos/example-owner/example-repo/wiki/MockHome":
			fmt.Fprint(w, `{"id":"MOCK-WIKI-1","slug":"MockHome","title":"Mock Home","body":"mock wiki","revision":"mock-rev-1"}`)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{Token: "selected-token"})
	issues, err := client.ListIssues(context.Background(), IssueListRequest{Owner: "example-owner", Repo: "example-repo"})
	if err != nil || len(issues.Items) != 1 || issues.Items[0].ID != "MOCK-ISSUE-7" {
		t.Fatalf("unexpected issues page=%+v err=%v", issues, err)
	}
	issue, err := client.GetIssue(context.Background(), IssueRequest{Owner: "example-owner", Repo: "example-repo", Number: 7})
	if err != nil || issue.ID != "MOCK-ISSUE-7" {
		t.Fatalf("unexpected issue=%+v err=%v", issue, err)
	}
	comments, err := client.ListIssueComments(context.Background(), IssueRequest{Owner: "example-owner", Repo: "example-repo", Number: 7})
	if err != nil || len(comments.Items) != 1 || comments.Items[0].ID != "MOCK-COMMENT-1" || comments.Items[0].IssueID != "MOCK-ISSUE-7" {
		t.Fatalf("unexpected comments=%+v err=%v", comments, err)
	}
	wikis, err := client.ListWikiPages(context.Background(), WikiListRequest{Owner: "example-owner", Repo: "example-repo"})
	if err != nil || len(wikis.Items) != 1 || wikis.Items[0].ID != "MOCK-WIKI-1" || wikis.Items[0].Slug != "MockHome" {
		t.Fatalf("unexpected wikis=%+v err=%v", wikis, err)
	}
	wiki, err := client.GetWikiPage(context.Background(), WikiPageRequest{Owner: "example-owner", Repo: "example-repo", Slug: "MockHome"})
	if err != nil || wiki.ID != "MOCK-WIKI-1" || wiki.Slug != "MockHome" {
		t.Fatalf("unexpected wiki=%+v err=%v", wiki, err)
	}
	joined := strings.Join(paths, " ")
	if strings.Contains(joined, "ISSUE-42") || strings.Contains(joined, "WIKI-HOME") {
		t.Fatalf("fixture identifiers leaked into live route paths: %s", joined)
	}
}

func TestContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("auth header not applied")
		}
		path := "../../fixtures" + r.URL.Path + ".json"
		if r.URL.Path == "/api/v5/repos/example-owner/example-repo/wiki" {
			path = "../../fixtures/api/v5/repos/example-owner/example-repo/wiki/pages.json"
		}
		http.ServeFile(w, r, path)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{Token: "test-token"})

	issues, err := client.ListIssues(context.Background(), IssueListRequest{Owner: "example-owner", Repo: "example-repo"})
	if err != nil {
		t.Fatalf("ListIssues returned error: %v", err)
	}
	if len(issues.Items) != 2 || issues.Items[1].ID != "ISSUE-42" || issues.Items[1].Title != "Cache first adapter" || issues.Items[1].Status != "open" || issues.Items[1].State != "open" {
		t.Fatalf("unexpected issue list: %+v", issues.Items)
	}
	if len(issues.Items[1].Labels) != 2 || issues.Items[1].Labels[0] != "adapter" || issues.Items[1].CreatedAt.IsZero() || issues.Items[1].UpdatedAt.IsZero() {
		t.Fatalf("unexpected issue list fields: %+v", issues.Items[1])
	}

	issue, err := client.GetIssue(context.Background(), IssueRequest{Owner: "example-owner", Repo: "example-repo", Number: 42})
	if err != nil {
		t.Fatalf("GetIssue returned error: %v", err)
	}
	if issue.ID != "ISSUE-42" || issue.Title != "Cache first adapter" || !strings.Contains(issue.Body, "Structured body") || issue.Status != "open" || issue.State != "open" {
		t.Fatalf("unexpected issue: %+v", issue)
	}
	if len(issue.Labels) != 2 || issue.Labels[0] != "adapter" || issue.CreatedAt.IsZero() || issue.UpdatedAt.IsZero() {
		t.Fatalf("unexpected issue fields: %+v", issue)
	}

	comments, err := client.ListIssueComments(context.Background(), IssueRequest{Owner: "example-owner", Repo: "example-repo", Number: 42})
	if err != nil {
		t.Fatalf("ListIssueComments returned error: %v", err)
	}
	if len(comments.Items) != 1 || comments.Items[0].ID != "COMMENT-1" || comments.Items[0].IssueID != "ISSUE-42" || comments.Items[0].Author != "example-owner" || comments.Items[0].CreatedAt.IsZero() {
		t.Fatalf("unexpected comments: %+v", comments.Items)
	}

	wikiPages, err := client.ListWikiPages(context.Background(), WikiListRequest{Owner: "example-owner", Repo: "example-repo"})
	if err != nil {
		t.Fatalf("ListWikiPages returned error: %v", err)
	}
	if len(wikiPages.Items) != 2 || wikiPages.Items[0].ID != "WIKI-HOME" || wikiPages.Items[0].Slug != "Home" || wikiPages.Items[0].Revision != "rev-home-1" || wikiPages.Items[0].UpdatedAt.IsZero() {
		t.Fatalf("unexpected wiki pages: %+v", wikiPages.Items)
	}

	wikiPage, err := client.GetWikiPage(context.Background(), WikiPageRequest{Owner: "example-owner", Repo: "example-repo", Slug: "Home"})
	if err != nil {
		t.Fatalf("GetWikiPage returned error: %v", err)
	}
	if wikiPage.ID != "WIKI-HOME" || wikiPage.Title != "Example Project Home" || !strings.Contains(wikiPage.Body, "api.example.com") || wikiPage.CreatedAt.IsZero() || wikiPage.UpdatedAt.IsZero() {
		t.Fatalf("unexpected wiki page: %+v", wikiPage)
	}
}

func TestScenario004SelectedBaseURLOnly(t *testing.T) {
	var selectedHits atomic.Int32
	selected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		selectedHits.Add(1)
		if r.URL.Path != "/api/v5/repos/example-owner/example-repo/issues" {
			t.Fatalf("unexpected selected path %s", r.URL.Path)
		}
		fmt.Fprint(w, `[{"id":"MOCK-SELECTED-1","number":1,"title":"selected"}]`)
	}))
	defer selected.Close()
	var fallbackHits atomic.Int32
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackHits.Add(1)
		t.Fatalf("fallback endpoint was used: %s", r.URL.Path)
	}))
	defer fallback.Close()
	_ = fallback.URL
	client := newTestClient(t, selected.URL, Config{Token: "selected-token"})
	page, err := client.ListIssues(context.Background(), IssueListRequest{Owner: "example-owner", Repo: "example-repo"})
	if err != nil {
		t.Fatalf("ListIssues returned error: %v", err)
	}
	if selectedHits.Load() == 0 || fallbackHits.Load() != 0 || len(page.Items) != 1 || page.Items[0].ID != "MOCK-SELECTED-1" {
		t.Fatalf("unexpected routing selected=%d fallback=%d page=%+v", selectedHits.Load(), fallbackHits.Load(), page)
	}
}

func TestAttachmentContract(t *testing.T) {
	var oversized bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v5/repos/example-owner/example-repo/issues/42/attachments":
			fmt.Fprint(w, `[{"id":"ATT-1","name":"evidence.txt","content_type":"text/plain","size":8,"checksum":"sha256:abc"}]`)
		case "/api/v5/repos/example-owner/example-repo/issues/42/attachments/ATT-1":
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("X-Checksum-Sha256", "sha256:abc")
			if oversized {
				fmt.Fprint(w, "01234567890123456789")
				return
			}
			fmt.Fprint(w, "evidence")
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{MaxResponseSize: 256})
	page, err := client.ListIssueAttachments(context.Background(), IssueRequest{Owner: "example-owner", Repo: "example-repo", Number: 42})
	if err != nil {
		t.Fatalf("ListIssueAttachments returned error: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "ATT-1" || page.Items[0].Name != "evidence.txt" {
		t.Fatalf("unexpected attachment metadata: %+v", page.Items)
	}
	body, err := client.GetAttachment(context.Background(), AttachmentRequest{Owner: "example-owner", Repo: "example-repo", IssueNumber: 42, AttachmentID: "ATT-1", Name: "evidence.txt"})
	if err != nil {
		t.Fatalf("GetAttachment returned error: %v", err)
	}
	if string(body.Body) != "evidence" || body.ContentType != "text/plain" || body.Checksum != "sha256:abc" {
		t.Fatalf("unexpected attachment body: %+v", body)
	}
	oversized = true
	smallClient := newTestClient(t, server.URL, Config{MaxResponseSize: 12})
	_, err = smallClient.GetAttachment(context.Background(), AttachmentRequest{Owner: "example-owner", Repo: "example-repo", IssueNumber: 42, AttachmentID: "ATT-1"})
	var tooLarge ErrPayloadTooLarge
	if !errors.As(err, &tooLarge) {
		t.Fatalf("expected ErrPayloadTooLarge, got %T %v", err, err)
	}
}

func TestReadRetry(t *testing.T) {
	var issueAttempts atomic.Int32
	var listAttempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v5/repos/example-owner/example-repo/issues/42":
			if issueAttempts.Add(1) == 1 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusTooManyRequests)
				fmt.Fprint(w, `{"message":"slow down"}`)
				return
			}
			fmt.Fprint(w, `{"id":"ISSUE-42","number":42,"title":"retried"}`)
		case "/api/v5/repos/example-owner/example-repo/issues":
			listAttempts.Add(1)
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"message":"rate limited"}`)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{MaxRetries: 1})
	issue, err := client.GetIssue(context.Background(), IssueRequest{Owner: "example-owner", Repo: "example-repo", Number: 42})
	if err != nil {
		t.Fatalf("retrying GetIssue returned error: %v", err)
	}
	if issue.Title != "retried" || issueAttempts.Load() != 2 {
		t.Fatalf("retry did not produce expected issue")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	page, err := client.ListIssues(ctx, IssueListRequest{Owner: "example-owner", Repo: "example-repo"})
	if len(page.Items) != 0 {
		t.Fatalf("expected no partial records, got %+v", page.Items)
	}
	var limited ErrRateLimited
	var unavailable ErrNetworkUnavailable
	if !errors.As(err, &limited) && !errors.As(err, &unavailable) {
		t.Fatalf("expected ErrRateLimited or context bounded ErrNetworkUnavailable, got %T %v", err, err)
	}
}

func TestTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		fmt.Fprint(w, `{}`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	_, err := client.GetIssue(ctx, IssueRequest{Owner: "example-owner", Repo: "example-repo", Number: 42})
	var unavailable ErrNetworkUnavailable
	if !errors.As(err, &unavailable) {
		t.Fatalf("expected ErrNetworkUnavailable, got %T %v", err, err)
	}
	if !strings.Contains(unavailable.Endpoint, "/issues/42") || !strings.Contains(unavailable.Error(), "retry") {
		t.Fatalf("missing endpoint or retry guidance: %+v", unavailable)
	}
}

func TestScenario004AuthAfterRequest(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var reads atomic.Int32
			var writes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					writes.Add(1)
				} else {
					reads.Add(1)
				}
				w.WriteHeader(status)
				fmt.Fprint(w, `{"message":"auth failed"}`)
			}))
			defer server.Close()
			client := newTestClient(t, server.URL, Config{Token: "invalid-token"})
			_, readErr := client.ListIssues(context.Background(), IssueListRequest{Owner: "example-owner", Repo: "example-repo"})
			if reads.Load() == 0 {
				t.Fatalf("read auth error occurred before HTTP request")
			}
			_, writeErr := client.CreateIssue(context.Background(), CreateIssueRequest{Owner: "example-owner", Repo: "example-repo", Title: "created"}, WriteOptions{IdempotencyKey: "key"})
			if writes.Load() == 0 {
				t.Fatalf("write auth error occurred before HTTP request")
			}
			for _, err := range []error{readErr, writeErr} {
				if status == http.StatusUnauthorized {
					var target ErrAuthExpired
					assertAs(t, err, &target)
				} else {
					var target ErrForbidden
					assertAs(t, err, &target)
				}
			}
		})
	}
}

func TestFailureModes(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		check  func(*testing.T, error)
	}{
		{"auth", 401, `{"message":"expired"}`, func(t *testing.T, err error) { var target ErrAuthExpired; assertAs(t, err, &target) }},
		{"forbidden", 403, `{"message":"permission denied"}`, func(t *testing.T, err error) {
			var target ErrForbidden
			assertAs(t, err, &target)
			if strings.Contains(target.Recovery, "retry") {
				t.Fatalf("forbidden recovery suggests retry: %s", target.Recovery)
			}
		}},
		{"not-found", 404, `{"message":"missing"}`, func(t *testing.T, err error) { var target ErrNotFound; assertAs(t, err, &target) }},
		{"conflict", 409, `{"message":"conflict","remote":"value"}`, func(t *testing.T, err error) {
			var target ErrConflict
			assertAs(t, err, &target)
			if len(target.RemotePayload) == 0 {
				t.Fatalf("missing remote payload")
			}
		}},
		{"server-error", 500, `{"message":"down"}`, func(t *testing.T, err error) { var target ErrNetworkUnavailable; assertAs(t, err, &target) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			}))
			defer server.Close()
			client := newTestClient(t, server.URL, Config{})
			_, err := client.GetIssue(context.Background(), IssueRequest{Owner: "example-owner", Repo: "example-repo", Number: 42})
			tt.check(t, err)
		})
	}
	t.Run("remote-not-found", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"gone"}`)
		}))
		defer server.Close()
		client := newTestClient(t, server.URL, Config{})
		_, err := client.GetIssue(context.Background(), IssueRequest{Owner: "example-owner", Repo: "example-repo", Number: 42, KnownRemoteAlias: true, RemoteAlias: "gitcode:42"})
		var target ErrRemoteNotFound
		assertAs(t, err, &target)
	})
	t.Run("invalid-retry-after", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "not-a-date")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"message":"rate limited"}`)
		}))
		defer server.Close()
		client := newTestClient(t, server.URL, Config{})
		_, err := client.GetIssue(context.Background(), IssueRequest{Owner: "example-owner", Repo: "example-repo", Number: 42})
		var target ErrRateLimited
		assertAs(t, err, &target)
		if target.RawRetryAfter != "not-a-date" {
			t.Fatalf("raw retry-after not preserved")
		}
	})
	t.Run("malformed-json", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"id":`) }))
		defer server.Close()
		client := newTestClient(t, server.URL, Config{})
		_, err := client.GetIssue(context.Background(), IssueRequest{Owner: "example-owner", Repo: "example-repo", Number: 42})
		var target ErrPartialResponse
		assertAs(t, err, &target)
	})
	t.Run("max-size", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"id":"0123456789"}`) }))
		defer server.Close()
		client := newTestClient(t, server.URL, Config{MaxResponseSize: 5})
		_, err := client.GetIssue(context.Background(), IssueRequest{Owner: "example-owner", Repo: "example-repo", Number: 42})
		var target ErrPayloadTooLarge
		assertAs(t, err, &target)
	})
}

func TestIntegrationLiveGitCodeGate(t *testing.T) {
	testnet.RequireLiveIntegration(t)
	token := testnet.LiveToken()
	client, err := NewHTTPClient(Config{Token: token, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = client.Search(ctx, SearchRequest{Query: "cache-first", Owner: "example-owner", Repo: "example-repo"})
	if err != nil {
		t.Fatalf("live Search returned error: %v", err)
	}
}

func TestEndpointsTemplate(t *testing.T) {
	if got := getIssueEndpoint("example-owner", "example-repo", 42); got != "/api/v5/repos/example-owner/example-repo/issues/42" {
		t.Fatalf("unexpected issue endpoint %s", got)
	}
	if got := getWikiPageEndpoint("example owner", "repo/name", "Release Notes"); got != "/api/v5/repos/example%20owner/repo%2Fname/wiki/Release%20Notes" {
		t.Fatalf("unexpected escaped wiki endpoint %s", got)
	}
	if got := listIssueCommentsEndpoint("example-owner", "example-repo", 42); got != "/api/v5/repos/example-owner/example-repo/issues/42/comments" {
		t.Fatalf("unexpected comments endpoint %s", got)
	}
	if got := searchIssuesEndpoint(); got != "/api/v5/search" {
		t.Fatalf("unexpected search endpoint %s", got)
	}
}

func TestAttachmentEndpointsTemplate(t *testing.T) {
	if got := issueAttachmentsEndpoint("example-owner", "example-repo", 42); got != "/api/v5/repos/example-owner/example-repo/issues/42/attachments" {
		t.Fatalf("unexpected attachment list endpoint %s", got)
	}
	if got := attachmentEndpoint("example owner", "example/repo", 42, "ATT 1/2"); got != "/api/v5/repos/example%20owner/example%2Frepo/issues/42/attachments/ATT%201%2F2" {
		t.Fatalf("unexpected attachment endpoint %s", got)
	}
}

func TestWriteEndpointsTemplate(t *testing.T) {
	tests := map[string]string{
		"create issue": createIssueEndpoint("example-owner", "example-repo"),
		"update issue": updateIssueEndpoint("example-owner", "example-repo", 42),
		"comment":      createIssueCommentEndpoint("example-owner", "example-repo", 42),
		"create wiki":  createWikiPageEndpoint("example-owner", "example-repo"),
		"update wiki":  updateWikiPageEndpoint("example-owner", "example-repo", "Home"),
		"add label":    addLabelEndpoint("example-owner", "example-repo", 42),
		"remove label": removeLabelEndpoint("example-owner", "example-repo", 42, "needs triage"),
	}
	expected := map[string]string{
		"create issue": "/api/v5/repos/example-owner/example-repo/issues",
		"update issue": "/api/v5/repos/example-owner/example-repo/issues/42",
		"comment":      "/api/v5/repos/example-owner/example-repo/issues/42/comments",
		"create wiki":  "/api/v5/repos/example-owner/example-repo/wiki",
		"update wiki":  "/api/v5/repos/example-owner/example-repo/wiki/Home",
		"add label":    "/api/v5/repos/example-owner/example-repo/issues/42/labels",
		"remove label": "/api/v5/repos/example-owner/example-repo/issues/42/labels/needs%20triage",
	}
	for name, got := range tests {
		if got != expected[name] {
			t.Fatalf("%s endpoint: got %s want %s", name, got, expected[name])
		}
	}
}

func TestScenario004CreateIssueContract(t *testing.T) {
	var sawAuth string
	var sawAccept string
	var sawKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v5/repos/example-owner/example-repo/issues" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		sawAuth = r.Header.Get("Authorization")
		sawAccept = r.Header.Get("Accept")
		sawKey = r.Header.Get("Idempotency-Key")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"id":"MOCK-CREATED-100","number":100,"title":"created","body":"safe body"}`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{Token: "resolved-token"})
	result, err := client.CreateIssue(context.Background(), CreateIssueRequest{Owner: "example-owner", Repo: "example-repo", Title: "created", Body: "safe body"}, WriteOptions{IdempotencyNonce: "nonce-004"})
	if err != nil {
		t.Fatalf("CreateIssue returned error: %v", err)
	}
	if sawAuth != "Bearer resolved-token" || sawAccept != "application/json" || sawKey == "" {
		t.Fatalf("missing live request headers auth=%q accept=%q key=%q", sawAuth, sawAccept, sawKey)
	}
	if !result.Confirmed || result.Operation != "CreateIssue" || result.Target != "example-owner/example-repo" || result.RemoteID != "MOCK-CREATED-100" || result.RemoteNumber != 100 || result.IdempotencyKey != sawKey || result.ResponseHash == "" || result.ProviderPayloadFingerprint == "" || result.ConfirmedAt.IsZero() {
		t.Fatalf("incomplete create confirmation: %+v", result)
	}
	if strings.Contains(strings.ToLower(result.Operation+result.Target+result.RemoteID), "fixture client is read-only") {
		t.Fatalf("fixture read-only marker leaked: %+v", result)
	}
}

func TestWriteIdempotency(t *testing.T) {
	t.Run("sends idempotency key and JSON payload", func(t *testing.T) {
		var sawPayload bool
		var sawKey string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/api/v5/repos/example-owner/example-repo/issues" {
				t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
			}
			sawKey = r.Header.Get("Idempotency-Key")
			var payload CreateIssueRequest
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			sawPayload = payload.Title == "idempotent write" && payload.Body == "safe body"
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"ISSUE-42","number":42,"title":"idempotent write","body":"safe body"}`)
		}))
		defer server.Close()
		client := newTestClient(t, server.URL, Config{})
		result, err := client.CreateIssue(context.Background(), CreateIssueRequest{Owner: "example-owner", Repo: "example-repo", Title: "idempotent write", Body: "safe body"}, WriteOptions{IdempotencyNonce: "nonce-1"})
		if err != nil {
			t.Fatalf("CreateIssue returned error: %v", err)
		}
		if sawKey == "" || result.IdempotencyKey != sawKey || len(sawKey) != defaultIdempotencyKeyLength {
			t.Fatalf("unexpected idempotency key request=%q result=%q", sawKey, result.IdempotencyKey)
		}
		if !sawPayload || result.Record.ID != "ISSUE-42" || !result.Confirmed || result.Operation != "CreateIssue" || result.Target != "example-owner/example-repo" || result.ProviderStatus != "201" || result.RemoteID != "ISSUE-42" || result.RemoteNumber != 42 || result.ResponseHash == "" || result.ConfirmedAt.IsZero() {
			t.Fatalf("unexpected write result payload=%v result=%+v", sawPayload, result)
		}
	})

	t.Run("conflict returns local and remote payloads", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `{"message":"conflict","remote":"existing"}`)
		}))
		defer server.Close()
		client := newTestClient(t, server.URL, Config{})
		_, err := client.CreateIssue(context.Background(), CreateIssueRequest{Owner: "example-owner", Repo: "example-repo", Title: "local title"}, WriteOptions{IdempotencyKey: "replay-key"})
		var conflict ErrConflict
		assertAs(t, err, &conflict)
		if !strings.Contains(string(conflict.LocalPayload), "local title") || !strings.Contains(string(conflict.RemotePayload), "existing") || strings.Contains(string(conflict.RemotePayload), "example-owner") {
			t.Fatalf("conflict payloads missing local or remote evidence or leaked sensitive data: %+v", conflict)
		}
	})

	t.Run("retry preserves key and replay option", func(t *testing.T) {
		var attempts atomic.Int32
		var firstKey string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get("Idempotency-Key")
			if key != "fixed-replay-key" {
				t.Fatalf("unexpected replay key %q", key)
			}
			if attempts.Add(1) == 1 {
				firstKey = key
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusTooManyRequests)
				fmt.Fprint(w, `{"message":"retry"}`)
				return
			}
			if key != firstKey {
				t.Fatalf("key changed across retry: %q then %q", firstKey, key)
			}
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"ISSUE-99","number":99,"title":"created"}`)
		}))
		defer server.Close()
		client := newTestClient(t, server.URL, Config{MaxRetries: 1})
		result, err := client.CreateIssue(context.Background(), CreateIssueRequest{Owner: "example-owner", Repo: "example-repo", Title: "created"}, WriteOptions{IdempotencyKey: "fixed-replay-key"})
		if err != nil {
			t.Fatalf("CreateIssue retry returned error: %v", err)
		}
		if result.Record.ID != "ISSUE-99" || !result.Confirmed || result.IdempotencyKey != "fixed-replay-key" || attempts.Load() != 2 {
			t.Fatalf("unexpected retry result: attempts=%d result=%+v", attempts.Load(), result)
		}
	})
}

func TestWriteUsesEndpointBuilders(t *testing.T) {
	seen := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[r.Method+" "+r.URL.Path] = true
		fmt.Fprint(w, `{"id":"OK","number":42,"title":"ok"}`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	if _, err := client.CreateIssue(context.Background(), CreateIssueRequest{Owner: "example-owner", Repo: "example-repo", Title: "ok"}, WriteOptions{IdempotencyKey: "k1"}); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if _, err := client.AddLabel(context.Background(), LabelRequest{Owner: "example-owner", Repo: "example-repo", Number: 42, Label: "triage"}, WriteOptions{IdempotencyKey: "k2"}); err != nil {
		t.Fatalf("AddLabel: %v", err)
	}
	if !seen["POST "+createIssueEndpoint("example-owner", "example-repo")] || !seen["POST "+addLabelEndpoint("example-owner", "example-repo", 42)] {
		t.Fatalf("write methods did not use endpoint builders: %+v", seen)
	}
}

func TestConfirmedWriteOperations(t *testing.T) {
	tests := []struct {
		name      string
		method    string
		path      string
		body      string
		invoke    func(*HTTPClient) (WriteResult[any], error)
		assertion func(t *testing.T, result WriteResult[any])
	}{
		{
			name:   "write-confirm-create-issue",
			method: http.MethodPost,
			path:   createIssueEndpoint("example-owner", "example-repo"),
			body:   `{"id":"ISSUE-101","number":101,"title":"created"}`,
			invoke: func(client *HTTPClient) (WriteResult[any], error) {
				result, err := client.CreateIssue(context.Background(), CreateIssueRequest{Owner: "example-owner", Repo: "example-repo", Title: "created"}, WriteOptions{IdempotencyKey: "key-create-issue"})
				return anyWriteResult(result), err
			},
			assertion: func(t *testing.T, result WriteResult[any]) {
				if result.RemoteID != "ISSUE-101" || result.RemoteNumber != 101 {
					t.Fatalf("missing issue identity: %+v", result)
				}
			},
		},
		{
			name:   "write-confirm-update-issue",
			method: http.MethodPatch,
			path:   updateIssueEndpoint("example-owner", "example-repo", 42),
			body:   `{"id":"ISSUE-42","number":42,"title":"updated"}`,
			invoke: func(client *HTTPClient) (WriteResult[any], error) {
				result, err := client.UpdateIssue(context.Background(), UpdateIssueRequest{Owner: "example-owner", Repo: "example-repo", Number: 42, Title: "updated"}, WriteOptions{IdempotencyKey: "key-update-issue"})
				return anyWriteResult(result), err
			},
			assertion: func(t *testing.T, result WriteResult[any]) {
				if result.RemoteID != "ISSUE-42" || result.RemoteNumber != 42 {
					t.Fatalf("missing issue identity: %+v", result)
				}
			},
		},
		{
			name:   "write-confirm-create-comment",
			method: http.MethodPost,
			path:   createIssueCommentEndpoint("example-owner", "example-repo", 42),
			body:   `{"id":"COMMENT-1","issue_id":"ISSUE-42","body":"comment"}`,
			invoke: func(client *HTTPClient) (WriteResult[any], error) {
				result, err := client.CreateIssueComment(context.Background(), CreateIssueCommentRequest{Owner: "example-owner", Repo: "example-repo", Number: 42, Body: "comment"}, WriteOptions{IdempotencyKey: "key-create-comment"})
				return anyWriteResult(result), err
			},
			assertion: func(t *testing.T, result WriteResult[any]) {
				if result.RemoteID != "COMMENT-1" || result.ParentIssueNumber != 42 || result.ParentIssueID != "ISSUE-42" {
					t.Fatalf("missing comment identity: %+v", result)
				}
			},
		},
		{
			name:   "write-confirm-create-wiki",
			method: http.MethodPost,
			path:   createWikiPageEndpoint("example-owner", "example-repo"),
			body:   `{"id":"WIKI-1","slug":"Home","title":"Home","revision":"rev1"}`,
			invoke: func(client *HTTPClient) (WriteResult[any], error) {
				result, err := client.CreateWikiPage(context.Background(), CreateWikiPageRequest{Owner: "example-owner", Repo: "example-repo", Title: "Home", Body: "body"}, WriteOptions{IdempotencyKey: "key-create-wiki"})
				return anyWriteResult(result), err
			},
			assertion: func(t *testing.T, result WriteResult[any]) {
				if result.RemoteID != "WIKI-1" || result.RemoteSlug != "Home" || result.RemoteRevision != "rev1" {
					t.Fatalf("missing wiki identity: %+v", result)
				}
			},
		},
		{
			name:   "write-confirm-update-wiki",
			method: http.MethodPut,
			path:   updateWikiPageEndpoint("example-owner", "example-repo", "Home"),
			body:   `{"id":"WIKI-1","slug":"Home","title":"Home","revision":"rev2"}`,
			invoke: func(client *HTTPClient) (WriteResult[any], error) {
				result, err := client.UpdateWikiPage(context.Background(), UpdateWikiPageRequest{Owner: "example-owner", Repo: "example-repo", Slug: "Home", Body: "body"}, WriteOptions{IdempotencyKey: "key-update-wiki"})
				return anyWriteResult(result), err
			},
			assertion: func(t *testing.T, result WriteResult[any]) {
				if result.RemoteID != "WIKI-1" || result.RemoteSlug != "Home" || result.RemoteRevision != "rev2" {
					t.Fatalf("missing wiki identity: %+v", result)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != tt.method || r.URL.Path != tt.path {
					t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("Idempotency-Key") == "" {
					t.Fatalf("missing idempotency key")
				}
				w.WriteHeader(http.StatusCreated)
				fmt.Fprint(w, tt.body)
			}))
			defer server.Close()
			result, err := tt.invoke(newTestClient(t, server.URL, Config{}))
			if err != nil {
				t.Fatalf("write returned error: %v", err)
			}
			if !result.Confirmed || result.Operation == "" || result.Target == "" || result.ProviderStatus != "201" || result.IdempotencyKey == "" || result.ResponseHash == "" || result.ConfirmedAt.IsZero() || result.ProviderPayloadFingerprint == "" {
				t.Fatalf("missing confirmation metadata: %+v", result)
			}
			tt.assertion(t, result)
		})
	}
}

func anyWriteResult[T any](result WriteResult[T]) WriteResult[any] {
	return WriteResult[any]{Record: result.Record, Confirmed: result.Confirmed, Operation: result.Operation, Target: result.Target, ProviderStatus: result.ProviderStatus, RemoteID: result.RemoteID, RemoteNumber: result.RemoteNumber, RemoteSlug: result.RemoteSlug, RemoteRevision: result.RemoteRevision, ParentIssueNumber: result.ParentIssueNumber, ParentIssueID: result.ParentIssueID, IdempotencyKey: result.IdempotencyKey, ResponseHash: result.ResponseHash, ConfirmedAt: result.ConfirmedAt, ProviderPayloadFingerprint: result.ProviderPayloadFingerprint}
}

func TestWriteNegativeScenariosDoNotConfirm(t *testing.T) {
	t.Run("write-validation-failed", func(t *testing.T) {
		client := newTestClient(t, "http://127.0.0.1", Config{})
		result, err := client.CreateIssue(context.Background(), CreateIssueRequest{Owner: "example-owner", Repo: "example-repo"}, WriteOptions{IdempotencyKey: "key"})
		var target ErrValidationFailed
		if !errors.As(err, &target) || result.Confirmed {
			t.Fatalf("expected validation failure without confirmation: result=%+v err=%T %v", result, err, err)
		}
	})

	tests := []struct {
		name   string
		status int
		body   string
		check  func(error) bool
	}{
		{"write-conflict-redacted", http.StatusConflict, `{"message":"conflict","owner":"example-owner","remote":"existing"}`, func(err error) bool {
			var target ErrConflict
			return errors.As(err, &target) && strings.Contains(string(target.RemotePayload), "existing") && !strings.Contains(string(target.RemotePayload), "example-owner")
		}},
		{"write-auth-expired", http.StatusUnauthorized, `{"message":"expired"}`, func(err error) bool { var target ErrAuthExpired; return errors.As(err, &target) }},
		{"write-forbidden", http.StatusForbidden, `{"message":"denied"}`, func(err error) bool { var target ErrForbidden; return errors.As(err, &target) }},
		{"write-rate-limited", http.StatusTooManyRequests, `{"message":"slow"}`, func(err error) bool { var target ErrRateLimited; return errors.As(err, &target) }},
		{"write-network-unavailable", http.StatusInternalServerError, `{"message":"down"}`, func(err error) bool { var target ErrNetworkUnavailable; return errors.As(err, &target) }},
		{"write-malformed-success", http.StatusCreated, `{"id":"ISSUE-1","number":`, func(err error) bool { var target ErrPartialResponse; return errors.As(err, &target) }},
		{"write-malformed-minima", http.StatusCreated, `{"id":"ISSUE-1","number":0}`, func(err error) bool { var target ErrValidationFailed; return errors.As(err, &target) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.status == http.StatusTooManyRequests {
					w.Header().Set("Retry-After", "0")
				}
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			}))
			defer server.Close()
			client := newTestClient(t, server.URL, Config{})
			result, err := client.CreateIssue(context.Background(), CreateIssueRequest{Owner: "example-owner", Repo: "example-repo", Title: "created"}, WriteOptions{IdempotencyKey: "key"})
			if !tt.check(err) || result.Confirmed || result.IdempotencyKey != "" || result.ResponseHash != "" || !result.ConfirmedAt.IsZero() {
				t.Fatalf("expected typed error without confirmation: result=%+v err=%T %v", result, err, err)
			}
		})
	}
}

func TestScenario004ReadValidation(t *testing.T) {
	client := newTestClient(t, "http://127.0.0.1", Config{})
	tests := []struct {
		name string
		err  error
	}{
		{name: "list-issues-owner", err: func() error {
			_, err := client.ListIssues(context.Background(), IssueListRequest{Repo: "example-repo"})
			return err
		}()},
		{name: "get-issue-number", err: func() error {
			_, err := client.GetIssue(context.Background(), IssueRequest{Owner: "example-owner", Repo: "example-repo"})
			return err
		}()},
		{name: "comments-number", err: func() error {
			_, err := client.ListIssueComments(context.Background(), IssueRequest{Owner: "example-owner", Repo: "example-repo"})
			return err
		}()},
		{name: "list-wikis-repo", err: func() error {
			_, err := client.ListWikiPages(context.Background(), WikiListRequest{Owner: "example-owner"})
			return err
		}()},
		{name: "get-wiki-slug", err: func() error {
			_, err := client.GetWikiPage(context.Background(), WikiPageRequest{Owner: "example-owner", Repo: "example-repo"})
			return err
		}()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var target ErrValidationFailed
			assertAs(t, tt.err, &target)
		})
	}
}

func TestPaginationSwappable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/repos/example-owner/example-repo/issues" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		switch {
		case r.URL.Query().Get("page") == "1":
			w.Header().Set("X-Next-Page", "2")
			fmt.Fprint(w, `[{"id":"ISSUE-1","number":1,"title":"first"}]`)
		case r.URL.Query().Get("page") == "2":
			fmt.Fprint(w, `[{"id":"ISSUE-2","number":2,"title":"second"}]`)
		case r.URL.Query().Get("cursor") == "":
			w.Header().Set("X-Next-Cursor", "next")
			fmt.Fprint(w, `[{"id":"ISSUE-1","number":1,"title":"first"}]`)
		case r.URL.Query().Get("cursor") == "next":
			fmt.Fprint(w, `[{"id":"ISSUE-2","number":2,"title":"second"}]`)
		default:
			t.Fatalf("unexpected query %s", r.URL.RawQuery)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, Config{Pagination: PaginationConfig{PerPage: 1}})
	page, err := client.ListIssues(context.Background(), IssueListRequest{Owner: "example-owner", Repo: "example-repo"})
	if err != nil {
		t.Fatalf("ListIssues page pagination returned error: %v", err)
	}
	if len(page.Items) != 2 || page.Items[0].ID != "ISSUE-1" || page.Items[1].ID != "ISSUE-2" {
		t.Fatalf("unexpected page pagination items: %+v", page.Items)
	}

	cursorClient := newTestClient(t, server.URL, Config{Pagination: PaginationConfig{Strategy: testCursorStrategy{}}})
	cursorPage, err := cursorClient.ListIssues(context.Background(), IssueListRequest{Owner: "example-owner", Repo: "example-repo"})
	if err != nil {
		t.Fatalf("ListIssues cursor pagination returned error: %v", err)
	}
	if len(cursorPage.Items) != 2 || cursorPage.Items[0].ID != "ISSUE-1" || cursorPage.Items[1].ID != "ISSUE-2" {
		t.Fatalf("unexpected cursor pagination items: %+v", cursorPage.Items)
	}
}

func TestPaginationLaterPageFailureReturnsNoRecords(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "1" {
			w.Header().Set("X-Next-Page", "2")
			fmt.Fprint(w, `[{"id":"ISSUE-1","number":1,"title":"first"}]`)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"message":"down"}`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	page, err := client.ListIssues(context.Background(), IssueListRequest{Owner: "example-owner", Repo: "example-repo"})
	if len(page.Items) != 0 {
		t.Fatalf("expected no partial records, got %+v", page.Items)
	}
	var unavailable ErrNetworkUnavailable
	assertAs(t, err, &unavailable)
}

type testCursorStrategy struct{}

func (testCursorStrategy) Apply(values url.Values, state PageState) {
	if state.Cursor != "" {
		values.Set("cursor", state.Cursor)
	}
}

func (testCursorStrategy) Next(headers http.Header, currentCount int) (PageState, bool) {
	cursor := headers.Get("X-Next-Cursor")
	if cursor == "" {
		return PageState{}, false
	}
	return PageState{Cursor: cursor}, true
}

func newTestClient(t *testing.T, baseURL string, cfg Config) *HTTPClient {
	t.Helper()
	cfg.BaseURL = baseURL
	client, err := NewHTTPClient(cfg)
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}
	return client
}

func assertAs[T error](t *testing.T, err error, target *T) {
	t.Helper()
	if !errors.As(err, target) {
		t.Fatalf("expected %T, got %T %v", *target, err, err)
	}
}
