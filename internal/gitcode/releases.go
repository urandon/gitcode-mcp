package gitcode

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

const (
	ReleaseStatusUnset      = 0
	ReleaseStatusPreRelease = 1
	ReleaseStatusLatest     = 2
)

type ReleaseRequest struct {
	Owner string
	Repo  string
	Tag   string
}

type ReleaseWriteRequest struct {
	Owner         string        `json:"-"`
	Repo          string        `json:"-"`
	TagName       string        `json:"tag_name,omitempty"`
	Ref           string        `json:"ref,omitempty"`
	Name          string        `json:"name,omitempty"`
	Description   string        `json:"description,omitempty"`
	ReleaseStatus int           `json:"release_status"`
	Links         []ReleaseLink `json:"links,omitempty"`
	Assets        []ReleaseLink `json:"assets,omitempty"`
}

type ReleaseLink struct {
	Name         string `json:"name"`
	URL          string `json:"url"`
	AttachmentID string `json:"attachment_id,omitempty"`
	Action       string `json:"action,omitempty"`
	Size         int64  `json:"size,omitempty"`
}

type Release struct {
	ID            string        `json:"id,omitempty"`
	TagName       string        `json:"tag_name"`
	Name          string        `json:"name"`
	Description   string        `json:"description"`
	ReleaseStatus int           `json:"release_status"`
	Assets        ReleaseAssets `json:"assets"`
	CreatedAt     time.Time     `json:"created_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
}

type ReleaseAssets struct {
	Assets  []ReleaseLink `json:"assets,omitempty"`
	Links   []ReleaseLink `json:"links,omitempty"`
	Sources []ReleaseLink `json:"sources,omitempty"`
}

func (c *HTTPClient) GetRelease(ctx context.Context, req ReleaseRequest) (Release, error) {
	if err := validateReleaseRequest(req); err != nil {
		return Release{}, err
	}
	endpoint := getReleaseEndpoint(req.Owner, req.Repo, req.Tag)
	var release Release
	body, _, err := c.getBytesWithOptions(ctx, endpoint, nil, requestOptions{})
	if err != nil {
		return Release{}, err
	}
	if err := decodeDataJSON(endpoint, body, &release); err != nil {
		return Release{}, err
	}
	return release, nil
}

func (c *HTTPClient) CreateRelease(ctx context.Context, req ReleaseWriteRequest, opts WriteOptions) (WriteResult[Release], error) {
	if err := validateReleaseWriteRequest(req, true); err != nil {
		return WriteResult[Release]{}, err
	}
	payload := releaseCreatePayload(req)
	return c.writeRelease(ctx, http.MethodPost, listReleasesEndpoint(req.Owner, req.Repo), "CreateRelease", req.Owner+"/"+req.Repo+"/releases/"+req.TagName, payload, req.TagName, opts)
}

func (c *HTTPClient) UpdateRelease(ctx context.Context, req ReleaseWriteRequest, opts WriteOptions) (WriteResult[Release], error) {
	if err := validateReleaseWriteRequest(req, false); err != nil {
		return WriteResult[Release]{}, err
	}
	payload := releaseUpdatePayload(req)
	return c.writeRelease(ctx, http.MethodPut, getReleaseEndpoint(req.Owner, req.Repo, req.TagName), "UpdateRelease", req.Owner+"/"+req.Repo+"/releases/"+req.TagName, payload, req.TagName, opts)
}

func (c *HTTPClient) writeRelease(ctx context.Context, method, endpoint, operation, target string, payload any, tag string, opts WriteOptions) (WriteResult[Release], error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return WriteResult[Release]{}, err
	}
	key := opts.IdempotencyKey
	if key == "" {
		key = GenerateIdempotencyKey(operation, target, payload, opts)
		opts.IdempotencyKey = key
	}
	respBody, headers, err := c.bytesWithOptions(ctx, method, endpoint, nil, body, requestOptions{idempotencyKey: key, localPayload: body})
	if err != nil {
		return WriteResult[Release]{}, err
	}
	var release Release
	if err := decodeDataJSON(endpoint, respBody, &release); err != nil {
		return WriteResult[Release]{}, err
	}
	if strings.TrimSpace(release.TagName) == "" {
		release.TagName = tag
	}
	if release.TagName != tag {
		return WriteResult[Release]{}, ErrValidationFailed{Field: "release.tag_name", Message: "release write confirmation requires matching tag"}
	}
	hash := sha256.Sum256(respBody)
	fingerprint := sha256.Sum256(RedactJSONBody(respBody, target))
	status := headers.Get("Status")
	if status == "" {
		status = "2xx"
	}
	return WriteResult[Release]{
		Record:                     release,
		Confirmed:                  true,
		Operation:                  operation,
		Target:                     target,
		ProviderStatus:             status,
		RemoteID:                   tag,
		RemoteSlug:                 tag,
		RemoteRevision:             hex.EncodeToString(hash[:]),
		IdempotencyKey:             key,
		ResponseHash:               hex.EncodeToString(hash[:]),
		ConfirmedAt:                time.Now().UTC(),
		ProviderPayloadFingerprint: hex.EncodeToString(fingerprint[:]),
	}, nil
}

func releaseCreatePayload(req ReleaseWriteRequest) any {
	return struct {
		RepoID        string        `json:"repoId"`
		TagName       string        `json:"tag_name"`
		Ref           string        `json:"ref"`
		Name          string        `json:"name"`
		Description   string        `json:"description"`
		ReleaseStatus int           `json:"release_status"`
		Links         []ReleaseLink `json:"links,omitempty"`
		Assets        []ReleaseLink `json:"assets,omitempty"`
	}{
		RepoID:        req.Owner + "/" + req.Repo,
		TagName:       req.TagName,
		Ref:           req.Ref,
		Name:          req.Name,
		Description:   req.Description,
		ReleaseStatus: req.ReleaseStatus,
		Links:         req.Links,
		Assets:        req.Assets,
	}
}

func releaseUpdatePayload(req ReleaseWriteRequest) any {
	return struct {
		Name          string        `json:"name"`
		Description   string        `json:"description"`
		ReleaseStatus int           `json:"release_status"`
		Links         []ReleaseLink `json:"links,omitempty"`
		Assets        []ReleaseLink `json:"assets,omitempty"`
	}{
		Name:          req.Name,
		Description:   req.Description,
		ReleaseStatus: req.ReleaseStatus,
		Links:         req.Links,
		Assets:        req.Assets,
	}
}

func decodeDataJSON(endpoint string, body []byte, out any) error {
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && len(envelope.Data) > 0 && string(envelope.Data) != "null" {
		dec := json.NewDecoder(bytes.NewReader(envelope.Data))
		if err := dec.Decode(out); err != nil {
			return ErrPartialResponse{Endpoint: endpoint, Got: int64(len(body)), Cause: err, Message: decodeMessage(err)}
		}
		return nil
	}
	return decodeJSON(endpoint, body, out)
}

func validateReleaseRequest(req ReleaseRequest) error {
	if err := validateReadRepo(req.Owner, req.Repo); err != nil {
		return err
	}
	if strings.TrimSpace(req.Tag) == "" {
		return ErrValidationFailed{Field: "tag", Message: "release tag is required"}
	}
	return nil
}

func validateReleaseWriteRequest(req ReleaseWriteRequest, requireRef bool) error {
	if err := validateReadRepo(req.Owner, req.Repo); err != nil {
		return err
	}
	if strings.TrimSpace(req.TagName) == "" {
		return ErrValidationFailed{Field: "tag", Message: "release tag is required"}
	}
	if requireRef && strings.TrimSpace(req.Ref) == "" {
		return ErrValidationFailed{Field: "ref", Message: "release ref is required"}
	}
	if strings.TrimSpace(req.Name) == "" {
		return ErrValidationFailed{Field: "name", Message: "release name is required"}
	}
	if strings.TrimSpace(req.Description) == "" {
		return ErrValidationFailed{Field: "description", Message: "release description is required"}
	}
	for _, link := range req.Links {
		if strings.TrimSpace(link.Name) == "" || strings.TrimSpace(link.URL) == "" {
			return ErrValidationFailed{Field: "asset", Message: "release asset links require name and url"}
		}
	}
	return nil
}
