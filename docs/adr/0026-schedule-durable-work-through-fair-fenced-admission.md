# Schedule durable work through Personal Workspace fairness and fenced capacity admission

SlideSmith will place a Scheduler deep module in the Platform Control Plane. A durable Scheduler Work Item represents one Task Orchestration enactment operation without becoming Task, Phase Run, or Runtime Run authority. Platform PostgreSQL owns enqueue, priority, fairness, Delivery Claim, Admission Grant, retry, dead-letter, and final delivery disposition. Redis, NATS, polling, notifications, and worker-local state are replaceable delivery adapters rather than queue authority.

Enterprise V1 schedules User work with equal-weight Personal Workspace deficit round-robin. Priority applies only inside the selected Workspace: post-publication manual edit is Interactive, ordinary Task work is Standard, and non-safety reconciliation is Background. Aging prevents starvation, while cancellation, fencing, lease revocation, capacity release, and recovery coordination use a reserved control lane. Users and administrators cannot grant ordinary work a cross-Workspace priority boost.

Every capacity-bearing admission must satisfy site, Personal Workspace, exact-capability, Resource Class or pool, and Execution Node limits. An immutable Resource Class binds enforceable CPU, memory, PIDs, ephemeral bytes and inodes, accelerators, capability, Execution Policy, platform, and network constraints. Scheduler issues a fenced Admission Grant only from current Runtime Execution node facts; Runtime Execution revalidates it before granting a Sandbox Lease. Missing or stale hard-resource evidence fails closed, and node loss quarantines physical capacity until containment and reset are proved.

A quota-bearing Runtime admission additionally requires an Active Phase Run Quota Reservation, but Scheduler neither owns nor inspects the Usage Ledger. Observation-mode quota shortage does not block enterprise V1. Delivery retry replays the same Work Item and operation; it never creates a new Phase Run or Runtime Run. Ambiguous downstream acceptance is reconciled before redelivery, stale workers cannot acknowledge or release capacity, and poison work moves to an immutable, audited dead-letter state.

## Considered options

- Using Task, Phase Run, or Runtime Run as the universal queue item was rejected because Task is too coarse, Phase Run owns zero or many Runtime Runs, and Runtime Run cannot represent validation, C04, publication, or other enactments.
- Global FIFO and strict priority were rejected because a large Workspace or manual-edit burst could starve other Personal Workspaces.
- Redis, NATS, Agent Compose, worker memory, process lists, or telemetry as authority were rejected because acknowledgement loss, restart, stale workers, and restore would create split-brain queue or capacity state.
- Releasing physical capacity immediately on lease or node loss was rejected because an uncontained process or stale node can still consume resources and emit evidence.
- Letting Scheduler create business retries or weaken a Resource Class was rejected because Task Orchestration and Runtime Binding already own attempt and execution semantics.

## Consequences

- Task Orchestration and Work Item enqueue facts commit atomically in Platform PostgreSQL; delivery remains at least once and dequeue requires durable downstream acceptance.
- The one-node acceptance topology and multi-node production topology use the same claim, admission, node-fact, lease, and release contracts. Production numerical capacity and hardware remain explicit versioned site inputs rather than architecture or SLO commitments.
- Manual edit uses the common post-publication Phase graph and Scheduler path. The existing ordinary-Task-first polling loop, separate edit-session queue, Task/edit claim authority, and status/updated-at ordering are hard-replaced.
- Scheduling records, policy versions, grant roots, dead-letter history, and mandatory audit join the PostgreSQL recovery set. Broker contents and live node readiness are rebuilt rather than restored as authority.
- Metrics, traces, and logs project queue, fairness, admission, claim, lease, retry, dead-letter, and quarantine facts without driving state or placing high-cardinality User identities in metric labels.
