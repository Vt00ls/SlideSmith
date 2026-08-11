# Artifact Publication Mandatory Audit, Bounded Observability, Safe Errors & Non-Leakage Completion Report v7 (C05-07, child SPEC #111)

- Date: 2026-08-11
- Parent SPEC: [#103](https://github.com/Vt00ls/SlideSmith/issues/103) (C05 Artifact Publication deep module)
- Child SPEC: [#111](https://github.com/Vt00ls/SlideSmith/issues/111) (C05-07 audit, observability, safe errors, full-surface non-leakage)
- Base: `112e182` (origin/main after child SPEC #109 / C05-06 merge)

## Result

C05-07 is implemented and verified. The Artifact Publication deep module now
has a **canonical mandatory audit** contract, **bounded, content-free
observability** (metrics, structured logs, trace spans, post-commit external
audit and telemetry projections), a **versioned closed safe-error taxonomy**,
and **full-surface non-leakage** across audit, telemetry, wire, PostgreSQL
records, publication evidence and protected diagnostics. Publication
authority stays deterministic under audit, telemetry and transport failure:
mandatory audit is part of the protected decision's fail-closed transaction
(canonicalization/authorization/persistence failure rolls back the whole
protected decision), while external audit sink, metrics, logs and trace
failures never roll back a committed decision and form a durable,
rebuildable projection backlog from the retained authoritative facts.

New files in `backend/internal/artifactpublication/`:

- `mandatory_audit.go` — the canonical content-free `PublicationAuditFact`
  envelope (versioned schema/integrity version, opaque AuditFactID, canonical
  digest, owning module, closed intent action, typed authority, committed
  stream facts) shared by the deterministic in-memory authority and the real
  PostgreSQL adapter. Every protected decision (prepare, verify, activate,
  reject, cancel, reconcile, record residue assembly, release residue,
  resolve cleanup debt) commits one fact in the SAME transaction as the
  business fact; content-target resolution facts are correlatable through
  the binding facts of the activation audit and the locator-free content
  target (mandatory access audit itself stays owned by the content delivery
  flow, per the parent SPEC and the pure-read Query seam).
- `observability.go` — the closed bounded metric registry
  (`RegisteredMetricSample`, `MetricSeriesUpperBound`, cardinality-rejection
  counter) whose labels are strictly typed closed enums (operation family,
  state, outcome, member kind, adapter class, residue disposition,
  safe-error category); User/Workspace/Task/version/member/operation/
  evidence/digest/path/locator/TraceID/free-form values cannot even be
  expressed as labels. Structured log and trace span records carry closed
  allowlists. `PublicationTelemetryProjection` and `ExternalAuditProjection`
  are the bounded, content-free, post-commit projection envelopes; a
  `DeterministicTelemetry` adapter records them idempotently and exposes one
  protected reason-bound, exact-scope, bounded `Snapshot` surface.
- `projection_backlog.go` — the protected projection delivery backlog:
  `InspectProjectionBacklog` / `RebuildProjectionDelivery` on both the
  in-memory authority and the real PostgreSQL authority. Duplicate or
  out-of-order delivery is idempotent by AuditFactID and canonical digest; a
  corrupt or unknown retained fact fails closed and is never projected as
  delivered or as zero; rebuild redrives only not-yet-delivered facts from
  the retained canonical audit rows.
- `postgres_projection.go` — post-commit projection delivery for real
  PostgreSQL: after a protected decision commits, the authority
  best-effort delivers the content-free external audit copy and the bounded
  telemetry projection into the durable `publication_projection_backlog`
  table; sink failure never rolls back and keeps a rebuildable backlog.
- `diagnostics.go` — the reason-bound `AdministratorMetadataAuthority`
  (operations/integrity) plus the content-free diagnostic view and the
  fail-closed diagnostic access-audit seam.
- `types.go` — the safe error is now versioned (`SafeErrorSchemaV1` +
  `SchemaVersion()`), and evidence-binding failures map to the closed
  `SafeErrorBindingUnavailable` category, completing the closed taxonomy:
  authorization, invalid, integrity, stale/conflict, binding,
  durability-unverified, retryable-unavailable, resource-exhausted,
  reconciliation-required and not-found.

Modified: the in-memory authority (`in_memory.go`, `lifecycle.go`,
`activation.go`, `residue_flow.go`) records the canonical audit fact for
every protected decision and best-effort projects external audit +
telemetry through configurable sinks; the real PostgreSQL adapter
(`postgres.go`, `postgres_mutate.go`, `postgres_residue.go`) persists the
canonical audit fact (schema/integrity version, owning module, canonical
digest, request/lineage facts) in the same transaction, adds the
`publication_projection_backlog` table, and emits post-commit projections.

## Acceptance criteria coverage

### Mandatory audit facts (acceptance #1)

- `TestMandatoryAuditFactsForEveryProtectedDecision` proves prepare, verify,
  activate, reject, cancel, reconcile, record residue assembly, release
  residue and resolve cleanup debt each commit one canonical audit fact
  binding the exact operation identity, request digest, closed action, typed
  authority and committed stream facts.
- `TestMandatoryAuditFactsCorrelateExactOperation` proves end-to-end
  correlation: the same opaque operation identity and policy facts appear in
  the operation record, the activation evidence, and every audit fact; the
  prepare fact binds the operation's canonical request digest.
- `TestMandatoryAuditFactsSurviveRestart` proves the facts are authoritative
  and retained across restart; `TestPostgresCanonicalAuditFactsPersistedInTransaction`
  proves the same over real PostgreSQL (every row carries the canonical
  digest and versioned schema, and survives a fresh authority).
- Content-target resolution: the C05 Query seam is pure read-only by design
  (parent SPEC Decision 31/34; mandatory access audit is owned by the
  content delivery flow). The resolution is correlatable through the
  activation audit fact and the locator-free content target binding
  (version, member, manifest digest, scope kind/generation). This is
  documented as the applicable interpretation in the completion boundary.

### Audit failure rollback vs external projection non-rollback (acceptance #2)

- `TestMandatoryAuditFailureRollsBackProtectedDecision` (real PostgreSQL)
  injects the audit fault exactly before the activation audit row: the whole
  activation transaction rolls back (no version, no head advance, no extra
  audit row). `TestPostgresActivationAllOrNoneOnFault` (existing) covers the
  same all-or-none property.
- `TestExternalAuditSinkFailureNeverRollsBackAndRebuilds` proves an external
  audit outage after commit does not roll back the committed decision, forms
  a durable pending backlog, and the protected rebuild surface redelivers
  once the sink recovers. `TestTelemetryOutageNeverChangesAuthority` proves
  the same for telemetry, with exact replay returning the original decision
  under outage. `TestPostgresProjectionBacklogRebuildProves` proves the
  durable backlog and rebuild over real PostgreSQL.

### Safe errors (acceptance #3)

- `TestNonLeakageSafeErrorVersionedAndContentFree` proves the safe error is
  versioned (`SafeErrorSchemaV1`, `SchemaVersion()`), content-free (never
  echoes submitted identity/locator material), and that evidence-binding
  failures map to the closed `SafeErrorBindingUnavailable` category. The
  closed category set covers authorization, invalid, integrity, stale/
  conflict, binding, durability-unverified, retryable-unavailable,
  resource-exhausted, reconciliation-required and not-found.

### Bounded metrics (acceptance #4)

- `TestMetricRegistryRejectsUnregisteredSamples` proves the registry is
  closed: unregistered names, invalid label values, cross-policy label
  combinations and zero-count samples are rejected.
- `TestMetricSeriesUpperBoundIsBounded` proves the series budget is a fixed
  constant of the closed registry and cannot be enlarged by runtime
  identities.
- `TestPostCommitTelemetryProjectionBounded` proves the post-commit
  projections carry only closed enums plus allowlisted protected correlation
  and never a business identity label.

### Full-surface non-leakage (acceptance #5)

- `TestNonLeakageAuditTelemetryAndProjectionSurfaces` injects hostile
  content, member-name, path, session, mount, object-key, credential and
  vendor canaries into the submitted evidence and proves they never reach
  the canonical audit facts, the external audit projections, the protected
  telemetry snapshot, or the projection backlog, and that a cross-Workspace
  operation identity of another Workspace never appears.
- The existing `TestNonLeakageDecisionsAndViews`, `TestNonLeakageSafeErrors`,
  `TestCanonicalEncodingNeverContainsLocators`,
  `TestNonLeakageActivationEvidenceAndVersionViews`, `TestTestOutputFreeOfLocators`
  and the owned-transport non-leakage tests continue to pass unchanged.
- The structural deletion gate now walks the new audit/observability seams
  and proves no path/object-key/bucket/mount/vendor/credential/session/
  signed-url/locator/latest/timestamp capability exists anywhere in them.

### Non-enumerating behaviors (acceptance #6)

- `TestNonLeakageSafeErrors` (existing) and `TestNonLeakageSafeErrorVersionedAndContentFree`
  prove unauthorized, cross-Workspace and missing-target behaviors resolve to
  the same non-enumerating not-found/ownership-denied error without
  disclosing another Workspace's existence; content-target scope gates
  (existing `content_target_contract_test.go`) prove owner/share/break-glass
  pre-verification stays non-enumerating.

### Projection duplicate/out-of-order, telemetry outage, unknown and rebuild (acceptance #7)

- `TestProjectionDuplicateOutOfOrderNeverChangesAuthority` proves duplicate
  projection delivery is idempotent and never rewrites the source fact or
  the committed decision.
- `TestUnknownNeverProjectedAsZero` proves an unavailable source never emits
  a fabricated zero and a missing backlog fact is reported pending with the
  source count derived only from retained facts.
- `TestProjectionRebuildRedeliversOnlyPendingFacts` and
  `TestPostgresCorruptRetainedAuditFailsClosedNotZero` prove rebuild redrives
  only pending facts and a corrupt retained fact fails closed (never
  delivered, never zero, never silently repaired).
- `TestProjectionBacklogInspectionProtected` proves the protected inspection
  and rebuild surfaces require a valid reason-bound administrator metadata
  authority and a bounded exact scope.

### Public/protected seam tests (acceptance #8)

All of the above are exercised through the public `Mutate/Query` seam and the
protected `InspectProjectionBacklog` / `RebuildProjectionDelivery` /
`Snapshot` seams (not through log text, SQL shape, or vendor implementations),
and the same observability contract suite runs over the deterministic
in-memory authority and real PostgreSQL.

## Validation

- `go test ./internal/artifactpublication -count=1` with real PostgreSQL via
  `SLIDESMITH_TEST_POSTGRES_DSN`: all pass (166 focused tests, ~30 new
  top-level C05-07 tests covering the canonical audit contract, bounded
  metric registry, post-commit telemetry, protected snapshots, external audit
  backlog + rebuild over in-memory and PostgreSQL, corrupt-fact fail-closed,
  and the new non-leakage surfaces).
- `go test ./internal/artifactpublication -race -count=1`: race-clean.
- `go test ./internal/taskorchestration -count=1`: all pass.
- `go vet ./...` and `gofmt -l`: clean. `go mod tidy`: no module drift.
- Full backend regression against real PostgreSQL passes except the
  pre-existing load-sensitive `runtimeexecution`
  `TestPostgresConcurrentCapsuleGenerationReplaysExactContentAndRejectsIdentityRebinding`
  flake, which passes in isolation and has no dependency on this module
  (same documented flake as C05-05 and C05-06).

## Completion boundary

- C05-07 delivers the canonical mandatory audit, bounded observability,
  versioned safe errors and full-surface non-leakage for the Artifact
  Publication deep module. It does NOT deliver the parent SPEC shared
  acceptance/completion audit (#112), which is the next child SPEC.
- Content-target resolution access audit stays owned by the content delivery
  flow (parent SPEC Decision 34 and the pure-read Query seam contract);
  C05's resolution facts are correlatable through the activation audit fact
  and the locator-free content target binding. No C05 Query writes audit.
- No production data mutation, legacy deletion, hard cutover, traffic
  enablement, production Durable Object vendor wiring, or deployment was
  performed.
