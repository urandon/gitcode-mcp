# Local authority contracts

Status: L1 contracts (#180), L2 tracker domain/store (#181) and the L3 shared
document/Git backend (#182), epic #179. This is not an enabled local project
runtime. Runtime ownership integration, operator recovery,
CLI/MCP workflows, Admin, backup and integrated feature acceptance remain
#183–#186. The ordinary daemon's `authority.v1`
negotiation reports `available: false`, an empty implemented-method list and
`authority_unavailable`. It never starts a daemon or promotes a cache.
The separate trusted coordinator/private-IPC foundation described below is
implemented within #183; this is not complete local-project feature rollout.

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

The ordinary daemon remains contracts-only: `AuthorityV1.Negotiate`,
`AuthorityV1.Read`, `AuthorityV1.Mutate`. Its L1 coordinator implements
negotiation only; direct valid read
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
An internal optional `AsOf` sequence supports durable projection-job intake.
Nil captures current authority; explicit zero selects the empty origin. A
future sequence or a continuation disagreeing with that explicit sequence is
refused before candidate inspection. It is not a public path/provider selector.
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

This provider-free foundation has a dedicated trusted coordinator/private IPC
integration described below, not persistent indexing or semantic retrieval.
Jobs, restricted real embeddings and native launch/policy qualification remain
L4 work. Public CLI/MCP and Admin rollout remain L5/L6; the ordinary daemon stays
contracts-only until its runtime/workflow gates are met.

### Disposable content-identity catalog foundation

The L4 private projection owner consumes an already prepared coordinator writer;
read-only or unprepared stores cannot acquire it. Opening or observing a missing
catalog does not create its namespace, enroll a job, or discover a provider.
This domain foundation is not yet wired into daemon index jobs or advertised
in private IPC/CLI/MCP/Admin. Those owned lifecycle and rollout gates remain.

One separately versioned `canonical-content-v1` catalog binds project UUID,
tracker instance, immutable manifest hash, random epoch, monotonic generation,
build UUID and one authoritative sequence. Sorted rows contain only immutable
citations, per-entity sequence, canonical content digest and finite active,
archived or tombstone state. It contains no title, caller key, actor, coordinate,
issue/comment text, document Markdown or chunk text. Document locators/bytes
remain in canonical SQL/Git; metadata alone cannot authorize a source write or
prove full-text/vector coverage. Source hydration always verifies its citation.

The segmented builder inspects at most 128 candidates per step, retaining only
metadata between steps. Actual canonical pages preserve the original as-of
sequence across intervening writes and sparse/byte-bounded pages. Failed,
cancelled, incomplete or over-capacity candidates cannot replace the prior
complete catalog; there is no silently published corpus prefix. Limits are
4096 rows, 2 MiB encoded final metadata, four preserved stages and 8 MiB staged
bytes. The shared stored-JSON decoder preserves the same closed/duplicate/null
grammar under an explicit finite file bound. Transport decoding remains capped
at its unchanged 512 KiB envelope, not the larger stored-file bound.

Content-equal later revisions can reuse semantic work while publishing their
new immutable citation. Actual edits are stale, new active sources missing,
and tombstoned/archived sources inactive. Comparison counts are scoped to the
supplied bounded canonical page, including explicit scanned/partial semantics;
they are not global corpus counts or claims that vectors exist. Authority
reads/history and real canonical lexical search do not depend on this catalog.

Only explicit complete-builder publication creates the fixed private namespace.
No-follow/nonblocking descriptor admission rejects symlink, hardlink, FIFO,
foreign owner/mode, unknown inventory and oversized files. Retained root,
namespace and final/stage inode/content identities fence replacement. Unique
staging, file/directory barriers, generation CAS and exact final readback precede
success. Pre-rename failure preserves the last complete catalog; post-rename
uncertainty fences only the projection until verified owner reopen. Explicit
writer admission requalifies the existing final file, namespace and parent
durability barriers before exposing a catalog; cancellation or any barrier
failure refuses admission. Missing namespaces remain unprovisioned and ordinary
metadata views do not run those barriers. Publication retries repeat the parent
barrier even if a failed attempt retained the newly created namespace. Reopened
metadata is not a source mutation receipt or an index-job completion proof.
Orphan stages are bounded and preserved, never adopted by recency or used to
reconstruct authority. Explicit corrupt-catalog repair/reset and durable job
reconciliation remain in the index lifecycle cohort, not an implicit read.

### Owned content-catalog index jobs

The coordinator owns a separate bounded index-intent ledger. Opening a missing
ledger does not provision it; only explicit local admission creates its fixed
private namespace. An existing namespace without valid final state is uncertain,
not first boot. Its caller-key/fingerprint hashes, original canonical as-of
sequence, tracker/manifest identity, base catalog digest, epoch/generation and
build UUID survive restart independently of the disposable catalog. No source
text, actor/key bytes, locator paths or provider body enter this ledger.

Content-catalog jobs currently certify metadata only, not semantic vectors.
Their shared coordinator methods are implemented by the dedicated protected
private IPC server. Public CLI/MCP/Admin rollout remains #184/#185, and complete
runtime qualification remains mandatory. Semantic readiness cannot be inferred
from a successful catalog job.

There are at most 128 retained jobs, one active job per project, eight attempts,
a 512 KiB final ledger, four preserved stages and 2 MiB staged bytes. Capacity
does not silently discard caller-key or cancellation evidence. Ordinary
observation never admits, retries or repairs a job. Closed state transitions
are queued, running, cancelling, succeeded, cancelled, failed and interrupted;
cancel/retry requires the exact expected job generation and explicit local
intent. Retry clears active cancellation but retains its original generation.
An inactive job cannot be retried while another job is active: this is a normal
conflict, not damaged metadata. A definitively rejected pre-rename cancellation
cannot stop a running attempt; only acknowledged intent or rename ambiguity
provides cancellation authority for that exact attempt.

A joined worker traverses at most 128 canonical candidates per turn and releases
the project lane between turns. Fixed as-of intake remains unchanged across
intervening writes. Real lock-release epochs, not polling or provider retries,
drive contention waits. Each turn has the coordinator call deadline and an
attempt has a five-minute budget. Restart rebuilds transient partial metadata
from the original selection; it never retargets it to newer source content.
The completed builder also waits for publication-lock release and rechecks
durable cancellation before retry; stale-base CAS is a distinct conflict.

Final completion requires exact catalog bytes, build UUID, namespace, source
sequence, epoch/generation and immutable source binding. A proved publication
wins over late cancellation; an uncertain rename cannot become a fabricated
failure or another publication. Reopen synchronizes file, namespace and parent
before reconciling retained intent. Earlier outcomes are reconciled before newer
jobs can advance the catalog. Failed ledger acknowledgement fences only its
job controls, including a definitive pre-rename terminal-save failure after
the joined catalog effect. Intact canonical reads, receipts and lexical retrieval remain
available. Shutdown joins admitted workers and controls before releasing the
project writer. Explicit corrupt-catalog reset, opaque observation and real
restricted embedding qualification remain L4 work.
Every failed joined-terminal exit, including ledger/proof contention deadline,
sets a nonblocking owner-local uncertainty seal. Subsequent admission/readback
and in-flight ledger acknowledgement honor that seal without taking the held
lock that prevented completion; only qualified owner reopen clears it.

Canonical Store identity is initialized before owner publication and thereafter
only compared during SQL revalidation. Content-free filesystem guards may read
that immutable identity without taking the source SQL mutex; manifest changes
are corruption, never permission to replace the admitted policy at runtime.

### Explicit derived-repair planning foundation (L4 in progress)

The shared coordinator can render a read-only `ProjectionRepairPlan`. This is
an internal service contract, not an advertised CLI/MCP/Admin or private-IPC
action and not a completed repair receipt. Durable apply, original-key replay,
quarantine phase recovery and verified post-effect owner reopen remain required
before that capability is enabled. Planning never creates, repairs or syncs a
missing namespace or initializes the separate index-intent ledger.

The plan binds project UUID, current canonical sequence, immutable source and
physical root identity, retained index-ledger generation/file digest/identity
and one stable bounded catalog inventory. Only its opaque plan digest, fixed
state/action/blocker labels and bounded file/byte counts are returned; corrupt
body, source text, actor/key, filename and coordinates never enter the view.
Missing or valid catalogs report `not_needed` only with a compatible current
owner. Valid replacement bytes cannot clear an existing publication/physical
fence: they require qualified owner reopen, not permission to purge.
A qualified owned corrupt final
or namespace lacking a final catalog can propose `quarantine`, not purge or
index readiness. Its plan explicitly requires joined owner reopen before new
catalog admission. A namespace with unsafe types/links/modes, unknown objects
or excess capacity is refused, not recursively followed or assumed disposable.

Queued/running/cancelling intent and an actual attempt whose lifetime has not
joined produce `blocked/index_active`. Retained ambiguous publication produces
`blocked/index_uncertain`, including after owner reopen; a terminal row is not
proof that uncertain intent can be discarded. Corrupt/uncertain ledger admission
returns its typed fixed error. The durable caller/cancellation/publication
history is not rewritten by a proposal. Canonical reads and lexical retrieval
remain separate from the damaged catalog.

Inspection admits at most five fixed-name owned regular files, each at most
2 MiB, with at most four stages/8 MiB and 10 MiB total. Reads are root-relative,
no-follow and nonblocking; special files are refused before reading. Stable
sorted inventory and exact inode/content identities are rechecked rather than
combining changed physical generations. Timestamp-only metadata does not change
the fingerprint; content, inode, source or ledger changes invalidate a prior
proposal. Confirmation must independently revalidate under owned execution and
preserve bounded quarantine plus durable intent; this planner alone grants no
permission to mutate. CLI/MCP #184, Admin #185 and backup/recovery #186 retain
the complete explicit plan/confirm/degraded/restart UX and evidence gates.

### Protected index control protocol

The dedicated private server also implements EnqueueIndex, IndexJob, IndexJobs,
CancelIndex and RetryIndex over this shared owner. Admission requires project
UUID, exact source sequence, bounded caller key and `write_mode: local`. Its
envelope includes a domain-separated SHA-256 digest of the typed caller request
and the exact metadata-only job view. This digest is response consistency, not
an authentication signature; OS peer authentication is still mandatory. Neither
the original key nor actor/physical selectors enter the view. A completed
`content_catalog` job is not evidence of vector readiness or hybrid search.

IndexJobs pages contain at most 32 UUID-sorted unique views, within 64 KiB.
Missing metadata returns `state: missing`, zero generation/count, an empty
array and no digest/continuation without provisioning a namespace. Ready pages
pin retained ledger generation, encoded snapshot digest, count, offset and last
UUID through a bounded opaque cursor. A changed ledger rejects prior cursors
with typed conflict; pages never mix generations or grant execution. All
protected results, including discarded responses, bind project/job, admission
digest, generation-fenced actions and exact continuation accounting. An active
retry cannot falsely acknowledge an unperformed retry. Unknown jobs have the
finite `authority_index_job_not_found` error; corrupt/uncertain job/catalog
diagnostics are fixed and never reflect peer error prose.
Durable records and protected views share causal generation floors: a start
precedes terminal/cancelling acknowledgement; running cancellation has a joined
terminal transition, while an unstarted queued cancellation remains valid.
Retried queued permission requires prior start, terminal state and explicit
retry. A first-running generation cannot masquerade as published success.

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

### Dedicated coordinator and protected IPC (L4 in progress)

The separate coordinator consumes opaque preparation and at most 16 trusted
UUID/physical-directory bindings. It inspects the entire cohort before opening
writers and refuses loopback/Admin policies in its strict factory. It owns no
ordinary remote manager, provider, credential or Admin dependency. Callers
cannot retarget admitted bindings; missing preparation or stores never trigger
initialization, schema migration or environment/home discovery.

One nonblocking turn per project bounds admitted canonical work. Calls have a
10-second or earlier caller deadline. Shutdown stops new admission, propagates
cancellation, joins actual operations, then closes stores. A bounded Close that
times out does not release still-live ownership or claim clean teardown; the
owned shutdown worker retains its stores until they join. Knowledge readiness
is distinct from tracker readiness: pending/damaged Git fences dependent
document work, not unrelated intact tracker reads/writes. Original document
intent replay remains the recovery boundary, not a new key or silent repair.

The dedicated Unix server is not `RPCServer`/`Manager.Run`. It requires an
existing private owned listener, authenticates actual OS peer credentials on
both sides and admits at most 32 handlers. It serves one bounded request per
connection and joins accepted handlers on shutdown; replacement socket names
are not unlinked. It holds no filesystem/FD selectors from peers and cannot
dispatch remote/service/Admin methods. The ordinary daemon remains unavailable
for authority operations instead of falling through to this graph implicitly.

The dedicated server negotiates implemented Read, Mutate, MutateDocument,
KnowledgeStatus, Search, EnqueueRecovery, RecoveryJob, RecoveryJobs,
CancelRecovery, RetryRecovery, EnqueueIndex, IndexJob, IndexJobs, CancelIndex
and RetryIndex methods. Exact implemented-method inventory
is required before an operation; contracts-only stays unavailable and an old,
incomplete, reordered or unknown inventory is unsupported, never auto-started
or routed to remote state. Concrete responses are validated even when
a client discards them. Shared pure domain verification binds write confirmation
to project, kind, original key, full canonical intent, stable local attribution,
revision and document outcome/ref expectations; another well-formed receipt is
not sufficient. Raw fault/driver/OS bodies and unknown codes never become error
prose. Replies retain the total 512 KiB ceiling, including RPC envelope; protected
results and continuation cursors reserve finite headroom inside that ceiling.
Response DTOs are complete snapshots: the client validates a fresh object and
only then replaces the caller's result. Omitted optional terminal codes,
receipts or cursors cannot survive an older response; invalid replies cannot
partially overwrite prior caller state.

Recovery private-IPC method parameters:

| Method (`AuthorityV1.` prefix) | Closed request fields |
| --- | --- |
| EnqueueRecovery | project_id, operation_id, observed_generation, write_mode |
| RecoveryJob | project_id, job_id |
| RecoveryJobs | project_id, limit, optional cursor |
| CancelRecovery / RetryRecovery | project_id, job_id, expected_generation, write_mode |

Writes require `write_mode=local`; generation and UUID validation precede
transport. No source key, document bytes, actor override, object/path selector
or destination is accepted. The dedicated server supplies fixed asserted
local-user attribution to shared job control without replacing original source
provenance. Get/action replies bind exact requested project/job UUIDs; action
responses cannot regress the observed job generation. Admission distinguishes
new queued intent, retained replay and original already-settled receipt proof.
New queue responses cannot fabricate running history, and original proof must
be a replayed document receipt. Job success retains the non-replayed original
receipt as durable metadata, not a new source operation.
Cancellation replies cannot acknowledge queued/running permission; retry must
advance the observed generation unless it is an already-succeeded no-op.
Historical retry replay may honestly return the later retained outcome. Fresh
queue admission cannot regress the caller's observed journal generation;
retained replay preserves its original observation instead of upgrading it.
Job views enforce generation/attempt consistency and require an actual attempt
for failed state; zero-attempt interrupted/cancelled/proof-success remains valid.

Job pages enforce requested limit plus independent 64 KiB result size, ordered
UUIDs, retained-count bounds and project/generation/last-job-bound continuation.
Resumed pages must advance past the prior UUID and preserve snapshot generation
and digest scope. Every page, including empty/final pages, echoes its validated
`snapshot_digest`, `offset` and `retained` total. Cursor `seen` and `total` counts
must agree with the actual last UUID ordinal and immutable snapshot. Offset plus
returned rows conserves declared coverage: a nonterminal prefix has a next
cursor, a terminal page reaches the retained total, and a resumed response
cannot silently shrink that total. These are consistency checks, not
cryptographic attestation of a peer's authority. Job-not-found,
corrupt/uncertain job metadata and bounded
attempt-limit errors retain fixed typed identities through IPC; arbitrary peer
prose and unknown error codes cannot escape as local result text.
Snapshot generation starts at one with zero jobs. A retained row's generation
must precede the snapshot generation, and retained count cannot exceed prior
snapshot transitions. Persisted records also bind their admission generation
to the number of later job transitions; self-consistent JSON digests cannot
make impossible lifecycle history valid on reopen.

Search accepts `project_id`, `query`, optional `mode`/`kind`/`cursor` and required
1–100 `limit`. An omitted mode resolves from the immutable manifest default;
explicit full_text remains explicit. In strict deny, requested hybrid returns
real full_text with `policy_denied` and `disabled_by_policy`, never fabricated
semantic vectors/coverage. The closed result has a tagged `lexical` object plus
requested/effective/fallback fields. Continuations bind the resolved mode and
canonical project/query/kind/sequence. Kind restriction is applied to bounded
physical candidates before history/Git hydration: issue-only search does not
open damaged unrelated document bytes, while document/full-corpus failure is
not silently hidden. Excluded kinds still count toward physical scan budgets
and advance sparse continuation.

Isolated tests exercise actual private Unix IPC, temporary authoritative SQLite
and Git, real mutations/replay/read/search, damaged-document scope isolation,
untrusted selector rejection, silent-peer joining and receipt tamper controls.
The test-only registry constructor is compiled only in augmented test builds,
not as a production path selector. This is not the native launcher, transferred
capability/restart, foreign-user, zero-network or complete L4 acceptance receipt.
Those qualifications and bounded projection jobs, restricted embedding
profiles and opaque lifecycle observations remain mandatory before #183 closure.

### Fixed-channel native launch (L4 in progress)

Explicit trusted setup can derive a single-use native preparation from the
opaque registry capability. It captures the entire existing cohort: UUID,
instance, canonical directory, retained directory/database identity and
immutable manifest digest. Every full owner record is inspected read-only;
missing/partial state refuses, with no initialization or binding upgrade.
Only the authenticated transient bootstrap frame contains coordinates.

The same-executable launcher transfers exactly three fixed roles: registry
directory, private Unix listener and connected private Unix bootstrap channel.
The early internal child entry has no public path/FD/profile/config selectors.
It audits the bounded native descriptor census and role types/owners before
ordinary configuration, redaction, account/provider/credential construction or
output. Unrelated descriptors, aliased incompatible roles and sockets used as
stdio refuse. Child environment is empty and stdio inert in the launcher.
Darwin inventory/listener inspection uses native process fdinfo; unsupported
Darwin non-cgo admission refuses instead of falling back. Linux uses bounded
self-fd inventory and native socket options. No arbitrary extra kernel object
or unavailable descriptor census is silently accepted.

The child issues a fresh challenge. A closed length-framed message binds it to
protocol, strict profile, fresh launch generation, registry identity and the
complete cohort. Each frame is at most 64 KiB and bootstrap work has a 10-second
or earlier caller budget. Receiver requalifies the entire physical/full cohort
before opening any writer; transferred preparation cannot widen its project
scope or silently follow physical/manifest changes. These checks authenticate
delegation between cooperating local users, not executable/image attestation
or a claim that UID alone proves a canonical registry namespace.

Activation is explicit READY -> ACTIVATE -> ACTIVE. Serving is not permitted
before activation succeeds. Close seals active or activating admission even
with an already expired join budget; a runtime closed while waiting ACTIVATE
cannot acknowledge ACTIVE. Parent drops its copy of the child bootstrap
endpoint immediately after exec so refused/exited children cannot masquerade
as silent peers. Startup failure closes copies, terminates and actually waits
for the child. A single Wait owner retains supervision after a bounded Close
timeout; graceful shutdown is followed by bounded forced termination/join when
needed. Unknown/failed owned shutdown returns a finite nonzero phase, not clean
success. Error text contains fixed phase names only, never OS/parser bodies.

Native fixtures execute the production fixed-role entry in the same-binary
launcher and separately build the actual product offline. They verify real
private control writes, canonical read-only coexistence, single-writer refusal,
fresh restart/exact replay, abrupt process exit and specific malformed/aliased/
unrelated/INET-stdio role refusals. Hostile inherited config/home/proxy/cache
sentinels do not provision stores. These are lifecycle/admission evidence, NOT
complete executable-startup network/credential/descendant observation or real
foreign-user qualification. Calibrated native observation (including package
initialization) and post-transition foreign-user connections remain separate
mandatory gates. No SIP weakening, setup fallback or fake-factory-only proof
may substitute for unavailable qualification. Projection/recovery jobs,
restricted real embeddings, opaque observations and L5/L6/L7 rollout remain
outstanding work, not implied by this backend launch API.

### Exact-journal recovery foundation (L4 in progress)

An internal recovery owner selects one canonical operation by project UUID,
unique operation UUID, original intent hash and observed journal generation.
It validates `1 <= observed <= canonical` and advances from the actual canonical
phase; an older observation cannot downgrade a later binding or receipt. No
caller key, Markdown, locator/path selector or substitute operation is accepted.
This primitive is not itself a public maintenance API. The separate coordinator
job owner below gates durable explicit recovery intent.

A reservation without the exact published operation pin remains
`document_recovery_required`, including an owned lock-only or partial object
collection. Recovery preserves those bytes and requires the original intent;
it cannot reconstruct a candidate from a similar tip/title or manufacture a pin.
With a published pin, it verifies the exact generated operation, expected
parent, single-document tree delta, digest/length and objects, requalifies the
existing pin/object barriers and CAS-binds that original reservation durably.
Settled prior history is independently verified; a tombstone cannot hide a
missing historical blob merely because it is absent from the candidate tree.
Bound completion shares the original caller's owner: expected ref becomes only
the exact candidate, or an already-candidate ref keeps its bytes/inode while
its file/directory barriers are requalified before SQL acknowledgement.

Settled recovery is proof-only: immutable evidence/history plus original receipt
replay, with no Git publication, object rewrite, pin cleanup or SQL mutation.
It cannot acknowledge an uncertain SQL publication fence; verified owner reopen
or the original retry path must requalify that durability first. Pending documents
continue to fence dependent document writes without disabling an intact tracker.

Admission is nonblocking and bounded to one selected operation with a ten-second
or earlier caller budget. Evidence/blob, namespace/object and history traversals
check cancellation between finite reads and before publication/transitions.
Individual object sizes remain fixed; OS persistence barriers are not hard
real-time and writer ownership is retained until the actual operation joins.
Deterministic fault/reopen and abrupt-exit controls distinguish unprepared,
pinned, bound, candidate-ref and settled states, including late cancellation,
monotonic retry, foreign/missing evidence and no-write settled replay. These do
not implement the still-required persistent index lifecycle, restricted real
embedding profile, opaque lifecycle observation or complete native qualification.

### Durable selected recovery jobs (L4 in progress)

The prepared coordinator owns a private metadata-only job namespace, separate
from canonical tracker/document state. The job identity is the original
operation UUID; admission accepts only project/operation UUIDs, an observed
journal generation and explicit `write_mode=local`. It accepts no document
bytes, source key, path, Git object selector or destination. Retained admission
replay never upgrades original scope or resumes a cancelled/failed operation.
An already-settled target without a retained job returns the original receipt
as proof-only replay, not fabricated job history.

At most 128 records, one active job per project and eight actual attempts per
job are retained. Snapshot size is capped at 512 KiB; at most 32 unpublished
stages and 16 MiB of stage evidence are preserved. Only proved successful
records may be retired. Read pages independently cap results at 100 records
and 64 KiB, with project/generation/digest-bound cursors rejecting stale state.
Protected job responses contain fixed lifecycle codes, generations, timestamps
and the original receipt, not actor text or source bytes.

Publication validates immutable project/tracker/manifest binding and exact
snapshot generation, file content and inode. Root-relative no-follow opens
verify private ownership, regular-file type and single link; existing metadata
opens are nonblocking so a FIFO replacement cannot strand coordinator startup.
Exclusive stages become canonical only after file/directory barriers and final
rename/readback. A post-rename ambiguous acknowledgement fences this job queue
until verified owner reopen; it does not make an intact tracker unavailable.
After an actual source call joins, failure to publish terminal job state also
fences the queue, even before metadata rename. It cannot advertise a running
attempt whose worker has exited. Owner reopen proves the canonical source
receipt before updating metadata; it does not repeat a settled source effect.
An existing namespace lacking final state is preserved as uncertain, never
silently reinitialized. Interrupted initial namespace setup therefore needs an
explicit operator-recovery decision in the #186 cohort.

Cancellation and retry use explicit job-generation CAS. Cancellation metadata
control does not take the worker-held project effect turn. Definitive failure
before final publication does not cancel an authorized source effect; an
acknowledged or rename-uncertain cancellation stops only its exact attempt
generation. Cancel is not rollback: the actual source call joins before fresh
canonical proof determines success, cancelled pending state or uncertainty.
Cancelled/failed/interrupted records can become succeeded through original
receipt proof without inventing another attempt if the original caller later
settles that operation. Only explicit external document writes coalesce this
proof reconciliation; reads and the worker's own release do not self-wake.

Workers count attempts only after obtaining the project effect turn and
durably marking running. Contention waits on the actual lock-release epoch,
not retries or polling. Owner restart may resume queued/running intent, never
source effects from cancelling/cancelled state. A fresh pre-effect uncertainty
can mark queued intent interrupted without inventing an attempt. Shutdown
fences new admission, cancels and joins workers/active calls, then releases
metadata descriptors, stores and UUID ownership. Native launch activates these
workers only after its fixed-channel ACTIVE acknowledgement and final checks.

These shared coordinator operations are now exposed through the dedicated
protected private IPC, not public CLI/MCP/Admin controls. CLI/MCP #184 and Admin
#185 remain explicit rollout dependencies. Backup and
restore #186 must preserve source journal/pins/objects and durable job intent,
including cancellations; uncertain cancellation history cannot be reset by
restoring an older metadata snapshot. The persistent search/index lifecycle,
restricted embedding profile, opaque observation and native qualification are
still required before #183 is complete.
