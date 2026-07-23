# Atomically activate and pin catalog template closures

SlideSmith will manage Catalog Template identity, immutable Template Version and Resource Bundle publication, lifecycle, scan and license eligibility, and one current Active Template Version per Catalog Template through a `Catalog Publication` deep module in the Platform Control Plane. A Draft Candidate is mutable staging rather than a published domain identity. Approval creates immutable manifests and package references; activation atomically changes the Catalog Template's current-version pointer and catalog generation only after the complete Resource Bundle closure is verified and Active.

For a Generation Route, Task Orchestration atomically records one immutable Template Lock in the same PostgreSQL transaction that accepts the Route and Execution Lock. The Template Lock binds the exact Template Version manifest and package, the canonical transitive Resource Bundle closure and its digests, compatibility evidence, and the observed catalog generation. Retry, recovery, cancellation, and manual edit retain this lock; ordinary activation, rollback, deprecation, or catalog retirement affects only new Tasks.

`Disabled` is distinct from ordinary deprecation and physical deletion. A security, integrity, authorization, license, or platform-control failure advances a catalog safety epoch and fences existing uncommitted work without rewriting Template Locks or automatically deleting published Artifact Versions. Valid locks remain retention references, so a planned reclamation cannot remove an exact package needed by an old Task. Fill Templates remain Task-owned Source Material and never enter this publication lifecycle.

## Considered options

- Keeping one mutable disk-synchronized registry row was rejected because it combines identity, lifecycle, path, and current bytes and cannot reproduce an old Task.
- Resolving the current version at Runtime or repinning on retry was rejected because it changes design inputs after the Task decision.
- Offering several active versions directly to ordinary Users was rejected for enterprise V1 because the stable Catalog Template selection would no longer have one deterministic meaning and would introduce a version-management product surface.
- Treating deprecation or catalog removal as deletion was rejected because it would break existing Template Locks and conflict with business-record and Recovery Point retention.
- Inferring shared Resource Bundles from equal files or directory layout was rejected because bytes do not establish dependency, licensing, distribution, or lifecycle authority.

## Consequences

- Catalog selection uses an observed version and catalog generation. Activation and Task selection are transactionally ordered; a stale selection conflicts instead of silently switching versions.
- Approved, Deprecated, or catalog-tombstoned exact versions remain usable by existing locks. Disabled, missing, corrupt, or unlicensed dependencies fail closed and never fall back to a newer version.
- Catalog packages remain read-only inputs outside Task Workspaces and Checkpoints and are materialized through opaque Durable Object capabilities.
- Publication, lifecycle, Task lock, materialization, backup, repair, and legacy conversion can be tested at intent-oriented module seams without exposing paths, locators, vendors, or schema.
- Legacy `TemplateRegistryEntry`, startup disk sync, source paths, and incomplete JSON locks are hard-replaced. Conversion requires exact frozen bytes and complete evidence; ambiguity remains non-executable history for issue 17.
