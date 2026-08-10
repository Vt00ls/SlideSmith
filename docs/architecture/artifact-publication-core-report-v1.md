# Artifact Publication Core Completion Report v1 (C05-01, child SPEC #104)

- Date: 2026-08-10
- Parent SPEC: [#103](https://github.com/Vt00ls/SlideSmith/issues/103) (C05 Artifact Publication deep module)
- Child SPEC: [#104](https://github.com/Vt00ls/SlideSmith/issues/104) (C05-01 canonical publication core and complete operation lifecycle)
- Base: `111d5d4694a5e472078d23a34c94f6fd0181a692` (origin/main at SPEC #103 fetch point)

## Result

The C05-01 canonical publication core and its complete operation lifecycle
are implemented and verified through the public seam. `Artifact Publication`
now exposes exactly one closed mutation seam, `Mutate(PublicationIntent) ->
PublicationDecision`, and exactly one pure read-only seam,
`Query(PublicationQuery) -> PublicationView`. The closed `PublicationIntent`
union covers `prepare`, `verify`, `reject`, `cancel`, and `reconcile`, and
every intent enters the same invariant engine. A canonical publication
request can prepare an immutable candidate, strictly verify exact Runtime
Evidence, Platform validation evidence, C04 commit evidence, and one Durable
Object verified-content capability per member, fail closed on any
missing/extra/partial/cross-scope/unknown evidence, be rejected safely,
cancelled by Task Orchestration or protected recovery authority, and
reconciled only against the original operation — with all retries always
referencing the original operation identity and canonical digest.

The implementation is a deterministic, restartable in-memory authority with
controlled clock, deterministic ID minting, registered authority doubles, a
Durable Object capability registry double, evidence doubles, fault injection
at every boundary, and deterministic race scheduling — all exercised through
the public seam contract (56 focused tests, race-clean, vet-clean, fmt-clean,
and the full backend regression passing).

This report is about the C05-01 canonical core only. It does not claim that
atomic Artifact Version activation, manual-edit lineage activation, version
queries, PostgreSQL/transport adapters, reconciliation residue disposal,
audit/observability, or the Task Orchestration bridge are complete — those
are the later child SPECs #105-#112. This conclusion also does not claim any
legacy migration, cutover, deployment, or production traffic enablement.

## What C05-01 delivers

### Authority and seam (acceptance #1, #2, #12)

- `backend/internal/artifactpublication` is the canonical core package.
  `PublicationCore` exposes only `Mutate` and `Query` (verified by
  `TestPublicSeamClosedSurfaces`, `TestMutationIntentUnionIsClosed`, and the
  structural capability-surface gate). There is no active setter, general
  repository, raw callback ingest, or caller-provided mutable snapshot.
- The `PublicationIntent` union is closed by an unexported marker and is
  limited to the five SPEC #104 intents
  (`TestMutationIntentUnionIsClosed`). Every intent goes through the same
  invariant engine (`Mutate`), which performs schema validation, canonical
  request digest binding, exact-replay lookup, and per-intent state machine
  enforcement.
- The deterministic in-memory authority (`NewInMemory`) supports controlled
  `Now` clocks, deterministic opaque ID minting, registered authority
  doubles, a Durable Object capability registry double
  (`CurrentContentCapability`), evidence doubles, a `FaultHook` with six
  bounded points, and a `ScheduleHook` for deterministic race scheduling.
  `InMemoryPersistence` is restartable: a new authority resumes the exact
  prior state (`TestRestartableStateFullLifecycle`,
  `TestRestartPreservesIdentityNonReuse`,
  `TestRestartAmbiguousOperationRemainsReconcileable`).

### Canonical request, manifest, and lineage (acceptance #3, #4, #5)

- Requests carry a versioned schema (`SchemaVersion` major/minor); unknown
  major versions fail closed (`TestUnknownMajorSchemaFailsClosed`), minor
  refinements are accepted (`TestMinorVersionRefinementAccepted`), and the
  canonical request digest is deterministic and schema-bound
  (`TestCanonicalDigestDeterminism`, `TestOperationDigestBindingRequired`).
- Field presence, member slot uniqueness, per-slot staging/capability
  consistency, integer/null and digest-algorithm rules are fixed
  (`TestCanonicalEncodingFieldPresence`, `TestDuplicateMembersFailClosed`,
  `TestUnsupportedKindAndMediaTypeFailClosed`). Unicode names are
  NFC-normalized and trimmed; empty, control-character, and path-like names
  fail closed (`TestUnicodeNameNormalization`).
- `ArtifactVersionID` and `ArtifactID` are opaque and never reused; identical
  bytes never merge business identity (`TestManifestDeterministicBinding`,
  `TestManifestSortStability`). Each Artifact belongs to exactly one
  candidate/version.
- The canonical manifest deterministically binds Task, ArtifactVersionID,
  publication kind, optional parent, lineage digest, and sorted members; the
  lineage commits the pinned upstream evidence references, generations,
  fence, and safety epoch (`TestLineageCommitsPinnedEvidenceReferences`).
  Manifest and lineage are locator-free (`TestLocatorFreeManifestAndLineage`,
  `TestCanonicalEncodingNeverContainsLocators`).
- A candidate is immutable after prepare; any member, parent, contract, or
  evidence-binding change requires a new request/operation (same key with a
  different payload is a durable integrity conflict).

### Evidence verification (acceptance #6)

- Verify accepts exact Runtime Evidence, Platform validation evidence, C04
  commit evidence, and one Durable Object verified-content capability per
  member, matching the evidence references pinned at prepare. Producer,
  scope (policy domain, Task, Phase Run, Task Workspace), operation, output
  proposal manifest facts, validation-to-runtime binding, C04-to-validation
  binding, policy domain, generation, fence, and safety epoch are checked
  item by item; missing, extra, partial, cross-scope, corrupted, duplicate,
  or unknown evidence fails closed (`TestEvidenceMatrixFailClosed`).
- A capability that is not currently resolvable in the Durable Object
  authority registry fails closed as durability-unverified and requires
  reconciliation, never a silent success or a determinate failure
  (`TestCapabilityNotCurrentRequiresReconciliation`,
  `TestUnregisteredCapabilityFailsClosed`).
- Manual-edit export lineage: the C04 commit evidence must bind the exact
  parent through validated export evidence; missing or wrong-source exports
  fail closed even under malformed pins
  (`TestManualEditExportLineageEvidenceFailsClosed`).

### Prepare, replay, reject, cancel, reconcile (acceptance #7, #8, #9, #10)

- Prepare persists the stable OperationID, canonical request digest,
  expected stream revision/head, candidate ArtifactVersionID, immutable
  manifest/lineage, and typed Durable Object staging references before any
  external physical action (`TestFirstGenerationPrepareVerifyLifecycle`).
- Exact replay takes priority over fresh-state validation and returns the
  original decision, candidate, and digest for every intent kind, including
  after later stream work and after restart
  (`TestExactReplayReturnsOriginalDecision`,
  `TestResponseLossReplayAfterLaterStreamWork`,
  `TestPrepareFaultBeforeResponseDurableReplay`). The same operation identity
  with a different canonical payload is a durable integrity conflict
  (`TestSameKeyDifferentPayloadIntegrityConflict`,
  `TestOperationIdentityNeverReusedProves`).
- Reject/cancel never create an Artifact Version, a member, or advance the
  current head, and release the typed staging references as C05-owned
  publication residue visible only through the exact operation query
  (`TestRejectNeverCreatesVersionOrHead`, `TestCancelFirstLinearizesLateVerifyStale`,
  `TestCrashBeforeCommitAfterPrepareLeavesResiduePath`). Late verify on a
  cancelled/rejected operation stays stale/terminal. Cancel requires the
  exact current generation/fence and the exact typed authority
  (`TestCancelRejectsStaleGenerationFence`,
  `TestRecoveryAuthorityCancelAndMisuseBinding`).
- Reconcile only inspects or replays the original operation, its evidence,
  and its references; it can complete a pending verification once the
  Durable Object capability becomes current and can confirm terminal
  dispositions, but cannot allocate a new ArtifactVersionID, modify the
  manifest or parent, or create a Task retry (`TestReconcileInspectAndCompleteVerification`,
  `TestReconcileConfirmTerminalReplaysOnlyOriginalOperation`).

### Concurrency, faults, and non-leakage (acceptance #11, #12)

- Deterministic race scheduling proves duplicate prepare, verify/cancel, and
  concurrent distinct operations each produce a single deterministic winner
  with stable candidate facts (`TestDuplicatePrepareRaceSingleWinner`,
  `TestVerifyCancelRaceDeterministicWinner`,
  `TestConcurrentDistinctOperationsIsolated`); queries interleaved with
  mutations never mutate state (`TestQueryDuringMutationNeverMutates`,
  `TestQueryIsPureReadOnly`).
- Fault injection at every prepare/verify boundary leaves either no trace
  (retry re-runs) or durable facts (retry exact-replays)
  (`TestFaultMatrixCoversEveryBoundary`,
  `TestPrepareFaultBeforeJournalLeavesNoOperation`).
- Public decisions, views, errors, and residue status never contain content,
  paths, object keys, buckets, vendors, credentials, sessions, or
  materialization locators (`TestNonLeakageDecisionsAndViews`,
  `TestNonLeakageSafeErrors`, `TestTestOutputFreeOfLocators`), and
  cross-workspace resolution is non-enumerating.

## Implementation boundary

- The core module is `backend/internal/artifactpublication` (types, manifest,
  lineage, evidence, intents, seam, state, deterministic in-memory engine,
  query). It depends only on the Go standard library and
  `golang.org/x/text` (Unicode normalization); the structural deletion gates
  prove by import-graph and capability inspection that no legacy package and
  no path/object-key/locator/repository/active-setter capability enters the
  closure (`TestStructuralDeletionGateLegacyPackagesAbsentFromBuildClosure`,
  `TestStructuralDeletionGateTargetBuildsWithoutLegacyPackages`,
  `TestStructuralDeletionGateCapabilitySurfaceAbsence`).
- Manual-edit prepare requires an exact activated parent matching the
  expected current head; until atomic activation exists (child SPEC #105) a
  manual-edit prepare fails closed rather than fabricating lineage
  (`TestManualEditWithoutActivatedParentFailsClosed`). The manual-edit
  evidence engine is fully enforced and tested at the unit level.
- Query exposes exact operation inspection for all states and immutable
  publication-safe metadata only for verified candidates
  (`TestRejectNeverCreatesVersionOrHead`); prepared/verifying/rejected/
  cancelled/residue are invisible to ordinary queries.

## Validation

All commands run from `backend/` on `go1.25.4`:

```text
go build ./...
go test ./... -count=1 -timeout 900s          # full backend regression: PASS
go test ./internal/artifactpublication/ -count=1 -race   # 56 focused tests, race-clean
go vet ./internal/artifactpublication/
gofmt -l internal/artifactpublication/          # empty
```

## Out of scope and follow-up

This report intentionally does not claim:

- atomic activation of an Artifact Version or manual-edit lineage activation
  (child SPEC #105);
- version history / current-head / member queries over activated versions and
  locator-free content targets (child SPEC #106);
- real PostgreSQL owned-persistence and Durable Object atomic publication
  adapter (child SPEC #107);
- restart-safe reconciliation residue and Cleanup Debt boundary (child
  SPEC #108);
- owned transport and Task Orchestration publication bridge (child
  SPEC #109);
- audit, observability, safe errors, and full-surface non-leakage
  (child SPEC #111);
- shared acceptance, structural deletion gates across the module, and parent
  SPEC completion audit (child SPEC #112);
- legacy migration, cutover, deployment, or production traffic enablement.

The next unblocked frontier is child SPEC #105 (atomic activation), which
reuses this package's invariant engine and public seam.
