package rag

import (
	"context"
	"fmt"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
)

type OperationStore interface {
	ResolveEmbeddingNamespace(context.Context, cache.EmbeddingNamespaceIdentity) (cache.EmbeddingNamespace, bool, error)
	ListChunks(context.Context, cache.ChunkFilter) ([]cache.Chunk, error)
	ListChunkEmbeddings(context.Context, cache.ChunkEmbeddingFilter) ([]cache.ChunkEmbedding, error)
	ListRAGIndexRuns(context.Context, cache.RAGIndexRunFilter) ([]cache.RAGIndexRun, error)
	GetRepoContentState(context.Context, string) (cache.RepoContentState, error)
	GetRAGCoverageState(context.Context, string, string) (cache.RAGCoverageState, bool, error)
	GetSourceScoped(context.Context, string, string) (cache.Source, error)
}

type ServiceStateFunc func(context.Context, string) (*ServiceStatus, *JobStatus)

type Operations struct {
	store           OperationStore
	config          config.Config
	providerOptions ProviderOptions
	serviceState    ServiceStateFunc
}

type OperationsOptions struct {
	ProviderOptions ProviderOptions
	ServiceState    ServiceStateFunc
}

func NewOperations(store OperationStore, cfg config.Config, opts OperationsOptions) Operations {
	return Operations{store: store, config: cfg, providerOptions: opts.ProviderOptions, serviceState: opts.ServiceState}
}

func (o Operations) Status(ctx context.Context, req StatusRequest) (StatusResult, error) {
	if o.store == nil {
		return StatusResult{}, fmt.Errorf("rag operations: cache store is required")
	}
	if o.serviceState != nil && req.Service == nil && req.ActiveJob == nil {
		req.Service, req.ActiveJob = o.serviceState(ctx, req.RepoID)
	}
	provider, err := NewEmbeddingProviderFromConfig(o.config, req.ProfileID, o.providerOptions)
	if err != nil {
		return StatusResult{}, err
	}
	return Status(ctx, o.store, provider, req)
}

func (o Operations) Search(ctx context.Context, req SearchRequest) (SearchResult, error) {
	if o.store == nil {
		return SearchResult{}, fmt.Errorf("rag operations: cache store is required")
	}
	provider, err := NewEmbeddingProviderFromConfig(o.config, req.ProfileID, o.providerOptions)
	if err != nil {
		return SearchResult{}, err
	}
	return NewRAGRetriever(o.store, provider, RAGRetrieverOptions{}).Search(ctx, req)
}
