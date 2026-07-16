# Enterprise Intranet V1 Scope

This document records confirmed delivery boundaries for SlideSmith's first enterprise-intranet release. It is updated as the architecture review resolves each scope decision.

## Ownership and collaboration

### In scope

- Authenticated Users with one private Personal Workspace each.
- Tasks and Artifact Versions owned through that Personal Workspace.
- Read-only sharing of one Artifact Version through a revocable Share Link protected by a separate Access Code.
- Mandatory Share Link expiry with a platform default and an administrator-configured maximum lifetime.
- Owner revocation and Access Code rotation, rate-limited verification, and access auditing without storing the plaintext code.
- Administrative recovery or transfer of retained work after a User is disabled.

### Out of scope

- Tenant and Membership layers.
- Projects as containers between Personal Workspace and Task.
- Shared or team Workspaces.
- Workspace invitations, collaborative editing, and team RBAC.
- Permanent Share Links or a guarantee that recipients cannot redistribute downloaded files.

## Authentication

### In scope

- Integrating with the existing internal centralized sign-in service.
- Mapping a stable external identity to one SlideSmith User and Personal Workspace.
- Retaining a disabled User's Tasks and Artifact Versions for administrative recovery or transfer.
- Allowing Share Link and Access Code access to one Artifact Version without an authenticated workspace session.

### Out of scope

- Storing passwords for ordinary Users in SlideSmith.
- Password registration, reset, recovery, SMS verification, or MFA implementation inside SlideSmith.
- Multiple simultaneous identity providers or cross-enterprise identity federation.
- Department synchronization and role mapping from the identity provider.

Protocol-specific OAuth or OIDC integration details are deferred to implementation design. SlideSmith must map a stable identity-provider subject to the User and must not treat a mutable email address as the external identity.

## Platform administration

### In scope

- One minimal Platform Administrator role for User activation, deactivation, recovery, and authorized transfer of retained work.
- Publishing Runtime Releases, Pipeline Versions, Template Versions, and Resource Bundles.
- Viewing system health, failure diagnostics, storage state, and aggregated usage metadata.
- Triggering cleanup, recovery, and safe rescheduling operations.
- Managing platform feature flags and revoking abnormal Share Links.
- Explicit, reason-bound, time-stamped, and audited break-glass access when User content must be inspected for support or recovery.

### Out of scope

- Implicit administrator access to Source Material, Decks, or other User content.
- Department administrators, Project administrators, delegated Workspace administration, or multi-level RBAC.
- Unlogged support impersonation or content inspection.

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
- Live Preview and post-publication manual edits that publish a new Artifact Version with parent lineage.

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
- Configurable cleanup policies with observable cleanup failures and retriable cleanup debt.

### Out of scope

- Automatic expiration of valid Artifact Versions in the first release.
- Legal hold, records-management, e-discovery, or archival-tier workflows.
- Keeping sandbox or hidden runtime state as a durable Task record.

## Availability and topology

### In scope

- One enterprise site or data-center deployment.
- Independently restartable control-plane, worker, and execution services.
- Horizontal scaling of workers and Agent Compose execution nodes.
- Recovery from worker, sandbox, or execution-node failure by retrying from validated Checkpoints.
- Durable Task, Artifact Version, sharing, and usage records outside execution nodes.
- Persistent PostgreSQL and object storage with backup and recovery exercises.
- Control-plane services that can be deployed as multiple instances.

### Out of scope

- Cross-site or cross-region active-active operation.
- Automatic disaster-recovery failover.
- Live migration of a running Sandbox.
- Lossless continuation of an in-flight Runtime Run after node failure.
- A formal availability target of 99.99% or higher.
- Global scheduling or multi-region data replication.

A single-server deployment is acceptable for development and acceptance testing, but it is not a highly available production topology.
