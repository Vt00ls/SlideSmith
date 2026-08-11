# Artifact Publication Activation Completion Report v2 (C05-02, child SPEC #105)

- Date: 2026-08-11
- Parent SPEC: [#103](https://github.com/Vt00ls/SlideSmith/issues/103) (C05 Artifact Publication deep module)
- Child SPEC: [#105](https://github.com/Vt00ls/SlideSmith/issues/105) (C05-02 atomic Artifact Version activation and manual-edit lineage)
- Base: `eb68ead` (origin/main after child SPEC #104 / C05-01 merge, PR #113)

## Result

The C05-02 atomic activation and manual-edit lineage activation are
implemented and verified through the same closed public seam. `activate`
joined the closed `PublicationIntent` union as the single business
linearization point: an exact canonical request that has been prepared and
verified can atomically commit an immutable Artifact Version (members,
manifest, lineage), advance the explicit publication-stream revision and
current head by CAS, mark the operation terminal-activated, and return
durable publication evidence bound to the OperationID, ArtifactVersionID,
manifest digest, Phase Run generation/fence, activity generation, and safety
epoch.

First-generation activation is only possible without a parent and when the
expected stream revision/head match; manual-edit child activation requires
the exact activated parent to be the expected current head and binds the C04
validated reconstruction/export source including the new Revision/Checkpoint.
Ordinary version queries (exact version, exact member, version history) only
expose activated immutable versions; prepared/verified/rejected/cancelled/
residue stay invisible to them. Parent and its members remain immutable,
retained, and independently queryable after a child activation. Concurrent
two-first-version, two-child, activate/cancel, query-during-activation, and
stale generation/fence races produce exactly one deterministic head winner;
the loser stays non-active with a typed stale/conflict disposition. Response
loss, restart, duplicate activation, claim loss, and replay after later
stream revisions never create a second Artifact Version or change lineage.
No authority other than Task Orchestration can activate a version or advance
a Task; C05 returns only publication evidence and never progresses a Task
itself.

The implementation remains the deterministic, restartable in-memory
authority that reuses the C05-01 invariant engine and public seam (28 new
tests; 84 focused tests total, race-clean, vet-clean, fmt-clean, full
backend regression passing).

This report is about child SPEC #105 (atomic activation + manual-edit
lineage) only. It does not claim that version content targets, PostgreSQL
owned-persistence, transport, audit/observability, or the Task Orchestration
bridge are complete — those are the later child SPECs #106-#112. It also
does not claim any legacy migration, cutover, deployment, or production
traffic enablement.

## What C05-02 delivers

### Activation linearization and evidence (acceptance #1, #3, #6, #9)

- `IntentActivatePublication` is a member of the closed `PublicationIntent`
  union (`TestMutationIntentUnionIsClosed`). Activation accepts only the
  exact Task Orchestration authority with the current operation generation
  and fence; Runtime, validator, C04, Durable Object, recovery, and
  unregistered authorities are rejected by construction and can never
  bypass `Mutate` to activate a version or advance a Task
  (`TestActivationAuthorityBindingDeniesNonOrchestration`).
- Activation requires a verified candidate; a prepared (unverified)
  operation fails closed and never creates a version or advances the stream
  (`TestActivationRequiresVerifiedCandidate`). Stale generation/fence and
  stale expected stream revision/head fail closed with a typed stale
  disposition and the candidate stays non-active
  (`TestActivationRejectsStaleGenerationFence`,
  `TestActivationRejectsWrongExpectedRevisionHead`).
- First-generation activation advances the stream from revision 0 to 1 and
  sets the explicit current-head pointer only when there is no parent and
  the expected revision/head match (`TestFirstGenerationActivationAdvancesStreamAtomically`).
- The returned `PublicationEvidence` is durable and canonically digestible,
  binding OperationID, request digest, ArtifactVersionID, manifest and
  lineage digests, publication kind, exact parent, committed stream
  revision and current head, Phase Run identity, publication
  generation/fence, activity generation, and safety epoch
  (`TestFirstGenerationActivationAdvancesStreamAtomically`).
- Activation-first linearization: a cancel submitted after the activation
  commits returns the existing active terminal result and never deletes the
  version or releases its references as residue; reject after activation is
  a terminal conflict (`TestActivationFirstCancelReturnsActiveTerminalResult`,
  `TestRejectAfterActivationIsTerminalConflict`). Cancel-first linearization
  closes the late activation (`TestCancelFirstClosesLateActivation`).

### Immutability and ordinary version queries (acceptance #2, #4, #5, #7)

- The committed Artifact Version is an immutable snapshot: members, manifest
  digest, and lineage digest are copied at the linearization point, later
  operations never modify or delete them, and non-activated candidates are
  never visible as versions (`TestActivatedVersionImmutableAcrossLaterOperations`).
- `QueryExactVersion`, `QueryExactMember`, and `QueryVersionHistory` resolve
  only activated versions. Before activation the candidate is invisible;
  after activation the exact version, its member, and the stream facts
  resolve (`TestFirstGenerationActivationAdvancesStreamAtomically`,
  `TestExactMemberAndVersionHistoryQueries`).
- Current head and history ordering use only the explicit committed stream
  revision and head pointer; a child recorded at an earlier diagnostic time
  still orders after its parent and never becomes the head by time or ID
  ordering (`TestBehavioralGateHistoryAndHeadUseExplicitStreamFactsOnly`).

### Manual-edit lineage activation (acceptance #6, #7)

- The manual-edit child is prepared against the exact activated parent,
  verified with the C04 validated reconstruction/export evidence, and
  activated only when the parent equals the expected current head at the
  linearization point (`TestManualEditChildActivationPreservesParent`).
- The export evidence must bind the same new Revision/Checkpoint the C04
  commit evidence binds; a mismatched revision/checkpoint fails closed
  (`TestManualEditExportLineageEvidenceFailsClosed` revision-mismatch case,
  `TestManualEditChildRequiresExactParentAndExpectedHead`).
- The parent and its members stay immutable, retained, and independently
  queryable; history is parent then child
  (`TestManualEditChildActivationPreservesParent`). A child pinned to a
  never-activated parent fails closed
  (`TestManualEditChildRequiresActivatedParentAtActivation`).

### Idempotency, failure, and concurrency (acceptance #8, #10, #12)

- Duplicate activation exact-replays the original committed decision with
  the same version identity, stream revision, digest, and evidence, and
  never creates a second version (`TestDuplicateActivationExactReplay`).
- Replay after a later stream revision and after restart returns the
  original activated decision (`TestActivationReplayAfterLaterStreamRevision`,
  `TestActivationSurvivesRestartAndReplay`).
- Fault injection at every activation boundary: crash before the commit
  leaves no version and the retry commits exactly one; crash after commit
  before response leaves the version durable and the retry exact-replays
  (`TestActivationFaultBeforeCommitLeavesNoVersion`,
  `TestActivationFaultBeforeResponseDurableReplay`,
  `TestActivationFaultMatrixCoversEveryBoundary`,
  `TestActivationFaultRestartConsistency`).
- Deterministic race scheduling proves exactly one winner for two
  first-version activations, two manual-edit child activations from the same
  parent, activate/cancel, and stale generation/fence; queries interleaved
  with an activation observe only committed facts
  (`TestTwoFirstVersionActivationRaceSingleWinner`,
  `TestTwoChildActivationRaceSingleWinner`,
  `TestActivateCancelRaceDeterministicWinner`,
  `TestQueryDuringActivationObservesCommittedFactsOnly`,
  `TestStaleGenerationFenceActivationRaceFailsClosed`).

### Non-leakage (acceptance from parent SPEC #103)

- Activation decisions, activation evidence, and version/member/history
  views never contain content, paths, object keys, buckets, vendors,
  credentials, sessions, or materialization locators even when hostile
  values are injected into the request
  (`TestNonLeakageActivationEvidenceAndVersionViews`).

## Implementation boundary

- The core module is `backend/internal/artifactpublication`. Activation
  (`activation.go`), the closed intent union (`intents.go`), the immutable
  activated-version state (`state.go`), the version queries (`query.go`),
  and the evidence binding (`lifecycle.go`) depend only on the Go standard
  library and `golang.org/x/text`; the structural deletion gates still prove
  by import-graph and capability inspection that no legacy package and no
  path/object-key/locator/repository/active-setter capability enters the
  closure.
- `activate` is the single linearization point: expected revision/head CAS,
  immutable version snapshot, explicit stream advance, terminal operation
  state, and activation evidence all commit under one authority lock; there
  is no remote I/O inside the transaction. The deterministic in-memory
  authority remains restartable and reusable by the later adapters.
- Ordinary version queries expose only activated immutable versions;
  prepared/verifying/rejected/cancelled/residue remain visible only through
  exact operation queries. Current head and history never derive from
  timestamps, ID/string ordering, row order, directories, or latest-file
  inference.

## Validation

All commands run from `backend/` on `go1.25.4`:

```text
go build ./...
go test ./... -count=1 -timeout 900s          # full backend regression: PASS
go test ./internal/artifactpublication/ -count=1 -race   # 84 focused tests, race-clean
go vet ./internal/artifactpublication/
gofmt -l internal/artifactpublication/          # empty
```

## Out of scope and follow-up

This report intentionally does not claim:

- locator-free content targets for read/download authorization and C04
  reconstruction input capabilities (child SPEC #106);
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

Implementation complete for #105 does not equal legacy wiring, migration,
cutover, deployment, or production Durable Object completion; no production
data mutation, legacy deletion, hard cutover, or traffic enablement was
performed.
