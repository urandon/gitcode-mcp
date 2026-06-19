package gitcode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const defaultMaxResponseSize int64 = 10 << 20

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
	var items []IssueSummary
	endpoint := c.path("/api/v5/repos/%s/%s/issues", req.Owner, req.Repo)
	err := c.getJSON(ctx, endpoint, issueListQuery(req), &items)
	if err != nil {
		return Page[IssueSummary]{}, err
	}
	return Page[IssueSummary]{Items: items, Page: firstPositive(req.Page, c.pagination.Page, 1), PerPage: firstPositive(req.PerPage, c.pagination.PerPage, len(items))}, nil
}

func (c *HTTPClient) GetIssue(ctx context.Context, req IssueRequest) (Issue, error) {
	var issue Issue
	endpoint := c.path("/api/v5/repos/%s/%s/issues/%d", req.Owner, req.Repo, req.Number)
	err := c.getJSONWithOptions(ctx, endpoint, nil, &issue, requestOptions{knownRemoteAlias: req.KnownRemoteAlias, remoteAlias: req.RemoteAlias})
	if err != nil {
		return Issue{}, err
	}
	return issue, nil
}

func (c *HTTPClient) ListIssueComments(ctx context.Context, req IssueRequest) (Page[Comment], error) {
	var items []Comment
	endpoint := c.path("/api/v5/repos/%s/%s/issues/%d/comments", req.Owner, req.Repo, req.Number)
	err := c.getJSON(ctx, endpoint, nil, &items)
	if err != nil {
		return Page[Comment]{}, err
	}
	return Page[Comment]{Items: items}, nil
}

func (c *HTTPClient) GetWikiPage(ctx context.Context, req WikiPageRequest) (WikiPage, error) {
	var page WikiPage
	endpoint := c.path("/api/v5/repos/%s/%s/wiki/%s", req.Owner, req.Repo, req.Slug)
	err := c.getJSON(ctx, endpoint, nil, &page)
	if err != nil {
		return WikiPage{}, err
	}
	return page, nil
}

func (c *HTTPClient) ListWikiPages(ctx context.Context, req WikiListRequest) (Page[WikiPage], error) {
	var items []WikiPage
	endpoint := c.path("/api/v5/repos/%s/%s/wiki", req.Owner, req.Repo)
	values := url.Values{}
	addPage(values, firstPositive(req.Page, c.pagination.Page, 0), firstPositive(req.PerPage, c.pagination.PerPage, 0))
	err := c.getJSON(ctx, endpoint, values, &items)
	if err != nil {
		return Page[WikiPage]{}, err
	}
	return Page[WikiPage]{Items: items}, nil
}

func (c *HTTPClient) Search(ctx context.Context, req SearchRequest) (Page[SearchResult], error) {
	var items []SearchResult
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
	addPage(values, firstPositive(req.Page, c.pagination.Page, 0), firstPositive(req.PerPage, c.pagination.PerPage, 0))
	err := c.getJSON(ctx, "/api/v5/search", values, &items)
	if err != nil {
		return Page[SearchResult]{}, err
	}
	return Page[SearchResult]{Items: items}, nil
}

func (c *HTTPClient) ListIssueAttachments(ctx context.Context, req IssueRequest) (Page[AttachmentSummary], error) {
	var items []AttachmentSummary
	endpoint := c.path("/api/v5/repos/%s/%s/issues/%d/attachments", req.Owner, req.Repo, req.Number)
	err := c.getJSON(ctx, endpoint, nil, &items)
	if err != nil {
		return Page[AttachmentSummary]{}, err
	}
	return Page[AttachmentSummary]{Items: items}, nil
}

func (c *HTTPClient) GetAttachment(ctx context.Context, req AttachmentRequest) (AttachmentBody, error) {
	endpoint := c.path("/api/v5/repos/%s/%s/issues/%d/attachments/%s", req.Owner, req.Repo, req.IssueNumber, req.AttachmentID)
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

type requestOptions struct {
	knownRemoteAlias bool
	remoteAlias      string
}

func (c *HTTPClient) getJSON(ctx context.Context, endpoint string, values url.Values, out any) error {
	return c.getJSONWithOptions(ctx, endpoint, values, out, requestOptions{})
}

func (c *HTTPClient) getJSONWithOptions(ctx context.Context, endpoint string, values url.Values, out any, opts requestOptions) error {
	body, _, err := c.getBytesWithOptions(ctx, endpoint, values, opts)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(out); err != nil {
		return ErrPartialResponse{Endpoint: endpoint, Got: int64(len(body)), Cause: err, Message: decodeMessage(err)}
	}
	return nil
}

func (c *HTTPClient) getBytes(ctx context.Context, endpoint string, values url.Values) ([]byte, http.Header, error) {
	return c.getBytesWithOptions(ctx, endpoint, values, requestOptions{})
}

func (c *HTTPClient) getBytesWithOptions(ctx context.Context, endpoint string, values url.Values, opts requestOptions) ([]byte, http.Header, error) {
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
		resp, err := c.do(ctx, http.MethodGet, endpoint, values, nil)
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
			return body, resp.Header, nil
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

func (c *HTTPClient) do(ctx context.Context, method, endpoint string, values url.Values, body io.Reader) (*http.Response, error) {
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
		return ErrConflict{Endpoint: endpoint, Status: status, RemotePayload: append([]byte(nil), body...), Message: msg}
	default:
		return ErrNetworkUnavailable{Endpoint: endpoint, Status: status, Attempts: 1}
	}
}

func (c *HTTPClient) path(format string, args ...any) string {
	escaped := make([]any, len(args))
	for i, arg := range args {
		switch v := arg.(type) {
		case string:
			escaped[i] = url.PathEscape(v)
		default:
			escaped[i] = v
		}
	}
	return fmt.Sprintf(format, escaped...)
}

func issueListQuery(req IssueListRequest) url.Values {
	values := url.Values{}
	if req.State != "" {
		values.Set("state", req.State)
	}
	for _, label := range req.Labels {
		values.Add("labels", label)
	}
	addPage(values, req.Page, req.PerPage)
	return values
}

func addPage(values url.Values, page, perPage int) {
	if page > 0 {
		values.Set("page", strconv.Itoa(page))
	}
	if perPage > 0 {
		values.Set("per_page", strconv.Itoa(perPage))
	}
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
