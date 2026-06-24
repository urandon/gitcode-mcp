# 009 audit-runtime task 1: persist audit confirmations

## 009-audit-runtime-task-1-persist-audit-confirmations-scenario-1

When an operator-equivalent live create issue route runs with a valid test token against a stubbed GitCode HTTP provider, the service/runtime write path records an audit confirmation visible through `GetAuditEventByKey(repo, idempotency_key)`. The record must include command metadata, `mode=live`, idempotency reference, timestamp, succeeded status, payload hash, and a remote alias or remote id.

## 009-audit-runtime-task-1-persist-audit-confirmations-scenario-2

The persisted audit confirmation and command/result surface must not contain token material, Authorization header content, cookie values, private URLs, or raw API body content. The live create issue route must not return or persist `fixture client is read-only`.

## 009-audit-runtime-task-1-persist-audit-confirmations-scenario-3

Executable evidence is offline and deterministic: the validation runner injects a temporary Go runtime test that uses a stubbed external GitCode HTTP provider plus the real cache/audit store and then runs `go test ./...` without real credentials, external network, or OS Keychain access.
