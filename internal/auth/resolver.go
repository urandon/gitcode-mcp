package auth

import (
	"context"

	"gitcode-mcp/internal/config"
)

// Run plan constraint (ai/design_implementator_gitcode_mcp_live_operations_iteration_6_run_plan.yaml line 40):
// Basic Auth (GITCODE_USER/GITCODE_PASS) is deliberately excluded from this resolver.
// Only env:GITCODE_TOKEN > keychain priority order is supported until sanitized evidence
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
	if r.result != nil {
		return *r.result
	}
	eff = normalizeEffectiveConfig(eff)
	secret, status, _ := r.provider.Resolve(ctx, eff)
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

func (r *CredentialResolver) Status(ctx context.Context, eff config.EffectiveConfig) Result {
	return r.Resolve(ctx, eff)
}

func normalizeEffectiveConfig(eff config.EffectiveConfig) config.EffectiveConfig {
	if eff.CredentialPolicy.Store == "" {
		eff.CredentialPolicy.Store = "auto"
	}
	return eff
}
