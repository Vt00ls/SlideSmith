# Artifact Publication Owned Transport & Task Orchestration Bridge Completion Report v6 (C05-06, child SPEC #109)

- Date: 2026-08-11
- Parent SPEC: [#103](https://github.com/Vt00ls/SlideSmith/issues/103) (C05 Artifact Publication deep module)
- Child SPEC: [#109](https://github.com/Vt00ls/SlideSmith/issues/109) (C05-06 owned transport + Task Orchestration publication bridge)
- Base: `d722366` (origin/main after child SPEC #108 / C05-05 merge)

## Result

C05-06 is implemented and verified. The Artifact Publication module now has
an **owned publication transport** (`OwnedTransportClient` /
`OwnedTransportHarness` in `backend/internal/artifactpublication/owned_transport.go`)
that carries the COMPLETE canonical C05 request and the original machine
authority between the Task Orchestration committed publication outbox and
the C05 closed `Mutate/Query` seam, and the **Task Orchestration publication
bridge** (`PublicationRequestBinding`, `PublicationBridge`,
`PublicationBridgeAdapter`, `DeterministicPublicationTransport` in
`backend/internal/taskorchestration/publication_bridge.go`) that fixes the
full canonical C05 request at outbox-commit time and delivers it through the
owned transport without ever re-assembling a request.

- The transport envelope binds strict schema/version
  (`slidesmith.artifact-publication.owned-transport/v1`), machine
  authorization (machine authority identity + generation + expiry), the
  exact OperationID, the business canonical request digest, the payload
  digest, the deadline, and the applicable generation/fence/safety epoch.
  Delivery attempts never change the business digest: the envelope digest is
  the intent's `CanonicalRequestDigest`, and the harness verifies the
  reconstructed intent's digest, header fields, policy domain, Task and
  OperationID against the envelope before any dispatch.
- At-least-once delivery: a binding store retains the first envelope per
  scope/method/OperationID/canonical-digest key. Exact duplicate delivery
  replays the same decision through the C05 operation journal (no second
  publication operation, Artifact Version, or stream revision);
  same-OperationID/different-canonical-request fails closed as an integrity
  conflict on both the client and the receiving harness.
- Timeout, disconnect, claim loss, response/acknowledgement loss and
  duplicate/out-of-order callbacks all return a typed
  reconciliation-required result; inspection and reconciliation always
  return to the ORIGINAL OperationID (operation-scoped queries keep the
  original OperationID on the wire) and preserve the original digest,
  parent, evidence references and candidate identity.
- The receiving harness dispatches ONLY through the closed `Mutate/Query`
  seam (it reconstructs the typed `PublicationIntent` through the public
  constructors and verifies the canonical digest); there is no active
  setter, general repository, raw callback ingest, or storage
  path/object-key capability. Wire errors are normalized to the closed C05
  safe-error surface and never leak content, paths, sessions, locators,
  vendors, credentials, or the existence of another Personal Workspace.
- The Task Orchestration bridge commits the complete canonical request —
  Task, Phase Run, operation, activity generation, expected stream
  revision/head, parent, contract, evidence references and
  generation/fence/safety-epoch bindings — as an immutable
  `PublicationRequestBinding` at outbox-commit time; the adapter passes the
  committed binding verbatim (validated by digest) and can never assemble,
  overwrite or re-bind request fields at delivery, callback, test or
  reconciliation time. Task Orchestration keeps deciding Phase Run and Task
  progression from the typed activation/rejection evidence C05 returns; the
  bridge never advances a Task.
- The same black-box transport contract runs over the deterministic
  in-memory authority and the production-shaped real PostgreSQL adapter,
  including restart/response-loss replay with the persisted binding and
  deadline.

## What C05-06 delivers

### Owned publication transport (acceptance #2, #3, #4, #5, #8)

- `OwnedTransportEnvelope` is the strict, versioned wire envelope. The
  payload is the canonical serialization of the typed `PublicationIntent`
  (or `PublicationQuery`); the harness reconstructs the intent through the
  constructors and verifies `CanonicalRequestDigest(intent) ==
  envelope.CanonicalRequestDigest == header.Operation.RequestDigest`, plus
  policy domain, Task, OperationID, generation, fence and safety epoch. The
  wire can never be a second authority (`verifyMutationEnvelope`,
  `verifyQueryEnvelope`).
- `OwnedTransportBindingStore` (in-memory, first-wins) retains the first
  envelope per scope/method/OperationID/canonical-digest key. Client and
  harness share the store, so an adapter restart can never extend the
  deadline or re-bind a request; `bindEnvelope` additionally rejects a
  same-key delivery whose payload bytes or deadline differ
  (`sameOwnedTransportBindingIdentity` includes the payload digest).
- `OwnedTransportHarness` authenticates the machine authority (HMAC +
  generation + expiry), validates the canonical payload round-trip
  (non-canonical wire fails closed), dispatches through the closed seam,
  and records deliveries/responses/callbacks/signals. Fault injection covers
  duplicate delivery, out-of-order delivery, queue claim loss, response
  loss, timeout after delivery, callback replay, non-canonical payload,
  unsafe dependency failure, forged response/callback/error payload and
  unknown wire error.
- `OwnedTransportAcknowledgementAmbiguity` (and `context.DeadlineExceeded`)
  map to the typed `ErrorReconciliationRequired`; disconnects map to
  `ErrorRetryableUnavailable`; forged/unknown wire maps to
  `ErrorIntegrityConflict`; unsafe dependency failures normalize to the
  closed retryable-unavailable safe category without raw detail.
- The structural deletion gate now walks the transport seams
  (`OwnedTransportEnvelope`, `OwnedTransportRequest/Response/Callback`,
  `OwnedTransportWireError`, `OwnedTransportMachineAuthority`,
  `OwnedTransportBinding`, `OwnedTransportClient`) and proves no
  path/object-key/bucket/mount/vendor/credential/session/locator/latest/
  timestamp capability exists anywhere in them
  (`TestStructuralDeletionGateCapabilitySurfaceAbsence`).

### Transport contract tests (acceptance #2, #3, #4, #5, #8, #9, #10)

- `owned_transport_contract_test.go` (deterministic in-memory): envelope
  binding (schema/version, machine auth, OperationID, canonical digest,
  payload digest, deadline, generation/fence/safety epoch; delivery
  attempts never change the business digest); the full prepare → verify →
  activate → query lifecycle through the transport with activation evidence
  binding the exact OperationID, Phase Run generation/fence, activity
  generation, safety epoch, ArtifactVersionID and manifest digest; duplicate
  exact delivery returning the same decision with no second version or
  stream revision; same-OperationID/different-payload integrity conflict on
  both client and harness; response loss → reconciliation-required with
  inspect/replay of the ORIGINAL OperationID and no second version;
  out-of-order/claim-loss/callback-replay redelivery always reusing the
  exact operation; adapter-restart deadline persistence; timeout/disconnect/
  round-trip-deadline ambiguity categories; canonicalization/forgery/unknown
  wire fail-closed matrix; unsafe-failure non-leakage; authorization denial
  and stale-fence categories; unauthenticated-machine denial without
  dispatch; manual-edit child lineage and locator-free content target
  through the transport; and raw-downstream-failure non-leakage across
  requests/responses/callbacks/signals/errors.
- `owned_transport_postgres_test.go`: the identical contract over real
  PostgreSQL — full lifecycle, duplicate exact delivery with no second
  version, and restart/response-loss replay over the same schema with the
  persisted binding, preserving the committed candidate identity and
  evidence.

### Task Orchestration publication bridge (acceptance #1, #4, #5, #7)

- `PublicationRequestBinding` is the COMPLETE canonical C05 request fixed at
  outbox-commit time. `CanonicalBytes()`/`CanonicalDigest()` deterministically
  encode Task, Phase Run, operation, activity generation, expected stream
  revision/head, parent, contract, members, staging references, required
  channels, evidence references, capability references and
  generation/fence/safety-epoch bindings; `Valid()` fails closed on any
  missing, mismatched, or cross-kind binding (first generation has no
  parent; manual edit requires the exact parent; member/staging/capability
  slots must agree; unknown kinds/channels/purposes fail closed).
- `PublicationBridge.Commit` fixes the binding and its canonical digest at
  outbox-commit time (an immutable deep copy); the same binding
  exact-replays, a different binding under the same OperationID is a
  durable integrity conflict. `Claim`/`Deliver`/`Inspect`/`Reconcile` move
  the committed request through the owned transport, always returning to
  the ORIGINAL OperationID and never creating a new Artifact Version,
  parent, head or Task retry.
- `PublicationBridgeAdapter` validates the committed binding shape and
  passes it verbatim to the `PublicationTransportPort`; it exposes no field
  or method that could assemble, overwrite or re-bind parent, manifest,
  prerequisite or authority facts.
- `DeterministicPublicationTransport` is the journaling transport double
  proving at-least-once (exact duplicate → same outcome flagged Duplicate),
  same-OperationID/different-request → integrity conflict, and
  inspection of never-delivered operations → unknown.
- `publication_bridge_test.go` covers complete-request commit fixing,
  claim/deliver/inspect/reconcile, integrity conflict and poisoned
  rejection, adapter non-assembly, canonical-digest stability across
  deliveries, and manual-edit exact-parent binding.

### Cross-module wiring (acceptance #1, #6, #7)

- `owned_transport_bridge_test.go` (and `..._postgres_test.go`) is the only
  place the two isolated deep modules meet: a wiring adapter implements the
  Task Orchestration `PublicationTransportPort` over the C05 owned transport
  client. The tests prove the committed binding reconstructs into the C05
  `PreparePublication` intent with the SAME canonical digest as the original
  C05 request (the outbox OperationID/payload digest equals the C05
  request's exact identity/digest), delivery reaches the C05 seam with the
  exact OperationID and digest, activation evidence binds the exact
  OperationID/Phase Run generation/fence/activity generation/safety epoch/
  ArtifactVersionID/manifest digest, response loss reconciles by the
  original OperationID, and a conflicting binding never creates a second
  operation or Artifact Version — over the deterministic in-memory authority
  and real PostgreSQL, including authority+adapter restart replay.

## Structural review performed

- The public seam remains exactly `Mutate` + `Query`; the owned transport is
  a delivery layer over that seam and reconstructs typed intents through the
  public constructors. No repository, no SQL authority, no active setter,
  and no locator-bearing capability is exposed (public-surface and
  structural deletion gates pass unchanged; the transport seams were added
  to the capability-surface absence walk).
- The Task Orchestration bridge exposes only the committed binding, the
  closed delivery outcomes and the narrow `PublicationTransportPort`; the
  adapter has no field that can assemble a request. `PublicationRequestSpec`
  and the binding never carry a path, object key, bucket, mount, vendor,
  credential or locator.
- Canary tests prove hostile content, member names, raw errors, paths,
  sessions, locators, credentials and foreign-workspace facts never reach
  the wire types, callbacks, signals or the public error.

## Validation

- `go test ./internal/artifactpublication -count=1` with real PostgreSQL via
  `SLIDESMITH_TEST_POSTGRES_DSN`: all pass (161 focused tests, ~34 new
  top-level C05-06 tests covering the transport envelope, duplicate/out-of-
  order/claim-loss/response-loss/callback-replay matrix, canonicalization/
  forgery/unknown-wire, adapter-restart deadline persistence, non-leakage,
  the in-memory + PostgreSQL transport contracts with restart/response-loss
  replay, and the in-memory + PostgreSQL Task Orchestration bridge wiring).
- `go test ./internal/taskorchestration -count=1`: all pass (bridge contract
  suite, 6 new top-level tests; full taskorchestration regression with real
  PostgreSQL passes).
- `go test ./internal/artifactpublication ./internal/taskorchestration
  -race -count=1`: race-clean.
- `go vet ./...` and `gofmt -l`: clean.
- Full backend regression against real PostgreSQL passes except the
  pre-existing load-sensitive `runtimeexecution`
  `TestPostgresConcurrentCapsuleGenerationReplaysExactContentAndRejectsIdentityRebinding`
  flake, which passes in isolation and has no dependency on this module
  (same documented flake as C05-05).

## Completion boundary

- C05-06 delivers the owned publication transport and the Task Orchestration
  publication bridge (complete canonical request fixed at outbox-commit,
  owned adapter delivery without re-assembly, ambiguity → reconciliation by
  the original OperationID, C05 exact activation/rejection evidence). It
  does NOT deliver: full audit/observability/safe-errors/non-leakage
  surfaces (#111), the parent SPEC shared acceptance/completion audit
  (#112), or production network-protocol / broker selection (the final
  network protocol is out of scope by the parent SPEC).
- The bridge adapter wiring in this report is the deterministic
  harness/transport pair; a production-shaped transport (e.g., over a real
  broker) implements the same `OwnedTransportRoundTripper` /
  `PublicationTransportPort` contracts without changing the domain
  semantics.
- No production data mutation, legacy deletion, hard cutover, traffic
  enablement, or production Durable Object vendor wiring was performed.
