package index

import (
	"context"
	"time"
)

type SourceReader interface {
	ListSources(context.Context) ([]SourceRecord, error)
}

type DerivedWriter interface {
	ReplaceSourceDerived(context.Context, SourceDerived) error
	WriteDiagnostics(context.Context, []CollisionDiagnostic) error
}

type DerivedLinkReader interface {
	ListDerivedLinks(context.Context) ([]DerivedLink, error)
	ListBrokenLinks(context.Context) ([]BrokenLink, error)
	ListCitationAnchors(context.Context) ([]CitationAnchor, error)
}

type SourceRecord struct {
	RepoID                   string
	ID                       string
	RecordID                 string
	SnapshotID               string
	Kind                     string
	Path                     string
	Title                    string
	Body                     string
	Metadata                 map[string]string
	Status                   string
	UpdatedAt                time.Time
	Aliases                  []Alias
	RemoteAliases            []Alias
	PreviousIndexedHash      string
	PreviousAnchorReferences []string
}

type Alias struct {
	Type string
	ID   string
}

type SourceDerived struct {
	SourceID          string
	ContentHash       string
	Links             []DerivedLink
	Backlinks         []BacklinkRow
	BrokenLinks       []BrokenLink
	CitationAnchors   []CitationAnchor
	Chunks            []Chunk
	SourceLedgerRows  []SourceLedgerRow
	TaskBacklogRows   []TaskBacklogRow
	TrackRows         []TrackRow
	AcceptanceRows    []AcceptanceRow
	OpenQuestionRows  []OpenQuestionRow
	IndexState        IndexState
	Diagnostics       []CollisionDiagnostic
	RewrittenRowCount int
}

type ParsedSource struct {
	SourceID       string
	ContentHash    string
	Frontmatter    Frontmatter
	FrontmatterEnd int
	Headings       []Heading
	Links          []Link
	StableIDs      []StableID
	Aliases        []Alias
	Statuses       []Status
	Diagnostics    []CollisionDiagnostic
	LineStarts     []int
	NormalizedBody string
}

type Frontmatter struct {
	Values map[string]string
	Valid  bool
}

type Heading struct {
	Level       int
	Title       string
	ByteStart   int
	ByteEnd     int
	LineStart   int
	LineEnd     int
	HeadingPath []string
}

type Link struct {
	Raw       string
	Target    string
	Text      string
	Kind      string
	ByteStart int
	ByteEnd   int
	LineStart int
	LineEnd   int
}

type ResolvedLink struct {
	Link     Link
	TargetID string
	Alias    Alias
}

type Status struct {
	Value       string
	ByteStart   int
	ByteEnd     int
	LineStart   int
	LineEnd     int
	HeadingPath []string
}

type StableID struct {
	ID        string
	ByteStart int
	ByteEnd   int
	LineStart int
	LineEnd   int
}

type DerivedRecord struct {
	ID        string
	SourceID  string
	Kind      string
	Title     string
	Status    string
	LineStart int
	LineEnd   int
	AnchorID  string
}

type DerivedLink struct {
	SourceID     string
	TargetID     string
	RawTarget    string
	Text         string
	Kind         string
	LineStart    int
	LineEnd      int
	SourceHash   string
	AnchorID     string
	TargetAnchor string
}

type BacklinkRow struct {
	SourceID  string
	TargetID  string
	Text      string
	Kind      string
	LineStart int
	LineEnd   int
}

type BrokenLink struct {
	SourceID   string
	SourcePath string
	RawTarget  string
	Text       string
	Reason     string
	LineStart  int
	LineEnd    int
}

type CollisionDiagnostic struct {
	SourceID  string
	Kind      string
	Key       string
	Message   string
	LineStart int
	LineEnd   int
}

type StaleReport struct {
	TotalStaleBacklinks int      `json:"total_stale_backlinks"`
	AffectedSourceIDs   []string `json:"affected_source_ids"`
	UnresolvedTargets   []string `json:"unresolved_targets"`
	BrokenRawLinkText   []string `json:"broken_raw_link_text"`
	AmbiguousAliases    []string `json:"ambiguous_aliases"`
	StaleAnchorRefs     []string `json:"stale_anchor_refs"`
}

type CitationAnchor struct {
	ID           string
	SourceID     string
	ContentHash  string
	ByteStart    int
	ByteEnd      int
	LineStart    int
	LineEnd      int
	HeadingPath  []string
	Kind         string
	Title        string
	DerivedRowID string
}

type ChunkPolicy string

const (
	ChunkPolicyHeading       ChunkPolicy = "heading"
	ChunkPolicySlidingWindow ChunkPolicy = "sliding_window"
)

type ChunkOptions struct {
	Policy       ChunkPolicy
	MaxBytes     int
	WindowBytes  int
	OverlapBytes int
}

type Chunk struct {
	ID                string
	RepoID            string
	SourceID          string
	RecordID          string
	SnapshotID        string
	ContentHash       string
	ByteStart         int
	ByteEnd           int
	LineStart         int
	LineEnd           int
	HeadingPath       []string
	Text              string
	NormalizedText    string
	InheritedMetadata map[string]string
	OutboundLinks     []string
	ResolvedAliases   map[string]string
	Embedding         []byte
	CitationAnchorID  string
	Policy            ChunkPolicy
}

type SourceLedgerRow struct {
	SourceID    string
	Kind        string
	Path        string
	Title       string
	Status      string
	ContentHash string
}

type TaskBacklogRow struct {
	SourceID  string
	Title     string
	Status    string
	LineStart int
	AnchorID  string
}

type TrackRow struct {
	SourceID string
	Track    string
	Status   string
	AnchorID string
}

type AcceptanceRow struct {
	SourceID  string
	Title     string
	LineStart int
	LineEnd   int
	AnchorID  string
}

type OpenQuestionRow struct {
	SourceID  string
	Question  string
	LineStart int
	LineEnd   int
	AnchorID  string
}

type IndexState struct {
	SourceID    string
	ContentHash string
	IndexedAt   time.Time
}

type BuildReport struct {
	ProcessedCount    int
	SkippedCount      int
	RewrittenRowCount int
	CollisionCount    int
	BrokenLinkCount   int
	Diagnostics       []CollisionDiagnostic
}

type ChunkQuery struct {
	RepoID     string
	SourceID   string
	RecordID   string
	SnapshotID string
	Policy     ChunkPolicy
	Limit      int
	Offset     int
}

type ChunkSearchQuery struct {
	ChunkQuery
	Query string
}

type SnippetQuery struct {
	RepoID     string
	SourceID   string
	RecordID   string
	SnapshotID string
	Policy     ChunkPolicy
	ChunkID    string
	ByteOffset int
	ByteLength int
	LineStart  int
	LineEnd    int
}

type IndexWarning struct {
	RepoID     string      `json:"repo_id,omitempty"`
	SourceID   string      `json:"source_id,omitempty"`
	RecordID   string      `json:"record_id,omitempty"`
	SnapshotID string      `json:"snapshot_id,omitempty"`
	Policy     ChunkPolicy `json:"policy,omitempty"`
	Code       string      `json:"code"`
	Message    string      `json:"message"`
}

type ChunkResult struct {
	ID                string            `json:"id"`
	RepoID            string            `json:"repo_id,omitempty"`
	SourceID          string            `json:"source_id"`
	RecordID          string            `json:"record_id,omitempty"`
	SnapshotID        string            `json:"snapshot_id,omitempty"`
	Policy            ChunkPolicy       `json:"policy"`
	ContentHash       string            `json:"content_hash"`
	ByteStart         int               `json:"byte_start"`
	ByteEnd           int               `json:"byte_end"`
	LineStart         int               `json:"line_start"`
	LineEnd           int               `json:"line_end"`
	HeadingPath       []string          `json:"heading_path"`
	Text              string            `json:"text,omitempty"`
	SnippetText       string            `json:"snippet_text,omitempty"`
	NormalizedText    string            `json:"normalized_text,omitempty"`
	CitationAnchorID  string            `json:"citation_anchor_id,omitempty"`
	InheritedMetadata map[string]string `json:"inherited_metadata,omitempty"`
	OutboundLinks     []string          `json:"outbound_links,omitempty"`
	ResolvedAliases   map[string]string `json:"resolved_aliases,omitempty"`
}

type ChunkQueryResult struct {
	Chunks   []ChunkResult  `json:"chunks"`
	Limit    int            `json:"limit"`
	Offset   int            `json:"offset"`
	Total    int            `json:"total"`
	Warnings []IndexWarning `json:"warnings"`
}

type ChunkIndexReader interface {
	ListChunks(context.Context, ChunkQuery) (ChunkQueryResult, error)
	SearchChunks(context.Context, ChunkSearchQuery) (ChunkQueryResult, error)
	GetSnippet(context.Context, SnippetQuery) (ChunkQueryResult, error)
}
