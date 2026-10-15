package servicectl

import (
	"gitcode-mcp/internal/capability"
	"testing"
)

func TestAdminCapabilityCatalogPreservesSurfaceDistinctions(t *testing.T) {
	for _, cap := range capability.Capabilities() {
		view := adminCapabilityObservation(cap)
		if view.ID != cap.ID || view.CLIEnabled != cap.CLI.Enabled || view.MCPEnabled != cap.MCP.Enabled || view.UIEnabled != cap.UI.Enabled {
			t.Fatalf("capability projection drift: %s", cap.ID)
		}
		if !view.UIEnabled && view.UIReason == "" {
			t.Fatalf("missing browser exclusion: %s", cap.ID)
		}
		if cap.ID == "admin_provider_smoke" && (!view.UIEnabled || view.CLIEnabled || view.MCPEnabled) {
			t.Fatal("model status was incorrectly advertised as embedding smoke")
		}
	}
}
