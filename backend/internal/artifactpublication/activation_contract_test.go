package artifactpublication

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// TestFirstGenerationActivationAdvancesStreamAtomically walks the C05-02
// tracer bullet: canonical request -> prepare -> verify exact upstream
// evidence -> atomic activation advances the publication stream
// revision/current head and returns publication evidence bound to the
// OperationID, ArtifactVersionID, manifest digest, Phase Run generation/
// fence, activity generation, and safety epoch.
func TestFirstGenerationActivationAdvancesStreamAtomically(t *testing.T) {
	f := newFixture(t)
	operationID := "op-activate-1"
	set, prepare, verify := f.prepareAndVerify(t, operationID)
	if verify.State != OperationVerified {
		t.Fatalf("unexpected verify state: %#v", verify)
	}

	// Before activation, ordinary version queries cannot see the candidate.
	if _, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryExactVersion, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: prepare.ArtifactVersionID,
	}); !isCode(err, ErrorNotFound) {
		t.Fatalf("exact version before activation error = %v, want not found", err)
	}
	if _, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryVersionHistory, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	}); !isCode(err, ErrorNotFound) {
		t.Fatalf("version history before activation error = %v, want not found", err)
	}

	activated, err := f.core.Mutate(context.Background(), f.activateIntent(operationID))
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if activated.State != OperationActivated || activated.ArtifactVersionID != prepare.ArtifactVersionID ||
		activated.ManifestDigest != prepare.ManifestDigest || activated.LineageDigest != prepare.LineageDigest {
		t.Fatalf("unexpected activation decision: %#v", activated)
	}
	if activated.StreamRevision != 1 {
		t.Fatalf("activation must advance the stream revision to 1, got %d", activated.StreamRevision)
	}
	if activated.Replay || activated.ResidueRelease {
		t.Fatal("fresh activation must not report replay or residue")
	}

	// The activation evidence is durable and bound to every authoritative
	// fact: OperationID, ArtifactVersionID, manifest digest, Phase Run
	// generation/fence, activity generation, and safety epoch.
	evidence := activated.ActivationEvidence
	if evidence == nil {
		t.Fatal("activation decision must carry committed publication evidence")
	}
	if evidence.OperationID != PublicationOperationID(operationID) ||
		evidence.ArtifactVersionID != prepare.ArtifactVersionID ||
		evidence.ManifestDigest != prepare.ManifestDigest ||
		evidence.LineageDigest != prepare.LineageDigest ||
		evidence.PhaseRunID != f.phaseRunID ||
		evidence.StreamRevision != 1 || evidence.CurrentHead != prepare.ArtifactVersionID ||
		evidence.PublicationKind != PublicationKindFirstGeneration || evidence.Parent != "" ||
		evidence.ActivityGeneration != f.generation ||
		evidence.Generation != f.generation || evidence.Fence != f.fence ||
		evidence.SafetyEpoch != f.safetyEpoch {
		t.Fatalf("activation evidence not bound to committed facts: %#v", evidence)
	}
	if !validDigest(evidence.CanonicalDigest()) {
		t.Fatalf("activation evidence digest must be canonical, got %q", evidence.CanonicalDigest())
	}

	// The stream facts advanced explicitly.
	stream, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryTaskStream, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if err != nil {
		t.Fatalf("query stream: %v", err)
	}
	if stream.StreamRevision != 1 || stream.CurrentHead != prepare.ArtifactVersionID {
		t.Fatalf("stream facts must advance to revision 1 / head %s: %#v", prepare.ArtifactVersionID, stream)
	}

	// After activation, ordinary version queries resolve the committed
	// version and its immutable members.
	versionView, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryExactVersion, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: prepare.ArtifactVersionID,
	})
	if err != nil {
		t.Fatalf("query exact version: %v", err)
	}
	if versionView.State != OperationActivated || versionView.StreamRevision != 1 ||
		versionView.ManifestDigest != prepare.ManifestDigest {
		t.Fatalf("unexpected version view: %#v", versionView)
	}
	if len(versionView.Members) != 1 {
		t.Fatalf("expected one member, got %d", len(versionView.Members))
	}
	member := versionView.Members[0]
	if member.ArtifactID == "" || member.Kind != ArtifactKindDeck ||
		member.LogicalName != "Deck.pptx" || member.MediaType != MediaTypePPTX ||
		member.Size != 1024 || !validDigest(member.ContentDigest) {
		t.Fatalf("unexpected member view: %#v", member)
	}
	_ = set
}

// TestExactMemberAndVersionHistoryQueries proves the exact-member query
// resolves one member of an activated version and version history is ordered
// exclusively by the committed stream revision.
func TestExactMemberAndVersionHistoryQueries(t *testing.T) {
	f := newFixture(t)
	_, activated := f.prepareVerifyActivate(t, "op-member-1")

	// The ArtifactID must be read from the committed version view first.
	version, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryExactVersion, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: activated.ArtifactVersionID,
	})
	if err != nil {
		t.Fatalf("query exact version: %v", err)
	}
	artifactID := version.Members[0].ArtifactID

	memberView, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryExactMember, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: activated.ArtifactVersionID, ArtifactID: artifactID,
	})
	if err != nil {
		t.Fatalf("query exact member: %v", err)
	}
	if memberView.Member == nil || memberView.Member.ArtifactID != artifactID ||
		memberView.Member.Kind != ArtifactKindDeck {
		t.Fatalf("unexpected member view: %#v", memberView)
	}
	// An unknown ArtifactID inside the same version is not found.
	if _, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryExactMember, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: activated.ArtifactVersionID, ArtifactID: "artifact-unknown",
	}); !isCode(err, ErrorNotFound) {
		t.Fatalf("unknown member error = %v, want not found", err)
	}

	history, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryVersionHistory, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if err != nil {
		t.Fatalf("query version history: %v", err)
	}
	if len(history.History) != 1 || history.History[0].ArtifactVersionID != activated.ArtifactVersionID ||
		history.History[0].StreamRevision != 1 || history.CurrentHead != activated.ArtifactVersionID {
		t.Fatalf("unexpected history: %#v", history)
	}
}

// TestActivationRequiresVerifiedCandidate proves activation on a prepared
// (unverified) operation fails closed and never creates a version or
// advances the stream.
func TestActivationRequiresVerifiedCandidate(t *testing.T) {
	f := newFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	f.mustPrepare(t, "op-not-verified", set)

	_, err := f.core.Mutate(context.Background(), f.activateIntent("op-not-verified"))
	if !isCode(err, ErrorInvalidIntent) {
		t.Fatalf("activate unprepared error = %v, want invalid intent", err)
	}
	stream, queryErr := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryTaskStream, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if queryErr != nil {
		t.Fatalf("query stream: %v", queryErr)
	}
	if stream.StreamRevision != 0 || stream.CurrentHead != "" {
		t.Fatalf("activation must not advance an unverified operation: %#v", stream)
	}
}

// TestActivationRejectsStaleGenerationFence proves activation requires the
// exact current operation generation and fence; stale values fail closed
// and the candidate stays non-active.
func TestActivationRejectsStaleGenerationFence(t *testing.T) {
	f := newFixture(t)
	f.prepareAndVerify(t, "op-stale-activate")

	header := f.header("op-stale-activate")
	header.Generation = f.generation + 9
	header.Fence = f.fence + 9
	_, err := f.core.Mutate(context.Background(), f.activateIntentWithHeader(header))
	if !isCode(err, ErrorStaleAuthority) {
		t.Fatalf("stale activation error = %v, want stale authority", err)
	}
	stream, queryErr := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryTaskStream, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if queryErr != nil {
		t.Fatalf("query stream: %v", queryErr)
	}
	if stream.StreamRevision != 0 {
		t.Fatalf("stale activation must not advance the stream: %#v", stream)
	}
}

// TestActivationRejectsWrongExpectedRevisionHead proves activation
// revalidates the expected stream revision/head at the linearization point:
// a stale expectation fails closed with a typed stale disposition and the
// operation stays non-active.
func TestActivationRejectsWrongExpectedRevisionHead(t *testing.T) {
	f := newFixture(t)
	f.prepareAndVerify(t, "op-wrong-cas")

	// Wrong expected revision.
	header := f.header("op-wrong-cas")
	header.ExpectedStreamRevision = 5
	_, err := f.core.Mutate(context.Background(), f.activateIntentWithHeader(header))
	if !isCode(err, ErrorStaleAuthority) {
		t.Fatalf("wrong expected revision error = %v, want stale authority", err)
	}
	// Wrong expected head.
	header = f.header("op-wrong-cas")
	header.ExpectedHead = "artifact-version-other"
	_, err = f.core.Mutate(context.Background(), f.activateIntentWithHeader(header))
	if !isCode(err, ErrorStaleAuthority) {
		t.Fatalf("wrong expected head error = %v, want stale authority", err)
	}
	// The operation remains verified and the stream is untouched.
	view, queryErr := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryOperation, PolicyDomainID: f.policyDomain, TaskID: f.taskID, OperationID: "op-wrong-cas",
	})
	if queryErr != nil {
		t.Fatalf("query operation: %v", queryErr)
	}
	if view.State != OperationVerified {
		t.Fatalf("loser must stay non-active: %#v", view)
	}
}

// TestManualEditChildActivationPreservesParent proves the manual-edit child
// is activated against the exact activated parent (matching the Task
// Orchestration activity source, the exact parent manifest, and the C04
// validated reconstruction/export source), the parent and its members stay
// immutable and independently queryable, and history is ordered by stream
// revision.
func TestManualEditChildActivationPreservesParent(t *testing.T) {
	f := newFixture(t)
	_, parentActivated := f.prepareVerifyActivate(t, "op-parent")
	parent := parentActivated.ArtifactVersionID

	// Manual-edit child pinned to the activated parent.
	childSet := f.childEvidenceSet(t, parent, "op-child")
	childHeader := f.header("op-child")
	childHeader.ExpectedStreamRevision = 1
	childHeader.ExpectedHead = parent
	childPrepare := bindDigest(NewPreparePublication(childHeader, f.childPreparePayload("op-child", parent, childSet)))
	if _, err := f.core.Mutate(context.Background(), childPrepare); err != nil {
		t.Fatalf("manual-edit child prepare: %v", err)
	}
	if _, err := f.core.Mutate(context.Background(), f.verifyIntent("op-child", f.verifyPayload(childSet))); err != nil {
		t.Fatalf("manual-edit child verify: %v", err)
	}
	childActivated, err := f.core.Mutate(context.Background(), f.activateIntentWithHeader(childHeader))
	if err != nil {
		t.Fatalf("manual-edit child activate: %v", err)
	}
	if childActivated.State != OperationActivated || childActivated.StreamRevision != 2 ||
		childActivated.ActivationEvidence == nil || childActivated.ActivationEvidence.Parent != parent {
		t.Fatalf("unexpected child activation: %#v", childActivated)
	}

	// The parent is still the exact same immutable version, independently
	// queryable with the same members, manifest, and lineage.
	parentView, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryExactVersion, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: parent,
	})
	if err != nil {
		t.Fatalf("query parent after child: %v", err)
	}
	if parentView.ManifestDigest != parentActivated.ManifestDigest ||
		parentView.LineageDigest != parentActivated.LineageDigest ||
		parentView.StreamRevision != 1 || len(parentView.Members) != 1 {
		t.Fatalf("parent mutated by child activation: %#v", parentView)
	}

	// History contains both versions ordered by committed stream revision;
	// the current head is the explicit pointer to the child.
	history, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryVersionHistory, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if err != nil {
		t.Fatalf("query history: %v", err)
	}
	if len(history.History) != 2 ||
		history.History[0].ArtifactVersionID != parent || history.History[0].StreamRevision != 1 ||
		history.History[1].ArtifactVersionID != childActivated.ArtifactVersionID || history.History[1].StreamRevision != 2 ||
		history.History[1].Parent != parent || history.CurrentHead != childActivated.ArtifactVersionID {
		t.Fatalf("unexpected history after manual edit: %#v", history)
	}
}

// TestManualEditChildRequiresExactParentAndExpectedHead proves a manual-edit
// child whose parent does not match the expected current head fails closed
// at activation and never advances the stream.
func TestManualEditChildRequiresExactParentAndExpectedHead(t *testing.T) {
	f := newFixture(t)
	_, parentActivated := f.prepareVerifyActivate(t, "op-parent-mismatch")
	parent := parentActivated.ArtifactVersionID

	childSet := f.childEvidenceSet(t, parent, "op-child-mismatch")
	// The prepare pins the correct parent and expected head; the activation
	// then submits a different expected head, so the CAS at the
	// linearization point must fail closed.
	childHeader := f.header("op-child-mismatch")
	childHeader.ExpectedStreamRevision = 1
	childHeader.ExpectedHead = parent
	childPrepare := bindDigest(NewPreparePublication(childHeader, f.childPreparePayload("op-child-mismatch", parent, childSet)))
	if _, err := f.core.Mutate(context.Background(), childPrepare); err != nil {
		t.Fatalf("child prepare: %v", err)
	}
	if _, err := f.core.Mutate(context.Background(), f.verifyIntent("op-child-mismatch", f.verifyPayload(childSet))); err != nil {
		t.Fatalf("child verify: %v", err)
	}
	staleHeader := f.header("op-child-mismatch")
	staleHeader.ExpectedStreamRevision = 1
	staleHeader.ExpectedHead = "artifact-version-other"
	_, err := f.core.Mutate(context.Background(), f.activateIntentWithHeader(staleHeader))
	if !isCode(err, ErrorStaleAuthority) {
		t.Fatalf("mismatched child head error = %v, want stale authority", err)
	}
	stream, queryErr := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryTaskStream, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if queryErr != nil {
		t.Fatalf("query stream: %v", queryErr)
	}
	if stream.CurrentHead != parent || stream.StreamRevision != 1 {
		t.Fatalf("mismatched child must not advance the stream: %#v", stream)
	}
}

// TestActivationAuthorityBindingDeniesNonOrchestration proves Runtime,
// validator, C04, Durable Object, and recovery authorities cannot bypass
// Mutate to activate a version or advance the Task: activation accepts only
// the exact Task Orchestration authority.
func TestActivationAuthorityBindingDeniesNonOrchestration(t *testing.T) {
	f := newFixture(t)
	f.prepareAndVerify(t, "op-auth-activate")

	authorities := []PublicationAuthority{
		{Kind: AuthorityRuntimeExecution, ID: f.runtimeAuthority, Generation: 1},
		{Kind: AuthorityPlatformValidation, ID: f.validationAuthority, Generation: 1},
		{Kind: AuthorityTaskWorkspaceLifecycle, ID: f.c04Authority, Generation: 1},
		{Kind: AuthorityDurableObject, ID: f.durableObjectAuthority, Generation: 1},
		f.recoveryAuthorityValue(),
		{Kind: AuthorityTaskOrchestration, ID: "unregistered-orchestrator", Generation: 1},
	}
	for _, authority := range authorities {
		header := f.header("op-auth-activate")
		header.Authority = authority
		_, err := f.core.Mutate(context.Background(), f.activateIntentWithHeader(header))
		// Non-orchestration authorities are rejected either at intent
		// validity (unregistered mutation authorities fail closed by
		// construction) or at authority binding; both are fail-closed safe
		// errors and never advance the stream.
		if !isCode(err, ErrorOwnershipDenied) && !isCode(err, ErrorInvalidIntent) {
			t.Fatalf("activation by %s authority error = %v, want ownership denied or invalid intent", authority.Kind, err)
		}
	}
	stream, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryTaskStream, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if err != nil {
		t.Fatalf("query stream: %v", err)
	}
	if stream.StreamRevision != 0 || stream.CurrentHead != "" {
		t.Fatalf("foreign authority must never advance the stream: %#v", stream)
	}
}

// TestCancelFirstClosesLateActivation proves cancel-first linearization:
// once the cancel commits, a late activation fails closed and never creates
// a version or advances the stream.
func TestCancelFirstClosesLateActivation(t *testing.T) {
	f := newFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	f.prepareAndVerify(t, "op-cancel-first")

	cancelled, err := f.core.Mutate(context.Background(), f.cancelIntent("op-cancel-first", CancelTaskOrchestration))
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if cancelled.State != OperationCancelled {
		t.Fatalf("unexpected cancel decision: %#v", cancelled)
	}
	_, err = f.core.Mutate(context.Background(), f.activateIntent("op-cancel-first"))
	var publicationError *Error
	if !errors.As(err, &publicationError) || publicationError.Code != ErrorTerminalConflict {
		t.Fatalf("late activation error = %v, want terminal conflict", err)
	}
	stream, queryErr := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryTaskStream, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if queryErr != nil {
		t.Fatalf("query stream: %v", queryErr)
	}
	if stream.StreamRevision != 0 || stream.CurrentHead != "" {
		t.Fatalf("late activation must not advance the stream: %#v", stream)
	}
	_ = set
}

// TestActivationFirstCancelReturnsActiveTerminalResult proves
// activation-first linearization: a cancel submitted after the activation
// commits returns the existing active terminal result, never deletes the
// version, and never releases its references as residue.
func TestActivationFirstCancelReturnsActiveTerminalResult(t *testing.T) {
	f := newFixture(t)
	set, activated := f.prepareVerifyActivate(t, "op-activate-first")

	cancelled, err := f.core.Mutate(context.Background(), f.cancelIntent("op-activate-first", CancelTaskOrchestration))
	if err != nil {
		t.Fatalf("cancel after activation: %v", err)
	}
	if cancelled.State != OperationActivated || !cancelled.Replay {
		t.Fatalf("cancel must return the existing active terminal result: %#v", cancelled)
	}
	if cancelled.ActivationEvidence == nil ||
		cancelled.ActivationEvidence.ArtifactVersionID != activated.ArtifactVersionID ||
		cancelled.ActivationEvidence.ManifestDigest != activated.ManifestDigest {
		t.Fatalf("cancel result must carry the committed activation evidence: %#v", cancelled)
	}
	if cancelled.ResidueRelease {
		t.Fatal("cancel after activation must never release activated references as residue")
	}

	// The version still exists and the head is unchanged.
	version, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryExactVersion, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: activated.ArtifactVersionID,
	})
	if err != nil {
		t.Fatalf("version must survive cancel: %v", err)
	}
	if version.State != OperationActivated || version.ManifestDigest != activated.ManifestDigest {
		t.Fatalf("version mutated by cancel: %#v", version)
	}
	stream, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryTaskStream, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if err != nil {
		t.Fatalf("query stream: %v", err)
	}
	if stream.CurrentHead != activated.ArtifactVersionID || stream.StreamRevision != 1 {
		t.Fatalf("cancel must not move the head: %#v", stream)
	}
	_ = set
}

// TestRejectAfterActivationIsTerminalConflict proves rejection cannot undo
// an atomic activation: the operation is already terminal-active and the
// committed version is untouched.
func TestRejectAfterActivationIsTerminalConflict(t *testing.T) {
	f := newFixture(t)
	_, activated := f.prepareVerifyActivate(t, "op-reject-after")

	_, err := f.core.Mutate(context.Background(), f.rejectIntent("op-reject-after", RejectCandidateSuperseded, nil))
	if !isCode(err, ErrorTerminalConflict) {
		t.Fatalf("reject after activation error = %v, want terminal conflict", err)
	}
	version, queryErr := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryExactVersion, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: activated.ArtifactVersionID,
	})
	if queryErr != nil {
		t.Fatalf("version must survive reject: %v", queryErr)
	}
	if version.ManifestDigest != activated.ManifestDigest {
		t.Fatalf("reject mutated the version: %#v", version)
	}
}

// TestActivatedVersionImmutableAcrossLaterOperations proves activated
// versions, members, manifest, and lineage are immutable: later prepares,
// verifies, cancels, and rejects on the same Task never modify or delete a
// committed version, and the members never change.
func TestActivatedVersionImmutableAcrossLaterOperations(t *testing.T) {
	f := newFixture(t)
	_, first := f.prepareVerifyActivate(t, "op-immutable-1")

	// A second first-generation operation cannot be prepared against the
	// committed stream without advancing the expected revision/head; this
	// proves the head only comes from explicit stream facts.
	set2 := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	header := f.header("op-immutable-2")
	header.ExpectedStreamRevision = 1
	header.ExpectedHead = first.ArtifactVersionID
	secondPrepare := bindDigest(NewPreparePublication(header, f.preparePayload("op-immutable-2", set2, []ArtifactMemberSpec{f.deckMemberSpec()})))
	second, err := f.core.Mutate(context.Background(), secondPrepare)
	if err != nil {
		t.Fatalf("second prepare: %v", err)
	}
	if second.State != OperationPrepared {
		t.Fatalf("unexpected second prepare: %#v", second)
	}

	firstView, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryExactVersion, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: first.ArtifactVersionID,
	})
	if err != nil {
		t.Fatalf("query first version: %v", err)
	}
	// The second operation stays non-active; ordinary queries still see only
	// the committed version.
	if _, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryExactVersion, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: second.ArtifactVersionID,
	}); !isCode(err, ErrorNotFound) {
		t.Fatalf("non-activated candidate visible as version: %v", err)
	}
	if firstView.Members[0].ArtifactID == "" {
		t.Fatal("first version must carry a member")
	}
	// Cancel the second operation; the committed version is untouched.
	if _, err := f.core.Mutate(context.Background(), f.cancelIntent("op-immutable-2", CancelTaskOrchestration)); err != nil {
		t.Fatalf("cancel second: %v", err)
	}
	after, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryExactVersion, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: first.ArtifactVersionID,
	})
	if err != nil {
		t.Fatalf("query first version after cancel: %v", err)
	}
	if !reflect.DeepEqual(firstView, after) {
		t.Fatalf("committed version mutated by later operation: before=%#v after=%#v", firstView, after)
	}
}

// TestDuplicateActivationExactReplayProves resubmitting the exact activation
// intent replays the original decision, the same version identity, the same
// stream revision, and the same committed evidence, and never creates a
// second version or changes lineage.
func TestDuplicateActivationExactReplay(t *testing.T) {
	f := newFixture(t)
	_, activated := f.prepareVerifyActivate(t, "op-dup-activate")

	replayed, err := f.core.Mutate(context.Background(), f.activateIntent("op-dup-activate"))
	if err != nil {
		t.Fatalf("replay activation: %v", err)
	}
	if !replayed.Replay || replayed.State != OperationActivated ||
		replayed.ArtifactVersionID != activated.ArtifactVersionID ||
		replayed.StreamRevision != activated.StreamRevision ||
		replayed.ManifestDigest != activated.ManifestDigest ||
		replayed.LineageDigest != activated.LineageDigest {
		t.Fatalf("unexpected activation replay: %#v", replayed)
	}
	if replayed.ActivationEvidence == nil ||
		replayed.ActivationEvidence.CanonicalDigest() != activated.ActivationEvidence.CanonicalDigest() {
		t.Fatalf("replay must return the identical committed evidence: %#v", replayed.ActivationEvidence)
	}

	history, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryVersionHistory, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if err != nil {
		t.Fatalf("query history: %v", err)
	}
	if len(history.History) != 1 {
		t.Fatalf("duplicate activation must not create a second version: %#v", history)
	}
}

// TestActivationReplayAfterLaterStreamRevisionProves exact replay returns
// the original activated decision even after later versions committed to the
// same stream (response loss / replay after later stream revision).
func TestActivationReplayAfterLaterStreamRevision(t *testing.T) {
	f := newFixture(t)
	_, first := f.prepareVerifyActivate(t, "op-later-1")

	// A manual-edit child commits later to the same stream, moving the
	// head and revision past the first activation.
	childSet := f.childEvidenceSet(t, first.ArtifactVersionID, "op-later-child")
	childHeader := f.header("op-later-child")
	childHeader.ExpectedStreamRevision = 1
	childHeader.ExpectedHead = first.ArtifactVersionID
	childPrepare := bindDigest(NewPreparePublication(childHeader, f.childPreparePayload("op-later-child", first.ArtifactVersionID, childSet)))
	if _, err := f.core.Mutate(context.Background(), childPrepare); err != nil {
		t.Fatalf("child prepare: %v", err)
	}
	if _, err := f.core.Mutate(context.Background(), f.verifyIntent("op-later-child", f.verifyPayload(childSet))); err != nil {
		t.Fatalf("child verify: %v", err)
	}
	if _, err := f.core.Mutate(context.Background(), f.activateIntentWithHeader(childHeader)); err != nil {
		t.Fatalf("child activate: %v", err)
	}

	// Replaying the first activation after the later stream revision still
	// returns the first committed decision with its own revision.
	replayed, err := f.core.Mutate(context.Background(), f.activateIntent("op-later-1"))
	if err != nil {
		t.Fatalf("replay after later revision: %v", err)
	}
	if !replayed.Replay || replayed.State != OperationActivated ||
		replayed.StreamRevision != 1 || replayed.ArtifactVersionID != first.ArtifactVersionID {
		t.Fatalf("unexpected replay after later revision: %#v", replayed)
	}
}

// TestActivationSurvivesRestartAndReplay proves activation is restartable:
// after a restart from the same persistence the committed version, head,
// evidence, and exact replay are unchanged, and no second version is
// created.
func TestActivationSurvivesRestartAndReplay(t *testing.T) {
	f := newFixture(t)
	_, activated := f.prepareVerifyActivate(t, "op-restart-activate")

	f.rebuild()
	version, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryExactVersion, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: activated.ArtifactVersionID,
	})
	if err != nil {
		t.Fatalf("query version after restart: %v", err)
	}
	if version.State != OperationActivated || version.ManifestDigest != activated.ManifestDigest ||
		version.StreamRevision != 1 {
		t.Fatalf("unexpected version after restart: %#v", version)
	}
	if version.ActivationEvidence == nil ||
		version.ActivationEvidence.CanonicalDigest() != activated.ActivationEvidence.CanonicalDigest() {
		t.Fatalf("activation evidence changed across restart: %#v", version.ActivationEvidence)
	}
	replayed, err := f.core.Mutate(context.Background(), f.activateIntent("op-restart-activate"))
	if err != nil {
		t.Fatalf("replay activation after restart: %v", err)
	}
	if !replayed.Replay || replayed.ArtifactVersionID != activated.ArtifactVersionID {
		t.Fatalf("unexpected replay after restart: %#v", replayed)
	}
}

// TestManualEditChildRequiresActivatedParentAtActivation proves the child
// activation revalidates that the exact parent is still activated in this
// authority; a parent that is not committed fails closed.
func TestManualEditChildRequiresActivatedParentAtActivation(t *testing.T) {
	f := newFixture(t)
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	prepare := f.mustPrepare(t, "op-parent-prep", set)
	if _, err := f.core.Mutate(context.Background(), f.verifyIntent("op-parent-prep", f.verifyPayload(set))); err != nil {
		t.Fatalf("verify: %v", err)
	}

	// The child pins the never-activated candidate as parent. The prepare
	// already fails closed because no activated parent exists; a forged
	// parent cannot be used.
	childSet := f.childEvidenceSet(t, prepare.ArtifactVersionID, "op-child-forge")
	header := f.header("op-child-forge")
	header.ExpectedStreamRevision = 1
	header.ExpectedHead = prepare.ArtifactVersionID
	childPrepare := bindDigest(NewPreparePublication(header, f.childPreparePayload("op-child-forge", prepare.ArtifactVersionID, childSet)))
	_, err := f.core.Mutate(context.Background(), childPrepare)
	if !isCode(err, ErrorStaleAuthority) && !isCode(err, ErrorIntegrityFailure) {
		t.Fatalf("forged child error = %v, want stale authority or integrity failure", err)
	}
}
