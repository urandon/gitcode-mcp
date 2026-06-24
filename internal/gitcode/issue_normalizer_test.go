package gitcode

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestIssueIdentity001NumericIDFloat64(t *testing.T) {
	body := `{"id":42,"number":42,"title":"numeric id"}`
	var summary IssueSummary
	if err := json.Unmarshal([]byte(body), &summary); err != nil {
		t.Fatalf("unmarshal numeric id: %v", err)
	}
	if summary.ID != "42" {
		t.Fatalf("ID: got %q, want \"42\"", summary.ID)
	}
	if summary.Number != 42 {
		t.Fatalf("Number: got %d, want 42", summary.Number)
	}
}

func TestIssueIdentity002StringID(t *testing.T) {
	body := `{"id":"ISSUE-99","number":99,"title":"string id"}`
	var summary IssueSummary
	if err := json.Unmarshal([]byte(body), &summary); err != nil {
		t.Fatalf("unmarshal string id: %v", err)
	}
	if summary.ID != "ISSUE-99" {
		t.Fatalf("ID: got %q, want \"ISSUE-99\"", summary.ID)
	}
	if summary.Number != 99 {
		t.Fatalf("Number: got %d, want 99", summary.Number)
	}
}

func TestIssueIdentity003StringNumber(t *testing.T) {
	body := `{"id":"1","number":"7","title":"string number"}`
	var summary IssueSummary
	if err := json.Unmarshal([]byte(body), &summary); err != nil {
		t.Fatalf("unmarshal string number: %v", err)
	}
	if summary.Number != 7 {
		t.Fatalf("Number: got %d, want 7", summary.Number)
	}
}

func TestIssueIdentity004MissingID(t *testing.T) {
	body := `{"number":1,"title":"missing id"}`
	var summary IssueSummary
	err := json.Unmarshal([]byte(body), &summary)
	if err == nil {
		t.Fatal("expected error for missing id, got nil")
	}
	var schemaErr *ErrSchemaDecode
	if !errors.As(err, &schemaErr) {
		t.Fatalf("expected *ErrSchemaDecode, got %T: %v", err, err)
	}
	if schemaErr.Field != "id" {
		t.Fatalf("Field: got %q, want \"id\"", schemaErr.Field)
	}
	if schemaErr.DiagnosticCode() != "schema_decode" {
		t.Fatalf("DiagnosticCode: got %q, want schema_decode", schemaErr.DiagnosticCode())
	}
}

func TestIssueIdentity005IDZero(t *testing.T) {
	body := `{"id":0,"number":1,"title":"id zero"}`
	var summary IssueSummary
	err := json.Unmarshal([]byte(body), &summary)
	if err == nil {
		t.Fatal("expected error for id=0, got nil")
	}
	var schemaErr *ErrSchemaDecode
	if !errors.As(err, &schemaErr) {
		t.Fatalf("expected *ErrSchemaDecode, got %T: %v", err, err)
	}
	if schemaErr.Field != "id" {
		t.Fatalf("Field: got %q, want \"id\"", schemaErr.Field)
	}
}

func TestIssueIdentity006MissingNumber(t *testing.T) {
	body := `{"id":"42","title":"missing number"}`
	var summary IssueSummary
	err := json.Unmarshal([]byte(body), &summary)
	if err == nil {
		t.Fatal("expected error for missing number, got nil")
	}
	var schemaErr *ErrSchemaDecode
	if !errors.As(err, &schemaErr) {
		t.Fatalf("expected *ErrSchemaDecode, got %T: %v", err, err)
	}
	if schemaErr.Field != "number" {
		t.Fatalf("Field: got %q, want \"number\"", schemaErr.Field)
	}
}

func TestIssueIdentity007NumberZero(t *testing.T) {
	body := `{"id":"7","number":0,"title":"number zero"}`
	var summary IssueSummary
	if err := json.Unmarshal([]byte(body), &summary); err != nil {
		t.Fatalf("unmarshal number=0: %v", err)
	}
	if summary.ID != "7" {
		t.Fatalf("ID: got %q, want \"7\"", summary.ID)
	}
	if summary.Number != 0 {
		t.Fatalf("Number: got %d, want 0", summary.Number)
	}
}

func TestIssueIdentity008NumberZeroString(t *testing.T) {
	body := `{"id":"7","number":"0","title":"number zero string"}`
	var summary IssueSummary
	if err := json.Unmarshal([]byte(body), &summary); err != nil {
		t.Fatalf("unmarshal number=\"0\": %v", err)
	}
	if summary.Number != 0 {
		t.Fatalf("Number: got %d, want 0", summary.Number)
	}
}

func TestIssueIdentity009RoundTripNumericIDStringNumber(t *testing.T) {
	body := `{"id":7,"number":"7","title":"round trip"}`
	var summary IssueSummary
	if err := json.Unmarshal([]byte(body), &summary); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if summary.ID != "7" {
		t.Fatalf("ID: got %q, want \"7\"", summary.ID)
	}
	if summary.Number != 7 {
		t.Fatalf("Number: got %d, want 7", summary.Number)
	}
	if summary.Title != "round trip" {
		t.Fatalf("Title: got %q, want \"round trip\"", summary.Title)
	}

	var issue Issue
	if err := json.Unmarshal([]byte(body), &issue); err != nil {
		t.Fatalf("unmarshal Issue: %v", err)
	}
	if issue.ID != "7" || issue.Number != 7 || issue.Title != "round trip" {
		t.Fatalf("Issue: id=%q number=%d title=%q", issue.ID, issue.Number, issue.Title)
	}
}

func TestIssueIdentity010MalformedPayloadSchemaDecodeDistinct(t *testing.T) {
	body := `{"number":"abc","title":"bad number"}`
	var summary IssueSummary
	err := json.Unmarshal([]byte(body), &summary)
	if err == nil {
		t.Fatal("expected error for malformed payload, got nil")
	}
	var schemaErr *ErrSchemaDecode
	if !errors.As(err, &schemaErr) {
		t.Fatalf("expected *ErrSchemaDecode, got %T: %v", err, err)
	}
	var netErr *ErrNetworkUnavailable
	if errors.As(err, &netErr) {
		t.Fatalf("schema decode error incorrectly matches ErrNetworkUnavailable")
	}
}

func TestIssueIdentity011SchemaDecodeNotTransport(t *testing.T) {
	body := `{"id":"","number":1,"title":"empty id string"}`
	var summary IssueSummary
	err := json.Unmarshal([]byte(body), &summary)
	if err == nil {
		t.Fatal("expected error for empty id string, got nil")
	}
	var schemaErr *ErrSchemaDecode
	if !errors.As(err, &schemaErr) {
		t.Fatalf("expected *ErrSchemaDecode, got %T: %v", err, err)
	}
	var netErr *ErrNetworkUnavailable
	if errors.As(err, &netErr) {
		t.Fatalf("schema decode error incorrectly matches ErrNetworkUnavailable")
	}
}

func TestIssueIdentity012IDZeroStringInvalid(t *testing.T) {
	body := `{"id":"0","number":1,"title":"zero string id"}`
	var summary IssueSummary
	err := json.Unmarshal([]byte(body), &summary)
	if err == nil {
		t.Fatal("expected error for id=\"0\", got nil")
	}
	var schemaErr *ErrSchemaDecode
	if !errors.As(err, &schemaErr) {
		t.Fatalf("expected *ErrSchemaDecode, got %T: %v", err, err)
	}
	if schemaErr.Field != "id" {
		t.Fatalf("Field: got %q, want \"id\"", schemaErr.Field)
	}
}

func TestIssueIdentity013ExistingFixtureStringIDsStillWork(t *testing.T) {
	body := `{"id":"ISSUE-41","number":41,"title":"existing fixture"}`
	var summary IssueSummary
	if err := json.Unmarshal([]byte(body), &summary); err != nil {
		t.Fatalf("unmarshal ISSUE-41: %v", err)
	}
	if summary.ID != "ISSUE-41" {
		t.Fatalf("ID: got %q, want \"ISSUE-41\"", summary.ID)
	}

	body2 := `{"id":"ISSUE-42","number":42,"title":"another fixture"}`
	if err := json.Unmarshal([]byte(body2), &summary); err != nil {
		t.Fatalf("unmarshal ISSUE-42: %v", err)
	}
	if summary.ID != "ISSUE-42" {
		t.Fatalf("ID: got %q, want \"ISSUE-42\"", summary.ID)
	}
}

func TestIssueIdentity014NilID(t *testing.T) {
	body := `{"id":null,"number":1,"title":"null id"}`
	var summary IssueSummary
	err := json.Unmarshal([]byte(body), &summary)
	if err == nil {
		t.Fatal("expected error for null id, got nil")
	}
	var schemaErr *ErrSchemaDecode
	if !errors.As(err, &schemaErr) {
		t.Fatalf("expected *ErrSchemaDecode, got %T: %v", err, err)
	}
	if schemaErr.Field != "id" {
		t.Fatalf("Field: got %q, want \"id\"", schemaErr.Field)
	}
}

func TestIssueIdentity015BoolIDRejected(t *testing.T) {
	body := `{"id":true,"number":1,"title":"bool id"}`
	var summary IssueSummary
	err := json.Unmarshal([]byte(body), &summary)
	if err == nil {
		t.Fatal("expected error for bool id, got nil")
	}
	var schemaErr *ErrSchemaDecode
	if !errors.As(err, &schemaErr) {
		t.Fatalf("expected *ErrSchemaDecode, got %T: %v", err, err)
	}
	if schemaErr.Field != "id" {
		t.Fatalf("Field: got %q, want \"id\"", schemaErr.Field)
	}
}

func TestIssueIdentity016LabelsStillWorkWithNumericID(t *testing.T) {
	body := `{"id":7,"number":"7","title":"Test","labels":[{"id":1,"name":"bug","color":"#FF0000"},{"id":2,"name":"enhancement","color":"#00FF00"}]}`
	var summary IssueSummary
	if err := json.Unmarshal([]byte(body), &summary); err != nil {
		t.Fatalf("unmarshal with labels: %v", err)
	}
	if summary.ID != "7" {
		t.Fatalf("ID: got %q, want \"7\"", summary.ID)
	}
	if len(summary.Labels) != 2 || summary.Labels[0] != "bug" || summary.Labels[1] != "enhancement" {
		t.Fatalf("Labels: got %v, want [bug enhancement]", summary.Labels)
	}
}
