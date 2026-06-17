package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestHelpReturnsSuccess(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Execute(nil, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("Execute(nil) code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "gitcode-mcp") {
		t.Fatalf("help output did not include program name: %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestStubCommandReturnsNotImplemented(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Execute([]string{"search", "DOC-123"}, &stdout, &stderr)

	if code != 2 {
		t.Fatalf("Execute(search) code = %d, want 2", code)
	}
	if !strings.Contains(stdout.String(), "not implemented yet") {
		t.Fatalf("stub output did not explain status: %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}
