# Legacy Business Migration and Compatibility

This document records the legacy business-record migration and compatibility
decisions resolved in
[GitHub issue 17](https://github.com/Vt00ls/SlideSmith/issues/17).
[CONTEXT.md](../../CONTEXT.md) is authoritative for domain language,
[ADR 0016](../adr/0016-hard-cut-over-legacy-execution-state.md) fixes the hard
cutover,
[ADR 0028](../adr/0028-promote-only-verifiable-legacy-business-facts.md)
records the durable migration choice, and the
[Identity & Ownership resolution](https://github.com/Vt00ls/SlideSmith/issues/18#issuecomment-5041095988),
[Task Orchestration](./task-orchestration.md),
[Runtime Execution](./runtime-execution.md),
[Release Management](./runtime-and-pipeline-releases.md),
[Catalog Publication](./catalog-template-publication.md),
[Durable Object](./durable-object-storage.md),
[Backup & Recovery](./backup-and-recovery.md), and
[observability, audit, and Cleanup Debt](./observability-audit-and-cleanup-debt.md)
contracts remain authoritative for their respective seams.

The [Workspace Export and Purge](./workspace-export-and-purge.md) contract
governs disabled-User offboarding after target cutover. A legacy cutover pin
that would retain purged bytes beyond the normal Recovery Point window blocks
purge until the pin is released.

The design fixes migration authority, ownership backfill, record disposition,
historical representation, batching, freeze, activation, validation, rollback,
and cleanup boundaries. It deliberately does not define a schema, migration
program, SQL, command line, deployment date, production seed identity, or
physical deletion procedure.

## Decision summary

Legacy migration follows three rules:

1. **Promote verifiable facts.** A legacy fact becomes a target domain fact
   only when authoritative source relationships and exact integrity evidence
   satisfy the target module's contract.
2. **Retain ambiguous history without granting authority.** Ambiguous or
   incomplete records remain in an owner-scoped, read-only legacy-history
   representation or a protected quarantine. They cannot authorize access,
   execution, recovery, publication, usage, audit, or cleanup.
3. **Hard-replace execution authority.** Legacy workspaces, sessions, caches,
   failed residue, path-based repositories, and execution compatibility
   surfaces do not survive cutover.

The migration is fail closed. Missing or corrupt retained Source Material,
active Artifact Version members, or accepted catalog packages block cutover.
Proceeding with known loss, assigning content without reliable ownership, or
restoring a pre-cutover point after target mutation requires a separate
human-authority decision.

## Standing constraints

- Enterprise V1 has no Tenant or Membership. One User owns one immutable,
  non-reused Personal Workspace; neither a Personal Workspace nor a Task can
  transfer to another User.
- Platform PostgreSQL and each owning Platform Control Plane module remain
  authoritative after activation. A migration ledger does not become a second
  business state machine.
- Source Material, active Artifact Versions and Artifacts, Task metadata,
  verifiable locks, and run history are retained. Execution material is not.
- A Runner Profile, mutable tag, environment value, path, session, copied Skill
  tree, capability snapshot, latest run, or status string cannot establish an
  Execution Lock, Template Lock, Checkpoint, Phase success, or Runtime Evidence.
- Every successful target content reference requires verified immutable bytes
  through the Durable Object seam. A row or locator alone is insufficient.
- The pre-cutover and post-cutover Recovery Points remain pinned until the
  rollback window is explicitly closed.
- No product code, schema, production data, or deletion is changed by this
  decision-only resolution.

## One-time migration module and authority

`Legacy Migration` is a bounded Platform Control Plane module used only for
the cutover. It owns:

- the migration identity, source-snapshot fingerprint, rules version, seed
  ownership input, cutover generation, and canonical request digest;
- source-record inventory and exactly-one disposition for every record;
- stable source-to-target identity assignments and batch state;
- freeze, staging, validation, read-only activation, cutover commit, rollback
  window, and cleanup-obligation issuance;
- authoritative migration audit facts and reconciliation evidence.

It does not own enduring User, Personal Workspace, Task, Phase Run, Runtime
Run, Artifact Version, catalog, release, object, retention, or cleanup state.
It asks each target module to validate and activate facts through that module's
internal migration adapter. After cutover, the ordinary module interfaces can
operate without Legacy Migration.

Representative high-level intents, not final method names, are:

```text
PrepareMigration(SourceSnapshot, SeedOwnership, RulesVersion)
Freeze(CutoverGeneration)
TransformBatch(MigrationID, BatchIdentity)
Validate(MigrationID)
ActivateReadOnly(MigrationID)
CommitCutover(MigrationID)
IssueCleanupObligations(MigrationID)
```

The interface never exposes unrestricted SQL, object keys, filesystem roots,
paths, sessions, directory delete, or a caller-selected target identity.

## Authoritative inputs and preconditions

A migration can enter freeze only when all of the following are true:

- the source database, local object storage, configured Task Workspace root,
  Agent Compose data/session root, template roots, and known failure-marker
  roots are inventoried under explicit deployment allowlists;
- the source database has a transaction-consistent snapshot identity and
  canonical table counts and roots;
- target release, catalog, Durable Object, identity, authorization, audit,
  backup, C04, orchestration, and Runtime contracts needed for conversion have
  passed acceptance tests;
- one complete migration rehearsal from a production-scale copy and one full
  pre-cutover recovery exercise have passed;
- the finalized recovery watermark is healthy, key escrow is verified, and a
  complete pre-cutover Recovery Point can be produced after freeze;
- a Platform Administrator supplies an authorized seed external identity and
  attests that the legacy deployment is one ownership domain; and
- every known retained business-byte inconsistency has been repaired exactly
  or remains a named cutover blocker.

Object, directory, process, and marker inventories discover candidates. They
do not prove business membership, ownership, publication, recovery, or
deletion eligibility.

## Ownership backfill

The current application has no User or Personal Workspace facts and exposes a
single global business namespace. Migration therefore treats one legacy
deployment, not an arbitrary row, as the ownership source.

- The authorized seed external identity resolves to one existing User and
  Personal Workspace or creates both transactionally under the accepted
  Identity & Ownership contract.
- Every formal legacy Task from that deployment is attached atomically to that
  Personal Workspace. Descendant ownership continues to resolve through the
  target parent chain; descendants do not gain independent writable owner
  fields.
- The exact seed identity is a deployment input protected by mandatory audit,
  not a value selected by the architecture or migration code.
- The seed cannot be the first person to sign in, a Platform Administrator by
  role, a shared system identity, a nullable owner, or a mutable email address.
- If the deployment contains work that the administrator cannot attest belongs
  to the seed, the affected Tasks stay quarantined and block formal cutover.
- A later external-identity rebind retains the same User and Personal Workspace.
  It never transfers the Task or changes its deduplication domain.

The ownership linearization point is the target activation transaction that
creates or reuses the seed identities and attaches all accepted Tasks under the
same migration generation. Mandatory audit failure rolls back that activation.

## Record migration matrix

| Legacy source | Target disposition | Required evidence and failure behavior |
| --- | --- | --- |
| `Task` | Retain Task identity, title, timestamps, original status, recognized Route, error, and business metadata | Parent ownership must resolve to the seed Personal Workspace; execution claims, paths, sessions, and mutable runtime fields have no target authority |
| completed Task | Preserve completed business history | Future manual edit is eligible only when exact Execution and Template Locks and a latest verified Artifact Version exist; otherwise the Task is owner-readable but non-executable |
| cancelled Task | Preserve terminal cancellation | No legacy recovery or retry |
| failed Task | Preserve failure history and mark non-recoverable | Existing retry switches, workspace state, and status do not prove a recoverable attempt |
| every other Task status | Terminate under the cutover generation as legacy non-recoverable | Preserve the original status and reason; continuation requires a new Task from retained Source Material or an Artifact Version |
| `TaskEvent` | Optional non-authoritative Task history under existing short retention | Never becomes mandatory audit, a transition decision, usage evidence, cleanup proof, or correlation authority |
| `TaskConfirmation` | Retain as owner-scoped legacy interaction history | It cannot complete a target Confirmation Gate or create a Phase Run without exact Pipeline and decision evidence |
| `TaskPhaseRun` | Promote only when its exact Pipeline Version, Phase definition, attempt, input binding, and outcome evidence are recognized; otherwise retain as legacy run history | A status string or output JSON cannot create validation, Task Workspace Revision, Checkpoint, or cursor advancement |
| `TaskRuntimeRun` | Promote only with one explicit parent, exact Runtime Binding, recognized terminal evidence, and consistent Task identity; otherwise retain as legacy runtime history | No lease, fence, Runtime Evidence, usage, or success is invented |
| `TaskEditSession` | Preserve published lineage and legacy draft metadata; terminalize every active session as non-recoverable | Edit workspace, claim, capability snapshot, and last-run fields do not permit resume |
| `TaskEditRun` | Promote only under the same exact manual-edit Pipeline and run-evidence gates; otherwise legacy history | An explicit run reference can help establish parentage but cannot establish a target binding or outcome by itself |
| `Artifact(kind=source)` | Convert to immutable Source Material | Read exact legacy bytes, compute digest and size independently, obtain a DurabilityReceipt, and attach in the seed Personal Workspace domain |
| active `TaskArtifactVersion` | Convert to an immutable Artifact Version and its exact members | Verify every member, manifest, PPTX reference, parent, and bytes; recompute a locator-free target manifest digest |
| published group without a version row | Deterministically backfill only when complete publication evidence exists | `(TaskID, PublishVersion)` is grouping evidence, not sufficient authority; Task completion/publication evidence, required kinds, unique membership, and all verified bytes are required |
| staging or failed artifact-version row | Retain publication-attempt metadata, not an Artifact Version | Associated bytes remain inaccessible failed residue and become cleanup candidates after the rollback boundary |
| non-source Artifact outside an active version | Retain metadata as legacy evidence, not a target Artifact | It is execution or publication residue unless an exact active manifest proves membership |
| `TemplateRegistryEntry` | At most one Catalog Template candidate and one Template Version candidate for its exact frozen bytes | Missing, unsafe, mismatched, unknown, or path-only input is quarantined; legacy status is a requested disposition, not approval or activation |
| legacy Task template lock JSON | Convert only when identity and recognized checksum map one-to-one to the frozen target package and complete closure | `SelectedTemplateID`, source path, current row, or lock time is insufficient |
| workspace, session, cache, temporary publication, promotion, marker, and failed-residue bytes | No domain migration; exact deletion inventory only | Never scanned for a Checkpoint or adopted as Source Material or publication |

Every source row receives exactly one durable disposition: `converted`,
`retained legacy evidence`, `quarantined blocker`, or `cleanup residue`.
Unknown and duplicated dispositions fail validation.

## Task status, Route, and lock conversion

### Task state

Cutover terminalization is a migration decision, not a claim about what a
legacy process actually did. It records:

- original Task status and update time;
- cutover generation and authoritative termination decision;
- `legacy-non-recoverable` recovery disposition;
- whether Source Material and Artifact Versions remain usable to start a new
  Task; and
- lock, content, and history conversion results.

A legacy `failed` status remains failure history but is no longer retryable.
A completed Task remains a completed publication owner even if its execution
lock cannot be converted. Lack of an executable lock never hides or deletes a
valid publication.

### Route

Recognized mappings are:

| Legacy Route | Target Route |
| --- | --- |
| `main` | Generation Route |
| `beautify` | Beautify Route |
| `template-fill` | Template Fill Route |

Route-selection JSON and standalone-workflow fields may corroborate this
mapping but cannot override a conflict. An unknown or inconsistent Route keeps
the Task in legacy history and blocks target execution.

### Execution Lock

Pipeline Version, Runtime Release, and Compatibility Approval are an all-or-
nothing set. A Task receives an Execution Lock only when a recognized immutable
legacy deployment inventory proves:

- the exact Pipeline Version manifest and Route graph used by that Task;
- the exact Runtime Release, Core Skill, toolchain, executor contract, and OCI
  image digests;
- a valid Compatibility Approval over that exact pair; and
- a one-to-one relationship between the Task's source evidence and those
  immutable facts.

Runner Profile, profile source or time, deployment defaults, environment,
mutable image tags, workspace manifests, copied Skill trees, Phase status,
recent runs, paths, and sessions are explicitly insufficient. No partial lock
is exposed. Tasks without a full lock remain readable and non-executable.

### Template Lock

- A Generation Task converts its Template Lock only when the legacy identity
  and recognized checksum identify one exact frozen Template Version package,
  all scan and license requirements pass, and its complete Resource Bundle
  closure is proved.
- An empty Bundle closure is valid only for a demonstrably self-contained
  package.
- Beautify has no Catalog Template in enterprise V1.
- A Template Fill input is Source Material and is never published into the
  catalog.
- Missing or ambiguous Generation locks make the Task non-executable; the raw
  JSON remains protected legacy evidence.

## Phase Run and Runtime Run history

The target `Phase Run 1 -> 0..N Runtime Run` relationship is established only
from explicit, unique source relationships:

1. Index every legacy Phase Run, Runtime Run, and manual-edit run by its source
   identity and Task.
2. A Runtime Run may have a candidate parent only when exactly one same-Task
   `TaskPhaseRun.RuntimeRunID` or `TaskEditRun.RuntimeRunID` names it.
3. Duplicate references, cross-Task references, missing referenced rows, or
   conflicting phase identity make the relationship ambiguous.
4. Phase text, timestamps, execution claims, `Task.LastRuntimeRunID`, external
   run/session identities, workspace paths, and nearest-run ordering are never
   fallback relationship algorithms.
5. A Phase Run with no Runtime Run may remain valid only when its exact pinned
   Phase contract permits zero Runtime Runs and its outcome evidence verifies.

Legacy `running` Runtime Runs become `Lost` at freeze only as cutover history;
legacy running Phase attempts become non-recoverable. Reported succeeded,
failed, or skipped states are displayed as `legacy-reported` unless their full
target evidence contract verifies. They never create Runtime Evidence,
Checkpoint, Task Workspace Revision, Usage Receipt, Usage Ledger entry, or
authoritative audit fact.

Ambiguous and unpromoted records remain in a target-native read-only history
projection. Owner access is scoped through the Task. Administrator metadata
access uses a separate protected diagnostic scope. The ordinary view excludes
host paths, sessions, commands, raw responses, unrestricted errors, and
content-bearing payloads. It exposes safe status, attempt, times, relationship
disposition, and opaque evidence references.

Raw path, session, command, response, event, and failure fields remain only in
the protected source Recovery Point or restricted evidence under existing
retention. A canonical digest and source-record reference remain in the
migration ledger. No target runtime, repository, or recovery caller can read
them as authority.

## Source Material and Artifact Version conversion

### Source Material

Only user-provided source rows are Source Material. Current derived kinds such
as normalized Markdown, conversion profiles, inventories, plans, reports, and
workspace output remain derived execution evidence unless an active Artifact
Version manifest explicitly publishes them.

For each Source Material row, migration:

1. resolves the owning Task and Personal Workspace;
2. opens the legacy object through a migration-only locator adapter;
3. computes digest and size while reading and verifies any recognized legacy
   checksum;
4. prepares verified immutable content through Durable Object; and
5. atomically attaches a new Source Material identity to its Task in the target
   activation transaction.

A missing or corrupt Source Material payload blocks cutover. A path or object
key is never copied into the target business record.

### Artifact Versions and Artifacts

An active source version converts only when:

- every member has one unique source Artifact identity and belongs to exactly
  that Task and version group;
- logical kind and name membership is unambiguous;
- every byte stream verifies against independently computed digest and size;
- the declared PPTX member exists, belongs to the group, and has the expected
  kind;
- route-specific required publication members and contracts pass;
- the target canonical manifest can be constructed without a path, object key,
  prefix, storage backend, URL, or materialization identity; and
- parent lineage, when declared, points to one earlier active version of the
  same Task and the graph is acyclic.

Equal bytes may deduplicate inside the seed Personal Workspace domain but do
not merge Artifact identities. Two source rows with the same logical member
identity are a conflict even when their bytes match.

The existing Artifact Version digest includes object keys and is not reused as
the target manifest digest. Migration retains it as legacy evidence and
computes the target canonical digest from stable semantic and content facts.

A current version is selected only when a unique lineage head and activation
ordering are provable. If several heads or an exact-time tie remain ambiguous,
all valid versions stay readable but the Task has no mutation-eligible latest
version until separately reconciled. Migration never chooses by lexical ID or
storage path merely to obtain a pointer.

Staging, failed, partial, unversioned derived output, and failed-publication
bytes remain inaccessible. Metadata may remain as legacy history; physical
content enters cleanup only after the rollback boundary.

## Catalog conversion

Catalog conversion follows the existing Catalog Publication contract:

- freeze both `TemplateRegistryEntry` rows and the exact disk inventory before
  conversion;
- use a source path only inside the migration adapter to read controlled input;
- allocate stable opaque Catalog Template and Template Version identities once
  in the migration ledger;
- construct a deterministic safe archive and canonical manifest, verifying
  regular-file, path, link, representation, digest, scan, provenance, license,
  and compatibility evidence;
- treat legacy active, deprecated, and disabled values as requested target
  dispositions requiring target approval, not as proof of target lifecycle;
- create a Resource Bundle only from explicit package and dependency evidence;
  shared directory names or equal bytes do not prove a Bundle; and
- activate at most one eligible current Template Version for each Catalog
  Template after the complete closure verifies.

Missing directories, unsafe members, checksum mismatch, incomplete dependency
evidence, or unsupported license terms quarantine the candidate. They never
cause a current Task lock to fall forward to another version.

After cutover, startup disk sync, mutable registry rows, source paths, current
Skill-directory reads, and path-bearing Template Locks are deleted from the
application surface rather than wrapped.

## Legacy execution inventory and cleanup ownership

The deletion inventory is built after freeze from explicit configured roots
and source relationships. It includes, when present:

- Task Workspace materializations and copied Skill/runtime trees;
- Agent Compose projects, runs, sessions, node-local databases, sandboxes, and
  session workspaces;
- manual-edit and template-fill API session, project-promotion, candidate, and
  committed-cleanup directories;
- caches, local materializations, temporary outputs, incomplete publications,
  failed rollback objects, and abandoned staging generations; and
- workflow-specific path-bearing cleanup markers.

Every inventory entry receives an opaque cleanup identity, exact owner,
resource class, source generation, allowlisted root, estimated bytes and
inodes, blockers, and evidence root before physical cleanup starts.

| Resource | Cleanup authority |
| --- | --- |
| process, Agent Compose session, sandbox, lease, containment and reset residue | Runtime Execution |
| Task Workspace, Runtime View, manual-edit workspace, template-fill session and promotion residue | C04 Task Workspace Lifecycle |
| object staging, failed publication bytes, cache, quarantine and immutable physical generations | Durable Object, after the publication or owning module releases semantic intent |
| catalog and release package references | Catalog Publication or Release Management decides reference release; Durable Object or OCI adapter performs physical reclamation |

A marker, directory, bucket listing, process observation, path, or log only
discovers a candidate. Before deletion, the owner revalidates stable resource
identity, exact generation, allowlisted containment, references, leases,
incidents, grace period, and Recovery Point pins. A symlink, path escape,
generation change, unknown owner, or inconsistent inventory quarantines the
candidate.

Actual deletion cannot start before `CommitCutover`. Each cleanup obligation is
durable before its first physical attempt. Success closes it only with exact
`Reclaimed` or `AlreadyAbsent` evidence. Failure creates or updates one Cleanup
Debt record under the resource owner. Missing paths, deleted markers, metrics,
logs, or operator assertions do not prove reclamation.

## Idempotent batching and crash semantics

The migration identity binds:

- source deployment and transaction-consistent snapshot fingerprint;
- source database, object, catalog, and execution-inventory roots;
- seed User and Personal Workspace identities;
- migration-rules and canonicalization versions;
- target release/catalog policy generations;
- cutover generation and canonical request digest.

Source-to-target mappings use a stable `(MigrationID, source type, source
identity)` scope. A target opaque identity is assigned once and returned on
exact replay. The same source identity with a different canonical source digest
is an integrity conflict.

Batches use deterministic source-key ranges or an equivalent stable cursor.
Each batch has one operation identity, expected migration generation,
canonical request digest, input root, output root, counts, byte totals, and
disposition totals. The PostgreSQL batch transaction is the linearization
point.

- A crash before commit leaves no batch decision.
- A crash after commit but before response returns the committed result on
  replay.
- A claim is expiring and fenced. Claim loss redelivers the same batch rather
  than assigning new target identities.
- Same operation identity with different input fails closed.
- Durable Object prepare may complete before a business activation. Such bytes
  remain inaccessible staging until exact replay, abort, expiry, or
  reconciliation.
- Cross-batch foreign relationships remain staged until their complete closure
  validates. No partial Execution Lock, Template Lock, Artifact Version, or
  catalog closure becomes visible.

GORM `AutoMigrate`, table existence, application startup, and a schema version
alone cannot serve as the data-migration ledger.

## Freeze and cutover sequence

1. **Rehearse.** Run the full migration twice from the same production-scale
   snapshot and require identical identity maps, manifest roots, counts, bytes,
   and dispositions. Complete the required end-to-end recovery exercise.
2. **Freeze.** Commit one authoritative migration freeze generation. Reject
   Task, edit, publication, release, catalog, and deletion mutation; stop new
   scheduling and Runtime admission. Physical work may stop or return evidence,
   but no result may commit across the fence.
3. **Finalize the source point.** Reconcile source transactions, claims, and
   inventories, terminalize active legacy work, then create and pin the fully
   verified pre-cutover Recovery Point.
4. **Transform into staging.** Run idempotent ownership, business-record,
   content, release, catalog, history, and cleanup-inventory batches. Target
   records remain inaccessible.
5. **Validate.** Reconcile every source row, target relationship, byte,
   manifest, lock, history disposition, ownership path, and cleanup candidate.
6. **Activate read-only.** One PostgreSQL transaction records the accepted
   migration roots, activates the complete target facts, advances target
   generations, commits mandatory audit, and switches read authority. Legacy
   tables and interfaces remain inaccessible; target mutation stays fenced.
7. **Prove the target.** Run owner, administrator, Artifact Version, Source
   Material, catalog, history, integrity, reconciliation, backup, and restore
   scenarios. Create and pin the post-cutover Recovery Point.
8. **Commit cutover.** A separate explicit, audited operator intent verifies
   the post point and all gates, closes the routine rollback window, and enables
   target mutation.
9. **Clean execution residue.** Issue exact cleanup obligations and reconcile
   them to verified completion or durable Cleanup Debt.

There is no dual-write phase. Read-only target activation permits verification
without allowing new facts to diverge from the pre-cutover point.

## Validation and reconciliation contract

Cutover succeeds only when every gate below passes.

### Inventory and ownership

- For every source table, total rows equal the sum of converted, retained
  evidence, quarantined blocker, and cleanup-residue dispositions.
- No source identity has zero or multiple dispositions or target mappings.
- Every formal Task has exactly one seed Personal Workspace parent, and every
  descendant ownership path terminates at that Task and Workspace.
- No quarantined ownership record remains in a normal owner, Share,
  administrator, scheduler, Runtime, or object interface.

### Task, lock, and run state

- No legacy Task, edit session, Phase attempt, Runtime invocation, execution
  claim, or workspace can still commit.
- Every non-terminal legacy Task has the cutover termination decision and
  retained original-state evidence.
- Every target Execution Lock and Template Lock is complete and recomputes from
  exact manifests and evidence; no partial or floating reference exists.
- Every promoted Runtime Run has exactly one Phase Run parent. Ambiguous and
  unverified rows appear only in the legacy-history projection.
- No migration record fabricates Runtime Evidence, Sandbox Lease, Checkpoint,
  Task Workspace Revision, Usage Receipt, Ledger entry, or mandatory audit.

### Content and publication

- Every retained Source Material and active Artifact member has a strict
  DurabilityReceipt and verified digest and size.
- Every Artifact belongs to exactly one immutable Artifact Version; membership,
  target manifest root, PPTX member, lineage, and latest disposition reconcile.
- Missing, corrupt, duplicated, or inconsistent retained business payloads are
  zero. Any unresolved instance blocks the gate.
- User-content deduplication is confined to the seed Personal Workspace domain.

### Catalog and release

- Every activated Catalog Template has at most one current Active Template
  Version and a complete acyclic verified Bundle closure.
- Every retained Template Lock and Execution Lock has all exact package,
  manifest, compatibility, scan, license, OCI, and retention dependencies.
- A path, tag, environment value, current directory, or object listing cannot
  reproduce a target lock in the test harness.

### Authorization, audit, cleanup, and recovery

- The seed Owner can access accepted Tasks, Source Material, and Artifact
  Versions only through the owner seam; a different Workspace receives a
  non-leaking denial.
- Administrator metadata access cannot open content, and no legacy Share Link,
  token, Access Code, or global ID surface is created.
- Mandatory migration, activation, cutover, quarantine, and cleanup-exception
  audit facts exist; telemetry is not counted as audit.
- Cleanup inventory counts, estimated bytes and inodes, owner classes,
  obligations, blockers, successes, and Cleanup Debt reconcile exactly.
- The pre- and post-cutover Recovery Points are complete and pinned. A fresh
  isolated restore of the post point passes the read-only and full gates.

Counts alone are insufficient. Validation uses canonical roots over ordered
source facts, target facts, reference closures, byte receipts, and dispositions
so equal totals cannot hide substitution or duplication.

## Rollback boundary

Rollback always restores one complete compound set.

| Point | Allowed response |
| --- | --- |
| Before target activation | Abandon or retry staging; keep the frozen legacy authority unchanged |
| After read-only activation but before `CommitCutover` | Restore the complete pre-cutover Recovery Point, including PostgreSQL, objects, packages, application release, and configuration |
| After `CommitCutover` or the first target mutation | Routine rollback is closed; forward repair is required |

Restoring the pre-cutover point after target mutation would discard new
business facts. It therefore requires a separate incident-specific
human-authority decision that explicitly accepts the loss. A pre-cutover
database is never combined with post-cutover objects or packages, or vice
versa.

Legacy execution directories are excluded from Recovery Points by design.
Their deletion is irreversible and uses a separate explicit, mandatory-audit
operator intent after `CommitCutover`. Publishing this architecture decision
does not authorize a production cutover or deletion.

The online legacy database and application interfaces remain disabled after
cutover. Source snapshots and retained raw evidence expire only under the
accepted Recovery Point, audit, and restricted-evidence policies; they do not
become an indefinite compatibility archive.

## Authorization, integrity, retention, and operational evidence

- Migration administration grants no User-content access. Inspecting content
  for repair still requires the applicable Owner or exact break-glass path.
- The seed mapping, freeze, activation, cutover commit, quarantine disposition,
  accepted exception, rollback selection, and cleanup resolution require
  authoritative audit committed with their decisions.
- Audit and telemetry contain no content, source filenames when restricted,
  paths, sessions, object locators, raw runtime/provider errors, credentials,
  or unrestricted JSON payloads.
- Migration mappings, canonical roots, disposition totals, activation facts,
  rollback closure, and authoritative audit remain retained engineering and
  business evidence.
- Raw Task events, logs, paths, sessions, commands, runtime responses, marker
  contents, and failure payloads follow existing short restricted-evidence
  retention. They do not enter a Recovery Point as target authority.
- Open Cleanup Debt remains until exact resolution. Resolved debt and its audit
  and evidence root follow the existing minimum 365-day retention.
- Repair may restore only exact bytes or facts matching the original identity,
  schema, digest, signature, scope, and relationship. It cannot change an
  expected digest, adopt an orphan, or select a newer release or template.

## Highest-level scenarios and adapter contracts

The Legacy Migration module is the highest-level migration test seam. A
deterministic harness with controllable transactions, clocks, object faults,
claim loss, acknowledgement loss, and filesystem inventories covers:

- exact seed ownership, rejected ambiguous ownership, disable and rebind;
- every Task status and Route, complete and incomplete locks, and non-
  recoverable terminalization;
- zero, one, several, missing, duplicate, cross-Task, and ambiguous Runtime Run
  relationships;
- active, missing, corrupt, duplicated, staging, failed, unversioned, parented,
  and multi-head Artifact Version cases;
- Source Material verification, cross-Workspace deduplication isolation,
  acknowledgement loss, and exact repair;
- catalog row conversion, unsafe packages, checksum mismatch, closure cycles,
  license failure, activation races, and ambiguous Task locks;
- duplicate batches, same-key/different-payload conflicts, concurrent claims,
  restart at every transition, and deterministic second-run roots;
- freeze races, evidence arriving after the fence, read-only activation,
  post-point failure, whole-set rollback, and forward-only behavior after
  cutover commit;
- marker and inventory discovery without authority, symlink and root escape,
  generation drift, deletion failure, Cleanup Debt retry, and exact absence
  proof; and
- owner/admin separation, non-leaking reads, protected diagnostics, redaction,
  mandatory-audit failure, and absence of every legacy compatibility surface.

Source database, target PostgreSQL, Identity & Ownership, Task Orchestration,
Runtime Execution, C04, Artifact Publication, Catalog Publication, Release
Management, Durable Object, backup, audit, and cleanup adapters receive
black-box contracts for idempotency, integrity, fencing, acknowledgement loss,
authorization, and non-leakage. Tests assert identities, roots, manifests,
receipts, dispositions, decisions, and evidence rather than SQL shape, GORM
behavior, paths, object keys, vendor commands, or log text.

## Deletion test and compatibility boundary

The only compatibility surface is a target-native, owner-scoped, read-only
legacy-history projection. It reports safe historical facts and migration
disposition but cannot provide a path, session, object locator, mutable Task
snapshot, workspace handle, run handle, Checkpoint, retry, resume, publication,
or cleanup capability.

After cutover the implementation must be able to delete rather than wrap:

- global Task and Artifact repositories and raw-ID HTTP paths;
- Runner Profile, environment, tag, workspace manifest, and current-directory
  release selection;
- `TaskPhaseRun.RuntimeRunID`, Runtime session, and workspace path coupling;
- direct TaskService execution, worker-written Task status, and recent-run
  recovery;
- `StorageService.Root`, `Path`, direct object keys, and caller deletion;
- mutable `TemplateRegistryEntry`, startup disk sync, and path-bearing locks;
- Agent Compose CLI, shared data root, Docker socket, session scanning, and
  directory-derived recovery authority;
- manual-edit and template-fill path/session progression and marker registries;
  and
- legacy events, logs, statuses, markers, directories, or storage listings as
  audit, success, usage, repair, or deletion authority.

If any target caller still requires one of these surfaces, the migration has
not passed the deletion test.

## Rejected alternatives and downstream inputs

Rejected alternatives include ownership by first login or administrator;
nullable or system Workspace ownership; Task transfer; in-place GORM data
migration without a ledger; online dual write; directory-scanned Checkpoints;
profile, tag, path, session, status, or recent-run lock inference; timestamp or
Phase-name Runtime relationship guesses; converting every Artifact row into a
publication; reusing locator-bearing manifest digests; silently choosing an
ambiguous latest Artifact Version; adopting catalog Bundles from matching
directories; partial database/object rollback; cleanup before the rollback
window closes; marker- or metric-based Cleanup Debt resolution; and retaining
a path-based or read/write legacy façade.

Stable inputs to the first migration specifications are:

- the complete record disposition and evidence matrix in this document;
- a one-deployment-to-one-seed-Workspace ownership contract with no transfer;
- exact all-or-nothing Execution and Template Lock conversion;
- explicit-only Phase/Runtime relationship mapping and safe legacy history;
- locator-free Source Material and Artifact Version conversion through Durable
  Object;
- deterministic catalog candidate conversion without inferred Bundles;
- fenced idempotent batches, read-only target activation, and whole-set
  rollback before `CommitCutover`;
- exhaustive reconciliation roots and acceptance gates; and
- cleanup obligations created before exact deletion, with every failure
  represented by the established Cleanup Debt contract.

Superseded accepted decisions: none.

New decision-only tickets: none.

Remaining fog affecting the first migration specifications: none. Exact schema,
SQL, serialized names, migration executable, batch sizes, deployment seed
identity, cutover date, object and database products, and physical cleanup
commands belong to implementation specifications or an authorized production
runbook and cannot weaken this contract.
