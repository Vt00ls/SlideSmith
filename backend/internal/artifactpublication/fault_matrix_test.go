package artifactpublication

import (
	"context"
	"errors"
	"testing"
)

// faultFixture wires a FaultHook that fails at exactly one point for one
// operation identity and one intent kind, and records every injected event.
type faultFixture struct {
	*fixture
	faultAt     FaultPoint
	operationID string
	intentKind  PublicationIntentKind
	failed      []FaultEvent
}

func newFaultFixture(t *testing.T, point FaultPoint, operationID string, intentKind PublicationIntentKind) *faultFixture {
	t.Helper()
	f := newFixture(t)
	ff := &faultFixture{fixture: f, faultAt: point, operationID: operationID, intentKind: intentKind}
	fired := false
	ff.core = NewInMemory(InMemoryConfig{
		Now:                          func() Instant { return f.now },
		RuntimeAuthorityID:           f.runtimeAuthority,
		ValidationAuthorityID:        f.validationAuthority,
		C04AuthorityID:               f.c04Authority,
		DurableObjectAuthorityID:     f.durableObjectAuthority,
		TaskOrchestrationAuthorityID: f.taskOrchestrationAuthority,
		RecoveryAuthorityID:          f.recoveryAuthority,
		CurrentContentCapability:     f.registry.resolve,
		FaultHook: func(event FaultEvent) error {
			ff.failed = append(ff.failed, event)
			if event.Point == point && string(event.OperationID) == operationID && event.IntentKind == intentKind && !fired {
				fired = true
				return errors.New("injected fault")
			}
			return nil
		},
	}, f.persistence)
	return ff
}

// TestPrepareFaultBeforeJournalLeavesNoOperation proves a crash before the
// operation journal leaves no trace: the retry re-runs as a fresh prepare.
func TestPrepareFaultBeforeJournalLeavesNoOperation(t *testing.T) {
	ff := newFaultFixture(t, FaultBeforeOperationJournal, "op-fault", IntentPreparePublication)
	set := ff.buildEvidence(t, []ArtifactMemberSpec{ff.deckMemberSpec()})

	_, err := ff.core.Mutate(context.Background(), ff.prepareIntent("op-fault", ff.preparePayload("op-fault", set, []ArtifactMemberSpec{ff.deckMemberSpec()})))
	if err == nil {
		t.Fatal("fault before journal must abort the prepare")
	}
	view, queryErr := ff.core.Query(context.Background(), PublicationQuery{
		Kind: QueryTaskStream, PolicyDomainID: ff.policyDomain, TaskID: ff.taskID,
	})
	if !isCode(queryErr, ErrorNotFound) {
		t.Fatalf("stream after crash-before-journal = %#v err=%v, want not found (no trace)", view, queryErr)
	}

	// The retry re-runs cleanly.
	decision, err := ff.core.Mutate(context.Background(), ff.prepareIntent("op-fault", ff.preparePayload("op-fault", set, []ArtifactMemberSpec{ff.deckMemberSpec()})))
	if err != nil {
		t.Fatalf("retry prepare after crash: %v", err)
	}
	if decision.State != OperationPrepared || decision.ArtifactVersionID == "" {
		t.Fatalf("unexpected retry decision: %#v", decision)
	}
}

// TestPrepareFaultBeforeResponseDurableReplay proves a crash after the
// candidate is durable but before the response is returned leaves the
// operation present; the retry exact-replays the original decision.
func TestPrepareFaultBeforeResponseDurableReplay(t *testing.T) {
	ff := newFaultFixture(t, FaultBeforeResponse, "op-fault-response", IntentPreparePublication)
	set := ff.buildEvidence(t, []ArtifactMemberSpec{ff.deckMemberSpec()})

	intent := ff.prepareIntent("op-fault-response", ff.preparePayload("op-fault-response", set, []ArtifactMemberSpec{ff.deckMemberSpec()}))
	if _, err := ff.core.Mutate(context.Background(), intent); err == nil {
		t.Fatal("fault before response must abort the call")
	}
	replayed, err := ff.core.Mutate(context.Background(), intent)
	if err != nil {
		t.Fatalf("replay after response loss: %v", err)
	}
	if !replayed.Replay || replayed.State != OperationPrepared || replayed.ArtifactVersionID == "" {
		t.Fatalf("unexpected replay decision: %#v", replayed)
	}
}

// TestFaultMatrixCoversEveryBoundary drives faults at every prepare and
// verify boundary and proves each leaves the authority in a restartable,
// deterministic state: either no trace (retry re-runs) or durable facts
// (retry exact-replays).
func TestFaultMatrixCoversEveryBoundary(t *testing.T) {
	points := []struct {
		point       FaultPoint
		prepareOnly bool // the fault only applies to prepare in this case
	}{
		{FaultBeforeOperationJournal, true},
		{FaultBeforeCandidatePersistence, true},
		{FaultBeforeResponse, true},
	}

	for _, test := range points {
		t.Run("prepare "+faultPointName(test.point), func(t *testing.T) {
			ff := newFaultFixture(t, test.point, "op-fault-matrix", IntentPreparePublication)
			set := ff.buildEvidence(t, []ArtifactMemberSpec{ff.deckMemberSpec()})
			intent := ff.prepareIntent("op-fault-matrix", ff.preparePayload("op-fault-matrix", set, []ArtifactMemberSpec{ff.deckMemberSpec()}))
			if _, err := ff.core.Mutate(context.Background(), intent); err == nil {
				t.Fatal("fault must abort the prepare")
			}
			decision, err := ff.core.Mutate(context.Background(), intent)
			if err != nil {
				t.Fatalf("retry prepare: %v", err)
			}
			if decision.State != OperationPrepared {
				t.Fatalf("unexpected retry decision: %#v", decision)
			}
		})
	}

	verifyPoints := []FaultPoint{
		FaultBeforeEvidenceAcceptance,
		FaultBeforeVerificationResult,
		FaultBeforeResponse,
	}
	for _, point := range verifyPoints {
		t.Run("verify "+faultPointName(point), func(t *testing.T) {
			ff := newFaultFixture(t, point, "op-fault-verify", IntentVerifyPublication)
			set := ff.buildEvidence(t, []ArtifactMemberSpec{ff.deckMemberSpec()})
			ff.mustPrepare(t, "op-fault-verify", set)
			verifyIntent := ff.verifyIntent("op-fault-verify", ff.verifyPayload(set))
			if _, err := ff.core.Mutate(context.Background(), verifyIntent); err == nil {
				t.Fatal("fault must abort the verify")
			}
			decision, err := ff.core.Mutate(context.Background(), verifyIntent)
			if err != nil {
				t.Fatalf("retry verify: %v", err)
			}
			if decision.State != OperationVerified {
				t.Fatalf("unexpected retry decision: %#v", decision)
			}
		})
	}
}

func faultPointName(point FaultPoint) string {
	switch point {
	case FaultBeforeOperationJournal:
		return "before_operation_journal"
	case FaultBeforeCandidatePersistence:
		return "before_candidate_persistence"
	case FaultBeforeEvidenceAcceptance:
		return "before_evidence_acceptance"
	case FaultBeforeVerificationResult:
		return "before_verification_result"
	case FaultBeforeActivationCommit:
		return "before_activation_commit"
	case FaultBeforeResponse:
		return "before_response"
	case FaultAfterResponse:
		return "after_response"
	default:
		return "unknown"
	}
}
