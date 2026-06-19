package gitcode

import (
	"context"
	"time"
)

type Client interface {
	ListIssues(context.Context, IssueListRequest) (Page[IssueSummary], error)
	GetIssue(context.Context, IssueRequest) (Issue, error)
	ListIssueComments(context.Context, IssueRequest) (Page[Comment], error)
	GetWikiPage(context.Context, WikiPageRequest) (WikiPage, error)
	ListWikiPages(context.Context, WikiListRequest) (Page[WikiPage], error)
	Search(context.Context, SearchRequest) (Page[SearchResult], error)
	ListIssueAttachments(context.Context, IssueRequest) (Page[AttachmentSummary], error)
	GetAttachment(context.Context, AttachmentRequest) (AttachmentBody, error)
}

type Config struct {
	BaseURL         string
	Token           string
	Timeout         time.Duration
	MaxResponseSize int64
	MaxRetries      int
	UserAgent       string
	Pagination      PaginationConfig
}

type PaginationConfig struct {
	Page     int
	PerPage  int
	Strategy PaginationStrategy
}
