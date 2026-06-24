#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
VALIDATION_DIR="$ROOT/tests/design_package/007-error-classifier-task-2-diagnosticcode-on-gitcode-errors"
TEST_FILE="$VALIDATION_DIR/task007_runtime_validation_test.go"
cleanup() {
  rm -f "$TEST_FILE"
}
trap cleanup EXIT

cat > "$TEST_FILE" <<'GOEOF'
package task007validation

import (
	"fmt"
	"net/http"
	"testing"

	"gitcode-mcp/internal/diagnostics"
	"gitcode-mcp/internal/gitcode"
)

type diagnosticCoded interface{ DiagnosticCode() string }

func TestScenario1DiagnosticCodesAndCanonicalClassification(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode string
		ctx      diagnostics.CommandContext
		want     diagnostics.Code
	}{
		{name: "SCN-DIAG-CODE-GITCODE-01 ErrNotFound", err: gitcode.ErrNotFound{Endpoint: "/api/v5/repos/o/r/issues/404"}, wantCode: "not_found", ctx: diagnostics.CommandContext{ProviderMode: "live-http", HTTPStatus: http.StatusNotFound, HTTPAttempted: true}, want: diagnostics.CodeAPIFailure},
		{name: "SCN-DIAG-CODE-GITCODE-02 ErrConflict", err: gitcode.ErrConflict{Endpoint: "/api/v5/repos/o/r/wiki/page", Status: http.StatusConflict}, wantCode: "remote_conflict", ctx: diagnostics.CommandContext{ProviderMode: "live-http", HTTPStatus: http.StatusConflict, HTTPAttempted: true}, want: diagnostics.CodeAPIFailure},
		{name: "SCN-DIAG-CODE-GITCODE-03 ErrRemoteCollision", err: gitcode.ErrRemoteCollision{Alias: "issue:3", ExistingID: "ISSUE-3", NewID: "ISSUE-4", Endpoint: "/api/v5/repos/o/r/issues"}, wantCode: "remote_collision", ctx: diagnostics.CommandContext{ProviderMode: "live-http", HTTPAttempted: true}, want: diagnostics.CodeAPIFailure},
		{name: "SCN-DIAG-CODE-GITCODE-04 ErrRemoteNotFound", err: gitcode.ErrRemoteNotFound{Endpoint: "/api/v5/repos/o/r/issues/404", Alias: "issue:404"}, wantCode: "remote_not_found", ctx: diagnostics.CommandContext{ProviderMode: "live-http", HTTPStatus: http.StatusNotFound, HTTPAttempted: true}, want: diagnostics.CodeAPIFailure},
		{name: "SCN-DIAG-CODE-GITCODE-05 ErrPayloadTooLarge", err: gitcode.ErrPayloadTooLarge{Endpoint: "/api/v5/repos/o/r/issues", Limit: 10, Size: 11, Source: "local_body_limit"}, wantCode: "payload_too_large", ctx: diagnostics.CommandContext{ProviderMode: "live-http", HTTPAttempted: true, FailureSource: "local_body_limit"}, want: diagnostics.CodeSchemaDecode},
		{name: "SCN-DIAG-CODE-GITCODE-06 ErrPartialResponse", err: gitcode.ErrPartialResponse{Endpoint: "/api/v5/repos/o/r/issues", Expected: 10, Got: 5}, wantCode: "partial_response", ctx: diagnostics.CommandContext{ProviderMode: "live-http", HTTPAttempted: true}, want: diagnostics.CodeSchemaDecode},
		{name: "SCN-DIAG-CODE-GITCODE-07 ErrRateLimited", err: gitcode.ErrRateLimited{Endpoint: "/api/v5/repos/o/r/issues", Attempts: 1}, wantCode: "rate_limited", ctx: diagnostics.CommandContext{ProviderMode: "live-http", HTTPStatus: http.StatusTooManyRequests, HTTPAttempted: true}, want: diagnostics.CodeAPIFailure},
	}
	decommissioned := map[diagnostics.Code]bool{
		diagnostics.CodeLiveTransportFailure:   true,
		diagnostics.CodeConfigurationError:     true,
		diagnostics.CodeLiveAPIFailure:         true,
		diagnostics.CodeLiveAuthFailure:        true,
		diagnostics.CodeUnsupportedMockPayload: true,
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, err := range []error{tc.err, fmt.Errorf("wrapped gitcode error: %w", tc.err)} {
				coded, ok := err.(diagnosticCoded)
				if !ok {
					var extracted diagnosticCoded
					if !asDiagnosticCode(err, &extracted) {
						t.Fatalf("%T does not expose DiagnosticCode through unwrap", err)
					}
					coded = extracted
				}
				if got := coded.DiagnosticCode(); got != tc.wantCode {
					t.Fatalf("DiagnosticCode()=%q want %q", got, tc.wantCode)
				}
				diag := diagnostics.Classify(err, tc.ctx)
				if diag.Code != tc.want {
					t.Fatalf("Classify()=%s want %s", diag.Code, tc.want)
				}
				if decommissioned[diag.Code] {
					t.Fatalf("decommissioned visible class returned: %s", diag.Code)
				}
			}
		})
	}
}

func TestScenario2GitCodeErrorMessagesUnchanged(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "ErrNotFound", err: gitcode.ErrNotFound{Endpoint: "/issues/404"}, want: "gitcode: not found at /issues/404"},
		{name: "ErrConflict", err: gitcode.ErrConflict{Endpoint: "/wiki/page"}, want: "gitcode: conflict for /wiki/page: remote conflict"},
		{name: "ErrRemoteCollision", err: gitcode.ErrRemoteCollision{Alias: "issue:1", ExistingID: "ISSUE-1", NewID: "ISSUE-2"}, want: "gitcode: remote id issue:1 already maps to ISSUE-1; cannot map to ISSUE-2"},
		{name: "ErrRemoteNotFound", err: gitcode.ErrRemoteNotFound{Endpoint: "/issues/404", Alias: "issue:404"}, want: "gitcode: remote record not found for alias issue:404 at /issues/404"},
		{name: "ErrPayloadTooLarge", err: gitcode.ErrPayloadTooLarge{Endpoint: "/issues", Limit: 10, Size: 11}, want: "gitcode: response for /issues exceeds maximum size 10 bytes"},
		{name: "ErrPartialResponse", err: gitcode.ErrPartialResponse{Endpoint: "/issues", Expected: 10, Got: 5}, want: "gitcode: partial response for /issues: expected 10 bytes, got 5 bytes"},
		{name: "ErrRateLimited", err: gitcode.ErrRateLimited{Endpoint: "/issues", Attempts: 1}, want: "gitcode: rate limited for /issues after 1 attempt(s): retry after 0s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Fatalf("Error()=%q want %q", got, tc.want)
			}
		})
	}
}

func asDiagnosticCode(err error, target *diagnosticCoded) bool {
	for err != nil {
		if coded, ok := err.(diagnosticCoded); ok {
			*target = coded
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}
GOEOF

cd "$ROOT"
go test ./tests/design_package/007-error-classifier-task-2-diagnosticcode-on-gitcode-errors
go test ./internal/diagnostics/...
go test ./internal/gitcode/...
