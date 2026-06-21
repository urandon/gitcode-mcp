# Design Package Component: internal-mcp-transport

This file is copied from the approved Triborg design package during implementator preflight.

# Component Design: MCP Transport Layer

## Summary
MCP Transport Layer owns this component design slice.

## Top-Level Alignment
This component design follows the approved architecture and the validated component-impact deltas.

## Tasks

### Task 1: Add HTTP/SSE transport handler with health/readine
Outcome IDs: outcome-12
Outcome Role: supporting_evidence
Decommission IDs: decommission-12
Change Type: add
Description: Implement the `HTTP/SSE transport handler with health/readiness endpoints` delta inside `MCP Transport Layer`. The component owns this local behavior and keeps the public handoff aligned with the approved architecture.
Existing Behavior / Reuse: Reuse existing `MCP Transport Layer` boundaries where available and add only the missing `HTTP/SSE transport handler with health/readiness endpoints` behavior.
Detailed Design: Add or change `HTTP/SSE transport handler with health/readiness endpoints` so it satisfies `Implement HTTP/SSE transport handler with stdio handler (existing) and HTTP/SSE handler: /health (GET → 200), /ready (GET → cache readable + repo configured), /sse (SSE endpoint), /message (JSON-RPC POST); include connection lifecycle, X-Request-ID correlation ID generation/logging.`. Keep ownership inside `MCP Transport Layer`, expose only the required handoff contract, and preserve fallback behavior for unsupported runtime cases.
Acceptance Criteria: gitcode-mcp --mcp → stdio transport works for single client. gitcode-mcp mcp serve --transport http-sse --bind 127.0.0.1:9020 → /health returns 200, /ready returns ready state, /sse streams, /message accepts JSON-RPC. Two concurrent MCP clients read from shared cache. Request logs include correlation IDs..
Workload: 1 MM

## Cross-Cutting Constraints
- Keep component-owned state explicit and testable.

## Data And Control Flow
- `MCP Transport Layer` receives architecture-approved inputs and returns validated component-local outputs.

## Component Interactions
- `MCP Transport Layer` -> `approved architecture` - component behavior remains traceable.

## Rationale
The validated component impact assigns concrete owned deltas to this component.

## Skip Rationale
Not skipped.
