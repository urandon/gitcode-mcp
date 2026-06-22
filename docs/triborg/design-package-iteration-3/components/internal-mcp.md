# Design Package Component: internal-mcp

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: MCP Server

## Summary
MCP Server owns this component design slice.

## Top-Level Alignment
This component design follows the approved architecture and the validated component-impact deltas.

## Tasks

### Task 1: Change MCP server tool registration and parity val
Outcome IDs: outcome-12
Outcome Role: supporting_evidence
Decommission IDs: decommission-12
Change Type: change
Description: Implement the `MCP server tool registration and parity validation` delta inside `MCP Server`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `MCP Server` boundaries where available and add only the missing `MCP server tool registration and parity validation` behavior.
Detailed Design: Add or change `MCP server tool registration and parity validation` so it satisfies `Update MCP server tool registration with corrected kind enums (issue/wiki); ensure tool dispatch routes to cache and search providers; implement MCP parity validation test that connects over stdio and HTTP/SSE, invokes all 7 read tools (cache_status, list_sources, get_source, sync_status, list_chunks, search_chunks, search_sources) against live-synced cache, asserts non-error responses and correct results.`. Keep ownership inside `MCP Server`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: MCP client connects over stdio → invokes all 7 read tools after live sync → all return correct non-error results. HTTP/SSE transport → same tools return correct results. Validation test passes against live-synced cache..
Workload: 1 MM

## Cross-Cutting Constraints
- Keep component-owned state explicit and testable.

## Data And Control Flow
- `MCP Server` receives architecture-approved inputs and returns validated component-local outputs.

## Component Interactions
- `MCP Server` -> `approved architecture` - component behavior remains traceable.

## Rationale
The validated component impact assigns concrete owned deltas to this component.

## Skip Rationale
Not skipped.
