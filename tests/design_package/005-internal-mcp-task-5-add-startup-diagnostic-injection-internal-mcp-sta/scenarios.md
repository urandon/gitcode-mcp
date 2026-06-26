# Validation Scenarios: Startup diagnostic injection

## 005-internal-mcp-task-5-add-startup-diagnostic-injection-internal-mcp-sta-scenario-1

`tools/list` serialized response is inspected through the MCP RPC handler on a minimal server carrying a startup diagnostic. The response must include `startup_diagnostic.error_class`, `startup_diagnostic.message`, and `startup_diagnostic.remediation` in the result metadata while still advertising the `doctor` tool.

## 005-internal-mcp-task-5-add-startup-diagnostic-injection-internal-mcp-sta-scenario-2

The `doctor` MCP tool is called through the MCP RPC handler when a startup diagnostic is present. The structured tool result must include a diagnostic object with `error_class`, `message`, and `remediation`. The `cache_path_unwritable` remediation must reference `chmod` or writable cache path configuration, and the `schema_incompatible` remediation must reference binary upgrade.

## 005-internal-mcp-task-5-add-startup-diagnostic-injection-internal-mcp-sta-scenario-3

A generic startup failure is converted into a structured `startup-failure` diagnostic. The diagnostic must contain actionable remediation text and must not expose raw panic text, file paths, or stack-trace-like implementation details.

## Offline validation command

Run:

```sh
bash tests/design_package/005-internal-mcp-task-5-add-startup-diagnostic-injection-internal-mcp-sta/run.sh
```
