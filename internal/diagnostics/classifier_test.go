package diagnostics

import (
	"errors"
	"net/http"
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
		name      string
		err       error
		ctx       CommandContext
		want      Code
		http      bool
		retryable bool
		exitClass string
	}{
		{name: "SCN-DIAG-PRECEDENCE-01 configuration before missing credential", err: codedError{code: "missing_credential", msg: "missing"}, ctx: CommandContext{ProviderMode: "live-http", BroaderConfigurationInvalid: true, MissingCredential: true}, want: CodeConfigCredential, exitClass: "configuration"},
		{name: "SCN-DIAG-PRECEDENCE-02 missing credential before invalid base", err: codedError{code: "invalid_api_base_url", msg: "bad"}, ctx: CommandContext{ProviderMode: "live-http", MissingCredential: true, InvalidSelectedAPIBaseURL: true}, want: CodeConfigCredential, exitClass: "configuration"},
		{name: "SCN-DIAG-PRECEDENCE-03 invalid base after credential present", err: codedError{code: "invalid_api_base_url", msg: "bad"}, ctx: CommandContext{ProviderMode: "live-http", InvalidSelectedAPIBaseURL: true}, want: CodeConfigCredential, exitClass: "configuration"},
		{name: "SCN-DIAG-PRECEDENCE-04 remote 401 attempted is api validation", err: codedError{code: "auth_expired", msg: "auth"}, ctx: CommandContext{ProviderMode: "live-http", HTTPStatus: http.StatusUnauthorized, HTTPAttempted: true}, want: CodeAPIFailure, http: true, exitClass: "provider"},
		{name: "SCN-DIAG-PRECEDENCE-05 remote 400 is api validation", err: codedError{code: "api_validation", msg: "bad request"}, ctx: CommandContext{ProviderMode: "live-http", HTTPStatus: http.StatusBadRequest, HTTPAttempted: true}, want: CodeAPIFailure, http: true, exitClass: "provider"},
		{name: "SCN-DIAG-PRECEDENCE-06 malformed 200 json is schema decode", err: codedError{code: "schema_decode", msg: "malformed"}, ctx: CommandContext{ProviderMode: "live-http", HTTPStatus: http.StatusOK, HTTPAttempted: true, MalformedSuccess: true}, want: CodeSchemaDecode, http: true, exitClass: "schema"},
		{name: "SCN-DIAG-PRECEDENCE-07 schema mismatch is schema decode", err: codedError{code: "schema_decode", msg: "shape"}, ctx: CommandContext{ProviderMode: "live-http", HTTPStatus: http.StatusOK, HTTPAttempted: true, SchemaDecodeFailure: true}, want: CodeSchemaDecode, http: true, exitClass: "schema"},
		{name: "SCN-DIAG-PRECEDENCE-08 local body limit is schema decode", err: codedError{code: "payload_too_large", msg: "large"}, ctx: CommandContext{ProviderMode: "live-http", HTTPAttempted: true, LocalPayloadTooLarge: true, FailureSource: "local_body_limit"}, want: CodeSchemaDecode, http: true, exitClass: "schema"},
		{name: "SCN-DIAG-PRECEDENCE-09 remote 413 is api validation", err: codedError{code: "payload_too_large", msg: "large"}, ctx: CommandContext{ProviderMode: "live-http", HTTPStatus: http.StatusRequestEntityTooLarge, HTTPAttempted: true, FailureSource: "remote_status"}, want: CodeAPIFailure, http: true, exitClass: "provider"},
		{name: "SCN-DIAG-PRECEDENCE-10 partial response is schema decode", err: codedError{code: "partial_response", msg: "partial"}, ctx: CommandContext{ProviderMode: "live-http", HTTPAttempted: true, FailureSource: "partial_response"}, want: CodeSchemaDecode, http: true, exitClass: "schema"},
		{name: "SCN-DIAG-PRECEDENCE-11 timeout is transport", err: codedError{code: "network_unavailable", msg: "timeout"}, ctx: CommandContext{ProviderMode: "live-http", HTTPAttempted: true, TransportFailure: true}, want: CodeLiveTransportFailure, http: true, retryable: true, exitClass: "transport"},
		{name: "SCN-DIAG-PRECEDENCE-12 http 500 is transport", err: codedError{code: "network_unavailable", msg: "server error"}, ctx: CommandContext{ProviderMode: "live-http", HTTPStatus: http.StatusInternalServerError, HTTPAttempted: true}, want: CodeLiveTransportFailure, http: true, retryable: true, exitClass: "transport"},
		{name: "SCN-DIAG-PRECEDENCE-13 401 without attempted is config credential", err: codedError{code: "auth_expired", msg: "auth"}, ctx: CommandContext{ProviderMode: "live-http", HTTPStatus: http.StatusUnauthorized}, want: CodeConfigCredential, exitClass: "configuration"},
		{name: "SCN-DIAG-PRECEDENCE-14 live fixture sentinel", err: codedError{code: "write_fixture_fallback_detected", msg: "fixture client is read-only"}, ctx: CommandContext{ProviderMode: "live-http"}, want: CodeFixtureFallbackDetected, exitClass: "fixture"},
		{name: "SCN-DIAG-PRECEDENCE-15 non-live fixture read only", err: codedError{code: "fixture_read_only", msg: "fixture client is read-only"}, ctx: CommandContext{ProviderMode: "offline-fixture"}, want: CodeFixtureReadOnly, exitClass: "fixture"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(tt.err, tt.ctx)
			if got.Code != tt.want || got.HTTPAttempted != tt.http || got.Retryable != tt.retryable || got.ExitClass != tt.exitClass {
				t.Fatalf("got code=%s http=%t retryable=%t exit=%s want code=%s http=%t retryable=%t exit=%s", got.Code, got.HTTPAttempted, got.Retryable, got.ExitClass, tt.want, tt.http, tt.retryable, tt.exitClass)
			}
		})
	}
}

func TestClassifierUnsupportedPayloadContextRedacted(t *testing.T) {
	d := Classify(errors.New("payload token=secret-token missing id"), CommandContext{ProviderMode: "live-http", Command: "sync", PayloadKind: "issue", PayloadSource: "mock token=secret-token", RequiredField: "id", UnsupportedPayload: true, SensitiveTerms: []string{"secret-token"}})
	if d.Code != CodeSchemaDecode {
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

func TestClassifierLegacyCodeNormalization(t *testing.T) {
	tests := []struct {
		name string
		err  error
		ctx  CommandContext
		want Code
	}{
		{name: "SCN-DIAG-LEGACY-NORMALIZATION-01 live api failure", err: codedError{code: "live_api_failure", msg: "api"}, ctx: CommandContext{ProviderMode: "live-http", HTTPAttempted: true}, want: CodeAPIFailure},
		{name: "SCN-DIAG-LEGACY-NORMALIZATION-02 unsupported mock payload", err: codedError{code: "unsupported_mock_payload", msg: "payload"}, ctx: CommandContext{ProviderMode: "live-http"}, want: CodeSchemaDecode},
		{name: "SCN-DIAG-LEGACY-NORMALIZATION-03 live auth with http", err: codedError{code: "live_auth_failure", msg: "auth"}, ctx: CommandContext{ProviderMode: "live-http", HTTPAttempted: true}, want: CodeAPIFailure},
		{name: "SCN-DIAG-LEGACY-NORMALIZATION-04 non-live legacy api", err: codedError{code: "live_api_failure", msg: "api"}, ctx: CommandContext{ProviderMode: "offline-fixture"}, want: CodeAPIFailure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.err, tt.ctx); got.Code != tt.want {
				t.Fatalf("got %s want %s", got.Code, tt.want)
			}
		})
	}
}

func TestClassifierLiveDecommissionInvariant(t *testing.T) {
	badCodes := map[Code]bool{CodeLiveTransportFailure: true, CodeConfigurationError: true, CodeLiveAPIFailure: true, CodeLiveAuthFailure: true, CodeUnsupportedMockPayload: true, CodeMissingCredential: true, CodeInvalidAPIBaseURL: true}
	tests := []struct {
		name string
		err  error
		ctx  CommandContext
	}{
		{name: "SCN-DIAG-DECOM-01 400", err: codedError{code: "api_validation", msg: "bad request"}, ctx: CommandContext{ProviderMode: "live-http", HTTPStatus: http.StatusBadRequest, HTTPAttempted: true}},
		{name: "SCN-DIAG-DECOM-02 malformed json", err: codedError{code: "schema_decode", msg: "malformed"}, ctx: CommandContext{ProviderMode: "live-http", HTTPStatus: http.StatusOK, HTTPAttempted: true, MalformedSuccess: true}},
		{name: "SCN-DIAG-DECOM-03 schema mismatch", err: codedError{code: "schema_decode", msg: "shape"}, ctx: CommandContext{ProviderMode: "live-http", HTTPStatus: http.StatusOK, HTTPAttempted: true, SchemaDecodeFailure: true}},
		{name: "SCN-DIAG-DECOM-04 partial response", err: codedError{code: "partial_response", msg: "partial"}, ctx: CommandContext{ProviderMode: "live-http", HTTPAttempted: true, FailureSource: "partial_response"}},
		{name: "SCN-DIAG-DECOM-05 local body limit", err: codedError{code: "payload_too_large", msg: "large"}, ctx: CommandContext{ProviderMode: "live-http", HTTPAttempted: true, FailureSource: "local_body_limit"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(tt.err, tt.ctx)
			if badCodes[got.Code] {
				t.Fatalf("decommissioned visible class returned: %s", got.Code)
			}
			if got.Code != CodeAPIFailure && got.Code != CodeSchemaDecode {
				t.Fatalf("got %s want api_validation or schema_decode", got.Code)
			}
		})
	}
}

func mapValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, value := range m {
		out = append(out, value)
	}
	return out
}

func TestClassifierFailureSourceMapping(t *testing.T) {
	tests := []struct {
		name string
		err  error
		ctx  CommandContext
		want Code
	}{
		{name: "SCN-DIAG-FAILURE-SOURCE-01 remote status payload size api validation", err: codedError{code: "payload_too_large", msg: "too large"}, ctx: CommandContext{ProviderMode: "live-http", HTTPStatus: http.StatusRequestEntityTooLarge, HTTPAttempted: true, FailureSource: "remote_status"}, want: CodeAPIFailure},
		{name: "SCN-DIAG-FAILURE-SOURCE-02 local body limit schema decode", err: codedError{code: "payload_too_large", msg: "too large"}, ctx: CommandContext{ProviderMode: "live-http", HTTPAttempted: true, FailureSource: "local_body_limit"}, want: CodeSchemaDecode},
		{name: "SCN-DIAG-FAILURE-SOURCE-03 local decode boundary schema decode", err: codedError{code: "partial_response", msg: "partial"}, ctx: CommandContext{ProviderMode: "live-http", HTTPAttempted: true, FailureSource: "local_decode_boundary"}, want: CodeSchemaDecode},
		{name: "SCN-DIAG-FAILURE-SOURCE-04 write wrapper local body limit beats provider fallback", err: codedError{code: "write_provider_error", msg: "provider"}, ctx: CommandContext{ProviderMode: "live-http", HTTPAttempted: true, FailureSource: "local_body_limit"}, want: CodeSchemaDecode},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(tt.err, tt.ctx)
			if got.Code != tt.want {
				t.Fatalf("got %s want %s", got.Code, tt.want)
			}
		})
	}
}

func TestClassifierUnsupportedCapability(t *testing.T) {
	err := codedError{code: "unsupported_capability", msg: "gitcode: unsupported capability \"comments_read\": deferred for iteration 5"}
	d := Classify(err, CommandContext{ProviderMode: "live-http"})
	if d.Code != CodeUnsupportedCapability {
		t.Fatalf("expected CodeUnsupportedCapability, got %s", d.Code)
	}
	if d.ExitClass != "capability" {
		t.Fatalf("expected exitClass capability, got %s", d.ExitClass)
	}
	if d.HTTPAttempted {
		t.Fatal("expected HTTPAttempted false")
	}
	if d.Retryable {
		t.Fatal("expected Retryable false")
	}
	if !strings.Contains(d.Message, "unsupported_capability") {
		t.Fatalf("expected message to contain unsupported_capability: %s", d.Message)
	}
}
