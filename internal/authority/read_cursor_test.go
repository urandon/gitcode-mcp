package authority

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func encodePage(t *testing.T, cursor ReadPageCursor) string {
	t.Helper()
	raw, err := json.Marshal(cursor)
	if err != nil {
		t.Fatal("synthetic cursor encoding failed")
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func TestReadCursorExactSchemaAndScope(t *testing.T) {
	r := ReadRequest{ProjectID: exampleID, Kind: Issue, Operation: "list"}
	c := ReadPageCursor{Project: exampleID, Kind: Issue, Operation: "list", Snapshot: 5, Last: 2}
	valid := encodePage(t, c)
	if _, err := DecodeReadPageCursor(valid, r); err != nil {
		t.Fatal("valid cursor rejected")
	}
	for _, raw := range []string{
		`{}`, `null`,
		`{"project":"12345678-1234-4234-8234-123456789abc","kind":"issue","operation":"list","entity":"","snapshot":5,"last":2,"last":3}`,
	} {
		_, err := DecodeReadPageCursor(base64.RawURLEncoding.EncodeToString([]byte(raw)), r)
		requireError(t, err, InvalidContract)
	}
	for _, mutate := range []func(*ReadPageCursor){
		func(c *ReadPageCursor) { c.Project = exampleEntity },
		func(c *ReadPageCursor) { c.Kind = Label },
		func(c *ReadPageCursor) { c.Operation = "history"; c.Entity = exampleEntity },
		func(c *ReadPageCursor) { c.Entity = exampleEntity },
		func(c *ReadPageCursor) { c.Snapshot = 0 },
		func(c *ReadPageCursor) { c.Last = 0 },
		func(c *ReadPageCursor) { c.Last = -1 },
		func(c *ReadPageCursor) { c.Last = 6 },
	} {
		changed := c
		mutate(&changed)
		_, err := DecodeReadPageCursor(encodePage(t, changed), r)
		requireError(t, err, InvalidContract)
	}
	for _, encoded := range []string{"", "malformed!", valid + "=", valid + "\n", strings.Repeat("a", 513)} {
		_, err := DecodeReadPageCursor(encoded, r)
		requireError(t, err, InvalidContract)
	}
	r.Cursor = valid
	if r.Validate() != nil {
		t.Fatal("valid input cursor rejected")
	}
	r.Kind = Label
	requireError(t, r.Validate(), InvalidContract)
}

func TestReadCursorMatchesFirstTerminalAndGetPages(t *testing.T) {
	view := EntityView{Citation: Citation{ProjectID: exampleID, EntityID: exampleEntity, Kind: Issue, Revision: 1}, DisplayAlias: "ISSUE-1", ProjectSequence: 3, Payload: json.RawMessage(`{"title":"Fixture","body":"","state":"open"}`)}
	r := ReadRequest{ProjectID: exampleID, Kind: Issue, Operation: "list", Limit: 1}
	c := ReadPageCursor{Project: exampleID, Kind: Issue, Operation: "list", Snapshot: 3, Last: 1}
	result := ReadResult{ProjectID: exampleID, Entities: []EntityView{view}, NextCursor: encodePage(t, c)}
	if result.Match(r) != nil {
		t.Fatal("valid initial page rejected")
	}
	result.NextCursor = ""
	if result.Match(r) != nil {
		t.Fatal("valid terminal initial page rejected")
	}
	r.Cursor = encodePage(t, c)
	requireError(t, result.Match(r), InvalidContract) // duplicate terminal row
	result.Entities = []EntityView{}
	if result.Match(r) != nil {
		t.Fatal("valid empty terminal page rejected")
	}
	result.NextCursor = encodePage(t, c)
	requireError(t, result.Match(r), InvalidContract)
	r = ReadRequest{ProjectID: exampleID, Kind: Issue, Operation: "get", EntityID: exampleEntity}
	result.Entities, result.NextCursor = []EntityView{view}, ""
	if result.Match(r) != nil {
		t.Fatal("valid get rejected")
	}
	result.NextCursor = encodePage(t, c)
	requireError(t, result.Match(r), InvalidContract)
	result.NextCursor = ""
	result.Entities = append(result.Entities, view)
	requireError(t, result.Match(r), InvalidContract)
}

func TestReadCursorTerminalOrdinalCannotExceedProjectSequence(t *testing.T) {
	for _, operation := range []string{"list", "history", "get"} {
		for _, continuation := range []bool{false, true} {
			if operation == "get" && continuation {
				continue
			}
			r := ReadRequest{ProjectID: exampleID, Kind: Issue, Operation: operation}
			c := ReadPageCursor{Project: exampleID, Kind: Issue, Operation: operation, Snapshot: 5, Last: 1}
			if operation != "list" {
				r.EntityID, c.Entity = exampleEntity, exampleEntity
			}
			if continuation {
				r.Cursor = encodePage(t, c)
			}
			for _, view := range []EntityView{
				{Citation: Citation{ProjectID: exampleID, EntityID: exampleEntity, Kind: Issue, Revision: 99}, DisplayAlias: "ISSUE-99", ProjectSequence: 2, Payload: json.RawMessage(`{"title":"Fixture","body":"","state":"open"}`)},
				{Citation: Citation{ProjectID: exampleID, EntityID: exampleEntity, Kind: Issue, Revision: 2}, DisplayAlias: "ISSUE-99", ProjectSequence: 2, Payload: json.RawMessage(`{"title":"Fixture","body":"","state":"open"}`)},
				{Citation: Citation{ProjectID: exampleID, EntityID: exampleEntity, Kind: Issue, Revision: 99}, DisplayAlias: "ISSUE-2", ProjectSequence: 2, Payload: json.RawMessage(`{"title":"Fixture","body":"","state":"open"}`)},
			} {
				result := ReadResult{ProjectID: exampleID, Entities: []EntityView{view}}
				requireError(t, result.Match(r), InvalidContract)
			}
		}
	}
}
