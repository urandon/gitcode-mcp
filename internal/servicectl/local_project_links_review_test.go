package servicectl

import (
	"strings"
	"testing"

	"gitcode-mcp/internal/localgit"
)

func TestLocalProjectLinksArrayBoundary(t *testing.T) {
	for _, raw := range []string{`[]`, `[{"kind":"wiki","id":"DOC-1"}]`, `[` + strings.TrimSuffix(strings.Repeat(`{"kind":"wiki","id":"DOC-1"},`, 64), ",") + `]`} {
		links, err := DecodeLocalProjectLinks([]byte(raw))
		if err != nil || links == nil {
			t.Fatal("valid bounded array rejected")
		}
		var object struct{}
		if DecodeLocalProjectJSON([]byte(raw), &object) == nil {
			t.Fatal("array fix weakened object-only request decoder")
		}
	}
	for _, raw := range []string{``, `null`, `{}`, `[null]`, `[{}]`, `[{"kind":"wiki"}]`, `[{"kind":"wiki","id":null}]`, `[{"Kind":"wiki","id":"DOC-1"}]`, `[{"kind":"wiki","kind":"wiki","id":"DOC-1"}]`, `[{"kind":"wiki","id":"DOC-1","extra":1}]`, `[] {}`, `[` + strings.TrimSuffix(strings.Repeat(`{"kind":"wiki","id":"DOC-1"},`, 65), ",") + `]`, strings.Repeat(" ", maxRPCRequestBytes+1), string([]byte{0xff})} {
		if _, err := DecodeLocalProjectLinks([]byte(raw)); err == nil {
			t.Fatal("invalid array accepted")
		}
	}
	var ref localgit.Reference
	if DecodeLocalProjectJSON([]byte(`{"kind":"wiki","id":"DOC-1"}`), &ref) != nil || ref.ID != "DOC-1" {
		t.Fatal("valid object boundary regressed")
	}
}
