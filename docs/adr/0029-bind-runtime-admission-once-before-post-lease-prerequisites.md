# Bind Runtime admission once before post-lease prerequisites

Status: Approved by Owner — effective when merged to the default branch.

This record contains the narrow supersession of accepted ADR 0022 and the #24
Runtime Execution resolution explicitly approved by the Owner in the
[#24 Class C approval](https://github.com/Vt00ls/SlideSmith/issues/24#issuecomment-5100851705).
The approval resolves the decision gate but does not itself authorize PR merge.
Implementation decomposition and `to-tickets` remain blocked until the
approved documentation is merged into the default branch.

SlideSmith will use one admission path for a Runtime Run. Task Orchestration
atomically commits its immutable Runtime enactment/outbox and the Scheduler Work
Item. Scheduler alone performs `ClaimAndAdmit`, reserves logical counters, and
issues an unbound Admission Grant for that Work Item, Task enactment
OperationID, canonical C03 start digest, selected node, Scheduler epoch/policy,
and monotonic grant generation. Authenticated delivery carries the unchanged
canonical start plus that grant. The C03 start-acceptance transaction uses a
restricted Scheduler transactional participant to atomically make three facts
true: C03 accepted the canonical start, the Work Item is downstream
`Accepted`, and the Admission Grant is `Bound`. C03 does not create an
admission-enactment outbox or any second admission authority.

The canonical start does not include a replaceable grant identity. A grant
authorizes one delivery attempt by binding to the already fixed operation and
digest; it cannot rewrite them. An expired or stale unbound grant can be
replaced by a higher generation for the same Work Item only when C03 has no
accepted start. Once start acceptance binds a grant, a later grant cannot
rebind that Runtime Run. Delivery or acknowledgement ambiguity is reconciled
by replaying or inspecting the original C03 operation.

Binding also fixes a `LeaseAcquireBy` no later than the existing grant expiry
or Runtime deadline and creates a C03 Runtime fence independent of any
Sandbox Lease fence. A bound grant never becomes unbound, never returns its
Accepted Work Item to eligibility, and never receives another generation.
Temporary same-generation node-readiness or prerequisite ambiguity may remain
`WaitingForLease`/`Reconciling` only until `LeaseAcquireBy`, using the same
durable lease-acquire operation. A permanently stale node generation,
Reservation, authorization, policy, or safety binding terminates the accepted
run as `Rejected`; an accepted cancel terminates it as `Cancelled`; the Runtime
deadline terminates it as `TimedOut`; bound-authority expiry before the Runtime
deadline terminates it as `Rejected`. None of those outcomes is `Lost` when C03
can prove that no lease or dispatch committed.

Every no-lease terminal transaction atomically records the terminal outcome,
advances the Runtime fence, emits `RuntimeFencedOrTerminal`, and records
`NoLeasePhysicalDisposition` for the exact Work Item, grant generation,
Runtime, operation, selected node generation, and stable lease-acquire
operation. This disposition proves only that this run acquired no physical
occupancy; it does not assert that the selected node is Ready or reset. C03
crash recovery inspects PostgreSQL and resumes the same lease-acquire operation:
before its transaction commits there is no lease, after it commits there is
exactly one, and ambiguity cannot be resolved from transport, telemetry, or
timeout. `PhysicalCapacityReleaseReady` applies only after a lease/physical
occupancy actually existed.

Scheduler logical capacity and C03 physical capacity are separate facts.
Scheduler may CAS-release global, Personal Workspace, capability, and Resource
Class counters from exact `RuntimeFencedOrTerminal` evidence under its current
epoch and policy. Only C03 may issue `PhysicalCapacityReleaseReady`, after
proving process and child-process termination, secret revocation, network
removal, and containment or reset for the exact node generation and lease.
Node loss can therefore release logical counters while the lost node's
physical capacity remains quarantined.

The accepted Runtime Execution public seam remains:

```text
Execute(StartRuntimeRun | CancelRuntimeRun) -> RuntimeDecision
Inspect(RuntimeRunRef) -> RuntimeSnapshot
```

Node-execution fences, containment/reset confirmation, and C03-owned Cleanup
Debt exception/reopen operations use a separate protected operational
interface, `RuntimeMaintenance`. It accepts only closed operational intents from
their typed Scheduler, security, recovery, or cleanup authorities, records
mandatory audit in the authoritative transaction, and cannot start/cancel a
Runtime Run, mutate Task/Phase state, change Scheduler counters, or resolve
another module's Cleanup Debt. Protected diagnostics remain read-only through
the operational diagnostics seam. Tests may exercise these ports through
their contracts but do not enlarge the public interface.

A mutating `StartRuntimeRun` binds an exact `RuntimeViewRequirement`, not an
existing Runtime View capability. The requirement fixes Task Workspace,
materialization/base Revision, effect class, lifecycle generation/fence,
expiry policy, and stable operation-derivation material available when Task
Orchestration creates the enactment. After the Admission Grant is bound, C03
grants the one Sandbox Lease. It then derives and durably records the exact C04
`OpenRuntimeViewRequest`, whose current `SandboxLeaseAuthority` is mandatory.
C04 acceptance is persisted before the capability enters the Execution
Capsule or worker dispatch. Response loss replays, inspects, and reconciles
that same C04 operation; it never creates another view or scans a path.

The unique ordering is:

```text
Task decision/outbox + Scheduler Work Item atomic commit
-> Scheduler ClaimAndAdmit and unbound Admission Grant
-> authenticated delivery
-> C03 Start acceptance + Work Item Accepted + Grant Bound
-> Sandbox Lease
-> C04 Runtime View and Gateway prerequisites
-> Execution Capsule
-> worker dispatch
```

## Considered options

- Adding maintenance variants to `Execute` was rejected because it would
  silently expand the accepted public business contract and mix node/cleanup
  operations with Runtime Run start/cancel authority.
- Binding an existing Runtime View capability in start was rejected because
  the accepted C04 contract requires the current `SandboxLeaseAuthority` in
  `OpenRuntimeViewRequest`; no such authority exists before admission and
  lease grant.
- Letting C03 accept start and then enqueue another admission operation was
  rejected because it duplicates Scheduler admission authority and makes
  Work Item acceptance, logical counters, and start idempotency ambiguous.
- Gating logical counter release on physical reset was rejected because a lost
  node would unnecessarily pin global and Workspace concurrency even after
  the Runtime is authoritatively fenced, while still failing to add any proof
  that the node is safe.

## Consequences

- ADR 0022's earlier wording that start binds a Runtime View capability is
  superseded by the requirement-then-open contract above upon default-branch
  merge.
- ADR 0022's public interface remains unchanged; maintenance is a separate
  protected operational interface.
- ADRs 0020, 0024, 0026, and 0027 retain their authorities and are clarified by
  this ordering, capacity split, Gateway-grant lifecycle, and audit boundary.
- Runtime Execution implementation and tests must prove one admission path,
  exact generation/replay behavior, the complete post-bind/pre-lease matrix,
  and separate logical/no-lease/physical-release evidence before #71 can be
  considered complete.
