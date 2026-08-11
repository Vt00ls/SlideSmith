package artifactpublication

import (
	"context"
	"testing"
)

// TestActivationFaultBeforeCommitLeavesNoVersion proves a crash after the
// activation revalidation but before the commit leaves no version, no head
// advance, and no stream revision; the retry re-runs the activation cleanly
// against the unchanged stream.
func TestActivationFaultBeforeCommitLeavesNoVersion(t *testing.T) {
	ff := newFaultFixture(t, FaultBeforeActivationCommit, "op-fault-activate", IntentActivatePublication)
	set := ff.buildEvidence(t, []ArtifactMemberSpec{ff.deckMemberSpec()})
	ff.mustPrepare(t, "op-fault-activate", set)
	ff.mustVerify(t, "op-fault-activate", set)

	if _, err := ff.core.Mutate(context.Background(), ff.activateIntent("op-fault-activate")); err == nil {
		t.Fatal("fault before activation commit must abort the activation")
	}
	stream, queryErr := ff.core.Query(context.Background(), PublicationQuery{
		Kind: QueryTaskStream, PolicyDomainID: ff.policyDomain, TaskID: ff.taskID,
	})
	if queryErr != nil {
		t.Fatalf("query stream after abort: %v", queryErr)
	}
	if stream.StreamRevision != 0 || stream.CurrentHead != "" {
		t.Fatalf("crash before commit must leave the stream untouched: %#v", stream)
	}

	// The retry re-runs and commits exactly one version.
	activated, err := ff.core.Mutate(context.Background(), ff.activateIntent("op-fault-activate"))
	if err != nil {
		t.Fatalf("retry activation: %v", err)
	}
	if activated.State != OperationActivated || activated.StreamRevision != 1 ||
		activated.ActivationEvidence == nil {
		t.Fatalf("unexpected retry activation: %#v", activated)
	}
	history, err := ff.core.Query(context.Background(), PublicationQuery{
		Kind: QueryVersionHistory, PolicyDomainID: ff.policyDomain, TaskID: ff.taskID,
	})
	if err != nil {
		t.Fatalf("query history: %v", err)
	}
	if len(history.History) != 1 {
		t.Fatalf("retry must create exactly one version: %#v", history)
	}
	_ = set
}

// TestActivationFaultBeforeResponseDurableReplayProves a crash after the
// activation commit but before the response leaves the version durable; the
// retry exact-replays the original committed decision and never creates a
// second version.
func TestActivationFaultBeforeResponseDurableReplay(t *testing.T) {
	ff := newFaultFixture(t, FaultBeforeResponse, "op-fault-activate-response", IntentActivatePublication)
	set := ff.buildEvidence(t, []ArtifactMemberSpec{ff.deckMemberSpec()})
	ff.mustPrepare(t, "op-fault-activate-response", set)
	ff.mustVerify(t, "op-fault-activate-response", set)

	intent := ff.activateIntent("op-fault-activate-response")
	if _, err := ff.core.Mutate(context.Background(), intent); err == nil {
		t.Fatal("fault before response must abort the call")
	}
	replayed, err := ff.core.Mutate(context.Background(), intent)
	if err != nil {
		t.Fatalf("replay after response loss: %v", err)
	}
	if !replayed.Replay || replayed.State != OperationActivated || replayed.StreamRevision != 1 {
		t.Fatalf("unexpected replay decision: %#v", replayed)
	}
	if replayed.ActivationEvidence == nil {
		t.Fatal("replayed activation must carry the committed evidence")
	}

	// Exactly one committed version, the head advanced once.
	history, err := ff.core.Query(context.Background(), PublicationQuery{
		Kind: QueryVersionHistory, PolicyDomainID: ff.policyDomain, TaskID: ff.taskID,
	})
	if err != nil {
		t.Fatalf("query history: %v", err)
	}
	if len(history.History) != 1 || history.CurrentHead != replayed.ArtifactVersionID {
		t.Fatalf("response loss must not create a second version: %#v", history)
	}
	_ = set
}

// TestActivationFaultMatrixCoversEveryBoundary drives activation faults at
// every activation boundary: before commit and before response, proving
// each leaves the authority restartable and deterministic.
func TestActivationFaultMatrixCoversEveryBoundary(t *testing.T) {
	for _, point := range []FaultPoint{FaultBeforeActivationCommit, FaultBeforeResponse} {
		t.Run(faultPointName(point), func(t *testing.T) {
			ff := newFaultFixture(t, point, "op-fault-matrix-activate", IntentActivatePublication)
			set := ff.buildEvidence(t, []ArtifactMemberSpec{ff.deckMemberSpec()})
			ff.mustPrepare(t, "op-fault-matrix-activate", set)
			ff.mustVerify(t, "op-fault-matrix-activate", set)
			intent := ff.activateIntent("op-fault-matrix-activate")
			if _, err := ff.core.Mutate(context.Background(), intent); err == nil {
				t.Fatal("fault must abort the activation")
			}
			decision, err := ff.core.Mutate(context.Background(), intent)
			if err != nil {
				t.Fatalf("retry activation: %v", err)
			}
			if decision.State != OperationActivated {
				t.Fatalf("unexpected retry decision: %#v", decision)
			}
		})
	}
}

// TestActivationFaultRestartConsistencyProves activation fault boundaries
// are restart-safe: after a fault the authority can be rebuilt and the
// retry commits the same single version.
func TestActivationFaultRestartConsistency(t *testing.T) {
	ff := newFaultFixture(t, FaultBeforeActivationCommit, "op-fault-activate-restart", IntentActivatePublication)
	set := ff.buildEvidence(t, []ArtifactMemberSpec{ff.deckMemberSpec()})
	ff.mustPrepare(t, "op-fault-activate-restart", set)
	ff.mustVerify(t, "op-fault-activate-restart", set)

	if _, err := ff.core.Mutate(context.Background(), ff.activateIntent("op-fault-activate-restart")); err == nil {
		t.Fatal("fault must abort the activation")
	}
	ff.rebuild()

	activated, err := ff.core.Mutate(context.Background(), ff.activateIntent("op-fault-activate-restart"))
	if err != nil {
		t.Fatalf("retry activation after restart: %v", err)
	}
	if activated.State != OperationActivated || activated.StreamRevision != 1 {
		t.Fatalf("unexpected retry after restart: %#v", activated)
	}
	// A second replay must not create a second version.
	replayed, err := ff.core.Mutate(context.Background(), ff.activateIntent("op-fault-activate-restart"))
	if err != nil {
		t.Fatalf("replay after restart: %v", err)
	}
	if !replayed.Replay || replayed.ArtifactVersionID != activated.ArtifactVersionID {
		t.Fatalf("unexpected replay: %#v", replayed)
	}
}
