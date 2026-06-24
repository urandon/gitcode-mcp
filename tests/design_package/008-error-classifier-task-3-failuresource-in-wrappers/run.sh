#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
VALIDATION_DIR="$ROOT/tests/design_package/008-error-classifier-task-3-failuresource-in-wrappers"
DIAG_TEST_FILE="$VALIDATION_DIR/task008_runtime_validation_test.go"
CLI_TEST_FILE="$ROOT/internal/cli/task008_runtime_validation_test.go"
MCP_TEST_FILE="$ROOT/internal/mcp/task008_runtime_validation_test.go"
cleanup() {
  rm -f "$DIAG_TEST_FILE" "$CLI_TEST_FILE" "$MCP_TEST_FILE"
}
trap cleanup EXIT

cat > "$DIAG_TEST_FILE" <<'GOEOF'
package task008validation

import (
	"errors"
	"net/http"
	"testing"

	"gitcode-mcp/internal/diagnostics"
	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/service"
)

func TestScenario1FailureSourceClassification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		ctx  diagnostics.CommandContext
		want diagnostics.Code
	}{
		{name: "008-error-classifier-task-3-failuresource-in-wrappers-scenario-1 remote 413", err: gitcode.ErrPayloadTooLarge{Endpoint: "/api/v5/repos/o/r/issues", Limit: 10, Size: 11, Source: "remote_status"}, ctx: diagnostics.CommandContext{ProviderMode: "live-http", HTTPStatus: http.StatusRequestEntityTooLarge, HTTPAttempted: true, FailureSource: "remote_status"}, want: diagnostics.CodeAPIFailure},
		{name: "008-error-classifier-task-3-failuresource-in-wrappers-scenario-1 local limit unknown status", err: gitcode.ErrPayloadTooLarge{Endpoint: "/api/v5/repos/o/r/issues", Limit: 10, Size: 11, Source: "local_body_limit"}, ctx: diagnostics.CommandContext{ProviderMode: "live-http", HTTPAttempted: true, FailureSource: "local_body_limit", LocalPayloadTooLarge: true}, want: diagnostics.CodeSchemaDecode},
		{name: "008-error-classifier-task-3-failuresource-in-wrappers-scenario-1 local limit success status", err: gitcode.ErrPayloadTooLarge{Endpoint: "/api/v5/repos/o/r/issues", Limit: 10, Size: 11, Source: "local_body_limit"}, ctx: diagnostics.CommandContext{ProviderMode: "live-http", HTTPStatus: http.StatusOK, HTTPAttempted: true, FailureSource: "local_body_limit", LocalPayloadTooLarge: true}, want: diagnostics.CodeSchemaDecode},
		{name: "008-error-classifier-task-3-failuresource-in-wrappers-scenario-1 partial response", err: gitcode.ErrPartialResponse{Endpoint: "/api/v5/repos/o/r/issues", Expected: 10, Got: 5}, ctx: diagnostics.CommandContext{ProviderMode: "live-http", HTTPStatus: http.StatusOK, HTTPAttempted: true, FailureSource: "partial_response", SchemaDecodeFailure: true}, want: diagnostics.CodeSchemaDecode},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tooLarge, ok := tc.err.(gitcode.ErrPayloadTooLarge); ok && tooLarge.FailureSource() != tooLarge.Source {
				t.Fatalf("FailureSource()=%q want Source %q", tooLarge.FailureSource(), tooLarge.Source)
			}
			got := diagnostics.Classify(tc.err, tc.ctx)
			if got.Code != tc.want {
				t.Fatalf("Classify()=%s want %s", got.Code, tc.want)
			}
			assertNotDecommissioned(t, got.Code)
		})
	}
}

func TestScenario2ServiceWrapperPreservesPayloadSource(t *testing.T) {
	cause := gitcode.ErrPayloadTooLarge{Endpoint: "/api/v5/repos/o/r/issues", Limit: 5, Size: 6, Source: "local_body_limit"}
	wrapped := service.ErrSyncFailure{Mode: "payload_too_large", Target: "issue:*", Endpoint: cause.Endpoint, LimitBytes: cause.Limit, SizeBytes: cause.Size, PayloadSource: cause.Source, Cause: cause}
	if wrapped.PayloadSource != "local_body_limit" {
		t.Fatalf("PayloadSource=%q want local_body_limit", wrapped.PayloadSource)
	}
	var extracted gitcode.ErrPayloadTooLarge
	if !errors.As(wrapped, &extracted) {
		t.Fatalf("errors.As did not reach ErrPayloadTooLarge")
	}
	if extracted.Source != "local_body_limit" {
		t.Fatalf("extracted Source=%q want local_body_limit", extracted.Source)
	}
	got := diagnostics.Classify(wrapped, diagnostics.CommandContext{ProviderMode: "live-http", HTTPAttempted: true, FailureSource: wrapped.PayloadSource, LocalPayloadTooLarge: true})
	if got.Code != diagnostics.CodeSchemaDecode {
		t.Fatalf("wrapped Classify()=%s want %s", got.Code, diagnostics.CodeSchemaDecode)
	}
	assertNotDecommissioned(t, got.Code)

	remote := service.ErrSyncFailure{Mode: "payload_too_large", Target: "issue:*", PayloadSource: "remote_status", Cause: gitcode.ErrPayloadTooLarge{Endpoint: "/api/v5/repos/o/r/issues", Limit: 5, Size: 6, Source: "remote_status"}}
	remoteGot := diagnostics.Classify(remote, diagnostics.CommandContext{ProviderMode: "live-http", HTTPStatus: http.StatusRequestEntityTooLarge, HTTPAttempted: true, FailureSource: remote.PayloadSource})
	if remoteGot.Code != diagnostics.CodeAPIFailure {
		t.Fatalf("remote wrapped Classify()=%s want %s", remoteGot.Code, diagnostics.CodeAPIFailure)
	}
	assertNotDecommissioned(t, remoteGot.Code)
}

func assertNotDecommissioned(t *testing.T, code diagnostics.Code) {
	t.Helper()
	decommissioned := map[diagnostics.Code]bool{
		diagnostics.CodeLiveTransportFailure:   true,
		diagnostics.CodeConfigurationError:     true,
		diagnostics.CodeLiveAPIFailure:         true,
		diagnostics.CodeLiveAuthFailure:        true,
		diagnostics.CodeUnsupportedMockPayload: true,
	}
	if decommissioned[code] {
		t.Fatalf("decommissioned visible class returned: %s", code)
	}
}
GOEOF

cat > "$CLI_TEST_FILE" <<'GOEOF'
package cli

import (
	"bytes"
	"strings"
	"testing"

	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/service"
)

func TestTask008CLIRenderingUsesPayloadSource(t *testing.T) {
	err := service.ErrSyncFailure{Mode: "payload_too_large", Target: "issue:*", PayloadSource: "local_body_limit", Cause: gitcode.ErrPayloadTooLarge{Endpoint: "/api/v5/repos/o/r/issues", Limit: 5, Size: 6, Source: "local_body_limit"}}
	var stderr bytes.Buffer
	writeCommandError(&stderr, "text", startupPlan{ProviderMode: "live-http", Command: "sync"}, err)
	out := stderr.String()
	if !strings.Contains(out, "failure_class: schema_decode") {
		t.Fatalf("CLI output missing schema_decode failure class: %q", out)
	}
	if strings.Contains(out, "failure_class: live_transport_failure") || strings.Contains(out, "failure_class: configuration_error") {
		t.Fatalf("CLI output used decommissioned failure class: %q", out)
	}
}
GOEOF

cat > "$MCP_TEST_FILE" <<'GOEOF'
package mcp

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"

	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/service"
)

func TestTask008MCPRenderingUsesPayloadSource(t *testing.T) {
	err := service.ErrWriteFailure{Code: "write_provider_error", PayloadSource: "local_body_limit", Cause: gitcode.ErrPayloadTooLarge{Endpoint: "/api/v5/repos/o/r/issues", Limit: 5, Size: 6, Source: "local_body_limit"}}
	var out bytes.Buffer
	id := json.RawMessage(`"task008"`)
	srv := &Server{writer: &out, stderr: io.Discard}
	srv.writeDomainError(&id, err)
	var resp response
	if err := json.Unmarshal(bytesTrimSpace(out.Bytes()), &resp); err != nil {
		t.Fatalf("decode MCP response: %v body=%q", err, out.String())
	}
	if resp.Error == nil || resp.Error.Data == nil {
		t.Fatalf("missing MCP error data: %#v", resp)
	}
	if resp.Error.Data.FailureClass != "schema_decode" {
		t.Fatalf("failure_class=%q want schema_decode body=%q", resp.Error.Data.FailureClass, out.String())
	}
}
GOEOF

cd "$ROOT"
go test ./tests/design_package/008-error-classifier-task-3-failuresource-in-wrappers
go test ./internal/diagnostics/...
go test ./internal/service/...
go test ./internal/cli -run TestTask008CLIRenderingUsesPayloadSource
go test ./internal/mcp -run TestTask008MCPRenderingUsesPayloadSource
