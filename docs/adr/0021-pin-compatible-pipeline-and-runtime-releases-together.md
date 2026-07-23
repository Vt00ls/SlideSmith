# Pin compatible Pipeline Versions and Runtime Releases together

SlideSmith will manage Pipeline Versions, Runtime Releases, compatibility evidence, rollout policy, and revocation through one `Release Management` deep module in the Platform Control Plane. When a Task's Route is determined, Task Orchestration atomically records one immutable Execution Lock that binds an exact Pipeline Version, Runtime Release, and Compatibility Approval; retry, recovery, cancellation, and manual edit never resolve a floating version or silently replace that lock.

Pipeline Versions and Runtime Releases remain independently publishable and can form several explicitly approved compatible pairs. This avoids republishing an unchanged Pipeline Version for every Runtime security or toolchain release, while the exact pair and positive compatibility evidence preserve reproducibility. A mutable tag, Runner Profile, feature-flag snapshot, semantic-version range, or live runtime handshake is insufficient authority.

Ordinary activation, rollout, rollback, and deprecation affect only new Task locks. Revocation is a distinct terminal trust decision reserved for security, integrity, authorization, or platform-control contract failure; it blocks new pins and fences uncommitted work for already locked Tasks without rewriting their locks or automatically deleting existing Artifact Versions.

## Considered options

- Hard-wiring one Runtime Release into each Pipeline Version was rejected because independent Runtime patches would force semantically unchanged Pipeline republishing.
- Selecting a Runtime Release per Phase was rejected because it violates Task-level reproducibility and multiplies compatibility and recovery states.
- Runtime negotiation from `latest`, tags, version ranges, environment configuration, or feature flags was rejected because it cannot prove exact capability, artifact, or recovery identity.
- Automatically upgrading locked Tasks was rejected because historical retries and manual edits would no longer enact the same approved Generation Pipeline.

## Consequences

Approved manifests and Compatibility Approvals are immutable and digest-bound. A Task pin is linearized by the same PostgreSQL transaction that records its Route and Task revision. Runtime Execution receives only an exact capability binding and opaque artifact handles, while rollout policy, Pipeline graph ownership, OCI locators, signing, retention, and repair remain behind Release Management and its internal adapters.
