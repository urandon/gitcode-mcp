package servicectl

import (
	"context"
	"sort"
	"strings"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/rag"
	"gitcode-mcp/internal/repositorydocs"
)

type RepositoryDocsQueryRequest struct {
	RepositoryDocsSourceSelector
	RepoID          string `json:"repo_id"`
	CachePath       string `json:"cache_path,omitempty"`
	Revision        string `json:"revision,omitempty"`
	IncludeWorktree bool   `json:"include_worktree,omitempty"`
	Query           string `json:"query,omitempty"`
	Mode            string `json:"mode,omitempty"`
	Limit           int    `json:"limit,omitempty"`
}

type RegisterRepositoryDocsSourceRequest struct {
	RepoID         string `json:"repo_id"`
	RepositoryPath string `json:"repository_path"`
	Profile        string `json:"profile,omitempty"`
	CachePath      string `json:"cache_path,omitempty"`
}

type RepositoryDocsSourceListRequest struct {
	RepoID    string `json:"repo_id"`
	CachePath string `json:"cache_path,omitempty"`
}

type RepositoryDocsSourceListResult struct {
	RepoID         string                           `json:"repo_id"`
	RegistrationID string                           `json:"registration_id"`
	Enabled        bool                             `json:"enabled"`
	Sources        []RepositoryDocsMaintenanceState `json:"sources"`
}

func (s RPCServer) repositoryDocsPolicy(ctx context.Context, req RepositoryDocsQueryRequest) (repositorydocs.PolicyResult, error) {
	source, repo, err := s.repositoryDocsQuerySource(ctx, req)
	if err != nil {
		return repositorydocs.PolicyResult{}, err
	}
	return repositorydocs.InspectPolicy(ctx, repo, repositorydocs.PolicyRequest{
		RepoID: source.RepoID, RegistrationID: source.RegistrationID, SourceRegistrationID: source.SourceRegistrationID,
		SourceRegistrationGeneration: source.SourceRegistrationGeneration, Revision: req.Revision, IncludeWorktree: req.IncludeWorktree,
	})
}

func (s RPCServer) repositoryDocsPlan(ctx context.Context, req RepositoryDocsQueryRequest) (repositorydocs.PlanResult, error) {
	source, repo, err := s.repositoryDocsQuerySource(ctx, req)
	if err != nil {
		return repositorydocs.PlanResult{}, err
	}
	return repositorydocs.InspectPlan(ctx, repo, repositorydocs.PlanRequest{
		RepoID: source.RepoID, RegistrationID: source.RegistrationID, SourceRegistrationID: source.SourceRegistrationID,
		SourceRegistrationGeneration: source.SourceRegistrationGeneration, Revision: req.Revision, IncludeWorktree: req.IncludeWorktree,
	})
}

func (s RPCServer) repositoryDocsStatus(ctx context.Context, req RepositoryDocsQueryRequest) (repositorydocs.StatusResult, error) {
	source, repo, err := s.repositoryDocsQuerySource(ctx, req)
	if err != nil {
		return repositorydocs.StatusResult{}, err
	}
	store, err := cache.NewSQLiteReadOnlyStore(ctx, source.CachePath)
	if err != nil {
		return repositorydocs.StatusResult{}, RepositoryDocsSourceUnavailableError{code: "repository_docs_cache_unavailable"}
	}
	defer store.Close()
	return repositorydocs.InspectStatus(ctx, store, repo, repositorydocs.StatusRequest{
		RepoID: source.RepoID, RegistrationID: source.RegistrationID, SourceRegistrationID: source.SourceRegistrationID,
		SourceRegistrationGeneration: source.SourceRegistrationGeneration, Revision: req.Revision, IncludeWorktree: req.IncludeWorktree,
	})
}

func (s RPCServer) repositoryDocsSearch(ctx context.Context, req RepositoryDocsQueryRequest) (repositorydocs.SearchResult, error) {
	source, repo, err := s.repositoryDocsQuerySource(ctx, req)
	if err != nil {
		return repositorydocs.SearchResult{}, err
	}
	store, err := cache.NewSQLiteReadOnlyStore(ctx, source.CachePath)
	if err != nil {
		return repositorydocs.SearchResult{}, RepositoryDocsSourceUnavailableError{code: "repository_docs_cache_unavailable"}
	}
	defer store.Close()
	var provider rag.EmbeddingProvider
	mode := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(req.Mode)), "_", "")
	if mode == "" || mode == repositorydocs.SearchModeHybrid {
		provider, _ = rag.NewEmbeddingProviderFromConfig(source.Config, source.Profile, rag.ProviderOptions{})
	}
	return repositorydocs.NewRetriever(store, provider).Search(ctx, repositorydocs.SearchRequest{
		RepoID: source.RepoID, RegistrationID: source.RegistrationID, SourceRegistrationID: source.SourceRegistrationID,
		SourceRegistrationGeneration: source.SourceRegistrationGeneration, Repository: repo, Revision: req.Revision,
		IncludeWorktree: req.IncludeWorktree, Query: req.Query, Mode: mode, Limit: req.Limit,
	})
}

func (s RPCServer) repositoryDocsQuerySource(ctx context.Context, req RepositoryDocsQueryRequest) (repositoryDocsAdminSource, *repositorydocs.Repository, error) {
	if s.Maintenance == nil {
		return repositoryDocsAdminSource{}, nil, RepositoryDocsSourceUnavailableError{code: "repository_docs_registration_unavailable"}
	}
	source, err := s.repositoryDocsSourceForRequest(ctx, req.RepoID, req.CachePath, req.RepositoryDocsSourceSelector)
	if err != nil {
		return repositoryDocsAdminSource{}, nil, err
	}
	if !repositoryDocsSourceMatchesRepo(ctx, source, req.RepoID) {
		return repositoryDocsAdminSource{}, nil, RepositoryDocsSourceUnavailableError{code: "repository_docs_source_repo_conflict"}
	}
	repo, err := repositorydocs.OpenRepository(ctx, source.RepositoryPath)
	if err != nil {
		return repositoryDocsAdminSource{}, nil, RepositoryDocsSourceUnavailableError{}
	}
	return source, repo, nil
}

func (s RPCServer) repositoryDocsSourceForRequest(ctx context.Context, repoID, cachePath string, selector RepositoryDocsSourceSelector) (repositoryDocsAdminSource, error) {
	if s.Maintenance == nil {
		return repositoryDocsAdminSource{}, RepositoryDocsSourceUnavailableError{code: "repository_docs_registration_unavailable"}
	}
	if err := validateRepositoryDocsSourceSelector(selector); err != nil {
		return repositoryDocsAdminSource{}, err
	}
	if strings.TrimSpace(selector.RegistrationID) != "" {
		source, err := s.Maintenance.repositoryDocsSourceForSelector(selector)
		if err != nil {
			return repositoryDocsAdminSource{}, err
		}
		if !repositoryDocsSourceMatchesRepo(ctx, source, repoID) {
			return repositoryDocsAdminSource{}, RepositoryDocsSourceUnavailableError{code: "repository_docs_source_repo_conflict"}
		}
		return source, nil
	}
	cacheUUID, canonicalRepoID, err := s.repositoryDocsCacheBinding(ctx, repoID, cachePath)
	if err != nil {
		return repositoryDocsAdminSource{}, err
	}
	return s.Maintenance.repositoryDocsSourceForCacheRepo(cacheUUID, canonicalRepoID)
}

func validateRepositoryDocsSourceSelector(selector RepositoryDocsSourceSelector) error {
	hasRegistration := strings.TrimSpace(selector.RegistrationID) != ""
	hasSource := strings.TrimSpace(selector.SourceRegistrationID) != ""
	hasGeneration := selector.SourceRegistrationGeneration > 0
	allOmitted := !hasRegistration && !hasSource && selector.SourceRegistrationGeneration == 0
	complete := hasRegistration && hasSource && hasGeneration
	if !allOmitted && !complete {
		return RepositoryDocsSourceUnavailableError{code: "repository_docs_source_selector_required"}
	}
	return nil
}

func (s RPCServer) repositoryDocsSources(ctx context.Context, req RepositoryDocsSourceListRequest) (RepositoryDocsSourceListResult, error) {
	if s.Maintenance == nil {
		return RepositoryDocsSourceListResult{}, RepositoryDocsSourceUnavailableError{code: "repository_docs_registration_unavailable"}
	}
	cacheUUID, canonicalRepoID, err := s.repositoryDocsCacheBinding(ctx, req.RepoID, req.CachePath)
	if err != nil {
		return RepositoryDocsSourceListResult{}, err
	}
	return s.Maintenance.repositoryDocsSourcesForCacheRepo(cacheUUID, canonicalRepoID)
}

func (s RPCServer) repositoryDocsCacheBinding(ctx context.Context, repoID, cachePath string) (string, string, error) {
	eff, err := effectiveJobConfig(s.Manager, cachePath)
	if err != nil {
		return "", "", RepositoryDocsSourceUnavailableError{code: "repository_docs_configuration_unavailable"}
	}
	store, err := cache.NewSQLiteReadOnlyStore(ctx, eff.Config.CachePath)
	if err != nil {
		return "", "", RepositoryDocsSourceUnavailableError{code: "repository_docs_cache_unavailable"}
	}
	binding, bindingErr := store.ResolveRepositoryBinding(ctx, strings.TrimSpace(repoID))
	identity, identityErr := store.CacheIdentity(ctx)
	store.Close()
	if bindingErr != nil {
		return "", "", RepositoryDocsSourceUnavailableError{code: "repository_docs_binding_unavailable"}
	}
	if identityErr != nil {
		return "", "", RepositoryDocsSourceUnavailableError{code: "repository_docs_cache_unavailable"}
	}
	return identity.UUID, binding.RepoID, nil
}

func (m *MaintenanceManager) repositoryDocsSourcesForCacheRepo(cacheUUID, repoID string) (RepositoryDocsSourceListResult, error) {
	m.mu.Lock()
	entry := m.maintenanceEntryForCacheRepoLocked(strings.TrimSpace(cacheUUID), strings.TrimSpace(repoID))
	if entry == nil {
		m.mu.Unlock()
		return RepositoryDocsSourceListResult{}, RepositoryDocsSourceUnavailableError{code: "repository_docs_registration_not_found"}
	}
	publicEntry := cloneMaintenanceEntry(entry)
	m.mu.Unlock()
	sources := append([]RepositoryDocsMaintenanceState(nil), publicEntry.RepositoryDocsSources...)
	sort.Slice(sources, func(i, j int) bool { return sources[i].SourceRegistrationID < sources[j].SourceRegistrationID })
	return RepositoryDocsSourceListResult{RepoID: publicEntry.RepoID, RegistrationID: publicEntry.RegistrationID, Enabled: publicEntry.Enabled, Sources: sources}, nil
}

func repositoryDocsSourceMatchesRepo(ctx context.Context, source repositoryDocsAdminSource, requested string) bool {
	requested = strings.TrimSpace(requested)
	if requested == "" || requested == source.RepoID {
		return true
	}
	store, err := cache.NewSQLiteReadOnlyStore(ctx, source.CachePath)
	if err != nil {
		return false
	}
	defer store.Close()
	binding, err := store.ResolveRepositoryBinding(ctx, requested)
	return err == nil && binding.RepoID == source.RepoID
}

func (s RPCServer) registerRepositoryDocsSource(ctx context.Context, req RegisterRepositoryDocsSourceRequest) (MaintenanceEntry, error) {
	if s.Maintenance == nil {
		return MaintenanceEntry{}, RepositoryDocsSourceUnavailableError{code: "repository_docs_registration_unavailable"}
	}
	eff, err := effectiveJobConfig(s.Manager, req.CachePath)
	if err != nil {
		return MaintenanceEntry{}, RepositoryDocsSourceUnavailableError{code: "repository_docs_configuration_unavailable"}
	}
	effectiveProfile := strings.TrimSpace(req.Profile)
	if effectiveProfile == "" {
		effectiveProfile = strings.TrimSpace(eff.Config.RAG.Indexing.Profile)
	}
	if effectiveProfile == "" {
		effectiveProfile = strings.TrimSpace(eff.Config.RAG.DefaultProfile)
	}
	store, err := cache.NewSQLiteReadOnlyStore(ctx, eff.Config.CachePath)
	if err != nil {
		return MaintenanceEntry{}, RepositoryDocsSourceUnavailableError{code: "repository_docs_cache_unavailable"}
	}
	binding, err := store.ResolveRepositoryBinding(ctx, strings.TrimSpace(req.RepoID))
	if err != nil {
		store.Close()
		return MaintenanceEntry{}, RepositoryDocsSourceUnavailableError{code: "repository_docs_binding_unavailable"}
	}
	identity, err := store.CacheIdentity(ctx)
	store.Close()
	if err != nil {
		return MaintenanceEntry{}, RepositoryDocsSourceUnavailableError{code: "repository_docs_cache_unavailable"}
	}
	entry, _, err := s.Maintenance.RegisterRepositoryDocsSource(ctx, identity.UUID, binding.RepoID, req.RepositoryPath, effectiveProfile)
	return entry, err
}
