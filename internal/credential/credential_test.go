package credential

import (
	"context"
	"testing"

	"gitcode-mcp/internal/config"
)

type memSource struct {
	env map[string]string
}

func (s *memSource) Env(key string) string              { return s.env[key] }
func (s *memSource) UserHomeDir() (string, error)       { return "/home/test", nil }
func (s *memSource) UserConfigDir() (string, error)     { return "/home/test/.config", nil }
func (s *memSource) UserCacheDir() (string, error)      { return "/home/test/.cache", nil }
func (s *memSource) ReadFile(path string) ([]byte, error) { return nil, nil }

func TestPipelineResolveEnvToken(t *testing.T) {
	src := &memSource{env: map[string]string{EnvToken: "test-token-xxx"}}
	p := DefaultPipeline(src)

	token, sourceStatus, found := p.Resolve(context.Background())
	if !found {
		t.Fatal("expected token to be found")
	}
	if token.Value != "test-token-xxx" {
		t.Fatalf("got token.Value=%q, want %q", token.Value, "test-token-xxx")
	}
	if sourceStatus.Name != "env:GITCODE_TOKEN" {
		t.Fatalf("got source name=%q, want %q", sourceStatus.Name, "env:GITCODE_TOKEN")
	}
	if !sourceStatus.Credentials.Present {
		t.Fatal("expected Present=true")
	}
}

func TestPipelineResolveNone(t *testing.T) {
	src := &memSource{env: map[string]string{}}
	p := DefaultPipeline(src)

	_, _, found := p.Resolve(context.Background())
	if found {
		t.Fatal("expected no token found")
	}
}

func TestPipelineStatusEnvPresent(t *testing.T) {
	src := &memSource{env: map[string]string{EnvToken: "test-token-xxx"}}
	p := DefaultPipeline(src)

	status := p.Status(context.Background())
	if !status.TokenPresent {
		t.Fatal("expected TokenPresent=true")
	}
	if len(status.AvailableSource) != 3 {
		t.Fatalf("expected 3 sources, got %d", len(status.AvailableSource))
	}

	envSource := status.AvailableSource[0]
	if envSource.Name != "env:GITCODE_TOKEN" {
		t.Fatalf("got first source name=%q, want env:GITCODE_TOKEN", envSource.Name)
	}
	if !envSource.Credentials.Present {
		t.Fatal("expected env source Present=true")
	}
}

func TestPipelineStatusNone(t *testing.T) {
	src := &memSource{env: map[string]string{}}
	p := DefaultPipeline(src)

	status := p.Status(context.Background())
	if status.TokenPresent {
		t.Fatal("expected TokenPresent=false")
	}
	if len(status.AvailableSource) != 3 {
		t.Fatalf("expected 3 sources, got %d", len(status.AvailableSource))
	}

	noneSource := status.AvailableSource[2]
	if noneSource.Name != "none" {
		t.Fatalf("got last source name=%q, want none", noneSource.Name)
	}
	if noneSource.Credentials.Present {
		t.Fatal("expected none source Present=false")
	}
	if !noneSource.Available {
		t.Fatal("expected none source Available=true")
	}
}

func TestPipelineStatusKeychainInChain(t *testing.T) {
	src := &memSource{env: map[string]string{}}
	p := DefaultPipeline(src)

	status := p.Status(context.Background())
	if len(status.AvailableSource) != 3 {
		t.Fatalf("expected 3 sources, got %d", len(status.AvailableSource))
	}

	kcSource := status.AvailableSource[1]
	if kcSource.Name != "keychain" {
		t.Fatalf("got second source name=%q, want keychain", kcSource.Name)
	}
	if kcSource.Credentials.Present {
		t.Fatal("expected keychain Present=false (stub)")
	}
}

func TestPipelineStatusReporter(t *testing.T) {
	src := &memSource{env: map[string]string{EnvToken: "test-token-xxx"}}
	p := DefaultPipeline(src)

	cs := p.StatusReporter(context.Background(), config.EffectiveConfig{})
	if cs.Source != "env:GITCODE_TOKEN" {
		t.Fatalf("got source=%q, want env:GITCODE_TOKEN", cs.Source)
	}
	if !cs.Present {
		t.Fatal("expected Present=true")
	}
	if cs.StoreMode != "auto" {
		t.Fatalf("got store_mode=%q, want auto", cs.StoreMode)
	}
}

func TestPipelineStatusReporterNone(t *testing.T) {
	src := &memSource{env: map[string]string{}}
	p := DefaultPipeline(src)

	cs := p.StatusReporter(context.Background(), config.EffectiveConfig{})
	if cs.Source != "missing" {
		t.Fatalf("got source=%q, want missing", cs.Source)
	}
	if cs.Present {
		t.Fatal("expected Present=false")
	}
	if cs.ErrorClass == "" {
		t.Fatal("expected non-empty error_class")
	}
	if !contains(cs.AvailableSources, "env:GITCODE_TOKEN") {
		t.Fatal("expected env:GITCODE_TOKEN in available_sources")
	}
	if !contains(cs.AvailableSources, "none") {
		t.Fatal("expected none in available_sources")
	}
}

func TestPipelineStatusWithProbe(t *testing.T) {
	src := &memSource{env: map[string]string{EnvToken: "test-token-xxx"}}
	p := DefaultPipeline(src)
	p.WithProbe(func(ctx context.Context, token string, baseURL string) (bool, string) {
		if token == "test-token-xxx" {
			return true, "success"
		}
		return false, "invalid token"
	})

	status := p.Status(context.Background())
	if status.AuthProbe == nil {
		t.Fatal("expected AuthProbe to be set")
	}
	if !status.AuthProbe.Success {
		t.Fatalf("expected probe success, got message=%q", status.AuthProbe.Message)
	}
}

func TestPipelineStatusWithFailingProbe(t *testing.T) {
	src := &memSource{env: map[string]string{EnvToken: "test-token-xxx"}}
	p := DefaultPipeline(src)
	p.WithProbe(func(ctx context.Context, token string, baseURL string) (bool, string) {
		return false, "invalid credentials"
	})

	status := p.Status(context.Background())
	if status.AuthProbe == nil {
		t.Fatal("expected AuthProbe to be set")
	}
	if status.AuthProbe.Success {
		t.Fatal("expected probe failure")
	}
	if status.FailureClass != "auth-failure" {
		t.Fatalf("got FailureClass=%q, want auth-failure", status.FailureClass)
	}
	if status.Remediation != "invalid credentials" {
		t.Fatalf("got Remediation=%q, want 'invalid credentials'", status.Remediation)
	}
}

func TestPipelineNoProbeWhenTokenMissing(t *testing.T) {
	src := &memSource{env: map[string]string{}}
	p := DefaultPipeline(src)
	p.WithProbe(func(ctx context.Context, token string, baseURL string) (bool, string) {
		t.Fatal("probe should not be called when no token is present")
		return false, ""
	})

	status := p.Status(context.Background())
	if status.AuthProbe != nil {
		t.Fatal("expected AuthProbe to be nil")
	}
}

func TestEnvProviderName(t *testing.T) {
	p := &EnvProvider{}
	if p.Name() != "env:GITCODE_TOKEN" {
		t.Fatalf("got Name=%q, want env:GITCODE_TOKEN", p.Name())
	}
}

func TestEnvProviderProbeEmpty(t *testing.T) {
	src := &memSource{env: map[string]string{}}
	p := &EnvProvider{Source: src}
	st := p.Probe(context.Background())
	if st.Present {
		t.Fatal("expected Present=false for empty env")
	}
	if st.ErrorClass != "token-missing" {
		t.Fatalf("got ErrorClass=%q, want token-missing", st.ErrorClass)
	}
}

func TestEnvProviderProbePresent(t *testing.T) {
	src := &memSource{env: map[string]string{EnvToken: "test-token-xxx"}}
	p := &EnvProvider{Source: src}
	st := p.Probe(context.Background())
	if !st.Present {
		t.Fatal("expected Present=true")
	}
	if st.Source != "env:GITCODE_TOKEN" {
		t.Fatalf("got Source=%q, want env:GITCODE_TOKEN", st.Source)
	}
}

func TestEnvProviderToken(t *testing.T) {
	src := &memSource{env: map[string]string{EnvToken: "test-token-xxx"}}
	p := &EnvProvider{Source: src}
	token := p.Token(context.Background())
	if token.Value != "test-token-xxx" {
		t.Fatalf("got Token=%q, want test-token-xxx", token.Value)
	}
}

func TestNoneProviderAlwaysTerminal(t *testing.T) {
	p := &NoneProvider{}
	if p.Name() != "none" {
		t.Fatalf("got Name=%q, want none", p.Name())
	}
	st := p.Probe(context.Background())
	if st.Present {
		t.Fatal("NoneProvider must never return Present=true")
	}
	if st.Available != true {
		t.Fatal("NoneProvider must return Available=true")
	}
}

func TestAuthStatusToCredentialStatusMapping(t *testing.T) {
	as := AuthStatus{
		TokenPresent: true,
		AvailableSource: []SourceStatus{
			{Name: "env:GITCODE_TOKEN", Available: true, Credentials: Status{Source: "env:GITCODE_TOKEN", Present: true, StoreMode: "auto"}},
			{Name: "keychain", Available: true, Credentials: Status{Source: "keychain", Present: false, StoreMode: "keychain", ErrorClass: "credential-store-unavailable"}},
			{Name: "none", Available: true, Credentials: Status{Source: "none", Present: false, StoreMode: "none"}},
		},
	}

	cs := as.ToCredentialStatus()
	if cs.Source != "env:GITCODE_TOKEN" {
		t.Fatalf("got source=%q, want env:GITCODE_TOKEN", cs.Source)
	}
	if !cs.Present {
		t.Fatal("expected Present=true")
	}
	if cs.StoreMode != "auto" {
		t.Fatalf("got store_mode=%q, want auto", cs.StoreMode)
	}
	if len(cs.AvailableSources) != 3 {
		t.Fatalf("got %d available sources, want 3", len(cs.AvailableSources))
	}
}

func TestAuthStatusToCredentialStatusMissing(t *testing.T) {
	as := AuthStatus{
		TokenPresent: false,
		AvailableSource: []SourceStatus{
			{Name: "env:GITCODE_TOKEN", Available: true, Credentials: Status{Source: "env:GITCODE_TOKEN", Present: false, StoreMode: "auto", ErrorClass: "token-missing"}},
			{Name: "keychain", Available: true, Credentials: Status{Source: "keychain", Present: false, StoreMode: "keychain", ErrorClass: "credential-store-unavailable"}},
			{Name: "none", Available: true, Credentials: Status{Source: "none", Present: false, StoreMode: "none"}},
		},
	}

	cs := as.ToCredentialStatus()
	if cs.Source != "missing" {
		t.Fatalf("got source=%q, want missing", cs.Source)
	}
	if cs.Present {
		t.Fatal("expected Present=false")
	}
	if !contains(cs.AvailableSources, "none") {
		t.Fatal("expected 'none' in available_sources")
	}
}

func contains(slice []string, want string) bool {
	for _, s := range slice {
		if s == want {
			return true
		}
	}
	return false
}
