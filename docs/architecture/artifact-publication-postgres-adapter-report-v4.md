# Artifact Publication Real PostgreSQL & Durable Object Atomic Adapter Completion Report v4 (C05-04, child SPEC #107)

- Date: 2026-08-11
- Parent SPEC: [#103](https://github.com/Vt00ls/SlideSmith/issues/103) (C05 Artifact Publication deep module)
- Child SPEC: [#107](https://github.com/Vt00ls/SlideSmith/issues/107) (C05-04 implement PostgreSQL and Durable Object atomic publication adapter)
- Base: `ea06b4b` (origin/main after child SPEC #106 / C05-03 merge)

## Result

C05-04 is implemented and verified. The Artifact Publication module now has a
production-shaped **real PostgreSQL owned persistence adapter**
(`PostgresAuthority`) behind the same closed public seam
(`Mutate(PublicationIntent) -> PublicationDecision`, `Query(PublicationQuery)
-> PublicationView`), together with a **restricted same-PostgreSQL Durable
Object participant** that attaches exact typed references during atomic
activation. The adapter owns the request/operation journal, candidate,
manifest, members, lineage, publication stream/head, verification evidence,
mandatory audit and Task Orchestration outbox; it never exposes SQL, a
general repository, a raw callback ingest, or a caller-controlled
transaction handle.

Atomic activation is the single business linearization point: one real
PostgreSQL transaction row-locks the publication stream and the original
operation, revalidates the OperationID, request digest, expected stream
revision/head, activity generation, publication fence and safety epoch, and
all-or-none commits the immutable Artifact Version, members, lineage, typed
Durable Object references (through the restricted participant), stream
revision/current head, terminal operation, mandatory audit, activation
evidence and outbox. No Durable Object call, network, filesystem or other
remote I/O happens inside the transaction.

The same invariant engine runs in the deterministic in-memory authority and
the PostgreSQL adapter: canonical digests, the closed evidence matrix
(`evaluateEvidence`), activation evidence construction, decision rendering
and query view rendering were extracted into shared pure functions so no
adapter can diverge on domain semantics. The shared black-box contract suite
(26 new top-level tests, 126 focused tests total) runs against the
PostgreSQL adapter;
the isolated-schema integration suite proves every acceptance criterion of
#107 (concurrent activation, same-key conflict, response loss, restart,
all-or-none commit, restricted participant failure, typed-reference-only
attach/release, safe persistence errors). Race-clean, vet-clean, fmt-clean,
full backend regression passing.

This report is about child SPEC #107 only. It does not claim that
restart-safe reconciliation residue/Cleanup Debt, audit/observability/safe-
error full surfaces, owned transport + Task Orchestration bridge, or the
parent SPEC completion audit are complete — those are the later child SPECs
#108-#112. It also does not claim any legacy migration, cutover, deployment,
or production traffic enablement.

## What C05-04 delivers

### Real PostgreSQL owned boundary persistence (acceptance #1, #3, #9)

- `PostgresAuthority` persists the request/operation journal, candidate,
  members, staging references, pinned runtime/validation/C04/capability
  evidence references, verification result and accepted evidence, immutable
  Artifact Version and members, explicit publication stream revision/head,
  identity non-reuse facts, typed Durable Object attach references, C05-owned
  residue, mandatory audit and Task Orchestration outbox in one owned schema
  (`postgres.go`, `postgres_codec.go`, `postgres_mutate.go`,
  `postgres_query.go`). No other package reads or writes these tables; the
  public seam exposes only `Mutate` and `Query`
  (`TestPostgresOwnedPersistenceTablesPopulated`).
- The migration DDL is idempotent and schema-qualified; the schema
  identifier is validated before any SQL is built
  (`TestPostgresSafePersistenceErrors`).
- Manifest/members/lineage digests are computed by the same shared canonical
  builders as the in-memory authority; the stored manifest facts never
  contain a path, object key, prefix, bucket, vendor, credential, or
  materialization locator (structural column gates,
  `TestPostgresTypedReferenceOnlyAttachAndRelease`).

### Row-lock/CAS activation transaction (acceptance #2, #4)

- Activation `SELECT ... FOR UPDATE`s the Task's stream row and the original
  operation row, then revalidates the OperationID, the stored request digest
  (which must equal the prepare outcome digest — the request journal is
  intact), the expected stream revision/head, the activity generation, the
  publication fence and the safety epoch; any mismatch fails closed with
  `ErrorStaleAuthority` and leaves nothing durable
  (`TestPostgresActivationRevalidatesFencedFacts`).
- The whole activation runs in one transaction with no remote I/O: the
  restricted Durable Object participant is a same-PostgreSQL participant and
  the outbox/audit/evidence writes are plain SQL in the same transaction.

### All-or-none commit (acceptance #3, #5, #6)

- A fault at any bounded point inside the activation transaction — before
  activation commit, before reference attach, before mandatory audit, before
  outbox, or before COMMIT — rolls the WHOLE transaction back: no active
  version, no activated members, no attach rows, no activation audit, no
  outbox, no stream advance; the exact retry commits the ORIGINAL candidate
  identity (`TestPostgresActivationAllOrNoneOnFault`).
- Crash-before-commit leaves no active version; crash-after-commit-before-
  response (fault after COMMIT) leaves the activation durable and the exact
  same intent replays the original decision with the same ArtifactVersionID
  and manifest digest, never reallocating identity
  (`TestPostgresResponseLossExactReplay`,
  `TestPostgresCrashBeforeCommitLeavesNoActiveVersion`).
- A brand-new authority over the same schema resumes every durable fact
  (stream, version, members, operation state, exact replay) and the
  lifecycle continues (manual-edit child) — including the combined
  restart-after-response-loss replay
  (`TestPostgresRestartResumesAllFacts`,
  `TestPostgresRestartAfterResponseLossExactReplay`).
- Restricted participant failure (capability no longer current, physical
  generation rotated) fails the attach and rolls back the whole activation —
  no half active version, no orphan membership, no readable unverified
  content (`TestPostgresDurableObjectParticipantFailureRollsBack`).

### Durable Object capability precise binding (acceptance #7, #8, #9)

- The closed evidence matrix (shared `evaluateEvidence`) binds every
  capability to the policy domain, purpose, ContentID, content digest, size,
  immutable write intent, physical generation, verification method, adapter
  identity and safety epoch, and requires current validity in the Durable
  Object authority registry at verification time.
- The restricted same-PostgreSQL participant revalidates every typed fact
  against the registry (with `FOR UPDATE`) and inserts the attach rows in the
  activation transaction; a ContentID or digest match alone never confers
  membership, ownership, or authorization.
- Verified-but-unattached content stays in inaccessible staging: ordinary
  queries, download targets and C04 reconstruction capabilities never
  resolve it; only the exact candidate query can inspect it
  (`TestPostgresVerifiedUnattachedContentInaccessible`).
- Attach and release accept only exact typed references: the attach, staging
  and residue tables structurally contain no path/object-key/prefix/bucket/
  vendor/URL/locator/signed-handle column, and reject/cancel release only the
  original operation's typed staging references while activated member
  references are never released (`TestPostgresTypedReferenceOnlyAttachAndRelease`,
  `TestPostgresRejectCancelReleaseOnlyExactOperationReferences`).

### Concurrency, idempotency and safe persistence errors (acceptance #10)

- Eight concurrent activations of the same verified operation commit exactly
  one version; every other caller either exact-replays the committed outcome
  or fails the stream CAS with the typed stale disposition
  (`TestPostgresConcurrentActivationSingleWinner`). Two different first-
  version candidates racing from the same empty stream produce exactly one
  committed winner and one stale loser (`TestPostgresTwoFirstVersionActivationRaceSingleWinner`);
  two manual-edit children from one parent produce exactly one current-head
  winner (`TestPostgresTwoChildActivationRaceSingleWinner`).
- Same-key/different-payload is a durable content-free integrity incident
  and a typed `ErrorIntegrityConflict`; the exact replay afterwards returns
  the original decision (`TestPostgresSameKeyDifferentPayloadIntegrityConflict`).
- Persistence failures surface as closed content-free safe errors with no
  SQL text, table names, DSNs, or raw chains; invalid schema identifiers,
  nil databases and closed connections all fail closed
  (`TestPostgresSafePersistenceErrors`).

### Shared contract parity with the in-memory authority

- The black-box contract suite runs over real PostgreSQL and proves
  identical public behavior through the same seam: first-generation
  prepare/verify lifecycle, atomic activation and evidence binding, exact
  replay, reject/cancel terminal semantics with residue, activate/cancel
  races, content-target scope gates, C04 capability issuance/verification,
  and ambiguous-verification reconciliation
  (`postgres_contract_test.go`).

## Structural review performed

- The public seam remains exactly `Mutate` + `Query`; the adapter exposes no
  repository, no SQL authority, no active setter, and no locator-bearing
  capability (structural deletion gates and public-surface gates pass
  unchanged).
- The PostgreSQL implementation depends only on the owned module, the
  standard library, and the pgx driver; no legacy service/handler/
  repository/model/router/config/database package is in the build closure.
- The in-memory engine and the PostgreSQL adapter share the same evidence
  matrix, activation evidence builder, decision renderer, and query view
  builders, so adapter parity is structural, not duplicated.

## Validation

- `go test ./... -count=1` (full backend regression, real PostgreSQL via
  `SLIDESMITH_TEST_POSTGRES_DSN`): all packages pass.
- `go test ./internal/artifactpublication -count=1 -race`: race-clean.
- `go vet ./...` and `gofmt -l`: clean.
- 126 focused tests (26 new), all green.

## Completion boundary

- C05-04 delivers the real PostgreSQL owned persistence adapter and the
  restricted Durable Object participant. It does NOT deliver: restart-safe
  reconciliation residue/Cleanup Debt boundaries (#108), owned transport +
  Task Orchestration publication bridge (#109/#110), full audit/
  observability/safe-errors/non-leakage surfaces (#111), or the parent SPEC
  shared acceptance/completion audit (#112).
- No production data mutation, legacy deletion, hard cutover, traffic
  enablement, or production Durable Object vendor wiring was performed.
