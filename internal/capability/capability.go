package capability

type Category string

const (
	CategoryWrite Category = "write"
)

type SafetyClass string

const (
	SafetyAuditedWrite                SafetyClass = "audited_write"
	SafetyDestructiveRemoteWrite      SafetyClass = "destructive_remote_write"
	SafetyDestructiveLocalMaintenance SafetyClass = "destructive_local_maintenance"
	SafetyCredentialManagement        SafetyClass = "credential_management"
	SafetyRawEscapeHatch              SafetyClass = "raw_escape_hatch"
)

type Surface struct {
	Enabled        bool
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

func enabled() Surface {
	return Surface{Enabled: true}
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
		MCPName:        "update_pr",
		ServiceCommand: "update-pr",
		Description:    "Update live pull request metadata through the audited write lifecycle.",
		CLI:            disabled("CLI update-pr command is not implemented yet; tracked by CLI/MCP parity issue #32."),
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
		MCP:            disabled("Wiki writes are CLI-only until MCP has stricter confirmation and cache-confirmation UX."),
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
		MCP:            disabled("Wiki writes are CLI-only until MCP has stricter confirmation and cache-confirmation UX."),
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
		MCP:            disabled("Destructive remote deletes are CLI-only and require explicit operator intent."),
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
		MCP:            disabled("Label mutation remains CLI-only until write parity defines single-record label semantics for MCP."),
	},
}

func WriteCapabilities() []Capability {
	return append([]Capability(nil), writeCapabilities...)
}

func LookupByMCPName(name string) (Capability, bool) {
	for _, cap := range writeCapabilities {
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

func MCPWriteToolNames() map[string]bool {
	names := map[string]bool{}
	for _, cap := range MCPWriteCapabilities() {
		names[cap.MCPName] = true
	}
	return names
}
