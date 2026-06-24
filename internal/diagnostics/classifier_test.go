package diagnostics

import (
	"errors"
	"strings"
	"testing"
)

type codedError struct {
	code string
	msg  string
}

func (e codedError) Error() string          { return e.msg }
func (e codedError) DiagnosticCode() string { return e.code }

func TestClassifierLivePrecedenceAndHTTPInvariants(t *testing.T) {
	tests := []struct {
		name string
		err  error
		ctx  CommandContext
		want Code
		http bool
	}{
		{name: "configuration before missing credential", err: codedError{code: "missing_credential", msg: "missing"}, ctx: CommandContext{ProviderMode: "live-http", BroaderConfigurationInvalid: true, MissingCredential: true}, want: CodeConfigurationError},
		{name: "missing credential before invalid base", err: codedError{code: "invalid_api_base_url", msg: "bad"}, ctx: CommandContext{ProviderMode: "live-http", MissingCredential: true, InvalidSelectedAPIBaseURL: true}, want: CodeMissingCredential},
		{name: "invalid base after credential present", err: codedError{code: "invalid_api_base_url", msg: "bad"}, ctx: CommandContext{ProviderMode: "live-http", InvalidSelectedAPIBaseURL: true}, want: CodeInvalidAPIBaseURL},
		{name: "401 requires attempted", err: codedError{code: "auth_expired", msg: "auth"}, ctx: CommandContext{ProviderMode: "live-http", HTTPStatus: 401, HTTPAttempted: true}, want: CodeLiveAuthFailure, http: true},
		{name: "401 without attempted is configuration", err: codedError{code: "auth_expired", msg: "auth"}, ctx: CommandContext{ProviderMode: "live-http", HTTPStatus: 401}, want: CodeConfigurationError},
		{name: "transport requires attempted", err: codedError{code: "network_unavailable", msg: "net"}, ctx: CommandContext{ProviderMode: "live-http"}, want: CodeConfigurationError},
		{name: "transport attempted", err: codedError{code: "network_unavailable", msg: "net"}, ctx: CommandContext{ProviderMode: "live-http", HTTPAttempted: true}, want: CodeLiveTransportFailure, http: true},
		{name: "api attempted", err: codedError{code: "write_provider_error", msg: "api"}, ctx: CommandContext{ProviderMode: "live-http", HTTPAttempted: true}, want: CodeLiveAPIFailure, http: true},
		{name: "live fixture sentinel", err: codedError{code: "write_fixture_fallback_detected", msg: "fixture client is read-only"}, ctx: CommandContext{ProviderMode: "live-http"}, want: CodeFixtureFallbackDetected},
		{name: "non-live fixture read only", err: codedError{code: "fixture_read_only", msg: "fixture client is read-only"}, ctx: CommandContext{ProviderMode: "offline-fixture"}, want: CodeFixtureReadOnly},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(tt.err, tt.ctx)
			if got.Code != tt.want || got.HTTPAttempted != tt.http {
				t.Fatalf("got code=%s http=%t want code=%s http=%t", got.Code, got.HTTPAttempted, tt.want, tt.http)
			}
		})
	}
}

func TestClassifierUnsupportedPayloadContextRedacted(t *testing.T) {
	d := Classify(errors.New("payload token=secret-token missing id"), CommandContext{ProviderMode: "live-http", Command: "sync", PayloadKind: "issue", PayloadSource: "mock token=secret-token", RequiredField: "id", UnsupportedPayload: true, SensitiveTerms: []string{"secret-token"}})
	if d.Code != CodeUnsupportedMockPayload {
		t.Fatalf("unexpected code: %s", d.Code)
	}
	if d.Context["payload_kind"] != "issue" || d.Context["payload_source"] == "" || d.Context["required_field"] != "id" || d.Context["command"] != "sync" || d.Context["provider_mode"] != "live-http" {
		t.Fatalf("missing required context: %#v", d.Context)
	}
	joined := d.Message + strings.Join(mapValues(d.Context), " ")
	if strings.Contains(joined, "secret-token") {
		t.Fatalf("diagnostic leaked secret: %#v message=%s", d.Context, d.Message)
	}
}

func mapValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, value := range m {
		out = append(out, value)
	}
	return out
}
