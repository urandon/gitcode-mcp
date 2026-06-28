package config

import (
	"context"
	"errors"
	"os"
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
	token, err := getKeyringToken(get)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return SecretString{}, CredentialStatus{
				Source:           "keyring",
				Present:          false,
				StoreMode:        keyringStoreMode(eff),
				ErrorClass:       "token-missing",
				Remediation:      "No token found in system keyring. Use GITCODE_TOKEN or store a token with service gitcode-mcp and account token.",
				AttemptedSources: []string{"keyring"},
				AvailableSources: []string{"keyring"},
			}, nil
		}
		return SecretString{}, CredentialStatus{
			Source:             "keyring",
			Present:            false,
			StoreMode:          keyringStoreMode(eff),
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
			ErrorClass:       "token-missing",
			Remediation:      "No token found in system keyring. Use GITCODE_TOKEN or store a token with service gitcode-mcp and account token.",
			AttemptedSources: []string{"keyring"},
			AvailableSources: []string{"keyring"},
		}, nil
	}
	return NewSecretString(strings.TrimSpace(token)), CredentialStatus{
		Source:           "keyring",
		Present:          true,
		StoreMode:        keyringStoreMode(eff),
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

func getKeyringToken(get func(service, user string) (string, error)) (string, error) {
	var lastNotFound error
	for _, user := range keyringUsers() {
		token, err := get(keyringService, user)
		if err == nil {
			return token, nil
		}
		if errors.Is(err, keyring.ErrNotFound) {
			lastNotFound = err
			continue
		}
		return "", err
	}
	if lastNotFound != nil {
		return "", keyring.ErrNotFound
	}
	return "", keyring.ErrNotFound
}

func keyringUsers() []string {
	return uniqueKeyringUsers([]string{keyringUser, os.Getenv("USER"), os.Getenv("USERNAME")})
}

func uniqueKeyringUsers(values []string) []string {
	seen := make(map[string]bool, len(values))
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
