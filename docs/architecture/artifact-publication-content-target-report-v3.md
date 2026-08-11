# Artifact Publication Content Target & C04 Reconstruction Completion Report v3 (C05-03, child SPEC #106)

- Date: 2026-08-11
- Parent SPEC: [#103](https://github.com/Vt00ls/SlideSmith/issues/103) (C05 Artifact Publication deep module)
- Child SPEC: [#106](https://github.com/Vt00ls/SlideSmith/issues/106) (C05-03 delivery version queries, authorized content target and C04 reconstruction contract)
- Base: `db60b9a` (origin/main after child SPEC #105 / C05-02 merge, PR #114)

## Result

C05-03 is implemented and verified through the same closed public seam.
Activated Artifact Versions can now be resolved into a locator-free opaque
`ArtifactContentTarget` for authorized content delivery, and C05 issues and
verifies an exact `C04ReconstructionCapability` for C04 manual-edit
reconstruction — always inside exactly one typed owner/share-link/break-glass
scope that Identity & Ownership and Sharing have already selected. C05 never
creates a principal, Share token, Access Code, Verification Session, or
implicit administrator content authority, and scopes can never union.

Four new pure read-only query kinds joined the closed `PublicationQuery`
union (`resolve_content_target`, `verify_content_target`,
`issue_c04_reconstruction_capability`,
`verify_c04_reconstruction_capability`). The content target binds the exact
ArtifactVersionID, ArtifactID, manifest and member digests, byte size,
registered media type, safe logical name, availability generation, and one
short-term intent; it never contains or produces a path, object key, bucket,
vendor URL, signed URL, credential, materialization locator, ReadHandle, or
bytes. Owner/share-link/break-glass scopes are mutually exclusive by
construction (a single query field) and by behavior (a target resolved under
one authority path never verifies under another). Exact version/member
lookup fails closed non-enumerating under a wrong Personal Workspace, Task,
or authority scope. Mandatory access audit and authorization remain with the
content delivery flow before any Durable Object open; C05's queries never
create a Durable Object read handle. The C04 reconstruction capability binds
the publication authority, policy domain, Task, exact ArtifactVersionID,
manifest digest, availability generation, and a Platform-declared expiry;
C05 issues it only for the exact version the Platform (Task Orchestration
authority) selected, C04 can only verify it, and no query kind ever resolves
a current/latest version or lets C04 choose a publication target. Unactivated
candidates and residue never resolve as targets or capabilities.

The implementation remains the deterministic, restartable in-memory
authority that reuses the C05-01/C05-02 invariant engine and public seam (16
new tests; 100 focused tests total, race-clean, vet-clean, fmt-clean, full
backend regression passing).

This report is about child SPEC #106 only. It does not claim that real
PostgreSQL owned-persistence, owned transport, restart-safe reconciliation
residue/Cleanup Debt, audit/observability/safe-errors, or the Task
Orchestration bridge are complete — those are the later child SPECs
#107-#112. It also does not claim any legacy migration, cutover, deployment,
or production traffic enablement.

## What C05-03 delivers

### Authorized locator-free content target (acceptance #5, #6, #8)

- `QueryResolveContentTarget` resolves one exact member of one activated
  Artifact Version into `ArtifactContentTarget` under exactly one typed
  scope and one short-term intent. The target binds the exact
  ArtifactVersionID, ArtifactID, manifest digest, member content digest,
  size, registered media type, safe logical name, the availability
  generation of the authorized scope, and the intent (`download` or
  `share_link_delivery`) (`TestResolveContentTargetBindsExactFacts`).
- The target is canonical-digestible and locator-free: it never contains or
  produces a path, object key, bucket, vendor URL, signed URL, credential,
  materialization locator, ReadHandle, or bytes, and a path-like logical
  name is rejected at prepare so a locator can never enter a member fact
  (`TestContentTargetAndCapabilityLocatorFree`, structural capability
  surface gates).
- `QueryVerifyContentTarget` re-validates a presented target against the
  current immutable version facts and the current availability fact of the
  presented scope; tampering with any binding fact fails closed as an
  integrity conflict (`TestVerifyContentTargetFailsClosedOnTamper`).
- C05's content-target queries are pure read-only: they never consult the
  Durable Object authority, never create a read handle, and never change
  version facts; mandatory access audit and authorization stay with the
  content delivery flow before any Durable Object open
  (`TestQueryNeverCreatesDurableObjectHandle`).

### Mutually exclusive owner/share-link/break-glass scopes (acceptance #7, #4, #12)

- `PublicationQuery` carries exactly one `ContentScope` field, so a union of
  owner/share-link/break-glass scopes is structurally impossible; a target
  resolved under one authority path verifies only under the same path, and a
  re-signed scope-kind swap fails closed (`TestContentScopeUnionImpossible`).
- The scope is the current availability fact of the exact version, resolved
  through the narrow black-box port `CurrentContentScope` (an Identity &
  Ownership / Sharing double). Wrong, unknown, revoked, or stale/rotated
  scopes fail closed with the same non-enumerating not-found error as a
  nonexistent version, and cross-workspace lookups never disclose identities
  (`TestResolveContentTargetWrongScopeNonEnumerating`,
  `TestResolveContentTargetCrossWorkspaceNonEnumerating`).
- A rotated/revoked availability generation makes every later resolution and
  verification of the old epoch fail closed (`TestResolveContentTargetStaleScopeGenerationFailsClosed`).
- Exact version/member lookup that presents an authority scope enforces the
  same gate; a wrong scope resolves to the identical non-enumerating error
  (`TestExactVersionMemberLookupScopeFailsClosed`). The historical
  scope-less ordinary lookup contract (C05-02) remains unchanged.

### C04 reconstruction capability contract (acceptance #9, #10, #11)

- `QueryIssueC04ReconstructionCapability` issues the exact Artifact Version
  input capability only for the exact ArtifactVersionID selected by the
  Platform (Task Orchestration authority) under exactly one typed scope with
  a future expiry. The capability binds the publication authority (C05's own
  authority identity), policy domain, Task, exact ArtifactVersionID, manifest
  digest, availability generation, and expiry
  (`TestIssueC04ReconstructionCapabilityBindsExactFacts`).
- C04 itself can never request issuance: only the Platform authority issues,
  and an empty identity, an unactivated candidate, or a cross-workspace
  identity fails closed; no query kind accepts a current/latest marker
  (`TestIssueC04RequiresPlatformAuthority`,
  `TestIssueC04RequiresExactVersionNotLatest`).
- `QueryVerifyC04ReconstructionCapability` re-derives the current facts:
  tampered digests, a wrong publication authority binding, an expired
  capability, a stale availability generation, and a revoked scope all fail
  closed (`TestVerifyC04ReconstructionCapabilityFailuresFailsClosed`,
  `TestC04CapabilityExpiryFailsClosed`).

### Delivery safety and non-enumeration (acceptance #12, #6)

- An unsafe active-content disposition (HTML/SVG members) fails closed for
  content delivery targets while safe attachments (PPTX) still resolve
  (`TestResolveContentTargetActiveContentDispositionFailsClosed`).
- Prepared, verified-but-not-activated, rejected, and cancelled candidates
  (including residue) never resolve as content targets or C04 capabilities
  (`TestResolveContentTargetRequiresActivatedVersion`,
  `TestIssueC04RequiresExactVersionNotLatest`).
- The query union is closed: exactly the ten SPEC kinds exist and unknown
  kinds (including any "latest" marker) fail closed
  (`TestQueryUnionIsClosed`).

## Implementation boundary

- The core module is `backend/internal/artifactpublication`. The new types
  (`ArtifactContentTarget`, `C04ReconstructionCapability`, `ContentScope`,
  `ContentScopeKey`, `ContentIntent`, `ContentDisposition`), the four query
  handlers, and the scope gate depend only on the Go standard library; the
  structural deletion gates still prove by import-graph and capability
  inspection that no legacy package and no path/object-key/locator/
  repository/active-setter capability enters the closure.
- `CurrentContentScope` is the narrow black-box port to Identity & Ownership
  / Sharing, exactly like the existing `CurrentContentCapability` port to
  the Durable Object authority: C05 binds and compares the scope's
  availability generation but never owns principal, grant, token, Access
  Code, Verification Session, expiry, revocation, or rate-limit mechanics.
- Scope failures are non-enumerating `not_found` errors identical to a
  missing identity; malformed queries (empty identities, invalid intents,
  already-expired issuance) are `invalid_intent`; tampering is an integrity
  conflict; expiry is stale-authority. Content targets and capabilities are
  deterministic functions of immutable version facts plus the current
  availability fact, so response-loss replay re-issues the same facts; the
  availability generation is the revocation fence.

## Validation

All commands run from `backend/` on `go1.25.4`:

```text
go build ./...
go test ./... -count=1 -timeout 900s          # full backend regression: PASS
go test ./internal/artifactpublication/ -count=1 -race   # 100 focused tests, race-clean
go vet ./internal/artifactpublication/
gofmt -l internal/artifactpublication/          # empty
```

## Out of scope and follow-up

This report intentionally does not claim:

- real PostgreSQL owned-persistence and Durable Object atomic publication
  adapter (child SPEC #107);
- restart-safe reconciliation residue and Cleanup Debt boundary (child
  SPEC #108);
- owned transport and Task Orchestration publication bridge (child
  SPEC #109);
- audit, observability, safe errors, and full-surface non-leakage (child
  SPEC #111);
- shared acceptance, structural deletion gates across the module, and parent
  SPEC completion audit (child SPEC #112);
- Share Link token/code/session/expiry/revocation/rate-limit mechanics, which
  remain with Sharing; mandatory access audit and authorization, which remain
  with the content delivery flow; Durable Object `Open`, which happens only
  after authorization and audit;
- legacy migration, cutover, deployment, or production traffic enablement.

Implementation complete for #106 does not equal legacy wiring, migration,
cutover, deployment, or production Durable Object completion; no production
data mutation, legacy deletion, hard cutover, or traffic enablement was
performed.
