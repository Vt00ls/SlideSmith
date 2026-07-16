# Enterprise Platform Domain Model

This document is a relationship view of the decisions confirmed during the SlideSmith enterprise-platform architecture review. [CONTEXT.md](../../CONTEXT.md) remains the authoritative glossary, the files in [docs/adr](../adr) record durable decisions, [enterprise-v1-scope.md](./enterprise-v1-scope.md) records first-release delivery boundaries, and [task-workspace-lifecycle.md](./task-workspace-lifecycle.md) records the grilled C04 lifecycle invariants.

## Ownership and publication

```mermaid
flowchart TD
    User -->|owns exactly one| PersonalWorkspace[Personal Workspace]
    PersonalWorkspace -->|owns| Task
    PersonalWorkspace -->|owns| UsageLedger[Usage Ledger]
    PersonalWorkspace -->|owns| QuotaReservation[Quota Reservation]
    Task -->|publishes| ArtifactVersion[Artifact Version]
    ArtifactVersion -->|contains| Artifact
    ArtifactVersion -->|may expose through| ShareLink[Share Link]
    ShareLink -->|requires| AccessCode[Access Code]
    PlatformAdministrator[Platform Administrator] -. audited break-glass only .-> PersonalWorkspace
```

- A Share Link grants no access to its Task or Personal Workspace.
- Moving a Task does not rewrite historical Usage Ledger ownership.
- Artifact Versions are immutable publication sets; edits publish child versions.

## Pipeline and execution

```mermaid
flowchart TD
    Task -->|selects| Route
    Route -->|pins| PipelineVersion[Pipeline Version]
    PipelineDefinition[Pipeline Definition] -->|publishes| PipelineVersion
    PipelineVersion -->|defines| Phase
    Phase -->|attempted as| PhaseRun[Phase Run]
    PhaseRun -->|owns 0..N| RuntimeRun[Runtime Run]
    PhaseRun -->|holds at most one| QuotaReservation[Quota Reservation]
    RuntimeRun -->|acquires 0..1| SandboxLease[Sandbox Lease]
    RuntimeRun -->|mutates through| RuntimeView[Runtime View]
    Task -->|owns one mutable| TaskWorkspace[Task Workspace]
    TaskWorkspace -->|advances as| WorkspaceRevision[Task Workspace Revision]
    WorkspaceRevision -->|materializes into| RuntimeView
    RuntimeView -->|validated commit advances| TaskWorkspace
    WorkspaceRevision -->|bound to| Checkpoint
    Checkpoint -->|restores| TaskWorkspace
```

- A Runtime Run cannot advance the Generation Pipeline directly; its Phase Run validates the outcome.
- Runtime Runs share explicit Task Workspace state through isolated Runtime Views, never hidden sandbox state.
- Each Runtime Run uses a fresh lease; infrastructure may reuse a fully reset physical sandbox under a new lease.
- Every successful Phase Run binds its validated contract, authoritative Task Workspace Revision, and a distinct durable Checkpoint identity.

## Task Workspace lifecycle

```mermaid
flowchart LR
    CP[Platform Control Plane<br/>authoritative decisions]
    Life[Task Workspace Lifecycle<br/>deep module]
    Materialization[Disposable materialization]
    Durable[(Durable Checkpoint content)]
    Runtime[Runtime Release]
    Packages[Template Version<br/>Resource Bundle]

    CP -->|opaque intent and identities| Life
    Life --> Materialization
    Life --> Durable
    Runtime -->|read-only| Materialization
    Packages -->|read-only| Materialization
    Life -->|revision and evidence| CP
```

- The lifecycle interface exposes high-level intent and opaque identities, never host paths, mounts, file operations, storage vendors, buckets, or node details.
- One authoritative writer advances each Task Workspace; mutating Runtime Runs commit or discard transactional Runtime Views after Phase Run validation.
- A Checkpoint captures declared recoverable Task-owned mutable state, not a directory snapshot. Immutable runtime and template packages, Source Material, cache, sessions, and failed residue stay outside it.
- Checkpoint content becomes authoritative only after node-independent durable acknowledgement; missing content or digest mismatch fails closed.
- Physical materialization can expire and be rebuilt on any eligible execution node. Cleanup failures become persistent, retriable Cleanup Debt rather than untracked directories.

## Runtime and design packages

```mermaid
flowchart TD
    Task -->|pins| RuntimeRelease[Runtime Release]
    RuntimeRelease -->|contains| CoreSkill[Core Skill]
    RuntimeRelease -->|contains| Toolchain
    RuntimeRelease -->|implements| RuntimeCapability[Pipeline runtime capabilities]
    CatalogTemplate[Catalog Template] -->|publishes| TemplateVersion[Template Version]
    TemplateVersion -->|pins by digest| ResourceBundle[Resource Bundle]
    Task -->|records selection in| TemplateLock[Template Lock]
    TemplateLock -->|pins| TemplateVersion
    TemplateLock -->|pins transitive digests| ResourceBundle
```

- Core Skills ship inside content-addressed Runtime Images for the first release.
- Catalog Templates and large non-executable Resource Bundles are separately versioned, read-only packages.
- No Task references a floating `latest` runtime, template, or resource package.

## Authority seam

| Platform Control Plane | Execution Data Plane |
| --- | --- |
| Users, Personal Workspaces, and access | Sandboxed process execution |
| Tasks, Route and Pipeline locks | Task Workspace materialization and byte mutation |
| Phase Run, Runtime Run, Checkpoint metadata, and commit authority | Runtime status and evidence emission |
| Runtime and Template locks | Temporary logs and outputs |
| Artifact Version metadata and sharing | Runtime Views, Checkpoint content, expiry, and cleanup |
| Usage Ledger and Quota Reservation | Measured usage receipts |

Execution output becomes authoritative only after the Platform Control Plane validates and records it.
