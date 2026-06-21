//go:build darwin

package credential

import (
	"context"
	"errors"

	"github.com/zalando/go-keyring"
)

const (
	keychainService = "gitcode-mcp"
	keychainUser    = "token"
)

type KeychainProvider struct{}

func (p *KeychainProvider) Name() string {
	return "keychain"
}

func (p *KeychainProvider) Probe(ctx context.Context) Status {
	token, err := keyring.Get(keychainService, keychainUser)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return Status{
				Source:      "keychain",
				Present:     false,
				StoreMode:   "keychain",
				Available:   true,
				ErrorClass:  "token-missing",
				Remediation: "No token found in macOS Keychain. Use GITCODE_TOKEN or store a token with: security add-generic-password -s gitcode-mcp -a token -w <TOKEN>",
			}
		}
		return Status{
			Source:      "keychain",
			Present:     false,
			StoreMode:   "keychain",
			ErrorClass:  "credential-store-unavailable",
			Remediation: "macOS Keychain access failed: " + err.Error(),
			Available:   true,
		}
	}
	if token == "" {
		return Status{
			Source:      "keychain",
			Present:     false,
			StoreMode:   "keychain",
			Available:   true,
			ErrorClass:  "token-missing",
			Remediation: "No token found in macOS Keychain. Use GITCODE_TOKEN or store a token with: security add-generic-password -s gitcode-mcp -a token -w <TOKEN>",
		}
	}
	return Status{
		Source:    "keychain",
		Present:   true,
		StoreMode: "keychain",
		Available: true,
	}
}

func (p *KeychainProvider) Token(ctx context.Context) ResolvedToken {
	token, err := keyring.Get(keychainService, keychainUser)
	if err != nil {
		return ResolvedToken{}
	}
	return ResolvedToken{Value: token}
}
