//go:build !darwin

package credential

import (
	"context"
)

type KeychainProvider struct{}

func (p *KeychainProvider) Name() string {
	return "keychain"
}

func (p *KeychainProvider) Probe(ctx context.Context) Status {
	return Status{
		Source:      "keychain",
		Present:     false,
		StoreMode:   "keychain",
		ErrorClass:  "credential-store-unavailable",
		Remediation: "Keychain is only available on macOS. Use GITCODE_TOKEN or configure credential.store: env.",
		Available:   false,
	}
}

func (p *KeychainProvider) Token(ctx context.Context) ResolvedToken {
	return ResolvedToken{}
}
