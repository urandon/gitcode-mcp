package live

import (
	"time"

	"gitcode-mcp/internal/gitcode"
)

type Client = gitcode.Client
type Provider = gitcode.Provider
type Config = gitcode.ProviderConfig
type ErrProviderUnavailable = gitcode.ErrProviderUnavailable

type (
	Page                      = gitcode.Page[any]
	IssueSummary              = gitcode.IssueSummary
	Issue                     = gitcode.Issue
	Comment                   = gitcode.Comment
	WikiPage                  = gitcode.WikiPage
	SearchResult              = gitcode.SearchResult
	WriteResult               = gitcode.WriteResult[any]
	WriteOptions              = gitcode.WriteOptions
	IssueListRequest          = gitcode.IssueListRequest
	IssueRequest              = gitcode.IssueRequest
	WikiListRequest           = gitcode.WikiListRequest
	WikiPageRequest           = gitcode.WikiPageRequest
	CreateIssueRequest        = gitcode.CreateIssueRequest
	UpdateIssueRequest        = gitcode.UpdateIssueRequest
	CreateIssueCommentRequest = gitcode.CreateIssueCommentRequest
	CreateWikiPageRequest     = gitcode.CreateWikiPageRequest
	UpdateWikiPageRequest     = gitcode.UpdateWikiPageRequest
	SearchRequest             = gitcode.SearchRequest
	RepoRequest               = gitcode.RepoRequest
	Repo                      = gitcode.Repo
	AuthProbeRequest          = gitcode.AuthProbeRequest
	AuthProbeResult           = gitcode.AuthProbeResult
	RedactedCapture           = gitcode.RedactedCapture
	RouteSchemaMatrix         = gitcode.RouteSchemaMatrix
	SurfaceSpec               = gitcode.SurfaceSpec
	UnsupportedDiagnostic     = gitcode.UnsupportedDiagnostic
	ProductArea               = gitcode.ProductArea
	SupportStatus             = gitcode.SupportStatus
	EvidenceClass             = gitcode.EvidenceClass
	ErrUnsupportedCapability  = gitcode.ErrUnsupportedCapability
)

func IsProviderUnavailable(err error) bool { return gitcode.IsProviderUnavailable(err) }

func IsUnsupportedCapability(err error) bool { return gitcode.IsUnsupportedCapability(err) }

func WithRouteSchemaMatrix(matrix RouteSchemaMatrix) gitcode.LiveProviderOption {
	return gitcode.WithRouteSchemaMatrix(matrix)
}

type LiveProviderOption = gitcode.LiveProviderOption

func NewLiveProvider(cfg Config) (Provider, error) {
	return gitcode.NewLiveProvider(cfg)
}

func NewHTTPClient(cfg HTTPClientConfig) (*gitcode.HTTPClient, error) {
	return gitcode.NewHTTPClient(gitcode.Config{
		BaseURL:         cfg.BaseURL,
		Token:           cfg.Token,
		Timeout:         cfg.Timeout,
		MaxResponseSize: cfg.MaxResponseSize,
		MaxRetries:      cfg.MaxRetries,
		UserAgent:       cfg.UserAgent,
		Pagination:      cfg.Pagination,
	})
}

type HTTPClientConfig struct {
	BaseURL         string
	Token           string
	Timeout         time.Duration
	MaxResponseSize int64
	MaxRetries      int
	UserAgent       string
	Pagination      gitcode.PaginationConfig
}
