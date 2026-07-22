# Enterprise Intranet V1 Scope

This document records confirmed delivery boundaries for SlideSmith's first enterprise-intranet release. It is updated as the architecture review resolves each scope decision.

## Ownership and collaboration

### In scope

- Authenticated Users with one private Personal Workspace each.
- Tasks and Artifact Versions owned through that Personal Workspace.
- Read-only sharing of one Artifact Version through a revocable Share Link protected by a separate Access Code.
- Mandatory Share Link expiry with a platform default and an administrator-configured maximum lifetime.
- Owner revocation and Access Code rotation, rate-limited verification, and access auditing without storing the plaintext code.
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
- Publishing Runtime Releases, Pipeline Versions, Template Versions, and Resource Bundles.
- Viewing system health, failure diagnostics, storage state, and aggregated usage metadata.
- Triggering cleanup, recovery, and safe rescheduling operations.
- Managing platform feature flags and revoking abnormal Share Links.
- Explicit, reason-bound, time-stamped, and audited break-glass access when User content must be inspected for support or recovery.

### Out of scope

- Implicit administrator access to Source Material, Decks, or other User content.
- Department administrators, Project administrators, delegated Workspace administration, or multi-level RBAC.
- Unlogged support impersonation or content inspection.
- Reassigning a Personal Workspace or Task to another User.

## Runtime and catalog extensibility

### In scope

- Packaging each approved Core Skill inside a content-addressed Runtime Image and publishing it as a Runtime Release.
- Shipping platform-approved, built-in Pipeline Versions.
- Publishing Catalog Templates, Template Versions, and Resource Bundles through an administrator-controlled versioned release process.
- Allowing ordinary Users to select approved Catalog Templates.
- Allowing a User to upload a Fill Template as Source Material for one Task.
- Supporting an initial administrator workflow based on release manifests, CLI tooling, or CI/CD rather than requiring a full management UI.

### Out of scope

- User-published Core Skills, executable scripts, or Pipeline Definitions.
- A Skill, template, or Resource Bundle marketplace.
- Treating a User's Fill Template as a public Catalog Template.
- An online template editor or self-service template publication workflow.
- A complete administrator catalog-management portal.

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

- Capturing measured usage when the provider and runtime supply it.
- Attributing usage to Personal Workspace, Task, Phase Run, and Runtime Run.
- Retaining append-only usage history and presenting usage totals.
- Preserving the Quota Reservation domain boundary for later enforcement.

### Out of scope

- Blocking task execution based on quota.
- Enforcing real token, image, compute, or monetary limits.
- Billing, payment, invoicing, or departmental chargeback.

## Data retention

### In scope

- Persisting Task metadata, Artifact Versions, Share Link configuration, Usage Ledger entries, and all release locks as business records.
- Retaining Artifact Versions until an authorized User explicitly deletes them or a future administrator retention policy applies.
- Revoking Share Links when their Artifact Version is deleted or becomes unavailable.
- Automatically releasing Sandbox Leases and cleaning sandbox state, Runtime Run temporary directories, expired Task Workspaces, disposable Checkpoints, caches, and incomplete publication residue.
- Recovering required mutable execution state from validated Checkpoints rather than treating a live Task Workspace as permanent storage.
- Configurable cleanup policies with observable cleanup failures and retriable Cleanup Debt.
- Treating Task Workspace as one logical identity whose node-local materialization is disposable and reconstructable.
- Capturing only declared recoverable Task-owned mutable state in Checkpoints; Runtime Releases, Template Versions, Resource Bundles, Source Material, shared caches, sessions, and failed residue stay outside.
- Retaining Checkpoints by recovery reachability and explicit references while allowing distinct Checkpoints to share content-addressed payloads.
- Recording unresolved cleanup as durable Cleanup Debt with resource identity, retry history, failure evidence, and estimated bytes and inodes.
- Exporting a disabled User's Personal Workspace through audited break-glass, verifying delivery outside SlideSmith, and requiring a separate administrator intent before purging its retained content.
- Retaining a 35-day joint PostgreSQL and durable-object point-in-time recovery window in at least one encrypted, immutable, independently controlled backup domain.
- Removing authorized deleted or purged content from online authority immediately while allowing encrypted, inaccessible bytes to remain in already locked backup copies until the recovery window expires.

### Out of scope

- Automatic expiration of valid Artifact Versions in the first release.
- Legal hold, records-management, e-discovery, or archival-tier workflows.
- Keeping sandbox or hidden runtime state as a durable Task record.
- Treating an export attempt, download start, or failed delivery as authorization to purge a Personal Workspace.
- Immediate physical erasure of deleted content from already immutable backup copies.
- Treating disaster-recovery backup as legal archive, records management, or permanent business history.

## Availability and topology

### In scope

- One enterprise site or data-center deployment.
- Independently restartable control-plane, worker, and execution services.
- Horizontal scaling of workers and Agent Compose execution nodes.
- Recovery from worker, sandbox, or execution-node failure by retrying from validated Checkpoints.
- Durable Task, Artifact Version, sharing, and usage records outside execution nodes.
- Persistent PostgreSQL and object storage with backup and recovery exercises.
- Control-plane services that can be deployed as multiple instances.
- A joint Recovery Point RPO of at most 15 minutes for PostgreSQL business state and every committed referenced byte, including total production-site loss or compromise.
- Staged manual recovery: verified Task metadata and Artifact Versions read-only within four hours, and full Source Material, Checkpoint, release/catalog, Runtime Image, mutation, and execution capability within eight hours.
- Read-only protection when the finalized recovery watermark would exceed the 15-minute RPO.
- Independent immutable backup authority, separated production and restore credentials, and dual control for restore/decrypt or premature retention changes.
- Invalidating every pre-incident Share Link and Access Code after restore; an Owner must issue a new Share Link.
- Monthly sampled and quarterly complete recovery as the target operating drill baseline, with recurring automation allowed to follow the first implementation phase.

### Out of scope

- Cross-site or cross-region active-active operation.
- Automatic disaster-recovery failover.
- Cross-site active standby or a guarantee of transparent recovery without an incident declaration and manual promotion.
- Mandatory physical offline backup media; independently controlled immutable copies remain required.
- Live migration of a running Sandbox.
- Lossless continuation of an in-flight Runtime Run after node failure.
- A formal availability target of 99.99% or higher.
- Global scheduling or multi-region data replication.

A single-server deployment is acceptable for development and acceptance testing, but it is not a highly available production topology.

## Task Workspace lifecycle cutover

### In scope

- A hard cutover with no migration of legacy Task Workspace, Agent Compose session, cache, or failed-residue directories.
- Preserving Task metadata, Source Material, Artifact Versions and Artifacts, release and template locks, and Phase Run and Runtime Run history.
- Terminating old non-terminal Tasks as non-recoverable and requiring a new Task to continue from retained inputs or publications.
- Deleting legacy execution data and recording every failed deletion as Cleanup Debt.
- Using a production remote-but-owned transport adapter and node-independent durable Checkpoint content so workers do not share or learn host workspace paths.

### Out of scope

- Inferring validated Checkpoints by scanning legacy session directories.
- Retaining a path-based compatibility facade after cutover.
- Migrating or resuming in-flight legacy execution state.
