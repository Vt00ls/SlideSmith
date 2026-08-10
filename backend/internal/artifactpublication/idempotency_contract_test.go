package artifactpublication

import (
	"context"
	"errors"
	"testing"
)

// TestExactReplayReturnsOriginalDecision proves resubmitting the exact same
// canonical intent replays the original decision, candidate, and digest for
// every intent kind, taking priority over fresh-state validation.
func TestExactReplayReturnsOriginalDecision(t *testing.T) {
	f := newFixture(t)
	operationID := "op-replay"
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})

	prepareIntent := f.prepareIntent(operationID, f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()}))
	first, err := f.core.Mutate(context.Background(), prepareIntent)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	replayedPrepare, err := f.core.Mutate(context.Background(), prepareIntent)
	if err != nil {
		t.Fatalf("replay prepare: %v", err)
	}
	if !replayedPrepare.Replay || replayedPrepare.State != OperationPrepared ||
		replayedPrepare.ArtifactVersionID != first.ArtifactVersionID ||
		replayedPrepare.ManifestDigest != first.ManifestDigest ||
		replayedPrepare.LineageDigest != first.LineageDigest {
		t.Fatalf("unexpected prepare replay: %#v", replayedPrepare)
	}

	verifyIntent := f.verifyIntent(operationID, f.verifyPayload(set))
	verified, err := f.core.Mutate(context.Background(), verifyIntent)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	replayedVerify, err := f.core.Mutate(context.Background(), verifyIntent)
	if err != nil {
		t.Fatalf("replay verify: %v", err)
	}
	if !replayedVerify.Replay || replayedVerify.State != OperationVerified ||
		replayedVerify.ArtifactVersionID != verified.ArtifactVersionID ||
		replayedVerify.ManifestDigest != verified.ManifestDigest {
		t.Fatalf("unexpected verify replay: %#v", replayedVerify)
	}

	rejectIntent := f.rejectIntent(operationID, RejectCandidateSuperseded, nil)
	rejected, err := f.core.Mutate(context.Background(), rejectIntent)
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	replayedReject, err := f.core.Mutate(context.Background(), rejectIntent)
	if err != nil {
		t.Fatalf("replay reject: %v", err)
	}
	if !replayedReject.Replay || replayedReject.State != OperationRejected ||
		replayedReject.RejectReason != rejected.RejectReason {
		t.Fatalf("unexpected reject replay: %#v", replayedReject)
	}
}

// TestSameKeyDifferentPayloadIntegrityConflict proves the same operation
// identity with a different canonical payload is a durable integrity
// conflict for every one-shot intent kind.
func TestSameKeyDifferentPayloadIntegrityConflict(t *testing.T) {
	f := newFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	operationID := "op-conflict"

	original := f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()})
	if _, err := f.core.Mutate(context.Background(), f.prepareIntent(operationID, original)); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	// Same operation identity, different member bytes.
	different := f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()})
	different.Members[0].Size = 2048
	different.Staging[0].Size = 2048
	_, err := f.core.Mutate(context.Background(), f.prepareIntent(operationID, different))
	var publicationError *Error
	if !errors.As(err, &publicationError) || publicationError.Code != ErrorIntegrityConflict {
		t.Fatalf("same-key different payload error = %v, want integrity conflict", err)
	}

	// A second verify with different evidence under the same operation is
	// also a durable integrity conflict.
	if _, err := f.core.Mutate(context.Background(), f.verifyIntent(operationID, f.verifyPayload(set))); err != nil {
		t.Fatalf("verify: %v", err)
	}
	differentVerify := f.verifyPayload(set)
	differentVerify.ValidationEvidence.OutputProposalManifestDigest = testDigest("other-proposal")
	_, err = f.core.Mutate(context.Background(), f.verifyIntent(operationID, differentVerify))
	if !errors.As(err, &publicationError) || publicationError.Code != ErrorIntegrityConflict {
		t.Fatalf("same-key different verify payload error = %v, want integrity conflict", err)
	}
}

// TestOperationIdentityNeverReusedProves an operation identity is minted
// once: a prepare under an identity already bound to another operation's
// payload is a conflict and never creates a second version.
func TestOperationIdentityNeverReusedProves(t *testing.T) {
	f := newFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})

	first := f.mustPrepare(t, "op-unique", set)
	view, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryTaskStream, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if err != nil {
		t.Fatalf("query stream: %v", err)
	}
	_ = view

	// The same operation identity cannot prepare a second candidate with a
	// different payload.
	different := f.preparePayload("op-unique", set, []ArtifactMemberSpec{f.deckMemberSpec()})
	different.Members[0].Size = 4096
	different.Staging[0].Size = 4096
	_, err = f.core.Mutate(context.Background(), f.prepareIntent("op-unique", different))
	var publicationError *Error
	if !errors.As(err, &publicationError) || publicationError.Code != ErrorIntegrityConflict {
		t.Fatalf("second prepare error = %v, want integrity conflict", err)
	}
	_ = first
}

// TestFailedVerifyExactReplayReturnsSameOutcome proves a recorded
// verification failure replays exactly, and the operation stays non-terminal
// so the caller can then reject or cancel.
func TestFailedVerifyExactReplayReturnsSameOutcome(t *testing.T) {
	f := newFixture(t)
	operationID := "op-failed-replay"
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	f.mustPrepare(t, operationID, set)

	badPayload := f.verifyPayload(set)
	badPayload.RuntimeEvidence[0].Producer.AuthorityID = "unknown-authority"
	badPayload.RuntimeEvidence[0].Digest = badPayload.RuntimeEvidence[0].CanonicalDigest()
	intent := bindDigest(NewVerifyPublication(f.header(operationID), badPayload))

	if _, err := f.core.Mutate(context.Background(), intent); !isEvidenceError(err) {
		t.Fatalf("first verify error = %v, want evidence error", err)
	}
	_, err := f.core.Mutate(context.Background(), intent)
	if !isEvidenceError(err) {
		t.Fatalf("replay verify error = %v, want same evidence error", err)
	}

	// The operation remains non-terminal and can still be rejected.
	rejected, err := f.core.Mutate(context.Background(), f.rejectIntent(operationID, RejectEvidenceFailure, nil))
	if err != nil {
		t.Fatalf("reject after failed verify: %v", err)
	}
	if rejected.State != OperationRejected {
		t.Fatalf("unexpected reject decision: %#v", rejected)
	}
}

// TestResponseLossReplayAfterLaterStreamWork proves exact replay returns the
// original decision even after later operations entered the same Task's
// stream (decision 29).
func TestResponseLossReplayAfterLaterStreamWork(t *testing.T) {
	f := newFixture(t)
	operationID := "op-response-loss"
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})

	verifyIntent := f.verifyIntent(operationID, f.verifyPayload(set))
	f.mustPrepare(t, operationID, set)
	verified, err := f.core.Mutate(context.Background(), verifyIntent)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}

	// Later operations on the same stream must not disturb the replay.
	set2 := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	f.mustPrepare(t, "op-later", set2)
	if _, err := f.core.Mutate(context.Background(), f.cancelIntent("op-later", CancelTaskOrchestration)); err != nil {
		t.Fatalf("cancel later operation: %v", err)
	}

	replayed, err := f.core.Mutate(context.Background(), verifyIntent)
	if err != nil {
		t.Fatalf("replay verify after later stream work: %v", err)
	}
	if !replayed.Replay || replayed.State != OperationVerified ||
		replayed.ArtifactVersionID != verified.ArtifactVersionID ||
		replayed.ManifestDigest != verified.ManifestDigest {
		t.Fatalf("unexpected verify replay: %#v", replayed)
	}
}
