# Artifact Publication Completion Report v8 (C05-08, child SPEC #112)

- Date: 2026-08-11
- Parent SPEC: [#103](https://github.com/Vt00ls/SlideSmith/issues/103) (C05 Artifact Publication deep module)
- Completion audit: [#112](https://github.com/Vt00ls/SlideSmith/issues/112) (C05-08 shared acceptance, structural deletion gates, parent SPEC completion audit)
- Audit start: `6d5ab20` (HEAD when the audit worktree opened; merge of PR #119, C05-07)
- Audited base: `111d5d4` (merge of PR #102, the C03-15 completion that precedes C05-01)

## Result

The Artifact Publication (C05) implementation contract defined by #103 is
complete. Every Implementation Decision (1-48), Testing Decision (1-18), and
Acceptance Criterion of the parent SPEC has implementation/test evidence or an
explicit scope disposition recorded in this report. The module has exactly one
public mutation seam (`Mutate(PublicationIntent) -> PublicationDecision`) and
one pure read seam (`Query(PublicationQuery) -> PublicationView`), persists
its authoritative facts atomically in real PostgreSQL, keeps worker,
transport, outbox, evidence, reconciliation, residue, Cleanup Debt, audit,
and telemetry mechanics private, and runs the same public black-box contract
unconditionally across three implementations: deterministic in-memory, real
PostgreSQL owned persistence, and owned transport-to-PostgreSQL. Structural
deletion gates prove by import-graph and capability-surface inspection that
the new C05 core, the Task Orchestration publication bridge, and the owned
transport adapter build and execute the contract with the legacy
TaskService / runtime publisher / manual-edit worker / download handler /
repository / filesystem / object-key / directory-scan / timestamp /
latest-file / `PublishVersion` authority absent.

This conclusion is about the #103 module and adapter contracts. It does not
claim that the legacy application has been cut over, that production
hardening, traffic enablement, vendor certification, or deployment is
complete, or that legacy records have been migrated, dual-written, or
deleted. No production data mutation, legacy code/data deletion, hard
cutover, traffic enablement, or deployment was performed in this work.

## Audit basis

The audit used the fixed comparison:

```text
git diff 111d5d4...HEAD
git log --first-parent 111d5d4..HEAD --oneline
```

The completion worktree is `Vt00ls/spec-child-c05-08-structural-deletion-gates-spec`
at `6d5ab20`; the C05-08 work (structural deletion gate extension for the
Task Orchestration bridge, behavioral gates, and this report) is the current
branch tip. Standards sources were `AGENTS.md`, `CONTEXT.md`,
`docs/architecture/artifact-publication-core-report-v1.md` through
`artifact-publication-audit-observability-report-v7.md`, ADR 0008, ADR 0012,
ADR 0016, ADR 0028, and the sibling module contracts (Task Orchestration,
Runtime Execution, Task Workspace Lifecycle, content authorization and
sharing, Durable Object storage, observability audit and Cleanup Debt). The
repository has no Makefile, task runner, or CI workflow; the reproducible
gates are therefore the native Go commands in the Validation section.

### Merged child evidence

| Child | Pull request | Merge commit | Result |
| --- | --- | --- | --- |
| #104 / C05-01 | [#113](https://github.com/Vt00ls/SlideSmith/pull/113) | `eb68ead` (`2a17f82` + reject-fence follow-up) | Merged |
| #105 / C05-02 | [#114](https://github.com/Vt00ls/SlideSmith/pull/114) | `db60b9a` | Merged |
| #106 / C05-03 | [#115](https://github.com/Vt00ls/SlideSmith/pull/115) | `ea06b4b` | Merged |
| #107 / C05-04 | [#116](https://github.com/Vt00ls/SlideSmith/pull/116) | `bcdc0f8` | Merged |
| #108 / C05-05 | [#117](https://github.com/Vt00ls/SlideSmith/pull/117) | `d722366` | Merged |
| #109 / C05-06 | [#118](https://github.com/Vt00ls/SlideSmith/pull/118) | `112e182` | Merged |
| #111 / C05-07 | [#119](https://github.com/Vt00ls/SlideSmith/pull/119) | `6d5ab20` | Merged |

## Evidence conventions

Code evidence is under `backend/internal/artifactpublication` unless a file
is named. Test names are public-seam contract tests in the same package
unless a file is named. The Task Orchestration publication bridge evidence
lives under `backend/internal/taskorchestration`
(`publication_bridge.go`, `publication_bridge_test.go`,
`publication_bridge_deletion_gate_test.go`). The principal accepted
decisions are:

- [ADR 0008](../adr/0008-publish-artifact-versions-as-immutable-sets.md): Artifact Versions are immutable sets.
- [ADR 0012](../adr/0012-retain-business-publications-and-expire-execution-state.md): business publications are retained; execution state expires.
- [ADR 0016](../adr/0016-hard-cut-over-legacy-execution-state.md): legacy execution state is deleted, not wrapped.
- [ADR 0028](../adr/0028-promote-only-verifiable-legacy-business-facts.md): only verifiable legacy business facts are promoted.
- [Artifact Publication core report](./artifact-publication-core-report-v1.md) through [audit-observability report](./artifact-publication-audit-observability-report-v7.md): the seven C05 child reports.
- [Legacy Business Migration and Compatibility](./legacy-business-migration-and-compatibility.md): cutover boundary and deletion inventory.

## #103 acceptance evidence matrix

Every Implementation Decision is cited. Closely-coupled decisions are
grouped in one row; each row names the concrete code and test evidence that
proves the requirement rather than relying on the issue checkbox.

### Authority and public seams (Decisions 1-6)

| Requirement | Code evidence | Test evidence | Decision/document evidence |
| --- | --- | --- | --- |
| Independent deep module; only public mutation seam is `Mutate(PublicationIntent) -> PublicationDecision`; only read seam is pure `Query(PublicationQuery) -> PublicationView` (D1, D2). | `seam.go`: `PublicationCore` exposes exactly `Mutate`/`Query`; `intents.go` closed intent union; `query.go` pure read-only dispatch. | `TestPublicSeamClosedSurfaces`; `TestMutationIntentUnionIsClosed`; `TestQueryIsPureReadOnly`; `TestNoRepositoryOrActiveSetterCapability`. | Artifact Publication core report; Decisions 1-2. |
| `PublicationIntent` is a closed typed union: prepare/verify/activate/reject/reconcile/cancel (+ C05-owned residue assembly/release/debt resolution) (D3). | `intents.go`; `state.go`; protected cleanup intents enter the same invariant engine. | `TestMutationIntentUnionIsClosed`; `TestFirstGenerationPrepareVerifyLifecycle`; `TestReconcileInspectAndCompleteVerification`; `TestRecordResidueAssemblyMintsSingleDebtBeforePhysicalAttempt`. | Decision 3. |
| Every mutation binds schema version, RequestID, OperationID, canonical digest, TaskID, expected stream revision, activity/generation/fence, safety epoch, one typed authority, and diagnostic occurred-at; telemetry fields never enter the business digest (D4). | `PublicationIntentHeader` in `types.go`; `CanonicalRequestDigest`; `bindDigest` in tests. | `TestOperationDigestBindingRequired`; `TestCanonicalDigestDeterminism`; `TestCanonicalEncodingFieldPresence`. | Decision 4; core report. |
| Task Orchestration outbox saves the complete canonical C05 request or its inseparable typed binding; OperationID/payload digest equal the C05 request identity; adapter cannot assemble fields (D5). | `taskorchestration/publication_bridge.go`: `PublicationRequestBinding` + `CanonicalBytes`/`CanonicalDigest`; `PublicationBridgeAdapter`. | `TestPublicationBridgeCommitFixesCompleteCanonicalRequest`; `TestPublicationBridgeAdapterCannotAssembleRequestFields`; `TestPublicationBridgeCanonicalDigestIsStableAcrossDeliveries`; `TestOwnedTransportBridgeCommitFixesCompleteCanonicalRequest`; `TestOwnedTransportBridgeDeliversThroughOwnedTransportToC05`. | Decision 5; owned transport report. |
| `PublicationDecision` reports only durable request/decision/operation identity, stream revision, candidate/active version, manifest digest, state, disposition, accepted evidence refs, and committed publication-evidence ref (D6). | `types.go` `PublicationDecision`; `state.go`; decision facts persisted in `in_memory.go`/`postgres_mutate.go`. | `TestFirstGenerationPrepareVerifyLifecycle`; `TestExactReplayReturnsOriginalDecision`; `TestNonLeakageDecisionsAndViews`. | Decision 6. |

### Artifact Version, manifest, membership and lineage (Decisions 7-14)

| Requirement | Code evidence | Test evidence | Decision/document evidence |
| --- | --- | --- | --- |
| Opaque non-reused ArtifactVersionID/ArtifactID; one Artifact belongs to exactly one version (D7). | `types.go` opaque IDs; candidate identity minted per operation; `TestOperationIdentityNeverReusedProves`. | `TestRestartPreservesIdentityNonReuse`; `TestOperationIdentityNeverReusedProves`; `TestBehavioralGatePublishVersionStringIsNotAuthority`. | Decision 7; ADR 0008. |
| Per-Task explicit publication stream revision and optional current-head pointer; CAS not time/string/file inference (D8). | `state.go` stream record; activation CAS on expected revision/head; `query.go` history/head projection. | `TestFirstGenerationActivationAdvancesStreamAtomically`; `TestActivationRejectsWrongExpectedRevisionHead`; `TestBehavioralGateHistoryAndHeadUseExplicitStreamFactsOnly`; `TestPostgresConcurrentActivationSingleWinner`; `TestPostgresTwoFirstVersionActivationRaceSingleWinner`. | Decision 8; activation report. |
| Activated version/member/manifest/lineage immutable; any change requires a new version and operation (D9). | `activation.go` immutable activation record; no mutation path after terminal. | `TestActivatedVersionImmutableAcrossLaterOperations`; `TestRejectAfterActivationIsTerminalConflict`; `TestManualEditChildActivationPreservesParent`. | Decision 9; ADR 0008. |
| Versioned deterministic canonical manifest encoding binds version ID, Task, kind, parent, lineage digest, sorted members; fail-closed on unknown major (D10, D11). | `manifest.go`; `canonical.go`-style deterministic encoding; `SchemaV1`. | `TestManifestDeterministicBinding`; `TestManifestSortStability`; `TestUnicodeNameNormalization`; `TestDuplicateMembersFailClosed`; `TestUnsupportedKindAndMediaTypeFailClosed`; `TestUnknownMajorSchemaFailsClosed`; `TestMinorVersionRefinementAccepted`; `TestCanonicalDigestDeterminism`. | Decisions 10-11; core report. |
| Manifest/lineage are locator-free; object key/path/prefix/bucket/mount/vendor/credential/temp name/materialization location excluded (D12). | `manifest.go` member encoding (ArtifactID, kind, normalized logical name, media type, size, digest); `normalizedLogicalName` rejects path separators. | `TestLocatorFreeManifestAndLineage`; `TestCanonicalEncodingNeverContainsLocators`; `TestBehavioralGateObjectKeyPrefixesAndLatestFactsDoNotDriveAuthority`; `TestUnicodeNameNormalization` (path-like names fail closed). | Decision 12; non-leakage report. |
| Lineage binds kind, parent (if any), Task/Phase Run and Task Orchestration operation, Runtime Evidence set/root, validation evidence, C04 revision/checkpoint/commit evidence, content/durability root, and applicable generations/fences; manifest digest commits lineage (D13). | `manifest.go` lineage derivation; `LineageDigest` from pinned evidence refs. | `TestLineageCommitsPinnedEvidenceReferences`; `TestManualEditExportLineageEvidenceFailsClosed`; `TestFirstGenerationPrepareVerifyLifecycle` (lineage digest binding). | Decision 13. |
| First-generation has no parent; manual edit requires exact parent matching Task Orchestration activity source, C04 export source, and C05 expected head (D14). | `activation.go` child validation; `ValidatedExportEvidence`; `ContentTarget`/C04 capability seam. | `TestManualEditChildRequiresExactParentAndExpectedHead`; `TestManualEditChildRequiresActivatedParentAtActivation`; `TestFirstGenerationPrepareRejectsParent`; `TestManualEditWithoutActivatedParentFailsClosed`; `TestManualEditExportLineageEvidenceFailsClosed`; `TestIssueC04RequiresExactVersionNotLatest`. | Decision 14; content-target report. |

### Exact upstream evidence binding (Decisions 15-21)

| Requirement | Code evidence | Test evidence | Decision/document evidence |
| --- | --- | --- | --- |
| Task Orchestration request binds TaskID, PhaseRunID/generation/fence, activity generation, OperationID/payload digest, safety epoch, contract digest, expected revision/head, required evidence refs, manual-edit parent (D15). | `PublicationRequestBinding` in `taskorchestration/publication_bridge.go`; C05 `PreparePublicationPayload`. | `TestPublicationBridgeCommitFixesCompleteCanonicalRequest`; `TestPublicationBridgeManualEditBindingBindsExactParent`; `TestOwnedTransportBridgePostgresDeliversThroughOwnedTransportToC05`. | Decision 15; bridge report. |
| Runtime Evidence from Runtime Execution authority, bound to same Task/Phase/Runtime operations, binding, immutable inputs, terminal outcome, output proposal manifest digest, opaque proposal refs (D16). | `evidence.go` `RuntimeEvidence`; fixture `runtimeEvidence`. | `TestEvidenceMatrixFailClosed`; `TestNonLeakageActivationEvidenceAndVersionViews`. | Decision 16. |
| Platform validation evidence from registered validator authority, accepting the exact publication contract, binding all required Runtime Evidence and the same output-proposal manifest facts (D17). | `evidence.go` `ValidationEvidence`; `RuntimeEvidenceRefs` pinned at prepare. | `TestEvidenceMatrixFailClosed`; `TestFirstGenerationPrepareVerifyLifecycle`. | Decision 17. |
| C04 commit evidence binds same Task/Workspace, exact validation, resulting Revision, distinct Checkpoint, declared-state manifest, content/durability roots, operation/generation/fence; manual edit validates `ValidatedExportEvidence` (D18). | `evidence.go` `C04CommitEvidence`, `ValidatedExportEvidence`; child evidence set in fixture. | `TestEvidenceMatrixFailClosed`; `TestManualEditExportLineageEvidenceFailsClosed`; `TestPostgresC04CapabilityIssuanceAndVerification`. | Decision 18; content-target report. |
| Durable Object capability binds same policy domain, purpose, opaque ContentID, digest, size, write intent, immutable physical generation, verification method, adapter identity, current validity (D19). | `evidence.go` `ContentCapabilityEvidence`; registry `resolve` current-validity gate. | `TestCapabilityNotCurrentRequiresReconciliation`; `TestUnregisteredCapabilityFailsClosed`; `TestPostgresVerifiedUnattachedContentInaccessible`; `TestPostgresTypedReferenceOnlyAttachAndRelease`. | Decision 19; postgres report. |
| Pre-activation cross-evidence comparison; missing/extra/partial/cross-scope/stale/unknown-major evidence fails closed; newer fields cannot compensate (D20). | `evidence.go` evidence validation; `verifyPayload` cross-binding checks. | `TestEvidenceMatrixFailClosed`; `TestPostgresActivationRevalidatesFencedFacts`; `TestActivationRejectsStaleGenerationFence`. | Decision 20. |
| 0..N Runtime Evidence rules pinned by the Task Orchestration contract binding; C05 never selects recent runs or merges directories (D21). | `RequiredChannels`/`RuntimeRefs` pinned in prepare; contract digest in request. | `TestFirstGenerationPrepareVerifyLifecycle`; `TestEvidenceMatrixFailClosed`. | Decision 21. |

### Prepare, verify, activate, reject, cancel, reconcile (Decisions 22-30)

| Requirement | Code evidence | Test evidence | Decision/document evidence |
| --- | --- | --- | --- |
| Prepare persists stable OperationID, request digest, expected head/revision, candidate version, immutable manifest/lineage, typed staging references before physical action (D22). | `lifecycle.go` prepare; `postgres_mutate.go` journal. | `TestFirstGenerationPrepareVerifyLifecycle`; `TestResidueDurablyRecordedBeforePhysicalAction`; `TestPrepareFaultBeforeJournalLeavesNoOperation`; `TestPostgresActivationAllOrNoneOnFault`. | Decision 22; core/postgres reports. |
| Verify accepts only exact evidence/capabilities and records replayable verification; candidate manifest immutable after prepare (D23). | `lifecycle.go` verify; verification result persistence. | `TestEvidenceMatrixFailClosed`; `TestFailedVerifyExactReplayReturnsSameOutcome`; `TestReconcileInspectAndCompleteVerification`. | Decision 23. |
| PostgreSQL activation is the only business linearization point; all-or-none transaction over stream lock/revalidation, version/members/lineage, restricted DO attach, head advance, terminal operation, mandatory audit, evidence/outbox; no remote I/O in transaction (D24, D25). | `postgres_mutate.go` activation transaction; `postgres_participant.go` restricted attach; `postgres.go` schema. | `TestPostgresActivationAllOrNoneOnFault`; `TestPostgresDurableObjectParticipantFailureRollsBack`; `TestPostgresConcurrentActivationSingleWinner`; `TestPostgresTypedReferenceOnlyAttachAndRelease`; `TestPostgresVerifiedUnattachedContentInaccessible`; `TestPostgresCanonicalAuditFactsPersistedInTransaction`. | Decisions 24-25; postgres report. |
| DO prepare/verify outside transaction; no 2PC; verified unattached content stays inaccessible staging until retry/release/expiry/reconciliation (D25). | `residue_flow.go`; `postgres_residue.go`; `postgres_participant.go`. | `TestPostgresVerifiedUnattachedContentInaccessible`; `TestResidueExpiryMarksButNeverGuessesClosure`; `TestPostgresReleaseNeverTouchesActivatedMember`. | Decision 25; residue report. |
| Reject is terminal non-activation with closed safe reason and exact evidence failure; never creates version/member/head mutation; may emit rejection evidence (D26). | `lifecycle.go` reject; `RejectReason`/`EvidenceFailure`. | `TestRejectNeverCreatesVersionOrHead`; `TestRejectBindsExactEvidenceFailure`; `TestRejectAfterActivationIsTerminalConflict`. | Decision 26. |
| Cancel accepts exact Task Orchestration/protected authority, current operation, generation/fence, reason; cancel-first closes late activation; activation-first replay keeps active result (D27). | `lifecycle.go` cancel; fence CAS. | `TestCancelFirstLinearizesLateVerifyStale`; `TestCancelFirstClosesLateActivation`; `TestActivationFirstCancelReturnsActiveTerminalResult`; `TestCancelRejectsStaleGenerationFence`; `TestPostgresActivateCancelRaceDeterministicWinner`. | Decision 27; activation report. |
| Reconcile only inspects/replays the original operation, evidence, refs, digest; cannot allocate new version, modify manifest/parent, or create Task retry (D28). | `reconciliation` in `lifecycle.go`/`postgres_mutate.go`. | `TestReconcileInspectAndCompleteVerification`; `TestReconcileConfirmTerminalReplaysOnlyOriginalOperation`; `TestRestartAmbiguousOperationRemainsReconcileable`; `TestPostgresReconcileAlwaysReevaluatesOriginalOperation`. | Decision 28; reconciliation report. |
| Exact replay precedes current revision/head validation; same scope/key/digest returns original decision; same key/different digest is permanent integrity conflict (D29). | replay lookup before validation in `in_memory.go`/`postgres_mutate.go`. | `TestExactReplayReturnsOriginalDecision`; `TestSameKeyDifferentPayloadIntegrityConflict`; `TestResponseLossReplayAfterLaterStreamWork`; `TestDuplicateActivationExactReplay`; `TestActivationReplayAfterLaterStreamRevision`; `TestPostgresResponseLossExactReplay`; `TestPostgresSameKeyDifferentPayloadIntegrityConflict`. | Decision 29; idempotency report. |
| Concurrent different activations from same expected head: at most one winner; loser keeps non-active operation with stale-head/conflict disposition; never last-writer-wins (D30). | activation CAS on stream revision/head; row lock in postgres. | `TestTwoChildActivationRaceSingleWinner`; `TestTwoFirstVersionActivationRaceSingleWinner`; `TestActivateCancelRaceDeterministicWinner`; `TestPostgresTwoChildActivationRaceSingleWinner`; `TestPostgresTwoFirstVersionActivationRaceSingleWinner`; `TestPostgresConcurrentActivationSingleWinner`. | Decision 30; race report. |

### Query, authorization, content targets (Decisions 31-35)

| Requirement | Code evidence | Test evidence | Decision/document evidence |
| --- | --- | --- | --- |
| Ordinary Query exposes only activated versions and immutable publication-safe metadata; residue/prepared/etc. only via exact operation query or protected diagnostics (D31). | `query.go` dispatch; `QueryOperation`/`QueryResidue`/`QueryCleanupDebt`. | `TestRejectNeverCreatesVersionOrHead` (invisible to ordinary queries); `TestResidueAndDebtViewsAreContentFree`; `TestQueryUnionIsClosed`. | Decision 31. |
| Query supports exact version/member/history/head/operation; history ordered by committed stream revision, not timestamps or ID order (D32). | `query.go` history from stream ordinal. | `TestExactMemberAndVersionHistoryQueries`; `TestBehavioralGateHistoryAndHeadUseExplicitStreamFactsOnly`; `TestPostgresExactMemberAndHistoryQueries`. | Decision 32. |
| Locator-free opaque `ArtifactContentTarget` binds exact version/member/manifest digest/member facts/availability generation and short-term intent (D33). | `content_target.go`; `ArtifactContentTarget`. | `TestResolveContentTargetBindsExactFacts`; `TestContentTargetAndCapabilityLocatorFree`; `TestVerifyContentTargetFailsClosedOnTamper`; `TestPostgresContentTargetScopeGates`. | Decision 33; content-target report. |
| Identity & Ownership chooses exactly one owner/share/break-glass path; Sharing owns links/codes/sessions/expiry/rate limits; Durable Object opens bytes after mandatory audit; C05 never issues ReadHandle or unions grants (D34). | `content_target.go` scope resolution; `ContentScope` single-kind. | `TestResolveContentTargetCrossWorkspaceNonEnumerating`; `TestResolveContentTargetWrongScopeNonEnumerating`; `TestContentScopeUnionImpossible`; `TestResolveContentTargetStaleScopeGenerationFailsClosed`; `TestQueryNeverCreatesDurableObjectHandle`. | Decision 34; content-target report. |
| C04 manual-edit reconstruction uses C05-issued/verified exact Artifact Version capability; C05 cannot choose current/latest, C04 cannot choose recovery target (D35). | `content_target.go` `C04ReconstructionCapability`; issuance requires Platform authority + exact version. | `TestIssueC04ReconstructionCapabilityBindsExactFacts`; `TestIssueC04RequiresPlatformAuthority`; `TestIssueC04RequiresExactVersionNotLatest`; `TestC04CapabilityExpiryFailsClosed`; `TestVerifyC04ReconstructionCapabilityFailuresFailsClosed`. | Decision 35; content-target report. |

### Retention, residue, Cleanup Debt, audit, security (Decisions 36-43)

| Requirement | Code evidence | Test evidence | Decision/document evidence |
| --- | --- | --- | --- |
| ADR 0012 retention; no ordinary `DeleteArtifactVersion` in the mutation union (D36). | `intents.go` union has no delete-version intent; `TestMutationIntentUnionIsClosed`. | `TestMutationIntentUnionIsClosed`; `TestActivatedVersionImmutableAcrossLaterOperations`. | Decision 36; ADR 0012. |
| Durable `PublicationResidue` records unactivated staging/reference disposition, owner module, generation/fence, expiry, retry/reconciliation state, content-free estimate (D37). | `residue.go`; `postgres_residue.go`. | `TestResidueDurablyRecordedBeforePhysicalAction`; `TestResidueAndDebtViewsAreContentFree`; `TestPostgresResidueDurablyPersisted`; `TestResidueExpiryMarksButNeverGuessesClosure`. | Decision 37; residue report. |
| Durable Object uniquely owns object staging/physical reclamation and its Cleanup Debt; C05 emits stable abort/release intent and content-free evidence reference only (D38). | `residue_flow.go` release port; `ReleaseStaging` double; postgres residue release. | `TestReleaseResidueReleasesOnlyExactStagingReferences`; `TestReleaseResidueNeverCreatesVersionOrChangesHead`; `TestPostgresReleaseResidueEvidenceBacked`; `TestReleaseResidueOnlyForTerminalNonActivated`. | Decision 38; residue report. |
| C05 Cleanup Debt only for C05-owned assembly resources; single DebtID; obligation precedes attempt; pending operation/release is backlog not debt (D39). | `cleanup_debt.go`; `residue_flow.go` assembly; `postgres_residue.go` debt tables. | `TestStagingOnlyResidueKeepsBacklogWithoutDebtID`; `TestRecordResidueAssemblyMintsSingleDebtBeforePhysicalAttempt`; `TestDebtClaimRetryBackoffAndBlockers`; `TestPostgresCleanupDebtLifecycle`. | Decision 39; residue report. |
| Cleanup cannot delete authoritative request/decision/audit/evidence or activated member reference; Reclaimed/AlreadyAbsent/RetainedByAuthority/AcceptedException require exact current evidence; path/listing/log/metric cannot close debt (D40). | `cleanup_debt.go` resolution classes; evidence-backed closure. | `TestDebtResolvesOnlyOnEvidenceBackedClosure`; `TestDebtAcceptedExceptionRequiresAuditApproval`; `TestDebtStaleCleanupFailsClosed`; `TestDebtPathListingLogMetricCannotClose`; `TestReleaseResidueAlreadyAbsentIsEvidenceBacked`; `TestReleaseResidueAmbiguousStaysReconciliationRequired`. | Decision 40; residue report. |
| Protected decisions commit mandatory audit with the business fact in the same transaction; external sink/metrics/logs/trace failure never rolls back; durable rebuildable projection backlog (D41). | `mandatory_audit.go`; `postgres_observability.go`; `projection_backlog.go`. | `TestMandatoryAuditFactsForEveryProtectedDecision`; `TestMandatoryAuditFailureRollsBackProtectedDecision`; `TestExternalAuditSinkFailureNeverRollsBackAndRebuilds`; `TestPostgresProjectionBacklogRebuildProves`; `TestProjectionDuplicateOutOfOrderNeverChangesAuthority`. | Decision 41; audit/observability report. |
| Metrics use only registered bounded dimensions; no business/high-cardinality primary labels (D42). | `observability.go` registered metric registry; `MetricLabels` closed enums. | `TestMetricRegistryRejectsUnregisteredSamples`; `TestMetricSeriesUpperBoundIsBounded`; `TestPostCommitTelemetryProjectionBounded`; `TestUnknownNeverProjectedAsZero`; `TestTelemetryOutageNeverChangesAuthority`. | Decision 42; observability report. |
| Public/wire/error/audit/telemetry types closed, versioned, content-free, non-leaking (D43). | `types.go` safe-error taxonomy; `mandatory_audit.go` content-free facts; `observability.go` bounded surfaces. | `TestNonLeakageDecisionsAndViews`; `TestNonLeakageSafeErrors`; `TestCanonicalEncodingNeverContainsLocators`; `TestNonLeakageActivationEvidenceAndVersionViews`; `TestNonLeakageAuditTelemetryAndProjectionSurfaces`; `TestNonLeakageSafeErrorVersionedAndContentFree`; `TestMandatoryAuditFactsContentFree`; `TestTelemetrySnapshotProtectedReasonBound`. | Decision 43; non-leakage report. |

### Adapters and implementation boundary (Decisions 44-48)

| Requirement | Code evidence | Test evidence | Decision/document evidence |
| --- | --- | --- | --- |
| Deterministic in-memory implementation with controlled clock/IDs, fault injection, restartable state, evidence/receipt doubles, race scheduling (D44). | `in_memory.go`; `fixture_test.go` doubles; `residue_flow.go` controlled release; `activation_race_test.go` deterministic scheduling. | All in-memory contract/race/fault tests; `TestRestartableStateFullLifecycle`; `TestFaultMatrixCoversEveryBoundary`. | Decision 44. |
| Real PostgreSQL owned persistence with atomic request/operation journal, manifest/members/lineage, head CAS, restricted DO participant, mandatory audit, evidence/outbox (D45). | `postgres.go`, `postgres_mutate.go`, `postgres_query.go`, `postgres_participant.go`, `postgres_projection.go`, `postgres_residue.go`, `postgres_codec.go`. | `TestPostgresOwnedPersistenceTablesPopulated`; `TestPostgresActivationAllOrNoneOnFault`; `TestPostgresConcurrentActivationSingleWinner`; `TestPostgresResponseLossExactReplay`; `TestPostgresCrashBeforeCommitLeavesNoActiveVersion`; `TestPostgresRestartResumesAllFacts`; `TestPostgresDurableObjectParticipantFailureRollsBack`; `TestPostgresSafePersistenceErrors`. | Decision 45; postgres report. |
| Owned publication transport adapter: machine authorization, strict schema/canonical envelope, at-least-once, duplicate/out-of-order, deadline/ack loss, safe error normalization, Query semantics (D46). | `owned_transport.go`; `OwnedTransportHarness`. | `TestOwnedTransportAuthenticatesAndBindsCanonicalEnvelope`; `TestOwnedTransportFullPublicationLifecycle`; `TestOwnedTransportDuplicateExactDeliveryReturnsSameDecision`; `TestOwnedTransportSameOperationDifferentPayloadIntegrityConflict`; `TestOwnedTransportResponseLossRequiresReconciliationAndReplaysOriginal`; `TestOwnedTransportRedeliveryModesReuseTheExactOperation`; `TestOwnedTransportTimeoutDisconnectAndAmbiguityCategories`; `TestOwnedTransportWireErrorsPreserveSafeSemanticsWithoutRawFailure`; `TestOwnedTransportPostgresFullPublicationLifecycle`; `TestOwnedTransportPostgresRestartResponseLossReplay`. | Decision 46; transport report. |
| Task Orchestration integration extends only the publication adapter's exact C05 request/evidence binding; no `Decide`/phase-state-machine redesign (D47). | `taskorchestration/publication_bridge.go`; `PublicationTransportPort` narrow port. | `TestPublicationBridgeClaimDeliverInspectAndReconcile`; `TestPublicationBridgeIntegrityConflictAndPoisonedRejection`; `TestOwnedTransportBridgeDeliversThroughOwnedTransportToC05`; `TestOwnedTransportBridgePostgresDeliversThroughOwnedTransportToC05`. | Decision 47; bridge report. |
| Legacy TaskService/runtime publisher/manual-edit worker/repository/download routes are gap/prior-art/deletion-target evidence only; C05 never calls, wraps, or dual-writes them; no traffic switch or legacy deletion in this SPEC (D48). | Structural deletion gates below; no import of `service/handler/repository/model/router/config/database` in the C05 closure. | `TestStructuralDeletionGateLegacyPackagesAbsentFromBuildClosure`; `TestStructuralDeletionGateTargetBuildsWithoutLegacyPackages`; `TestStructuralDeletionGateCapabilitySurfaceAbsence`; `TestPublicationBridgeDeletionGateCapabilitySurfaceAbsence`; behavioral gates. | Decision 48; ADR 0016. |

## #103 Testing Decision evidence matrix

| Testing Decision | Evidence |
| --- | --- |
| Highest-level test seam is the public `Mutate/Query` boundary; no private SQL/worker/queue/path assertions. | All `internal/artifactpublication/*_test.go` drive `PublicationCore.Mutate/Query` only; `TestQueryUnionIsClosed`; `TestPublicSeamClosedSurfaces`; no test asserts SQL shape (fixtures count rows only as durability inspection). |
| Same shared black-box suite runs unconditionally on deterministic in-memory, real PostgreSQL, and owned transport-to-PostgreSQL. | In-memory: `lifecycle_contract_test.go`, `activation_contract_test.go`, `canonical_contract_test.go`, `manifest_contract_test.go`, `evidence_contract_test.go`, `idempotency_contract_test.go`, `content_target_contract_test.go`, `residue_lifecycle_test.go`, `cleanup_debt_test.go`. PostgreSQL: `postgres_contract_test.go` (mirrors every in-memory scenario), `postgres_integration_test.go`, `postgres_residue_test.go`, `postgres_observability_test.go`. Transport-to-PostgreSQL: `owned_transport_contract_test.go` + `owned_transport_postgres_test.go`; bridge: `owned_transport_bridge_test.go` + `owned_transport_bridge_postgres_test.go`. |
| Main scenarios: first generation prepare→verify exact evidence→activate→query exact/current/history/member; manual edit with exact parent→reconstructed child evidence→child activate→parent still queryable. | `TestFirstGenerationPrepareVerifyLifecycle`; `TestFirstGenerationActivationAdvancesStreamAtomically`; `TestExactMemberAndVersionHistoryQueries`; `TestManualEditChildActivationPreservesParent`; `TestManualEditChildRequiresExactParentAndExpectedHead`; `TestPostgresFirstGenerationPrepareVerifyLifecycle`; `TestPostgresTwoChildActivationRaceSingleWinner` (manual-edit parent preservation under PostgreSQL); `TestOwnedTransportFullPublicationLifecycle`; `TestOwnedTransportManualEditAndContentTargetThroughTransport`. |
| Manifest/lineage suite: empty/duplicate/extra members, kind/name/media normalization, sort stability, digest algorithm, size, unknown major/required field, different canonical encodings, parent mismatch, lineage evidence mismatch, locator injection, same bytes/different Artifact identities. | `TestManifestDeterministicBinding`; `TestManifestSortStability`; `TestLineageCommitsPinnedEvidenceReferences`; `TestLocatorFreeManifestAndLineage`; `TestUnicodeNameNormalization`; `TestDuplicateMembersFailClosed`; `TestUnsupportedKindAndMediaTypeFailClosed`; `TestCanonicalDigestDeterminism`; `TestUnknownMajorSchemaFailsClosed`; `TestMinorVersionRefinementAccepted`; `TestCanonicalEncodingFieldPresence`. |
| Evidence matrix: unauthorized producer, cross-Task/Workspace/Phase/operation, wrong Runtime output manifest, validation not binding Runtime, C04 not binding validation/Revision/Checkpoint, manual-edit export source mismatch, DO capability wrong purpose/domain/digest/size/generation, missing/partial/duplicate evidence. | `TestEvidenceMatrixFailClosed`; `TestManualEditExportLineageEvidenceFailsClosed`; `TestCapabilityNotCurrentRequiresReconciliation`; `TestUnregisteredCapabilityFailsClosed`; `TestRejectBindsExactEvidenceFailure`. |
| Idempotency suite: exact replay of every intent kind, replay after later stream revisions, same-key/different-payload, OperationID collision, EvidenceID/CapabilityID digest conflict, post-commit response loss. | `TestExactReplayReturnsOriginalDecision`; `TestSameKeyDifferentPayloadIntegrityConflict`; `TestOperationIdentityNeverReusedProves`; `TestFailedVerifyExactReplayReturnsSameOutcome`; `TestResponseLossReplayAfterLaterStreamWork`; `TestDuplicateActivationExactReplay`; `TestActivationReplayAfterLaterStreamRevision`; `TestPostgresResponseLossExactReplay`; `TestPostgresRestartAfterResponseLossExactReplay`; `TestOwnedTransportResponseLossRequiresReconciliationAndReplaysOriginal`. |
| Concurrency/race suite: duplicate prepare, verify vs cancel, activate vs cancel, two different children, two first-version attempts, query during activation, reference attach vs cleanup, same content concurrent prepare, stale generation/fence; single current-head winner, parent/member immutable. | `TestDuplicatePrepareRaceSingleWinner`; `TestVerifyCancelRaceDeterministicWinner`; `TestActivateCancelRaceDeterministicWinner`; `TestTwoChildActivationRaceSingleWinner`; `TestTwoFirstVersionActivationRaceSingleWinner`; `TestQueryDuringActivationObservesCommittedFactsOnly`; `TestQueryDuringMutationNeverMutates`; `TestStaleGenerationFenceActivationRaceFailsClosed`; `TestConcurrentDistinctOperationsIsolated`; `TestPostgresActivateCancelRaceDeterministicWinner`; `TestPostgresTwoChildActivationRaceSingleWinner`; `TestPostgresTwoFirstVersionActivationRaceSingleWinner`; `TestPostgresConcurrentActivationSingleWinner`; `TestPostgresConcurrentConflictingPreparesNoDeadlock`. |
| Fault injection: request journal, candidate persistence, DO prepare/receipt/verify, evidence acceptance, restricted attach, version/member/lineage/head/audit/evidence/outbox persistence stages, PostgreSQL commit before/after, response before/after, outbox send/ack, abort/release, residue/Cleanup Debt persistence. | `TestFaultMatrixCoversEveryBoundary`; `TestPrepareFaultBeforeJournalLeavesNoOperation`; `TestPrepareFaultBeforeResponseDurableReplay`; `TestActivationFaultBeforeCommitLeavesNoVersion`; `TestActivationFaultBeforeResponseDurableReplay`; `TestActivationFaultMatrixCoversEveryBoundary`; `TestActivationFaultRestartConsistency`; `TestReleaseFaultBeforeResponseReEvaluates`; `TestRecordAssemblyFaultBeforeResponseNeverDuplicatesDebt`; `TestResolveDebtFaultBeforeResponseReplay`; `TestPostgresActivationAllOrNoneOnFault`; `TestPostgresCrashBeforeCommitLeavesNoActiveVersion`; `TestPostgresDurableObjectParticipantFailureRollsBack`. |
| Real PostgreSQL integration: isolated schemas, all-or-none activation transaction, row-lock/CAS single winner, exact replay no identity reallocation, same-key conflict persistent, mandatory audit rollback, external projection non-rollback, restricted DO participant failure rollback, restart reconciliation, safe persistence errors. | `TestPostgresOwnedPersistenceTablesPopulated`; `TestPostgresActivationAllOrNoneOnFault`; `TestPostgresConcurrentActivationSingleWinner`; `TestPostgresResponseLossExactReplay`; `TestPostgresCrashBeforeCommitLeavesNoActiveVersion`; `TestPostgresRestartResumesAllFacts`; `TestPostgresDurableObjectParticipantFailureRollsBack`; `TestPostgresSafePersistenceErrors`; `TestPostgresCanonicalAuditFactsPersistedInTransaction`; `TestPostgresProjectionBacklogRebuildProves`; `TestPostgresCorruptRetainedAuditFailsClosedNotZero`; `TestPostgresRestartAfterResponseLossExactReplay`; `TestPostgresResidueRestartSafeResponseLoss`. |
| Cancellation/reconciliation suite: cancel-first rejects late activation, activation-first retains version, ambiguous DO/transport stays reconciliation-required, reconciler only uses original operation and never changes digest/parent/version. | `TestCancelFirstClosesLateActivation`; `TestActivationFirstCancelReturnsActiveTerminalResult`; `TestCancelFirstLinearizesLateVerifyStale`; `TestReconcileInspectAndCompleteVerification`; `TestReconcileConfirmTerminalReplaysOnlyOriginalOperation`; `TestRestartAmbiguousOperationRemainsReconcileable`; `TestPostgresReconcileAlwaysReevaluatesOriginalOperation`; `TestPostgresAmbiguousVerificationRequiresReconciliation`; `TestPostgresReleaseAmbiguousStaysReconciliationRequired`; `TestOwnedTransportResponseLossRequiresReconciliationAndReplaysOriginal`. |
| Retention/residue suite: activated references not released by execution cleanup or later versions; reject/cancel/expiry release only exact staging; DO physical debt vs C05 assembly debt not duplicated; re-reference/activation vs cleanup race fails closed. | `TestReleaseResidueOnlyForTerminalNonActivated`; `TestReleaseResidueReleasesOnlyExactStagingReferences`; `TestReleaseResidueStaleFenceFailsClosed`; `TestReleaseResidueNeverCreatesVersionOrChangesHead`; `TestStagingOnlyResidueKeepsBacklogWithoutDebtID`; `TestRecordResidueAssemblyMintsSingleDebtBeforePhysicalAttempt`; `TestDebtStaleCleanupFailsClosed`; `TestPostgresReleaseNeverTouchesActivatedMember`; `TestPostgresCleanupDebtLifecycle`. |
| Content-target contract: exact authorized scope resolves only its version/member; no ReadHandle; no path/object key/signed URL/bytes before authorization/audit; owner/share/break-glass not unioned; cross-Workspace and pre-verification non-enumerating. | `TestResolveContentTargetBindsExactFacts`; `TestResolveContentTargetRequiresActivatedVersion`; `TestResolveContentTargetCrossWorkspaceNonEnumerating`; `TestResolveContentTargetWrongScopeNonEnumerating`; `TestResolveContentTargetStaleScopeGenerationFailsClosed`; `TestContentScopeUnionImpossible`; `TestResolveContentTargetActiveContentDispositionFailsClosed`; `TestVerifyContentTargetFailsClosedOnTamper`; `TestContentTargetAndCapabilityLocatorFree`; `TestQueryNeverCreatesDurableObjectHandle`; `TestExactVersionMemberLookupScopeFailsClosed`; `TestPostgresContentTargetScopeGates`. |
| Task Orchestration adapter contract: complete canonical C05 request fixed at outbox-commit; adapter cannot re-write; evidence binds OperationID/generation/fence/epoch/version/manifest; C05 cannot advance Task; Task Orchestration cannot forge activation. | `TestPublicationBridgeCommitFixesCompleteCanonicalRequest`; `TestPublicationBridgeClaimDeliverInspectAndReconcile`; `TestPublicationBridgeIntegrityConflictAndPoisonedRejection`; `TestPublicationBridgeAdapterCannotAssembleRequestFields`; `TestPublicationBridgeCanonicalDigestIsStableAcrossDeliveries`; `TestPublicationBridgeManualEditBindingBindsExactParent`; `TestOwnedTransportBridgeCommitFixesCompleteCanonicalRequest`; `TestOwnedTransportBridgeDeliversThroughOwnedTransportToC05`; `TestOwnedTransportBridgeAmbiguityAndConflictProveNoSecondOperation`. |
| C04 reconstruction contract: C05 verifies only Task Orchestration-selected exact Artifact Version capability; C04 returns exact reconstruction/export evidence; manual-edit lineage binds source parent and new Revision/Checkpoint; neither selects latest nor leaks physical state. | `TestIssueC04ReconstructionCapabilityBindsExactFacts`; `TestIssueC04RequiresPlatformAuthority`; `TestIssueC04RequiresExactVersionNotLatest`; `TestVerifyC04ReconstructionCapabilityFailuresFailsClosed`; `TestC04CapabilityExpiryFailsClosed`; `TestPostgresC04CapabilityIssuanceAndVerification`; `TestManualEditExportLineageEvidenceFailsClosed`. |
| Non-leakage suite: inject content/member name/raw error/host path/object key/bucket/vendor URL/credential/cross-Workspace canaries; check public types, wire, PostgreSQL records, evidence, audit, logs, traces, metrics, diagnostics. | `TestNonLeakageDecisionsAndViews`; `TestNonLeakageSafeErrors`; `TestCanonicalEncodingNeverContainsLocators`; `TestNonLeakageActivationEvidenceAndVersionViews`; `TestTestOutputFreeOfLocators`; `TestNonLeakageAuditTelemetryAndProjectionSurfaces`; `TestNonLeakageSafeErrorVersionedAndContentFree`; `TestMandatoryAuditFactsContentFree`; `TestResidueAndDebtViewsAreContentFree`; `TestOwnedTransportNonLeakageAcrossWireTypes`; `TestPostgresSafePersistenceErrors`; `TestPostgresCorruptRetainedAuditFailsClosedNotZero`. |
| Bounded observability: metrics reject business/high-cardinality labels; unknown never projected as zero; telemetry duplicate/out-of-order never changes authority; protected diagnostics reason-bound and content-free. | `TestMetricRegistryRejectsUnregisteredSamples`; `TestMetricSeriesUpperBoundIsBounded`; `TestPostCommitTelemetryProjectionBounded`; `TestUnknownNeverProjectedAsZero`; `TestTelemetryOutageNeverChangesAuthority`; `TestTelemetrySnapshotProtectedReasonBound`; `TestProjectionDuplicateOutOfOrderNeverChangesAuthority`; `TestPostgresCanonicalAuditFactsPersistedInTransaction`; `TestPostgresProjectionBacklogRebuildProves`; `TestPostgresCorruptRetainedAuditFailsClosedNotZero`. |
| Structural deletion gates use allowlisted dependency/import graph and public/wire capability-surface inspection; prove C05 core, Task Orchestration bridge, owned adapter do not depend on legacy service/handler/raw storage/filesystem discovery, and do not expose path, object key, or legacy `PublishVersion` string authority; behavioral tests plant misleading timestamps, version strings, object-key prefixes, directory entries, "latest file"; result fully determined by explicit IDs/manifest/revision/head/evidence; gates verify capability absence, not fixed filename/method spelling/error text/source substring; ordinary renames cannot satisfy or break them. | `TestStructuralDeletionGateLegacyPackagesAbsentFromBuildClosure` (now also gates `./internal/taskorchestration`); `TestStructuralDeletionGateTargetBuildsWithoutLegacyPackages` (builds both targets); `TestStructuralDeletionGateCapabilitySurfaceAbsence` (C05 + owned transport + audit/observability + `PublishVersion`/`version_string`/directory-scan fragments); `TestPublicationBridgeDeletionGateCapabilitySurfaceAbsence` (bridge seams); `TestBehavioralGateMisleadingTimestampsDoNotDriveAuthority`; `TestBehavioralGateHistoryAndHeadUseExplicitStreamFactsOnly`; `TestBehavioralGateObjectKeyPrefixesAndLatestFactsDoNotDriveAuthority`; `TestBehavioralGatePublishVersionStringIsNotAuthority`. |
| Completion gates: focused C05 suite, three-way shared suite, real PostgreSQL integration, owned transport contract, Task Orchestration/C04/DO/authorization adapter contracts, fault matrix, race detector, security/non-leakage, full backend regression, vet, format, module drift, diff hygiene; completion report must declare implementation complete != migration/cutover/deployment. | Validation section below; this report. |

## #103 Acceptance Criteria coverage

| Acceptance criterion | Status | Evidence |
| --- | --- | --- |
| Module and authority (C05 independent, `Mutate` only; Query pure read-only; no active setter/general repository/raw callback/path/object-key API; worker/Runtime/C04/validator/DO/legacy cannot activate; Task Orchestration only consumes evidence) | Complete | `TestPublicSeamClosedSurfaces`; `TestMutationIntentUnionIsClosed`; `TestNoRepositoryOrActiveSetterCapability`; `TestActivationAuthorityBindingDeniesNonOrchestration`; `TestQueryIsPureReadOnly`; structural capability gates. |
| Identity, manifest, lineage (opaque non-reused IDs; locator-free deterministic manifest fail-closed; current head via explicit stream revision/head CAS; manual edit creates child of exact parent, parent immutable) | Complete | `TestRestartPreservesIdentityNonReuse`; `TestOperationIdentityNeverReusedProves`; `TestManifestDeterministicBinding`; `TestLocatorFreeManifestAndLineage`; `TestBehavioralGateHistoryAndHeadUseExplicitStreamFactsOnly`; `TestManualEditChildActivationPreservesParent`; `TestActivatedVersionImmutableAcrossLaterOperations`. |
| Evidence and atomic activation (Runtime/validation/C04/DO exact binding; activation in one real PostgreSQL transaction; no remote I/O in transaction; verified staging inaccessible pre-commit; evidence references committed version only) | Complete | `TestEvidenceMatrixFailClosed`; `TestPostgresActivationAllOrNoneOnFault`; `TestPostgresVerifiedUnattachedContentInaccessible`; `TestPostgresTypedReferenceOnlyAttachAndRelease`; `TestFirstGenerationActivationAdvancesStreamAtomically`. |
| Idempotency, failure, concurrency (exact replay returns original decision/version/revision/digest/evidence; same-key/different-payload conflict; response/claim loss, restart, duplicate never create a new version or change parent; cancel/activate, two-child, cleanup/reference races produce single deterministic winner and fence late work; reconcile only original operation) | Complete | `TestExactReplayReturnsOriginalDecision`; `TestSameKeyDifferentPayloadIntegrityConflict`; `TestPostgresResponseLossExactReplay`; `TestPostgresRestartAfterResponseLossExactReplay`; race suite; `TestReconcileInspectAndCompleteVerification`; `TestOwnedTransportRedeliveryModesReuseTheExactOperation`. |
| Query, authorization, retention (ordinary Query only activated immutable versions; history/current via explicit stream facts; read/download only exact locator-free content target; Identity/Sharing/audit/DO authority independent; activated versions retained, no ordinary deletion mutation; unactivated residue not queryable/shareable/downloadable/C04-reconstructable) | Complete | `TestExactMemberAndVersionHistoryQueries`; `TestResolveContentTargetBindsExactFacts`; `TestContentScopeUnionImpossible`; `TestQueryNeverCreatesDurableObjectHandle`; `TestActivatedVersionImmutableAcrossLaterOperations`; `TestMutationIntentUnionIsClosed` (no delete); `TestResidueAndDebtViewsAreContentFree`. |
| Cleanup, adapters, security (DO vs C05 Cleanup Debt ownership non-duplicated, one owner/DebtID; in-memory/PostgreSQL/owned transport same contract; PostgreSQL fault/race/audit/projection/restart gates; no content/path/object-key/locator/credential/cross-Workspace leakage; structural gates forbid path/object key/PublishVersion string/directory scan/latest-file inference) | Complete | `TestStagingOnlyResidueKeepsBacklogWithoutDebtID`; `TestRecordResidueAssemblyMintsSingleDebtBeforePhysicalAttempt`; three-way suites; `TestPostgresCanonicalAuditFactsPersistedInTransaction`; `TestPostgresCorruptRetainedAuditFailsClosedNotZero`; non-leakage suite; deletion gates (this work). |
| Completion boundary (existing backend tests, new focused/shared/PostgreSQL/race/security suites, vet, format all pass; completion report states module/adapter complete != legacy wiring/migration/cutover/deployment/production vendor; no production mutation/deletion/cutover/traffic enablement) | Complete | Validation section below; Completion boundary section; this report. |

## Child matrices (C05-01 .. C05-07)

### #104 / C05-01 (canonical core, complete operation lifecycle)

| Requirement | Code | Test |
| --- | --- | --- |
| Canonical publication core with complete prepare/verify/reject/cancel/reconcile lifecycle over the closed `Mutate`/`Query` seam. | `intents.go`, `lifecycle.go`, `state.go`, `manifest.go`, `in_memory.go`, `query.go`, `seam.go`. | `TestFirstGenerationPrepareVerifyLifecycle`; `TestRejectNeverCreatesVersionOrHead`; `TestCancelFirstLinearizesLateVerifyStale`; `TestReconcileInspectAndCompleteVerification`; `TestQueryIsPureReadOnly`. |
| Versioned deterministic canonical encoding, manifest/lineage, fail-closed validation. | `canonical_contract_test.go`, `manifest_contract_test.go`. | `TestUnknownMajorSchemaFailsClosed`; `TestManifestDeterministicBinding`; `TestLocatorFreeManifestAndLineage`. |
| Exact evidence verification with fail-closed matrix. | `evidence_contract_test.go`. | `TestEvidenceMatrixFailClosed`; `TestCapabilityNotCurrentRequiresReconciliation`. |

### #105 / C05-02 (atomic activation, manual-edit lineage)

| Requirement | Code | Test |
| --- | --- | --- |
| Atomic activation advances stream revision/head; single CAS winner. | `activation.go`. | `TestFirstGenerationActivationAdvancesStreamAtomically`; `TestActivationRejectsWrongExpectedRevisionHead`; `TestTwoFirstVersionActivationRaceSingleWinner`. |
| Manual-edit child lineage with exact parent; parent immutable and queryable. | `activation.go` child validation; `ValidatedExportEvidence`. | `TestManualEditChildActivationPreservesParent`; `TestManualEditChildRequiresExactParentAndExpectedHead`; `TestManualEditChildRequiresActivatedParentAtActivation`; `TestTwoChildActivationRaceSingleWinner`. |
| Cancel/activate and reject/activate terminal races. | `activation.go`. | `TestCancelFirstClosesLateActivation`; `TestActivationFirstCancelReturnsActiveTerminalResult`; `TestRejectAfterActivationIsTerminalConflict`. |

### #106 / C05-03 (query, content targets, C04 reconstruction)

| Requirement | Code | Test |
| --- | --- | --- |
| Exact version/member/history/current-head queries; history by stream ordinal. | `query.go`. | `TestExactMemberAndVersionHistoryQueries`; `TestBehavioralGateHistoryAndHeadUseExplicitStreamFactsOnly`. |
| Locator-free content target with exclusive owner/share/break-glass scope and non-enumeration. | `content_target.go`. | `TestResolveContentTargetBindsExactFacts`; `TestResolveContentTargetCrossWorkspaceNonEnumerating`; `TestContentScopeUnionImpossible`; `TestContentTargetAndCapabilityLocatorFree`. |
| C04 reconstruction capability: exact version only, Platform authority, expiry, tamper fail-closed. | `content_target.go`. | `TestIssueC04ReconstructionCapabilityBindsExactFacts`; `TestIssueC04RequiresPlatformAuthority`; `TestIssueC04RequiresExactVersionNotLatest`; `TestC04CapabilityExpiryFailsClosed`. |

### #107 / C05-04 (real PostgreSQL owned persistence, DO atomic adapter)

| Requirement | Code | Test |
| --- | --- | --- |
| Owned PostgreSQL schema + atomic activation transaction with restricted DO attach participant. | `postgres.go`, `postgres_mutate.go`, `postgres_query.go`, `postgres_participant.go`, `postgres_codec.go`. | `TestPostgresOwnedPersistenceTablesPopulated`; `TestPostgresActivationAllOrNoneOnFault`; `TestPostgresDurableObjectParticipantFailureRollsBack`; `TestPostgresTypedReferenceOnlyAttachAndRelease`. |
| Row-lock/CAS single winner; exact replay no identity reallocation; safe persistence errors. | `postgres_mutate.go`. | `TestPostgresConcurrentActivationSingleWinner`; `TestPostgresResponseLossExactReplay`; `TestPostgresSafePersistenceErrors`; `TestPostgresSameKeyDifferentPayloadIntegrityConflict`. |
| Real PostgreSQL shared black-box contract parity. | `postgres_contract_test.go`. | `TestPostgresFirstGenerationPrepareVerifyLifecycle`; `TestPostgresExactReplayReturnsOriginalDecision`; `TestPostgresExactMemberAndHistoryQueries`; `TestPostgresContentTargetScopeGates`; `TestPostgresC04CapabilityIssuanceAndVerification`. |

### #108 / C05-05 (restart-safe reconciliation, residue, Cleanup Debt)

| Requirement | Code | Test |
| --- | --- | --- |
| Restart-safe reconciliation; crash-before-commit leaves no active version; ambiguous operations remain reconcileable. | `postgres_mutate.go` reconciliation; `restart_contract_test.go`. | `TestRestartableStateFullLifecycle`; `TestCrashBeforeCommitAfterPrepareLeavesResiduePath`; `TestRestartAmbiguousOperationRemainsReconcileable`; `TestPostgresCrashBeforeCommitLeavesNoActiveVersion`; `TestPostgresRestartResumesAllFacts`. |
| Durable PublicationResidue; evidence-backed release; only exact staging references; never activated members. | `residue.go`, `postgres_residue.go`, `residue_flow.go`. | `TestResidueDurablyRecordedBeforePhysicalAction`; `TestReleaseResidueEvidenceBackedReleased`; `TestReleaseResidueReleasesOnlyExactStagingReferences`; `TestReleaseResidueOnlyForTerminalNonActivated`; `TestReleaseResidueNeverCreatesVersionOrChangesHead`; `TestPostgresResidueDurablyPersisted`; `TestPostgresReleaseNeverTouchesActivatedMember`. |
| C05-owned Cleanup Debt vs DO physical debt boundary; obligation-before-attempt; evidence-backed resolution; path/listing cannot close. | `cleanup_debt.go`, `postgres_residue.go`. | `TestStagingOnlyResidueKeepsBacklogWithoutDebtID`; `TestRecordResidueAssemblyMintsSingleDebtBeforePhysicalAttempt`; `TestDebtResolvesOnlyOnEvidenceBackedClosure`; `TestDebtPathListingLogMetricCannotClose`; `TestPostgresCleanupDebtLifecycle`. |

### #109 / C05-06 (owned transport, Task Orchestration bridge)

| Requirement | Code | Test |
| --- | --- | --- |
| Owned publication transport: machine auth, strict envelope, at-least-once, ack loss, safe errors, Query semantics. | `owned_transport.go`. | `TestOwnedTransportAuthenticatesAndBindsCanonicalEnvelope`; `TestOwnedTransportFullPublicationLifecycle`; `TestOwnedTransportDuplicateExactDeliveryReturnsSameDecision`; `TestOwnedTransportSameOperationDifferentPayloadIntegrityConflict`; `TestOwnedTransportResponseLossRequiresReconciliationAndReplaysOriginal`; `TestOwnedTransportWireErrorsPreserveSafeSemanticsWithoutRawFailure`; `TestOwnedTransportNonLeakageAcrossWireTypes`. |
| Owned transport to real PostgreSQL. | `owned_transport_postgres_test.go`. | `TestOwnedTransportPostgresFullPublicationLifecycle`; `TestOwnedTransportPostgresRestartResponseLossReplay`. |
| Task Orchestration bridge commits complete canonical request; adapter cannot assemble fields; inspect/reconcile by original OperationID. | `taskorchestration/publication_bridge.go`. | `TestPublicationBridgeCommitFixesCompleteCanonicalRequest`; `TestPublicationBridgeClaimDeliverInspectAndReconcile`; `TestPublicationBridgeIntegrityConflictAndPoisonedRejection`; `TestPublicationBridgeAdapterCannotAssembleRequestFields`; `TestPublicationBridgeManualEditBindingBindsExactParent`. |
| Bridge through owned transport to C05 (in-memory and PostgreSQL). | `owned_transport_bridge_test.go`, `owned_transport_bridge_postgres_test.go`. | `TestOwnedTransportBridgeCommitFixesCompleteCanonicalRequest`; `TestOwnedTransportBridgeDeliversThroughOwnedTransportToC05`; `TestOwnedTransportBridgeAmbiguityAndConflictProveNoSecondOperation`; `TestOwnedTransportBridgePostgresDeliversThroughOwnedTransportToC05`; `TestOwnedTransportBridgePostgresRestartResponseLossReplay`. |

### #111 / C05-07 (mandatory audit, bounded observability, safe errors, non-leakage)

| Requirement | Code | Test |
| --- | --- | --- |
| Canonical mandatory audit facts for every protected decision in the same transaction. | `mandatory_audit.go`, `postgres_observability.go`. | `TestMandatoryAuditFactsForEveryProtectedDecision`; `TestMandatoryAuditFailureRollsBackProtectedDecision`; `TestMandatoryAuditFactsContentFree`; `TestPostgresCanonicalAuditFactsPersistedInTransaction`. |
| Bounded content-free observability; external audit/telemetry projections; rebuildable backlog. | `observability.go`, `projection_backlog.go`, `postgres_projection.go`. | `TestMetricRegistryRejectsUnregisteredSamples`; `TestPostCommitTelemetryProjectionBounded`; `TestTelemetryOutageNeverChangesAuthority`; `TestUnknownNeverProjectedAsZero`; `TestExternalAuditSinkFailureNeverRollsBackAndRebuilds`; `TestProjectionRebuildRedeliversOnlyPendingFacts`; `TestPostgresProjectionBacklogRebuildProves`; `TestPostgresCorruptRetainedAuditFailsClosedNotZero`. |
| Versioned closed safe errors; full-surface non-leakage. | `types.go` `SafeErrorSchemaV1`; `diagnostics.go`. | `TestNonLeakageSafeErrors`; `TestNonLeakageSafeErrorVersionedAndContentFree`; `TestNonLeakageAuditTelemetryAndProjectionSurfaces`; `TestTelemetrySnapshotProtectedReasonBound`. |

## Structural deletion gate results (this work, C05-08)

The gates are structural: they inspect the actual Go build closure (`go list
-deps -test`) and the actual interface/value-type graph (`reflect`), not a
fixed filename, method spelling, error string, or source substring. An
ordinary rename of a helper, a different method spelling, or a different
error text cannot make a gate pass or fail; the gates verify capability
absence.

| Gate | Target | Result |
| --- | --- | --- |
| Legacy packages absent from build closure | `./internal/artifactpublication` AND `./internal/taskorchestration` (C05 core + owned adapter + Task Orchestration bridge) | PASS: every SlideSmith package in the transitive closure (including test deps) is an allowlisted owned port (artifactpublication, taskorchestration, taskworkspace, runtimeexecution, scheduler, testpostgres); no `service/handler/repository/model/router/config/database` dependency. |
| Targets build without legacy packages | `go build ./internal/artifactpublication ./internal/taskorchestration` | PASS. |
| Capability-surface absence (C05 + owned transport + audit/observability seams) | `PublicationCore`, `PublicationIntent`, decisions/views/errors, `ArtifactContentTarget`, `C04ReconstructionCapability`, owned transport wire types, audit/telemetry/projection types | PASS: no `path`/`object_key`/`prefix`/`bucket`/`mount`/`vendor`/`credential`/`session`/`signed_url`/`locator`/`latest`/`created_at`/`activated_at`/`timestamp`/`publish_version`/`version_string`/`directory_scan` field fragment; no `SetActive`/`Set`/`Update`/`Delete`/`Insert`/`Save`/`Create`/`Repo`/`Repository`/`MutateOther`/`Callback`/`Ingest` method fragment; no `os`/`os/exec`/`syscall` package reachable. |
| Capability-surface absence (Task Orchestration bridge) | `PublicationBridge`, `PublicationBridgeAdapter`, `PublicationTransportPort`, `PublicationRequestBinding`/Spec/MemberSpec/StagingReference/refs, `PublicationDelivery`, `PublicationBridgeError`, `DeterministicPublicationTransport` | PASS: `TestPublicationBridgeDeletionGateCapabilitySurfaceAbsence` (this work). |
| Behavioral: misleading timestamps never drive authority | in-memory authority | PASS: `TestBehavioralGateMisleadingTimestampsDoNotDriveAuthority`. |
| Behavioral: history/head use explicit stream facts only (version strings / lexical "latest" cannot win) | in-memory authority | PASS: `TestBehavioralGateHistoryAndHeadUseExplicitStreamFactsOnly`. |
| Behavioral: object-key prefixes, directory entries, "latest file" facts never drive authority and never leak | in-memory authority | PASS: `TestBehavioralGateObjectKeyPrefixesAndLatestFactsDoNotDriveAuthority` (this work). |
| Behavioral: `PublishVersion` string is not an identity/query authority | in-memory authority | PASS: `TestBehavioralGatePublishVersionStringIsNotAuthority` (this work). |

## Standards and Spec review

- **Public seam**: `TestPublicSeamClosedSurfaces`, `TestMutationIntentUnionIsClosed`,
  `TestNoRepositoryOrActiveSetterCapability`, `TestQueryIsPureReadOnly`. No
  active setter, general repository, raw callback, or path/object-key API.
- **Task Orchestration progression authority**: Task Orchestration consumes
  only typed publication evidence; C05 never advances a Task. `TestPublicationBridgeClaimDeliverInspectAndReconcile`;
  bridge outcome is evidence, not progression.
- **C04 reconstruction authority**: C05 issues exact-version capabilities only
  and never selects current/latest; C04 returns exact export evidence.
  `TestIssueC04RequiresExactVersionNotLatest`.
- **Security**: owner/share/break-glass mutually exclusive, non-enumerating,
  no locator leakage. `TestContentScopeUnionImpossible`;
  `TestResolveContentTargetCrossWorkspaceNonEnumerating`; non-leakage suite.
- **Observability**: bounded labels, no business identities in primary labels,
  unknown never projected as zero. `TestMetricRegistryRejectsUnregisteredSamples`;
  `TestUnknownNeverProjectedAsZero`.
- **Cleanup Debt ownership**: no duplicated physical obligation; single
  DebtID for C05-owned assembly. `TestStagingOnlyResidueKeepsBacklogWithoutDebtID`;
  `TestRecordResidueAssemblyMintsSingleDebtBeforePhysicalAttempt`.
- **Structural deletion**: gates above; the C05 closure and the Task
  Orchestration bridge closure contain no legacy package, and no seam exposes
  path/object-key/`PublishVersion`/directory-scan/latest-file capability.

## Remediation in this work

- Extended `TestStructuralDeletionGateLegacyPackagesAbsentFromBuildClosure`
  and `TestStructuralDeletionGateTargetBuildsWithoutLegacyPackages` to gate
  `./internal/taskorchestration` explicitly (the Task Orchestration bridge),
  and added `scheduler` to the owned-port allowlist (an owned Platform
  Control Plane deep module consumed by Task Orchestration).
- Extended the capability-surface gate fragments with `publish_version`,
  `version_string`, `directory_scan` (and spellings without underscores) so
  the legacy `PublishVersion` string authority and directory-scan capability
  classes are verified absent on every C05 seam.
- Added `TestPublicationBridgeDeletionGateCapabilitySurfaceAbsence` in
  `backend/internal/taskorchestration` to gate the bridge seams themselves.
- Added `TestBehavioralGateObjectKeyPrefixesAndLatestFactsDoNotDriveAuthority`
  and `TestBehavioralGatePublishVersionStringIsNotAuthority` behavioral
  gates: misleading object-key prefixes, directory entries, "latest file"
  facts, and `PublishVersion`-style strings never become manifest, identity,
  head, or query authority, and never leak.
- No production code change was required: the audit found no C05-scoped
  blocking gap beyond the gate coverage extension.

## Validation

All commands were run from `backend/`. PostgreSQL tests used the real server
at `postgres://postgres@127.0.0.1:54723/slidesmith_test?sslmode=disable`;
every test created and removed an isolated schema.

| Gate | Reproducible command | Result |
| --- | --- | --- |
| Focused C05 module | `go test ./internal/artifactpublication -count=1` | PASS |
| Focused C05 with PostgreSQL | `SLIDESMITH_TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:54723/slidesmith_test?sslmode=disable' go test ./internal/artifactpublication -count=1` | PASS |
| Task Orchestration (incl. bridge) | `go test ./internal/taskorchestration -count=1` | PASS |
| Task Orchestration with PostgreSQL | `SLIDESMITH_TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:54723/slidesmith_test?sslmode=disable' go test ./internal/taskorchestration -count=1` | PASS |
| Structural deletion gates (extended) | `go test ./internal/artifactpublication ./internal/taskorchestration -run 'TestStructuralDeletionGate|TestPublicationBridgeDeletionGate' -count=1 -v` | PASS (all) |
| Behavioral gates (extended) | `go test ./internal/artifactpublication -run 'TestBehavioralGate' -count=1 -v` | PASS (all four) |
| Race detector (focused C05 + bridge) | `go test -race ./internal/artifactpublication ./internal/taskorchestration -count=1` | PASS |
| Full backend regression with PostgreSQL | `SLIDESMITH_TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:54723/slidesmith_test?sslmode=disable' go test ./... -count=1` | PASS except the pre-existing load-sensitive `runtimeexecution` `TestPostgresConcurrentCapsuleGenerationReplaysExactContentAndRejectsIdentityRebinding` flake, which passes in isolation and has no dependency on this module (same documented flake as C05-05, C05-06, C05-07) |
| Full backend race with PostgreSQL | `SLIDESMITH_TEST_POSTGRES_DSN='postgres://postgres@127.0.0.1:54723/slidesmith_test?sslmode=disable' go test -race ./... -count=1` | PASS |
| Vet | `go vet ./...` | PASS |
| Format | `gofmt -l .` | PASS, no output |
| Module-file drift | `go mod tidy -diff` | PASS, no output |
| Diff hygiene | `git diff --check` | PASS, no output |

## Completion boundary

- C05 module/adapter implementation complete means the #103 contract is
  implemented and tested: the closed `Mutate`/`Query` authority, immutable
  locator-free Artifact Versions with explicit stream/head CAS, exact
  evidence binding, atomic real-PostgreSQL activation, idempotent replay,
  deterministic races, residue/Cleanup Debt boundaries, mandatory audit,
  bounded observability, owned transport, Task Orchestration bridge, and
  structural deletion gates.
- It does NOT mean: legacy record migration, ownership backfill, dual-write,
  read/write compatibility facade, hard cutover, `CommitCutover`, production
  data mutation, legacy code/data deletion, deployment, traffic switch, or
  vendor certification are complete. Those remain governed by the separately
  scoped migration/cutover plan (ADR 0016 / ADR 0028 / Legacy Business
  Migration and Compatibility).
- No production data mutation, legacy code/data deletion, hard cutover,
  traffic enablement, or deployment was performed in this work.

> #103 implementation complete does not mean legacy wiring, migration,
> dual-write, cutover, deployment, traffic switch, or vendor certification
> is complete.
