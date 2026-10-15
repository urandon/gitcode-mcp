package mcp

import (
	"gitcode-mcp/internal/service"
	"testing"
)

func TestCachedSourcePageDiscoveryMatchesService(t *testing.T) {
	wanted := map[string]bool{"list_sources": true, "search_sources": true, "source_backlinks": true}
	for _, tool := range toolDefs {
		if !wanted[tool.Name] {
			continue
		}
		limit := tool.InputSchema.Properties["limit"]
		offset := tool.InputSchema.Properties["offset"]
		if limit.Default != float64(service.DefaultSourceQueryLimit) || limit.Minimum == nil || *limit.Minimum != 1 || limit.Maximum == nil || *limit.Maximum != float64(service.MaxSourceQueryLimit) || offset.Minimum == nil || *offset.Minimum != 0 {
			t.Fatalf("%s discovery pagination differs from service", tool.Name)
		}
		delete(wanted, tool.Name)
	}
	if len(wanted) != 0 {
		t.Fatal("cached-source paged tool missing from discovery")
	}
}
