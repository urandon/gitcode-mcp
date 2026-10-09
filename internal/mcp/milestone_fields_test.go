package mcp

import (
	"bytes"
	"encoding/json"
	"gitcode-mcp/internal/service"
	"io"
	"strings"
	"testing"
)

func TestMilestoneFieldErrorHasActionableSafeMessage(t *testing.T) {
	var out bytes.Buffer
	id := json.RawMessage(`1`)
	srv := &Server{writer: &out, stderr: io.Discard}
	srv.writeDomainError(&id, service.ErrInvalidQuery{Field: "milestone.description", Message: "description exceeds GitCode's maximum of 2000 UTF-16 code units; shorten it explicitly"})
	var resp response
	if err := json.Unmarshal(bytesTrimSpace(out.Bytes()), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil || resp.Error.Data == nil || resp.Error.Data.Code != "invalid_query" || !strings.Contains(out.String(), "2000 UTF-16") || !strings.Contains(out.String(), "shorten") {
		t.Fatalf("missing actionable diagnosis: %s", out.String())
	}
}

func TestMilestoneSchemaMatchesLiveFieldContract(t *testing.T) {
	create := writeToolInputSchema("create_milestone")
	if len(create.Properties["state"].Enum) != 1 || create.Properties["state"].Enum[0] != "open" || !strings.Contains(create.Properties["description"].Description, "2000 UTF-16") {
		t.Fatal("creation schema advertises unsupported input")
	}
	update := writeToolInputSchema("update_milestone")
	for _, field := range []string{"title", "due_on"} {
		found := false
		for _, v := range update.Required {
			if v == field {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing required field %s", field)
		}
	}
}
