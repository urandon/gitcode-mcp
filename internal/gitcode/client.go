package gitcode

import (
	"context"
	"time"
)

type Client interface {
	ListIssues(context.Context, IssueListRequest) (Page[IssueSummary], error)
	GetIssue(context.Context, IssueRequest) (Issue, error)
	ListIssueComments(context.Context, IssueRequest) (Page[Comment], error)
	ListPRs(context.Context, PRListRequest) (Page[PullRequest], error)
	GetPR(context.Context, PRRequest) (PullRequest, error)
	ListPRComments(context.Context, PRRequest) (Page[PRComment], error)
	CreatePR(context.Context, CreatePRRequest, WriteOptions) (WriteResult[PullRequest], error)
	UpdatePR(context.Context, UpdatePRRequest, WriteOptions) (WriteResult[PullRequest], error)
	LinkPRIssue(context.Context, LinkPRIssueRequest, WriteOptions) (WriteResult[[]Issue], error)
	GetWikiPage(context.Context, WikiPageRequest) (WikiPage, error)
	ListWikiPages(context.Context, WikiListRequest) (Page[WikiPage], error)
	Search(context.Context, SearchRequest) (Page[SearchResult], error)
	ListIssueAttachments(context.Context, IssueRequest) (Page[AttachmentSummary], error)
	GetAttachment(context.Context, AttachmentRequest) (AttachmentBody, error)
	CreateIssue(context.Context, CreateIssueRequest, WriteOptions) (WriteResult[Issue], error)
	UpdateIssue(context.Context, UpdateIssueRequest, WriteOptions) (WriteResult[Issue], error)
	CreateIssueComment(context.Context, CreateIssueCommentRequest, WriteOptions) (WriteResult[Comment], error)
	UpdateIssueComment(context.Context, UpdateIssueCommentRequest, WriteOptions) (WriteResult[Comment], error)
	CreatePRComment(context.Context, CreatePRCommentRequest, WriteOptions) (WriteResult[PRComment], error)
	CreateWikiPage(context.Context, CreateWikiPageRequest, WriteOptions) (WriteResult[WikiPage], error)
	UpdateWikiPage(context.Context, UpdateWikiPageRequest, WriteOptions) (WriteResult[WikiPage], error)
	DeleteWikiPage(context.Context, DeleteWikiPageRequest, WriteOptions) (WriteResult[WikiPage], error)
	AddLabel(context.Context, LabelRequest, WriteOptions) (WriteResult[Issue], error)
	RemoveLabel(context.Context, LabelRequest, WriteOptions) (WriteResult[Issue], error)
	ListMilestones(context.Context, MilestoneListRequest) (Page[Milestone], error)
	GetMilestone(context.Context, MilestoneRequest) (Milestone, error)
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
