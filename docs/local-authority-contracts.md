# Local authority contracts

Status: L1 contracts (#180), L2 tracker domain/store (#181) and the L3 shared
document/Git backend (#182), epic #179. This is not an enabled local project
runtime. Runtime ownership integration, operator recovery,
CLI/MCP workflows, Admin, backup and integrated feature acceptance remain
#183–#186. Current `authority.v1`
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
aliases; the L2 authority store allocates and preserves them transactionally.
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

The L1 `Preflight` validator requires private IPC, matching Admin/provider
capabilities and loopback-only TCP when requested. All profiles forbid UDP/DNS,
external-network factories, remote cohosting, credential resolution and provider
setup. Deny profiles have no embedding factory; embedding profiles require a
pre-provisioned capability. An installed binary alone is not that runtime
capability. Preflight does not itself disable a process's sockets or certify a
runtime. L4/L7 must qualify
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
not mean an omitted typed scalar. Every field without `omitempty` is required,
including explicit empty body/description/due-date and false archive/replay flags.
Typed result decoding also validates UUID/citation namespaces, positive sequence,
display alias/kind, concrete payload shape, local commit evidence/digests and
projection state; a syntactically decoded empty object is not a valid receipt.
Store adapters still enforce lifecycle,
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
changed intent conflicts. L1 creates no confirmed receipt. L2 confirms tracker
transactions only after FULL SQLite and publication barriers; L3 must additionally
qualify ordered exact Git intent/object/pin/binding/ref barriers for documents.
Client cancellation never proves rollback.

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

## Durable tracker layer (L2)

`internal/localauthority` is a shared, separately versioned authoritative store,
not an adapter over `internal/cache`. Its dedicated database application/schema
identity, manifest UUID and random store-instance UUID are checked before writer
admission. Explicit initialization never replaces an existing database. Existing
open never initializes, promotes a cache or silently migrates. Supported database
versions are 1 and 2: existing v1 preserves its exact original catalog and tracker
operations; fresh explicit initialization selects v2's dedicated knowledge
metadata/journal catalog. Manifest, document and transport schema versions are
independent of the database version. Unknown or foreign/damaged schemas fail
closed; bytes and identity are preserved for the
CLI-only migration/backup/recovery cohort (#186).

The v2 catalog alone does not initialize Git or enable document workflows. Its
populated setup/journal admission and integrity are explicit shared service
operations; current negotiation remains contracts-only. There is no automatic
v1-to-v2 migration, and the existing cache schema is unrelated to this store.

The machine registry is global to the current OS user across every runtime
profile, not configurable per project/daemon. Kernel leases use project UUID.
The durable private binding includes the canonical storage coordinate hash,
store instance and physical database identity. A second process, clone, or
same-path file replacement cannot acquire a writer. Closing a lease does not
truncate/unlink its binding. Relocation/restore requires the explicit verified
rebind contract in #186. There is no distributed multi-host lease promise.
Initialization and runtime policy/cohosting admission remain trusted CLI and
coordinator responsibilities (#183/#184); this library alone does not enroll a
project or start a runtime.

Initialization holds a non-published project lease while preparing a uniquely
named private empty store. Schema/manifest/instance validation, checkpoint,
close and store/directory barriers precede a bounded pending-init journal.
The journal records only version/UUIDs and manifest/directory/physical/content
digests. Exclusive final-name publication never overwrites an existing file;
known temporary aliases are removed only after identical physical/content
validation and durable final-name preservation. Final ownership is bound and
synced only to that verified store. Ordinary writer admission returns
`authority_initialization_pending` until explicit same-manifest retry settles
the publication. Retry preserves a journaled instance, including lost final
acknowledgement, and never promotes an unknown store. Unknown/orphan staging
files remain untouched; staging allocation and directory scanning are bounded.
The broader orphan/rebind/backup recovery workflow remains #186. Runtime #183
and CLI/MCP/Admin #184/#185 must expose this finite remediation instead of
silently guessing an identity or hiding the pending fence.

Writer qualification requires a private owned regular database, WAL,
`synchronous=FULL`, foreign keys and the configured full-sync pragmas on every
connection, plus checked file/directory persistence primitives. The initial
qualified filesystem set is local APFS/HFS on Darwin and ext-family/XFS/Btrfs on
Linux. Other filesystems/platforms refuse writes; there is no weaker fallback.
Darwin file barriers use `F_FULLFSYNC`. SQLite's FULL commit is followed by
database/WAL file and directory publication barriers before acknowledgement.
This assumes the filesystem/hardware honors OS barriers; process fault tests
are not physical power-loss certification. The broader platform, Git barrier,
restore and power-loss models remain explicit integrated gates in #182/#186.
See [SQLite synchronous semantics](https://www.sqlite.org/pragma.html#pragma_synchronous)
and [WAL's same-host/local-filesystem boundary](https://www.sqlite.org/wal.html).

One transaction settles a generated entity UUID, immutable revision, never-reused
display alias, current typed relationship, receipt, content-free audit and project
sequence/generation. Actor identity is asserted local metadata, not remote
authentication. Protected revision metadata carries actor/time; audit carries
only project-scoped hashes and fixed operation/opaque identity fields. Caller
keys are never persisted as text. Canonical typed payloads remove JSON field
order/whitespace from intent identity; actor and expected revision remain part
of the fingerprint. Same-key/same-intent replay preserves the original outcome,
even after later edits/restart; changed intent conflicts. Cancellation or lost
acknowledgement means retain the original caller key, never invent a new one.

Publication failure fences new writes as `recovery_required`/`quiesced`; the
kernel lease is still held. Same-key confirmed replay or verified reopen can
requalify persistence without another entity mutation. The original receipt is
immutable and projection state stays `pending`; actual indexed generation and
coverage are separate L4 observations. Store status reports manifest policy,
not process/network isolation certification.

Issues close/reopen; comments retain their exact original issue citation;
labels/milestones archive; links create/update/tombstone without erasing history.
All referenced immutable revisions must exist and current targets must be live.
Label assignments are issue-to-label and unique; milestone membership is singular
per issue. `evidence_for` is comment/document-to-issue; `blocks` is issue-to-issue
and rejects cycles under a finite traversal budget. At most 4096 active `blocks`
edges are admitted, so supported mutations cannot build a graph whose restart
validation exceeds the same bound. A coherently oversized external graph returns
`authority_capacity`, not silent trimming or an acyclicity guess. `relates_to` connects distinct
non-link entities. Tombstoned links cannot be resurrected; new relationships get
new UUIDs/aliases. No irreversible purge/reset API exists. The generic tracker
mutation envelope still rejects document writes: document admission uses the
separate exact-ref contract below, never Markdown persisted in SQL.

## Owned Git knowledge layer (L3)

`SetupKnowledge` is an explicit trusted service operation, not a side effect of
read, open, document mutation or runtime enrollment. Its original caller key
reserves a knowledge-instance UUID and binds project/tracker identity, canonical
coordinate digest, physical directory identity and exact SHA256 Git config.
Uninitialized v2 reports setup-required; v1 reports migration-required without
creating Git or changing its catalog. A known bound partial setup resumes only
its original identity. A directory published before durable physical binding
remains initialization-pending and requires the operator-recovery cohort; its
shape/name is not proof authorizing adoption. Unknown directories are preserved.

The private bare store owns a fixed layout, loose SHA256 objects and a single
controlled history ref. It never runs Git subprocesses/helpers, loads inherited
Git configuration, follows alternates, fetches, invokes hooks/filters, accesses
credentials or changes a worktree. Symlinks, hard links, foreign/packed state,
unsupported filesystems and same-path physical replacements refuse document
writes. Native Git is used only as an offline test oracle.

`MutateDocument` accepts strict document contract schema1: required project UUID,
`write_mode: local`, original key, operation/payload and exact lowercase64hex
`expected_ref_oid`. All-zero64 means explicit unborn ref, not an inferred CAS.
Create omits entity/revision; update/tombstone require existing UUID and positive
revision. Live payload replaces title and exact validated UTF8 Markdown;
tombstone payload is exactly `{}`. Legacy generic requests do not acquire these
effects, and the contracts-only transport still advertises no implementation.

New-key ref/revision/lifecycle/capacity checks precede any reservation, alias,
object or pin. An existing original key instead takes precedence over today's
CAS: exact intent resumes/replays the original identity; changed intent conflicts.
A different pending operation owns the lane and blocks new document admission,
not unrelated tracker writes. Planned OIDs and content-free canonical metadata
are durably reserved with the generated operation/entity UUID and display alias.
Pending creates count toward never-reused alias integrity. Only actual object
and deterministic pin barriers permit the subsequent SQL binding phase; only
that exact binding permits ref CAS/publication and FULL receipt settlement.

Algorithm1 constructs canonical Git objects from exact bytes, lexically sorted
UUID-derived document paths and the exact reserved parent. Commits have a fixed
synthetic identity, epoch timestamp and operation-UUID-only message. Caller keys,
actors, titles, prompts and filesystem coordinates are not commit metadata.
Paired final/lock refs are refused by ordinary primitives. Original-key recovery
may account for a pair only with immutable SQL operation identity, exact candidate
objects and original pin/ref proof. Equal OIDs alone never clear the fence.

SQLite persists canonical titles, locators, digests/lengths, CAS fields, journal
phases/generations, attribution, immutable receipts and content-free audit only.
Document Markdown/chunks are never stored in the database or WAL. Protected
get/list/history hydrates the exact saved commit/blob, not current HEAD; intact
old revisions remain readable during a verified original pending update.
The distinct result contains the unchanged generic receipt, ordered OS barrier
capability and either a live exact locator or tombstone evidence, never both.
Replay after subsequent edits/restart returns that original immutable result.

Limits are 4096 active documents, 64 retained operation pins, 65536 loose objects,
256 KiB per Markdown blob, bounded canonical trees/commits, and the existing
512 KiB encoded response/request limits. New admission refuses before effects;
no history, pending operation or pin is evicted to make room. Explicit
`CleanupDocumentPin` handles one original settled operation: it proves controlled
ancestry protects the candidate, requalifies barriers, removes only its pin and
durably records cleanup. Interrupted cleanup resumes the same key; retained or
partially cleaned pin status is separate from the immutable receipt. It does not
delete document objects/history or alter project sequence.

`KnowledgeStatus` observes setup readiness, the exact verified current ref OID
for a deliberate new intent, and an optional opaque pending operation. It never
refreshes CAS inside a submitted write. `DocumentOperationStatus` reports finite original phase/pin/remediation
without key text or coordinates. Git-only corruption fences documents while
intact tracker data remains available. SQL logical corruption fences the global
writer and cannot be cleared by replaying an unrelated receipt. SQL barrier
uncertainty retains the original key until verified reopen/requalification;
cancellation never proves rollback. Fixed errors distinguish ordinary stale CAS,
setup/migration, pending lane, original recovery, corruption, capacity and
durability uncertainty. Backup/rebind/migration/operator recovery and exposed
CLI/MCP/Admin workflows remain the downstream cohorts, not implicit repair.

Ordered OS barriers assume the qualified filesystem/hardware honors them.
Abrupt-process-exit fixtures exercise each admission/object/pin/binding/ref/SQL
boundary for create, update and tombstone. They do not certify physical power-loss behavior
or replace the exact-SHA independent review and head/resulting-main CI gates.

Canonical get/list/history/receipt/metadata reads use the authority directly.
List/history cursors fence project, kind, operation, entity and sequence snapshot;
count and encoded-byte limits preserve continuation instead of dropping trailing
entities. Inspection validates one consistent read snapshot, not several samples
mixed across concurrent commits. Projection reset/corruption cannot delete or
block canonical entity history. Admission checks exact schema/constraint/trigger
guards, contiguous sequence/revision/alias domains, canonical revision/receipt
values, recomputed canonical operation/payload/actor/CAS intent fingerprints,
audit-to-receipt/revision/actor/time bindings and active relation parity
with current non-tombstoned links in that same bounded snapshot. Historical
citations must resolve to an earlier live exact revision, with a live target at
the relationship's original sequence; later archive does not invalidate history.
Every comment revision retains its original issue citation, and the existing
active `blocks` graph is bounded and acyclic. Audit fields
are bounded and validated before returning them. Structurally valid but
logically inconsistent SQLite is not writable authority; no implicit repair
or projection-based reconstruction is attempted. Initial manifests are immutable intake; future
generation-fenced transport/search policy changes must be separately recorded,
not an arbitrary manifest overwrite.

Backend fixed failures additionally distinguish `authority_read_only`,
`authority_corrupt`, `authority_durability_unavailable`, `authority_capacity`,
`authority_storage_unavailable`, `authority_commit_uncertain` and
`authority_initialization_pending`. Driver errors,
private coordinates and payloads are never reflected. These are not newly
advertised MCP tools: #183/#184 own runtime admission, negotiated error/capability
compatibility and workflow rollout; #185 owns protected actor/history/conflict/
degraded UI; #186 owns verified backups, identity-preserving rebind and migrations.

## Bounded canonical search foundation (L4 in progress)

The internal `Store.Snapshot` operation captures all entity kinds at one
committed project sequence. Its strict continuation retains that sequence
across intervening writes and carries only project/sequence/entity selectors.
Each page first enumerates at most 100 physical entity candidates plus one
continuation lookahead, before filtering to the historical sequence. As-of
revision lookup uses at most 63 primary-key point probes per candidate over the
contiguous signed-64-bit revision domain, never a retained-history scan. An
empty historical slice still advances its cursor. `scanned` counts candidate
inspections, including excluded newer entities and byte-budget tail candidates
which a later page may inspect again; it is not a distinct source/coverage count.
The 512 KiB encoded contract ceiling preserves trailing entities through
continuation. Documents are hydrated
from verified immutable Git locators only as transient protected data. No
projection table, document-text copy, writer admission or repair is created.
Tombstones remain in the snapshot for derived invalidation. Per-entity content
digests exclude timestamps, actor metadata and revision/sequence: a new citation
can reuse identical content while actual edits change its digest.

`Store.LexicalSearch` performs deterministic case-normalized whole-token AND
matching against typed content, not JSON field names. It accepts 1–64 distinct
query terms within 4096 UTF-8 bytes, inspects at most 128 candidates per call and
returns at most 100 citation/digest/score hits with 512-byte UTF-8 snippets.
Ranking is top-K within the reported slice, not a claim of global corpus
ranking. Incomplete corpus coverage is explicitly partial with a query/kind/
project/sequence-fenced continuation; later calls do not search a newer revision
by accident. Tombstoned documents/links and archived labels/milestones are
excluded from active matches without deleting historical citations.

This is the provider-free canonical lexical foundation, not enabled runtime
negotiation, persistent indexing or semantic retrieval. Coordinator admission,
jobs, hybrid fallback, restricted real embeddings and native policy
qualification remain L4 work. Public CLI/MCP and Admin rollout remain L5/L6;
contracts-only negotiation is unchanged until their runtime gates are met.

### Existing-only runtime ownership preparation

Trusted explicit setup may prepare an opaque, non-serialized registry
capability. Runtime admission consumes that capability without account lookup,
environment discovery, directory creation, owner-record creation, partial
binding upgrade or schema initialization/migration. It requires the existing
full project/instance/coordinate/database binding; missing or partial owner
state refuses instead of creating a second ownership namespace.

Preparation retains the private registry-directory identity. An admitted writer
also retains an independent exclusive kernel lease on its authority directory,
not on the SQLite database (database flock interferes with SQLite locking on
Darwin). Admission and publication validate registry, authority-directory and
database identities. Replacing the authority directory around the same database
inode therefore cannot bypass the guard. Global UUID owner-record locking and
clone refusal remain mandatory. Protected canonical read-only inspection can
coexist with the runtime writer; it acquires no writer lease.

The capability cannot close while stores own it. Coordinators must cancel and
join jobs, close stores, then release preparation; abrupt process exit releases
both kernel leases and retains confirmed authority state for existing-only
restart. Contended preparation admission refuses nonblocking with typed
conflict rather than waiting past cancellation behind another integrity scan;
mandatory lifetime release bookkeeping is still joined, not discarded.
This internal foundation does not supply a serialized FD assertion,
public path selector, daemon launcher, IPC authentication or no-network proof.
Those runtime integration gates remain outstanding in L4.

Only explicit setup resolves the machine registry account. Linux uses bounded
root-owned local account metadata with no NSS or environment fallback; missing,
duplicate or invalid account data refuses. Qualified Darwin builds use native
account lookup; Darwin `osusergo` and unsupported writer platforms fail closed
instead of falling back to `HOME`. Strict runtime never invokes that resolver.
