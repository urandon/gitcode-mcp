#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
VALIDATION_TMPDIR="$(mktemp -d "${TMPDIR:-/tmp}/gitcode-mcp-startup-diag.XXXXXX")"
cleanup() {
  rm -rf "$VALIDATION_TMPDIR"
}
trap cleanup EXIT

TEST_FILE="$ROOT/internal/mcp/startup_diagnostic_design_validation_test.go"
cat > "$TEST_FILE" <<'GOEOF'
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"gitcode-mcp/internal/cache"
)

func TestDesignPackageStartupDiagnosticInjectionValidation(t *testing.T) {
	diagnostic := StartupDiagnosticFromError(os.ErrPermission)
	handler := NewMinimalRPCHandler(diagnostic)

	listID := json.RawMessage(`"list"`)
	listReq := request{JSONRPC: "2.0", ID: &listID, Method: "tools/list"}
	listResp, ok := handler.Handle(context.Background(), listReq)
	if !ok || listResp.Error != nil {
		t.Fatalf("tools/list response=%+v ok=%t", listResp, ok)
	}
	serializedList, err := json.Marshal(listResp)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"startup_diagnostic"`, `"error_class"`, `"message"`, `"remediation"`, `"doctor"`} {
		if !strings.Contains(string(serializedList), field) {
			t.Fatalf("serialized tools/list missing %s: %s", field, serializedList)
		}
	}
	var listResult toolsListResult
	if err := json.Unmarshal(listResp.Result, &listResult); err != nil {
		t.Fatal(err)
	}
	if listResult.StartupDiagnostic == nil || listResult.StartupDiagnostic.ErrorClass != "cache_path_unwritable" || listResult.StartupDiagnostic.Message == "" || listResult.StartupDiagnostic.Remediation == "" {
		t.Fatalf("tools/list startup diagnostic=%+v", listResult.StartupDiagnostic)
	}
	foundDoctor := false
	for _, tool := range listResult.Tools {
		if tool.Name == "doctor" {
			foundDoctor = true
		}
	}
	if !foundDoctor {
		t.Fatalf("tools/list did not advertise doctor: %+v", listResult.Tools)
	}

	doctorID := json.RawMessage(`"doctor"`)
	doctorParams := json.RawMessage(`{"name":"doctor","arguments":{}}`)
	doctorReq := request{JSONRPC: "2.0", ID: &doctorID, Method: "tools/call", Params: &doctorParams}
	doctorResp, ok := handler.Handle(context.Background(), doctorReq)
	if !ok || doctorResp.Error != nil {
		t.Fatalf("doctor response=%+v ok=%t", doctorResp, ok)
	}
	var callResult toolCallResult
	if err := json.Unmarshal(doctorResp.Result, &callResult); err != nil {
		t.Fatal(err)
	}
	var doctor doctorResult
	decodeStructured(t, callResult, &doctor)
	if doctor.Status != "degraded" || len(doctor.Diagnostics) != 1 {
		t.Fatalf("doctor result=%+v", doctor)
	}
	got := doctor.Diagnostics[0]
	if got.ErrorClass != "cache_path_unwritable" || got.Message == "" || got.Remediation == "" {
		t.Fatalf("doctor diagnostic=%+v", got)
	}
	if !strings.Contains(got.Remediation, "chmod") && !strings.Contains(got.Remediation, "cache-path") && !strings.Contains(got.Remediation, "cache path") {
		t.Fatalf("cache_path_unwritable remediation not actionable: %q", got.Remediation)
	}
}

func TestDesignPackageStartupDiagnosticRemediationValidation(t *testing.T) {
	schemaDiag := StartupDiagnosticFromError(&cache.SchemaVersionError{Compat: cache.VersionCompatibility{Message: "cache schema is newer than supported", Remediation: "upgrade the gitcode-mcp binary to a version that supports this schema"}})
	if schemaDiag.ErrorClass != "schema_incompatible" || schemaDiag.Message == "" || schemaDiag.Remediation == "" {
		t.Fatalf("schema diagnostic=%+v", schemaDiag)
	}
	if !strings.Contains(strings.ToLower(schemaDiag.Remediation), "upgrade") || !strings.Contains(strings.ToLower(schemaDiag.Remediation), "binary") {
		t.Fatalf("schema remediation must reference binary upgrade: %q", schemaDiag.Remediation)
	}

	genericDiag := StartupDiagnosticFromError(errors.New("panic: secret stack trace\n/private/path/file.go:10 goroutine 1 [running]"))
	if genericDiag.ErrorClass != "startup-failure" || genericDiag.Message == "" || genericDiag.Remediation == "" {
		t.Fatalf("generic diagnostic=%+v", genericDiag)
	}
	leaked := []string{"panic", "stack trace", "/private/path", ".go:10", "goroutine"}
	combined := strings.ToLower(genericDiag.Message + " " + genericDiag.Remediation)
	for _, token := range leaked {
		if strings.Contains(combined, token) {
			t.Fatalf("startup-failure diagnostic leaked raw failure detail %q: %+v", token, genericDiag)
		}
	}
	actionable := strings.Contains(genericDiag.Remediation, "gitcode-mcp") || strings.Contains(genericDiag.Remediation, "cache") || strings.Contains(genericDiag.Remediation, "retry")
	if !actionable {
		t.Fatalf("startup-failure remediation is not actionable: %q", genericDiag.Remediation)
	}
}
GOEOF

cleanup_test() {
  rm -f "$TEST_FILE"
}
trap 'cleanup_test; cleanup' EXIT

cd "$ROOT"
go test ./internal/mcp/... -run 'TestDesignPackageStartupDiagnostic' -count=1
go test ./...
git diff --check
