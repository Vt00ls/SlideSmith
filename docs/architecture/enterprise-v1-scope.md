# Enterprise Intranet V1 Scope

This document records confirmed delivery boundaries for SlideSmith's first enterprise-intranet release. It is updated as the architecture review resolves each scope decision.

The complete record-by-record hard-cutover, ownership backfill, validation,
rollback, and Cleanup Debt contract is recorded in
[legacy-business-migration-and-compatibility.md](./legacy-business-migration-and-compatibility.md).

## Ownership and collaboration

### In scope

- Authenticated Users with one private Personal Workspace each.
- Tasks and Artifact Versions owned through that Personal Workspace.
- Read-only sharing of one Artifact Version through a revocable Share Link protected by a separate Access Code.
- Mandatory Share Link expiry with a seven-day default, a thirty-day platform hard maximum, and an administrator-configured maximum that may only be shorter.
- No in-place lifetime extension: an Owner may shorten or revoke a grant, while a later expiry requires a new Share Link.
- Owner revocation and Access Code rotation, generation-bound Verification Sessions, rate-limited verification, and access auditing without storing recoverable plaintext link or code secrets.
- Administrative recovery or audited Workspace Export followed by an independently authorized purge after a User is disabled.

### Out of scope

- Tenant and Membership layers.
- Projects as containers between Personal Workspace and Task.
- Shared or team Workspaces.
- Workspace invitations, collaborative editing, and team RBAC.
- Personal Workspace or Task ownership transfer between Users.
- Permanent Share Links or a guarantee that recipients cannot redistribute downloaded files.

## Authentication

### In scope

- Integrating with the existing internal centralized sign-in service.
- Mapping a stable external identity to one SlideSmith User and Personal Workspace.
- Retaining a disabled User's Tasks and Artifact Versions for administrative recovery or Workspace Export until an explicit purge is authorized.
- Allowing Share Link and Access Code access to one Artifact Version without an authenticated workspace session.

### Out of scope

- Storing passwords for ordinary Users in SlideSmith.
- Password registration, reset, recovery, SMS verification, or MFA implementation inside SlideSmith.
- Multiple simultaneous identity providers or cross-enterprise identity federation.
- Department synchronization and role mapping from the identity provider.

Protocol-specific OAuth or OIDC integration details are deferred to implementation design. SlideSmith must map a stable identity-provider subject to the User and must not treat a mutable email address as the external identity.

## Platform administration

### In scope

- One minimal Platform Administrator role for User activation, deactivation, recovery, audited Workspace Export, and explicit purge of retained work.
- Publishing Runtime Releases and Pipeline Versions independently; approving exact compatible pairs; controlling activation, rollout, deprecation, and revocation; and publishing Catalog Templates, Template Versions, and Resource Bundles through audited manifest/CLI/CI intents.
- Viewing system health, failure diagnostics, storage state, and aggregated usage metadata.
- Triggering cleanup, recovery, and safe rescheduling operations.
- Managing platform feature flags and revoking abnormal Share Links.
- Explicit, reason-bound, time-stamped, exact-target, and audited break-glass access when User content must be inspected for support or recovery.
- Two distinct active Platform Administrators for break-glass request and approval; active grants default to thirty minutes, cannot exceed sixty minutes, and cannot be renewed.

### Out of scope

- Implicit administrator access to Source Material, Decks, or other User content.
- Administrator self-approval, Owner impersonation, or a BreakGlass Grant that permits mutation, manual edit, Share Link management, arbitrary Workspace browsing, or purge.
- Department administrators, Project administrators, delegated Workspace administration, or multi-level RBAC.
- Unlogged support impersonation or content inspection.
- Reassigning a Personal Workspace or Task to another User.

## Runtime and catalog extensibility

### In scope

- Packaging each approved Core Skill inside a content-addressed Runtime Image and publishing it as a Runtime Release.
- Shipping platform-approved, built-in Pipeline Versions independently of Runtime Releases.
- Requiring an immutable positive Compatibility Approval before one exact Pipeline Version and Runtime Release pair can serve a Task.
- Atomically recording one immutable Execution Lock when a Task's Route is determined; retry, recovery, cancellation, and post-publication manual edit preserve that lock.
- Applying ordinary rollout, rollback, deactivation, and deprecation only to new Tasks while reserving terminal revocation for security, integrity, authorization, or platform-control failures that must fence existing uncommitted work.
- Deriving an exact Runtime Binding for every Runtime Run and executing it through a fenced Sandbox Lease on an attested Execution Node.
- Supporting Agent Workers and Tool Workers behind one Runtime Execution lifecycle, cancellation, error, and evidence protocol without allowing either worker class to advance a Phase.
- Treating production Agent and Tool execution as hostile relative to the host and other Personal Workspaces; requiring configuration-specific threat-model and hardening acceptance before an Execution Node can admit work.
- Supplying verified immutable inputs, isolated Runtime Views, short-lived purpose-bound secrets, and explicit default-deny network grants without exposing host paths or platform credentials to workers.
- Publishing Catalog Templates, Template Versions, and Resource Bundles through an administrator-controlled versioned release process.
- Maintaining at most one current Active Template Version per Catalog Template for ordinary User selection while retaining exact old packages for existing Template Locks.
- Atomically recording a Generation Route's immutable Template Lock with its Execution Lock, including the complete Resource Bundle closure; retry, recovery, cancellation, and manual edit preserve it.
- Applying ordinary catalog activation, rollback, deprecation, and retirement only to new Tasks while using Disabled plus a catalog safety epoch for security, integrity, authorization, license, or platform-control failures that must fence existing uncommitted work.
- Allowing ordinary Users to select approved Catalog Templates without exposing arbitrary historical versions or a self-service publication interface.
- Allowing a User to upload a Fill Template as Source Material for one Task.
- Supporting an initial administrator workflow based on release manifests, CLI tooling, or CI/CD rather than requiring a full management UI.

### Out of scope

- User-published Core Skills, executable scripts, or Pipeline Definitions.
- A Skill, template, or Resource Bundle marketplace.
- Treating a User's Fill Template as a public Catalog Template.
- An online template editor or self-service template publication workflow.
- A complete administrator catalog-management portal.
- Ordinary User selection among several simultaneous active Template Versions of one Catalog Template.
- Floating `latest`, semver-only compatibility, automatic in-place Task release upgrades, or repinning an existing Task during ordinary rollback.
- Declaring a sandbox driver safe by product name, using an unreviewed driver or host configuration for production, or treating Agent Compose project, session, sandbox, path, or SQLite state as SlideSmith authority.
- Arbitrary caller-provided shell execution, worker-created Runtime Runs, or a vendor terminal status that directly advances a Phase.

## User workflows

### In scope

- Generation Route Tasks that create a new Deck from Source Material under an approved Catalog Template.
- Beautify Route Tasks that redesign an existing source Deck while preserving its frozen visible content and Slide count.
- Template Fill Route Tasks that fill a User-uploaded Fill Template with new Source Material.
- Confirmation Gates for reviewing and approving route-specific plans and constraints.
- Task cancellation, retry, and recovery from validated Checkpoints.
- Artifact Version history, individual Artifact downloads, and read-only sharing through Share Links and Access Codes.
- Live Preview and post-publication manual edits of the latest Artifact Version that reuse the Task's Task Workspace identity, reconstruct expired execution state when necessary, and create a new Task Workspace Revision and Checkpoint before publishing a child Artifact Version.

### Out of scope

- Concurrent multi-User editing of one Task.
- A general-purpose object-level editor intended to replace PowerPoint.
- Comments, approval workflows, and work assignment between Users.
- A desktop client or offline synchronization.
- User-defined Routes or arbitrary third-party workflow plugins.

## Usage and quota

### In scope

- Routing every production LLM and generative-image provider call through the Platform-controlled LLM Gateway without giving sandboxes, Agent Compose, Agent Workers, or Tool Workers provider credentials or direct provider egress.
- Recording every real provider retry or fallback as a distinct Gateway Attempt and issuing authenticated, content-free Usage Receipts with explicit provider-reported, estimated, unknown, not-applicable, or proven-no-send evidence state.
- Capturing measured usage when the provider and runtime supply it.
- Attributing usage to Personal Workspace, Task, Phase Run, and Runtime Run.
- Retaining append-only usage history, offsetting corrections, late evidence, and unresolved reconciliation state while presenting actual, estimated, and unknown totals separately.
- Preserving the Phase Run-scoped Quota Reservation domain boundary in observation mode for later enforcement.

### Out of scope

- Blocking task execution based on quota.
- Enforcing real token, image, compute, or monetary limits.
- Billing, payment, invoicing, or departmental chargeback.
- Treating missing usage as zero, allocating shared provider aggregate discrepancies proportionally to Tasks, or deriving authoritative usage from Agent Compose results, logs, traces, or Runtime outcome.

## Data retention

### In scope

- Persisting Task metadata, Artifact Versions, Share Link configuration, Gateway Call and Attempt evidence roots, Usage Receipts, Usage Ledger entries, Quota Reservations, corrections, Execution Locks, Template Locks, Compatibility Approvals, release and catalog lifecycle, safety, withdrawal, tombstone, and audit history as business records.
- Retaining Artifact Versions until an authorized User explicitly deletes them or a future administrator retention policy applies.
- Revoking Share Links when their Artifact Version is deleted or becomes unavailable.
- Terminally invalidating a Personal Workspace's Share Links on User disable or identity rebind, and requiring new grants after reactivation.
- Automatically releasing Sandbox Leases and cleaning sandbox state, Runtime Run temporary directories, expired Task Workspaces, disposable Checkpoints, caches, and incomplete publication residue.
- Retaining authoritative Scheduler Work Item dispositions, Scheduling Policy and Resource Class versions, Admission Grant evidence roots, unresolved dead-letter state, redrive history, and mandatory scheduling audit in Platform PostgreSQL.
- Recovering required mutable execution state from validated Checkpoints rather than treating a live Task Workspace as permanent storage.
- Configurable cleanup policies with observable cleanup failures and retriable Cleanup Debt.
- Treating Task Workspace as one logical identity whose node-local materialization is disposable and reconstructable.
- Capturing only declared recoverable Task-owned mutable state in Checkpoints; Runtime Releases, Template Versions, Resource Bundles, Source Material, shared caches, sessions, and failed residue stay outside.
- Retaining Checkpoints by recovery reachability and explicit references while allowing distinct Checkpoints to share content-addressed payloads.
- Recording unresolved cleanup as durable Cleanup Debt with resource identity, retry history, failure evidence, and estimated bytes and inodes.
- Exporting a disabled User's Personal Workspace through audited break-glass, verifying delivery outside SlideSmith, and requiring a separate administrator intent before purging its retained content.
- Retaining a 35-day joint PostgreSQL and durable-object point-in-time recovery window in at least one encrypted, immutable, independently controlled backup domain.
- Removing authorized deleted or purged content from online authority immediately while allowing encrypted, inaccessible bytes to remain in already locked backup copies until the recovery window expires.
- Retaining exact Pipeline and Runtime manifests, OCI images, supplementary packages, and compatibility evidence while an Execution Lock, active rollout, integrity incident, or retained Recovery Point references them.
- Retaining exact Template Version and Resource Bundle manifests and packages while Approved, Active, Deprecated, referenced by a Template Lock, required by an incident or license obligation, or included in a retained Recovery Point; ordinary catalog retirement never breaks an old Task.

### Out of scope

- Automatic expiration of valid Artifact Versions in the first release.
- Legal hold, records-management, e-discovery, or archival-tier workflows.
- Keeping sandbox or hidden runtime state as a durable Task record.
- Treating an export attempt, download start, or failed delivery as authorization to purge a Personal Workspace.
- Immediate physical erasure of deleted content from already immutable backup copies.
- Treating disaster-recovery backup as legal archive, records management, or permanent business history.

## Observability and authoritative audit

### In scope

- Keeping authoritative domain facts and mandatory audit facts in Platform
  PostgreSQL under their owning Platform Control Plane modules while treating
  metrics, traces, structured logs, dashboards, and external audit delivery as
  incomplete, expiring projections.
- Correlating User, Personal Workspace, Task, Phase Run, Runtime Run, Sandbox
  Lease, Checkpoint, Artifact Version, Scheduler, Gateway, and Cleanup Debt
  through typed authoritative identities, revisions, generations, fences, and
  evidence rather than depending on a trace ID.
- Propagating sanitized W3C trace context across owned HTTP, queue, remote
  adapter, Runtime, Tool, Agent, and Gateway seams without putting business
  identity, content, or secrets in baggage.
- Mandatory fail-closed audit for content access, break-glass, release and
  catalog publication, scheduling policy administration, usage correction,
  recovery control, purge, and authorized cleanup exceptions.
- Bounded-cardinality metrics, deny-by-default structured telemetry, protected
  diagnostics, explicit unknown/stale state, minimum integrity/security alerts,
  and short telemetry retention independent of business-record retention.
- Persistent, retriable Cleanup Debt with one resource owner, opaque resource
  identity, retry and failure evidence, estimated bytes and inodes, age,
  fencing, blockers, and audited resolution.
- Detecting missing/corrupt referenced content and orphan candidates through
  authoritative inventory reconciliation without using telemetry or storage
  listings as deletion authority.

### Out of scope

- Selecting a telemetry, audit, collector, dashboard, or alert-management
  vendor.
- Treating logs, traces, metrics, dashboards, external audit copies, provider
  aggregates, process state, paths, or inventory scans as business authority.
- Unbounded high-cardinality metric labels, content-bearing telemetry, or
  propagation of User and Workspace identity to providers.
- Legal hold, e-discovery, a general User audit-search product, or a new
  externally visible availability or latency SLO.

## Availability and topology

### In scope

- One enterprise site or data-center deployment.
- Independently restartable control-plane, worker, and execution services.
- Horizontal scaling of workers and Agent Compose execution nodes.
- PostgreSQL-authoritative Scheduler Work Items with at-least-once delivery and queue brokers or notifications as replaceable projections.
- Equal-weight Personal Workspace fairness with bounded platform-assigned priority, aging, and a reserved safety-control lane.
- Layered site, Personal Workspace, capability, Resource Class, and Execution Node concurrency admission.
- Immutable Resource Classes covering enforceable CPU, memory, PIDs, ephemeral bytes and inodes, accelerators, capability, Execution Policy, platform, and network requirements.
- Fenced Delivery Claims and Admission Grants, stale-completion rejection, bounded retry, dead-letter, and audited immutable redrive.
- One-node serial acceptance and multi-node production through the same Scheduler, Runtime Execution, node-fact, Sandbox Lease, and capacity-release interfaces.
- Recovery from worker, sandbox, or execution-node failure by retrying from validated Checkpoints.
- Durable Task, Artifact Version, sharing, and usage records outside execution nodes.
- Persistent PostgreSQL and object storage with backup and recovery exercises.
- Control-plane services that can be deployed as multiple instances.
- A joint Recovery Point RPO of at most 15 minutes for PostgreSQL business state and every committed referenced byte, including total production-site loss or compromise.
- Staged manual recovery: verified Task metadata and Artifact Versions read-only within four hours, and full Source Material, Checkpoint, Execution Lock, compatibility/revocation, release/catalog package, exact Runtime Image, mutation, and execution capability within eight hours.
- Read-only protection when the finalized recovery watermark would exceed the 15-minute RPO.
- Independent immutable backup authority, separated production and restore credentials, and dual control for restore/decrypt or premature retention changes.
- Invalidating every pre-incident Share Link and Access Code after restore; an Owner must issue a new Share Link.
- Advancing a Recovery Epoch so restored Share Grant, Verification Session, and BreakGlass Grant records cannot become valid even when an older database point recorded them as active.
- Monthly sampled and quarterly complete recovery as the target operating drill baseline, with recurring automation allowed to follow the first implementation phase.

### Out of scope

- Cross-site or cross-region active-active operation.
- Automatic disaster-recovery failover.
- Cross-site active standby or a guarantee of transparent recovery without an incident declaration and manual promotion.
- Mandatory physical offline backup media; independently controlled immutable copies remain required.
- Live migration of a running Sandbox.
- Lossless continuation of an in-flight Runtime Run after node failure.
- Lossless workload preemption, User-selected scheduling priority, or administrator priority boosts that bypass another Personal Workspace's fair turn.
- Inferring production capacity from worker count, process state, Agent Compose stats, or unconfigured host resources.
- A formal availability target of 99.99% or higher.
- Global scheduling or multi-region data replication.

A single-server deployment is acceptable for development and acceptance testing, but it is not a highly available production topology.

## Task Workspace lifecycle cutover

### In scope

- A hard cutover with no migration of legacy Task Workspace, Agent Compose session, cache, or failed-residue directories.
- Preserving Task metadata, Source Material, Artifact Versions and Artifacts, Execution Locks, Template Locks, and Phase Run and Runtime Run history.
- Mapping legacy work to an Execution Lock only from exact trusted release evidence; never inferring one from a Runner Profile, environment, mutable tag, path, session, capability snapshot, or recent Runtime Run.
- Terminating old non-terminal Tasks as non-recoverable and requiring a new Task to continue from retained inputs or publications.
- Deleting legacy execution data and recording every failed deletion as Cleanup Debt.
- Using a production remote-but-owned transport adapter and node-independent durable Checkpoint content so workers do not share or learn host workspace paths.

### Out of scope

- Inferring validated Checkpoints by scanning legacy session directories.
- Retaining a path-based compatibility facade after cutover.
- Migrating or resuming in-flight legacy execution state.
