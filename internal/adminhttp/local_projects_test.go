package adminhttp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLocalProjectHTTPUnavailableClosedBoundsAndFiniteRoutes(t *testing.T) {
	c := New(Config{})
	c.sessions[sha256.Sum256([]byte("fixture-session"))] = session{Expires: time.Now().Add(time.Minute), CSRF: "fixture-csrf"}
	headers := map[string]string{"Cookie": sessionCookie + "=fixture-session", "Origin": "http://127.0.0.1:8080", "Sec-Fetch-Site": "same-origin", "X-CSRF-Token": "fixture-csrf"}
	for _, operation := range []string{"list", "get", "history", "search", "mutation-plan", "mutation-apply", "index-plan", "index-apply"} {
		got := request(t, c.handler(), "POST", "/api/admin/v1/local-projects/"+operation, []byte("{}"), headers)
		if got.Code != http.StatusNotImplemented || !strings.Contains(got.Body.String(), "capability_unavailable") {
			t.Fatal("unavailable local capability was not closed")
		}
		want := "read"
		if operation == "search" {
			want = "search"
		} else if strings.HasSuffix(operation, "-plan") || strings.HasSuffix(operation, "-apply") {
			want = "control"
		}
		pattern := "POST /api/admin/v1/local-projects/" + operation
		if adminMetricRoute(pattern, httptest.NewRequest("POST", "http://127.0.0.1/", nil)) != want {
			t.Fatal("new local route escaped finite metrics classification")
		}
	}
	calls := 0
	c.cfg.LocalProjects = func(ctx context.Context, operation string, raw json.RawMessage) (any, error) {
		calls++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 10*time.Second {
			t.Fatal("HTTP provider lost its bounded context")
		}
		if operation != "list" || string(raw) != "{}" {
			t.Fatal("HTTP transport changed typed intent")
		}
		return []any{}, nil
	}
	got := request(t, c.handler(), "POST", "/api/admin/v1/local-projects/list", []byte("{}"), headers)
	if got.Code != http.StatusOK || calls != 1 || got.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("bounded local read unavailable")
	}
	for _, target := range []string{"/api/admin/v1/local-projects/list?path=private-marker", "/api/admin/v1/local-projects/list?project=lab"} {
		got := request(t, c.handler(), "POST", target, []byte("{}"), headers)
		if got.Code != http.StatusBadRequest || strings.Contains(got.Body.String(), "private-marker") {
			t.Fatal("URL selector admitted or reflected")
		}
	}
	got = request(t, c.handler(), "POST", "/api/admin/v1/local-projects/list", []byte(strings.Repeat("x", (1<<20)+1)), headers)
	if got.Code != http.StatusBadRequest || calls != 1 {
		t.Fatal("oversized body reached the shared service")
	}
	c.cfg.LocalProjects = func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatal("browser cancellation was not propagated")
		}
		return nil, errors.New("private-provider-marker")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest("POST", "http://127.0.0.1:8080/api/admin/v1/local-projects/list", strings.NewReader("{}")).WithContext(ctx)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	res := httptest.NewRecorder()
	c.handler().ServeHTTP(res, req)
	if res.Code != http.StatusServiceUnavailable || strings.Contains(res.Body.String(), "private-provider-marker") {
		t.Fatal("cancelled callback error was not sanitized")
	}
}
