package adminhttp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

type MaintenanceControlRequest struct {
	CacheRef            string   `json:"cache_ref"`
	RepoID              string   `json:"repo_id"`
	SyncMode            string   `json:"sync_mode,omitempty"`
	Collections         []string `json:"collections,omitempty"`
	RAGMode             string   `json:"rag_mode,omitempty"`
	Profile             string   `json:"profile,omitempty"`
	HeadIntervalSeconds int      `json:"head_interval_seconds,omitempty"`
	RAGIntervalSeconds  int      `json:"rag_interval_seconds,omitempty"`
	HeadMaxPages        int      `json:"head_max_pages,omitempty"`
	TailSlicePages      int      `json:"tail_slice_pages,omitempty"`
	PerPage             int      `json:"per_page,omitempty"`
	PlanID              string   `json:"plan_id,omitempty"`
	IdempotencyKey      string   `json:"idempotency_key,omitempty"`
}

type RegistrationControlRequest struct {
	RegistrationID string `json:"-"`
	IdempotencyKey string `json:"idempotency_key"`
}

type BindingControlRequest struct {
	CacheRef       string   `json:"cache_ref"`
	RepoID         string   `json:"repo_id"`
	Owner          string   `json:"owner,omitempty"`
	Name           string   `json:"name,omitempty"`
	APIBaseURL     string   `json:"api_base_url,omitempty"`
	Scopes         []string `json:"scopes,omitempty"`
	Aliases        []string `json:"aliases,omitempty"`
	DisplayName    *string  `json:"display_name,omitempty"`
	PlanID         string   `json:"plan_id,omitempty"`
	IdempotencyKey string   `json:"idempotency_key,omitempty"`
}

type MaintenanceControlProvider func(context.Context, MaintenanceControlRequest) (any, error)
type RegistrationControlProvider func(context.Context, RegistrationControlRequest) (any, error)
type BindingControlProvider func(context.Context, BindingControlRequest) (any, error)

type ControlError struct {
	Code        string
	Message     string
	Remediation string
	Status      int
}

func (e ControlError) Error() string { return e.Code }

func (c *Controller) planMaintenance(w http.ResponseWriter, r *http.Request) {
	applyControl(c, w, r, c.cfg.PlanMaintenance, MaintenanceControlRequest{}, false)
}

func (c *Controller) applyMaintenance(w http.ResponseWriter, r *http.Request) {
	applyControl(c, w, r, c.cfg.ApplyMaintenance, MaintenanceControlRequest{}, true)
}

func (c *Controller) disableMaintenance(w http.ResponseWriter, r *http.Request) {
	req := RegistrationControlRequest{RegistrationID: strings.TrimSpace(r.PathValue("registration_id"))}
	applyControl(c, w, r, c.cfg.DisableMaintenance, req, true)
}

func (c *Controller) reconcileMaintenance(w http.ResponseWriter, r *http.Request) {
	req := RegistrationControlRequest{RegistrationID: strings.TrimSpace(r.PathValue("registration_id"))}
	applyControl(c, w, r, c.cfg.ReconcileMaintenance, req, true)
}

func (c *Controller) planBinding(w http.ResponseWriter, r *http.Request) {
	applyControl(c, w, r, c.cfg.PlanBinding, BindingControlRequest{}, false)
}

func (c *Controller) applyBinding(w http.ResponseWriter, r *http.Request) {
	applyControl(c, w, r, c.cfg.ApplyBinding, BindingControlRequest{}, true)
}

func applyControl[T any](c *Controller, w http.ResponseWriter, r *http.Request, provider func(context.Context, T) (any, error), req T, requireIdempotency bool) {
	session, ok := c.authenticate(r)
	if !ok {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "Admin session required.", "Run gitcode-mcp admin open.")
		return
	}
	if !c.validMutationOrigin(r) || !subtleEqual(r.Header.Get("X-CSRF-Token"), session.CSRF) {
		writeAPIError(w, http.StatusForbidden, "csrf_validation_failed", "The control request did not pass same-origin CSRF validation.", "Reload the admin page and render a new plan.")
		return
	}
	if provider == nil {
		writeAPIError(w, http.StatusNotImplemented, "capability_unavailable", "This admin control is not available in the running daemon.", "Use the CLI capability catalog for the supported surface.")
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "The control request is not valid.", "Correct the typed fields and render a new plan.")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "The control request contains trailing data.", "Send exactly one JSON object and render a new plan.")
		return
	}
	key := controlIdempotencyKey(req)
	if requireIdempotency && (key == "" || len(key) > 256) {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "idempotency_key is required.", "Generate one key for this confirmed intent.")
		return
	}
	result, err := provider(r.Context(), req)
	if err != nil {
		var typed ControlError
		if errors.As(err, &typed) {
			writeAPIError(w, typed.Status, typed.Code, typed.Message, typed.Remediation)
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "control_failed", "The requested control could not be completed.", "Refresh current state and render a new plan.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"api_version": observationAPIVersion, "result": result})
}

func controlIdempotencyKey(value any) string {
	switch req := value.(type) {
	case MaintenanceControlRequest:
		return strings.TrimSpace(req.IdempotencyKey)
	case RegistrationControlRequest:
		return strings.TrimSpace(req.IdempotencyKey)
	case BindingControlRequest:
		return strings.TrimSpace(req.IdempotencyKey)
	default:
		return ""
	}
}
