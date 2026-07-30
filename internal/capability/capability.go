package capability

type Category string

const (
	CategoryWrite Category = "write"
	CategoryRAG   Category = "rag"
)

type SafetyClass string

const (
	SafetyReadOnly                    SafetyClass = "read_only"
	SafetyBackgroundJob               SafetyClass = "background_job"
	SafetyAuditedWrite                SafetyClass = "audited_write"
	SafetyDestructiveRemoteWrite      SafetyClass = "destructive_remote_write"
	SafetyDestructiveLocalMaintenance SafetyClass = "destructive_local_maintenance"
	SafetyCredentialManagement        SafetyClass = "credential_management"
	SafetyReleaseAutomationWrite      SafetyClass = "release_automation_write"
	SafetyRawEscapeHatch              SafetyClass = "raw_escape_hatch"
)

type Surface struct {
	Enabled        bool
	EnabledReason  string
	DisabledReason string
}

type Capability struct {
	ID             string
	Category       Category
	Safety         SafetyClass
	CLIName        string
	CLIAliases     []string
	MCPName        string
	ServiceCommand string
	Description    string
	CLI            Surface
	MCP            Surface
}

func enabled(reason ...string) Surface {
	surface := Surface{Enabled: true}
	if len(reason) > 0 {
		surface.EnabledReason = reason[0]
	}
	return surface
}

func disabled(reason string) Surface {
	return Surface{Enabled: false, DisabledReason: reason}
}

var writeCapabilities = []Capability{
	{
		ID:             "create_issue",
		Category:       CategoryWrite,
		Safety:         SafetyAuditedWrite,
		CLIName:        "create-issue",
		MCPName:        "create_issue",
		ServiceCommand: "create-issue",
		Description:    "Create a live issue through the audited write lifecycle.",
		CLI:            enabled(),
		MCP:            enabled(),
	},
	{
		ID:             "update_issue",
		Category:       CategoryWrite,
		Safety:         SafetyAuditedWrite,
		CLIName:        "update-issue",
		MCPName:        "update_issue",
		ServiceCommand: "update-issue",
		Description:    "Update live issue metadata through the audited write lifecycle.",
		CLI:            enabled(),
		MCP:            enabled(),
	},
	{
		ID:             "add_issue_comment",
		Category:       CategoryWrite,
		Safety:         SafetyAuditedWrite,
		CLIName:        "add-comment",
		MCPName:        "add_issue_comment",
		ServiceCommand: "add-comment",
		Description:    "Add a live comment to an issue through the audited write lifecycle.",
		CLI:            enabled(),
		MCP:            enabled(),
	},
	{
		ID:             "update_issue_comment",
		Category:       CategoryWrite,
		Safety:         SafetyAuditedWrite,
		CLIName:        "update-comment",
		MCPName:        "update_issue_comment",
		ServiceCommand: "update-comment",
		Description:    "Update a live issue comment through the audited write lifecycle.",
		CLI:            enabled(),
		MCP:            enabled(),
	},
	{
		ID:             "create_pr",
		Category:       CategoryWrite,
		Safety:         SafetyAuditedWrite,
		CLIName:        "create-pr",
		CLIAliases:     []string{"create-mr"},
		MCPName:        "create_pr",
		ServiceCommand: "create-pr",
		Description:    "Create a live pull request through the audited write lifecycle.",
		CLI:            enabled(),
		MCP:            enabled(),
	},
	{
		ID:             "update_pr",
		Category:       CategoryWrite,
		Safety:         SafetyAuditedWrite,
		CLIName:        "update-pr",
		MCPName:        "update_pr",
		ServiceCommand: "update-pr",
		Description:    "Update live pull request metadata through the audited write lifecycle.",
		CLI:            enabled(),
		MCP:            enabled(),
	},
	{
		ID:             "list_milestones",
		Category:       CategoryWrite,
		Safety:         SafetyReadOnly,
		CLIName:        "milestones",
		MCPName:        "list_milestones",
		ServiceCommand: "milestones",
		Description:    "List live repository milestones and refresh cached milestone records.",
		CLI:            enabled(),
		MCP:            enabled(),
	},
	{
		ID:             "list_push_remote_mirrors",
		Category:       CategoryWrite,
		Safety:         SafetyReadOnly,
		CLIName:        "push-mirrors",
		MCPName:        "list_push_remote_mirrors",
		ServiceCommand: "push-mirrors",
		Description:    "List live repository push mirrors and refresh sanitized cached records.",
		CLI:            enabled(),
		MCP:            enabled(),
	},
	{
		ID:             "create_milestone",
		Category:       CategoryWrite,
		Safety:         SafetyAuditedWrite,
		CLIName:        "create-milestone",
		MCPName:        "create_milestone",
		ServiceCommand: "create-milestone",
		Description:    "Create a live milestone through the audited write lifecycle.",
		CLI:            enabled(),
		MCP:            enabled(),
	},
	{
		ID:             "update_milestone",
		Category:       CategoryWrite,
		Safety:         SafetyAuditedWrite,
		CLIName:        "update-milestone",
		MCPName:        "update_milestone",
		ServiceCommand: "update-milestone",
		Description:    "Update live milestone metadata through the audited write lifecycle.",
		CLI:            enabled(),
		MCP:            enabled(),
	},
	{
		ID:             "set_issue_milestone",
		Category:       CategoryWrite,
		Safety:         SafetyAuditedWrite,
		CLIName:        "set-issue-milestone",
		MCPName:        "set_issue_milestone",
		ServiceCommand: "set-issue-milestone",
		Description:    "Assign a live issue milestone through the audited write lifecycle.",
		CLI:            enabled(),
		MCP:            enabled(),
	},
	{
		ID:             "clear_issue_milestone",
		Category:       CategoryWrite,
		Safety:         SafetyAuditedWrite,
		CLIName:        "clear-issue-milestone",
		MCPName:        "clear_issue_milestone",
		ServiceCommand: "clear-issue-milestone",
		Description:    "Clear a live issue milestone through the audited write lifecycle.",
		CLI:            enabled(),
		MCP:            enabled(),
	},
	{
		ID:             "add_pr_comment",
		Category:       CategoryWrite,
		Safety:         SafetyAuditedWrite,
		MCPName:        "add_pr_comment",
		ServiceCommand: "add-pr-comment",
		Description:    "Add a live pull request comment through the audited write lifecycle.",
		CLI:            disabled("CLI add-pr-comment command is not implemented yet; tracked by CLI/MCP parity issue #32."),
		MCP:            enabled(),
	},
	{
		ID:             "add_pr_review_comment",
		Category:       CategoryWrite,
		Safety:         SafetyAuditedWrite,
		CLIName:        "add-pr-review-comment",
		MCPName:        "add_pr_review_comment",
		ServiceCommand: "add-pr-review-comment",
		Description:    "Create a live inline pull request review comment through the audited write lifecycle.",
		CLI:            enabled(),
		MCP:            enabled(),
	},
	{
		ID:             "reply_pr_review_comment",
		Category:       CategoryWrite,
		Safety:         SafetyAuditedWrite,
		CLIName:        "reply-pr-review-comment",
		MCPName:        "reply_pr_review_comment",
		ServiceCommand: "reply-pr-review-comment",
		Description:    "Reply inside a live pull request review discussion with readback through the audited write lifecycle.",
		CLI:            enabled(),
		MCP:            enabled(),
	},
	{
		ID:             "link_pr_issue",
		Category:       CategoryWrite,
		Safety:         SafetyAuditedWrite,
		MCPName:        "link_pr_issue",
		ServiceCommand: "link-pr-issue",
		Description:    "Link a live pull request to an issue through the GitCode relation API, with deterministic description fallback when unsupported.",
		CLI:            disabled("CLI link-pr-issue command is not implemented yet; tracked by CLI/MCP parity issue #32."),
		MCP:            enabled(),
	},
	{
		ID:             "create_page",
		Category:       CategoryWrite,
		Safety:         SafetyAuditedWrite,
		CLIName:        "create-page",
		MCPName:        "create_page",
		ServiceCommand: "create-page",
		Description:    "Create a live wiki page through the audited write lifecycle.",
		CLI:            enabled(),
		MCP:            enabled(),
	},
	{
		ID:             "update_page",
		Category:       CategoryWrite,
		Safety:         SafetyAuditedWrite,
		CLIName:        "update-page",
		MCPName:        "update_page",
		ServiceCommand: "update-page",
		Description:    "Update a live wiki page through the audited write lifecycle.",
		CLI:            enabled(),
		MCP:            enabled(),
	},
	{
		ID:             "delete_page",
		Category:       CategoryWrite,
		Safety:         SafetyDestructiveRemoteWrite,
		CLIName:        "delete-page",
		MCPName:        "delete_page",
		ServiceCommand: "delete-page",
		Description:    "Delete a live wiki page.",
		CLI:            enabled(),
		MCP:            enabled("Destructive remote delete exposed only through audited MCP write access and explicit write_mode live."),
	},
	{
		ID:             "add_label",
		Category:       CategoryWrite,
		Safety:         SafetyAuditedWrite,
		CLIName:        "add-label",
		MCPName:        "add_label",
		ServiceCommand: "add-label",
		Description:    "Add a label to a live issue.",
		CLI:            enabled(),
		MCP:            enabled(),
	},
	{
		ID:             "publish_release",
		Category:       CategoryWrite,
		Safety:         SafetyReleaseAutomationWrite,
		CLIName:        "publish-release",
		ServiceCommand: "publish-release",
		Description:    "Create or update a GitCode release from maintainer release automation.",
		CLI:            enabled(),
		MCP:            disabled("Release publishing is CI/maintainer automation; expose through MCP only after explicit product decision and audit semantics."),
	},
}

var ragCapabilities = []Capability{
	{
		ID:             "rag_status",
		Category:       CategoryRAG,
		Safety:         SafetyReadOnly,
		CLIName:        "rag-status",
		MCPName:        "rag_status",
		ServiceCommand: "rag-status",
		Description:    "Report RAG provider readiness, namespace coverage, last index run, and active daemon job state.",
		CLI:            enabled(),
		MCP:            enabled(),
	},
	{
		ID:             "rag_search",
		Category:       CategoryRAG,
		Safety:         SafetyReadOnly,
		CLIName:        "rag-search",
		MCPName:        "rag_search",
		ServiceCommand: "rag-search",
		Description:    "Run semantic/hybrid RAG retrieval over cached chunks with citations, provenance, and transparent score breakdowns.",
		CLI:            enabled(),
		MCP:            enabled(),
	},
	{
		ID:             "rag_index",
		Category:       CategoryRAG,
		Safety:         SafetyBackgroundJob,
		CLIName:        "rag",
		MCPName:        "rag_index",
		ServiceCommand: "rag-index",
		Description:    "Start a daemon-owned RAG index job.",
		CLI:            enabled("Available as the grouped CLI command `rag index`."),
		MCP:            disabled("MCP-triggered RAG indexing needs an explicit job policy; keep it CLI-only until the global lease/cancel semantics are designed for MCP."),
	},
	{
		ID:             "rag_purge_embeddings",
		Category:       CategoryRAG,
		Safety:         SafetyDestructiveLocalMaintenance,
		CLIName:        "rag",
		MCPName:        "rag_purge_embeddings",
		ServiceCommand: "rag-purge-embeddings",
		Description:    "Purge cached RAG embeddings.",
		CLI:            disabled("Not implemented; destructive cache maintenance requires an explicit product design."),
		MCP:            disabled("Destructive cache-invalidating RAG maintenance is not exposed through MCP by default."),
	},
	{
		ID:             "rag_delete_namespace",
		Category:       CategoryRAG,
		Safety:         SafetyDestructiveLocalMaintenance,
		CLIName:        "rag",
		MCPName:        "rag_delete_namespace",
		ServiceCommand: "rag-delete-namespace",
		Description:    "Delete a RAG embedding namespace.",
		CLI:            disabled("Not implemented; destructive cache maintenance requires an explicit product design."),
		MCP:            disabled("Destructive cache-invalidating RAG maintenance is not exposed through MCP by default."),
	},
	{
		ID:             "rag_rebuild_all_namespaces",
		Category:       CategoryRAG,
		Safety:         SafetyDestructiveLocalMaintenance,
		CLIName:        "rag",
		MCPName:        "rag_rebuild_all_namespaces",
		ServiceCommand: "rag-rebuild-all-namespaces",
		Description:    "Rebuild every RAG embedding namespace.",
		CLI:            disabled("Not implemented; destructive cache maintenance requires an explicit product design."),
		MCP:            disabled("Destructive cache-invalidating RAG maintenance is not exposed through MCP by default."),
	},
	{
		ID:             "rag_reset_derived_state",
		Category:       CategoryRAG,
		Safety:         SafetyDestructiveLocalMaintenance,
		CLIName:        "rag",
		MCPName:        "rag_reset_derived_state",
		ServiceCommand: "rag-reset-derived-state",
		Description:    "Reset derived RAG state.",
		CLI:            disabled("Not implemented; destructive cache maintenance requires an explicit product design."),
		MCP:            disabled("Destructive cache-invalidating RAG maintenance is not exposed through MCP by default."),
	},
}

func WriteCapabilities() []Capability {
	return append([]Capability(nil), writeCapabilities...)
}

func RAGCapabilities() []Capability {
	return append([]Capability(nil), ragCapabilities...)
}

func Capabilities() []Capability {
	out := append([]Capability(nil), writeCapabilities...)
	out = append(out, ragCapabilities...)
	return out
}

func LookupByMCPName(name string) (Capability, bool) {
	for _, cap := range writeCapabilities {
		if cap.MCPName == name {
			return cap, true
		}
	}
	for _, cap := range ragCapabilities {
		if cap.MCPName == name {
			return cap, true
		}
	}
	return Capability{}, false
}

func MCPWriteCapabilities() []Capability {
	var out []Capability
	for _, cap := range writeCapabilities {
		if cap.MCP.Enabled && cap.MCPName != "" {
			out = append(out, cap)
		}
	}
	return out
}

func MCPRAGCapabilities() []Capability {
	var out []Capability
	for _, cap := range ragCapabilities {
		if cap.MCP.Enabled && cap.MCPName != "" {
			out = append(out, cap)
		}
	}
	return out
}

func MCPWriteToolNames() map[string]bool {
	names := map[string]bool{}
	for _, cap := range MCPWriteCapabilities() {
		if cap.Safety == SafetyReadOnly {
			continue
		}
		names[cap.MCPName] = true
	}
	return names
}
