# Scheduling and Capacity Admission

This document records the queue, Personal Workspace fairness, concurrency,
resource admission, delivery, retry, and recovery decisions confirmed while
resolving
[GitHub issue 20](https://github.com/Vt00ls/SlideSmith/issues/20).
[CONTEXT.md](../../CONTEXT.md) is authoritative for domain language,
[ADR 0026](../adr/0026-schedule-durable-work-through-fair-fenced-admission.md)
records the durable module choice,
[task-orchestration.md](./task-orchestration.md) defines Task, Phase Run,
Runtime Run, and enactment authority,
[runtime-execution.md](./runtime-execution.md) defines Sandbox Lease, fence,
node-fact, and capacity-release authority, and
[llm-gateway-and-usage-accounting.md](./llm-gateway-and-usage-accounting.md)
defines the Quota Reservation prerequisite, and
[observability-audit-and-cleanup-debt.md](./observability-audit-and-cleanup-debt.md)
defines correlation, signal, alert, audit, and retention contracts.

The design fixes authority, work granularity, fairness, priorities, aging,
layered concurrency, Resource Classes, capacity admission, backpressure,
claims, heartbeats, leases, stale completion, delivery retry, dead-letter,
recovery, topology, and adapter contracts. It does not select a queue vendor,
schema, serialized protocol, production node count, hardware purchase,
autoscaler, or externally visible SLO.

## Decision summary

Scheduler is a deep module in the Platform Control Plane. It owns durable
Scheduler Work Item delivery state, Personal Workspace fairness, priority and
aging policy, Delivery Claims, Scheduling Policy and Resource Class
definitions, layered concurrency decisions, placement, Admission Grants,
delivery retry, dead-letter, and capacity reconciliation.

Platform PostgreSQL is authoritative for queue and admission decisions. Redis,
NATS, an in-memory channel, polling, and notifications are replaceable wakeup
or transport adapters. Their messages, acknowledgements, offsets, visibility
timeouts, and local buffers are never enqueue, dequeue, claim, or capacity
authority.

Scheduler consumes truthful Runtime Execution facts about Execution Node
readiness, attested capabilities, Sandbox Lease occupancy, containment, reset,
and quarantine. It does not manufacture those facts. Runtime Execution
consumes an Admission Grant but revalidates every binding before granting a
Sandbox Lease.

Enterprise V1 remains single-site and non-preemptive. The same interface serves
a one-node serial acceptance topology and multiple production Execution Nodes.
This is not cross-site or global scheduling.

## Standing constraints

- Personal Workspace, not Tenant, is the fairness and per-owner concurrency
  unit.
- Task Orchestration creates every Phase Run and Runtime Run identity before
  delivery. Scheduler cannot create an attempt or decide a Phase outcome.
- A Phase Run owns zero or more Runtime Runs. Runtime Run, Phase Run, and Task
  are therefore not interchangeable queue items.
- Runtime Execution owns Runtime process state, Sandbox Lease and fence,
  deadline, containment, reset, node truth, and verified Runtime Evidence.
- Resource admission cannot weaken an Execution Lock, Runtime Binding,
  Resource Class, Execution Policy, release or catalog safety epoch, network
  policy, or authorization generation.
- A quota-bearing admission requires an Active Phase Run Quota Reservation.
  Enterprise V1 observes quota shortage but does not enforce it.
- Manual edit uses the Task's post-publication Phase graph and the same
  Scheduler contract. It is not a second scheduling authority.
- Queue delivery, Runtime completion, Phase outcome, C04 commit, publication,
  provider usage settlement, and capacity release remain distinct facts.

## Authority and ownership matrix

| Fact or action | Authority | Scheduler relationship |
| --- | --- | --- |
| Task revision, Phase Run identity and outcome, Runtime Run membership, enactment intent | Task Orchestration | Receives one immutable enactment binding; never changes business state |
| Scheduler Work Item, ready time, priority class, fairness state, Delivery Claim, delivery outcome | Scheduler in Platform PostgreSQL | Owns durable queue and delivery decisions |
| Scheduling Policy, Resource Class, layered limits, placement, Admission Grant | Scheduler | Owns policy and admission without weakening bindings |
| Runtime Binding, capability and environment requirement keys | Release Management | Scheduler resolves only an approved exact class |
| Runtime process, Sandbox Lease, fence, node facts, containment and reset | Runtime Execution | Supplies facts and consumes an Admission Grant |
| Personal Workspace ownership and machine-authority generation | Identity & Ownership | Supplies a fenced scope; worker assertions are ignored |
| Quota Reservation, Usage Ledger and enforcement disposition | Usage Accounting | Scheduler validates an opaque Reservation; it cannot inspect or mutate the Ledger |
| Runtime View, Revision, Checkpoint and cleanup | Task Workspace Lifecycle | Capacity release cannot imply C04 commit or cleanup |
| Broker delivery, poll cursor, worker local state and Agent Compose stats | Adapter projection | Never authoritative |
| Metrics, traces, logs and external audit projection | [Observability and audit](./observability-audit-and-cleanup-debt.md) | Consume facts without driving scheduling state |

## Scheduler Work Item

A Scheduler Work Item is the durable delivery and admission record for exactly
one Task Orchestration enactment operation.

It binds at least:

- immutable Work Item and enactment operation identities;
- Personal Workspace, Task, Phase Run, and optional existing Runtime Run;
- machine-authority and Task revisions or generations;
- immutable request and payload digests;
- capability, worker class, Resource Class requirement, Execution Policy,
  Runtime Binding, and safety epochs where applicable;
- priority class assigned by closed platform policy;
- original enqueue and current eligible times;
- Quota Reservation identity and generation when the capability may create
  quota-bearing provider work;
- cancellation, deadline, and evidence-contract references;
- non-authoritative trace context.

The payload contains only opaque identities, digests, policy references, and
small typed parameters. It never contains User content, prompts, output,
credentials, provider endpoints, object locators, host paths, mounts, shell
commands, or Agent Compose project, session, sandbox, or data-root paths.

One Work Item may reference an existing Runtime Run start, a platform
validation, a C04 intent, publication, or another typed asynchronous
enactment. Capacity-free safety controls use the same durable identity rules
but run in a reserved control lane.

Task is too coarse because it would make status and worker code own workflow.
Phase Run is too coarse because it may own zero or many Runtime Runs. Runtime
Run alone is too narrow because it cannot represent validation, C04,
publication, or other platform enactments. Scheduler Work Item adds delivery
state without becoming another business lifecycle.

## Module interface

Representative semantic families, not final method or wire names, are:

~~~text
Scheduling.Decide(
  OfferWork | CancelWork |
  ClaimAndAdmit | HeartbeatClaim | AcknowledgeAcceptance |
  ReleaseCapacity | QuarantineNode |
  DeadLetter | Redrive | Reconcile
) -> SchedulingDecision

Scheduling.Query(OwnerScope | AdministratorAggregateScope)
  -> SchedulingView
~~~

Task Orchestration supplies an immutable enactment through an internal
transactional adapter. Runtime Execution, workers, node-fact adapters, and
Usage Accounting use narrow typed adapters. No caller receives the Scheduler
database, a mutable queue record, a general counter interface, or the ability
to choose its own Workspace, priority, class, limit, or node.

## Enqueue, claim, acknowledgement, and linearization

For a Task Orchestration enactment, the Task decision/outbox and Scheduler Work
Item enqueue facts commit in the same Platform PostgreSQL transaction. The
immutable enactment fields remain Task Orchestration authority; the scheduling
fields remain Scheduler authority. If backpressure rejects a new external
intent, neither the Work Item nor its Task transition commits.

Other trusted producers offer work through a scope-bound operation identity and
canonical digest. Exact replay returns the existing Work Item. Reuse with a
different binding is an integrity conflict.

The queue progression is:

~~~text
Queued -> Delivering -> Accepted
   |          |
   |          +-> Queued          # claim lost and downstream not accepted
   +-> Cancelled
   +-> DeadLettered
~~~

The corresponding Admission Grant progression is:

~~~text
Reserved -> Bound -> Releasing -> Released
    \-> ExpiredUnbound
~~~

Linearization points are:

- enqueue: the PostgreSQL transaction that records the Work Item;
- claim and admission: one transaction that selects fairly, validates every
  current prerequisite, allocates counters, chooses a node, and records a
  Delivery Claim plus unbound Admission Grant;
- dequeue as accepted: the transaction that verifies durable downstream
  acceptance for the exact operation and marks the Work Item Accepted;
- cancellation or dead-letter: the corresponding terminal Scheduler
  transaction;
- Runtime terminal, Phase outcome, and capacity release: separate transactions
  under their owning modules.

A broker acknowledgement or worker response is never dequeue authority.
At-least-once delivery is required. If acknowledgement is lost after downstream
acceptance, another worker replays the same operation, receives the existing
acceptance, and completes the original Work Item.

## Personal Workspace fairness

Enterprise V1 uses hierarchical weighted deficit round-robin.

1. Scheduler groups eligible User work by Personal Workspace and compatible
   scheduling pool.
2. All Personal Workspaces have fixed weight one in V1. Neither a User nor a
   Platform Administrator can grant an ordinary Workspace a larger weight.
3. Each round adds a quantum to the Workspace deficit. A Resource Class supplies
   a bounded positive admission cost. The deficit carries across rounds, so a
   larger class cannot starve permanently.
4. At most one new admission is granted to a Workspace per pass.
5. Scheduler then selects that Workspace's highest effective-priority eligible
   item.
6. Placement runs only after the fairness candidate is selected. Cache
   locality, image presence, or node preference cannot let another Workspace
   bypass the round.
7. An item that cannot fit any healthy eligible node consumes no deficit and
   does not block another compatible pool for the same Workspace.

The per-Workspace active limit prevents one long queue from filling all
capacity. Non-preemptive long work can still occupy its admitted resources;
V1 does not claim duration-proportional or lossless preemptive fairness.

## Priority and aging

User work has three closed priority classes:

- Interactive: post-publication manual edit;
- Standard: new Task work, ordinary Phase continuation, and Task retry;
- Background: non-safety reconciliation and administrator redrive.

Cancellation, fencing, lease revocation, capacity release, recovery
coordination, and other safety controls use a reserved control lane rather than
an ordinary User priority.

Priority is applied only inside the Workspace selected by fairness. Manual edit
can precede Standard work in its own Workspace but cannot jump ahead of another
Workspace's turn. Users cannot submit a priority, and administrator operational
authority does not imply a User-work priority boost.

The default aging step is five minutes. Each complete step promotes effective
priority by one tier, capped at Interactive. Capacity deferral and delivery
retry retain the original eligible age. A newly authorized business retry is a
new Work Item with a new eligible time. An audited dead-letter redrive retains
identity and history but begins a new redrive eligibility interval.

Within equal effective priority, eligible time and then immutable Work Item
identity provide deterministic order. V1 does not preempt a running workload.

## Layered concurrency limits

Every capacity-bearing Admission Grant must satisfy all of:

- site-wide active limit;
- Personal Workspace active limit;
- exact capability limit;
- Resource Class or scheduling-pool limit;
- Execution Node allocatable vector and max-active limit.

Counters start at Admission Grant Reserved, not at worker claim, process
observation, or Runtime completion. A Delivery Claim is not capacity.

The V1 default per-Personal-Workspace active limit is one. It is versioned and
configurable, but changing it does not change fairness identity. The serial
acceptance topology uses:

- site/global limit: one;
- per-Personal-Workspace limit: one;
- per-capability limit: one;
- per-node limit: one.

Production site-wide, capability, class, and node ceilings must be explicitly
configured. Missing production configuration means zero eligible capacity;
the platform must not infer a value from worker count, process count, Agent
Compose stats, or host hardware.

Ordinary limit reductions drain existing Admission Grants and block new work.
They do not cancel work, repin a Task, or weaken a class. An emergency safety
decision uses explicit node quarantine or Runtime fencing instead.

Logical Workspace and policy concurrency may release after Runtime Execution
authoritatively fences or terminates a lost run. Physical capacity on the lost
node remains unavailable until containment and reset are proved. This allows a
new Runtime Run on another node without pretending the old node is safe.

## Resource Class

A Resource Class is an immutable, versioned Scheduling Policy entry with a
canonical digest. It contains at least:

- CPU milli-units;
- memory bytes;
- process or PID limit;
- ephemeral-disk bytes and inode reservation;
- accelerator count, type, memory, and attested partition identity where
  applicable;
- worker and capability keys;
- Execution Policy key;
- operating-system, architecture, driver, image, runtime, and readiness
  constraints;
- network and egress policy key;
- admission cost and optional scheduling-pool ceiling.

Runtime Binding carries an approved requirement key. Scheduler resolves it to
one exact Resource Class and never silently selects smaller memory, weaker
isolation, a different accelerator, broader network access, or another
capability. An explicitly approved equivalence set may list alternatives in
the immutable binding; Scheduler cannot invent one.

Memory, PIDs, disk, inodes, and accelerator capacity cannot be overcommitted.
CPU overcommit is disabled by default. Accelerator sharing is allowed only
when a hardware- or runtime-enforced partition is independently attested and
advertised as an allocatable unit.

Runtime Execution supplies node allocatable capacity after operating-system,
daemon, cache, cleanup, and safety reserves. Missing, unknown, stale, or
unenforced dimensions fail admission closed. Agent Compose stats and ordinary
host telemetry are observations, not capacity reservations.

## Capacity admission and placement

ClaimAndAdmit performs:

1. validate Work Item, Personal Workspace and machine authority, Task and run
   generations, deadlines, recovery mode, release and catalog safety epochs;
2. validate the exact Runtime Binding, Resource Class and Execution Policy;
3. validate an Active Quota Reservation when the capability may create
   quota-bearing provider usage;
4. apply queue eligibility, priority, aging, fairness deficit, and every
   concurrency limit;
5. filter nodes by fresh Runtime Execution facts, exact capability, policy,
   immutable image and package access, network and secret readiness, capacity,
   quarantine, and reset state;
6. choose a node by best fit over the hard resource vector, preserving scarce
   capacity; use stable node identity to break a tie;
7. atomically reserve counters and record the Delivery Claim and Admission
   Grant.

Existing immutable inputs or cached packages may break a tie between equally
eligible nodes. They cannot prove availability, integrity, ownership, or
recovery and never override fairness or a hard constraint.

Runtime Execution revalidates current node facts and the exact Admission Grant
before granting a Sandbox Lease. If a binding or fact changed, the grant is
rejected or expires unbound and the Work Item is safely reconsidered.

## Quota Reservation seam

A Work Item for a capability that may use an LLM or generative-image provider
is ineligible for capacity admission until its Phase Run Quota Reservation is
Active.

Scheduler stores and validates only the opaque Reservation identity,
generation, mode, and admission disposition. It cannot create, renew, close,
settle, inspect Ledger entries, or reinterpret usage.

In enterprise V1 Observe mode, quota shortage returns an allowed observation
and does not block admission. Missing ownership, persistence, policy integrity,
active status, generation, or recovery authority still fails closed.

The interface preserves a future Insufficient or Deferred enforcement
disposition. Changing from Observe to Enforce remains a separate product
decision and requires no change to Work Item, fairness, class, or grant
identity.

## Backpressure and saturation

Queue budgets cover outstanding-item count and serialized payload bytes at
site, pool, and Personal Workspace scopes. The repository's local and
acceptance defaults are:

- 1,000 outstanding User Work Items site-wide;
- 100 outstanding items per Personal Workspace;
- 64 KiB maximum immutable Work Item payload;
- high watermark at 80 percent;
- 10 percent of the queue budget reserved for accepted-Task continuation and
  safety control.

Production must explicitly configure these values. They are operational
defaults, not an external throughput SLO.

At the high watermark, new Task start, new manual-edit start, and User retry
return a typed retryable QueueSaturated result without committing the
enqueue-causing Task transition. Accepted Task continuation, cancellation,
fencing, release, and reconciliation use the reserved budget. At the hard
watermark, nonessential Background offers stop.

AdmissionDeferred means the Work Item is valid but one or more temporary
capacity or concurrency limits currently prevent admission. It does not create
a Delivery Claim or count as a delivery failure.

Unschedulable means no currently registered healthy node can ever satisfy one
or more exact requirements. The error identifies a safe missing dimension for
administrator diagnostics without weakening the Resource Class or exposing
User content.

Notifications wake schedulers after commit. Jittered polling is a fallback.
There is no unbounded in-memory queue, hot polling loop, or wait while holding
a database transaction.

## Delivery Claim, heartbeat, and lease timing

The versioned V1 defaults are:

- Delivery Claim TTL: 60 seconds;
- Delivery Claim heartbeat: no later than every 20 seconds;
- unbound Admission Grant TTL: 60 seconds;
- Execution Node readiness heartbeat: every 20 seconds, unavailable after 60
  seconds without a trustworthy refresh;
- Sandbox Lease TTL: 90 seconds with heartbeat no later than every 30 seconds;
- capacity-bearing claim batch: one, and never more than declared free worker
  delivery slots.

Heartbeat cadence must remain no slower than one third of the applicable TTL.
A delivery heartbeat cannot extend an unbound Admission Grant indefinitely.
Sandbox renewal never crosses a Runtime deadline, cancel or revocation fence,
stale safety epoch, node quarantine, or invalid authority.

Delivery Claim binds Work Item, claim generation, worker authority, operation,
payload digest, Admission Grant, Scheduling Policy version, Scheduler epoch,
heartbeat, and expiry. Claim expiry changes delivery ownership only.

Runtime completion still requires the Runtime Run, operation, Sandbox Lease,
fence, Runtime Binding, Task revision, and safety epochs defined by Runtime
Execution. A queue token is not a Runtime fence.

## Stale completion and capacity release

- A stale worker cannot acknowledge, renew, release capacity, or mutate a Work
  Item after claim generation changes.
- If downstream acceptance already committed before claim loss, reconciliation
  marks the original Work Item Accepted; it does not create new work.
- If no downstream acceptance exists, an unbound Admission Grant expires,
  releases its reservations, and returns the same Work Item to eligibility.
- Once a Sandbox Lease binds the grant, Delivery Claim loss cannot release
  capacity.
- Runtime cancellation, timeout, revocation, or loss fences before best-effort
  stop. Late success cannot cross the fence.
- Node loss makes physical capacity unknown and quarantined, not free.
- Process termination, logical Runtime terminal, C04 discard, physical
  containment/reset, and capacity release remain separately evidenced.
- A new execution attempt after node loss requires Task Orchestration to create
  a new Runtime Run. The old run never receives another lease.

Admission and release use Scheduler epoch, policy version, grant generation,
node-capacity generation, Runtime Run, lease, and fence compare-and-swap.
Duplicate exact evidence is idempotent; mismatched or stale evidence is
diagnostic and cannot alter counters.

## Delivery retry and dead-letter

Delivery retry reuses the same Work Item and operation identity. It never
creates a Phase Run, Runtime Run, or provider Attempt.

Transient delivery failures use full-jitter exponential backoff from one
second, doubling per counted attempt, capped at five minutes. The default
maximum is eight counted delivery failures.

Capacity waiting, ordinary queue waiting, Observe-mode quota shortage, and an
unavailable scheduling pool do not count. Claim expiry counts only after
Scheduler proves downstream did not accept. If acceptance exists, the item is
reconciled instead.

Unknown schema, payload-digest conflict, cross-Workspace binding, invalid
authority, integrity failure, or permanent incompatible capability is
quarantined or dead-lettered immediately rather than retried transiently.

Dead-letter produces authenticated DeliveryDeadLettered evidence. Task
Orchestration applies the pinned Pipeline policy and decides the Phase or Task
effect. Scheduler never automatically creates business retry.

Administrator redrive is reason-bound and audited. It preserves Work Item,
operation, payload digest, history, and downstream identity while advancing a
redrive generation and resetting eligibility age. Redrive cannot mutate the
payload. Correcting a payload requires a new authoritative enactment or
business attempt, while the original dead-letter remains historical evidence.

Runtime, tool, model-provider, validation, C04, and publication failures do not
enter Scheduler dead-letter unless the failure was delivery of their
enactment. Their owning modules retain their retry semantics.

## Authorization, integrity, and non-leakage

Every Scheduler machine capability binds Personal Workspace, Task, Phase Run,
optional Runtime Run, operation, purpose, authority generation, and expiry.
Workers and nodes cannot claim ownership, priority, class, policy, capacity, or
counter values.

Scheduler revalidates disabled-User state, authorization generation, Task and
run revisions, recovery mode, release and catalog epochs, Reservation, and
policy before claim and admission. Retryability cannot weaken these checks.

Scheduling Policy and Resource Class publication, concurrency changes, node
quarantine, manual capacity release, dead-letter resolution, and redrive
produce authoritative audit facts. These administrator operations expose
content-free metadata and grant no User-content access.

User-facing errors do not reveal another Personal Workspace, Task, Work Item,
node, capability inventory, content, path, object locator, credential, or
provider endpoint. Metrics labels do not use Workspace, Task, Run, Work Item,
claim, lease, or grant identities.

## Retention, backup, restore, and repair

Scheduler Work Item identity and immutable binding, final disposition,
Scheduling Policy and Resource Class versions, Admission Grant evidence root,
dead-letter and redrive history, and mandatory audit are authoritative
execution history. An unresolved dead-letter cannot be reclaimed.

High-frequency heartbeat and renewal detail may be compacted only after a
terminal disposition into authenticated first, last, count, reason, and
evidence-root summaries under the
[observability retention policy](./observability-audit-and-cleanup-debt.md).
Compaction never removes a current claim, grant, unresolved dead-letter, stale
evidence conflict, or capacity-reconciliation obligation.

Authoritative Scheduler records belong to Platform PostgreSQL and the joint
Recovery Point. Broker messages, Redis data, worker memory, local poll state,
Execution Node readiness, and live capacity are not backed up as business
authority.

Restore:

1. advances Scheduler epoch and invalidates every old Delivery Claim and
   unbound Admission Grant;
2. retains bound grants only as reconciliation obligations and asks Runtime
   Execution to fence or classify non-terminal leases;
3. restores no node as Ready until fresh readiness, capacity, containment, and
   reset evidence is accepted;
4. rebuilds queue state from Task Orchestration outbox facts, Scheduler final
   dispositions, and Runtime authoritative operation facts;
5. reconciles a possibly accepted operation before any redelivery and never
   blindly starts provider or Runtime work;
6. preserves dead-letter and capacity debt until explicitly resolved.

Repair can restore only facts that match the original authority, schema,
identity, digest, policy version, operation, Workspace and run binding, lease
and fence. A broker scan, process list, Agent Compose database, log, metric, or
host directory cannot invent queued, accepted, released, or successful state.

## Operational evidence projected through observability

The [observability contract](./observability-audit-and-cleanup-debt.md)
receives:

- queue depth, serialized bytes, oldest eligible age, priority class, pool,
  saturation watermark and rejection category;
- active Workspace count, fairness rounds and deficit lag without using
  Workspace identity as a metric label;
- admissions, deferrals and unschedulable results by safe limiting dimension;
- global, capability, class, and node allocation and fragmentation;
- claim acquisition, heartbeat, expiry, stale acknowledgement and
  reconciliation;
- Admission Grant reserve, bind, expiry, release and stale-release conflict;
- node readiness expiry, quarantine, containment, reset and capacity debt;
- delivery attempt, backoff, dead-letter, redrive and resolution;
- Reservation validation, Observe disposition and structural rejection;
- Scheduler epoch, policy and Resource Class version.

High-cardinality identities belong in protected traces, audit records, and
administrator diagnostics, not metric labels. Telemetry outage cannot roll
back an authoritative scheduling decision; mandatory audit failure for an
audited policy or administrator mutation fails that mutation closed.

## Highest-level scenarios and adapter contracts

Scheduler is the highest-level scheduling test seam. A deterministic harness
with controllable clock, PostgreSQL transactions, node facts, workers, Runtime
Execution, Usage Accounting, and broker faults covers:

- equal Workspace fairness under one noisy producer and several quiet
  Workspaces;
- Interactive manual edit inside one Workspace without cross-Workspace
  priority bypass;
- Standard and Background aging, large admission cost, incompatible pools and
  absence of head-of-line starvation;
- global, Workspace, capability, class, and node limits individually and in
  combination;
- exact Resource Class matching, GPU partitioning, disk and inode pressure,
  stale readiness, quarantine and reset;
- Active Observe-mode Reservation, missing or stale Reservation, and
  future-enforcement result compatibility;
- enqueue transaction failure, duplicate offer, same-key different-payload
  conflict, queue saturation and reserved continuation capacity;
- claim loss before and after downstream acceptance, acknowledgement loss,
  stale heartbeat, duplicate acceptance and stale release;
- unbound grant expiry, Sandbox Lease binding, cancel, timeout, late success,
  node loss and physical-capacity quarantine;
- delivery backoff, immediate poison work, eight-failure dead-letter, immutable
  redrive and new-payload rejection;
- restore with queued, delivering, bound, dead-lettered and ambiguous work;
- serial local and multi-node adapters producing identical decisions.

PostgreSQL, Task Orchestration, Runtime Execution, Usage Accounting, node-fact,
worker, broker, notification, clock, audit, backup and query adapters receive
black-box contracts. Tests assert identities, decisions, order, claims,
grants, counters, fences, evidence and outcomes rather than SQL shape, queue
product, worker loop, process count, paths, Agent Compose state, log text or
dashboard values.

## Acceptance and production topology

The local acceptance adapter advertises one logical Execution Node, one
enforced active slot, and only the Resource Classes it can genuinely satisfy.
An unsupported class remains Unschedulable; acceptance never weakens it.

Production runs multiple independent Execution Nodes. Each Agent Compose node
has its own daemon and data root. Runtime Execution retains the node and opaque
external-run mapping. Multiple Scheduler and worker instances use PostgreSQL
compare-and-swap or row locking to form one logical single-site authority.
Nodes never share an Agent Compose data root, and no scheduler depends on a
host path.

Production numerical class vectors, node count, site ceilings, and hardware are
versioned deployment inputs. Production startup or admission fails closed when
they are absent. This avoids making an architecture ticket a hardware purchase
or SLO commitment while keeping the first implementation contract complete.

## Hard cutover and deletion test

This is replace-not-layer migration. The target architecture deletes:

- ordinary-Task-first and manual-edit-second polling;
- Task status and updated-at order as queue and fairness authority;
- Task and edit-session execution-claim columns as target delivery or lease
  authority;
- worker-owned Task progression and direct next-handler calls;
- a separate manual-edit scheduler;
- Runtime path, session, recent-run, process, or Agent Compose state as claim,
  capacity, recovery, or success authority;
- Redis or another broker as the unique queue record;
- queue delivery retry that creates Phase Runs, Runtime Runs, or changes Task
  status directly.

Redis may remain as a notification or delivery optimization through the same
adapter contract. Legacy in-flight claims, sessions and paths are not converted
to new claims or leases. Issue 17 owns record-level hard cutover and terminal
handling.

## Rejected alternatives

Rejected alternatives include Task, Phase Run, or Runtime Run as the universal
queue item; global FIFO; strict priority; manual edit jumping across
Workspaces; Tenant fairness; User-selected priority; administrator ordinary
work boosts; Redis, NATS, worker memory, Agent Compose, process lists, or stats
as authority; optimistic hard-resource overcommit; capacity fallback that
rewrites a Runtime Binding; immediate physical release on lease or node loss;
Scheduler-created business retry; quota enforcement in V1; blind replay after
ambiguous acceptance; mutable dead-letter payload redrive; and a compatibility
facade around the legacy worker loops.

## Stable downstream inputs and remaining fog

- The [observability and audit contract](./observability-audit-and-cleanup-debt.md)
  receives the signal catalog and authoritative-versus-projection boundary
  described above.
- Issue 17 receives the scheduler deletion test and the prohibition on
  converting legacy claims, sessions, paths, or queue state.
- Runtime Execution receives exact Admission Grant, class, node, grant
  generation, release, and stale-evidence rules.
- Task Orchestration receives Work Item enqueue, accepted delivery,
  dead-letter evidence, and the rule that Scheduler never creates a business
  attempt.
- Usage Accounting retains exclusive Quota Reservation and Ledger authority.

Superseded decisions: none. This contract follows the existing issue 18
correction that Personal Workspace and Task transfer are not supported.

New decision-only tickets: none.

Remaining fog affecting the first scheduling specification: none. Concrete
production node count, CPU, memory, disk, inode, accelerator, site ceilings,
sandbox driver, schema, serialized names, queue vendor, deployment autoscaling,
and external SLO remain explicit deployment, adapter-acceptance, or later
product inputs. They do not reopen authority, fairness, state, failure, or
interface decisions.
