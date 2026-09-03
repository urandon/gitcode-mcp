package adminhttp

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

type JobActionRequest struct {
	JobID          string `json:"job_id"`
	Collection     string `json:"collection,omitempty"`
	IdempotencyKey string `json:"idempotency_key"`
}

type JobActionReceipt struct {
	ReceiptID string    `json:"receipt_id"`
	Action    string    `json:"action"`
	TargetJob string    `json:"target_job_id"`
	ResultJob string    `json:"result_job_id,omitempty"`
	Outcome   string    `json:"outcome"`
	JobStatus string    `json:"job_status"`
	Replayed  bool      `json:"replayed"`
	CreatedAt time.Time `json:"created_at"`
}

type JobActionProvider func(context.Context, JobActionRequest) (JobActionReceipt, error)

type JobActionError struct {
	Code        string
	Message     string
	Remediation string
	Status      int
}

func (e JobActionError) Error() string { return e.Code }

func (c *Controller) getSession(w http.ResponseWriter, r *http.Request) {
	session, ok := c.authenticate(r)
	if !ok {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "Admin session required.", "Run gitcode-mcp admin open.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"api_version": observationAPIVersion, "csrf_token": session.CSRF})
}

func (c *Controller) cancelJob(w http.ResponseWriter, r *http.Request) {
	c.applyJobAction(w, r, "cancel", c.cfg.CancelJob)
}

func (c *Controller) retryJob(w http.ResponseWriter, r *http.Request) {
	c.applyJobAction(w, r, "retry", c.cfg.RetryJob)
}

func (c *Controller) applyJobAction(w http.ResponseWriter, r *http.Request, action string, provider JobActionProvider) {
	session, ok := c.authenticate(r)
	if !ok {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "Admin session required.", "Run gitcode-mcp admin open.")
		return
	}
	if !c.validMutationOrigin(r) || !subtleEqual(r.Header.Get("X-CSRF-Token"), session.CSRF) {
		writeAPIError(w, http.StatusForbidden, "csrf_validation_failed", "The mutation request did not pass same-origin CSRF validation.", "Reload the admin page and retry the explicit action.")
		return
	}
	if provider == nil {
		writeAPIError(w, http.StatusNotImplemented, "capability_unavailable", "This job action is not available in the running daemon.", "Use the CLI capability catalog for the exact supported surface.")
		return
	}
	var body struct {
		IdempotencyKey string `json:"idempotency_key"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || strings.TrimSpace(body.IdempotencyKey) == "" || len(strings.TrimSpace(body.IdempotencyKey)) > 256 {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "A bounded idempotency_key is required.", "Generate a new action key up to 256 characters and retry once.")
		return
	}
	receipt, err := provider(r.Context(), JobActionRequest{JobID: strings.TrimSpace(r.PathValue("job_id")), IdempotencyKey: strings.TrimSpace(body.IdempotencyKey)})
	if err != nil {
		var actionErr JobActionError
		if errors.As(err, &actionErr) {
			writeAPIError(w, actionErr.Status, actionErr.Code, actionErr.Message, actionErr.Remediation)
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "job_action_failed", "The job action could not be completed.", "Refresh the job state before retrying.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"api_version": observationAPIVersion, "action": action, "receipt": receipt})
}

func subtleEqual(left, right string) bool {
	if len(left) != len(right) || left == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}
