# Design Package Component: internal-mcp-tools

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: MCP Tool Schemas

## Summary
MCP Tool Schemas owns this component design slice.

## Top-Level Alignment
This component design follows the approved architecture and the validated component-impact deltas.

## Tasks

### Task 1: Change MCP tool schemas with corrected kind enums
Outcome IDs: outcome-8
Outcome Role: primary_product
Decommission IDs: decommission-8
Change Type: change
Description: Implement the `MCP tool schemas with corrected kind enums (issue/wiki)` delta inside `MCP Tool Schemas`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `MCP Tool Schemas` boundaries where available and add only the missing `MCP tool schemas with corrected kind enums (issue/wiki)` behavior.
Detailed Design: Add or change `MCP tool schemas with corrected kind enums (issue/wiki)` so it satisfies `Update kind enums in list_sources, search_sources, search_chunks tool schemas to include issue and wiki; remove legacy values (source/task/page/decision/handoff); ensure MCP inspector shows corrected schemas.`. Keep ownership inside `MCP Tool Schemas`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: MCP inspector queries list_sources schema → kind enum includes issue and wiki. search_sources schema → kind filter enum includes issue and wiki. search_chunks schema → kind filter enum includes issue and wiki. Legacy values absent..
Workload: 1 MM

## Cross-Cutting Constraints
- Keep component-owned state explicit and testable.

## Data And Control Flow
- `MCP Tool Schemas` receives architecture-approved inputs and returns validated component-local outputs.

## Component Interactions
- `MCP Tool Schemas` -> `approved architecture` - component behavior remains traceable.

## Rationale
The validated component impact assigns concrete owned deltas to this component.

## Skip Rationale
Not skipped.
