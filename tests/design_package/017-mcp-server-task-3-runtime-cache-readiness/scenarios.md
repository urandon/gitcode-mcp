# Runtime cache readiness validation scenarios

## 017-mcp-server-task-3-runtime-cache-readiness-scenario-1

With one temporary SQLite cache, two MCP HTTP/SSE clients issue read tool calls while a sync/index operation in another process or test harness holds the writer lock; safe reads complete with normal JSON-RPC responses and any lock-affected operation returns typed `busy`, `cache_owned`, or `migration_blocked` error data instead of hanging or reporting success.

Concrete validation:
- Build a temporary SQLite cache with repository `fixture-a` and a source record.
- Open multiple cache handles, hold a real cache writer lease with operation `sync-index`, and serve MCP HTTP/SSE against the same cache.
- Connect two SSE sessions and post concurrent `tools/call` `list_sources` read requests; both must receive successful JSON-RPC responses over their own SSE streams.
- Post a read-path request through a lock-contention service shim backed by the same held cache writer lease; the JSON-RPC response must be an error with `error.data.code` equal to `cache_owned`, `busy`, or `migration_blocked`, not a success and not a timeout.

## 017-mcp-server-task-3-runtime-cache-readiness-scenario-2

`/ready` reflects unreadable or migration-blocked cache states, `/health` continues to report process liveness, and stdio returns the same typed JSON-RPC error mapping for equivalent cache failures.

Concrete validation:
- Hold a real cache writer lease, attempt a second cache open to trigger the cache component's migration-blocked lock-contention error, and wire that error into HTTP/SSE readiness.
- Assert `GET /health` returns 200 while `GET /ready` returns 503 with typed `migration_blocked` readiness/error data.
- Run an equivalent stdio JSON-RPC `tools/call` through the same MCP runtime error mapper and assert `error.data.code` matches the HTTP/SSE typed code family.

## 017-mcp-server-task-3-runtime-cache-readiness-scenario-3

A runtime test cancels one HTTP `/message` request or disconnects one SSE client during a slow read, verifies the session is cleaned up and no goroutine/queue leak blocks the server, and verifies another client read succeeds concurrently; executable evidence is an MCP runtime concurrency test backed by the cache component’s temporary SQLite lock harness plus a server/API cancellation test.

Concrete validation:
- Serve MCP HTTP/SSE with a slow read service path that blocks until the test releases it and observes request cancellation.
- Start one SSE session and cancel its `/message` HTTP request during the slow read; verify no JSON-RPC response is enqueued to that session after cancellation.
- Start another SSE session at the same time and verify a normal `list_sources` read succeeds, proving the cancelled/slow request does not block another client or session queue.
