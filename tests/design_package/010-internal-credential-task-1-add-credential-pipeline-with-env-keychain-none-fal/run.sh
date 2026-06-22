#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
cd "${REPO_ROOT}"

VALIDATION_TEST="${SCRIPT_DIR}/validation_runtime_test.go"
trap 'rm -f "${VALIDATION_TEST}"' EXIT

cat > "${VALIDATION_TEST}" <<'GO'
package validation_test

import (
	"context"
	"runtime"
	"testing"

	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/credential"
)

type memSource struct {
	env map[string]string
}

func (s memSource) Env(key string) string { return s.env[key] }
func (s memSource) UserHomeDir() (string, error) { return "/tmp/gitcode-mcp-validation-home", nil }
func (s memSource) UserConfigDir() (string, error) { return "/tmp/gitcode-mcp-validation-config", nil }
func (s memSource) UserCacheDir() (string, error) { return "/tmp/gitcode-mcp-validation-cache", nil }
func (s memSource) ReadFile(path string) ([]byte, error) { return nil, nil }

var _ config.Source = memSource{}

func TestTask010Scenario1EnvTokenWins(t *testing.T) {
	p := credential.DefaultPipeline(memSource{env: map[string]string{credential.EnvToken: "test-token-env-010"}})

	token, source, found := p.Resolve(context.Background())
	if !found {
		t.Fatalf("expected GITCODE_TOKEN to resolve")
	}
	if token.Value != "test-token-env-010" {
		t.Fatalf("resolved token mismatch: got %q", token.Value)
	}
	if source.Name != "env:GITCODE_TOKEN" || source.Credentials.Source != "env:GITCODE_TOKEN" || !source.Credentials.Present {
		t.Fatalf("expected env source with present token, got source=%+v", source)
	}
}

func TestTask010Scenario2ProductionDarwinKeychainProviderIsNotUnavailableStub(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("native keychain acceptance is darwin-specific")
	}

	provider := &credential.KeychainProvider{}
	status := provider.Probe(context.Background())
	token := provider.Token(context.Background())

	if status.Source != "keychain" || provider.Name() != "keychain" {
		t.Fatalf("expected production keychain source identity, got provider=%q status=%+v", provider.Name(), status)
	}
	if status.ErrorClass == "credential-store-unavailable" || !status.Available {
		t.Fatalf("production darwin KeychainProvider is still unavailable stub; expected native keychain resolution path, got status=%+v", status)
	}
	if !status.Present || token.Value == "" {
		t.Logf("keychain entry not present on this machine (status.Present=%t, tokenEmpty=%t); provider is integrated with native keychain, just no stored token", status.Present, token.Value == "")
	}
}

func TestTask010Scenario3NoneFallbackAndOfflineStatus(t *testing.T) {
	p := credential.DefaultPipeline(memSource{env: map[string]string{}})

	_, _, found := p.Resolve(context.Background())
	if found {
		t.Fatalf("expected no token when env and keychain are unavailable")
	}

	status := p.Status(context.Background())
	if status.TokenPresent {
		t.Fatalf("expected TokenPresent=false, got %+v", status)
	}
	if len(status.AvailableSource) == 0 {
		t.Fatalf("expected source inventory in auth status")
	}
	last := status.AvailableSource[len(status.AvailableSource)-1]
	if last.Name != "none" || last.Credentials.Source != "none" || last.Credentials.Present {
		t.Fatalf("expected terminal none fallback source, got %+v", last)
	}
}
GO

go test "./tests/design_package/010-internal-credential-task-1-add-credential-pipeline-with-env-keychain-none-fal" -run TestTask010 -count=1 -v
go test ./internal/credential/... -count=1 -v
go test ./... -count=1
