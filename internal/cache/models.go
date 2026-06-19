package cache

import (
	"context"
	"time"
)

type Store interface {
	AddRepository(context.Context, RepositoryBinding) error
	GetRepository(context.Context, string) (RepositoryBinding, error)
	ListRepositories(context.Context) ([]RepositoryBinding, error)
	UpsertSourceGraph(context.Context, SourceGraph) error
	UpsertSource(context.Context, Source) error
	GetSource(context.Context, string) (Source, error)
	GetSourceScoped(context.Context, string, string) (Source, error)
	ListSources(context.Context, SourceFilter) ([]Source, error)
	SearchSources(context.Context, SearchQuery) ([]SearchResult, error)
	UpsertIdentity(context.Context, Identity) error
	GetIdentityMap(context.Context, string) ([]Identity, error)
	GetIdentityMapScoped(context.Context, string, string) ([]Identity, error)
	ResolveAlias(context.Context, RemoteAlias) (Identity, error)
	ResolveAliasScoped(context.Context, string, RemoteAlias) (Identity, error)
	DiagnoseAlias(context.Context, RemoteAlias) ([]Identity, error)
	UpsertLink(context.Context, Link) error
	ListLinks(context.Context, LinkFilter) ([]Link, error)
	GetBacklinks(context.Context, string) ([]Source, error)
	GetBacklinksScoped(context.Context, string, string) ([]Source, error)
	UpsertChunk(context.Context, Chunk) (Chunk, error)
	GetChunks(context.Context, string) ([]Chunk, error)
	GetChunksScoped(context.Context, string, string) ([]Chunk, error)
	RecordSyncEvent(context.Context, SyncEvent) error
	GetSyncEventByKey(context.Context, string) (*SyncEvent, error)
	GetSyncStatus(context.Context, string) (SyncStatus, error)
	GetSyncStatusScoped(context.Context, string, string) (SyncStatus, error)
	UpsertConflict(context.Context, Conflict) error
	GetConflicts(context.Context, string) ([]Conflict, error)
	IntegrityCheck(context.Context) error
	AcquireLock(context.Context, string) (*LockHandle, error)
	ReleaseLock(context.Context, *LockHandle) error
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
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Aliases     []Identity
}

type SourceFilter struct {
	RepoID string
	Kind   string
	Status string
	Limit  int
}

type SearchQuery struct {
	RepoID string
	Query  string
	Kind   string
	Limit  int
}

type SearchResult struct {
	RepoID  string
	ID      string
	Path    string
	Title   string
	Snippet string
	Score   float64
	Line    int
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

type Chunk struct {
	RepoID            string
	ID                string
	SourceID          string
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
	Source     Source
	Identities []Identity
	Links      []Link
	Chunks     []Chunk
	SyncStatus *SyncStatus
	SyncEvents []SyncEvent
	Conflicts  []Conflict
}
