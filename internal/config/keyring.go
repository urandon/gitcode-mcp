package config

import (
	"context"
	"errors"
	"strings"

	"github.com/zalando/go-keyring"
)

const (
	keyringService = "gitcode-mcp"
	keyringUser    = "token"
)

type KeychainCredentialProvider struct {
	Get func(service, user string) (string, error)
}

func (p KeychainCredentialProvider) Resolve(ctx context.Context, eff EffectiveConfig) (SecretString, CredentialStatus, error) {
	_ = ctx
	get := p.Get
	if get == nil {
		get = keyring.Get
	}
	service, user := effectiveKeyringIdentity(eff)
	token, err := get(service, user)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return SecretString{}, CredentialStatus{
				Source:           "keyring",
				Present:          false,
				StoreMode:        keyringStoreMode(eff),
				KeyringService:   service,
				KeyringAccount:   user,
				ErrorClass:       "token-missing",
				Remediation:      "No token found in system keyring. Use GITCODE_TOKEN or store a token with the configured keyring service and account.",
				AttemptedSources: []string{"keyring"},
				AvailableSources: []string{"keyring"},
			}, nil
		}
		return SecretString{}, CredentialStatus{
			Source:             "keyring",
			Present:            false,
			StoreMode:          keyringStoreMode(eff),
			KeyringService:     service,
			KeyringAccount:     user,
			ErrorClass:         "credential-store-unavailable",
			Remediation:        "System keyring access failed: " + err.Error(),
			AttemptedSources:   []string{"keyring"},
			UnavailableSources: []string{"keyring"},
		}, nil
	}
	if strings.TrimSpace(token) == "" {
		return SecretString{}, CredentialStatus{
			Source:           "keyring",
			Present:          false,
			StoreMode:        keyringStoreMode(eff),
			KeyringService:   service,
			KeyringAccount:   user,
			ErrorClass:       "token-missing",
			Remediation:      "No token found in system keyring. Use GITCODE_TOKEN or store a token with the configured keyring service and account.",
			AttemptedSources: []string{"keyring"},
			AvailableSources: []string{"keyring"},
		}, nil
	}
	return NewSecretString(strings.TrimSpace(token)), CredentialStatus{
		Source:           "keyring",
		Present:          true,
		StoreMode:        keyringStoreMode(eff),
		KeyringService:   service,
		KeyringAccount:   user,
		AttemptedSources: []string{"keyring"},
		AvailableSources: []string{"keyring"},
	}, nil
}

func (p KeychainCredentialProvider) Status(ctx context.Context, eff EffectiveConfig) CredentialStatus {
	_, status, _ := p.Resolve(ctx, eff)
	return status
}

func keyringStoreMode(eff EffectiveConfig) string {
	if eff.CredentialPolicy.Store != "" {
		return eff.CredentialPolicy.Store
	}
	return "keyring"
}

func effectiveKeyringIdentity(eff EffectiveConfig) (string, string) {
	service := strings.TrimSpace(eff.CredentialPolicy.KeyringService)
	if service == "" {
		service = keyringService
	}
	user := strings.TrimSpace(eff.CredentialPolicy.KeyringAccount)
	if user == "" {
		user = keyringUser
	}
	return service, user
}
