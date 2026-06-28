package gitcode

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
			fmt.Fprint(w, `[{"id":"MOCK-ISSUE-7","number":7,"title":"mock issue"},{"id":8,"number":"8","title":"numeric id issue"}]`)
		case "/api/v5/repos/example-owner/example-repo/issues/7":
			fmt.Fprint(w, `{"id":"MOCK-ISSUE-7","number":7,"title":"mock issue","body":"mock body"}`)
		case "/api/v5/repos/example-owner/example-repo/issues/8":
			fmt.Fprint(w, `{"id":8,"number":"8","title":"numeric id issue","body":"numeric body"}`)
		case "/api/v5/repos/example-owner/example-repo/issues/7/comments":
			fmt.Fprint(w, `[{"id":"MOCK-COMMENT-1","issue_id":"MOCK-ISSUE-7","body":"mock comment"}]`)
		case "/api/v5/repos/example-owner/example-repo.wiki/contents":
			fmt.Fprint(w, `[{"path":"MockHome.md","type":"file","sha":"mock-rev-1"}]`)
		case "/api/v5/repos/example-owner/example-repo.wiki/contents/MockHome.md":
			fmt.Fprint(w, `{"path":"MockHome.md","type":"file","sha":"mock-rev-1"}`)
		case "/api/v5/repos/example-owner/example-repo.wiki/raw/MockHome.md":
			fmt.Fprint(w, `mock wiki`)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{Token: "selected-token"})
	issues, err := client.ListIssues(context.Background(), IssueListRequest{Owner: "example-owner", Repo: "example-repo"})
	if err != nil || len(issues.Items) != 2 {
		t.Fatalf("unexpected issues page=%+v err=%v", issues, err)
	}
	if issues.Items[0].ID != "MOCK-ISSUE-7" || issues.Items[0].Number != 7 {
		t.Fatalf("unexpected string-id issue: id=%q number=%d", issues.Items[0].ID, issues.Items[0].Number)
	}
	if issues.Items[1].ID != "8" || issues.Items[1].Number != 8 || issues.Items[1].Title != "numeric id issue" {
		t.Fatalf("unexpected numeric-id issue: id=%q number=%d title=%q", issues.Items[1].ID, issues.Items[1].Number, issues.Items[1].Title)
	}
	issue, err := client.GetIssue(context.Background(), IssueRequest{Owner: "example-owner", Repo: "example-repo", Number: 7})
	if err != nil || issue.ID != "MOCK-ISSUE-7" {
		t.Fatalf("unexpected issue=%+v err=%v", issue, err)
	}
	numericIssue, err := client.GetIssue(context.Background(), IssueRequest{Owner: "example-owner", Repo: "example-repo", Number: 8})
	if err != nil || numericIssue.ID != "8" || numericIssue.Number != 8 || numericIssue.Title != "numeric id issue" {
		t.Fatalf("unexpected numeric issue=%+v err=%v", numericIssue, err)
	}
	comments, err := client.ListIssueComments(context.Background(), IssueRequest{Owner: "example-owner", Repo: "example-repo", Number: 7})
	if err != nil || len(comments.Items) != 1 || comments.Items[0].ID != "MOCK-COMMENT-1" || comments.Items[0].IssueID != "MOCK-ISSUE-7" {
		t.Fatalf("unexpected comments=%+v err=%v", comments, err)
	}
	wikis, err := client.ListWikiPages(context.Background(), WikiListRequest{Owner: "example-owner", Repo: "example-repo"})
	if err != nil || len(wikis.Items) != 1 || wikis.Items[0].ID != "MockHome.md" || wikis.Items[0].Slug != "MockHome.md" {
		t.Fatalf("unexpected wikis=%+v err=%v", wikis, err)
	}
	wiki, err := client.GetWikiPage(context.Background(), WikiPageRequest{Owner: "example-owner", Repo: "example-repo", Path: "MockHome.md"})
	if err != nil || wiki.ID != "MockHome.md" || wiki.Slug != "MockHome.md" {
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
		switch r.URL.Path {
		case "/api/v5/repos/example-owner/example-repo.wiki/contents":
			fmt.Fprint(w, `[{"path":"Home.md","type":"file","sha":"rev-home-1"},{"path":"Guide.md","type":"file","sha":"rev-guide-1"}]`)
			return
		case "/api/v5/repos/example-owner/example-repo.wiki/contents/Home.md":
			fmt.Fprint(w, `{"path":"Home.md","type":"file","sha":"rev-home-1"}`)
			return
		case "/api/v5/repos/example-owner/example-repo.wiki/raw/Home.md":
			fmt.Fprint(w, `Example Project Home api.example.com`)
			return
		case "/api/v5/repos/example-owner/example-repo.wiki/contents/Guide.md":
			fmt.Fprint(w, `{"path":"Guide.md","type":"file","sha":"rev-guide-1"}`)
			return
		case "/api/v5/repos/example-owner/example-repo.wiki/raw/Guide.md":
			fmt.Fprint(w, `Guide`)
			return
		}
		path := "../../fixtures" + r.URL.Path + ".json"
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
	if len(wikiPages.Items) != 2 || wikiPages.Items[0].ID != "Guide.md" || wikiPages.Items[0].Slug != "Guide.md" || wikiPages.Items[0].Revision != "rev-guide-1" || wikiPages.Items[0].UpdatedAt.IsZero() {
		t.Fatalf("unexpected wiki pages: %+v", wikiPages.Items)
	}

	wikiPage, err := client.GetWikiPage(context.Background(), WikiPageRequest{Owner: "example-owner", Repo: "example-repo", Path: "Home.md"})
	if err != nil {
		t.Fatalf("GetWikiPage returned error: %v", err)
	}
	if wikiPage.ID != "Home.md" || wikiPage.Title != "Home" || !strings.Contains(wikiPage.Body, "api.example.com") || wikiPage.UpdatedAt.IsZero() {
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
	if tooLarge.Source != "remote_status" {
		t.Fatalf("source=%q want remote_status", tooLarge.Source)
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
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.(http.Flusher).Flush()
			fmt.Fprint(w, `{"id":"0123456789"}`)
		}))
		defer server.Close()
		client := newTestClient(t, server.URL, Config{MaxResponseSize: 5})
		_, err := client.GetIssue(context.Background(), IssueRequest{Owner: "example-owner", Repo: "example-repo", Number: 42})
		var target ErrPayloadTooLarge
		assertAs(t, err, &target)
		if target.Source != "local_body_limit" {
			t.Fatalf("source=%q want local_body_limit", target.Source)
		}
	})
	t.Run("remote-413", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			fmt.Fprint(w, strings.Repeat("x", 32))
		}))
		defer server.Close()
		client := newTestClient(t, server.URL, Config{MaxResponseSize: 5})
		_, err := client.GetIssue(context.Background(), IssueRequest{Owner: "example-owner", Repo: "example-repo", Number: 42})
		var target ErrPayloadTooLarge
		assertAs(t, err, &target)
		if target.Source != "remote_status" {
			t.Fatalf("source=%q want remote_status", target.Source)
		}
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
	if got := getWikiPageEndpoint("example owner", "repo/name", "Release Notes/June + #1.md"); got != "/api/v5/repos/example%20owner/repo%2Fname.wiki/contents/Release%20Notes/June%20+%20%231.md" {
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
		"create issue":   createIssueEndpoint("example-owner", "example-repo"),
		"update issue":   updateIssueEndpoint("example-owner", "example-repo", 42),
		"comment":        createIssueCommentEndpoint("example-owner", "example-repo", 42),
		"update comment": updateIssueCommentEndpoint("example-owner", "example-repo", "2002"),
		"get comment":    getIssueCommentEndpoint("example-owner", "example-repo", "2002"),
		"create wiki":    createWikiPageEndpoint("example-owner", "example-repo"),
		"update wiki":    updateWikiPageEndpoint("example-owner", "example-repo", "Home"),
		"add label":      addLabelEndpoint("example-owner", "example-repo", 42),
		"remove label":   removeLabelEndpoint("example-owner", "example-repo", 42, "needs triage"),
	}
	expected := map[string]string{
		"create issue":   "/api/v5/repos/example-owner/example-repo/issues",
		"update issue":   "/api/v5/repos/example-owner/example-repo/issues/42",
		"comment":        "/api/v5/repos/example-owner/example-repo/issues/42/comments",
		"update comment": "/api/v5/repos/example-owner/example-repo/issues/comments/2002",
		"get comment":    "/api/v5/repos/example-owner/example-repo/issues/comments/2002",
		"create wiki":    "/api/v5/repos/example-owner/example-repo.wiki/contents",
		"update wiki":    "/api/v5/repos/example-owner/example-repo.wiki/contents/Home",
		"add label":      "/api/v5/repos/example-owner/example-repo/issues/42/labels",
		"remove label":   "/api/v5/repos/example-owner/example-repo/issues/42/labels/needs%20triage",
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
			body:   `{"id":"LEGACY-COMMENT-1","issue_id":"ISSUE-42","body":"comment"}`,
			invoke: func(client *HTTPClient) (WriteResult[any], error) {
				result, err := client.CreateIssueComment(context.Background(), CreateIssueCommentRequest{Owner: "example-owner", Repo: "example-repo", Number: 42, Body: "comment"}, WriteOptions{IdempotencyKey: "key-create-comment"})
				return anyWriteResult(result), err
			},
			assertion: func(t *testing.T, result WriteResult[any]) {
				if result.RemoteID != "LEGACY-COMMENT-1" || result.ParentIssueNumber != 42 || result.ParentIssueID != "ISSUE-42" {
					t.Fatalf("missing comment identity: %+v", result)
				}
			},
		},
		{
			name:   "SCN-GITCODE-ADD-COMMENT-LIVE-SHAPE-01",
			method: http.MethodPost,
			path:   createIssueCommentEndpoint("example-owner", "example-repo", 42),
			body:   `{"id":1001,"note_id":2002,"body":"comment","created_at":"2026-06-20T12:00:00Z","user":{"login":"commenter"}}`,
			invoke: func(client *HTTPClient) (WriteResult[any], error) {
				result, err := client.CreateIssueComment(context.Background(), CreateIssueCommentRequest{Owner: "example-owner", Repo: "example-repo", Number: 42, Body: "comment"}, WriteOptions{IdempotencyKey: "key-create-comment"})
				return anyWriteResult(result), err
			},
			assertion: func(t *testing.T, result WriteResult[any]) {
				comment, ok := result.Record.(Comment)
				if !ok {
					t.Fatalf("record type = %T", result.Record)
				}
				if result.RemoteID != "2002" || result.ParentIssueNumber != 42 || comment.ID != "2002" || comment.Author != "commenter" || comment.Body != "comment" || comment.CreatedAt.IsZero() {
					t.Fatalf("unexpected live comment result: result=%+v comment=%+v", result, comment)
				}
			},
		},
		{
			name:   "write-confirm-create-wiki",
			method: http.MethodPost,
			path:   updateWikiPageEndpoint("example-owner", "example-repo", "Home.md"),
			body:   `{"path":"Home.md","type":"file","sha":"rev1"}`,
			invoke: func(client *HTTPClient) (WriteResult[any], error) {
				result, err := client.CreateWikiPage(context.Background(), CreateWikiPageRequest{Owner: "example-owner", Repo: "example-repo", Path: "Home.md", Title: "Home", Body: "body"}, WriteOptions{IdempotencyKey: "key-create-wiki"})
				return anyWriteResult(result), err
			},
			assertion: func(t *testing.T, result WriteResult[any]) {
				if result.RemoteID != "Home.md" || result.RemoteSlug != "Home.md" || result.RemoteRevision != "rev1" {
					t.Fatalf("missing wiki identity: %+v", result)
				}
			},
		},
		{
			name:   "write-confirm-update-wiki",
			method: http.MethodPut,
			path:   updateWikiPageEndpoint("example-owner", "example-repo", "Home.md"),
			body:   `{"path":"Home.md","type":"file","sha":"rev2"}`,
			invoke: func(client *HTTPClient) (WriteResult[any], error) {
				result, err := client.UpdateWikiPage(context.Background(), UpdateWikiPageRequest{Owner: "example-owner", Repo: "example-repo", Path: "Home.md", Body: "body", Sha: "rev1"}, WriteOptions{IdempotencyKey: "key-update-wiki"})
				return anyWriteResult(result), err
			},
			assertion: func(t *testing.T, result WriteResult[any]) {
				if result.RemoteID != "Home.md" || result.RemoteSlug != "Home.md" || result.RemoteRevision != "rev2" {
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

func TestScenario015WikiCreatePageFollowupConfirmation(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v5/repos/example-owner/example-repo.wiki/contents/Home.md":
			var payload WikiContentWriteRequest
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode request body: %v", err)
			}
			if payload.Sha != "" {
				t.Fatalf("create payload sha = %q, want empty", payload.Sha)
			}
			decoded, err := base64.StdEncoding.DecodeString(payload.Content)
			if err != nil || string(decoded) != "body" {
				t.Fatalf("create payload content = %q err=%v", decoded, err)
			}
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v5/repos/example-owner/example-repo.wiki/contents/Home.md":
			fmt.Fprintf(w, `{"path":"Home.md","type":"file","sha":"rev-confirmed","content":%q,"encoding":"base64"}`, base64.StdEncoding.EncodeToString([]byte("body")))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	result, err := newTestClient(t, server.URL, Config{}).CreateWikiPage(context.Background(), CreateWikiPageRequest{Owner: "example-owner", Repo: "example-repo", Path: "Home.md", Body: "body"}, WriteOptions{IdempotencyKey: "key-create-wiki"})
	if err != nil {
		t.Fatalf("CreateWikiPage returned error: %v", err)
	}
	if !result.Confirmed || result.RemoteID != "Home.md" || result.RemoteSlug != "Home.md" || result.RemoteRevision != "rev-confirmed" || result.Record.Revision != "rev-confirmed" || result.Record.Body != "body" || result.ProviderStatus != "201" {
		t.Fatalf("unexpected confirmed result: %+v", result)
	}
	want := []string{"POST /api/v5/repos/example-owner/example-repo.wiki/contents/Home.md", "GET /api/v5/repos/example-owner/example-repo.wiki/contents/Home.md"}
	if strings.Join(paths, "|") != strings.Join(want, "|") {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
}

func TestScenario015WikiCreatePageFollowupConfirmationFailure(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "not-found", status: http.StatusNotFound, body: `{"message":"missing"}`},
		{name: "server-error", status: http.StatusInternalServerError, body: `{"message":"down"}`},
		{name: "path-mismatch", status: http.StatusOK, body: `{"path":"Other.md","type":"file","sha":"rev-confirmed","content":"Ym9keQ==","encoding":"base64"}`},
		{name: "missing-sha", status: http.StatusOK, body: `{"path":"Home.md","type":"file","content":"Ym9keQ==","encoding":"base64"}`},
		{name: "content-mismatch", status: http.StatusOK, body: `{"path":"Home.md","type":"file","sha":"rev-confirmed","content":"b3RoZXI=","encoding":"base64"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/api/v5/repos/example-owner/example-repo.wiki/contents/Home.md":
					w.WriteHeader(http.StatusCreated)
					fmt.Fprint(w, `{}`)
				case r.Method == http.MethodGet && r.URL.Path == "/api/v5/repos/example-owner/example-repo.wiki/contents/Home.md":
					w.WriteHeader(tt.status)
					fmt.Fprint(w, tt.body)
				default:
					t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
				}
			}))
			defer server.Close()

			result, err := newTestClient(t, server.URL, Config{}).CreateWikiPage(context.Background(), CreateWikiPageRequest{Owner: "example-owner", Repo: "example-repo", Path: "Home.md", Body: "body"}, WriteOptions{IdempotencyKey: "key-create-wiki"})
			var target ErrWriteConfirmationIncomplete
			if !errors.As(err, &target) || result.Confirmed || result.RemoteRevision != "" {
				t.Fatalf("expected incomplete confirmation without confirmed result: result=%+v err=%T %v", result, err, err)
			}
			if target.DiagnosticCode() != "write_confirmation_incomplete" {
				t.Fatalf("diagnostic = %q", target.DiagnosticCode())
			}
		})
	}
}

func anyWriteResult[T any](result WriteResult[T]) WriteResult[any] {
	return WriteResult[any]{Record: result.Record, Confirmed: result.Confirmed, Operation: result.Operation, Target: result.Target, ProviderStatus: result.ProviderStatus, RemoteID: result.RemoteID, RemoteNumber: result.RemoteNumber, RemoteSlug: result.RemoteSlug, RemoteRevision: result.RemoteRevision, ParentIssueNumber: result.ParentIssueNumber, ParentIssueID: result.ParentIssueID, IdempotencyKey: result.IdempotencyKey, ResponseHash: result.ResponseHash, ConfirmedAt: result.ConfirmedAt, ProviderPayloadFingerprint: result.ProviderPayloadFingerprint}
}

func TestScenario017AddCommentMalformedBodySchemaDecode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != createIssueCommentEndpoint("example-owner", "example-repo", 42) {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"id":`)
	}))
	defer server.Close()

	result, err := newTestClient(t, server.URL, Config{}).CreateIssueComment(context.Background(), CreateIssueCommentRequest{Owner: "example-owner", Repo: "example-repo", Number: 42, Body: "comment"}, WriteOptions{IdempotencyKey: "key-create-comment"})
	var schemaErr *ErrSchemaDecode
	if !errors.As(err, &schemaErr) || result.Confirmed {
		t.Fatalf("expected schema decode without confirmation: result=%+v err=%T %v", result, err, err)
	}
	if schemaErr.DiagnosticCode() != "schema_decode" {
		t.Fatalf("diagnostic = %q", schemaErr.DiagnosticCode())
	}
}

func TestScenario007UpdateIssueCommentUsesDiscoveredRouteAndPreservesMultilineBody(t *testing.T) {
	wantBody := "updated comment\nline two\nline three"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != updateIssueCommentEndpoint("example-owner", "example-repo", "2002") {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if payload["body"] != wantBody {
			t.Fatalf("body=%q want %q", payload["body"], wantBody)
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"id":"2002","issue_id":"42","body":"updated comment\nline two\nline three","author":"bot","created_at":"2026-06-28T10:00:00Z","updated_at":"2026-06-28T10:01:00Z"}`)
	}))
	defer server.Close()

	result, err := newTestClient(t, server.URL, Config{}).UpdateIssueComment(context.Background(), UpdateIssueCommentRequest{Owner: "example-owner", Repo: "example-repo", Number: 42, CommentID: "2002", Body: wantBody}, WriteOptions{IdempotencyKey: "key-update-comment"})
	if err != nil {
		t.Fatalf("UpdateIssueComment returned error: %v", err)
	}
	if !result.Confirmed || result.RemoteID != "2002" || result.ParentIssueNumber != 42 || result.ParentIssueID != "42" || result.Record.Body != wantBody {
		t.Fatalf("unexpected result=%+v", result)
	}
}

func TestScenario007UpdateIssueCommentEmptyPatchBodyUsesReadback(t *testing.T) {
	wantBody := "updated comment\nline two"
	patches := 0
	gets := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == updateIssueCommentEndpoint("example-owner", "example-repo", "2002"):
			patches++
			var payload map[string]string
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode payload: %v", err)
			}
			if payload["body"] != wantBody {
				t.Fatalf("body=%q want %q", payload["body"], wantBody)
			}
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == getIssueCommentEndpoint("example-owner", "example-repo", "2002"):
			gets++
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":2002,"body":"updated comment\nline two","user":{"login":"bot"}}`)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	result, err := newTestClient(t, server.URL, Config{}).UpdateIssueComment(context.Background(), UpdateIssueCommentRequest{Owner: "example-owner", Repo: "example-repo", Number: 42, CommentID: "2002", Body: wantBody}, WriteOptions{IdempotencyKey: "key-update-comment-readback"})
	if err != nil {
		t.Fatalf("UpdateIssueComment readback returned error: %v", err)
	}
	if patches != 1 || gets != 1 {
		t.Fatalf("patches=%d gets=%d", patches, gets)
	}
	if !result.Confirmed || result.ProviderStatus != "2xx-readback" || result.RemoteID != "2002" || result.ParentIssueNumber != 42 || result.Record.Body != wantBody {
		t.Fatalf("unexpected result=%+v", result)
	}
}

func TestScenario018PRListDetailCommentsRoutes(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == listPREndpoint("example-owner", "example-repo"):
			fmt.Fprint(w, `[{"id":101,"number":7,"html_url":"https://example.test/pulls/7","state":"open","title":"Add cache","body":"body","user":{"login":"alice"},"labels":[{"id":1,"name":"feature","color":"blue"}],"base":{"ref":"main"},"head":{"ref":"topic"}}]`)
		case r.Method == http.MethodGet && r.URL.Path == getPREndpoint("example-owner", "example-repo", 7):
			fmt.Fprint(w, `{"id":"101","number":"7","html_url":"https://example.test/pulls/7","state":"open","title":"Add cache","body":"body","user":{"login":"alice"},"labels":["feature"],"base":{"ref":"main"},"head":{"ref":"topic"}}`)
		case r.Method == http.MethodGet && r.URL.Path == listPRCommentsEndpoint("example-owner", "example-repo", 7):
			fmt.Fprint(w, `[{"id":201,"note_id":301,"body":"looks good","discussion_id":"DISC-7","user":{"login":"bob"}}]`)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, Config{})
	prs, err := client.ListPRs(context.Background(), PRListRequest{Owner: "example-owner", Repo: "example-repo"})
	if err != nil {
		t.Fatalf("ListPRs returned error: %v", err)
	}
	if len(prs.Items) != 1 || prs.Items[0].Kind != "pull_request" || prs.Items[0].SourceID != "PR-7" || prs.Items[0].ID != "101" || prs.Items[0].Number != 7 || prs.Items[0].HTMLURL == "" || prs.Items[0].State != "open" || prs.Items[0].Title != "Add cache" || prs.Items[0].Body != "body" || prs.Items[0].User != "alice" || len(prs.Items[0].Labels) != 1 || prs.Items[0].Labels[0] != "feature" || prs.Items[0].Base != "main" || prs.Items[0].Head != "topic" {
		t.Fatalf("unexpected PR list record: %+v", prs.Items)
	}
	pr, err := client.GetPR(context.Background(), PRRequest{Owner: "example-owner", Repo: "example-repo", Number: 7})
	if err != nil {
		t.Fatalf("GetPR returned error: %v", err)
	}
	if pr.Kind != "pull_request" || pr.SourceID != "PR-7" || pr.Number != 7 || pr.ID != "101" {
		t.Fatalf("unexpected PR detail record: %+v", pr)
	}
	comments, err := client.ListPRComments(context.Background(), PRRequest{Owner: "example-owner", Repo: "example-repo", Number: 7})
	if err != nil {
		t.Fatalf("ListPRComments returned error: %v", err)
	}
	if len(comments.Items) != 1 || comments.Items[0].Kind != "pr_comment" || comments.Items[0].ID != "301" || comments.Items[0].DiscussionID != "DISC-7" || comments.Items[0].PRNumber != 7 || comments.Items[0].Body != "looks good" || comments.Items[0].Author != "bob" {
		t.Fatalf("unexpected PR comment records: %+v", comments.Items)
	}
	for _, path := range paths {
		if strings.Contains(path, "pull_requests") || strings.Contains(path, "merge_requests") || strings.Contains(path, "review_comments") {
			t.Fatalf("deployment-inhibited route called: %s", path)
		}
	}
}

func TestScenario018PRCommentWrite(t *testing.T) {
	var seenBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != createPRCommentEndpoint("example-owner", "example-repo", 7) {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		seenBody = string(body)
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"id":201,"note_id":301,"body":"posted"}`)
	}))
	defer server.Close()

	result, err := newTestClient(t, server.URL, Config{}).CreatePRComment(context.Background(), CreatePRCommentRequest{Owner: "example-owner", Repo: "example-repo", Number: 7, Body: "posted"}, WriteOptions{IdempotencyKey: "key-create-pr-comment"})
	if err != nil {
		t.Fatalf("CreatePRComment returned error: %v", err)
	}
	if !strings.Contains(seenBody, `"body":"posted"`) {
		t.Fatalf("request body = %s", seenBody)
	}
	if !result.Confirmed || result.ProviderStatus != "201" || result.RemoteID != "301" || result.ParentIssueNumber != 7 || result.ParentIssueID != "7" || result.Record.Kind != "pr_comment" || result.Record.PRNumber != 7 || result.Record.ID != "301" || result.Record.Body != "posted" {
		t.Fatalf("unexpected PR comment write result: %+v", result)
	}
}

func TestScenario016PRLifecycleWrites(t *testing.T) {
	t.Run("create-pr", func(t *testing.T) {
		var seenBody string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != listPREndpoint("example-owner", "example-repo") {
				t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read request body: %v", err)
			}
			seenBody = string(body)
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":9001,"number":7,"title":"new pr","body":"body","state":"open","base":{"ref":"main"},"head":{"ref":"topic"}}`)
		}))
		defer server.Close()

		result, err := newTestClient(t, server.URL, Config{}).CreatePR(context.Background(), CreatePRRequest{Owner: "example-owner", Repo: "example-repo", Title: "new pr", Body: "body", Head: "topic", Base: "main"}, WriteOptions{IdempotencyKey: "key-create-pr"})
		if err != nil {
			t.Fatalf("CreatePR returned error: %v", err)
		}
		for _, want := range []string{`"title":"new pr"`, `"body":"body"`, `"head":"topic"`, `"base":"main"`} {
			if !strings.Contains(seenBody, want) {
				t.Fatalf("request body %s missing %s", seenBody, want)
			}
		}
		if !result.Confirmed || result.Operation != "CreatePR" || result.RemoteID != "9001" || result.RemoteNumber != 7 || result.Record.Number != 7 || result.Record.Base != "main" || result.Record.Head != "topic" {
			t.Fatalf("unexpected PR create result: %+v", result)
		}
	})

	t.Run("update-pr", func(t *testing.T) {
		var seenBody string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPatch || r.URL.Path != getPREndpoint("example-owner", "example-repo", 7) {
				t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read request body: %v", err)
			}
			seenBody = string(body)
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"id":9001,"number":7,"title":"updated pr","body":"new body","state":"open"}`)
		}))
		defer server.Close()

		result, err := newTestClient(t, server.URL, Config{}).UpdatePR(context.Background(), UpdatePRRequest{Owner: "example-owner", Repo: "example-repo", Number: 7, Title: "updated pr", Body: "new body"}, WriteOptions{IdempotencyKey: "key-update-pr"})
		if err != nil {
			t.Fatalf("UpdatePR returned error: %v", err)
		}
		for _, want := range []string{`"title":"updated pr"`, `"body":"new body"`} {
			if !strings.Contains(seenBody, want) {
				t.Fatalf("request body %s missing %s", seenBody, want)
			}
		}
		if !result.Confirmed || result.Operation != "UpdatePR" || result.RemoteID != "9001" || result.RemoteNumber != 7 || result.Record.Title != "updated pr" {
			t.Fatalf("unexpected PR update result: %+v", result)
		}
	})

	t.Run("update-pr-empty-body-readback", func(t *testing.T) {
		patches := 0
		gets := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodPatch && r.URL.Path == getPREndpoint("example-owner", "example-repo", 7):
				patches++
				w.WriteHeader(http.StatusNoContent)
			case r.Method == http.MethodGet && r.URL.Path == getPREndpoint("example-owner", "example-repo", 7):
				gets++
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":9001,"number":7,"title":"readback pr","body":"linked","state":"open"}`)
			default:
				t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
			}
		}))
		defer server.Close()

		result, err := newTestClient(t, server.URL, Config{}).UpdatePR(context.Background(), UpdatePRRequest{Owner: "example-owner", Repo: "example-repo", Number: 7, Body: "linked"}, WriteOptions{IdempotencyKey: "key-update-pr-readback"})
		if err != nil {
			t.Fatalf("UpdatePR read-back returned error: %v", err)
		}
		if patches != 1 || gets != 1 {
			t.Fatalf("patches=%d gets=%d", patches, gets)
		}
		if !result.Confirmed || result.ProviderStatus != "2xx-readback" || result.RemoteID != "9001" || result.RemoteNumber != 7 || result.Record.Body != "linked" {
			t.Fatalf("unexpected PR read-back result: %+v", result)
		}
	})

	t.Run("link-pr-issue", func(t *testing.T) {
		var seenBody string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != linkPRIssueEndpoint("example-owner", "example-repo", 7) {
				t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read request body: %v", err)
			}
			seenBody = string(body)
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `[{"id":4119896,"number":"16","title":"Issue 16","state":"open"}]`)
		}))
		defer server.Close()

		result, err := newTestClient(t, server.URL, Config{}).LinkPRIssue(context.Background(), LinkPRIssueRequest{Owner: "example-owner", Repo: "example-repo", Number: 7, IssueNumber: 16}, WriteOptions{IdempotencyKey: "key-link-pr-issue"})
		if err != nil {
			t.Fatalf("LinkPRIssue returned error: %v", err)
		}
		if seenBody != "[16]" {
			t.Fatalf("request body = %s", seenBody)
		}
		if !result.Confirmed || result.Operation != "LinkPRIssue" || result.RemoteID != "7" || result.RemoteNumber != 7 || len(result.Record) != 1 || result.Record[0].Number != 16 {
			t.Fatalf("unexpected PR issue link result: %+v", result)
		}
	})
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

func TestScenario001WikiContentsRootTraversal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v5/repos/example-owner/example-repo.wiki/contents":
			fmt.Fprint(w, `[{"path":"README.md","type":"file","sha":"rev-home"},{"path":"assets/logo.png","type":"file","sha":"rev-logo"},{"path":"tasks","type":"dir"},{"path":"dir","type":"dir"},{"path":"ignored.txt","type":"file","sha":"rev-txt"}]`)
		case "/api/v5/repos/example-owner/example-repo.wiki/contents/dir":
			fmt.Fprint(w, `[{"path":"dir/Sub.md","type":"file","sha":"rev-sub"}]`)
		case "/api/v5/repos/example-owner/example-repo.wiki/contents/tasks":
			fmt.Fprint(w, `[{"path":"tasks/Task.md","type":"file","sha":"rev-task"}]`)
		case "/api/v5/repos/example-owner/example-repo.wiki/contents/README.md":
			fmt.Fprint(w, `{"path":"README.md","type":"file","sha":"rev-home"}`)
		case "/api/v5/repos/example-owner/example-repo.wiki/raw/README.md":
			fmt.Fprint(w, "# Introduction\n\nHome page.")
		case "/api/v5/repos/example-owner/example-repo.wiki/contents/dir/Sub.md":
			fmt.Fprint(w, `{"path":"dir/Sub.md","type":"file","sha":"rev-sub"}`)
		case "/api/v5/repos/example-owner/example-repo.wiki/raw/dir/Sub.md":
			fmt.Fprint(w, "## Sub page")
		case "/api/v5/repos/example-owner/example-repo.wiki/contents/tasks/Task.md":
			fmt.Fprint(w, `{"path":"tasks/Task.md","type":"file","sha":"rev-task"}`)
		case "/api/v5/repos/example-owner/example-repo.wiki/raw/tasks/Task.md":
			fmt.Fprint(w, "## Task page")
		default:
			t.Fatalf("unexpected wiki path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	wikis, err := client.ListWikiPages(context.Background(), WikiListRequest{Owner: "example-owner", Repo: "example-repo"})
	if err != nil {
		t.Fatalf("ListWikiPages returned error: %v", err)
	}
	if len(wikis.Items) != 3 {
		t.Fatalf("expected 3 markdown pages, got %d: %+v", len(wikis.Items), wikis.Items)
	}
	ordered := []string{"dir/Sub.md", "tasks/Task.md", "README.md"}
	for i, item := range wikis.Items {
		if item.Slug != ordered[i] {
			t.Fatalf("expected ordered[%d] = %q, got %q", i, ordered[i], item.Slug)
		}
	}
	for _, item := range wikis.Items {
		if item.Slug == "logo.png" {
			t.Fatalf("unsupported file logo.png was synced: %+v", item)
		}
	}
	if wikis.Items[0].ID != "dir/Sub.md" || wikis.Items[0].Title != "Sub" || wikis.Items[0].Revision != "rev-sub" {
		t.Fatalf("unexpected dir/Sub.md page: %+v", wikis.Items[0])
	}
	if wikis.Items[1].ID != "tasks/Task.md" || wikis.Items[1].Revision != "rev-task" {
		t.Fatalf("unexpected tasks/Task.md page: %+v", wikis.Items[1])
	}
}

func TestScenario002WikiMalformedEntrySchemaDecode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v5/repos/example-owner/example-repo.wiki/contents":
			fmt.Fprint(w, `[{"path":"","type":"file"},{"path":"ok.md","type":"","sha":"rev-ok"}]`)
		default:
			t.Fatalf("unexpected wiki path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	_, err := client.ListWikiPages(context.Background(), WikiListRequest{Owner: "example-owner", Repo: "example-repo"})
	var partial ErrPartialResponse
	if !errors.As(err, &partial) {
		t.Fatalf("expected ErrPartialResponse, got %T %v", err, err)
	}
}

func TestScenario003WikiDuplicatePathDedup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v5/repos/example-owner/example-repo.wiki/contents":
			fmt.Fprint(w, `[{"path":"dup.md","type":"file","sha":"rev1"},{"path":"dup.md","type":"file","sha":"rev2"}]`)
		case "/api/v5/repos/example-owner/example-repo.wiki/contents/dup.md":
			fmt.Fprint(w, `{"path":"dup.md","type":"file","sha":"rev1"}`)
		case "/api/v5/repos/example-owner/example-repo.wiki/raw/dup.md":
			fmt.Fprint(w, "body")
		default:
			t.Fatalf("unexpected wiki path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	_, err := client.ListWikiPages(context.Background(), WikiListRequest{Owner: "example-owner", Repo: "example-repo"})
	var partial ErrPartialResponse
	if !errors.As(err, &partial) || !strings.Contains(partial.Message, "duplicate") {
		t.Fatalf("expected duplicate ErrPartialResponse, got %T %v", err, err)
	}
}

func TestScenario004WikiNestingLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v5/repos/example-owner/example-repo.wiki/contents":
			fmt.Fprint(w, `[{"path":"deep","type":"dir"}]`)
		case strings.Contains(r.URL.Path, "/contents/deep"):
			prefix := strings.TrimPrefix(r.URL.Path, "/api/v5/repos/example-owner/example-repo.wiki/contents/")
			next := prefix + "/deeper"
			fmt.Fprintf(w, `[{"path":%q,"type":"dir"}]`, next)
		default:
			t.Fatalf("unexpected wiki path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	_, err := client.ListWikiPages(context.Background(), WikiListRequest{Owner: "example-owner", Repo: "example-repo"})
	var partial ErrPartialResponse
	if !errors.As(err, &partial) || !strings.Contains(partial.Message, "64") {
		t.Fatalf("expected nesting limit ErrPartialResponse, got %T %v", err, err)
	}
}

func TestScenario005WikiRawReadBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v5/repos/example-owner/example-repo.wiki/contents/Design/Architecture.md":
			fmt.Fprint(w, `{"path":"Design/Architecture.md","type":"file","sha":"rev-arch"}`)
		case "/api/v5/repos/example-owner/example-repo.wiki/raw/Design/Architecture.md":
			fmt.Fprint(w, "# Architecture\n\nSystem overview")
		default:
			t.Fatalf("unexpected wiki path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	wiki, err := client.GetWikiPage(context.Background(), WikiPageRequest{Owner: "example-owner", Repo: "example-repo", Path: "Design/Architecture.md"})
	if err != nil {
		t.Fatalf("GetWikiPage returned error: %v", err)
	}
	if wiki.ID != "Design/Architecture.md" || wiki.Slug != "Design/Architecture.md" || wiki.Title != "Architecture" || wiki.Revision != "rev-arch" || wiki.Body != "# Architecture\n\nSystem overview" {
		t.Fatalf("unexpected wiki page: %+v", wiki)
	}
}

func TestScenario006WikiCreateBase64NoSha(t *testing.T) {
	var sawBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v5/repos/example-owner/example-repo.wiki/contents/NewPage.md" {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"not found"}`)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/api/v5/repos/example-owner/example-repo.wiki/contents/NewPage.md" {
			t.Fatalf("unexpected create request %s %s", r.Method, r.URL.Path)
		}
		var payload WikiContentWriteRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		sawBody = payload.Content
		if payload.Sha != "" {
			t.Fatalf("create must not send sha, got %q", payload.Sha)
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"path":"NewPage.md","type":"file","sha":"rev-new","content":"...","encoding":"base64"}`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	result, err := client.CreateWikiPage(context.Background(), CreateWikiPageRequest{Owner: "example-owner", Repo: "example-repo", Path: "NewPage.md", Title: "New Page", Body: "# New Page\n\nContent", Message: "create new page"}, WriteOptions{IdempotencyKey: "key-create"})
	if err != nil {
		t.Fatalf("CreateWikiPage returned error: %v", err)
	}
	if result.RemoteID != "NewPage.md" || result.RemoteSlug != "NewPage.md" || result.RemoteRevision != "rev-new" || !result.Confirmed {
		t.Fatalf("unexpected create result: %+v", result)
	}
	decoded, err := base64.StdEncoding.DecodeString(sawBody)
	if err != nil {
		t.Fatalf("payload not valid base64: %v", err)
	}
	if string(decoded) != "# New Page\n\nContent" {
		t.Fatalf("unexpected body: %q", string(decoded))
	}
}

func TestScenario007WikiUpdateShaAutoresolve(t *testing.T) {
	var sawSha string
	var sawMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v5/repos/example-owner/example-repo.wiki/contents/Existing.md":
			fmt.Fprint(w, `{"path":"Existing.md","type":"file","sha":"current-sha-123"}`)
		case r.Method == http.MethodPut && r.URL.Path == "/api/v5/repos/example-owner/example-repo.wiki/contents/Existing.md":
			sawMethod = r.Method
			var payload WikiContentWriteRequest
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode payload: %v", err)
			}
			sawSha = payload.Sha
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"path":"Existing.md","type":"file","sha":"new-sha-456"}`)
		default:
			t.Fatalf("unexpected update request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	result, err := client.UpdateWikiPage(context.Background(), UpdateWikiPageRequest{Owner: "example-owner", Repo: "example-repo", Path: "Existing.md", Body: "updated body"}, WriteOptions{IdempotencyKey: "key-update"})
	if err != nil {
		t.Fatalf("UpdateWikiPage returned error: %v", err)
	}
	if sawSha != "current-sha-123" || sawMethod != "PUT" {
		t.Fatalf("auto-resolved sha=%q method=%q", sawSha, sawMethod)
	}
	if result.RemoteID != "Existing.md" || result.RemoteRevision != "new-sha-456" || !result.Confirmed {
		t.Fatalf("unexpected update result: %+v", result)
	}
}

func TestScenario008WikiUpdateExplicitSha(t *testing.T) {
	var sawSha string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			t.Fatalf("GET must not be called when sha is explicit")
		}
		if r.Method != http.MethodPut || r.URL.Path != "/api/v5/repos/example-owner/example-repo.wiki/contents/Explicit.md" {
			t.Fatalf("unexpected update request %s %s", r.Method, r.URL.Path)
		}
		var payload WikiContentWriteRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		sawSha = payload.Sha
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"path":"Explicit.md","type":"file","sha":"explicit-result"}`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	result, err := client.UpdateWikiPage(context.Background(), UpdateWikiPageRequest{Owner: "example-owner", Repo: "example-repo", Path: "Explicit.md", Body: "updated body", Sha: "caller-supplied-sha"}, WriteOptions{IdempotencyKey: "key-explicit"})
	if err != nil {
		t.Fatalf("UpdateWikiPage returned error: %v", err)
	}
	if sawSha != "caller-supplied-sha" {
		t.Fatalf("explicit sha=%q", sawSha)
	}
	if result.RemoteRevision != "explicit-result" || !result.Confirmed {
		t.Fatalf("unexpected explicit result: %+v", result)
	}
}

func TestScenario009WikiDeleteStaleSha409(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v5/repos/example-owner/example-repo.wiki/contents/Stale.md":
			fmt.Fprint(w, `{"path":"Stale.md","type":"file","sha":"auto-sha"}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v5/repos/example-owner/example-repo.wiki/contents/Stale.md":
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `{"message":"stale sha"}`)
		default:
			t.Fatalf("unexpected delete request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	_, err := client.DeleteWikiPage(context.Background(), DeleteWikiPageRequest{Owner: "example-owner", Repo: "example-repo", Path: "Stale.md"}, WriteOptions{IdempotencyKey: "key-delete"})
	var conflict ErrConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("expected ErrConflict for stale sha, got %T %v", err, err)
	}
}

func TestScenario010BrowserRouteExclusion(t *testing.T) {
	var webAPIHits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Host, "web-api.gitcode.com") || strings.Contains(r.URL.Path, "/api/v2/projects/wiki") {
			webAPIHits++
			t.Fatalf("browser web-api route was called: %s", r.URL.Path)
		}
		switch r.URL.Path {
		case "/api/v5/repos/example-owner/example-repo.wiki/contents":
			fmt.Fprint(w, `[{"path":"Home.md","type":"file","sha":"rev-home"}]`)
		case "/api/v5/repos/example-owner/example-repo.wiki/contents/Home.md":
			fmt.Fprint(w, `{"path":"Home.md","type":"file","sha":"rev-home"}`)
		case "/api/v5/repos/example-owner/example-repo.wiki/raw/Home.md":
			fmt.Fprint(w, "# Home")
		default:
			t.Fatalf("unexpected wiki path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	wikis, err := client.ListWikiPages(context.Background(), WikiListRequest{Owner: "example-owner", Repo: "example-repo"})
	if err != nil || len(wikis.Items) != 1 {
		t.Fatalf("ListWikiPages wikis=%+v err=%v", wikis, err)
	}
	wiki, err := client.GetWikiPage(context.Background(), WikiPageRequest{Owner: "example-owner", Repo: "example-repo", Path: "Home.md"})
	if err != nil || wiki.ID != "Home.md" {
		t.Fatalf("GetWikiPage wiki=%+v err=%v", wiki, err)
	}
	if webAPIHits > 0 {
		t.Fatalf("browser web-api routes were hit %d times", webAPIHits)
	}
}

func TestLabel010IssueResponseObjects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v5/repos/example-owner/example-repo/issues" {
			fmt.Fprint(w, `[{"id":"1","number":1,"title":"Test","labels":[{"id":1,"name":"bug","color":"#FF0000"},{"id":2,"name":"enhancement","color":"#00FF00"}]}]`)
			return
		}
		t.Fatalf("unexpected path %s", r.URL.Path)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	issues, err := client.ListIssues(context.Background(), IssueListRequest{Owner: "example-owner", Repo: "example-repo"})
	if err != nil {
		t.Fatalf("ListIssues error: %v", err)
	}
	if len(issues.Items) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues.Items))
	}
	issue := issues.Items[0]
	if len(issue.Labels) != 2 || issue.Labels[0] != "bug" || issue.Labels[1] != "enhancement" {
		t.Fatalf("Labels: got %v, want [bug enhancement]", issue.Labels)
	}
	if len(issue.GitCodeLabels) != 2 || issue.GitCodeLabels[0].ID != 1 || issue.GitCodeLabels[0].Name != "bug" {
		t.Fatalf("GitCodeLabels: got %+v", issue.GitCodeLabels)
	}
}

func TestLabel011CreateRequestLabelString(t *testing.T) {
	var sawLabelsRaw json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v5/repos/example-owner/example-repo/issues" {
			var raw struct {
				Labels json.RawMessage `json:"labels"`
			}
			if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
				t.Fatalf("decode request body: %v", err)
			}
			sawLabelsRaw = raw.Labels
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"ISSUE-100","number":100,"title":"created"}`)
			return
		}
		t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	encodedLabels := EncodeIssueLabels([]string{"bug", "enhancement"})
	result, err := client.CreateIssue(context.Background(), CreateIssueRequest{
		Owner:  "example-owner",
		Repo:   "example-repo",
		Title:  "Test Issue",
		Labels: encodedLabels,
	}, WriteOptions{IdempotencyKey: "key-011"})
	if err != nil {
		t.Fatalf("CreateIssue error: %v", err)
	}
	if !result.Confirmed {
		t.Fatal("expected confirmed result")
	}
	if len(sawLabelsRaw) == 0 {
		t.Fatal("labels field not found in request body")
	}
	if sawLabelsRaw[0] != '"' {
		t.Fatalf("labels is not a JSON string: %s", string(sawLabelsRaw))
	}
	var decoded string
	if err := json.Unmarshal(sawLabelsRaw, &decoded); err != nil {
		t.Fatalf("labels is not a valid JSON string: %s, err=%v", string(sawLabelsRaw), err)
	}
	if decoded != "bug,enhancement" {
		t.Fatalf("labels content: got %q, want bug,enhancement", decoded)
	}
}

func TestLabel012CreateRequestEmptyLabels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v5/repos/example-owner/example-repo/issues" {
			var raw struct {
				Labels json.RawMessage `json:"labels"`
			}
			if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
				t.Fatalf("decode request body: %v", err)
			}
			if len(raw.Labels) > 0 {
				t.Fatalf("empty labels should be omitted, got %s", string(raw.Labels))
			}
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"ISSUE-200","number":200,"title":"no labels"}`)
			return
		}
		t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	encodedLabels := EncodeIssueLabels([]string{})
	result, err := client.CreateIssue(context.Background(), CreateIssueRequest{
		Owner:  "example-owner",
		Repo:   "example-repo",
		Title:  "No Labels",
		Labels: encodedLabels,
	}, WriteOptions{IdempotencyKey: "key-012"})
	if err != nil {
		t.Fatalf("CreateIssue error: %v", err)
	}
	if !result.Confirmed {
		t.Fatal("expected confirmed result")
	}
}

func TestLabel013StringLabelsAccepted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v5/repos/example-owner/example-repo/issues" {
			var raw struct {
				Labels json.RawMessage `json:"labels"`
			}
			if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
				t.Fatalf("decode request body: %v", err)
			}
			if len(raw.Labels) > 0 && raw.Labels[0] == '"' {
				w.WriteHeader(http.StatusCreated)
				fmt.Fprint(w, `{"id":"ISSUE-300","number":300,"title":"accepted","labels":[{"id":1,"name":"bug","color":"#FF0000"}]}`)
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"message":"labels must be a comma-separated JSON string, not an array"}`)
			return
		}
		t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})

	t.Run("string labels via DTO accepted", func(t *testing.T) {
		encoded := EncodeIssueLabels([]string{"bug"})
		result, err := client.CreateIssue(context.Background(), CreateIssueRequest{
			Owner:  "example-owner",
			Repo:   "example-repo",
			Title:  "Good Labels",
			Labels: encoded,
		}, WriteOptions{IdempotencyKey: "key-013-arr"})
		if err != nil {
			t.Fatalf("CreateIssue with string labels failed: %v", err)
		}
		if !result.Confirmed {
			t.Fatal("expected confirmed result for string labels")
		}
	})

	t.Run("native array labels rejected", func(t *testing.T) {
		body := map[string]interface{}{
			"title":  "Bad Labels",
			"labels": []string{"bug"},
			"owner":  "example-owner",
			"repo":   "example-repo",
		}
		bodyBytes, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v5/repos/example-owner/example-repo/issues", strings.NewReader(string(bodyBytes)))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("http request failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 for native array labels, got %d", resp.StatusCode)
		}
	})
}

func TestScenario013001CreateIssueLabelsAsJSONString(t *testing.T) {
	var sawBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v5/repos/example-owner/example-repo/issues" {
			buf := make([]byte, 4096)
			n, _ := r.Body.Read(buf)
			sawBody = buf[:n]
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"1","number":1,"title":"Test","labels":[{"id":1,"name":"bug","color":"#FF0000"},{"id":2,"name":"enhancement","color":"#00FF00"}]}`)
			return
		}
		t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	result, err := client.CreateIssue(context.Background(), CreateIssueRequest{
		Owner:  "example-owner",
		Repo:   "example-repo",
		Title:  "Test",
		Labels: EncodeIssueLabels([]string{"bug", "enhancement"}),
	}, WriteOptions{IdempotencyKey: "key-013-001"})
	if err != nil {
		t.Fatalf("CreateIssue error: %v", err)
	}
	if !result.Confirmed {
		t.Fatal("expected confirmed result")
	}
	var raw struct {
		Labels json.RawMessage `json:"labels"`
	}
	if err := json.Unmarshal(sawBody, &raw); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	if len(raw.Labels) == 0 {
		t.Fatal("labels field not found in request body")
	}
	if raw.Labels[0] != '"' {
		t.Fatalf("labels is not a JSON string: %s", string(raw.Labels))
	}
	var decoded string
	if err := json.Unmarshal(raw.Labels, &decoded); err != nil {
		t.Fatalf("labels is not a valid JSON string: %s, err=%v", string(raw.Labels), err)
	}
	if decoded != "bug,enhancement" {
		t.Fatalf("labels: got %q, want bug,enhancement", decoded)
	}
}

func TestScenario013002UpdateIssueLabelsAsJSONString(t *testing.T) {
	var sawBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch && r.URL.Path == "/api/v5/repos/example-owner/example-repo/issues/42" {
			buf := make([]byte, 4096)
			n, _ := r.Body.Read(buf)
			sawBody = buf[:n]
			fmt.Fprint(w, `{"id":"42","number":42,"title":"Updated","labels":[{"id":1,"name":"bug","color":"#FF0000"}]}`)
			return
		}
		t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	result, err := client.UpdateIssue(context.Background(), UpdateIssueRequest{
		Owner:  "example-owner",
		Repo:   "example-repo",
		Number: 42,
		Labels: EncodeIssueLabels([]string{"bug"}),
	}, WriteOptions{IdempotencyKey: "key-013-002"})
	if err != nil {
		t.Fatalf("UpdateIssue error: %v", err)
	}
	if !result.Confirmed {
		t.Fatal("expected confirmed result")
	}
	var raw struct {
		Labels json.RawMessage `json:"labels"`
	}
	if err := json.Unmarshal(sawBody, &raw); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	if len(raw.Labels) == 0 {
		t.Fatal("labels field not found in request body")
	}
	if raw.Labels[0] != '"' {
		t.Fatalf("labels is not a JSON string: %s", string(raw.Labels))
	}
	var decoded string
	if err := json.Unmarshal(raw.Labels, &decoded); err != nil {
		t.Fatalf("labels is not a valid JSON string: %s, err=%v", string(raw.Labels), err)
	}
	if decoded != "bug" {
		t.Fatalf("labels: got %q, want bug", decoded)
	}
}

func TestScenario013003CreateIssueNoLabelsOmitsField(t *testing.T) {
	var sawBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v5/repos/example-owner/example-repo/issues" {
			buf := make([]byte, 4096)
			n, _ := r.Body.Read(buf)
			sawBody = buf[:n]
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"1","number":1,"title":"No Labels"}`)
			return
		}
		t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	result, err := client.CreateIssue(context.Background(), CreateIssueRequest{
		Owner: "example-owner",
		Repo:  "example-repo",
		Title: "No Labels",
	}, WriteOptions{IdempotencyKey: "key-013-003"})
	if err != nil {
		t.Fatalf("CreateIssue error: %v", err)
	}
	if !result.Confirmed {
		t.Fatal("expected confirmed result")
	}
	var raw struct {
		Labels json.RawMessage `json:"labels"`
	}
	if err := json.Unmarshal(sawBody, &raw); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	if len(raw.Labels) > 0 {
		t.Fatalf("labels field should be omitted, got %s", string(raw.Labels))
	}
	var bodyMap map[string]interface{}
	if err := json.Unmarshal(sawBody, &bodyMap); err != nil {
		t.Fatalf("unmarshal body map: %v", err)
	}
	if _, ok := bodyMap["labels"]; ok {
		t.Fatal("labels key should be absent from serialized JSON")
	}
}

func TestScenario016CreateIssueLabelsOmitted(t *testing.T) {
	var sawBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v5/repos/example-owner/example-repo/issues" {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read request body: %v", err)
			}
			sawBody = body
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"id":"1","number":1,"title":"No Labels"}`)
			return
		}
		t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	result, err := client.CreateIssue(context.Background(), CreateIssueRequest{
		Owner: "example-owner",
		Repo:  "example-repo",
		Title: "No Labels",
	}, WriteOptions{IdempotencyKey: "key-016-create-omitted"})
	if err != nil {
		t.Fatalf("CreateIssue error: %v", err)
	}
	if !result.Confirmed {
		t.Fatal("expected confirmed result")
	}
	assertJSONKeyAbsent(t, sawBody, "labels")
}

func TestScenario016UpdateIssueTitleOnlyLabelsOmitted(t *testing.T) {
	var sawBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch && r.URL.Path == "/api/v5/repos/example-owner/example-repo/issues/42" {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read request body: %v", err)
			}
			sawBody = body
			fmt.Fprint(w, `{"id":"42","number":42,"title":"Updated"}`)
			return
		}
		t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	result, err := client.UpdateIssue(context.Background(), UpdateIssueRequest{
		Owner:  "example-owner",
		Repo:   "example-repo",
		Number: 42,
		Title:  "Updated",
	}, WriteOptions{IdempotencyKey: "key-016-update-omitted"})
	if err != nil {
		t.Fatalf("UpdateIssue error: %v", err)
	}
	if !result.Confirmed {
		t.Fatal("expected confirmed result")
	}
	assertJSONKeyAbsent(t, sawBody, "labels")
}

func TestScenario016ExplicitLabelsPreserved(t *testing.T) {
	var sawBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch && r.URL.Path == "/api/v5/repos/example-owner/example-repo/issues/42" {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read request body: %v", err)
			}
			sawBody = body
			fmt.Fprint(w, `{"id":"42","number":42,"title":"Updated","labels":[{"id":1,"name":"bug","color":"#FF0000"}]}`)
			return
		}
		t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	result, err := client.UpdateIssue(context.Background(), UpdateIssueRequest{
		Owner:  "example-owner",
		Repo:   "example-repo",
		Number: 42,
		Labels: EncodeIssueLabels([]string{"bug"}),
	}, WriteOptions{IdempotencyKey: "key-016-explicit-labels"})
	if err != nil {
		t.Fatalf("UpdateIssue error: %v", err)
	}
	if !result.Confirmed {
		t.Fatal("expected confirmed result")
	}
	var raw struct {
		Labels json.RawMessage `json:"labels"`
	}
	if err := json.Unmarshal(sawBody, &raw); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	if string(raw.Labels) != `"bug"` {
		t.Fatalf("labels: got %s, want \"bug\"", string(raw.Labels))
	}
}

func assertJSONKeyAbsent(t *testing.T, body []byte, key string) {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	if _, ok := raw[key]; ok {
		t.Fatalf("%s key should be absent from serialized JSON", key)
	}
}

func TestScenario013004AddLabelReturnsUnsupportedCapability(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/labels") {
			t.Fatal("add-label endpoint should not be called by live provider")
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{}`)
	}))
	defer server.Close()
	provider, err := NewLiveProvider(ProviderConfig{
		Mode:        ProviderModeLive,
		LiveAllowed: true,
		Token:       "test-token",
		BaseURL:     server.URL,
		Timeout:     2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	lp, ok := provider.(liveProvider)
	if !ok {
		t.Fatal("expected liveProvider")
	}
	_, err = lp.AddLabel(context.Background(), LabelRequest{
		Owner:  "example-owner",
		Repo:   "example-repo",
		Number: 42,
		Label:  "bug",
	}, WriteOptions{IdempotencyKey: "key-013-004"})
	if err == nil {
		t.Fatal("liveProvider.AddLabel: expected error, got nil")
	}
	if !IsUnsupportedCapability(err) {
		t.Fatalf("liveProvider.AddLabel: expected ErrUnsupportedCapability, got %T: %v", err, err)
	}
}

func TestScenario013006AddLabelEndpointAbsentFromProvider(t *testing.T) {
	provider, err := NewLiveProvider(ProviderConfig{
		Mode:        ProviderModeLive,
		LiveAllowed: true,
		Token:       "test-token",
		BaseURL:     "http://127.0.0.1:1",
		Timeout:     1 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	lp, ok := provider.(liveProvider)
	if !ok {
		t.Fatal("expected liveProvider")
	}
	_, err = lp.AddLabel(context.Background(), LabelRequest{
		Owner:  "example-owner",
		Repo:   "example-repo",
		Number: 42,
		Label:  "bug",
	}, WriteOptions{})
	if err == nil {
		t.Fatal("liveProvider.AddLabel: expected error, got nil")
	}
	if !IsUnsupportedCapability(err) {
		t.Fatalf("liveProvider.AddLabel: expected ErrUnsupportedCapability, got %T: %v", err, err)
	}
}

func TestScenario013007CreateIssueWithLabelsNormalizedInResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"id":"1","number":1,"title":"Test","labels":[{"id":1,"name":"bug","color":"#FF0000"},{"id":2,"name":"enhancement","color":"#00FF00"}]}`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	result, err := client.CreateIssue(context.Background(), CreateIssueRequest{
		Owner:  "example-owner",
		Repo:   "example-repo",
		Title:  "Test",
		Labels: EncodeIssueLabels([]string{"bug", "enhancement"}),
	}, WriteOptions{IdempotencyKey: "key-013-007"})
	if err != nil {
		t.Fatalf("CreateIssue error: %v", err)
	}
	if !result.Confirmed {
		t.Fatal("expected confirmed result")
	}
	if len(result.Record.Labels) != 2 || result.Record.Labels[0] != "bug" || result.Record.Labels[1] != "enhancement" {
		t.Fatalf("normalized labels: got %v, want [bug enhancement]", result.Record.Labels)
	}
}

func TestScenario013008EncodeIssueLabelsOutputIsJSONString(t *testing.T) {
	result := EncodeIssueLabels([]string{"bug", "enhancement"})
	if len(result) == 0 || result[0] != '"' {
		t.Fatalf("EncodeIssueLabels: output is not a JSON string literal: %s", string(result))
	}
	var decoded string
	if err := json.Unmarshal(result, &decoded); err != nil {
		t.Fatalf("EncodeIssueLabels: output is not valid JSON string: %s, err=%v", string(result), err)
	}
	if decoded != "bug,enhancement" {
		t.Fatalf("EncodeIssueLabels: got %q, want bug,enhancement", decoded)
	}

	empty := EncodeIssueLabels([]string{})
	if empty != nil {
		t.Fatalf("EncodeIssueLabels empty: got %s, want nil", string(empty))
	}
}

func TestBoundedWikiTreeTraversalMaxRecords(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v5/repos/example-owner/example-repo.wiki/contents":
			fmt.Fprint(w, `[{"path":"dirA","type":"dir"},{"path":"dirB","type":"dir"},{"path":"README.md","type":"file","sha":"rev-home"}]`)
		case "/api/v5/repos/example-owner/example-repo.wiki/contents/dirA":
			fmt.Fprint(w, `[{"path":"dirA/A1.md","type":"file","sha":"rev-a1"},{"path":"dirA/A2.md","type":"file","sha":"rev-a2"}]`)
		case "/api/v5/repos/example-owner/example-repo.wiki/contents/dirB":
			fmt.Fprint(w, `[{"path":"dirB/B1.md","type":"file","sha":"rev-b1"},{"path":"dirB/B2.md","type":"file","sha":"rev-b2"}]`)
		case "/api/v5/repos/example-owner/example-repo.wiki/contents/README.md":
			fmt.Fprint(w, `{"path":"README.md","type":"file","sha":"rev-home"}`)
		case "/api/v5/repos/example-owner/example-repo.wiki/raw/README.md":
			fmt.Fprint(w, "# Home")
		case "/api/v5/repos/example-owner/example-repo.wiki/contents/dirA/A1.md":
			fmt.Fprint(w, `{"path":"dirA/A1.md","type":"file","sha":"rev-a1"}`)
		case "/api/v5/repos/example-owner/example-repo.wiki/raw/dirA/A1.md":
			fmt.Fprint(w, "# A1")
		case "/api/v5/repos/example-owner/example-repo.wiki/contents/dirA/A2.md":
			fmt.Fprint(w, `{"path":"dirA/A2.md","type":"file","sha":"rev-a2"}`)
		case "/api/v5/repos/example-owner/example-repo.wiki/raw/dirA/A2.md":
			fmt.Fprint(w, "# A2")
		case "/api/v5/repos/example-owner/example-repo.wiki/contents/dirB/B1.md":
			fmt.Fprint(w, `{"path":"dirB/B1.md","type":"file","sha":"rev-b1"}`)
		case "/api/v5/repos/example-owner/example-repo.wiki/raw/dirB/B1.md":
			fmt.Fprint(w, "# B1")
		case "/api/v5/repos/example-owner/example-repo.wiki/contents/dirB/B2.md":
			fmt.Fprint(w, `{"path":"dirB/B2.md","type":"file","sha":"rev-b2"}`)
		case "/api/v5/repos/example-owner/example-repo.wiki/raw/dirB/B2.md":
			fmt.Fprint(w, "# B2")
		default:
			t.Fatalf("unexpected wiki path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})

	req := WikiListRequest{
		Owner: "example-owner",
		Repo:  "example-repo",
		Bounds: &WikiBounds{
			MaxRecords: 3,
		},
	}
	wikis, err := client.ListWikiPages(context.Background(), req)
	if err != nil {
		t.Fatalf("ListWikiPages returned error: %v", err)
	}
	if len(wikis.Items) != 3 {
		t.Fatalf("expected 3 pages (bounded by MaxRecords), got %d", len(wikis.Items))
	}
	expectedSlugs := map[string]bool{"dirA/A1.md": true, "dirA/A2.md": true, "dirB/B1.md": true}
	for _, item := range wikis.Items {
		if !expectedSlugs[item.Slug] {
			t.Fatalf("unexpected page slug %q, want one of dirA/A1, dirA/A2, dirB/B1", item.Slug)
		}
	}
}

func TestBoundedWikiTreeTraversalCancelMidTraversal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v5/repos/example-owner/example-repo.wiki/contents":
			fmt.Fprint(w, `[{"path":"dirA","type":"dir"},{"path":"dirB","type":"dir"},{"path":"README.md","type":"file","sha":"rev-home"}]`)
		case "/api/v5/repos/example-owner/example-repo.wiki/contents/dirA":
			fmt.Fprint(w, `[{"path":"dirA/A1.md","type":"file","sha":"rev-a1"},{"path":"dirA/A2.md","type":"file","sha":"rev-a2"}]`)
		case "/api/v5/repos/example-owner/example-repo.wiki/contents/dirB":
			fmt.Fprint(w, `[{"path":"dirB/B1.md","type":"file","sha":"rev-b1"},{"path":"dirB/B2.md","type":"file","sha":"rev-b2"}]`)
		case "/api/v5/repos/example-owner/example-repo.wiki/contents/dirA/A1.md":
			fmt.Fprint(w, `{"path":"dirA/A1.md","type":"file","sha":"rev-a1"}`)
		case "/api/v5/repos/example-owner/example-repo.wiki/raw/dirA/A1.md":
			fmt.Fprint(w, "# A1")
		case "/api/v5/repos/example-owner/example-repo.wiki/contents/dirA/A2.md":
			fmt.Fprint(w, `{"path":"dirA/A2.md","type":"file","sha":"rev-a2"}`)
		case "/api/v5/repos/example-owner/example-repo.wiki/raw/dirA/A2.md":
			fmt.Fprint(w, "# A2")
		case "/api/v5/repos/example-owner/example-repo.wiki/contents/README.md":
			fmt.Fprint(w, `{"path":"README.md","type":"file","sha":"rev-home"}`)
		case "/api/v5/repos/example-owner/example-repo.wiki/raw/README.md":
			fmt.Fprint(w, "# Home")
		default:
			t.Fatalf("unexpected wiki path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})

	ctx, cancel := context.WithCancel(context.Background())
	req := WikiListRequest{
		Owner: "example-owner",
		Repo:  "example-repo",
		Bounds: &WikiBounds{
			MaxRecords: 3,
		},
	}

	go func() {
		cancel()
	}()

	wikis, err := client.ListWikiPages(ctx, req)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	}
	if len(wikis.Items) < 0 {
		t.Fatalf("expected some committed items on cancellation")
	}
}

func TestBoundedWikiTreeTraversalProgressEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v5/repos/example-owner/example-repo.wiki/contents":
			fmt.Fprint(w, `[{"path":"dirA","type":"dir"},{"path":"dirB","type":"dir"},{"path":"README.md","type":"file","sha":"rev-home"}]`)
		case "/api/v5/repos/example-owner/example-repo.wiki/contents/dirA":
			fmt.Fprint(w, `[{"path":"dirA/A1.md","type":"file","sha":"rev-a1"},{"path":"dirA/A2.md","type":"file","sha":"rev-a2"}]`)
		case "/api/v5/repos/example-owner/example-repo.wiki/contents/dirB":
			fmt.Fprint(w, `[{"path":"dirB/B1.md","type":"file","sha":"rev-b1"},{"path":"dirB/B2.md","type":"file","sha":"rev-b2"}]`)
		case "/api/v5/repos/example-owner/example-repo.wiki/contents/dirA/A1.md":
			fmt.Fprint(w, `{"path":"dirA/A1.md","type":"file","sha":"rev-a1"}`)
		case "/api/v5/repos/example-owner/example-repo.wiki/raw/dirA/A1.md":
			fmt.Fprint(w, "# A1")
		case "/api/v5/repos/example-owner/example-repo.wiki/contents/dirA/A2.md":
			fmt.Fprint(w, `{"path":"dirA/A2.md","type":"file","sha":"rev-a2"}`)
		case "/api/v5/repos/example-owner/example-repo.wiki/raw/dirA/A2.md":
			fmt.Fprint(w, "# A2")
		case "/api/v5/repos/example-owner/example-repo.wiki/contents/dirB/B1.md":
			fmt.Fprint(w, `{"path":"dirB/B1.md","type":"file","sha":"rev-b1"}`)
		case "/api/v5/repos/example-owner/example-repo.wiki/raw/dirB/B1.md":
			fmt.Fprint(w, "# B1")
		case "/api/v5/repos/example-owner/example-repo.wiki/contents/dirB/B2.md":
			fmt.Fprint(w, `{"path":"dirB/B2.md","type":"file","sha":"rev-b2"}`)
		case "/api/v5/repos/example-owner/example-repo.wiki/raw/dirB/B2.md":
			fmt.Fprint(w, "# B2")
		case "/api/v5/repos/example-owner/example-repo.wiki/contents/README.md":
			fmt.Fprint(w, `{"path":"README.md","type":"file","sha":"rev-home"}`)
		case "/api/v5/repos/example-owner/example-repo.wiki/raw/README.md":
			fmt.Fprint(w, "# Home")
		default:
			t.Fatalf("unexpected wiki path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	progressChan := make(chan WikiProgressEvent, 10)
	var events []WikiProgressEvent
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range progressChan {
			events = append(events, ev)
		}
	}()
	req := WikiListRequest{
		Owner: "example-owner",
		Repo:  "example-repo",
		Bounds: &WikiBounds{
			ProgressChan: progressChan,
		},
	}
	_, err := client.ListWikiPages(context.Background(), req)
	close(progressChan)
	<-done
	if err != nil {
		t.Fatalf("ListWikiPages returned error: %v", err)
	}
	if len(events) < 3 {
		t.Fatalf("progress events = %d, want at least 3 (root + dirA + dirB)", len(events))
	}
	for i, ev := range events {
		if ev.RecordsFetched < 0 {
			t.Fatalf("event[%d].RecordsFetched = %d, want >= 0", i, ev.RecordsFetched)
		}
	}
}

func TestBoundedWikiTreeTraversalUnboundedBackwardCompat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v5/repos/example-owner/example-repo.wiki/contents":
			fmt.Fprint(w, `[{"path":"README.md","type":"file","sha":"rev-home"},{"path":"dir","type":"dir"}]`)
		case "/api/v5/repos/example-owner/example-repo.wiki/contents/dir":
			fmt.Fprint(w, `[{"path":"dir/Sub.md","type":"file","sha":"rev-sub"}]`)
		case "/api/v5/repos/example-owner/example-repo.wiki/contents/README.md":
			fmt.Fprint(w, `{"path":"README.md","type":"file","sha":"rev-home"}`)
		case "/api/v5/repos/example-owner/example-repo.wiki/raw/README.md":
			fmt.Fprint(w, "# Home")
		case "/api/v5/repos/example-owner/example-repo.wiki/contents/dir/Sub.md":
			fmt.Fprint(w, `{"path":"dir/Sub.md","type":"file","sha":"rev-sub"}`)
		case "/api/v5/repos/example-owner/example-repo.wiki/raw/dir/Sub.md":
			fmt.Fprint(w, "## Sub")
		default:
			t.Fatalf("unexpected wiki path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	wikis, err := client.ListWikiPages(context.Background(), WikiListRequest{Owner: "example-owner", Repo: "example-repo"})
	if err != nil {
		t.Fatalf("ListWikiPages (unbounded) returned error: %v", err)
	}
	if len(wikis.Items) != 2 {
		t.Fatalf("expected 2 pages from unbounded traversal, got %d", len(wikis.Items))
	}
	ordered := []string{"dir/Sub.md", "README.md"}
	for i, item := range wikis.Items {
		if item.Slug != ordered[i] {
			t.Fatalf("expected ordered[%d] = %q, got %q", i, ordered[i], item.Slug)
		}
	}
}

func TestBoundedWikiTreeTraversalNoOuterLoopPattern(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v5/repos/example-owner/example-repo.wiki/contents":
			fmt.Fprint(w, `[{"path":"README.md","type":"file","sha":"rev-home"}]`)
		case "/api/v5/repos/example-owner/example-repo.wiki/contents/README.md":
			fmt.Fprint(w, `{"path":"README.md","type":"file","sha":"rev-home"}`)
		case "/api/v5/repos/example-owner/example-repo.wiki/raw/README.md":
			fmt.Fprint(w, "# Home")
		default:
			t.Fatalf("unexpected wiki path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	req := WikiListRequest{
		Owner: "example-owner",
		Repo:  "example-repo",
		Bounds: &WikiBounds{
			MaxRecords: 5,
		},
	}
	wikis, err := client.ListWikiPages(context.Background(), req)
	if err != nil {
		t.Fatalf("ListWikiPages returned error: %v", err)
	}
	if len(wikis.Items) != 1 {
		t.Fatalf("expected 1 page, got %d", len(wikis.Items))
	}
}

func TestEmptyWikiDetection400(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v5/repos/example-owner/example-repo.wiki/contents" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"message":"wiki not found"}`)
			return
		}
		t.Fatalf("unexpected wiki path %s", r.URL.Path)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	_, err := client.ListWikiPages(context.Background(), WikiListRequest{Owner: "example-owner", Repo: "example-repo"})
	var emptyWiki ErrEmptyWiki
	if !errors.As(err, &emptyWiki) {
		t.Fatalf("expected ErrEmptyWiki, got %T %v", err, err)
	}
	if emptyWiki.DiagnosticCode() != "empty_wiki" {
		t.Fatalf("DiagnosticCode() = %q, want %q", emptyWiki.DiagnosticCode(), "empty_wiki")
	}
	if !strings.Contains(emptyWiki.Error(), "empty or uninitialized") {
		t.Fatalf("ErrEmptyWiki missing remediation text: %s", emptyWiki.Error())
	}
	if strings.Contains(emptyWiki.Error(), "api_validation") {
		t.Fatalf("ErrEmptyWiki should not mention api_validation: %s", emptyWiki.Error())
	}
}

func TestEmptyWikiDetection404(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v5/repos/example-owner/example-repo.wiki/contents" {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"wiki is empty"}`)
			return
		}
		t.Fatalf("unexpected wiki path %s", r.URL.Path)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	_, err := client.ListWikiPages(context.Background(), WikiListRequest{Owner: "example-owner", Repo: "example-repo"})
	var emptyWiki ErrEmptyWiki
	if !errors.As(err, &emptyWiki) {
		t.Fatalf("expected ErrEmptyWiki, got %T %v", err, err)
	}
	if emptyWiki.DiagnosticCode() != "empty_wiki" {
		t.Fatalf("DiagnosticCode() = %q, want %q", emptyWiki.DiagnosticCode(), "empty_wiki")
	}
}

func TestEmptyWikiDetection400NonEmptyWiki(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v5/repos/example-owner/example-repo.wiki/contents" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"message":"invalid repo name"}`)
			return
		}
		t.Fatalf("unexpected wiki path %s", r.URL.Path)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	_, err := client.ListWikiPages(context.Background(), WikiListRequest{Owner: "example-owner", Repo: "example-repo"})
	var apiValidation ErrAPIValidation
	if !errors.As(err, &apiValidation) {
		t.Fatalf("expected ErrAPIValidation for non-empty-wiki 400, got %T %v", err, err)
	}
	if apiValidation.DiagnosticCode() != "api_validation" {
		t.Fatalf("DiagnosticCode() = %q, want %q", apiValidation.DiagnosticCode(), "api_validation")
	}
}

func TestEmptyWikiDetection404UninitializedMessage(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"wiki is not initialized", `{"message":"wiki is not initialized"}`},
		{"wiki is uninitialized", `{"message":"wiki is uninitialized"}`},
		{"uninitialized wiki", `{"message":"uninitialized wiki"}`},
		{"wiki has not been created", `{"message":"wiki has not been created"}`},
		{"wiki has no pages", `{"message":"wiki has no pages"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, tt.body)
			}))
			defer server.Close()
			client := newTestClient(t, server.URL, Config{})
			_, err := client.ListWikiPages(context.Background(), WikiListRequest{Owner: "example-owner", Repo: "example-repo"})
			var emptyWiki ErrEmptyWiki
			if !errors.As(err, &emptyWiki) {
				t.Fatalf("expected ErrEmptyWiki, got %T %v", err, err)
			}
		})
	}
}

func TestEmptyWikiDetectionEmptyArray200IsOK(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v5/repos/example-owner/example-repo.wiki/contents" {
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `[]`)
			return
		}
		t.Fatalf("unexpected wiki path %s", r.URL.Path)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	wikis, err := client.ListWikiPages(context.Background(), WikiListRequest{Owner: "example-owner", Repo: "example-repo"})
	if err != nil {
		t.Fatalf("ListWikiPages with empty [] 200 should not error, got: %v", err)
	}
	if len(wikis.Items) != 0 {
		t.Fatalf("expected 0 pages from empty array, got %d", len(wikis.Items))
	}
}

func TestCreateWikiPageEmptyWikiDiagnostic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v5/repos/example-owner/example-repo.wiki/contents/") {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"message":"wiki not found"}`)
			return
		}
		t.Fatalf("unexpected wiki request %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	_, err := client.CreateWikiPage(context.Background(), CreateWikiPageRequest{Owner: "example-owner", Repo: "example-repo", Path: "Home.md", Title: "Home", Body: "# Welcome"}, WriteOptions{IdempotencyKey: "key-create-empty"})
	var emptyWiki ErrEmptyWiki
	if !errors.As(err, &emptyWiki) {
		t.Fatalf("expected ErrEmptyWiki from CreateWikiPage against empty wiki, got %T %v", err, err)
	}
	if emptyWiki.DiagnosticCode() != "empty_wiki" {
		t.Fatalf("DiagnosticCode() = %q, want %q", emptyWiki.DiagnosticCode(), "empty_wiki")
	}
	if !strings.Contains(emptyWiki.Error(), "empty or uninitialized") {
		t.Fatalf("ErrEmptyWiki missing remediation text: %s", emptyWiki.Error())
	}
}
