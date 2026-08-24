package service

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"gitcode-mcp/internal/feedback"
	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/index"
)

type ServiceConfig struct {
	BaseURL         string
	LockPath        string
	Timeout         time.Duration
	MaxResponseSize int64
	MaxRetries      int
	UserAgent       string
	Pagination      gitcode.PaginationConfig
	RateLimitRPS    float64
	RateLimitBurst  int
	Feedback        feedback.Config
}

type RepositoryScope string

const (
	RepositoryScopeIssues RepositoryScope = "issues"
	RepositoryScopeWiki   RepositoryScope = "wiki"
)

type RepositoryBinding struct {
	RepoID      string            `json:"repo_id"`
	Owner       string            `json:"owner"`
	Name        string            `json:"name"`
	APIBaseURL  string            `json:"api_base_url"`
	Scopes      []RepositoryScope `json:"scopes"`
	DisplayName string            `json:"display_name,omitempty"`
	Aliases     []string          `json:"aliases"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

type AddRepositoryRequest struct {
	RepoID      string   `json:"repo_id"`
	Owner       string   `json:"owner"`
	Name        string   `json:"name"`
	APIBaseURL  string   `json:"api_base_url"`
	Scopes      []string `json:"scopes"`
	DisplayName string   `json:"display_name,omitempty"`
	Aliases     []string `json:"aliases"`
}

type RepositoryStatusRequest struct {
	RepoID string `json:"repo_id"`
}

type RepositoryStatus struct {
	RepoID                     string                    `json:"repo_id"`
	SuggestedRepoID            string                    `json:"suggested_repo_id,omitempty"`
	AvailableBindings          []string                  `json:"available_bindings,omitempty"`
	Owner                      string                    `json:"owner"`
	Name                       string                    `json:"name"`
	APIBaseURL                 string                    `json:"api_base_url"`
	Scopes                     []RepositoryScope         `json:"scopes"`
	DisplayName                string                    `json:"display_name,omitempty"`
	Aliases                    []string                  `json:"aliases"`
	BindingState               string                    `json:"binding_state"`
	AliasConflictState         string                    `json:"alias_conflict_state"`
	CacheState                 string                    `json:"cache_state"`
	IndexState                 string                    `json:"index_state"`
	FailureClass               string                    `json:"failure_class,omitempty"`
	BinaryVersion              string                    `json:"binary_version"`
	BinaryCommit               string                    `json:"binary_commit,omitempty"`
	BinaryBuildDate            string                    `json:"binary_build_date,omitempty"`
	BinaryVersionSource        string                    `json:"binary_version_source"`
	CacheSchemaVersion         int                       `json:"cache_schema_version"`
	ExpectedCacheSchemaVersion int                       `json:"expected_cache_schema_version"`
	IssueRecords               int                       `json:"issue_records"`
	IssueComments              int                       `json:"issue_comments"`
	IssueCommentQueueState     string                    `json:"issue_comment_queue_state"`
	IssueCommentQueue          *IssueCommentQueueSummary `json:"issue_comment_queue,omitempty"`
}

type CacheStatusRequest struct {
	RepoID string `json:"repo_id"`
}

type ResetLiveCacheRequest struct {
	RepoID string `json:"repo_id"`
}

type ResetLiveCacheResult struct {
	RepoID string `json:"repo_id"`
	Reset  string `json:"reset"`
}

type CacheStatusResult struct {
	RepoID                  string         `json:"repo_id"`
	WALCapable              bool           `json:"wal_capable"`
	JournalMode             string         `json:"journal_mode"`
	Records                 int            `json:"records"`
	Comments                int            `json:"comments"`
	IdentityAliases         int            `json:"identity_aliases"`
	SyncEvents              int            `json:"sync_events"`
	AuditRows               int            `json:"audit_rows"`
	Snapshots               int            `json:"snapshots"`
	SnapshotChunks          int            `json:"snapshot_chunks"`
	Chunks                  int            `json:"chunks"`
	RemoteRevisions         int            `json:"remote_revisions"`
	RAGNamespaces           int            `json:"rag_namespaces"`
	RAGEmbeddings           int            `json:"rag_embeddings"`
	RAGIndexRuns            int            `json:"rag_index_runs"`
	IndexFreshnessWarnings  int            `json:"index_freshness_warnings"`
	IndexFreshnessByWarning map[string]int `json:"index_freshness_by_warning,omitempty"`
}

type RepositoryRoute struct {
	RepoID     string            `json:"repo_id"`
	Owner      string            `json:"owner"`
	Name       string            `json:"name"`
	APIBaseURL string            `json:"api_base_url"`
	Scopes     []RepositoryScope `json:"scopes"`
}

type LiveRepositoryBinding struct {
	RepoID        string            `json:"repo_id"`
	Owner         string            `json:"owner"`
	Name          string            `json:"name"`
	APIBaseURL    string            `json:"api_base_url"`
	Scopes        []RepositoryScope `json:"scopes"`
	CachePath     string            `json:"cache_path"`
	AuditPath     string            `json:"audit_path"`
	BaseURLSource string            `json:"base_url_source"`
}

type LiveRepositoryBindingRequest struct {
	RepoID             string          `json:"repo_id"`
	RequestedScope     RepositoryScope `json:"requested_scope"`
	CachePath          string          `json:"cache_path"`
	AuditPath          string          `json:"audit_path"`
	FallbackAPIBaseURL string          `json:"fallback_api_base_url,omitempty"`
}

type SearchSourcesRequest struct {
	RepoID     string `json:"repo_id"`
	Query      string `json:"query"`
	Mode       string `json:"mode,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Provenance string `json:"provenance,omitempty"`
	Limit      int    `json:"limit,omitempty"`
	Offset     int    `json:"offset,omitempty"`
}

type SearchSourcesResult struct {
	RepoID         string               `json:"repo_id"`
	Query          string               `json:"query"`
	SearchMode     string               `json:"search_mode"`
	RequestedMode  string               `json:"requested_mode"`
	EffectiveMode  string               `json:"effective_mode"`
	RAGState       string               `json:"rag_state"`
	FallbackReason string               `json:"fallback_reason,omitempty"`
	Coverage       SearchRAGCoverage    `json:"coverage"`
	Repair         SearchRepairStatus   `json:"repair"`
	Results        []SearchSourceResult `json:"results"`
	Limit          int                  `json:"limit"`
	Offset         int                  `json:"offset"`
}

type SearchSourceResult struct {
	RepoID     string           `json:"repo_id"`
	ID         string           `json:"id"`
	Path       string           `json:"path"`
	Title      string           `json:"title"`
	Kind       string           `json:"kind"`
	Status     string           `json:"status"`
	Provenance string           `json:"provenance"`
	Snippet    string           `json:"snippet"`
	LineStart  *int             `json:"line_start"`
	LineEnd    *int             `json:"line_end"`
	Score      float64          `json:"score"`
	Rank       int              `json:"rank"`
	Match      SearchMatch      `json:"match"`
	Citations  []SearchCitation `json:"citations"`
}

type SearchMatch struct {
	LexicalRank  int     `json:"lexical_rank,omitempty"`
	SemanticRank int     `json:"semantic_rank,omitempty"`
	ExactMatch   bool    `json:"exact_match"`
	FusionScore  float64 `json:"fusion_score"`
}

type SearchCitation struct {
	ChunkID   string `json:"chunk_id"`
	LineStart int    `json:"line_start,omitempty"`
	LineEnd   int    `json:"line_end,omitempty"`
	Snippet   string `json:"snippet"`
}

type SearchRAGCoverage struct {
	EligibleChunks int     `json:"eligible_chunks"`
	EmbeddedChunks int     `json:"embedded_chunks"`
	MissingChunks  int     `json:"missing_chunks"`
	StaleChunks    int     `json:"stale_chunks"`
	Ratio          float64 `json:"ratio"`
	NamespaceID    string  `json:"namespace_id,omitempty"`
}

type SearchRepairStatus struct {
	State string `json:"state"`
}

type GetSourceRequest struct {
	RepoID    string `json:"repo_id"`
	ID        string `json:"id,omitempty"`
	AliasType string `json:"alias_type,omitempty"`
	AliasID   string `json:"alias_id,omitempty"`
}

type SourceRecord struct {
	RepoID         string           `json:"repo_id"`
	ID             string           `json:"id"`
	StableSourceID string           `json:"stable_source_id"`
	IssueNumber    int              `json:"issue_number,omitempty"`
	Path           string           `json:"path"`
	RemoteAlias    string           `json:"remote_alias"`
	Kind           string           `json:"kind"`
	Title          string           `json:"title"`
	Body           string           `json:"body"`
	Status         string           `json:"status"`
	Provenance     string           `json:"provenance"`
	Labels         []string         `json:"labels"`
	Links          []LinkResult     `json:"links"`
	Backlinks      []BacklinkResult `json:"backlinks,omitempty"`
	UpdatedAt      time.Time        `json:"updated_at"`
}

type ListSourcesRequest struct {
	RepoID     string `json:"repo_id"`
	Kind       string `json:"kind,omitempty"`
	Status     string `json:"status,omitempty"`
	Provenance string `json:"provenance,omitempty"`
	Limit      int    `json:"limit,omitempty"`
	Offset     int    `json:"offset,omitempty"`
}

func (r ListSourcesRequest) limitPlusOffset() int {
	if r.Limit <= 0 {
		return 0
	}
	return r.Limit + r.Offset
}

type ListSourcesResult struct {
	RepoID  string          `json:"repo_id"`
	Results []SourceSummary `json:"results"`
	Limit   int             `json:"limit"`
	Offset  int             `json:"offset"`
}

type SourceSummary struct {
	RepoID         string    `json:"repo_id"`
	ID             string    `json:"id"`
	StableSourceID string    `json:"stable_source_id"`
	IssueNumber    int       `json:"issue_number,omitempty"`
	Path           string    `json:"path"`
	RemoteAlias    string    `json:"remote_alias,omitempty"`
	Kind           string    `json:"kind"`
	Title          string    `json:"title"`
	Status         string    `json:"status"`
	Provenance     string    `json:"provenance"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type GetBacklinksRequest struct {
	RepoID    string `json:"repo_id"`
	ID        string `json:"id,omitempty"`
	AliasType string `json:"alias_type,omitempty"`
	AliasID   string `json:"alias_id,omitempty"`
	Limit     int    `json:"limit,omitempty"`
	Offset    int    `json:"offset,omitempty"`
}

type BacklinksResult struct {
	RepoID    string           `json:"repo_id"`
	ID        string           `json:"id"`
	Backlinks []BacklinkResult `json:"backlinks"`
	Limit     int              `json:"limit"`
	Offset    int              `json:"offset"`
}

type BacklinkResult struct {
	SourceSummary
	TargetID string `json:"target_id"`
}

type ResolveIDRequest struct {
	RepoID    string `json:"repo_id"`
	ID        string `json:"id,omitempty"`
	AliasType string `json:"alias_type,omitempty"`
	AliasID   string `json:"alias_id,omitempty"`
}

type ResolvedID struct {
	RepoID         string `json:"repo_id"`
	ID             string `json:"id"`
	StableSourceID string `json:"stable_source_id"`
	IssueNumber    int    `json:"issue_number,omitempty"`
	Path           string `json:"path"`
	RemoteAlias    string `json:"remote_alias"`
	Kind           string `json:"kind"`
	Title          string `json:"title"`
}

type ChunkPolicy = index.ChunkPolicy

type ChunkQuery = index.ChunkQuery

type ChunkSearchQuery = index.ChunkSearchQuery

type SnippetQuery = index.SnippetQuery

type ChunkQueryResult = index.ChunkQueryResult

type ChunkResult = index.ChunkResult

type IndexWarning = index.IndexWarning

type SnippetRequest struct {
	RepoID    string `json:"repo_id"`
	ID        string `json:"id,omitempty"`
	AliasType string `json:"alias_type,omitempty"`
	AliasID   string `json:"alias_id,omitempty"`
	LineStart int    `json:"line_start,omitempty"`
	LineEnd   int    `json:"line_end,omitempty"`
	ChunkID   string `json:"chunk_id,omitempty"`
}

type SnippetResult struct {
	RepoID    string   `json:"repo_id"`
	ID        string   `json:"id"`
	Path      string   `json:"path"`
	Text      string   `json:"text"`
	LineStart int      `json:"line_start"`
	LineEnd   int      `json:"line_end"`
	ChunkID   string   `json:"chunk_id,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
}

type SyncStatusRequest struct {
	RepoID    string `json:"repo_id"`
	ID        string `json:"id,omitempty"`
	AliasType string `json:"alias_type,omitempty"`
	AliasID   string `json:"alias_id,omitempty"`
}

type SyncStatusResult struct {
	RepoID         string                      `json:"repo_id"`
	SourceID       string                      `json:"source_id"`
	RemoteType     string                      `json:"remote_type"`
	RemoteID       string                      `json:"remote_id"`
	RemoteRevision string                      `json:"remote_revision"`
	Status         string                      `json:"status"`
	Freshness      string                      `json:"freshness"`
	Provenance     string                      `json:"provenance"`
	LocalUpdatedAt time.Time                   `json:"local_updated_at"`
	LastFetchedAt  time.Time                   `json:"last_fetched_at"`
	IssueComments  *IssueCommentCoverageStatus `json:"issue_comments,omitempty"`
}

type IssueCommentCoverageStatus struct {
	Status         string    `json:"status"`
	RemoteRevision string    `json:"remote_revision"`
	ExpectedCount  int       `json:"expected_count"`
	Attempts       int       `json:"attempts"`
	LastErrorClass string    `json:"last_error_class,omitempty"`
	RetryAfter     string    `json:"retry_after,omitempty"`
	LastAttemptAt  time.Time `json:"last_attempt_at,omitempty"`
	QueueUpdatedAt time.Time `json:"queue_updated_at"`
}

type SyncStatusSummaryResult struct {
	RepoID              string                    `json:"repo_id"`
	Results             []SyncStatusResult        `json:"results"`
	FreshCount          int                       `json:"fresh_count"`
	StaleCount          int                       `json:"stale_count"`
	LastSyncAt          time.Time                 `json:"last_sync_at"`
	LastSyncStartedAt   time.Time                 `json:"last_sync_started_at"`
	LastSyncCompletedAt time.Time                 `json:"last_sync_completed_at"`
	ZeroDelta           bool                      `json:"zero_delta"`
	CacheEmpty          bool                      `json:"cache_empty"`
	Limit               int                       `json:"limit"`
	Offset              int                       `json:"offset"`
	IssueComments       *IssueCommentQueueSummary `json:"issue_comments,omitempty"`
	Warnings            []string                  `json:"warnings,omitempty"`
}

type FreshnessState = string

const (
	FreshnessFresh         FreshnessState = "fresh"
	FreshnessStale         FreshnessState = "stale"
	FreshnessMissingRemote FreshnessState = "missing_remote"
	FreshnessUnknown       FreshnessState = "unknown"
)

type SyncRequest struct {
	RepoID         string `json:"repo_id"`
	Source         string `json:"source,omitempty"`
	TrackerID      string `json:"tracker_id,omitempty"`
	StableID       string `json:"stable_id,omitempty"`
	RemoteAlias    string `json:"remote_alias,omitempty"`
	AliasType      string `json:"alias_type,omitempty"`
	AliasID        string `json:"alias_id,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	MaxAttempts    int    `json:"max_attempts,omitempty"`
	BackoffBase    string `json:"backoff_base,omitempty"`
	BackoffMax     string `json:"backoff_max,omitempty"`
	Timeout        string `json:"timeout,omitempty"`
	MaxSize        int64  `json:"max_size,omitempty"`
	ParentSourceID string `json:"-"`
}

type SyncCounts struct {
	Fetched           int `json:"fetched"`
	Skipped           int `json:"skipped"`
	Updated           int `json:"updated"`
	Conflicts         int `json:"conflicts"`
	Inserted          int `json:"inserted"`
	Listed            int `json:"listed,omitempty"`
	FetchedDetail     int `json:"fetched_detail,omitempty"`
	SkippedByRevision int `json:"skipped_by_revision,omitempty"`
	Deferred          int `json:"deferred,omitempty"`
	Failed            int `json:"failed,omitempty"`
}

type SyncResult struct {
	IdempotencyKey string        `json:"idempotency_key"`
	Status         string        `json:"status"`
	Counts         SyncCounts    `json:"counts"`
	Replayed       bool          `json:"replayed"`
	SyncEventID    string        `json:"sync_event_id"`
	Freshness      string        `json:"freshness"`
	Record         SourceSummary `json:"record"`
	GeneratedAt    time.Time     `json:"generated_at"`
	StartedAt      time.Time     `json:"started_at"`
	CompletedAt    time.Time     `json:"completed_at"`
	ZeroDelta      bool          `json:"zero_delta"`
}

type SyncResourcesResult struct {
	Results            []SyncResult              `json:"results"`
	SuccessCount       int                       `json:"success_count"`
	FailureCount       int                       `json:"failure_count"`
	Failures           []ResourceError           `json:"failures,omitempty"`
	PagesListed        int                       `json:"pages_listed,omitempty"`
	RecordsListed      int                       `json:"records_listed,omitempty"`
	SkippedByWatermark int                       `json:"skipped_by_watermark,omitempty"`
	StopReason         string                    `json:"stop_reason,omitempty"`
	Ordering           string                    `json:"ordering,omitempty"`
	TraversalStatus    string                    `json:"traversal_status,omitempty"`
	WatermarkStatus    string                    `json:"watermark_status,omitempty"`
	WatermarkReason    string                    `json:"watermark_reason,omitempty"`
	IssueComments      *IssueCommentQueueSummary `json:"issue_comments,omitempty"`
}

type IssueCommentQueueSummary struct {
	Phase                 string `json:"phase"`
	Strategy              string `json:"strategy,omitempty"`
	FallbackReason        string `json:"fallback_reason,omitempty"`
	Pending               int    `json:"pending"`
	Deferred              int    `json:"deferred"`
	Complete              int    `json:"complete"`
	Total                 int    `json:"total"`
	Attempted             int    `json:"attempted,omitempty"`
	Drained               int    `json:"drained,omitempty"`
	AggregateRequests     int    `json:"aggregate_requests,omitempty"`
	CommentsListed        int    `json:"comments_listed,omitempty"`
	ParentRequestsAvoided int    `json:"parent_requests_avoided,omitempty"`
	Unreconciled          int    `json:"unreconciled,omitempty"`
}

type ResourceError struct {
	SourceID       string `json:"source_id"`
	RemoteType     string `json:"remote_type"`
	FailureClass   string `json:"failure_class,omitempty"`
	Endpoint       string `json:"endpoint,omitempty"`
	StatusCode     int    `json:"status_code,omitempty"`
	ResponseBytes  int64  `json:"response_bytes,omitempty"`
	ContentType    string `json:"content_type,omitempty"`
	DecodeOffset   int64  `json:"decode_offset,omitempty"`
	Attempts       int    `json:"attempts,omitempty"`
	RecoveryAction string `json:"recovery_action,omitempty"`
	Err            error  `json:"-"`
	Message        string `json:"message"`
}

func (e ResourceError) Error() string {
	return e.Message
}

func (e ResourceError) Unwrap() error { return e.Err }

func newResourceError(sourceID, remoteType string, err error) ResourceError {
	return newResourceErrorWithMessage(sourceID, remoteType, err, "")
}

func newResourceErrorWithMessage(sourceID, remoteType string, err error, message string) ResourceError {
	if message == "" && err != nil {
		message = err.Error()
	}
	re := ResourceError{SourceID: sourceID, RemoteType: remoteType, Err: err, Message: message}
	re.FailureClass, re.Endpoint, re.StatusCode, re.RecoveryAction = resourceFailureMetadata(err)
	re.ResponseBytes, re.ContentType, re.DecodeOffset, re.Attempts = resourceResponseMetadata(err)
	return re
}

func resourceResponseMetadata(err error) (responseBytes int64, contentType string, decodeOffset int64, attempts int) {
	var partial gitcode.ErrPartialResponse
	if errors.As(err, &partial) {
		return partial.Got, partial.ContentType, partial.Offset, partial.Attempts
	}
	var malformed gitcode.ErrMalformedJSON
	if errors.As(err, &malformed) {
		return malformed.ResponseSize, malformed.ContentType, malformed.Offset, malformed.Attempts
	}
	var unexpected gitcode.ErrUnexpectedContentType
	if errors.As(err, &unexpected) {
		return unexpected.ResponseSize, unexpected.ContentType, 0, unexpected.Attempts
	}
	return 0, "", 0, 0
}

func resourceFailureMetadata(err error) (failureClass, endpoint string, statusCode int, recoveryAction string) {
	if err == nil {
		return "", "", 0, ""
	}
	var syncFailure ErrSyncFailure
	if errors.As(err, &syncFailure) {
		failureClass = syncFailure.Mode
		endpoint = syncFailure.Endpoint
		statusCode = resourceStatusCode(syncFailure.Cause)
		recoveryAction = syncFailure.RecoveryAction
		if endpoint == "" {
			endpoint = resourceEndpoint(syncFailure.Cause)
		}
		if statusCode == 0 {
			statusCode = resourceStatusCode(err)
		}
		return failureClass, endpoint, statusCode, recoveryAction
	}
	if coded, ok := err.(interface{ DiagnosticCode() string }); ok {
		failureClass = coded.DiagnosticCode()
	}
	endpoint = resourceEndpoint(err)
	statusCode = resourceStatusCode(err)
	var forbidden gitcode.ErrForbidden
	if errors.As(err, &forbidden) {
		recoveryAction = forbidden.Recovery
	}
	return failureClass, endpoint, statusCode, recoveryAction
}

func resourceEndpoint(err error) string {
	var network gitcode.ErrNetworkUnavailable
	if errors.As(err, &network) {
		return network.Endpoint
	}
	var rate gitcode.ErrRateLimited
	if errors.As(err, &rate) {
		return rate.Endpoint
	}
	var auth gitcode.ErrAuthExpired
	if errors.As(err, &auth) {
		return auth.Endpoint
	}
	var forbidden gitcode.ErrForbidden
	if errors.As(err, &forbidden) {
		return forbidden.Endpoint
	}
	var notFound gitcode.ErrNotFound
	if errors.As(err, &notFound) {
		return notFound.Endpoint
	}
	var remoteNotFound gitcode.ErrRemoteNotFound
	if errors.As(err, &remoteNotFound) {
		return remoteNotFound.Endpoint
	}
	var validation gitcode.ErrAPIValidation
	if errors.As(err, &validation) {
		return validation.Endpoint
	}
	var conflict gitcode.ErrConflict
	if errors.As(err, &conflict) {
		return conflict.Endpoint
	}
	var partial gitcode.ErrPartialResponse
	if errors.As(err, &partial) {
		return partial.Endpoint
	}
	var malformed gitcode.ErrMalformedJSON
	if errors.As(err, &malformed) {
		return malformed.Endpoint
	}
	var unexpected gitcode.ErrUnexpectedContentType
	if errors.As(err, &unexpected) {
		return unexpected.Endpoint
	}
	var schema *gitcode.ErrSchemaDecode
	if errors.As(err, &schema) {
		return schema.Endpoint
	}
	var tooLarge gitcode.ErrPayloadTooLarge
	if errors.As(err, &tooLarge) {
		return tooLarge.Endpoint
	}
	return ""
}

func resourceStatusCode(err error) int {
	var network gitcode.ErrNetworkUnavailable
	if errors.As(err, &network) && network.Status != 0 {
		return network.Status
	}
	var rate gitcode.ErrRateLimited
	if errors.As(err, &rate) {
		return http.StatusTooManyRequests
	}
	var auth gitcode.ErrAuthExpired
	if errors.As(err, &auth) {
		return auth.Status
	}
	var forbidden gitcode.ErrForbidden
	if errors.As(err, &forbidden) {
		return forbidden.Status
	}
	var notFound gitcode.ErrNotFound
	if errors.As(err, &notFound) {
		return http.StatusNotFound
	}
	var remoteNotFound gitcode.ErrRemoteNotFound
	if errors.As(err, &remoteNotFound) {
		return http.StatusNotFound
	}
	var validation gitcode.ErrAPIValidation
	if errors.As(err, &validation) {
		return validation.Status
	}
	var conflict gitcode.ErrConflict
	if errors.As(err, &conflict) {
		return conflict.Status
	}
	var tooLarge gitcode.ErrPayloadTooLarge
	if errors.As(err, &tooLarge) {
		return http.StatusRequestEntityTooLarge
	}
	return 0
}

type PartialSyncError struct {
	Errors         []ResourceError `json:"errors"`
	SuccessCount   int             `json:"success_count"`
	FailureCount   int             `json:"failure_count"`
	Diagnostic     SyncDiagnostic  `json:"diagnostic,omitempty"`
	TotalRequested int             `json:"total_requested,omitempty"`
}

func (e PartialSyncError) Error() string {
	return fmt.Sprintf("sync: %d succeeded, %d failed", e.SuccessCount, e.FailureCount)
}

func (e PartialSyncError) Unwrap() []error {
	out := make([]error, 0, len(e.Errors))
	for _, resourceErr := range e.Errors {
		if resourceErr.Err != nil {
			out = append(out, resourceErr.Err)
		}
	}
	return out
}

type RecentChangesRequest struct {
	RepoID string `json:"repo_id"`
	Kind   string `json:"kind,omitempty"`
	Status string `json:"status,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	Offset int    `json:"offset,omitempty"`
}

type RecentChangesResult struct {
	RepoID  string               `json:"repo_id"`
	Results []RecentChangeResult `json:"results"`
	Limit   int                  `json:"limit"`
	Offset  int                  `json:"offset"`
}

type RecentChangeResult struct {
	RepoID         string    `json:"repo_id"`
	ID             string    `json:"id"`
	StableSourceID string    `json:"stable_source_id"`
	IssueNumber    int       `json:"issue_number,omitempty"`
	Path           string    `json:"path"`
	Title          string    `json:"title"`
	Kind           string    `json:"kind"`
	Status         string    `json:"status"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type LinkCheckRequest struct {
	RepoID string `json:"repo_id"`
	Strict bool   `json:"strict,omitempty"`
}

type LinkCheckResult struct {
	RepoID           string              `json:"repo_id"`
	CheckedCount     int                 `json:"checked_count"`
	BrokenCount      int                 `json:"broken_count"`
	BrokenLinks      []BrokenLinkResult  `json:"broken_links"`
	SuggestedAliases map[string][]string `json:"suggested_aliases"`
}

type BrokenLinkResult struct {
	RepoID   string `json:"repo_id"`
	SourceID string `json:"source_id"`
	TargetID string `json:"target_id"`
	Kind     string `json:"kind"`
	Text     string `json:"text"`
}

type StaleIndexRequest struct {
	RepoID string `json:"repo_id"`
	Strict bool   `json:"strict,omitempty"`
}

type StaleIndexResult struct {
	RepoID            string                       `json:"repo_id"`
	StaleCount        int                          `json:"stale_count"`
	AffectedSourceIDs []string                     `json:"affected_source_ids"`
	MissingTargetIDs  []string                     `json:"missing_target_ids"`
	LastIndexedAt     time.Time                    `json:"last_indexed_at"`
	Warnings          []IndexWarning               `json:"warnings"`
	Records           []index.IndexFreshnessRecord `json:"records,omitempty"`
}

type ExportSnapshotRequest struct {
	RepoID      string   `json:"repo_id"`
	SnapshotID  string   `json:"snapshot_id,omitempty"`
	Format      string   `json:"format,omitempty"`
	IncludeBody bool     `json:"include_body,omitempty"`
	SourceIDs   []string `json:"source_ids,omitempty"`
	OutputPath  string   `json:"output_path,omitempty"`
	InlineLimit int      `json:"inline_limit,omitempty"`
}

type ExportSnapshotResult struct {
	RepoID        string    `json:"repo_id"`
	SnapshotID    string    `json:"snapshot_id"`
	Format        string    `json:"format"`
	RecordCount   int       `json:"record_count"`
	GeneratedAt   time.Time `json:"generated_at"`
	ContentHash   string    `json:"content_hash"`
	InlineContent string    `json:"inline_content,omitempty"`
	OutputPath    string    `json:"output_path,omitempty"`
	Warnings      []string  `json:"warnings,omitempty"`
}

type SnapshotRef struct {
	Kind   string `json:"kind"`
	Path   string `json:"path,omitempty"`
	Bytes  []byte `json:"bytes,omitempty"`
	Format string `json:"format,omitempty"`
}

type Snapshot struct {
	SchemaVersion string               `json:"schema_version"`
	RepoID        string               `json:"repo_id,omitempty"`
	CreatedAt     time.Time            `json:"created_at"`
	Sources       []SnapshotSource     `json:"sources"`
	Aliases       []SnapshotAlias      `json:"aliases"`
	Links         []SnapshotLink       `json:"links"`
	Backlinks     []SnapshotLink       `json:"backlinks"`
	SyncStatus    []SnapshotSyncStatus `json:"sync_status"`
	Chunks        []SnapshotChunk      `json:"chunks"`
	Warnings      []IndexWarning       `json:"warnings"`
	ManifestHash  string               `json:"manifest_hash,omitempty"`
	ChunkSetHash  string               `json:"chunk_set_hash,omitempty"`
}

type SnapshotSource struct {
	RepoID      string    `json:"repo_id"`
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	Path        string    `json:"path"`
	Title       string    `json:"title"`
	Body        string    `json:"body,omitempty"`
	Status      string    `json:"status"`
	Labels      []string  `json:"labels,omitempty"`
	ContentHash string    `json:"content_hash"`
	CreatedAt   time.Time `json:"created_at,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
}

type SnapshotAlias struct {
	RepoID     string `json:"repo_id"`
	SourceID   string `json:"source_id"`
	AliasKind  string `json:"alias_kind"`
	AliasValue string `json:"alias_value"`
	RemoteKind string `json:"remote_kind,omitempty"`
	RemoteID   string `json:"remote_id,omitempty"`
}

type SnapshotLink struct {
	RepoID   string `json:"repo_id"`
	SourceID string `json:"source_id"`
	TargetID string `json:"target_id"`
	LinkType string `json:"link_type"`
	Text     string `json:"text,omitempty"`
}

type SnapshotSyncStatus struct {
	RepoID         string    `json:"repo_id"`
	SourceID       string    `json:"source_id"`
	RemoteType     string    `json:"remote_type,omitempty"`
	RemoteID       string    `json:"remote_id,omitempty"`
	RemoteRevision string    `json:"remote_revision,omitempty"`
	Status         string    `json:"status"`
	Freshness      string    `json:"freshness"`
	LastFetchedAt  time.Time `json:"last_fetched_at,omitempty"`
}

type SnapshotChunk struct {
	RepoID             string            `json:"repo_id"`
	ID                 string            `json:"id"`
	SourceID           string            `json:"source_id"`
	ContentHash        string            `json:"content_hash"`
	ByteStart          int               `json:"byte_start"`
	ByteEnd            int               `json:"byte_end"`
	LineStart          int               `json:"line_start"`
	LineEnd            int               `json:"line_end"`
	HeadingPath        []string          `json:"heading_path"`
	Text               string            `json:"text,omitempty"`
	NormalizedText     string            `json:"normalized_text,omitempty"`
	InheritedMetadata  map[string]string `json:"inherited_metadata,omitempty"`
	OutboundLinks      []string          `json:"outbound_links,omitempty"`
	ResolvedAliases    map[string]string `json:"resolved_aliases,omitempty"`
	RecordID           string            `json:"record_id,omitempty"`
	SourceRevisionHash string            `json:"source_revision_hash,omitempty"`
	IndexBuildID       string            `json:"index_build_id,omitempty"`
	Ordinal            int               `json:"ordinal,omitempty"`
}

type DiffSnapshotRequest struct {
	RepoID              string      `json:"repo_id"`
	BaseSnapshotID      string      `json:"base_snapshot_id,omitempty"`
	HeadSnapshotID      string      `json:"head_snapshot_id,omitempty"`
	BaseContent         string      `json:"base_content,omitempty"`
	BaseSnapshotContent string      `json:"base_snapshot_content,omitempty"`
	Base                SnapshotRef `json:"base,omitempty"`
	Head                SnapshotRef `json:"head,omitempty"`
	Format              string      `json:"format,omitempty"`
}

type SnapshotRecordChange struct {
	ID                string   `json:"id"`
	BeforeContentHash string   `json:"before_content_hash,omitempty"`
	AfterContentHash  string   `json:"after_content_hash,omitempty"`
	ChangedFields     []string `json:"changed_fields,omitempty"`
}

type DiffSnapshotResult struct {
	RepoID            string                 `json:"repo_id"`
	BaseSnapshotID    string                 `json:"base_snapshot_id"`
	HeadSnapshotID    string                 `json:"head_snapshot_id"`
	Format            string                 `json:"format"`
	AddedSources      []SnapshotSource       `json:"added_sources,omitempty"`
	RemovedSources    []SnapshotSource       `json:"removed_sources,omitempty"`
	ChangedSources    []SnapshotRecordChange `json:"changed_sources,omitempty"`
	AddedChunks       []SnapshotChunk        `json:"added_chunks,omitempty"`
	RemovedChunks     []SnapshotChunk        `json:"removed_chunks,omitempty"`
	ChangedChunks     []SnapshotRecordChange `json:"changed_chunks,omitempty"`
	AddedLinks        []SnapshotLink         `json:"added_links,omitempty"`
	RemovedLinks      []SnapshotLink         `json:"removed_links,omitempty"`
	ChangedAliases    []SnapshotRecordChange `json:"changed_aliases,omitempty"`
	ChangedSyncStatus []SnapshotRecordChange `json:"changed_sync_status,omitempty"`
	ChangedSourceIDs  []string               `json:"changed_source_ids"`
	AddedSourceIDs    []string               `json:"added_source_ids"`
	RemovedSourceIDs  []string               `json:"removed_source_ids"`
	ModifiedSourceIDs []string               `json:"modified_source_ids"`
	DiffText          string                 `json:"diff_text"`
	Warnings          []string               `json:"warnings,omitempty"`
}

type LinkResult struct {
	SourceID string `json:"source_id"`
	TargetID string `json:"target_id"`
	Kind     string `json:"kind"`
	Text     string `json:"text"`
}

type OperationRequest struct {
	RepoID     string `json:"repo_id"`
	Mode       string `json:"mode,omitempty"`
	InputPath  string `json:"input_path,omitempty"`
	OutputPath string `json:"output_path,omitempty"`
	Strict     bool   `json:"strict,omitempty"`
}

type OperationResult struct {
	Command        string    `json:"command"`
	Status         string    `json:"status"`
	ProcessedCount int       `json:"processed_count,omitempty"`
	Evidence       string    `json:"evidence,omitempty"`
	GeneratedAt    time.Time `json:"generated_at"`
}

type WriteMode string

const (
	WriteModeDryRun WriteMode = "dry_run"
	WriteModeLive   WriteMode = "live"
)

type WriteCommandRequest struct {
	Owner          string    `json:"owner,omitempty"`
	Repo           string    `json:"repo,omitempty"`
	RepoID         string    `json:"repo_id,omitempty"`
	Mode           WriteMode `json:"write_mode,omitempty"`
	ID             string    `json:"id,omitempty"`
	IssueID        string    `json:"issue_id,omitempty"`
	Number         int       `json:"number,omitempty"`
	IssueNumber    int       `json:"issue_number,omitempty"`
	CommentID      string    `json:"comment_id,omitempty"`
	DiscussionID   string    `json:"discussion_id,omitempty"`
	ParentID       string    `json:"parent_comment_id,omitempty"`
	Slug           string    `json:"slug,omitempty"`
	Path           string    `json:"path,omitempty"`
	Line           int       `json:"line,omitempty"`
	StartLine      int       `json:"start_line,omitempty"`
	EndLine        int       `json:"end_line,omitempty"`
	Position       int       `json:"position,omitempty"`
	Sha            string    `json:"sha,omitempty"`
	Title          string    `json:"title,omitempty"`
	Body           string    `json:"body,omitempty"`
	Description    string    `json:"description,omitempty"`
	DueOn          string    `json:"due_on,omitempty"`
	Milestone      string    `json:"milestone,omitempty"`
	ClearMilestone bool      `json:"clear_milestone,omitempty"`
	Head           string    `json:"head,omitempty"`
	Base           string    `json:"base,omitempty"`
	State          string    `json:"state,omitempty"`
	Label          string    `json:"label,omitempty"`
	Labels         []string  `json:"labels,omitempty"`
	Strategy       string    `json:"strategy,omitempty"`
	IdempotencyKey string    `json:"idempotency_key,omitempty"`

	pushMirrorPreviousStatus string
	idempotencyFingerprint   string
	parentSourceID           string
}

type WriteCommandResult struct {
	Command           string                  `json:"command"`
	Status            string                  `json:"status"`
	RepoID            string                  `json:"repo_id,omitempty"`
	ID                string                  `json:"id,omitempty"`
	StableSourceID    string                  `json:"stable_source_id,omitempty"`
	IssueNumber       int                     `json:"issue_number,omitempty"`
	RemoteID          string                  `json:"remote_id,omitempty"`
	RemoteNumber      int                     `json:"remote_number,omitempty"`
	RemoteSlug        string                  `json:"remote_slug,omitempty"`
	RemoteRevision    string                  `json:"remote_revision,omitempty"`
	APIPath           string                  `json:"api_path,omitempty"`
	CachePath         string                  `json:"cache_path,omitempty"`
	BrowserURL        string                  `json:"browser_url,omitempty"`
	IdempotencyKey    string                  `json:"idempotency_key"`
	SourceFingerprint string                  `json:"source_fingerprint,omitempty"`
	Replayed          bool                    `json:"replayed,omitempty"`
	Milestone         *WriteMilestoneReceipt  `json:"milestone,omitempty"`
	PushMirror        *WritePushMirrorReceipt `json:"push_mirror,omitempty"`
	Evidence          string                  `json:"evidence,omitempty"`
	GeneratedAt       time.Time               `json:"generated_at"`
}

type WriteMilestoneReceipt struct {
	ID       string `json:"id,omitempty"`
	RemoteID string `json:"remote_id,omitempty"`
	Title    string `json:"title,omitempty"`
	Cleared  bool   `json:"cleared,omitempty"`
}

type WritePushMirrorReceipt struct {
	MirrorID       string    `json:"mirror_id"`
	Status         string    `json:"status"`
	PreviousStatus string    `json:"previous_status,omitempty"`
	ReadbackStatus string    `json:"readback_status,omitempty"`
	TriggeredAt    time.Time `json:"triggered_at"`
}

type MilestoneRecord struct {
	ID          string    `json:"id"`
	RemoteID    string    `json:"remote_id"`
	Title       string    `json:"title"`
	Description string    `json:"description,omitempty"`
	State       string    `json:"state"`
	DueOn       string    `json:"due_on,omitempty"`
	BrowserURL  string    `json:"browser_url,omitempty"`
	CreatedAt   time.Time `json:"created_at,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
}

type MilestoneListRequest struct {
	RepoID  string `json:"repo_id,omitempty"`
	Repo    string `json:"repo,omitempty"`
	State   string `json:"state,omitempty"`
	Page    int    `json:"page,omitempty"`
	PerPage int    `json:"per_page,omitempty"`
}

type MilestoneListResult struct {
	RepoID      string            `json:"repo_id"`
	Milestones  []MilestoneRecord `json:"milestones"`
	Page        int               `json:"page,omitempty"`
	PerPage     int               `json:"per_page,omitempty"`
	Count       int               `json:"count"`
	Evidence    string            `json:"evidence,omitempty"`
	GeneratedAt time.Time         `json:"generated_at"`
}

type PushMirrorRecord struct {
	ID                     string    `json:"id"`
	RemoteID               string    `json:"remote_id"`
	ProjectID              string    `json:"project_id,omitempty"`
	Destination            string    `json:"destination"`
	Force                  bool      `json:"force"`
	Private                bool      `json:"is_private"`
	UpdateStatus           string    `json:"update_status,omitempty"`
	NumberOfFailures       int       `json:"number_of_failures"`
	Message                string    `json:"message,omitempty"`
	CreatedAt              time.Time `json:"created_at,omitempty"`
	LastUpdateAt           time.Time `json:"last_update_at,omitempty"`
	LastSuccessfulUpdateAt time.Time `json:"last_successful_update_at,omitempty"`
}

type PushMirrorListRequest struct {
	RepoID string `json:"repo_id,omitempty"`
	Repo   string `json:"repo,omitempty"`
}

type PushMirrorListResult struct {
	RepoID      string             `json:"repo_id"`
	Mirrors     []PushMirrorRecord `json:"mirrors"`
	Count       int                `json:"count"`
	Evidence    string             `json:"evidence,omitempty"`
	GeneratedAt time.Time          `json:"generated_at"`
}

type PushMirrorWaitRequest struct {
	RepoID         string        `json:"repo_id,omitempty"`
	Repo           string        `json:"repo,omitempty"`
	MirrorID       string        `json:"mirror_id,omitempty"`
	After          time.Time     `json:"after,omitempty"`
	TimeoutSeconds int           `json:"timeout_seconds,omitempty"`
	Timeout        time.Duration `json:"-"`
	PollInterval   time.Duration `json:"-"`
}

type PushMirrorWaitResult struct {
	RepoID                 string    `json:"repo_id"`
	MirrorID               string    `json:"mirror_id"`
	Status                 string    `json:"status"`
	UpdateStatus           string    `json:"update_status,omitempty"`
	NumberOfFailures       int       `json:"number_of_failures"`
	Message                string    `json:"message,omitempty"`
	LastUpdateAt           time.Time `json:"last_update_at,omitempty"`
	LastSuccessfulUpdateAt time.Time `json:"last_successful_update_at,omitempty"`
	After                  time.Time `json:"after,omitempty"`
	Evidence               string    `json:"evidence,omitempty"`
	GeneratedAt            time.Time `json:"generated_at"`
}

type PublishReleaseRequest struct {
	RepoID         string             `json:"repo_id,omitempty"`
	Repo           string             `json:"repo,omitempty"`
	Mode           WriteMode          `json:"write_mode,omitempty"`
	Tag            string             `json:"tag"`
	Ref            string             `json:"ref,omitempty"`
	Title          string             `json:"title"`
	Body           string             `json:"body"`
	Status         string             `json:"status,omitempty"`
	Assets         []PublishAssetLink `json:"assets,omitempty"`
	IdempotencyKey string             `json:"idempotency_key,omitempty"`
}

type PublishAssetLink struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type PublishReleaseResult struct {
	Command           string             `json:"command"`
	Status            string             `json:"status"`
	RepoID            string             `json:"repo_id"`
	Tag               string             `json:"tag"`
	RemoteID          string             `json:"remote_id,omitempty"`
	ReleaseStatus     int                `json:"release_status"`
	AssetLinks        []PublishAssetLink `json:"asset_links,omitempty"`
	IdempotencyKey    string             `json:"idempotency_key"`
	SourceFingerprint string             `json:"source_fingerprint,omitempty"`
	Evidence          string             `json:"evidence,omitempty"`
	GeneratedAt       time.Time          `json:"generated_at"`
}

type BulkSyncScope string

const (
	BulkSyncScopeIssues   BulkSyncScope = "issues"
	BulkSyncScopeWiki     BulkSyncScope = "wiki"
	BulkSyncScopePulls    BulkSyncScope = "pulls"
	BulkSyncScopeComments BulkSyncScope = "comments"
	BulkSyncScopeAll      BulkSyncScope = "all"
)

type BulkSyncRequest struct {
	RepoID           string               `json:"repo_id"`
	RemoteAlias      string               `json:"remote_alias,omitempty"`
	Scope            BulkSyncScope        `json:"scope"`
	IdempotencyKey   string               `json:"idempotency_key,omitempty"`
	MaxAttempts      int                  `json:"max_attempts,omitempty"`
	MaxSize          int64                `json:"max_size,omitempty"`
	Page             int                  `json:"page,omitempty"`
	PerPage          int                  `json:"per_page,omitempty"`
	Bounds           *SyncBounds          `json:"-"`
	ProgressChan     chan<- ProgressEvent `json:"-"`
	IncrementalQueue bool                 `json:"-"`
}

type PRDiscussionRequest struct {
	RepoID         string `json:"repo_id"`
	Number         int    `json:"number"`
	UnresolvedOnly bool   `json:"unresolved_only,omitempty"`
}

type PRDiscussionsResult struct {
	RepoID         string         `json:"repo_id"`
	Number         int            `json:"number"`
	UnresolvedOnly bool           `json:"unresolved_only,omitempty"`
	Discussions    []PRDiscussion `json:"discussions"`
	GeneratedAt    time.Time      `json:"generated_at"`
}

type PRDiscussion struct {
	ID                     string            `json:"id"`
	Replyable              bool              `json:"replyable"`
	ReplyDiscussionID      string            `json:"reply_discussion_id,omitempty"`
	ReplyUnavailableReason string            `json:"reply_unavailable_reason,omitempty"`
	Kind                   string            `json:"kind"`
	Resolved               *bool             `json:"resolved,omitempty"`
	Resolvable             *bool             `json:"resolvable,omitempty"`
	Path                   string            `json:"path,omitempty"`
	Line                   int               `json:"line,omitempty"`
	StartLine              int               `json:"start_line,omitempty"`
	EndLine                int               `json:"end_line,omitempty"`
	Position               *PRReviewPosition `json:"position,omitempty"`
	Comments               []PRReviewComment `json:"comments"`
}

type PRReviewComment struct {
	ID               string             `json:"id"`
	SourceID         string             `json:"source_id"`
	DiscussionID     string             `json:"discussion_id,omitempty"`
	Kind             string             `json:"kind"`
	Body             string             `json:"body"`
	Author           string             `json:"author"`
	Path             string             `json:"path,omitempty"`
	Line             int                `json:"line,omitempty"`
	StartLine        int                `json:"start_line,omitempty"`
	EndLine          int                `json:"end_line,omitempty"`
	Position         int                `json:"position,omitempty"`
	OriginalPosition int                `json:"original_position,omitempty"`
	Resolved         *bool              `json:"resolved,omitempty"`
	Resolvable       *bool              `json:"resolvable,omitempty"`
	ParentID         string             `json:"parent_id,omitempty"`
	Positions        []PRReviewPosition `json:"positions,omitempty"`
	CreatedAt        time.Time          `json:"created_at"`
	UpdatedAt        time.Time          `json:"updated_at"`
}

type PRReviewPosition struct {
	Kind          string `json:"kind"`
	PositionType  string `json:"position_type,omitempty"`
	BaseSHA       string `json:"base_sha,omitempty"`
	StartSHA      string `json:"start_sha,omitempty"`
	HeadSHA       string `json:"head_sha,omitempty"`
	OldPath       string `json:"old_path,omitempty"`
	NewPath       string `json:"new_path,omitempty"`
	OldLine       int    `json:"old_line,omitempty"`
	NewLine       int    `json:"new_line,omitempty"`
	StartOldLine  int    `json:"start_old_line,omitempty"`
	StartNewLine  int    `json:"start_new_line,omitempty"`
	LineCode      string `json:"line_code,omitempty"`
	StartLineCode string `json:"start_line_code,omitempty"`
	PatchsetIID   int    `json:"patchset_iid,omitempty"`
	DiffID        int    `json:"diff_id,omitempty"`
	VersionSHA    string `json:"version_sha,omitempty"`
	Side          string `json:"side,omitempty"`
	IsOutdated    *bool  `json:"is_outdated,omitempty"`
}
