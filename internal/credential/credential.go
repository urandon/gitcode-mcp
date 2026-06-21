package credential

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"gitcode-mcp/internal/config"
)

type Status struct {
	Source      string `json:"source"`
	Present     bool   `json:"present"`
	StoreMode   string `json:"store_mode"`
	ErrorClass  string `json:"error_class,omitempty"`
	Remediation string `json:"remediation,omitempty"`
	Available   bool   `json:"available"`
}

type ResolvedToken struct {
	Value string
	Salt  string
}

func (rt ResolvedToken) RedactToken() string {
	if rt.Salt != "" {
		return rt.Salt
	}
	v := strings.TrimSpace(rt.Value)
	if v == "" {
		return ""
	}
	if utf8.RuneCountInString(v) < 8 {
		return "***"
	}
	prefixLen := 3
	suffixLen := 3
	runes := []rune(v)
	return string(runes[:prefixLen]) + "***" + string(runes[len(runes)-suffixLen:])
}

type TokenDiagnostic struct {
	Valid  bool     `json:"valid"`
	Issues []string `json:"issues,omitempty"`
}

func ValidateTokenFormat(token string) TokenDiagnostic {
	v := strings.TrimSpace(token)
	if v == "" {
		return TokenDiagnostic{Valid: false, Issues: []string{"empty"}}
	}
	if utf8.RuneCountInString(v) < 8 {
		return TokenDiagnostic{Valid: false, Issues: []string{"too_short"}}
	}
	return TokenDiagnostic{Valid: true}
}

type AuthFailure interface {
	AuthFailure() bool
}

func ClassifyAuthError(err error) (bool, string) {
	if err == nil {
		return false, ""
	}
	var af AuthFailure
	if errors.As(err, &af) {
		return true, err.Error()
	}
	return false, ""
}

type Provider interface {
	Probe(ctx context.Context) Status
	Token(ctx context.Context) ResolvedToken
	Name() string
}

type Pipeline struct {
	providers []Provider
	prober    ProbeFunc
}

type ProbeFunc func(ctx context.Context, token string, baseURL string) (bool, string)

func NewPipeline(providers []Provider) *Pipeline {
	return &Pipeline{providers: providers}
}

func (p *Pipeline) WithProbe(prober ProbeFunc) *Pipeline {
	p.prober = prober
	return p
}

func (p *Pipeline) Resolve(ctx context.Context) (ResolvedToken, SourceStatus, bool) {
	for _, provider := range p.providers {
		st := provider.Probe(ctx)
		if st.Present {
			token := provider.Token(ctx)
			src := SourceStatus{
				Name:        provider.Name(),
				Available:   st.Available,
				Credentials: st,
			}
			return token, src, true
		}
	}
	return ResolvedToken{}, SourceStatus{}, false
}

func (p *Pipeline) Status(ctx context.Context) AuthStatus {
	var results []SourceStatus
	var foundPresent bool
	var resolvedToken ResolvedToken

	for _, provider := range p.providers {
		st := provider.Probe(ctx)
		results = append(results, SourceStatus{
			Name:        provider.Name(),
			Available:   st.Available,
			Credentials: st,
		})
		if st.Present {
			foundPresent = true
			resolvedToken = provider.Token(ctx)
		}
	}

	status := AuthStatus{
		TokenPresent:    foundPresent,
		AvailableSource: results,
	}

	if foundPresent {
		status.RedactedToken = resolvedToken.RedactToken()
		diag := ValidateTokenFormat(resolvedToken.Value)
		status.TokenDiagnostic = &diag
	}

	if p.prober != nil && foundPresent && resolvedToken.Value != "" {
		ok, msg := p.prober(ctx, resolvedToken.Value, "")
		status.AuthProbe = &AuthProbeResult{
			Success: ok,
			Message: msg,
		}
		if !ok {
			status.FailureClass = "auth-failure"
			status.Remediation = msg
		}
	}

	return status
}

func (p *Pipeline) StatusReporter(ctx context.Context, eff config.EffectiveConfig) config.CredentialStatus {
	_ = eff
	return p.Status(ctx).ToCredentialStatus()
}

type AuthStatus struct {
	TokenPresent    bool             `json:"token_present"`
	RedactedToken   string           `json:"redacted_token,omitempty"`
	TokenDiagnostic *TokenDiagnostic `json:"token_diagnostic,omitempty"`
	AvailableSource []SourceStatus   `json:"available_sources"`
	FailureClass    string           `json:"failure_class,omitempty"`
	Remediation     string           `json:"remediation,omitempty"`
	AuthProbe       *AuthProbeResult `json:"auth_probe,omitempty"`
}

type AuthProbeResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

type SourceStatus struct {
	Name        string `json:"name"`
	Available   bool   `json:"available"`
	Credentials Status `json:"credentials"`
}

func (as AuthStatus) ToCredentialStatus() config.CredentialStatus {
	source := "missing"
	storeMode := "auto"
	var errorClass, remediation string
	var availableSources []string

	for _, s := range as.AvailableSource {
		availableSources = append(availableSources, s.Name)
		if s.Credentials.Present {
			source = s.Credentials.Source
			storeMode = s.Credentials.StoreMode
		}
		if s.Credentials.ErrorClass != "" {
			if errorClass == "" {
				errorClass = s.Credentials.ErrorClass
			} else {
				errorClass = errorClass + "," + s.Credentials.ErrorClass
			}
		}
		if s.Credentials.Remediation != "" {
			remediation = s.Credentials.Remediation
		}
	}

	if as.FailureClass != "" {
		if errorClass != "" {
			errorClass = errorClass + "," + as.FailureClass
		} else {
			errorClass = as.FailureClass
		}
	}
	if as.Remediation != "" && remediation == "" {
		remediation = as.Remediation
	}

	if !as.TokenPresent && errorClass == "" {
		errorClass = "token-missing"
	}
	if remediation == "" && errorClass == "token-missing" {
		remediation = "Set GITCODE_TOKEN or configure a credential store."
	}

	return config.CredentialStatus{
		Source:           source,
		Present:          as.TokenPresent,
		StoreMode:        storeMode,
		ErrorClass:       errorClass,
		Remediation:      remediation,
		AvailableSources: availableSources,
	}
}

func (as AuthStatus) RenderText() string {
	var b strings.Builder
	for _, s := range as.AvailableSource {
		fmt.Fprintf(&b, "source: %s\n", s.Name)
		fmt.Fprintf(&b, "  available: %t\n", s.Available)
		fmt.Fprintf(&b, "  token_present: %t\n", s.Credentials.Present)
		if s.Credentials.Source != "" {
			fmt.Fprintf(&b, "  credential_source: %s\n", s.Credentials.Source)
		}
		if s.Credentials.ErrorClass != "" {
			fmt.Fprintf(&b, "  error_class: %s\n", s.Credentials.ErrorClass)
		}
		if s.Credentials.Remediation != "" {
			fmt.Fprintf(&b, "  remediation: %s\n", s.Credentials.Remediation)
		}
	}

	fmt.Fprintf(&b, "token_present: %t\n", as.TokenPresent)
	if as.RedactedToken != "" {
		fmt.Fprintf(&b, "redacted_token: %s\n", as.RedactedToken)
	}
	if as.TokenDiagnostic != nil {
		fmt.Fprintf(&b, "token_valid: %t\n", as.TokenDiagnostic.Valid)
		for _, issue := range as.TokenDiagnostic.Issues {
			fmt.Fprintf(&b, "token_issue: %s\n", issue)
		}
	}
	if as.FailureClass != "" {
		fmt.Fprintf(&b, "failure_class: %s\n", as.FailureClass)
	}
	if as.Remediation != "" {
		fmt.Fprintf(&b, "remediation: %s\n", as.Remediation)
	}
	if as.AuthProbe != nil {
		fmt.Fprintf(&b, "auth_probe:\n")
		fmt.Fprintf(&b, "  success: %t\n", as.AuthProbe.Success)
		fmt.Fprintf(&b, "  message: %s\n", as.AuthProbe.Message)
	}
	return b.String()
}
