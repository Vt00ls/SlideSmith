# Catalog Template Publication and Locking

This document records the catalog publication and Task-lock decisions resolved
in [GitHub issue 23](https://github.com/Vt00ls/SlideSmith/issues/23).
[CONTEXT.md](../../CONTEXT.md) is authoritative for domain language,
[ADR 0004](../adr/0004-version-catalog-templates-and-pin-resource-bundles.md)
establishes immutable Template Versions and Resource Bundle pinning, and
[ADR 0023](../adr/0023-atomically-activate-and-pin-catalog-template-closures.md)
records atomic activation and Task selection. The
[Durable Object](./durable-object-storage.md),
[Task Orchestration](./task-orchestration.md),
[Runtime Execution](./runtime-execution.md), and
[Backup & Recovery](./backup-and-recovery.md) contracts remain authoritative
for their respective seams.

The design fixes authority, identities, manifests, lifecycle, activation,
selection, Template Lock closure, distribution, security, retention, recovery,
and legacy conversion boundaries. It does not choose a registry, object-store,
scanner, CI product, schema, serialized protocol, administrator UI, or
implementation sequence.

## Decision summary and standing constraints

`Catalog Publication` is a deep module in the Platform Control Plane. It owns
Catalog Template identity and listing revisions, immutable Template Version and
Resource Bundle publication, lifecycle, the one current Active Template Version
for each Catalog Template, scan and license eligibility, catalog generations,
and catalog safety epochs.

Task Orchestration owns each Task's Template Lock. For a Generation Route, the
same PostgreSQL transaction that accepts the Route and Execution Lock validates
the observed catalog selection, computes the complete Resource Bundle closure,
and records one immutable Template Lock plus its retention references. Retry,
recovery, cancellation, and post-publication manual edit never repin it.

Beautify does not consume a Catalog Template in enterprise V1. A Fill Template
remains Source Material owned by one Task in one Personal Workspace; it never
enters Catalog Publication.

Ordinary activation, deactivation, rollback, deprecation, and catalog retirement
affect only new Task selection. `Disabled` is the authority-reducing state for a
security, integrity, authorization, license, or platform-control failure. It
advances a catalog safety epoch and fences uncommitted work under existing
Template Locks without rewriting those locks or automatically suppressing an
already published Artifact Version.

## Authority and module seam

| Fact or action | Authority | Relationship |
| --- | --- | --- |
| Catalog Template identity, listing metadata revision, current Active Version, and catalog generation | Catalog Publication in Platform PostgreSQL | Ordinary User queries consume a safe projection; callers cannot set a current version |
| Template Version and Resource Bundle manifests, lifecycle, scan/license eligibility, and safety epoch | Catalog Publication | Candidate, CI, scanner, provenance, and license adapters supply evidence only |
| Immutable package bytes and verification receipts | Durable Object | Catalog Publication owns semantic lifecycle and typed references; Durable Object owns byte integrity and materialization |
| Task Route, Execution Lock, Template Lock, Task revision, and Phase progression | Task Orchestration | Catalog Publication validates an observed selection inside the Task transaction |
| Runtime Run admission, Sandbox Lease, and accepted Runtime Evidence | Runtime Execution | Consumes the exact Template Lock and current catalog safety authorization; cannot choose a version |
| Runtime View, Task Workspace Revision, and Checkpoint | Task Workspace Lifecycle | Template packages are read-only inputs outside the Task Workspace and Checkpoint |
| Task and Personal Workspace ownership | Identity & Ownership | User selection requires owner authority; platform packages remain in a separate platform policy domain |
| Catalog recovery inventory and promotion gates | Backup & Recovery | Restores exact manifests, packages, locks, lifecycle, evidence, and current disable inventory |

The module hides candidate staging, canonicalization, dependency closure,
activation ordering, scan and license evaluation, package distribution,
retention references, reconciliation, and repair behind intent-oriented seams.
It does not expose PostgreSQL, object locators, buckets, paths, mounts, registry
tags, CDN URLs, credentials, cache layout, or scanner-specific results.

## Identity, manifest, and digest boundaries

Business identity and content integrity remain separate:

- A Catalog Template has one stable opaque identity that is never reused. It
  owns audited listing metadata revisions and an optional current Active
  Template Version pointer. Human-readable names, slugs, categories, and
  version labels are not authority.
- A Template Version has an immutable opaque identity under one Catalog
  Template. Its canonical manifest binds the design definition, schema,
  previews, embedded assets, exact direct Resource Bundle references,
  compatibility requirements, provenance, scan and license evidence roots, and
  deterministic package digest.
- Each Resource Bundle identity denotes one immutable published revision. Its
  canonical manifest binds safe logical members, file types and modes, sizes,
  digests, exact downstream Resource Bundle references, provenance, scan and
  license evidence roots, and deterministic package digest. A family or version
  label never becomes a floating reference.
- Every digest includes its algorithm. Enterprise V1 supports SHA-256. Unknown
  algorithms, unknown manifest schema majors, non-canonical encoding, missing
  evidence, or digest disagreement fail closed.
- Manifest, package, and member digests are integrity facts and
  deduplication inputs, not business identities or physical locators.

Catalog Template listing metadata may change without a new Template Version
only when the change cannot affect design, preview bytes, compatibility, or
runtime behavior. Any change to the design definition, runtime-consumed
metadata, preview member, embedded asset, dependency, or contract publishes a
new Template Version.

## Embedded assets and Resource Bundle closure

Every runtime-consumed byte appears through exactly one declared member:

- An asset may remain embedded when it is exclusive to one Template Version and
  shares that version's distribution, scan, license, retention, and withdrawal
  lifecycle.
- An asset that needs independent reuse, distribution, scanning, licensing,
  retention, or withdrawal belongs in a Resource Bundle.
- Package size may influence a configurable packaging optimization, but no byte
  threshold becomes a business boundary.
- Preview assets are explicit Template Version members. A directory listing or
  permanent preview URL cannot establish membership.
- Implicit shared roots, network-fetched assets, floating bundle families, and
  runtime directory discovery are forbidden.

Resource Bundle dependencies form an exact directed acyclic graph. Publication
rejects cycles, a repeated identity with different digests, conflicting logical
destinations, undeclared input, or an incomplete closure. The Template Lock
records a canonical ordering and closure-root digest; object-store deduplication
does not merge business identities or dependency edges.

Packages contain only policy-approved non-executable visual assets. Traversal,
symlinks and hardlinks, devices, executable modes, macros, scripts, unsafe SVG,
undeclared nested archives, and external references are rejected unless the
current approved policy can prove the exact representation safe.

## Publication lifecycle

A Draft Candidate is mutable staging and is not yet a Template Version or
Resource Bundle under the domain definitions. Approval creates the immutable
business identity.

| State | New Task or new dependency | Existing Template Lock |
| --- | --- | --- |
| Draft Candidate | Never | Not applicable |
| Approved | Not offered to ordinary Users | Continues |
| Active | Eligible when the complete closure is also Active and valid | Continues |
| Deprecated | No new selection or dependency | Continues with exact packages |
| Disabled | Never | Materialization, admission, commit, and publication fail closed |

Normal transitions are:

- Draft Candidate to Approved after complete package, manifest, receipt,
  compatibility, provenance, scan, and license validation;
- Approved to Active and Active to Approved for ordinary activation,
  deactivation, or rollback;
- Approved or Active to Deprecated for retirement;
- Deprecated to Approved only after the current validation gate succeeds;
- Approved, Active, or Deprecated to Disabled for a trust, rights, integrity,
  or platform-control failure;
- Disabled to Approved only after exact bytes and all current validation gates
  succeed. A separate activation is still required.

Resource Bundles activate independently. A Template Version is effectively
eligible for a new Task only when it is the Catalog Template's current Active
Version and every member of its transitive bundle closure is Active. Deprecating
a Bundle immediately removes dependent versions from new selection without
breaking existing locks. Disabling a Bundle advances the safety fence for every
dependent version and existing lock.

## Atomic activation and selection ordering

Enterprise V1 exposes one current Active Template Version per Catalog Template.
Activating an Approved Version uses expected catalog and version revisions and
one PostgreSQL transaction to:

1. revalidate the target, complete Active bundle closure, manifests, receipts,
   compatibility, scan and license evidence, and recovery mode;
2. set the target Active and replace the current-version pointer;
3. return the prior Active Version to Approved so it remains a rollback
   candidate;
4. advance the catalog generation; and
5. commit the authoritative audit fact and reconciliation outbox.

The transaction is the activation linearization point. Catalog listing reads
the PostgreSQL authority rather than a CDN, cache, tag, or filesystem view.
Post-commit distribution and projection work is idempotently reconciled.

An ordinary User selects a Catalog Template from a listing projection that
includes a selection token bound to the Catalog Template, observed current
Template Version and manifest, listing revision, and catalog generation. The
Task transaction revalidates that token. If activation committed first, the
stale token conflicts instead of silently selecting a version the User did not
observe. If the Task transaction committed first, later lifecycle changes do
not rewrite its Template Lock.

## Template Lock contract

The Template Lock linearizes when Task Orchestration accepts the Generation
Route. That transaction validates ownership, Task input revision, Route,
recovery mode, the observed catalog selection, and compatibility with the
exact Execution Lock. It then records the Template Lock and every typed
retention reference before any consuming Phase Run can begin.

A Generation Route without a valid Template Lock fails closed. Catalog
selection on Beautify or Template Fill is inconsistent input and does not
create an unused lock. A pre-Route UI choice remains a selection intent, not a
lock, until the authoritative Task transaction succeeds.

The canonical Template Lock binds at least:

- schema, digest algorithm, and lock digest;
- Task, Route, and Catalog Template identity;
- Template Version identity, manifest digest, and package digest;
- the ordered Resource Bundle closure with each identity, manifest digest,
  package digest, and dependency relationship;
- the closure-root digest;
- exact compatibility-evaluation identity and digest, including its Execution
  Lock binding;
- catalog generation, Task input revision, Task revision, decision and
  operation identities, and acceptance time; and
- approval, provenance, scan, and license evidence identities and digests.

The lock contains no host or source path, mount, bucket, object key, registry or
CDN URL, signed URL, cache location, materialization identity, mutable tag, or
floating lifecycle reference. One Task has at most one Template Lock; changing
the selected design requires a new Task.

## Listing, locked reads, and materialization

The ordinary catalog query returns only Catalog Templates whose current Active
Version and complete bundle closure are eligible for a new Task. It does not
list Draft, Approved, Deprecated, Disabled, tombstoned, or arbitrary historical
versions, and it exposes no package locator or administrator evidence.

Administrator inspection can view complete lifecycle and diagnostic evidence
without acquiring User-content authority. Task-bound locked reads use a
separate internal scope and resolve the exact lock rather than the current
catalog pointer:

- Active, Approved, Deprecated, and catalog-tombstoned exact versions remain
  materializable for existing locks;
- Disabled, missing, corrupt, unlicensed, or unverified content fails closed;
- the resulting capability is bound to Personal Workspace, Task, Runtime Run
  or Task Workspace operation, fence, purpose, and expiry.

Every materialization verifies the lock digest, closure, manifests, exact
package bytes, and current catalog safety authorization. Runtime Run start and
accepted Runtime Evidence bind the Template Lock digest, immutable input
manifest, and safety epoch. Runtime admission, C04 commit, and Artifact
publication revalidate the epoch so a late completion cannot cross a disable
fence. Ordinary deprecation or activation does not advance the safety epoch.

Durable Object and the execution-node materializer download and expand packages
through random temporary entries, validate digest, size, manifest, paths, file
types, links, representation policy, bytes, and inodes, and atomically promote
a read-only cache entry. The sandbox receives only a controlled logical mount;
it receives no storage credential or host path.

## Scan, license, withdrawal, and fail-closed behavior

Approval requires structural and malicious-content scanning, provenance, and
license evidence under named policy versions. The immutable package does not
change when new evidence is produced; activation and materialization evaluate
the exact package against current policy.

License evidence states the permitted internal distribution and use scope,
effective interval, attribution obligations, and whether a restriction only
blocks new Task locks or also blocks rematerialization for existing locks.
Catalog Publication does not invent or reinterpret legal terms:

- missing, expired, inconsistent, or unprovable rights fail closed;
- a new-selection prohibition produces deprecation semantics;
- a prohibition on existing-lock reuse produces Disabled semantics and a
  safety fence;
- no ordinary administrator override can bypass scan, license, manifest, or
  integrity evidence.

An approved policy controller may automatically perform an authority-reducing
disable when deterministic evidence expires or fails. It cannot approve or
re-enable content. A candidate whose license requires irreversible deletion
earlier than the accepted 35-day Recovery Point window is ineligible for
enterprise V1; changing that recovery or compliance posture requires a
separate human-authority decision.

Disabling a Template Version or Resource Bundle does not automatically delete
or suppress an already published Artifact Version. Quarantining a business
publication is a separate security-incident decision.

## Retention, logical retirement, and physical reclamation

An operational `Deleted` version state does not exist. Catalog retirement
clears new selection and retains a non-reusable tombstone. Template Version and
Resource Bundle metadata, manifests, lifecycle history, safety facts, audit
records, and tombstones remain business records.

Approved, Active, and Deprecated packages remain retained. Disabled packages
remain while any Template Version, Template Lock, lease, incident, rollback
pin, license obligation, or Recovery Point requires them. Every Template Lock
creates explicit typed references; therefore planned reclamation cannot break a
valid old Task.

After the final reference and lease are released and no incident or Recovery
Point requires the bytes, Durable Object enters `pending_reclaim` and applies
the existing non-zero grace period, currently seven days. Physical deletion is
idempotent and asynchronous; failure becomes Cleanup Debt. Disabled is not
deletion authority, and an immutable backup may retain inaccessible encrypted
bytes until its accepted recovery window expires.

If a referenced package is physically missing or corrupt, that is an integrity
incident rather than planned deletion. Repair may use only bytes that exactly
match the original digest, size, manifest, and policy facts. The system never
substitutes the current Active Version or changes an expected digest.

## Administrator interface, authorization, and audit

Representative intent families, not final method names, are:

```text
SubmitCandidate
ApprovePublication
Activate | Deactivate | Deprecate | Disable
RetireCatalogTemplate
InspectPublication
ResolveSelectionForTask
AuthorizeLockedMaterialization
```

A controlled CI or build identity may submit candidate bytes, manifests, and
machine evidence. Only a Platform Administrator may approve, activate,
deactivate, deprecate, retire, or re-enable publication. An approved safety
controller may only reduce authority by disabling. None of these paths grants
access to Source Material, Tasks, Artifact Versions, or Personal Workspaces,
and catalog administration never implies break-glass.

Every authoritative decision commits its audit fact in the same transaction.
It binds actor and authority class, reason or ticket, operation identity,
canonical request digest, before and after revisions, catalog generation and
safety epoch, affected identities and digests, evidence roots, affected lock
and uncommitted-work counts, time, adapter identities, and reconciliation
outcome. Mandatory audit failure rolls back the decision. Logs, metrics, and
traces are projections and cannot become a second state machine.

Every intent has a scope-bound idempotency identity, canonical request digest,
and expected revision. Exact replay returns the original decision. Same key
with different content conflicts. Compare-and-swap rejects stale administrators
and reconcilers.

## Publication protocol and crash reconciliation

PostgreSQL and object storage do not use XA:

1. A candidate intent records operation identity, candidate revision, purpose,
   actor, and expiry.
2. Durable Object writes immutable bytes and returns strict digest, size,
   generation, and durability evidence.
3. Catalog Publication canonicalizes manifests and verifies the complete
   closure, compatibility, provenance, scan, and license evidence.
4. The approval transaction creates the immutable business identity, typed
   references, audit fact, and reconciliation outbox.
5. Activation is a separate idempotent compare-and-swap decision.

A crash before approval leaves no published version. Bytes without a committed
approval remain inaccessible staging. A crash after commit but before response
is recovered by replay. Object listing, a matching directory, tag, checksum, or
cache entry cannot be adopted as publication authority.

In `recovery-degraded/read-only`, approval, activation, re-enable, deprecation,
retirement, and deletion are blocked. A strictly authority-reducing emergency
disable remains permitted and is copied to the independent immutable safety
inventory so an older restore cannot resurrect withdrawn content.

## Backup and recovery

The full Recovery Point inventory includes Catalog Template, Template Version,
Resource Bundle, Template Lock and typed-reference metadata; exact manifests
and packages; compatibility, provenance, scan and license evidence; lifecycle,
catalog generation, safety epoch, disable inventory, and tombstones.

Artifact Version read-only recovery within four hours does not require catalog
execution packages. Task mutation, retry, recovery, and Runtime admission wait
for the eight-hour `FullReady` gate to verify every exact catalog dependency.
Restore reconciles the selected database point with the independent current
disable inventory. An older database or package cannot reactivate Disabled
content, and a missing dependency is never replaced with a newer version.

Recovery rematerializes exact packages from the Template Lock. It never reads
the current active pointer, scans a Checkpoint, workspace, session, directory,
cache, or object prefix, or infers a package from a recent Runtime Run.

## Legacy TemplateRegistryEntry conversion and deletion test

The current `TemplateRegistryEntry` combines a catalog key, mutable version and
status, host paths, checksum, previews, and compatibility JSON. Server startup
rewrites it from the current Skill directory; deprecated entries remain listed
and lockable; a missing directory changes status. The current Template Lock
contains a source path and directory checksum but no canonical package manifest
or Resource Bundle closure. Runtime workspace construction copies the current
Skill tree and does not prove that the selected bytes match the lock.

The target conversion boundary is therefore:

- freeze the registry and exact disk inventory before conversion; a host path
  is a one-time controlled migration input and never a target fact;
- one legacy row can produce at most one Catalog Template candidate and one
  Template Version candidate for its exact current bytes; it cannot establish
  historical versions;
- legacy ID, kind, and name are source keys. Target opaque identities are
  assigned once in the migration ledger and reused on replay. A version string,
  including `workspace`, remains only a label;
- legacy active, deprecated, and disabled status records a requested
  disposition, not target approval or activation authority;
- conversion builds a canonical manifest and deterministic archive from safe
  regular files, computes target digests, and verifies any recognized legacy
  checksum. Missing, unsafe, unknown, or mismatched input is quarantined rather
  than fabricated;
- equal files, common directory names, and Skill-wide `charts`, `icons`, or
  `assets` do not prove a Resource Bundle. Only explicit dependency, package,
  and license evidence can create one. Core Skill support content stays in the
  Runtime Release;
- a legacy Task lock converts only when its identity fields and recognized
  checksum map one-to-one to exact frozen bytes and a fully proved target
  Template Version closure. Source path, lock time, current registry row, or
  SelectedTemplateID alone is insufficient;
- an empty target Bundle closure is valid only when the package is demonstrably
  self-contained;
- an unprovable non-terminal Task becomes non-recoverable under issue 17. Its
  legacy JSON and paths may remain only as non-executable historical evidence;
- a Fill Template is never converted into catalog content; and
- after cutover, startup disk sync, path-based asset and lock authority, and the
  `TemplateRegistryEntry` compatibility surface are deleted rather than
  wrapped.

This target supersedes the registry/path/copy authority implied by the older
milestones in [the implementation design record](../design-template-driven-pipeline.md).
It does not conflict with an accepted ADR.

## Highest-level scenarios and adapter contracts

Catalog Publication is the highest-level test seam. A deterministic harness
covers:

- candidate, approval, activation, deactivation, rollback, deprecation,
  disable, revalidation, retirement, and exact replay;
- activation racing Task selection, disable, rollback, and stale writers;
- Bundle closure, cycles, conflicting digests and destinations, embedded
  assets, and incomplete dependencies;
- partial, missing, corrupt, malicious, unlicensed, or expired packages and
  exact repair;
- ordinary listing versus administrator inspection and locked reads;
- old-Task retry under Approved, Deprecated, or tombstoned state and fencing
  under Disabled state;
- disable before and after Runtime admission, C04 commit, and Artifact
  publication;
- cache poisoning, traversal, links, dangerous representations, and byte or
  inode exhaustion;
- Recovery Point, disable non-resurrection, retention, reclamation, and Cleanup
  Debt; and
- deterministic, ambiguous, and rejected legacy conversions.

PostgreSQL, Durable Object, scanner, provenance, license, audit, backup,
materializer, cache, CI/CLI, and owned-transport adapters receive black-box
contracts for immutable receipts, idempotency, acknowledgement loss,
authorization, exact repair, and non-leakage. Tests assert identities,
manifests, decisions, closures, locks, epochs, and evidence rather than schema,
paths, vendor commands, URLs, or log text.

## Rejected alternatives and downstream inputs

Rejected alternatives include retaining mutable `TemplateRegistryEntry` as the
target; path, directory, object listing, URL, version label, or digest as
business authority; floating `latest`; runtime selection or automatic repin;
ordinary User selection among multiple active versions; offering deprecated
versions to new Tasks; breaking old locks during ordinary deprecation; treating
Disabled as deletion; accepting stale work after a Bundle disable; forcing all
assets into one package; inferring Bundles from duplicate bytes or paths;
putting catalog packages in Checkpoints; preserving startup disk sync or a path
compatibility facade; CI inheriting administrator power; bypassing scan or
license evidence; publishing Fill Templates; and creating a marketplace or
catalog-management UI.

Stable downstream inputs are:

- Durable Object receives platform-domain immutable packages, canonical
  manifests, typed references, retention, and exact-repair requirements;
- Task Orchestration receives selection tokens, stale-selection conflict,
  atomic Template Lock creation, and fixed retry and recovery semantics;
- Runtime Execution receives lock and input-manifest digests, opaque
  materialization capabilities, and the catalog safety epoch;
- Backup & Recovery receives the exact catalog inventory and current disable
  non-resurrection contract; and
- issue 17 receives the deterministic row and Task-lock conversion boundary and
  complete deletion test.

Superseded accepted decisions: none.

New decision-only tickets: none.

Remaining fog affecting the first Catalog Publication specification: none.
Concrete schema, serialized names, registry and scanner products, CI commands,
file allowlists, cache watermarks, and administrator presentation belong to
implementation specifications or adapters and do not reopen this contract.
