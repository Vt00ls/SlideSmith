# Centralize Task transitions behind a command-decision seam

SlideSmith will place a `Task Orchestration` deep module inside the Platform Control Plane and make one command-decision operation the mutation seam for Task, Generation Pipeline, and Phase Run state. The operation accepts an authenticated or evidence-bearing transition intent with a Task identity, expected Task revision, and idempotency identity; it loads the pinned Pipeline Version and authoritative history, validates the intent, and commits one transition decision plus any idempotent enactments in a single PostgreSQL transaction.

Task aggregate status is a coarse projection of the pinned Pipeline Version, current Phase cursor, Confirmation Gate, active Phase Run, and terminal outcomes. Route-specific Phase order does not live in status constants or worker switches. Runtime evidence, Task Workspace Lifecycle commit evidence, publication evidence, and authorized user commands all enter the same decision seam, but they retain distinct authority types. A Runtime Run never advances the Generation Pipeline directly, and retries always create a new Phase Run attempt.

Events, audit records, and enactment outboxes remain useful internal products of an accepted transition decision. They are not the external mutation boundary. This preserves pre-write authorization, optimistic concurrency, and fail-closed rejection without requiring an untrusted, stale, or semantically invalid external event to become an authoritative fact.

The decision is supported by the throwaway prototype on branch `codex/prototype-task-orchestration-19`, commit `0c7295effa11d7ebb71d2600e9a00d17f2523b4a`. The prototype exercised all three Routes, zero-Runtime-Run Confirmation Gates, fixed locks, post-publication manual edit, retry, cancellation fencing, claim loss, acknowledgement loss, and crash reconciliation against command/decision and event/enactment boundaries.

## Considered options

- An event/enactment external interface was rejected because user intent, worker availability, Runtime evidence, and C04 evidence do not share the same authority. Appending them before authorization and concurrency validation leaves rejected facts in the authoritative stream; adding a pre-append validator recreates the chosen command-decision seam.
- Independent workflow services or state machines per Route were rejected because Confirmation Gates, retry, cancellation, evidence acceptance, and Phase Run history would be duplicated and could drift. Route variation belongs to immutable Pipeline Versions.
- Keeping `TaskService` as the orchestration boundary was rejected because it mixes HTTP entry, route dispatch, execution claims, Runtime invocation, path handling, publication, recovery, and manual edit, exposing shallow mechanics and duplicating authority.
- Allowing Phase handlers, workers, Runtime Runs, C04, or publication adapters to advance Task state directly was rejected because it creates multiple writers and makes crash reconciliation depend on call order.

## Consequences

- The PostgreSQL transaction that records a transition decision and its enactments is the linearization point. A response lost after commit is replayed by idempotency identity; a crash before commit leaves no decision.
- Workers and queue adapters deliver durable enactments. Claim loss changes only delivery ownership, not Task or Phase Run outcome. Reconciliation reissues the same enactment identity instead of creating a new attempt.
- Each Phase Run references one Phase definition in the pinned Pipeline Version and owns zero or more Runtime Runs. Confirmation and publication Phases may have zero Runtime Runs. Only Phase validation and required C04 commit evidence can make a mutating attempt successful.
- Cancellation first fences new work and any possible C04 commit. The Task becomes terminally cancelled only after fencing evidence proves that no mutation can still commit; a commit that already linearized is recorded before later work is stopped.
- Post-publication manual edit uses the same decision seam, Task Workspace identity, and immutable Task locks. It is a platform-approved Phase graph rather than a fourth Route or a separate orchestration authority.
- Migration replaces route/status switches, direct worker calls, path-bearing run coupling, and the separate manual-edit progression path. It does not preserve them behind a compatibility facade.
