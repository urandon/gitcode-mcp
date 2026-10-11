package auth

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gitcode-mcp/internal/config"
)

type testSource struct {
	env map[string]string
}

func newTestSource() *testSource {
	return &testSource{env: map[string]string{}}
}

func (s *testSource) Env(key string) string                { return s.env[key] }
func (s *testSource) UserHomeDir() (string, error)         { return "/tmp/test-home", nil }
func (s *testSource) UserConfigDir() (string, error)       { return "/tmp/test-config", nil }
func (s *testSource) UserCacheDir() (string, error)        { return "/tmp/test-cache", nil }
func (s *testSource) ReadFile(path string) ([]byte, error) { return nil, nil }

func TestCredentialResolverEnvTokenPresent(t *testing.T) {
	// Priority order: env:GITCODE_TOKEN > keyring (via mock)
	// When GITCODE_TOKEN is set, the resolver should pick the env source.
	src := newTestSource()
	src.env[config.EnvToken] = "secret-token-value"

	resolver := NewCredentialResolver(src)
	result := resolver.Resolve(context.Background(), config.EffectiveConfig{})

	if !result.Present {
		t.Fatalf("expected credential present, got Present=%t", result.Present)
	}
	if result.Source != "env:GITCODE_TOKEN" {
		t.Fatalf("expected source env:GITCODE_TOKEN, got %q", result.Source)
	}
	if result.Token != "secret-token-value" {
		t.Fatalf("expected token 'secret-token-value', got %q", result.Token)
	}
	if result.ErrorClass != "" {
		t.Fatalf("expected no error_class, got %q", result.ErrorClass)
	}
}

func TestCredentialResolverMockKeyring(t *testing.T) {
	// When GITCODE_TOKEN is not set but GITCODE_MCP_TEST_KEYCHAIN_TOKEN is,
	// the resolver should pick the mock-keyring source.
	src := newTestSource()
	src.env[config.EnvToken] = ""
	src.env["GITCODE_MCP_TEST_KEYCHAIN_TOKEN"] = "keyring-secret-value"

	resolver := NewCredentialResolver(src)
	result := resolver.Resolve(context.Background(), config.EffectiveConfig{})

	if !result.Present {
		t.Fatalf("expected credential present, got Present=%t", result.Present)
	}
	if result.Source != "mock-keyring" {
		t.Fatalf("expected source mock-keyring, got %q", result.Source)
	}
	if result.Token != "keyring-secret-value" {
		t.Fatalf("expected token 'keyring-secret-value', got %q", result.Token)
	}
}

func TestCredentialResolverNoCredential(t *testing.T) {
	// When no credential is available, resolver should report not present
	// with error class and remediation.
	// Use a chain with only EnvCredentialProvider (no real keyring) so the
	// test is deterministic regardless of OS keyring state.
	src := newTestSource()

	resolver := NewCredentialResolverWithProvider(config.ChainCredentialProvider{
		Providers: []config.CredentialProvider{
			config.EnvCredentialProvider{Source: src},
		},
	})
	result := resolver.Resolve(context.Background(), config.EffectiveConfig{})

	if result.Present {
		t.Fatalf("expected credential not present, got Present=%t", result.Present)
	}
	if result.ErrorClass != "token-missing" {
		t.Fatalf("expected error_class token-missing, got %q", result.ErrorClass)
	}
	if result.Remediation == "" {
		t.Fatalf("expected non-empty remediation, got %q", result.Remediation)
	}
	if !strings.Contains(result.Remediation, "GITCODE_TOKEN") {
		t.Fatalf("expected remediation mentioning GITCODE_TOKEN, got %q", result.Remediation)
	}
}

func TestCredentialResolverDeterministic(t *testing.T) {
	// Resolve() must be idempotent: the same result object returned every call.
	src := newTestSource()
	src.env[config.EnvToken] = "deterministic-token"

	resolver := NewCredentialResolver(src)
	r1 := resolver.Resolve(context.Background(), config.EffectiveConfig{})
	r2 := resolver.Resolve(context.Background(), config.EffectiveConfig{})

	if r1.Present != r2.Present || r1.Source != r2.Source || r1.Token != r2.Token {
		t.Fatalf("resolver not deterministic: r1=%#v r2=%#v", r1, r2)
	}
}

func TestCredentialResolverStatusMatchesResolve(t *testing.T) {
	// Status() must return the same result as Resolve().
	src := newTestSource()
	src.env[config.EnvToken] = "status-token"

	resolver := NewCredentialResolver(src)
	statusResult := resolver.Status(context.Background(), config.EffectiveConfig{})
	resolveResult := resolver.Resolve(context.Background(), config.EffectiveConfig{})

	if statusResult.Present != resolveResult.Present || statusResult.Source != resolveResult.Source {
		t.Fatalf("Status() doesn't match Resolve(): status=%#v resolve=%#v", statusResult, resolveResult)
	}
}

func TestCredentialResolverEnvOverKeyring(t *testing.T) {
	// When both env and keyring are available, env takes priority.
	src := newTestSource()
	src.env[config.EnvToken] = "env-token"
	src.env["GITCODE_MCP_TEST_KEYCHAIN_TOKEN"] = "keyring-token"

	resolver := NewCredentialResolver(src)
	result := resolver.Resolve(context.Background(), config.EffectiveConfig{})

	if !result.Present {
		t.Fatalf("expected credential present, got Present=%t", result.Present)
	}
	if result.Source != "env:GITCODE_TOKEN" {
		t.Fatalf("expected source env:GITCODE_TOKEN, got %q (keyring should not take priority)", result.Source)
	}
	if result.Token != "env-token" {
		t.Fatalf("expected token 'env-token', got %q", result.Token)
	}
}

type blockingCredentialFixture struct {
	calls   atomic.Int64
	started chan struct{}
	release chan struct{}
}

func (p *blockingCredentialFixture) Resolve(ctx context.Context, _ config.EffectiveConfig) (config.SecretString, config.CredentialStatus, error) {
	p.calls.Add(1)
	close(p.started)
	select {
	case <-p.release:
		return config.NewSecretString("synthetic-private-credential"), config.CredentialStatus{Source: "mock-keyring", Present: true}, nil
	case <-ctx.Done():
		return config.SecretString{}, config.CredentialStatus{}, ctx.Err()
	}
}
func (p *blockingCredentialFixture) Status(ctx context.Context, eff config.EffectiveConfig) config.CredentialStatus {
	_, status, _ := p.Resolve(ctx, eff)
	return status
}

func TestCredentialResolverConcurrentLazyFirstUseDoesNotWaitOrCacheBusy(t *testing.T) {
	p := &blockingCredentialFixture{started: make(chan struct{}), release: make(chan struct{})}
	r := NewCredentialResolverWithProvider(p)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan Result, 1)
	go func() { done <- r.Resolve(ctx, config.EffectiveConfig{}) }()
	select {
	case <-p.started:
	case <-time.After(time.Second):
		t.Fatal("credential fixture did not start")
	}
	var wg sync.WaitGroup
	var wrong atomic.Bool
	start := time.Now()
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result := r.Status(context.Background(), config.EffectiveConfig{})
			if result.Present || result.Token != "" || result.ErrorClass != "credential-resolution-busy" {
				wrong.Store(true)
			}
		}()
	}
	wg.Wait()
	if wrong.Load() || p.calls.Load() != 1 || time.Since(start) > time.Second {
		t.Error("concurrent first use waited, amplified credential calls or cached busy state")
	}
	close(p.release)
	select {
	case result := <-done:
		if !result.Present {
			t.Fatal("owning credential resolution failed")
		}
	case <-time.After(time.Second):
		t.Fatal("credential owner did not join")
	}
	if !r.Status(context.Background(), config.EffectiveConfig{}).Present || p.calls.Load() != 1 {
		t.Fatal("completed credential result was not reused")
	}
}

func TestCredentialResolverCancelledReadDoesNotResolveOrCacheAbsence(t *testing.T) {
	src := newTestSource()
	src.env[config.EnvToken] = "synthetic-private-credential"
	r := NewCredentialResolverWithProvider(config.EnvCredentialProvider{Source: src})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := r.Resolve(ctx, config.EffectiveConfig{})
	if result.Present || result.ErrorClass != "credential-resolution-cancelled" || r.result != nil {
		t.Fatal("cancelled resolution cached absence or ignored context")
	}
	if !r.Resolve(context.Background(), config.EffectiveConfig{}).Present {
		t.Fatal("cancelled first use prevented later explicit resolution")
	}
}
