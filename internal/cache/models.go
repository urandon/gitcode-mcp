package cache

import (
	"context"
	"time"
)

type Store interface {
	UpsertSourceGraph(context.Context, SourceGraph) error
	UpsertSource(context.Context, Source) error
	GetSource(context.Context, string) (Source, error)
	ListSources(context.Context, SourceFilter) ([]Source, error)
	SearchSources(context.Context, SearchQuery) ([]SearchResult, error)
	UpsertIdentity(context.Context, Identity) error
	GetIdentityMap(context.Context, string) ([]Identity, error)
	ResolveAlias(context.Context, RemoteAlias) (Identity, error)
	UpsertLink(context.Context, Link) error
	ListLinks(context.Context, LinkFilter) ([]Link, error)
	GetBacklinks(context.Context, string) ([]Source, error)
	UpsertChunk(context.Context, Chunk) (Chunk, error)
	GetChunks(context.Context, string) ([]Chunk, error)
	RecordSyncEvent(context.Context, SyncEvent) error
	GetSyncEventByKey(context.Context, string) (*SyncEvent, error)
	GetSyncStatus(context.Context, string) (SyncStatus, error)
	UpsertConflict(context.Context, Conflict) error
	GetConflicts(context.Context, string) ([]Conflict, error)
	IntegrityCheck(context.Context) error
	AcquireLock(context.Context, string) (*LockHandle, error)
	ReleaseLock(context.Context, *LockHandle) error
	Close() error
}

type Source struct {
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
	Kind   string
	Status string
	Limit  int
}

type SearchQuery struct {
	Query string
	Kind  string
	Limit int
}

type SearchResult struct {
	ID      string
	Path    string
	Title   string
	Snippet string
	Score   float64
	Line    int
}

type Identity struct {
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
	SourceID string
	TargetID string
	Kind     string
	Text     string
}

type LinkFilter struct {
	SourceID string
	TargetID string
}

type Chunk struct {
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
	SourceID       string
	RemoteType     string
	RemoteID       string
	RemoteRevision string
	Status         string
	LastFetchedAt  time.Time
}

type Conflict struct {
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
