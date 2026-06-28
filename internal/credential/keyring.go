package credential

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

type KeychainProvider struct {
	Service string
	User    string
	Get     func(service, user string) (string, error)
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
	service, user := p.keyringIdentity()
	token, err := get(service, user)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return Status{
				Source:      "keyring",
				Present:     false,
				StoreMode:   "keyring",
				Available:   true,
				ErrorClass:  "token-missing",
				Remediation: "No token found in system keyring. Use GITCODE_TOKEN or store a token with the configured keyring service and account.",
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
	service, user := p.keyringIdentity()
	token, err := get(service, user)
	if err != nil {
		return ResolvedToken{}
	}
	return ResolvedToken{Value: strings.TrimSpace(token)}
}

func (p *KeychainProvider) keyringIdentity() (string, string) {
	service := strings.TrimSpace(p.Service)
	if service == "" {
		service = keyringService
	}
	user := strings.TrimSpace(p.User)
	if user == "" {
		user = keyringUser
	}
	return service, user
}
