//go:build !darwin

package config

import "context"

type KeychainCredentialProvider struct{}

func (p KeychainCredentialProvider) Resolve(ctx context.Context, eff EffectiveConfig) (SecretString, CredentialStatus, error) {
	_ = ctx
	return SecretString{}, CredentialStatus{
		Source:             "keychain",
		Present:            false,
		StoreMode:          keychainStoreMode(eff),
		ErrorClass:         "credential-store-unavailable",
		Remediation:        "Keychain is only available on macOS. Use GITCODE_TOKEN or configure credential.store: env.",
		AttemptedSources:   []string{"keychain"},
		UnavailableSources: []string{"keychain"},
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
