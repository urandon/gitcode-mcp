package capability

import "strings"

// Local-only discovery is opt-in; these are never GitCode write capabilities.
func LocalProjectCapabilities() []Capability {
	var out []Capability
	for _, name := range []string{"status", "get", "list", "history", "mutate"} {
		safety := SafetyReadOnly
		if name == "mutate" {
			safety = SafetyAuditedWrite
		}
		description := map[string]string{
			"status":  "Discover registered local project selectors, stable identity, readable/read-only state, exact current commit and finite conflict evidence. No content or storage paths.",
			"get":     "Read one current local issue, comment or wiki entity by project-scoped stable ID, with its exact commit/blob/Git-tree citation.",
			"list":    "List bounded local issue/comment/wiki entities and exact citations. Comments use a project-wide stable counter. Default 20, maximum 128; continue explicitly with offset.",
			"history": "Read bounded first-parent Git revision citations for one local entity, including tombstones. Default 64, maximum 256 commits; exhausted history is unavailable, not a complete or absent result.",
			"mutate":  "Create, replace or tombstone a local issue/comment/wiki with a fixed typed operation. Requires write_mode=local, caller key and expected full HEAD for creates or entity blob/HEAD for edits/deletes. Updates replace all editable fields; explicitly preserve retained metadata/body. After ambiguity or restart reuse the same key/intent, never blind-retry another key; receipts return the original result.",
		}[name] + " Local Git only: never GitCode, no implicit daemon start, cache, credential discovery or embedding provider."
		out = append(out, Capability{ID: "local_project_" + name, Category: CategoryLocalProject, Safety: safety, CLIName: "project", MCPName: "local_project_" + name, ServiceCommand: "LocalProjects." + strings.ToUpper(name[:1]) + name[1:], Description: description, CLI: enabled(), MCP: enabled(), UI: disabled("Protected Local projects Admin is required separately by epic #200 child #206.")})
	}
	out = append(out, Capability{ID: "local_project_setup", Category: "local_project", Safety: SafetyDestructiveLocalMaintenance, CLIName: "project", ServiceCommand: "local-project-setup", Description: "Explicitly initialize/register a managed Git store in trusted machine configuration.", CLI: enabled(), MCP: disabled("Storage selection and machine configuration are CLI-only confirmed handoffs."), UI: disabled("CLI-only machine setup; #206 does not accept storage paths.")})
	return out
}
