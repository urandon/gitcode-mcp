package credential

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

type KeychainProvider struct {
	Get func(service, user string) (string, error)
}

func (p *KeychainProvider) Name() string {
	return "keyring"
}

func (p *KeychainProvider) Probe(ctx context.Context) Status {
	_ = ctx
	get := p.Get
	if get == nil {
		get = keyring.Get
	}
	token, err := getKeyringToken(get)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return Status{
				Source:      "keyring",
				Present:     false,
				StoreMode:   "keyring",
				Available:   true,
				ErrorClass:  "token-missing",
				Remediation: "No token found in system keyring. Use GITCODE_TOKEN or store a token with service gitcode-mcp and account token.",
			}
		}
		return Status{
			Source:      "keyring",
			Present:     false,
			StoreMode:   "keyring",
			ErrorClass:  "credential-store-unavailable",
			Remediation: "System keyring access failed: " + err.Error(),
			Available:   false,
		}
	}
	if strings.TrimSpace(token) == "" {
		return Status{
			Source:      "keyring",
			Present:     false,
			StoreMode:   "keyring",
			Available:   true,
			ErrorClass:  "token-missing",
			Remediation: "No token found in system keyring. Use GITCODE_TOKEN or store a token with service gitcode-mcp and account token.",
		}
	}
	return Status{
		Source:    "keyring",
		Present:   true,
		StoreMode: "keyring",
		Available: true,
	}
}

func (p *KeychainProvider) Token(ctx context.Context) ResolvedToken {
	_ = ctx
	get := p.Get
	if get == nil {
		get = keyring.Get
	}
	token, err := getKeyringToken(get)
	if err != nil {
		return ResolvedToken{}
	}
	return ResolvedToken{Value: strings.TrimSpace(token)}
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
	return uniqueNonEmpty([]string{keyringUser, os.Getenv("USER"), os.Getenv("USERNAME")})
}

func uniqueNonEmpty(values []string) []string {
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
