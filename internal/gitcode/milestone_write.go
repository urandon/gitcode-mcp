package gitcode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ErrMilestoneWrite retains only the canonical identity learned before a
// confirmation failure. The service uses it for GET-only reconciliation.
type ErrMilestoneWrite struct {
	RemoteID string
	Cause    error
}

func (e ErrMilestoneWrite) Error() string {
	return "gitcode: milestone write needs canonical reconciliation"
}
func (e ErrMilestoneWrite) Unwrap() error { return e.Cause }

// ConfirmMilestoneFields never includes requested/provider values in errors.
func ConfirmMilestoneFields(req MilestoneWriteRequest, m Milestone, create bool) error {
	checks := []struct {
		field, got, want string
		check            bool
	}{
		{"title", m.Title, strings.TrimSpace(req.Title), true},
		{"description", m.Body, req.Description, create || req.Description != ""},
		{"due_on", m.DueOn, normalizeMilestoneDueOn(req.DueOn), true},
		{"state", m.Status, strings.TrimSpace(req.State), req.State != ""},
	}
	if create {
		checks[3].want, checks[3].check = "open", true
	}
	for _, c := range checks {
		if c.check && c.got != c.want {
			return &ErrSchemaDecode{Field: "milestone." + c.field, Expected: "requested canonical value", Received: "mismatch", Message: "milestone readback does not confirm the requested field"}
		}
	}
	return nil
}

func (c *HTTPClient) writeMilestoneConfirmed(ctx context.Context, req MilestoneWriteRequest, opts WriteOptions, create bool) (WriteResult[Milestone], error) {
	method, endpoint, operation := http.MethodPatch, getMilestoneEndpoint(req.Owner, req.Repo, req.ID), "UpdateMilestone"
	remoteID := strconv.Itoa(req.ID)
	if create {
		method, endpoint, operation, remoteID = http.MethodPost, listMilestonesEndpoint(req.Owner, req.Repo), "CreateMilestone", ""
	}
	payload := milestoneWritePayload(req, create)
	body, err := json.Marshal(payload)
	if err != nil {
		return WriteResult[Milestone]{}, err
	}
	key := opts.IdempotencyKey
	if key == "" {
		key = GenerateIdempotencyKey(operation, req.Owner+"/"+req.Repo+"/"+strings.TrimSpace(req.Title), payload, opts)
	}
	attempted := false
	failure := func(phase string, err error) (WriteResult[Milestone], error) {
		return WriteResult[Milestone]{}, ErrMilestoneWrite{RemoteID: remoteID, Cause: ErrWriteMutationPhase{Phase: phase, MutationAttempted: attempted, Cause: err}}
	}
	response, headers, err := c.bytesWithOptions(ctx, method, endpoint, nil, body, requestOptions{idempotencyKey: key, localPayload: body, noRetry: true, beforeAttempt: func() { attempted = true }})
	if err != nil {
		return failure(strings.ToLower(method), err)
	}
	if create {
		var acknowledgement Milestone
		decodeErr := decodeJSON(endpoint, response, &acknowledgement)
		// A partial acknowledgement can still establish the identity needed
		// for a later GET-only recovery; it never establishes confirmation.
		remoteID = acknowledgement.RemoteID
		if decodeErr != nil {
			return failure("acknowledgement", decodeErr)
		}
	}
	id, err := strconv.Atoi(remoteID)
	if err != nil || id <= 0 {
		return failure("acknowledgement", &ErrSchemaDecode{Field: "milestone.id", Expected: "positive canonical identity", Received: "missing"})
	}
	readback, err := c.GetMilestone(ctx, MilestoneRequest{Owner: req.Owner, Repo: req.Repo, ID: id})
	if err != nil {
		return failure("readback", err)
	}
	if err := ConfirmMilestoneFields(req, readback, create); err != nil {
		return failure("readback_mismatch", err)
	}
	hash := sha256.Sum256(response)
	fingerprint := sha256.Sum256(RedactJSONBody(response))
	return WriteResult[Milestone]{Record: readback, Confirmed: true, Operation: operation, Target: remoteID, RemoteID: remoteID, RemoteRevision: firstNonEmpty(readback.UpdatedAt, hex.EncodeToString(hash[:])), BrowserURL: readback.HTMLURL, ProviderStatus: firstNonEmpty(headers.Get("Status"), "2xx-readback"), IdempotencyKey: key, ResponseHash: hex.EncodeToString(hash[:]), ProviderPayloadFingerprint: hex.EncodeToString(fingerprint[:]), ConfirmedAt: time.Now().UTC()}, nil
}
