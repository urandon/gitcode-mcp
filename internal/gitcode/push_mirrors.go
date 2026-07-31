package gitcode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const redactedPushMirrorDestination = "[redacted]"

var pushMirrorURLPattern = regexp.MustCompile(`(?i)\b(?:https?|ssh|git)://[^\s<>"']+`)

type PushMirrorListRequest struct {
	Owner string
	Repo  string
}

type PushMirrorTriggerRequest struct {
	Owner    string
	Repo     string
	MirrorID string
}

// PushMirror is sanitized at JSON decode time. URL and Message never contain
// URL user-info, query parameters, or fragments after crossing the adapter
// boundary.
type PushMirror struct {
	RemoteID               string `json:"-"`
	ProjectID              string `json:"-"`
	URL                    string `json:"-"`
	Force                  bool   `json:"-"`
	Private                bool   `json:"-"`
	UpdateStatus           string `json:"-"`
	NumberOfFailures       int    `json:"-"`
	Message                string `json:"-"`
	CreatedAt              string `json:"-"`
	LastUpdateAt           string `json:"-"`
	LastSuccessfulUpdateAt string `json:"-"`
}

func (m *PushMirror) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID                     any    `json:"id"`
		ProjectID              any    `json:"project_id"`
		URL                    string `json:"url"`
		Force                  bool   `json:"force"`
		Private                bool   `json:"is_private"`
		UpdateStatus           string `json:"update_status"`
		NumberOfFailures       int    `json:"number_of_failures"`
		Message                string `json:"message"`
		CreatedAt              string `json:"created_at"`
		LastUpdateAt           string `json:"last_update_at"`
		LastSuccessfulUpdateAt string `json:"last_successful_update_at"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	remoteID, err := decodePushMirrorID("push_mirror.id", raw.ID, true)
	if err != nil {
		return err
	}
	projectID, err := decodePushMirrorID("push_mirror.project_id", raw.ProjectID, false)
	if err != nil {
		return err
	}
	m.RemoteID = remoteID
	m.ProjectID = projectID
	m.URL = redactPushMirrorURL(raw.URL)
	m.Force = raw.Force
	m.Private = raw.Private
	m.UpdateStatus = strings.TrimSpace(raw.UpdateStatus)
	if raw.NumberOfFailures < 0 {
		return &ErrSchemaDecode{Field: "push_mirror.number_of_failures", Expected: "non-negative integer", Received: strconv.Itoa(raw.NumberOfFailures)}
	}
	m.NumberOfFailures = raw.NumberOfFailures
	m.Message = redactPushMirrorMessage(raw.Message)
	if m.CreatedAt, err = validatePushMirrorTimestamp("push_mirror.created_at", raw.CreatedAt); err != nil {
		return err
	}
	if m.LastUpdateAt, err = validatePushMirrorTimestamp("push_mirror.last_update_at", raw.LastUpdateAt); err != nil {
		return err
	}
	if m.LastSuccessfulUpdateAt, err = validatePushMirrorTimestamp("push_mirror.last_successful_update_at", raw.LastSuccessfulUpdateAt); err != nil {
		return err
	}
	return nil
}

func decodePushMirrorID(field string, value any, required bool) (string, error) {
	switch v := value.(type) {
	case nil:
		if required {
			return "", &ErrSchemaDecode{Field: field, Expected: "positive integer", Received: "missing"}
		}
		return "", nil
	case float64:
		if v <= 0 || v != math.Trunc(v) {
			return "", &ErrSchemaDecode{Field: field, Expected: "positive integer", Received: fmt.Sprintf("%v", v)}
		}
		return strconv.FormatInt(int64(v), 10), nil
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" && !required {
			return "", nil
		}
		n, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil || n <= 0 {
			return "", &ErrSchemaDecode{Field: field, Expected: "positive integer", Received: fmt.Sprintf("%q", v)}
		}
		return strconv.FormatInt(n, 10), nil
	default:
		return "", &ErrSchemaDecode{Field: field, Expected: "number or decimal string", Received: fmt.Sprintf("type %T", value)}
	}
}

func redactPushMirrorURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return redactedPushMirrorDestination
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" || parsed.Opaque != "" {
		return redactedPushMirrorDestination
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "ssh", "git":
	default:
		return redactedPushMirrorDestination
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return parsed.String()
}

func redactPushMirrorMessage(message string) string {
	return strings.TrimSpace(pushMirrorURLPattern.ReplaceAllStringFunc(message, redactPushMirrorURL))
}

func validatePushMirrorTimestamp(field, raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	if _, err := time.Parse(time.RFC3339, value); err != nil {
		return "", &ErrSchemaDecode{Field: field, Expected: "RFC3339 timestamp or absent", Received: fmt.Sprintf("%q", value)}
	}
	return value, nil
}

func (c *HTTPClient) ListPushRemoteMirrors(ctx context.Context, req PushMirrorListRequest) ([]PushMirror, error) {
	if err := validateReadRepo(req.Owner, req.Repo); err != nil {
		return nil, err
	}
	endpoint := listPushRemoteMirrorsEndpoint(req.Owner, req.Repo)
	var mirrors []PushMirror
	if err := c.getJSON(ctx, endpoint, nil, &mirrors); err != nil {
		return nil, err
	}
	return mirrors, nil
}

func (c *HTTPClient) TriggerPushRemoteMirror(ctx context.Context, req PushMirrorTriggerRequest, opts WriteOptions) (WriteResult[PushMirror], error) {
	if err := validateReadRepo(req.Owner, req.Repo); err != nil {
		return WriteResult[PushMirror]{}, err
	}
	mirrorID, err := decodePushMirrorID("push_mirror.id", strings.TrimSpace(req.MirrorID), true)
	if err != nil {
		return WriteResult[PushMirror]{}, err
	}
	endpoint := triggerPushRemoteMirrorEndpoint(req.Owner, req.Repo, mirrorID)
	payload := struct {
		Force bool `json:"force"`
	}{Force: true}
	body, err := json.Marshal(payload)
	if err != nil {
		return WriteResult[PushMirror]{}, err
	}
	key := strings.TrimSpace(opts.IdempotencyKey)
	if key == "" {
		key = GenerateIdempotencyKey("TriggerPushRemoteMirror", req.Owner+"/"+req.Repo+"/push-mirrors/"+mirrorID, payload, opts)
	}
	responseBody, headers, err := c.bytesWithOptions(ctx, http.MethodPost, endpoint, nil, body, requestOptions{
		knownRemoteAlias: true,
		remoteAlias:      mirrorID,
		idempotencyKey:   key,
		localPayload:     body,
		noRetry:          true,
	})
	if err != nil {
		var forbidden ErrForbidden
		if errors.As(err, &forbidden) && strings.Contains(strings.ToLower(forbidden.Message), "sync too frequently") {
			return WriteResult[PushMirror]{}, ErrPushMirrorSyncInProgress{Endpoint: endpoint, Message: "sync too frequently; wait for the current synchronization or cooldown to finish"}
		}
		return WriteResult[PushMirror]{}, err
	}

	mirrors, err := c.ListPushRemoteMirrors(ctx, PushMirrorListRequest{Owner: req.Owner, Repo: req.Repo})
	if err != nil {
		return WriteResult[PushMirror]{}, ErrWriteConfirmationIncomplete{Endpoint: endpoint, Message: "trigger was accepted but sanitized readback failed", Cause: err}
	}
	var mirror PushMirror
	found := false
	for _, candidate := range mirrors {
		if candidate.RemoteID == mirrorID {
			mirror = candidate
			found = true
			break
		}
	}
	if !found {
		return WriteResult[PushMirror]{}, ErrWriteConfirmationIncomplete{Endpoint: endpoint, Message: "trigger was accepted but mirror was absent from sanitized readback"}
	}
	responseHash := sha256.Sum256(responseBody)
	payloadFingerprint := sha256.Sum256(RedactJSONBody(responseBody, req.Owner+"/"+req.Repo+"/push-mirrors/"+mirrorID))
	providerStatus := headers.Get("Status")
	if providerStatus == "" {
		providerStatus = "2xx"
	}
	return WriteResult[PushMirror]{
		Record:                     mirror,
		Confirmed:                  true,
		Operation:                  "TriggerPushRemoteMirror",
		Target:                     req.Owner + "/" + req.Repo + "/push-mirrors/" + mirrorID,
		ProviderStatus:             providerStatus + "-sanitized-readback",
		RemoteID:                   mirrorID,
		RemoteRevision:             hex.EncodeToString(responseHash[:]),
		IdempotencyKey:             key,
		ResponseHash:               hex.EncodeToString(responseHash[:]),
		ConfirmedAt:                time.Now().UTC(),
		ProviderPayloadFingerprint: hex.EncodeToString(payloadFingerprint[:]),
	}, nil
}
