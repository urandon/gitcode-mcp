package credential

import (
	"context"
	"strings"

	"gitcode-mcp/internal/config"
)

const (
	EnvToken = "GITCODE_TOKEN"
)

type EnvProvider struct {
	Source config.Source
}

func (p *EnvProvider) Name() string {
	return "env:GITCODE_TOKEN"
}

func (p *EnvProvider) Probe(ctx context.Context) Status {
	src := p.Source
	if src == nil {
		src = config.OSSource{}
	}
	value := strings.TrimSpace(src.Env(EnvToken))
	if value == "" {
		return Status{
			Source:      "env:GITCODE_TOKEN",
			Present:     false,
			StoreMode:   "auto",
			ErrorClass:  "token-missing",
			Remediation: "Set GITCODE_TOKEN environment variable.",
			Available:   true,
		}
	}
	return Status{
		Source:    "env:GITCODE_TOKEN",
		Present:   true,
		StoreMode: "auto",
		Available: true,
	}
}
