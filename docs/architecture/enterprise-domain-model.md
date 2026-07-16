# Enterprise Platform Domain Model

This document is a relationship view of the decisions confirmed during the SlideSmith enterprise-platform architecture review. [CONTEXT.md](../../CONTEXT.md) remains the authoritative glossary, the files in [docs/adr](../adr) record durable decisions, and [enterprise-v1-scope.md](./enterprise-v1-scope.md) records first-release delivery boundaries.

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
    Task -->|owns one mutable| TaskWorkspace[Task Workspace]
    TaskWorkspace -->|captured at validated boundaries| Checkpoint
    Checkpoint -->|restores| TaskWorkspace
```

- A Runtime Run cannot advance the Generation Pipeline directly; its Phase Run validates the outcome.
- Runtime Runs share explicit Task Workspace state, never hidden sandbox state.
- Each Runtime Run uses a fresh lease; infrastructure may reuse a fully reset physical sandbox under a new lease.

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

## Authority boundary

| Platform Control Plane | Execution Data Plane |
| --- | --- |
| Users, Personal Workspaces, and access | Sandboxed process execution |
| Tasks, Route and Pipeline locks | Task Workspace byte mutation |
| Phase Run and Runtime Run authority | Runtime status and evidence emission |
| Runtime and Template locks | Temporary logs and outputs |
| Artifact Version metadata and sharing | Sandbox allocation and cleanup |
| Usage Ledger and Quota Reservation | Measured usage receipts |

Execution output becomes authoritative only after the Platform Control Plane validates and records it.
