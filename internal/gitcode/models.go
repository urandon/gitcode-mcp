package gitcode

import (
	"encoding/json"
	"strconv"
	"time"
)

type IssueListRequest struct {
	Owner   string
	Repo    string
	State   string
	Labels  []string
	Page    int
	PerPage int
}

type IssueRequest struct {
	Owner            string
	Repo             string
	Number           int
	KnownRemoteAlias bool
	RemoteAlias      string
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

type IssueSummary struct {
	ID        string    `json:"id"`
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Status    string    `json:"status"`
	State     string    `json:"state"`
	Labels    []string  `json:"labels"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (i *IssueSummary) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID        any       `json:"id"`
		Number    any       `json:"number"`
		Title     string    `json:"title"`
		Body      string    `json:"body"`
		Status    string    `json:"status"`
		State     string    `json:"state"`
		Labels    []string  `json:"labels"`
		CreatedAt time.Time `json:"created_at"`
		UpdatedAt time.Time `json:"updated_at"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	i.ID = jsonScalarString(raw.ID)
	i.Number = jsonScalarInt(raw.Number)
	i.Title = raw.Title
	i.Body = raw.Body
	i.Status = raw.Status
	i.State = raw.State
	i.Labels = raw.Labels
	i.CreatedAt = raw.CreatedAt
	i.UpdatedAt = raw.UpdatedAt
	return nil
}

type Issue struct {
	ID        string    `json:"id"`
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Status    string    `json:"status"`
	State     string    `json:"state"`
	Labels    []string  `json:"labels"`
	Author    string    `json:"author"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (i *Issue) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID        any       `json:"id"`
		Number    any       `json:"number"`
		Title     string    `json:"title"`
		Body      string    `json:"body"`
		Status    string    `json:"status"`
		State     string    `json:"state"`
		Labels    []string  `json:"labels"`
		Author    string    `json:"author"`
		CreatedAt time.Time `json:"created_at"`
		UpdatedAt time.Time `json:"updated_at"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	i.ID = jsonScalarString(raw.ID)
	i.Number = jsonScalarInt(raw.Number)
	i.Title = raw.Title
	i.Body = raw.Body
	i.Status = raw.Status
	i.State = raw.State
	i.Labels = raw.Labels
	i.Author = raw.Author
	i.CreatedAt = raw.CreatedAt
	i.UpdatedAt = raw.UpdatedAt
	return nil
}

func jsonScalarString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case nil:
		return ""
	default:
		return ""
	}
}

func jsonScalarInt(value any) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case string:
		n, _ := strconv.Atoi(v)
		return n
	default:
		return 0
	}
}

type Comment struct {
	ID        string    `json:"id"`
	IssueID   string    `json:"issue_id"`
	Body      string    `json:"body"`
	Author    string    `json:"author"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
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
	Owner  string   `json:"-"`
	Repo   string   `json:"-"`
	Title  string   `json:"title"`
	Body   string   `json:"body,omitempty"`
	Labels []string `json:"labels,omitempty"`
}

type UpdateIssueRequest struct {
	Owner  string   `json:"-"`
	Repo   string   `json:"-"`
	Number int      `json:"-"`
	Title  string   `json:"title,omitempty"`
	Body   string   `json:"body,omitempty"`
	State  string   `json:"state,omitempty"`
	Labels []string `json:"labels,omitempty"`
}

type CreateIssueCommentRequest struct {
	Owner  string `json:"-"`
	Repo   string `json:"-"`
	Number int    `json:"-"`
	Body   string `json:"body"`
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
