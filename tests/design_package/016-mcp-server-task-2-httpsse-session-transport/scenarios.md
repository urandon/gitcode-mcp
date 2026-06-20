# 016 MCP Server Task 2 HTTPSSE Session Transport Scenarios

## 016-mcp-server-task-2-httpsse-session-transport-scenario-1

A developer starts the MCP runtime with `gitcode-mcp mcp serve --transport http-sse --bind 127.0.0.1:<port>` against a local fixture cache. Validation covers the product CLI routing by invoking the entrypoint parser and requiring the `http-sse` transport and explicit localhost bind to reach the MCP serve startup path.

## 016-mcp-server-task-2-httpsse-session-transport-scenario-2

With the HTTP/SSE transport serving the same cache-first MCP runtime, `GET /health` and `GET /ready` return successful status and `X-Request-ID` correlation. `GET /sse` creates an opaque session, emits a valid SSE `endpoint` event containing `/message?session_id=...`, and keeps the stream open. `POST /message` for that live session accepts exactly one JSON-RPC request; `initialize` and `tools/call` responses are delivered as SSE `message` events on the matching stream rather than as ordinary HTTP response bodies.

## 016-mcp-server-task-2-httpsse-session-transport-scenario-3

The HTTP/SSE transport rejects missing, unknown, and closed sessions with typed transport errors and correlation headers. Two simultaneous HTTP/SSE clients issue cache read tool calls against the same fixture cache and each receives only its own JSON-RPC response. The retained compatibility path `gitcode-mcp mcp serve --transport stdio` still supports one local JSON-RPC client for `initialize` and read tool calls.
