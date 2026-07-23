# Run runtime capabilities through fenced sandbox leases

SlideSmith will place a `Runtime Execution` deep module between Task Orchestration and owned Execution Data Plane adapters. Task Orchestration creates each Runtime Run identity and Phase Run relationship, while Runtime Execution owns its process state, deadline, cancellation, Sandbox Lease, fence, node execution facts, and verified Runtime Evidence. Agent Workers and Tool Workers share one execution, lease, error, and evidence protocol but remain replaceable adapters; neither can advance a Phase, commit a Runtime View, publish an Artifact Version, select a release, or create authoritative run history.

Every start binds one existing Runtime Run to an exact Runtime Binding, canonical request digest, immutable input manifest, worker class, Execution Policy, deadline, and at most one Sandbox Lease. Exact replay returns the same operation, payload mismatch fails closed, and node loss requires a new Runtime Run rather than a second lease or live migration. Runtime terminal evidence is accepted only against the current Task revision, operation, Runtime Binding, safety epoch, lease, and fence. Cancellation, timeout, and revocation fence first; a late completion cannot cross that fence.

Production Agent and Tool execution is treated as hostile relative to the host and every other Personal Workspace. No sandbox driver is trusted by name. An exact driver, host, mount, network, credential, and reset configuration must pass threat-model and hardening acceptance before admission. Workers receive verified immutable inputs, one isolated C04 Runtime View when mutation is allowed, short-lived purpose-bound secret capabilities, and explicit default-deny network grants without receiving host paths or platform credentials.

## Considered options

- Letting Agent Compose own Runtime Runs, Sandbox Leases, or workspace recovery was rejected because its node-local project, run, sandbox, session, SQLite, and path facts cannot establish SlideSmith business authority or survive node loss.
- Giving Agent Workers and Tool Workers separate state machines was rejected because identity, lease, cancellation, retry, fencing, and evidence semantics would drift while their capability payloads can remain typed variants behind one protocol.
- Reusing one Runtime Run on another node after loss was rejected because hidden sandbox state cannot be proved or transferred and the accepted model requires one independent lease per run.
- Treating a vendor terminal result as Phase success was rejected because guest output is an untrusted proposal and Phase validation, C04 commit, and publication retain separate authorities.
- Preserving CLI, session, or path compatibility was rejected because it would keep node-local layout as execution and recovery authority and fail the deletion test.

## Consequences

- The external interface accepts typed start and cancel intents and exposes Runtime snapshots and evidence; sync, poll, callback, and queue transports normalize behind the same durable asynchronous contract.
- Runtime Run terminal state is immutable, while process containment, Runtime View discard, sandbox cleanup, and capacity release remain separately evidenced facts.
- Runtime Execution owns truthful node and lease facts and enforcement. The Scheduler owns fairness, concrete resource classes, placement, and admission policy and cannot weaken the Runtime Binding.
- Agent Compose production integration uses a pinned v2 API adapter, stable request identity with SlideSmith payload binding, one daemon and data root per node, protected transport, opaque external IDs, and configuration-specific security acceptance. CLI shell-out and shared host paths are not the enterprise seam.
- Migration hard-replaces direct TaskService execution, single-run Phase coupling, session and path inference, shared Docker sockets and data roots, and fallback recovery from directories or recent runs.
