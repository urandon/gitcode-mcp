package capability

import "strings"

// Explicit dependencies of existing effect/bootstrap paths; no namespace or
// category grants implicit authority to a future operation.
var capabilityDependencies = map[string][]string{
	"service_job_attach": {"service_job_status"},
	"get_repo_metadata":  {"repo_status"}, "list_repo_labels": {"repo_status"}, "create_repo_label": {"repo_status"},
	"prepare_feedback": {"feedback_status"}, "submit_feedback": {"repo_status", "prepare_feedback", "feedback_status"}, "create_issue": {"repo_status"}, "update_issue": {"repo_status"}, "add_issue_comment": {"repo_status"}, "update_issue_comment": {"repo_status"},
	"create_pr": {"repo_status"}, "update_pr": {"repo_status"}, "merge_pr": {"repo_status"}, "add_pr_comment": {"repo_status"}, "add_pr_review_comment": {"repo_status"}, "reply_pr_review_comment": {"repo_status"}, "link_pr_issue": {"repo_status"},
	"list_milestones": {"repo_status"}, "create_milestone": {"repo_status"}, "update_milestone": {"repo_status"}, "set_issue_milestone": {"repo_status"}, "clear_issue_milestone": {"repo_status"},
	"list_push_remote_mirrors": {"repo_status"}, "trigger_push_remote_mirror": {"repo_status", "list_push_remote_mirrors"}, "wait_push_remote_mirror": {"repo_status", "list_push_remote_mirrors"},
	"create_page": {"repo_status"}, "update_page": {"repo_status"}, "delete_page": {"repo_status"}, "add_label": {"repo_status"}, "publish_release": {"repo_status"}, "sync_live": {"repo_status"},
	"repository_local_bootstrap": {"repository_binding"},
}

func CLIControl(args []string) bool {
	if len(args) == 0 {
		return true
	}
	if args[0] == "version" || args[0] == "--version" {
		return true
	}
	isHelp := func(s string) bool { return s == "--help" || s == "-h" }
	if isHelp(args[0]) || len(args) == 2 && isHelp(args[1]) {
		return true
	}
	if len(args) == 3 && isHelp(args[2]) {
		for _, forms := range CLIGroupedForms() {
			for _, form := range forms {
				if form == args[0]+" "+args[1] {
					return true
				}
			}
		}
	}
	return false
}

func (p *Policy) CheckCLI(args []string) error {
	if p == nil || CLIControl(args) {
		return nil
	}
	operation := args[0]
	groups := map[string]bool{}
	for _, forms := range CLIGroupedForms() {
		for _, form := range forms {
			groups[strings.Fields(form)[0]] = true
		}
	}
	if groups[operation] && len(args) > 1 {
		operation += " " + args[1]
	}
	return p.Require("cli", operation)
}

// Requirements names concrete entry points. Unknown operations have no grants:
// a managed policy rejects them instead of guessing from a namespace/prefix.
func Requirements(surface, operation string) []string {
	if surface == "mcp" {
		if item, ok := LookupByMCPName(operation); ok && item.MCP.Enabled {
			return []string{item.ID}
		}
		return nil
	}
	if surface == "cli" {
		if operation == "repo add" || operation == "repo set-scopes" {
			return []string{"admin_binding_plan_apply", "repository_binding"}
		}
		for id, forms := range CLIGroupedForms() {
			for _, form := range forms {
				if operation == form {
					return []string{id}
				}
			}
		}
		if strings.Contains(operation, " ") {
			return nil
		}
		// A namespace root is not an executable capability. However, grouped
		// entries can also expose a concrete legacy spelling (rag-status),
		// which must keep the same grant as its grouped form.
		for _, forms := range CLIGroupedForms() {
			for _, form := range forms {
				if operation == strings.Fields(form)[0] {
					return nil
				}
			}
		}
		for _, item := range Capabilities() {
			if !item.CLI.Enabled {
				continue
			}
			if item.CLIName == operation {
				return []string{item.ID}
			}
			for _, alias := range item.CLIAliases {
				if alias == operation {
					return []string{item.ID}
				}
			}
		}
		return nil
	}
	if surface == "service" {
		if ids, ok := serviceRequirements[operation]; ok {
			return append([]string(nil), ids...)
		}
		for _, item := range Capabilities() {
			if item.ServiceCommand == operation {
				return []string{item.ID}
			}
		}
	}
	if surface == "ipc" {
		return append([]string(nil), ipcRequirements[operation]...)
	}
	if surface == "job" {
		return append([]string(nil), jobRequirements[operation]...)
	}
	if surface == "admin" {
		return append([]string(nil), adminRequirements[operation]...)
	}
	if surface == "admin_route" {
		if method, ok := adminRoutes[operation]; ok {
			return Requirements("admin", method)
		}
	}
	return nil
}

// UIRequirements includes child grants of composite browser actions.
func UIRequirements(id string) []string {
	ids := []string{id}
	seen := map[string]bool{id: true}
	for _, required := range adminRequirements {
		if len(required) == 0 || required[0] != id {
			continue
		}
		for _, child := range required[1:] {
			if !seen[child] {
				ids = append(ids, child)
				seen[child] = true
			}
		}
	}
	return ids
}

var adminRequirements = map[string][]string{
	"Readiness":       {"service_status"},
	"PlanMaintenance": {"admin_maintenance_plan_apply", "maintenance_plan"}, "ApplyMaintenance": {"admin_maintenance_plan_apply", "enable_cache_maintenance"},
	"PlanBinding": {"admin_binding_plan_apply", "repository_binding"}, "ApplyBinding": {"admin_binding_plan_apply", "repository_binding"},
	"PlanFeedbackSetup": {"admin_feedback_setup"}, "ApplyFeedbackSetup": {"admin_feedback_setup"},
	"DisableMaintenance": {"admin_registration_controls", "enable_cache_maintenance"}, "ReconcileMaintenance": {"admin_registration_controls", "enable_cache_maintenance"},
	"PlanMaintenanceConflictResolution": {"admin_maintenance_conflict_resolution", "enable_cache_maintenance"}, "ApplyMaintenanceConflictResolution": {"admin_maintenance_conflict_resolution", "enable_cache_maintenance"},
	"SearchRepositoryDocs": {"repository_docs_search"}, "PlanRepositoryDocs": {"repository_docs_plan"}, "IndexRepositoryDocs": {"repository_docs_index"},
	"CompareSearch": {"admin_search_compare", "search_sources"}, "SmokeProvider": {"admin_provider_smoke", "rag_status"}, "PlanRAGRepair": {"admin_rag_bounded_repair", "rag_index"}, "ApplyRAGRepair": {"admin_rag_bounded_repair", "rag_index"},
	"CancelJob": {"service_job_cancel"}, "RetryJob": {"service_jobs"},
	"Caches": {"repo_status", "cache_status"}, "Jobs": {"service_jobs"}, "Maintenance": {"maintenance_status"}, "Diagnostics": {"doctor"},
	"Observability": {"admin_service_observation", "service_observability"}, "Metrics": {"admin_service_observation", "service_metrics"}, "Events": {"admin_service_observation", "service_events"},
	"LocalStatus": {"local_project_status"}, "LocalGet": {"local_project_get"}, "LocalList": {"local_project_list"}, "LocalHistory": {"local_project_history"}, "LocalSearch": {"local_project_search"}, "LocalMutate": {"local_project_mutate"}, "LocalIndex": {"local_project_index"},
}

var adminRoutes = map[string]string{
	"GET /api/admin/v1/readiness":         "Readiness",
	"GET /api/admin/v1/events":            "Events",
	"POST /api/admin/v1/maintenance/plan": "PlanMaintenance", "POST /api/admin/v1/maintenance/apply": "ApplyMaintenance",
	"POST /api/admin/v1/maintenance/{registration_id}/disable": "DisableMaintenance", "POST /api/admin/v1/maintenance/{registration_id}/reconcile": "ReconcileMaintenance",
	"POST /api/admin/v1/maintenance/{registration_id}/conflict-resolution/plan": "PlanMaintenanceConflictResolution", "POST /api/admin/v1/maintenance/{registration_id}/conflict-resolution/apply": "ApplyMaintenanceConflictResolution",
	"POST /api/admin/v1/bindings/plan": "PlanBinding", "POST /api/admin/v1/bindings/apply": "ApplyBinding",
	"POST /api/admin/v1/feedback/setup/plan": "PlanFeedbackSetup", "POST /api/admin/v1/feedback/setup/apply": "ApplyFeedbackSetup",
	"POST /api/admin/v1/search/compare": "CompareSearch", "POST /api/admin/v1/rag/provider/smoke": "SmokeProvider", "POST /api/admin/v1/rag/repair/plan": "PlanRAGRepair", "POST /api/admin/v1/rag/repair/apply": "ApplyRAGRepair",
	"POST /api/admin/v1/repository-docs/{registration_id}/search": "SearchRepositoryDocs", "POST /api/admin/v1/repository-docs/{registration_id}/plan": "PlanRepositoryDocs", "POST /api/admin/v1/repository-docs/{registration_id}/index": "IndexRepositoryDocs",
	"POST /api/admin/v1/jobs/{job_id}/cancel": "CancelJob", "POST /api/admin/v1/jobs/{job_id}/retry": "RetryJob",
	"GET /api/admin/v1/caches": "Caches", "GET /api/admin/v1/caches/{cache_ref}": "Caches", "GET /api/admin/v1/caches/{cache_ref}/repositories/{repo_id...}": "Caches",
	"GET /api/admin/v1/jobs": "Jobs", "GET /api/admin/v1/jobs/{job_id}": "Jobs", "GET /api/admin/v1/maintenance": "Maintenance", "GET /api/admin/v1/diagnostics": "Diagnostics",
	"GET /api/admin/v1/observability": "Observability", "GET /api/admin/v1/observability/metrics": "Metrics", "GET /api/admin/v1/observability/events": "Events",
	"GET /api/admin/v1/local-projects": "LocalStatus", "POST /api/admin/v1/local-projects/get": "LocalGet", "POST /api/admin/v1/local-projects/list": "LocalList", "POST /api/admin/v1/local-projects/history": "LocalHistory", "POST /api/admin/v1/local-projects/search": "LocalSearch",
	"POST /api/admin/v1/local-projects/mutation-plan": "LocalMutate", "POST /api/admin/v1/local-projects/mutation-apply": "LocalMutate", "POST /api/admin/v1/local-projects/index-plan": "LocalIndex", "POST /api/admin/v1/local-projects/index-apply": "LocalIndex",
}

var serviceRequirements = map[string][]string{
	"AddComment": {"add_issue_comment"}, "AddPRComment": {"add_pr_comment"}, "AddPRReviewComment": {"add_pr_review_comment"}, "ReplyPRReviewComment": {"reply_pr_review_comment"}, "UpdateComment": {"update_issue_comment"},
	"AddLabel": {"add_label"}, "LinkPRIssue": {"link_pr_issue"}, "PublishRelease": {"publish_release"},
	"CreateIssue": {"create_issue"}, "UpdateIssue": {"update_issue"}, "CreatePR": {"create_pr"}, "UpdatePR": {"update_pr"}, "MergePR": {"merge_pr"},
	"CreateMilestone": {"create_milestone"}, "UpdateMilestone": {"update_milestone"}, "SetIssueMilestone": {"set_issue_milestone"}, "ClearIssueMilestone": {"clear_issue_milestone"},
	"CreatePage": {"create_page"}, "UpdatePage": {"update_page"}, "DeletePage": {"delete_page"},
	"GetRepositoryMetadata": {"get_repo_metadata"}, "ListRepositoryLabels": {"list_repo_labels"}, "CreateRepositoryLabel": {"create_repo_label"},
	"ListMilestones": {"list_milestones"}, "ListPushRemoteMirrors": {"list_push_remote_mirrors"}, "TriggerPushRemoteMirror": {"trigger_push_remote_mirror"}, "WaitPushRemoteMirror": {"wait_push_remote_mirror"},
	"PrepareFeedback": {"prepare_feedback"}, "SubmitFeedback": {"submit_feedback"}, "FeedbackReadiness": {"feedback_status"},
	"RepositoryStatus": {"repo_status"}, "AddRepository": {"repository_binding"}, "UpdateRepository": {"repository_binding"}, "SetRepositoryScopes": {"repository_binding"},
	"ResetLiveCache": {"cache_maintenance"}, "GetBacklinks": {"source_backlinks"}, "GetSnippet": {"get_snippet"}, "GetChunkSnippet": {"get_snippet"},
	"GetSyncStatus": {"sync_status"}, "SyncStatus": {"sync_status"}, "ResolveID": {"resolve_id"}, "ResolveLiveRepositoryBinding": {"repo_status"}, "BuildAdapterRoute": {"repo_status"}, "DiagnoseUnscopedAlias": {"repo_status"},
}

func init() {
	for _, name := range []string{"SyncToCache", "SyncResources", "BulkSyncIssues", "BulkSyncIssueComments", "BulkSyncWiki", "BulkSyncPullRequests", "BulkSyncPRComments", "BulkSyncAll", "ResolveSyncSelection", "FetchIssueSyncBatch", "CommitIssueSyncBatch", "FetchPullSyncBatch", "CommitPullSyncBatch", "FetchWikiSyncBatch", "CommitWikiSyncBatch", "FetchIssueCommentSyncBatch", "CommitIssueCommentSyncBatch", "FetchPRCommentSyncBatch", "CommitPRCommentSyncBatch"} {
		serviceRequirements[name] = []string{"sync_live"}
	}
}

var ipcRequirements = map[string][]string{
	"Policy.Status": {"capability_policy_status"}, "Policy.Handshake": {"capability_policy_status"},
	"Service.Status": {"service_status"}, "Service.Health": {"service_status"}, "Service.Doctor": {"doctor"},
	"Admin.Status": {"admin_listener"}, "Admin.Open": {"admin_listener"}, "Jobs.StartFake": {"service_lifecycle"},
	"Jobs.StartRAGIndex": {"rag_index"}, "Jobs.StartRepositoryDocsIndex": {"repository_docs_index"}, "Jobs.StartSync": {"sync_live"},
	"Jobs.List": {"service_jobs"}, "Jobs.Get": {"service_job_status"}, "Jobs.Cancel": {"service_job_cancel"},
	"RepositoryDocs.RegisterSource": {"repository_docs_registration"}, "RepositoryDocs.RebindSource": {"repository_docs_registration"}, "RepositoryDocs.Sources": {"repository_docs_sources"},
	"RepositoryDocs.Policy": {"repository_docs_policy"}, "RepositoryDocs.Plan": {"repository_docs_plan"}, "RepositoryDocs.Status": {"repository_docs_status"}, "RepositoryDocs.Search": {"repository_docs_search"},
	"Maintenance.Enroll": {"enable_cache_maintenance"}, "Maintenance.Capabilities": {"maintenance_plan"}, "Maintenance.List": {"maintenance_status"},
	"Maintenance.Reconcile": {"service_lifecycle"}, "Maintenance.ReconcileRegistration": {"enable_cache_maintenance"}, "Maintenance.ResolveConfig": {"maintenance_plan"}, "Maintenance.Disable": {"enable_cache_maintenance"},
	"Observability.Capabilities": {"capability_policy_status"}, "Observability.Summary": {"service_observability"}, "Observability.Metrics": {"service_metrics"}, "Observability.Events": {"service_events"},
	"LocalProjects.Status": {"local_project_status"}, "LocalProjects.Get": {"local_project_get"}, "LocalProjects.List": {"local_project_list"}, "LocalProjects.History": {"local_project_history"},
	"LocalProjects.Search": {"local_project_search"}, "LocalProjects.Index": {"local_project_index"}, "LocalProjects.Mutate": {"local_project_mutate"},
}

var jobRequirements = map[string][]string{
	"sync": {"sync_live"}, "rag-index": {"rag_index"}, "repository-docs-index": {"repository_docs_index"}, "fake": {"service_lifecycle"},
}
