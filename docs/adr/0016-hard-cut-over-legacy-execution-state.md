# Hard-cut over legacy execution state

The Task Workspace Lifecycle rollout will not migrate legacy workspace, Agent Compose session, cache, or failed-residue directories. SlideSmith will preserve Task metadata, Source Material, Artifact Versions and Artifacts, release and template locks, and Phase Run and Runtime Run history; old non-terminal Tasks will be terminated as non-recoverable, and legacy execution data will be deleted. Any deletion failure becomes durable Cleanup Debt rather than a reason to restore directory scanning or path-based recovery authority.

## Considered options

- Inferring a canonical Checkpoint from legacy session directories was rejected because those directories do not prove validated recovery state and would preserve the exact path-based authority the new lifecycle seam removes.
- Keeping a read-only compatibility facade was rejected because callers would continue to depend on host paths and ambiguous last-session state, weakening the new module's depth and deletion test.

## Consequences

The cutover deliberately sacrifices recovery of in-flight legacy execution in exchange for one trustworthy recovery model. Retained business publications and history remain available, but users must start a new Task when an old non-terminal Task is terminated.
