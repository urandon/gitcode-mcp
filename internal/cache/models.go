package cache

import (
	"context"
	"time"
)

type Store interface {
	AddRepository(context.Context, RepositoryBinding) error
	UpsertRepo(context.Context, RepositoryBinding) error
	GetRepository(context.Context, string) (RepositoryBinding, error)
	GetRepo(context.Context, string) (RepositoryBinding, error)
	ListRepositories(context.Context) ([]RepositoryBinding, error)
	UpsertSourceGraph(context.Context, SourceGraph) error
	UpsertRecordGraph(context.Context, RecordGraph) error
	UpsertSyncGraph(context.Context, SyncGraph) error
	UpsertSource(context.Context, Source) error
	GetSource(context.Context, string) (Source, error)
	GetSourceScoped(context.Context, string, string) (Source, error)
	ListSources(context.Context, SourceFilter) ([]Source, error)
	SearchSources(context.Context, SearchQuery) ([]SearchResult, error)
	UpsertPRReviewComment(context.Context, PRReviewComment) error
	ListPRReviewComments(context.Context, PRReviewCommentFilter) ([]PRReviewComment, error)
	UpsertPRReviewDiscussion(context.Context, PRReviewDiscussion) error
	ListPRReviewDiscussions(context.Context, PRReviewDiscussionFilter) ([]PRReviewDiscussion, error)
	UpsertPRReviewPosition(context.Context, PRReviewPosition) error
	ListPRReviewPositions(context.Context, PRReviewPositionFilter) ([]PRReviewPosition, error)
	GetRecord(context.Context, string, string) (Record, error)
	ListRecords(context.Context, RecordFilter) ([]Record, error)
	SearchRecords(context.Context, SearchQuery) ([]SearchResult, error)
	UpsertIdentity(context.Context, Identity) error
	GetIdentityMap(context.Context, string) ([]Identity, error)
	GetIdentityMapScoped(context.Context, string, string) ([]Identity, error)
	ResolveAlias(context.Context, RemoteAlias) (Identity, error)
	ResolveAliasScoped(context.Context, string, RemoteAlias) (Identity, error)
	ResolveRepoAlias(context.Context, string, RemoteAlias) (Identity, error)
	DiagnoseAlias(context.Context, RemoteAlias) ([]Identity, error)
	UpsertLink(context.Context, Link) error
	ListLinks(context.Context, LinkFilter) ([]Link, error)
	GetBacklinks(context.Context, string) ([]Source, error)
	GetBacklinksScoped(context.Context, string, string) ([]Source, error)
	UpsertChunk(context.Context, Chunk) (Chunk, error)
	GetChunks(context.Context, string) ([]Chunk, error)
	GetChunksScoped(context.Context, string, string) ([]Chunk, error)
	ListChunks(context.Context, ChunkFilter) ([]Chunk, error)
	RecordSyncEvent(context.Context, SyncEvent) error
	GetSyncEventByKey(context.Context, string) (*SyncEvent, error)
	ListCompletedSyncEventsScoped(context.Context, string) ([]SyncEvent, error)
	UpsertSyncFrontier(context.Context, SyncFrontier) error
	GetSyncFrontier(context.Context, string, string, string, string) (SyncFrontier, bool, error)
	RecordCacheConfirmation(context.Context, CacheConfirmationRecord) error
	GetCacheConfirmationByKey(context.Context, string, string) (*CacheConfirmationRecord, error)
	RecordAuditEvent(context.Context, AuditTrailEntry) error
	GetAuditEventByKey(context.Context, string, string) (*AuditTrailEntry, error)
	GetSyncStatus(context.Context, string) (SyncStatus, error)
	GetSyncStatusScoped(context.Context, string, string) (SyncStatus, error)
	UpsertConflict(context.Context, Conflict) error
	GetConflicts(context.Context, string) ([]Conflict, error)
	RecordCounts(context.Context, string) (RecordCounts, error)
	WALCapable(context.Context) (bool, string, error)
	UpsertSnapshot(context.Context, Snapshot) error
	GetSnapshot(context.Context, string, string) (Snapshot, error)
	ListSnapshotChunks(context.Context, string, string) ([]SnapshotChunk, error)
	IntegrityCheck(context.Context) error
	ResetLive(context.Context, string) error
	AcquireLock(context.Context, string) (*LockHandle, error)
	ReleaseLock(context.Context, *LockHandle) error
	AcquireWriter(context.Context, WriterRequest) (*WriterLease, error)
	ReleaseWriter(context.Context, *WriterLease) error
	Checkpoint(context.Context, string) error
	Close() error
}

type RepositoryScope string

const (
	RepositoryScopeIssues RepositoryScope = "issues"
	RepositoryScopeWiki   RepositoryScope = "wiki"
)

type RepositoryBinding struct {
	RepoID      string
	Owner       string
	Name        string
	APIBaseURL  string
	Scopes      []RepositoryScope
	DisplayName string
	Aliases     []string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type Repo = RepositoryBinding

type Provenance string

const (
	ProvenanceRemote     Provenance = "remote"
	ProvenanceProjection Provenance = "projection"
	ProvenanceBridge     Provenance = "bridge"
	ProvenanceFixture    Provenance = "fixture"
	ProvenanceLive       Provenance = "live"
)

type Record struct {
	RepoID         string
	ID             string
	Type           string
	Path           string
	Title          string
	Body           string
	Status         string
	Labels         []string
	ContentHash    string
	Provenance     Provenance
	RemoteType     string
	RemoteID       string
	RemoteRevision string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	Aliases        []Identity
	Comments       []RecordComment
}

type RecordComment struct {
	RepoID         string
	RecordID       string
	CommentID      string
	Author         string
	Body           string
	ContentHash    string
	RemoteRevision string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type PRReviewComment struct {
	RepoID           string
	SourceID         string
	PRNumber         int
	CommentID        string
	DiscussionID     string
	ReviewKind       string
	Author           string
	Path             string
	Line             int
	StartLine        int
	EndLine          int
	Position         int
	OriginalPosition int
	Resolved         *bool
	Resolvable       *bool
	ParentID         string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type PRReviewDiscussion struct {
	RepoID         string
	PRNumber       int
	DiscussionID   string
	Kind           string
	Resolved       *bool
	Resolvable     *bool
	FirstCommentID string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type PRReviewPosition struct {
	RepoID        string
	PRNumber      int
	CommentID     string
	PositionKind  string
	DiscussionID  string
	PositionType  string
	BaseSHA       string
	StartSHA      string
	HeadSHA       string
	OldPath       string
	NewPath       string
	OldLine       int
	NewLine       int
	StartOldLine  int
	StartNewLine  int
	LineCode      string
	StartLineCode string
	PatchsetIID   int
	DiffID        int
	VersionSHA    string
	Side          string
	IsOutdated    *bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type IdentityAlias = Identity

type RemoteRevision struct {
	RepoID         string
	RecordID       string
	RemoteType     string
	RemoteID       string
	RemoteRevision string
	Status         string
	LastFetchedAt  time.Time
}

type SyncFrontier struct {
	RepoID        string
	RemoteType    string
	Ordering      string
	FilterKey     string
	Status        string
	HighUpdatedAt time.Time
	HighRemoteID  string
	HighNumber    int
	StopReason    string
	PagesListed   int
	RecordsListed int
	UpdatedAt     time.Time
}

type AuditTrailEntry struct {
	RepoID          string
	ID              string
	Operation       string
	Command         string
	Mode            string
	RecordID        string
	RemoteType      string
	RemoteID        string
	IdempotencyKey  string
	Status          string
	Message         string
	PayloadHash     string
	RequestMetadata map[string]string
	CreatedAt       time.Time
}

type CacheConfirmationRecord struct {
	RepoID            string
	ID                string
	Command           string
	RecordID          string
	RecordType        string
	RemoteType        string
	RemoteID          string
	IdempotencyKey    string
	Status            string
	SourceFingerprint string
	CreatedAt         time.Time
}

type Snapshot struct {
	RepoID        string
	ID            string
	Format        string
	ContentHash   string
	RecordCount   int
	CreatedAt     time.Time
	SchemaVersion string
	ManifestHash  string
	ChunkSetHash  string
	ChunkCount    int
	ManifestJSON  string
	WarningsJSON  string
	Metadata      map[string]string
	Chunks        []SnapshotChunk
}

type SnapshotChunk struct {
	RepoID              string
	SnapshotID          string
	ChunkID             string
	SourceType          string
	SourceID            string
	RecordID            string
	SourceContentHash   string
	SourceRevisionHash  string
	IndexBuildID        string
	ChunkContentHash    string
	ByteStart           int
	ByteEnd             int
	LineStart           int
	LineEnd             int
	HeadingPathJSON     string
	Ordinal             int
	Text                string
	MetadataJSON        string
	OutboundLinksJSON   string
	ResolvedAliasesJSON string
	Citation            string
	ContentHash         string
}

type RecordCounts struct {
	RepoID          string
	Records         int
	Comments        int
	IdentityAliases int
	SyncEvents      int
	AuditRows       int
	Snapshots       int
	SnapshotChunks  int
	Chunks          int
	RemoteRevisions int
}

type RecordFilter struct {
	RepoID     string
	Type       string
	Status     string
	Provenance Provenance
	Limit      int
}

type RecordGraph struct {
	Record              Record
	Comments            []RecordComment
	PRReviewComments    []PRReviewComment
	PRReviewDiscussions []PRReviewDiscussion
	PRReviewPositions   []PRReviewPosition
	Identities          []Identity
	Links               []Link
	RemoteRevisions     []RemoteRevision
	SyncEvents          []SyncEvent
	AuditTrail          []AuditTrailEntry
	Snapshots           []Snapshot
}

type SyncGraph struct {
	RepoID              string
	Provenance          Provenance
	Record              Record
	Comments            []RecordComment
	PRReviewComments    []PRReviewComment
	PRReviewDiscussions []PRReviewDiscussion
	PRReviewPositions   []PRReviewPosition
	Identities          []Identity
	Links               []Link
	Chunks              []Chunk
	RemoteRevisions     []RemoteRevision
	SyncEvents          []SyncEvent
}

type Source struct {
	RepoID      string
	ID          string
	Kind        string
	Path        string
	Title       string
	Body        string
	Status      string
	Labels      []string
	ContentHash string
	Provenance  Provenance
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Aliases     []Identity
}

func (s Source) GetProvenance() string {
	return string(s.Provenance)
}

type SourceFilter struct {
	RepoID     string
	Kind       string
	Status     string
	Provenance Provenance
	Limit      int
}

func (f *SourceFilter) SetProvenance(provenance Provenance) {
	f.Provenance = provenance
}

type SearchQuery struct {
	RepoID     string
	Query      string
	Kind       string
	Provenance Provenance
	Limit      int
}

func (q *SearchQuery) SetProvenance(provenance Provenance) {
	q.Provenance = provenance
}

type SearchResult struct {
	RepoID     string
	ID         string
	Path       string
	Title      string
	Snippet    string
	Score      float64
	Line       int
	Provenance Provenance
}

type Identity struct {
	RepoID    string
	SourceID  string
	AliasType string
	Alias     string
	Remote    RemoteAlias
}

type RemoteAlias struct {
	Type string
	ID   string
}

type Link struct {
	RepoID   string
	SourceID string
	TargetID string
	Kind     string
	Text     string
}

type LinkFilter struct {
	RepoID   string
	SourceID string
	TargetID string
}

type ChunkFilter struct {
	RepoID     string
	SourceID   string
	RecordID   string
	SnapshotID string
	Policy     string
}

type Chunk struct {
	RepoID            string
	ID                string
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
	Policy            string
}

type SyncEvent struct {
	RepoID         string
	ID             string
	SourceID       string
	RemoteType     string
	RemoteID       string
	RemoteRevision string
	Status         string
	IdempotencyKey string
	Message        string
	CreatedAt      time.Time
	StartedAt      time.Time
	CompletedAt    time.Time
	ZeroDelta      bool
}

type SyncStatus struct {
	RepoID         string
	SourceID       string
	RemoteType     string
	RemoteID       string
	RemoteRevision string
	Status         string
	LastFetchedAt  time.Time
}

type Conflict struct {
	RepoID        string
	ID            string
	SourceID      string
	Kind          string
	LocalPayload  string
	RemotePayload string
	CreatedAt     time.Time
}

type SourceGraph struct {
	Source              Source
	Comments            []RecordComment
	PRReviewComments    []PRReviewComment
	PRReviewDiscussions []PRReviewDiscussion
	PRReviewPositions   []PRReviewPosition
	Identities          []Identity
	Links               []Link
	Chunks              []Chunk
	SyncStatus          *SyncStatus
	SyncEvents          []SyncEvent
	Conflicts           []Conflict
}

type PRReviewCommentFilter struct {
	RepoID   string
	PRNumber int
	SourceID string
}

type PRReviewDiscussionFilter struct {
	RepoID       string
	PRNumber     int
	DiscussionID string
}

type PRReviewPositionFilter struct {
	RepoID       string
	PRNumber     int
	CommentID    string
	DiscussionID string
}
