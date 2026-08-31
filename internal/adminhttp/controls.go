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
	RegistrationID               string `json:"-"`
	SourceRegistrationID         string `json:"source_registration_id,omitempty"`
	SourceRegistrationGeneration int64  `json:"source_registration_generation,omitempty"`
	IdempotencyKey               string `json:"idempotency_key"`
}

type MaintenanceConflictResolutionRequest struct {
	RegistrationID     string `json:"-"`
	CandidateRef       string `json:"candidate_ref"`
	ExpectedGeneration int64  `json:"expected_generation"`
	PlanID             string `json:"plan_id,omitempty"`
	IdempotencyKey     string `json:"idempotency_key,omitempty"`
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

type SearchCompareRequest struct {
	CacheRef   string `json:"cache_ref"`
	RepoID     string `json:"repo_id"`
	Query      string `json:"query"`
	Kind       string `json:"kind,omitempty"`
	Provenance string `json:"provenance,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

// RepositoryDocsSearchRequest deliberately carries no filesystem path. The
// daemon resolves local Git and cache authority from its private maintenance
// registration after authenticating the loopback Admin session.
type RepositoryDocsSearchRequest struct {
	RegistrationID               string `json:"-"`
	SourceRegistrationID         string `json:"source_registration_id,omitempty"`
	SourceRegistrationGeneration int64  `json:"source_registration_generation,omitempty"`
	Query                        string `json:"query"`
	Revision                     string `json:"revision,omitempty"`
	Mode                         string `json:"mode,omitempty"`
	Limit                        int    `json:"limit,omitempty"`
	IncludeWorktree              bool   `json:"include_worktree,omitempty"`
}

type RepositoryDocsPlanRequest struct {
	RegistrationID               string `json:"-"`
	SourceRegistrationID         string `json:"source_registration_id,omitempty"`
	SourceRegistrationGeneration int64  `json:"source_registration_generation,omitempty"`
	Revision                     string `json:"revision,omitempty"`
	IncludeWorktree              bool   `json:"include_worktree,omitempty"`
}

type ProviderSmokeRequest struct {
	CacheRef string `json:"cache_ref"`
	RepoID   string `json:"repo_id"`
	Profile  string `json:"profile,omitempty"`
}

type RAGRepairRequest struct {
	CacheRef       string `json:"cache_ref"`
	RepoID         string `json:"repo_id"`
	Profile        string `json:"profile,omitempty"`
	MaxChunks      int    `json:"max_chunks,omitempty"`
	PlanID         string `json:"plan_id,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

type MaintenanceControlProvider func(context.Context, MaintenanceControlRequest) (any, error)
type RegistrationControlProvider func(context.Context, RegistrationControlRequest) (any, error)
type MaintenanceConflictResolutionProvider func(context.Context, MaintenanceConflictResolutionRequest) (any, error)
type BindingControlProvider func(context.Context, BindingControlRequest) (any, error)
type SearchCompareProvider func(context.Context, SearchCompareRequest) (any, error)
type RepositoryDocsSearchProvider func(context.Context, RepositoryDocsSearchRequest) (any, error)
type RepositoryDocsPlanProvider func(context.Context, RepositoryDocsPlanRequest) (any, error)
type ProviderSmokeProvider func(context.Context, ProviderSmokeRequest) (any, error)
type RAGRepairProvider func(context.Context, RAGRepairRequest) (any, error)

type ControlError struct {
	Code        string
	Message     string
	Remediation string
	Field       string
	Blockers    []string
	CLIHandoff  string
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

func (c *Controller) planMaintenanceConflictResolution(w http.ResponseWriter, r *http.Request) {
	req := MaintenanceConflictResolutionRequest{RegistrationID: strings.TrimSpace(r.PathValue("registration_id"))}
	applyControl(c, w, r, c.cfg.PlanMaintenanceConflictResolution, req, false)
}

func (c *Controller) applyMaintenanceConflictResolution(w http.ResponseWriter, r *http.Request) {
	req := MaintenanceConflictResolutionRequest{RegistrationID: strings.TrimSpace(r.PathValue("registration_id"))}
	applyControl(c, w, r, c.cfg.ApplyMaintenanceConflictResolution, req, true)
}

func (c *Controller) planBinding(w http.ResponseWriter, r *http.Request) {
	applyControl(c, w, r, c.cfg.PlanBinding, BindingControlRequest{}, false)
}

func (c *Controller) applyBinding(w http.ResponseWriter, r *http.Request) {
	applyControl(c, w, r, c.cfg.ApplyBinding, BindingControlRequest{}, true)
}

func (c *Controller) compareSearch(w http.ResponseWriter, r *http.Request) {
	applyControl(c, w, r, c.cfg.CompareSearch, SearchCompareRequest{}, false)
}

func (c *Controller) searchRepositoryDocs(w http.ResponseWriter, r *http.Request) {
	req := RepositoryDocsSearchRequest{RegistrationID: strings.TrimSpace(r.PathValue("registration_id"))}
	applyControl(c, w, r, c.cfg.SearchRepositoryDocs, req, false)
}

func (c *Controller) planRepositoryDocs(w http.ResponseWriter, r *http.Request) {
	req := RepositoryDocsPlanRequest{RegistrationID: strings.TrimSpace(r.PathValue("registration_id"))}
	applyControl(c, w, r, c.cfg.PlanRepositoryDocs, req, false)
}

func (c *Controller) indexRepositoryDocs(w http.ResponseWriter, r *http.Request) {
	req := RegistrationControlRequest{RegistrationID: strings.TrimSpace(r.PathValue("registration_id"))}
	applyControl(c, w, r, c.cfg.IndexRepositoryDocs, req, true)
}

func (c *Controller) smokeProvider(w http.ResponseWriter, r *http.Request) {
	applyControl(c, w, r, c.cfg.SmokeProvider, ProviderSmokeRequest{}, false)
}

func (c *Controller) planRAGRepair(w http.ResponseWriter, r *http.Request) {
	applyControl(c, w, r, c.cfg.PlanRAGRepair, RAGRepairRequest{}, false)
}

func (c *Controller) applyRAGRepair(w http.ResponseWriter, r *http.Request) {
	applyControl(c, w, r, c.cfg.ApplyRAGRepair, RAGRepairRequest{}, true)
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
			writeControlError(w, typed)
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "control_failed", "The requested control could not be completed.", "Refresh current state and render a new plan.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"api_version": observationAPIVersion, "result": result})
}

func writeControlError(w http.ResponseWriter, failure ControlError) {
	body := map[string]any{
		"code": failure.Code, "message": failure.Message, "remediation": failure.Remediation,
	}
	if failure.Field != "" {
		body["field"] = failure.Field
	}
	if len(failure.Blockers) > 0 {
		body["blockers"] = failure.Blockers
	}
	if failure.CLIHandoff != "" {
		body["cli_handoff"] = failure.CLIHandoff
	}
	writeJSON(w, failure.Status, map[string]any{"error": body})
}

func controlIdempotencyKey(value any) string {
	switch req := value.(type) {
	case MaintenanceControlRequest:
		return strings.TrimSpace(req.IdempotencyKey)
	case RegistrationControlRequest:
		return strings.TrimSpace(req.IdempotencyKey)
	case MaintenanceConflictResolutionRequest:
		return strings.TrimSpace(req.IdempotencyKey)
	case BindingControlRequest:
		return strings.TrimSpace(req.IdempotencyKey)
	case RAGRepairRequest:
		return strings.TrimSpace(req.IdempotencyKey)
	default:
		return ""
	}
}
