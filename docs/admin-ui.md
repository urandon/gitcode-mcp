# Embedded admin UI

The admin UI is a Svelte 5/SvelteKit static SPA compiled into `internal/adminui/assets` and embedded in the Go binary. A source install (`go install ./...`) does not require Node.js because the reviewed production assets are checked in. Frontend contributors use `scripts/build-admin-ui.sh`; CI uses `scripts/check-admin-ui-assets.sh` to reject stale generated assets.

## Runtime ownership

The existing `gitcode-mcp service run` process owns both the local JSON-RPC control socket and the optional admin HTTP listener. `gitcode-mcp admin open` connects to that daemon; it never starts a second coordinator or opens the cache independently.

```sh
gitcode-mcp service run
gitcode-mcp admin open
gitcode-mcp admin status --format json
```

`admin open` starts the listener lazily on `127.0.0.1` with a dynamic port. For foreground debugging, `service run --admin` starts it eagerly. `--admin-bind 127.0.0.1:9000` selects a stable loopback address. Non-loopback binds are rejected unless the operator also supplies the conspicuous `--admin-unsafe-allow-non-loopback` override; this override is intended only for controlled development environments and does not add TLS or remote-user authentication.

The listener is disabled at daemon startup by default. Its optional runtime configuration is deliberately explicit:

| `service run` option | Default | Effect |
| --- | --- | --- |
| `--admin` | off | Start eagerly instead of waiting for `admin open`. |
| `--admin-bind` | `127.0.0.1:0` | Select loopback host and port; port `0` asks the OS for a free port. |
| `--admin-unsafe-allow-non-loopback` | off | Permit a non-loopback bind without adding remote security. |

To keep the listener disabled, omit `--admin` and do not invoke `admin open`. A lazily or eagerly started listener stops with its owning daemon and is not persisted as a second service; restart the daemon without `--admin` to return to the disabled state.

Use `admin open --no-browser` when a browser must be opened manually. The printed URL contains a one-time token in its fragment. The SPA removes the fragment from browser history before exchanging it for a bounded SameSite Strict, HttpOnly session cookie. The launch token expires after one minute and cannot be replayed.

## Browser boundary

The admin listener applies these controls:

- loopback bind and Host validation by default;
- Origin and Fetch Metadata validation for mutations;
- a per-session CSRF token for unsafe requests;
- a generated hash-based Content Security Policy for the SvelteKit bootstrap, self-only resources, no CORS, no framing, and no referrer propagation;
- immutable caching for hashed SvelteKit assets, explicit no-store revalidation for `index.html`, and SPA fallback routing;
- readiness payloads that contain local operational state but never credentials, launch tokens, session cookies, or CSRF material.

## Themes

The visible selector has exactly three choices: **Light**, **Dark**, and **System**. **System is the default** when no preference is saved or an invalid value is found. It follows `prefers-color-scheme`; explicit Light and Dark choices are stored only in browser-local storage. A small external head script applies the choice before the application starts, avoiding a first-paint theme flash while keeping the CSP free of inline script allowances.
