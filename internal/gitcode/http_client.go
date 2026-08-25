package gitcode

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gitcode-mcp/internal/diagnostics"
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
	rateLimiter     *clientRateLimiter
	rateLimiterMu   sync.Mutex
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
	return &HTTPClient{
		baseURL:         parsed,
		token:           cfg.Token,
		maxResponseSize: limit,
		maxRetries:      cfg.MaxRetries,
		userAgent:       ua,
		client:          client,
		pagination:      cfg.Pagination,
		rateLimiter:     newClientRateLimiter(cfg.RateLimitRPS, cfg.RateLimitBurst),
	}, nil
}

func (c *HTTPClient) ListIssues(ctx context.Context, req IssueListRequest) (Page[IssueSummary], error) {
	if err := validateReadRepo(req.Owner, req.Repo); err != nil {
		return Page[IssueSummary]{}, err
	}
	endpoint := listIssuesEndpoint(req.Owner, req.Repo)
	items, page, err := getPaged[IssueSummary](ctx, c, endpoint, issueListQuery(req), PageState{Page: req.Page, PerPage: req.PerPage})
	if err != nil {
		if !recoverableCollectionDecode(err) {
			items = nil
		}
		return Page[IssueSummary]{Items: items, Page: page.Page, PerPage: page.PerPage}, err
	}
	return Page[IssueSummary]{Items: items, Page: page.Page, PerPage: page.PerPage}, nil
}

func (c *HTTPClient) GetIssue(ctx context.Context, req IssueRequest) (Issue, error) {
	if err := validateIssueRequest(req); err != nil {
		return Issue{}, err
	}
	var issue Issue
	endpoint := getIssueEndpoint(req.Owner, req.Repo, req.Number)
	err := c.getJSONWithOptions(ctx, endpoint, nil, &issue, requestOptions{knownRemoteAlias: req.KnownRemoteAlias, remoteAlias: req.RemoteAlias})
	if err != nil {
		return Issue{}, err
	}
	return issue, nil
}

func (c *HTTPClient) ListIssueComments(ctx context.Context, req IssueRequest) (Page[Comment], error) {
	if err := validateIssueRequest(req); err != nil {
		return Page[Comment]{}, err
	}
	endpoint := listIssueCommentsEndpoint(req.Owner, req.Repo, req.Number)
	items, page, err := getPaged[Comment](ctx, c, endpoint, nil, PageState{})
	if err != nil {
		return Page[Comment]{}, err
	}
	return Page[Comment]{Items: items, Page: page.Page, PerPage: page.PerPage}, nil
}

func (c *HTTPClient) ListRepositoryIssueComments(ctx context.Context, req RepositoryIssueCommentListRequest) (Page[Comment], error) {
	if err := validateReadRepo(req.Owner, req.Repo); err != nil {
		return Page[Comment]{}, err
	}
	page := firstPositive(req.Page, c.pagination.Page, 1)
	perPage := firstPositive(req.PerPage, c.pagination.PerPage, 100)
	endpoint := listRepositoryIssueCommentsEndpoint(req.Owner, req.Repo)
	values := url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(perPage)}}
	body, headers, err := c.getBytes(ctx, endpoint, values)
	if err != nil {
		var notFound ErrNotFound
		var validation ErrAPIValidation
		if errors.As(err, &notFound) || (errors.As(err, &validation) && validation.Status == http.StatusMethodNotAllowed) {
			return Page[Comment]{}, ErrUnsupportedCapability{CapabilityKey: "repository_issue_comments", Message: "repository-wide issue comments are unavailable; use the per-issue compatibility path"}
		}
		return Page[Comment]{}, err
	}
	var items []Comment
	if err := decodeJSON(endpoint, body, &items); err != nil {
		return Page[Comment]{}, err
	}
	totalCount := headerInt(headers, "total_count")
	totalPages := headerInt(headers, "total_page")
	nextPage := headerInt(headers, "X-Next-Page")
	if nextPage == 0 && totalPages > page {
		nextPage = page + 1
	}
	return Page[Comment]{Items: items, Page: page, PerPage: perPage, TotalCount: totalCount, NextPage: nextPage}, nil
}

func (c *HTTPClient) ListPRs(ctx context.Context, req PRListRequest) (Page[PullRequest], error) {
	if err := validateReadRepo(req.Owner, req.Repo); err != nil {
		return Page[PullRequest]{}, err
	}
	endpoint := listPREndpoint(req.Owner, req.Repo)
	items, page, err := getPaged[PullRequest](ctx, c, endpoint, prListQuery(req), PageState{Page: req.Page, PerPage: req.PerPage})
	if err != nil {
		return Page[PullRequest]{}, err
	}
	return Page[PullRequest]{Items: items, Page: page.Page, PerPage: page.PerPage}, nil
}

func (c *HTTPClient) GetPR(ctx context.Context, req PRRequest) (PullRequest, error) {
	if err := validatePRRequest(req); err != nil {
		return PullRequest{}, err
	}
	var pr PullRequest
	endpoint := getPREndpoint(req.Owner, req.Repo, req.Number)
	if err := c.getJSON(ctx, endpoint, nil, &pr); err != nil {
		return PullRequest{}, err
	}
	return pr, nil
}

func (c *HTTPClient) ListPRComments(ctx context.Context, req PRRequest) (Page[PRComment], error) {
	if err := validatePRRequest(req); err != nil {
		return Page[PRComment]{}, err
	}
	endpoint := listPRCommentsEndpoint(req.Owner, req.Repo, req.Number)
	items, page, err := getPaged[PRComment](ctx, c, endpoint, nil, PageState{})
	if err != nil {
		return Page[PRComment]{}, err
	}
	items = flattenPRCommentReplies(items, req.Number)
	values := url.Values{"page": {"1"}, "per_page": {"100"}, "sort": {"asc"}, "type": {"user"}}
	if body, _, discussionErr := c.getBytes(ctx, listPRDiscussionsEndpoint(req.Owner, req.Repo, req.Number), values); discussionErr == nil {
		if discussionItems, decodeErr := decodePRDiscussionComments(listPRDiscussionsEndpoint(req.Owner, req.Repo, req.Number), body, req.Number); decodeErr == nil {
			items = mergePRCommentRepresentations(items, discussionItems)
		}
	}
	return Page[PRComment]{Items: items, Page: page.Page, PerPage: page.PerPage}, nil
}

func flattenPRCommentReplies(items []PRComment, prNumber int) []PRComment {
	flattened := make([]PRComment, 0, len(items))
	for _, root := range items {
		root.PRNumber = prNumber
		replies := root.Replies
		root.Replies = nil
		flattened = append(flattened, root)
		for _, reply := range replies {
			reply.PRNumber = prNumber
			reply.DiscussionID = firstNonEmpty(reply.DiscussionID, root.DiscussionID)
			reply.ParentID = firstNonEmpty(reply.ParentID, root.ID)
			reply.ReviewKind = firstNonEmpty(reply.ReviewKind, root.ReviewKind)
			reply.Path = firstNonEmpty(reply.Path, root.Path)
			reply.Line = firstPositive(reply.Line, root.Line)
			if len(reply.Positions) == 0 {
				reply.Positions = append([]PRCommentPosition(nil), root.Positions...)
			}
			flattened = append(flattened, reply)
		}
	}
	return flattened
}

func mergePRCommentRepresentations(v5, discussions []PRComment) []PRComment {
	v5ByID := make(map[string]PRComment, len(v5))
	for _, comment := range v5 {
		v5ByID[comment.ID] = comment
	}
	merged := make([]PRComment, 0, len(v5)+len(discussions))
	seen := make(map[string]bool, len(v5)+len(discussions))
	for _, comment := range discussions {
		if fallback, ok := v5ByID[comment.ID]; ok {
			comment.ParentID = firstNonEmpty(comment.ParentID, fallback.ParentID)
			comment.DiscussionID = firstNonEmpty(comment.DiscussionID, fallback.DiscussionID)
			comment.Body = firstNonEmpty(comment.Body, fallback.Body)
			comment.Author = firstNonEmpty(comment.Author, fallback.Author)
			if comment.CreatedAt.IsZero() {
				comment.CreatedAt = fallback.CreatedAt
			}
			if comment.UpdatedAt.IsZero() {
				comment.UpdatedAt = fallback.UpdatedAt
			}
		}
		merged = append(merged, comment)
		seen[comment.ID] = true
	}
	for _, comment := range v5 {
		if !seen[comment.ID] {
			merged = append(merged, comment)
		}
	}
	return merged
}

func (c *HTTPClient) CreatePR(ctx context.Context, req CreatePRRequest, opts WriteOptions) (WriteResult[PullRequest], error) {
	if err := validateCreatePR(req); err != nil {
		return WriteResult[PullRequest]{}, err
	}
	target := req.Owner + "/" + req.Repo
	return writeConfirmedJSON[PullRequest](ctx, c, http.MethodPost, listPREndpoint(req.Owner, req.Repo), "CreatePR", target, req, opts, func(result WriteResult[PullRequest]) (WriteResult[PullRequest], error) {
		pr := result.Record
		if strings.TrimSpace(pr.ID) == "" || pr.Number <= 0 {
			return WriteResult[PullRequest]{}, ErrValidationFailed{Field: "response", Message: "pull request create confirmation requires id and number"}
		}
		result.RemoteID = pr.ID
		result.RemoteNumber = pr.Number
		return result, nil
	})
}

func (c *HTTPClient) UpdatePR(ctx context.Context, req UpdatePRRequest, opts WriteOptions) (WriteResult[PullRequest], error) {
	if err := validateUpdatePR(req); err != nil {
		return WriteResult[PullRequest]{}, err
	}
	target := req.Owner + "/" + req.Repo + "/pulls/" + strconv.Itoa(req.Number)
	key := opts.IdempotencyKey
	if key == "" {
		key = GenerateIdempotencyKey("UpdatePR", target, req, opts)
		opts.IdempotencyKey = key
	}
	result, err := writeConfirmedJSON[PullRequest](ctx, c, http.MethodPatch, getPREndpoint(req.Owner, req.Repo, req.Number), "UpdatePR", target, req, opts, func(result WriteResult[PullRequest]) (WriteResult[PullRequest], error) {
		pr := result.Record
		if strings.TrimSpace(pr.ID) == "" || pr.Number != req.Number {
			return WriteResult[PullRequest]{}, ErrValidationFailed{Field: "response", Message: "pull request update confirmation requires id and matching number"}
		}
		result.RemoteID = pr.ID
		result.RemoteNumber = pr.Number
		return result, nil
	})
	if err == nil {
		return result, nil
	}
	var partial ErrPartialResponse
	if !errors.As(err, &partial) {
		return WriteResult[PullRequest]{}, err
	}
	pr, getErr := c.GetPR(ctx, PRRequest{Owner: req.Owner, Repo: req.Repo, Number: req.Number})
	if getErr != nil {
		return WriteResult[PullRequest]{}, err
	}
	if strings.TrimSpace(pr.ID) == "" || pr.Number != req.Number {
		return WriteResult[PullRequest]{}, ErrValidationFailed{Field: "response", Message: "pull request update read-back requires id and matching number"}
	}
	body, _ := json.Marshal(pr)
	hash := sha256.Sum256(body)
	fingerprint := sha256.Sum256(RedactJSONBody(body, target))
	return WriteResult[PullRequest]{Record: pr, Confirmed: true, Operation: "UpdatePR", Target: target, ProviderStatus: "2xx-readback", IdempotencyKey: key, ResponseHash: hex.EncodeToString(hash[:]), ProviderPayloadFingerprint: hex.EncodeToString(fingerprint[:]), RemoteID: pr.ID, RemoteNumber: pr.Number, ConfirmedAt: time.Now().UTC()}, nil
}

func (c *HTTPClient) MergePR(ctx context.Context, req MergePRRequest, opts WriteOptions) (WriteResult[PullRequest], error) {
	if err := validateMergePR(req); err != nil {
		return WriteResult[PullRequest]{}, err
	}
	target := req.Owner + "/" + req.Repo + "/pulls/" + strconv.Itoa(req.Number) + "/merge"
	key := opts.IdempotencyKey
	if key == "" {
		key = GenerateIdempotencyKey("MergePR", target, req, opts)
		opts.IdempotencyKey = key
	}
	before, err := c.GetPR(ctx, PRRequest{Owner: req.Owner, Repo: req.Repo, Number: req.Number})
	if err != nil {
		return WriteResult[PullRequest]{}, err
	}
	if strings.TrimSpace(req.HeadSHA) != "" && !strings.EqualFold(strings.TrimSpace(req.HeadSHA), strings.TrimSpace(before.HeadSHA)) {
		return WriteResult[PullRequest]{}, ErrConflict{Endpoint: mergePREndpoint(req.Owner, req.Repo, req.Number), Status: http.StatusConflict, Message: "pull request head SHA changed before merge"}
	}
	if strings.EqualFold(strings.TrimSpace(before.State), "merged") {
		return confirmedExistingPRMerge(before, target, key)
	}
	merged, err := writeConfirmedJSON[MergePRResponse](ctx, c, http.MethodPut, mergePREndpoint(req.Owner, req.Repo, req.Number), "MergePR", target, req, opts, func(result WriteResult[MergePRResponse]) (WriteResult[MergePRResponse], error) {
		if !mergeResponseConfirmed(result.Record.Merged) {
			return WriteResult[MergePRResponse]{}, ErrValidationFailed{Field: "response.merged", Message: "merge confirmation requires merged=true"}
		}
		return result, nil
	})
	if err != nil {
		return WriteResult[PullRequest]{}, err
	}
	pr, err := c.GetPR(ctx, PRRequest{Owner: req.Owner, Repo: req.Repo, Number: req.Number})
	if err != nil {
		return WriteResult[PullRequest]{}, ErrPartialResponse{Endpoint: mergePREndpoint(req.Owner, req.Repo, req.Number), Cause: err, Message: "merge succeeded but pull request readback failed"}
	}
	if pr.Number != req.Number || strings.TrimSpace(pr.ID) == "" || !strings.EqualFold(strings.TrimSpace(pr.State), "merged") {
		return WriteResult[PullRequest]{}, ErrPartialResponse{Endpoint: mergePREndpoint(req.Owner, req.Repo, req.Number), Cause: ErrValidationFailed{Field: "readback.state", Message: "merged pull request readback requires state=merged"}, Message: "merge succeeded but readback did not confirm state=merged"}
	}
	return WriteResult[PullRequest]{Record: pr, Confirmed: merged.Confirmed, Operation: merged.Operation, Target: merged.Target, ProviderStatus: merged.ProviderStatus, RemoteID: pr.ID, RemoteNumber: pr.Number, RemoteRevision: strings.TrimSpace(merged.Record.SHA), BrowserURL: pr.HTMLURL, IdempotencyKey: merged.IdempotencyKey, ResponseHash: merged.ResponseHash, ConfirmedAt: merged.ConfirmedAt, ProviderPayloadFingerprint: merged.ProviderPayloadFingerprint}, nil
}

func confirmedExistingPRMerge(pr PullRequest, target, key string) (WriteResult[PullRequest], error) {
	body, err := json.Marshal(pr)
	if err != nil {
		return WriteResult[PullRequest]{}, err
	}
	hash := sha256.Sum256(body)
	fingerprint := sha256.Sum256(RedactJSONBody(body, target))
	return WriteResult[PullRequest]{Record: pr, Confirmed: true, Operation: "MergePR", Target: target, ProviderStatus: "readback-existing", RemoteID: pr.ID, RemoteNumber: pr.Number, BrowserURL: pr.HTMLURL, IdempotencyKey: key, ResponseHash: hex.EncodeToString(hash[:]), ConfirmedAt: time.Now().UTC(), ProviderPayloadFingerprint: hex.EncodeToString(fingerprint[:])}, nil
}

func mergeResponseConfirmed(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case float64:
		return typed == 1
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true") || strings.TrimSpace(typed) == "1"
	default:
		return false
	}
}

func (c *HTTPClient) LinkPRIssue(ctx context.Context, req LinkPRIssueRequest, opts WriteOptions) (WriteResult[[]Issue], error) {
	if err := validateLinkPRIssue(req); err != nil {
		return WriteResult[[]Issue]{}, err
	}
	target := req.Owner + "/" + req.Repo + "/pulls/" + strconv.Itoa(req.Number) + "/issues/" + strconv.Itoa(req.IssueNumber)
	result, err := writeConfirmedJSON[[]Issue](ctx, c, http.MethodPost, linkPRIssueEndpoint(req.Owner, req.Repo, req.Number), "LinkPRIssue", target, []int{req.IssueNumber}, opts, func(result WriteResult[[]Issue]) (WriteResult[[]Issue], error) {
		if !linkedIssuesContain(result.Record, req.IssueNumber) {
			return WriteResult[[]Issue]{}, ErrValidationFailed{Field: "response", Message: "pull request issue link confirmation requires matching linked issue"}
		}
		result.RemoteID = strconv.Itoa(req.Number)
		result.RemoteNumber = req.Number
		return result, nil
	})
	if err != nil {
		return WriteResult[[]Issue]{}, linkPRIssueUnsupportedError(err)
	}
	return result, nil
}

func (c *HTTPClient) GetWikiPage(ctx context.Context, req WikiPageRequest) (WikiPage, error) {
	if err := validateWikiPageRequest(req); err != nil {
		return WikiPage{}, err
	}
	return c.getWikiPageByPath(ctx, req.Owner, req.Repo, wikiRequestPath(req.Path, req.Slug))
}

func (c *HTTPClient) ListWikiPages(ctx context.Context, req WikiListRequest) (Page[WikiPage], error) {
	if err := validateReadRepo(req.Owner, req.Repo); err != nil {
		return Page[WikiPage]{}, err
	}
	walker := &wikiTraversal{client: c, owner: req.Owner, repo: req.Repo, seenDirs: map[string]bool{}, seenFiles: map[string]bool{}}
	pageNumber := firstPositive(req.Page, 1)
	perPage := req.PerPage
	if req.Bounds != nil {
		if perPage <= 0 {
			perPage = firstPositive(req.Bounds.MaxRecords, 1)
		}
		if pageNumber > 1 && perPage > 0 {
			maxInt := int(^uint(0) >> 1)
			if pageNumber-1 > maxInt/perPage {
				return Page[WikiPage]{}, ErrValidationFailed{Field: "page", Message: "wiki page offset is too large"}
			}
			walker.skipRecords = (pageNumber - 1) * perPage
		}
		walker.maxRecords = req.Bounds.MaxRecords
		walker.progressChan = req.Bounds.ProgressChan
	}
	items, err := walker.walk(ctx, "", 0)
	if err != nil {
		return Page[WikiPage]{}, err
	}
	nextPage := 0
	if req.Bounds != nil && req.Bounds.MaxRecords > 0 && walker.hasMore {
		logicalPages := (len(items) + perPage - 1) / perPage
		nextPage = pageNumber + logicalPages
	}
	return Page[WikiPage]{Items: items, Page: pageNumber, PerPage: firstPositive(perPage, len(items)), TotalCount: len(items), NextPage: nextPage}, nil
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
	return writeConfirmedJSON[Issue](ctx, c, http.MethodPost, createIssueEndpoint(req.Owner, req.Repo), "CreateIssue", target, createIssuePayload(req), opts, func(result WriteResult[Issue]) (WriteResult[Issue], error) {
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
	wireState, expectedState, err := normalizeIssueUpdateState(req.State)
	if err != nil {
		return WriteResult[Issue]{}, err
	}
	req.State = wireState
	target := req.Owner + "/" + req.Repo + "/" + strconv.Itoa(req.Number)
	endpoint := updateIssueEndpoint(req.Owner, req.Repo, req.Number)
	result, err := writeConfirmedJSON[Issue](ctx, c, http.MethodPatch, endpoint, "UpdateIssue", target, updateIssuePayload(req), opts, func(result WriteResult[Issue]) (WriteResult[Issue], error) {
		issue := result.Record
		if strings.TrimSpace(issue.ID) == "" || issue.Number != req.Number {
			return WriteResult[Issue]{}, ErrValidationFailed{Field: "response", Message: "issue update confirmation requires id and matching number"}
		}
		result.RemoteID = issue.ID
		result.RemoteNumber = issue.Number
		return result, nil
	})
	if err != nil || expectedState == "" {
		return result, err
	}
	readback, err := c.GetIssue(ctx, IssueRequest{Owner: req.Owner, Repo: req.Repo, Number: req.Number})
	if err != nil {
		return WriteResult[Issue]{}, ErrWriteConfirmationIncomplete{Endpoint: endpoint, Message: "issue state update requires readback", Cause: err}
	}
	if strings.TrimSpace(readback.ID) == "" || readback.Number != req.Number {
		return WriteResult[Issue]{}, ErrValidationFailed{Field: "response", Message: "issue state update readback requires id and matching number"}
	}
	if strings.TrimSpace(readback.State) != expectedState {
		return WriteResult[Issue]{}, ErrValidationFailed{Field: "state", Message: fmt.Sprintf("issue state update readback = %q, want %q", readback.State, expectedState)}
	}
	result.Record = readback
	result.RemoteID = readback.ID
	result.RemoteNumber = readback.Number
	result.ProviderStatus = "2xx-readback"
	if !readback.UpdatedAt.IsZero() {
		result.RemoteRevision = readback.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	return result, nil
}

func (c *HTTPClient) CreateIssueComment(ctx context.Context, req CreateIssueCommentRequest, opts WriteOptions) (WriteResult[Comment], error) {
	if err := validateCreateIssueComment(req); err != nil {
		return WriteResult[Comment]{}, err
	}
	target := req.Owner + "/" + req.Repo + "/" + strconv.Itoa(req.Number)
	return writeConfirmedSchemaJSON[Comment](ctx, c, http.MethodPost, createIssueCommentEndpoint(req.Owner, req.Repo, req.Number), "CreateIssueComment", target, req, opts, func(result WriteResult[Comment]) (WriteResult[Comment], error) {
		comment := result.Record
		if strings.TrimSpace(comment.ID) == "" {
			return WriteResult[Comment]{}, &ErrSchemaDecode{Field: "comment.id", Expected: "note_id or id", Received: "missing"}
		}
		result.RemoteID = comment.ID
		result.ParentIssueNumber = req.Number
		result.ParentIssueID = comment.IssueID
		return result, nil
	})
}

func (c *HTTPClient) UpdateIssueComment(ctx context.Context, req UpdateIssueCommentRequest, opts WriteOptions) (WriteResult[Comment], error) {
	if err := validateUpdateIssueComment(req); err != nil {
		return WriteResult[Comment]{}, err
	}
	target := req.Owner + "/" + req.Repo + "/issues/comments/" + req.CommentID
	key := opts.IdempotencyKey
	if key == "" {
		key = GenerateIdempotencyKey("UpdateIssueComment", target, req, opts)
		opts.IdempotencyKey = key
	}
	result, err := writeConfirmedSchemaJSON[Comment](ctx, c, http.MethodPatch, updateIssueCommentEndpoint(req.Owner, req.Repo, req.CommentID), "UpdateIssueComment", target, req, opts, func(result WriteResult[Comment]) (WriteResult[Comment], error) {
		comment := result.Record
		if strings.TrimSpace(comment.ID) == "" {
			return WriteResult[Comment]{}, &ErrSchemaDecode{Field: "comment.id", Expected: "note_id or id", Received: "missing"}
		}
		if comment.ID != req.CommentID {
			return WriteResult[Comment]{}, ErrValidationFailed{Field: "comment_id", Message: "comment update confirmation requires matching id"}
		}
		result.RemoteID = comment.ID
		result.ParentIssueNumber = req.Number
		result.ParentIssueID = comment.IssueID
		return result, nil
	})
	if err == nil {
		return result, nil
	}
	var schemaErr *ErrSchemaDecode
	var partial ErrPartialResponse
	if !errors.As(err, &schemaErr) && !errors.As(err, &partial) {
		return WriteResult[Comment]{}, err
	}
	comment, getErr := c.getIssueComment(ctx, req)
	if getErr != nil {
		return WriteResult[Comment]{}, err
	}
	if strings.TrimSpace(comment.ID) == "" || comment.ID != req.CommentID {
		return WriteResult[Comment]{}, ErrValidationFailed{Field: "comment_id", Message: "comment update read-back requires matching id"}
	}
	if comment.Body != req.Body {
		return WriteResult[Comment]{}, ErrValidationFailed{Field: "body", Message: "comment update read-back requires matching body"}
	}
	body, _ := json.Marshal(comment)
	hash := sha256.Sum256(body)
	fingerprint := sha256.Sum256(RedactJSONBody(body, target))
	return WriteResult[Comment]{Record: comment, Confirmed: true, Operation: "UpdateIssueComment", Target: target, ProviderStatus: "2xx-readback", IdempotencyKey: key, ResponseHash: hex.EncodeToString(hash[:]), ProviderPayloadFingerprint: hex.EncodeToString(fingerprint[:]), RemoteID: comment.ID, ParentIssueNumber: req.Number, ParentIssueID: comment.IssueID, ConfirmedAt: time.Now().UTC()}, nil
}

func (c *HTTPClient) getIssueComment(ctx context.Context, req UpdateIssueCommentRequest) (Comment, error) {
	var comment Comment
	err := c.getJSON(ctx, getIssueCommentEndpoint(req.Owner, req.Repo, req.CommentID), nil, &comment)
	if err != nil {
		return Comment{}, err
	}
	return comment, nil
}

func (c *HTTPClient) CreatePRComment(ctx context.Context, req CreatePRCommentRequest, opts WriteOptions) (WriteResult[PRComment], error) {
	if err := validateCreatePRComment(req); err != nil {
		return WriteResult[PRComment]{}, err
	}
	target := req.Owner + "/" + req.Repo + "/pulls/" + strconv.Itoa(req.Number)
	return writeConfirmedSchemaJSON[PRComment](ctx, c, http.MethodPost, createPRCommentEndpoint(req.Owner, req.Repo, req.Number), "CreatePRComment", target, req, opts, func(result WriteResult[PRComment]) (WriteResult[PRComment], error) {
		comment := result.Record
		if strings.TrimSpace(comment.ID) == "" {
			return WriteResult[PRComment]{}, &ErrSchemaDecode{Field: "pr_comment.id", Expected: "note_id or id", Received: "missing"}
		}
		comment.PRNumber = req.Number
		if comment.DiscussionID == "" {
			comment.DiscussionID = strconv.Itoa(req.Number)
		}
		result.Record = comment
		result.RemoteID = comment.ID
		result.ParentIssueNumber = req.Number
		result.ParentIssueID = comment.DiscussionID
		return result, nil
	})
}

func (c *HTTPClient) CreatePRReviewComment(ctx context.Context, req CreatePRReviewCommentRequest, opts WriteOptions) (WriteResult[PRComment], error) {
	if err := validateCreatePRReviewComment(req); err != nil {
		return WriteResult[PRComment]{}, err
	}
	if req.Line == 0 {
		req.Line = req.Position
	}
	req.Position = req.Line
	pr, err := c.GetPR(ctx, PRRequest{Owner: req.Owner, Repo: req.Repo, Number: req.Number})
	if err != nil {
		return WriteResult[PRComment]{}, err
	}
	if pr.BaseSHA == "" || pr.HeadSHA == "" {
		return WriteResult[PRComment]{}, ErrValidationFailed{Field: "pull_request.sha", Message: "inline review comment requires base and head sha from pull request detail"}
	}
	target := req.Owner + "/" + req.Repo + "/pulls/" + strconv.Itoa(req.Number)
	endpoint := createPRCommentEndpoint(req.Owner, req.Repo, req.Number)
	payload := prReviewCommentPayload(req, pr)
	key := opts.IdempotencyKey
	if key == "" {
		key = GenerateIdempotencyKey("CreatePRReviewComment", target, payload, opts)
		opts.IdempotencyKey = key
	}
	result, err := writeConfirmedSchemaJSON[PRComment](ctx, c, http.MethodPost, endpoint, "CreatePRReviewComment", target, payload, opts, func(result WriteResult[PRComment]) (WriteResult[PRComment], error) {
		created := result.Record
		if strings.TrimSpace(created.ID) == "" {
			return WriteResult[PRComment]{}, &ErrSchemaDecode{Field: "pr_review_comment.id", Expected: "note_id or id", Received: "missing"}
		}
		if created.Body != "" && created.Body != req.Body {
			return WriteResult[PRComment]{}, ErrValidationFailed{Field: "body", Message: "inline review comment confirmation requires matching body"}
		}
		result.Record = requestConfirmedPRReviewComment(created, req)
		result.RemoteID = result.Record.ID
		result.ParentIssueNumber = req.Number
		result.ParentIssueID = result.Record.DiscussionID
		return result, nil
	})
	if err != nil {
		return WriteResult[PRComment]{}, err
	}
	return c.confirmCreatedPRReviewComment(ctx, endpoint, req, pr, result)
}

func (c *HTTPClient) confirmCreatedPRReviewComment(ctx context.Context, endpoint string, req CreatePRReviewCommentRequest, pr PullRequest, result WriteResult[PRComment]) (WriteResult[PRComment], error) {
	readback, err := c.ListPRComments(ctx, PRRequest{Owner: req.Owner, Repo: req.Repo, Number: req.Number})
	if err != nil {
		return WriteResult[PRComment]{}, ErrWriteConfirmationIncomplete{Endpoint: endpoint, Message: "inline review comment requires anchor readback", RemoteID: result.RemoteID, Cause: err}
	}
	for _, comment := range readback.Items {
		if comment.ID != result.RemoteID {
			continue
		}
		path, line := prReviewCommentAnchor(comment)
		if path == "" || line <= 0 {
			return WriteResult[PRComment]{}, ErrWriteConfirmationIncomplete{Endpoint: endpoint, Message: "inline review comment readback omitted path or line", RemoteID: result.RemoteID}
		}
		if path != req.Path || line != req.Line {
			return WriteResult[PRComment]{}, ErrPRReviewAnchorMismatch{Endpoint: endpoint, CommentID: result.RemoteID, ExpectedPath: req.Path, ActualPath: path, ExpectedLine: req.Line, ActualLine: line}
		}
		if comment.Body != "" && comment.Body != req.Body {
			return WriteResult[PRComment]{}, ErrWriteConfirmationIncomplete{Endpoint: endpoint, Message: "inline review comment readback body mismatch", RemoteID: result.RemoteID}
		}
		comment.Body = firstNonEmpty(comment.Body, req.Body)
		comment.PRNumber = req.Number
		comment.ReviewKind = "inline"
		result.Record = ensurePRReviewCommentPosition(comment, req, pr)
		result.ParentIssueID = result.Record.DiscussionID
		result.ProviderStatus += "-readback"
		return result, nil
	}
	return WriteResult[PRComment]{}, ErrWriteConfirmationIncomplete{Endpoint: endpoint, Message: "created inline review comment was absent from readback", RemoteID: result.RemoteID}
}

func prReviewCommentAnchor(comment PRComment) (string, int) {
	for _, position := range comment.Positions {
		if position.PositionKind != "" && position.PositionKind != "current" {
			continue
		}
		path := firstNonEmpty(position.NewPath, position.OldPath)
		line := firstPositive(position.NewLine, position.OldLine)
		if path != "" && line > 0 {
			return path, line
		}
	}
	return strings.TrimSpace(comment.Path), comment.Line
}

func (c *HTTPClient) ReplyPRReviewComment(ctx context.Context, req ReplyPRReviewCommentRequest, opts WriteOptions) (WriteResult[PRComment], error) {
	if err := validateReplyPRReviewComment(req); err != nil {
		return WriteResult[PRComment]{}, err
	}
	comments, err := c.ListPRComments(ctx, PRRequest{Owner: req.Owner, Repo: req.Repo, Number: req.Number})
	if err != nil {
		return WriteResult[PRComment]{}, err
	}
	if strings.HasPrefix(req.DiscussionID, "comment:") {
		syntheticParent := strings.TrimPrefix(req.DiscussionID, "comment:")
		if syntheticParent != req.ParentCommentID {
			return WriteResult[PRComment]{}, ErrDiscussionReplyUnavailable{DiscussionID: req.DiscussionID, ParentCommentID: req.ParentCommentID, Message: "synthetic thread id does not match the requested parent comment"}
		}
		resolved := ""
		for _, comment := range comments.Items {
			if comment.ID == req.ParentCommentID {
				resolved = strings.TrimSpace(comment.DiscussionID)
				break
			}
		}
		if resolved == "" {
			return WriteResult[PRComment]{}, ErrDiscussionReplyUnavailable{DiscussionID: req.DiscussionID, ParentCommentID: req.ParentCommentID}
		}
		req.DiscussionID = resolved
	}
	target := req.Owner + "/" + req.Repo + "/pulls/" + strconv.Itoa(req.Number) + "/discussions/" + req.DiscussionID
	key := strings.TrimSpace(opts.IdempotencyKey)
	if key == "" {
		key = GenerateIdempotencyKey("ReplyPRReviewComment", target, req, opts)
		opts.IdempotencyKey = key
	}
	parentFound := false
	thread := make([]PRComment, 0)
	var existing *PRComment
	for _, comment := range comments.Items {
		if comment.DiscussionID == req.DiscussionID {
			thread = append(thread, comment)
		}
		if comment.ID == req.ParentCommentID && comment.DiscussionID == req.DiscussionID {
			parentFound = true
		}
		if comment.DiscussionID == req.DiscussionID && comment.ParentID == req.ParentCommentID && comment.Body == req.Body {
			copy := comment
			existing = &copy
		}
	}
	if existing != nil {
		existing.Thread = thread
		return confirmedExistingPRReviewReply(*existing, target, key), nil
	}
	if !parentFound {
		return WriteResult[PRComment]{}, ErrValidationFailed{Field: "parent_comment_id", Message: "parent comment must belong to the requested discussion"}
	}
	endpoint := replyPRReviewCommentEndpoint(req.Owner, req.Repo, req.Number, req.DiscussionID)
	result, err := writeConfirmedSchemaJSON[PRComment](ctx, c, http.MethodPost, endpoint, "ReplyPRReviewComment", target, struct {
		Body string `json:"body"`
	}{Body: req.Body}, opts, func(result WriteResult[PRComment]) (WriteResult[PRComment], error) {
		if strings.TrimSpace(result.Record.ID) == "" {
			return WriteResult[PRComment]{}, &ErrSchemaDecode{Field: "pr_review_reply.id", Expected: "note_id", Received: "missing"}
		}
		result.Record.Body = firstNonEmpty(result.Record.Body, req.Body)
		result.Record.DiscussionID = req.DiscussionID
		result.Record.ParentID = req.ParentCommentID
		result.Record.PRNumber = req.Number
		result.Record.ReviewKind = "inline"
		result.RemoteID = result.Record.ID
		result.ParentIssueNumber = req.Number
		result.ParentIssueID = req.DiscussionID
		return result, nil
	})
	if err != nil {
		return WriteResult[PRComment]{}, err
	}
	readback, err := c.ListPRComments(ctx, PRRequest{Owner: req.Owner, Repo: req.Repo, Number: req.Number})
	if err != nil {
		return WriteResult[PRComment]{}, ErrWriteConfirmationIncomplete{Endpoint: endpoint, Message: "reply requires discussion readback", RemoteID: result.RemoteID, Cause: err}
	}
	thread = thread[:0]
	var confirmed *PRComment
	for _, comment := range readback.Items {
		if comment.DiscussionID == req.DiscussionID {
			thread = append(thread, comment)
		}
		if comment.ID == result.RemoteID && comment.DiscussionID == req.DiscussionID && comment.ParentID == req.ParentCommentID && comment.Body == req.Body {
			copy := comment
			confirmed = &copy
		}
	}
	if confirmed != nil {
		confirmed.Thread = thread
		result.Record = *confirmed
		result.ProviderStatus += "-readback"
		return result, nil
	}
	return WriteResult[PRComment]{}, ErrWriteConfirmationIncomplete{Endpoint: endpoint, Message: "reply was absent from discussion readback", RemoteID: result.RemoteID}
}

func confirmedExistingPRReviewReply(comment PRComment, target, key string) WriteResult[PRComment] {
	payload, _ := json.Marshal(comment)
	hash := sha256.Sum256(payload)
	return WriteResult[PRComment]{Record: comment, Confirmed: true, Operation: "ReplyPRReviewComment", Target: target, ProviderStatus: "readback-existing", IdempotencyKey: key, ResponseHash: hex.EncodeToString(hash[:]), ProviderPayloadFingerprint: hex.EncodeToString(hash[:]), RemoteID: comment.ID, ParentIssueNumber: comment.PRNumber, ParentIssueID: comment.DiscussionID, ConfirmedAt: time.Now().UTC()}
}

func (c *HTTPClient) CreateWikiPage(ctx context.Context, req CreateWikiPageRequest, opts WriteOptions) (WriteResult[WikiPage], error) {
	if err := validateCreateWikiPage(req); err != nil {
		return WriteResult[WikiPage]{}, err
	}
	wikiPath := wikiWritePath(req.Path, req.Slug)
	payload := WikiContentWriteRequest{Content: base64.StdEncoding.EncodeToString([]byte(req.Body)), Message: wikiWriteMessage(req.Message, "create wiki page")}
	target := req.Owner + "/" + req.Repo + "/" + wikiPath
	return c.writeWikiContent(ctx, http.MethodPost, wikiContentsPathEndpoint(req.Owner, req.Repo, wikiPath), "CreateWikiPage", target, payload, opts, req.Owner, req.Repo, wikiPath, req.Body)
}

func (c *HTTPClient) UpdateWikiPage(ctx context.Context, req UpdateWikiPageRequest, opts WriteOptions) (WriteResult[WikiPage], error) {
	if err := validateUpdateWikiPage(req); err != nil {
		return WriteResult[WikiPage]{}, err
	}
	wikiPath := wikiWritePath(req.Path, req.Slug)
	sha := strings.TrimSpace(req.Sha)
	if sha == "" {
		meta, err := c.getWikiMetadata(ctx, req.Owner, req.Repo, wikiPath)
		if err != nil {
			return WriteResult[WikiPage]{}, err
		}
		sha = meta.Sha
	}
	payload := WikiContentWriteRequest{Content: base64.StdEncoding.EncodeToString([]byte(req.Body)), Message: wikiWriteMessage(req.Message, "update wiki page"), Sha: sha}
	target := req.Owner + "/" + req.Repo + "/" + wikiPath
	return c.writeWikiContent(ctx, http.MethodPut, wikiContentsPathEndpoint(req.Owner, req.Repo, wikiPath), "UpdateWikiPage", target, payload, opts, req.Owner, req.Repo, wikiPath, req.Body)
}

func (c *HTTPClient) DeleteWikiPage(ctx context.Context, req DeleteWikiPageRequest, opts WriteOptions) (WriteResult[WikiPage], error) {
	if err := validateDeleteWikiPage(req); err != nil {
		return WriteResult[WikiPage]{}, err
	}
	wikiPath := wikiWritePath(req.Path, req.Slug)
	sha := strings.TrimSpace(req.Sha)
	if sha == "" {
		meta, err := c.getWikiMetadata(ctx, req.Owner, req.Repo, wikiPath)
		if err != nil {
			return WriteResult[WikiPage]{}, err
		}
		sha = meta.Sha
	}
	payload := WikiContentWriteRequest{Message: wikiWriteMessage(req.Message, "delete wiki page"), Sha: sha}
	target := req.Owner + "/" + req.Repo + "/" + wikiPath
	return c.deleteWikiContent(ctx, deleteWikiPageEndpoint(req.Owner, req.Repo, wikiPath), "DeleteWikiPage", target, payload, opts, req.Owner, req.Repo, wikiPath, sha)
}

func (c *HTTPClient) AddLabel(ctx context.Context, req LabelRequest, opts WriteOptions) (WriteResult[Issue], error) {
	return writeJSON[Issue](ctx, c, http.MethodPost, addLabelEndpoint(req.Owner, req.Repo, req.Number), "AddLabel", req.Owner+"/"+req.Repo+"/"+strconv.Itoa(req.Number), req, opts)
}

func (c *HTTPClient) RemoveLabel(ctx context.Context, req LabelRequest, opts WriteOptions) (WriteResult[Issue], error) {
	return writeJSON[Issue](ctx, c, http.MethodDelete, removeLabelEndpoint(req.Owner, req.Repo, req.Number, req.Label), "RemoveLabel", req.Owner+"/"+req.Repo+"/"+strconv.Itoa(req.Number)+"/"+req.Label, req, opts)
}

func (c *HTTPClient) ListMilestones(ctx context.Context, req MilestoneListRequest) (Page[Milestone], error) {
	if err := validateReadRepo(req.Owner, req.Repo); err != nil {
		return Page[Milestone]{}, err
	}
	endpoint := listMilestonesEndpoint(req.Owner, req.Repo)
	items, page, err := getPaged[Milestone](ctx, c, endpoint, milestoneListQuery(req), PageState{Page: req.Page, PerPage: req.PerPage})
	if err != nil {
		return Page[Milestone]{}, err
	}
	return Page[Milestone]{Items: items, Page: page.Page, PerPage: page.PerPage}, nil
}

func (c *HTTPClient) GetMilestone(ctx context.Context, req MilestoneRequest) (Milestone, error) {
	if err := validateMilestoneRequest(req); err != nil {
		return Milestone{}, err
	}
	endpoint := getMilestoneEndpoint(req.Owner, req.Repo, req.ID)
	var milestone Milestone
	if err := c.getJSON(ctx, endpoint, nil, &milestone); err != nil {
		return Milestone{}, err
	}
	if milestone.RemoteID == "" {
		return Milestone{}, &ErrSchemaDecode{Field: "milestone.id", Expected: "non-empty positive integer", Received: "missing"}
	}
	if strconv.Itoa(req.ID) != milestone.RemoteID {
		return Milestone{}, &ErrSchemaDecode{Field: "milestone.id", Expected: strconv.Itoa(req.ID), Received: milestone.RemoteID, Message: "milestone response id does not match route id"}
	}
	return milestone, nil
}

func (c *HTTPClient) CreateMilestone(ctx context.Context, req MilestoneWriteRequest, opts WriteOptions) (WriteResult[Milestone], error) {
	if err := validateCreateMilestone(req); err != nil {
		return WriteResult[Milestone]{}, err
	}
	target := req.Owner + "/" + req.Repo + "/" + strings.TrimSpace(req.Title)
	return writeConfirmedJSON[Milestone](ctx, c, http.MethodPost, listMilestonesEndpoint(req.Owner, req.Repo), "CreateMilestone", target, milestoneWritePayload(req, true), opts, func(result WriteResult[Milestone]) (WriteResult[Milestone], error) {
		result.RemoteID = result.Record.RemoteID
		result.RemoteRevision = firstNonEmpty(result.Record.UpdatedAt, result.ResponseHash)
		result.BrowserURL = result.Record.HTMLURL
		return result, nil
	})
}

func (c *HTTPClient) UpdateMilestone(ctx context.Context, req MilestoneWriteRequest, opts WriteOptions) (WriteResult[Milestone], error) {
	if err := validateUpdateMilestone(req); err != nil {
		return WriteResult[Milestone]{}, err
	}
	endpoint := getMilestoneEndpoint(req.Owner, req.Repo, req.ID)
	target := req.Owner + "/" + req.Repo + "/milestones/" + strconv.Itoa(req.ID)
	return writeConfirmedJSON[Milestone](ctx, c, http.MethodPatch, endpoint, "UpdateMilestone", target, milestoneWritePayload(req, false), opts, func(result WriteResult[Milestone]) (WriteResult[Milestone], error) {
		readback, err := c.GetMilestone(ctx, MilestoneRequest{Owner: req.Owner, Repo: req.Repo, ID: req.ID})
		if err != nil {
			return WriteResult[Milestone]{}, err
		}
		result.Record = readback
		result.RemoteID = readback.RemoteID
		result.RemoteRevision = firstNonEmpty(readback.UpdatedAt, result.ResponseHash)
		result.BrowserURL = readback.HTMLURL
		return result, nil
	})
}

type wikiTraversal struct {
	client       *HTTPClient
	owner        string
	repo         string
	seenDirs     map[string]bool
	seenFiles    map[string]bool
	skipRecords  int
	maxRecords   int
	hasMore      bool
	progressChan chan<- WikiProgressEvent
}

type walkStackEntry struct {
	kind      string // "dir" or "file"
	dir       string
	depth     int
	entryPath string
	entrySha  string
}

func (w *wikiTraversal) walk(ctx context.Context, dir string, depth int) ([]WikiPage, error) {
	stack := []walkStackEntry{{kind: "dir", dir: dir, depth: depth}}
	var out []WikiPage
	pageCount := 0

	for len(stack) > 0 {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		if current.kind == "file" {
			if strings.TrimSpace(current.entrySha) == "" {
				return nil, ErrPartialResponse{Endpoint: wikiContentsPathEndpoint(w.owner, w.repo, current.dir), Message: "wiki file entry requires sha"}
			}
			if w.seenFiles[current.entryPath] {
				return nil, ErrPartialResponse{Endpoint: wikiContentsPathEndpoint(w.owner, w.repo, current.dir), Message: "duplicate wiki file path " + current.entryPath}
			}
			w.seenFiles[current.entryPath] = true
			if !isImportableWikiMarkdown(current.entryPath) {
				continue
			}
			if w.skipRecords > 0 {
				w.skipRecords--
				continue
			}
			if w.maxRecords > 0 && len(out) >= w.maxRecords {
				w.hasMore = true
				break
			}
			page, err := w.client.getWikiPageByPath(ctx, w.owner, w.repo, current.entryPath)
			if err != nil {
				return out, err
			}
			out = append(out, page)
			continue
		}

		if current.depth > 64 {
			return nil, ErrPartialResponse{Endpoint: wikiContentsPathEndpoint(w.owner, w.repo, current.dir), Message: "wiki contents nesting exceeds 64 levels"}
		}
		normalizedDir := normalizeWikiPath(current.dir)
		if w.seenDirs[normalizedDir] {
			continue
		}
		w.seenDirs[normalizedDir] = true
		entries, err := w.client.listWikiEntries(ctx, w.owner, w.repo, normalizedDir)
		if err != nil {
			return out, err
		}
		sort.Slice(entries, func(i, j int) bool {
			left := normalizeWikiPath(entries[i].Path)
			right := normalizeWikiPath(entries[j].Path)
			if entries[i].Type != entries[j].Type {
				return entries[i].Type == "dir"
			}
			return left < right
		})

		startLen := len(out)

		for i := len(entries) - 1; i >= 0; i-- {
			entry := entries[i]
			entryPath := normalizeWikiPath(entry.Path)
			if entryPath == "" || strings.TrimSpace(entry.Type) == "" {
				return nil, ErrPartialResponse{Endpoint: wikiContentsPathEndpoint(w.owner, w.repo, normalizedDir), Message: "wiki contents entry requires path and type"}
			}
			switch strings.ToLower(strings.TrimSpace(entry.Type)) {
			case "dir", "directory", "tree":
				stack = append(stack, walkStackEntry{kind: "dir", dir: entryPath, depth: current.depth + 1})
			case "file", "blob":
				stack = append(stack, walkStackEntry{kind: "file", dir: current.dir, depth: current.depth, entryPath: entryPath, entrySha: strings.TrimSpace(entry.Sha)})
			}
		}

		recordsThisDir := len(out) - startLen
		pageCount++
		w.emitProgress(normalizedDir, pageCount, recordsThisDir)
	}
	return out, nil
}

func (w *wikiTraversal) emitProgress(path string, page int, records int) {
	if w.progressChan == nil {
		return
	}
	select {
	case w.progressChan <- WikiProgressEvent{Path: path, RecordsFetched: records}:
	default:
	}
}

func (c *HTTPClient) listWikiEntries(ctx context.Context, owner, repo, dir string) ([]WikiContentsEntry, error) {
	endpoint := wikiContentsRootEndpoint(owner, repo)
	if dir != "" {
		endpoint = wikiContentsPathEndpoint(owner, repo, dir)
	}

	resp, err := c.do(ctx, http.MethodGet, endpoint, nil, nil, requestOptions{})
	if err != nil {
		return nil, ErrNetworkUnavailable{Endpoint: endpoint, Attempts: 1, Cause: err}
	}
	defer resp.Body.Close()

	body, readErr := c.readBounded(resp, endpoint)
	if readErr != nil {
		return nil, readErr
	}

	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		// Empty array [] is not an empty-wiki condition — the route exists and
		// returns valid content (just no pages in this directory).
	} else if isWikiEmptyResponse(resp.StatusCode, body) {
		return nil, ErrEmptyWiki{Owner: owner, Repo: repo}
	} else {
		// Fall through to normal error handling via the existing statusError path.
		return nil, c.statusError(resp.StatusCode, endpoint, body, requestOptions{})
	}

	var entries []WikiContentsEntry
	if err := decodeJSON(endpoint, body, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func isWikiEmptyResponse(statusCode int, body []byte) bool {
	if statusCode != http.StatusBadRequest && statusCode != http.StatusNotFound {
		return false
	}
	lower := strings.ToLower(string(body))
	patterns := []string{
		"wiki not found",
		"wiki is empty",
		"wiki has no pages",
		"wiki is not initialized",
		"wiki is uninitialized",
		"wiki has not been created",
		"uninitialized wiki",
	}
	for _, p := range patterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

func (c *HTTPClient) getWikiPageByPath(ctx context.Context, owner, repo, wikiPath string) (WikiPage, error) {
	meta, err := c.getWikiMetadata(ctx, owner, repo, wikiPath)
	if err != nil {
		return WikiPage{}, err
	}
	body, _, err := c.getBytes(ctx, wikiRawPathEndpoint(owner, repo, wikiPath), nil)
	if err != nil {
		if strings.TrimSpace(meta.Content) == "" {
			return WikiPage{}, err
		}
		decoded, decodeErr := decodeWikiContent(meta, wikiContentsPathEndpoint(owner, repo, wikiPath))
		if decodeErr != nil {
			return WikiPage{}, decodeErr
		}
		body = decoded
	}
	return wikiPageFromMetadata(meta, string(body)), nil
}

func (c *HTTPClient) getWikiMetadata(ctx context.Context, owner, repo, wikiPath string) (WikiContentsFile, error) {
	endpoint := wikiContentsPathEndpoint(owner, repo, wikiPath)
	var meta WikiContentsFile
	if err := c.getJSON(ctx, endpoint, nil, &meta); err != nil {
		return WikiContentsFile{}, err
	}
	if normalizeWikiPath(meta.Path) == "" || strings.TrimSpace(meta.Sha) == "" {
		return WikiContentsFile{}, ErrPartialResponse{Endpoint: endpoint, Message: "wiki file metadata requires path and sha"}
	}
	return meta, nil
}

func (c *HTTPClient) writeWikiContent(ctx context.Context, method, endpoint, operation, target string, payload WikiContentWriteRequest, opts WriteOptions, owner, repo, wikiPath, body string) (WriteResult[WikiPage], error) {
	requestPath := normalizeWikiPath(wikiPath)
	result, err := writeConfirmedJSON[WikiContentsFile](ctx, c, method, endpoint, operation, target, payload, opts, func(result WriteResult[WikiContentsFile]) (WriteResult[WikiContentsFile], error) {
		if normalizeWikiPath(result.Record.Path) == "" || strings.TrimSpace(result.Record.Sha) == "" {
			meta, err := c.confirmWikiWrite(ctx, owner, repo, requestPath, body)
			if err != nil {
				return WriteResult[WikiContentsFile]{}, err
			}
			result.Record = meta
		}
		confirmedPath := normalizeWikiPath(result.Record.Path)
		if confirmedPath == "" || strings.TrimSpace(result.Record.Sha) == "" {
			return WriteResult[WikiContentsFile]{}, ErrWriteConfirmationIncomplete{Endpoint: endpoint, Message: "wiki write confirmation requires path and sha"}
		}
		if confirmedPath != requestPath {
			return WriteResult[WikiContentsFile]{}, ErrWriteConfirmationIncomplete{Endpoint: endpoint, Message: "wiki write confirmation path mismatch"}
		}
		result.RemoteID = confirmedPath
		result.RemoteSlug = result.RemoteID
		result.RemoteRevision = result.Record.Sha
		c.setWikiWriteLocations(&result, endpoint, owner, repo, confirmedPath)
		return result, nil
	})
	if err != nil {
		return WriteResult[WikiPage]{}, err
	}
	page := wikiPageFromMetadata(result.Record, body)
	return WriteResult[WikiPage]{Record: page, Confirmed: result.Confirmed, Operation: result.Operation, Target: result.Target, ProviderStatus: result.ProviderStatus, RemoteID: result.RemoteID, RemoteSlug: result.RemoteSlug, RemoteRevision: result.RemoteRevision, APIPath: result.APIPath, CachePath: result.CachePath, BrowserURL: result.BrowserURL, IdempotencyKey: result.IdempotencyKey, ResponseHash: result.ResponseHash, ConfirmedAt: result.ConfirmedAt, ProviderPayloadFingerprint: result.ProviderPayloadFingerprint}, nil
}

func (c *HTTPClient) deleteWikiContent(ctx context.Context, endpoint, operation, target string, payload WikiContentWriteRequest, opts WriteOptions, owner, repo, wikiPath, sha string) (WriteResult[WikiPage], error) {
	requestPath := normalizeWikiPath(wikiPath)
	result, err := writeConfirmedJSON[WikiContentsFile](ctx, c, http.MethodDelete, endpoint, operation, target, payload, opts, func(result WriteResult[WikiContentsFile]) (WriteResult[WikiContentsFile], error) {
		if confirmedPath := normalizeWikiPath(result.Record.Path); confirmedPath != "" && confirmedPath != requestPath {
			return WriteResult[WikiContentsFile]{}, ErrWriteConfirmationIncomplete{Endpoint: endpoint, Message: "wiki delete confirmation path mismatch"}
		}
		if err := c.confirmWikiDelete(ctx, owner, repo, requestPath); err != nil {
			return WriteResult[WikiContentsFile]{}, err
		}
		if normalizeWikiPath(result.Record.Path) == "" {
			result.Record.Path = requestPath
		}
		if strings.TrimSpace(result.Record.Sha) == "" {
			result.Record.Sha = strings.TrimSpace(sha)
		}
		result.RemoteID = requestPath
		result.RemoteSlug = requestPath
		result.RemoteRevision = firstNonEmpty(result.Record.Sha, result.ResponseHash)
		c.setWikiWriteLocations(&result, endpoint, owner, repo, requestPath)
		return result, nil
	})
	if err != nil {
		return WriteResult[WikiPage]{}, err
	}
	page := wikiPageFromMetadata(result.Record, "")
	return WriteResult[WikiPage]{Record: page, Confirmed: result.Confirmed, Operation: result.Operation, Target: result.Target, ProviderStatus: result.ProviderStatus, RemoteID: result.RemoteID, RemoteSlug: result.RemoteSlug, RemoteRevision: result.RemoteRevision, APIPath: result.APIPath, CachePath: result.CachePath, BrowserURL: result.BrowserURL, IdempotencyKey: result.IdempotencyKey, ResponseHash: result.ResponseHash, ConfirmedAt: result.ConfirmedAt, ProviderPayloadFingerprint: result.ProviderPayloadFingerprint}, nil
}

func (c *HTTPClient) confirmWikiWrite(ctx context.Context, owner, repo, wikiPath, body string) (WikiContentsFile, error) {
	endpoint := wikiContentsPathEndpoint(owner, repo, wikiPath)
	meta, err := c.getWikiMetadata(ctx, owner, repo, wikiPath)
	if err != nil {
		return WikiContentsFile{}, ErrWriteConfirmationIncomplete{Endpoint: endpoint, Message: "wiki confirmation GET failed", Cause: err}
	}
	confirmedPath := normalizeWikiPath(meta.Path)
	if confirmedPath != normalizeWikiPath(wikiPath) {
		return WikiContentsFile{}, ErrWriteConfirmationIncomplete{Endpoint: endpoint, Message: "wiki confirmation path mismatch"}
	}
	if strings.TrimSpace(meta.Sha) == "" {
		return WikiContentsFile{}, ErrWriteConfirmationIncomplete{Endpoint: endpoint, Message: "wiki confirmation missing sha"}
	}
	if strings.TrimSpace(meta.Content) != "" {
		decoded, err := decodeWikiContent(meta, endpoint)
		if err != nil {
			return WikiContentsFile{}, ErrWriteConfirmationIncomplete{Endpoint: endpoint, Message: "wiki confirmation content decode failed", Cause: err}
		}
		if string(decoded) != body {
			return WikiContentsFile{}, ErrWriteConfirmationIncomplete{Endpoint: endpoint, Message: "wiki confirmation content mismatch"}
		}
	}
	return meta, nil
}

func (c *HTTPClient) confirmWikiDelete(ctx context.Context, owner, repo, wikiPath string) error {
	endpoint := wikiContentsPathEndpoint(owner, repo, wikiPath)
	meta, err := c.getWikiMetadata(ctx, owner, repo, wikiPath)
	if err != nil {
		if isNotFoundError(err) {
			return nil
		}
		return ErrWriteConfirmationIncomplete{Endpoint: endpoint, Message: "wiki delete confirmation GET failed", Cause: err}
	}
	if normalizeWikiPath(meta.Path) == normalizeWikiPath(wikiPath) {
		return ErrWriteConfirmationIncomplete{Endpoint: endpoint, Message: "wiki delete confirmation expected not found"}
	}
	return ErrWriteConfirmationIncomplete{Endpoint: endpoint, Message: "wiki delete confirmation path mismatch"}
}

func isNotFoundError(err error) bool {
	var notFound ErrNotFound
	if errors.As(err, &notFound) {
		return true
	}
	var remoteNotFound ErrRemoteNotFound
	return errors.As(err, &remoteNotFound)
}

type requestOptions struct {
	knownRemoteAlias bool
	remoteAlias      string
	idempotencyKey   string
	localPayload     []byte
	noRetry          bool
}

func (c *HTTPClient) getJSON(ctx context.Context, endpoint string, values url.Values, out any) error {
	return c.getJSONWithOptions(ctx, endpoint, values, out, requestOptions{})
}

func (c *HTTPClient) getJSONWithOptions(ctx context.Context, endpoint string, values url.Values, out any, opts requestOptions) error {
	body, headers, err := c.getBytesWithOptions(ctx, endpoint, values, opts)
	if err != nil {
		return err
	}
	return decodeJSONResponse(endpoint, body, headers, out)
}

func decodeJSON(endpoint string, body []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(out); err != nil {
		return classifyJSONDecodeError(endpoint, body, err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return ErrMalformedJSON{Endpoint: endpoint, ResponseSize: int64(len(body)), Offset: dec.InputOffset()}
		}
		return classifyJSONDecodeError(endpoint, body, err)
	}
	return nil
}

func decodeJSONResponse(endpoint string, body []byte, headers http.Header, out any) error {
	contentType := strings.TrimSpace(headers.Get("Content-Type"))
	err := decodeJSON(endpoint, body, out)
	if err != nil && contentType != "" && !isJSONContentType(contentType) && !looksLikeJSON(body) {
		return ErrUnexpectedContentType{Endpoint: endpoint, ContentType: sanitizeContentType(contentType), ResponseSize: int64(len(body))}
	}
	var partial ErrPartialResponse
	if errors.As(err, &partial) && partial.ContentType == "" {
		partial.ContentType = sanitizeContentType(contentType)
		return partial
	}
	var malformed ErrMalformedJSON
	if errors.As(err, &malformed) && malformed.ContentType == "" {
		malformed.ContentType = sanitizeContentType(contentType)
		return malformed
	}
	return err
}

func classifyJSONDecodeError(endpoint string, body []byte, err error) error {
	var schema *ErrSchemaDecode
	if errors.As(err, &schema) {
		if schema.Endpoint == "" {
			schema.Endpoint = endpoint
		}
		return schema
	}
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		field := strings.TrimSpace(typeErr.Field)
		if field == "" {
			field = endpoint
		}
		return &ErrSchemaDecode{Endpoint: endpoint, Field: field, Expected: typeErr.Type.String(), Received: "incompatible JSON value"}
	}
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		if errors.Is(err, io.ErrUnexpectedEOF) || strings.Contains(strings.ToLower(syntaxErr.Error()), "unexpected end") {
			offset := syntaxErr.Offset
			if offset == 0 {
				offset = int64(len(body))
			}
			return ErrPartialResponse{Endpoint: endpoint, Got: int64(len(body)), Cause: err, Message: "truncated JSON", Offset: offset}
		}
		return ErrMalformedJSON{Endpoint: endpoint, ResponseSize: int64(len(body)), Offset: syntaxErr.Offset}
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return ErrPartialResponse{Endpoint: endpoint, Got: int64(len(body)), Cause: err, Message: "truncated JSON", Offset: int64(len(body))}
	}
	return &ErrSchemaDecode{Endpoint: endpoint, Field: endpoint, Expected: "response matching adapter schema", Received: "incompatible JSON value"}
}

func retryableResponseDecode(err error) bool {
	var partial ErrPartialResponse
	if errors.As(err, &partial) {
		return true
	}
	var malformed ErrMalformedJSON
	if errors.As(err, &malformed) {
		return true
	}
	var contentType ErrUnexpectedContentType
	return errors.As(err, &contentType)
}

func withResponseAttempt(err error, attempt int) error {
	var partial ErrPartialResponse
	if errors.As(err, &partial) {
		partial.Attempts = attempt
		return partial
	}
	var malformed ErrMalformedJSON
	if errors.As(err, &malformed) {
		malformed.Attempts = attempt
		return malformed
	}
	var contentType ErrUnexpectedContentType
	if errors.As(err, &contentType) {
		contentType.Attempts = attempt
		return contentType
	}
	return err
}

func withResponseContentType(err error, contentType string) error {
	normalized := sanitizeContentType(contentType)
	var partial ErrPartialResponse
	if errors.As(err, &partial) {
		partial.ContentType = normalized
		return partial
	}
	var malformed ErrMalformedJSON
	if errors.As(err, &malformed) {
		malformed.ContentType = normalized
		return malformed
	}
	return err
}

func isJSONContentType(value string) bool {
	mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(value, ";", 2)[0]))
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

func looksLikeJSON(body []byte) bool {
	trimmed := bytes.TrimSpace(body)
	return len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')
}

func sanitizeContentType(value string) string {
	mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(value, ";", 2)[0]))
	if len(mediaType) > 128 {
		return mediaType[:128]
	}
	return mediaType
}

func decodeSchemaJSON(endpoint string, body []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(out); err != nil {
		var schema *ErrSchemaDecode
		if errors.As(err, &schema) {
			if schema.Endpoint == "" {
				schema.Endpoint = endpoint
			}
			return schema
		}
		return &ErrSchemaDecode{Endpoint: endpoint, Field: endpoint, Expected: "valid JSON response", Received: decodeMessage(err)}
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

func createIssuePayload(req CreateIssueRequest) any {
	payload := struct {
		Title     string           `json:"title"`
		Body      string           `json:"body,omitempty"`
		Labels    *json.RawMessage `json:"labels,omitempty"`
		Milestone *json.RawMessage `json:"milestone,omitempty"`
	}{Title: req.Title, Body: req.Body}
	if len(req.Labels) > 0 {
		labels := req.Labels
		payload.Labels = &labels
	}
	if len(req.Milestone) > 0 {
		milestone := req.Milestone
		payload.Milestone = &milestone
	}
	return payload
}

func updateIssuePayload(req UpdateIssueRequest) any {
	payload := struct {
		Title     string           `json:"title,omitempty"`
		Body      string           `json:"body,omitempty"`
		State     string           `json:"state,omitempty"`
		Labels    *json.RawMessage `json:"labels,omitempty"`
		Milestone *json.RawMessage `json:"milestone,omitempty"`
	}{Title: req.Title, Body: req.Body, State: req.State}
	if len(req.Labels) > 0 {
		labels := req.Labels
		payload.Labels = &labels
	}
	if len(req.Milestone) > 0 {
		milestone := req.Milestone
		payload.Milestone = &milestone
	}
	return payload
}

func milestoneWritePayload(req MilestoneWriteRequest, includeRequired bool) any {
	payload := struct {
		Title       string `json:"title,omitempty"`
		Description string `json:"description,omitempty"`
		DueOn       string `json:"due_on,omitempty"`
		State       string `json:"state,omitempty"`
	}{}
	if includeRequired || strings.TrimSpace(req.Title) != "" {
		payload.Title = strings.TrimSpace(req.Title)
	}
	if strings.TrimSpace(req.Description) != "" {
		payload.Description = req.Description
	}
	if strings.TrimSpace(req.DueOn) != "" {
		payload.DueOn = normalizeMilestoneDueOn(req.DueOn)
	}
	if strings.TrimSpace(req.State) != "" {
		payload.State = strings.TrimSpace(req.State)
	}
	return payload
}

func writeConfirmedJSON[T any](ctx context.Context, c *HTTPClient, method, endpoint, operation, target string, payload any, opts WriteOptions, confirm func(WriteResult[T]) (WriteResult[T], error)) (WriteResult[T], error) {
	return writeConfirmedWithDecoder(ctx, c, method, endpoint, operation, target, payload, opts, decodeJSON, confirm)
}

func writeConfirmedSchemaJSON[T any](ctx context.Context, c *HTTPClient, method, endpoint, operation, target string, payload any, opts WriteOptions, confirm func(WriteResult[T]) (WriteResult[T], error)) (WriteResult[T], error) {
	return writeConfirmedWithDecoder(ctx, c, method, endpoint, operation, target, payload, opts, decodeSchemaJSON, confirm)
}

func writeConfirmedWithDecoder[T any](ctx context.Context, c *HTTPClient, method, endpoint, operation, target string, payload any, opts WriteOptions, decode func(string, []byte, any) error, confirm func(WriteResult[T]) (WriteResult[T], error)) (WriteResult[T], error) {
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
	if err := decode(endpoint, respBody, &record); err != nil {
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

func linkedIssuesContain(issues []Issue, number int) bool {
	for _, issue := range issues {
		if issue.Number == number {
			return true
		}
	}
	return false
}

func linkPRIssueUnsupportedError(err error) error {
	var notFound ErrNotFound
	if errors.As(err, &notFound) {
		return ErrUnsupportedCapability{CapabilityKey: "pr_issue_relation", Message: "pull request issue relation endpoint is not available"}
	}
	var validation ErrAPIValidation
	if errors.As(err, &validation) && validation.Status == http.StatusMethodNotAllowed {
		return ErrUnsupportedCapability{CapabilityKey: "pr_issue_relation", Message: "pull request issue relation endpoint does not allow linking"}
	}
	return err
}

func validateReadRepo(owner, repo string) error {
	if strings.TrimSpace(owner) == "" {
		return ErrValidationFailed{Field: "owner", Message: "owner is required"}
	}
	if strings.TrimSpace(repo) == "" {
		return ErrValidationFailed{Field: "repo", Message: "repo is required"}
	}
	return nil
}

func validateMilestoneRequest(req MilestoneRequest) error {
	if err := validateReadRepo(req.Owner, req.Repo); err != nil {
		return err
	}
	if req.ID <= 0 {
		return ErrValidationFailed{Field: "milestone.id", Message: "positive milestone id is required"}
	}
	return nil
}

func validateCreateMilestone(req MilestoneWriteRequest) error {
	if err := validateWriteRepo(req.Owner, req.Repo); err != nil {
		return err
	}
	if strings.TrimSpace(req.Title) == "" {
		return ErrValidationFailed{Field: "milestone.title", Message: "title is required"}
	}
	if strings.TrimSpace(req.DueOn) == "" {
		return ErrValidationFailed{Field: "milestone.due_on", Message: "due_on is required by GitCode"}
	}
	if _, err := parseMilestoneDueOn(req.DueOn); err != nil {
		return err
	}
	return validateMilestoneWriteState(req.State)
}

func validateUpdateMilestone(req MilestoneWriteRequest) error {
	if err := validateWriteRepo(req.Owner, req.Repo); err != nil {
		return err
	}
	if req.ID <= 0 {
		return ErrValidationFailed{Field: "milestone.id", Message: "positive milestone id is required"}
	}
	if strings.TrimSpace(req.DueOn) != "" {
		if _, err := parseMilestoneDueOn(req.DueOn); err != nil {
			return err
		}
	}
	return validateMilestoneWriteState(req.State)
}

func validateMilestoneWriteState(state string) error {
	switch strings.TrimSpace(state) {
	case "", "open", "closed":
		return nil
	default:
		return ErrValidationFailed{Field: "milestone.state", Message: "state must be open or closed"}
	}
}

func normalizeMilestoneDueOn(raw string) string {
	dueOn, err := parseMilestoneDueOn(raw)
	if err != nil {
		return strings.TrimSpace(raw)
	}
	return dueOn
}

func parseMilestoneDueOn(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", ErrValidationFailed{Field: "milestone.due_on", Message: "due_on is required"}
	}
	if t, err := time.Parse("2006-01-02", trimmed); err == nil {
		return t.Format("2006-01-02"), nil
	}
	if t, err := time.Parse(time.RFC3339, trimmed); err == nil {
		return t.Format("2006-01-02"), nil
	}
	return "", ErrValidationFailed{Field: "milestone.due_on", Message: "due_on must be YYYY-MM-DD or RFC3339"}
}

func validateIssueRequest(req IssueRequest) error {
	if err := validateReadRepo(req.Owner, req.Repo); err != nil {
		return err
	}
	if req.Number <= 0 {
		return ErrValidationFailed{Field: "number", Message: "positive issue number is required"}
	}
	return nil
}

func validatePRRequest(req PRRequest) error {
	if err := validateReadRepo(req.Owner, req.Repo); err != nil {
		return err
	}
	if req.Number <= 0 {
		return ErrValidationFailed{Field: "number", Message: "positive pull request number is required"}
	}
	return nil
}

func validateWikiPageRequest(req WikiPageRequest) error {
	if err := validateReadRepo(req.Owner, req.Repo); err != nil {
		return err
	}
	if wikiRequestPath(req.Path, req.Slug) == "" {
		return ErrValidationFailed{Field: "path", Message: "wiki path is required"}
	}
	return nil
}

func validateWriteRepo(owner, repo string) error {
	return validateReadRepo(owner, repo)
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

func normalizeIssueUpdateState(state string) (wireState string, expectedState string, err error) {
	switch strings.TrimSpace(state) {
	case "":
		return "", "", nil
	case "open":
		return "reopen", "open", nil
	case "closed":
		return "close", "closed", nil
	default:
		return "", "", ErrValidationFailed{Field: "state", Message: "state must be open or closed"}
	}
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

func validateUpdateIssueComment(req UpdateIssueCommentRequest) error {
	if err := validateWriteRepo(req.Owner, req.Repo); err != nil {
		return err
	}
	if strings.TrimSpace(req.CommentID) == "" {
		return ErrValidationFailed{Field: "comment_id", Message: "comment id is required"}
	}
	if strings.TrimSpace(req.Body) == "" {
		return ErrValidationFailed{Field: "body", Message: "comment body is required"}
	}
	return nil
}

func validateCreatePRComment(req CreatePRCommentRequest) error {
	if err := validateWriteRepo(req.Owner, req.Repo); err != nil {
		return err
	}
	if req.Number <= 0 {
		return ErrValidationFailed{Field: "number", Message: "positive pull request number is required"}
	}
	if strings.TrimSpace(req.Body) == "" {
		return ErrValidationFailed{Field: "body", Message: "comment body is required"}
	}
	return nil
}

func validateCreatePRReviewComment(req CreatePRReviewCommentRequest) error {
	if err := validateWriteRepo(req.Owner, req.Repo); err != nil {
		return err
	}
	if req.Number <= 0 {
		return ErrValidationFailed{Field: "number", Message: "positive pull request number is required"}
	}
	if strings.TrimSpace(req.Body) == "" {
		return ErrValidationFailed{Field: "body", Message: "comment body is required"}
	}
	if strings.TrimSpace(req.Path) == "" {
		return ErrValidationFailed{Field: "path", Message: "file path is required"}
	}
	if req.Line <= 0 && req.Position <= 0 {
		return ErrValidationFailed{Field: "line", Message: "line or position is required"}
	}
	if req.StartLine < 0 || req.EndLine < 0 || req.Line < 0 || req.Position < 0 {
		return ErrValidationFailed{Field: "line", Message: "line and position values must be positive"}
	}
	if req.StartLine > 0 && req.EndLine > 0 && req.StartLine > req.EndLine {
		return ErrValidationFailed{Field: "start_line", Message: "start_line must be less than or equal to end_line"}
	}
	line := firstPositive(req.Line, req.Position)
	if req.Line > 0 && req.Position > 0 && req.Line != req.Position {
		return ErrValidationFailed{Field: "position", Message: "position is a deprecated file-line alias and must equal line when both are supplied"}
	}
	if req.EndLine > 0 && req.EndLine != line {
		return ErrValidationFailed{Field: "end_line", Message: "end_line must equal the anchor line"}
	}
	if req.StartLine > line {
		return ErrValidationFailed{Field: "start_line", Message: "start_line must be less than or equal to the anchor line"}
	}
	return nil
}

func validateReplyPRReviewComment(req ReplyPRReviewCommentRequest) error {
	if err := validateWriteRepo(req.Owner, req.Repo); err != nil {
		return err
	}
	if req.Number <= 0 {
		return ErrValidationFailed{Field: "number", Message: "positive pull request number is required"}
	}
	if strings.TrimSpace(req.DiscussionID) == "" {
		return ErrValidationFailed{Field: "discussion_id", Message: "discussion id is required"}
	}
	if strings.TrimSpace(req.ParentCommentID) == "" {
		return ErrValidationFailed{Field: "parent_comment_id", Message: "parent comment id is required"}
	}
	if strings.TrimSpace(req.Body) == "" {
		return ErrValidationFailed{Field: "body", Message: "comment body is required"}
	}
	return nil
}

func validateCreatePR(req CreatePRRequest) error {
	if err := validateWriteRepo(req.Owner, req.Repo); err != nil {
		return err
	}
	if strings.TrimSpace(req.Title) == "" {
		return ErrValidationFailed{Field: "title", Message: "title is required"}
	}
	if strings.TrimSpace(req.Head) == "" {
		return ErrValidationFailed{Field: "head", Message: "head branch is required"}
	}
	if strings.TrimSpace(req.Base) == "" {
		return ErrValidationFailed{Field: "base", Message: "base branch is required"}
	}
	return nil
}

func validateUpdatePR(req UpdatePRRequest) error {
	if err := validateWriteRepo(req.Owner, req.Repo); err != nil {
		return err
	}
	if req.Number <= 0 {
		return ErrValidationFailed{Field: "number", Message: "positive pull request number is required"}
	}
	return nil
}

func validateMergePR(req MergePRRequest) error {
	if err := validateWriteRepo(req.Owner, req.Repo); err != nil {
		return err
	}
	if req.Number <= 0 {
		return ErrValidationFailed{Field: "number", Message: "positive pull request number is required"}
	}
	switch strings.ToLower(strings.TrimSpace(req.MergeMethod)) {
	case "", "merge", "squash", "rebase":
		return nil
	default:
		return ErrValidationFailed{Field: "merge_method", Message: "merge method must be merge, squash, or rebase"}
	}
}

func validateLinkPRIssue(req LinkPRIssueRequest) error {
	if err := validateWriteRepo(req.Owner, req.Repo); err != nil {
		return err
	}
	if req.Number <= 0 {
		return ErrValidationFailed{Field: "number", Message: "positive pull request number is required"}
	}
	if req.IssueNumber <= 0 {
		return ErrValidationFailed{Field: "issue_number", Message: "positive issue number is required"}
	}
	return nil
}

func validateCreateWikiPage(req CreateWikiPageRequest) error {
	if err := validateWriteRepo(req.Owner, req.Repo); err != nil {
		return err
	}
	if wikiRequestPath(req.Path, req.Slug) == "" {
		return ErrValidationFailed{Field: "path", Message: "wiki path is required"}
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
	if wikiRequestPath(req.Path, req.Slug) == "" {
		return ErrValidationFailed{Field: "path", Message: "wiki path is required"}
	}
	return nil
}

func validateDeleteWikiPage(req DeleteWikiPageRequest) error {
	if err := validateWriteRepo(req.Owner, req.Repo); err != nil {
		return err
	}
	if wikiRequestPath(req.Path, req.Slug) == "" {
		return ErrValidationFailed{Field: "path", Message: "wiki path is required"}
	}
	return nil
}

func wikiRequestPath(pathValue, slug string) string {
	if normalized := normalizeWikiPath(pathValue); normalized != "" {
		return normalized
	}
	return normalizeWikiPath(slug)
}

func wikiWritePath(pathValue, slug string) string {
	return ensureWikiMarkdownPath(wikiRequestPath(pathValue, slug))
}

func ensureWikiMarkdownPath(wikiPath string) string {
	if wikiPath == "" || strings.EqualFold(path.Ext(wikiPath), ".md") {
		return wikiPath
	}
	return strings.TrimSuffix(wikiPath, "/") + ".md"
}

func normalizeWikiPath(value string) string {
	trimmed := strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" {
		return ""
	}
	parts := strings.Split(trimmed, "/")
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || part == "." || part == ".." {
			continue
		}
		cleaned = append(cleaned, part)
	}
	return strings.Join(cleaned, "/")
}

func (c *HTTPClient) setWikiWriteLocations(result *WriteResult[WikiContentsFile], endpoint, owner, repo, wikiPath string) {
	result.APIPath = endpoint
	result.CachePath = wikiCachePath(wikiPath)
	result.BrowserURL = c.wikiBrowserURL(owner, repo, wikiPath)
}

func wikiCachePath(wikiPath string) string {
	wikiPath = normalizeWikiPath(wikiPath)
	if wikiPath == "" {
		return "wiki/Home.md"
	}
	return "wiki/" + ensureWikiMarkdownPath(wikiPath)
}

func (c *HTTPClient) wikiBrowserURL(owner, repo, wikiPath string) string {
	base := c.browserBaseURL()
	return base + "/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/wiki/" + url.PathEscape(ensureWikiMarkdownPath(normalizeWikiPath(wikiPath)))
}

func (c *HTTPClient) browserBaseURL() string {
	u := *c.baseURL
	if u.Host == "api.gitcode.com" || u.Host == "www.api.gitcode.com" {
		u.Host = "gitcode.com"
	}
	u.RawQuery = ""
	u.Fragment = ""
	u.Path = strings.TrimRight(u.Path, "/")
	for _, suffix := range []string{"/api/v5", "/api/v4", "/api"} {
		u.Path = strings.TrimSuffix(u.Path, suffix)
	}
	return strings.TrimRight(u.String(), "/")
}

func isImportableWikiMarkdown(wikiPath string) bool {
	switch strings.ToLower(path.Ext(wikiPath)) {
	case ".md", ".markdown", ".mdown", ".mkd":
		return true
	default:
		return false
	}
}

func wikiPageFromMetadata(meta WikiContentsFile, body string) WikiPage {
	wikiPath := normalizeWikiPath(meta.Path)
	return WikiPage{ID: wikiPath, Slug: wikiPath, Title: wikiTitleFromPath(wikiPath), Body: body, Revision: strings.TrimSpace(meta.Sha), UpdatedAt: time.Now().UTC()}
}

func wikiTitleFromPath(wikiPath string) string {
	base := path.Base(wikiPath)
	ext := path.Ext(base)
	return strings.TrimSuffix(base, ext)
}

func decodeWikiContent(meta WikiContentsFile, endpoint string) ([]byte, error) {
	if strings.ToLower(strings.TrimSpace(meta.Encoding)) != "base64" {
		return nil, ErrPartialResponse{Endpoint: endpoint, Message: "wiki content encoding must be base64"}
	}
	content := strings.ReplaceAll(strings.TrimSpace(meta.Content), "\n", "")
	if content == "" {
		return nil, ErrPartialResponse{Endpoint: endpoint, Message: "wiki content is required"}
	}
	decoded, err := base64.StdEncoding.DecodeString(content)
	if err != nil {
		return nil, ErrPartialResponse{Endpoint: endpoint, Cause: err, Message: "invalid base64 wiki content"}
	}
	return decoded, nil
}

func wikiWriteMessage(message, fallback string) string {
	if strings.TrimSpace(message) != "" {
		return strings.TrimSpace(message)
	}
	return fallback
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
	if opts.noRetry {
		attempts = 1
	}
	var lastRetryAfter time.Duration
	var rawRetryAfter string
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, ErrNetworkUnavailable{Endpoint: endpoint, Attempts: attempt - 1, Cause: err}
		}
		if err := c.waitForRateLimit(ctx, method, endpoint, attempt); err != nil {
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
		if resp.StatusCode == http.StatusRequestEntityTooLarge {
			resp.Body.Close()
			return nil, nil, ErrPayloadTooLarge{Endpoint: endpoint, Limit: c.maxResponseSize, Size: resp.ContentLength, Source: "remote_status"}
		}
		body, readErr := c.readBounded(resp, endpoint)
		headers := resp.Header.Clone()
		resp.Body.Close()
		if readErr != nil {
			readErr = withResponseContentType(withResponseAttempt(readErr, attempt), headers.Get("Content-Type"))
			if retryableResponseDecode(readErr) && attempt < attempts {
				continue
			}
			if resp.StatusCode >= 200 && resp.StatusCode <= 299 && recoverableCollectionDecode(readErr) && len(body) > 0 {
				headers.Set("Status", strconv.Itoa(resp.StatusCode))
				return body, headers, readErr
			}
			return nil, nil, readErr
		}
		switch {
		case resp.StatusCode >= 200 && resp.StatusCode <= 299:
			headers.Set("Status", strconv.Itoa(resp.StatusCode))
			return body, headers, nil
		case resp.StatusCode == http.StatusTooManyRequests:
			rawRetryAfter = resp.Header.Get("Retry-After")
			lastRetryAfter = effectiveRateLimitRetryAfter(rawRetryAfter, time.Now(), attempt)
			emitRateLimitEvent(ctx, RateLimitEvent{Type: RateLimitEventResponseRateLimited, Method: method, Endpoint: endpoint, Attempt: attempt, Wait: lastRetryAfter, ResumeAt: time.Now().Add(lastRetryAfter), RawRetryAfter: rawRetryAfter})
			if attempt < attempts {
				resumeAt := time.Now().Add(lastRetryAfter)
				emitRateLimitEvent(ctx, RateLimitEvent{Type: RateLimitEventRetryAfterWaitStarted, Method: method, Endpoint: endpoint, Attempt: attempt, Wait: lastRetryAfter, ResumeAt: resumeAt, RawRetryAfter: rawRetryAfter})
				if err := sleepContext(ctx, lastRetryAfter); err != nil {
					return nil, nil, ErrNetworkUnavailable{Endpoint: endpoint, Attempts: attempt, Cause: err}
				}
				emitRateLimitEvent(ctx, RateLimitEvent{Type: RateLimitEventRetryAfterWaitCompleted, Method: method, Endpoint: endpoint, Attempt: attempt, Wait: lastRetryAfter, ResumeAt: resumeAt, RawRetryAfter: rawRetryAfter})
				continue
			}
			return nil, nil, ErrRateLimited{RetryAfter: lastRetryAfter, RawRetryAfter: rawRetryAfter, Endpoint: endpoint, Attempts: attempt}
		case resp.StatusCode >= 500 && resp.StatusCode <= 599:
			if attempt < attempts {
				continue
			}
			return nil, nil, ErrNetworkUnavailable{Endpoint: endpoint, Status: resp.StatusCode, Attempts: attempt}
		case isWikiEmptyResponse(resp.StatusCode, body):
			owner, repo := parseWikiEndpointOwnerRepo(endpoint)
			return nil, nil, ErrEmptyWiki{Owner: owner, Repo: repo}
		default:
			return nil, nil, c.statusError(resp.StatusCode, endpoint, body, opts)
		}
	}
	return nil, nil, ErrNetworkUnavailable{Endpoint: endpoint, Attempts: attempts}
}

func effectiveRateLimitRetryAfter(raw string, now time.Time, attempt int) time.Duration {
	if wait := parseRetryAfter(raw, now); wait > 0 {
		return wait
	}
	if attempt < 1 {
		attempt = 1
	}
	wait := time.Second << min(attempt-1, 5)
	if wait > 30*time.Second {
		return 30 * time.Second
	}
	return wait
}

func (c *HTTPClient) waitForRateLimit(ctx context.Context, method, endpoint string, attempt int) error {
	if c.rateLimiter == nil {
		return nil
	}
	c.rateLimiterMu.Lock()
	wait, resumeAt := c.rateLimiter.reserve(time.Now())
	rps := c.rateLimiter.rps
	burst := c.rateLimiter.burst
	c.rateLimiterMu.Unlock()
	if wait <= 0 {
		return nil
	}
	emitRateLimitEvent(ctx, RateLimitEvent{Type: RateLimitEventThrottleWaitStarted, Method: method, Endpoint: endpoint, Attempt: attempt, Wait: wait, ResumeAt: resumeAt, RPS: rps, Burst: burst})
	if err := sleepContext(ctx, wait); err != nil {
		return err
	}
	emitRateLimitEvent(ctx, RateLimitEvent{Type: RateLimitEventThrottleWaitCompleted, Method: method, Endpoint: endpoint, Attempt: attempt, Wait: wait, ResumeAt: resumeAt, RPS: rps, Burst: burst})
	return nil
}

func (c *HTTPClient) do(ctx context.Context, method, endpoint string, values url.Values, body io.Reader, opts requestOptions) (*http.Response, error) {
	if endpoint == "" || strings.HasPrefix(endpoint, "//") {
		return nil, ErrValidationFailed{Field: "endpoint", Message: "relative endpoint path is required"}
	}
	if parsed, err := url.Parse(endpoint); err != nil || parsed.IsAbs() || parsed.Host != "" {
		return nil, ErrValidationFailed{Field: "endpoint", Message: "relative endpoint path is required"}
	}
	v4Request := strings.HasPrefix(endpoint, "/api/v4/")
	baseURL := c.baseURL
	if v4Request {
		baseURL = c.v4BaseURL()
	}
	ref := &url.URL{Path: endpoint}
	if strings.Contains(endpoint, "%") {
		if unescaped, err := url.PathUnescape(endpoint); err == nil {
			ref.Path = unescaped
			ref.RawPath = endpoint
		}
	}
	u := baseURL.ResolveReference(ref)
	if values != nil {
		u.RawQuery = values.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	if c.token != "" && v4Request {
		req.Header.Set("PRIVATE-TOKEN", c.token)
	} else if c.token != "" {
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

func (c *HTTPClient) v4BaseURL() *url.URL {
	u := *c.baseURL
	if u.Host == "gitcode.com" || u.Host == "www.gitcode.com" {
		u.Scheme = "https"
		u.Host = "api.gitcode.com"
	}
	return &u
}

func (c *HTTPClient) readBounded(resp *http.Response, endpoint string) ([]byte, error) {
	if resp.ContentLength > c.maxResponseSize {
		return nil, ErrPayloadTooLarge{Endpoint: endpoint, Limit: c.maxResponseSize, Size: resp.ContentLength, Source: "remote_status"}
	}
	limit := c.maxResponseSize + 1
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if int64(len(body)) > c.maxResponseSize {
		return nil, ErrPayloadTooLarge{Endpoint: endpoint, Limit: c.maxResponseSize, Size: int64(len(body)), Source: "local_body_limit"}
	}
	if err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return body, ErrPartialResponse{Endpoint: endpoint, Expected: resp.ContentLength, Got: int64(len(body)), Cause: err}
		}
		return nil, ErrNetworkUnavailable{Endpoint: endpoint, Cause: err, Attempts: 1}
	}
	if resp.ContentLength >= 0 && resp.ContentLength != int64(len(body)) {
		return body, ErrPartialResponse{Endpoint: endpoint, Expected: resp.ContentLength, Got: int64(len(body))}
	}
	return body, nil
}

func (c *HTTPClient) statusError(status int, endpoint string, body []byte, opts requestOptions) error {
	if status == http.StatusForbidden && strings.Contains(endpoint, "/push_remote_mirrors/") && pushMirrorCooldownResponse(body) {
		return ErrPushMirrorSyncInProgress{Endpoint: endpoint, Message: "sync too frequently; wait for the current synchronization or cooldown to finish"}
	}
	msg := responseMessage(status, body)
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
	case http.StatusRequestEntityTooLarge:
		return ErrPayloadTooLarge{Endpoint: endpoint, Limit: c.maxResponseSize, Size: int64(len(body)), Source: "remote_status"}
	default:
		if status >= 400 && status <= 499 {
			return ErrAPIValidation{Endpoint: endpoint, Status: status, Message: msg}
		}
		return ErrNetworkUnavailable{Endpoint: endpoint, Status: status, Attempts: 1}
	}
}

func pushMirrorCooldownResponse(body []byte) bool {
	var payload struct {
		ErrorMessage string `json:"error_message"`
		Message      string `json:"message"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(firstNonEmpty(payload.ErrorMessage, payload.Message)))
	return strings.Contains(message, "sync too frequently")
}

func issueListQuery(req IssueListRequest) url.Values {
	values := url.Values{}
	if req.State != "" {
		values.Set("state", req.State)
	}
	if req.OrderBy != "" {
		values.Set("order_by", req.OrderBy)
	}
	if req.Direction != "" {
		values.Set("sort", req.Direction)
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

func responseMessage(status int, body []byte) string {
	if len(body) == 0 {
		return http.StatusText(status)
	}
	return diagnostics.NewFilter().RawAPIResponseSummary(status, body)
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

func parseWikiEndpointOwnerRepo(endpoint string) (owner, repo string) {
	trimmed := strings.TrimLeft(endpoint, "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) < 5 || parts[0] != "api" || parts[1] != "v5" || parts[2] != "repos" {
		return "", ""
	}
	owner = parts[3]
	repoWithWiki := parts[4]
	repo = strings.TrimSuffix(repoWithWiki, ".wiki")
	return owner, repo
}
