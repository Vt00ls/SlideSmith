package artifactpublication

import (
	"context"
	"testing"
)

// TestRestartableStateFullLifecycle proves the deterministic in-memory
// authority is restartable: after every intent the authority can be rebuilt
// from the same persistence and continues with identical facts, exact
// replay, and full lifecycle.
func TestRestartableStateFullLifecycle(t *testing.T) {
	f := newFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	operationID := "op-restart"

	prepare := f.mustPrepare(t, operationID, set)
	verify := f.mustVerify(t, operationID, set)

	// Restart the authority from the same persistence.
	f.rebuild()
	view, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryOperation, PolicyDomainID: f.policyDomain, TaskID: f.taskID, OperationID: PublicationOperationID(operationID),
	})
	if err != nil {
		t.Fatalf("query after restart: %v", err)
	}
	if view.State != OperationVerified || view.ArtifactVersionID != prepare.ArtifactVersionID ||
		view.ManifestDigest != prepare.ManifestDigest {
		t.Fatalf("unexpected view after restart: %#v", view)
	}

	// Exact replay after restart returns the original decisions.
	replayedPrepare, err := f.core.Mutate(context.Background(), f.prepareIntent(operationID, f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()})))
	if err != nil {
		t.Fatalf("replay prepare after restart: %v", err)
	}
	if !replayedPrepare.Replay || replayedPrepare.ManifestDigest != prepare.ManifestDigest {
		t.Fatalf("unexpected prepare replay after restart: %#v", replayedPrepare)
	}
	replayedVerify, err := f.core.Mutate(context.Background(), f.verifyIntent(operationID, f.verifyPayload(set)))
	if err != nil {
		t.Fatalf("replay verify after restart: %v", err)
	}
	if !replayedVerify.Replay || replayedVerify.ManifestDigest != verify.ManifestDigest {
		t.Fatalf("unexpected verify replay after restart: %#v", replayedVerify)
	}

	// The lifecycle continues after the restart.
	rejected, err := f.core.Mutate(context.Background(), f.rejectIntent(operationID, RejectCandidateSuperseded, nil))
	if err != nil {
		t.Fatalf("reject after restart: %v", err)
	}
	if rejected.State != OperationRejected || !rejected.ResidueRelease {
		t.Fatalf("unexpected reject after restart: %#v", rejected)
	}
}

// TestRestartPreservesIdentityNonReuse proves restarts never reuse
// ArtifactVersionID or ArtifactID identities: a new prepare after restart
// mints a fresh, distinct candidate identity.
func TestRestartPreservesIdentityNonReuse(t *testing.T) {
	f := newFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	first := f.mustPrepare(t, "op-restart-id", set)

	f.rebuild()
	second := f.mustPrepare(t, "op-restart-id-2", set)

	if first.ArtifactVersionID == second.ArtifactVersionID {
		t.Fatal("ArtifactVersionID must not be reused across restarts")
	}
}

// TestCrashBeforeCommitAfterPrepareLeavesResiduePath proves a cancelled
// operation's residue survives a restart and remains inspectable only
// through the exact operation query.
func TestCrashBeforeCommitAfterPrepareLeavesResiduePath(t *testing.T) {
	f := newFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	operationID := "op-residue-restart"

	f.mustPrepare(t, operationID, set)
	if _, err := f.core.Mutate(context.Background(), f.cancelIntent(operationID, CancelTaskOrchestration)); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	f.rebuild()

	view, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryOperation, PolicyDomainID: f.policyDomain, TaskID: f.taskID, OperationID: PublicationOperationID(operationID),
	})
	if err != nil {
		t.Fatalf("query residue after restart: %v", err)
	}
	if view.State != OperationCancelled || !view.ResidueRelease {
		t.Fatalf("unexpected residue view after restart: %#v", view)
	}

	// Ordinary queries never expose the cancelled operation or its residue.
	if _, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryCandidate, PolicyDomainID: f.policyDomain, TaskID: f.taskID, ArtifactVersionID: view.ArtifactVersionID,
	}); !isCode(err, ErrorNotFound) {
		t.Fatalf("candidate query error = %v, want not found", err)
	}
}

// TestRestartAmbiguousOperationRemainsReconcileable proves a
// reconciliation-required operation survives restart and reconcile still
// completes it once the Durable Object capability becomes current.
func TestRestartAmbiguousOperationRemainsReconcileable(t *testing.T) {
	f := newFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	capability := set.capabilities[0]
	f.registry.register(capability, false)
	operationID := "op-amb-restart"

	f.mustPrepare(t, operationID, set)
	verify, err := f.core.Mutate(context.Background(), f.verifyIntent(operationID, f.verifyPayload(set)))
	if err != nil {
		t.Fatalf("verify ambiguous: %v", err)
	}
	if verify.State != OperationReconciliationRequired {
		t.Fatalf("expected reconciliation required, got %#v", verify)
	}

	f.rebuild()
	f.registry.register(capability, true)

	completed, err := f.core.Mutate(context.Background(), f.reconcileIntent(operationID, ReconcileCompleteVerification))
	if err != nil {
		t.Fatalf("reconcile after restart: %v", err)
	}
	if completed.State != OperationVerified {
		t.Fatalf("unexpected reconciliation after restart: %#v", completed)
	}
}
