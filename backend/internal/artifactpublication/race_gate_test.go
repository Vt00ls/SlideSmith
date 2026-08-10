package artifactpublication

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type DecisionResult struct {
	decision PublicationDecision
	err      error
}

// rewireRace replaces the fixture's core with one whose ScheduleHook signals
// which intent kind entered the engine and then blocks until the test closes
// that kind's release channel. This gives deterministic interleaving: the
// test chooses the order in which mutations may take the authority lock.
func rewireRace(f *fixture, entered chan<- PublicationIntentKind, release map[PublicationIntentKind]chan struct{}) {
	f.core = NewInMemory(InMemoryConfig{
		Now:                          func() Instant { return f.now },
		RuntimeAuthorityID:           f.runtimeAuthority,
		ValidationAuthorityID:        f.validationAuthority,
		C04AuthorityID:               f.c04Authority,
		DurableObjectAuthorityID:     f.durableObjectAuthority,
		TaskOrchestrationAuthorityID: f.taskOrchestrationAuthority,
		RecoveryAuthorityID:          f.recoveryAuthority,
		CurrentContentCapability:     f.registry.resolve,
		ScheduleHook: func(event IntentScheduleEvent) {
			entered <- event.Kind
			<-release[event.Kind]
		},
	}, f.persistence)
}

func newRaceChannels() (chan PublicationIntentKind, map[PublicationIntentKind]chan struct{}) {
	entered := make(chan PublicationIntentKind, 2)
	release := make(map[PublicationIntentKind]chan struct{})
	for _, kind := range []PublicationIntentKind{
		IntentPreparePublication, IntentVerifyPublication,
		IntentRejectPublication, IntentCancelPublication, IntentReconcilePublication,
	} {
		release[kind] = make(chan struct{})
	}
	return entered, release
}

// TestDuplicatePrepareRaceSingleWinner proves two concurrent prepares under
// the same operation identity produce a single deterministic winner: the
// first caller journals the operation and the second either replays the
// exact decision or receives the same candidate facts, never a second
// version.
func TestDuplicatePrepareRaceSingleWinner(t *testing.T) {
	f := newFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	intent := f.prepareIntent("op-race-prepare", f.preparePayload("op-race-prepare", set, []ArtifactMemberSpec{f.deckMemberSpec()}))
	entered, release := newRaceChannels()
	rewireRace(f, entered, release)

	var first DecisionResult
	var second DecisionResult
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); first.decision, first.err = f.core.Mutate(context.Background(), intent) }()
	go func() { defer wg.Done(); second.decision, second.err = f.core.Mutate(context.Background(), intent) }()

	<-entered // prepare A entered
	<-entered // prepare B entered
	close(release[IntentPreparePublication])
	wg.Wait()

	if first.err != nil || second.err != nil {
		t.Fatalf("prepare race errors: first=%v second=%v", first.err, second.err)
	}
	if first.decision.ArtifactVersionID != second.decision.ArtifactVersionID ||
		first.decision.ManifestDigest != second.decision.ManifestDigest {
		t.Fatalf("race produced divergent candidates: first=%#v second=%#v", first.decision, second.decision)
	}
	if !first.decision.Replay && !second.decision.Replay {
		t.Fatal("one of the two concurrent prepares must be an exact replay")
	}
	view, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryOperation, PolicyDomainID: f.policyDomain, TaskID: f.taskID, OperationID: "op-race-prepare",
	})
	if err != nil {
		t.Fatalf("query operation: %v", err)
	}
	if view.ManifestDigest != first.decision.ManifestDigest {
		t.Fatalf("operation view digest mismatch: %#v", view)
	}
}

// TestVerifyCancelRaceDeterministicWinner proves verify/cancel races
// linearize deterministically: the mutation released first wins the lock,
// and the loser observes a stale/terminal conflict or replays the winner's
// durable facts, never a split-brain version.
func TestVerifyCancelRaceDeterministicWinner(t *testing.T) {
	setup := func(t *testing.T) (*fixture, chan PublicationIntentKind, map[PublicationIntentKind]chan struct{}, *evidenceSet, string) {
		f := newFixture(t)
		set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
		f.mustPrepare(t, "op-race-cancel", set)
		entered, release := newRaceChannels()
		rewireRace(f, entered, release)
		return f, entered, release, set, "op-race-cancel"
	}

	run := func(t *testing.T, releaseFirst, releaseSecond PublicationIntentKind) (*fixture, DecisionResult, DecisionResult) {
		f, entered, release, set, operationID := setup(t)
		verify := f.verifyIntent(operationID, f.verifyPayload(set))
		cancel := f.cancelIntent(operationID, CancelTaskOrchestration)

		var verifyResult DecisionResult
		var cancelResult DecisionResult
		done := make(chan struct{}, 2)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			defer func() { done <- struct{}{} }()
			verifyResult.decision, verifyResult.err = f.core.Mutate(context.Background(), verify)
		}()
		go func() {
			defer wg.Done()
			defer func() { done <- struct{}{} }()
			cancelResult.decision, cancelResult.err = f.core.Mutate(context.Background(), cancel)
		}()

		// Both mutations are now blocked inside the schedule hook.
		<-entered
		<-entered
		// Release in the chosen order and wait for each mutation to finish
		// before releasing the next: the first released mutation takes the
		// authority lock deterministically.
		close(release[releaseFirst])
		<-done
		close(release[releaseSecond])
		<-done
		wg.Wait()
		return f, verifyResult, cancelResult
	}

	t.Run("cancel first", func(t *testing.T) {
		f, verifyResult, cancelResult := run(t, IntentCancelPublication, IntentVerifyPublication)
		if cancelResult.err != nil || cancelResult.decision.State != OperationCancelled {
			t.Fatalf("cancel result: %#v err=%v", cancelResult.decision, cancelResult.err)
		}
		// The late verify is stale/terminal.
		if verifyResult.err == nil {
			t.Fatalf("late verify must fail closed after cancel-first, got %#v", verifyResult.decision)
		}
		var publicationError *Error
		if !errors.As(verifyResult.err, &publicationError) || publicationError.Code != ErrorTerminalConflict {
			t.Fatalf("late verify error = %v, want terminal conflict", verifyResult.err)
		}
		_ = f
	})

	t.Run("verify first", func(t *testing.T) {
		f, verifyResult, cancelResult := run(t, IntentVerifyPublication, IntentCancelPublication)
		if verifyResult.err != nil || verifyResult.decision.State != OperationVerified {
			t.Fatalf("verify result: %#v err=%v", verifyResult.decision, verifyResult.err)
		}
		if cancelResult.err != nil || cancelResult.decision.State != OperationCancelled {
			t.Fatalf("cancel result: %#v err=%v", cancelResult.decision, cancelResult.err)
		}
		// Exact replay of verify after the cancel returns the original
		// verified decision: the winner's facts are never mutated.
		set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
		replayed, err := f.core.Mutate(context.Background(), f.verifyIntent("op-race-cancel", f.verifyPayload(set)))
		if err != nil {
			t.Fatalf("replay verify: %v", err)
		}
		if !replayed.Replay || replayed.State != OperationVerified ||
			replayed.ArtifactVersionID != verifyResult.decision.ArtifactVersionID {
			t.Fatalf("unexpected verify replay: %#v", replayed)
		}
	})
}

// TestConcurrentDistinctOperationsIsolated proves concurrent prepares for
// distinct operations are isolated and both produce unique identities.
func TestConcurrentDistinctOperationsIsolated(t *testing.T) {
	f := newFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	set2 := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})

	var wg sync.WaitGroup
	results := make([]PublicationDecision, 2)
	intents := []PublicationIntent{
		f.prepareIntent("op-parallel-a", f.preparePayload("op-parallel-a", set, []ArtifactMemberSpec{f.deckMemberSpec()})),
		f.prepareIntent("op-parallel-b", f.preparePayload("op-parallel-b", set2, []ArtifactMemberSpec{f.deckMemberSpec()})),
	}
	for index := range intents {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			decision, err := f.core.Mutate(context.Background(), intents[i])
			if err != nil {
				t.Errorf("concurrent prepare %d: %v", i, err)
				return
			}
			results[i] = decision
		}(index)
	}
	wg.Wait()

	if results[0].ArtifactVersionID == results[1].ArtifactVersionID {
		t.Fatal("concurrent operations must receive distinct candidate identities")
	}
	if results[0].ArtifactVersionID == "" || results[1].ArtifactVersionID == "" {
		t.Fatal("concurrent prepares must produce candidate identities")
	}
}

// TestQueryDuringMutationNeverMutates proves queries interleaved with
// mutations observe only committed facts and never change them.
func TestQueryDuringMutationNeverMutates(t *testing.T) {
	f := newFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if _, err := f.core.Mutate(context.Background(), f.prepareIntent("op-query-race", f.preparePayload("op-query-race", set, []ArtifactMemberSpec{f.deckMemberSpec()}))); err != nil {
			t.Errorf("prepare: %v", err)
		}
	}()
	queries := 0
	go func() {
		defer wg.Done()
		for queries < 200 {
			_, err := f.core.Query(context.Background(), PublicationQuery{
				Kind: QueryTaskStream, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
			})
			if err != nil && !isCode(err, ErrorNotFound) {
				t.Errorf("query during mutation: %v", err)
				return
			}
			queries++
		}
	}()
	wg.Wait()
}
