package artifactpublication

// Owned publication transport contract (child SPEC #109 / C05-06). These
// tests prove the owned transport envelope semantics over the deterministic
// in-memory authority: strict schema/version + machine authorization +
// OperationID + canonical request digest + deadline + generation/fence/
// safety epoch; at-least-once duplicate exact delivery returning the same
// decision; same-OperationID/different-payload integrity conflict; timeout/
// disconnect/claim loss/response+ack loss/callback duplicate/out-of-order
// returning typed reconciliation-required without a second publication
// operation or Artifact Version; inspect/replay of the ORIGINAL OperationID;
// adapter-restart deadline persistence; and non-leakage of the closed safe
// error surface. The identical contract runs against real PostgreSQL in
// owned_transport_postgres_test.go.

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// ownedTransportHarnessFor builds the deterministic transport harness over
// the fixture's core with the fixture's controlled clock and scope.
func ownedTransportHarnessFor(t *testing.T, f *fixture) *OwnedTransportHarness {
	t.Helper()
	return NewOwnedTransportHarness(OwnedTransportHarnessConfig{
		Core: f.core,
		MachineAuthority: OwnedTransportMachineAuthority{
			ID: "task-orchestration-machine-1", Generation: 7, ExpiresAt: f.now + 10_000,
		},
		AuthenticationKey: []byte("owned-publication-transport-test-key"),
		Authorize: func(scope OwnedTransportAuthorityScope) bool {
			return scope.PolicyDomainID == f.policyDomain && scope.TaskID == f.taskID
		},
		Now: func() Instant { return f.now }, RequestLifetime: 50,
	})
}

// TestOwnedTransportAuthenticatesAndBindsCanonicalEnvelope proves the
// envelope carries strict schema/version, machine authorization, the exact
// OperationID, the business canonical request digest, the payload digest,
// the deadline, and the applicable generation/fence/safety epoch — and that
// a delivery attempt never changes the business digest.
func TestOwnedTransportAuthenticatesAndBindsCanonicalEnvelope(t *testing.T) {
	f := newFixture(t)
	harness := ownedTransportHarnessFor(t, f)
	client := harness.Client()
	operationID := "transport-envelope-1"
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	intent := f.prepareIntent(operationID, f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()}))
	decision, err := client.Mutate(context.Background(), intent)
	if err != nil || decision.State != OperationPrepared || decision.ArtifactVersionID == "" {
		t.Fatalf("prepare through owned transport = %#v, err = %v", decision, err)
	}

	requests := harness.Requests()
	if len(requests) != 1 {
		t.Fatalf("captured requests = %d, want 1", len(requests))
	}
	envelope := requests[0].Envelope
	if envelope.SchemaVersion != OwnedTransportWireSchemaV1 ||
		envelope.Method != ownedTransportMutate ||
		envelope.MachineAuthorityID != "task-orchestration-machine-1" ||
		envelope.MachineAuthorityGeneration != 7 ||
		envelope.PolicyDomainID != f.policyDomain || envelope.TaskID != f.taskID ||
		envelope.OperationID != PublicationOperationID(operationID) ||
		envelope.CanonicalRequestDigest != intent.header().Operation.RequestDigest ||
		envelope.CanonicalRequestDigest != CanonicalRequestDigest(intent) ||
		envelope.Deadline != f.now+50 ||
		envelope.Generation != f.generation || envelope.Fence != f.fence ||
		envelope.SafetyEpoch != f.safetyEpoch || len(envelope.Payload) == 0 ||
		envelope.PayloadDigest != ownedTransportDigest(envelope.Payload) ||
		envelope.AuthenticationTag == "" {
		t.Fatalf("owned transport envelope did not preserve exact authority binding: %#v", envelope)
	}

	// A second delivery of the exact same canonical request reuses the same
	// business digest (delivery attempts never change it).
	replayed, err := client.Mutate(context.Background(), intent)
	if err != nil || !assertOwnedTransportSameDecision(replayed, decision) {
		t.Fatalf("exact replay through owned transport = %#v, err = %v", replayed, err)
	}
	second := harness.Requests()[1].Envelope
	if second.CanonicalRequestDigest != envelope.CanonicalRequestDigest ||
		second.OperationID != envelope.OperationID ||
		second.Deadline != envelope.Deadline {
		t.Fatalf("delivery attempt changed business facts: %#v vs %#v", envelope, second)
	}
}

// TestOwnedTransportFullPublicationLifecycle drives prepare -> verify ->
// activate -> query through the owned transport and proves the committed
// activation evidence binds the exact OperationID, publication
// generation/fence, activity generation, safety epoch, ArtifactVersionID
// and manifest digest.
func TestOwnedTransportFullPublicationLifecycle(t *testing.T) {
	f := newFixture(t)
	harness := ownedTransportHarnessFor(t, f)
	client := harness.Client()
	operationID := "transport-lifecycle-1"
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})

	prepare, err := client.Mutate(context.Background(), f.prepareIntent(operationID, f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()})))
	if err != nil || prepare.State != OperationPrepared {
		t.Fatalf("prepare through transport = %#v, err = %v", prepare, err)
	}
	verify, err := client.Mutate(context.Background(), f.verifyIntent(operationID, f.verifyPayload(set)))
	if err != nil || verify.State != OperationVerified || verify.ArtifactVersionID != prepare.ArtifactVersionID {
		t.Fatalf("verify through transport = %#v, err = %v", verify, err)
	}
	activated, err := client.Mutate(context.Background(), f.activateIntent(operationID))
	if err != nil || activated.State != OperationActivated || activated.ActivationEvidence == nil {
		t.Fatalf("activate through transport = %#v, err = %v", activated, err)
	}
	evidence := activated.ActivationEvidence
	if evidence.OperationID != PublicationOperationID(operationID) ||
		evidence.PolicyDomainID != f.policyDomain || evidence.TaskID != f.taskID ||
		evidence.PhaseRunID != f.phaseRunID ||
		evidence.ArtifactVersionID != prepare.ArtifactVersionID ||
		evidence.ManifestDigest != prepare.ManifestDigest ||
		evidence.ActivityGeneration != f.generation ||
		evidence.Generation != f.generation || evidence.Fence != f.fence ||
		evidence.SafetyEpoch != f.safetyEpoch ||
		evidence.StreamRevision != 1 || evidence.CurrentHead != prepare.ArtifactVersionID {
		t.Fatalf("activation evidence did not bind exact facts: %#v", evidence)
	}

	view, err := client.Query(context.Background(), PublicationQuery{
		Kind: QueryExactVersion, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: prepare.ArtifactVersionID,
	})
	if err != nil || view.ArtifactVersionID != prepare.ArtifactVersionID ||
		view.ManifestDigest != prepare.ManifestDigest || len(view.Members) != 1 {
		t.Fatalf("exact version query through transport = %#v, err = %v", view, err)
	}

	// Response loss on a later delivery still exact-replays the original.
	replayed, err := client.Mutate(context.Background(), f.activateIntent(operationID))
	if err != nil || !assertOwnedTransportSameDecision(replayed, activated) {
		t.Fatalf("activation replay through transport = %#v, err = %v", replayed, err)
	}
}

// TestOwnedTransportDuplicateExactDeliveryReturnsSameDecision proves
// duplicate exact delivery returns the same decision and never creates a
// second publication operation, Artifact Version, or stream revision.
func TestOwnedTransportDuplicateExactDeliveryReturnsSameDecision(t *testing.T) {
	f := newFixture(t)
	harness := ownedTransportHarnessFor(t, f)
	client := harness.Client()
	operationID := "transport-duplicate-1"
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	prepare, err := client.Mutate(context.Background(), f.prepareIntent(operationID, f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()})))
	if err != nil {
		t.Fatalf("prepare through transport: %v", err)
	}
	if _, err := client.Mutate(context.Background(), f.verifyIntent(operationID, f.verifyPayload(set))); err != nil {
		t.Fatalf("verify through transport: %v", err)
	}

	deliveryOffset := len(harness.Deliveries())
	harness.FailNext(OwnedTransportDuplicateDelivery)
	first, err := client.Mutate(context.Background(), f.activateIntent(operationID))
	if err != nil || first.State != OperationActivated {
		t.Fatalf("duplicate-delivered activation = %#v, err = %v", first, err)
	}
	deliveries := harness.Deliveries()[deliveryOffset:]
	assertOwnedTransportDeliveryKinds(t, deliveries,
		OwnedTransportDeliveryOriginal, OwnedTransportDeliveryDuplicate)
	for _, delivery := range deliveries {
		if delivery.OperationID != PublicationOperationID(operationID) {
			t.Fatalf("duplicate delivery changed OperationID: %#v", deliveries)
		}
	}

	history, err := client.Query(context.Background(), PublicationQuery{
		Kind: QueryVersionHistory, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if err != nil || len(history.History) != 1 || history.StreamRevision != 1 ||
		history.CurrentHead != prepare.ArtifactVersionID {
		t.Fatalf("duplicate delivery must not create a second version: %#v, err = %v", history, err)
	}
}

// TestOwnedTransportSameOperationDifferentPayloadIntegrityConflict proves
// that re-binding an existing OperationID to a different canonical payload
// is rejected by the client binding store and by the receiving harness.
func TestOwnedTransportSameOperationDifferentPayloadIntegrityConflict(t *testing.T) {
	f := newFixture(t)
	harness := ownedTransportHarnessFor(t, f)
	client := harness.Client()
	operationID := "transport-conflict-1"
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	first := f.prepareIntent(operationID, f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()}))
	if _, err := client.Mutate(context.Background(), first); err != nil {
		t.Fatalf("prepare through transport: %v", err)
	}

	// Same OperationID, different canonical payload (a different logical
	// member name on the same slot): the request reaches the authority with
	// a different digest and the operation journal rejects re-binding.
	conflicting := f.prepareIntent(operationID, f.preparePayload(operationID, set, []ArtifactMemberSpec{
		{Slot: "slot-deck", Kind: ArtifactKindDeck, LogicalName: "Deck-renamed.pptx",
			MediaType: MediaTypePPTX, Size: 1024, ContentDigest: testDigest("deck-content")},
	}))
	_, err := client.Mutate(context.Background(), conflicting)
	assertOwnedTransportErrorCode(t, err, ErrorIntegrityConflict)

	// The harness itself also rejects a same-OperationID envelope with a
	// different payload even when the envelope is freshly crafted and signed.
	differentWire, err := ownedTransportMutationWire(conflicting)
	if err != nil {
		t.Fatalf("build conflicting wire: %v", err)
	}
	payload, err := json.Marshal(differentWire)
	if err != nil {
		t.Fatalf("marshal conflicting wire: %v", err)
	}
	envelope := OwnedTransportEnvelope{
		SchemaVersion: OwnedTransportWireSchemaV1, Method: ownedTransportMutate,
		MachineAuthorityID: "task-orchestration-machine-1", MachineAuthorityGeneration: 7,
		PolicyDomainID: f.policyDomain, TaskID: f.taskID, OperationID: PublicationOperationID(operationID),
		CanonicalRequestDigest: conflicting.header().Operation.RequestDigest,
		PayloadDigest:          ownedTransportDigest(payload),
		Deadline:               f.now + 50, Generation: f.generation, Fence: f.fence,
		SafetyEpoch: f.safetyEpoch, Payload: payload,
	}
	envelope.AuthenticationTag = signOwnedTransportEnvelope(envelope, harness.authenticationKey)
	response, roundTripErr := harness.RoundTrip(context.Background(), OwnedTransportRequest{Envelope: envelope})
	if roundTripErr != nil || response.Error == nil || response.Error.Code != ErrorIntegrityConflict {
		t.Fatalf("harness same-OperationID/different-payload = %#v, err = %v", response, roundTripErr)
	}

	// The original operation remains intact and inspectable.
	view, err := client.Query(context.Background(), PublicationQuery{
		Kind: QueryOperation, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		OperationID: PublicationOperationID(operationID),
	})
	if err != nil || view.OperationID != PublicationOperationID(operationID) ||
		view.ManifestDigest != first.header().Operation.RequestDigest && view.State == "" {
		t.Fatalf("original operation after conflict = %#v, err = %v", view, err)
	}
}

// TestOwnedTransportResponseLossRequiresReconciliationAndReplaysOriginal
// proves response loss returns a typed reconciliation-required result, the
// ORIGINAL OperationID can be inspected and exact-replayed through the same
// transport, and no second Artifact Version is created.
func TestOwnedTransportResponseLossRequiresReconciliationAndReplaysOriginal(t *testing.T) {
	f := newFixture(t)
	harness := ownedTransportHarnessFor(t, f)
	client := harness.Client()
	operationID := "transport-response-loss-1"
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	prepare, err := client.Mutate(context.Background(), f.prepareIntent(operationID, f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()})))
	if err != nil {
		t.Fatalf("prepare through transport: %v", err)
	}
	if _, err := client.Mutate(context.Background(), f.verifyIntent(operationID, f.verifyPayload(set))); err != nil {
		t.Fatalf("verify through transport: %v", err)
	}

	harness.FailNext(OwnedTransportResponseLoss)
	_, err = client.Mutate(context.Background(), f.activateIntent(operationID))
	assertOwnedTransportErrorCode(t, err, ErrorReconciliationRequired)

	// Inspect the ORIGINAL operation through the transport.
	inspection, err := client.Query(context.Background(), PublicationQuery{
		Kind: QueryOperation, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		OperationID: PublicationOperationID(operationID),
	})
	if err != nil || inspection.State != OperationActivated ||
		inspection.ArtifactVersionID != prepare.ArtifactVersionID ||
		inspection.ManifestDigest != prepare.ManifestDigest ||
		inspection.ActivationEvidence == nil ||
		inspection.ActivationEvidence.OperationID != PublicationOperationID(operationID) {
		t.Fatalf("inspect response-lost activation = %#v, err = %v", inspection, err)
	}

	// Exact replay returns the same decision, evidence and candidate.
	replayed, err := client.Mutate(context.Background(), f.activateIntent(operationID))
	if err != nil || replayed.State != OperationActivated ||
		!reflect.DeepEqual(replayed.ActivationEvidence, inspection.ActivationEvidence) {
		t.Fatalf("replay response-lost activation = %#v, err = %v", replayed, err)
	}

	history, err := client.Query(context.Background(), PublicationQuery{
		Kind: QueryVersionHistory, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if err != nil || len(history.History) != 1 || history.StreamRevision != 1 {
		t.Fatalf("response loss must not create a second version: %#v, err = %v", history, err)
	}

	// A rejected operation reconciles through ReconcilePublication and
	// never produces a version.
	rejectOperationID := "transport-response-loss-reject-1"
	rejectSet := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	rejectHeader := f.header(rejectOperationID)
	rejectHeader.ExpectedStreamRevision = 1
	rejectHeader.ExpectedHead = prepare.ArtifactVersionID
	reject := bindDigest(NewPreparePublication(rejectHeader, f.preparePayload(rejectOperationID, rejectSet, []ArtifactMemberSpec{f.deckMemberSpec()})))
	if _, err := client.Mutate(context.Background(), reject); err != nil {
		t.Fatalf("prepare rejection candidate: %v", err)
	}
	harness.FailNext(OwnedTransportResponseLoss)
	_, err = client.Mutate(context.Background(), f.rejectIntent(rejectOperationID, RejectEvidenceFailure, &EvidenceFailure{
		Kind: "validation", EvidenceID: rejectSet.validation.ID,
	}))
	assertOwnedTransportErrorCode(t, err, ErrorReconciliationRequired)
	reconciled, err := client.Mutate(context.Background(), f.reconcileIntent(rejectOperationID, ReconcileConfirmRejection))
	if err != nil || reconciled.State != OperationRejected || reconciled.RejectReason != RejectEvidenceFailure {
		t.Fatalf("reconcile rejection = %#v, err = %v", reconciled, err)
	}
}

// TestOwnedTransportRedeliveryModesReuseTheExactOperation proves
// out-of-order, claim-loss and callback-replay deliveries always return to
// the original OperationID and never duplicate the candidate.
func TestOwnedTransportRedeliveryModesReuseTheExactOperation(t *testing.T) {
	for _, failure := range []OwnedTransportFailure{
		OwnedTransportDuplicateDelivery,
		OwnedTransportOutOfOrderDelivery,
		OwnedTransportQueueClaimLoss,
		OwnedTransportCallbackReplay,
	} {
		t.Run(string(failure), func(t *testing.T) {
			f := newFixture(t)
			harness := ownedTransportHarnessFor(t, f)
			client := harness.Client()
			operationID := "transport-redelivery-" + strings.ReplaceAll(string(failure), "_", "-")
			set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
			intent := f.prepareIntent(operationID, f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()}))
			deliveryOffset := len(harness.Deliveries())
			callbackOffset := len(harness.Callbacks())
			acceptedOffset := len(harness.AcceptedCallbacks())
			harness.FailNext(failure)
			first, err := client.Mutate(context.Background(), intent)
			if err != nil {
				t.Fatalf("redelivered prepare: %v", err)
			}
			deliveries := harness.Deliveries()[deliveryOffset:]
			callbacks := harness.Callbacks()[callbackOffset:]
			accepted := harness.AcceptedCallbacks()[acceptedOffset:]
			switch failure {
			case OwnedTransportDuplicateDelivery:
				assertOwnedTransportDeliveryKinds(t, deliveries,
					OwnedTransportDeliveryOriginal, OwnedTransportDeliveryDuplicate)
			case OwnedTransportOutOfOrderDelivery:
				assertOwnedTransportDeliveryKinds(t, deliveries,
					OwnedTransportDeliveryRedelivery, OwnedTransportDeliveryOriginal)
			case OwnedTransportQueueClaimLoss:
				assertOwnedTransportDeliveryKinds(t, deliveries,
					OwnedTransportDeliveryOriginal, OwnedTransportDeliveryRedelivery)
				if deliveries[0].ClaimGeneration != 1 || deliveries[1].ClaimGeneration != 2 {
					t.Fatalf("claim-loss delivery generations = %#v", deliveries)
				}
			case OwnedTransportCallbackReplay:
				assertOwnedTransportDeliveryKinds(t, deliveries, OwnedTransportDeliveryOriginal)
				if len(callbacks) != 2 || len(accepted) != 1 {
					t.Fatalf("callback replay deliveries=%d accepted=%d, want 2/1",
						len(callbacks), len(accepted))
				}
			}
			for _, delivery := range deliveries {
				if delivery.OperationID != PublicationOperationID(operationID) {
					t.Fatalf("redelivery changed OperationID: %#v", deliveries)
				}
			}
			replayed, err := client.Mutate(context.Background(), intent)
			if err != nil || !assertOwnedTransportSameDecision(replayed, first) {
				t.Fatalf("redelivered operation replay = %#v, err = %v", replayed, err)
			}
			view, err := client.Query(context.Background(), PublicationQuery{
				Kind: QueryOperation, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
				OperationID: PublicationOperationID(operationID),
			})
			if err != nil || view.ArtifactVersionID != first.ArtifactVersionID {
				t.Fatalf("redelivered operation inspection = %#v, err = %v", view, err)
			}
		})
	}
}

// TestOwnedTransportAdapterRestartKeepsPersistedDeadline proves a restarted
// adapter cannot extend the original deadline or re-bind the request.
func TestOwnedTransportAdapterRestartKeepsPersistedDeadline(t *testing.T) {
	f := newFixture(t)
	bindings := NewOwnedTransportBindingStore()
	config := OwnedTransportHarnessConfig{
		Core: f.core,
		MachineAuthority: OwnedTransportMachineAuthority{
			ID: "task-orchestration-machine-1", Generation: 7, ExpiresAt: f.now + 10_000,
		},
		AuthenticationKey: []byte("owned-publication-transport-test-key"),
		Authorize: func(scope OwnedTransportAuthorityScope) bool {
			return scope.PolicyDomainID == f.policyDomain && scope.TaskID == f.taskID
		},
		Now: func() Instant { return f.now }, RequestLifetime: 50,
		BindingStore: bindings,
	}
	first := NewOwnedTransportHarness(config)
	operationID := "transport-restart-1"
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	intent := f.prepareIntent(operationID, f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()}))
	first.SetConnected(false)
	_, err := first.Client().Mutate(context.Background(), intent)
	assertOwnedTransportErrorCode(t, err, ErrorRetryableUnavailable)
	if got := first.Requests()[0].Envelope.Deadline; got != f.now+50 {
		t.Fatalf("first adapter deadline = %d, want %d", got, f.now+50)
	}

	f.now = f.now + 51
	restarted := NewOwnedTransportHarness(config)
	_, err = restarted.Client().Mutate(context.Background(), intent)
	assertOwnedTransportErrorCode(t, err, ErrorStaleAuthority)
	if got := restarted.Requests()[0].Envelope.Deadline; got != f.now-1 {
		t.Fatalf("restarted adapter weakened deadline to %d, want persisted %d", got, f.now-1)
	}
}

// TestOwnedTransportTimeoutDisconnectAndAmbiguityCategories proves timeout
// after delivery and round-trip deadline ambiguity are typed
// reconciliation-required, and disconnect is typed retryable-unavailable.
func TestOwnedTransportTimeoutDisconnectAndAmbiguityCategories(t *testing.T) {
	t.Run("timeout after delivery", func(t *testing.T) {
		f := newFixture(t)
		harness := ownedTransportHarnessFor(t, f)
		client := harness.Client()
		operationID := "transport-timeout-1"
		set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
		harness.FailNext(OwnedTransportTimeoutAfterDelivery)
		_, err := client.Mutate(context.Background(), f.prepareIntent(operationID, f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()})))
		assertOwnedTransportErrorCode(t, err, ErrorReconciliationRequired)
		// The operation committed; the original can be inspected.
		view, inspectErr := client.Query(context.Background(), PublicationQuery{
			Kind: QueryOperation, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
			OperationID: PublicationOperationID(operationID),
		})
		if inspectErr != nil || view.State != OperationPrepared {
			t.Fatalf("inspect timed-out operation = %#v, err = %v", view, inspectErr)
		}
	})

	t.Run("disconnect", func(t *testing.T) {
		f := newFixture(t)
		harness := ownedTransportHarnessFor(t, f)
		harness.SetConnected(false)
		operationID := "transport-disconnect-1"
		set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
		_, err := harness.Client().Mutate(context.Background(), f.prepareIntent(operationID, f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()})))
		assertOwnedTransportErrorCode(t, err, ErrorRetryableUnavailable)
	})

	t.Run("round trip deadline ambiguity", func(t *testing.T) {
		f := newFixture(t)
		client := NewOwnedTransportClient(OwnedTransportClientConfig{
			Transport: ownedTransportRoundTripperFunc(func(
				context.Context, OwnedTransportRequest,
			) (OwnedTransportResponse, error) {
				return OwnedTransportResponse{}, context.DeadlineExceeded
			}),
			MachineAuthority: OwnedTransportMachineAuthority{
				ID: "deadline-machine-1", Generation: 1, ExpiresAt: 10_000,
			},
			AuthenticationKey: []byte("deadline-key"),
			Now:               func() Instant { return f.now }, RequestLifetime: 50,
		})
		operationID := "transport-deadline-ambiguity-1"
		set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
		_, err := client.Mutate(context.Background(), f.prepareIntent(operationID, f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()})))
		assertOwnedTransportErrorCode(t, err, ErrorReconciliationRequired)
	})
}

// TestOwnedTransportCanonicalizationForgeryAndUnknownWire proves
// non-canonical payloads, forged responses/callbacks/error payloads and
// unknown wire errors all fail closed.
func TestOwnedTransportCanonicalizationForgeryAndUnknownWire(t *testing.T) {
	t.Run("non-canonical payload", func(t *testing.T) {
		f := newFixture(t)
		harness := ownedTransportHarnessFor(t, f)
		harness.FailNext(OwnedTransportNonCanonicalPayload)
		operationID := "transport-non-canonical-1"
		set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
		_, err := harness.Client().Mutate(context.Background(), f.prepareIntent(operationID, f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()})))
		assertOwnedTransportErrorCode(t, err, ErrorIntegrityConflict)
	})

	t.Run("forged response", func(t *testing.T) {
		f := newFixture(t)
		harness := ownedTransportHarnessFor(t, f)
		harness.FailNext(OwnedTransportForgedResponse)
		operationID := "transport-forged-response-1"
		set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
		_, err := harness.Client().Mutate(context.Background(), f.prepareIntent(operationID, f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()})))
		assertOwnedTransportErrorCode(t, err, ErrorIntegrityConflict)
	})

	t.Run("forged callback", func(t *testing.T) {
		f := newFixture(t)
		harness := ownedTransportHarnessFor(t, f)
		harness.FailNext(OwnedTransportForgedCallback)
		operationID := "transport-forged-callback-1"
		set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
		_, err := harness.Client().Mutate(context.Background(), f.prepareIntent(operationID, f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()})))
		if err != nil {
			t.Fatalf("response paired with forged callback: %v", err)
		}
		if len(harness.Callbacks()) != 1 || len(harness.AcceptedCallbacks()) != 0 {
			t.Fatalf("forged callback deliveries=%d accepted=%d, want 1/0",
				len(harness.Callbacks()), len(harness.AcceptedCallbacks()))
		}
	})

	t.Run("forged error response payload", func(t *testing.T) {
		f := newFixture(t)
		harness := ownedTransportHarnessFor(t, f)
		harness.FailNext(OwnedTransportForgedErrorResponse)
		operationID := "transport-forged-error-response-1"
		set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
		_, err := harness.Client().Mutate(context.Background(), f.prepareIntent(operationID, f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()})))
		assertOwnedTransportErrorCode(t, err, ErrorIntegrityConflict)
	})

	t.Run("forged error callback payload", func(t *testing.T) {
		f := newFixture(t)
		harness := ownedTransportHarnessFor(t, f)
		harness.FailNext(OwnedTransportForgedErrorCallback)
		operationID := "transport-forged-error-callback-1"
		set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
		_, err := harness.Client().Mutate(context.Background(), f.prepareIntent(operationID, f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()})))
		assertOwnedTransportErrorCode(t, err, ErrorRetryableUnavailable)
		if len(harness.Callbacks()) != 1 || len(harness.AcceptedCallbacks()) != 0 {
			t.Fatalf("forged error callback deliveries=%d accepted=%d, want 1/0",
				len(harness.Callbacks()), len(harness.AcceptedCallbacks()))
		}
	})

	t.Run("unknown wire error code", func(t *testing.T) {
		f := newFixture(t)
		harness := ownedTransportHarnessFor(t, f)
		harness.FailNext(OwnedTransportUnknownWireError)
		operationID := "transport-unknown-wire-1"
		set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
		_, err := harness.Client().Mutate(context.Background(), f.prepareIntent(operationID, f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()})))
		assertOwnedTransportErrorCode(t, err, ErrorIntegrityConflict)
	})
}

// TestOwnedTransportWireErrorsPreserveSafeSemanticsWithoutRawFailure proves
// unsafe dependency failures are normalized to the closed safe-error surface
// and that no canary (content, path, session, mount, locator, credential,
// vendor, key) leaks into requests, responses, callbacks, signals or the
// public error.
func TestOwnedTransportWireErrorsPreserveSafeSemanticsWithoutRawFailure(t *testing.T) {
	const canary = "unsafe vendor /private/c04 session=c04 mount=c04 locator=c04 " +
		"credential=c04 content=c04 foreign-workspace-exists-c04"
	f := newFixture(t)
	harness := ownedTransportHarnessFor(t, f)
	harness.FailNext(OwnedTransportUnsafeFailure)
	operationID := "transport-unsafe-1"
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	_, err := harness.Client().Mutate(context.Background(), f.prepareIntent(operationID, f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()})))
	assertOwnedTransportErrorCode(t, err, ErrorRetryableUnavailable)
	if publicationError := err.(*Error); publicationError.SafeCategory() != SafeErrorRetryableUnavailable ||
		!publicationError.Retryable() || publicationError.ReconciliationRequired() {
		t.Fatalf("unsafe dependency semantics = %#v", publicationError)
	}
	callbacks := harness.Callbacks()
	if len(callbacks) != 1 || callbacks[0].Error == nil ||
		callbacks[0].Error.SafeCategory != SafeErrorRetryableUnavailable ||
		!callbacks[0].Error.Retryable || callbacks[0].Error.ReconciliationRequired {
		t.Fatalf("normalized wire error = %#v", callbacks)
	}
	captured, marshalErr := json.Marshal(struct {
		Requests  []OwnedTransportRequest
		Responses []OwnedTransportResponse
		Callbacks []OwnedTransportCallback
		Signals   []OwnedTransportOperationalSignal
	}{harness.Requests(), harness.Responses(), callbacks, harness.OperationalSignals()})
	if marshalErr != nil {
		t.Fatalf("marshal captured owned transport traffic: %v", marshalErr)
	}
	for _, secret := range []string{
		"unsafe vendor", "/private", "session", "mount", "locator", "credential", "content",
		"foreign-workspace-exists", "owned-publication-transport-test-key",
	} {
		if strings.Contains(strings.ToLower(string(captured)), strings.ToLower(secret)) ||
			strings.Contains(strings.ToLower(err.Error()), strings.ToLower(secret)) {
			t.Fatalf("owned transport traffic or error leaked %q", secret)
		}
	}
}

// TestOwnedTransportAuthorizationAndFencingCategories proves authorization
// denial, stale generation/fence and ambiguity map to the closed safe
// categories.
func TestOwnedTransportAuthorizationAndFencingCategories(t *testing.T) {
	t.Run("authorization", func(t *testing.T) {
		f := newFixture(t)
		harness := ownedTransportHarnessFor(t, f)
		operationID := "transport-denied-1"
		set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
		header := f.header(operationID)
		header.PolicyDomainID = "policy-domain-2"
		header.Operation.RequestDigest = Digest("")
		intent := bindDigest(NewPreparePublication(header, f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()})))
		_, err := harness.Client().Mutate(context.Background(), intent)
		assertOwnedTransportErrorCode(t, err, ErrorOwnershipDenied)
	})

	t.Run("stale fence", func(t *testing.T) {
		f := newFixture(t)
		harness := ownedTransportHarnessFor(t, f)
		client := harness.Client()
		operationID := "transport-stale-1"
		set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
		if _, err := client.Mutate(context.Background(), f.prepareIntent(operationID, f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()}))); err != nil {
			t.Fatalf("prepare through transport: %v", err)
		}
		staleHeader := f.header(operationID)
		staleHeader.Fence++
		staleHeader.Operation.RequestDigest = Digest("")
		stale := bindDigest(NewVerifyPublication(staleHeader, f.verifyPayload(set)))
		_, err := client.Mutate(context.Background(), stale)
		assertOwnedTransportErrorCode(t, err, ErrorStaleAuthority)
	})

	t.Run("ambiguity", func(t *testing.T) {
		f := newFixture(t)
		harness := ownedTransportHarnessFor(t, f)
		harness.FailNext(OwnedTransportResponseLoss)
		operationID := "transport-ambiguous-1"
		set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
		_, err := harness.Client().Mutate(context.Background(), f.prepareIntent(operationID, f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()})))
		assertOwnedTransportErrorCode(t, err, ErrorReconciliationRequired)
		signals := harness.OperationalSignals()
		if len(signals) != 1 || signals[0].SafeCategory != SafeErrorReconciliationRequired ||
			!signals[0].ReconciliationRequired || signals[0].Retryable ||
			signals[0].Result != OwnedTransportSignalAmbiguous {
			t.Fatalf("ambiguous transport signal = %#v", signals)
		}
	})
}

// TestOwnedTransportRejectsUnauthenticatedMachineWithoutDispatch proves a
// machine with the wrong authentication key is denied without reaching the
// C05 authority.
func TestOwnedTransportRejectsUnauthenticatedMachineWithoutDispatch(t *testing.T) {
	f := newFixture(t)
	harness := ownedTransportHarnessFor(t, f)
	client := NewOwnedTransportClient(OwnedTransportClientConfig{
		Transport:                 harness,
		MachineAuthority:          harness.authority,
		AuthenticationKey:         []byte("wrong-key"),
		ResponseAuthenticationKey: []byte("owned-publication-transport-test-key"),
		Now:                       func() Instant { return f.now },
		RequestLifetime:           50,
	})
	operationID := "transport-unauthenticated-1"
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	_, err := client.Mutate(context.Background(), f.prepareIntent(operationID, f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()})))
	assertOwnedTransportErrorCode(t, err, ErrorOwnershipDenied)

	view, inspectErr := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryOperation, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		OperationID: PublicationOperationID(operationID),
	})
	if inspectErr == nil || view.OperationID != "" {
		t.Fatalf("unauthenticated request reached the authority: %#v, err=%v", view, inspectErr)
	}
}

// TestOwnedTransportManualEditAndContentTargetThroughTransport proves the
// manual-edit child lineage and locator-free content target contract run
// unchanged through the owned transport.
func TestOwnedTransportManualEditAndContentTargetThroughTransport(t *testing.T) {
	f := newFixture(t)
	harness := ownedTransportHarnessFor(t, f)
	client := harness.Client()
	parentOperationID := "transport-parent-1"
	_, parent := f.prepareVerifyActivate(t, parentOperationID)
	if parent.ActivationEvidence == nil {
		t.Fatalf("parent activation missing evidence: %#v", parent)
	}

	childOperationID := "transport-child-1"
	childSet := f.childEvidenceSet(t, parent.ArtifactVersionID, childOperationID)
	childHeader := f.header(childOperationID)
	childHeader.ExpectedStreamRevision = 1
	childHeader.ExpectedHead = parent.ArtifactVersionID
	childPrepare := bindDigest(NewPreparePublication(childHeader, f.childPreparePayload(childOperationID, parent.ArtifactVersionID, childSet)))
	if _, err := client.Mutate(context.Background(), childPrepare); err != nil {
		t.Fatalf("child prepare through transport: %v", err)
	}
	if _, err := client.Mutate(context.Background(), f.verifyIntent(childOperationID, f.verifyPayload(childSet))); err != nil {
		t.Fatalf("child verify through transport: %v", err)
	}
	child, err := client.Mutate(context.Background(), f.activateIntentWithHeader(childHeader))
	if err != nil || child.ActivationEvidence == nil ||
		child.ActivationEvidence.Parent != parent.ArtifactVersionID ||
		child.ActivationEvidence.CurrentHead != child.ArtifactVersionID ||
		child.ActivationEvidence.StreamRevision != 2 {
		t.Fatalf("child activation through transport = %#v, err = %v", child, err)
	}

	f.registerScope(parent.ArtifactVersionID, f.ownerScope(parent.ArtifactVersionID))
	parentView, err := client.Query(context.Background(), PublicationQuery{
		Kind: QueryExactVersion, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: parent.ArtifactVersionID,
	})
	if err != nil || len(parentView.Members) != 1 {
		t.Fatalf("query parent version = %#v, err = %v", parentView, err)
	}
	target, err := client.Query(context.Background(), PublicationQuery{
		Kind: QueryResolveContentTarget, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: parent.ArtifactVersionID, ArtifactID: parentView.Members[0].ArtifactID,
		Scope: f.ownerScope(parent.ArtifactVersionID), ContentIntent: ContentIntentDownload,
	})
	if err != nil || target.ContentTarget == nil || target.ContentTarget.ArtifactVersionID != parent.ArtifactVersionID {
		t.Fatalf("content target through transport = %#v, err = %v", target, err)
	}
}

// TestOwnedTransportNonLeakageAcrossWireTypes proves a raw downstream
// failure carrying hostile content, paths, sessions, locators, credentials
// and foreign-workspace canaries never reaches the wire types or the public
// error: it is normalized to the closed retryable-unavailable surface.
func TestOwnedTransportNonLeakageAcrossWireTypes(t *testing.T) {
	const canary = "raw vendor content /private/path session=c04 mount=c04 " +
		"locator=c04 credential=c04 foreign-workspace-exists-c04"
	f := newFixture(t)
	persistence := newPersistence()
	core := NewInMemory(InMemoryConfig{
		Now:                          func() Instant { return f.now },
		RuntimeAuthorityID:           f.runtimeAuthority,
		ValidationAuthorityID:        f.validationAuthority,
		C04AuthorityID:               f.c04Authority,
		DurableObjectAuthorityID:     f.durableObjectAuthority,
		TaskOrchestrationAuthorityID: f.taskOrchestrationAuthority,
		RecoveryAuthorityID:          f.recoveryAuthority,
		CleanupAuthorityID:           f.cleanupAuthority,
		PublicationAuthorityID:       f.publicationAuthority,
		CurrentContentCapability:     f.registry.resolve,
		CurrentContentScope:          f.scopes.resolve,
		FaultHook: func(event FaultEvent) error {
			if event.Point == FaultBeforeOperationJournal {
				return errors.New(canary)
			}
			return nil
		},
	}, persistence)
	harness := NewOwnedTransportHarness(OwnedTransportHarnessConfig{
		Core: core,
		MachineAuthority: OwnedTransportMachineAuthority{
			ID: "task-orchestration-machine-1", Generation: 7, ExpiresAt: f.now + 10_000,
		},
		AuthenticationKey: []byte("owned-publication-transport-test-key"),
		Authorize: func(scope OwnedTransportAuthorityScope) bool {
			return scope.PolicyDomainID == f.policyDomain && scope.TaskID == f.taskID
		},
		Now: func() Instant { return f.now }, RequestLifetime: 50,
	})
	client := harness.Client()
	operationID := "transport-non-leakage-1"
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	_, err := client.Mutate(context.Background(), f.prepareIntent(operationID, f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()})))
	assertOwnedTransportErrorCode(t, err, ErrorRetryableUnavailable)
	captured, marshalErr := json.Marshal(struct {
		Requests  []OwnedTransportRequest
		Responses []OwnedTransportResponse
		Callbacks []OwnedTransportCallback
		Signals   []OwnedTransportOperationalSignal
	}{harness.Requests(), harness.Responses(), harness.Callbacks(), harness.OperationalSignals()})
	if marshalErr != nil {
		t.Fatalf("marshal wire traffic: %v", marshalErr)
	}
	for _, forbidden := range []string{
		"raw vendor", "/private", "session=c04", "mount=c04", "locator=c04",
		"credential=c04", "foreign-workspace-exists-c04",
	} {
		if strings.Contains(string(captured), forbidden) || strings.Contains(err.Error(), forbidden) {
			t.Fatalf("owned transport wire or error leaked %q", forbidden)
		}
	}
}

func assertOwnedTransportErrorCode(t *testing.T, err error, code ErrorCode) {
	t.Helper()
	var publicationError *Error
	if !errors.As(err, &publicationError) || publicationError.Code != code {
		t.Fatalf("owned transport error = %T/%v, want code %q", err, err, code)
	}
}

// assertOwnedTransportSameDecision compares the substantive facts of two
// decisions; the Replay flag is a delivery fact and may differ between the
// original and an exact replay.
func assertOwnedTransportSameDecision(first, second PublicationDecision) bool {
	return first.Operation == second.Operation &&
		first.State == second.State &&
		first.StreamRevision == second.StreamRevision &&
		first.ArtifactVersionID == second.ArtifactVersionID &&
		first.ManifestDigest == second.ManifestDigest &&
		first.LineageDigest == second.LineageDigest &&
		reflect.DeepEqual(first.Verification, second.Verification) &&
		reflect.DeepEqual(first.ActivationEvidence, second.ActivationEvidence) &&
		first.IntegrityConflict == second.IntegrityConflict &&
		first.RejectReason == second.RejectReason && first.CancelReason == second.CancelReason &&
		first.ReconcileMode == second.ReconcileMode && first.ResidueRelease == second.ResidueRelease
}

func assertOwnedTransportDeliveryKinds(
	t *testing.T,
	deliveries []OwnedTransportDelivery,
	want ...OwnedTransportDeliveryKind,
) {
	t.Helper()
	if len(deliveries) != len(want) {
		t.Fatalf("delivery count = %d, want %d: %#v", len(deliveries), len(want), deliveries)
	}
	for index := range want {
		if deliveries[index].Kind != want[index] {
			t.Fatalf("delivery %d kind = %q, want %q: %#v", index, deliveries[index].Kind, want[index], deliveries)
		}
	}
}

type ownedTransportRoundTripperFunc func(
	context.Context,
	OwnedTransportRequest,
) (OwnedTransportResponse, error)

func (f ownedTransportRoundTripperFunc) RoundTrip(
	ctx context.Context,
	request OwnedTransportRequest,
) (OwnedTransportResponse, error) {
	return f(ctx, request)
}
