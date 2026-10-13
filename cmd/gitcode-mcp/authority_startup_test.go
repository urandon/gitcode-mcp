package main

import (
	"bytes"
	"testing"

	"gitcode-mcp/internal/servicectl"
)

// A nil source panics on any ordinary config/account/redaction access. Fixed
// native entry refuses missing roles/extra selectors without invoking it.
// This route regression supplements, not replaces, native startup observation.
func TestAuthorityStartupIsBeforeOrdinarySourceAccess(t *testing.T) {
	for _, args := range [][]string{{servicectl.AuthorityRuntimeSelector}, {servicectl.AuthorityRuntimeSelector, "--config", "synthetic-must-not-load"}} {
		var out, diagnostic bytes.Buffer
		if run(args, &bytes.Buffer{}, &out, &diagnostic, nil) == 0 || out.Len() != 0 || diagnostic.Len() != 0 {
			t.Fatal("missing native preparation reached ordinary startup or diagnostic output")
		}
	}
}
