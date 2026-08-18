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
	for _, cap := range Capabilities() {
		switch cap.Safety {
		case SafetyDestructiveLocalMaintenance, SafetyCredentialManagement, SafetyRawEscapeHatch:
			if cap.MCP.Enabled {
				t.Fatalf("%s is safety class %s but is MCP-enabled", cap.ID, cap.Safety)
			}
		}
	}
}

func TestRAGCapabilitiesDeclareSafeSurfacePolicy(t *testing.T) {
	caps := RAGCapabilities()
	if len(caps) == 0 {
		t.Fatal("RAG capabilities missing")
	}
	byID := map[string]Capability{}
	for _, cap := range caps {
		byID[cap.ID] = cap
		if cap.ID == "" || cap.ServiceCommand == "" || cap.Description == "" {
			t.Fatalf("RAG capability has incomplete identity: %#v", cap)
		}
		if !cap.CLI.Enabled && cap.CLI.DisabledReason == "" {
			t.Fatalf("%s is CLI-disabled without a reason", cap.ID)
		}
		if !cap.MCP.Enabled && cap.MCP.DisabledReason == "" {
			t.Fatalf("%s is MCP-disabled without a reason", cap.ID)
		}
	}
	for _, id := range []string{"rag_status", "rag_search"} {
		cap, ok := byID[id]
		if !ok {
			t.Fatalf("%s capability missing", id)
		}
		if cap.Safety != SafetyReadOnly || !cap.CLI.Enabled || !cap.MCP.Enabled {
			t.Fatalf("%s policy = safety:%s CLI:%+v MCP:%+v, want safe on both surfaces", id, cap.Safety, cap.CLI, cap.MCP)
		}
	}
	if cap := byID["rag_index"]; cap.Safety != SafetyBackgroundJob || !cap.CLI.Enabled || cap.MCP.Enabled {
		t.Fatalf("rag_index policy = safety:%s CLI:%+v MCP:%+v, want CLI-only background job", cap.Safety, cap.CLI, cap.MCP)
	}
	for _, id := range []string{"rag_purge_embeddings", "rag_delete_namespace", "rag_rebuild_all_namespaces", "rag_reset_derived_state"} {
		cap, ok := byID[id]
		if !ok {
			t.Fatalf("%s capability missing", id)
		}
		if cap.Safety != SafetyDestructiveLocalMaintenance || cap.MCP.Enabled {
			t.Fatalf("%s policy = safety:%s MCP:%+v, want destructive local maintenance disabled in MCP", id, cap.Safety, cap.MCP)
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

func TestFeedbackCapabilitiesSeparatePreparationFromSubmission(t *testing.T) {
	prepare, ok := LookupByMCPName("prepare_feedback")
	if !ok || prepare.Safety != SafetyReadOnly || !prepare.MCP.Enabled || !prepare.CLI.Enabled {
		t.Fatalf("prepare_feedback=%#v ok=%t", prepare, ok)
	}
	submit, ok := LookupByMCPName("submit_feedback")
	if !ok || submit.Safety != SafetyAuditedWrite || !submit.MCP.Enabled || !submit.CLI.Enabled {
		t.Fatalf("submit_feedback=%#v ok=%t", submit, ok)
	}
}
