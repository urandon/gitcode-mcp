package gitcode

import "time"

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
	Status    string    `json:"status"`
	State     string    `json:"state"`
	Labels    []string  `json:"labels"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
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

type CreateWikiPageRequest struct {
	Owner string `json:"-"`
	Repo  string `json:"-"`
	Slug  string `json:"slug,omitempty"`
	Title string `json:"title"`
	Body  string `json:"body"`
}

type UpdateWikiPageRequest struct {
	Owner string `json:"-"`
	Repo  string `json:"-"`
	Slug  string `json:"-"`
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
}

type LabelRequest struct {
	Owner  string `json:"-"`
	Repo   string `json:"-"`
	Number int    `json:"-"`
	Label  string `json:"label"`
}
