package mcp

import "testing"

func TestSourceMetadataNullAndDiscovery(t *testing.T) {
	for _, raw := range []string{`null`, `{"labels":null}`, `{"status":null}`, `{"title_contains":null}`, `{"labels_match":null}`} {
		if validSourceMetadataJSON([]byte(raw)) {
			t.Fatalf("accepted null metadata: %s", raw)
		}
	}
	if !validSourceMetadataJSON([]byte(`{"labels":[],"title_contains":"中文"}`)) {
		t.Fatal("valid metadata rejected")
	}
	for _, name := range []string{"list_sources", "search_sources"} {
		tool := toolDefinitionByName(name)
		for _, field := range []string{"status", "labels", "labels_match", "title_contains"} {
			if tool.InputSchema.Properties[field].Type == "" {
				t.Fatalf("%s lacks %s", name, field)
			}
		}
		if name == "search_sources" && containsString(tool.InputSchema.Required, "query") {
			t.Fatal("metadata-only discovery still requires dummy query")
		}
	}
}
