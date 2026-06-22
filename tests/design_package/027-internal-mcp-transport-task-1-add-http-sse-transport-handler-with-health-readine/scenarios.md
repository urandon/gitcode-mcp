# Validation scenarios: 027-internal-mcp-transport-task-1-add-http-sse-transport-handler-with-health-readine

## 027-internal-mcp-transport-task-1-add-http-sse-transport-handler-with-health-readine-scenario-1
gitcode-mcp --mcp stdio transport works for a single client connecting through stdin/stdout JSON-RPC frames. The client sends `initialize` and receives a valid init response with protocol version, server info `gitcode-mcp`, and tools capability. The client then invokes a read tool (e.g., `list_sources`) against a populated cache and receives a non-error JSON-RPC tool result with structured content.

gitcode-mcp mcp serve --transport http-sse with internally-bound port serves:
- `GET /health` returns HTTP 200 with content-type application/json and body `{"status":"ok"}`.
- `GET /ready` returns HTTP 200 with content-type application/json and body containing `"ready":true` when the cache has repositories configured; returns HTTP 503 with `"ready":false` and a stable error `code` when the cache has no repositories or is unreadable.
- `GET /sse` establishes an event-stream, emits an `event: endpoint` with the session-bound `/message?session_id=...` URL, then waits for messages. When a `POST /message?session_id=...` arrives with a valid JSON-RPC request, the response is delivered over the SSE stream as `event: message` with the JSON-RPC response as data.
- `POST /message` accepts exactly one JSON-RPC body, validates the session_id, dispatches through the RPC handler, enqueues the response to the SSE session, and returns HTTP 202 Accepted.

Error handling produces deterministic transport errors: `missing_session` (400 when no session_id param), `unknown_session` (404 when session is not live or already closed), `invalid_json` (400 on parse errors or multiple JSON bodies), and `method_not_allowed` (405 on wrong HTTP method).

## 027-internal-mcp-transport-task-1-add-http-sse-transport-handler-with-health-readine-scenario-2
Two concurrent SSE clients connect to the same HTTP/SSE server backed by a shared populated cache. Each client opens its own `/sse` session and invokes MCP read tools concurrently through their respective `/message` endpoints. Responses must be delivered to the correct SSE session without cross-client response leakage — client A receives only responses to client A's requests, client B receives only responses to client B's requests. Closing one SSE session causes subsequent POST messages to that session to return 404, while the other session continues to serve requests correctly. Session ID deduplication ensures that if the same session ID generator produces a duplicate, the registry assigns a unique suffixed ID and both sessions function independently.

## 027-internal-mcp-transport-task-1-add-http-sse-transport-handler-with-health-readine-scenario-3
Every HTTP response from the transport includes an `X-Request-ID` header. When the client includes `X-Request-ID` in the request, the same value is echoed on the response. When no client `X-Request-ID` is present, the server generates one and sets it on the response. Request logs (when a logger is configured) include `request_id`, `route`, `status`, and optionally `session_id` and `code` fields for transport errors, enabling correlation of requests with log entries.

## Failure expectations
The validation must fail if:
- stdio MCP fails to initialize or tool call returns an error against a populated cache.
- `/health` does not return 200 with `{"status":"ok"}`.
- `/ready` does not return the correct readiness state for the cache state.
- SSE endpoint does not emit the session endpoint or fails to deliver JSON-RPC responses.
- `/message` accepts requests with missing or unknown session IDs.
- Concurrent SSE sessions leak responses across clients.
- Closed sessions still accept POST messages.
- Responses lack `X-Request-ID` header.
- Transport errors do not include `X-Request-ID` in the response header.

