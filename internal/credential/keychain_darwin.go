//go:build darwin

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
		Remediation: "macOS Keychain integration is not yet available. Use GITCODE_TOKEN or configure credential.store: env.",
		Available:   true,
	}
}
