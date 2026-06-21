package credential

import (
	"context"

	"gitcode-mcp/internal/config"
)

func DefaultPipeline(src config.Source) *Pipeline {
	providers := []Provider{
		&EnvProvider{Source: src},
		&KeychainProvider{},
		&NoneProvider{},
	}
	return NewPipeline(providers)
}

type NoneProvider struct{}

func (p *NoneProvider) Name() string { return "none" }

func (p *NoneProvider) Probe(ctx context.Context) Status {
	return Status{
		Source:      "none",
		Present:     false,
		StoreMode:   "none",
		Available:   true,
		ErrorClass:  "token-missing",
		Remediation: "No credential source available. Set GITCODE_TOKEN or configure a credential store.",
	}
}

func (p *NoneProvider) Token(ctx context.Context) ResolvedToken {
	return ResolvedToken{}
}
