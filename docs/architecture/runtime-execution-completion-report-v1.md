# Runtime Execution Completion Report v1

- Date: 2026-08-10
- Parent SPEC: [#71](https://github.com/Vt00ls/SlideSmith/issues/71)
- Completion audit: [#87](https://github.com/Vt00ls/SlideSmith/issues/87)
- Audit start: `7a5e7dae64c3d279f6b4758d9e2f9cbd9c3bb9e0` (HEAD when the audit worktree opened)
- Audited base: `02d27805fe51593619c49bcffa8eefe55e4aa168` (merge of PR #72, the architecture-blocker resolution that precedes C03-01)

## Result

The Runtime Execution (C03) implementation contract defined by #71 is complete.
The Standards and Spec reviews have no unresolved blocking finding. The module
has one public mutation seam (`Execute(StartRuntimeRun | CancelRuntimeRun)`),
one public read seam (`Inspect(RuntimeRunRef) -> RuntimeSnapshot`), separate
protected `RuntimeMaintenance` and read-only `OperationalDiagnostics` seams,
persists its authoritative facts atomically in real PostgreSQL, keeps worker,
transport, outbox, evidence, reconciliation, cleanup, and telemetry mechanics
private, and runs one unconditional four-way shared black-box contract suite
(deterministic in-memory, local development, real PostgreSQL, and
production-shaped owned transport + worker). Structural deletion gates prove
by import-graph and capability inspection that the target C03 and its Task
Orchestration integration build and execute the contract with the legacy
CLI/session/path/shared-daemon/recent-run/single-run packages absent.

This conclusion is about the #71 module and adapter contracts. It does not
claim that the legacy application has been cut over, that production
hardening or traffic enablement is complete, or that the downstream Scheduler,
Usage, Observability, C04, or Gateway production wiring has been deployed.

## Audit basis

The audit used the fixed comparison:

```text
git diff 02d27805fe...HEAD
git log --first-parent 02d27805fe..HEAD --oneline
```

The completion worktree was also reviewed because #87 remediation (formatting,
the evidence-terminal decision-fact fix, and the structural deletion gates)
was not part of `HEAD` at the fixed comparison point. Standards sources were
`AGENTS.md`, `CONTEXT.md`, `docs/architecture/runtime-execution.md`,
`docs/architecture/legacy-business-migration-and-compatibility.md`, ADR 0016,
ADR 0022, ADR 0028, ADR 0029, and the sibling module contracts
(task orchestration, scheduling and capacity admission, task workspace
lifecycle, LLM gateway and usage accounting, observability audit and Cleanup
Debt). The repository has no Makefile, task runner, or CI workflow; the
reproducible gates are therefore the native Go commands in the Validation
section.

### Merged child evidence

| Child | Pull request | Merge commit | Result |
| --- | --- | --- | --- |
| #73 / C03-01 | [#88](https://github.com/Vt00ls/SlideSmith/pull/88) | `7e8929f158` | Merged |
| #74 / C03-02 | [#89](https://github.com/Vt00ls/SlideSmith/pull/89) | `3e7a2567c9` | Merged |
| #75 / C03-03 | [#90](https://github.com/Vt00ls/SlideSmith/pull/90) | `6698494bd3` | Merged |
| #76 / C03-04 | [#91](https://github.com/Vt00ls/SlideSmith/pull/91) | `9292592a1f` | Merged |
| #77 / C03-05 | [#92](https://github.com/Vt00ls/SlideSmith/pull/92) | `ab9654dfdb` | Merged |
| #78 / C03-06 | [#93](https://github.com/Vt00ls/SlideSmith/pull/93) | `599acf922e` | Merged |
| #79 / C03-07 | [#94](https://github.com/Vt00ls/SlideSmith/pull/94) | `c03dc0c9ae` | Merged |
| #80 / C03-08 | [#95](https://github.com/Vt00ls/SlideSmith/pull/95) | `140abdad24` | Merged |
| #81 / C03-09 | [#97](https://github.com/Vt00ls/SlideSmith/pull/97) | `7acf2f5788` | Merged |
| #82 / C03-10 | [#96](https://github.com/Vt00ls/SlideSmith/pull/96) | `43bbd3e967` | Merged |
| #83 / C03-11 | [#98](https://github.com/Vt00ls/SlideSmith/pull/98) | `4172912bc7` | Merged |
| #84 / C03-12 | [#99](https://github.com/Vt00ls/SlideSmith/pull/99) | `e458109c2f` | Merged |
| #85 / C03-13 | [#100](https://github.com/Vt00ls/SlideSmith/pull/100) | `495008ca2a` | Merged |
| #86 / C03-14 | [#101](https://github.com/Vt00ls/SlideSmith/pull/101) | `7a5e7dae64` | Merged |

The architecture-blocker resolution PR #72 (`02d27805fe`) carries ADR 0029 and
the #24 Class C approval into the tree that the C03 children implemented
against.

## Evidence conventions

Code evidence is under `backend/internal/runtimeexecution` unless a file is
named. Test names are public-seam contract tests in the same package unless a
file is named. The Task Orchestration consumer and Scheduler bridge evidence
lives under `backend/internal/taskorchestration` and
`backend/internal/scheduler` respectively. The principal accepted decisions
are:

- [ADR 0016](../adr/0016-hard-cut-over-legacy-execution-state.md): legacy execution state is deleted, not wrapped.
- [ADR 0022](../adr/0022-run-runtime-capabilities-through-fenced-sandbox-leases.md): fenced Sandbox Leases with Runtime Execution authority.
- [ADR 0028](../adr/0028-promote-only-verifiable-legacy-business-facts.md): only verifiable legacy business facts are promoted.
- [ADR 0029](../adr/0029-bind-runtime-admission-once-before-post-lease-prerequisites.md): one admission path; Start/Accepted/Bound linearize before post-lease prerequisites.
- [Runtime Execution](./runtime-execution.md): authority matrix, minimal interface, admission, lease, worker, evidence, security, adapter, reconciliation, cleanup, and deletion-test contracts.
- [Legacy Business Migration and Compatibility](./legacy-business-migration-and-compatibility.md): cutover boundary and deletion inventory.

## #71 acceptance evidence matrix

Every Implementation Decision is cited. Closely-coupled decisions are grouped
in one row; each row names the concrete code and test evidence that proves
the requirement rather than relying on the issue checkbox.

### Authority and information-hiding boundary (Decisions 1-9)

| Requirement | Code evidence | Test evidence | Decision/document evidence |
| --- | --- | --- | --- |
| Public knowledge boundary is typed intents, opaque identities, canonical digests, `RuntimeDecision`, versioned closed `RuntimeSnapshot`, safe errors, and evidence references (D1). | `types.go`: `StartRuntimeRun`, `CancelRuntimeRun`, `RuntimeRunRef`, `RuntimeDecision`, `RuntimeSnapshot`, opaque `*ID` value types, `Digest`, `TraceID`. | `TestPublicSurfaceHasOnlyExecuteAndInspect`; `TestOperationalDiagnosticsIsReadOnlyAndNonEnumerating`. | Runtime Execution "Minimal external interface". |
| Mutation seam is exactly `Execute(StartRuntimeRun \| CancelRuntimeRun)`; read seam is read-only `Inspect`; no second mutation authority (D2, D5). | `RuntimeExecution` interface has exactly `Execute`/`Inspect`; worker/heartbeat/observation/callback/poll/queue/deadline/evidence paths all enter the private invariant engine. | `TestPublicSurfaceHasOnlyExecuteAndInspect`; `TestGatewayUsageContractsCarryNoProviderOrPersistenceAuthority`; worker protocol structural allowlists (`assertExactStructFieldTypes`, `assertExactInterfaceSignatures`). | Runtime Execution "Minimal external interface"; Decision 5. |
| `RuntimeDecision` is a durable acceptance/rejection fact, not a synchronous completion claim (D3). | `RuntimeDecisionFact` carries DecisionID, request identity/digest, revisions, state/outcome, operation, evidence identity, retry and reconciliation dispositions; `RuntimeDecision` = fact + snapshot. | `TestExecuteAcceptedCanonicalStartAndInspectCurrentSnapshot`; `TestDecisionIncludesExistingTerminalEvidenceIdentity`. | Runtime Execution "Minimal external interface". |
| `RuntimeSnapshot` is a versioned closed result with fail-closed unknown-major and lossless-projection rules (D4, D96). | `SnapshotSchemaVersion`/`SchemaV1`, closed `RuntimeSnapshot` struct with value-typed fields only. | `TestRuntimeSnapshotClosedSchemaAndLosslessProjectionRules`; `TestPostgresCapsuleAuditUnknownFieldTamperFailsInspectClosedWithoutLeakage`; schema contract tests. | Runtime Execution "Minimal external interface" and "Request and rejection journal". |
| Task Orchestration creates Runtime Run identity/membership; C03 never rewrites Phase Run or business history (D6, D8). | `taskorchestration/runtime_enactment.go` creates the canonical C03 start; C03 `StartRuntimeRun` carries existing Runtime Run refs. | `TestStartRequiresTaskOrchestrationAuthority`; `TestTerminalRuntimeNeverAcceptsAnotherStart`; `TestNonCreatedRuntimeNeverAcceptsAnotherStart`; `TestPostgresRuntimeWorkItemBindsCompleteCanonicalC03RequestInTaskTransaction`. | Task Orchestration authority matrix; Runtime Execution "Standing constraints". |
| Task Orchestration committed enactment binds the complete canonical C03 start; outbox OperationID/payload digest equal the C03 request (D7). | `taskorchestration/runtime_enactment.go` (`canonicalRuntimeStartEnactment`) builds `StartRuntimeRun` + `EnactmentRef` with payload digest from the canonical request; scheduler participant stores exact work item binding. | `TestPostgresRuntimeWorkItemBindsCompleteCanonicalC03RequestInTaskTransaction`; `TestPostgresGenerationRuntimeWorkItemCarriesExactPinnedCatalogPackages`; `TestPostgresNonGenerationRuntimeWorkItemsDoNotFabricateCatalogPackages`. | ADR 0029; Task Orchestration "Enactment delivery". |
| C03 → Task Orchestration is one-way: evidence-only consumer bridge, typed disposition + exact EvidenceID/digest (D9). | `taskorchestration/runtime_enactment.go` evidence adapter consumes typed Runtime evidence; C03 emits evidence via owned outbox. | `TestPostgresRuntimeCodecRoundTripsAdmissionTerminalAndCapacityEvidence`; Runtime evidence-only adapter tests. | Runtime Execution authority matrix. |

### Identity, canonical request, and concurrency (Decisions 10-20)

| Requirement | Code evidence | Test evidence | Decision/document evidence |
| --- | --- | --- | --- |
| Independent typed identities/counters; no universal revision or TraceID (D10). | Distinct opaque ID types and typed counters (`RuntimeRevision`, `OperationGeneration`, `LeaseGeneration`, `RuntimeFence`, `LeaseFence`, safety epochs, `AdmissionGrantGeneration`). | `TestPublicSurfaceHasOnlyExecuteAndInspect` (`assertIndependentType` matrix); `TestTraceIDIsDiagnosticContextAndNeverBecomesAKey`. | Runtime Execution "Minimal external interface"; Decision 10. |
| Start binds Workspace/Task/Phase/Runtime/attempt/revisions/authority generation/operation identity/digest/deadline/cancellation (D11, D12). | `StartRuntimeRunInput` + `canonicalStartEncoding`; `RuntimeViewRequirement`, `CatalogExecutionBinding`, `ProviderExecutionBinding`, policy references. | `TestCanonicalStartBindsCompleteExecutionAuthority`; `TestCanonicalStartEncodingIsVersionedDeterministicAndExact`. | Runtime Execution "Minimal external interface". |
| Mutating run binds only an exact opaque C04 `RuntimeViewRequirement`, not a Runtime View capability; exact `OpenRuntimeViewRequest` derived after lease (D13, D93). | `RuntimeViewRequirement` in start; `SandboxLeaseAuthority` derivation after lease; exact C04 request binding. | `TestImmutableInputManifestAndRuntimeViewEffectInvalidCasesFailClosed`; `TestC04OpenNeverRunsBeforeSandboxLeaseCommit`; `TestMutatingRuntimeOpensExactlyOnePostLeaseC04View`; `TestC04OpenAmbiguityReconcilesTheOriginalRequestButRejectsStaleLeaseAuthority`. | Runtime Execution "Execution Capsule, Runtime View, inputs…"; ADR 0029. |
| Cancel binds exact run/revision/current start operation/fence/authority/reason/safety epoch (D14). | `CancelRuntimeRunInput` typed fields; cancellation cannot borrow other authorities. | `TestWorkerStopReasonMustMatchAuthoritativeCause`; cancellation contract tests. | Runtime Execution "Minimal external interface". |
| Canonical encoding is versioned, deterministic, stable under set ordering/empty values, and excludes trace/transport/queue/cursor attributes (D15). | `canonical.go` stable canonical encoding; digest over business facts only. | `TestCanonicalStartEncodingIsVersionedDeterministicAndExact`; `TestDecision97RequestPersistenceMatrix`. | Runtime Execution "Minimal external interface". |
| Exact replay lookup precedes revision validation; no identity/side-effect reallocation (D16). | `lookupPostgresCommandReplay` before current-revision validation; deterministic replay journal. | `TestExactStartReplayAfterCancelReturnsOriginalFactAndCurrentSnapshot`; `TestStaleCancelReplayAfterLaterRevisionReturnsOriginalRejectionAndCurrentSnapshot`; `TestPostgresCommitResponseLossReplaysOriginalDecisionAfterRestart`; `TestPostgresRetainedStartReplayIgnoresNewerRedundantGrant`. | Runtime Execution "Request and rejection journal". |
| Same key/different digest is a permanent integrity conflict; one canonical start per run (D17). | Request journal binds operation identity to canonical digest. | `TestPostgresOperationIdentityCannotCrossCommandKinds`; `TestPostgresOutboxExactDuplicateIsIdempotentAndConflictFailsClosed`; `TestReconciliationContract_DifferentDigestSameOperationID_IntegrityConflict`; `TestConcurrentExactStartAndCancelReplayOneDecision`. | Runtime Execution "Request and rejection journal". |
| Fresh mutation advances a monotonic revision; stale revisions write nothing (D18). | Optimistic revision checks on every protected write. | `TestPostgresConcurrentWritersProduceOneRuntimeRevisionWinner`; `TestPostgresWritersForDifferentRuntimesUseRowLevelConcurrency`. | Runtime Execution "Runtime Run state and linearization". |
| Start/cancel concurrency linearizes through row lock/CAS; fence ordering decides dispatch vs cancel (D19). | PostgreSQL row lock + fence CAS on terminal/evidence ingestion. | `TestConcurrentExactStartAndCancelReplayOneDecision`; `TestPostgresBindCancelAndDeadlineRacesUseOneAcceptedBinding`; `TestPostgresLeaseCancelAndDeadlineRacesCommitZeroOrOneLease`. | Runtime Execution "Runtime Run state and linearization". |
| Revision/generation/fence/epoch express distinct ordering domains; none is superseded by another being newer (D20). | Independent typed counters persisted in `RuntimeFixture`. | `TestPublicSurfaceHasOnlyExecuteAndInspect` independence matrix; `TestWorkerHeartbeatCanonicalDigestBindsCompleteLeaseAndNodeSnapshots`; `TestLeaseAcquireRejectsStaleNodeCatalogSafetyEpoch`. | Runtime Execution "Runtime Run state and linearization". |

### Runtime Run lifecycle and linearization (Decisions 21-31)

| Requirement | Code evidence | Test evidence | Decision/document evidence |
| --- | --- | --- | --- |
| C03 non-terminal states cover Accepted/WaitingForLease/Reconciling/PreparingPrerequisites/Starting/Running/Stopping; vendor state is never exposed as domain state (D21). | `RuntimeState` enum; private state machine in the invariant engine. | `TestImmutableTerminalOutcomes`; lifecycle contract tests; `TestPostgresRuntimeExecutionContract`. | Runtime Execution "Runtime Run state and linearization". |
| Immutable terminal outcomes Succeeded/Failed/Cancelled/TimedOut/Lost/Rejected; terminal is never overwritten or reopened by retry (D22). | `RuntimeOutcome` enum; terminal CAS in PostgreSQL; `advancePostgresEvidenceTerminal`. | `TestImmutableTerminalOutcomes`; `TestTerminalRuntimeNeverAcceptsAnotherStart`; `TestPostgresWorkerStopWinsLateSuccessRaceAndReplaysAfterResponseLoss`. | Runtime Execution "Runtime Run state and linearization". |
| Start linearization is the atomic C03 transaction with Runtime fence, stable lease-acquire OperationID/digest, fixed `LeaseAcquireBy`, and restricted Scheduler participant Accepted+Bound (D23, D91). | `executePostgresStart` atomic transaction; `postgresSchedulerAcceptanceTransaction`; `scheduler_participant.go`. | `TestPostgresFoundationDoesNotCreateAcceptedBoundHalfState`; `TestPostgresStartAcceptedBoundFaultMatrixIsAtomic`; `TestPostgresAcceptedBoundGrantCannotRotateRequeueOrRebind`; `TestPostgresSchedulerParticipantFailureRollsBackTaskAndWorkItem`. | ADR 0029; Runtime Execution "One admission path and grant binding". |
| Post-bind/pre-lease contract: temporary same-generation ambiguity stays WaitingForLease/Reconciling; permanent stale bindings terminal Rejected; authority expiry, cancel, deadline distinct; crash proves zero-or-one lease (D24). | `advancePostgresPreLease`, `advancePostgresPreLeaseTimeBounds`, `executePostgresPreLeaseTerminal`, `commitPostgresPreLease`. | `TestPostgresPostBindPreLeaseMatrixAndCapacityEvidenceSeparation`; `TestPostgresPreLeaseAuthorityExpiryDeadlineAndCancelAreDistinct`; `TestPostgresLeaseCommitFaultBoundariesProveZeroOrOneLease`; `TestDeterministicPostBindPreLeaseObservationMatrixUsesOneStableLeaseOperation`. | Runtime Execution "Post-bind, pre-lease failure contract". |
| Terminal linearization splits pre-lease (no-lease terminal transaction, two dispositions, no fabricated post-lease effects) and post-lease (evidence ingestion, terminal CAS) (D25, D77). | `executePostgresPreLeaseTerminal` / `executePostgresRuntimeTerminal`; `postgres_terminal.go`. | `TestPostgresNoLeaseTerminalFaultMatrixIsAllOrNone`; `TestPostgresPreLeaseTerminalDigestBindsExactCanonicalBytes`; `TestPostgresPreLeaseRuntimeBindingRejectionCommitLossReplaysTheTerminalRuntime`; `TestPostLeaseCancelFencesAuthorityBeforeCleanupAndDoesNotClaimPhysicalRelease`. | Runtime Execution "Runtime Run state and linearization". |
| Success vs cancel/timeout/revocation share one authoritative terminal/fence order; late success is bounded diagnostic evidence (D26). | Fence CAS on terminal ingestion. | `TestPostgresWorkerStopWinsLateSuccessRaceAndReplaysAfterResponseLoss`; `TestPostgresWorkerHeartbeatAndLeaseRevokeRacePreservesSingleCurrentFence`; `TestInMemoryLateRuntimeViewAcceptanceAtomicallyRetainsFenceBeforeReset`. | Runtime Execution "Runtime Run state and linearization". |
| Platform deadline is C03-controlled-clock interpreted; pre-lease deadline uses no-lease terminal; post-lease deadline requests stop/secret/network/C04 discard; caller context timeout is not downstream termination (D27). | `advancePostgresPreLeaseTimeBounds`, `advancePostgresPostLeaseDeadline`, controlled `now` clock. | `TestPostgresRuntimeDeadlineWinsOverLateRuntimeBindingRejection`; `TestPostOpenRuntimeDeadlineFencesC03AndDiscardsTheExactC04RuntimeView`; `TestPostgresPostOpenRuntimeDeadlinePersistsC03AndExactC04Discard`; `TestExpiredRuntimeDeadlineIsDurablyRejected`. | Runtime Execution "Runtime Run state and linearization". |
| User/administrator cancellation is fence-first; pre-lease cancel proves no lease/process/dispatch; post-lease cancel does not itself prove stop/discard/reset (D28). | `executePostgresCancel`; no-lease vs post-lease cancellation dispositions. | `TestPostgresCanonicalCancellationUsesControlLaneWithoutPoisoningStartAdmission`; `TestPostLeaseCancelFencesAuthorityBeforeCleanupAndDoesNotClaimPhysicalRelease`; `TestPostOpenCancelFencesTheExactC04RuntimeViewOnce`. | Runtime Execution "Runtime Run state and linearization". |
| Command rejection is not terminal `Rejected`; terminal `Rejected` only after accepted start with permanent pre-process prerequisite failure (D29). | `RuntimeDecisionFact.Disposition` vs `RuntimeSnapshot.Outcome`; journal distinguishes command rejection from terminal. | `TestCommandRejectionIsNotTerminalRuntimeRejected`; `TestDecision97RequestPersistenceMatrix`; `TestRejectedImmutableInputsDiscardTheOpenedC04RuntimeViewOnce`. | Runtime Execution "Request and rejection journal". |
| `Lost` only after lease/possible process effect and final unreconcilable ambiguity; no-lease proof forbids `Lost`; no second lease or live migration (D30). | Recovery classification (`recovery.go`) requires possible-process-effect evidence for `Lost`. | `TestRecoveryContract_ClassifyZeroLease_Rejected`; `TestRecoveryContract_ClassifyPossibleProcessEffect_Lost`; `TestRecoveryContract_ZeroLeaseProofed`; `TestRecoveryContract_ClassifyAmbiguousLease_Reconcile`. | Runtime Execution "Adapter normalization and reconciliation". |
| Raw worker/vendor status maps only to adapter observation; never alone proves terminal/C04/containment/capacity facts (D31). | Worker observation normalization; evidence trust validation. | `TestAgentComposeObservationNormalization`; `TestWorkerObserveOrdersCursorAndKeepsTerminalClaimsAsEvidenceCandidates`; `TestValidateEvidenceTrust`. | Runtime Execution "Adapter normalization and reconciliation". |

### Sandbox Lease, node truth, and capacity facts (Decisions 32-41)

| Requirement | Code evidence | Test evidence | Decision/document evidence |
| --- | --- | --- | --- |
| Zero-or-one time-bounded exclusive lease per run; stable acquire operation; lease binds run/start/grant/node/class/policy/deadline/epochs/fence (D32). | `lease_lifecycle.go`, `postgres_lease_lifecycle.go`, stable lease-acquire binding; zero-or-one proof via lease-root tables. | `TestPostgresLeaseCommitFaultBoundariesProveZeroOrOneLease`; `TestDeterministicLeaseCommitResponseLossReplaysExactlyOneLease`; `TestInspectShowsAuthoritativeActiveLeaseAndPhysicalOccupancy`. | ADR 0022; Runtime Execution "Sandbox Lease and capacity semantics". |
| Acquire only after grant + node revalidation; provider-capable revalidates Active Quota Reservation; C03 cannot widen class/policy or repin (D33). | `commitPostgresPreLease` revalidates grant/node/reservation; `validatePostgresQuotaReservation`. | `TestPostgresCapsuleCreationRevalidatesCurrentQuotaReservationAuthority`; `TestProviderCapableAdmissionAndLeaseRequireExactActiveQuotaReservation`; `TestPostgresRuntimeBindingIsRevalidatedBeforeDelayedLeaseAcquisition`. | Runtime Execution "Sandbox Lease and capacity semantics". |
| Renewal only from current owned worker/node authority with exact generation/fence/fresh attestation (D34). | Lease renewal path in `lease_lifecycle.go`; `RuntimeMaintenance.RenewSandboxLease`. | `TestLeaseRenewalIsFencedIdempotentAndInspectable`; `TestWorkerHeartbeatDelegatesToCurrentLeaseLifecycleAndRejectsStaleFence`; `TestLeaseAcquireRejectsStaleNodeCatalogSafetyEpoch`. | Runtime Execution "Sandbox Lease and capacity semantics". |
| Revoke fences before stop, revokes secrets/network, prevents new authoritative success, requests Runtime View discard, enters containment reconciliation; pre-lease paths do not fabricate these (D35). | Post-lease revocation path; fence-first ordering; `FenceSandboxLease` maintenance. | `TestPostgresInFlightRuntimeViewOpenAcceptanceIsFencedByConcurrentLeaseRevokeWithoutReplay`; `TestPostgresPostLeaseRuntimeBindingRevocationStopsLaterPrerequisitesAndDiscardsRuntimeView`; `TestPostgresWorkerHeartbeatAndLeaseRevokeRacePreservesSingleCurrentFence`. | Runtime Execution "Sandbox Lease and capacity semantics". |
| Lease expiry never reactivates; fences old worker, quarantines capacity, issues stop/observe/reset work; release needs exact containment/reset (D36, D37). | Expiry/revoke/reset state machine; `SandboxResetAuthority`. | `TestRevokeResetAndPoolReuseRequireCompleteCurrentEvidence`; `TestPostgresHeartbeatCompactionPreservesCurrentAuthorityFacts`; `TestNodeLossFencingAuthorizesOnlyLogicalCapacityRelease`. | Runtime Execution "Sandbox Lease and capacity semantics". |
| Runtime Execution owns physical node facts; Scheduler owns logical admission; three non-derivable evidence classes bridge authority (D38, D41, D92). | `RuntimeCapacityEvidenceSnapshot` with `RuntimeFencedOrTerminal`/`NoLeasePhysicalDisposition`/`PhysicalCapacityReleaseReady`; Scheduler bridge validates exact evidence. | `TestSchedulerAuthorityCannotManufactureExecutionNodeTruth`; `TestPostgresSchedulerCapacityEvidenceBridgeRejectsConflictsAndSeparatesAuthority`; `TestPostgresSchedulerCapacityEvidenceRetainsExactTerminalIdentityAcrossEvidenceClasses`; `TestNodeLossFencingAuthorizesOnlyLogicalCapacityRelease`. | Runtime Execution "Sandbox Lease and capacity semantics"; Scheduling and Capacity Admission. |
| Node loss/stale generation quarantines capacity; no-lease disposition does not clear quarantine; stale nodes need fresh readiness/reset (D39). | Node occupancy/quarantine facts; readiness attestation. | `TestRecoveryContract_ClassifyAmbiguousLease_Reconcile`; `TestPostgresBoundSelectedNodeReservationBlocksAnotherAdmission`; `TestNodeLossFencingAuthorizesOnlyLogicalCapacityRelease`. | Runtime Execution "Sandbox Lease and capacity semantics". |
| Physical sandbox pools only after complete reset with new identity/lease/fence; no cross-lease residue (D40). | Reset/attestation maintenance; `AttestExecutionNode`, `ConfirmSandboxReset`. | `TestRevokeResetAndPoolReuseRequireCompleteCurrentEvidence`; `TestPostgresCleanupMaintenanceFullLifecycleThroughMaintain`. | Runtime Execution "Sandbox Lease and capacity semantics". |

### Worker responsibility and shared private protocol (Decisions 42-46)

| Requirement | Code evidence | Test evidence | Decision/document evidence |
| --- | --- | --- | --- |
| Agent/Tool workers are replaceable adapters with no business authority (D42). | `worker_protocol.go` capability adapters; `agentWorkerBackend`/`toolWorkerBackend`; typed capability payloads. | `TestToolWorkerUsesIndependentBackendAndClosedTypedCapabilityPayload`; `TestAgentAndToolWorkersShareLifecycleFenceEvidenceReplayAndSafeErrorContract`; `TestAgentToolWorkerParity`. | Runtime Execution "Agent Worker and Tool Worker". |
| Shared private protocol families: Accept/Heartbeat/Observe/Stop with shared semantics (D43). | `workerProtocol` interface (`accept`, `heartbeat`, `observe`, `stop`). | `TestAgentWorkerAcceptIsDurableInspectableAndPayloadBound`; `TestWorkerHeartbeatCanonicalDigestBindsCompleteLeaseAndNodeSnapshots`; `TestWorkerObserveBindsCurrentTaskAndRuntimeRevisions`; `TestWorkerStopIsExactIdempotentAndClaimsOnlyBestEffortAcceptance`. | Runtime Execution "Agent Worker and Tool Worker". |
| Accept is durable acceptance only; duplicate exact ack; same operation/different capsule digest is integrity conflict (D44). | `newWorkerAccept` + durable accept journal. | `TestAgentWorkerAcceptIsDurableInspectableAndPayloadBound`; `TestPostgresWorkerAcceptIsAtomicAcrossFaultsAndResponseLoss`; `TestPostgresConcurrentDuplicateWorkerAcceptDispatchesBackendOnce`. | Runtime Execution "Agent Worker and Tool Worker". |
| Observe returns ordered cursor-bound observations; cursor never substitutes for operation/lease/fence/revision (D45). | `workerObserve` + `WorkerCursorSnapshot`. | `TestWorkerObserveOrdersCursorAndKeepsTerminalClaimsAsEvidenceCandidates`; `TestPostgresWorkerObservationPersistsCursorReplayOrderingAndConflictsAcrossRestart`; `TestPostgresWorkerObserveResamplesTimeAndAuthorityAfterBackendCall`. | Runtime Execution "Agent Worker and Tool Worker". |
| Stop is best-effort enforcement; ack never implies containment/lease release/discard/capacity (D46). | `workerStopIntent`/`workerStopAck`; stop evidence normalization. | `TestWorkerStopIsExactIdempotentAndClaimsOnlyBestEffortAcceptance`; `TestWorkerStopReasonMustMatchAuthoritativeCause`; `TestPostgresWorkerStopFaultsRollbackOrReplayExactAck`. | Runtime Execution "Agent Worker and Tool Worker". |

### Runtime Binding, Execution Capsule, Runtime View, and manifests (Decisions 47-54)

| Requirement | Code evidence | Test evidence | Decision/document evidence |
| --- | --- | --- | --- |
| C03 validates binding/lock digests without reading pipeline graph, rollout, registry tags, or candidates (D47). | `RuntimeBindingValidator`, `runtimeBindingValidationRequest`. | `TestRuntimeBindingRejectionsFailClosedWithoutLaterPrerequisites`; `TestPostgresRuntimeBindingIsRevalidatedBeforeDelayedLeaseAcquisition`; `TestMissingRuntimeBindingValidatorCannotReachLeaseAcquisition`. | Runtime Execution "Runtime Binding…"; Release Management. |
| Private immutable Execution Capsule created only after admission+lease+durable prerequisites; binds scope/operation/binding/class/image/executor/deadline/lease/fence/contracts/Runtime View/GatewayGrant/network-secret policy (D48). | `capsule.go`, `capsule_resolution.go`, `ensurePostgresExecutionCapsule`. | `TestExecuteBuildsImmutableCapsuleAndOwnedDispatchWithoutLeakingPrivateCanaries`; `TestCapsuleReadinessExpiresWithLeaseAuthority`; `TestPostgresCapsuleAuditAndDispatchCommitAtomicallyAndReplayAfterRestart`. | Runtime Execution "Execution Capsule, Runtime View, inputs…". |
| Immutable input manifest uses opaque content identities/digests/size/media class/read-only capability; never bucket keys/registry locators/host paths/credentials/mounts (D49). | `ImmutableInputManifestBinding`, `ImmutableInputBinding`. | `TestImmutableInputManifestAndRuntimeViewEffectInvalidCasesFailClosed`; `TestZeroEntryImmutableInputManifestStillRequiresValidation`; `TestResolvedImmutableInputManifestFailsClosedAcrossIntegrityScopeAndFreshnessMatrix`. | Runtime Execution "Execution Capsule, Runtime View, inputs…". |
| Every input is validated before dispatch; missing/partial/duplicate/cross-scope/corrupt fail closed; existing path never infers success (D50). | `ImmutableInputValidator`; prerequisite persistence. | `TestResolvedImmutableInputManifestFailsClosedAcrossIntegrityScopeAndFreshnessMatrix`; `TestImmutableInputIntegrityRejectionsRemainSafeAndNotReady`; `TestRejectedImmutableInputsDiscardTheOpenedC04RuntimeViewOnce`; `TestPostgresRejectedImmutableInputsCommitC04DiscardOutboxBeforeDelivery`. | Runtime Execution "Execution Capsule, Runtime View, inputs…". |
| Mutating run opens exactly one isolated C04 Runtime View after lease; dispatch only after durable open; worker sees only sandbox-local logical locations; C03 never writes authoritative Task Workspace or commits Revision/Checkpoint (D51). | C04 open adapter (`prerequisite.go`), `SandboxLeaseAuthority`, exact open request binding. | `TestMutatingRuntimeOpensExactlyOnePostLeaseC04View`; `TestC04OpenNeverRunsBeforeSandboxLeaseCommit`; `TestC04ResponseLossUsesInspectForTheOriginalOperation`; `TestPostgresC04OpenAcceptedAfterLeaseRenewalCannotSatisfyReadiness`. | Runtime Execution "Execution Capsule, Runtime View, inputs…"; Task Workspace Lifecycle. |
| Read-only runs get no writable view; effect class is a canonical start binding that cannot upgrade (D52). | `EffectClass` binding in start and capsule. | `TestReadOnlyCapsuleReadinessRequiresDurableRuntimeBindingAndImmutableInputs`; `TestImmutableInputManifestAndRuntimeViewEffectInvalidCasesFailClosed`. | Runtime Execution "Execution Capsule, Runtime View, inputs…". |
| Output leaves only through declared channels; canonical manifest binds member identity/class/digest/size/contract/opaque reference/complete-partial; escape/symlink/device/oversize fail (D53). | `output.go` manifest validation. | `TestDeclaredOutputChannelsValidateUntrustedProposalSecurityMatrix`; output contract tests. | Runtime Execution "Execution Capsule, Runtime View, inputs…". |
| Output and manifest stay untrusted proposals; validation/C04 commit/publication remain separate authorities (D54). | Evidence terminal ingestion treats guest output as proposal; C04 commit only via Task Orchestration exact fenced intent. | `TestWorkerTerminalObservationIsImmutableExceptForExactReplay`; `TestValidateEvidenceTrust`; C04 prerequisite tests. | Runtime Execution "Runtime Evidence and trust levels". |

### Secret, network, and non-leakage (Decisions 55-60)

| Requirement | Code evidence | Test evidence | Decision/document evidence |
| --- | --- | --- | --- |
| Hostile-execution treatment; driver name is not security proof; exact configuration must pass Execution Policy (D55). | `security.go` policy/attestation acceptance; `HostileExecutionSecurityContract`. | `TestHostileExecutionSecurityContractFailsClosedAcrossPolicyNetworkAndSecrets`; `TestProductionOnlySecurityEvidenceLocalPolicyIsNotHardening`. | Runtime Execution "Production security requirements". |
| Network default-deny; only explicit destinations/protocols; Gateway-only provider egress; GatewayGrant binds current run/operation/lease/fence/reservation/route/expiry (D56). | `network_policy` references; Gateway grant validation (`gateway.go`, `gateway_calls.go`). | `TestGatewayGrantIsRequestedOnlyAfterLeaseAndBindsExactAuthority`; `TestGatewayPerCallValidationRejectsLeaseRevokeAndTimeout`; `TestSecretCapabilityUseIsBoundToCurrentPurposeExpiryFenceAndRevocation`. | Runtime Execution "Production security requirements"; LLM Gateway and Usage Accounting. |
| Secret broker issues short-lived purpose-bound capabilities; fence/timeout/revoke invalidate new use; secrets never enter durable/content surfaces (D57, D58). | `SecretClass*` taxonomy and `SecretCapabilityReference` binding. | `TestSecretCapabilityUseIsBoundToCurrentPurposeExpiryFenceAndRevocation`; `TestCanaryNonLeakageAcrossEveryDecision20Surface`; `TestPrivateSecurityReferencesRejectNetworkAndSecretLocatorShapes`. | Runtime Execution "Production security requirements". |
| Host path/mount/session/data-root/object locator/credential/content/raw error/cross-Workspace existence never appear on public seam, wire, PostgreSQL records, evidence, or diagnostics (D59). | Opaque identities, content-free snapshot fields, closed safe errors. | `TestCanaryNonLeakageAcrossEveryDecision20Surface`; `TestCanaryNonLeakagePublicAndObservabilityTypes`; `TestPostgresPrerequisiteAndRuntimeViewErrorsRetainNoSensitiveDetails`; `TestProductionOnlySecurityEvidenceOwnedTransportNeverLeaksCanaries`. | Runtime Execution "Production security requirements". |
| Deny-by-default closed error/diagnostic fields; non-enumerating authorization/not-found (D60). | `safe_error.go` taxonomy; closed diagnostics. | `TestSafeErrorsRetainNoRawCauseMessageLocatorOrCrossWorkspaceExistence`; `TestOperationalDiagnosticsIsReadOnlyAndNonEnumerating`; `TestPostgresOperationalDiagnosticsReadOnlyNonEnumerating`; `TestClaimDispatchNonEnumerationMakesMissingWrongAndCrossWorkspaceIdentitiesIndistinguishable`. | Runtime Execution "Error taxonomy". |

### Adapter normalization and reconciliation (Decisions 61-68)

| Requirement | Code evidence | Test evidence | Decision/document evidence |
| --- | --- | --- | --- |
| Public contract is always durable asynchronous; synchronous adapters run only inside owned workers with stable operation binding and Inspect/Observe support (D61). | `adapter_normalization.go`; worker protocol. | `TestDurableBindingMustBeReconcilable`; `TestAgentWorkerAcceptIsDurableInspectableAndPayloadBound`. | Runtime Execution "Adapter normalization and reconciliation". |
| Poll persists opaque external operation+cursor before polling; callback validates producer auth/operation/digest/generation/lease/fence/ordering; queue at-least-once with durable acceptance ack (D62). | `adapter_normalization.go` (`ExternalOperationHandle`, `DurableAdapterBinding`, `ExternalOperationCursor`). | `TestDurableBindingMustBeReconcilable`; `TestAgentComposeObservationNormalization`; `TestNullBackendsFailClosed`. | Runtime Execution "Adapter normalization and reconciliation". |
| Transport/callback/poll/queue/worker loss enters Reconciling; never alone fabricates Failed/Cancelled/Lost; reconciler inspects the original operation (D63). | `reconciliation.go`; `ReconcilingResult`. | `TestSharedRuntimeExecutionFaultMatrix`; `TestReconcilerIntegration_ReconcilerCreatedFromHarness`; `TestReconciliationContract_ResponseLossAfterCommit_ReplayReturnsOriginalDecision`; `TestPostgresPostLeaseAmbiguousRuntimeBindingRevocationClearsReadiness`. | Runtime Execution "Adapter normalization and reconciliation". |
| Worker loss before external ack replays the same start; after ack observes the existing operation; claim changes never change identity/outcome (D64). | Worker protocol replay/observe paths. | `TestPostgresWorkerAcceptIsAtomicAcrossFaultsAndResponseLoss`; `TestPostgresWorkerObservationPersistsCursorReplayOrderingAndConflictsAcrossRestart`; `TestReconciliationContract_DuplicateExactStart_Idempotent`. | Runtime Execution "Adapter normalization and reconciliation". |
| Duplicate exact evidence idempotent; same ID/different digest integrity incident; out-of-order deferred/safe-rejected; stale facts bounded diagnostic (D65). | Evidence idempotency journal; `WorkerEvidenceCandidateSnapshot`. | `TestPostgresOutboxExactDuplicateIsIdempotentAndConflictFailsClosed`; `TestPostgresWorkerObservationPersistsCursorReplayOrderingAndConflictsAcrossRestart`; `TestReconciliationContract_DifferentDigestSameOperationID_IntegrityConflict`. | Runtime Execution "Adapter normalization and reconciliation". |
| Agent Compose production adapter uses pinned HTTP/Connect contract, not CLI shell-out; stable operation mapped to vendor request identity; same-key/same-payload enforced (D66). | `agent_compose_http_adapter.go` (`NewAgentComposeHTTPAdapter`, `buildAgentComposeHTTPClient`). | `TestAgentComposeHTTPAdapterRejectsCLIShellOut`; `TestAgentComposeHTTPAdapterClientRequestIDMapping`; `TestAgentComposeObservationNormalization`. | Runtime Execution "Production and test adapters". |
| Agent Compose daemon/guest/image/executor pinned to exact versions/digests; per-node daemon and data root; endpoint only on owned protected network; identities opaque (D67). | `AgentComposeDaemonConfig`, `AgentComposeTLSConfig`, `ValidateDaemonConfig`. | `TestAgentComposeDaemonConfigValidation`; `TestPrivateSecurityReferencesRejectNetworkAndSecretLocatorShapes`. | Runtime Execution "Production and test adapters". |
| Tool executor can use a separate sandbox backend or map through the same contract; raw exec/caller shell/path-only is not a production seam (D68). | `tool_executor_parity_adapter.go` (`ToolExecutorViaAgentCompose`). | `TestToolExecutorAgentComposeParity`; `TestToolWorkerUsesIndependentBackendAndClosedTypedCapabilityPayload`; `TestToolExecutorCapabilityContractDigest`; `TestToolExecutorProductionQualification`. | Runtime Execution "Production and test adapters". |

### Runtime Evidence and trust levels (Decisions 69-73)

| Requirement | Code evidence | Test evidence | Decision/document evidence |
| --- | --- | --- | --- |
| Runtime Evidence binds the full normalized identity/scope/digest/epoch/fence/outcome/manifest/receipt/cursor/cleanup fact set (D69). | `RuntimeEvidenceSnapshot`/evidence root derivation (`EvidenceRootDerivation`). | `TestEvidenceRootDerivation`; `TestUsageReceiptEvidenceRootHasNoArbitraryReferenceCap`; `TestPostgresRuntimeAggregateCodecRetainsGatewayAndUsageEvidence`. | Runtime Execution "Runtime Evidence and trust levels". |
| Fixed trust order: PostgreSQL facts authoritative; adapter evidence verified before acceptance; guest output untrusted; validation independent; receipts only via Usage Accounting; projections never drive state (D70). | Trust validation in evidence ingestion; projection rebuild reads retained facts. | `TestValidateEvidenceTrust`; `TestPostgresProjectionRebuildRedrivesFromRetainedFacts`; `TestPostgresProjectionFailureDoesNotRollbackCommittedAuthority`. | Runtime Execution "Runtime Evidence and trust levels". |
| Agent Compose raw detail/stdout/events/stats/callbacks/external IDs stay adapter evidence (D71). | `agent_compose_http_adapter.go` observation normalization to opaque evidence. | `TestAgentComposeObservationNormalization`; `TestAdapterErrorNeverLeaksRawVendorDetail`. | Runtime Execution "Runtime Evidence and trust levels". |
| Evidence ingestion validates producer authority/digest/scope/binding/manifests/revision/generations/epochs/fence/prerequisites; cross-scope/missing/partial/corrupt/unknown-major fail closed (D72). | `validateEvidenceForTerminalIngestion`; evidence trust matrix. | `TestValidateEvidenceTrust`; `TestWorkerTerminalObservationIsImmutableExceptForExactReplay`; `TestPostgresPrerequisiteAuditConflictFailsClosed`. | Runtime Execution "Runtime Evidence and trust levels". |
| Usage Receipt references express known/unknown/missing/estimated/not-applicable/proven-no-send; missing usage is not zero; late usage does not change terminal outcome (D73). | `UsageReceiptReference` states; `RuntimeUsageEvidenceSnapshot`. | `TestNonProviderGatewayAndUsageAreExplicitlyNotApplicable`; `TestPostgresRuntimeAggregateCodecRetainsGatewayAndUsageEvidence`; `TestProviderCapableRunKeepsGatewayExtensionExplicitlyUnsatisfied`. | Runtime Execution "Runtime Evidence and trust levels"; LLM Gateway and Usage Accounting. |

### PostgreSQL atomicity, outbox, audit, and recovery boundary (Decisions 74-82)

| Requirement | Code evidence | Test evidence | Decision/document evidence |
| --- | --- | --- | --- |
| PostgreSQL is the sole authority for execution state/journal/lease/node/evidence/audit/outbox/bridge/debt; worker memory/queue/SQLite/session/path/process/telemetry never are (D74). | `postgres.go` authority; all protected writes in PostgreSQL tables. | `TestPostgresAuthorityReplaysRetainedDecisionWithCurrentSnapshot`; `TestPostgresAuthorityReconstructsRuntimeSnapshotAfterRestart`; `TestPostgresCorruptPersistenceFailsClosedWithoutPrivateDetail`. | Runtime Execution "Runtime Evidence and trust levels"; Decision 74. |
| Task Orchestration atomic enactment+Work Item first; fresh C03 start transaction atomically binds Accepted+Bound+fence+lease operation+audit (D75). | `taskorchestration` scheduler participant; `executePostgresStart`. | `TestPostgresRuntimeWorkItemBindsCompleteCanonicalC03RequestInTaskTransaction`; `TestPostgresSchedulerParticipantFailureRollsBackTaskAndWorkItem`; `TestPostgresStartAcceptedBoundFaultMatrixIsAtomic`. | ADR 0029; Runtime Execution "One admission path and grant binding". |
| Lease-grant transaction consumes only the bound grant; revalidates reservation; commits occupancy/fence/audit/lease-attachment outbox; no re-admit/rebind/node-switch; zero-or-one proof; no remote I/O in transaction (D76). | `commitPostgresPreLease`; `postgresSchedulerLeaseAttachmentTransaction`. | `TestPostgresLeaseCommitFaultBoundariesProveZeroOrOneLease`; `TestPostgresCapsuleFaultsRollbackAtomicallyOrReplayCommittedResponseLoss`; `TestPostgresConcurrentCapsuleGenerationReplaysExactContentAndRejectsIdentityRebinding`. | Runtime Execution "One admission path and grant binding". |
| Pre-lease terminal is a no-lease transaction with two dispositions; post-lease terminal ingests evidence atomically; Scheduler consumes evidence under its own CAS (D77). | `postgres_terminal.go`; `postgres_evidence_terminal.go`. | `TestPostgresNoLeaseTerminalFaultMatrixIsAllOrNone`; `TestPostgresPreLeaseRuntimeBindingRejectionCommitLossReplaysTheTerminalRuntime`; `TestPostgresSchedulerCapacityEvidenceBridgeRejectsConflictsAndSeparatesAuthority`. | Runtime Execution "Runtime Run state and linearization"; Scheduling and Capacity Admission. |
| Owned outbox is at-least-once delivery source, not a second state machine; ack changes only delivery disposition (D78). | `runtime_execution_outbox` + delivery disposition table. | `TestPostgresOutboxAcknowledgementCannotRewriteAuthorityBinding`; `TestPostgresOutboxExactDuplicateIsIdempotentAndConflictFailsClosed`; `TestPostgresCapsuleAuditAndDispatchCommitAtomicallyAndReplayAfterRestart`. | Runtime Execution "Runtime Evidence and trust levels"; ADR 0020 pattern. |
| Mandatory audit failure fails the protected operation closed; projection/telemetry failure never rolls back a committed decision (D79). | `postgres_audit.go` in-transaction audit; asynchronous projection backlog. | `TestPostgresMandatoryAuditFailureRollsBackProtectedReconciliation`; `TestPostgresMandatoryAuditPersistsCompleteCanonicalFact`; `TestPostgresProjectionFailureDoesNotRollbackCommittedAuthority`; `TestPostgresProjectionRebuildRedrivesFromRetainedFacts`. | ADR 0027; Observability, Audit, and Cleanup Debt. |
| Heartbeat/renewal is authoritative lease fact; compaction preserves current authority facts and never compacts conflicts/uncontained resources/unresolved reconciliation (D80). | Retained heartbeat compaction. | `TestPostgresHeartbeatCompactionPreservesCurrentAuthorityFacts`; `TestWorkerHeartbeatDelegatesToCurrentLeaseLifecycleAndRejectsStaleFence`. | Runtime Execution "Runtime Evidence and trust levels"; Observability, Audit, and Cleanup Debt. |
| Recovery Point retains authoritative metadata/roots only; restore fences old work; Accepted/Bound zero-lease becomes Rejected + NoLease disposition; ambiguous lease reconciles; possible effect may become Lost (D81). | `recovery.go` classification; `postgres_recovery.go`. | `TestRecoveryContract_ClassifyZeroLease_Rejected`; `TestRecoveryContract_ClassifyAmbiguousLease_Reconcile`; `TestRecoveryContract_ClassifyPossibleProcessEffect_Lost`; `TestRecoveryContract_ClassifyFenced_BehindFence`; `TestRecoveryContract_OperationalModeNames`; `TestRecoveryContract_ValidRestoreDecision`. | Backup and Recovery; Runtime Execution "Cleanup, retention, backup, and repair". |
| Repair accepts only exact matching original identity/schema/digest/signature/manifest/scope/generation/fence; never orphan adoption/digest change/directory-session scan/log inference (D82). | `repair.go` exact-match validation. | `TestExactRepair_ExactMatch_Accepted`; `TestExactRepair_DigestMismatch_Rejected`; `TestExactRepair_IdentityMismatch_Rejected`; `TestExactRepair_OrphanCandidate_Rejected`; `TestExactRepair_FenceMismatch_Rejected`; `TestExactRepair_SchemaMismatch_Rejected`; `TestExactRepair_InvalidSource_Rejected`. | Runtime Execution "Cleanup, retention, backup, and repair". |

### Cleanup Debt, telemetry, and safe errors (Decisions 83-88)

| Requirement | Code evidence | Test evidence | Decision/document evidence |
| --- | --- | --- | --- |
| Runtime Execution uniquely owns process/sandbox/lease/containment/reset cleanup; C04 owns Runtime View/workspace/Checkpoint cleanup; no duplicate DebtID across modules (D83). | `cleanup_maintenance.go`; `CleanupOwnershipMatrix` analog. | `TestCleanupMaintenanceCrossModuleDebtDuplicationRejected`; `TestPostgresCleanupDebtSameResourceCannotBeDuplicated`; `TestPostgresCleanupDebtCreationReplaysAndSurvivesRestart`. | Observability, Audit, and Cleanup Debt; Runtime Execution "Cleanup, retention, backup, and repair". |
| Cleanup obligation is durable before any physical attempt, with owner/resource/generation/intent/fence/blockers/retry/safe error/estimates/unknown state (D84). | `CreateCleanupObligation`; `postgres_cleanup.go`. | `TestCleanupMaintenanceObligationBeforeAttemptAndRetryLifecycle`; `TestPostgresCleanupDebtPersistsRetryEstimationBlockersAndResolution`; `TestPostgresCleanupDebtCreationReplaysAndSurvivesRestart`. | Observability, Audit, and Cleanup Debt. |
| Resolutions require exact current authority/evidence; path disappearance/marker/metric/operator assertion never closes debt; AcceptedException cannot claim reclaimed capacity (D85). | `ResolveCleanupDebt`/`ExpireCleanupDebtException`; class-specific proof facts. | `TestCleanupMaintenanceResolutionsRequireExactEvidence`; `TestCleanupResolutionProofRequiresClassSpecificFacts`; `TestCleanupMaintenanceAcceptedExceptionExpiryAndSafeReopen`. | Observability, Audit, and Cleanup Debt. |
| Metrics use only registered bounded dimensions; business/high-cardinality values never become primary labels (D86). | `MetricLabels` allowlist; `observability.go`. | `TestBoundedMetricRegistryRejectsBusinessIDsAndFreeFormValues`; `TestTelemetrySurfacesNeverCarryBusinessIdentitiesOrFreeFormText`; `TestPostgresTelemetryProjectionIsBoundedAndCommittedAfterDecision`. | ADR 0027; Observability, Audit, and Cleanup Debt. |
| Structured logs/traces use allowlisted protected correlation with pre-export redaction; TraceID is diagnostic context only (D87). | `TraceMetadata`; allowlisted trace correlation. | `TestTraceIDIsDiagnosticContextAndNeverBecomesAKey`; `TestTelemetryDiagnosticRequiresAdministratorAndExactRuntimeRun`; `TestTelemetrySurfacesNeverCarryBusinessIdentitiesOrFreeFormText`. | ADR 0027; Observability, Audit, and Cleanup Debt. |
| Safe error taxonomy covers the Decision 88 categories; retryability never weakens auth/integrity/deadline/fencing (D88). | `safe_error.go` closed taxonomy. | `TestSafeErrorClosedTaxonomyCoversDecision88`; `TestSafeErrorCodeIsTypedAndNeverCarriesCallerText`; `TestSafeErrorsSurfaceFromPublicSeamAreContentFree`; `TestAdapterErrorRetryDisposition`. | Runtime Execution "Error taxonomy". |

### Cross-module prerequisite, maintenance, and local-development contracts (Decisions 89-99)

| Requirement | Code evidence | Test evidence | Decision/document evidence |
| --- | --- | --- | --- |
| One fixed admission ordering; no module/adapter skips a step, creates early side effects, or forms a second admission authority (D89). | `scheduler_participant.go` restricted participant; `executePostgresStart` ordering. | `TestPostgresStartAcceptedBoundFaultMatrixIsAtomic`; `TestPostgresCanonicalCancellationUsesControlLaneWithoutPoisoningStartAdmission`; `TestSchedulerAuthorityCannotManufactureExecutionNodeTruth`. | ADR 0029; Runtime Execution "One admission path and grant binding". |
| Work Item references exact Task enactment OperationID and full canonical C03 start digest; grant binds the fixed start and cannot rewrite it (D90). | `SchedulerAcceptanceFact`; `taskorchestration/runtime_enactment.go`. | `TestPostgresRuntimeWorkItemBindsCompleteCanonicalC03RequestInTaskTransaction`; `TestPostgresAcceptedBoundGrantCannotRotateRequeueOrRebind`; `TestPostgresRetainedStartReplayIgnoresNewerRedundantGrant`. | ADR 0029; Scheduling and Capacity Admission. |
| `ClaimAndAdmit` is the only admission decision; Accepted/Bound atomic in C03 transaction; unbound expiry/stale rules; crash proves zero/one lease before any terminal inference (D91). | Scheduler `ClaimAndAdmit`; C03 restricted participant; zero-or-one lease proof. | `TestPostgresStartAcceptedBoundFaultMatrixIsAtomic`; `TestPostgresAcceptedBoundGrantCannotRotateRequeueOrRebind`; `TestPostgresLeaseCommitFaultBoundariesProveZeroOrOneLease`; `TestPostgresStartRequestLookupFaultPreservesOriginalDecision`. | ADR 0029; Runtime Execution "One admission path and grant binding". |
| Three non-derivable bridge evidence classes; each atomically outboxed with authoritative facts; duplicate exact idempotent, conflict/stale fail closed; no module fabricates another's facts (D92). | `RuntimeCapacityEvidenceSnapshot`; Scheduler evidence validation. | `TestPostgresSchedulerCapacityEvidenceBridgeRejectsConflictsAndSeparatesAuthority`; `TestPostgresSchedulerCapacityEvidenceRetainsExactTerminalIdentityAcrossEvidenceClasses`; `TestPostgresOutboxExactDuplicateIsIdempotentAndConflictFailsClosed`. | Runtime Execution "Sandbox Lease and capacity semantics"; Scheduling and Capacity Admission. |
| Mutating start binds only the exact `RuntimeViewRequirement`; `SandboxLeaseAuthority` derived after lease; exact C04 open binding; response-loss replay never creates a second view; C04 owns view cleanup (D93). | `prerequisite.go` C04 open adapter; `SandboxLeaseAuthority`. | `TestC04OpenNeverRunsBeforeSandboxLeaseCommit`; `TestC04ResponseLossUsesInspectForTheOriginalOperation`; `TestC04FenceResponseLossInspectsTheOriginalTerminalOperation`; `TestMutatingRuntimeOpensExactlyOnePostLeaseC04View`; `TestNodeLossFencesTheExactOpenedC04RuntimeViewOnce`. | Runtime Execution "Execution Capsule, Runtime View, inputs…"; Task Workspace Lifecycle. |
| Provider-capable admission/lease require Active Quota Reservation; lease-then-grant ordering; short-lived GatewayGrant; stable refresh/rotation with monotonic generation and activation CAS; no direct-provider fallback (D94). | `gateway.go`, `gateway_grant_contract` paths, `gateway_calls.go`. | `TestProviderCapableAdmissionAndLeaseRequireExactActiveQuotaReservation`; `TestGatewayGrantIsRequestedOnlyAfterLeaseAndBindsExactAuthority`; `TestGatewayGrantRefreshUsesMonotonicGenerationAndActivationCAS`; `TestGatewayGrantExpiryUsesEveryAuthorityUpperBound`; `TestGatewayPerCallValidationRejectsOldGenerationAndCancelButAcceptedAttemptsSettle`; `TestGatewayRefreshRejectsScopeExpansion`; `TestPostgresRuntimeAggregateCodecRetainsGatewayAndUsageEvidence`. | Runtime Execution "Execution Capsule, Runtime View, inputs…"; LLM Gateway and Usage Accounting. |
| Public seam stays Execute/Inspect; protected `RuntimeMaintenance` and read-only `OperationalDiagnostics` carry typed authority/reason/expected facts with mandatory audit and no mutation of Task/Scheduler/C04/usage/site policy (D95). | `RuntimeMaintenance` (Maintain only), `OperationalDiagnostics` (Diagnose only). | `TestLeaseLifecycleUsesIndependentClosedMaintenanceSurface`; `TestOperationalDiagnosticsIsReadOnlyAndNonEnumerating`; `TestPostgresOperationalDiagnosticsReadOnlyNonEnumerating`; `TestPostgresMaintenanceReplayCodecRejectsMissingLifecycleAuthority`. | Runtime Execution "Minimal external interface". |
| `RuntimeSnapshot` versioned closed schema with unknown-major fail-closed, no implicit downgrade, lossless registered projections only (D96). | `SnapshotSchemaVersion`; projection validation. | `TestRuntimeSnapshotClosedSchemaAndLosslessProjectionRules`; `TestPostgresCapsuleAuditUnknownFieldTamperFailsInspectClosedWithoutLeakage`; schema contract tests. | Runtime Execution "Minimal external interface". |
| Request persistence matrix: accepted/rejection/stale/unauthorized/malformed/unknown-major/same-key-different-digest each persist and replay exactly as specified (D97). | `runtime_execution_requests`/`decisions` journal; security observation records. | `TestDecision97RequestPersistenceMatrix`; `TestDecision97HostileIngressPersistsSanitizedObservations`; `TestNilRuntimeCommandIsInvalidRequest`; `TestMalformedZeroSchemaCancelIsInvalidRequest`; `TestMalformedZeroSchemaInspectIsInvalidRequest`; `TestPostgresMalformedCancelIsInvalidRequest`. | Runtime Execution "Request and rejection journal". |
| Agent/Tool internal model calls/SDK retries/tools/subprocesses stay under one Runtime Run; only a new Task Orchestration enactment creates a new Runtime Run (D98). | Worker protocol lineage; gateway call lineage under one run. | `TestProviderCapableAgentWorkerInvocationBindsGatewayAndQuotaLineage`; `TestProviderCapableToolWorkerLifecyclePreservesGatewayLineage`; `TestGatewayCallAcceptanceLinearizesBeforeConcurrentCancel`. | Runtime Execution "Agent Worker and Tool Worker". |
| Local-development implementation is an independent owned adapter (not a deterministic in-memory fake), runs the same public/protected suites, preserves canonical/lease/disposition/prerequisite/evidence semantics, and exposes no legacy seam (D99). | `local_development.go` + file-backed journal + explicit `LocalDevelopmentPolicy`. | `TestLocalDevelopmentRuntimeExecutionContract`; `TestLocalDevelopmentSmokeAndRestartFlow`; `TestLocalDevelopmentRestartRetainsStaleRejectionAndNoLegacySeam`; `TestLocalDevelopmentWorkerFlowRunsRealOwnedWorkerProtocol`; `TestProductionOnlySecurityEvidenceLocalPolicyIsNotHardening`. | Runtime Execution "Production and test adapters"; Decision 99. |

## Structural deletion gates

Testing Decision 23 requires structural dependency and capability gates. The
audit added a dedicated gate file,
`backend/internal/runtimeexecution/deletion_gate_test.go`, containing three
tests:

| Gate | What it proves | How it is structural |
| --- | --- | --- |
| `TestStructuralDeletionGateLegacyExecutionPackagesAbsentFromBuildClosure` | The transitive build closure (including test dependencies) of `internal/runtimeexecution` and `internal/taskorchestration` contains no legacy execution package (`internal/service`, `internal/handler`, `internal/repository`, `internal/model`, `internal/router`, `internal/config`, `internal/database`) and only the allowlisted owned ports (`runtimeexecution`, `taskorchestration`, `taskworkspace`, `scheduler`, `testpostgres`). | It runs `go list -deps -test` and checks the actual import graph. Package import paths are the only path-like facts; harmless file or method renames do not change the graph. |
| `TestStructuralDeletionGateTargetsBuildWithoutLegacyPackages` | The two target packages compile as standalone build targets, so the contract builds with the legacy execution packages absent. | It runs `go build` on the exact package set; combined with the import-graph gate, absence is proven by dependency, not by file spelling. |
| `TestStructuralDeletionGateCapabilitySurfaceAbsence` | No public, protected, or private seam interface (`RuntimeExecution`, `RuntimeMaintenance`, `OperationalDiagnostics`, `workerProtocol`, `workerCapabilityAdapter`, `agentWorkerBackend`, `toolWorkerBackend`, `ownedWorkerTransport`) exposes a host-path, session, recent-run discovery, arbitrary-shell, shared-daemon control, or general repository mutation capability. | It walks the actual method signatures and reachable value types via reflection. Sandbox-local logical locations and opaque digest roots are explicitly allowed, matching Decisions 51/59; a renamed legacy method that is not part of a seam cannot satisfy or break the gate. |

The capability classes deleted rather than wrapped are taken from ADR 0016 and
`docs/architecture/legacy-business-migration-and-compatibility.md` ("Deletion
test and compatibility boundary"): `AgentComposeClient.Up/Run` CLI contract and
Docker-exec wrapper, session IDs and `/sessions/<id>/workspace` inference,
Task last-run/last-session/workspace paths, single-Runtime Run per Phase Run,
TaskService direct execution, Docker socket / data-root / workspace-path API
access, mutable `latest` selection, and status/session/directory/Skill-tree
recovery fallbacks. The gates prove these are absent from the C03 contract
without depending on one literal filename, method spelling, error string, or
source substring.

## Remediation completed by #87

The audit found and repaired implementation gaps instead of recording them as
paper findings:

1. **PostgreSQL evidence-terminal decision fact.** `advancePostgresEvidenceTerminal`
   returned `RuntimeDecision{Snapshot: snapshot}` with an empty fact whenever
   the current snapshot was not a terminal-evidence candidate, and
   `Execute(Start)`/`Execute(Cancel)` replaced their full decision with that
   value. `TestPostgresPostBindPreLeaseMatrixAndCapacityEvidenceSeparation`
   (permanent pre-lease bindings) and
   `TestPostgresBindCancelAndDeadlineRacesUseOneAcceptedBinding` (bind/deadline)
   failed on real PostgreSQL because the returned decision lost its command
   fact. The function now preserves the caller's command fact and replaces only
   the snapshot; the tests pass and every other PostgreSQL gate stays green.
2. **Format gate.** `gofmt -l` flagged 21 files (20 in `runtimeexecution`,
   1 in `taskorchestration`) as unformatted. All were normalized with
   `gofmt -w`; the diff is whitespace-only (`git diff -w` is empty except for
   trailing-blank-line and double-blank-line normalization).
3. **Structural deletion gates.** Added the three gates described above
   (Testing Decision 23), which previously existed only as scattered seam
   tests and informal grep checks.

## Standards and Spec review

### Standards

Pass, with no unresolved blocking finding. The implementation follows the
documented domain language and ADR boundaries: opaque identity types stay
independent, the public/protected/private seam split is enforced
reflection-structurally, PostgreSQL is the single execution authority, worker
and transport mechanics never become a second mutation path, safe errors are
closed, telemetry is bounded, and the legacy execution surface is proven
absent by import-graph and capability gates rather than by wrapping it. The
audit's two fixes (decision-fact preservation and formatting) did not change
any public contract.

### Spec

Pass, with no unresolved blocking finding. Every #71 Implementation Decision
(1-99) and Testing Decision (1-30) has code, test, and decision evidence in
the matrices above. The completion audit intentionally did not add a second
public mutation, a general repository, a raw callback ingest seam, an
untyped administrator command, or any caller-provided mutable Runtime
snapshot: #71's closed variants and the protected `RuntimeMaintenance`/
`OperationalDiagnostics` seams already define the only allowed extensions, and
any future product workflow must enter through a typed intent under its owning
module.

## Testing Decisions evidence

| Testing Decision | Evidence |
| --- | --- |
| 1. Highest-level seam is `Execute/Inspect`; tests assert decisions/snapshots/identities/revisions/states/leases/fences/evidence/containment/safe errors/dispositions. | All `runtimeexecution` contract tests; `TestPublicSurfaceHasOnlyExecuteAndInspect`. |
| 2. One shared black-box suite runs on four implementations unconditionally. | `RunRuntimeExecutionContract` driven by `TestDeterministicInMemoryRuntimeExecutionContract`, `TestLocalDevelopmentRuntimeExecutionContract`, `TestPostgresRuntimeExecutionContract`, `TestProductionShapedOwnedTransportWorkerRuntimeExecutionContract`. |
| 3. Task Orchestration consumer contract proves Task-first creation, exact binding, atomic Accepted/Bound, evidence-only consumer, no C03 Phase progression. | `TestPostgresRuntimeWorkItemBindsCompleteCanonicalC03RequestInTaskTransaction`; `TestPostgresGenerationRuntimeWorkItemCarriesExactPinnedCatalogPackages`; Runtime evidence-only adapter tests. |
| 4. Public-surface tests prove no second mutation seam, general repository, public lease mutator, raw callback completion, path/session API, or worker-created Runtime Run. | `TestPublicSurfaceHasOnlyExecuteAndInspect`; `TestGatewayUsageContractsCarryNoProviderOrPersistenceAuthority`; `TestLeaseLifecycleUsesIndependentClosedMaintenanceSurface`; `TestOperationalDiagnosticsIsReadOnlyAndNonEnumerating`. |
| 5. Identity/idempotency suite covers replay, later-revision replay, same-key/different-payload, WorkItem↔operation digest mismatch, grant generations, lease-acquire operation replay, rebinding denials. | `replay_test.go`, `concurrency_contract_test.go`, `TestPostgresRetainedStartReplayIgnoresNewerRedundantGrant`, `TestPostgresAcceptedBoundGrantCannotRotateRequeueOrRebind`, `TestPostgresOperationIdentityCannotCrossCommandKinds`. |
| 6. Concurrency suite covers concurrent start/start, bind/cancel, bind/deadline, lease-commit races, duplicate terminal evidence, two workers/one lease, single terminal winner. | `concurrency_contract_test.go`; `TestPostgresBindCancelAndDeadlineRacesUseOneAcceptedBinding`; `TestPostgresLeaseCancelAndDeadlineRacesCommitZeroOrOneLease`; `TestPostgresConcurrentDuplicateWorkerAcceptDispatchesBackendOnce`; `TestPostgresConcurrentWritersProduceOneRuntimeRevisionWinner`. |
| 7. Runtime lifecycle suite covers all non-terminal states and all six terminal outcomes with the post-bind/pre-lease matrix. | `TestImmutableTerminalOutcomes`; `TestPostgresPostBindPreLeaseMatrixAndCapacityEvidenceSeparation`; `TestCommandRejectionIsNotTerminalRuntimeRejected`; `TestTerminalRuntimeNeverAcceptsAnotherStart`. |
| 8. Sandbox Lease suite covers stable acquire, zero-or-one proof, renew, revoke, expiry, release, stale generations, safety epoch, quarantine, reset, pool reuse. | `lease_lifecycle_contract_test.go`; `TestPostgresLeaseCommitFaultBoundariesProveZeroOrOneLease`; `TestLeaseAcquireRejectsStaleNodeCatalogSafetyEpoch`; `TestRevokeResetAndPoolReuseRequireCompleteCurrentEvidence`. |
| 9. Agent/Tool matrix covers success/failure, multi-call under one run, invalid arbitrary shell, wrong capability/entrypoint, provider Tool via Gateway, shared protocol, backend independence. | `worker_protocol_contract_test.go`; `TestToolWorkerFailsClosedForCapabilityEntrypointParameterSchemaAndGatewayMismatch`; `TestToolWorkerUsesIndependentBackendAndClosedTypedCapabilityPayload`; `TestProviderCapableAgentWorkerInvocationBindsGatewayAndQuotaLineage`. |
| 10. Capsule/input/output suite covers exact bindings, wrong platform/executor, epochs, read-only/mutating, single C04 view, missing/corrupt/duplicate/cross-scope inputs, malicious outputs, canonical manifests. | `capsule_contract_test.go`; `input_contract_test.go`; `output_contract_test.go`; `TestExecuteBuildsImmutableCapsuleAndOwnedDispatchWithoutLeakingPrivateCanaries`; `TestPostgresRejectedImmutableInputsCommitC04DiscardOutboxBeforeDelivery`. |
| 11. Secret/network security suite covers default-deny, allowed destination, direct provider denial, Gateway-only egress, purpose/expiry/fence, revocation, platform-credential denial, non-leakage. | `security_contract_test.go`; `TestHostileExecutionSecurityContractFailsClosedAcrossPolicyNetworkAndSecrets`; `TestSecretCapabilityUseIsBoundToCurrentPurposeExpiryFenceAndRevocation`; `TestProductionOnlySecurityEvidence*`. |
| 12. Adapter normalization suite covers sync response loss, poll cursor, callback auth/duplicate/out-of-order, queue claim/ack loss, Agent Compose binding/restart/drift, Tool parity. | `adapter_normalization_contract_test.go`; `TestAgentComposeHTTPAdapterClientRequestIDMapping`; `TestAgentComposeObservationNormalization`; `TestDurableBindingMustBeReconcilable`. |
| 13. Reconciliation suite covers crashes at every boundary, worker loss before/after ack, daemon restart, node loss/return, ack loss, ambiguous timeouts, duplicate/out-of-order/stale evidence, only-after-possible-effect `Lost`. | `reconciliation_contract_test.go`; `TestSharedRuntimeExecutionFaultMatrix`; `TestPostgresLeaseCommitFaultBoundariesProveZeroOrOneLease`; `TestRecoveryContract_Classify*`. |
| 14. Evidence trust suite covers producer auth, canonical digest, scope, binding/manifests, revision/generation/epoch/fence, prerequisites, duplicate exact, same-ID conflict, cross-Workspace attacks, Usage Receipt states. | `TestValidateEvidenceTrust`; `TestWorkerTerminalObservationIsImmutableExceptForExactReplay`; `TestPostgresRuntimeAggregateCodecRetainsGatewayAndUsageEvidence`; `TestPostgresPrerequisiteAuditConflictFailsClosed`. |
| 15. Fault-injection matrix covers every protected persistence boundary. | `fault_matrix_test.go`; `TestPostgresStartAcceptedBoundFaultMatrixIsAtomic`; `TestPostgresNoLeaseTerminalFaultMatrixIsAllOrNone`; `TestPostgresCapsuleFaultsRollbackAtomicallyOrReplayCommittedResponseLoss`; `TestPostgresPreCommitFaultsLeaveNoAuthoritativePartialState`. |
| 16. PostgreSQL integration uses real PostgreSQL and isolated schemas, proving all-or-none transactions, audit rollback, non-rollback of projections, single terminal winner, outbox restart, exact replay, stale-writer rejection. | `postgres_integration_test.go` and `postgres_*_integration_test.go` across `runtimeexecution` and `taskorchestration`; `testpostgres` harness. |
| 17. Mandatory audit/telemetry tests prove fail-closed audit, non-rollback projection, projection rebuild, bounded labels, unknown-source not zero. | `TestPostgresMandatoryAuditFailureRollsBackProtectedReconciliation`; `TestPostgresMandatoryAuditPersistsCompleteCanonicalFact`; `TestPostgresProjectionRebuildRedrivesFromRetainedFacts`; `TestBoundedMetricRegistryRejectsBusinessIDsAndFreeFormValues`. |
| 18. Cleanup Debt suite covers obligation-before-attempt, claim loss, retry, blockers, generation/fence drift, ambiguous cleanup, exact resolutions, exception expiry, no false capacity release, no cross-module duplication. | `cleanup_maintenance_contract_test.go`; `postgres_cleanup_proof_test.go`; `TestPostgresCleanupDebtPersistsRetryEstimationBlockersAndResolution`; `TestPostgresCleanupDebtSameResourceCannotBeDuplicated`; `TestCleanupMaintenanceAcceptedExceptionExpiryAndSafeReopen`. |
| 19. Recovery/repair suite covers restore generation advancement, old claim/lease/worker invalidation, zero-lease proof → Rejected+NoLease, ambiguous lease → reconcile, possible effect → Lost, read-only recovery, exact repair, orphan rejection. | `recovery_contract_test.go`; `repair_contract_test.go`; `TestPostgresRuntimeViewOpenFinalFactRepairsPendingAckAfterRestart`; `TestPostgresRuntimeViewTerminalFinalStateRepairsPendingAckAfterRestart`. |
| 20. Non-leakage suite injects canaries across content/prompt/parameters/raw errors/credentials/paths/object keys/registry locators/URLs/cross-Workspace identity. | `non_leakage_contract_test.go` (`TestCanaryNonLeakageAcrossEveryDecision20Surface`, `TestCanaryNonLeakagePublicAndObservabilityTypes`); `TestPostgresPrerequisiteAndRuntimeViewErrorsRetainNoSensitiveDetails`. |
| 21. Adapter parity gate compares all four implementations' decisions/snapshots/errors/dispositions; differences only in opaque evidence roots or explicit production-only proof. | The four-way `RunRuntimeExecutionContract` tests; `TestProductionOnlySecurityEvidenceLocalPolicyIsNotHardening`. |
| 22. Race gate uses real concurrency and the race detector over Execute/Inspect, deadline timer, worker observation, lease renewal, terminal ingestion, reconciliation, cleanup claim. | `race_gate_test.go` (`TestRaceGate*`); Validation race commands below. |
| 23. Deletion tests are structural dependency/capability gates with legacy-absence build proof, rename-stable. | `deletion_gate_test.go` (added by #87). |
| 24. Completion gates: focused C03, four-way shared, local smoke/restart, real PostgreSQL, production-shaped transport/worker, fault/race, security/non-leakage, full backend regression, format/static. | Validation section below. |
| 25. C04 prerequisite suite proves no view before admission/lease, exact open binding with `SandboxLeaseAuthority`, no dispatch before open acceptance, response-loss replay, rejection/timeout/cancel/discard, C04 cleanup ownership. | `prerequisite_contract_test.go` (`TestC04OpenNeverRunsBeforeSandboxLeaseCommit`, `TestMutatingRuntimeOpensExactlyOnePostLeaseC04View`, `TestC04ResponseLossUsesInspectForTheOriginalOperation`, `TestC04PermanentOpenRejectionIsDurableAndNotRetried`). |
| 26. Usage/Gateway suite proves Active Reservation gate, lease-then-grant, short-lived exact grant, refresh/rotation monotonic generation, per-Call revalidation, late settlement, no direct fallback. | `gateway_grant_contract_test.go`, `gateway_call_contract_test.go` (`TestGatewayGrantIsRequestedOnlyAfterLeaseAndBindsExactAuthority`, `TestGatewayGrantRefreshUsesMonotonicGenerationAndActivationCAS`, `TestGatewayPerCallValidationRejectsOldGenerationAndCancelButAcceptedAttemptsSettle`). |
| 27. Scheduler bridge suite proves the single admission path, three evidence classes, atomic evidence outbox, ack-loss replay, conflict/stale rejection, no general `ReleaseCapacity`, no fabricated readiness. | `TestPostgresSchedulerCapacityEvidenceBridgeRejectsConflictsAndSeparatesAuthority`; `TestPostgresSchedulerCapacityEvidenceRetainsExactTerminalIdentityAcrossEvidenceClasses`; `TestSchedulerAuthorityCannotManufactureExecutionNodeTruth`; `TestPostgresBoundSelectedNodeReservationBlocksAnotherAdmission`. |
| 28. Protected maintenance suite runs only through `RuntimeMaintenance` and proves authority/reason/revision/fence validation, audit rollback, cross-module denial; diagnostics read-only/non-enumerating; no maintenance variants in the public union. | `cleanup_maintenance_contract_test.go`; `TestLeaseLifecycleUsesIndependentClosedMaintenanceSurface`; `TestOperationalDiagnosticsReadOnlyContentFreeNonEnumerating`; `TestPostgresMaintenanceReplayCodecRejectsMissingLifecycleAuthority`; `TestPostgresOperationalDiagnosticsReadOnlyNonEnumerating`. |
| 29. Command-journal suite covers the Decision 97 matrix row by row. | `journal_contract_test.go` (`TestDecision97RequestPersistenceMatrix`, `TestDecision97HostileIngressPersistsSanitizedObservations`); `schema_contract_test.go`; `TestPostgresMalformedCancelIsInvalidRequest`. |
| 30. RuntimeSnapshot schema suite covers unknown request/evidence major, unknown required field/variant, optional minor additions, lossless old projection, lossy downgrade denial. | `schema_contract_test.go`; `TestRuntimeSnapshotClosedSchemaAndLosslessProjectionRules`; `TestPostgresCapsuleAuditUnknownFieldTamperFailsInspectClosedWithoutLeakage`. |

## Child acceptance evidence matrices

Each child row names the concrete specialization where the parent matrix
already contains the full proof, per the same convention as the Task
Orchestration completion report.

### #73 / C03-01

| Requirement | Code | Test |
| --- | --- | --- |
| Public `Execute(StartRuntimeRun \| CancelRuntimeRun)/Inspect(RuntimeRunRef)` seam. | `RuntimeExecution`, `RuntimeCommand` closed union. | `TestPublicSurfaceHasOnlyExecuteAndInspect`; `TestExecuteAcceptedCanonicalStartAndInspectCurrentSnapshot`. |
| Canonical journal with versioned deterministic encoding. | `canonical.go`. | `TestCanonicalStartEncodingIsVersionedDeterministicAndExact`; `TestCanonicalStartBindsCompleteExecutionAuthority`. |
| Deterministic in-memory model with controllable clock/IDs/faults/restart. | `in_memory.go` (`NewDeterministicHarness`). | `TestDeterministicHarnessRestartFaultsAndClock`; `TestDeterministicInMemoryRuntimeExecutionContract`. |
| Independent typed identities; no universal revision/TraceID. | `types.go` opaque IDs. | `TestPublicSurfaceHasOnlyExecuteAndInspect` independence matrix. |
| Exact replay and same-key/different-payload conflict. | Request journal. | `TestExactStartReplayAfterCancelReturnsOriginalFactAndCurrentSnapshot`; `TestStaleCancelReplayAfterLaterRevisionReturnsOriginalRejectionAndCurrentSnapshot`. |
| Malformed/unknown-schema fail closed without business identity. | Ingress validation. | `TestNilRuntimeCommandIsInvalidRequest`; `TestMalformedZeroSchemaCancelIsInvalidRequest`; `TestMalformedZeroSchemaInspectIsInvalidRequest`. |

### #74 / C03-02

| Requirement | Code | Test |
| --- | --- | --- |
| Real PostgreSQL authority for run state, journal, lease operation, fence, audit, outbox. | `postgres.go`, `postgres_foundation.go`, `postgres_audit.go`. | `TestPostgresAuthorityReplaysRetainedDecisionWithCurrentSnapshot`; `TestPostgresAuthorityReconstructsRuntimeSnapshotAfterRestart`; `TestPostgresCorruptPersistenceFailsClosedWithoutPrivateDetail`. |
| Mandatory audit fails the protected operation closed; projection failure does not roll back. | `postgres_audit.go`; async projection backlog. | `TestPostgresMandatoryAuditFailureRollsBackProtectedReconciliation`; `TestPostgresProjectionFailureDoesNotRollbackCommittedAuthority`; `TestPostgresProjectionRebuildRedrivesFromRetainedFacts`. |
| Owned at-least-once outbox that never rewrites authority. | `runtime_execution_outbox` + delivery disposition. | `TestPostgresOutboxAcknowledgementCannotRewriteAuthorityBinding`; `TestPostgresOutboxExactDuplicateIsIdempotentAndConflictFailsClosed`; `TestPostgresCapsuleAuditAndDispatchCommitAtomicallyAndReplayAfterRestart`. |
| Corrupt/tampered persistence fails closed without private detail. | Snapshot/codec validation. | `TestPostgresCorruptMandatoryAuditFailsClosedOnReplay`; `TestPostgresCorruptPersistenceFailsClosedWithoutPrivateDetail`; `TestPostgresCapsuleAuditUnknownFieldTamperFailsInspectClosedWithoutLeakage`. |

### #75 / C03-03

| Requirement | Code | Test |
| --- | --- | --- |
| Atomic Start/Accepted/Bound linearization with Runtime fence, `LeaseAcquireBy`, stable lease operation. | `executePostgresStart`; restricted Scheduler participant. | `TestPostgresFoundationDoesNotCreateAcceptedBoundHalfState`; `TestPostgresStartAcceptedBoundFaultMatrixIsAtomic`; `TestPostgresStartRequestLookupFaultPreservesOriginalDecision`. |
| Bound grant never unbound/requeued/rebound to a higher generation. | Grant binding rules. | `TestPostgresAcceptedBoundGrantCannotRotateRequeueOrRebind`; `TestPostgresRetainedStartReplayIgnoresNewerRedundantGrant`. |
| Full post-bind/pre-lease matrix with temporary/permanent/expiry/cancel/deadline/crash rows. | `advancePostgresPreLease*`; `executePostgresPreLeaseTerminal`. | `TestPostgresPostBindPreLeaseMatrixAndCapacityEvidenceSeparation`; `TestPostgresPreLeaseAuthorityExpiryDeadlineAndCancelAreDistinct`; `TestPostgresBindCancelAndDeadlineRacesUseOneAcceptedBinding`. |
| No-lease terminal transaction with both dispositions; zero-or-one lease proof. | `postgres_terminal.go`. | `TestPostgresNoLeaseTerminalFaultMatrixIsAllOrNone`; `TestPostgresLeaseCommitFaultBoundariesProveZeroOrOneLease`; `TestPostgresPreLeaseTerminalDigestBindsExactCanonicalBytes`. |

### #76 / C03-04

| Requirement | Code | Test |
| --- | --- | --- |
| Zero-or-one time-bounded exclusive lease; stable acquire; zero-or-one proof across crashes. | `lease_lifecycle.go`, `postgres_lease_lifecycle.go`. | `TestPostgresLeaseCommitFaultBoundariesProveZeroOrOneLease`; `TestDeterministicLeaseCommitResponseLossReplaysExactlyOneLease`; `TestInspectShowsAuthoritativeActiveLeaseAndPhysicalOccupancy`. |
| Renewal fenced to current owned authority/fresh attestation. | Renewal path. | `TestLeaseRenewalIsFencedIdempotentAndInspectable`; `TestWorkerHeartbeatDelegatesToCurrentLeaseLifecycleAndRejectsStaleFence`. |
| Revoke fences first; expiry never reactivates; release needs exact containment/reset. | Fence-first revocation; reset authority. | `TestPostgresInFlightRuntimeViewOpenAcceptanceIsFencedByConcurrentLeaseRevokeWithoutReplay`; `TestRevokeResetAndPoolReuseRequireCompleteCurrentEvidence`; `TestPostgresHeartbeatCompactionPreservesCurrentAuthorityFacts`. |
| Node truth: quarantine on loss; no-lease disposition does not clear quarantine; logical vs physical release separated. | Node occupancy/quarantine facts; three evidence classes. | `TestNodeLossFencingAuthorizesOnlyLogicalCapacityRelease`; `TestPostgresBoundSelectedNodeReservationBlocksAnotherAdmission`; `TestSchedulerAuthorityCannotManufactureExecutionNodeTruth`. |
| Sandbox pools only after full reset with new identity/lease/fence. | `ConfirmSandboxReset`; `AttestExecutionNode`. | `TestRevokeResetAndPoolReuseRequireCompleteCurrentEvidence`; `TestPostgresCleanupMaintenanceFullLifecycleThroughMaintain`. |

### #77 / C03-05

| Requirement | Code | Test |
| --- | --- | --- |
| Runtime Binding revalidation before delayed lease; binding rejection terminalizes pre-lease. | `advancePostgresRuntimeBindingRejection`. | `TestPostgresRuntimeBindingIsRevalidatedBeforeDelayedLeaseAcquisition`; `TestPostgresRuntimeBindingRejectionTerminalizesBeforeLeaseAcquisition`; `TestRuntimeBindingRejectionsFailClosedWithoutLaterPrerequisites`. |
| Release/Catalog safety epoch and Template Lock closure validated; no catalog listing reads or repinning. | `CatalogExecutionBinding`; `RuntimeViewRequirement`. | `TestGenerationCatalogValidationReceivesOnlyTheExactTemplateLockClosureAndEpoch`; `TestLeaseAcquireRejectsStaleNodeCatalogSafetyEpoch`; `TestPostgresC04OpenAcceptedAfterLeaseRenewalCannotSatisfyReadiness`. |
| Post-lease C04 prerequisite: exactly one view, exact open binding, no dispatch before acceptance. | `prerequisite.go` C04 open adapter. | `TestC04OpenNeverRunsBeforeSandboxLeaseCommit`; `TestMutatingRuntimeOpensExactlyOnePostLeaseC04View`; `TestPostgresC04OpenAcceptedAfterLeaseRenewalCannotSatisfyReadiness`. |
| Capsule readiness only after durable prerequisites; immutable inputs validated; read-only runs skip views. | `capsule_resolution.go`; `ImmutableInputValidator`. | `TestCapsuleReadinessExpiresWithLeaseAuthority`; `TestReadOnlyCapsuleReadinessRequiresDurableRuntimeBindingAndImmutableInputs`; `TestResolvedImmutableInputManifestFailsClosedAcrossIntegrityScopeAndFreshnessMatrix`. |

### #78 / C03-06

| Requirement | Code | Test |
| --- | --- | --- |
| Provider-capable admission and lease require the same Active Quota Reservation. | `validatePostgresQuotaReservation`. | `TestProviderCapableAdmissionAndLeaseRequireExactActiveQuotaReservation`; `TestProviderCapableAdmissionRequiresExactActiveQuotaReservation`; `TestProviderCapableLeaseRevalidatesReservationAfterAdmission`. |
| Short-lived exact GatewayGrant requested only after lease; expiry bounded by every authority upper bound. | `gateway.go`. | `TestGatewayGrantIsNotRequestedBeforeLease`; `TestGatewayGrantIsRequestedOnlyAfterLeaseAndBindsExactAuthority`; `TestGatewayGrantExpiryUsesEveryAuthorityUpperBound`; `TestGatewayGrantWaitsForAllPostLeasePrerequisites`. |
| Refresh/rotation uses stable operation, monotonic generation, activation CAS, scope non-expansion. | `gateway_calls.go`. | `TestGatewayGrantRefreshUsesMonotonicGenerationAndActivationCAS`; `TestGatewayRefreshRejectsScopeExpansion`; `TestGatewayGrantReconciliationRetainsOriginalRequestAcrossTimeAndRestart`. |
| Per-Call revalidation; accepted attempts settle; no direct-provider fallback; missing usage not zero. | `GatewayCallAccess`; usage evidence. | `TestGatewayPerCallValidationRejectsLeaseRevokeAndTimeout`; `TestGatewayPerCallValidationRejectsOldGenerationAndCancelButAcceptedAttemptsSettle`; `TestNonProviderGatewayAndUsageAreExplicitlyNotApplicable`; `TestPostgresRuntimeAggregateCodecRetainsGatewayAndUsageEvidence`. |

### #79 / C03-07

| Requirement | Code | Test |
| --- | --- | --- |
| Private immutable Execution Capsule bound to every exact authority fact. | `capsule.go`. | `TestExecuteBuildsImmutableCapsuleAndOwnedDispatchWithoutLeakingPrivateCanaries`; `TestClaimDispatchBindsEveryExactRuntimeViewCapabilityFact`. |
| Claim/dispatch denies stale/expired/cancelled/missing/wrong-scope authority without capsule bytes; non-enumerating. | `capsule_resolution.go`. | `TestClaimDispatchDeniesStaleAuthorityMatrixWithoutCapsuleBytes`; `TestClaimDispatchNonEnumerationMakesMissingWrongAndCrossWorkspaceIdentitiesIndistinguishable`; `TestPostgresClaimDispatchDeniesExpiredAndCancelledAuthorityWithoutPayload`. |
| Immutable inputs/outputs validated against canonical manifests; hostile-output matrix fails closed. | `input_contract_test.go`; `output_contract_test.go`. | `TestResolvedImmutableInputManifestFailsClosedAcrossIntegrityScopeAndFreshnessMatrix`; `TestDeclaredOutputChannelsValidateUntrustedProposalSecurityMatrix`; `TestPostgresRejectedImmutableInputsCommitC04DiscardOutboxBeforeDelivery`. |
| Secrets/network never reach durable surfaces; hostile-execution policy acceptance. | `security.go`. | `TestHostileExecutionSecurityContractFailsClosedAcrossPolicyNetworkAndSecrets`; `TestSecretCapabilityUseIsBoundToCurrentPurposeExpiryFenceAndRevocation`; `TestPrivateSecurityReferencesRejectNetworkAndSecretLocatorShapes`. |

### #80 / C03-08

| Requirement | Code | Test |
| --- | --- | --- |
| Agent/Tool worker shared private protocol (Accept/Heartbeat/Observe/Stop). | `worker_protocol.go`. | `TestAgentAndToolWorkersShareLifecycleFenceEvidenceReplayAndSafeErrorContract`; `TestAgentWorkerAcceptIsDurableInspectableAndPayloadBound`. |
| Capability substitution/entrypoint/parameter/schema/gateway mismatch fails closed. | `toolWorkerCapabilityAdapter`, `agentWorkerCapabilityAdapter`. | `TestToolWorkerFailsClosedForCapabilityEntrypointParameterSchemaAndGatewayMismatch`; `TestAgentCapabilityContractRejectsIntentAndPromptSubstitution`. |
| Workers are replaceable adapters; backend independence; provider Tool via Gateway only. | `agentWorkerBackend`/`toolWorkerBackend`. | `TestToolWorkerUsesIndependentBackendAndClosedTypedCapabilityPayload`; `TestProviderCapableToolWorkerLifecyclePreservesGatewayLineage`; `TestPostgresWorkerProtocolContextFailuresAreDependencyUnavailable`. |
| Worker revalidates authority after backend calls; atomic accept; heartbeat/revoke race keeps one fence. | `postgres_worker_protocol.go`. | `TestPostgresWorkerAcceptAndStopRevalidateAuthorityAfterBackendCall`; `TestPostgresWorkerHeartbeatAndLeaseRevokeRacePreservesSingleCurrentFence`; `TestPostgresWorkerHeartbeatNormalizesRuntimeLoadFailureSeparatelyFromStaleState`. |

### #81 / C03-09

| Requirement | Code | Test |
| --- | --- | --- |
| Durable adapter normalization with opaque external handles and cursors. | `adapter_normalization.go`. | `TestDurableBindingMustBeReconcilable`; `TestAdapterNormalizedErrorClosedCategories`; `TestAdapterErrorRetryDisposition`. |
| Agent Compose production adapter uses pinned HTTP contract, not CLI; maps client_request_id; daemon config validation. | `agent_compose_http_adapter.go`. | `TestAgentComposeHTTPAdapterRejectsCLIShellOut`; `TestAgentComposeHTTPAdapterClientRequestIDMapping`; `TestAgentComposeDaemonConfigValidation`; `TestAgentComposeObservationNormalization`. |
| Tool executor parity through the same contract or independent backend; raw exec/shell is not a production seam. | `tool_executor_parity_adapter.go`. | `TestToolExecutorAgentComposeParity`; `TestToolExecutorCapabilityContractDigest`; `TestToolExecutorProductionQualification`. |
| Errors never leak raw vendor detail. | Safe error normalization. | `TestAdapterErrorNeverLeaksRawVendorDetail`; `TestPostgresDriverFailuresNormalizeWithoutPrivateDetail`. |

### #82 / C03-10

| Requirement | Code | Test |
| --- | --- | --- |
| Post-lease terminal CAS with accepted Runtime Evidence; single terminal winner; late success diagnostic-only. | `postgres_evidence_terminal.go`. | `TestPostgresWorkerStopWinsLateSuccessRaceAndReplaysAfterResponseLoss`; `TestWorkerTerminalObservationIsImmutableExceptForExactReplay`; `TestImmutableTerminalOutcomes`. |
| Evidence trust validation across authority/digest/scope/binding/revision/generation/epoch/fence/prerequisites. | `validateEvidenceForTerminalIngestion`. | `TestValidateEvidenceTrust`; `TestEvidenceRootDerivation`; `TestUsageReceiptEvidenceRootHasNoArbitraryReferenceCap`. |
| Terminal races across success/cancel/timeout and worker/lease revocation. | `evidence_terminal_contract_test.go`; `concurrency_contract_test.go`. | `TestPostgresWorkerStopWinsLateSuccessRaceAndReplaysAfterResponseLoss`; `TestPostgresWorkerHeartbeatAndLeaseRevokeRacePreservesSingleCurrentFence`; `TestInMemoryLateRuntimeViewAcceptanceAtomicallyRetainsFenceBeforeReset`. |

### #83 / C03-11

| Requirement | Code | Test |
| --- | --- | --- |
| Reconciliation classifies crash boundaries by zero/one lease and possible process effect; same-operation replay; no new grant generation. | `reconciliation.go`, `recovery.go`. | `TestReconciliationContract_CrashBeforeCommit_RestartReplaysAcceptance`; `TestReconciliationContract_CrashAfterLeaseCommit_EntersReconciling`; `TestRecoveryContract_Classify*`; `TestPostgresLeaseCommitFaultBoundariesProveZeroOrOneLease`. |
| Restore recovery: zero-lease → Rejected+NoLease; ambiguous lease → reconcile; possible effect → Lost; read-only mode rejects new starts. | `recovery.go`, `postgres_recovery.go`. | `TestRecoveryContract_ClassifyZeroLease_Rejected`; `TestRecoveryContract_ClassifyAmbiguousLease_Reconcile`; `TestRecoveryContract_ClassifyPossibleProcessEffect_Lost`; `TestRecoveryContract_OperationalModeNames`; `TestRecoveryContract_ValidRestoreDecision`. |
| Exact repair only: identity/schema/digest/scope/generation/fence match; orphans/digest changes rejected. | `repair.go`. | `TestExactRepair_ExactMatch_Accepted`; `TestExactRepair_OrphanCandidate_Rejected`; `TestExactRepair_DigestMismatch_Rejected`; `TestExactRepair_IdentityMismatch_Rejected`; `TestPostgresRuntimeViewOpenFinalFactRepairsPendingAckAfterRestart`. |
| Response-loss replay/inspect of the original operation never creates a second view. | C04/evidence replay paths. | `TestC04ResponseLossUsesInspectForTheOriginalOperation`; `TestC04FenceResponseLossInspectsTheOriginalTerminalOperation`; `TestPostgresRuntimeViewTerminalIntentFaultsRollbackOrResumeAfterRestart`. |

### #84 / C03-12

| Requirement | Code | Test |
| --- | --- | --- |
| Protected `RuntimeMaintenance` interface with typed Scheduler/security/recovery/cleanup authority; never starts/cancels runs or mutates Task/Scheduler/C04/usage/site policy. | `cleanup_maintenance.go`, `RuntimeMaintenance`. | `TestLeaseLifecycleUsesIndependentClosedMaintenanceSurface`; `TestPostgresMaintenanceReplayCodecRejectsMissingLifecycleAuthority`; `TestPostgresOperationalDiagnosticsReadOnlyNonEnumerating`. |
| Node-execution fence, containment/reset resolution with reason/authority/revision/fence validation and exact replay. | `FenceSandboxLease`, `ConfirmSandboxReset`, `AttestExecutionNode`. | `TestPostgresCleanupMaintenanceFullLifecycleThroughMaintain`; `TestPostgresCleanupMaintenanceMandatoryAuditRollback`; `TestCleanupMaintenanceExactReplayAndReasonConflict`. |
| C03 Cleanup Debt: obligation-before-attempt, retry, blockers, exact resolution evidence, exception expiry/reopen, no cross-module duplication, no false capacity release. | `cleanup_maintenance.go`, `postgres_cleanup*.go`. | `TestCleanupMaintenanceObligationBeforeAttemptAndRetryLifecycle`; `TestCleanupMaintenanceResolutionsRequireExactEvidence`; `TestCleanupMaintenanceAcceptedExceptionExpiryAndSafeReopen`; `TestCleanupMaintenanceCrossModuleDebtDuplicationRejected`; `TestPostgresCleanupDebtSameResourceCannotBeDuplicated`; `TestPostgresCleanupDebtPersistsRetryEstimationBlockersAndResolution`. |

### #85 / C03-13

| Requirement | Code | Test |
| --- | --- | --- |
| Bounded metric registry with allowlisted labels; no business/high-cardinality primary labels. | `observability.go`. | `TestBoundedMetricRegistryRejectsBusinessIDsAndFreeFormValues`; `TestTelemetrySurfacesNeverCarryBusinessIdentitiesOrFreeFormText`; `TestPostgresTelemetryProjectionIsBoundedAndCommittedAfterDecision`. |
| Allowlisted protected trace correlation; TraceID never becomes a key; pre-export redaction. | `TraceMetadata`. | `TestTraceIDIsDiagnosticContextAndNeverBecomesAKey`; `TestTelemetryDiagnosticRequiresAdministratorAndExactRuntimeRun`. |
| Safe error taxonomy (Decision 88) closed; no caller text/raw cause/locator/cross-Workspace existence. | `safe_error.go`. | `TestSafeErrorClosedTaxonomyCoversDecision88`; `TestSafeErrorCodeIsTypedAndNeverCarriesCallerText`; `TestSafeErrorsRetainNoRawCauseMessageLocatorOrCrossWorkspaceExistence`; `TestSafeErrorsSurfaceFromPublicSeamAreContentFree`. |
| Audit projection rebuilds from retained facts; projection failure does not roll back; end-to-end non-leakage. | `postgres_audit.go`; `projection_rebuild.go`. | `TestPostgresProjectionRebuildRedrivesFromRetainedFacts`; `TestPostgresMandatoryAuditPersistsCompleteCanonicalFact`; `TestCanaryNonLeakageAcrossEveryDecision20Surface`; `TestCanaryNonLeakagePublicAndObservabilityTypes`. |

### #86 / C03-14

| Requirement | Code | Test |
| --- | --- | --- |
| Four-way shared suite runs unconditionally with parity. | `contract_suite_test.go` (`RunRuntimeExecutionContract` + four factories). | `TestDeterministicInMemoryRuntimeExecutionContract`; `TestLocalDevelopmentRuntimeExecutionContract`; `TestPostgresRuntimeExecutionContract`; `TestProductionShapedOwnedTransportWorkerRuntimeExecutionContract`. |
| Local smoke/restart flow is developer-runnable and uses no legacy seam. | `local_development.go` file-backed journal. | `TestLocalDevelopmentSmokeAndRestartFlow`; `TestLocalDevelopmentRestartRetainsStaleRejectionAndNoLegacySeam`; `TestLocalDevelopmentWorkerFlowRunsRealOwnedWorkerProtocol`. |
| Production-shaped transport verifies machine auth, strict versioning, at-least-once, ack loss. | `owned_worker_transport.go`. | `TestOwnedWorkerTransportMachineAuthorizationAndStrictVersioning`; `TestOwnedWorkerTransportAtLeastOnceAndAckLoss`; `TestProductionOnlySecurityEvidenceOwnedTransportNeverLeaksCanaries`. |
| Fault matrix (Testing Decision 15) has reproducible cases; race gate covers Testing Decision 22 components. | `fault_matrix_test.go`; `race_gate_test.go`. | `TestSharedRuntimeExecutionFaultMatrix`; `TestRaceGateConcurrentExecuteInspectAndDeadlineTimer`; `TestRaceGateDeadlineTimerTimesOutPendingLease`; `TestRaceGateConcurrentWorkerObservationAndLeaseRenewal`; `TestRaceGateConcurrentReconciliationAndCleanupClaim`. |
| Production-only security evidence supplements rather than changes public semantics. | `TestProductionOnlySecurityEvidence*`. | `TestProductionOnlySecurityEvidenceLocalPolicyIsNotHardening`; `TestProductionOnlySecurityEvidenceMachineAuthAndNonEnumeration`; `TestProductionOnlySecurityEvidenceOwnedTransportNeverLeaksCanaries`. |

## Validation

All commands were run from `backend/`. PostgreSQL tests used the real server at
`postgres://postgres@127.0.0.1:54723/slidesmith_test?sslmode=disable`; every
test created and removed an isolated schema.

| Gate | Reproducible command | Result |
| --- | --- | --- |
| Focused C03 module | `go test ./internal/runtimeexecution -count=1` | PASS |
| Focused C03 with PostgreSQL | `SLIDESMITH_TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:54723/slidesmith_test?sslmode=disable' go test ./internal/runtimeexecution -count=1` | PASS |
| Task Orchestration integration with PostgreSQL | `SLIDESMITH_TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:54723/slidesmith_test?sslmode=disable' go test ./internal/taskorchestration -count=1` | PASS (after #87 evidence-terminal fix) |
| Four-way shared black-box suite | Exact command below | PASS on all four implementations |
| Local-development smoke/restart + no-legacy-seam | Exact command below | PASS |
| Structural deletion gates | `go test ./internal/runtimeexecution -run '^TestStructuralDeletionGate' -count=1` | PASS (all three) |
| Fault/race gates | `go test -race ./internal/runtimeexecution -run '^(TestSharedRuntimeExecutionFaultMatrix\|TestRaceGate)' -count=1` | PASS |
| Full backend regression with PostgreSQL | `SLIDESMITH_TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:54723/slidesmith_test?sslmode=disable' go test ./... -count=1` | PASS |
| Full backend race with PostgreSQL | `SLIDESMITH_TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:54723/slidesmith_test?sslmode=disable' go test -race ./... -count=1` | PASS |
| Vet | `go vet ./...` | PASS |
| Format | `gofmt -l .` | PASS, no output (after #87 normalization) |
| Module-file drift | `go mod tidy -diff` | PASS, no output |
| Diff hygiene | `git diff --check` | PASS, no output |

Commands containing regular-expression alternation are recorded verbatim so
Markdown escaping cannot change their shell meaning:

```bash
SLIDESMITH_TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:54723/slidesmith_test?sslmode=disable' \
go test ./internal/runtimeexecution \
  -run '^Test(DeterministicInMemory|LocalDevelopment|Postgres|ProductionShapedOwnedTransportWorker)RuntimeExecutionContract$' \
  -count=1 -v

SLIDESMITH_TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:54723/slidesmith_test?sslmode=disable' \
go test ./internal/runtimeexecution \
  -run '^(TestLocalDevelopmentSmokeAndRestartFlow|TestLocalDevelopmentRestartRetainsStaleRejectionAndNoLegacySeam|TestLocalDevelopmentWorkerFlowRunsRealOwnedWorkerProtocol)$' \
  -count=1 -v

SLIDESMITH_TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:54723/slidesmith_test?sslmode=disable' \
go test ./internal/runtimeexecution \
  -run '^(TestSharedRuntimeExecutionFaultMatrix|TestRaceGate|TestStructuralDeletionGate|TestPublicSurfaceHasOnlyExecuteAndInspect|TestLeaseLifecycleUsesIndependentClosedMaintenanceSurface|TestOperationalDiagnosticsIsReadOnlyAndNonEnumerating)$' \
  -count=1 -v
```

## Cleanup Debt ownership

Runtime Execution is the physical-resource owner for process, sandbox, lease,
containment, and reset cleanup only. The closed matrix assigns:

- Runtime Views, Task Workspace materializations, and Checkpoints to C04 Task
  Workspace Lifecycle;
- object staging, cache, and quarantine to Durable Object;
- usage/quota and Gateway artifacts to Usage Accounting / LLM Gateway;
- catalog and release package materializations to Catalog Publication and
  Release Management;
- backup-copy staging to Backup and Recovery.

C03 retains only content-free Cleanup Debt facts and the same-resource
non-duplication proof (`TestCleanupMaintenanceCrossModuleDebtDuplicationRejected`,
`TestPostgresCleanupDebtSameResourceCannotBeDuplicated`). It never stores
physical location, credential, retry schedule, or deletion authority for
another module's resources.

## Out of scope and follow-up

No #71-scoped blocking implementation work remains. The following are explicit
platform follow-ups, not completion blockers:

- legacy record migration, ownership backfill, dual-write, hard cutover,
  `CommitCutover`, and the actual deletion of legacy CLI/session/path/
  shared-daemon/recent-run/single-run code under the separately governed
  migration plan (Issue 17 / ADR 0016 / ADR 0028);
- production hardening, legal/open-source approval, provider onboarding,
  threat-model acceptance, and traffic enablement for the Agent Compose and
  Tool executor production adapters;
- production Scheduler placement, Usage Accounting settlement, Gateway
  onboarding, telemetry vendor, retention, and SLO configuration;
- deployment, production data mutation, or any irreversible cleanup; and
- any future product workflow that wants a new Runtime capability must be
  specified under its owning module and still enter C03 through a typed
  `Execute`/`RuntimeMaintenance` intent rather than a new generic seam.

> #71 implementation complete does not mean legacy wiring, production
> hardening, deployment, or traffic cutover is complete.
