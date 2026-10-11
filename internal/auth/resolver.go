package auth

import (
	"context"
	"sync"

	"gitcode-mcp/internal/config"
)

// Basic Auth (GITCODE_USER/GITCODE_PASS) is deliberately excluded from this resolver.
// Only env:GITCODE_TOKEN > system keyring priority order is supported until sanitized evidence
// proves GitCode accepts basic auth.
type Result struct {
	Present     bool
	Token       string
	Source      string
	StoreMode   string
	ErrorClass  string
	Remediation string
}

type CredentialResolver struct {
	mu       sync.Mutex
	provider config.CredentialProvider
	result   *Result
}

func NewCredentialResolver(src config.Source) *CredentialResolver {
	return &CredentialResolver{
		provider: config.DefaultCredentialProvider(src),
	}
}

func NewCredentialResolverWithProvider(provider config.CredentialProvider) *CredentialResolver {
	return &CredentialResolver{provider: provider}
}

func (r *CredentialResolver) Resolve(ctx context.Context, eff config.EffectiveConfig) Result {
	if ctx.Err() != nil {
		return cancelledResolution()
	}
	// MCP can now reach first-use credential diagnostics concurrently, without
	// an eager startup result. Serialize that first use without making a second
	// request wait behind a platform credential provider or cache transient state.
	if !r.mu.TryLock() {
		return Result{Source: "unavailable", ErrorClass: "credential-resolution-busy", Remediation: "Credential resolution is in progress; retry the explicit diagnostic or live operation."}
	}
	defer r.mu.Unlock()
	if r.result != nil {
		return *r.result
	}
	eff = normalizeEffectiveConfig(eff)
	secret, status, _ := r.provider.Resolve(ctx, eff)
	if ctx.Err() != nil {
		return cancelledResolution()
	}
	result := Result{
		Present:     status.Present,
		Token:       secret.Value(),
		Source:      status.Source,
		StoreMode:   status.StoreMode,
		ErrorClass:  status.ErrorClass,
		Remediation: status.Remediation,
	}
	r.result = &result
	return result
}

func cancelledResolution() Result {
	return Result{Source: "unavailable", ErrorClass: "credential-resolution-cancelled", Remediation: "Credential resolution was cancelled; retry only if still needed."}
}

func (r *CredentialResolver) Status(ctx context.Context, eff config.EffectiveConfig) Result {
	return r.Resolve(ctx, eff)
}

func normalizeEffectiveConfig(eff config.EffectiveConfig) config.EffectiveConfig {
	if eff.CredentialPolicy.Store == "" {
		eff.CredentialPolicy.Store = "auto"
	}
	if eff.CredentialPolicy.KeyringService == "" {
		eff.CredentialPolicy.KeyringService = "gitcode-mcp"
	}
	if eff.CredentialPolicy.KeyringAccount == "" {
		eff.CredentialPolicy.KeyringAccount = "token"
	}
	return eff
}
