package artifactpublication

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// TestFirstGenerationPrepareVerifyLifecycle walks the tracer bullet:
// canonical request -> prepare immutable candidate -> verify exact Runtime,
// Platform validation, C04 commit, and Durable Object evidence -> verified,
// then inspects the operation and candidate through the pure read-only Query
// seam.
func TestFirstGenerationPrepareVerifyLifecycle(t *testing.T) {
	f := newFixture(t)
	operationID := "op-1"

	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	prepare := f.mustPrepare(t, operationID, set)
	if prepare.State != OperationPrepared || prepare.ArtifactVersionID == "" ||
		!validDigest(prepare.ManifestDigest) || !validDigest(prepare.LineageDigest) {
		t.Fatalf("unexpected prepare decision: %#v", prepare)
	}
	if prepare.Replay || prepare.IntegrityConflict {
		t.Fatal("fresh prepare must not report replay or conflict")
	}
	versionID := prepare.ArtifactVersionID

	verify := f.mustVerify(t, operationID, set)
	if verify.State != OperationVerified || verify.ArtifactVersionID != versionID ||
		verify.ManifestDigest != prepare.ManifestDigest || verify.LineageDigest != prepare.LineageDigest {
		t.Fatalf("unexpected verify decision: %#v", verify)
	}
	if verify.Verification == nil || verify.Verification.State != VerificationVerified {
		t.Fatalf("missing verified verification result: %#v", verify.Verification)
	}
	if len(verify.Verification.AcceptedEvidence) != 4 {
		t.Fatalf("expected four accepted evidence refs (runtime, validation, c04, capability), got %d",
			len(verify.Verification.AcceptedEvidence))
	}

	operationView, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryOperation, PolicyDomainID: f.policyDomain, TaskID: f.taskID, OperationID: PublicationOperationID(operationID),
	})
	if err != nil {
		t.Fatalf("query operation: %v", err)
	}
	if operationView.State != OperationVerified || operationView.ArtifactVersionID != versionID ||
		operationView.ManifestDigest != prepare.ManifestDigest {
		t.Fatalf("unexpected operation view: %#v", operationView)
	}
	if len(operationView.Members) != 1 {
		t.Fatalf("expected one member view, got %d", len(operationView.Members))
	}
	member := operationView.Members[0]
	if member.ArtifactID == "" || member.Kind != ArtifactKindDeck ||
		member.LogicalName != "Deck.pptx" || member.MediaType != MediaTypePPTX ||
		member.Size != 1024 || !validDigest(member.ContentDigest) {
		t.Fatalf("unexpected member view: %#v", member)
	}

	candidateView, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryCandidate, PolicyDomainID: f.policyDomain, TaskID: f.taskID, ArtifactVersionID: versionID,
	})
	if err != nil {
		t.Fatalf("query candidate: %v", err)
	}
	if candidateView.ArtifactVersionID != versionID || candidateView.PublicationKind != PublicationKindFirstGeneration ||
		candidateView.Parent != "" {
		t.Fatalf("unexpected candidate view: %#v", candidateView)
	}
}

// TestRejectNeverCreatesVersionOrHead proves rejection is terminal, creates
// no Artifact Version, member, or current-head mutation, releases staging as
// residue, and remains invisible to ordinary queries.
func TestRejectNeverCreatesVersionOrHead(t *testing.T) {
	f := newFixture(t)
	operationID := "op-reject"
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})

	f.mustPrepare(t, operationID, set)
	failure := &EvidenceFailure{Kind: "runtime_evidence_mismatch", EvidenceID: "runtime-evidence-1"}
	rejected, err := f.core.Mutate(context.Background(), f.rejectIntent(operationID, RejectEvidenceFailure, failure))
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if rejected.State != OperationRejected || !rejected.ResidueRelease {
		t.Fatalf("unexpected reject decision: %#v", rejected)
	}

	view, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryOperation, PolicyDomainID: f.policyDomain, TaskID: f.taskID, OperationID: PublicationOperationID(operationID),
	})
	if err != nil {
		t.Fatalf("query rejected operation: %v", err)
	}
	if view.State != OperationRejected || !view.ResidueRelease {
		t.Fatalf("unexpected rejected view: %#v", view)
	}

	stream, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryTaskStream, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if err != nil {
		t.Fatalf("query stream: %v", err)
	}
	if stream.CurrentHead != "" || stream.StreamRevision != 0 {
		t.Fatalf("reject must not advance current head or stream revision: %#v", stream)
	}

	// Late verify on a rejected operation stays terminal.
	_, err = f.core.Mutate(context.Background(), f.verifyIntent(operationID, f.verifyPayload(set)))
	var publicationError *Error
	if !errors.As(err, &publicationError) || publicationError.Code != ErrorTerminalConflict {
		t.Fatalf("late verify after reject error = %v, want terminal conflict", err)
	}
}

// TestCancelFirstLinearizesLateVerifyStale proves cancel-first wins the
// race: a later verify stays stale/terminal and the operation is terminal.
func TestCancelFirstLinearizesLateVerifyStale(t *testing.T) {
	f := newFixture(t)
	operationID := "op-cancel"
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})

	f.mustPrepare(t, operationID, set)
	cancelled, err := f.core.Mutate(context.Background(), f.cancelIntent(operationID, CancelTaskOrchestration))
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if cancelled.State != OperationCancelled || !cancelled.ResidueRelease {
		t.Fatalf("unexpected cancel decision: %#v", cancelled)
	}

	_, err = f.core.Mutate(context.Background(), f.verifyIntent(operationID, f.verifyPayload(set)))
	var publicationError *Error
	if !errors.As(err, &publicationError) || publicationError.Code != ErrorTerminalConflict {
		t.Fatalf("late verify after cancel error = %v, want terminal conflict", err)
	}

	view, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryOperation, PolicyDomainID: f.policyDomain, TaskID: f.taskID, OperationID: PublicationOperationID(operationID),
	})
	if err != nil {
		t.Fatalf("query cancelled operation: %v", err)
	}
	if view.State != OperationCancelled || !view.ResidueRelease {
		t.Fatalf("unexpected cancelled view: %#v", view)
	}
}

// TestVerifyFirstThenCancelKeepsOperationTerminal proves activation-first
// semantics for the core: once verification has committed, cancel still
// linearizes deterministically and exact replay of verify returns the
// original verified decision even after cancel.
func TestVerifyFirstThenCancelAndVerifyReplay(t *testing.T) {
	f := newFixture(t)
	operationID := "op-verify-then-cancel"
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})

	f.mustPrepare(t, operationID, set)
	verify := f.mustVerify(t, operationID, set)
	cancelled, err := f.core.Mutate(context.Background(), f.cancelIntent(operationID, CancelTaskOrchestration))
	if err != nil {
		t.Fatalf("cancel after verify: %v", err)
	}
	if cancelled.State != OperationCancelled {
		t.Fatalf("unexpected cancel decision: %#v", cancelled)
	}

	// Exact replay of the verify intent returns the original verified
	// decision despite the later cancel.
	replayed, err := f.core.Mutate(context.Background(), f.verifyIntent(operationID, f.verifyPayload(set)))
	if err != nil {
		t.Fatalf("replay verify: %v", err)
	}
	if !replayed.Replay || replayed.State != OperationVerified ||
		replayed.ManifestDigest != verify.ManifestDigest {
		t.Fatalf("unexpected verify replay: %#v", replayed)
	}
}

// TestCancelRejectsStaleGenerationFence proves cancel requires the exact
// current operation generation and fence; a stale value fails closed.
func TestCancelRejectsStaleGenerationFence(t *testing.T) {
	f := newFixture(t)
	operationID := "op-stale-cancel"
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})

	f.mustPrepare(t, operationID, set)
	header := f.header(operationID)
	header.Generation = f.generation + 5 // stale
	header.Fence = f.fence + 5
	intent := bindDigest(NewCancelPublication(header, CancelTaskOrchestration))

	_, err := f.core.Mutate(context.Background(), intent)
	var publicationError *Error
	if !errors.As(err, &publicationError) || publicationError.Code != ErrorStaleAuthority {
		t.Fatalf("stale cancel error = %v, want stale authority", err)
	}
}

// TestRecoveryAuthorityCancelAndMisuseBinding proves cancel binds the exact
// typed authority: the protected recovery authority may cancel only with the
// recovery reason and only for its registered identity.
func TestRecoveryAuthorityCancelAndMisuseBinding(t *testing.T) {
	f := newFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	f.mustPrepare(t, "op-recovery", set)

	header := f.header("op-recovery")
	header.Authority = f.recoveryAuthorityValue()
	recoveryCancel := bindDigest(NewCancelPublication(header, CancelRecovery))
	decision, err := f.core.Mutate(context.Background(), recoveryCancel)
	if err != nil {
		t.Fatalf("recovery cancel: %v", err)
	}
	if decision.State != OperationCancelled {
		t.Fatalf("unexpected recovery cancel decision: %#v", decision)
	}

	f.mustPrepare(t, "op-misuse", set)
	header = f.header("op-misuse")
	header.Authority = f.recoveryAuthorityValue()
	misuse := bindDigest(NewCancelPublication(header, CancelTaskOrchestration))
	_, err = f.core.Mutate(context.Background(), misuse)
	var publicationError *Error
	if !errors.As(err, &publicationError) || publicationError.Code != ErrorInvalidIntent {
		t.Fatalf("recovery misuse error = %v, want invalid intent", err)
	}
}

// TestReconcileInspectAndCompleteVerification proves reconcile only inspects
// or replays the original operation: it can complete a pending verification
// once the Durable Object capability becomes current, and it can never
// allocate a new identity, modify the manifest or parent, or create a retry.
func TestReconcileInspectAndCompleteVerification(t *testing.T) {
	f := newFixture(t)
	operationID := "op-ambiguous"
	member := f.deckMemberSpec()
	set := f.buildEvidence(t, []ArtifactMemberSpec{member})

	// The capability is registered but not currently valid: verify must
	// fail closed as ambiguous and require reconciliation.
	capability, _ := f.registry.facts[set.capabilities[0].ID]
	f.registry.register(capability, false)

	prepare := f.mustPrepare(t, operationID, set)
	verify, err := f.core.Mutate(context.Background(), f.verifyIntent(operationID, f.verifyPayload(set)))
	if err != nil {
		t.Fatalf("verify ambiguous: %v", err)
	}
	if verify.State != OperationReconciliationRequired {
		t.Fatalf("expected reconciliation required, got %#v", verify)
	}
	if verify.Verification == nil || verify.Verification.State != VerificationAmbiguous {
		t.Fatalf("expected ambiguous verification, got %#v", verify.Verification)
	}

	// Inspect must not change state.
	inspect, err := f.core.Mutate(context.Background(), f.reconcileIntent(operationID, ReconcileInspect))
	if err != nil {
		t.Fatalf("reconcile inspect: %v", err)
	}
	if inspect.State != OperationReconciliationRequired || inspect.ArtifactVersionID != prepare.ArtifactVersionID {
		t.Fatalf("unexpected inspect decision: %#v", inspect)
	}

	// Still ambiguous after the first completion attempt.
	still, err := f.core.Mutate(context.Background(), f.reconcileIntent(operationID, ReconcileCompleteVerification))
	if err != nil {
		t.Fatalf("reconcile complete verification: %v", err)
	}
	if still.State != OperationReconciliationRequired {
		t.Fatalf("expected still reconciliation required, got %#v", still)
	}

	// The Durable Object capability becomes current; reconcile completes the
	// pending verification using only the original references.
	f.registry.register(capability, true)
	completed, err := f.core.Mutate(context.Background(), f.reconcileIntent(operationID, ReconcileCompleteVerification))
	if err != nil {
		t.Fatalf("reconcile complete verification after currency: %v", err)
	}
	if completed.State != OperationVerified ||
		completed.ArtifactVersionID != prepare.ArtifactVersionID ||
		completed.ManifestDigest != prepare.ManifestDigest {
		t.Fatalf("unexpected completed reconciliation: %#v", completed)
	}

	// Reconcile can never change the candidate identity or manifest.
	view, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryCandidate, PolicyDomainID: f.policyDomain, TaskID: f.taskID, ArtifactVersionID: prepare.ArtifactVersionID,
	})
	if err != nil {
		t.Fatalf("query candidate: %v", err)
	}
	if view.ManifestDigest != prepare.ManifestDigest || view.LineageDigest != prepare.LineageDigest {
		t.Fatalf("reconcile modified the candidate: %#v", view)
	}
}

// TestReconcileConfirmTerminalReplaysOnlyOriginalOperation proves the
// confirm modes only replay the original operation's terminal decision.
func TestReconcileConfirmTerminalReplaysOnlyOriginalOperation(t *testing.T) {
	f := newFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	f.mustPrepare(t, "op-r1", set)
	if _, err := f.core.Mutate(context.Background(), f.cancelIntent("op-r1", CancelTaskOrchestration)); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	confirmed, err := f.core.Mutate(context.Background(), f.reconcileIntent("op-r1", ReconcileConfirmCancellation))
	if err != nil {
		t.Fatalf("reconcile confirm cancellation: %v", err)
	}
	if confirmed.State != OperationCancelled {
		t.Fatalf("unexpected confirm cancellation decision: %#v", confirmed)
	}

	// Confirm rejection on a cancelled operation is a terminal conflict.
	_, err = f.core.Mutate(context.Background(), f.reconcileIntent("op-r1", ReconcileConfirmRejection))
	var publicationError *Error
	if !errors.As(err, &publicationError) || publicationError.Code != ErrorTerminalConflict {
		t.Fatalf("confirm rejection on cancelled error = %v, want terminal conflict", err)
	}

	// Reconcile on an unknown operation is not-found.
	_, err = f.core.Mutate(context.Background(), f.reconcileIntent("op-unknown", ReconcileInspect))
	if !errors.As(err, &publicationError) || publicationError.Code != ErrorNotFound {
		t.Fatalf("reconcile unknown error = %v, want not found", err)
	}
}

// TestQueryIsPureReadOnly proves Query never mutates state: repeated
// operation, candidate, and stream queries return identical views and leave
// the stream untouched.
func TestQueryIsPureReadOnly(t *testing.T) {
	f := newFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	f.mustPrepare(t, "op-ro", set)
	f.mustVerify(t, "op-ro", set)

	before, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryTaskStream, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if err != nil {
		t.Fatalf("query stream before: %v", err)
	}
	for index := 0; index < 3; index++ {
		view, queryErr := f.core.Query(context.Background(), PublicationQuery{
			Kind: QueryOperation, PolicyDomainID: f.policyDomain, TaskID: f.taskID, OperationID: "op-ro",
		})
		if queryErr != nil {
			t.Fatalf("query operation: %v", queryErr)
		}
		if view.State != OperationVerified || len(view.Members) != 1 {
			t.Fatalf("unexpected operation view: %#v", view)
		}
	}
	after, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryTaskStream, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if err != nil {
		t.Fatalf("query stream after: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("Query mutated the stream: before=%#v after=%#v", before, after)
	}
}

// TestManualEditWithoutActivatedParentFailsClosed proves a manual-edit
// prepare requires an exact activated parent matching the expected current
// head; until activation exists it fails closed instead of fabricating
// lineage.
func TestManualEditWithoutActivatedParentFailsClosed(t *testing.T) {
	f := newFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	payload := f.preparePayload("op-manual", set, []ArtifactMemberSpec{f.deckMemberSpec()})
	payload.Kind = PublicationKindManualEdit
	payload.Parent = "artifact-version-unknown"

	// The caller declares the expected head equal to the parent, but no
	// activated parent exists in this authority: the stream head is empty
	// and the prepare must fail closed as stale.
	header := f.header("op-manual")
	header.ExpectedHead = "artifact-version-unknown"
	intent := bindDigest(NewPreparePublication(header, payload))

	_, err := f.core.Mutate(context.Background(), intent)
	var publicationError *Error
	if !errors.As(err, &publicationError) || publicationError.Code != ErrorStaleAuthority {
		t.Fatalf("manual edit without activated parent error = %v, want stale authority", err)
	}
}

// TestFirstGenerationPrepareRejectsParent proves first-generation candidates
// cannot declare a parent.
func TestFirstGenerationPrepareRejectsParent(t *testing.T) {
	f := newFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	payload := f.preparePayload("op-parent", set, []ArtifactMemberSpec{f.deckMemberSpec()})
	payload.Parent = "artifact-version-1"

	_, err := f.core.Mutate(context.Background(), f.prepareIntent("op-parent", payload))
	var publicationError *Error
	if !errors.As(err, &publicationError) || publicationError.Code != ErrorInvalidIntent {
		t.Fatalf("first generation with parent error = %v, want invalid intent", err)
	}
}

// TestUnknownOperationVerifyIsNotFound proves verify/reject/cancel/reconcile
// always reference an original operation and never enumerate other scopes.
func TestUnknownOperationVerifyIsNotFound(t *testing.T) {
	f := newFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})

	_, err := f.core.Mutate(context.Background(), f.verifyIntent("op-never-prepared", f.verifyPayload(set)))
	var publicationError *Error
	if !errors.As(err, &publicationError) || publicationError.Code != ErrorNotFound {
		t.Fatalf("verify unknown error = %v, want not found", err)
	}
}
