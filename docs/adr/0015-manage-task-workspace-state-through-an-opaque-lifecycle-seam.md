# Manage Task Workspace state through an opaque lifecycle seam

Each Task Workspace is one logical Task-owned identity whose physical materialization is disposable. The Platform Control Plane remains authoritative for Task, Phase Run, Checkpoint metadata, and commit decisions, while a deep Task Workspace Lifecycle module in the Execution Data Plane owns workspace bytes, Runtime Views, durable Checkpoint content, restore, expiry, and Cleanup Debt. Its external seam accepts only high-level lifecycle intent and opaque identities; production uses a remote-but-owned transport adapter, development and tests use local or in-memory adapters, and host paths, mounts, file operations, storage vendors, and execution-node details never cross the interface.

## Considered options

- Treating a shared canonical directory as the Task Workspace was rejected because it leaks node-local paths, prevents independent worker scaling, and makes directory availability part of business recovery semantics.
- Retaining each Runtime Run's full session snapshot was rejected because it duplicates immutable runtime packages and caches, makes recovery depend on directory scanning, and already caused `/data/workspaces` to exhaust inodes.
- Combining workspace lifecycle, Runtime Run/Sandbox Lease ownership, and Artifact publication was rejected because their authorities, retention rules, and highest-level test seams differ.

## Relationship to earlier decisions

This decision deepens ADR 0002 and ADR 0012 and preserves the Sandbox Lease and publication seams in ADR 0007 and ADR 0008. It refines ADR 0011: Agent Compose remains an Execution Data Plane adapter and may enact work inside a Runtime View, but it does not own Task Workspace lifecycle or recovery authority and its session filesystem is never authoritative.

## Consequences

- One authoritative writer advances a Task Workspace. Every mutating Runtime Run receives an isolated transactional Runtime View and can only propose explicit output; validation precedes atomic commit or discard.
- A successful Phase Run binds its validated contract, resulting Task Workspace Revision, and distinct Checkpoint identity. Commit is fenced and idempotently reconcilable after crashes, and it fails closed until Checkpoint metadata and referenced content are durable outside the execution node.
- A Checkpoint contains only declared recoverable Task-owned mutable state. Runtime Releases, Template Versions, Resource Bundles, Source Material, shared caches, sessions, duplicate durable inputs, and failed residue remain outside it; distinct Checkpoints may share content-addressed payloads.
- Checkpoint retention follows recovery reachability and explicit references. Artifact Versions remain independently retained business publications.
- Manual editing reuses the Task's Task Workspace identity, reconstructing it from the latest Artifact Version if execution state expired; a successful edit creates a new Task Workspace Revision and Checkpoint before the publication module publishes a child Artifact Version.
- Lifecycle facts and Cleanup Debt are durable and observable. Metrics, tracing, and logs are projections through internal adapters rather than caller-managed pass-through interfaces.
- The Task Workspace Lifecycle interface is also its highest-level test seam. Path-layout tests are replaced by lifecycle and adapter contract tests, after which session scanning, last-session authority, copied runtime packages, path bags, direct recursive deletion, and per-workflow cleanup markers are removed rather than hidden behind a compatibility facade.
