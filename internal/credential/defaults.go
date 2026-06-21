package credential

import (
	"gitcode-mcp/internal/config"
)

func DefaultPipeline(src config.Source) *Pipeline {
	providers := []Provider{
		&EnvProvider{Source: src},
		&KeychainProvider{},
	}
	return NewPipeline(providers)
}
