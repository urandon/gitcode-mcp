package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"gitcode-mcp/internal/service"
	"io"
	"strings"
	"testing"
)

func TestWikiWriteErrorExposesSafeReconciliation(t *testing.T) {
	attempted := true
	err := service.ErrWriteFailure{Code: "write_ambiguous_remote", RemoteID: "Home.md", IdempotencyKey: "wiki-key", WritePhase: "readback", MutationAttempted: &attempted, ProviderFailureClass: "schema_decode", Reconciliation: "reuse same key for GET-only reconciliation", Cause: errors.New("opaque-private-provider-body")}
	var out bytes.Buffer
	id := json.RawMessage(`1`)
	srv := &Server{writer: &out, stderr: io.Discard}
	srv.writeDomainError(&id, err)
	var resp response
	if e := json.Unmarshal(bytesTrimSpace(out.Bytes()), &resp); e != nil {
		t.Fatal(e)
	}
	if resp.Error == nil || resp.Error.Data == nil {
		t.Fatal("missing typed error")
	}
	data := resp.Error.Data
	if data.IdempotencyKey != "wiki-key" || data.RemotePath != "Home.md" || data.WritePhase != "readback" || data.MutationAttempted == nil || !*data.MutationAttempted || data.ProviderFailureClass != "schema_decode" || !strings.Contains(data.Remediation, "GET-only") || strings.Contains(out.String(), "opaque-private") {
		t.Fatalf("unsafe or incomplete response: %s", out.String())
	}
}
