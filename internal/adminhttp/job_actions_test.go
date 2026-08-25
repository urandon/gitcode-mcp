package adminhttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestJobActionsRequireSessionOriginCSRFAndIdempotency(t *testing.T) {
	called := 0
	provider := func(_ context.Context, req JobActionRequest) (JobActionReceipt, error) {
		called++
		return JobActionReceipt{ReceiptID: "receipt-1", Action: "cancel", TargetJob: req.JobID, ResultJob: req.JobID, Outcome: "cancelled", JobStatus: "cancelled", CreatedAt: time.Now().UTC()}, nil
	}
	c := New(Config{Assets: fstest.MapFS{"index.html": {Data: []byte("index")}}, CancelJob: provider})
	cookie := authorizeObservationController(c, time.Now().Add(time.Hour))
	handler := c.handler()

	sessionReq := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/api/admin/v1/session", nil)
	sessionReq.AddCookie(cookie)
	sessionResult := httptest.NewRecorder()
	handler.ServeHTTP(sessionResult, sessionReq)
	if sessionResult.Code != http.StatusOK || !strings.Contains(sessionResult.Body.String(), `"csrf_token":"csrf"`) {
		t.Fatalf("session status=%d body=%s", sessionResult.Code, sessionResult.Body.String())
	}

	assertActionStatus := func(name string, withCookie bool, headers map[string]string, body string, want int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/admin/v1/jobs/job-1/cancel", strings.NewReader(body))
		if withCookie {
			req.AddCookie(cookie)
		}
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, req)
		if result.Code != want {
			t.Fatalf("%s status=%d body=%s", name, result.Code, result.Body.String())
		}
	}
	validHeaders := map[string]string{"Origin": "http://127.0.0.1:8080", "Sec-Fetch-Site": "same-origin", "X-CSRF-Token": "csrf"}
	assertActionStatus("unauthorized", false, validHeaders, `{"idempotency_key":"key-1"}`, http.StatusUnauthorized)
	assertActionStatus("origin", true, map[string]string{"Origin": "http://attacker.example", "X-CSRF-Token": "csrf"}, `{"idempotency_key":"key-1"}`, http.StatusForbidden)
	assertActionStatus("csrf", true, map[string]string{"Origin": "http://127.0.0.1:8080", "X-CSRF-Token": "wrong"}, `{"idempotency_key":"key-1"}`, http.StatusForbidden)
	assertActionStatus("idempotency", true, validHeaders, `{}`, http.StatusBadRequest)
	assertActionStatus("accepted", true, validHeaders, `{"idempotency_key":"key-1"}`, http.StatusOK)
	if called != 1 {
		t.Fatalf("provider calls=%d want=1", called)
	}
}

func TestJobActionTypedErrorsAndUnavailableCapability(t *testing.T) {
	c := New(Config{
		Assets: fstest.MapFS{"index.html": {Data: []byte("index")}},
		RetryJob: func(context.Context, JobActionRequest) (JobActionReceipt, error) {
			return JobActionReceipt{}, JobActionError{Status: http.StatusConflict, Code: "job_not_terminal", Message: "terminal required", Remediation: "wait"}
		},
	})
	cookie := authorizeObservationController(c, time.Now().Add(time.Hour))
	handler := c.handler()
	call := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080"+path, strings.NewReader(`{"idempotency_key":"key"}`))
		req.AddCookie(cookie)
		req.Header.Set("Origin", "http://127.0.0.1:8080")
		req.Header.Set("X-CSRF-Token", "csrf")
		result := httptest.NewRecorder()
		handler.ServeHTTP(result, req)
		return result
	}
	retry := call("/api/admin/v1/jobs/job-1/retry")
	if retry.Code != http.StatusConflict || !strings.Contains(retry.Body.String(), `"code":"job_not_terminal"`) {
		t.Fatalf("retry status=%d body=%s", retry.Code, retry.Body.String())
	}
	cancel := call("/api/admin/v1/jobs/job-1/cancel")
	if cancel.Code != http.StatusNotImplemented || !strings.Contains(cancel.Body.String(), `"code":"capability_unavailable"`) {
		t.Fatalf("cancel status=%d body=%s", cancel.Code, cancel.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(retry.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
}
