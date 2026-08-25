package adminhttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestAdminControlsRequireSessionOriginCSRFAndStrictIntent(t *testing.T) {
	maintenanceCalls := 0
	bindingCalls := 0
	c := New(Config{
		Assets: fstest.MapFS{"index.html": {Data: []byte("index")}},
		PlanMaintenance: func(_ context.Context, req MaintenanceControlRequest) (any, error) {
			maintenanceCalls++
			return map[string]any{"plan_id": "plan-1", "cache_ref": req.CacheRef}, nil
		},
		ApplyBinding: func(_ context.Context, req BindingControlRequest) (any, error) {
			bindingCalls++
			return map[string]any{"outcome": "added", "repo_id": req.RepoID}, nil
		},
	})
	cookie := authorizeObservationController(c, time.Now().Add(time.Hour))
	handler := c.handler()
	validHeaders := map[string]string{"Origin": "http://127.0.0.1:8080", "Sec-Fetch-Site": "same-origin", "X-CSRF-Token": "csrf"}
	call := func(path, body string, withCookie bool, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080"+path, strings.NewReader(body))
		if withCookie {
			req.AddCookie(cookie)
		}
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, req)
		return result
	}
	if got := call("/api/admin/v1/maintenance/plan", `{"cache_ref":"cache-a","repo_id":"owner/repo"}`, false, validHeaders); got.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d body=%s", got.Code, got.Body.String())
	}
	if got := call("/api/admin/v1/maintenance/plan", `{"cache_ref":"cache-a","repo_id":"owner/repo"}`, true, map[string]string{"Origin": "http://attacker.example", "X-CSRF-Token": "csrf"}); got.Code != http.StatusForbidden {
		t.Fatalf("origin status=%d body=%s", got.Code, got.Body.String())
	}
	if got := call("/api/admin/v1/maintenance/plan", `{"cache_ref":"cache-a","repo_id":"owner/repo","cache_path":"/private/cache.db"}`, true, validHeaders); got.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status=%d body=%s", got.Code, got.Body.String())
	}
	if got := call("/api/admin/v1/maintenance/plan", `{"cache_ref":"cache-a","repo_id":"owner/repo"}{}`, true, validHeaders); got.Code != http.StatusBadRequest {
		t.Fatalf("trailing payload status=%d body=%s", got.Code, got.Body.String())
	}
	if got := call("/api/admin/v1/maintenance/plan", `{"cache_ref":"cache-a","repo_id":"owner/repo"}`, true, validHeaders); got.Code != http.StatusOK {
		t.Fatalf("plan status=%d body=%s", got.Code, got.Body.String())
	}
	if got := call("/api/admin/v1/bindings/apply", `{"cache_ref":"cache-a","repo_id":"owner/repo","plan_id":"plan-1"}`, true, validHeaders); got.Code != http.StatusBadRequest {
		t.Fatalf("missing key status=%d body=%s", got.Code, got.Body.String())
	}
	if got := call("/api/admin/v1/bindings/apply", `{"cache_ref":"cache-a","repo_id":"owner/repo","plan_id":"plan-1","idempotency_key":"key-1"}`, true, validHeaders); got.Code != http.StatusOK {
		t.Fatalf("apply status=%d body=%s", got.Code, got.Body.String())
	}
	if maintenanceCalls != 1 || bindingCalls != 1 {
		t.Fatalf("provider calls maintenance=%d binding=%d", maintenanceCalls, bindingCalls)
	}
}

func TestAdminControlTypedErrorsUnavailableAndPathTargetWins(t *testing.T) {
	registration := ""
	c := New(Config{
		Assets: fstest.MapFS{"index.html": {Data: []byte("index")}},
		DisableMaintenance: func(_ context.Context, req RegistrationControlRequest) (any, error) {
			registration = req.RegistrationID
			return nil, ControlError{Status: http.StatusConflict, Code: "stale_plan", Message: "stale", Remediation: "replan"}
		},
	})
	cookie := authorizeObservationController(c, time.Now().Add(time.Hour))
	handler := c.handler()
	call := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080"+path, strings.NewReader(body))
		req.AddCookie(cookie)
		req.Header.Set("Origin", "http://127.0.0.1:8080")
		req.Header.Set("X-CSRF-Token", "csrf")
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, req)
		return result
	}
	if got := call("/api/admin/v1/maintenance/reg-path/disable", `{"registration_id":"body-target","idempotency_key":"key"}`); got.Code != http.StatusBadRequest {
		t.Fatalf("body target status=%d body=%s", got.Code, got.Body.String())
	}
	got := call("/api/admin/v1/maintenance/reg-path/disable", `{"idempotency_key":"key"}`)
	if got.Code != http.StatusConflict || !strings.Contains(got.Body.String(), `"code":"stale_plan"`) || registration != "reg-path" {
		t.Fatalf("typed status=%d registration=%q body=%s", got.Code, registration, got.Body.String())
	}
	if got := call("/api/admin/v1/maintenance/reg-path/reconcile", `{"idempotency_key":"key-2"}`); got.Code != http.StatusNotImplemented || !strings.Contains(got.Body.String(), `"code":"capability_unavailable"`) {
		t.Fatalf("unavailable status=%d body=%s", got.Code, got.Body.String())
	}
}
