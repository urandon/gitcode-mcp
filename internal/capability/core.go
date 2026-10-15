package capability

// CoreCapabilities fills the read/coordinator inventory alongside the existing
// write, RAG, Admin and local-project catalogs. It does not register transports.
func CoreCapabilities() []Capability {
	read := func(id, cli, usecase, description string, aliases ...string) Capability {
		return Capability{ID: id, Category: "read", Safety: SafetyReadOnly, CLIName: cli, CLIAliases: aliases, MCPName: id, ServiceCommand: usecase, Description: description, CLI: enabled(), MCP: enabled(), UI: disabled("No standalone Admin action; composed browser workbenches have their own declared capability and bounds.")}
	}
	out := []Capability{
		read("search_sources", "search", "SearchSources", "Cached hybrid/full-text source search; shared source-query page and provenance filters.", "search_sources"),
		read("list_sources", "list", "ListSources", "Cached source list; default 20, explicit 1..100, nonnegative offset and cache kind/provenance filters."),
		read("get_source", "get", "GetSource", "Get a cached source by stable identity or alias."),
		read("source_backlinks", "backlinks", "GetBacklinks", "Read cached stable-source backlinks."),
		read("resolve_id", "", "ResolveID", "Resolve a cached alias to stable identity."),
		read("list_chunks", "list-chunks", "ListChunks", "Read bounded cached index chunks."),
		read("search_chunks", "", "SearchChunks", "Deterministic token search over cached chunks; distinct from hybrid source search."),
		read("get_snippet", "get-snippet", "GetChunkSnippet", "Read an explicitly addressed cached snippet.", "snippet", "snippets"),
		read("recent_changes", "recent", "RecentChanges", "Read cached recent changes with filters and pagination."),
		read("link_check", "link-check", "LinkCheck", "Inspect cached link validity."),
		read("stale_index_report", "stale-index", "StaleIndex", "Inspect missing/stale derived index state; strict mode may fail."),
		read("cache_status", "cache-status", "CacheStatus", "Read cache coverage and schema status without a live request."),
		read("sync_status", "sync-status", "SyncStatus", "Read source/aggregate sync coverage; aggregate omission remains unbounded.", "sync_status"),
		read("export_snapshot", "export", "ExportSnapshot", "Export a deterministic cached snapshot.", "export-snapshot"),
		read("diff_snapshot", "diff", "DiffSnapshot", "Compare deterministic cached snapshots.", "diff-snapshot"),
		read("repo_status", "repo", "RepositoryStatus", "Read local repository bindings and cache state, not live repository metadata."),
		read("list_pr_discussions", "pr-discussions", "ListPRDiscussions", "Read cached PR discussions and unresolved state.", "pr-review-discussions"),
		read("service_status", "service", "Service.Status", "Read coordinator status without implicit start or setup."),
		read("maintenance_status", "service maintenance", "Maintenance.List", "Read registered maintenance policies and coverage."),
		read("maintenance_plan", "maintenance", "Maintenance.Plan", "Inspect a local maintenance effect ledger; planning does not apply effects."),
		read("service_jobs", "service", "Jobs.List", "Read bounded coordinator job inventory."),
		read("service_job_status", "service", "Jobs.Get", "Read one coordinator job."),
		read("service_job_attach", "service", "Jobs.Attach", "Observe job progress without starting work."),
		read("service_observability", "service", "Observability.Summary", "Bounded private IPC observation; no cache/credential bootstrap."),
		read("service_metrics", "service", "Observability.Metrics", "Bounded fixed-name metrics and retained minute history."),
		read("service_events", "service", "Observability.Events", "Bounded execution ledger; opaque bound cursor and finite query errors."),
		read("auth_status", "auth", "AuthStatus", "Report redacted credential readiness; never returns a secret."),
		read("doctor", "doctor", "Doctor", "Read finite local diagnostics and handoffs without setup."),
		read("sync_live", "sync", "SyncLive", "Explicit bounded provider-to-cache collection synchronization."),
		read("index_repo", "index", "Index", "Explicit derived cache index maintenance."),
		read("enable_cache_maintenance", "maintenance", "Maintenance.Enable", "Confirm and apply an explicit local maintenance plan with a durable caller key."),
		read("service_job_cancel", "service", "Jobs.Cancel", "Explicit coordinator job cancellation."),
	}
	for i := range out {
		switch out[i].ID {
		case "resolve_id", "search_chunks":
			out[i].CLI = disabled("Dedicated CLI command is not implemented; MCP exposes the shared service read directly. Source get/search and snippet addressing are not equivalent aliases.")
		case "sync_live", "index_repo", "enable_cache_maintenance", "service_job_cancel":
			out[i].Category, out[i].Safety = "coordinator", SafetyBackgroundJob
		}
	}
	// Local machine/file operations are intentionally not RPC tools. These entries
	// make their command names auditable without broadening MCP authority.
	for _, item := range []struct {
		id, cli, usecase, reason string
		safety                   SafetyClass
	}{
		{"ingest", "ingest", "Ingest", "Trusted fixture/file ingestion is an explicit CLI handoff, not a remote MCP upload.", SafetyBackgroundJob},
		{"cache_maintenance", "cache", "CacheReset", "Cache reset is destructive local maintenance with explicit CLI confirmation.", SafetyDestructiveLocalMaintenance},
		{"config_management", "config", "Config", "Trusted machine configuration/files are CLI-only; browser setup uses bounded plans.", SafetyCredentialManagement},
		{"admin_listener", "admin", "AdminListener", "Loopback listener lifecycle/open is a trusted local CLI handoff, not an MCP browser launcher.", SafetyBackgroundJob},
		{"service_lifecycle", "service", "ServiceLifecycle", "Machine service install/start/repair/stop and local test jobs are explicit CLI handoffs, not MCP methods.", SafetyBackgroundJob},
		{"rag_setup", "rag", "RAGSetup", "Provider/model installation and download require explicit local CLI setup authority, never an implicit MCP effect.", SafetyDestructiveLocalMaintenance},
		{"cache_migration", "migrate-cache", "MigrateCache", "Schema migrations are explicit local CLI maintenance, never a read or MCP bootstrap.", SafetyDestructiveLocalMaintenance},
		{"repository_binding", "bind", "AddRepositoryBinding", "Binding setup is explicit local CLI/Admin work; MCP does not accept cache paths.", SafetyBackgroundJob},
		{"repository_local_bootstrap", "repo", "InitLocalRepository", "Repository-local configuration/cache bootstrap is an explicit trusted CLI action, distinct from scope-only binding updates.", SafetyBackgroundJob},
		{"repository_docs_registration", "repo-docs", "RepositoryDocs.RegisterSource/RebindSource", "Trusted Git worktree path selection is CLI-only; MCP source discovery returns opaque registered selectors, not equivalent setup authority.", SafetyBackgroundJob},
		{"legacy_add_comment", "add-comment", "RejectAmbiguousCommentTarget", "Deprecated ambiguous command deliberately rejects; use add-issue-comment or add-pr-comment.", SafetyReadOnly},
	} {
		out = append(out, Capability{ID: item.id, Category: "local", Safety: item.safety, CLIName: item.cli, ServiceCommand: item.usecase, Description: item.reason, CLI: enabled(item.reason), MCP: disabled(item.reason), UI: disabled("No matching browser command; Admin cohorts declare their own controls.")})
	}
	return out
}
