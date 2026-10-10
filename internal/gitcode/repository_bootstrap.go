package gitcode

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// RepositoryBootstrapClient is an optional surface: older/custom clients fail
// closed instead of substituting local binding data for provider identity.
type RepositoryBootstrapClient interface {
	GetRepositoryMetadata(context.Context, RepoRequest) (RepositoryMetadata, error)
	ListRepositoryLabels(context.Context, RepoRequest) (RepositoryLabels, error)
	CreateRepositoryLabel(context.Context, RepositoryLabelRequest, WriteOptions) (WriteResult[RepositoryLabel], error)
}

func (p liveProvider) ListRepositoryLabels(ctx context.Context, req RepoRequest) (RepositoryLabels, error) {
	if err := p.matrix.Preflight(ProductAreaLabels); err != nil {
		return RepositoryLabels{}, err
	}
	return p.HTTPClient.ListRepositoryLabels(ctx, req)
}
func (p liveProvider) CreateRepositoryLabel(ctx context.Context, req RepositoryLabelRequest, opts WriteOptions) (WriteResult[RepositoryLabel], error) {
	if err := p.matrix.Preflight(ProductAreaLabels); err != nil {
		return WriteResult[RepositoryLabel]{}, err
	}
	return p.HTTPClient.CreateRepositoryLabel(ctx, req, opts)
}

type RepositoryMetadata struct {
	ProviderID    string `json:"provider_id"`
	Owner         string `json:"owner"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
}

type RepositoryLabel struct {
	ID           json.Number `json:"id"`
	Name         string      `json:"name"`
	Color        string      `json:"color"`
	Description  string      `json:"description"`
	RepositoryID json.Number `json:"repository_id"`
}

type RepositoryLabels struct {
	RepositoryID string            `json:"repository_id"`
	Labels       []RepositoryLabel `json:"labels"`
}

type RepositoryLabelRequest struct {
	Owner string
	Repo  string
	Name  string
	Color string
}

type ErrRepositoryLabelWrite struct {
	RemoteID string
	Cause    error
}

func (e ErrRepositoryLabelWrite) Error() string {
	return "gitcode: label creation needs canonical reconciliation"
}
func (e ErrRepositoryLabelWrite) Unwrap() error { return e.Cause }

func bootstrapSchema(field string) error {
	return &ErrSchemaDecode{Field: field, Expected: "canonical repository identity and complete label data", Received: "invalid or missing", Message: "repository bootstrap response cannot establish the requested invariant"}
}

func positiveBootstrapID(n json.Number) bool {
	i, err := strconv.ParseInt(n.String(), 10, 64)
	return err == nil && i > 0 && strconv.FormatInt(i, 10) == n.String()
}

func (c *HTTPClient) GetRepositoryMetadata(ctx context.Context, req RepoRequest) (RepositoryMetadata, error) {
	var raw struct {
		ID    json.Number `json:"id"`
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
		Name          string `json:"name"`
		FullName      string `json:"full_name"`
		Private       *bool  `json:"private"`
		DefaultBranch string `json:"default_branch"`
	}
	if err := c.getJSON(ctx, getRepoEndpoint(req.Owner, req.Repo), nil, &raw); err != nil {
		return RepositoryMetadata{}, err
	}
	if !positiveBootstrapID(raw.ID) || raw.Owner.Login != req.Owner || raw.Name != req.Repo || raw.FullName != req.Owner+"/"+req.Repo || raw.Private == nil || strings.TrimSpace(raw.DefaultBranch) == "" {
		return RepositoryMetadata{}, bootstrapSchema("repository")
	}
	return RepositoryMetadata{ProviderID: raw.ID.String(), Owner: raw.Owner.Login, Name: raw.Name, FullName: raw.FullName, Private: *raw.Private, DefaultBranch: raw.DefaultBranch}, nil
}

// All pages are required before absence or an exact name can be established.
// The cap, repeated-page detection, strict array decoding and repository-id
// checks prevent partial/malformed lists from authorizing a creation.
func (c *HTTPClient) ListRepositoryLabels(ctx context.Context, req RepoRequest) (RepositoryLabels, error) {
	meta, err := c.GetRepositoryMetadata(ctx, req)
	if err != nil {
		return RepositoryLabels{}, err
	}
	out := RepositoryLabels{RepositoryID: meta.ProviderID, Labels: []RepositoryLabel{}}
	ids, names := map[string]bool{}, map[string]bool{}
	endpoint := getRepoEndpoint(req.Owner, req.Repo) + "/labels"
	for page := 1; page <= 20; page++ {
		var labels []RepositoryLabel
		if err := c.getJSON(ctx, endpoint, url.Values{"page": {strconv.Itoa(page)}, "per_page": {"100"}}, &labels); err != nil {
			return RepositoryLabels{}, err
		}
		if labels == nil || len(labels) > 100 {
			return RepositoryLabels{}, bootstrapSchema("labels.page")
		}
		for _, label := range labels {
			color, colorErr := NormalizeRepositoryLabelColor(label.Color)
			if !positiveBootstrapID(label.ID) || label.RepositoryID.String() != meta.ProviderID || strings.TrimSpace(label.Name) == "" || colorErr != nil || ids[label.ID.String()] || names[label.Name] {
				return RepositoryLabels{}, bootstrapSchema("labels.identity")
			}
			label.Color = color
			ids[label.ID.String()], names[label.Name] = true, true
			out.Labels = append(out.Labels, label)
		}
		// Even a short nonempty page may reflect a provider-side page cap.
		// Continue until an explicit empty page rather than infer absence.
		if len(labels) == 0 {
			sort.Slice(out.Labels, func(i, j int) bool { return out.Labels[i].Name < out.Labels[j].Name })
			return out, nil
		}
	}
	return RepositoryLabels{}, bootstrapSchema("labels.pagination_limit")
}

func NormalizeRepositoryLabelColor(raw string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if len(value) == 4 && value[0] == '#' {
		value = "#" + string([]byte{value[1], value[1], value[2], value[2], value[3], value[3]})
	}
	if len(value) != 7 || value[0] != '#' {
		return "", ErrValidationFailed{Field: "color", Message: "color must be # followed by six hexadecimal digits"}
	}
	if _, err := hex.DecodeString(value[1:]); err != nil {
		return "", ErrValidationFailed{Field: "color", Message: "color must be # followed by six hexadecimal digits"}
	}
	return value, nil
}

func ValidateRepositoryLabel(req RepositoryLabelRequest) error {
	if strings.TrimSpace(req.Name) == "" || req.Name != strings.TrimSpace(req.Name) || len(req.Name) > 255 || strings.IndexFunc(req.Name, unicode.IsControl) >= 0 {
		return ErrValidationFailed{Field: "name", Message: "name must be nonempty, at most 255 bytes, without control characters or edge whitespace"}
	}
	if len(strings.TrimSpace(req.Color)) != 7 {
		return ErrValidationFailed{Field: "color", Message: "creation color must be #RRGGBB"}
	}
	_, err := NormalizeRepositoryLabelColor(req.Color)
	return err
}

func (c *HTTPClient) CreateRepositoryLabel(ctx context.Context, req RepositoryLabelRequest, opts WriteOptions) (WriteResult[RepositoryLabel], error) {
	if err := ValidateRepositoryLabel(req); err != nil {
		return WriteResult[RepositoryLabel]{}, err
	}
	if strings.TrimSpace(opts.IdempotencyKey) == "" {
		return WriteResult[RepositoryLabel]{}, ErrValidationFailed{Field: "idempotency_key", Message: "caller key is required"}
	}
	color, _ := NormalizeRepositoryLabelColor(req.Color)
	body := []byte(url.Values{"name": {req.Name}, "color": {color}}.Encode())
	endpoint := getRepoEndpoint(req.Owner, req.Repo) + "/labels"
	attempted := false
	remoteID := ""
	failure := func(phase string, err error) (WriteResult[RepositoryLabel], error) {
		return WriteResult[RepositoryLabel]{}, ErrRepositoryLabelWrite{RemoteID: remoteID, Cause: ErrWriteMutationPhase{Phase: phase, MutationAttempted: attempted, Cause: err}}
	}
	response, _, err := c.bytesWithOptions(ctx, http.MethodPost, endpoint, nil, body, requestOptions{contentType: "application/x-www-form-urlencoded", idempotencyKey: opts.IdempotencyKey, noRetry: true, beforeAttempt: func() { attempted = true }})
	if err != nil {
		return failure("post", err)
	}
	var acknowledgement RepositoryLabel
	decodeErr := decodeJSON(endpoint, response, &acknowledgement)
	if positiveBootstrapID(acknowledgement.ID) {
		remoteID = acknowledgement.ID.String()
	}
	if decodeErr != nil {
		return failure("acknowledgement", decodeErr)
	}
	if !positiveBootstrapID(acknowledgement.ID) {
		return failure("acknowledgement", bootstrapSchema("label.id"))
	}
	list, err := c.ListRepositoryLabels(ctx, RepoRequest{Owner: req.Owner, Repo: req.Repo})
	if err != nil {
		return failure("readback", err)
	}
	for _, label := range list.Labels {
		if label.ID == acknowledgement.ID && label.Name == req.Name && label.Color == color && label.RepositoryID.String() == list.RepositoryID {
			return WriteResult[RepositoryLabel]{Record: label, Confirmed: true, Operation: "CreateRepositoryLabel", RemoteID: label.ID.String(), IdempotencyKey: opts.IdempotencyKey, ConfirmedAt: time.Now().UTC()}, nil
		}
	}
	return failure("readback_mismatch", bootstrapSchema("label.readback"))
}
