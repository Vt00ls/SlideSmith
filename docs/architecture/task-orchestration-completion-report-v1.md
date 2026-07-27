# Task Orchestration Completion Report v1

- Date: 2026-07-27
- Parent SPEC: [#54](https://github.com/Vt00ls/SlideSmith/issues/54)
- Completion audit: [#62](https://github.com/Vt00ls/SlideSmith/issues/62)
- Audit start: `0e75410dae5951f555cb6f5f8efd8868a9de24f0`
- Audited base: `150ccdb638e0356e7d5d57ccf477ac702aca805d`

## Result

The Task Orchestration implementation contract defined by #54 is complete. The
Standards and Spec reviews have no unresolved blocking finding. The module has
one public Task mutation seam, owns its accepted decisions and progression
facts, persists its protected transaction atomically in PostgreSQL, and keeps
downstream execution, delivery, telemetry, audit projection, reconciliation,
and cleanup mechanics from becoming alternate Task state machines.

This conclusion is about the #54 module and adapter contracts. It does not
claim that the legacy application has been cut over or that the downstream
enterprise modules have been implemented or deployed.

## Audit basis

The audit used the fixed comparison requested by #62:

```text
git diff 0e75410dae5951f555cb6f5f8efd8868a9de24f0...HEAD
git log 0e75410dae5951f555cb6f5f8efd8868a9de24f0..HEAD --oneline
```

The completion worktree was also reviewed because #62 remediation was not part
of `HEAD` at the fixed comparison point. Standards sources were `AGENTS.md`,
`CONTEXT.md`, the architecture documents referenced below, and the accepted
ADRs. The repository has no Makefile, task runner, or GitHub Actions workflow;
the reproducible gates are therefore the native Go commands in this report.

### Merged child evidence

| Child | Pull request | Merge commit | Result |
| --- | --- | --- | --- |
| #55 / TO-01 | [#63](https://github.com/Vt00ls/SlideSmith/pull/63) | `b5764e1e893ada697d63d6af50dddad86b5b2781` | Merged |
| #56 / TO-02 | [#64](https://github.com/Vt00ls/SlideSmith/pull/64) | `1dd42d9d91313a00664a3098310c109eb1477265` | Merged |
| #57 / TO-03 | [#65](https://github.com/Vt00ls/SlideSmith/pull/65) | `a3f5cf58f1d2f2d645ece118e184fd0506ca1c88` | Merged |
| #58 / TO-04 | [#66](https://github.com/Vt00ls/SlideSmith/pull/66) | `d805778f981f2aa6f76be2f7bdb84bf5a54ff213` | Merged |
| #59 / TO-05 | [#67](https://github.com/Vt00ls/SlideSmith/pull/67) | `4a151e8ed09378c8b54df03815eb91cbd69e9561` | Merged |
| #60 / TO-06 | [#68](https://github.com/Vt00ls/SlideSmith/pull/68) | `7ee44047defada727aa804b53a711cb5769c5521` | Merged |
| #61 / TO-07 | [#69](https://github.com/Vt00ls/SlideSmith/pull/69) | `150ccdb638e0356e7d5d57ccf477ac702aca805d` | Merged |

## Evidence conventions

Code evidence is under `backend/internal/taskorchestration`. Test names are
public-seam contract tests in the same package unless a file is named. The
principal accepted decisions are:

- [ADR 0020](../adr/0020-centralize-task-transitions-behind-a-command-decision-seam.md): one command/decision mutation seam and authoritative outbox.
- [ADR 0021](../adr/0021-pin-compatible-pipeline-and-runtime-releases-together.md): immutable compatible Pipeline/Runtime locks.
- [ADR 0027](../adr/0027-separate-authoritative-facts-from-telemetry-projections.md): authoritative facts remain separate from telemetry projections.
- [Task Orchestration](./task-orchestration.md): aggregate, authority, concurrency, delivery, cancellation, recovery, and test contracts.
- [Task Workspace Lifecycle](./task-workspace-lifecycle.md): opaque C04 lifecycle and reconstruction authority.
- [Observability, Audit, and Cleanup Debt](./observability-audit-and-cleanup-debt.md): mandatory audit, bounded telemetry, non-authoritative projections, and physical-resource ownership.

## #54 acceptance evidence matrix

### Module and authority

| Requirement | Code evidence | Test evidence | Decision/document evidence |
| --- | --- | --- | --- |
| Task Orchestration is an independent Platform Control Plane deep module. | Package `taskorchestration`; closed `TransitionIntent`; `TaskOrchestration` and `TaskOrchestrationQuery`. | `TestDecideCommitsThroughTheTaskOrchestrationSeam`; shared adapter contract. | ADR 0020; Task Orchestration “Boundary and ownership”. |
| All authoritative Task mutation passes through `Decide`. | `TaskOrchestration.Decide`; aggregate mutation is private in `harness.go`; PostgreSQL adapter exposes the same method. | `TestTaskOrchestrationPublicMutationSeamIsOnlyDecide`; `TestQueryIsReadOnlyAndDoesNotAllocateMutationFacts`. | ADR 0020 decision and rejected alternatives. |
| No public status/phase/general-repository mutation seam exists. | No exported `SetStatus`, `CompletePhase`, `NextPhase`, mutable Task snapshot, or Task repository; downstream interfaces expose only `Enact`/`Resolve`. | `TestTaskOrchestrationPublicMutationSeamIsOnlyDecide`; `TestDownstreamAdapterPublicSurfacesDoNotExposeProgressionOrPhysicalMechanics`. | Task Orchestration “Deletion test and migration implications”. |
| Task projection contains no route-specific workflow authority. | `TaskOrchestrationView` is a coarse projection; Phase definitions remain in the pinned Pipeline Contract. | `TestPinnedPipelineVersionCreatesTheEntryPhaseFromItsContract`; three-route contract. | ADR 0020; Task Orchestration “Task projection”. |
| Pipeline progression comes from the pinned Pipeline Contract. | `PinnedTaskStart`, `PipelineContract`, `PhaseDefinition`, and private aggregate cursor/progression logic. | `TestPinnedPipelineVersionFailsClosedWithoutItsValidationContract`; `TestThreePinnedRoutesCompleteFromStartToPublication`. | ADR 0020; ADR 0021. |
| Phase Run owns `0..N` Runtime Runs and history is immutable. | `PhaseRunView.RuntimeRuns`; retry creates records instead of replacing them. | `TestThreePinnedRoutesCompleteFromStartToPublication`; `TestRetryCreatesANewAttemptAndPreservesLocksAndHistory`; `TestRuntimeRetryCreatesANewRuntimeRunInsideTheSamePhaseRun`. | ADR 0020 consequences; Task Orchestration “Phase Run”. |
| Gate, retry, cancel, reconcile, and manual edit use one interface. | Closed intent constructors all return `TransitionIntent` and enter `Decide`. | Gate, retry, cancellation, reconciliation, and manual-edit aggregate/coordination contracts. | ADR 0020; Task Orchestration “Closed variants”. |

### Decision and outbox atomicity

| Requirement | Code evidence | Test evidence | Decision/document evidence |
| --- | --- | --- | --- |
| Decision, revision, relationships, evidence, audit, and outbox commit together. | `PostgresAdapter.Decide` and `persistFreshDecision`; `mandatory_audit.go`; PostgreSQL owned tables. | `TestPostgresAdapterCommitsAndRecoversOwnedTaskState`; `TestPostgresMandatoryAuditFailureRollsBackProtectedDecisionAndOutbox`; `TestPostgresPersistsEvidenceAndReplaysAfterRevisionAdvances`. | ADR 0020 linearization; Task Orchestration “Decision linearization”. |
| Pre-commit crash leaves no Decision; post-commit response loss exact-replays. | Transaction rollback and request lookup before current revision validation. | `TestPostgresCommitAndResponseCrashBoundariesRecoverByExactReplay`; shared fault matrix. | ADR 0020 consequences. |
| Enactments have stable OperationID and canonical payload digest. | `EnactmentRef`; canonical helpers; operation collision validation. | `TestEnactmentRefKeepsClosedTypedDurableFacts`; `TestPostgresOperationIdentityCollisionFailsClosed`; owned transport rebinding test. | Task Orchestration “Idempotency” and “Enactment delivery”. |
| Returned decisions reference only committed enactments. | Decision/outbox insertion precedes transaction commit and response. | `TestPostgresDecisionAndClaimNeverPerformRemoteIO`; `TestPostgresAdapterCommitsAndRecoversOwnedTaskState`. | ADR 0020; Task Orchestration “Decision linearization”. |
| Scheduler enqueue can participate in the Task PostgreSQL transaction. | `SchedulerTransactionalParticipant`, restricted `SchedulerTransaction.Enqueue`, `SchedulerEnqueueFact`. | `TestPostgresSchedulerParticipantFailureRollsBackTaskAndWorkItem`. | Task Orchestration “Enactment delivery”; Scheduling and Capacity Admission authority boundary. |
| Remote delivery is outside the Decision transaction. | `NewOutboxDispatcher` consumes committed rows after `Decide`; participant exposes only a local PostgreSQL function. | `TestPostgresDecisionAndClaimNeverPerformRemoteIO`; `TestDispatcherDeliversTheCommittedEnvelopeWithoutMutatingTaskAuthority`. | ADR 0020; Task Orchestration “Enactment delivery”. |

### Idempotency and concurrency

| Requirement | Code evidence | Test evidence | Decision/document evidence |
| --- | --- | --- | --- |
| Exact replay returns the original Decision, revision, and OperationIDs. | Request journal lookup by scoped request ID and canonical digest. | `TestExactReplayPrecedesCurrentRevisionValidation`; PostgreSQL replay tests; shared coordination contract. | Task Orchestration “Idempotency”. |
| Same key with another payload returns typed integrity conflict. | Canonical digest comparison in harness/PostgreSQL/owned transport. | `TestDecisionRequestIdentityCannotBeReboundToAnotherPayload`; `TestPostgresDecisionRequestCannotBeReboundToDifferentPayload`; owned transport rebinding test. | Task Orchestration “Idempotency”. |
| Stale expected revision writes neither Task nor outbox. | Revision check before decision construction/persistence. | concurrent confirmation/cancel/retry test; PostgreSQL single-winner test. | Task Orchestration “Idempotency”. |
| Stale generation, fence, or safety epoch cannot change Task/Phase. | Typed intent bindings and effective recovery/safety checks. | `TestRuntimeEvidenceRejectsEveryStaleTypedBinding`; lifecycle stale-binding test; recovery fence tests. | Task Orchestration “State and concurrency model”. |
| Duplicate/out-of-order evidence cannot advance twice. | Accepted-evidence replay index, prerequisite and terminal-operation checks. | duplicate Runtime evidence; terminal-operation; out-of-order delivery and shared fault tests. | Task Orchestration “Reconciliation”. |
| Business, execution, and delivery retries use the correct identity. | `RetryPhase`, `RetryRuntimeRun`, and dispatcher replay are distinct paths. | `TestRetryCreatesANewAttemptAndPreservesLocksAndHistory`; `TestRuntimeRetryCreatesANewRuntimeRunInsideTheSamePhaseRun`; `TestRetryIdentitiesRemainTypedByOwningRetryLayer`. | Task Orchestration “Retry and recovery”. |

### Evidence and downstream authority

| Requirement | Code evidence | Test evidence | Decision/document evidence |
| --- | --- | --- | --- |
| Runtime success alone cannot advance a Phase. | Runtime evidence updates only its Runtime Run; validator owns Phase contract evidence. | `TestRuntimeSuccessWaitsForValidationBeforeAdvancingANonMutatingPhase`; Runtime adapter authority test. | ADR 0020; Runtime Execution authority boundary. |
| Mutating Phase success needs validation plus exact C04 Revision/Checkpoint evidence. | Aggregate prerequisite checks; typed lifecycle binding and opaque adapter. | `TestMutatingPhaseWaitsForValidatedC04CommitEvidence`; C04 adapter tests. | ADR 0020; Task Workspace Lifecycle. |
| Publication success needs exact Artifact Version activation evidence. | Publication evidence binding and publication phase gate. | `TestPublicationPhaseCompletesOnlyFromArtifactActivationEvidence`; publication adapter contract. | Task Orchestration “Phase Run”; Artifact Publication authority boundary. |
| Scheduler delivery facts cannot mutate Task/Phase. | Scheduler adapter only creates typed evidence intent; claims/grants remain downstream fields. | `TestSchedulerAdapterReturnsEvidenceWithoutChangingTaskOrPhase`; exact work-item binding test. | Task Orchestration authority matrix; Scheduling and Capacity Admission. |
| C04 remains opaque and exposes no physical mechanics. | `TaskWorkspaceLifecyclePort`; reconstruction adapter uses C04 public request/result and content-free proof digest. | C04 commit/reconstruction, response-loss, malformed/cross-scope, and public-surface tests. | ADR 0015; Task Workspace Lifecycle; ADR 0020. |
| Missing, partial, corrupt, unauthorized, or cross-scope evidence fails closed. | Per-family typed binding validation and closed safe errors. | downstream adapter negative matrix; coordination cross-scope/stale tests; shared contracts. | Task Orchestration “Authorization, integrity…” and test strategy. |

### Failure and reconciliation

| Requirement | Code evidence | Test evidence | Decision/document evidence |
| --- | --- | --- | --- |
| Response loss, duplicate delivery, claim loss, and restart do not create a business attempt. | Stable outbox OperationID, durable delivery lease/disposition, inspect/reconcile. | dispatcher acknowledgement/claim/restart tests; PostgreSQL delivery suite; shared fault matrix. | ADR 0020; Task Orchestration “Enactment delivery”. |
| Ambiguous timeout remains reconciliation-required. | Safe downstream/transport error normalization and ambiguous disposition. | `TestPostgresTimeoutDuringSendPersistsReconciliationRequired`; in-memory response-loss tests. | Task Orchestration “Enactment delivery”. |
| Commit-first and fence-first cancellation are deterministic. | Cancelling aggregate, exact canonical C04 operation binding, typed fences and terminal checks. | `TestCancellationCommitFirstAndFenceFirstOrdering`; shared fault matrix on all adapter combinations. | ADR 0020; Task Orchestration “Cancellation race”. |
| Recovery read-only blocks mutation and rejects stale pre-recovery evidence. | Recovery projection, safety epoch, recovery generation/fence validation. | `TestRecoveryReadOnlyAndPostRecoverySafetyEpochFenceOldEvidence`; recovery delivery supersession tests. | Backup and Recovery; Task Orchestration “Retry and recovery”. |
| Reconciler cannot invent authority from diagnostics or physical state. | Reconcile intent accepts an existing OperationID/fence only; diagnostics/projections have no Task mutation interface. | original-operation reconciliation tests; telemetry/cleanup non-authority tests; non-leakage gate. | ADR 0020; ADR 0027; Observability, Audit, and Cleanup Debt. |

### Adapters, audit, and security

| Requirement | Code evidence | Test evidence | Decision/document evidence |
| --- | --- | --- | --- |
| In-memory, PostgreSQL, and owned transport run one black-box suite. | Shared adapter factories expose only Decide/Query/downstream public adapters. | `TestSharedTaskOrchestrationAdapterContract`; `TestSharedTaskOrchestrationCoordinationContract`; shared fault matrix, each with three adapters. | Task Orchestration “Test strategy”. |
| Mandatory audit failure fails the protected decision closed. | `newMandatoryAuditFact` plus in-transaction insert and fault points. | `TestPostgresMandatoryAuditFailureRollsBackProtectedDecisionAndOutbox`. | ADR 0027; Observability, Audit, and Cleanup Debt. |
| Telemetry/external projection failure does not roll back an accepted decision. | Asynchronous projection delivery scans retained authoritative facts. | `TestPostgresProjectionObserverFailureDoesNotRollBackCommittedDecision`; external projection rebuild tests. | ADR 0027. |
| Metrics are bounded and contain no business-ID labels. | Closed `MetricName`, `MetricLabels`, allowlist, `MetricSeriesUpperBound`. | `TestBoundedTelemetryUsesTypedLabelsAndAllowlistedLogsAndTraces`; Task-scoped diagnostic isolation test. | Observability, Audit, and Cleanup Debt. |
| Public/wire/error/telemetry surfaces do not leak protected mechanics or content. | Opaque ID types, closed safe errors, allowlisted projections and wire envelopes. | `TestTaskOrchestrationCanaryRedactionAndNonLeakageContract`; downstream and PostgreSQL public-surface tests. | ADR 0027; Task Orchestration security contract. |
| Physical Cleanup Debt is not duplicated in Task Orchestration. | Closed `CleanupOwnershipMatrix`; only `CleanupEvidenceReference` is retained. | `TestCleanupDebtOwnershipAndEvidenceReferenceContract`. | Observability, Audit, and Cleanup Debt. |

### Completion boundary

| Requirement | Code evidence | Test evidence | Decision/document evidence |
| --- | --- | --- | --- |
| Module, persistence, outbox, transport, and adapter contracts pass. | All `taskorchestration` production files and public adapter seams. | Focused, shared, PostgreSQL, full backend, and race commands below. | #54/#55–#61 accepted scope. |
| C04 opaque adapter integration passes. | Lifecycle and reconstruction adapters, exact immutable-input validation. | C04 adapter suite including response loss and reconstruction mismatch tests. | ADR 0015; Task Workspace Lifecycle. |
| Existing backend, module, race, and vet gates pass. | No generated or module-file drift. | Validation table below. | Repository-native Go gates. |
| Completion does not claim legacy/downstream/deployment wiring. | No legacy service, Runtime, Scheduler, Publication, deployment, or migration file changed. | Diff scope inspection. | “Out of scope and follow-up” below. |
| No production migration, cutover, deletion, or data mutation occurred. | Only source/tests/docs changed; PostgreSQL used isolated test schemas. | Worktree/diff inspection and test harness cleanup. | #54/#62 prohibitions. |

## Child acceptance evidence matrices

These tables retain every child criterion. Where the parent matrix already
contains the full proof, the child row names the exact code/test specialization
instead of treating the merged PR description or checkbox as evidence.

### #55 / TO-01

| Requirement | Code | Test | Decision/document |
| --- | --- | --- | --- |
| `Decide` is the only public mutation operation. | `TaskOrchestration`. | `TestTaskOrchestrationPublicMutationSeamIsOnlyDecide`. | ADR 0020. |
| No generic status/phase/repository/map mutation surface. | Closed interfaces and private aggregate records. | Public seam and downstream public-surface tests. | ADR 0020 rejected alternatives. |
| One typed authority per intent. | Concrete authority wrappers; private `authorityValue`; closed intent union. | `TestClosedIntentFamiliesCarryExactlyOneTypedAuthority`. | Task Orchestration authority matrix. |
| Canonical digest is stable/sensitive and unknown major fails closed. | `canonical.go`; schema-major validation. | `TestDecideUsesDeterministicBusinessDigest`; `TestDecideFailsClosedForUnknownSchemaMajor`. | Task Orchestration intent header. |
| Request, Decision, Operation, and Trace identities are distinct. | Separate opaque types in `types.go`. | `TestPublicContractUsesOpaqueSeparatedTypes`. | Domain terminology in Task Orchestration. |
| Public types/errors/diagnostics exclude protected fields. | Opaque structs and safe errors. | contract and non-leakage canary tests. | Security contract. |
| Harness supports clock, IDs, response loss, and crash boundaries. | `HarnessConfig`, deterministic fault/response controllers. | harness restart, response-loss, crash-boundary, and fault-hook tests. | ADR 0020 linearization. |
| Module and existing backend tests pass. | Package compiles with backend. | Validation table. | Repository gates. |

### #56 / TO-02

| Requirement | Code | Test | Decision/document |
| --- | --- | --- | --- |
| Three Routes complete start-to-publication in memory. | Aggregate progression from a pinned Pipeline Version and its Pipeline Contract. | `TestThreePinnedRoutesCompleteFromStartToPublication`. | Task Orchestration Pipeline model. |
| Route progression comes only from the pinned contract. | Phase graph in the pinned Pipeline Contract; coarse status projection. | pinned-entry and missing-validation-contract tests. | ADR 0020/0021. |
| Phase Run supports zero, one, and multiple Runtime Runs. | Runtime Run slice per Phase Run. | three-route test and explicit Runtime retry test. | ADR 0020 consequences. |
| Runtime success does not finish a Phase. | Evidence aggregation before validation. | `TestRuntimeSuccessWaitsForValidationBeforeAdvancingANonMutatingPhase`. | Runtime authority boundary. |
| Non-mutating, mutating, and publication gates differ correctly. | Phase-kind/validation contract handling. | non-mutating, mutating C04, and publication tests. | Task Orchestration “Phase Run”. |
| Gate exact replay and same-key/different-values conflict. | Gate binding in canonical intent and request journal. | `TestConfirmationGateHasNoRuntimeRunsAndReplaysOnlyExactOwningUserSubmission`. | Task Orchestration “Confirmation Gate”. |
| Retry preserves locks/history and creates a new attempt. | `RetryPhase` aggregate transition. | `TestRetryCreatesANewAttemptAndPreservesLocksAndHistory`. | ADR 0021. |
| Manual edit starts from latest Artifact Version under the same Task. | Pinned post-publication graph and artifact binding. | `TestManualEditUsesLatestArtifactAndThePinnedPostPublicationGraph`; expiry reconstruction test. | ADR 0020 consequence. |
| At most one active mutation-bearing activity exists. | Aggregate activity generation/active run checks. | manual-edit and concurrent coordination contracts. | Task Orchestration “Task projection”. |
| Tests avoid legacy handlers/status/path/session/Agent Compose. | Tests use Decide/Query and typed adapter ports only. | shared/public contract suites. | ADR 0020 deletion test. |

### #57 / TO-03

| Requirement | Code | Test | Decision/document |
| --- | --- | --- | --- |
| Exact replay survives later revisions. | Request journal is checked before revision validation. | `TestExactReplayPrecedesCurrentRevisionValidation`; PostgreSQL evidence replay. | Task Orchestration “Idempotency”. |
| Same-key/different-payload always fails closed. | Canonical request digest binding. | in-memory/PostgreSQL/transport conflict tests. | Task Orchestration “Idempotency”. |
| Stale revision/generation/fence/safety epoch cannot progress. | Typed aggregate validation. | stale Runtime/lifecycle/recovery test matrix. | State and concurrency model. |
| Concurrent confirm/cancel/retry has one revision winner. | optimistic Task revision. | `TestConcurrentConfirmationCancelAndRetryCancelHaveOneRevisionWinner`; PostgreSQL writer race. | Decision linearization. |
| Exact duplicate evidence is idempotent; mismatch is diagnostic only. | evidence replay and rejected diagnostic persistence. | duplicate Runtime evidence; mismatched diagnostic; PostgreSQL diagnostic restart. | Evidence authority model. |
| Commit-first cancellation retains Revision/Checkpoint then stops. | cancellation and lifecycle evidence ordering. | `TestCancellationCommitFirstAndFenceFirstOrdering`; shared fault matrix. | Cancellation race. |
| Fence-first cancellation rejects late commit and terminates. | exact C04 cancellation fence binding. | `TestCancellationCommitFirstAndFenceFirstOrdering`; shared fault matrix. | Cancellation race. |
| Ambiguous response becomes reconciliation-required. | fault controller and reconciliation disposition. | response-loss and post-commit fault tests. | Reconciliation. |
| Retry identity families are not mixed. | distinct Phase/Runtime/delivery/Cleanup types and APIs. | `TestRetryIdentitiesRemainTypedByOwningRetryLayer`. | Domain terminology. |
| Race/fault tests pass. | deterministic hooks plus real race detector. | shared fault matrix and validation table. | Test strategy. |

### #58 / TO-04

| Requirement | Code | Test | Decision/document |
| --- | --- | --- | --- |
| Real PostgreSQL proves all-or-none Decision/revision/audit/outbox. | `PostgresAdapter.Decide`, migrations, in-transaction mandatory audit/outbox. | adapter recovery and mandatory-audit rollback tests. | ADR 0020/0027. |
| SQLite/in-memory is not the PostgreSQL acceptance substitute. | Tests require `SLIDESMITH_TEST_POSTGRES_DSN` and isolated PostgreSQL schemas. | PostgreSQL integration suite in the validation table. | #58 scope. |
| Pre-commit crash leaves no Decision; post-commit response loss replays. | transaction and request journal. | PostgreSQL crash-boundary test. | ADR 0020. |
| Exact replay does not allocate DecisionID/OperationID. | persisted original decision state. | PostgreSQL replay tests. | Idempotency contract. |
| Persistent same-key conflict fails closed. | persisted digest comparison. | `TestPostgresDecisionRequestCannotBeReboundToDifferentPayload`. | Idempotency contract. |
| Concurrent writers have one expected-revision winner. | row locking and revision check. | `TestPostgresConcurrentWritersProduceOneExpectedRevisionWinner`. | Decision linearization. |
| Mandatory audit failure rolls back Decision/outbox. | mandatory-audit fault points. | `TestPostgresMandatoryAuditFailureRollsBackProtectedDecisionAndOutbox`. | ADR 0027. |
| External audit/telemetry failure does not roll back. | asynchronous projection backlog. | `TestPostgresProjectionObserverFailureDoesNotRollBackCommittedDecision`. | ADR 0027. |
| Scheduler participant failure rolls back Task and Work Item. | restricted transaction participant. | `TestPostgresSchedulerParticipantFailureRollsBackTaskAndWorkItem`. | Scheduler transaction boundary. |
| Persistence types/errors do not leak SQL or protected data. | typed `PersistenceError`; content-free state. | PostgreSQL error non-leakage tests and canary gate. | Security contract. |

### #59 / TO-05

| Requirement | Code | Test | Decision/document |
| --- | --- | --- | --- |
| Claim loss/crash/restart redelivers the original OperationID. | durable lease/disposition and outbox identity. | in-memory and PostgreSQL claim-loss/restart tests. | Enactment delivery. |
| Ack loss uses inspect/reconcile and no business attempt. | dispatcher `Inspect`/`Reconcile`; downstream replay. | acknowledgement-loss and reconstruction/commit response-loss tests. | ADR 0020. |
| Exact duplicate is idempotent; operation rebinding conflicts. | owned transport replay digest. | `TestOwnedTransportReplaysExactDuplicateAndRejectsOperationRebinding`. | Idempotency contract. |
| Out-of-order delivery is prerequisite/generation/fence safe. | owned transport prerequisite and typed fence checks. | revision-gap, deferred prerequisite, cancellation/recovery supersession tests. | Enactment delivery. |
| Cancellation/recovery deterministically supersedes pending work without rewriting history. | superseded dispositions retain outbox/decision. | in-memory and PostgreSQL supersession suites. | Cancellation/recovery contracts. |
| Ambiguous timeout remains reconciliation-required. | ambiguous disposition. | PostgreSQL timeout test; crash-after-send tests. | Reconciliation. |
| Batching/backpressure/lease expiry/poison have deterministic dispositions. | bounded claim config and closed delivery outcomes. | dispatcher batch, backpressure, expiry, and poison tests. | Delivery contract. |
| No remote I/O in Decision transaction; decision refs are committed. | dispatcher boundary. | `TestPostgresDecisionAndClaimNeverPerformRemoteIO`. | ADR 0020. |
| Transport/errors/diagnostics do not leak protected data. | strict owned wire and safe errors. | wire strictness plus non-leakage canary. | Security contract. |
| Restart/fault and PostgreSQL integration pass. | durable dispatcher state. | delivery PostgreSQL suite and validation table. | Test strategy. |

### #60 / TO-06

| Requirement | Code | Test | Decision/document |
| --- | --- | --- | --- |
| C04 adapter follows the completed opaque contract without physical inspection. | `TaskWorkspaceLifecyclePort` and reconstruction port method. | C04 commit/reconstruction/public-surface tests. | ADR 0015; Task Workspace Lifecycle. |
| Runtime success alone cannot complete a Phase; mutating also needs validation+C04. | Runtime adapter returns evidence intent only. | Runtime adapter/aggregate gating tests. | ADR 0020. |
| Publication requires exact activation evidence. | publication adapter and phase gate. | publication adapter/aggregate tests. | Artifact Publication authority. |
| Scheduler claim/admission/dead-letter cannot change Task/Phase. | scheduler evidence adapter only. | two Scheduler adapter tests. | Scheduler authority matrix. |
| Release/Catalog consume exact pinned contracts and never repin. | `Resolve` ports, task-bound verification digest, `TaskStartAdmission`. | never-repin, verified admission, stale epoch/generation, bundle-order tests. | ADR 0021/0023. |
| Duplicate evidence replays; mismatched/cross-scope/stale fails closed. | adapter replay cache and typed bindings. | per-adapter negative tests and shared downstream black box. | Evidence integrity contract. |
| Out-of-order evidence is prerequisite/fence safe and ignores recent-run inference. | explicit operation/prerequisite binding. | Runtime prerequisite omission and coordination order tests. | ADR 0020. |
| Cancellation covers C04 commit/fence and late Runtime/Publication evidence. | lifecycle/publication terminal checks. | C04 cancellation race; late publication; coordination cancellation tests. | Cancellation race. |
| Raw failures map to typed safe errors and surfaces do not leak. | `DownstreamError` normalization and opaque public records. | malformed/error/public-surface/non-leakage tests. | Security contract. |
| Shared black-box doubles prove no downstream progression authority. | adapters expose `Enact` or `Resolve` only. | `TestSharedDownstreamEvidenceAdapterBlackBoxContract`; shared orchestration suite. | ADR 0020 rejected alternatives. |

### #61 / TO-07

| Requirement | Code | Test | Decision/document |
| --- | --- | --- | --- |
| One suite runs in-memory, PostgreSQL, and owned transport. | shared adapter factories. | both shared contracts and fault matrix, all three adapters. | Task Orchestration test strategy. |
| Suite crosses only public seams and avoids SQL/queue/handler/path/session internals. | Decide/Query/downstream adapter interfaces. | shared suite structure and public-surface test. | Deep-module boundary. |
| Fault matrix covers crash/response/claim/duplicate/order/stale/cancellation. | shared fault scenarios and adapter controls. | `TestSharedTaskOrchestrationFaultMatrix` plus PostgreSQL delivery suite. | Failure/reconciliation contracts. |
| Audit persistence fails closed; projection failure does not roll back. | protected audit transaction and async projection backlog. | PostgreSQL audit/projection tests. | ADR 0027. |
| Metrics have bounded cardinality; logs/traces are allowlisted. | typed metric/log/trace projections. | bounded telemetry and task isolation tests. | Observability, Audit, and Cleanup Debt. |
| Canaries prove non-leakage and cross-Workspace nondisclosure. | redaction and closed diagnostics. | `TestTaskOrchestrationCanaryRedactionAndNonLeakageContract`. | Security contract. |
| Cleanup Debt has one physical owner; Task Orchestration stores refs only. | `CleanupOwnershipMatrix`, `CleanupEvidenceReference`. | cleanup ownership contract. | Cleanup Debt decision. |
| Telemetry/audit projection/cleanup/reconciliation failure cannot repair or advance Task. | no mutation interface on those adapters; reconciliation binds existing operation. | observability, cleanup, and reconciliation non-authority tests. | ADR 0020/0027. |
| Race, PostgreSQL, full backend, and vet pass. | No tool-generated drift. | Validation table. | Repository-native gates. |

## Remediation completed by #62

The audit found and repaired implementation gaps instead of recording them as
paper findings:

1. Bare `StartTask` no longer accepts caller-built locks. Start now requires a
   task-bound `TaskStartAdmission` assembled from exact Release Management and,
   for Generation, Catalog Publication verification results. Cross-Task,
   unverified, stale-safety, incompatible, or zero admissions fail closed.
2. The Task projection now retains the exact Execution Lock and Template Lock;
   downstream producer authorities are taken from the pinned contract rather
   than caller-selected fallbacks.
3. Every accepted Decision now carries a complete canonical mandatory audit
   envelope. PostgreSQL persists that envelope, including exact evidence and
   enactment bindings, in the protected transaction. External audit delivery
   projects a deep, non-aliasing copy of the authoritative fact.
4. Runtime execution retry creates a new Runtime Run inside the same Phase Run;
   it does not incorrectly create a new Phase attempt or reuse a terminal run.
5. Post-expiry manual edit now enters a pending reconstruction state, cannot
   make work available before reconstruction, accepts only exact C04
   reconstruction evidence, updates the opaque Revision/Checkpoint lineage,
   and does not invent a zero Phase Run.
6. A dedicated C04 reconstruction adapter now provides exact replay and
   response-loss inspect/reconcile. It validates operation, Task Workspace,
   Artifact Version, publication authority, generation, fence, safety epoch,
   canonical artifact evidence, and every immutable read-only input. Missing,
   different, duplicated, or corrupt materializations fail closed.
7. The shared public contract now exercises manual reconstruction and Runtime
   Run retry across in-memory, owned PostgreSQL, and owned transport-to-
   PostgreSQL combinations.
8. Public-surface, PostgreSQL JSONB, and projection tests now directly prove
   the unique mutation seam and complete mandatory audit bindings instead of
   relying on issue checkboxes or PR descriptions.
9. Every Decide-generated C04 commit, cancellation fence, and reconstruction
   enactment now comes from an opaque typed binding constructed from a complete
   canonical C04 request. The committed OperationID and payload digest are the
   exact C04 request values, so the real adapter consumes the outbox reference
   directly without caller or test mutation. Self-consistent requests with
   cross-Task validation, lease, or Artifact input scope fail closed.
10. Accepted C04 evidence now carries both requested and observed lifecycle
    generation/fence. Task Orchestration rejects regressive lineage, projects
    the advanced values, persists them through PostgreSQL restart, and requires
    the next commit/fence/reconstruction request to bind the current lineage.

Representative red-to-green tracer tests were:

```text
TestManualEditAfterExpiryRequiresExactC04ReconstructionEvidence
TestSharedTaskOrchestrationAdapterContract
TestTaskStartAdmissionRequiresExactTaskBoundReleaseAndCatalogVerification
TestTaskWorkspaceReconstructionAdapterRejectsNonExactReadOnlyInputs
TestDecideGeneratedC04CommitEnactmentBindsTheExactCanonicalRequest
TestDecideGeneratedC04CancellationEnactmentBindsTheExactCanonicalRequest
TestC04ReconstructionAdvancesLifecycleLineageForTheNextExactCommit
TestExactC04RequestBindingsRejectSelfConsistentButUnconsumableScope
```

`TestTaskWorkspaceReconstructionAdapterRejectsNonExactReadOnlyInputs` first
failed all three missing/different/duplicate immutable-input cases because the
adapter accepted them, then passed after the exact boundary validation was
added.

## Standards and Spec review

### Standards

Pass, with no unresolved blocking finding. The implementation follows the
documented domain language and ADR boundaries, keeps authority wrappers and
identities typed, leaves aggregate/persistence mechanics private, avoids a
generic repository or label map, uses public-seam contract tests, is formatted,
and passes `go vet` and the race detector. No baseline code smell rises to a
blocking design issue. Production responsibilities remain separated among the
aggregate, persistence, delivery, downstream adapters, observability, and
cleanup ownership; exact C04 request bindings keep complete downstream request
shape outside the aggregate while retaining only immutable identities and the
canonical digest.

### Spec

Pass, with no unresolved blocking finding. Every #54 and #55–#61 criterion has
code, test, and decision evidence above. The completion audit intentionally did
not add a generic administrator “manual resolution” mutation: #54's accepted
closed variants and criteria define reason-bound administrator cancellation,
machine-authority recovery fencing/reconciliation, and audited diagnostic
redrive, but do not define a second administrator progression interface.
Adding such a generic mutation would weaken the single-seam and authority
contracts. Any future product workflow must be specified under its owning
module and still enter Task mutation through a typed `TransitionIntent`.

## Validation

All commands were run from `backend/`. PostgreSQL tests used the real server at
`postgres://postgres@127.0.0.1:54723/slidesmith_test?sslmode=disable`; every
test created and removed an isolated schema.

| Gate | Reproducible command | Result |
| --- | --- | --- |
| Focused module | `go test ./internal/taskorchestration -count=1` | PASS |
| Focused module with PostgreSQL | `SLIDESMITH_TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:54723/slidesmith_test?sslmode=disable' go test ./internal/taskorchestration -count=1` | PASS |
| Shared black-box contracts | Exact command below | PASS: in-memory, PostgreSQL owned persistence, owned transport-to-PostgreSQL |
| Shared fault/race semantics | `SLIDESMITH_TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:54723/slidesmith_test?sslmode=disable' go test ./internal/taskorchestration -run '^TestSharedTaskOrchestrationFaultMatrix$' -count=1 -v` | PASS on all three adapter combinations |
| Non-leakage/telemetry/Cleanup Debt | Exact command below | PASS |
| Full backend with PostgreSQL | `SLIDESMITH_TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:54723/slidesmith_test?sslmode=disable' go test ./... -count=1` | PASS |
| Full backend race with PostgreSQL | `SLIDESMITH_TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:54723/slidesmith_test?sslmode=disable' go test -race ./... -count=1` | PASS |
| Vet | `go vet ./...` | PASS |
| Format | `gofmt -l .` | PASS, no output |
| Module-file drift | `go mod tidy -diff` | PASS, no output |
| Diff hygiene | `git diff --check` | PASS, no output |

Commands containing regular-expression alternation are recorded verbatim here
so Markdown table escaping cannot change their shell meaning:

```bash
SLIDESMITH_TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:54723/slidesmith_test?sslmode=disable' \
go test ./internal/taskorchestration \
  -run '^TestSharedTaskOrchestration(Adapter|Coordination)Contract$' -count=1 -v

SLIDESMITH_TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:54723/slidesmith_test?sslmode=disable' \
go test ./internal/taskorchestration \
  -run '^(TestTaskOrchestrationCanaryRedactionAndNonLeakageContract|TestBoundedTelemetryUsesTypedLabelsAndAllowlistedLogsAndTraces|TestTelemetryDiagnosticSnapshotIsolatesEverySignalToTheAuthorizedTask|TestCleanupDebtOwnershipAndEvidenceReferenceContract)$' \
  -count=1 -v
```

## Cleanup Debt ownership

Task Orchestration is not a physical-resource owner. The closed matrix assigns:

- Task Workspace Runtime Views, materializations, and Checkpoints to Task
  Workspace Lifecycle (C04);
- Runtime processes, sandboxes, leases, and containment to Runtime Execution;
- staging, physical generations, cache, quarantine, and reclamation to Durable
  Object;
- Artifact staging to Artifact Publication;
- release and catalog package materializations to Release Management and
  Catalog Publication respectively; and
- backup-copy staging to Backup and Recovery.

Task Orchestration retains only a content-free `CleanupEvidenceReference`
containing the owner, resource class, evidence identity, operation identity,
and bounded diagnostic category. It does not store physical location, cleanup
state machine, credential, retry schedule, or deletion authority.

## Out of scope and follow-up

No #54-scoped blocking implementation work remains. The following are explicit
platform follow-ups, not completion blockers:

- replace the legacy `TaskService`, route/status switches, polling workers, and
  edit-session progression path under the separately governed migration plan;
- implement and production-wire full Runtime Execution, Scheduler, Artifact
  Publication, Release Management, Catalog Publication, Usage Accounting, and
  their transports;
- select production queue, telemetry, audit sink, capacity, retention, and SLO
  configurations;
- perform legacy schema/data migration, dual-write, hard cutover, deployment,
  or production mutation/deletion; and
- define any future administrator manual-resolution product workflow under a
  separate authority/spec decision rather than introducing a generic escape
  hatch here.

> #54 implementation complete does not mean legacy TaskService, full Runtime Execution, Scheduler, Artifact Publication, Release/Catalog production wiring, deployment, migration, dual-write, cutover, or production mutation/deletion is complete.
