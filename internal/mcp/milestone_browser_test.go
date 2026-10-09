package mcp

import (
	"bytes"
	"encoding/json"
	"gitcode-mcp/internal/service"
	"io"
	"testing"
)

func TestMilestoneCanonicalLocatorStructuredResult(t *testing.T) {
	const locator = "https://example.invalid/owner/repo/milestones/123456?iid=7"
	var out bytes.Buffer
	server := &Server{writer: &out, stderr: io.Discard}
	id := json.RawMessage(`1`)
	server.writeToolResult(&id, toolCallResult{StructuredContent: service.MilestoneListResult{Milestones: []service.MilestoneRecord{{ID: "MILESTONE-123456", RemoteID: "123456", IID: "7", BrowserURL: locator}}}})
	var wire struct {
		Result struct {
			StructuredContent service.MilestoneListResult `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(bytesTrimSpace(out.Bytes()), &wire); err != nil {
		t.Fatal(err)
	}
	m := wire.Result.StructuredContent.Milestones[0]
	if m.ID != "MILESTONE-123456" || m.RemoteID != "123456" || m.IID != "7" || m.BrowserURL != locator {
		t.Fatalf("MCP identities/locator missing: %+v", m)
	}
}
