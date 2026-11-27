# Instance Capability Policy

Capability policy is an optional, immutable per-process ceiling over the shared
CLI, MCP, private service IPC, background workers, and Admin controls. It is not
an OS sandbox, per-repository content ACL, or multi-user authorization server.
Protect the executable, trusted config, account, files, and service socket using
deployment/OS permissions. A user who controls those can launch another process.

## Trusted configuration

The canonical machine/user file is `gitcode-mcp/config.yaml` in the account's
native config directory: macOS `Library/Application Support`, Linux `.config`,
Windows `AppData/Roaming`, under the account home resolved independently of
runtime config environment overrides. Its dedicated `capabilities` section is
read before credentials, cache, provider, or job construction.

An operator can add a tightening deployment layer with a **leading** launch
flag, before the command:

```sh
gitcode-mcp --deployment-config /operator/instance.yaml --mcp
gitcode-mcp --deployment-config /operator/instance.yaml capabilities
```

Both files are independently validated and intersected; a deployment layer
cannot remove the machine ceiling. `GITCODE_MCP_CONFIG`, `GITCODE_CONFIG`,
repository config, request arguments, and retained jobs cannot select or
replace policy. A `capabilities` section in lower-trust runtime/repository config
is rejected, not merged. There is no environment/header/RPC profile selector or
hot reload. Restart the instance after an operator policy edit.

```yaml
capabilities:
  schema: 1
  profile: research
  ceiling:
    deny: [merge_pr, config_management, service_lifecycle]
  profiles:
    research:
      allow: [repo_status, list_sources, get_source, search_sources, source_backlinks, doctor]
    commenter:
      allow: [repo_status, list_sources, get_source, search_sources, add_issue_comment, doctor]
      deny: [merge_pr]
```

Every ID is a canonical capability from the product registry, not a CLI alias
or arbitrary method prefix. `gitcode-mcp capabilities`, MCP
`capability_policy_status`, and the protected Admin policy card expose sorted
effective decisions, finite reasons, selected profile, authority classes and
fingerprint. They do not expose policy file paths, raw YAML or credentials.
Policy status is an always-readable content-free control-plane exception even
to an empty allowlist. `doctor` remains a separate, policy-gated broader probe;
its structured report includes the same policy view.

Rules and bounds:

- Deny wins. Every layer's ceiling and selected named profile intersect.
- Omitted `allow` means the current universe; `allow: []` means no ordinary
  capability. Omitted/empty `deny` adds no restriction. Null is invalid.
- Explicit allowlists exclude future capabilities. Deny-only rules deliberately
  allow new capabilities unless a later ceiling blocks them.
- All profiles, even unselected ones, must use known IDs. Unknown schema, ID,
  profile, policy field, duplicate key/ID, alias node or wrong type fails closed.
- One UTF-8 YAML document, at most 1 MiB; at most 32 profiles, 512 distinct IDs
  per list; names are 1..128 ASCII letters, digits, underscores or hyphens.
- An explicit coarse `mcp.tools.access: read` (or legacy `mcp_tool_access: read`)
  in a trusted source is an upper bound; later environment `write` cannot relax
  it. A newly discovered conflicting repository read ceiling rejects startup
  rather than changing a serving instance. Existing no-policy deployments keep
  their legacy transport behavior.

## Effects and composite operations

`tools/list` hides denied tools. Calling a known hidden tool directly is rejected
before its arguments, lazy bootstrap, cache, credentials, provider or queue are
used. CLI aliases and grouped verbs map to the same canonical grants. Service
entrypoints and daemon IPC independently authorize operations. Unmapped managed
operations fail closed with `capability_policy_unmapped`; known denied calls use
`capability_policy_denied` (MCP hidden tools retain `tool_disabled_by_policy`).
Errors tell the caller to ask the operator, not to widen a runtime setting.

Composite actions also require their child grants. For example browser search
comparison requires `admin_search_compare` and `search_sources`; binding writes
require `admin_binding_plan_apply` and `repository_binding`; maintenance apply
requires `enable_cache_maintenance`, its plan/status dependencies, and enabled
`sync_live`/`rag_index` children. RAG setup and native installation additionally
require `rag_setup` and `service_lifecycle`. A planning grant is not an apply
grant. Ordinary shared-service dependencies such as identity/status resolution
remain separately authorized; use the decision view to diagnose a narrow role.

The Admin card is read-only: no policy/profile editor. Server-side guards remain
authoritative even for hand-crafted HTTP requests. When the broad operator
snapshot is not granted, Admin returns a content-free, explicitly restricted
snapshot instead of probing caches/credentials or presenting an empty estate.
Policy decisions also disable denied composite actions and job action flags.

## Service hosting, recovery and isolation

`service_host` grants the trusted `service run` host loop, not native lifecycle
or child work. An externally supervised read role can explicitly grant it while
denying `service_lifecycle`; an explicit coarse read ceiling still limits all
effectful grants. Native installation/start/repair requires lifecycle authority.
There is no new multi-service manager: isolate agent roles as separate trusted
launches, runtime directories/sockets, and OS/deployment access. Do not point a
restricted agent at a privileged socket and assume tool discovery protects it.

A managed client verifies policy protocol and fingerprint before daemon calls.
An old/unavailable or differently configured daemon fails closed; there is no
permissive fallback. Native launch definitions retain the deployment config and
expected fingerprint. Missing or changed authority refuses startup/recovery;
repair/observation upgrades preserve those flags. Confirm a new native definition
after an intentional operator policy change; do not delete its marker to bypass
the check. Legacy cache-migration definitions cannot silently drop managed flags.

New and recovered sync/RAG/document-index work is reauthorized against the
daemon's own current policy, never an enrollment or retained runtime snapshot.
Policy-blocked jobs/registrations use finite `capability_policy_denied` evidence.
Fetched sync stages, receipts, caches and Git source truth are not deleted by a
denial. Pending retry intents remain retained rather than re-driving denied
provider work; restore authority and use the original reviewed intent as needed.

This layer does not broaden live write authority: explicit write intent,
idempotency, audit, provider confirmation and credential checks still apply.
