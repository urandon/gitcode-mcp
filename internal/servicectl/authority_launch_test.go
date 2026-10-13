package servicectl

import (
	"context"
	"testing"

	"gitcode-mcp/internal/authority"
)

func TestAuthorityNativeCleanupFailureNeverReturnsCleanStatus(t *testing.T) {
	for _, err := range []error{context.DeadlineExceeded, context.Canceled, authority.Error{Code: authority.Unavailable}} {
		if authorityJoinStatus(0, err) != 98 {
			t.Fatal("failed or unknown native cleanup reported clean success")
		}
		if authorityJoinStatus(83, err) != 83 {
			t.Fatal("cleanup replaced the original startup refusal")
		}
	}
	if authorityJoinStatus(0, nil) != 0 {
		t.Fatal("confirmed clean native join refused")
	}
}
