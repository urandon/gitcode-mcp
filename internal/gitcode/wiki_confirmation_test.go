package gitcode

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWikiWriteConfirmsOpaqueResponseByExactReadback(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut} {
		t.Run(method, func(t *testing.T) {
			mutations, reads := 0, 0
			const body = "# Fixture\n\nExact intended body.\n"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v5/repos/example-owner/example-repo.wiki/contents/Home.md" {
					t.Error("unexpected path")
					w.WriteHeader(404)
					return
				}
				if r.Method == method {
					mutations++
					fmt.Fprint(w, `{"content":{"path":"Home.md","sha":"new"},"commit":{"sha":"commit"}}`)
					return
				}
				if r.Method != http.MethodGet {
					t.Error("unexpected method")
				}
				reads++
				_ = json.NewEncoder(w).Encode(WikiContentsFile{Path: "Home.md", Sha: "new", Content: base64.StdEncoding.EncodeToString([]byte(body)), Encoding: "base64"})
			}))
			defer server.Close()
			client := newTestClient(t, server.URL, Config{})
			var result WriteResult[WikiPage]
			var err error
			if method == http.MethodPost {
				result, err = client.CreateWikiPage(context.Background(), CreateWikiPageRequest{Owner: "example-owner", Repo: "example-repo", Path: "Home.md", Body: body}, WriteOptions{IdempotencyKey: "create"})
			} else {
				result, err = client.UpdateWikiPage(context.Background(), UpdateWikiPageRequest{Owner: "example-owner", Repo: "example-repo", Path: "Home.md", Sha: "old", Body: body}, WriteOptions{IdempotencyKey: "update"})
			}
			if err != nil || !result.Confirmed || result.Record.Body != body || result.RemoteID != "Home.md" || mutations != 1 || reads != 1 {
				t.Fatalf("confirmation failed: confirmed=%t mutations=%d reads=%d err=%v", result.Confirmed, mutations, reads, err)
			}
		})
	}
}

func TestWikiAvailabilityDiagnosticsDoNotGuessInitialization(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body, code string
	}{
		{"empty", 404, `{"message":"wiki is not initialized"}`, "empty_wiki"},
		{"disabled", 403, `{"message":"wiki is disabled"}`, "wiki_disabled"},
		{"unsupported", 405, `{}`, "wiki_route_unsupported"},
		{"not-implemented", 501, `{}`, "wiki_route_unsupported"},
		{"bare-not-found", 404, `{"message":"not found"}`, "wiki_unavailable"},
		{"path-validation", 400, `{"message":"invalid path"}`, "api_validation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) }))
			defer server.Close()
			client := newTestClient(t, server.URL, Config{})
			_, err := client.ListWikiPages(context.Background(), WikiListRequest{Owner: "example-owner", Repo: "example-repo"})
			var diagnostic interface{ DiagnosticCode() string }
			if !errors.As(err, &diagnostic) || diagnostic.DiagnosticCode() != tc.code || strings.Contains(err.Error(), "wiki init") {
				t.Fatalf("diagnostic=%v expected=%s", err, tc.code)
			}
			_, err = client.UpdateWikiPage(context.Background(), UpdateWikiPageRequest{Owner: "example-owner", Repo: "example-repo", Path: "Home.md", Body: "wanted"}, WriteOptions{IdempotencyKey: "key"})
			var phase ErrWriteMutationPhase
			if !errors.As(err, &phase) || phase.MutationAttempted || phase.Phase != "preflight" {
				t.Fatalf("preflight evidence missing: %v", err)
			}
		})
	}
}

func TestWikiWriteReadbackFailurePreservesMutationPhase(t *testing.T) {
	for _, failure := range []string{"mismatch", "unavailable", "metadata-only", "transport"} {
		t.Run(failure, func(t *testing.T) {
			mutations := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPut {
					mutations++
					if failure == "transport" {
						w.WriteHeader(503)
						return
					}
					fmt.Fprint(w, `{"path":"Home.md","sha":"new"}`)
					return
				}
				if failure == "unavailable" || failure == "metadata-only" && r.URL.Path == "/api/v5/repos/example-owner/example-repo.wiki/raw/Home.md" {
					w.WriteHeader(404)
					return
				}
				meta := WikiContentsFile{Path: "Home.md", Sha: "new"}
				if failure == "mismatch" {
					meta.Content, meta.Encoding = base64.StdEncoding.EncodeToString([]byte("different body")), "base64"
				}
				_ = json.NewEncoder(w).Encode(meta)
			}))
			defer server.Close()
			client := newTestClient(t, server.URL, Config{MaxRetries: 2})
			_, err := client.UpdateWikiPage(context.Background(), UpdateWikiPageRequest{Owner: "example-owner", Repo: "example-repo", Path: "Home.md", Sha: "old", Body: "wanted"}, WriteOptions{IdempotencyKey: "update"})
			var phase ErrWriteMutationPhase
			if !errors.As(err, &phase) || !phase.MutationAttempted || mutations != 1 {
				t.Fatalf("expected one attempted mutation and typed phase: mutations=%d err=%v", mutations, err)
			}
		})
	}
}
