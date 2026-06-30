package capability

import "testing"

func TestWriteCapabilitiesDeclareSurfaceReasons(t *testing.T) {
	for _, cap := range WriteCapabilities() {
		if cap.ID == "" || cap.ServiceCommand == "" || cap.Description == "" {
			t.Fatalf("capability has incomplete identity: %#v", cap)
		}
		if !cap.CLI.Enabled && cap.CLI.DisabledReason == "" {
			t.Fatalf("%s has CLI disabled without reason", cap.ID)
		}
		if !cap.MCP.Enabled && cap.MCP.DisabledReason == "" {
			t.Fatalf("%s has MCP disabled without reason", cap.ID)
		}
		if cap.MCP.Enabled && cap.MCPName == "" {
			t.Fatalf("%s has MCP enabled without MCPName", cap.ID)
		}
		if cap.CLI.Enabled && cap.CLIName == "" {
			t.Fatalf("%s has CLI enabled without CLIName", cap.ID)
		}
	}
}

func TestDangerousCapabilitiesAreNotMCPEnabled(t *testing.T) {
	for _, cap := range WriteCapabilities() {
		switch cap.Safety {
		case SafetyDestructiveLocalMaintenance, SafetyCredentialManagement, SafetyRawEscapeHatch:
			if cap.MCP.Enabled {
				t.Fatalf("%s is safety class %s but is MCP-enabled", cap.ID, cap.Safety)
			}
		}
	}
}

func TestCreateIssueIsSharedWriteCapability(t *testing.T) {
	cap, ok := LookupByMCPName("create_issue")
	if !ok {
		t.Fatal("create_issue capability missing")
	}
	if !cap.CLI.Enabled || !cap.MCP.Enabled {
		t.Fatalf("create_issue surfaces = CLI:%+v MCP:%+v, want both enabled", cap.CLI, cap.MCP)
	}
	if cap.CLIName != "create-issue" || cap.ServiceCommand != "create-issue" {
		t.Fatalf("create_issue names = CLI %q service %q", cap.CLIName, cap.ServiceCommand)
	}
}
