package adminui

import (
	"io/fs"
	"strings"
	"testing"
)

func TestEmbeddedIndexHasStrictGeneratedCSP(t *testing.T) {
	index, err := fs.ReadFile(Files, "assets/index.html")
	if err != nil {
		t.Fatalf("read embedded index: %v", err)
	}
	html := string(index)
	for _, want := range []string{
		`http-equiv="content-security-policy"`,
		`script-src 'self' 'sha256-`,
		`src="/theme-init.js"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("embedded index is missing %q", want)
		}
	}
	if strings.Contains(html, "'unsafe-inline'") {
		t.Fatal("embedded index must not allow unsafe inline scripts or styles")
	}
}
