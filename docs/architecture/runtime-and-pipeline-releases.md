# Runtime and Pipeline Release Management

This document records the release, compatibility, and Task-lock decisions confirmed while resolving [GitHub issue 22](https://github.com/Vt00ls/SlideSmith/issues/22). [CONTEXT.md](../../CONTEXT.md) is authoritative for domain language, [ADR 0003](../adr/0003-package-core-skills-in-runtime-images.md) fixes Core Skill packaging, [ADR 0005](../adr/0005-select-controlled-versioned-pipelines-by-route.md) fixes Pipeline Version ownership, [ADR 0021](../adr/0021-pin-compatible-pipeline-and-runtime-releases-together.md) records the paired-lock decision, [task-orchestration.md](./task-orchestration.md) defines Task transition authority, and [runtime-execution.md](./runtime-execution.md) defines how each resulting Runtime Binding is admitted, executed, fenced, and evidenced.

The design fixes authority, identities, manifests, lifecycle, compatibility, pinning, revocation, retention, repair, recovery, and test seams. It does not select a registry vendor, signing product, CI system, schema, serialized protocol, rollout algorithm, administrator UI, or implementation sequence.

## Decision summary

`Release Management` is a deep module in the Platform Control Plane. It owns Pipeline Version and Runtime Release publication, positive Compatibility Approvals, lifecycle state, rollout selection policy, revocation, and the decision that supplies a Task's immutable Execution Lock.

A Pipeline Version and Runtime Release remain independently publishable. They become usable together only through an immutable Compatibility Approval over their exact manifest digests. When a Task's Route is determined, Task Orchestration atomically records one Execution Lock containing the selected Pipeline Version, Runtime Release, and Compatibility Approval. No later operation resolves another pair for that Task.

Ordinary rollout, rollback, deactivation, and deprecation change only which pair a new Task may pin. Revocation is a separate terminal trust decision: it is reserved for security, integrity, authorization, or platform-control contract failure and fences uncommitted work belonging to existing locks.

## Standing constraints

- The Platform Control Plane remains authoritative for Route, Execution Lock, Phase Run outcome, and commit decisions.
- Core Skill instructions, references, scripts, and compatible toolchain live inside a content-addressed Runtime Image. A mutable host Skill path or Task Workspace copy cannot override them.
- Pipeline Versions define Phases, dependencies, Confirmation Gates, contracts, retry eligibility, required runtime capabilities, and the post-publication manual-edit entry point.
- Runtime Releases implement declared capabilities but cannot add, remove, reorder, or bypass Pipeline controls.
- Enterprise V1 publishes only built-in Pipeline Definitions and administrator-controlled Runtime Releases. Users cannot publish a Pipeline Definition, Core Skill, executable extension, or Runtime Release.
- Retry, recovery, cancellation, and manual edit preserve every Task lock. Runtime, worker, deployment, and feature-flag state cannot silently change a locked Task.
- Runtime Execution, scheduling, Task Workspace Lifecycle, Artifact publication, Durable Object, backup, identity, usage, and observability remain separate authorities.

## Authority and module seam

| Fact or action | Authority | Adapter relationship |
| --- | --- | --- |
| Pipeline Definition stable identity and immutable Pipeline Version manifest | Release Management in Platform PostgreSQL | Candidate and administrator adapters submit intent; callers never write records directly |
| Runtime Release identity, manifest, lifecycle, and package references | Release Management in Platform PostgreSQL | OCI, Durable Object, signing, scan, and provenance adapters supply verified evidence |
| Compatibility Approval and resolved capability mapping | Release Management | Contract-suite and policy evaluators are internal adapters |
| Rollout selection policy and policy generation | Release Management | Administrator or controlled CI adapter submits an audited policy intent |
| Task Route, Execution Lock, Task revision, and Phase history | Task Orchestration | Release Management supplies a selection inside the authoritative Task transaction |
| Runtime Image bytes | OCI registry adapter | Tags and locators remain private; exact digests cross internal seams only as integrity evidence |
| Supplementary immutable package bytes | Durable Object | Release Management owns semantic lifecycle; Durable Object owns verified bytes |
| Runtime Run admission, process lifecycle, and execution evidence | Runtime Execution | Receives an intent-bound Runtime Binding and returns typed evidence |
| Recovery inventory and promotion gates | Backup & Recovery | Consumes authoritative release, lock, package, OCI, and revocation inventories |

Release Management hides candidate staging, artifact verification, manifest canonicalization, compatibility evaluation, lifecycle revisions, rollout policy, revocation fencing, retention, backup inventory, reconciliation, and repair behind a small intent-oriented interface. If the module were removed, this complexity would reappear in Task Orchestration, Runtime Execution, CI, workers, and deployment configuration; it therefore passes the deletion test.

## Identity and canonical manifests

Business identity and content integrity are distinct:

- a Pipeline Definition has one stable opaque identity and publishes immutable Pipeline Version identities;
- a Runtime Release has one opaque identity assigned when its approved manifest becomes authoritative;
- a Compatibility Approval has its own opaque identity and canonical digest;
- an Execution Lock belongs to one Task and has a canonical digest over its exact references and selection evidence;
- human-readable names, version strings, channels, and OCI tags are labels, never authority.

Every canonical digest carries its algorithm identity. Enterprise V1 requires SHA-256 support. Unknown algorithms, unknown manifest schema majors, non-canonical encodings, missing signatures, and digest disagreement fail closed.

### Pipeline Version manifest

An immutable Pipeline Version manifest binds at least:

- Pipeline Definition identity, Pipeline Version identity, Route, manifest schema, and canonical digest;
- stable Phase definition keys, dependencies, ordering, and entry and terminal rules;
- Confirmation Gates and the post-publication manual-edit entry point;
- each Phase's runner class and mutation or publication effect class;
- input, output, validation, and execution-evidence contract identities and digests;
- retry eligibility and policy, cancellation constraints, and required commit or publication evidence;
- every Runtime-bearing Phase's required capability key, acceptable capability-contract range, and execution-environment requirement keys;
- publication provenance, signing evidence, approval evidence, and immutable package references.

Zero-Runtime-Run rules, Confirmation Gates, validation, and publication remain Pipeline-controlled. A Runtime Release cannot claim those authorities through an extra capability.

### Runtime Release manifest

An immutable Runtime Release manifest binds at least:

- Runtime Release identity, manifest schema, canonical digest, and publication provenance;
- exact Core Skill identity and digest embedded in the Runtime Image;
- Runtime Image OCI index digest and each supported platform's exact manifest and configuration digests;
- executor, guest, and daemon compatibility set plus executor-contract version;
- toolchain inventory, SBOM, build provenance, scan evidence, and signatures;
- provided capability keys, exact capability-contract versions, and input, output, and evidence schema digests;
- supported operating-system, architecture, driver, readiness, and execution-policy requirement keys;
- supplementary immutable package identities and digests.

One Runtime Release may contain several platform variants only when the same release contract suite proves each declared variant. The Task pins the Runtime Release manifest; each Runtime Run records the exact platform image digest it used.

Changing a Phase graph, Gate, retry rule, contract, or manual-edit entry creates a new Pipeline Version. Changing the Core Skill, toolchain, executor contract, Runtime Image, or capability implementation creates a new Runtime Release.

## Compatibility Approval

Compatibility is an explicit positive fact, not a default assumption. A Compatibility Approval binds:

- exact Pipeline Version and Runtime Release identities and manifest digests;
- every required capability key and the exact provided contract selected for it;
- the resolved capability matrix digest;
- contract-suite evidence for every supported platform and executor variant;
- evaluator and policy versions, approval actor, time, canonical digest, and signature.

A Pipeline requirement may use a version range to identify candidates, but publication resolves it to exact contracts. A Task pins that resolution through the Compatibility Approval. An extra Runtime capability is inert unless a Phase in the pinned Pipeline Version names it.

A Compatibility Approval can be revoked independently when only that pair has been disproved. Revoking a pair does not imply that either member is unsafe with every other approved pair.

## Candidate and publication lifecycle

A Draft Candidate is mutable staging and is not yet a Runtime Release or Pipeline Version under the domain definitions. Approval creates the immutable business identity.

| State | Meaning | New Task pin | Existing Execution Lock |
| --- | --- | --- | --- |
| Draft Candidate | Mutable candidate content and evidence | Never | Not applicable |
| Approved | Immutable, verified, signed, and approved but inactive | No | May continue if already referenced |
| Active | Eligible for rollout policy | Only through an active compatible pair selected by policy | Continues |
| Deprecated | Retained for existing locks but not offered to new Tasks | No | Continues |
| Revoked | Trust is invalid; terminal | No | Further admission and uncommitted effects are fenced |

Normal transitions are:

- Draft Candidate to Approved after complete verification;
- Approved to Active and Active to Approved for activation or ordinary rollback;
- Approved or Active to Deprecated;
- Deprecated to Active only through a new, audited activation decision that reruns the current validation gate;
- Approved, Active, or Deprecated to Revoked as a terminal transition.

Multiple Pipeline Versions and Runtime Releases may be Active simultaneously. Active status is necessary but not sufficient for new selection: both members must be Active, their Compatibility Approval must be valid, and the versioned rollout policy must select the exact pair.

## Publication and activation protocol

Registry and object storage do not share a transaction with PostgreSQL. Publication therefore uses verified intents and receipts:

1. A candidate intent records an operation identity, expected revision, purpose, actor, and expiry.
2. OCI and Durable Object adapters write immutable content and return strict digest, size, generation, signature, and durability evidence.
3. Manifest canonicalization verifies all transitive references, platform variants, signatures, provenance, and policy evidence.
4. The PostgreSQL approval transaction creates the immutable Pipeline Version or Runtime Release, authoritative audit fact, and reconciliation outbox. This transaction is the publication linearization point.
5. Contract evaluation creates an immutable Compatibility Approval in a separate idempotent decision.
6. Activation and rollout-policy changes use lifecycle revision and policy-generation compare-and-swap.

A crash before an approval transaction leaves no release. A crash after commit but before response is recovered by replaying the operation identity. An external artifact written without a committed candidate or approval remains inaccessible staging evidence; registry listing or a matching tag cannot adopt it as a release.

## Execution Lock and Route transaction

Route classification is a pre-Pipeline Task decision. A Route proposal can be calculated outside the Task transaction, but it must bind the authoritative Task input revision and be revalidated before acceptance.

Task Orchestration then uses one PostgreSQL transaction to:

1. validate the expected Task revision, Route evidence, and recovery mode;
2. read and fence the current rollout-policy generation;
3. select one policy-listed pair whose Pipeline Version, Runtime Release, and Compatibility Approval are eligible;
4. verify exact manifests, resolved capabilities, and package receipts;
5. atomically record Route, Execution Lock, Task revision, decision, audit fact, and any enactment outbox records.

That transaction is the Task pin linearization point. The first consuming Phase Run can be created only after the lock inside the same transaction or by a later accepted Task Orchestration decision.

An Execution Lock binds at least:

- Task identity, Route, and authoritative input revision;
- Pipeline Version identity and manifest digest;
- Runtime Release identity and manifest digest;
- Compatibility Approval identity and digest;
- resolved capability-matrix digest;
- rollout-policy generation;
- Task revision, lock time, and canonical lock digest.

Concurrent rollout changes and Task pins are ordered by the policy generation and transaction. If a policy change commits first, the Task resolves under the new generation. If the Task lock commits first, later ordinary rollout changes cannot affect it. Exact replay returns the committed lock; reuse of an idempotency identity with different inputs is a typed conflict.

If no eligible pair exists, Route start fails closed. The platform cannot switch Route, downgrade to another Runner Profile, select an unlisted fallback, or resolve `latest`.

## Rollout, rollback, feature flags, and emergency control

The rollout selection policy is a versioned authoritative fact containing explicit eligible pairs and their selection rules. Percentage, cohort, or ordered-preference algorithms are adapter or SPEC choices, but selection must be deterministic for a Task and policy generation, auditable, and idempotently replayable.

Ordinary rollout control affects only new Tasks:

- removing a pair from the selection policy;
- changing the preferred pair or cohort allocation;
- deactivating a version back to Approved;
- deprecating a version.

Existing Tasks retain their Execution Locks. An unavailable image or node does not authorize repinning; the Task waits or fails with typed, retryable or terminal evidence according to the existing contract.

A mutable feature flag may choose which rollout-policy generation applies to a new Task. It cannot alter a locked Phase graph, capability, validation rule, Gate, or output contract. A semantic behavior change requires a new Pipeline Version, Runtime Release, or immutable Task input. Operational security, recovery, and capacity fences may block an existing Task but never replace its lock or silently change its behavior.

Revocation is reserved for a credible condition where continued execution is no longer trustworthy:

- manifest, signature, image, package, or provenance integrity cannot be proved;
- the Core Skill, executor, or capability has a security defect;
- a Pipeline can bypass authorization, Confirmation Gates, validation, publication, or other platform controls;
- a Compatibility Approval has been disproved;
- accepting the result would require a new trust assumption or unverifiable evidence.

The revocation transaction records the reason and affected scope, advances a release-safety epoch, and emits fenced reconciliation work. Runtime admission, Runtime evidence acceptance, C04 commit, and Artifact publication must reject a stale epoch. If a C04 commit linearized before revocation, Task Orchestration records the exact Revision and Checkpoint before stopping later work. A proposal that had not committed is fenced and discarded.

Revocation does not rewrite an Execution Lock and does not automatically delete or suppress an already published Artifact Version. Quarantining an existing business publication is a separate security-incident decision with its own human authority.

## Retry, recovery, cancellation, and manual edit

- Retry creates a new Phase Run attempt of the same Phase definition and uses the same Execution Lock.
- Recovery restores the lock and permitted Checkpoint from authoritative recovery state; it never reads the current rollout default.
- Cancellation preserves the lock as Task history and fences late Runtime and C04 evidence.
- Post-publication manual edit enters the manual-edit graph in the same pinned Pipeline Version and uses the same Runtime Release and Compatibility Approval.
- A Task whose pinned graph or Runtime Release lacks a required manual-edit capability fails closed. Continuing with a newer release requires a new Task rather than an in-place upgrade.
- Missing or corrupt release content can be repaired only with bytes that match the original digest, size, manifest, and signature evidence.

## Minimal caller interface

The following capability families describe the interface; final method and serialization names belong to implementation design.

| Caller | Intent | Result and knowledge boundary |
| --- | --- | --- |
| Administrator or controlled CI adapter | submit candidate, approve, activate, deactivate, deprecate, revoke, or change rollout policy | Typed release decision, lifecycle revision, policy generation, evidence and audit identities |
| Task Orchestration | select a compatible pair for an accepted Route inside the Task transaction | Execution Lock and immutable Pipeline Contract; no tag, locator, registry, or candidate detail |
| Runtime Execution | authorize one Phase capability for an exact lock, node attestation, Runtime Run, operation, and fence | Intent-bound Runtime Binding with exact capability contract, opaque artifact handles, permitted platform digests, executor contract, evidence requirements, and safety epoch |

Task Orchestration reads the Pipeline Contract to decide Phase progression, Gate, retry, validation, and manual-edit entry. It does not inspect Runtime images or rollout candidates.

Runtime Execution receives only the current capability binding. It does not read the complete Pipeline graph, selection policy, other releases, registry paths, object locators, or administrator state. Its evidence binds Task, Phase Run, Runtime Run, operation, lease and fence, Execution Lock digest, capability contract, actual image digest, execution node, executor, safety epoch, and terminal result.

Production uses an owned transport adapter between the Platform Control Plane and Runtime Execution. In-memory and local adapters provide deterministic test seams. OCI registry, Durable Object, signing or KMS, scan and provenance, backup inventory, PostgreSQL, audit, and transport are internal seams rather than additions to the caller interface.

## Idempotency, concurrency, failure, and repair semantics

- Every publication, lifecycle, rollout, compatibility, pin, and revocation intent carries a scope-bound operation identity, expected revision, and canonical request digest.
- Exact replay returns the original decision. Same identity with different content is a typed conflict.
- Lifecycle and policy changes use compare-and-swap. A stale administrator, worker, or reconciler cannot overwrite a newer decision.
- Unknown manifest or capability schemas, missing receipts, partial packages, duplicate inconsistent evidence, signature failure, digest mismatch, revoked identity, stale safety epoch, and node incompatibility fail closed.
- A registry tag, object listing, file path, process state, or telemetry signal never proves a release, lock, compatibility, or repair fact.
- Repair creates or selects an exact verified physical generation. It cannot alter an approved manifest or expected digest to match replacement bytes.
- Errors distinguish authorization denial, no eligible pair, lock conflict, revocation, integrity failure, unavailable artifact, incompatible node, recovery read-only, and retryable transport. No error permits floating fallback.

## Authorization, audit, and operational evidence

Only an authenticated Platform Administrator authority can approve, activate, deactivate, deprecate, revoke, or change rollout policy. A build or CI identity can submit candidate artifacts and machine evidence but cannot inherit administrator authority. Release operations grant no User-content access and never use break-glass implicitly.

An authoritative audit fact commits with every release decision. It binds at least:

- actor and mutually exclusive human or machine authority;
- reason or ticket, operation identity, request digest, and expected revision;
- before and after lifecycle or policy revision;
- Pipeline, Runtime, Compatibility Approval, manifest, and package digests;
- rollout-policy diff and affected Route or locked-Task counts;
- timestamps, validation and signature evidence roots, adapter identities, retries, and reconciliation result.

Metrics, traces, logs, and an external audit sink project these facts and may retry without becoming another state machine. Manifests and telemetry cannot contain User content, secrets, registry credentials, object locators, or unrestricted package handles.

## Retention, reclamation, backup, and restore

Execution Locks, approved manifests, Compatibility Approvals, lifecycle history, revocations, authoritative audit facts, and release tombstones are retained business records.

Runtime Images and supplementary packages cannot be reclaimed while referenced by any Active or Deprecated release, Execution Lock, rollout or rollback pin, integrity incident, or retained Recovery Point. Revocation is not deletion authority. Once no reference, lease, incident, or Recovery Point needs a package, physical reclamation may begin only after a non-zero configurable grace period; shortening policy cannot retroactively accelerate existing eligibility. Failure becomes Cleanup Debt.

Release metadata and Execution Locks belong to the PostgreSQL recovery set. Required OCI images, Pipeline packages, Runtime supplementary packages, compatibility evidence, and revocation inventory belong to the joint Recovery Point and the eight-hour full-operation gate. Release activation and rollout-policy mutation are forbidden in `recovery-degraded/read-only` mode.

Restore must reconcile the independent immutable audit domain's current revocation inventory before enabling Runtime admission. Restoring an older database point cannot reactivate a revoked Pipeline Version, Runtime Release, or Compatibility Approval. A missing exact release dependency blocks the full-operation promotion gate; it is never replaced with a newer release.

## Highest-level scenarios and adapter contracts

The Release Management interface is the highest-level test seam. A deterministic harness covers:

- duplicate candidate and approval intent, same-key/different-payload conflict, partial upload, digest or signature failure, and response loss around approval commit;
- concurrent approve, activate, deactivate, deprecate, policy update, pin, and revocation;
- multiple Active pairs, deterministic rollout, and ordinary rollback affecting only new Tasks;
- missing, extra, unknown, incompatible, or revoked capability mappings;
- OCI multi-platform variants, wrong platform, missing image, registry tag movement, and exact repair;
- fixed Execution Locks across retry, recovery, cancellation, and manual edit;
- revocation before Runtime admission, during execution, before and after C04 commit, and before Artifact publication;
- stale or cross-Task Runtime evidence, late success, node or executor drift, and safety-epoch mismatch;
- backup and restore, revocation non-resurrection, reference retention, package reclamation, and Cleanup Debt;
- absence of tag, path, registry, vendor, credential, and content leakage across the interface.

PostgreSQL, OCI registry, Durable Object, signing or KMS, scan and provenance, audit, backup inventory, and owned-transport adapters receive black-box contracts for idempotency, immutable receipts, acknowledgement loss, exact repair, authorization, and non-leakage. Tests assert identities, manifests, decisions, locks, bindings, evidence, and lifecycle outcomes rather than schema, tag, path, vendor command, or log text.

## Hard cutover and deletion test

The current Runner Profile, Runner Profile source and timestamp, route-specific JSON, feature capability snapshot, worker-compiled Phase registry, environment-selected Runtime Image, Task Workspace runtime manifest, copied Skill tree, Runtime session, and workspace path do not constitute a Pipeline Version, Runtime Release, Compatibility Approval, or Execution Lock.

Migration is replace-not-layer:

- a new Task cannot enter its first Route-specific Phase without an authoritative Execution Lock;
- no lock is inferred from environment configuration, mutable tags, current workspace content, session directories, latest Runtime Run, Runner Profile, or feature snapshots;
- legacy records can map to a lock only when exact, trusted release evidence exists;
- legacy non-terminal Tasks without such evidence terminate as non-recoverable under ADR 0016;
- retained Artifact Versions and execution history remain readable historical business records;
- no `latest`, path, Runner Profile, or capability-snapshot compatibility facade survives the cutover.

Once the interface and adapter contracts exist, the target architecture deletes worker-compiled Route phase authority, environment-selected Runtime behavior, mutable Skill copies, and caller-owned compatibility checks rather than wrapping them behind Release Management.

## Rejected alternatives and downstream inputs

Rejected alternatives include floating `latest` or mutable tags, semver-only runtime negotiation, one Runtime Release hard-wired into every Pipeline Version, per-Phase Runtime Release selection, automatic upgrade of locked Tasks, feature snapshots as release authority, ordinary rollback implemented as revocation, revocation that affects only new Tasks, Runtime Execution reading the Pipeline graph or rollout registry, and deleting revoked images as a substitute for fencing and audit.

Stable downstream inputs are:

- the resolved [Runtime Execution contract](./runtime-execution.md) receives exact Runtime Bindings, capability and image digests, executor requirements, safety epochs, and evidence bindings; Runtime Execution never chooses a release;
- the resolved [Scheduler contract](./scheduling-and-capacity-admission.md) schedules only onto nodes satisfying the binding and cannot use capacity fallback to rewrite an Execution Lock;
- issue 17 never fabricates an Execution Lock from legacy environment, profile, tag, path, session, or recent-run evidence;
- issue 13 consumes publication, compatibility, pin, rollout, revocation, repair, reclamation, and Cleanup Debt facts;
- Backup & Recovery includes Execution Locks and required OCI and package inventories in the joint point;
- issue 23 may reuse candidate, approval, activation, retention, and audit patterns while keeping Template Lock authority separate.

Concrete Runtime Execution driver acceptance, execution-node resource values, provider usage receipts, telemetry backend, vendor choices, schema, serialized method names, rollout algorithm, CI implementation, and administrator UI remain in their named downstream decisions, adapter acceptance, or implementation design. They do not reopen this module interface.

Remaining fog affecting the first Runtime and Pipeline release implementation specification: none.
