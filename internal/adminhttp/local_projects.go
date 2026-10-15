package adminhttp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

// This transport cannot select storage or construct a provider. The existing
// local host supplies typed, closed decoding and the shared service operations.
type LocalProjectProvider func(context.Context, string, json.RawMessage) (any, error)

func (c *Controller) localProjectStatus(w http.ResponseWriter, r *http.Request) {
	c.serveLocalProject(w, r, "status", false)
}

func (c *Controller) localProjectRequest(operation string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { c.serveLocalProject(w, r, operation, true) }
}

func (c *Controller) serveLocalProject(w http.ResponseWriter, r *http.Request, operation string, post bool) {
	session, ok := c.authenticate(r)
	if !ok {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "Admin session required.", "Run gitcode-mcp admin open.")
		return
	}
	if post && (!c.validMutationOrigin(r) || !subtleEqual(r.Header.Get("X-CSRF-Token"), session.CSRF)) {
		writeAPIError(w, http.StatusForbidden, "csrf_validation_failed", "Same-origin CSRF validation failed.", "Reload the Admin session.")
		return
	}
	if c.cfg.LocalProjects == nil {
		writeAPIError(w, http.StatusNotImplemented, "capability_unavailable", "Local projects are unavailable in this daemon.", "Use gitcode-mcp project status with the local configuration.")
		return
	}
	if len(r.URL.Query()) != 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "Local project requests accept no URL selectors.", "Use the typed request body.")
		return
	}
	raw := []byte("{}")
	if post {
		var err error
		raw, err = io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "local_git_limit_exceeded", "The bounded local request could not be read.", "Reduce the request; preserve the same key after an ambiguous write.")
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	result, err := c.cfg.LocalProjects(ctx, operation, raw)
	if err != nil {
		var failure ControlError
		if errors.As(err, &failure) {
			writeControlError(w, failure)
		} else {
			writeAPIError(w, http.StatusServiceUnavailable, "local_git_unavailable", "Local project operation is unavailable.", "Retain draft and the same confirmed key; inspect project status before retrying.")
		}
		return
	}
	body, err := json.Marshal(map[string]any{"api_version": observationAPIVersion, "result": result})
	if err != nil || len(body) > 64<<20 {
		writeAPIError(w, http.StatusServiceUnavailable, "local_git_limit_exceeded", "Local response exceeds its bound.", "Request a smaller page; preserve the same key after an ambiguous write.")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
