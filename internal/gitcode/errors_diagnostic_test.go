package gitcode

import (
	"fmt"
	"net/http"
	"testing"

	"gitcode-mcp/internal/diagnostics"
)

func TestDiagnosticCodeOnGitCodeErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
		code diagnostics.Code
		ctx  diagnostics.CommandContext
	}{
		{name: "SCN-DIAG-CODE-GITCODE-01 not found", err: ErrNotFound{Endpoint: "/issues/404"}, want: "not_found", code: diagnostics.CodeAPIFailure, ctx: diagnostics.CommandContext{ProviderMode: "live-http", HTTPStatus: http.StatusNotFound, HTTPAttempted: true}},
		{name: "SCN-DIAG-CODE-GITCODE-02 conflict", err: ErrConflict{Endpoint: "/wiki/page", Status: http.StatusConflict}, want: "remote_conflict", code: diagnostics.CodeAPIFailure, ctx: diagnostics.CommandContext{ProviderMode: "live-http", HTTPStatus: http.StatusConflict, HTTPAttempted: true}},
		{name: "SCN-DIAG-CODE-GITCODE-03 remote collision", err: ErrRemoteCollision{Alias: "issue:1", ExistingID: "ISSUE-1", NewID: "ISSUE-2"}, want: "remote_collision", code: diagnostics.CodeAPIFailure, ctx: diagnostics.CommandContext{ProviderMode: "live-http", HTTPAttempted: true}},
		{name: "SCN-DIAG-CODE-GITCODE-04 remote not found", err: ErrRemoteNotFound{Endpoint: "/issues/404", Alias: "issue:404"}, want: "remote_not_found", code: diagnostics.CodeAPIFailure, ctx: diagnostics.CommandContext{ProviderMode: "live-http", HTTPStatus: http.StatusNotFound, HTTPAttempted: true}},
		{name: "SCN-DIAG-CODE-GITCODE-05 payload too large", err: ErrPayloadTooLarge{Endpoint: "/issues", Limit: 10, Size: 11, Source: "local_body_limit"}, want: "payload_too_large", code: diagnostics.CodeSchemaDecode, ctx: diagnostics.CommandContext{ProviderMode: "live-http", HTTPAttempted: true, FailureSource: "local_body_limit"}},
		{name: "SCN-DIAG-CODE-GITCODE-06 partial response", err: ErrPartialResponse{Endpoint: "/issues", Expected: 10, Got: 5}, want: "partial_response", code: diagnostics.CodeSchemaDecode, ctx: diagnostics.CommandContext{ProviderMode: "live-http", HTTPAttempted: true}},
		{name: "SCN-DIAG-CODE-GITCODE-07 rate limited", err: ErrRateLimited{Endpoint: "/issues", Attempts: 1}, want: "rate_limited", code: diagnostics.CodeAPIFailure, ctx: diagnostics.CommandContext{ProviderMode: "live-http", HTTPStatus: http.StatusTooManyRequests, HTTPAttempted: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, err := range []error{tt.err, fmt.Errorf("wrap: %w", tt.err)} {
				coded, ok := err.(interface{ DiagnosticCode() string })
				if !ok {
					coded = unwrapDiagnosticCode(err)
				}
				if coded == nil || coded.DiagnosticCode() != tt.want {
					t.Fatalf("DiagnosticCode(%T)=%v want %q", err, coded, tt.want)
				}
				if got := diagnostics.Classify(err, tt.ctx); got.Code != tt.code {
					t.Fatalf("Classify(%T)=%s want %s", err, got.Code, tt.code)
				}
			}
		})
	}
}

func unwrapDiagnosticCode(err error) interface{ DiagnosticCode() string } {
	wrapped := err
	for {
		coded, ok := wrapped.(interface{ DiagnosticCode() string })
		if ok {
			return coded
		}
		unwrapper, ok := wrapped.(interface{ Unwrap() error })
		if !ok || unwrapper.Unwrap() == nil {
			return nil
		}
		wrapped = unwrapper.Unwrap()
	}
}
