package gitcode

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
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
