# Design Package Component: internal-mcp

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: MCP Lifecycle Surface

## Summary
MCP Lifecycle Surface owns this component design slice.

## Top-Level Alignment
This component design follows the approved architecture and the validated component-impact deltas.

## Tasks

### Task 1: Change MCP tool registry (internal/mcp/tools.go)
Outcome IDs: outcome-1
Outcome Role: primary_product
Decommission IDs: decommission-1
Change Type: change
Description: Implement the `MCP tool registry (internal/mcp/tools.go)` delta inside `MCP Lifecycle Surface`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `MCP Lifecycle Surface` boundaries where available and add only the missing `MCP tool registry (internal/mcp/tools.go)` behavior.
Detailed Design: Add or change `MCP tool registry (internal/mcp/tools.go)` so it satisfies `Migrate tool registry from positional slice []MCPTool to map[string]MCPTool keyed by tool name. tools/call resolves handlers by map key lookup. Adding a lifecycle tool adds a map entry without shifting existing handler mappings.`. Keep ownership inside `MCP Lifecycle Surface`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: MCP client calls tools/call with tool name; handler resolved by map key lookup. New lifecycle tool addition proves handler resolution by name. go test ./internal/mcp/... passes with map-based registry..
Workload: 1 MM

### Task 2: Add Lifecycle MCP tools (internal/mcp/lifecycle_to
Outcome IDs: outcome-1
Outcome Role: primary_product
Decommission IDs: decommission-2
Change Type: add
Description: Implement the `Lifecycle MCP tools (internal/mcp/lifecycle_tools.go)` delta inside `MCP Lifecycle Surface`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `MCP Lifecycle Surface` boundaries where available and add only the missing `Lifecycle MCP tools (internal/mcp/lifecycle_tools.go)` behavior.
Detailed Design: Add or change `Lifecycle MCP tools (internal/mcp/lifecycle_tools.go)` so it satisfies `Register lifecycle MCP tools: repo_status (returns binding state including nothing-bound), sync_live (accepts collection selectors --issues/--wiki/--comments/--pulls), index_repo (invokes Service.Index not Service.StaleIndex), auth_status (returns credential presence/source), doctor (returns structured diagnostics). Define MCP parameter schemas and result shapes for each.`. Keep ownership inside `MCP Lifecycle Surface`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: MCP client calls tools/list against writable cache; sees repo_status, sync_live, index_repo, auth_status, doctor tool names. Calling repo_status on empty cache returns nothing bound. Calling sync_live --issues returns sync event with fresh count. Calling index_repo invokes Service.Index (observed by index outcome, not stale-index diagnostic). Calling search_sources/list_sources returns synced records..
Workload: 1 MM

### Task 3: Add Unsupported capability handler (internal/mcp/u
Outcome IDs: outcome-1
Outcome Role: supporting_evidence
Decommission IDs: decommission-1, decommission-2
Change Type: add
Description: Implement the `Unsupported capability handler (internal/mcp/unsupported_capability.go)` delta inside `MCP Lifecycle Surface`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `MCP Lifecycle Surface` boundaries where available and add only the missing `Unsupported capability handler (internal/mcp/unsupported_capability.go)` behavior.
Detailed Design: Add or change `Unsupported capability handler (internal/mcp/unsupported_capability.go)` so it satisfies `Create shared unsupportedCapabilityHandler for known write tool names (create_issue, update_issue, add_comment, create_page, update_page). Returns structured unsupported_capability result without credential lookup or outbound HTTP call. Write tool names are not advertised in tools/list.`. Keep ownership inside `MCP Lifecycle Surface`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: MCP client calls tools/call with create_issue tool name; receives structured unsupported_capability result. No credential lookup performed. No outbound HTTP call emitted on provider mock. Write tool names absent from serialized tools/list response..
Workload: 1 MM

### Task 4: Add Minimal MCP server construction path (internal
Outcome IDs: outcome-2
Outcome Role: primary_product
Decommission IDs: decommission-2-2
Change Type: add
Description: Implement the `Minimal MCP server construction path (internal/mcp/server.go)` delta inside `MCP Lifecycle Surface`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `MCP Lifecycle Surface` boundaries where available and add only the missing `Minimal MCP server construction path (internal/mcp/server.go)` behavior.
Detailed Design: Add or change `Minimal MCP server construction path (internal/mcp/server.go)` so it satisfies `Split Server.New() into healthy path (full Service-backed server) and minimal fallback path (DoctorService-backed server) when cache init, schema validation, or Service construction fails. Minimal server carries StartupDiagnostic struct (error class, message, remediation text) and injects it into every tools/list capability metadata and doctor tool result. Doctor tool always advertised.`. Keep ownership inside `MCP Lifecycle Surface`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: MCP server starts with read-only cache path; tools/list returns doctor tool + cache_path_unwritable diagnostic in capability metadata. Schema version > 11 yields schema_incompatible diagnostic. Writer-locked cache yields cache_lock_contention diagnostic. Injected cache init failure before Service creation yields minimal server with doctor tool; calling doctor returns startup-failure diagnostic with actionable text..
Workload: 1 MM

### Task 5: Add Startup diagnostic injection (internal/mcp/sta
Outcome IDs: outcome-2
Outcome Role: supporting_evidence
Decommission IDs: decommission-2-2
Change Type: add
Description: Implement the `Startup diagnostic injection (internal/mcp/startup_diagnostic.go)` delta inside `MCP Lifecycle Surface`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `MCP Lifecycle Surface` boundaries where available and add only the missing `Startup diagnostic injection (internal/mcp/startup_diagnostic.go)` behavior.
Detailed Design: Add or change `Startup diagnostic injection (internal/mcp/startup_diagnostic.go)` so it satisfies `Define StartupDiagnostic struct with fields for error class, message, and remediation text. Inject into tools/list response via server capability metadata block. Doctor MCP tool handler returns structured diagnostic body including startup-failure when present and actionable remediation referencing CLI commands or GitCode UI steps.`. Keep ownership inside `MCP Lifecycle Surface`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: tools/list serialized response inspected for diagnostic fields in capability block. Doctor tool call result contains structured diagnostic with error_class, message, remediation fields. cache_path_unwritable includes text referencing chmod or cache path config. schema_incompatible includes text referencing binary upgrade. Startup-failure diagnostic includes actionable text not a raw stack trace..
Workload: 1 MM

### Task 6: Validate MCP lifecycle tool integration tests (int
Outcome IDs: outcome-1
Outcome Role: supporting_evidence
Decommission IDs: decommission-1, decommission-2
Change Type: validate
Description: Implement the `MCP lifecycle tool integration tests (internal/mcp/lifecycle_test.go)` delta inside `MCP Lifecycle Surface`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `MCP Lifecycle Surface` boundaries where available and add only the missing `MCP lifecycle tool integration tests (internal/mcp/lifecycle_test.go)` behavior.
Detailed Design: Add or change `MCP lifecycle tool integration tests (internal/mcp/lifecycle_test.go)` so it satisfies `Write mocked MCP tests: empty writable cache initialized, repo_status returns nothing bound, sync_live --issues returns sync event with fresh count, index_repo invokes Service.Index (test observes index outcome), search_sources/list_sources returns synced records. Separate test for name-based handler resolution after adding new lifecycle tool. Test that create_issue via tools/call returns unsupported_capability without credential lookup or HTTP call.`. Keep ownership inside `MCP Lifecycle Surface`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: go test ./internal/mcp/... passes. All lifecycle tool integration scenarios verified with mocked Service and cache backend. Test assertions inspect serialized MCP response objects for tool names, result shapes, and handler routing..
Workload: 1 MM

### Task 7: Validate MCP startup/readiness diagnostic tests (i
Outcome IDs: outcome-2
Outcome Role: supporting_evidence
Decommission IDs: decommission-2-2
Change Type: validate
Description: Implement the `MCP startup/readiness diagnostic tests (internal/mcp/startup_test.go)` delta inside `MCP Lifecycle Surface`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `MCP Lifecycle Surface` boundaries where available and add only the missing `MCP startup/readiness diagnostic tests (internal/mcp/startup_test.go)` behavior.
Detailed Design: Add or change `MCP startup/readiness diagnostic tests (internal/mcp/startup_test.go)` so it satisfies `Write mocked MCP tests: read-only cache path yields tools/list with doctor + cache_path_unwritable diagnostic. Schema version > 11 yields schema_incompatible diagnostic. Writer-locked cache yields cache_lock_contention diagnostic. Injected cache init failure before Service construction yields minimal server with doctor whose tool result contains startup-failure diagnostic.`. Keep ownership inside `MCP Lifecycle Surface`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: go test ./internal/mcp/... passes. Each failure scenario produces typed diagnostic in tools/list capability metadata. Doctor tool call returns structured diagnostic body with actionable remediation text for each failure class..
Workload: 1 MM

## Cross-Cutting Constraints
- Keep component-owned state explicit and testable.

## Data And Control Flow
- `MCP Lifecycle Surface` receives architecture-approved inputs and returns validated component-local outputs.

## Component Interactions
- `MCP Lifecycle Surface` -> `approved architecture` - component behavior remains traceable.

## Rationale
The validated component impact assigns concrete owned deltas to this component.

## Skip Rationale
Not skipped.
