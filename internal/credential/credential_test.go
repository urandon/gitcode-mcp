package credential

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"gitcode-mcp/internal/config"
)

type memSource struct {
	env map[string]string
}

func (s *memSource) Env(key string) string                { return s.env[key] }
func (s *memSource) UserHomeDir() (string, error)         { return "/home/test", nil }
func (s *memSource) UserConfigDir() (string, error)       { return "/home/test/.config", nil }
func (s *memSource) UserCacheDir() (string, error)        { return "/home/test/.cache", nil }
func (s *memSource) ReadFile(path string) ([]byte, error) { return nil, nil }

func defaultTestPipeline(src config.Source) *Pipeline {
	return NewPipeline([]Provider{
		&EnvProvider{Source: src},
		&testKeychainProvider{},
		&NoneProvider{},
	})
}

type testKeychainProvider struct{}

func (p *testKeychainProvider) Name() string { return "keyring" }

func (p *testKeychainProvider) Probe(ctx context.Context) Status {
	return Status{Source: "keyring", Present: false, StoreMode: "keyring", Available: true, ErrorClass: "credential-store-unavailable"}
}

func (p *testKeychainProvider) Token(ctx context.Context) ResolvedToken {
	return ResolvedToken{}
}

func TestPipelineResolveEnvToken(t *testing.T) {
	src := &memSource{env: map[string]string{EnvToken: "test-token-xxx"}}
	p := defaultTestPipeline(src)

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
	p := defaultTestPipeline(src)

	_, _, found := p.Resolve(context.Background())
	if found {
		t.Fatal("expected no token found")
	}
}

func TestPipelineStatusEnvPresent(t *testing.T) {
	src := &memSource{env: map[string]string{EnvToken: "test-token-xxx"}}
	p := defaultTestPipeline(src)

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
	p := defaultTestPipeline(src)

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

func TestPipelineStatusKeyringInChain(t *testing.T) {
	src := &memSource{env: map[string]string{}}
	p := defaultTestPipeline(src)

	status := p.Status(context.Background())
	if len(status.AvailableSource) != 3 {
		t.Fatalf("expected 3 sources, got %d", len(status.AvailableSource))
	}

	kcSource := status.AvailableSource[1]
	if kcSource.Name != "keyring" {
		t.Fatalf("got second source name=%q, want keyring", kcSource.Name)
	}
	if kcSource.Credentials.Present {
		t.Fatal("expected keyring Present=false (stub)")
	}
}

func TestKeyringProviderUsesSystemServiceAndAccount(t *testing.T) {
	p := &KeychainProvider{Get: func(service, user string) (string, error) {
		if service != "gitcode-mcp" || user != "token" {
			t.Fatalf("keyring lookup = %s/%s, want gitcode-mcp/token", service, user)
		}
		return "test-token-xxx", nil
	}}
	if p.Name() != "keyring" {
		t.Fatalf("Name = %q, want keyring", p.Name())
	}
	st := p.Probe(context.Background())
	if !st.Present || st.Source != "keyring" || st.StoreMode != "keyring" {
		t.Fatalf("status=%#v", st)
	}
	token := p.Token(context.Background())
	if token.Value != "test-token-xxx" {
		t.Fatalf("token = %q, want test-token-xxx", token.Value)
	}
}

func TestPipelineStatusReporter(t *testing.T) {
	src := &memSource{env: map[string]string{EnvToken: "test-token-xxx"}}
	p := defaultTestPipeline(src)

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
	p := defaultTestPipeline(src)

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
	p := defaultTestPipeline(src)
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
	p := defaultTestPipeline(src)
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
	p := defaultTestPipeline(src)
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
			{Name: "keyring", Available: true, Credentials: Status{Source: "keyring", Present: false, StoreMode: "keyring", ErrorClass: "credential-store-unavailable"}},
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
			{Name: "keyring", Available: true, Credentials: Status{Source: "keyring", Present: false, StoreMode: "keyring", ErrorClass: "credential-store-unavailable"}},
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

func TestRedactedTokenPreview(t *testing.T) {
	tests := []struct {
		name  string
		token ResolvedToken
		want  string
	}{
		{
			name:  "well-formed token",
			token: ResolvedToken{Value: "glpat-abc123xyz"},
			want:  "glp***xyz",
		},
		{
			name:  "salt takes precedence",
			token: ResolvedToken{Value: "glpat-abc123xyz", Salt: "custom-salt"},
			want:  "custom-salt",
		},
		{
			name:  "short token all redacted",
			token: ResolvedToken{Value: "ab"},
			want:  "***",
		},
		{
			name:  "exactly 8 chars",
			token: ResolvedToken{Value: "abcdefgh"},
			want:  "abc***fgh",
		},
		{
			name:  "empty token",
			token: ResolvedToken{Value: ""},
			want:  "",
		},
		{
			name:  "long token with special chars",
			token: ResolvedToken{Value: "glpat-QWxpY2U-2024-token-secret"},
			want:  "glp***ret",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.token.RedactToken()
			if got != tt.want {
				t.Fatalf("RedactToken() = %q, want %q", got, tt.want)
			}
			if tt.token.Value != "" && strings.Contains(got, tt.token.Value) {
				t.Fatalf("RedactToken() = %q contains full token %q", got, tt.token.Value)
			}
		})
	}
}

func TestValidateTokenFormat_WellFormed(t *testing.T) {
	tests := []string{
		"glpat-abcdef123456",
		"GITCODE-PAT-12345678",
		"12345678",
	}
	for _, token := range tests {
		t.Run(token, func(t *testing.T) {
			diag := ValidateTokenFormat(token)
			if !diag.Valid {
				t.Fatalf("ValidateTokenFormat(%q).Valid = false, want true", token)
			}
			if len(diag.Issues) != 0 {
				t.Fatalf("ValidateTokenFormat(%q).Issues = %v, want []", token, diag.Issues)
			}
		})
	}
}

func TestValidateTokenFormat_Empty(t *testing.T) {
	diag := ValidateTokenFormat("")
	if diag.Valid {
		t.Fatal("ValidateTokenFormat(\"\").Valid = true, want false")
	}
	if !contains(diag.Issues, "empty") {
		t.Fatalf("ValidateTokenFormat(\"\").Issues = %v, want [empty]", diag.Issues)
	}
}

func TestValidateTokenFormat_TooShort(t *testing.T) {
	diag := ValidateTokenFormat("ab")
	if diag.Valid {
		t.Fatal("ValidateTokenFormat(\"ab\").Valid = true, want false")
	}
	if !contains(diag.Issues, "too_short") {
		t.Fatalf("ValidateTokenFormat(\"ab\").Issues = %v, want [too_short]", diag.Issues)
	}
}

func TestValidateTokenFormat_WhitespaceTrimmed(t *testing.T) {
	diag := ValidateTokenFormat("  glpat-abcdef123456  ")
	if !diag.Valid {
		t.Fatal("ValidateTokenFormat with whitespace: Valid = false, want true")
	}
}

func TestRenderText_IncludesRedactedToken(t *testing.T) {
	as := AuthStatus{
		TokenPresent:    true,
		RedactedToken:   "glp***xyz",
		TokenDiagnostic: &TokenDiagnostic{Valid: true},
		AvailableSource: []SourceStatus{
			{Name: "env:GITCODE_TOKEN", Available: true, Credentials: Status{Source: "env:GITCODE_TOKEN", Present: true, StoreMode: "auto"}},
		},
	}
	text := as.RenderText()
	if !strings.Contains(text, "redacted_token: glp***xyz") {
		t.Fatalf("RenderText missing redacted_token line:\n%s", text)
	}
	if !strings.Contains(text, "token_valid: true") {
		t.Fatalf("RenderText missing token_valid line:\n%s", text)
	}
}

func TestRenderText_NoToken(t *testing.T) {
	as := AuthStatus{
		TokenPresent: false,
		AvailableSource: []SourceStatus{
			{Name: "env:GITCODE_TOKEN", Available: true, Credentials: Status{Source: "env:GITCODE_TOKEN", Present: false, StoreMode: "auto", ErrorClass: "token-missing"}},
		},
	}
	text := as.RenderText()
	if strings.Contains(text, "redacted_token:") {
		t.Fatalf("RenderText should not contain redacted_token when no token:\n%s", text)
	}
}

func TestRenderText_InvalidTokenDiagnostic(t *testing.T) {
	as := AuthStatus{
		TokenPresent:    true,
		RedactedToken:   "***",
		TokenDiagnostic: &TokenDiagnostic{Valid: false, Issues: []string{"too_short"}},
		AvailableSource: []SourceStatus{
			{Name: "env:GITCODE_TOKEN", Available: true, Credentials: Status{Source: "env:GITCODE_TOKEN", Present: true, StoreMode: "auto"}},
		},
	}
	text := as.RenderText()
	if !strings.Contains(text, "token_valid: false") {
		t.Fatalf("RenderText missing token_valid: false:\n%s", text)
	}
	if !strings.Contains(text, "token_issue: too_short") {
		t.Fatalf("RenderText missing token_issue line:\n%s", text)
	}
}

func TestPipelineStatusPopulatesRedactedToken(t *testing.T) {
	src := &memSource{env: map[string]string{EnvToken: "glpat-abcdef123456"}}
	p := defaultTestPipeline(src)
	status := p.Status(context.Background())
	if !status.TokenPresent {
		t.Fatal("expected TokenPresent=true")
	}
	if status.RedactedToken == "" {
		t.Fatal("expected RedactedToken to be set")
	}
	if status.RedactedToken == "glpat-abcdef123456" {
		t.Fatal("RedactedToken must not be the full token")
	}
	if strings.Contains(status.RedactedToken, "glpat-abcdef123456") {
		t.Fatalf("RedactedToken=%q must not contain the full token", status.RedactedToken)
	}
	if status.TokenDiagnostic == nil {
		t.Fatal("expected TokenDiagnostic to be set")
	}
	if !status.TokenDiagnostic.Valid {
		t.Fatal("expected TokenDiagnostic.Valid=true")
	}
}

func TestPipelineStatusNoTokenNoRedacted(t *testing.T) {
	src := &memSource{env: map[string]string{}}
	p := defaultTestPipeline(src)
	status := p.Status(context.Background())
	if status.TokenPresent {
		t.Fatal("expected TokenPresent=false")
	}
	if status.RedactedToken != "" {
		t.Fatalf("expected RedactedToken empty, got %q", status.RedactedToken)
	}
	if status.TokenDiagnostic != nil {
		t.Fatal("expected TokenDiagnostic to be nil when no token")
	}
}

func TestPipelineStatusWithProbeAndAuthFailureClass(t *testing.T) {
	src := &memSource{env: map[string]string{EnvToken: "glpat-invalid_token"}}
	p := defaultTestPipeline(src)
	p.WithProbe(func(ctx context.Context, token string, baseURL string) (bool, string) {
		return false, "expired token"
	})

	status := p.Status(context.Background())
	if !status.TokenPresent {
		t.Fatal("expected TokenPresent=true")
	}
	if status.RedactedToken == "" {
		t.Fatal("expected RedactedToken set even when probe fails")
	}
	if status.FailureClass != "auth-failure" {
		t.Fatalf("got FailureClass=%q, want auth-failure", status.FailureClass)
	}
}

type testAuthFailureError struct {
	msg string
}

func (e testAuthFailureError) Error() string     { return e.msg }
func (e testAuthFailureError) AuthFailure() bool { return true }

func TestClassifyAuthError_AuthFailure(t *testing.T) {
	err := testAuthFailureError{msg: "expired token"}
	isAuth, msg := ClassifyAuthError(err)
	if !isAuth {
		t.Fatal("expected isAuth=true")
	}
	if msg != "expired token" {
		t.Fatalf("got msg=%q, want 'expired token'", msg)
	}
}

func TestClassifyAuthError_WrappedAuthFailure(t *testing.T) {
	inner := testAuthFailureError{msg: "forbidden"}
	err := fmt.Errorf("sync failed: %w", inner)
	isAuth, msg := ClassifyAuthError(err)
	if !isAuth {
		t.Fatal("expected isAuth=true for wrapped auth error")
	}
	if msg != "sync failed: forbidden" {
		t.Fatalf("got msg=%q, want 'sync failed: forbidden'", msg)
	}
}

func TestClassifyAuthError_NonAuthError(t *testing.T) {
	err := fmt.Errorf("network timeout")
	isAuth, _ := ClassifyAuthError(err)
	if isAuth {
		t.Fatal("expected isAuth=false for non-auth error")
	}
}

func TestClassifyAuthError_Nil(t *testing.T) {
	isAuth, msg := ClassifyAuthError(nil)
	if isAuth {
		t.Fatal("expected isAuth=false for nil")
	}
	if msg != "" {
		t.Fatalf("expected empty msg for nil, got %q", msg)
	}
}
