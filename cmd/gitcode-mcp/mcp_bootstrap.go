package main

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"gitcode-mcp/internal/auth"
	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/capability"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/mcp"
	"gitcode-mcp/internal/observability"
	"gitcode-mcp/internal/servicectl"
)

// Only an explicitly requested cache/provider tool opens this store. Observation
// and discovery never take mu, so a blocked bootstrap cannot block diagnosis.
type mcpBootstrap struct {
	active     sync.WaitGroup
	mu         sync.Mutex
	deps       StartupDeps
	store      *cache.SQLiteStore
	closed     bool
	diagnostic mcp.StartupDiagnostic
}

func newMCPBootstrap(deps StartupDeps) *mcpBootstrap { return &mcpBootstrap{deps: deps} }
func newMCPHTTPHandler(deps StartupDeps) (*mcp.RPCHandler, *mcpBootstrap) {
	b := newMCPBootstrap(deps)
	h := mcp.NewRPCHandlerWithCredentialResolverAndToolAccess(nil, deps.CredentialResolver, mcp.ToolAccess(deps.Config.MCPToolAccess))
	h.SetToolBootstrap(b.ForTool)
	h.SetBootstrapDiagnostic(b.Diagnostic)
	h.SetServiceClient(b.ServiceClient)
	h.SetRuntimeContext(mcpRuntimeContext(deps))
	setMCPMaintenanceProviders(h.SetMaintenanceProviders, deps)
	return h, b
}
func (b *mcpBootstrap) ServiceClient() (*servicectl.RPCClient, error) {
	return (servicectl.Manager{Source: b.deps.Source, RuntimeDir: b.deps.Config.Service.RuntimeDir}).Client()
}
func (b *mcpBootstrap) Close() {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	b.active.Wait()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.store != nil {
		_ = b.store.Close()
		b.store = nil
	}
}
func (b *mcpBootstrap) Readiness(ctx context.Context) mcp.Readiness {
	if ctx.Err() != nil {
		return mcp.Readiness{Ready: false, Code: "operation_cancelled", Message: "readiness observation was cancelled"}
	}
	if !b.mu.TryLock() {
		return mcp.Readiness{Ready: true, Code: "bootstrap_busy", Message: "transport is available; a requested tool bootstrap is in progress"}
	}
	defer b.mu.Unlock()
	if b.closed {
		return mcp.Readiness{Ready: false, Code: "service_unavailable", Message: "MCP transport is closing"}
	}
	if b.diagnostic.ErrorClass != "" {
		return mcp.Readiness{Ready: true, Code: b.diagnostic.ErrorClass, Message: "transport and daemon observation remain available; requested cache tools are degraded"}
	}
	return mcp.Readiness{Ready: true, Code: "transport_ready", Message: "transport is available; cache/provider readiness is evaluated only for a requested tool"}
}

func mcpToolRequiresLive(name string) bool {
	if name == "sync_live" {
		return true
	}
	cap, ok := capability.LookupByMCPName(name)
	return ok && cap.Category == capability.CategoryWrite && cap.ID != "feedback_status" && cap.ID != "prepare_feedback"
}

func (b *mcpBootstrap) ForTool(ctx context.Context, name string, _ json.RawMessage) (*mcp.RPCHandler, error) {
	ctx, cancel := context.WithTimeout(ctx, observability.ReadBudget)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !b.mu.TryLock() {
		return nil, servicectl.RPCDomainError{Code: "cache_bootstrap_busy", Message: "requested cache tool bootstrap is busy; retry the tool"}
	}
	defer b.mu.Unlock()
	if b.closed {
		return nil, servicectl.RPCDomainError{Code: "service_unavailable", Message: "MCP transport is closing"}
	}
	deps := b.deps
	deps.GitCode.Live = false
	deps.GitCode.Token = ""
	deps.GitCode.token = ""
	liveRequired := mcpToolRequiresLive(name)
	liveReadiness := name == "feedback_status" && !deps.GitCode.Offline
	if liveRequired || liveReadiness {
		if deps.GitCode.Offline {
			return nil, servicectl.RPCDomainError{Code: "live_disabled", Message: "requested live operation is unavailable in explicit offline mode"}
		}
		eff, err := config.LoadEffective(deps.Source, config.Overrides{})
		if err != nil {
			return nil, servicectl.RPCDomainError{Code: "configuration_error", Message: "effective configuration cannot be read for the requested live operation"}
		}
		// Per-request resolver avoids sharing mutable credential results between
		// concurrent explicit auth_status and provider operations.
		credential := auth.NewCredentialResolver(deps.Source).Resolve(ctx, eff)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if liveRequired && (!credential.Present || strings.TrimSpace(credential.Token) == "") {
			return nil, servicectl.RPCDomainError{Code: "credential_unavailable", Message: "requested live operation needs a configured GitCode credential"}
		}
		deps.GitCode.Live = credential.Present && strings.TrimSpace(credential.Token) != ""
		deps.GitCode.Token = credential.Token
		deps.GitCode.token = credential.Token
	}
	if b.store == nil {
		if err := ensureParentDir(deps.Config.CachePath); err != nil {
			return nil, b.fail(err)
		}
		store, err := cache.NewSQLiteStore(ctx, deps.Config.CachePath)
		if err != nil {
			return nil, b.fail(err)
		}
		b.store = store
		b.diagnostic = mcp.StartupDiagnostic{}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	svc, err := resolveService(b.store, deps)
	if err != nil {
		return nil, b.fail(err)
	}
	h := mcp.NewRPCHandlerWithCredentialResolverAndToolAccess(svc, deps.CredentialResolver, mcp.ToolAccess(deps.Config.MCPToolAccess))
	h.SetRAGStatusProvider(newMCPRAGStatusProvider(b.store, deps))
	h.SetRAGSearchProvider(newMCPRAGSearchProvider(b.store, deps))
	setMCPRepositoryDocsProviders(h.SetRepositoryDocsProviders, b.store, deps)
	b.active.Add(1)
	var release sync.Once
	h.SetToolRelease(func() { release.Do(b.active.Done) })
	return h, nil
}

func (b *mcpBootstrap) Diagnostic() mcp.StartupDiagnostic {
	if !b.mu.TryLock() {
		return mcp.StartupDiagnostic{}
	}
	defer b.mu.Unlock()
	return b.diagnostic
}
func (b *mcpBootstrap) fail(err error) error {
	b.diagnostic = mcp.StartupDiagnosticFromError(err)
	return mcp.BootstrapError{Diagnostic: b.diagnostic}
}
