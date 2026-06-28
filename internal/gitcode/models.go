package gitcode

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type IssueListRequest struct {
	Owner     string
	Repo      string
	State     string
	Labels    []string
	OrderBy   string
	Direction string
	Page      int
	PerPage   int
}

type IssueRequest struct {
	Owner            string
	Repo             string
	Number           int
	KnownRemoteAlias bool
	RemoteAlias      string
}

type PRListRequest struct {
	Owner     string
	Repo      string
	State     string
	OrderBy   string
	Direction string
	Page      int
	PerPage   int
}

type PRRequest struct {
	Owner  string
	Repo   string
	Number int
}

type CreatePRRequest struct {
	Owner string `json:"-"`
	Repo  string `json:"-"`
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
	Head  string `json:"head"`
	Base  string `json:"base"`
}

type UpdatePRRequest struct {
	Owner  string `json:"-"`
	Repo   string `json:"-"`
	Number int    `json:"-"`
	Title  string `json:"title,omitempty"`
	Body   string `json:"body,omitempty"`
	State  string `json:"state,omitempty"`
}

type CreatePRCommentRequest struct {
	Owner  string `json:"-"`
	Repo   string `json:"-"`
	Number int    `json:"-"`
	Body   string `json:"body"`
}

type LinkPRIssueRequest struct {
	Owner       string `json:"-"`
	Repo        string `json:"-"`
	Number      int    `json:"-"`
	IssueNumber int    `json:"-"`
}

type WikiPageRequest struct {
	Owner string
	Repo  string
	Slug  string
	Path  string
}

type WikiListRequest struct {
	Owner   string
	Repo    string
	Page    int
	PerPage int
	Bounds  *WikiBounds
}

type WikiBounds struct {
	MaxRecords   int
	ProgressChan chan<- WikiProgressEvent
}

type WikiProgressEvent struct {
	Path           string
	RecordsFetched int
}

type SearchRequest struct {
	Query   string
	Owner   string
	Repo    string
	Type    string
	Page    int
	PerPage int
}

type AttachmentRequest struct {
	Owner        string
	Repo         string
	IssueNumber  int
	AttachmentID string
	Name         string
}

type Page[T any] struct {
	Items      []T
	Page       int
	PerPage    int
	TotalCount int
	NextPage   int
}

type GitCodeLabel struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Color     string `json:"color"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type IssueSummary struct {
	ID            string         `json:"id"`
	Number        int            `json:"number"`
	Title         string         `json:"title"`
	Body          string         `json:"body"`
	Status        string         `json:"status"`
	State         string         `json:"state"`
	Comments      int            `json:"comments"`
	GitCodeLabels []GitCodeLabel `json:"labels"`
	Labels        []string       `json:"-"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

func (i *IssueSummary) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID        any             `json:"id"`
		Number    any             `json:"number"`
		Title     string          `json:"title"`
		Body      string          `json:"body"`
		Status    string          `json:"status"`
		State     string          `json:"state"`
		Comments  any             `json:"comments"`
		Labels    json.RawMessage `json:"labels"`
		CreatedAt time.Time       `json:"created_at"`
		UpdatedAt time.Time       `json:"updated_at"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	var err error
	i.ID, err = decodeID(raw.ID)
	if err != nil {
		return err
	}
	i.Number, err = decodeNumber(raw.Number)
	if err != nil {
		return err
	}
	i.Title = raw.Title
	i.Body = raw.Body
	i.Status = raw.Status
	i.State = raw.State
	i.Comments, err = decodeOptionalInt(raw.Comments)
	if err != nil {
		return err
	}
	if len(raw.Labels) > 0 {
		if isLabelObjectArray(raw.Labels) {
			if err := json.Unmarshal(raw.Labels, &i.GitCodeLabels); err != nil {
				return err
			}
			i.Labels, err = NormalizeLabels(i.GitCodeLabels)
			if err != nil {
				return err
			}
		} else {
			var strs []string
			if err := json.Unmarshal(raw.Labels, &strs); err != nil {
				return err
			}
			i.Labels = strs
		}
	}
	i.CreatedAt = raw.CreatedAt
	i.UpdatedAt = raw.UpdatedAt
	return nil
}

type Issue struct {
	ID            string         `json:"id"`
	Number        int            `json:"number"`
	Title         string         `json:"title"`
	Body          string         `json:"body"`
	Status        string         `json:"status"`
	State         string         `json:"state"`
	Comments      int            `json:"comments"`
	GitCodeLabels []GitCodeLabel `json:"labels"`
	Labels        []string       `json:"-"`
	Author        string         `json:"author"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

func (i *Issue) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID        any             `json:"id"`
		Number    any             `json:"number"`
		Title     string          `json:"title"`
		Body      string          `json:"body"`
		Status    string          `json:"status"`
		State     string          `json:"state"`
		Comments  any             `json:"comments"`
		Labels    json.RawMessage `json:"labels"`
		Author    string          `json:"author"`
		CreatedAt time.Time       `json:"created_at"`
		UpdatedAt time.Time       `json:"updated_at"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	var err error
	i.ID, err = decodeID(raw.ID)
	if err != nil {
		return err
	}
	i.Number, err = decodeNumber(raw.Number)
	if err != nil {
		return err
	}
	i.Title = raw.Title
	i.Body = raw.Body
	i.Status = raw.Status
	i.State = raw.State
	i.Comments, err = decodeOptionalInt(raw.Comments)
	if err != nil {
		return err
	}
	if len(raw.Labels) > 0 {
		if isLabelObjectArray(raw.Labels) {
			if err := json.Unmarshal(raw.Labels, &i.GitCodeLabels); err != nil {
				return err
			}
			i.Labels, err = NormalizeLabels(i.GitCodeLabels)
			if err != nil {
				return err
			}
		} else {
			var strs []string
			if err := json.Unmarshal(raw.Labels, &strs); err != nil {
				return err
			}
			i.Labels = strs
		}
	}
	i.Author = raw.Author
	i.CreatedAt = raw.CreatedAt
	i.UpdatedAt = raw.UpdatedAt
	return nil
}

func isLabelObjectArray(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return len(s) >= 2 && s[0] == '[' && s[1] == '{'
}

func decodeID(value any) (string, error) {
	switch v := value.(type) {
	case string:
		if v == "" || v == "0" {
			return "", &ErrSchemaDecode{Field: "id", Message: "id must not be empty or zero"}
		}
		return v, nil
	case float64:
		if v == 0 {
			return "", &ErrSchemaDecode{Field: "id", Message: "id must not be empty or zero"}
		}
		return strconv.FormatInt(int64(v), 10), nil
	case nil:
		return "", &ErrSchemaDecode{Field: "id", Message: "id is required"}
	default:
		return "", &ErrSchemaDecode{Field: "id", Message: fmt.Sprintf("unexpected id type %T", v)}
	}
}

func decodeNumber(value any) (int, error) {
	switch v := value.(type) {
	case float64:
		return int(v), nil
	case string:
		if v == "" {
			return 0, &ErrSchemaDecode{Field: "number", Message: "number is required"}
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return 0, &ErrSchemaDecode{Field: "number", Message: fmt.Sprintf("cannot parse number %q: %v", v, err)}
		}
		return n, nil
	case nil:
		return 0, &ErrSchemaDecode{Field: "number", Message: "number is required"}
	default:
		return 0, &ErrSchemaDecode{Field: "number", Message: fmt.Sprintf("unexpected number type %T", v)}
	}
}

func decodeOptionalInt(value any) (int, error) {
	if value == nil {
		return 0, nil
	}
	return decodeNumber(value)
}

type Comment struct {
	ID        string    `json:"id"`
	IssueID   string    `json:"issue_id"`
	Body      string    `json:"body"`
	Author    string    `json:"author"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (c *Comment) UnmarshalJSON(data []byte) error {
	type rawComment struct {
		ID        any             `json:"id"`
		NoteID    any             `json:"note_id"`
		IssueID   any             `json:"issue_id"`
		Body      string          `json:"body"`
		Author    string          `json:"author"`
		User      json.RawMessage `json:"user"`
		CreatedAt string          `json:"created_at"`
		UpdatedAt string          `json:"updated_at"`
	}
	var raw rawComment
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	id, err := decodeOptionalID(firstNonNil(raw.NoteID, raw.ID))
	if err != nil {
		return err
	}
	issueID, err := decodeOptionalID(raw.IssueID)
	if err != nil {
		return err
	}
	created, err := decodeOptionalTime("comment.created_at", raw.CreatedAt)
	if err != nil {
		return err
	}
	updated, err := decodeOptionalTime("comment.updated_at", raw.UpdatedAt)
	if err != nil {
		return err
	}
	*c = Comment{ID: id, IssueID: issueID, Body: raw.Body, Author: firstNonEmpty(raw.Author, decodeCommentUser(raw.User)), CreatedAt: created, UpdatedAt: updated}
	return nil
}

func firstNonNil(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func decodeOptionalID(value any) (string, error) {
	if value == nil {
		return "", nil
	}
	return decodeID(value)
}

func decodeOptionalTime(field, value string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, &ErrSchemaDecode{Field: field, Expected: "RFC3339 timestamp or absent", Received: fmt.Sprintf("%q", value)}
	}
	return parsed, nil
}

func decodeCommentUser(data json.RawMessage) string {
	if len(data) == 0 || string(data) == "null" {
		return ""
	}
	var user struct {
		Login    string `json:"login"`
		Username string `json:"username"`
		Name     string `json:"name"`
		ID       any    `json:"id"`
	}
	if err := json.Unmarshal(data, &user); err != nil {
		return ""
	}
	id, _ := decodeOptionalID(user.ID)
	return firstNonEmpty(user.Login, user.Username, user.Name, id)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

type PullRequest struct {
	Kind          string         `json:"-"`
	SourceID      string         `json:"-"`
	ID            string         `json:"id"`
	Number        int            `json:"number"`
	HTMLURL       string         `json:"html_url"`
	State         string         `json:"state"`
	Title         string         `json:"title"`
	Body          string         `json:"body"`
	User          string         `json:"-"`
	GitCodeLabels []GitCodeLabel `json:"labels"`
	Labels        []string       `json:"-"`
	Base          string         `json:"-"`
	Head          string         `json:"-"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

func (p *PullRequest) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID        any             `json:"id"`
		Number    any             `json:"number"`
		HTMLURL   string          `json:"html_url"`
		State     string          `json:"state"`
		Title     string          `json:"title"`
		Body      string          `json:"body"`
		User      json.RawMessage `json:"user"`
		Labels    json.RawMessage `json:"labels"`
		Base      json.RawMessage `json:"base"`
		Head      json.RawMessage `json:"head"`
		CreatedAt time.Time       `json:"created_at"`
		UpdatedAt time.Time       `json:"updated_at"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	id, err := decodeID(raw.ID)
	if err != nil {
		return err
	}
	number, err := decodeNumber(raw.Number)
	if err != nil {
		return err
	}
	p.Kind = "pull_request"
	p.SourceID = pullRequestSourceID(number)
	p.ID = id
	p.Number = number
	p.HTMLURL = raw.HTMLURL
	p.State = raw.State
	p.Title = raw.Title
	p.Body = raw.Body
	p.User = decodeActor(raw.User)
	if len(raw.Labels) > 0 {
		if isLabelObjectArray(raw.Labels) {
			if err := json.Unmarshal(raw.Labels, &p.GitCodeLabels); err != nil {
				return err
			}
			p.Labels, err = NormalizeLabels(p.GitCodeLabels)
			if err != nil {
				return err
			}
		} else {
			var strs []string
			if err := json.Unmarshal(raw.Labels, &strs); err != nil {
				return err
			}
			p.Labels = strs
		}
	}
	p.Base = decodeRef(raw.Base)
	p.Head = decodeRef(raw.Head)
	p.CreatedAt = raw.CreatedAt
	p.UpdatedAt = raw.UpdatedAt
	return nil
}

type PRComment struct {
	Kind             string    `json:"-"`
	ID               string    `json:"id"`
	Body             string    `json:"body"`
	Author           string    `json:"author"`
	DiscussionID     string    `json:"discussion_id"`
	ReviewKind       string    `json:"review_kind,omitempty"`
	Path             string    `json:"path,omitempty"`
	Line             int       `json:"line,omitempty"`
	StartLine        int       `json:"start_line,omitempty"`
	EndLine          int       `json:"end_line,omitempty"`
	Position         int       `json:"position,omitempty"`
	OriginalPosition int       `json:"original_position,omitempty"`
	Resolved         *bool     `json:"resolved,omitempty"`
	Resolvable       *bool     `json:"resolvable,omitempty"`
	ParentID         string    `json:"parent_id,omitempty"`
	PRNumber         int       `json:"-"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func (c *PRComment) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID               any             `json:"id"`
		NoteID           any             `json:"note_id"`
		Body             string          `json:"body"`
		Author           string          `json:"author"`
		User             json.RawMessage `json:"user"`
		DiscussionID     any             `json:"discussion_id"`
		Type             string          `json:"type"`
		Kind             string          `json:"kind"`
		NoteableType     string          `json:"noteable_type"`
		Path             string          `json:"path"`
		FilePath         string          `json:"file_path"`
		NewPath          string          `json:"new_path"`
		Line             any             `json:"line"`
		NewLine          any             `json:"new_line"`
		StartLine        any             `json:"start_line"`
		EndLine          any             `json:"end_line"`
		Position         any             `json:"position"`
		OriginalPosition any             `json:"original_position"`
		Resolved         *bool           `json:"resolved"`
		Resolvable       *bool           `json:"resolvable"`
		ParentID         any             `json:"parent_id"`
		InReplyToID      any             `json:"in_reply_to_id"`
		ReplyID          any             `json:"reply_id"`
		CreatedAt        string          `json:"created_at"`
		UpdatedAt        string          `json:"updated_at"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	id, err := decodeOptionalID(firstNonNil(raw.NoteID, raw.ID))
	if err != nil {
		return err
	}
	discussionID, err := decodeOptionalID(raw.DiscussionID)
	if err != nil {
		return err
	}
	created, err := decodeOptionalTime("pr_comment.created_at", raw.CreatedAt)
	if err != nil {
		return err
	}
	updated, err := decodeOptionalTime("pr_comment.updated_at", raw.UpdatedAt)
	if err != nil {
		return err
	}
	line, err := decodeOptionalInt(firstNonNil(raw.Line, raw.NewLine))
	if err != nil {
		return err
	}
	startLine, err := decodeOptionalInt(raw.StartLine)
	if err != nil {
		return err
	}
	endLine, err := decodeOptionalInt(raw.EndLine)
	if err != nil {
		return err
	}
	position, err := decodeOptionalInt(raw.Position)
	if err != nil {
		return err
	}
	originalPosition, err := decodeOptionalInt(raw.OriginalPosition)
	if err != nil {
		return err
	}
	parentID, err := decodeOptionalID(firstNonNil(raw.ParentID, raw.InReplyToID, raw.ReplyID))
	if err != nil {
		return err
	}
	path := firstNonEmpty(raw.Path, raw.FilePath, raw.NewPath)
	reviewKind := "general"
	rawKind := strings.ToLower(firstNonEmpty(raw.Kind, raw.Type, raw.NoteableType))
	if path != "" || line > 0 || position > 0 || strings.Contains(rawKind, "inline") || strings.Contains(rawKind, "diff") {
		reviewKind = "inline"
	}
	*c = PRComment{Kind: "pr_comment", ID: id, Body: raw.Body, Author: firstNonEmpty(raw.Author, decodeActor(raw.User)), DiscussionID: discussionID, ReviewKind: reviewKind, Path: path, Line: line, StartLine: startLine, EndLine: endLine, Position: position, OriginalPosition: originalPosition, Resolved: raw.Resolved, Resolvable: raw.Resolvable, ParentID: parentID, CreatedAt: created, UpdatedAt: updated}
	return nil
}

func pullRequestSourceID(number int) string {
	return "PR-" + strconv.Itoa(number)
}

func decodeActor(data json.RawMessage) string {
	return decodeCommentUser(data)
}

func decodeRef(data json.RawMessage) string {
	if len(data) == 0 || string(data) == "null" {
		return ""
	}
	var ref struct {
		Ref   string `json:"ref"`
		Label string `json:"label"`
		Sha   string `json:"sha"`
		Repo  struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	}
	if err := json.Unmarshal(data, &ref); err != nil {
		return ""
	}
	return firstNonEmpty(ref.Ref, ref.Label, ref.Sha, ref.Repo.FullName)
}

type WikiPage struct {
	ID        string    `json:"id"`
	Slug      string    `json:"slug"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Revision  string    `json:"revision"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type SearchResult struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	URL       string    `json:"url"`
	Score     float64   `json:"score"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type AttachmentSummary struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	ContentType string    `json:"content_type"`
	Size        int64     `json:"size"`
	Checksum    string    `json:"checksum"`
	DownloadURL string    `json:"download_url"`
	CreatedAt   time.Time `json:"created_at"`
}

type AttachmentBody struct {
	ID             string
	Name           string
	ContentType    string
	Size           int64
	Body           []byte
	SourceEndpoint string
	Checksum       string
}

type WriteOptions struct {
	IdempotencyKey   string
	IdempotencyNonce string
}

type WriteResult[T any] struct {
	Record                     T
	Confirmed                  bool
	Operation                  string
	Target                     string
	ProviderStatus             string
	RemoteID                   string
	RemoteNumber               int
	RemoteSlug                 string
	RemoteRevision             string
	ParentIssueNumber          int
	ParentIssueID              string
	IdempotencyKey             string
	ResponseHash               string
	ConfirmedAt                time.Time
	ProviderPayloadFingerprint string
}

type CreateIssueRequest struct {
	Owner  string          `json:"-"`
	Repo   string          `json:"-"`
	Title  string          `json:"title"`
	Body   string          `json:"body,omitempty"`
	Labels json.RawMessage `json:"labels,omitempty"`
}

type UpdateIssueRequest struct {
	Owner  string          `json:"-"`
	Repo   string          `json:"-"`
	Number int             `json:"-"`
	Title  string          `json:"title,omitempty"`
	Body   string          `json:"body,omitempty"`
	State  string          `json:"state,omitempty"`
	Labels json.RawMessage `json:"labels,omitempty"`
}

type CreateIssueCommentRequest struct {
	Owner  string `json:"-"`
	Repo   string `json:"-"`
	Number int    `json:"-"`
	Body   string `json:"body"`
}

type UpdateIssueCommentRequest struct {
	Owner     string `json:"-"`
	Repo      string `json:"-"`
	Number    int    `json:"-"`
	CommentID string `json:"-"`
	Body      string `json:"body"`
}

type WikiContentsEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Sha  string `json:"sha,omitempty"`
	Size int64  `json:"size,omitempty"`
}

type WikiContentsFile struct {
	Path     string `json:"path"`
	Type     string `json:"type"`
	Sha      string `json:"sha"`
	Content  string `json:"content,omitempty"`
	Encoding string `json:"encoding,omitempty"`
	Size     int64  `json:"size,omitempty"`
}

type WikiContentWriteRequest struct {
	Content string `json:"content,omitempty"`
	Message string `json:"message,omitempty"`
	Sha     string `json:"sha,omitempty"`
}

type CreateWikiPageRequest struct {
	Owner   string `json:"-"`
	Repo    string `json:"-"`
	Slug    string `json:"-"`
	Path    string `json:"-"`
	Title   string `json:"-"`
	Body    string `json:"-"`
	Message string `json:"-"`
}

type UpdateWikiPageRequest struct {
	Owner   string `json:"-"`
	Repo    string `json:"-"`
	Slug    string `json:"-"`
	Path    string `json:"-"`
	Title   string `json:"-"`
	Body    string `json:"-"`
	Sha     string `json:"-"`
	Message string `json:"-"`
}

type DeleteWikiPageRequest struct {
	Owner   string `json:"-"`
	Repo    string `json:"-"`
	Slug    string `json:"-"`
	Path    string `json:"-"`
	Sha     string `json:"-"`
	Message string `json:"-"`
}

type LabelRequest struct {
	Owner  string `json:"-"`
	Repo   string `json:"-"`
	Number int    `json:"-"`
	Label  string `json:"label"`
}

type MilestoneListRequest struct {
	Owner   string
	Repo    string
	State   string
	Page    int
	PerPage int
}

type MilestoneRequest struct {
	Owner string
	Repo  string
	ID    int
}

type Milestone struct {
	RemoteID  string `json:"-"`
	SourceID  string `json:"-"`
	Title     string `json:"-"`
	Body      string `json:"-"`
	Status    string `json:"-"`
	DueOn     string `json:"-"`
	HTMLURL   string `json:"-"`
	CreatedAt string `json:"-"`
	UpdatedAt string `json:"-"`
}

func (m *Milestone) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID          any    `json:"id"`
		Title       string `json:"title"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Body        string `json:"body"`
		State       string `json:"state"`
		Status      string `json:"status"`
		DueOn       string `json:"due_on"`
		DueDate     string `json:"due_date"`
		HTMLURL     string `json:"html_url"`
		CreatedAt   string `json:"created_at"`
		UpdatedAt   string `json:"updated_at"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	var err error
	m.RemoteID, err = decodeMilestoneID(raw.ID)
	if err != nil {
		m.RemoteID = ""
		return err
	}
	if strings.TrimSpace(raw.Title) == "" {
		if strings.TrimSpace(raw.Name) != "" {
			return &ErrSchemaDecode{Field: "milestone.title", Expected: "non-empty string", Received: "missing", Message: "title is required; name alone is not accepted"}
		}
		return &ErrSchemaDecode{Field: "milestone.title", Expected: "non-empty string", Received: "missing"}
	}
	m.SourceID = "MILESTONE-" + m.RemoteID
	m.Title = raw.Title
	if strings.TrimSpace(raw.Description) != "" {
		m.Body = raw.Description
	} else {
		m.Body = raw.Body
	}
	switch r := any(raw.Description).(type) {
	case string:
		break
	case nil:
		break
	default:
		return &ErrSchemaDecode{Field: "milestone.description", Expected: "string or absent", Received: fmt.Sprintf("type %T", r)}
	}
	switch r := any(raw.Body).(type) {
	case string:
		break
	case nil:
		break
	default:
		return &ErrSchemaDecode{Field: "milestone.body", Expected: "string or absent", Received: fmt.Sprintf("type %T", r)}
	}
	m.Status, err = decodeMilestoneStatus(raw.State, raw.Status)
	if err != nil {
		return err
	}
	m.DueOn, err = decodeMilestoneDate(raw.DueOn, raw.DueDate)
	if err != nil {
		return err
	}
	if raw.CreatedAt != "" {
		if !isValidTimestamp(raw.CreatedAt) {
			return &ErrSchemaDecode{Field: "milestone.created_at", Expected: "RFC3339 timestamp or absent", Received: fmt.Sprintf("%q", raw.CreatedAt)}
		}
		m.CreatedAt = raw.CreatedAt
	}
	if raw.UpdatedAt != "" {
		if !isValidTimestamp(raw.UpdatedAt) {
			return &ErrSchemaDecode{Field: "milestone.updated_at", Expected: "RFC3339 timestamp or absent", Received: fmt.Sprintf("%q", raw.UpdatedAt)}
		}
		m.UpdatedAt = raw.UpdatedAt
	}
	m.HTMLURL = raw.HTMLURL
	return nil
}

func decodeMilestoneID(value any) (string, error) {
	switch v := value.(type) {
	case string:
		if v == "" {
			return "", &ErrSchemaDecode{Field: "milestone.id", Expected: "non-empty decimal string", Received: "empty string"}
		}
		if _, err := strconv.Atoi(v); err != nil {
			return "", &ErrSchemaDecode{Field: "milestone.id", Expected: "decimal integer string", Received: fmt.Sprintf("%q", v)}
		}
		// reject "0" and negative
		n, _ := strconv.Atoi(v)
		if n == 0 {
			return "", &ErrSchemaDecode{Field: "milestone.id", Expected: "positive integer", Received: fmt.Sprintf("%q", v)}
		}
		return v, nil
	case float64:
		if v == 0 {
			return "", &ErrSchemaDecode{Field: "milestone.id", Expected: "non-empty positive integer", Received: "zero"}
		}
		if v != float64(int64(v)) {
			return "", &ErrSchemaDecode{Field: "milestone.id", Expected: "integer value", Received: fmt.Sprintf("fractional %.1f", v)}
		}
		if v < 0 {
			return "", &ErrSchemaDecode{Field: "milestone.id", Expected: "positive integer", Received: fmt.Sprintf("%.0f", v)}
		}
		return strconv.FormatInt(int64(v), 10), nil
	case nil:
		return "", &ErrSchemaDecode{Field: "milestone.id", Expected: "non-empty positive integer", Received: "nil"}
	default:
		return "", &ErrSchemaDecode{Field: "milestone.id", Expected: "number or string", Received: fmt.Sprintf("type %T", v)}
	}
}

func decodeMilestoneStatus(state, status string) (string, error) {
	val := strings.ToLower(strings.TrimSpace(state))
	if val == "" {
		val = strings.ToLower(strings.TrimSpace(status))
	}
	switch val {
	case "open", "active":
		return "open", nil
	case "closed":
		return "closed", nil
	case "":
		return "open", nil
	default:
		field := "milestone.state"
		if strings.TrimSpace(state) == "" {
			field = "milestone.status"
		}
		return "", &ErrSchemaDecode{
			Field:    field,
			Expected: "open, active, closed, or absent",
			Received: fmt.Sprintf("%q", val),
		}
	}
}

func decodeMilestoneDate(dueOn, dueDate string) (string, error) {
	val := strings.TrimSpace(dueOn)
	if val == "" {
		val = strings.TrimSpace(dueDate)
	}
	if val == "" {
		return "", nil
	}
	if t, err := time.Parse(time.RFC3339, val); err == nil {
		return t.Format("2006-01-02"), nil
	}
	if t, err := time.Parse("2006-01-02", val); err == nil {
		return t.Format("2006-01-02"), nil
	}
	return "", &ErrSchemaDecode{Field: "milestone.due_on", Expected: "YYYY-MM-DD or RFC3339", Received: fmt.Sprintf("%q", val)}
}

func isValidTimestamp(val string) bool {
	_, err := time.Parse(time.RFC3339, val)
	return err == nil
}

func milestoneListQuery(req MilestoneListRequest) url.Values {
	values := url.Values{}
	if req.State != "" {
		values.Set("state", req.State)
	}
	return values
}

func prListQuery(req PRListRequest) url.Values {
	values := url.Values{}
	if req.State != "" {
		values.Set("state", req.State)
	}
	if req.OrderBy != "" {
		values.Set("order_by", req.OrderBy)
	}
	if req.Direction != "" {
		values.Set("direction", req.Direction)
	}
	return values
}
