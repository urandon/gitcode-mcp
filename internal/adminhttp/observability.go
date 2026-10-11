package adminhttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"gitcode-mcp/internal/observability"
)

func (c *Controller) observationReadGuard(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Observation requires GET.", "")
		return false
	}
	if !c.requireSession(w, r) {
		return false
	}
	site := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")))
	if site != "" && site != "same-origin" && site != "none" {
		writeAPIError(w, http.StatusForbidden, "origin_forbidden", "Observation requires same-origin access.", "")
		return false
	}
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" && !c.validMutationOrigin(r) {
		writeAPIError(w, http.StatusForbidden, "origin_forbidden", "Observation requires same-origin access.", "")
		return false
	}
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			writeAPIError(w, http.StatusForbidden, "origin_forbidden", "Observation requires same-origin access.", "")
			return false
		}
	}
	if r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid_query", "Observation GET accepts no body.", "")
		return false
	}
	return true
}

func observationQuery(r *http.Request) ([]byte, error) {
	invalid := observability.QueryError{Code: "invalid_query", Reason: "unsupported_request"}
	if len(r.URL.RawQuery) > observability.MaxReadRequestBytes {
		return nil, invalid
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, invalid
	}
	params := map[string]any{}
	for key, entries := range values {
		if len(entries) != 1 {
			return nil, invalid
		}
		if key == "limit" || key == "history_minutes" {
			value, err := strconv.Atoi(entries[0])
			if err != nil || entries[0] != strconv.Itoa(value) {
				return nil, invalid
			}
			params[key] = value
		} else {
			params[key] = entries[0]
		}
	}
	return json.Marshal(params)
}

func (c *Controller) getObservability(w http.ResponseWriter, r *http.Request) {
	c.readObservability(w, r, "summary")
}
func (c *Controller) getObservabilityMetrics(w http.ResponseWriter, r *http.Request) {
	c.readObservability(w, r, "metrics")
}
func (c *Controller) getObservabilityEvents(w http.ResponseWriter, r *http.Request) {
	c.readObservability(w, r, "events")
}

func (c *Controller) readObservability(w http.ResponseWriter, r *http.Request, kind string) {
	ctx, cancel := context.WithTimeout(r.Context(), observability.ReadBudget)
	defer cancel()
	deadline, _ := ctx.Deadline()
	_ = http.NewResponseController(w).SetWriteDeadline(deadline)
	defer http.NewResponseController(w).SetWriteDeadline(time.Time{})
	if !c.observationReadGuard(w, r) {
		return
	}
	raw, err := observationQuery(r)
	if err != nil {
		writeObservationError(w, err)
		return
	}
	var value any
	switch kind {
	case "summary":
		err = observability.DecodeReadRequest(raw, &struct{}{})
		if err == nil {
			value, err = c.cfg.Observability.Summary(ctx)
		}
	case "metrics":
		var req observability.MetricsRequest
		req, err = observability.DecodeMetricsRequest(raw)
		if err == nil {
			value, err = c.cfg.Observability.MetricSnapshot(ctx, req)
		}
	case "events":
		var req observability.EventsRequest
		req, err = observability.DecodeEventsRequest(raw)
		if err == nil {
			value, err = c.cfg.Observability.Events(ctx, req)
		}
	}
	if err != nil {
		writeObservationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func writeObservationError(w http.ResponseWriter, err error) {
	status := http.StatusServiceUnavailable
	q := observability.QueryError{Code: "observation_unavailable", Reason: "reader_unavailable"}
	if errors.As(err, &q) {
		switch q.Code {
		case "invalid_query", "invalid_cursor":
			status = http.StatusBadRequest
		case "snapshot_required":
			status = http.StatusConflict
		}
	} else if errors.Is(err, context.Canceled) {
		q = observability.QueryError{Code: "observation_cancelled", Reason: "request_cancelled"}
	} else if errors.Is(err, context.DeadlineExceeded) {
		status = http.StatusGatewayTimeout
		q = observability.QueryError{Code: "observation_timeout", Reason: "read_budget"}
	}
	writeJSON(w, status, map[string]any{"error": q})
}

// Compact invalidation only, from an owned poller joined at daemon shutdown.
// This intentionally bypasses the broad Admin/cache/feedback snapshot provider.
func (c *Controller) pollObservability(ctx context.Context) {
	previous := ""
	for {
		if ctx.Err() != nil {
			return
		}
		if revision, err := c.cfg.Observability.Revision(ctx); err == nil && revision != previous {
			c.events.append(Event{Kind: "observability_changed", EntityType: "observability", EntityID: "daemon", Revision: revision})
			previous = revision
		}
		// Measure from completion of the actual publication. A fixed ticker can
		// retain a tick while append is delayed and burst on the next iteration.
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
