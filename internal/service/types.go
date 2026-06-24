package service

import (
	"fmt"
	"time"

	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/index"
)

type ServiceConfig struct {
	BaseURL         string
	Timeout         time.Duration
	MaxResponseSize int64
	MaxRetries      int
	UserAgent       string
	Pagination      gitcode.PaginationConfig
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
	RepoID             string            `json:"repo_id"`
	Owner              string            `json:"owner"`
	Name               string            `json:"name"`
	APIBaseURL         string            `json:"api_base_url"`
	Scopes             []RepositoryScope `json:"scopes"`
	DisplayName        string            `json:"display_name,omitempty"`
	Aliases            []string          `json:"aliases"`
	BindingState       string            `json:"binding_state"`
	AliasConflictState string            `json:"alias_conflict_state"`
	CacheState         string            `json:"cache_state"`
	IndexState         string            `json:"index_state"`
	FailureClass       string            `json:"failure_class,omitempty"`
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
	RepoID string `json:"repo_id"`
	Query  string `json:"query"`
	Kind   string `json:"kind,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	Offset int    `json:"offset,omitempty"`
}

type SearchSourcesResult struct {
	RepoID  string               `json:"repo_id"`
	Query   string               `json:"query"`
	Results []SearchSourceResult `json:"results"`
	Limit   int                  `json:"limit"`
	Offset  int                  `json:"offset"`
}

type SearchSourceResult struct {
	RepoID     string  `json:"repo_id"`
	ID         string  `json:"id"`
	Path       string  `json:"path"`
	Title      string  `json:"title"`
	Kind       string  `json:"kind"`
	Status     string  `json:"status"`
	Provenance string  `json:"provenance"`
	Snippet    string  `json:"snippet"`
	LineStart  *int    `json:"line_start"`
	LineEnd    *int    `json:"line_end"`
	Score      float64 `json:"score"`
}

type GetSourceRequest struct {
	RepoID    string `json:"repo_id"`
	ID        string `json:"id,omitempty"`
	AliasType string `json:"alias_type,omitempty"`
	AliasID   string `json:"alias_id,omitempty"`
}

type SourceRecord struct {
	RepoID      string           `json:"repo_id"`
	ID          string           `json:"id"`
	Path        string           `json:"path"`
	RemoteAlias string           `json:"remote_alias"`
	Kind        string           `json:"kind"`
	Title       string           `json:"title"`
	Body        string           `json:"body"`
	Status      string           `json:"status"`
	Provenance  string           `json:"provenance"`
	Labels      []string         `json:"labels"`
	Links       []LinkResult     `json:"links"`
	Backlinks   []BacklinkResult `json:"backlinks,omitempty"`
	UpdatedAt   time.Time        `json:"updated_at"`
}

type ListSourcesRequest struct {
	RepoID string `json:"repo_id"`
	Kind   string `json:"kind,omitempty"`
	Status string `json:"status,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	Offset int    `json:"offset,omitempty"`
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
	RepoID      string    `json:"repo_id"`
	ID          string    `json:"id"`
	Path        string    `json:"path"`
	RemoteAlias string    `json:"remote_alias,omitempty"`
	Kind        string    `json:"kind"`
	Title       string    `json:"title"`
	Status      string    `json:"status"`
	Provenance  string    `json:"provenance"`
	UpdatedAt   time.Time `json:"updated_at"`
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
	RepoID      string `json:"repo_id"`
	ID          string `json:"id"`
	Path        string `json:"path"`
	RemoteAlias string `json:"remote_alias"`
	Kind        string `json:"kind"`
	Title       string `json:"title"`
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
	RepoID         string    `json:"repo_id"`
	SourceID       string    `json:"source_id"`
	RemoteType     string    `json:"remote_type"`
	RemoteID       string    `json:"remote_id"`
	RemoteRevision string    `json:"remote_revision"`
	Status         string    `json:"status"`
	Freshness      string    `json:"freshness"`
	Provenance     string    `json:"provenance"`
	LocalUpdatedAt time.Time `json:"local_updated_at"`
	LastFetchedAt  time.Time `json:"last_fetched_at"`
}

type SyncStatusSummaryResult struct {
	RepoID              string             `json:"repo_id"`
	Results             []SyncStatusResult `json:"results"`
	FreshCount          int                `json:"fresh_count"`
	StaleCount          int                `json:"stale_count"`
	LastSyncAt          time.Time          `json:"last_sync_at"`
	LastSyncStartedAt   time.Time          `json:"last_sync_started_at"`
	LastSyncCompletedAt time.Time          `json:"last_sync_completed_at"`
	ZeroDelta           bool               `json:"zero_delta"`
	CacheEmpty          bool               `json:"cache_empty"`
	Limit               int                `json:"limit"`
	Offset              int                `json:"offset"`
	Warnings            []string           `json:"warnings,omitempty"`
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
}

type SyncCounts struct {
	Fetched   int `json:"fetched"`
	Skipped   int `json:"skipped"`
	Updated   int `json:"updated"`
	Conflicts int `json:"conflicts"`
	Inserted  int `json:"inserted"`
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
	Results      []SyncResult    `json:"results"`
	SuccessCount int             `json:"success_count"`
	FailureCount int             `json:"failure_count"`
	Failures     []ResourceError `json:"failures,omitempty"`
}

type ResourceError struct {
	SourceID   string `json:"source_id"`
	RemoteType string `json:"remote_type"`
	Err        error  `json:"-"`
	Message    string `json:"message"`
}

func (e ResourceError) Error() string {
	return e.Message
}

func (e ResourceError) Unwrap() error { return e.Err }

type PartialSyncError struct {
	Errors       []ResourceError `json:"errors"`
	SuccessCount int             `json:"success_count"`
	FailureCount int             `json:"failure_count"`
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
	RepoID    string    `json:"repo_id"`
	ID        string    `json:"id"`
	Path      string    `json:"path"`
	Title     string    `json:"title"`
	Kind      string    `json:"kind"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
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
	Number         int       `json:"number,omitempty"`
	Slug           string    `json:"slug,omitempty"`
	Path           string    `json:"path,omitempty"`
	Sha            string    `json:"sha,omitempty"`
	Title          string    `json:"title,omitempty"`
	Body           string    `json:"body,omitempty"`
	State          string    `json:"state,omitempty"`
	Label          string    `json:"label,omitempty"`
	Labels         []string  `json:"labels,omitempty"`
	IdempotencyKey string    `json:"idempotency_key,omitempty"`
}

type WriteCommandResult struct {
	Command           string    `json:"command"`
	Status            string    `json:"status"`
	RepoID            string    `json:"repo_id,omitempty"`
	ID                string    `json:"id,omitempty"`
	RemoteID          string    `json:"remote_id,omitempty"`
	RemoteNumber      int       `json:"remote_number,omitempty"`
	RemoteSlug        string    `json:"remote_slug,omitempty"`
	RemoteRevision    string    `json:"remote_revision,omitempty"`
	IdempotencyKey    string    `json:"idempotency_key"`
	SourceFingerprint string    `json:"source_fingerprint,omitempty"`
	Replayed          bool      `json:"replayed,omitempty"`
	Evidence          string    `json:"evidence,omitempty"`
	GeneratedAt       time.Time `json:"generated_at"`
}

type BulkSyncScope string

const (
	BulkSyncScopeIssues BulkSyncScope = "issues"
	BulkSyncScopeWiki   BulkSyncScope = "wiki"
	BulkSyncScopeAll    BulkSyncScope = "all"
)

type BulkSyncRequest struct {
	RepoID         string        `json:"repo_id"`
	Scope          BulkSyncScope `json:"scope"`
	IdempotencyKey string        `json:"idempotency_key,omitempty"`
	MaxAttempts    int           `json:"max_attempts,omitempty"`
	MaxSize        int64         `json:"max_size,omitempty"`
	Page           int           `json:"page,omitempty"`
	PerPage        int           `json:"per_page,omitempty"`
}
