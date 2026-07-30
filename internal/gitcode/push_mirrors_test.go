package gitcode

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListPushRemoteMirrorsUsesV5RouteAndSanitizes(t *testing.T) {
	const secret = "credential-value-that-must-not-leak"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method=%s want GET", r.Method)
		}
		if r.URL.EscapedPath() != "/api/v5/repos/example-owner/example-repo/push_remote_mirrors" {
			t.Errorf("path=%q", r.URL.EscapedPath())
		}
		if r.URL.RawQuery != "" {
			t.Errorf("query=%q want empty", r.URL.RawQuery)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("authorization header=%q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Accept") != "application/json" {
			t.Errorf("accept=%q", r.Header.Get("Accept"))
		}
		for _, header := range []string{"Cookie", "Origin", "Referer", "X-Device-ID", "page-uri"} {
			if value := r.Header.Get(header); value != "" {
				t.Errorf("%s=%q want absent", header, value)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `[{
			"id": 17,
			"project_id": 101,
			"url": "https://user:%s@mirror.example.invalid/example/repo.git?access_token=%s&ref=main#secret",
			"force": true,
			"is_private": true,
			"update_status": "finished",
			"number_of_failures": 2,
			"message": "retry https://user:%s@mirror.example.invalid/example/repo.git?token=%s",
			"created_at": "2026-07-29T09:00:00Z",
			"last_update_at": "2026-07-29T10:00:00Z",
			"last_successful_update_at": "2026-07-29T10:00:00Z"
		}]`, secret, secret, secret, secret)
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, Config{Token: "test-token"})
	mirrors, err := client.ListPushRemoteMirrors(context.Background(), PushMirrorListRequest{Owner: "example-owner", Repo: "example-repo"})
	if err != nil {
		t.Fatalf("ListPushRemoteMirrors: %v", err)
	}
	if len(mirrors) != 1 {
		t.Fatalf("mirrors=%#v", mirrors)
	}
	mirror := mirrors[0]
	if mirror.RemoteID != "17" || mirror.ProjectID != "101" {
		t.Fatalf("ids=%#v", mirror)
	}
	if mirror.URL != "https://mirror.example.invalid/example/repo.git" {
		t.Fatalf("url=%q", mirror.URL)
	}
	if mirror.Message != "retry https://mirror.example.invalid/example/repo.git" {
		t.Fatalf("message=%q", mirror.Message)
	}
	if !mirror.Force || !mirror.Private || mirror.UpdateStatus != "finished" || mirror.NumberOfFailures != 2 {
		t.Fatalf("mirror=%#v", mirror)
	}
	if strings.Contains(fmt.Sprintf("%+v", mirror), secret) {
		t.Fatalf("sanitized mirror leaked secret: %+v", mirror)
	}
}

func TestListPushRemoteMirrorsFixtureShape(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("..", "..", "testdata", "gitcode", "push-remote-mirrors-v5.json"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture)
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, Config{Token: "test-token"})
	mirrors, err := client.ListPushRemoteMirrors(context.Background(), PushMirrorListRequest{Owner: "example-owner", Repo: "example-repo"})
	if err != nil {
		t.Fatalf("ListPushRemoteMirrors: %v", err)
	}
	if len(mirrors) != 1 || mirrors[0].RemoteID != "17" || mirrors[0].URL != "https://mirror.example.invalid/example-owner/example-repo.git" {
		t.Fatalf("mirrors=%#v", mirrors)
	}
}

func TestListPushRemoteMirrorsEmptyList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	client := newTestClient(t, server.URL, Config{Token: "test-token"})
	mirrors, err := client.ListPushRemoteMirrors(context.Background(), PushMirrorListRequest{Owner: "example-owner", Repo: "example-repo"})
	if err != nil {
		t.Fatalf("ListPushRemoteMirrors: %v", err)
	}
	if len(mirrors) != 0 {
		t.Fatalf("mirrors=%#v want empty", mirrors)
	}
}

func TestListPushRemoteMirrorsErrorsAreTyped(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		check  func(error) bool
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, body: `{"message":"unauthorized"}`, check: func(err error) bool {
			var target ErrAuthExpired
			return errors.As(err, &target)
		}},
		{name: "forbidden", status: http.StatusForbidden, body: `{"message":"forbidden"}`, check: func(err error) bool {
			var target ErrForbidden
			return errors.As(err, &target)
		}},
		{name: "malformed", status: http.StatusOK, body: `[{"id":`, check: func(err error) bool {
			var target ErrPartialResponse
			return errors.As(err, &target)
		}},
		{name: "html-login", status: http.StatusOK, body: `<!doctype html><title>login</title>`, check: func(err error) bool {
			var target ErrPartialResponse
			return errors.As(err, &target)
		}},
		{name: "invalid-timestamp", status: http.StatusOK, body: `[{"id":1,"url":"https://mirror.example.invalid/repo.git","last_update_at":"not-a-time"}]`, check: func(err error) bool {
			var target ErrPartialResponse
			return errors.As(err, &target)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			client := newTestClient(t, server.URL, Config{Token: "test-token"})
			_, err := client.ListPushRemoteMirrors(context.Background(), PushMirrorListRequest{Owner: "example-owner", Repo: "example-repo"})
			if err == nil || !test.check(err) {
				t.Fatalf("error=%T %v", err, err)
			}
		})
	}
}

func TestListPushRemoteMirrorsInvalidDestinationIsRedacted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"id":1,"project_id":2,"url":"not a safe remote"}]`))
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{Token: "test-token"})
	mirrors, err := client.ListPushRemoteMirrors(context.Background(), PushMirrorListRequest{Owner: "example-owner", Repo: "example-repo"})
	if err != nil {
		t.Fatalf("ListPushRemoteMirrors: %v", err)
	}
	if len(mirrors) != 1 || mirrors[0].URL != redactedPushMirrorDestination {
		t.Fatalf("mirrors=%#v", mirrors)
	}
}
