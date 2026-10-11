package adminhttp

import (
	"context"
	"net/http"
	"strings"
	"time"

	"gitcode-mcp/internal/observability"
)

// Classification uses registered router patterns, never request coordinates.
// Every return is a member of the fixed metric catalog, including fallbacks.
func adminMetricRoute(pattern string, r *http.Request) string {
	switch pattern {
	case "POST /api/admin/v1/session", "GET /api/admin/v1/session", "DELETE /api/admin/v1/session":
		return "session"
	case "GET /api/admin/v1/readiness", "GET /api/admin/v1/snapshot", "GET /api/admin/v1/caches", "GET /api/admin/v1/caches/{cache_ref}",
		"GET /api/admin/v1/caches/{cache_ref}/repositories/{repo_id...}", "GET /api/admin/v1/jobs", "GET /api/admin/v1/jobs/{job_id}",
		"GET /api/admin/v1/maintenance", "GET /api/admin/v1/diagnostics", "GET /api/admin/v1/capabilities",
		"GET /api/admin/v1/observability", "GET /api/admin/v1/observability/metrics", "GET /api/admin/v1/observability/events":
		return "read"
	case "POST /api/admin/v1/search/compare", "POST /api/admin/v1/repository-docs/{registration_id}/search":
		return "search"
	case "POST /api/admin/v1/jobs/{job_id}/cancel", "POST /api/admin/v1/jobs/{job_id}/retry", "POST /api/admin/v1/maintenance/plan", "POST /api/admin/v1/maintenance/apply",
		"POST /api/admin/v1/maintenance/{registration_id}/disable", "POST /api/admin/v1/maintenance/{registration_id}/reconcile",
		"POST /api/admin/v1/maintenance/{registration_id}/conflict-resolution/plan", "POST /api/admin/v1/maintenance/{registration_id}/conflict-resolution/apply",
		"POST /api/admin/v1/bindings/plan", "POST /api/admin/v1/bindings/apply", "POST /api/admin/v1/feedback/setup/plan", "POST /api/admin/v1/feedback/setup/apply",
		"POST /api/admin/v1/repository-docs/{registration_id}/plan", "POST /api/admin/v1/repository-docs/{registration_id}/index",
		"POST /api/admin/v1/rag/provider/smoke", "POST /api/admin/v1/rag/repair/plan", "POST /api/admin/v1/rag/repair/apply":
		return "control"
	case "GET /api/admin/v1/events":
		return "events"
	case "/":
		if !strings.HasPrefix(r.URL.Path, "/api/admin/") {
			return "assets"
		}
	}
	return "unknown"
}

type adminRouteKey struct{}
type adminRouteObservation struct {
	class           string
	observationRead bool
}

// Only registered observation GET patterns are excluded. A user-entered path
// with this prefix is not a telemetry read until the mux actually registers it.
func adminObservationRead(pattern string) bool {
	switch pattern {
	case "GET /api/admin/v1/observability", "GET /api/admin/v1/observability/metrics", "GET /api/admin/v1/observability/events":
		return true
	}
	return false
}

func observeAdminRoute(mux *http.ServeMux, r *http.Request) {
	if state, ok := r.Context().Value(adminRouteKey{}).(*adminRouteObservation); ok {
		// ServeMux.Handler exposes the registered pattern on the supported Go
		// baseline, unlike Request.Pattern (introduced in Go 1.23). Routing and
		// PathValue assignment still belong to the unchanged ServeHTTP call.
		_, pattern := mux.Handler(r)
		state.class = adminMetricRoute(pattern, r)
		state.observationRead = adminObservationRead(pattern)
	}
}

type metricResponse struct {
	http.ResponseWriter
	status int
	failed bool
}

func (w *metricResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *metricResponse) WriteHeader(status int) {
	if w.status == 0 && (status >= 200 || status == http.StatusSwitchingProtocols) {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *metricResponse) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(body)
	w.failed = w.failed || err != nil || n != len(body)
	return n, err
}

// Preserve optional streaming support: a non-Flusher must remain one. The
// Unwrap chain also preserves ResponseController operations on the writer.
type metricFlusher struct {
	*metricResponse
}

func (w *metricFlusher) Flush() {
	// The void Flusher contract still discards its return value, but retain
	// any error for the completed handler outcome before cancellation wins.
	_ = w.FlushError()
}

func (w *metricFlusher) FlushError() error {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	err := http.NewResponseController(w.ResponseWriter).Flush()
	w.failed = w.failed || err != nil
	return err
}

func (c *Controller) observeRequests(next http.Handler) http.Handler {
	metrics := c.cfg.Metrics
	collector := c.cfg.Observation
	if metrics == nil && collector == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		var correlation observability.Ref
		if collector != nil {
			correlation = collector.AdminRequestCorrelation()
		}
		routeState := &adminRouteObservation{class: "unknown"}
		ctx := observability.WithMetrics(r.Context(), metrics)
		r = r.WithContext(context.WithValue(ctx, adminRouteKey{}, routeState))
		observed := &metricResponse{ResponseWriter: w}
		var writer http.ResponseWriter = observed
		if _, ok := w.(http.Flusher); ok {
			writer = &metricFlusher{metricResponse: observed}
		}
		returned := false
		defer func() {
			outcome := "succeeded"
			switch {
			case !returned || observed.failed || observed.status >= 500:
				outcome = "failed"
			case r.Context().Err() != nil:
				outcome = "cancelled"
			case observed.status >= 400:
				outcome = "rejected"
			}
			route := routeState.class
			if metrics != nil {
				metrics.Add("admin_requests", map[string]string{"route": route, "outcome": outcome}, 1)
				metrics.Observe("admin_duration", map[string]string{"route": route}, time.Since(started).Seconds())
			}
			if collector != nil && !routeState.observationRead {
				collector.Emit(observability.AdminRequestCompleted, observability.Context{Correlation: correlation,
					ExecutionDetails: observability.ExecutionDetails{Phase: "admin", AdminRoute: route, Outcome: outcome}})
			}
		}()
		next.ServeHTTP(writer, r)
		returned = true
	})
}

// SSEConnectionsProbe is an atomic in-memory observation. It takes no session,
// event, job or cache locks and never invokes a provider or maintenance action.
func (c *Controller) SSEConnectionsProbe() observability.ProbeValue {
	if c == nil {
		return observability.Unavailable()
	}
	return observability.Available(float64(c.sseActive.Load()))
}
