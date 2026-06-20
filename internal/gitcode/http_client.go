package gitcode

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const defaultMaxResponseSize int64 = 10 << 20
const defaultIdempotencyKeyLength = 32

type HTTPClient struct {
	baseURL         *url.URL
	token           string
	maxResponseSize int64
	maxRetries      int
	userAgent       string
	client          *http.Client
	pagination      PaginationConfig
}

func NewHTTPClient(cfg Config) (*HTTPClient, error) {
	base := strings.TrimSpace(cfg.BaseURL)
	if base == "" {
		base = "https://gitcode.com"
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	limit := cfg.MaxResponseSize
	if limit <= 0 {
		limit = defaultMaxResponseSize
	}
	client := &http.Client{}
	if cfg.Timeout > 0 {
		client.Timeout = cfg.Timeout
	}
	ua := cfg.UserAgent
	if ua == "" {
		ua = "gitcode-mcp"
	}
	return &HTTPClient{baseURL: parsed, token: cfg.Token, maxResponseSize: limit, maxRetries: cfg.MaxRetries, userAgent: ua, client: client, pagination: cfg.Pagination}, nil
}

func (c *HTTPClient) ListIssues(ctx context.Context, req IssueListRequest) (Page[IssueSummary], error) {
	endpoint := listIssuesEndpoint(req.Owner, req.Repo)
	items, page, err := getPaged[IssueSummary](ctx, c, endpoint, issueListQuery(req), PageState{Page: req.Page, PerPage: req.PerPage})
	if err != nil {
		return Page[IssueSummary]{}, err
	}
	return Page[IssueSummary]{Items: items, Page: page.Page, PerPage: page.PerPage}, nil
}

func (c *HTTPClient) GetIssue(ctx context.Context, req IssueRequest) (Issue, error) {
	var issue Issue
	endpoint := getIssueEndpoint(req.Owner, req.Repo, req.Number)
	err := c.getJSONWithOptions(ctx, endpoint, nil, &issue, requestOptions{knownRemoteAlias: req.KnownRemoteAlias, remoteAlias: req.RemoteAlias})
	if err != nil {
		return Issue{}, err
	}
	return issue, nil
}

func (c *HTTPClient) ListIssueComments(ctx context.Context, req IssueRequest) (Page[Comment], error) {
	endpoint := listIssueCommentsEndpoint(req.Owner, req.Repo, req.Number)
	items, page, err := getPaged[Comment](ctx, c, endpoint, nil, PageState{})
	if err != nil {
		return Page[Comment]{}, err
	}
	return Page[Comment]{Items: items, Page: page.Page, PerPage: page.PerPage}, nil
}

func (c *HTTPClient) GetWikiPage(ctx context.Context, req WikiPageRequest) (WikiPage, error) {
	var page WikiPage
	endpoint := getWikiPageEndpoint(req.Owner, req.Repo, req.Slug)
	err := c.getJSON(ctx, endpoint, nil, &page)
	if err != nil {
		return WikiPage{}, err
	}
	return page, nil
}

func (c *HTTPClient) ListWikiPages(ctx context.Context, req WikiListRequest) (Page[WikiPage], error) {
	endpoint := listWikiPagesEndpoint(req.Owner, req.Repo)
	items, page, err := getPaged[WikiPage](ctx, c, endpoint, nil, PageState{Page: req.Page, PerPage: req.PerPage})
	if err != nil {
		return Page[WikiPage]{}, err
	}
	return Page[WikiPage]{Items: items, Page: page.Page, PerPage: page.PerPage}, nil
}

func (c *HTTPClient) Search(ctx context.Context, req SearchRequest) (Page[SearchResult], error) {
	values := url.Values{}
	values.Set("q", req.Query)
	if req.Owner != "" {
		values.Set("owner", req.Owner)
	}
	if req.Repo != "" {
		values.Set("repo", req.Repo)
	}
	if req.Type != "" {
		values.Set("type", req.Type)
	}
	items, page, err := getPaged[SearchResult](ctx, c, searchIssuesEndpoint(), values, PageState{Page: req.Page, PerPage: req.PerPage})
	if err != nil {
		return Page[SearchResult]{}, err
	}
	return Page[SearchResult]{Items: items, Page: page.Page, PerPage: page.PerPage}, nil
}

func (c *HTTPClient) ListIssueAttachments(ctx context.Context, req IssueRequest) (Page[AttachmentSummary], error) {
	endpoint := issueAttachmentsEndpoint(req.Owner, req.Repo, req.Number)
	items, page, err := getPaged[AttachmentSummary](ctx, c, endpoint, nil, PageState{})
	if err != nil {
		return Page[AttachmentSummary]{}, err
	}
	return Page[AttachmentSummary]{Items: items, Page: page.Page, PerPage: page.PerPage}, nil
}

func (c *HTTPClient) GetAttachment(ctx context.Context, req AttachmentRequest) (AttachmentBody, error) {
	endpoint := attachmentEndpoint(req.Owner, req.Repo, req.IssueNumber, req.AttachmentID)
	body, headers, err := c.getBytes(ctx, endpoint, nil)
	if err != nil {
		return AttachmentBody{}, err
	}
	name := req.Name
	if name == "" {
		name = req.AttachmentID
	}
	return AttachmentBody{ID: req.AttachmentID, Name: name, ContentType: headers.Get("Content-Type"), Size: int64(len(body)), Body: body, SourceEndpoint: endpoint, Checksum: headers.Get("X-Checksum-Sha256")}, nil
}

func (c *HTTPClient) CreateIssue(ctx context.Context, req CreateIssueRequest, opts WriteOptions) (WriteResult[Issue], error) {
	if err := validateCreateIssue(req); err != nil {
		return WriteResult[Issue]{}, err
	}
	target := req.Owner + "/" + req.Repo
	return writeConfirmedJSON[Issue](ctx, c, http.MethodPost, createIssueEndpoint(req.Owner, req.Repo), "CreateIssue", target, req, opts, func(result WriteResult[Issue]) (WriteResult[Issue], error) {
		issue := result.Record
		if strings.TrimSpace(issue.ID) == "" || issue.Number <= 0 {
			return WriteResult[Issue]{}, ErrValidationFailed{Field: "response", Message: "issue create confirmation requires id and number"}
		}
		result.RemoteID = issue.ID
		result.RemoteNumber = issue.Number
		return result, nil
	})
}

func (c *HTTPClient) UpdateIssue(ctx context.Context, req UpdateIssueRequest, opts WriteOptions) (WriteResult[Issue], error) {
	if err := validateUpdateIssue(req); err != nil {
		return WriteResult[Issue]{}, err
	}
	target := req.Owner + "/" + req.Repo + "/" + strconv.Itoa(req.Number)
	return writeConfirmedJSON[Issue](ctx, c, http.MethodPatch, updateIssueEndpoint(req.Owner, req.Repo, req.Number), "UpdateIssue", target, req, opts, func(result WriteResult[Issue]) (WriteResult[Issue], error) {
		issue := result.Record
		if strings.TrimSpace(issue.ID) == "" || issue.Number != req.Number {
			return WriteResult[Issue]{}, ErrValidationFailed{Field: "response", Message: "issue update confirmation requires id and matching number"}
		}
		result.RemoteID = issue.ID
		result.RemoteNumber = issue.Number
		return result, nil
	})
}

func (c *HTTPClient) CreateIssueComment(ctx context.Context, req CreateIssueCommentRequest, opts WriteOptions) (WriteResult[Comment], error) {
	if err := validateCreateIssueComment(req); err != nil {
		return WriteResult[Comment]{}, err
	}
	target := req.Owner + "/" + req.Repo + "/" + strconv.Itoa(req.Number)
	return writeConfirmedJSON[Comment](ctx, c, http.MethodPost, createIssueCommentEndpoint(req.Owner, req.Repo, req.Number), "CreateIssueComment", target, req, opts, func(result WriteResult[Comment]) (WriteResult[Comment], error) {
		comment := result.Record
		if strings.TrimSpace(comment.ID) == "" {
			return WriteResult[Comment]{}, ErrValidationFailed{Field: "response", Message: "comment confirmation requires id"}
		}
		result.RemoteID = comment.ID
		result.ParentIssueNumber = req.Number
		result.ParentIssueID = comment.IssueID
		return result, nil
	})
}

func (c *HTTPClient) CreateWikiPage(ctx context.Context, req CreateWikiPageRequest, opts WriteOptions) (WriteResult[WikiPage], error) {
	if err := validateCreateWikiPage(req); err != nil {
		return WriteResult[WikiPage]{}, err
	}
	target := req.Owner + "/" + req.Repo
	return writeConfirmedJSON[WikiPage](ctx, c, http.MethodPost, createWikiPageEndpoint(req.Owner, req.Repo), "CreateWikiPage", target, req, opts, func(result WriteResult[WikiPage]) (WriteResult[WikiPage], error) {
		page := result.Record
		if strings.TrimSpace(page.Slug) == "" || (strings.TrimSpace(page.ID) == "" && strings.TrimSpace(page.Revision) == "") {
			return WriteResult[WikiPage]{}, ErrValidationFailed{Field: "response", Message: "wiki create confirmation requires slug and id or revision"}
		}
		result.RemoteID = page.ID
		result.RemoteSlug = page.Slug
		result.RemoteRevision = page.Revision
		return result, nil
	})
}

func (c *HTTPClient) UpdateWikiPage(ctx context.Context, req UpdateWikiPageRequest, opts WriteOptions) (WriteResult[WikiPage], error) {
	if err := validateUpdateWikiPage(req); err != nil {
		return WriteResult[WikiPage]{}, err
	}
	target := req.Owner + "/" + req.Repo + "/" + req.Slug
	return writeConfirmedJSON[WikiPage](ctx, c, http.MethodPut, updateWikiPageEndpoint(req.Owner, req.Repo, req.Slug), "UpdateWikiPage", target, req, opts, func(result WriteResult[WikiPage]) (WriteResult[WikiPage], error) {
		page := result.Record
		if strings.TrimSpace(page.Slug) != req.Slug || (strings.TrimSpace(page.ID) == "" && strings.TrimSpace(page.Revision) == "") {
			return WriteResult[WikiPage]{}, ErrValidationFailed{Field: "response", Message: "wiki update confirmation requires matching slug and id or revision"}
		}
		result.RemoteID = page.ID
		result.RemoteSlug = page.Slug
		result.RemoteRevision = page.Revision
		return result, nil
	})
}

func (c *HTTPClient) AddLabel(ctx context.Context, req LabelRequest, opts WriteOptions) (WriteResult[Issue], error) {
	return writeJSON[Issue](ctx, c, http.MethodPost, addLabelEndpoint(req.Owner, req.Repo, req.Number), "AddLabel", req.Owner+"/"+req.Repo+"/"+strconv.Itoa(req.Number), req, opts)
}

func (c *HTTPClient) RemoveLabel(ctx context.Context, req LabelRequest, opts WriteOptions) (WriteResult[Issue], error) {
	return writeJSON[Issue](ctx, c, http.MethodDelete, removeLabelEndpoint(req.Owner, req.Repo, req.Number, req.Label), "RemoveLabel", req.Owner+"/"+req.Repo+"/"+strconv.Itoa(req.Number)+"/"+req.Label, req, opts)
}

type requestOptions struct {
	knownRemoteAlias bool
	remoteAlias      string
	idempotencyKey   string
	localPayload     []byte
}

func (c *HTTPClient) getJSON(ctx context.Context, endpoint string, values url.Values, out any) error {
	return c.getJSONWithOptions(ctx, endpoint, values, out, requestOptions{})
}

func (c *HTTPClient) getJSONWithOptions(ctx context.Context, endpoint string, values url.Values, out any, opts requestOptions) error {
	body, _, err := c.getBytesWithOptions(ctx, endpoint, values, opts)
	if err != nil {
		return err
	}
	return decodeJSON(endpoint, body, out)
}

func decodeJSON(endpoint string, body []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(out); err != nil {
		return ErrPartialResponse{Endpoint: endpoint, Got: int64(len(body)), Cause: err, Message: decodeMessage(err)}
	}
	return nil
}

func (c *HTTPClient) getBytes(ctx context.Context, endpoint string, values url.Values) ([]byte, http.Header, error) {
	return c.getBytesWithOptions(ctx, endpoint, values, requestOptions{})
}

func writeJSON[T any](ctx context.Context, c *HTTPClient, method, endpoint, operation, target string, payload any, opts WriteOptions) (WriteResult[T], error) {
	return writeConfirmedJSON[T](ctx, c, method, endpoint, operation, target, payload, opts, func(result WriteResult[T]) (WriteResult[T], error) {
		return result, nil
	})
}

func writeConfirmedJSON[T any](ctx context.Context, c *HTTPClient, method, endpoint, operation, target string, payload any, opts WriteOptions, confirm func(WriteResult[T]) (WriteResult[T], error)) (WriteResult[T], error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return WriteResult[T]{}, err
	}
	key := opts.IdempotencyKey
	if key == "" {
		key = GenerateIdempotencyKey(operation, target, payload, opts)
	}
	if strings.TrimSpace(key) == "" {
		return WriteResult[T]{}, ErrValidationFailed{Field: "idempotency_key", Message: "idempotency key is required"}
	}
	respBody, headers, err := c.bytesWithOptions(ctx, method, endpoint, nil, body, requestOptions{idempotencyKey: key, localPayload: body})
	if err != nil {
		return WriteResult[T]{}, err
	}
	var record T
	if err := decodeJSON(endpoint, respBody, &record); err != nil {
		return WriteResult[T]{}, err
	}
	hash := sha256.Sum256(respBody)
	result := WriteResult[T]{Record: record, Confirmed: true, Operation: operation, Target: target, ProviderStatus: headers.Get("Status"), IdempotencyKey: key, ResponseHash: hex.EncodeToString(hash[:]), ConfirmedAt: time.Now().UTC()}
	if result.ProviderStatus == "" {
		result.ProviderStatus = "2xx"
	}
	fingerprint := sha256.Sum256(RedactJSONBody(respBody, target))
	result.ProviderPayloadFingerprint = hex.EncodeToString(fingerprint[:])
	result, err = confirm(result)
	if err != nil {
		return WriteResult[T]{}, err
	}
	if !result.Confirmed || result.Operation == "" || result.Target == "" || result.ProviderStatus == "" || result.IdempotencyKey == "" || result.ResponseHash == "" || result.ConfirmedAt.IsZero() {
		return WriteResult[T]{}, ErrValidationFailed{Field: "response", Message: "write confirmation metadata incomplete"}
	}
	return result, nil
}

func validateWriteRepo(owner, repo string) error {
	if strings.TrimSpace(owner) == "" {
		return ErrValidationFailed{Field: "owner", Message: "owner is required"}
	}
	if strings.TrimSpace(repo) == "" {
		return ErrValidationFailed{Field: "repo", Message: "repo is required"}
	}
	return nil
}

func validateCreateIssue(req CreateIssueRequest) error {
	if err := validateWriteRepo(req.Owner, req.Repo); err != nil {
		return err
	}
	if strings.TrimSpace(req.Title) == "" {
		return ErrValidationFailed{Field: "title", Message: "title is required"}
	}
	return nil
}

func validateUpdateIssue(req UpdateIssueRequest) error {
	if err := validateWriteRepo(req.Owner, req.Repo); err != nil {
		return err
	}
	if req.Number <= 0 {
		return ErrValidationFailed{Field: "number", Message: "positive issue number is required"}
	}
	return nil
}

func validateCreateIssueComment(req CreateIssueCommentRequest) error {
	if err := validateWriteRepo(req.Owner, req.Repo); err != nil {
		return err
	}
	if req.Number <= 0 {
		return ErrValidationFailed{Field: "number", Message: "positive issue number is required"}
	}
	if strings.TrimSpace(req.Body) == "" {
		return ErrValidationFailed{Field: "body", Message: "comment body is required"}
	}
	return nil
}

func validateCreateWikiPage(req CreateWikiPageRequest) error {
	if err := validateWriteRepo(req.Owner, req.Repo); err != nil {
		return err
	}
	if strings.TrimSpace(req.Title) == "" {
		return ErrValidationFailed{Field: "title", Message: "wiki title is required"}
	}
	if strings.TrimSpace(req.Body) == "" {
		return ErrValidationFailed{Field: "body", Message: "wiki body is required"}
	}
	return nil
}

func validateUpdateWikiPage(req UpdateWikiPageRequest) error {
	if err := validateWriteRepo(req.Owner, req.Repo); err != nil {
		return err
	}
	if strings.TrimSpace(req.Slug) == "" {
		return ErrValidationFailed{Field: "slug", Message: "wiki slug is required"}
	}
	return nil
}

func GenerateIdempotencyKey(operation, target string, payload any, opts WriteOptions) string {
	if opts.IdempotencyKey != "" {
		return opts.IdempotencyKey
	}
	nonce := opts.IdempotencyNonce
	if nonce == "" {
		nonce = time.Now().UTC().Format(time.RFC3339Nano)
	}
	encoded, _ := json.Marshal(struct {
		Operation string `json:"operation"`
		Target    string `json:"target"`
		Payload   any    `json:"payload"`
		Nonce     string `json:"nonce"`
	}{Operation: operation, Target: target, Payload: payload, Nonce: nonce})
	sum := sha256.Sum256(encoded)
	key := hex.EncodeToString(sum[:])
	if len(key) > defaultIdempotencyKeyLength {
		return key[:defaultIdempotencyKeyLength]
	}
	return key
}

func (c *HTTPClient) getBytesWithOptions(ctx context.Context, endpoint string, values url.Values, opts requestOptions) ([]byte, http.Header, error) {
	return c.bytesWithOptions(ctx, http.MethodGet, endpoint, values, nil, opts)
}

func (c *HTTPClient) bytesWithOptions(ctx context.Context, method, endpoint string, values url.Values, requestBody []byte, opts requestOptions) ([]byte, http.Header, error) {
	attempts := c.maxRetries + 1
	if attempts < 1 {
		attempts = 1
	}
	var lastRetryAfter time.Duration
	var rawRetryAfter string
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, ErrNetworkUnavailable{Endpoint: endpoint, Attempts: attempt - 1, Cause: err}
		}
		resp, err := c.do(ctx, method, endpoint, values, bytes.NewReader(requestBody), opts)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, ErrNetworkUnavailable{Endpoint: endpoint, Attempts: attempt, Cause: ctx.Err()}
			}
			if isRetryableTransport(err) && attempt < attempts {
				continue
			}
			return nil, nil, ErrNetworkUnavailable{Endpoint: endpoint, Attempts: attempt, Cause: err}
		}
		body, readErr := c.readBounded(resp, endpoint)
		resp.Body.Close()
		if readErr != nil {
			return nil, nil, readErr
		}
		switch {
		case resp.StatusCode >= 200 && resp.StatusCode <= 299:
			headers := resp.Header.Clone()
			headers.Set("Status", strconv.Itoa(resp.StatusCode))
			return body, headers, nil
		case resp.StatusCode == http.StatusTooManyRequests:
			rawRetryAfter = resp.Header.Get("Retry-After")
			lastRetryAfter = parseRetryAfter(rawRetryAfter, time.Now())
			if attempt < attempts {
				if err := sleepContext(ctx, lastRetryAfter); err != nil {
					return nil, nil, ErrNetworkUnavailable{Endpoint: endpoint, Attempts: attempt, Cause: err}
				}
				continue
			}
			return nil, nil, ErrRateLimited{RetryAfter: lastRetryAfter, RawRetryAfter: rawRetryAfter, Endpoint: endpoint, Attempts: attempt}
		case resp.StatusCode >= 500 && resp.StatusCode <= 599:
			if attempt < attempts {
				continue
			}
			return nil, nil, ErrNetworkUnavailable{Endpoint: endpoint, Status: resp.StatusCode, Attempts: attempt}
		default:
			return nil, nil, c.statusError(resp.StatusCode, endpoint, body, opts)
		}
	}
	return nil, nil, ErrNetworkUnavailable{Endpoint: endpoint, Attempts: attempts}
}

func (c *HTTPClient) do(ctx context.Context, method, endpoint string, values url.Values, body io.Reader, opts requestOptions) (*http.Response, error) {
	u := c.baseURL.ResolveReference(&url.URL{Path: endpoint})
	if values != nil {
		u.RawQuery = values.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil && method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
	}
	if opts.idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", opts.idempotencyKey)
	}
	req.Header.Set("User-Agent", c.userAgent)
	return c.client.Do(req)
}

func (c *HTTPClient) readBounded(resp *http.Response, endpoint string) ([]byte, error) {
	if resp.ContentLength > c.maxResponseSize {
		return nil, ErrPayloadTooLarge{Endpoint: endpoint, Limit: c.maxResponseSize, Size: resp.ContentLength}
	}
	limit := c.maxResponseSize + 1
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if int64(len(body)) > c.maxResponseSize {
		return nil, ErrPayloadTooLarge{Endpoint: endpoint, Limit: c.maxResponseSize, Size: int64(len(body))}
	}
	if err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, ErrPartialResponse{Endpoint: endpoint, Expected: resp.ContentLength, Got: int64(len(body)), Cause: err}
		}
		return nil, ErrNetworkUnavailable{Endpoint: endpoint, Cause: err, Attempts: 1}
	}
	if resp.ContentLength >= 0 && resp.ContentLength != int64(len(body)) {
		return nil, ErrPartialResponse{Endpoint: endpoint, Expected: resp.ContentLength, Got: int64(len(body))}
	}
	return body, nil
}

func (c *HTTPClient) statusError(status int, endpoint string, body []byte, opts requestOptions) error {
	msg := responseMessage(body)
	switch status {
	case http.StatusUnauthorized:
		return ErrAuthExpired{Endpoint: endpoint, Status: status, Message: msg}
	case http.StatusForbidden:
		return ErrForbidden{Endpoint: endpoint, Status: status, Message: msg, Recovery: "check GitCode permissions for this resource"}
	case http.StatusNotFound:
		if opts.knownRemoteAlias {
			return ErrRemoteNotFound{Endpoint: endpoint, Alias: opts.remoteAlias, Message: msg}
		}
		return ErrNotFound{Endpoint: endpoint, Message: msg}
	case http.StatusConflict:
		return ErrConflict{Endpoint: endpoint, Status: status, LocalPayload: append([]byte(nil), opts.localPayload...), RemotePayload: RedactJSONBody(body), Message: msg}
	default:
		return ErrNetworkUnavailable{Endpoint: endpoint, Status: status, Attempts: 1}
	}
}

func issueListQuery(req IssueListRequest) url.Values {
	values := url.Values{}
	if req.State != "" {
		values.Set("state", req.State)
	}
	for _, label := range req.Labels {
		values.Add("labels", label)
	}
	return values
}

func cloneValues(values url.Values) url.Values {
	clone := url.Values{}
	for key, value := range values {
		clone[key] = append([]string(nil), value...)
	}
	return clone
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func responseMessage(body []byte) string {
	var payload struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(body, &payload) == nil {
		if payload.Message != "" {
			return payload.Message
		}
		return payload.Error
	}
	return ""
}

func decodeMessage(err error) string {
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return "truncated JSON"
	}
	return "malformed JSON"
}

func parseRetryAfter(raw string, now time.Time) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(raw); err == nil {
		if seconds < 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(raw); err == nil {
		if at.Before(now) {
			return 0
		}
		return at.Sub(now)
	}
	return 0
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isRetryableTransport(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout() || netErr.Temporary()
	}
	return true
}
