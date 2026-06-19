package service

import "time"

type SearchSourcesRequest struct {
	Query  string `json:"query"`
	Kind   string `json:"kind,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	Offset int    `json:"offset,omitempty"`
}

type SearchSourceResult struct {
	ID        string  `json:"id"`
	Path      string  `json:"path"`
	Title     string  `json:"title"`
	Kind      string  `json:"kind"`
	Status    string  `json:"status"`
	Snippet   string  `json:"snippet"`
	LineStart *int    `json:"line_start"`
	LineEnd   *int    `json:"line_end"`
	Score     float64 `json:"score"`
}

type GetSourceRequest struct {
	ID        string `json:"id,omitempty"`
	AliasType string `json:"alias_type,omitempty"`
	AliasID   string `json:"alias_id,omitempty"`
}

type SourceRecord struct {
	ID          string           `json:"id"`
	Path        string           `json:"path"`
	RemoteAlias string           `json:"remote_alias"`
	Kind        string           `json:"kind"`
	Title       string           `json:"title"`
	Body        string           `json:"body"`
	Status      string           `json:"status"`
	Labels      []string         `json:"labels"`
	Links       []LinkResult     `json:"links"`
	Backlinks   []BacklinkResult `json:"backlinks,omitempty"`
	UpdatedAt   time.Time        `json:"updated_at"`
}

type ListSourcesRequest struct {
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

type SourceSummary struct {
	ID          string    `json:"id"`
	Path        string    `json:"path"`
	RemoteAlias string    `json:"remote_alias,omitempty"`
	Kind        string    `json:"kind"`
	Title       string    `json:"title"`
	Status      string    `json:"status"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type GetBacklinksRequest struct {
	ID        string `json:"id,omitempty"`
	AliasType string `json:"alias_type,omitempty"`
	AliasID   string `json:"alias_id,omitempty"`
}

type BacklinkResult struct {
	SourceSummary
	TargetID string `json:"target_id"`
}

type ResolveIDRequest struct {
	ID        string `json:"id,omitempty"`
	AliasType string `json:"alias_type,omitempty"`
	AliasID   string `json:"alias_id,omitempty"`
}

type ResolvedID struct {
	ID          string `json:"id"`
	Path        string `json:"path"`
	RemoteAlias string `json:"remote_alias"`
	Kind        string `json:"kind"`
	Title       string `json:"title"`
}

type SnippetRequest struct {
	ID        string `json:"id,omitempty"`
	AliasType string `json:"alias_type,omitempty"`
	AliasID   string `json:"alias_id,omitempty"`
	LineStart int    `json:"line_start,omitempty"`
	LineEnd   int    `json:"line_end,omitempty"`
	ChunkID   string `json:"chunk_id,omitempty"`
}

type SnippetResult struct {
	ID        string   `json:"id"`
	Path      string   `json:"path"`
	Text      string   `json:"text"`
	LineStart int      `json:"line_start"`
	LineEnd   int      `json:"line_end"`
	ChunkID   string   `json:"chunk_id,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
}

type SyncStatusRequest struct {
	ID        string `json:"id,omitempty"`
	AliasType string `json:"alias_type,omitempty"`
	AliasID   string `json:"alias_id,omitempty"`
}

type SyncStatusResult struct {
	SourceID       string    `json:"source_id"`
	RemoteType     string    `json:"remote_type"`
	RemoteID       string    `json:"remote_id"`
	RemoteRevision string    `json:"remote_revision"`
	Status         string    `json:"status"`
	LastFetchedAt  time.Time `json:"last_fetched_at"`
}

type RecentChangesRequest struct {
	Kind   string `json:"kind,omitempty"`
	Status string `json:"status,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	Offset int    `json:"offset,omitempty"`
}

type RecentChangeResult struct {
	ID        string    `json:"id"`
	Path      string    `json:"path"`
	Title     string    `json:"title"`
	Kind      string    `json:"kind"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}

type LinkCheckRequest struct {
	Strict bool `json:"strict,omitempty"`
}

type LinkCheckResult struct {
	CheckedCount     int                 `json:"checked_count"`
	BrokenCount      int                 `json:"broken_count"`
	BrokenLinks      []BrokenLinkResult  `json:"broken_links"`
	SuggestedAliases map[string][]string `json:"suggested_aliases"`
}

type BrokenLinkResult struct {
	SourceID string `json:"source_id"`
	TargetID string `json:"target_id"`
	Kind     string `json:"kind"`
	Text     string `json:"text"`
}

type StaleIndexRequest struct {
	Strict bool `json:"strict,omitempty"`
}

type StaleIndexResult struct {
	StaleCount        int       `json:"stale_count"`
	AffectedSourceIDs []string  `json:"affected_source_ids"`
	MissingTargetIDs  []string  `json:"missing_target_ids"`
	LastIndexedAt     time.Time `json:"last_indexed_at"`
}

type ExportSnapshotRequest struct {
	Format      string `json:"format,omitempty"`
	OutputPath  string `json:"output_path,omitempty"`
	InlineLimit int    `json:"inline_limit,omitempty"`
}

type ExportSnapshotResult struct {
	SnapshotID    string    `json:"snapshot_id"`
	Format        string    `json:"format"`
	RecordCount   int       `json:"record_count"`
	GeneratedAt   time.Time `json:"generated_at"`
	ContentHash   string    `json:"content_hash"`
	InlineContent string    `json:"inline_content,omitempty"`
	OutputPath    string    `json:"output_path,omitempty"`
	Warnings      []string  `json:"warnings,omitempty"`
}

type DiffSnapshotRequest struct {
	BaseSnapshotID      string `json:"base_snapshot_id,omitempty"`
	HeadSnapshotID      string `json:"head_snapshot_id,omitempty"`
	BaseContent         string `json:"base_content,omitempty"`
	BaseSnapshotContent string `json:"base_snapshot_content,omitempty"`
	Format              string `json:"format,omitempty"`
}

type DiffSnapshotResult struct {
	BaseSnapshotID    string   `json:"base_snapshot_id"`
	HeadSnapshotID    string   `json:"head_snapshot_id"`
	Format            string   `json:"format"`
	ChangedSourceIDs  []string `json:"changed_source_ids"`
	AddedSourceIDs    []string `json:"added_source_ids"`
	RemovedSourceIDs  []string `json:"removed_source_ids"`
	ModifiedSourceIDs []string `json:"modified_source_ids"`
	DiffText          string   `json:"diff_text"`
	Warnings          []string `json:"warnings,omitempty"`
}

type LinkResult struct {
	SourceID string `json:"source_id"`
	TargetID string `json:"target_id"`
	Kind     string `json:"kind"`
	Text     string `json:"text"`
}

type OperationRequest struct {
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

type WriteCommandRequest struct {
	Owner          string   `json:"owner,omitempty"`
	Repo           string   `json:"repo,omitempty"`
	ID             string   `json:"id,omitempty"`
	Number         int      `json:"number,omitempty"`
	Slug           string   `json:"slug,omitempty"`
	Title          string   `json:"title,omitempty"`
	Body           string   `json:"body,omitempty"`
	State          string   `json:"state,omitempty"`
	Label          string   `json:"label,omitempty"`
	Labels         []string `json:"labels,omitempty"`
	IdempotencyKey string   `json:"idempotency_key,omitempty"`
}

type WriteCommandResult struct {
	Command        string    `json:"command"`
	Status         string    `json:"status"`
	ID             string    `json:"id,omitempty"`
	IdempotencyKey string    `json:"idempotency_key"`
	Evidence       string    `json:"evidence,omitempty"`
	GeneratedAt    time.Time `json:"generated_at"`
}
