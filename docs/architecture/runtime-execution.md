# Runtime Execution

This document records the Runtime Execution, Agent Worker, Tool Worker, and
Sandbox Lease decisions resolved in
[GitHub issue 24](https://github.com/Vt00ls/SlideSmith/issues/24).
[CONTEXT.md](https://github.com/Vt00ls/SlideSmith/blob/codex/ARCH-01-enterprise-platform-review/CONTEXT.md)
is authoritative for domain language,
[ADR 0022](https://github.com/Vt00ls/SlideSmith/blob/codex/ARCH-01-enterprise-platform-review/docs/adr/0022-run-runtime-capabilities-through-fenced-sandbox-leases.md)
records the durable module choice,
[ADR 0029](../adr/0029-bind-runtime-admission-once-before-post-lease-prerequisites.md)
proposes the superseding admission, maintenance, logical/physical capacity,
and post-lease Runtime View ordering; it becomes effective only after explicit
Owner approval and merge,
[task-orchestration.md](https://github.com/Vt00ls/SlideSmith/blob/codex/ARCH-01-enterprise-platform-review/docs/architecture/task-orchestration.md)
defines Phase Run and Runtime Run membership authority,
[runtime-and-pipeline-releases.md](https://github.com/Vt00ls/SlideSmith/blob/codex/ARCH-01-enterprise-platform-review/docs/architecture/runtime-and-pipeline-releases.md)
defines Runtime Bindings,
[catalog-template-publication.md](./catalog-template-publication.md) defines
Template Lock materialization and catalog safety epochs,
[llm-gateway-and-usage-accounting.md](./llm-gateway-and-usage-accounting.md)
defines Gateway Grants, provider egress, Usage Receipts, and settlement,
[scheduling-and-capacity-admission.md](./scheduling-and-capacity-admission.md)
defines Work Items, fairness, Resource Classes, concurrency, placement, and
Admission Grants, and
[task-workspace-lifecycle.md](https://github.com/Vt00ls/SlideSmith/blob/codex/ARCH-01-enterprise-platform-review/docs/architecture/task-workspace-lifecycle.md)
defines Runtime View and commit authority, and
[observability-audit-and-cleanup-debt.md](./observability-audit-and-cleanup-debt.md)
defines correlation, telemetry, audit, alert, and retention contracts.

The design fixes authority, interface depth, worker roles, execution and lease
state, fencing, evidence, security, reconciliation, adapter contracts, and the
legacy deletion test. It does not define a schema, serialized protocol,
concrete production resource values, LLM ledger, telemetry vendor,
implementation sequence, or production sandbox product.

## Decision summary

`Runtime Execution` is a deep module with a control-side authority in the
Platform Control Plane and owned execution adapters in the Execution Data
Plane. The control side owns authoritative Runtime Run execution state,
Sandbox Leases, fences, deadlines, cancellation, node execution facts, and
verified Runtime Evidence. The data side executes an exact Runtime Binding and
returns evidence; it cannot decide Task, Phase Run, Task Workspace, Checkpoint,
Artifact Version, or release state.

Agent Compose, a Tool executor, an Execution Node runtime, a queue, polling,
and callbacks are replaceable adapters. None of their project, run, session,
sandbox, process, path, or transport identities are business authority.

Production Agent and Tool execution is treated as hostile. No driver is safe
by name. An exact driver, host, kernel or runtime, mount, network, credential,
and reset configuration must pass an independent threat-model and hardening
acceptance before it can serve production work. Failure to prove the required
Execution Policy fails admission closed. The current Docker, CLI, shared
socket, and shared path topology is development or test evidence only; this
decision does not preselect Docker, BoxLite, Microsandbox, or another product
for production.

A successful Runtime Run means only that one approved runtime capability
invocation produced verified terminal execution evidence. Phase validation,
C04 commit, and Artifact publication remain separate decisions.

## Standing constraints

- The Platform Control Plane remains authoritative for Task, Phase Run and
  Runtime Run relationships, release locks, evidence acceptance, and commit
  decisions.
- Task Orchestration creates the Runtime Run identity and attaches it to one
  Phase Run before delivery. A worker never creates an authoritative Phase Run
  or Runtime Run.
- Every Runtime Run belongs to exactly one Phase Run and acquires at most one
  independent Sandbox Lease. Node loss never moves that run to a second lease.
- Runtime Execution receives only an exact, capability-scoped Runtime Binding.
  It never selects a Pipeline Version, Runtime Release, rollout policy, Phase,
  or fallback release.
- A Generation Runtime Run receives the exact Template Lock digest, immutable
  Template Version and Resource Bundle input manifest, and current catalog
  safety epoch. It never reads the current catalog pointer, selects another
  version, or accepts a path-bearing fallback.
- A mutating Runtime Run uses one isolated C04 Runtime View. Its output remains
  a proposal until independent Phase validation succeeds and C04 accepts an
  exact fenced commit.
- Immutable inputs, Source Material, packages, Task Workspace state, outputs,
  and publications remain behind their owning modules and opaque capability
  seams.
- Scheduling, quota and usage settlement, observability, C04, publication,
  release management, identity, Durable Object, and backup remain separate
  authorities.

## Authority and ownership matrix

| Fact or action | Authority | Runtime Execution relationship |
| --- | --- | --- |
| Task revision, Phase cursor, Phase Run identity and outcome, Runtime Run membership | Task Orchestration | Receives a durable enactment; returns typed Runtime Evidence |
| Runtime Binding, exact capability contract, image set, executor requirements, safety epoch | Release Management | Revalidates the intent-bound binding; never selects or changes it |
| Template Lock, exact package closure, catalog eligibility, and catalog safety epoch | Catalog Publication and Task Orchestration | Revalidates an opaque materialization authorization; never reads catalog listing state or repins |
| Runtime Run execution state, deadline, cancellation, terminal outcome, and evidence acceptance | Runtime Execution in Platform PostgreSQL | Owns through its command and reconciliation seam |
| Sandbox Lease identity, node binding, fence, expiry, revoke, release, and containment evidence | Runtime Execution | Owns the lease lifecycle and enforcement contract |
| Node readiness, attested capabilities, current lease occupancy, containment and reset facts | Runtime Execution | Supplies truthful capacity facts to scheduling |
| Queue order, Personal Workspace fairness, Resource Classes, concurrency, placement, and admission policy | [Scheduler](./scheduling-and-capacity-admission.md) | Supplies an Admission Grant; cannot mutate a Runtime Run outcome |
| Runtime View, Task Workspace bytes, Revision, Checkpoint, commit, discard, and cleanup | C04 | Uses one opaque Runtime View capability; cannot commit it |
| Immutable bytes and node materialization | Durable Object and C04 | Receives opaque verified capabilities; never receives object-store credentials |
| User and machine authority | Identity & Ownership | Validates Task, Personal Workspace, generation, purpose, and expiry |
| Phase validation | Platform validator | Consumes output proposal and Runtime Evidence independently of the worker |
| Provider egress, Usage Receipt issuance, ledger settlement and Quota Reservation | LLM Gateway and Usage Accounting | Correlates Gateway Grants and receipt references; Runtime Execution does not call providers directly, invent usage, or settle it |
| Logs, metrics, traces, and external audit projections | [Observability and audit](./observability-audit-and-cleanup-debt.md) | Projects authoritative identities and facts; never drives state |

Runtime Execution owns truthful execution capacity facts and lease enforcement;
the Scheduler owns policy and admission. An unavailable or incompatible node
can delay or reject admission, but cannot repin the Task or weaken the Runtime
Binding.

## Minimal external interface

The intent families are:

```text
Execute(StartRuntimeRun | CancelRuntimeRun)
  -> RuntimeDecision

Inspect(RuntimeRunRef)
  -> RuntimeSnapshot
```

The final method and wire names belong to implementation design. The semantic
boundary does not. `RuntimeDecision` reports a durable acceptance or rejection,
the Runtime Run revision and state, stable operation identities, and any
evidence identity already accepted. It does not imply synchronous completion.
Terminal evidence is emitted through an owned evidence adapter and is also
available through the query seam.

A start intent binds at least:

- Personal Workspace, Task, Phase Run, existing Runtime Run, attempt, Task
  revision, and machine-authority generation;
- a stable, scope-bound operation or idempotency identity and canonical
  request digest;
- the exact Runtime Binding, Execution Lock digest, capability contract,
  allowed platform images, executor contract, and release-safety epoch;
- the exact Template Lock and closure-root digests when required, verified
  immutable package-input manifest, and catalog safety epoch;
- Agent Worker or Tool Worker class;
- an immutable input manifest, output contract, and evidence contract;
- read-only or mutating effect; a mutating run binds one exact C04
  `RuntimeViewRequirement` that can be fixed by Task Orchestration, not a
  Runtime View capability that cannot exist before a Sandbox Lease;
- resource-class requirement, Execution Policy, deadline, cancellation policy,
  secret and network-policy references, and non-authoritative trace context.
- an active Phase Run Quota Reservation and an opaque Gateway policy reference
  whenever the capability may use an LLM or generative-image provider.

It never carries a shell command, host path, mount, object key, bucket,
registry locator, provider credential, Agent Compose project or session path,
or floating release reference.

Every command has a canonical request digest. Exact replay returns the
original decision and current snapshot. Reusing the identity with different
content is a typed conflict. One Runtime Run accepts at most one canonical
start payload. An acknowledgement loss replays the same operation; a true
execution retry requires Task Orchestration to create a new Runtime Run.

The public seam is intentionally unchanged by node and cleanup operations.
Node-execution fences, containment/reset confirmation, and C03-owned Cleanup
Debt resolution or exception expiry enter the separate protected operational
interface `RuntimeMaintenance`. It accepts only closed operational intents with
typed Scheduler, security, recovery, or cleanup authority and reason-bound
mandatory audit. It cannot start or cancel a Runtime Run, mutate Task/Phase
state, alter Scheduler counters, resolve C04 debt, or select a production site
policy. Content-free administrator diagnostics use the read-only operational
diagnostics seam defined by Observability, not new `Execute` or `Inspect`
variants. Private worker/evidence protocols still converge on the same C03
invariant engine without becoming public mutation authority.

`RuntimeSnapshot` is a versioned closed result. An unknown request or evidence
major version fails closed before ownership or existence can be disclosed; the
server never silently interprets it as the current major. There is no implicit
downgrade. A caller may explicitly request a still-supported older projection
only when a registered renderer can represent every authoritative state,
fence, unknown/cleanup disposition, and security-relevant fact without loss;
otherwise inspection returns `unsupported_schema`. Minor-version additions
must be optional, content-free projections and cannot change decision
semantics. An unknown required field or result variant fails closed.

## One admission path and grant binding

Runtime admission has one ordering and one authority:

```text
Task Orchestration decision/outbox + Scheduler Work Item atomic commit
-> Scheduler ClaimAndAdmit, logical counters, and unbound Admission Grant
-> authenticated delivery of unchanged canonical start plus grant
-> C03 Start acceptance/revalidation
   + Scheduler Work Item Accepted
   + Admission Grant Bound
-> Sandbox Lease
-> C04 Runtime View and Gateway prerequisites
-> Execution Capsule
-> worker dispatch
```

Task Orchestration creates the Runtime Run and a complete canonical
`StartRuntimeRun` envelope. Its OperationID and payload digest are also stored
on the Scheduler Work Item in the same Platform PostgreSQL transaction. The
Scheduler Work Item has its own identity but cannot rewrite, supplement, or
re-canonicalize the Task enactment at delivery.

`ClaimAndAdmit` is the only admission decision. In one Scheduler transaction it
selects the exact Work Item and node, applies fairness and current policy,
reserves logical global/Workspace/capability/Resource Class counters, records a
Delivery Claim, and creates an unbound Admission Grant. The grant binds at
least Work Item identity and generation, Task enactment OperationID, canonical
C03 start digest, Runtime Run, selected node and node-capacity generation,
Resource Class, Execution Policy, Scheduler epoch/policy, current Reservation
binding when applicable, grant identity/generation, and expiry. The grant
authorizes this fixed payload; its identity and generation are not fields that
may change the canonical Task/C03 start digest.

Authenticated delivery presents two separately verified envelopes: the
unchanged canonical start and the current unbound grant. C03 revalidates both.
Its start-acceptance PostgreSQL transaction uses a restricted Scheduler
transactional participant so C03 start acceptance, downstream Work Item
`Accepted`, and Admission Grant `Bound` are one linearization point. The grant
binds to that exact C03 Decision, Runtime Run, operation, digest, and accepted
revision. The same transaction creates a C03 Runtime fence that exists even if
no Sandbox Lease is ever granted and fixes `LeaseAcquireBy` no later than the
already presented grant expiry or Runtime deadline. It may schedule private
lease/reconciliation work, but
it must not create an admission-enactment outbox, call `ClaimAndAdmit` again, or
manufacture a second admission state machine.

The failure and replay rules are:

- an unbound grant that expires or becomes stale cannot be accepted or bound;
  Scheduler releases its logical reservations by grant-generation CAS;
- claim, delivery, or acknowledgement loss first inspects/replays the original
  C03 operation. If C03 accepted it, replay returns the original decision and
  Scheduler observes the already atomic Accepted/Bound facts;
- only when C03 has no accepted start and the previous grant is proved
  unbound and expired/released may the same Work Item be admitted again. It
  keeps the Task operation and payload digest and receives a new grant identity
  and higher generation;
- C03 journals a stale-grant rejection under the grant attempt. Replaying the
  same start/digest and same grant generation returns that rejection. The same
  canonical start with a newer current grant generation is a fresh admission
  proof, not a payload rebind, and may be evaluated only while no start is
  accepted;
- if start is already accepted, presenting a newer grant generation returns
  the original C03 decision and marks the extra grant unnecessary/stale for
  Scheduler release; it never rebinds the accepted start;
- after Accepted/Bound, the Work Item is not re-admitted, the bound grant never
  returns to unbound, and no later generation is valid for that Runtime Run. A
  true execution retry requires a new Runtime Run and Task enactment.

### Post-bind, pre-lease failure contract

C03 owns the complete interval between Accepted/Bound and the lease-grant
transaction. The stable lease-acquire OperationID and canonical digest are
persisted before an attempt; retry and recovery always inspect or replay that
operation. Scheduler delivery has already completed and cannot reconsider the
Work Item.

| Observation before lease commit | C03 state and terminal result | Replay and capacity disposition |
| --- | --- | --- |
| Same node-capacity generation is temporarily unavailable, or Reservation/authority validation is explicitly retryable or ambiguous | `WaitingForLease` or `Reconciling` only while the Runtime deadline and `LeaseAcquireBy` remain current | Revalidate the same bound grant and same lease-acquire operation. Keep logical counters and the selected-node scheduling reservation; do not re-admit. |
| Node-capacity generation changed, node is permanently ineligible, or Reservation, authorization, Resource Class, Execution Policy, Scheduler policy/epoch, release/catalog safety, or other immutable binding is definitively stale/revoked | Terminal `Rejected` with a safe reason; never `Lost` | Atomically advance the Runtime fence and emit `RuntimeFencedOrTerminal` plus `NoLeasePhysicalDisposition`. Exact Start/Inspect replay returns the same terminal decision. |
| Bound grant/lease-acquire authority expires before the Runtime deadline | Terminal `Rejected` with `admission_authority_expired_before_lease` | Same no-lease terminal transaction; the grant does not become unbound and the Work Item is not eligible again. |
| Authorized cancel wins before lease commit | Terminal `Cancelled` | Advance the Runtime fence and emit the same two no-lease dispositions; no stop, C04 discard, or physical release is fabricated. |
| Runtime deadline wins before lease commit | Terminal `TimedOut` | Advance the Runtime fence and emit the same two no-lease dispositions; grant expiry cannot rewrite this outcome after the deadline. |
| C03 crashes before or during lease acquisition | No outcome is inferred from the crash | If acceptance did not commit, Scheduler still owns the unbound path. If Accepted/Bound committed, inspect PostgreSQL and replay the same lease-acquire operation. Transaction state proves zero or one lease. Until that proof, keep the logical/node reservation and emit neither no-lease nor release-ready evidence. |

The no-lease terminal transaction atomically persists the outcome, Runtime
revision/fence, mandatory audit, `RuntimeFencedOrTerminal`, and
`NoLeasePhysicalDisposition` with its owned outbox. The latter binds Work Item,
grant generation, Runtime/operation/start digest, selected node and
node-capacity generation, stable lease-acquire operation, and an explicit
proof that no Sandbox Lease, physical occupancy, process, secret/network
capability, Runtime View, Execution Capsule, or worker dispatch committed. It
does not claim the selected node is Ready, contained, reset, or reusable.

Scheduler alone decides admission and logical counters. C03 only revalidates a
grant, accepts the exact start, owns lease/physical truth, and returns evidence.
Task Orchestration, workers, brokers, node adapters, Gateway, and C04 cannot
mint, replace, bind, or release an Admission Grant.

## Agent Worker and Tool Worker

| Responsibility | Agent Worker | Tool Worker |
| --- | --- | --- |
| Capability | Executes an approved agent capability, including model interaction and bundled internal tools | Executes a declared deterministic or constrained tool capability from the Runtime Release |
| Invocation | Intent or prompt plus immutable inputs | Capability key plus typed parameters and immutable inputs |
| Entrypoint | Resolved privately from the exact Runtime Binding | Resolved privately from the exact Runtime Binding; never arbitrary caller shell |
| Model usage | May produce several Gateway Calls, each with one or more Gateway Attempts and Usage Receipt references | None by default; approved provider use still crosses the LLM Gateway and usage seam |
| Output | Untrusted proposal under the declared output contract | Untrusted proposal under the declared output contract |
| Business authority | None | None |

Both worker classes share the same private protocol families:

```text
Accept(ExecutionCapsule) -> OperationAck
Heartbeat(LeaseFence) -> LeaseDecision
Observe(OperationRef, Cursor) -> WorkerObservation
Stop(StopIntent) -> StopAck
```

They share identity, lease, deadline, terminal-state, error, evidence, and
non-leakage semantics. They need not share a binary, queue, adapter, node pool,
or scaling policy.

One Pipeline-declared, top-level Execution Data Plane capability invocation is
one Runtime Run. Model calls, subprocesses, and bundled tool calls made inside
an Agent Worker remain evidence under that Runtime Run. They become separate
Runtime Runs only when the pinned Pipeline explicitly requests independent
capability invocations. Confirmation Gates, Platform validation, publication,
and pure control-plane decisions may use zero Runtime Runs.

## Runtime Run state and linearization

Before C03 acceptance, queueing, claiming, and admission are Scheduler Work
Item states rather than C03 Runtime states. C03's semantic non-terminal
progression begins only after the atomic Start/Accepted/Bound transaction:

```text
Accepted -> WaitingForLease <-> Reconciling
         -> Rejected | Cancelled | TimedOut       (no lease committed)
         -> PreparingPrerequisites -> Starting -> Running
                                    -> Reconciling | Stopping
```

`PreparingPrerequisites` is post-lease and includes the exact C04 Runtime View
and Gateway prerequisites required by the run. Serialized state names may
differ, but they cannot put admission after C03 start acceptance or dispatch a
worker before prerequisites are durable.

The immutable terminal outcomes are:

- `Succeeded`: verified terminal capability success;
- `Failed`: verified capability or executor failure after acceptance;
- `Cancelled`: cancellation fenced the run before success linearized;
- `TimedOut`: the Platform deadline fenced the run;
- `Lost`: the worker, daemon, node, or operation can no longer be reconciled
  after a lease or possible process effect existed and its lease is fenced;
- `Rejected`: a non-retryable authorization, binding, policy, compatibility,
  or integrity condition prevented execution.

An unknown transport result is not terminal. It enters `Reconciling`. A
terminal Runtime Run does not prove Phase success, C04 commit or discard,
sandbox cleanup, or capacity release; those facts retain separate states and
authorities.

The command-acceptance PostgreSQL transaction described above is the start,
Work Item Accepted, and Admission Grant Bound linearization point. The later
lease-grant transaction binds the selected node and fence before any process
may start. It does not repeat admission. A pre-lease `Rejected`, `Cancelled`,
or `TimedOut` result linearizes through the authoritative no-lease terminal
transaction above and requires the Runtime fence, not a nonexistent lease
fence or worker evidence. After lease grant, a terminal adapter result
linearizes only when authenticated evidence matching the current operation,
Runtime Binding, Task revision, lease fence, and safety epoch commits in
PostgreSQL.

Cancellation, timeout, and revocation first advance the authoritative fence
and then request downstream termination. Terminal evidence and a fence use
compare-and-swap: if verified success commits first, later Runtime cancellation
is a no-op for that run; if the fence commits first, a late success is retained
only as diagnostic evidence. Task cancellation and C04 commit still apply
their own independent ordering rules.

## Sandbox Lease and capacity semantics

A Runtime Run may acquire zero or one time-bounded, exclusive Sandbox Lease.
The lease binds Runtime Run, Execution Node, resource class, Execution Policy,
deadline, release-safety epoch, lease generation, and an unforgeable fence.

- Acquire occurs only after a valid Runtime Binding and Scheduler Admission
  Grant are revalidated against current node facts.
- Renewal is accepted only from the current owned worker or node authority and
  cannot extend beyond the Runtime deadline, a cancellation or revocation
  fence, a stale safety epoch, or node quarantine.
- Revoke advances the fence before best-effort process stop, secret revocation,
  network removal, and Runtime View discard.
- Release requires trustworthy evidence that the process is stopped and the
  sandbox is contained or reset. Missing teardown evidence keeps capacity
  unavailable even when the Runtime Run is terminal.
- Expired, lost, or revoked leases never reactivate. Node loss uses a new
  Runtime Run rather than a second lease or live migration.
- A physical sandbox may be pooled only after a complete reset and under a new
  identity and lease, without Task state, secrets, cache mutations, or prior
  run evidence.

Node loss changes capacity to unknown or quarantined, not free. The Scheduler
cannot place work on the node until Runtime Execution accepts a fresh readiness
and reset attestation.

Logical concurrency and physical reuse are deliberately different:

- `RuntimeFencedOrTerminal` is exact C03 evidence bound to Work Item, Admission
  Grant and generation, Runtime Run, operation/start digest, Runtime revision,
  Scheduler epoch, policy, and current fence. Scheduler may use it under its
  own current epoch/policy CAS to release logical global, Personal Workspace,
  capability, and Resource Class counters. Node loss may produce this evidence
  after C03 fences the run or records `Lost`; it does not need containment or
  reset proof.
- `NoLeasePhysicalDisposition` is exact C03 evidence for an Accepted/Bound run
  that proves the lease-grant transaction, physical occupancy, and worker
  dispatch never committed. Scheduler may use it to clear only that grant's
  selected-node scheduling reservation. It does not attest node readiness; a
  stale or unavailable node remains quarantined until a current C03 readiness
  fact for the exact node-capacity generation exists.
- `PhysicalCapacityReleaseReady` is exact C03 evidence bound additionally to
  Execution Node and physical-capacity generation, Sandbox Lease/generation,
  process tree, secret/network revocation, and containment/reset evidence. C03
  issues it only after the old process cannot act and the sandbox is contained
  or reset. Scheduler may make that node vector allocatable only from this
  evidence under its own node-generation CAS.
- `UnknownOrQuarantined`, containment, and reset facts remain C03 physical
  truth. Releasing logical counters never clears node quarantine, and a
  physical release fact never edits Scheduler policy counters directly.

Thus a lost node can stop consuming unrelated Workspace concurrency while all
of its physical capacity remains quarantined. A no-lease run can release its
logical and selected-node scheduling reservations without fabricating physical
release. Metrics and audit report logical release lag, no-lease disposition
lag, and physical release lag separately.

## Execution Capsule, Runtime View, inputs, secrets, and network

After admission and C03 start acceptance, Runtime Execution first grants the
one Sandbox Lease. A mutating run then derives one stable C04 open OperationID
and canonical request from the start's `RuntimeViewRequirement` plus the exact
current `SandboxLeaseAuthority`. That authority is mandatory in the existing
C04 `OpenRuntimeViewRequest`. C03 durably records the request before delivery;
C04 exact replay must return the same Runtime View. A response loss replays the
same request and uses C04 `InspectOperation`/`ReconcileOperation`; it never
allocates a second view, scans a path, or infers success from worker state.

Only after C04 open acceptance is verified and persisted, and all other
applicable prerequisites are current, does Runtime Execution create a private
immutable `ExecutionCapsule` for the owned node adapter. Immutable inputs are
acquired by opaque capability, verified against their manifests and digests,
and mounted read-only. A mutating run receives the returned isolated Runtime
View capability as its only writable Task state. Output leaves through declared
channels and a canonical output manifest. A read-only run creates no writable
Runtime View.

Provider-capable runs also require a current short-lived `GatewayGrant` before
dispatch. Its expiry cannot exceed the Runtime deadline, Sandbox Lease,
authorization generation, Runtime fence, Active Quota Reservation, or Provider
Route policy. For a long Runtime Run, C03 refreshes or rotates through one
stable idempotent grant operation and a monotonic grant generation. A
replacement preserves every scope and can only narrow expiry/policy; atomic
activation prevents the prior generation from accepting new Gateway Calls.
Acknowledgement loss replays/inspects the refresh operation. Already accepted
Gateway Attempts remain settleable. If refresh fails or the grant expires, new
Calls fail closed; non-provider work may continue only when the capability
contract permits, otherwise the run pauses/reconciles and eventually follows
its declared failure policy without direct provider egress.

The worker receives only sandbox-local logical locations. Host paths remain
inside the node adapter and never enter the Platform interface, PostgreSQL
business records, Runtime Evidence, queue payloads, or logs. An existing path
is not proof of materialization, ownership, or recovery.

Production security requirements are:

- treat guest code, agent actions, tool subprocesses, prompts, and supplied
  content as hostile relative to the host and every other Personal Workspace;
- use default-deny network policy and enable only destinations and protocols
  explicitly authorized by the Runtime Binding and Execution Policy; all LLM
  and generative-image provider access must use the central LLM Gateway;
- inject secrets through a node secret broker as short-lived, purpose-bound
  capabilities tied to Runtime Run, lease, node, fence, and expiry;
- prevent secrets from entering Task Workspace state, Checkpoints, outputs,
  Runtime Evidence, logs, crash dumps, or cache;
- never give a sandbox object-store, registry, Platform PostgreSQL, long-lived
  provider, scheduler, or Agent Compose daemon credentials;
- fail admission closed when an image, manifest, policy, attestation, mount,
  network rule, secret grant, or reset state cannot be proved.

Driver selection remains an adapter acceptance decision. KVM or a microVM is
not automatically trusted, and a hardened container is not automatically
rejected; the exact production configuration must prove the hostile-execution
contract before admission.

## Adapter normalization and reconciliation

The module presents one durable asynchronous contract regardless of downstream
style:

- a synchronous adapter runs only inside an owned worker and is production
  eligible only when it can bind a stable operation and reconcile ambiguity;
- a polling adapter persists the opaque external operation and cursor before
  scheduling reconciliation;
- a callback adapter authenticates and deduplicates callbacks, maps them to the
  exact operation and fence, and treats them as evidence rather than business
  authorization;
- a queue adapter uses at-least-once delivery and acknowledges only after
  durable start acceptance;
- transport timeout, callback loss, polling interruption, or queue claim loss
  enters reconciliation instead of fabricating failure.

If a worker disappears before an external acknowledgement, another worker
replays the same start operation. If it disappears after start, a reconciler
observes the existing external operation. A worker or delivery claim loss does
not change Task, Phase Run, or Runtime Run outcome.

Platform timeout first fences the run as `TimedOut`, then requests process
termination, revokes capabilities, and asks C04 to discard the Runtime View.
After a lease or possible process effect exists, node or daemon loss fences the
lease and may eventually produce `Lost` if the exact operation cannot be
reconciled; the node remains quarantined until containment and reset are
proved. When C03 proves that no lease or process effect committed, loss cannot
produce `Lost` and follows the no-lease terminal contract instead. A returning
stale worker cannot cross the fence.

Process termination, prevention of C04 commit, and capacity release are three
independent facts. Agent Compose `succeeded`, `failed`, or `canceled` proves
none of the other two by itself.

## Runtime Evidence and trust levels

Runtime Evidence binds at least:

- evidence schema and normalization identity;
- Personal Workspace, Task, Phase Run, Runtime Run, and operation;
- canonical request and immutable input-manifest digests;
- Execution Lock, Runtime Binding, capability contract, and safety epoch;
- worker class, Execution Node, Sandbox Lease, and fence;
- actual image digest, executor, adapter, and versions;
- start, completion, deadline, cancel reason, and normalized terminal outcome;
- canonical output manifest, opaque output references, and digests;
- raw adapter-evidence digest or reference, ordered event cursor, log
  references, and usage receipt references with explicit known, unknown,
  missing, or estimated status;
- containment, cleanup, and stale-evidence rejection facts.

Trust is layered:

1. Runtime Execution's PostgreSQL decisions, lease facts, and terminal records
   are authoritative execution facts.
2. Owned node and adapter evidence becomes trusted Runtime Evidence only after
   authority, binding, release and catalog epochs, digest, and fence validation.
3. Guest, agent, and tool outputs remain untrusted proposals until independent
   Platform validation.
4. Agent Compose raw detail, stdout, events, stats, callbacks, and external IDs
   remain adapter evidence. Its v2607.10.0 public run/result contract does not
   preserve provider usage, provider request IDs, or the original terminal
   provider evidence.
5. Phase validation evidence is produced independently by the Platform
   validator; a worker cannot attest its own Phase success.
6. Usage Receipts become Usage Ledger facts only through the
   [LLM Gateway and Usage Accounting](./llm-gateway-and-usage-accounting.md)
   verification and settlement seam. The
   [issue 14 provider evidence research](./llm-provider-agent-compose-usage-evidence.md)
   requires provider-native capture per outbound attempt; missing evidence is
   never zero usage and may arrive after Runtime Run terminal state.
7. Logs, traces, and metrics are incomplete or expiring projections and cannot
   drive a state transition.

## Error taxonomy

Errors expose a safe category, retry disposition, and whether reconciliation
is required. Categories include authorization or ownership denial; invalid
intent or idempotency conflict; stale Task revision, lease fence, operation, or
safety epoch; revoked, incompatible, or unavailable Runtime Binding; input,
output, policy, attestation, or evidence integrity failure; admission deferred
or resource exhausted; retryable adapter unavailable; ambiguous transport;
agent or tool failure; cancellation or deadline exceeded; worker, daemon, or
node lost; and cleanup pending or Cleanup Debt.

Errors never reveal content, path, locator, credential, another Personal
Workspace's existence, or an unrestricted raw provider error. Retryability
cannot weaken authorization, integrity, release, deadline, or fencing rules.

## Request and rejection journal

Persistence distinguishes accepted commands, replayable canonical rejection,
and hostile or non-canonical ingress:

| Input class | Durable record | Exact replay and side effects |
| --- | --- | --- |
| Accepted canonical start/cancel | Authoritative request binding, Decision, Runtime revision, mandatory audit, and allowed owned outbox facts | Same key/digest returns the original Decision and current snapshot; no identity or side effect is reallocated |
| Authenticated, canonical, policy/integrity rejection | Content-free rejection decision keyed by scope, request key/digest, and admission-grant attempt where applicable | Same full binding returns the original safe rejection; no Runtime revision, lease, prerequisite, or worker effect |
| Stale revision/generation/fence/epoch or stale grant | Content-free stale rejection including the exact typed stale binding; grant-stale records include grant identity/generation | Exact replay of the same stale binding is stable. A higher valid grant generation may authorize the unchanged start only if no start was accepted |
| Unauthorized or non-owner probe | Rate-bounded, server-identified security/audit observation with non-enumerating result; no caller-controlled business request binding | Does not reveal whether the target/key exists and cannot reserve a replay key or create work |
| Malformed request | Bounded sanitized ingress observation only; no canonical request journal | Deterministic safe invalid result, no business identity allocation or side effect |
| Unknown major schema | Bounded sanitized ingress/schema observation only; no downgrade and no canonical request journal | `unsupported_schema`, non-enumerating and side-effect free |
| Same key with different canonical digest | Original binding remains immutable; append an integrity incident/audit fact for the conflicting attempt | Always conflict; the later payload never overwrites, replays, authorizes, or becomes terminal `Rejected` |

Runtime terminal `Rejected` remains distinct: it is available only after a
canonical start was accepted and a permanent pre-process prerequisite later
failed. Command rejection never fabricates a Runtime terminal outcome.

## Cleanup, retention, backup, and repair

Runtime Execution owns process, sandbox, lease, containment, and reset cleanup.
C04 owns Runtime View, Task Workspace materialization, and workspace cleanup.
Failures create Cleanup Debt under the authority that owns the resource rather
than duplicate debt in both modules.

Runtime Run identities, terminal outcomes, lease and fence history, evidence
roots, and Phase Run relationships remain authoritative history. Raw logs,
transcripts, temporary output, node databases, sandboxes, and caches are
expiring execution material under the
[observability and retention contract](./observability-audit-and-cleanup-debt.md).

Backup retains authoritative Runtime Run and evidence metadata and necessary
opaque references. It does not back up or restore a live sandbox, Agent Compose
session, Runtime View, node-local database, or queue projection. Restore fences
old non-terminal Runtime Runs. An Accepted/Bound run with proof that no lease
committed becomes pre-process `Rejected` with `NoLeasePhysicalDisposition`; a
run with an ambiguous or possible lease/process may become `Lost` and keeps the
node quarantined. Recovery creates new Runtime Runs from validated Checkpoints
only when Task Orchestration permits it.

Repair can restore only evidence or bytes that exactly match the original
authority, schema, digest, and manifest. It cannot adopt an orphan output,
change the expected digest, scan a session or directory, or infer success from
a process or log.

## Production and test adapters

The Agent Compose production adapter must:

- use a pinned v2 Connect or HTTP contract rather than CLI shell-out as the
  enterprise write seam;
- map the stable SlideSmith operation to `client_request_id` while SlideSmith
  independently enforces same-key and same-payload binding;
- use one owned daemon and data root per Execution Node, never a shared root;
- pin daemon, guest, runtime image, and executor contract digests;
- expose the daemon only on an owned protected network with controlled TLS or
  mTLS and short-lived daemon credentials;
- keep Agent Compose project, run, and sandbox identities opaque;
- prove expected version, driver, image, policy, node readiness, capacity,
  secret and network availability, and hostile-execution acceptance before
  admission;
- keep project provisioning, `up`, data-root layout, and paths private.

Agent Compose production adoption remains subject to legal and open-source
compliance approval. This external gate does not enlarge the interface or make
the vendor authoritative.

A Tool Worker may use a separate sandbox executor. It may map to an Agent
Compose command-run path only after passing the same contract. A raw `exec`,
arbitrary caller shell, or path without a stable Runtime Run audit identity is
not a production seam.

Adapters include a deterministic in-memory implementation with a controllable
clock and fault injection, a local development implementation, a pinned Agent
Compose integration adapter, a production Tool executor integration adapter,
and an owned transport adapter.

## Highest-level scenarios and adapter contracts

The Runtime Execution module is the highest-level execution test seam. Its
scenario suite covers:

- Agent and Tool success and failure, including multiple internal calls under
  one Runtime Run;
- exact replay, same-key and different-payload conflict, and concurrent start
  and cancel;
- Work Item/admission atomicity, unbound-grant expiry, stale/new grant
  generations, delivery/acknowledgement loss, and the absence of a second
  admission path;
- every post-bind/pre-lease matrix row, including temporary same-generation
  node/Reservation ambiguity, permanent stale binding, bound-authority expiry,
  cancel, deadline, and crash on both sides of the lease transaction; exact
  replay; no `Lost` without possible physical effect; and atomic
  `RuntimeFencedOrTerminal` plus `NoLeasePhysicalDisposition`;
- lease grant, renew, expiry, revoke, release, reset, and node quarantine;
- worker, daemon, node, acknowledgement, poll, callback, and queue loss;
- duplicate, missing, delayed, out-of-order, unauthorized, corrupt, cross-Task,
  and cross-Workspace evidence;
- timeout, late success, terminal races, and cancellation on either side of C04
  commit;
- stale Task revision, authorization generation, lease fence, operation, and
  release-safety epoch;
- immutable input or output-manifest mismatch, malicious output, secret and
  network denial, and path, locator, and credential non-leakage;
- cleanup failure, capacity not being released early, recovery read-only mode,
  and restore without in-flight continuation;
- Agent Compose request-binding gaps, restart behavior, and contract drift.

Worker, Agent Compose, Tool executor, node runtime, scheduler, C04, Durable
Object, secret broker, network policy, PostgreSQL, queue, callback, polling,
audit, and transport adapters receive black-box contracts where applicable.
Tests assert identities, decisions, leases, fences, evidence, containment, and
outcomes rather than CLI output, paths, sessions, vendor states, queue
products, SQL shape, or log text.

Deletion tests are structural. Package/import-graph gates keep the target C03
and Task Orchestration integration dependent only on allowlisted owned ports;
capability-surface gates reject any interface that exposes host path, session,
recent-run discovery, arbitrary shell, shared daemon control, or general
repository mutation. Contract fixtures prove the module still builds and
executes with legacy execution packages absent. Tests do not depend on one
literal filename, repository path, method spelling, error string, or source
substring, so harmless renames cannot satisfy or break the deletion gate.

## Hard cutover and deletion test

This is replace-not-layer migration. Once the module and adapter contracts
exist, the target architecture deletes rather than wraps:

- the `AgentComposeClient.Up/Run` CLI contract and Docker-exec wrapper;
- session IDs, `SessionDataRoot`, and `/sessions/<id>/workspace` inference;
- Task last-run, last-session, and Runtime workspace paths as current authority;
- a Phase Run's single Runtime Run, session, and workspace-path coupling;
- TaskService calls that run agents or tools, copy runtime workspaces, and
  directly advance business state;
- API and worker access to the Docker socket, Agent Compose data root, and host
  Task Workspace paths;
- mutable `latest`, environment-selected runtime behavior, and caller parsing
  of Agent Compose JSON;
- fallback that recovers current work from status, sessions, directories,
  copied Skill trees, or the most recent run.

Issue 17 owns the record-by-record migration and irreversible cutover. Legacy
path and session values may survive only as non-executable historical evidence.

## Rejected alternatives

Rejected alternatives include making Agent Compose authoritative; giving Agent
and Tool Workers separate business state machines; letting workers create
Runtime Runs or advance Phases; creating a Runtime Run for every internal tool
call; moving one Runtime Run to a second node or lease; treating a vendor
terminal state as Phase success; interpreting caller timeout as downstream
termination; accepting success after a fence; unrestricted network or
long-lived secrets; sandbox access to platform credentials; using host paths,
sessions, queue claims, logs, or stats as authority; retaining the legacy
surface behind a compatibility facade; and declaring any sandbox driver safe
for hostile code without configuration-specific evidence.

## Stable downstream inputs and remaining fog

- The resolved
  [Scheduler contract](./scheduling-and-capacity-admission.md) receives worker
  class, resource requirements, Execution Policy,
  truthful node facts, Sandbox Lease, and Admission Grant seams. It owns
  fairness, concurrency, placement, and concrete resource-class policy.
- The resolved
  [LLM Gateway and Usage Accounting contract](./llm-gateway-and-usage-accounting.md)
  consumes Runtime Run and operation correlation, network and secret seams,
  active Phase Run Reservations, and Usage Receipt references. It requires
  Gateway-only provider egress and accepts legitimate late usage independently
  of the Runtime fence.
- Issue 17 receives the target Runtime Run relationships, terminal, fence, and
  evidence model plus the complete deletion test.
- The [observability and audit contract](./observability-audit-and-cleanup-debt.md)
  consumes Runtime Run, lease, node, operation, fence, error, and cleanup
  correlation under the authoritative-versus-projection boundary.
- Issue 14 has established the provider and Agent Compose usage evidence facts;
  its remaining provider-selection and SLA unknowns are explicit Gateway
  onboarding, reconciliation, and fail-closed acceptance inputs rather than a
  Runtime Execution contract blocker.

Superseded decisions: none.

New decision-only tickets: none.

Remaining fog affecting the first Runtime Execution specification: none.
Concrete driver product, resource values, fairness algorithm, provider route,
telemetry vendor, schema, and serialized method names belong to named
downstream decisions, adapter acceptance, or implementation specifications and
do not reopen this module.
