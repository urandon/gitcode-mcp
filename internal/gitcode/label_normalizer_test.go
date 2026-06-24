package gitcode

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestLabel001EncodeJSONString(t *testing.T) {
	result := EncodeIssueLabels([]string{"bug", "enhancement"})
	if string(result) != `["bug","enhancement"]` {
		t.Fatalf("EncodeIssueLabels: got %q, want [\"bug\",\"enhancement\"]", result)
	}
}

func TestLabel002EncodeEmpty(t *testing.T) {
	result := EncodeIssueLabels([]string{})
	if string(result) != "[]" {
		t.Fatalf("EncodeIssueLabels empty: got %q, want []", result)
	}
}

func TestLabel003EncodeTrimDrops(t *testing.T) {
	result := EncodeIssueLabels([]string{" bug ", "", "enhancement"})
	if string(result) != `["bug","enhancement"]` {
		t.Fatalf("EncodeIssueLabels trim/drop: got %q, want [\"bug\",\"enhancement\"]", result)
	}
}

func TestLabel004NormalizeValid(t *testing.T) {
	result, err := NormalizeLabels([]GitCodeLabel{
		{ID: 1, Name: "bug", Color: "#FF0000"},
		{ID: 2, Name: "enhancement"},
	})
	if err != nil {
		t.Fatalf("NormalizeLabels returned error: %v", err)
	}
	if len(result) != 2 || result[0] != "bug" || result[1] != "enhancement" {
		t.Fatalf("NormalizeLabels: got %v, want [bug enhancement]", result)
	}
}

func TestLabel005NormalizeEmptyInput(t *testing.T) {
	result, err := NormalizeLabels([]GitCodeLabel{})
	if err != nil {
		t.Fatalf("NormalizeLabels empty: unexpected error: %v", err)
	}
	if result == nil || len(result) != 0 {
		t.Fatalf("NormalizeLabels empty: got %v, want empty non-nil slice", result)
	}
}

func TestLabel006NormalizeMissingID(t *testing.T) {
	_, err := NormalizeLabels([]GitCodeLabel{
		{ID: 0, Name: "bug"},
	})
	if err == nil {
		t.Fatal("NormalizeLabels missing id: expected error, got nil")
	}
	var schemaErr *ErrSchemaDecode
	if !errors.As(err, &schemaErr) {
		t.Fatalf("NormalizeLabels missing id: expected *ErrSchemaDecode, got %T: %v", err, err)
	}
	if schemaErr.Field != "labels[0].id" {
		t.Fatalf("NormalizeLabels missing id: Field=%q, want labels[0].id", schemaErr.Field)
	}
	if schemaErr.DiagnosticCode() != "schema_decode" {
		t.Fatalf("NormalizeLabels missing id: DiagnosticCode=%q, want schema_decode", schemaErr.DiagnosticCode())
	}
}

func TestLabel007NormalizeMissingName(t *testing.T) {
	_, err := NormalizeLabels([]GitCodeLabel{
		{ID: 1, Name: ""},
	})
	if err == nil {
		t.Fatal("NormalizeLabels missing name: expected error, got nil")
	}
	var schemaErr *ErrSchemaDecode
	if !errors.As(err, &schemaErr) {
		t.Fatalf("NormalizeLabels missing name: expected *ErrSchemaDecode, got %T: %v", err, err)
	}
	if schemaErr.Field != "labels[0].name" {
		t.Fatalf("NormalizeLabels missing name: Field=%q, want labels[0].name", schemaErr.Field)
	}
}

func TestLabel008NormalizeSingle(t *testing.T) {
	result, err := NormalizeSingleLabel(GitCodeLabel{ID: 1, Name: "bug"})
	if err != nil {
		t.Fatalf("NormalizeSingleLabel: unexpected error: %v", err)
	}
	if result != "bug" {
		t.Fatalf("NormalizeSingleLabel: got %q, want bug", result)
	}
}

func TestLabel009NormalizeSingleInvalid(t *testing.T) {
	_, err := NormalizeSingleLabel(GitCodeLabel{ID: 0, Name: ""})
	if err == nil {
		t.Fatal("NormalizeSingleLabel invalid: expected error, got nil")
	}
	var schemaErr *ErrSchemaDecode
	if !errors.As(err, &schemaErr) {
		t.Fatalf("NormalizeSingleLabel invalid: expected *ErrSchemaDecode, got %T: %v", err, err)
	}
	if schemaErr.DiagnosticCode() != "schema_decode" {
		t.Fatalf("NormalizeSingleLabel invalid: DiagnosticCode=%q, want schema_decode", schemaErr.DiagnosticCode())
	}
}

func TestLabel014SchemaDecodeDistinctFromTransport(t *testing.T) {
	_, err := NormalizeLabels([]GitCodeLabel{
		{ID: 0, Name: "bug"},
	})
	var schemaErr *ErrSchemaDecode
	if !errors.As(err, &schemaErr) {
		t.Fatalf("expected *ErrSchemaDecode, got %T: %v", err, err)
	}
	var netErr *ErrNetworkUnavailable
	if errors.As(err, &netErr) {
		t.Fatalf("schema decode error incorrectly matches ErrNetworkUnavailable")
	}
}

func TestLabel010IssueResponseNormalized(t *testing.T) {
	body := `{"id":"1","number":1,"title":"Test","labels":[{"id":1,"name":"bug","color":"#FF0000"},{"id":2,"name":"enhancement","color":"#00FF00"}]}`
	var summary IssueSummary
	if err := json.Unmarshal([]byte(body), &summary); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if summary.GitCodeLabels == nil || len(summary.GitCodeLabels) != 2 {
		t.Fatalf("GitCodeLabels: got %v, want 2 items", summary.GitCodeLabels)
	}
	if len(summary.Labels) != 2 || summary.Labels[0] != "bug" || summary.Labels[1] != "enhancement" {
		t.Fatalf("Labels: got %v, want [bug enhancement]", summary.Labels)
	}

	var issue Issue
	if err := json.Unmarshal([]byte(body), &issue); err != nil {
		t.Fatalf("unmarshal issue: %v", err)
	}
	if len(issue.Labels) != 2 || issue.Labels[0] != "bug" || issue.Labels[1] != "enhancement" {
		t.Fatalf("Issue Labels: got %v, want [bug enhancement]", issue.Labels)
	}
}

func TestLabel010FixtureStringsStillWork(t *testing.T) {
	body := `{"id":"ISSUE-42","number":42,"title":"Test","labels":["adapter","offline"]}`
	var summary IssueSummary
	if err := json.Unmarshal([]byte(body), &summary); err != nil {
		t.Fatalf("unmarshal fixture labels: %v", err)
	}
	if len(summary.Labels) != 2 || summary.Labels[0] != "adapter" || summary.Labels[1] != "offline" {
		t.Fatalf("Fixture Labels: got %v, want [adapter offline]", summary.Labels)
	}
	if summary.GitCodeLabels != nil {
		t.Fatalf("Fixture GitCodeLabels: expected nil, got %v", summary.GitCodeLabels)
	}

	var issue Issue
	if err := json.Unmarshal([]byte(body), &issue); err != nil {
		t.Fatalf("unmarshal fixture issue labels: %v", err)
	}
	if len(issue.Labels) != 2 || issue.Labels[0] != "adapter" || issue.Labels[1] != "offline" {
		t.Fatalf("Fixture Issue Labels: got %v, want [adapter offline]", issue.Labels)
	}
}

func TestLabel015ObjectLabelWithMissingIDReturnsSchemaDecode(t *testing.T) {
	body := `{"id":"1","number":1,"title":"Test","labels":[{"id":0,"name":"bug"}]}`
	var summary IssueSummary
	err := json.Unmarshal([]byte(body), &summary)
	if err == nil {
		t.Fatal("expected error for missing label id, got nil")
	}
	var schemaErr *ErrSchemaDecode
	if !errors.As(err, &schemaErr) {
		t.Fatalf("expected *ErrSchemaDecode, got %T: %v", err, err)
	}
}
