package gitcode

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
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
	if err := validateReadRepo(req.Owner, req.Repo); err != nil {
		return Page[IssueSummary]{}, err
	}
	endpoint := listIssuesEndpoint(req.Owner, req.Repo)
	items, page, err := getPaged[IssueSummary](ctx, c, endpoint, issueListQuery(req), PageState{Page: req.Page, PerPage: req.PerPage})
	if err != nil {
		return Page[IssueSummary]{}, err
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
	for i := range items {
		items[i].PRNumber = req.Number
		if items[i].DiscussionID == "" {
			items[i].DiscussionID = strconv.Itoa(req.Number)
		}
	}
	return Page[PRComment]{Items: items, Page: page.Page, PerPage: page.PerPage}, nil
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
	if req.Bounds != nil {
		walker.maxRecords = req.Bounds.MaxRecords
		walker.progressChan = req.Bounds.ProgressChan
	}
	items, err := walker.walk(ctx, "", 0)
	if err != nil {
		return Page[WikiPage]{}, err
	}
	return Page[WikiPage]{Items: items, Page: firstPositive(req.Page, 1), PerPage: firstPositive(req.PerPage, len(items)), TotalCount: len(items)}, nil
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
	target := req.Owner + "/" + req.Repo + "/" + strconv.Itoa(req.Number)
	return writeConfirmedJSON[Issue](ctx, c, http.MethodPatch, updateIssueEndpoint(req.Owner, req.Repo, req.Number), "UpdateIssue", target, updateIssuePayload(req), opts, func(result WriteResult[Issue]) (WriteResult[Issue], error) {
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

func (c *HTTPClient) CreateWikiPage(ctx context.Context, req CreateWikiPageRequest, opts WriteOptions) (WriteResult[WikiPage], error) {
	if err := validateCreateWikiPage(req); err != nil {
		return WriteResult[WikiPage]{}, err
	}
	wikiPath := wikiRequestPath(req.Path, req.Slug)
	payload := WikiContentWriteRequest{Content: base64.StdEncoding.EncodeToString([]byte(req.Body)), Message: wikiWriteMessage(req.Message, "create wiki page")}
	target := req.Owner + "/" + req.Repo + "/" + wikiPath
	return c.writeWikiContent(ctx, http.MethodPost, wikiContentsPathEndpoint(req.Owner, req.Repo, wikiPath), "CreateWikiPage", target, payload, opts, req.Owner, req.Repo, wikiPath, req.Body)
}

func (c *HTTPClient) UpdateWikiPage(ctx context.Context, req UpdateWikiPageRequest, opts WriteOptions) (WriteResult[WikiPage], error) {
	if err := validateUpdateWikiPage(req); err != nil {
		return WriteResult[WikiPage]{}, err
	}
	wikiPath := wikiRequestPath(req.Path, req.Slug)
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
	wikiPath := wikiRequestPath(req.Path, req.Slug)
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
	return c.writeWikiContent(ctx, http.MethodDelete, deleteWikiPageEndpoint(req.Owner, req.Repo, wikiPath), "DeleteWikiPage", target, payload, opts, req.Owner, req.Repo, wikiPath, "")
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

type wikiTraversal struct {
	client       *HTTPClient
	owner        string
	repo         string
	seenDirs     map[string]bool
	seenFiles    map[string]bool
	maxRecords   int
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
		if w.maxRecords > 0 && len(out) >= w.maxRecords {
			break
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
		return result, nil
	})
	if err != nil {
		return WriteResult[WikiPage]{}, err
	}
	page := wikiPageFromMetadata(result.Record, body)
	return WriteResult[WikiPage]{Record: page, Confirmed: result.Confirmed, Operation: result.Operation, Target: result.Target, ProviderStatus: result.ProviderStatus, RemoteID: result.RemoteID, RemoteSlug: result.RemoteSlug, RemoteRevision: result.RemoteRevision, IdempotencyKey: result.IdempotencyKey, ResponseHash: result.ResponseHash, ConfirmedAt: result.ConfirmedAt, ProviderPayloadFingerprint: result.ProviderPayloadFingerprint}, nil
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

func decodeSchemaJSON(endpoint string, body []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(out); err != nil {
		return &ErrSchemaDecode{Field: endpoint, Expected: "valid JSON response", Received: decodeMessage(err)}
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
		Title  string           `json:"title"`
		Body   string           `json:"body,omitempty"`
		Labels *json.RawMessage `json:"labels,omitempty"`
	}{Title: req.Title, Body: req.Body}
	if len(req.Labels) > 0 {
		labels := req.Labels
		payload.Labels = &labels
	}
	return payload
}

func updateIssuePayload(req UpdateIssueRequest) any {
	payload := struct {
		Title  string           `json:"title,omitempty"`
		Body   string           `json:"body,omitempty"`
		State  string           `json:"state,omitempty"`
		Labels *json.RawMessage `json:"labels,omitempty"`
	}{Title: req.Title, Body: req.Body, State: req.State}
	if len(req.Labels) > 0 {
		labels := req.Labels
		payload.Labels = &labels
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
		if resp.StatusCode == http.StatusRequestEntityTooLarge {
			resp.Body.Close()
			return nil, nil, ErrPayloadTooLarge{Endpoint: endpoint, Limit: c.maxResponseSize, Size: resp.ContentLength, Source: "remote_status"}
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
		case isWikiEmptyResponse(resp.StatusCode, body):
			owner, repo := parseWikiEndpointOwnerRepo(endpoint)
			return nil, nil, ErrEmptyWiki{Owner: owner, Repo: repo}
		default:
			return nil, nil, c.statusError(resp.StatusCode, endpoint, body, opts)
		}
	}
	return nil, nil, ErrNetworkUnavailable{Endpoint: endpoint, Attempts: attempts}
}

func (c *HTTPClient) do(ctx context.Context, method, endpoint string, values url.Values, body io.Reader, opts requestOptions) (*http.Response, error) {
	if endpoint == "" || strings.HasPrefix(endpoint, "//") {
		return nil, ErrValidationFailed{Field: "endpoint", Message: "relative endpoint path is required"}
	}
	if parsed, err := url.Parse(endpoint); err != nil || parsed.IsAbs() || parsed.Host != "" {
		return nil, ErrValidationFailed{Field: "endpoint", Message: "relative endpoint path is required"}
	}
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
		return nil, ErrPayloadTooLarge{Endpoint: endpoint, Limit: c.maxResponseSize, Size: resp.ContentLength, Source: "remote_status"}
	}
	limit := c.maxResponseSize + 1
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if int64(len(body)) > c.maxResponseSize {
		return nil, ErrPayloadTooLarge{Endpoint: endpoint, Limit: c.maxResponseSize, Size: int64(len(body)), Source: "local_body_limit"}
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
