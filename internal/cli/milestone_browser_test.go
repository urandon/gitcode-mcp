package cli

import (
	"bytes"
	"encoding/json"
	"gitcode-mcp/internal/service"
	"strings"
	"testing"
)

func TestMilestoneCanonicalBrowserLocatorOutput(t *testing.T) {
	const locator = "https://example.invalid/owner/repo/milestones/123456?iid=7"
	result := service.MilestoneListResult{Milestones: []service.MilestoneRecord{{ID: "MILESTONE-123456", RemoteID: "123456", IID: "7", BrowserURL: locator}}}
	var out bytes.Buffer
	if render(&out, "json", result, renderMilestonesText) != 0 {
		t.Fatal("JSON render failed")
	}
	var decoded service.MilestoneListResult
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil || decoded.Milestones[0].IID != "7" || decoded.Milestones[0].BrowserURL != locator {
		t.Fatalf("JSON locator missing: %s %v", out.String(), err)
	}
	out.Reset()
	renderMilestonesText(&out, result)
	if !strings.Contains(out.String(), "iid=7") || !strings.Contains(out.String(), "browser_url="+locator) {
		t.Fatal("text locator missing")
	}
}
