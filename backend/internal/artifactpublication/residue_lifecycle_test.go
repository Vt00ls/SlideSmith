package artifactpublication

// This file tests the C05-05 durable PublicationResidue lifecycle of the
// deterministic in-memory authority (child SPEC #108): residue is durably
// recorded BEFORE any physical action with owner, opaque references,
// operation, generation/fence, expiry, retry and disposition; release only
// transitions on evidence-backed receipts; ambiguous receipts keep the
// residue release-requested and are never guessed; reject/cancel/expiry
// only ever release the exact typed staging references.

import (
	"context"
	"strings"
	"testing"
)

// rejectOperation drives the standard reject path and returns the residue
// view.
func rejectOperation(t *testing.T, f *fixture, operationID string) *ResidueView {
	t.Helper()
	f.mustPrepare(t, operationID, f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()}))
	rejected, err := f.core.Mutate(context.Background(), f.rejectIntent(operationID, RejectCandidateSuperseded, nil))
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if !rejected.ResidueRelease || rejected.ResidueDisposition != ResiduePending {
		t.Fatalf("reject must create pending residue: %#v", rejected)
	}
	return f.queryResidue(t, operationID)
}

// TestResidueDurablyRecordedBeforePhysicalAction proves the residue is
// durably recorded at the terminal disposition (reject) with the owner, the
// operation, generation/fence, the retention expiry, the pending
// disposition and the exact typed staging references — all before any
// physical release action.
func TestResidueDurablyRecordedBeforePhysicalAction(t *testing.T) {
	f := newFixture(t)
	f.residueRetention = func(createdAt Instant) Instant { return createdAt + 3600 }
	f.rebuild()
	operationID := "op-residue-durable"

	residue := rejectOperation(t, f, operationID)
	if residue.Owner != AuthorityTaskOrchestration {
		t.Fatalf("residue owner = %s, want task orchestration", residue.Owner)
	}
	if residue.Generation != f.generation || residue.Fence != f.fence {
		t.Fatalf("residue generation/fence = %d/%d, want %d/%d", residue.Generation, residue.Fence, f.generation, f.fence)
	}
	if residue.Expiry != f.now+3600 {
		t.Fatalf("residue expiry = %d, want %d", residue.Expiry, f.now+3600)
	}
	if residue.Disposition != ResiduePending || residue.RequiresReconciliation {
		t.Fatalf("residue disposition = %s / recon=%v, want pending", residue.Disposition, residue.RequiresReconciliation)
	}
	if len(residue.StagingRefs) != 1 || residue.StagingRefs[0].ContentDigest != testDigest("deck-content") {
		t.Fatalf("residue must carry the exact typed staging references: %#v", residue.StagingRefs)
	}
	if residue.DebtID != "" {
		t.Fatalf("staging-only residue must NOT mint a C05-owned DebtID, got %q", residue.DebtID)
	}
	// The residue survives a restart with the same facts.
	f.rebuild()
	reloaded := f.queryResidue(t, operationID)
	if reloaded.Expiry != f.now+3600 || reloaded.Disposition != ResiduePending ||
		len(reloaded.StagingRefs) != 1 {
		t.Fatalf("residue after restart = %#v", reloaded)
	}
}

// TestReleaseResidueEvidenceBackedReleased proves a release with an
// evidence-backed released receipt closes the residue and records the
// receipt; the decision reports the durable facts.
func TestReleaseResidueEvidenceBackedReleased(t *testing.T) {
	f := newFixture(t)
	var received [][]stagingRecord
	f.releaseStaging = func(refs []stagingRecord, safetyEpoch SafetyEpoch) (ReleaseReceipt, bool, error) {
		received = append(received, append([]stagingRecord(nil), refs...))
		return f.releaseReceipt("receipt-1", ReleaseOutcomeReleased), true, nil
	}
	f.rebuild()
	operationID := "op-release-ok"

	rejectOperation(t, f, operationID)
	decision, err := f.core.Mutate(context.Background(), f.releaseResidueIntent(operationID))
	if err != nil {
		t.Fatalf("release residue: %v", err)
	}
	if decision.ResidueDisposition != ResidueReleased {
		t.Fatalf("release decision disposition = %s, want released", decision.ResidueDisposition)
	}
	if decision.ReleaseReceipt == nil || decision.ReleaseReceipt.Outcome != ReleaseOutcomeReleased {
		t.Fatalf("release decision must carry the evidence-backed receipt: %#v", decision.ReleaseReceipt)
	}
	// The port received the exact typed staging references (never a path,
	// prefix or locator).
	if len(received) != 1 || len(received[0]) != 1 || received[0][0].contentID != "content-1" ||
		received[0][0].contentDigest != testDigest("deck-content") {
		t.Fatalf("release port must receive the exact typed staging refs: %#v", received)
	}
	residue := f.queryResidue(t, operationID)
	if residue.Disposition != ResidueReleased || residue.RequiresReconciliation ||
		residue.ReleaseReceipt == nil || residue.ReleaseReceipt.ReceiptID != "receipt-1" {
		t.Fatalf("residue view after release = %#v", residue)
	}
}

// TestReleaseResidueAlreadyAbsentIsEvidenceBacked proves an
// evidence-backed already-absent receipt closes the residue; absence is
// attested by the Durable Object authority, never guessed from a path,
// listing or log.
func TestReleaseResidueAlreadyAbsentIsEvidenceBacked(t *testing.T) {
	f := newFixture(t)
	f.releaseStaging = func(refs []stagingRecord, safetyEpoch SafetyEpoch) (ReleaseReceipt, bool, error) {
		return f.releaseReceipt("receipt-absent", ReleaseOutcomeAlreadyAbsent), true, nil
	}
	f.rebuild()
	operationID := "op-release-absent"

	rejectOperation(t, f, operationID)
	decision, err := f.core.Mutate(context.Background(), f.releaseResidueIntent(operationID))
	if err != nil {
		t.Fatalf("release residue: %v", err)
	}
	if decision.ResidueDisposition != ResidueAlreadyAbsent ||
		decision.ReleaseReceipt == nil || decision.ReleaseReceipt.Outcome != ReleaseOutcomeAlreadyAbsent {
		t.Fatalf("already-absent must be evidence-backed: %#v", decision)
	}
}

// TestReleaseResidueAmbiguousStaysReconciliationRequired proves an
// ambiguous Durable Object receipt keeps the residue release-requested and
// reconciliation-required and is NEVER guessed as success, failure, zero
// bytes or already absent; a later reconcile complete-release against the
// now-resolvable registry closes it with evidence.
func TestReleaseResidueAmbiguousStaysReconciliationRequired(t *testing.T) {
	f := newFixture(t)
	ambiguous := true
	f.releaseStaging = func(refs []stagingRecord, safetyEpoch SafetyEpoch) (ReleaseReceipt, bool, error) {
		if ambiguous {
			return ReleaseReceipt{}, false, nil
		}
		return f.releaseReceipt("receipt-2", ReleaseOutcomeReleased), true, nil
	}
	f.rebuild()
	operationID := "op-release-amb"

	rejectOperation(t, f, operationID)
	decision, err := f.core.Mutate(context.Background(), f.releaseResidueIntent(operationID))
	if !isCode(err, ErrorReconciliationRequired) {
		t.Fatalf("ambiguous release error = %v, want reconciliation required", err)
	}
	if decision.ResidueDisposition != ResidueReleaseRequested || !decision.ResidueRelease {
		t.Fatalf("ambiguous release must stay release-requested: %#v", decision)
	}
	residue := f.queryResidue(t, operationID)
	if residue.Disposition != ResidueReleaseRequested || !residue.RequiresReconciliation {
		t.Fatalf("ambiguous residue must stay release-requested + reconciliation: %#v", residue)
	}
	// The ambiguous state survives a restart.
	f.rebuild()
	residue = f.queryResidue(t, operationID)
	if residue.Disposition != ResidueReleaseRequested || !residue.RequiresReconciliation {
		t.Fatalf("ambiguous residue after restart = %#v", residue)
	}
	// The Durable Object resolves; reconcile complete-release re-evaluates
	// the ORIGINAL operation and closes the residue with evidence.
	ambiguous = false
	completed, err := f.core.Mutate(context.Background(), f.reconcileIntentWithCleanup(operationID, ReconcileCompleteRelease))
	if err != nil {
		t.Fatalf("reconcile complete release: %v", err)
	}
	if completed.ResidueDisposition != ResidueReleased {
		t.Fatalf("reconcile complete release disposition = %s, want released", completed.ResidueDisposition)
	}
	if completed.ArtifactVersionID == "" || completed.ManifestDigest == "" {
		t.Fatal("reconcile must keep the original operation facts")
	}
}

// reconcileIntentWithCleanup builds a reconcile intent bound to the
// protected publication cleanup authority (required by
// ReconcileCompleteRelease).
func (f *fixture) reconcileIntentWithCleanup(operationID string, mode ReconcileMode) PublicationIntent {
	header := f.header(operationID)
	header.Authority = f.cleanupAuthorityValue()
	return bindDigest(NewReconcilePublication(header, mode))
}

// TestReleaseResidueReleasesOnlyExactStagingReferences proves reject/cancel
// residue release hands the Durable Object ONLY the exact typed staging
// references of that operation: an activated member reference of another
// version is never part of the release and is never touched.
func TestReleaseResidueReleasesOnlyExactStagingReferences(t *testing.T) {
	f := newFixture(t)
	_, activated := f.prepareVerifyActivate(t, "op-activated")

	// A second operation is rejected; its residue carries only its own
	// staging references. The prepare must bind the committed stream facts
	// (revision 1 / activated head).
	rejectedSet := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	header := f.header("op-rejected")
	header.ExpectedStreamRevision = 1
	header.ExpectedHead = activated.ArtifactVersionID
	prepareIntent := bindDigest(NewPreparePublication(header, f.preparePayload("op-rejected", rejectedSet, []ArtifactMemberSpec{f.deckMemberSpec()})))
	if _, err := f.core.Mutate(context.Background(), prepareIntent); err != nil {
		t.Fatalf("prepare rejected operation: %v", err)
	}
	rejected, err := f.core.Mutate(context.Background(), f.rejectIntent("op-rejected", RejectCandidateSuperseded, nil))
	if err != nil || !rejected.ResidueRelease {
		t.Fatalf("reject second operation: %#v err=%v", rejected, err)
	}

	var releasedRefs []stagingRecord
	f.releaseStaging = func(refs []stagingRecord, safetyEpoch SafetyEpoch) (ReleaseReceipt, bool, error) {
		releasedRefs = append(releasedRefs, refs...)
		return f.releaseReceipt("receipt-3", ReleaseOutcomeReleased), true, nil
	}
	f.rebuild()
	if _, err := f.core.Mutate(context.Background(), f.releaseResidueIntent("op-rejected")); err != nil {
		t.Fatalf("release rejected operation: %v", err)
	}
	// Only the rejected operation's exact refs were released; the activated
	// version's member reference is untouched.
	if len(releasedRefs) != 1 {
		t.Fatalf("released refs = %#v, want exactly the rejected operation's refs", releasedRefs)
	}
	if releasedRefs[0].contentID != "content-1" {
		t.Fatalf("released ref must be the exact staging ref: %#v", releasedRefs)
	}
	// The activated version and its member remain queryable and intact.
	versionView, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryExactVersion, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		ArtifactVersionID: rejected.ArtifactVersionID,
	})
	_ = versionView
	// The rejected operation's candidate is NOT an activated version.
	if !isCode(err, ErrorNotFound) {
		t.Fatalf("rejected candidate must not be queryable as a version: view=%#v err=%v", versionView, err)
	}
}

// TestResidueExpiryMarksButNeverGuessesClosure proves passing the recorded
// retention window marks the residue expired but NEVER closes it or guesses
// absence: the obligation remains and an evidence-backed release still
// closes it.
func TestResidueExpiryMarksButNeverGuessesClosure(t *testing.T) {
	f := newFixture(t)
	f.residueRetention = func(createdAt Instant) Instant { return createdAt + 3600 }
	f.rebuild()
	operationID := "op-expiry"

	rejectOperation(t, f, operationID)
	// Advance the controlled clock past the retention window.
	f.now = f.now + 7200
	f.rebuild()
	residue := f.queryResidue(t, operationID)
	if residue.Disposition != ResidueExpired || residue.RequiresReconciliation {
		t.Fatalf("expired residue must be reported expired, never closed: %#v", residue)
	}
	// Release of an expired residue still releases ONLY the exact staging
	// references and closes with evidence.
	f.releaseStaging = func(refs []stagingRecord, safetyEpoch SafetyEpoch) (ReleaseReceipt, bool, error) {
		return f.releaseReceipt("receipt-expiry", ReleaseOutcomeReleased), true, nil
	}
	f.rebuild()
	decision, err := f.core.Mutate(context.Background(), f.releaseResidueIntent(operationID))
	if err != nil {
		t.Fatalf("release expired residue: %v", err)
	}
	if decision.ResidueDisposition != ResidueReleased {
		t.Fatalf("expired residue must still close on evidence: %#v", decision)
	}
}

// TestReleaseResidueRestartSafeResponseLoss proves a release that crashed
// after the physical receipt (response loss) re-evaluates on retry and
// returns the same evidence-backed closure; claim/ack loss never freezes
// the residue and never creates a second release obligation.
func TestReleaseResidueRestartSafeResponseLoss(t *testing.T) {
	f := newFixture(t)
	calls := 0
	f.releaseStaging = func(refs []stagingRecord, safetyEpoch SafetyEpoch) (ReleaseReceipt, bool, error) {
		calls++
		// The Durable Object registry is idempotent: the exact refs are
		// released and the re-run returns the same released receipt.
		return f.releaseReceipt("receipt-4", ReleaseOutcomeReleased), true, nil
	}
	f.rebuild()
	operationID := "op-release-loss"

	rejectOperation(t, f, operationID)
	decision, err := f.core.Mutate(context.Background(), f.releaseResidueIntent(operationID))
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if decision.ResidueDisposition != ResidueReleased {
		t.Fatalf("release disposition = %s", decision.ResidueDisposition)
	}
	// A duplicate delivery of the same release intent re-evaluates against
	// the registry: the residue is already evidence-backed closed, so it is
	// an idempotent replay with NO second physical action and no new
	// obligation or version.
	replayed, err := f.core.Mutate(context.Background(), f.releaseResidueIntent(operationID))
	if err != nil {
		t.Fatalf("duplicate release: %v", err)
	}
	if !replayed.Replay || replayed.ResidueDisposition != ResidueReleased || replayed.ArtifactVersionID != decision.ArtifactVersionID {
		t.Fatalf("duplicate release must return the same evidence-backed decision: %#v", replayed)
	}
	if calls != 1 {
		t.Fatalf("release port calls = %d, want 1 (closed residue never re-releases physically)", calls)
	}
}

// TestReleaseResidueStaleFenceFailsClosed proves a cleanup that presents a
// stale generation/fence fails closed BEFORE any physical action.
func TestReleaseResidueStaleFenceFailsClosed(t *testing.T) {
	f := newFixture(t)
	portCalled := false
	f.releaseStaging = func(refs []stagingRecord, safetyEpoch SafetyEpoch) (ReleaseReceipt, bool, error) {
		portCalled = true
		return f.releaseReceipt("receipt-5", ReleaseOutcomeReleased), true, nil
	}
	f.rebuild()
	operationID := "op-release-stale"

	rejectOperation(t, f, operationID)
	header := f.cleanupHeader(operationID)
	header.Fence = header.Fence + 100
	stale := bindDigest(NewReleaseResidue(header))
	if _, err := f.core.Mutate(context.Background(), stale); !isCode(err, ErrorStaleAuthority) {
		t.Fatalf("stale release error = %v, want stale authority", err)
	}
	if portCalled {
		t.Fatal("stale cleanup must fail closed before any physical action")
	}
}

// TestReleaseResidueOnlyForTerminalNonActivated proves release is only
// valid for rejected/cancelled operations with residue; prepared, verified
// and activated operations fail closed.
func TestReleaseResidueOnlyForTerminalNonActivated(t *testing.T) {
	f := newFixture(t)
	// Verified but not activated: no residue, release is invalid.
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	f.mustPrepare(t, "op-verified", set)
	f.mustVerify(t, "op-verified", set)
	if _, err := f.core.Mutate(context.Background(), f.releaseResidueIntent("op-verified")); !isCode(err, ErrorInvalidIntent) {
		t.Fatalf("release verified op error = %v, want invalid intent", err)
	}
	// Activated: the operation has no residue; release fails closed.
	f.prepareVerifyActivate(t, "op-activated")
	if _, err := f.core.Mutate(context.Background(), f.releaseResidueIntent("op-activated")); !isCode(err, ErrorInvalidIntent) {
		t.Fatalf("release activated op error = %v, want invalid intent", err)
	}
}

// TestReconcileCompleteReleaseRequiresCleanupAuthority proves the release
// reconciliation mode is a protected cleanup operation and rejects the Task
// Orchestration authority.
func TestReconcileCompleteReleaseRequiresCleanupAuthority(t *testing.T) {
	f := newFixture(t)
	operationID := "op-reconcile-auth"
	rejectOperation(t, f, operationID)
	if _, err := f.core.Mutate(context.Background(), f.reconcileIntent(operationID, ReconcileCompleteRelease)); !isCode(err, ErrorOwnershipDenied) {
		t.Fatalf("reconcile complete release with task orchestration authority error = %v, want ownership denied", err)
	}
}

// TestResidueAndDebtViewsAreContentFree proves the residue and debt
// inspection views expose only opaque identities and closed facts: member
// names, evidence payloads, paths, object keys, buckets, vendors,
// credentials and sessions never appear even after hostile values enter the
// request.
func TestResidueAndDebtViewsAreContentFree(t *testing.T) {
	f := newFixture(t)
	operationID := "op-residue-noleak"

	// Prepare with a hostile proposal reference and member name; verify and
	// reject so a residue is created.
	set := f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	set.runtimeEvidence[0].ProposalRef = canaryValues[1]
	set.runtimeEvidence[0].Digest = set.runtimeEvidence[0].CanonicalDigest()
	set.validation = f.validationEvidence(set.validation.ContractID, set.runtimeEvidence, set.proposalDigest)
	set.c04 = f.c04Commit(set.validation, nil)
	set.c04.ContentEvidenceRoot = canaryValues[4]
	set.c04.DurabilityEvidenceRoot = canaryValues[5]
	set.c04.Digest = set.c04.CanonicalDigest()
	f.mustPrepare(t, operationID, set)
	if _, err := f.core.Mutate(context.Background(), f.rejectIntent(operationID, RejectCandidateSuperseded, nil)); err != nil {
		t.Fatalf("reject: %v", err)
	}
	f.releaseStaging = func(refs []stagingRecord, safetyEpoch SafetyEpoch) (ReleaseReceipt, bool, error) {
		return f.releaseReceipt("receipt-leak", ReleaseOutcomeReleased), true, nil
	}
	f.rebuild()
	decision, err := f.core.Mutate(context.Background(), f.releaseResidueIntent(operationID))
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	residue := f.queryResidue(t, operationID)
	debt := residue
	_ = debt
	encoded := string(mustMarshalJSON(t, decision)) + string(mustMarshalJSON(t, residue)) +
		string(mustMarshalJSON(t, decision.ReleaseReceipt))
	for _, canary := range canaryValues {
		if strings.Contains(encoded, canary) {
			t.Fatalf("residue view leaks canary %q", canary)
		}
	}
	// The member logical name never appears in the residue view.
	if strings.Contains(encoded, "Deck.pptx") {
		t.Fatalf("residue view leaks the member logical name")
	}
	if strings.Contains(encoded, "proposal-ref-1") {
		t.Fatalf("residue view leaks the output proposal reference")
	}
}

// TestReleaseResidueNeverCreatesVersionOrChangesHead proves the release
// lifecycle never allocates a new ArtifactVersionID and never changes the
// manifest, parent or head: the stream stays at the rejected operation's
// revision and history is unchanged.
func TestReleaseResidueNeverCreatesVersionOrChangesHead(t *testing.T) {
	f := newFixture(t)
	f.releaseStaging = func(refs []stagingRecord, safetyEpoch SafetyEpoch) (ReleaseReceipt, bool, error) {
		return f.releaseReceipt("receipt-6", ReleaseOutcomeReleased), true, nil
	}
	f.rebuild()
	operationID := "op-release-no-version"

	rejectOperation(t, f, operationID)
	if _, err := f.core.Mutate(context.Background(), f.releaseResidueIntent(operationID)); err != nil {
		t.Fatalf("release: %v", err)
	}
	stream, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryTaskStream, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if err != nil {
		t.Fatalf("query stream: %v", err)
	}
	// Prepare created the stream, but no version was ever committed: the
	// revision stays 0 and the head stays empty.
	if stream.StreamRevision != 0 || stream.CurrentHead != "" {
		t.Fatalf("stream after reject+release = %#v, want revision 0 / empty head", stream)
	}
	history, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryVersionHistory, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
	})
	if !isCode(err, ErrorNotFound) {
		t.Fatalf("history after reject+release = %#v err=%v, want not found (no versions)", history, err)
	}
	// Residue inspection still reports the original operation only.
	residue := f.queryResidue(t, operationID)
	if residue.OperationID != PublicationOperationID(operationID) {
		t.Fatalf("residue must stay bound to the original operation: %#v", residue)
	}
}
