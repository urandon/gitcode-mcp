package capability

import "strings"

// Local-only discovery is opt-in; these are never GitCode write capabilities.
func LocalProjectCapabilities() []Capability {
	var out []Capability
	for _, name := range []string{"status", "get", "list", "history", "mutate", "search", "index"} {
		safety := SafetyReadOnly
		if name == "mutate" {
			safety = SafetyAuditedWrite
		}
		if name == "index" {
			// Same derived-work tier as repository_docs_index; no project data
			// deletion, arbitrary cache reset or machine-level maintenance.
			safety = SafetyBackgroundJob
		}
		description := map[string]string{
			"status":  "Discover registered local project selectors, stable identity, readable/read-only state, exact current commit and finite conflict evidence. No content or storage paths.",
			"get":     "Read one current local issue, comment or wiki entity by project-scoped stable ID, with its exact commit/blob/Git-tree citation.",
			"list":    "List bounded local issue/comment/wiki entities and exact citations. Comments use a project-wide stable counter. Default 20, maximum 128; continue explicitly with offset.",
			"history": "Read bounded first-parent Git revision citations for one local entity, including tombstones. Default 64, maximum 256 commits; exhausted history is unavailable, not a complete or absent result.",
			"mutate":  "Create, replace or tombstone a local issue/comment/wiki with a fixed typed operation. Requires write_mode=local, caller key and expected full HEAD for creates or entity blob/HEAD for edits/deletes. Updates replace all editable fields; explicitly preserve retained metadata/body. After ambiguity or restart reuse the same key/intent, never blind-retry another key; receipts return the original result.",
			"search":  "Search active local issue/comment/wiki files at one exact Git commit, returning project-scoped entity citations and bounded snippets. Default fulltext works without cache/provider. Explicit hybrid needs a configured pre-provisioned profile and ready derived index; missing/stale/failed state visibly falls back to lexical. Never repairs, installs, starts or downloads anything.",
			"index":   "Explicit bounded foreground indexing of active local project Git files into disposable metadata/vectors using the existing repository-docs engine. Requires write_mode=local and a pre-provisioned local-boundary profile; max_chunks defaults64, maximum128 new embeddings per call. Partial coverage is not ready; repeat to resume/reuse vectors. No setup/download, GitCode, content copies or authoritative project mutation.",
		}[name] + " Local Git only: no GitCode, credential discovery or implicit daemon start."
		out = append(out, Capability{ID: "local_project_" + name, Category: CategoryLocalProject, Safety: safety, CLIName: "project", MCPName: "local_project_" + name, ServiceCommand: "LocalProjects." + strings.ToUpper(name[:1]) + name[1:], Description: description, CLI: enabled(), MCP: enabled(), UI: enabled("Available only in explicitly enabled protected loopback Local projects Admin; writes require plan/confirm.")})
	}
	out = append(out, Capability{ID: "local_project_setup", Category: "local_project", Safety: SafetyDestructiveLocalMaintenance, CLIName: "project", ServiceCommand: "local-project-setup", Description: "Explicitly initialize/register a managed Git store in trusted machine configuration.", CLI: enabled(), MCP: disabled("Storage selection and machine configuration are CLI-only confirmed handoffs."), UI: disabled("CLI-only machine setup; #206 does not accept storage paths.")})
	out = append(out, Capability{ID: "local_project_backup", Category: CategoryLocalProject, Safety: SafetyDestructiveLocalMaintenance, CLIName: "project", ServiceCommand: "local-project-backup", Description: "Confirmed private ordinary Git bundle export/restore. Restore validates exact identity/ref/head and complete bounded source/receipt history; only new inactive managed destinations, no overwrite or cache authority.", CLI: enabled(), MCP: disabled("Private backup files and storage registration are CLI-only handoffs."), UI: disabled("Use project export/restore --help in the trusted CLI; browser never accepts backup paths or bytes.")})
	return out
}
