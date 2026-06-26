package diagnostics

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

type Code string

const (
	CodeMissingCredential       Code = "missing_credential"
	CodeConfigCredential        Code = "config_credential"
	CodeLiveAuthFailure         Code = "live_auth_failure"
	CodeLiveTransportFailure    Code = "live_transport_failure"
	CodeLiveAPIFailure          Code = "live_api_failure"
	CodeConfigurationError      Code = "configuration_error"
	CodeInvalidAPIBaseURL       Code = "invalid_api_base_url"
	CodeUnsupportedMockPayload  Code = "unsupported_mock_payload"
	CodeFixtureReadOnly         Code = "fixture_read_only"
	CodeFixtureFallbackDetected Code = "fixture_fallback_detected"
	CodeUnsupportedCapability   Code = "unsupported_capability"
	CodeCacheBusy               Code = "cache_busy"
	CodeSchemaDecode            Code = "schema_decode"
	CodeAPIFailure              Code = "api_validation"
)

type Diagnostic struct {
	Code          Code              `json:"failure_class"`
	Message       string            `json:"message"`
	ExitClass     string            `json:"exit_class"`
	HTTPAttempted bool              `json:"http_attempted"`
	Retryable     bool              `json:"retryable"`
	Context       map[string]string `json:"context,omitempty"`
}

type CommandContext struct {
	ProviderMode                string
	Command                     string
	SelectedAPIBaseURL          string
	RepositoryBindingID         string
	CachePathPresent            bool
	AuditPathPresent            bool
	PayloadKind                 string
	PayloadSource               string
	RequiredField               string
	HTTPStatus                  int
	HTTPAttempted               bool
	SensitiveTerms              []string
	SensitiveContextKeys        []string
	BroaderConfigurationInvalid bool
	MissingCredential           bool
	InvalidSelectedAPIBaseURL   bool
	UnsupportedPayload          bool
	FixtureFallbackSentinel     bool
	FixtureReadOnly             bool
	TransportFailure            bool
	APIFailure                  bool
	SchemaDecodeFailure         bool
	MalformedSuccess            bool
	LocalPayloadTooLarge        bool
	FailureSource               string
}

type Classifier struct {
	Filter Filter
}

func NewClassifier(sensitiveTerms ...string) Classifier {
	return Classifier{Filter: NewFilter(sensitiveTerms...)}
}

func Classify(err error, ctx CommandContext) Diagnostic {
	return NewClassifier(ctx.SensitiveTerms...).Classify(err, ctx)
}

func (c Classifier) Classify(err error, ctx CommandContext) Diagnostic {
	filter := c.Filter
	if len(ctx.SensitiveTerms) > 0 {
		filter.SensitiveTerms = append(filter.SensitiveTerms, ctx.SensitiveTerms...)
	}
	code := classifyCode(err, ctx)
	d := Diagnostic{Code: code, Message: messageFor(code, err), ExitClass: exitClassFor(code), HTTPAttempted: httpAttemptedFor(code, ctx), Retryable: retryableFor(code)}
	d.Message = filter.RedactText(d.Message)
	d.Context = redactedContext(filter, ctx)
	return d
}

func (d Diagnostic) Error() string {
	if d.Message == "" {
		return string(d.Code)
	}
	return d.Message
}

func (d Diagnostic) JSON() ([]byte, error) {
	return json.Marshal(d)
}

func classifyCode(err error, ctx CommandContext) Code {
	if ctx.ProviderMode != "live-http" {
		if ctx.FixtureReadOnly || hasCode(err, "fixture_read_only") || hasCode(err, "write_fixture_read_only") {
			return CodeFixtureReadOnly
		}
		return codeFromError(err)
	}
	if ctx.BroaderConfigurationInvalid || hasCode(err, "configuration_error") || (!ctx.HTTPAttempted && isConfigurationInputBug(err)) {
		return CodeConfigCredential
	}
	if ctx.MissingCredential || hasCode(err, "missing_credential") || hasCode(err, "write_missing_credential") {
		return CodeConfigCredential
	}
	if ctx.InvalidSelectedAPIBaseURL || hasCode(err, "invalid_api_base_url") {
		return CodeConfigCredential
	}
	if (ctx.HTTPStatus == http.StatusUnauthorized || ctx.HTTPStatus == http.StatusForbidden || hasCode(err, "live_auth_failure") || hasCode(err, "auth_expired") || hasCode(err, "forbidden") || hasCode(err, "write_unauthorized")) && ctx.HTTPAttempted {
		return CodeAPIFailure
	}
	if hasCode(err, "auth_expired") || hasCode(err, "forbidden") || hasCode(err, "write_unauthorized") || hasCode(err, "live_auth_failure") {
		return CodeConfigCredential
	}
	if ctx.HTTPStatus >= 200 && ctx.HTTPStatus <= 299 && (ctx.SchemaDecodeFailure || ctx.MalformedSuccess || hasCode(err, "schema_decode")) {
		return CodeSchemaDecode
	}
	if ctx.HTTPStatus >= 400 && ctx.HTTPStatus <= 499 && ctx.HTTPStatus != http.StatusUnauthorized && ctx.HTTPStatus != http.StatusForbidden && ctx.HTTPAttempted {
		return CodeAPIFailure
	}
	if ctx.FailureSource == "remote_status" && ctx.HTTPAttempted {
		return CodeAPIFailure
	}
	if ctx.LocalPayloadTooLarge || ctx.FailureSource == "local_body_limit" || ctx.FailureSource == "local_decode_boundary" || ctx.FailureSource == "partial_response" || hasCode(err, "partial_response") || hasCode(err, "unsupported_mock_payload") || hasCode(err, "live_graph_invalid") || hasCode(err, "validation_failed") {
		return CodeSchemaDecode
	}
	if (ctx.APIFailure || hasCode(err, "live_api_failure") || hasCode(err, "write_provider_error")) && ctx.HTTPAttempted {
		return CodeAPIFailure
	}
	if (hasCode(err, "api_validation") || hasCode(err, "not_found") || hasCode(err, "remote_conflict") || hasCode(err, "remote_collision") || hasCode(err, "remote_not_found") || hasCode(err, "rate_limited")) && ctx.HTTPAttempted {
		return CodeAPIFailure
	}
	if ctx.UnsupportedPayload || hasCode(err, "unsupported_mock_payload") || hasCode(err, "live_graph_invalid") || hasCode(err, "validation_failed") {
		return CodeSchemaDecode
	}
	if hasCode(err, "schema_decode") {
		return CodeSchemaDecode
	}
	if (ctx.TransportFailure || hasCode(err, "live_transport_failure") || hasCode(err, "network_unavailable") || hasCode(err, "write_network_unavailable")) && ctx.HTTPAttempted {
		return CodeLiveTransportFailure
	}
	if ctx.HTTPStatus >= 500 && ctx.HTTPAttempted {
		return CodeLiveTransportFailure
	}
	if ctx.FixtureFallbackSentinel || hasCode(err, "fixture_fallback_detected") || hasCode(err, "write_fixture_fallback_detected") || hasCode(err, "fixture_read_only") || hasCode(err, "write_fixture_read_only") {
		return CodeFixtureFallbackDetected
	}
	if hasCode(err, "unsupported_capability") {
		return CodeUnsupportedCapability
	}
	if hasCode(err, "cache_busy") {
		return CodeCacheBusy
	}
	return CodeConfigurationError
}

func codeFromError(err error) Code {
	for _, code := range diagnosticCodes(err) {
		switch code {
		case "missing_credential":
			return CodeMissingCredential
		case "live_auth_failure":
			return CodeAPIFailure
		case "live_transport_failure", "network_unavailable", "write_network_unavailable":
			return CodeLiveTransportFailure
		case "live_api_failure", "write_provider_error":
			return CodeAPIFailure
		case "configuration_error":
			return CodeConfigurationError
		case "invalid_api_base_url":
			return CodeInvalidAPIBaseURL
		case "unsupported_mock_payload", "live_graph_invalid", "validation_failed":
			return CodeSchemaDecode
		case "fixture_read_only", "write_fixture_read_only":
			return CodeFixtureReadOnly
		case "fixture_fallback_detected", "write_fixture_fallback_detected":
			return CodeFixtureFallbackDetected
		case "unsupported_capability":
			return CodeUnsupportedCapability
		case "cache_busy":
			return CodeCacheBusy
		case "schema_decode", "partial_response":
			return CodeSchemaDecode
		case "auth_expired", "forbidden", "write_unauthorized", "not_found", "remote_conflict", "remote_collision", "remote_not_found", "rate_limited":
			return CodeAPIFailure
		}
	}
	return CodeConfigurationError
}

func hasCode(err error, want string) bool {
	for _, code := range diagnosticCodes(err) {
		if code == want {
			return true
		}
	}
	return false
}

func diagnosticCodes(err error) []string {
	var out []string
	for err != nil {
		if coded, ok := err.(interface{ DiagnosticCode() string }); ok {
			if code := strings.TrimSpace(coded.DiagnosticCode()); code != "" {
				out = append(out, code)
			}
		}
		unwrapped := errors.Unwrap(err)
		if unwrapped == nil {
			break
		}
		err = unwrapped
	}
	return out
}

func isConfigurationInputBug(err error) bool {
	if err == nil {
		return false
	}
	for _, code := range diagnosticCodes(err) {
		if code == "invalid_query" || code == "repo_required" || code == "not_found" {
			return true
		}
	}
	return false
}

func httpAttemptedFor(code Code, ctx CommandContext) bool {
	switch code {
	case CodeLiveAuthFailure, CodeLiveTransportFailure, CodeLiveAPIFailure, CodeAPIFailure, CodeSchemaDecode:
		return ctx.HTTPAttempted
	case CodeConfigCredential:
		return false
	default:
		return false
	}
}

func retryableFor(code Code) bool {
	return code == CodeLiveTransportFailure || code == CodeCacheBusy
}

func exitClassFor(code Code) string {
	switch code {
	case CodeMissingCredential, CodeLiveAuthFailure:
		return "auth"
	case CodeConfigCredential:
		return "configuration"
	case CodeLiveTransportFailure:
		return "transport"
	case CodeLiveAPIFailure, CodeAPIFailure:
		return "provider"
	case CodeUnsupportedMockPayload:
		return "payload"
	case CodeFixtureFallbackDetected, CodeFixtureReadOnly:
		return "fixture"
	case CodeUnsupportedCapability:
		return "capability"
	case CodeSchemaDecode:
		return "schema"
	case CodeCacheBusy:
		return "cache"
	default:
		return "configuration"
	}
}

func messageFor(code Code, err error) string {
	base := string(code)
	switch code {
	case CodeMissingCredential:
		base += ": live provider requires a configured credential"
	case CodeConfigCredential:
		base += ": live provider requires valid configuration and credential"
	case CodeLiveAuthFailure:
		base += ": live provider rejected credentials"
	case CodeLiveTransportFailure:
		base += ": live provider request failed before a valid API response"
	case CodeLiveAPIFailure:
		base += ": live provider returned an API failure"
	case CodeAPIFailure:
		base += ": live provider returned an API validation failure"
	case CodeConfigurationError:
		base += ": live command configuration is invalid"
	case CodeInvalidAPIBaseURL:
		base += ": selected live api_base_url is invalid"
	case CodeUnsupportedMockPayload:
		base += ": live provider payload is unsupported"
	case CodeFixtureReadOnly:
		base += ": fixture provider is read-only"
	case CodeFixtureFallbackDetected:
		base += ": live route reached fixture behavior"
	case CodeUnsupportedCapability:
		base += ": requested capability is not supported for this provider"
	case CodeSchemaDecode:
		base += ": response schema decode failure"
	case CodeCacheBusy:
		base += ": cache is busy, retry later"
	}
	if err != nil {
		return base + ": " + err.Error()
	}
	return base
}

func redactedContext(filter Filter, ctx CommandContext) map[string]string {
	values := map[string]string{}
	put := func(key, value string) {
		if strings.TrimSpace(value) != "" {
			values[key] = filter.RedactText(value)
		}
	}
	put("provider_mode", ctx.ProviderMode)
	put("command", ctx.Command)
	put("api_base_url", filter.RedactURL(ctx.SelectedAPIBaseURL))
	put("repository_binding", ctx.RepositoryBindingID)
	put("payload_kind", ctx.PayloadKind)
	put("payload_source", ctx.PayloadSource)
	put("required_field", ctx.RequiredField)
	if ctx.HTTPStatus != 0 {
		put("http_status", fmt.Sprintf("%d", ctx.HTTPStatus))
	}
	put("cache_path_present", fmt.Sprintf("%t", ctx.CachePathPresent))
	put("audit_path_present", fmt.Sprintf("%t", ctx.AuditPathPresent))
	for _, key := range ctx.SensitiveContextKeys {
		delete(values, key)
	}
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make(map[string]string, len(values))
	for _, key := range keys {
		out[key] = values[key]
	}
	return out
}
