package adminhttp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestSessionExchangeIsOneTimeAndProtectsReadiness(t *testing.T) {
	assets := fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>admin</title>")}}
	c := New(Config{Assets: assets, Readiness: func(context.Context) Readiness {
		return Readiness{Version: "v1.2.3", CacheConnected: true, SchemaVersion: 17}
	}})
	launch := "one-time-launch-material"
	c.launch[sha256.Sum256([]byte(launch))] = time.Now().Add(time.Minute)
	handler := c.handler()

	unauthorized := request(t, handler, http.MethodGet, "/api/admin/v1/readiness", nil, nil)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized readiness status=%d", unauthorized.Code)
	}

	body, _ := json.Marshal(map[string]string{"launch_token": launch})
	exchange := request(t, handler, http.MethodPost, "/api/admin/v1/session", body, map[string]string{"Origin": "http://127.0.0.1:8080", "Sec-Fetch-Site": "same-origin"})
	if exchange.Code != http.StatusCreated {
		t.Fatalf("exchange status=%d body=%s", exchange.Code, exchange.Body.String())
	}
	if exchange.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("session exchange cache policy=%q", exchange.Header().Get("Cache-Control"))
	}
	cookie := exchange.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie security=%+v", cookie)
	}
	var sessionPayload map[string]string
	if err := json.Unmarshal(exchange.Body.Bytes(), &sessionPayload); err != nil {
		t.Fatal(err)
	}

	replay := request(t, handler, http.MethodPost, "/api/admin/v1/session", body, map[string]string{"Origin": "http://127.0.0.1:8080"})
	if replay.Code != http.StatusUnauthorized {
		t.Fatalf("replay status=%d", replay.Code)
	}

	readyReq := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/admin/v1/readiness", nil)
	readyReq.AddCookie(cookie)
	ready := httptest.NewRecorder()
	handler.ServeHTTP(ready, readyReq)
	if ready.Code != http.StatusOK || !strings.Contains(ready.Body.String(), `"version":"v1.2.3"`) {
		t.Fatalf("readiness status=%d body=%s", ready.Code, ready.Body.String())
	}
	if ready.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("readiness cache policy=%q", ready.Header().Get("Cache-Control"))
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "http://127.0.0.1:8080/api/admin/v1/session", nil)
	deleteReq.Header.Set("Origin", "http://127.0.0.1:8080")
	deleteReq.AddCookie(cookie)
	denied := httptest.NewRecorder()
	handler.ServeHTTP(denied, deleteReq)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("missing csrf status=%d", denied.Code)
	}
	deleteReq = httptest.NewRequest(http.MethodDelete, "http://127.0.0.1:8080/api/admin/v1/session", nil)
	deleteReq.Header.Set("Origin", "http://127.0.0.1:8080")
	deleteReq.Header.Set("X-CSRF-Token", sessionPayload["csrf_token"])
	deleteReq.AddCookie(cookie)
	deleted := httptest.NewRecorder()
	handler.ServeHTTP(deleted, deleteReq)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
}

func TestBrowserSecurityAndStaticCachePolicy(t *testing.T) {
	assets := fstest.MapFS{
		"index.html":                         {Data: []byte("index")},
		"_app/immutable/assets/app.hash.css": {Data: []byte("body{}")},
	}
	c := New(Config{Assets: assets})
	handler := c.handler()

	badHost := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	badHost.Host = "example.com"
	badHostResult := httptest.NewRecorder()
	handler.ServeHTTP(badHostResult, badHost)
	if badHostResult.Code != http.StatusBadRequest {
		t.Fatalf("bad host status=%d", badHostResult.Code)
	}

	index := request(t, handler, http.MethodGet, "/route/without/extension", nil, nil)
	if index.Code != http.StatusOK || index.Header().Get("Cache-Control") != "no-cache, no-store, must-revalidate" {
		t.Fatalf("fallback status=%d cache=%q", index.Code, index.Header().Get("Cache-Control"))
	}
	if index.Header().Get("Content-Security-Policy") != "frame-ancestors 'none'" || index.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("security headers=%v", index.Header())
	}

	immutable := request(t, handler, http.MethodGet, "/_app/immutable/assets/app.hash.css", nil, nil)
	if immutable.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("immutable cache=%q", immutable.Header().Get("Cache-Control"))
	}

	if err := validateBind("0.0.0.0:0", false); err == nil {
		t.Fatal("non-loopback bind accepted without unsafe override")
	}
	if err := validateBind("0.0.0.0:0", true); err != nil {
		t.Fatalf("unsafe override rejected: %v", err)
	}
	unsafeController := New(Config{Assets: assets, AllowNonLoopback: true})
	if !unsafeController.validHost("operator.example:8080") {
		t.Fatal("unsafe override did not permit a non-loopback host")
	}
}

func request(t *testing.T, handler http.Handler, method, target string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "http://127.0.0.1:8080"+target, bytes.NewReader(body))
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, req)
	return result
}
