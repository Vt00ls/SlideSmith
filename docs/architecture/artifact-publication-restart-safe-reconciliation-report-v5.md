# Artifact Publication Restart-Safe Reconciliation, Residue & Cleanup Debt Completion Report v5 (C05-05, child SPEC #108)

- Date: 2026-08-11
- Parent SPEC: [#103](https://github.com/Vt00ls/SlideSmith/issues/103) (C05 Artifact Publication deep module)
- Child SPEC: [#108](https://github.com/Vt00ls/SlideSmith/issues/108) (C05-05 implement restart-safe reconciliation, residue and Cleanup Debt boundaries)
- Base: `bcdc0f8` (origin/main after child SPEC #107 / C05-04 merge)

## Result

C05-05 is implemented and verified. The Artifact Publication module now has a
durable **PublicationResidue** lifecycle and a **restart-safe reconciliation
flow** that covers crash, response loss, ambiguous Durable Object receipt,
expiry, reject, cancel and release failure, together with the explicit
**C05 / Durable Object Cleanup Debt ownership boundary**. The same closed
`Mutate(PublicationIntent) -> PublicationDecision` seam and the pure
read-only `Query(PublicationQuery) -> PublicationView` seam are extended —
nothing new is exposed outside the module.

- Residue is durably recorded at the terminal non-activation disposition
  (reject/cancel) BEFORE any physical action, with the owner, the opaque
  typed staging references, the operation, generation/fence, expiry, retry
  state and the release disposition. It carries the optional opaque
  C05-owned publication assembly resource and, only when C05 actually
  created a physical assembly resource, exactly one C05-owned Cleanup Debt
  (DebtID); a staging-only residue keeps the reconciliation backlog and
  never duplicates a DebtID.
- Residue release runs through a restricted Durable Object release
  participant (same-PostgreSQL default; a narrow black-box port for the
  deterministic in-memory authority) that re-verifies every exact typed
  reference against the current Durable Object registry, verifies no
  activated member reference (attach row) exists, and returns an
  evidence-backed receipt. Only `released`/`already_absent` receipts close
  the residue; `ambiguous` keeps it release-requested and
  reconciliation-required and is never guessed as success, failure, zero
  bytes or already absent; `blocked` keeps it open with blocker classes.
  Activated member references are never touched by cleanup.
- Reconcile gained `ReconcileCompleteRelease`: it re-evaluates the ORIGINAL
  operation's residue against the current registry, never allocates a new
  ArtifactVersionID and never changes the manifest, parent or head. Release
  and release-reconciliation always re-evaluate (they never replay a frozen
  snapshot), so claim loss, callback/ack loss, response loss and
  duplicate/out-of-order receipts return to the original OperationID and
  the exact typed references.
- C05-owned Cleanup Debts support claim, retry, exponential backoff,
  blocker and safe error; the obligation is persisted before the first
  physical attempt; cleanup re-verifies resource identity, generation,
  reference, lease/grace/incident (blockers) and fence, and stale cleanup
  fails closed. A debt closes only on evidence-backed
  `Reclaimed`/`AlreadyAbsent`/`RetainedByAuthority` (evidence produced by
  the registered Durable Object authority and bound to the exact resource
  identity/generation/fence) or an audited `AcceptedException` (approval
  reference + future expiry + mandatory audit fact). Path disappearance,
  empty directories, object listings, logs, metrics and operator assertions
  can never close a debt or prove capacity reclaimed.
- Controlled-clock/restart/fault tests cover residue persistence,
  reconciliation, release response/claim loss, duplicate/out-of-order
  release, re-reference/activation collision, and evidence-backed debt
  resolution, over both the deterministic in-memory authority and real
  PostgreSQL (isolated schema, same DSN harness as C05-04).

## What C05-05 delivers

### Durable PublicationResidue recorded before any physical action (acceptance #1, #4)

- `residueRecord` is extended with `expiry`, `disposition`
  (pending/release-requested/released/already-absent/retained-by-authority/
  blocked/expired), `requiresReconciliation`, attempt/backoff/claim facts
  (`attemptCount`, `consecutiveFailures`, `nextRetryAt`, `claimGeneration`,
  `claimFence`, `lastErrorCategory`), the evidence-backed `releaseReceipt`,
  the optional C05-owned `assembly` reference and the `debtID`.
  (`residue.go`, `state.go`, `seam.go`.)
- Reject/cancel create the residue in the same transaction as the terminal
  disposition — before any physical release — with the owner, the exact
  typed staging references, generation/fence, the retention expiry and the
  pending disposition (`lifecycle.go`, `postgres_mutate.go`). Expiry marks
  only that the retention window passed; it never guesses absence and the
  release obligation continues until an evidence-backed receipt
  (`effectiveDisposition`, `TestResidueExpiryMarksButNeverGuessesClosure`).
- The PostgreSQL DDL is extended idempotently (new columns on
  `publication_residue`, new `publication_cleanup_debt` and
  `publication_do_release` tables, `released` flag on
  `publication_do_capability`, plus `ALTER TABLE ... ADD COLUMN IF NOT
  EXISTS` for existing schemas) (`postgres.go`).

### Restart-safe release and release reconciliation (acceptance #2, #3, #10)

- `ReleaseResidue` (protected publication cleanup authority) re-verifies
  the operation identity, residue generation/fence, expiry and every exact
  typed reference against the current Durable Object registry, claims the
  C05-owned debt (when present), calls the restricted release participant,
  records the evidence-backed receipt, and transitions the residue. A stale
  generation/fence fails closed before any physical action; an ambiguous
  receipt keeps release-requested + reconciliation-required; a duplicate or
  delayed delivery of an already-closed residue is an idempotent replay
  (`residue_flow.go`, `postgres_residue.go`).
- `ReconcileCompleteRelease` re-evaluates the original operation's residue
  against the current registry and never creates a new ArtifactVersionID or
  changes manifest/parent/head (`lifecycle.go`, `postgres_mutate.go`).
- Fault tests prove crash-after-physical-action-before-response re-evaluates
  to the same evidence-backed closure, and record-assembly/resolve-debt
  crash-before-response exact-replays without duplicating the DebtID or
  re-resolving (`residue_fault_test.go`).

### Evidence-backed release receipts; ambiguous never guessed (acceptance #3, #8)

- `ReleaseReceipt` is a digest-bound, producer-bound receipt issued by the
  registered Durable Object authority; `validateReleaseReceipt` is the pure
  evidence gate. `applyReleaseReceipt` maps only evidence-backed outcomes to
  closed dispositions (`residue.go`).
- The default same-PostgreSQL release participant re-verifies the exact
  typed references against `publication_do_capability` (locked), fails
  closed on mismatch/rotation, returns `ambiguous` when a capability is not
  currently resolvable (current=false), returns `already_absent` only when
  the Durable Object authority attests every exact reference is absent from
  its registry, and returns `released` only after marking the capabilities
  released and writing the release evidence row in the same transaction
  (`postgres_participant.go`).

### Cleanup Debt ownership boundary (acceptance #4, #5, #6, #7, #8, #9)

- Staging-only residue keeps the reconciliation backlog with no DebtID;
  `RecordResidueAssembly` (protected cleanup authority) durably mints
  exactly ONE C05-owned Cleanup Debt per operation/resource before the first
  physical attempt, bound to the opaque assembly reference, identity digest,
  generation and fence. A different assembly reference under the same
  operation is a durable integrity conflict; a crash before response never
  duplicates the DebtID (`residue_flow.go`, `postgres_residue.go`,
  `TestStagingOnlyResidueKeepsBacklogWithoutDebtID`,
  `TestRecordResidueAssemblyMintsSingleDebtBeforePhysicalAttempt`).
- The debt lifecycle supports claim (status open/claimed), retry/backoff
  (exponential `nextRetryAt`), blockers (lease/reference/incident/grace/
  quarantine classes) and safe error categories, all content-free
  (`residue.go`, `TestDebtClaimRetryBackoffAndBlockers`).
- Cleanup re-verifies resource identity, generation, reference and fence
  before any physical action; stale cleanup fails closed
  (`TestDebtStaleCleanupFailsClosed`, `TestReleaseResidueStaleFenceFailsClosed`).
- `ResolveCleanupDebt` accepts exactly one closure: evidence-backed
  `Reclaimed`/`AlreadyAbsent`/`RetainedByAuthority` (producer = registered
  Durable Object authority, canonical digest, exact resource identity/
  generation/fence binding) or audited `AcceptedException` (approval
  reference + future expiry + mandatory audit fact). No other mutation
  surface exists, so path disappearance, empty directories, object listings,
  logs, metrics and operator assertions can never close a debt
  (`TestDebtResolvesOnlyOnEvidenceBackedClosure`,
  `TestDebtAcceptedExceptionRequiresAuditApproval`,
  `TestDebtPathListingLogMetricCannotClose`).

### Adapter parity and owned persistence

- The residue/debt transitions (`validateReleaseReceipt`, `applyReleaseReceipt`,
  `recordResidueAttempt`, `recordDebtAttempt`, `validateCleanupResolutionEvidence`,
  `effectiveDisposition`, `residueView`, `debtView`) are shared pure
  functions; the deterministic in-memory authority and the real PostgreSQL
  adapter run exactly the same semantics. The PostgreSQL adapter owns the
  residue, debt, release-evidence and audit persistence; it never exposes
  SQL, a repository, or a caller-controlled transaction handle.
- The protected publication cleanup authority can only
  record-assembly/release/resolve/reconcile-release; it can never
  prepare/verify/activate/reject/cancel a publication or mutate a candidate
  (`ensureAuthority` on both adapters).

## Structural review performed

- The public seam remains exactly `Mutate` + `Query`; the residue/debt
  lifecycle enters the same closed intent union and the same pure read-only
  query union. No repository, no SQL authority, no active setter, and no
  locator-bearing capability is exposed.
- The release participant can only release exact typed staging references of
  one residue; it cannot list objects, infer a publication from a path/
  prefix/bucket/vendor, or perform remote I/O.
- Residue and debt inspection views expose only opaque identities and closed
  facts; hostile content, member names, evidence payloads, paths, object
  keys, buckets, vendors, credentials and sessions never appear (canary
  test `TestResidueAndDebtViewsAreContentFree`; existing structural
  deletion/public-surface/non-leakage gates pass unchanged).

## Validation

- `go test ./internal/artifactpublication -count=1` with real PostgreSQL
  via `SLIDESMITH_TEST_POSTGRES_DSN`: all pass (128 focused tests, ~24 new
  top-level C05-05 tests covering residue persistence, release evidence,
  ambiguous reconciliation, expiry, assembly debt, claim/retry/backoff/
  blockers, evidence-backed resolution, audited exceptions, stale fail-closed,
  restart, response loss, re-reference/activation collision, and content-free
  views).
- `go test ./internal/artifactpublication -count=1 -race`: race-clean.
- `go vet ./...` and `gofmt -l`: clean.
- Full backend regression against real PostgreSQL passes (the pre-existing
  `runtimeexecution` concurrent-capsule test is a load-sensitive flake that
  passes in isolation and has no dependency on this module).

## Completion boundary

- C05-05 delivers the durable residue lifecycle, restart-safe release
  reconciliation and the C05/Durable Object Cleanup Debt ownership boundary
  over the deterministic in-memory authority and the real PostgreSQL owned
  persistence adapter. It does NOT deliver: owned transport + Task
  Orchestration publication bridge (#109), full audit/observability/
  safe-errors/non-leakage surfaces (#111), or the parent SPEC shared
  acceptance/completion audit (#112).
- The C05-owned assembly obligation is testable through the protected
  `RecordResidueAssembly` intent; the current adapters create no physical
  assembly resource, so the ordinary staging-only path keeps a reconciliation
  backlog with no DebtID, exactly as the SPEC requires.
- No production data mutation, legacy deletion, hard cutover, traffic
  enablement, or production Durable Object vendor wiring was performed.
