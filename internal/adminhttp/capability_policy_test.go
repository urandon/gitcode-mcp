package adminhttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/capability"
)

func TestCapabilityPolicyAdminRejectsBeforeBodyOrObservation(t *testing.T) {
	p, err := capability.CompilePolicy(false, capability.PolicyLayer{Source: "machine", Specification: &capability.Specification{Ceiling: capability.Rule{Allow: []string{}}}})
	if err != nil {
		t.Fatal(err)
	}
	c := New(Config{Policy: p, Snapshot: func(context.Context) (ObservationSnapshot, error) {
		t.Fatal("restricted snapshot probed operator state")
		return ObservationSnapshot{}, nil
	}, ApplyBinding: func(context.Context, BindingControlRequest) (any, error) {
		t.Fatal("denied control ran")
		return nil, nil
	}})
	cookie := authorizeObservationController(c, time.Now().Add(time.Hour))
	h := c.handler()
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://127.0.0.1:8080"+path, strings.NewReader(body))
		req.AddCookie(cookie)
		req.Header.Set("Origin", "http://127.0.0.1:8080")
		req.Header.Set("X-CSRF-Token", "csrf")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	for _, path := range []string{"/api/admin/v1/bindings/apply", "/api/admin/v1/search/compare", "/api/admin/v1/local-projects/mutation-apply"} {
		w := call(http.MethodPost, path, `"invalid-private-input"`)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "capability_policy_denied") || strings.Contains(w.Body.String(), "invalid-private-input") {
			t.Fatal("denial did not precede body decoding")
		}
	}
	for _, path := range []string{"/api/admin/v1/caches", "/api/admin/v1/jobs", "/api/admin/v1/events"} {
		if w := call(http.MethodGet, path, ""); w.Code != http.StatusForbidden {
			t.Fatalf("unguarded %s", path)
		}
	}
	w := call(http.MethodGet, "/api/admin/v1/snapshot", "")
	var snapshot ObservationSnapshot
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &snapshot) != nil || !snapshot.PolicyRestricted || len(snapshot.Caches) != 0 || snapshot.Policy == nil {
		t.Fatal("restricted snapshot is not explicit")
	}
	w = call(http.MethodGet, "/api/admin/v1/policy", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), p.Fingerprint()) {
		t.Fatal("content-free policy unavailable")
	}
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/admin/v1/policy", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatal("policy view is not session protected")
	}
}
