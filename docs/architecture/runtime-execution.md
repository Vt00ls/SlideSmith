# Runtime Execution

This document records the Runtime Execution, Agent Worker, Tool Worker, and
Sandbox Lease decisions resolved in
[GitHub issue 24](https://github.com/Vt00ls/SlideSmith/issues/24).
[CONTEXT.md](https://github.com/Vt00ls/SlideSmith/blob/codex/ARCH-01-enterprise-platform-review/CONTEXT.md)
is authoritative for domain language,
[ADR 0022](https://github.com/Vt00ls/SlideSmith/blob/codex/ARCH-01-enterprise-platform-review/docs/adr/0022-run-runtime-capabilities-through-fenced-sandbox-leases.md)
records the durable module choice,
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
- read-only or mutating effect; a mutating run also binds one opaque C04
  Runtime View capability;
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

The semantic non-terminal progression is:

```text
Pending -> WaitingForAdmission -> LeaseGranted -> Starting -> Running
        -> Reconciling | Stopping
```

The immutable terminal outcomes are:

- `Succeeded`: verified terminal capability success;
- `Failed`: verified capability or executor failure after acceptance;
- `Cancelled`: cancellation fenced the run before success linearized;
- `TimedOut`: the Platform deadline fenced the run;
- `Lost`: the worker, daemon, node, or operation can no longer be reconciled
  and its lease is fenced;
- `Rejected`: a non-retryable authorization, binding, policy, compatibility,
  or integrity condition prevented execution.

An unknown transport result is not terminal. It enters `Reconciling`. A
terminal Runtime Run does not prove Phase success, C04 commit or discard,
sandbox cleanup, or capacity release; those facts retain separate states and
authorities.

The command-acceptance PostgreSQL transaction is the start linearization
point. The lease-grant transaction binds one node and fence before any process
may start. A terminal result linearizes only when authenticated evidence
matching the current operation, Runtime Binding, Task revision, lease fence,
and safety epoch commits in PostgreSQL.

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

## Execution Capsule, Runtime View, inputs, secrets, and network

After admission, Runtime Execution creates a private `ExecutionCapsule` for the
owned node adapter. Immutable inputs are acquired by opaque capability,
verified against their manifests and digests, and mounted read-only. A
mutating run receives exactly one isolated C04 Runtime View as its only writable
Task state. Output leaves through declared channels and a canonical output
manifest.

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
Node or daemon loss fences the lease and eventually produces `Lost`; the node
remains quarantined until containment and reset are proved. A returning stale
worker cannot cross the fence.

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
or marks old non-terminal Runtime Runs lost and creates new Runtime Runs from
validated Checkpoints when Task Orchestration permits recovery.

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
