//go:build darwin

package config

import (
	"context"
	"errors"

	"github.com/zalando/go-keyring"
)

const (
	keychainService = "gitcode-mcp"
	keychainUser    = "token"
)

type KeychainCredentialProvider struct{}

func (p KeychainCredentialProvider) Resolve(ctx context.Context, eff EffectiveConfig) (SecretString, CredentialStatus, error) {
	_ = ctx
	token, err := keyring.Get(keychainService, keychainUser)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return SecretString{}, CredentialStatus{
				Source:      "keychain",
				Present:     false,
				StoreMode:   keychainStoreMode(eff),
				ErrorClass:  "token-missing",
				Remediation: "No token found in macOS Keychain. Use GITCODE_TOKEN or store a token in Keychain.",
			}, nil
		}
		return SecretString{}, CredentialStatus{
			Source:      "keychain",
			Present:     false,
			StoreMode:   keychainStoreMode(eff),
			ErrorClass:  "credential-store-unavailable",
			Remediation: "macOS Keychain access failed: " + err.Error(),
		}, nil
	}
	if token == "" {
		return SecretString{}, CredentialStatus{
			Source:      "keychain",
			Present:     false,
			StoreMode:   keychainStoreMode(eff),
			ErrorClass:  "token-missing",
			Remediation: "No token found in macOS Keychain. Use GITCODE_TOKEN or store a token in Keychain.",
		}, nil
	}
	return NewSecretString(token), CredentialStatus{
		Source:    "keychain",
		Present:   true,
		StoreMode: keychainStoreMode(eff),
	}, nil
}

func (p KeychainCredentialProvider) Status(ctx context.Context, eff EffectiveConfig) CredentialStatus {
	_, status, _ := p.Resolve(ctx, eff)
	return status
}

func keychainStoreMode(eff EffectiveConfig) string {
	if eff.CredentialPolicy.Store != "" {
		return eff.CredentialPolicy.Store
	}
	return "keychain"
}
