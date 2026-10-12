# Local authority contracts

Status: L1 contracts for #180 / epic #179. This is not an enabled local project
runtime. Storage, single-writer ownership, recovery, CLI/MCP workflows, Admin,
backup and feature acceptance remain #181–#186. Current `authority.v1`
negotiation reports `available: false`, an empty implemented-method list and
`authority_unavailable`. It never starts a daemon or promotes a cache.

## Authority and identity

Existing configurations remain remote-authoritative and cache-first. Local
authority is explicit, immutable after initialization, and separate from cache
placement and GitCode provider endpoints. There is no remote/local fallback.
A project manifest supplied to a legacy runtime configuration loader is
rejected before cache/provider effects, including JSON, YAML and YAML merges.
Unknown unrelated legacy settings retain their existing compatibility behavior.

Project and entity identities are canonical lowercase random version-4 UUIDs.
The human project slug is not identity. `ISSUE-N`, `COMMENT-N`, `LABEL-N`,
`MILESTONE-N`, `DOC-N` and `LINK-N` are transactional, never-reused display
aliases; allocation/immutability is the authoritative store's L2 responsibility.
Citations bind project UUID, entity UUID, kind and immutable positive revision.
UTC timestamps are metadata, not CAS ordering. Project sequence is authoritative.

Local tracker bytes belong to a separately versioned authority store, not the
cache. Document bytes belong only to Git; SQLite/projections retain locators,
digests and vectors. Protected read/write payloads may carry transient document
text; audit, diagnostic and projection metadata must not copy it.

## Separate intent files

The following project intent is understood by the shared strict L1 parser, not
the legacy `config.yaml` loader. Unknown/duplicate keys, multiple documents,
anchors/aliases, mixed cache/endpoints/credentials/sync fields, invalid bounds
or unsupported schema fail without echoing scalar content.

```yaml
project:
  schema: 1
  slug: research-example
  authority: local_authoritative
  entities: [issue, comment, label, milestone, document, link]
  network:
    data_egress: deny
    control_transport: ipc_only
  search:
    default: hybrid
    fallback: full_text
```

All six initial entity kinds are required exactly once. `id` is absent only in
an initialization intent; an explicit CLI plan generates it once. Confirmed
manifests must retain that UUID. No environment/cache-path/global default may
substitute an identity or change authority. A supplied ID must already be a
canonical v4 UUID; future restore is separately identity/integrity-gated.

Machine-local registration intent is separate:

```yaml
storage_ref: workstation-store
writer: coordinator
backup:
  retain: 7
  max_bytes: 1073741824
indexing:
  interval_seconds: 900
  max_chunks: 128
  embedding_profile: disabled
```

`storage_ref` is an opaque name, never a path. Only explicit CLI setup may
resolve it through trusted private machine registration. Plan output contains
UUID, opaque ref, intent digest, network label and `planned_not_enrolled`.
Planning alone performs no filesystem/network work and grants no authority.
Path resolution, confirmation/CAS and durable initialization are rollout-gated
by the workflow/runtime cohorts. Admin/MCP cannot create storage registrations.

Bounds: slug/ref 1–64 lowercase alphanumeric/hyphen characters starting with a
letter; backups retain 1–128, 1 MiB–1 TiB; indexing interval 60–86400 seconds,
1–4096 chunks. Deny data egress requires `embedding_profile: disabled`.
Loopback embeddings require an explicit named pre-provisioned profile, never
install/download/autostart. Changing an existing authority or relaxing policy
requires its separately generation-fenced CLI plan; no per-call override.

## Data egress and control transport

| Data egress | Control | Truthful effective label |
| --- | --- | --- |
| deny | ipc_only | zero_network |
| deny | loopback_admin | loopback_control_only |
| loopback_embeddings | ipc_only or loopback_admin | loopback_provider_network |

Strict zero-network means deny **plus** ipc_only. Enrollment requires a strict
runtime with no TCP/UDP/DNS/Admin, remote projects or loopback-enabled cohosting.
An incompatible coordinator refuses before writable opening, credentials,
provider construction or work admission. UUID writer ownership remains global
across runtime profiles. A strict project never starts another daemon implicitly.
Local authority control requests use private Unix IPC (or future qualified
named pipes), not TCP even when separate loopback UI/embedding is permitted.

The L1 `Preflight` validator describes these admission requirements. It does
not itself disable a process's sockets or certify a runtime. L4/L7 must qualify
policy-disabled factories and isolated black-box startup/operation tests,
including inherited proxies, providers, tokens and DNS. Windows private-pipe
implementation/qualification is not implied by a Unix fixture or `mem` test.
An explicitly loopback-enabled process is never described as zero-network.

## Private protocol and schemas

Methods: `AuthorityV1.Negotiate`, `AuthorityV1.Read`, `AuthorityV1.Mutate`.
The production L1 coordinator implements negotiation only; direct valid read
or mutation requests return `authority_not_found`, without remote dispatch.
Clients negotiate first and return `authority_unavailable` without sending an
entity operation when the build reports no implemented authority methods.
Missing/old daemons never trigger credentials, network setup or auto-start.

Negotiation request:

```json
{"protocol":"authority.v1","authority":"local_authoritative","schema_min":1,"schema_max":1}
```

Negotiation is bounded and fail-closed. Unknown protocol/schema ranges, methods,
response codes, mismatched IDs, duplicate/unknown/case-aliased fields and
malformed envelopes are refused with fixed errors. Control requests are at
most 512 KiB including envelope, replies 512 KiB, call budget at most 10 seconds
or the earlier caller deadline. Cancellation closes and joins its own socket
callback. There is no write retry. `mem` is a deterministic fixture transport,
not an end-user authority. TCP control is refused before dialing.

Read DTO: `project_id`, `operation` (status/list/get/history), optional `kind`,
`entity_id`, `revision`, `limit` (0/default or 1–100), bounded opaque `cursor`.
Status takes no entity/pagination fields. List takes kind; get/history take kind
and entity UUID. Revision selects immutable history, never a timestamp.
Pages must fit the encoded response-byte bound, not merely the entity-count
bound; return the durable continuation cursor rather than dropping oversized
trailing entities. Result DTOs are `ProjectStatus`, `ReadResult`, `EntityView`, `Citation` and
`DocumentLocator` in `internal/authority`. Authority reads hydrate committed
state even while disposable search/index generations are stale.

Mutation DTO: `intent`, `operation`, `payload`. Intent requires project UUID,
`write_mode: local`, a nonempty bounded caller key (1–128 UTF-8 bytes, no outer
whitespace/control characters). Create forbids caller-chosen entity UUID or
revision; updates/tombstones require entity UUID and positive expected revision.
Payloads are full replacements, not ambiguous partial patches. JSON null does
not mean an omitted typed scalar. Store adapters still enforce lifecycle,
relationship integrity and immutable history before any transaction.

| Entity | Typed payload fields | Lifecycle |
| --- | --- | --- |
| Issue | title, body, state=open/closed | create; revision-fenced update/close/reopen |
| Comment | issue citation, body | create; revision-fenced edit |
| Label | name, color=#RRGGBB, archived | create; revision-fenced edit/archive |
| Milestone | title, description, due_date=YYYY-MM-DD or empty, archived | create; revision-fenced edit/archive |
| Document | title, markdown | Git-backed create/update; fenced tombstone with empty object |
| Link | from/to project-scoped citations, relation | create/update; fenced tombstone with empty object |

Links support relates_to, blocks, evidence_for, label and milestone relations;
label assignment and milestone membership are typed relationships, not provider
IDs. Title bounds are 256 bytes (label name 128), tracker text 128 KiB, document
Markdown 256 KiB, additionally subject to the encoded request-byte bound.
Reference type/existence, cycles and target lifecycle are L2
authority checks, not proof from syntactically valid input.

Receipt: project UUID, generated operation UUID, exact citation, project sequence,
hashed caller key/intent, `evidence: local_commit_confirmed`, replay flag and
projection state. Same-key/same-intent returns the durable original receipt;
changed intent conflicts. L1 creates no confirmed receipt. L2/L3 must qualify
FULL SQLite acknowledgement and ordered exact Git intent/object/pin/binding/ref
barriers before producing success. Client cancellation never proves rollback.

Fixed errors include authority_invalid_contract, authority_unsupported,
authority_unavailable, authority_not_found, authority_policy_denied,
authority_conflict, authority_revision_conflict and authority_initialization_required.
No raw caller key, private path, payload, peer error or provider body is reflected.

## Transport names and Admin handoff

Final mutation names are declared by `authority.OperationContracts()`, not yet
advertised in MCP discovery. CLI uses `project <kind> create/update`; MCP uses
project_create_issue/project_update_issue, project_add_comment/project_update_comment,
project_create_label/project_update_label, project_create_milestone/project_update_milestone,
project_create_document/project_update_document/project_tombstone_document,
project_create_link/project_update_link/project_tombstone_link.
Read workflows are `project status/list/get/history` and
project_status/project_list_entities/project_get_entity/project_history.
Explicit CLI-only initialization is `project init plan` then exact plan confirm;
these names are L5 implementation contracts, not currently runnable commands.

L6 protected Admin examples use opaque UUID selectors and these same typed
payloads, revision-conflict state and local intent. Public diagnostics show
fixed lifecycle/readiness/authority/network labels, not content or machine paths.
Under strict policy Admin is unavailable with an explicit CLI private-IPC
handoff; a loopback browser test cannot fulfill strict-mode acceptance. Explicit
loopback control supplies protected local entity/history/document views and
plan/confirm actions. Storage/init/restore/migration remain CLI-only. #185 owns
the complete UI cohort; #186 gates integrated recovery and release.
