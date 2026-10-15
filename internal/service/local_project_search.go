package service

import (
	"context"
	"errors"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/localgit"
	"gitcode-mcp/internal/rag"
	"gitcode-mcp/internal/repositorydocs"
)

type LocalProjectSearchRequest struct {
	Project string `json:"project"`
	Query   string `json:"query"`
	Mode    string `json:"mode,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}
type LocalProjectIndexRequest struct {
	Project   string `json:"project"`
	WriteMode string `json:"write_mode"`
	MaxChunks int    `json:"max_chunks,omitempty"`
}
type LocalProjectSearchHit struct {
	Citation  localgit.Citation `json:"citation"`
	Snippet   string            `json:"snippet"`
	LineStart int               `json:"line_start"`
	LineEnd   int               `json:"line_end"`
	Digest    string            `json:"digest"`
	Score     float64           `json:"score"`
}
type LocalProjectSearchResult struct {
	Project       string                        `json:"project"`
	ProjectID     string                        `json:"project_id"`
	Commit        string                        `json:"commit"`
	RequestedMode string                        `json:"requested_mode"`
	EffectiveMode string                        `json:"effective_mode"`
	IndexState    string                        `json:"index_state"`
	Fallback      string                        `json:"fallback,omitempty"`
	Coverage      repositorydocs.SearchCoverage `json:"coverage"`
	Hits          []LocalProjectSearchHit       `json:"hits"`
}
type LocalProjectIndexResult struct {
	Project        string `json:"project"`
	ProjectID      string `json:"project_id"`
	Commit         string `json:"commit"`
	RevisionSetID  string `json:"revision_set_id"`
	State          string `json:"state"`
	EligibleChunks int    `json:"eligible_chunks"`
	EmbeddedChunks int    `json:"embedded_chunks"`
	ReusedChunks   int    `json:"reused_chunks"`
	FailedChunks   int    `json:"failed_chunks"`
}

// WithSearchConfig has no I/O or provider construction. The local serving
// branch still starts without any cache, credential or provider dependency.
func (s *LocalProjects) WithSearchConfig(cfg config.Config) *LocalProjects {
	s.searchConfig = cfg
	return s
}

func (s *LocalProjects) Search(ctx context.Context, q LocalProjectSearchRequest) (LocalProjectSearchResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var zero LocalProjectSearchResult
	if q.Mode == "" {
		q.Mode = repositorydocs.SearchModeFullText
	}
	if q.Limit == 0 {
		q.Limit = 10
	}
	if !utf8.ValidString(q.Query) || strings.TrimSpace(q.Query) == "" || len(q.Query) > 4096 || q.Limit < 1 || q.Limit > 32 || q.Mode != "fulltext" && q.Mode != "hybrid" {
		return zero, localProjectError("invalid_argument", "search")
	}
	b, err := s.binding(q.Project)
	if err != nil {
		return zero, err
	}
	snap, err := projectSnapshot(ctx, b)
	if err != nil {
		return zero, err
	}
	repo, citations, err := snap.SearchCorpus(ctx)
	if err != nil {
		return zero, err
	}
	var store *cache.SQLiteStore
	var searchStore repositorydocs.SearchStore
	var provider rag.EmbeddingProvider
	fallback := ""
	if q.Mode == "hybrid" {
		if b.profile == "" || b.profile == "disabled" {
			fallback = "profile_disabled"
		} else if localProjectIndexPath(s.searchConfig) == "" {
			fallback = "index_unavailable"
		} else {
			store, err = cache.NewSQLiteReadOnlyStore(ctx, localProjectIndexPath(s.searchConfig))
			if err != nil {
				fallback = "index_unavailable"
			} else {
				defer store.Close()
				searchStore = store
				provider, err = s.localProjectProvider(b.profile)
				if err != nil {
					fallback = "profile_unavailable"
				}
			}
		}
	}
	req := repositorydocs.SearchRequest{RepoID: localProjectIndexScope(b), SourceRegistrationID: b.runtime.projectID, SourceRegistrationGeneration: 1, Repository: repo, Revision: snap.Commit(), Query: q.Query, Mode: q.Mode, Limit: q.Limit}
	r, err := repositorydocs.NewRetriever(searchStore, provider).Search(ctx, req)
	if err != nil && ctx.Err() == nil {
		// A broken disposable index cannot hide current Git-backed matches.
		fallback = "index_unavailable"
		req.Mode = "fulltext"
		r, err = repositorydocs.NewRetriever(nil, nil).Search(ctx, req)
	}
	if err != nil {
		return zero, localProjectSearchError(ctx)
	}
	out := LocalProjectSearchResult{Project: q.Project, ProjectID: b.runtime.projectID, Commit: snap.Commit(), RequestedMode: q.Mode, EffectiveMode: r.EffectiveMode, IndexState: "not_requested", Coverage: r.Coverage, Hits: []LocalProjectSearchHit{}}
	if q.Mode == "hybrid" {
		out.IndexState = "unavailable"
		if fallback != "" {
			out.Fallback = fallback
			out.EffectiveMode = "fulltext"
		} else if r.EffectiveMode == "hybrid" {
			out.IndexState = "ready"
		} else if r.Fallback == "revision_set_unavailable" {
			out.IndexState = "missing"
			commit, policy, _ := repositorydocs.ResolvePolicy(ctx, repo, snap.Commit(), false)
			setID := repositorydocs.NewRevisionSetIdentity(req.RepoID, req.SourceRegistrationID, 1, repo, commit, policy, "", repositorydocs.DefaultProcessingPolicy(), r.RequestedNamespaceID).ID()
			set, getErr := store.GetRepositoryDocRevisionSet(ctx, req.RepoID, setID)
			if getErr == nil {
				switch set.State {
				case cache.RepoDocSetPartial:
					out.IndexState = "partial"
				case cache.RepoDocSetBlocked:
					out.IndexState = "failed"
				default:
					out.IndexState = "unavailable"
				}
			} else if errors.Is(getErr, cache.ErrNotFound) {
				sets, _ := store.ListRepositoryDocRevisionSets(ctx, cache.RepositoryDocRevisionSetFilter{RepoID: req.RepoID, GitStoreRef: repo.GitStoreRef, NamespaceID: r.RequestedNamespaceID, Limit: 1})
				if len(sets) > 0 {
					out.IndexState = "stale"
				}
			}
			out.Fallback = "index_" + out.IndexState
		} else {
			out.Fallback = "semantic_unavailable"
		}
	}
	for _, hit := range r.Hits {
		citation, ok := citations[hit.Citation.Path]
		if !ok || citation.Blob != hit.Citation.BlobOID || citation.Commit != hit.Citation.CommitOID || math.IsNaN(hit.Score) || math.IsInf(hit.Score, 0) {
			if q.Mode != "hybrid" {
				return zero, localProjectError("invalid_response", "citation")
			}
			req.Mode = "fulltext"
			r, err = repositorydocs.NewRetriever(nil, nil).Search(ctx, req)
			if err != nil {
				return zero, localProjectSearchError(ctx)
			}
			out.EffectiveMode, out.IndexState, out.Fallback, out.Coverage = "fulltext", "unavailable", "index_unavailable", r.Coverage
			break
		}
	}
	for _, hit := range r.Hits {
		citation, ok := citations[hit.Citation.Path]
		if !ok || citation.Blob != hit.Citation.BlobOID || citation.Commit != hit.Citation.CommitOID {
			return zero, localProjectError("invalid_response", "citation")
		}
		out.Hits = append(out.Hits, LocalProjectSearchHit{Citation: citation, Snippet: hit.Snippet, LineStart: hit.Citation.LineStart, LineEnd: hit.Citation.LineEnd, Digest: hit.Citation.RawSliceDigest, Score: hit.Score})
	}
	return out, nil
}

func localProjectIndexScope(b localProjectBinding) string {
	return "local-project/" + b.runtime.projectID
}

// Keep metadata scope rows out of the remote repository-binding cache. This
// uses the same disposable SQLite schema/engine, not a new storage authority.
func localProjectIndexPath(cfg config.Config) string {
	if cfg.CachePath == "" || cfg.CachePath == ":memory:" || strings.HasPrefix(cfg.CachePath, "file:") {
		return ""
	}
	return cfg.CachePath + ".local-project-index.db"
}

func (s *LocalProjects) Index(ctx context.Context, q LocalProjectIndexRequest) (LocalProjectIndexResult, error) {
	return s.indexAt(ctx, q, "")
}

func (s *LocalProjects) indexAt(ctx context.Context, q LocalProjectIndexRequest, expectedCommit string) (LocalProjectIndexResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var zero LocalProjectIndexResult
	if q.MaxChunks == 0 {
		q.MaxChunks = 64
	}
	if q.WriteMode != "local" || q.MaxChunks < 1 || q.MaxChunks > 128 {
		return zero, localProjectError("invalid_argument", "index")
	}
	b, err := s.binding(q.Project)
	if err != nil {
		return zero, err
	}
	if b.readOnly {
		return zero, localProjectError("read_only", "project")
	}
	provider, err := s.localProjectProvider(b.profile)
	if err != nil {
		return zero, localProjectError("invalid_configuration", "rag_profile")
	}
	snap, err := projectSnapshot(ctx, b)
	if err != nil {
		return zero, err
	}
	if expectedCommit != "" && snap.Commit() != expectedCommit {
		return zero, localProjectError("stale_plan", "commit")
	}
	repo, _, err := snap.SearchCorpus(ctx)
	if err != nil {
		return zero, err
	}
	if localProjectIndexPath(s.searchConfig) == "" {
		return zero, localProjectError("invalid_configuration", "cache")
	}
	if os.MkdirAll(filepath.Dir(localProjectIndexPath(s.searchConfig)), 0700) != nil {
		return zero, localProjectSearchError(ctx)
	}
	store, err := cache.NewSQLiteStore(ctx, localProjectIndexPath(s.searchConfig))
	if err != nil {
		return zero, localProjectSearchError(ctx, err)
	}
	defer store.Close()
	lease, err := store.AcquireWriter(ctx, cache.WriterRequest{Operation: "local-project-index-scope", RepoID: localProjectIndexScope(b)})
	if err != nil {
		return zero, localProjectSearchError(ctx, err)
	}
	_, scopeErr := store.GetRepository(ctx, localProjectIndexScope(b))
	if errors.Is(scopeErr, cache.ErrNotFound) {
		scopeErr = store.AddRepository(ctx, cache.RepositoryBinding{RepoID: localProjectIndexScope(b), Scopes: []cache.RepositoryScope{}})
	}
	releaseErr := store.ReleaseWriter(context.WithoutCancel(ctx), lease)
	if scopeErr != nil || releaseErr != nil {
		return zero, localProjectSearchError(ctx, scopeErr, releaseErr)
	}
	checkpoints, err := repositorydocs.NewFileVectorCheckpointStore(localProjectIndexPath(s.searchConfig) + ".repository-doc-vector-checkpoints")
	if err != nil {
		return zero, localProjectSearchError(ctx)
	}
	if _, err = checkpoints.Prune(ctx, repositorydocs.VectorCheckpointRetentionPolicy{MaxAge: 7 * 24 * time.Hour, MaxBytes: 512 << 20}); err != nil {
		return zero, localProjectSearchError(ctx)
	}
	r, err := repositorydocs.NewIndexer(store, provider).WithVectorCheckpointStore(checkpoints).Run(ctx, repositorydocs.IndexRequest{RepoID: localProjectIndexScope(b), SourceRegistrationID: b.runtime.projectID, SourceRegistrationGeneration: 1, Repository: repo, Revision: snap.Commit(), MaxChunks: q.MaxChunks, BatchSize: 16})
	if err != nil {
		return zero, localProjectSearchError(ctx, err)
	}
	lease, err = store.AcquireWriter(ctx, cache.WriterRequest{Operation: "local-project-index-retention", RepoID: localProjectIndexScope(b)})
	if err != nil {
		return zero, localProjectSearchError(ctx, err)
	}
	now := time.Now().UTC()
	_, gcErr := store.PruneRepositoryDocRevisionSets(ctx, localProjectIndexScope(b), cache.RepositoryDocRetentionPolicy{RetainCommittedPerIdentity: 8, CommittedCutoff: now.Add(-30 * 24 * time.Hour), TerminalCutoff: now.Add(-7 * 24 * time.Hour), MaxVectorBytes: 512 << 20, ProtectedSetIDs: []string{r.RevisionSetID}})
	releaseErr = store.ReleaseWriter(context.WithoutCancel(ctx), lease)
	if gcErr != nil || releaseErr != nil {
		return zero, localProjectSearchError(ctx, gcErr, releaseErr)
	}
	return LocalProjectIndexResult{Project: q.Project, ProjectID: b.runtime.projectID, Commit: snap.Commit(), RevisionSetID: r.RevisionSetID, State: r.State, EligibleChunks: r.EligibleChunks, EmbeddedChunks: r.EmbeddedChunks, ReusedChunks: r.ReusedChunks, FailedChunks: r.FailedChunks}, nil
}

func localProjectSearchError(ctx context.Context, causes ...error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for _, cause := range causes {
		var contention cache.ErrLockContention
		if errors.As(cause, &contention) {
			return localProjectError("writer_busy", "index")
		}
	}
	return localProjectError("unavailable", "index")
}

func (s *LocalProjects) localProjectProvider(name string) (rag.EmbeddingProvider, error) {
	profile, ok := s.searchConfig.RAG.Profiles[name]
	provider, providerOK := s.searchConfig.RAG.Providers[profile.Provider]
	if !ok || !providerOK || name == "" || name == "disabled" || profile.Dimensions < 1 || profile.Dimensions > 16384 || provider.DataBoundary != "local_process" && provider.DataBoundary != "local_network" {
		return nil, localProjectError("invalid_configuration", "rag_profile")
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: localProjectEmbeddingTransport{}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return rag.NewEmbeddingProviderFromConfig(s.searchConfig, name, rag.ProviderOptions{HTTPClient: client})
}

type localProjectEmbeddingTransport struct{}

var localProjectEmbeddingHTTP = &http.Transport{Proxy: nil, MaxIdleConns: 4, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second}

func (localProjectEmbeddingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r, err := localProjectEmbeddingHTTP.RoundTrip(req)
	if err == nil {
		r.Body = http.MaxBytesReader(nil, r.Body, 4<<20)
	}
	return r, err
}
