# Validation Scenarios: 004 Minimal MCP server construction path

## 004-internal-mcp-task-4-add-minimal-mcp-server-construction-path-internal-scenario-1

MCP server starts with a read-only cache path whose missing parent directory cannot be created. A real built `gitcode-mcp --mcp --cache-path ...` stdio invocation sends `tools/list`; the JSON-RPC response must still include the `doctor` tool and must include a `cache_path_unwritable` startup diagnostic in the response metadata.

## 004-internal-mcp-task-4-add-minimal-mcp-server-construction-path-internal-scenario-2

MCP server starts with a local SQLite cache whose `schema_version` is greater than the binary-supported schema version. A real built `gitcode-mcp --mcp --cache-path ...` stdio invocation sends `tools/list` and `tools/call doctor`; `tools/list` must include `doctor` and `schema_incompatible`, and `doctor` must return structured actionable upgrade remediation.

## 004-internal-mcp-task-4-add-minimal-mcp-server-construction-path-internal-scenario-3

MCP server starts while the cache writer lock is held by a local lock-holder process. A real built `gitcode-mcp --mcp --cache-path ...` stdio invocation sends `tools/list`; the response must still include the `doctor` tool and must include `cache_lock_contention` instead of returning an empty tool list or exiting before serving JSON-RPC.

## 004-internal-mcp-task-4-add-minimal-mcp-server-construction-path-internal-scenario-4

MCP server starts with a deterministic pre-service cache initialization failure by using a cache path below a regular file. A real built `gitcode-mcp --mcp --cache-path ...` stdio invocation sends `tools/list` and `tools/call doctor`; the response must include the minimal `doctor` tool and the doctor result must include a `startup-failure` diagnostic with actionable remediation text, not raw stack-like text.
