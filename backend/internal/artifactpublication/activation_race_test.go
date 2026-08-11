package artifactpublication

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// TestActivateCancelRaceDeterministicWinner proves activate/cancel races
// linearize deterministically: when cancel commits first the late
// activation fails closed; when activation commits first the cancel returns
// the existing active terminal result and never deletes the version.
func TestActivateCancelRaceDeterministicWinner(t *testing.T) {
	setup := func(t *testing.T, operationID string) (*fixture, chan PublicationIntentKind, map[PublicationIntentKind]chan struct{}) {
		f := newFixture(t)
		f.prepareAndVerify(t, operationID)
		entered, release := newRaceChannels()
		rewireRace(f, entered, release)
		return f, entered, release
	}

	run := func(t *testing.T, releaseFirst, releaseSecond PublicationIntentKind) (*fixture, DecisionResult, DecisionResult, string) {
		operationID := "op-race-activate-cancel"
		f, entered, release := setup(t, operationID)
		activate := f.activateIntent(operationID)
		cancel := f.cancelIntent(operationID, CancelTaskOrchestration)

		var activateResult DecisionResult
		var cancelResult DecisionResult
		done := make(chan struct{}, 2)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			defer func() { done <- struct{}{} }()
			activateResult.decision, activateResult.err = f.core.Mutate(context.Background(), activate)
		}()
		go func() {
			defer wg.Done()
			defer func() { done <- struct{}{} }()
			cancelResult.decision, cancelResult.err = f.core.Mutate(context.Background(), cancel)
		}()

		<-entered
		<-entered
		close(release[releaseFirst])
		<-done
		close(release[releaseSecond])
		<-done
		wg.Wait()
		return f, activateResult, cancelResult, operationID
	}

	t.Run("cancel first", func(t *testing.T) {
		f, activateResult, cancelResult, operationID := run(t, IntentCancelPublication, IntentActivatePublication)
		if cancelResult.err != nil || cancelResult.decision.State != OperationCancelled {
			t.Fatalf("cancel result: %#v err=%v", cancelResult.decision, cancelResult.err)
		}
		// The late activation is closed and no version was created.
		if activateResult.err == nil {
			t.Fatalf("late activation must fail closed after cancel-first, got %#v", activateResult.decision)
		}
		var publicationError *Error
		if !errors.As(activateResult.err, &publicationError) || publicationError.Code != ErrorTerminalConflict {
			t.Fatalf("late activation error = %v, want terminal conflict", activateResult.err)
		}
		stream, err := f.core.Query(context.Background(), PublicationQuery{
			Kind: QueryTaskStream, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		})
		if err != nil {
			t.Fatalf("query stream: %v", err)
		}
		if stream.StreamRevision != 0 || stream.CurrentHead != "" {
			t.Fatalf("cancel-first must leave the stream empty: %#v", stream)
		}
		_ = operationID
	})

	t.Run("activate first", func(t *testing.T) {
		f, activateResult, cancelResult, _ := run(t, IntentActivatePublication, IntentCancelPublication)
		if activateResult.err != nil || activateResult.decision.State != OperationActivated {
			t.Fatalf("activate result: %#v err=%v", activateResult.decision, activateResult.err)
		}
		// The cancel observes the existing active terminal result and never
		// deletes the version or releases residue.
		if cancelResult.err != nil || cancelResult.decision.State != OperationActivated ||
			!cancelResult.decision.Replay {
			t.Fatalf("cancel after activation must replay the active terminal result: %#v err=%v",
				cancelResult.decision, cancelResult.err)
		}
		if cancelResult.decision.ResidueRelease {
			t.Fatal("cancel after activation must not release activated references as residue")
		}
		version, err := f.core.Query(context.Background(), PublicationQuery{
			Kind: QueryExactVersion, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
			ArtifactVersionID: activateResult.decision.ArtifactVersionID,
		})
		if err != nil {
			t.Fatalf("version must survive the race: %v", err)
		}
		if version.ManifestDigest != activateResult.decision.ManifestDigest {
			t.Fatalf("version mutated by cancel: %#v", version)
		}
	})
}

// TestTwoFirstVersionActivationRaceSingleWinner proves two different
// first-version activations from the same empty expected head/revision
// produce at most one winner: the loser stays non-active with a typed stale
// disposition and the stream/head reflect only the winner.
func TestTwoFirstVersionActivationRaceSingleWinner(t *testing.T) {
	f := newFixture(t)
	setA := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	setB := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	f.mustPrepare(t, "op-first-a", setA)
	f.mustPrepare(t, "op-first-b", setB)
	f.mustVerify(t, "op-first-a", setA)
	f.mustVerify(t, "op-first-b", setB)

	entered, release := newRaceChannels()
	rewireRace(f, entered, release)
	activateA := f.activateIntent("op-first-a")
	activateB := f.activateIntent("op-first-b")

	var resultA DecisionResult
	var resultB DecisionResult
	done := make(chan struct{}, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer func() { done <- struct{}{} }()
		resultA.decision, resultA.err = f.core.Mutate(context.Background(), activateA)
	}()
	go func() {
		defer wg.Done()
		defer func() { done <- struct{}{} }()
		resultB.decision, resultB.err = f.core.Mutate(context.Background(), activateB)
	}()

	<-entered
	<-entered
	close(release[IntentActivatePublication])
	wg.Wait()

	winners := 0
	losers := 0
	for _, result := range []DecisionResult{resultA, resultB} {
		if result.err == nil && result.decision.State == OperationActivated {
			winners++
		} else {
			losers++
			var publicationError *Error
			if !errors.As(result.err, &publicationError) || publicationError.Code != ErrorStaleAuthority {
				t.Fatalf("loser error = %v, want typed stale disposition", result.err)
			}
		}
	}
	if winners != 1 || losers != 1 {
		t.Fatalf("two first-version activations must have exactly one winner: winners=%d losers=%d", winners, losers)
	}

	// The stream facts reflect exactly the single winner; the loser stays
	// non-active and its candidate is not an activated version.
	stream, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryTaskStream, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if err != nil {
		t.Fatalf("query stream: %v", err)
	}
	if stream.StreamRevision != 1 {
		t.Fatalf("exactly one revision must be committed, got %d", stream.StreamRevision)
	}
	var winnerVersion ArtifactVersionID
	if resultA.err == nil {
		winnerVersion = resultA.decision.ArtifactVersionID
	} else {
		winnerVersion = resultB.decision.ArtifactVersionID
	}
	if stream.CurrentHead != winnerVersion {
		t.Fatalf("current head must be the single winner: head=%s winner=%s", stream.CurrentHead, winnerVersion)
	}
	history, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryVersionHistory, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if err != nil {
		t.Fatalf("query history: %v", err)
	}
	if len(history.History) != 1 || history.History[0].ArtifactVersionID != winnerVersion {
		t.Fatalf("history must contain exactly the winner: %#v", history)
	}
}

// TestTwoChildActivationRaceSingleWinner proves two different manual-edit
// children prepared from the same activated parent and the same expected
// head/revision produce at most one next-head winner; the loser stays
// non-active with a typed stale disposition, and the parent is never
// modified or covered.
func TestTwoChildActivationRaceSingleWinner(t *testing.T) {
	f := newFixture(t)
	_, parent := f.prepareVerifyActivate(t, "op-race-parent")

	childA := f.childEvidenceSet(t, parent.ArtifactVersionID, "op-child-a")
	childB := f.childEvidenceSet(t, parent.ArtifactVersionID, "op-child-b")
	prepareChild := func(t *testing.T, operationID string, set *evidenceSet) {
		t.Helper()
		header := f.header(operationID)
		header.ExpectedStreamRevision = 1
		header.ExpectedHead = parent.ArtifactVersionID
		intent := bindDigest(NewPreparePublication(header, f.childPreparePayload(operationID, parent.ArtifactVersionID, set)))
		if _, err := f.core.Mutate(context.Background(), intent); err != nil {
			t.Fatalf("child %s prepare: %v", operationID, err)
		}
		if _, err := f.core.Mutate(context.Background(), f.verifyIntent(operationID, f.verifyPayload(set))); err != nil {
			t.Fatalf("child %s verify: %v", operationID, err)
		}
	}
	prepareChild(t, "op-child-a", childA)
	prepareChild(t, "op-child-b", childB)

	entered, release := newRaceChannels()
	rewireRace(f, entered, release)
	headerA := f.header("op-child-a")
	headerA.ExpectedStreamRevision = 1
	headerA.ExpectedHead = parent.ArtifactVersionID
	headerB := f.header("op-child-b")
	headerB.ExpectedStreamRevision = 1
	headerB.ExpectedHead = parent.ArtifactVersionID
	activateA := f.activateIntentWithHeader(headerA)
	activateB := f.activateIntentWithHeader(headerB)

	var resultA DecisionResult
	var resultB DecisionResult
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		resultA.decision, resultA.err = f.core.Mutate(context.Background(), activateA)
	}()
	go func() {
		defer wg.Done()
		resultB.decision, resultB.err = f.core.Mutate(context.Background(), activateB)
	}()

	<-entered
	<-entered
	close(release[IntentActivatePublication])
	wg.Wait()

	winners := 0
	losers := 0
	for _, result := range []DecisionResult{resultA, resultB} {
		if result.err == nil && result.decision.State == OperationActivated {
			winners++
		} else {
			losers++
			var publicationError *Error
			if !errors.As(result.err, &publicationError) || publicationError.Code != ErrorStaleAuthority {
				t.Fatalf("child loser error = %v, want typed stale disposition", result.err)
			}
		}
	}
	if winners != 1 || losers != 1 {
		t.Fatalf("two child activations must have exactly one winner: winners=%d losers=%d", winners, losers)
	}

	var winnerVersion ArtifactVersionID
	if resultA.err == nil {
		winnerVersion = resultA.decision.ArtifactVersionID
	} else {
		winnerVersion = resultB.decision.ArtifactVersionID
	}
	stream, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryTaskStream, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if err != nil {
		t.Fatalf("query stream: %v", err)
	}
	if stream.CurrentHead != winnerVersion || stream.StreamRevision != 2 {
		t.Fatalf("child race must advance exactly once to the winner: %#v", stream)
	}

	// The parent remains immutable and queryable; the history is ordered
	// parent -> winner child and never contains the loser.
	history, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryVersionHistory, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if err != nil {
		t.Fatalf("query history: %v", err)
	}
	if len(history.History) != 2 ||
		history.History[0].ArtifactVersionID != parent.ArtifactVersionID ||
		history.History[1].ArtifactVersionID != winnerVersion {
		t.Fatalf("history must be parent then single winner child: %#v", history)
	}
	parentView, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryExactVersion, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: parent.ArtifactVersionID,
	})
	if err != nil {
		t.Fatalf("parent must remain queryable: %v", err)
	}
	if parentView.ManifestDigest != parent.ManifestDigest || len(parentView.Members) != 1 {
		t.Fatalf("parent mutated by child race: %#v", parentView)
	}
}

// TestQueryDuringActivationObservesCommittedFactsOnly proves queries
// interleaved with an atomic activation observe either the empty stream or
// the fully committed version/head, never a partial state, and never mutate
// anything.
func TestQueryDuringActivationObservesCommittedFactsOnly(t *testing.T) {
	f := newFixture(t)
	f.prepareAndVerify(t, "op-query-activate")

	entered, release := newRaceChannels()
	rewireRace(f, entered, release)

	var activation DecisionResult
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		activation.decision, activation.err = f.core.Mutate(context.Background(), f.activateIntent("op-query-activate"))
	}()

	// The activation is blocked inside the schedule hook; queries observe
	// the pre-commit state (empty stream), then after release observe the
	// committed state.
	<-entered
	before, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryTaskStream, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if err != nil {
		t.Fatalf("query before commit: %v", err)
	}
	if before.StreamRevision != 0 || before.CurrentHead != "" {
		t.Fatalf("query during activation must not observe partial commit: %#v", before)
	}
	close(release[IntentActivatePublication])
	wg.Wait()

	if activation.err != nil {
		t.Fatalf("activation: %v", activation.err)
	}
	after, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryVersionHistory, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if err != nil {
		t.Fatalf("query history after commit: %v", err)
	}
	if len(after.History) != 1 ||
		after.History[0].ArtifactVersionID != activation.decision.ArtifactVersionID ||
		after.StreamRevision != 1 {
		t.Fatalf("query after commit must observe the committed version: %#v", after)
	}
}

// TestStaleGenerationFenceActivationRaceFailsClosed proves an activation
// carrying a stale generation/fence can never advance the stream regardless
// of race ordering, and exactly one valid activation commits.
func TestStaleGenerationFenceActivationRaceFailsClosed(t *testing.T) {
	f := newFixture(t)
	f.prepareAndVerify(t, "op-race-stale")

	entered, release := newRaceChannels()
	rewireRace(f, entered, release)

	validHeader := f.header("op-race-stale")
	staleHeader := f.header("op-race-stale")
	staleHeader.Generation = f.generation + 99
	staleHeader.Fence = f.fence + 99
	valid := f.activateIntentWithHeader(validHeader)
	stale := f.activateIntentWithHeader(staleHeader)

	var validResult DecisionResult
	var staleResult DecisionResult
	done := make(chan struct{}, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer func() { done <- struct{}{} }()
		validResult.decision, validResult.err = f.core.Mutate(context.Background(), valid)
	}()
	go func() {
		defer wg.Done()
		defer func() { done <- struct{}{} }()
		staleResult.decision, staleResult.err = f.core.Mutate(context.Background(), stale)
	}()

	<-entered
	<-entered
	close(release[IntentActivatePublication])
	wg.Wait()

	// The stale activation must fail closed with a typed safe error and
	// must never be the committed head.
	if staleResult.err == nil || staleResult.decision.State == OperationActivated {
		t.Fatalf("stale generation/fence activation must fail closed: %#v err=%v", staleResult.decision, staleResult.err)
	}
	var publicationError *Error
	if !errors.As(staleResult.err, &publicationError) {
		t.Fatalf("stale activation must return a typed safe error, got %v", staleResult.err)
	}
	if validResult.err != nil || validResult.decision.State != OperationActivated {
		t.Fatalf("valid activation must win: %#v err=%v", validResult.decision, validResult.err)
	}
	stream, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryTaskStream, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if err != nil {
		t.Fatalf("query stream: %v", err)
	}
	if stream.StreamRevision != 1 || stream.CurrentHead != validResult.decision.ArtifactVersionID {
		t.Fatalf("head must be the valid activation only: %#v", stream)
	}
}
