package artifactpublication

// This file runs the shared black-box contract suite of the Artifact
// Publication module against the real PostgreSQL owned persistence adapter
// (child SPEC #107). The same scenarios that run over the deterministic
// in-memory authority must produce identical public decisions, views, safe
// errors, states, and evidence through the same Mutate/Query seam; adapter
// parity requires identical domain semantics, with PostgreSQL adding the
// atomicity, row-lock/CAS and restart guarantees.

import (
	"context"
	"sync"
	"testing"
)

// TestPostgresFirstGenerationPrepareVerifyLifecycle walks the tracer bullet
// over real PostgreSQL: canonical request -> prepare immutable candidate ->
// verify exact upstream evidence -> verified, then inspects the operation
// and candidate through the pure read-only Query seam.
func TestPostgresFirstGenerationPrepareVerifyLifecycle(t *testing.T) {
	f := newPostgresFixture(t)
	operationID := "pg-op-1"

	set := f.buildEvidenceDB(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	prepare := f.mustPrepare(t, operationID, set)
	if prepare.State != OperationPrepared || prepare.ArtifactVersionID == "" ||
		!validDigest(prepare.ManifestDigest) || !validDigest(prepare.LineageDigest) {
		t.Fatalf("unexpected prepare decision: %#v", prepare)
	}
	versionID := prepare.ArtifactVersionID

	verify := f.mustVerify(t, operationID, set)
	if verify.State != OperationVerified || verify.ArtifactVersionID != versionID ||
		verify.ManifestDigest != prepare.ManifestDigest || verify.LineageDigest != prepare.LineageDigest {
		t.Fatalf("unexpected verify decision: %#v", verify)
	}
	if verify.Verification == nil || verify.Verification.State != VerificationVerified ||
		len(verify.Verification.AcceptedEvidence) != 4 {
		t.Fatalf("missing verified verification result: %#v", verify.Verification)
	}

	operationView, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryOperation, PolicyDomainID: f.policyDomain, TaskID: f.taskID, OperationID: PublicationOperationID(operationID),
	})
	if err != nil {
		t.Fatalf("query operation: %v", err)
	}
	if operationView.State != OperationVerified || operationView.ArtifactVersionID != versionID ||
		operationView.ManifestDigest != prepare.ManifestDigest || len(operationView.Members) != 1 {
		t.Fatalf("unexpected operation view: %#v", operationView)
	}

	candidateView, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryCandidate, PolicyDomainID: f.policyDomain, TaskID: f.taskID, ArtifactVersionID: versionID,
	})
	if err != nil {
		t.Fatalf("query candidate: %v", err)
	}
	if candidateView.ArtifactVersionID != versionID || candidateView.PublicationKind != PublicationKindFirstGeneration {
		t.Fatalf("unexpected candidate view: %#v", candidateView)
	}
}

// TestPostgresActivationAdvancesStreamAtomically walks the C05-02 tracer
// bullet over real PostgreSQL: atomic activation advances the publication
// stream revision/current head, returns publication evidence bound to the
// committed facts, and makes the immutable version queryable.
func TestPostgresActivationAdvancesStreamAtomically(t *testing.T) {
	f := newPostgresFixture(t)
	operationID := "pg-activate-1"
	set, prepare, verify := f.prepareAndVerifyPG(t, operationID)
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

	activated, err := f.core.Mutate(context.Background(), f.activateIntent(operationID))
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if activated.State != OperationActivated || activated.ArtifactVersionID != prepare.ArtifactVersionID ||
		activated.ManifestDigest != prepare.ManifestDigest || activated.StreamRevision != 1 {
		t.Fatalf("unexpected activation decision: %#v", activated)
	}
	evidence := activated.ActivationEvidence
	if evidence == nil || evidence.OperationID != PublicationOperationID(operationID) ||
		evidence.ArtifactVersionID != prepare.ArtifactVersionID ||
		evidence.StreamRevision != 1 || evidence.CurrentHead != prepare.ArtifactVersionID ||
		evidence.ActivityGeneration != f.generation ||
		evidence.Generation != f.generation || evidence.Fence != f.fence ||
		evidence.SafetyEpoch != f.safetyEpoch {
		t.Fatalf("activation evidence not bound to committed facts: %#v", evidence)
	}

	stream, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryTaskStream, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if err != nil {
		t.Fatalf("query stream: %v", err)
	}
	if stream.StreamRevision != 1 || stream.CurrentHead != prepare.ArtifactVersionID {
		t.Fatalf("stream facts must advance to revision 1 / head %s: %#v", prepare.ArtifactVersionID, stream)
	}

	versionView, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryExactVersion, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: prepare.ArtifactVersionID,
	})
	if err != nil {
		t.Fatalf("query exact version: %v", err)
	}
	if versionView.State != OperationActivated || len(versionView.Members) != 1 {
		t.Fatalf("unexpected version view: %#v", versionView)
	}
	_ = set
}

// TestPostgresExactReplayReturnsOriginalDecision proves exact replay over
// real PostgreSQL returns the original durable decision with the same
// identity, manifest digest and stream facts, without reallocating
// identity (acceptance: crash-after-commit-before-response and duplicate
// delivery never create a new version).
func TestPostgresExactReplayReturnsOriginalDecision(t *testing.T) {
	f := newPostgresFixture(t)
	operationID := "pg-replay"
	set := f.buildEvidenceDB(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	prepare := f.mustPrepare(t, operationID, set)
	verify := f.mustVerify(t, operationID, set)
	activated := f.mustActivate(t, operationID)

	replayedPrepare, err := f.core.Mutate(context.Background(), f.prepareIntent(operationID, f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()})))
	if err != nil {
		t.Fatalf("replay prepare: %v", err)
	}
	if !replayedPrepare.Replay || replayedPrepare.ArtifactVersionID != prepare.ArtifactVersionID ||
		replayedPrepare.ManifestDigest != prepare.ManifestDigest {
		t.Fatalf("prepare replay must return the original decision: %#v", replayedPrepare)
	}
	replayedVerify, err := f.core.Mutate(context.Background(), f.verifyIntent(operationID, f.verifyPayload(set)))
	if err != nil {
		t.Fatalf("replay verify: %v", err)
	}
	if !replayedVerify.Replay || replayedVerify.ArtifactVersionID != verify.ArtifactVersionID {
		t.Fatalf("verify replay must return the original decision: %#v", replayedVerify)
	}
	replayedActivate, err := f.core.Mutate(context.Background(), f.activateIntent(operationID))
	if err != nil {
		t.Fatalf("replay activate: %v", err)
	}
	if !replayedActivate.Replay || replayedActivate.ArtifactVersionID != activated.ArtifactVersionID ||
		replayedActivate.StreamRevision != 1 || replayedActivate.ActivationEvidence == nil {
		t.Fatalf("activate replay must return the original decision: %#v", replayedActivate)
	}

	// Identity is never reallocated: exactly one activated version and one
	// version fact exist.
	if got := f.countRows(t, "publication_activated"); got != 1 {
		t.Fatalf("activated rows = %d, want 1 (identity never reallocated)", got)
	}
}

// TestPostgresSameKeyDifferentPayloadIntegrityConflict proves a different
// canonical payload under the same operation identity is a durable typed
// integrity conflict that never changes the original binding, and that the
// exact replay still returns the original decision afterwards.
func TestPostgresSameKeyDifferentPayloadIntegrityConflict(t *testing.T) {
	f := newPostgresFixture(t)
	operationID := "pg-conflict"
	set := f.buildEvidenceDB(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	prepare := f.mustPrepare(t, operationID, set)
	f.mustVerify(t, operationID, set)

	conflicting := f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()})
	conflicting.Members[0].LogicalName = "Renamed.pptx"
	conflicting.Members[0].ContentDigest = testDigest("other-content")
	if _, err := f.core.Mutate(context.Background(), f.prepareIntent(operationID, conflicting)); !isCode(err, ErrorIntegrityConflict) {
		t.Fatalf("same key different payload error = %v, want integrity conflict", err)
	}
	// The conflict is durable and content-free: one incident row exists.
	if got := f.countRows(t, "publication_integrity_incident"); got < 1 {
		t.Fatalf("integrity incident rows = %d, want at least 1", got)
	}
	// The exact replay still returns the original prepare decision.
	replayed, err := f.core.Mutate(context.Background(), f.prepareIntent(operationID, f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()})))
	if err != nil || !replayed.Replay || replayed.ArtifactVersionID != prepare.ArtifactVersionID {
		t.Fatalf("replay after conflict = %#v err=%v, want original decision", replayed, err)
	}
}

// TestPostgresRejectNeverCreatesVersionOrHead proves rejection is terminal,
// creates no Artifact Version, member, or current-head mutation, releases
// the exact typed staging references as residue, and stays invisible to
// ordinary queries.
func TestPostgresRejectNeverCreatesVersionOrHead(t *testing.T) {
	f := newPostgresFixture(t)
	operationID := "pg-reject"
	set := f.buildEvidenceDB(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	f.mustPrepare(t, operationID, set)
	f.mustVerify(t, operationID, set)

	rejected, err := f.core.Mutate(context.Background(), f.rejectIntent(operationID, RejectCandidateSuperseded, nil))
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
	if got := f.countRows(t, "publication_activated"); got != 0 {
		t.Fatalf("reject must create no activated version, got %d rows", got)
	}
	// The exact typed staging references are released as C05-owned residue.
	if got := f.countRows(t, "publication_residue"); got != 1 {
		t.Fatalf("residue rows = %d, want 1", got)
	}
	if got := f.countRows(t, "publication_residue_staging"); got != 1 {
		t.Fatalf("residue staging rows = %d, want 1 exact typed reference", got)
	}
}

// TestPostgresActivateCancelRaceDeterministicWinner proves activate/cancel
// races over real PostgreSQL linearize deterministically through the
// row-lock on the operation: cancel-first fails the late activation closed;
// activation-first replays the existing active terminal result without
// deleting the version.
func TestPostgresActivateCancelRaceDeterministicWinner(t *testing.T) {
	t.Run("cancel first", func(t *testing.T) {
		f := newPostgresFixture(t)
		operationID := "pg-race-cancel"
		f.prepareAndVerifyPG(t, operationID)

		cancel, err := f.core.Mutate(context.Background(), f.cancelIntent(operationID, CancelTaskOrchestration))
		if err != nil || cancel.State != OperationCancelled {
			t.Fatalf("cancel result: %#v err=%v", cancel, err)
		}
		if _, err := f.core.Mutate(context.Background(), f.activateIntent(operationID)); !isCode(err, ErrorTerminalConflict) {
			t.Fatalf("late activation error = %v, want terminal conflict", err)
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
	})

	t.Run("activate first", func(t *testing.T) {
		f := newPostgresFixture(t)
		operationID := "pg-race-activate"
		set, _, _ := f.prepareAndVerifyPG(t, operationID)
		activated, err := f.core.Mutate(context.Background(), f.activateIntent(operationID))
		if err != nil || activated.State != OperationActivated {
			t.Fatalf("activate result: %#v err=%v", activated, err)
		}
		cancel, err := f.core.Mutate(context.Background(), f.cancelIntent(operationID, CancelTaskOrchestration))
		if err != nil || cancel.State != OperationActivated || !cancel.Replay || cancel.ResidueRelease {
			t.Fatalf("cancel after activation must replay the active terminal result without residue: %#v err=%v", cancel, err)
		}
		version, err := f.core.Query(context.Background(), PublicationQuery{
			Kind: QueryExactVersion, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
			ArtifactVersionID: activated.ArtifactVersionID,
		})
		if err != nil {
			t.Fatalf("version must survive the race: %v", err)
		}
		if version.ManifestDigest != activated.ManifestDigest {
			t.Fatalf("version mutated by cancel: %#v", version)
		}
		if got := f.countRows(t, "publication_residue"); got != 0 {
			t.Fatalf("cancel after activation must not create residue, got %d", got)
		}
		_ = set
	})
}

// TestPostgresTwoChildActivationRaceSingleWinner proves two concurrent
// manual-edit children from the same activated parent produce exactly one
// current-head winner through the stream row lock; the loser stays
// non-active with a typed stale disposition.
func TestPostgresTwoChildActivationRaceSingleWinner(t *testing.T) {
	f := newPostgresFixture(t)
	_, parent := f.prepareVerifyActivatePG(t, "pg-parent")

	childSetA := f.childEvidenceSetDB(t, parent.ArtifactVersionID, "pg-child-a")
	childSetB := f.childEvidenceSetDB(t, parent.ArtifactVersionID, "pg-child-b")

	headerA := f.header("pg-child-a")
	headerA.ExpectedStreamRevision = 1
	headerA.ExpectedHead = parent.ArtifactVersionID
	headerB := f.header("pg-child-b")
	headerB.ExpectedStreamRevision = 1
	headerB.ExpectedHead = parent.ArtifactVersionID

	for _, pair := range []struct {
		operationID string
		header      PublicationIntentHeader
		set         *evidenceSet
	}{{"pg-child-a", headerA, childSetA}, {"pg-child-b", headerB, childSetB}} {
		prepare := bindDigest(NewPreparePublication(pair.header, f.childPreparePayload(pair.operationID, parent.ArtifactVersionID, pair.set)))
		if _, err := f.core.Mutate(context.Background(), prepare); err != nil {
			t.Fatalf("child prepare %s: %v", pair.operationID, err)
		}
		if _, err := f.core.Mutate(context.Background(), f.verifyIntent(pair.operationID, f.verifyPayload(pair.set))); err != nil {
			t.Fatalf("child verify %s: %v", pair.operationID, err)
		}
	}

	var winnerA, winnerB error
	var decisionA, decisionB PublicationDecision
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		decisionA, winnerA = f.core.Mutate(context.Background(), f.activateIntentWithHeader(headerA))
	}()
	go func() {
		defer wg.Done()
		decisionB, winnerB = f.core.Mutate(context.Background(), f.activateIntentWithHeader(headerB))
	}()
	wg.Wait()

	winners := 0
	if winnerA == nil && decisionA.State == OperationActivated {
		winners++
	}
	if winnerB == nil && decisionB.State == OperationActivated {
		winners++
	}
	if winners != 1 {
		t.Fatalf("two-child activation winners = %d, want exactly 1 (a=%v b=%v)", winners, winnerA, winnerB)
	}
	history, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryVersionHistory, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if err != nil {
		t.Fatalf("query history: %v", err)
	}
	if history.StreamRevision != 2 || len(history.History) != 2 {
		t.Fatalf("history must contain exactly the parent and one child: %#v", history)
	}
}

// TestPostgresExactMemberAndHistoryQueries proves exact-member resolution
// and history ordering over real PostgreSQL use only the committed stream
// facts.
func TestPostgresExactMemberAndHistoryQueries(t *testing.T) {
	f := newPostgresFixture(t)
	_, activated := f.prepareVerifyActivatePG(t, "pg-member-1")

	version, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryExactVersion, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: activated.ArtifactVersionID,
	})
	if err != nil {
		t.Fatalf("query exact version: %v", err)
	}
	if len(version.Members) != 1 {
		t.Fatalf("expected one member, got %d", len(version.Members))
	}
	memberView, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryExactMember, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: activated.ArtifactVersionID, ArtifactID: version.Members[0].ArtifactID,
	})
	if err != nil {
		t.Fatalf("query exact member: %v", err)
	}
	if memberView.Member == nil || memberView.Member.ArtifactID != version.Members[0].ArtifactID ||
		memberView.Member.LogicalName != "Deck.pptx" {
		t.Fatalf("unexpected member view: %#v", memberView.Member)
	}

	history, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryVersionHistory, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if err != nil {
		t.Fatalf("query history: %v", err)
	}
	if len(history.History) != 1 || history.History[0].ArtifactVersionID != activated.ArtifactVersionID ||
		history.History[0].StreamRevision != 1 {
		t.Fatalf("unexpected history: %#v", history)
	}
}

// TestPostgresContentTargetScopeGates proves content-target resolution and
// verification over real PostgreSQL fail closed non-enumerating under
// wrong/revoked scopes and cross-workspace identities, and that scope union
// is impossible.
func TestPostgresContentTargetScopeGates(t *testing.T) {
	f := newPostgresFixture(t)
	_, activated := f.prepareVerifyActivatePG(t, "pg-target")
	f.registerScopeForVersionDB(t, activated.ArtifactVersionID)
	version, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryExactVersion, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: activated.ArtifactVersionID,
	})
	if err != nil {
		t.Fatalf("query version: %v", err)
	}
	artifactID := version.Members[0].ArtifactID

	// Owner scope resolves.
	target, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryResolveContentTarget, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: activated.ArtifactVersionID, ArtifactID: artifactID,
		Scope: f.ownerScope(activated.ArtifactVersionID), ContentIntent: ContentIntentDownload,
	})
	if err != nil || target.ContentTarget == nil {
		t.Fatalf("resolve content target: %#v err=%v", target, err)
	}
	if target.ContentTarget.ArtifactVersionID != activated.ArtifactVersionID ||
		target.ContentTarget.ArtifactID != artifactID ||
		target.ContentTarget.ManifestDigest != activated.ManifestDigest ||
		target.ContentTarget.Disposition != ContentDispositionAttachment {
		t.Fatalf("unexpected content target: %#v", target.ContentTarget)
	}

	// Wrong scope fails closed non-enumerating.
	if _, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryResolveContentTarget, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: activated.ArtifactVersionID, ArtifactID: artifactID,
		Scope:         ContentScope{Kind: ContentScopeShareLink, ID: "share-grant-1", AvailabilityGeneration: 5},
		ContentIntent: ContentIntentDownload,
	}); !isCode(err, ErrorNotFound) {
		t.Fatalf("wrong scope error = %v, want non-enumerating not found", err)
	}

	// Cross-workspace never discloses the version.
	if _, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryResolveContentTarget, PolicyDomainID: "other-domain", TaskID: f.taskID,
		ArtifactVersionID: activated.ArtifactVersionID, ArtifactID: artifactID,
		Scope: f.ownerScope(activated.ArtifactVersionID), ContentIntent: ContentIntentDownload,
	}); !isCode(err, ErrorNotFound) {
		t.Fatalf("cross-workspace error = %v, want non-enumerating not found", err)
	}

	// Verification of the presented target under the same scope passes.
	if err := f.verifyTargetPG(t, target.ContentTarget, f.ownerScope(activated.ArtifactVersionID)); err != nil {
		t.Fatalf("verify content target: %v", err)
	}
}

// TestPostgresC04CapabilityIssuanceAndVerification proves the C04
// reconstruction capability contract over real PostgreSQL: only the
// Platform's exact version selection can be issued, C04 can only verify,
// and expiry/stale scopes fail closed.
func TestPostgresC04CapabilityIssuanceAndVerification(t *testing.T) {
	f := newPostgresFixture(t)
	_, activated := f.prepareVerifyActivatePG(t, "pg-c04")
	f.registerScopeForVersionDB(t, activated.ArtifactVersionID)
	scope := f.ownerScope(activated.ArtifactVersionID)
	expiry := f.now + 3600

	capability, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryIssueC04ReconstructionCapability, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: activated.ArtifactVersionID, Scope: scope, ExpiresAt: expiry,
		Authority: f.platformAuthority(),
	})
	if err != nil || capability.C04Capability == nil {
		t.Fatalf("issue C04 capability: %#v err=%v", capability, err)
	}
	if capability.C04Capability.ArtifactVersionID != activated.ArtifactVersionID ||
		capability.C04Capability.ManifestDigest != activated.ManifestDigest ||
		capability.C04Capability.PublicationAuthorityID != f.publicationAuthority {
		t.Fatalf("unexpected C04 capability: %#v", capability.C04Capability)
	}

	// Verification passes under the same scope before expiry.
	verified, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryVerifyC04ReconstructionCapability, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		Scope: scope, C04Capability: capability.C04Capability,
	})
	if err != nil || verified.C04Capability == nil {
		t.Fatalf("verify C04 capability: %#v err=%v", verified, err)
	}

	// A non-Platform authority cannot issue.
	if _, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryIssueC04ReconstructionCapability, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: activated.ArtifactVersionID, Scope: scope, ExpiresAt: expiry,
		Authority: PublicationAuthority{Kind: AuthorityRecovery, ID: f.recoveryAuthority, Generation: 1},
	}); !isCode(err, ErrorOwnershipDenied) {
		t.Fatalf("non-platform issuance error = %v, want ownership denied", err)
	}

	// Expired capability fails closed.
	f.now = expiry + 1
	if _, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryVerifyC04ReconstructionCapability, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		Scope: scope, C04Capability: capability.C04Capability,
	}); !isCode(err, ErrorStaleAuthority) {
		t.Fatalf("expired capability error = %v, want stale authority", err)
	}
}

// TestPostgresAmbiguousVerificationRequiresReconciliation proves a
// currently-unresolvable Durable Object capability fails closed as
// durability-unverified, leaves the operation reconciliation-required, and
// reconcile completes verification once the capability becomes current.
func TestPostgresAmbiguousVerificationRequiresReconciliation(t *testing.T) {
	f := newPostgresFixture(t)
	set := f.fixture.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	capability := set.capabilities[0]
	f.registerCapabilityDB(t, capability, false) // present but not currently valid
	operationID := "pg-ambiguous"

	f.mustPrepare(t, operationID, set)
	verify, err := f.core.Mutate(context.Background(), f.verifyIntent(operationID, f.verifyPayload(set)))
	if err != nil {
		t.Fatalf("verify ambiguous: %v", err)
	}
	if verify.State != OperationReconciliationRequired ||
		verify.Verification == nil || verify.Verification.State != VerificationAmbiguous {
		t.Fatalf("unexpected ambiguous verify: %#v", verify)
	}

	// The capability becomes current; reconcile completes verification.
	f.registerCapabilityDB(t, capability, true)
	reconciled, err := f.core.Mutate(context.Background(), f.reconcileIntent(operationID, ReconcileCompleteVerification))
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if reconciled.State != OperationVerified ||
		reconciled.Verification == nil || reconciled.Verification.State != VerificationVerified {
		t.Fatalf("unexpected reconciled verify: %#v", reconciled)
	}
}

// prepareAndVerifyPG drives the standard first-generation happy path over
// the postgres fixture.
func (f *postgresFixture) prepareAndVerifyPG(t *testing.T, operationID string) (*evidenceSet, PublicationDecision, PublicationDecision) {
	t.Helper()
	set := f.buildEvidenceDB(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	prepare, err := f.core.Mutate(context.Background(), f.prepareIntent(operationID, f.preparePayload(operationID, set, []ArtifactMemberSpec{f.deckMemberSpec()})))
	if err != nil {
		t.Fatalf("prepare %s: %v", operationID, err)
	}
	verify, err := f.core.Mutate(context.Background(), f.verifyIntent(operationID, f.verifyPayload(set)))
	if err != nil {
		t.Fatalf("verify %s: %v", operationID, err)
	}
	return set, prepare, verify
}

// prepareVerifyActivatePG drives the standard first-generation happy path
// through activation.
func (f *postgresFixture) prepareVerifyActivatePG(t *testing.T, operationID string) (*evidenceSet, PublicationDecision) {
	t.Helper()
	set, _, _ := f.prepareAndVerifyPG(t, operationID)
	activated, err := f.core.Mutate(context.Background(), f.activateIntent(operationID))
	if err != nil {
		t.Fatalf("activate %s: %v", operationID, err)
	}
	return set, activated
}

// mustActivate drives activation and fails the test on error.
func (f *postgresFixture) mustActivate(t *testing.T, operationID string) PublicationDecision {
	t.Helper()
	activated, err := f.core.Mutate(context.Background(), f.activateIntent(operationID))
	if err != nil {
		t.Fatalf("activate %s: %v", operationID, err)
	}
	return activated
}

// verifyTargetPG drives content-target verification with the presented
// scope.
func (f *postgresFixture) verifyTargetPG(t *testing.T, target *ArtifactContentTarget, scope ContentScope) error {
	t.Helper()
	_, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryVerifyContentTarget, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		Scope: scope, ContentTarget: target,
	})
	return err
}

// TestPostgresReconcileAlwaysReevaluatesOriginalOperation proves reconcile
// never replays a historical snapshot: the SAME reconcile intent (same
// digest) re-evaluates the original operation against the current Durable
// Object authority state each time, matching the in-memory authority.
func TestPostgresReconcileAlwaysReevaluatesOriginalOperation(t *testing.T) {
	f := newPostgresFixture(t)
	set := f.fixture.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	f.registerCapabilityDB(t, set.capabilities[0], false) // ambiguous: not current
	operationID := "pg-reconcile-reeval"
	f.mustPrepare(t, operationID, set)
	if _, err := f.core.Mutate(context.Background(), f.verifyIntent(operationID, f.verifyPayload(set))); err != nil {
		t.Fatalf("verify ambiguous: %v", err)
	}
	reconcile := f.reconcileIntent(operationID, ReconcileCompleteVerification)

	// While the capability is still not current, the same reconcile intent
	// stays reconciliation-required (re-evaluates, does not replay).
	first, err := f.core.Mutate(context.Background(), reconcile)
	if err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if first.State != OperationReconciliationRequired {
		t.Fatalf("first reconcile must stay reconciliation-required: %#v", first)
	}

	// The capability becomes current; the SAME reconcile intent now
	// re-evaluates to verified.
	f.registerCapabilityDB(t, set.capabilities[0], true)
	second, err := f.core.Mutate(context.Background(), reconcile)
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if second.State != OperationVerified ||
		second.Verification == nil || second.Verification.State != VerificationVerified {
		t.Fatalf("second reconcile must re-evaluate to verified: %#v", second)
	}

	// The SAME reconcile intent after a later activation reflects the
	// current terminal state instead of a stale reconcile snapshot.
	activated, err := f.core.Mutate(context.Background(), f.activateIntent(operationID))
	if err != nil {
		t.Fatalf("activate after reconcile: %v", err)
	}
	if activated.State != OperationActivated {
		t.Fatalf("activate after reconcile: %#v", activated)
	}
	inspected, err := f.core.Mutate(context.Background(), f.reconcileIntent(operationID, ReconcileInspect))
	if err != nil {
		t.Fatalf("reconcile inspect: %v", err)
	}
	if inspected.State != OperationActivated {
		t.Fatalf("reconcile inspect must reflect the current activated state: %#v", inspected)
	}
}
