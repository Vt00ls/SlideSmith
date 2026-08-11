package artifactpublication

// This file tests the C05-05 residue/debt lifecycle over the real
// PostgreSQL owned persistence adapter (child SPEC #108): residue is
// durably persisted before any physical action, release runs through the
// restricted same-PostgreSQL Durable Object participant with
// evidence-backed receipts, ambiguous receipts stay reconciliation-required,
// activated member references are never touched by cleanup, and the
// C05-owned Cleanup Debt closes only on evidence-backed (or audited)
// resolution and survives restarts.

import (
	"context"
	"testing"
)

// rejectPG prepares + rejects one operation over real PostgreSQL and
// returns the reject decision.
func rejectPG(t *testing.T, f *postgresFixture, operationID string) PublicationDecision {
	t.Helper()
	set := f.buildEvidenceDB(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	f.mustPrepare(t, operationID, set)
	rejected, err := f.core.Mutate(context.Background(), f.rejectIntent(operationID, RejectCandidateSuperseded, nil))
	if err != nil || !rejected.ResidueRelease {
		t.Fatalf("reject: %#v err=%v", rejected, err)
	}
	return rejected
}

// setCapabilityCurrentDB updates the current-validity flag of one
// capability in the Durable Object registry.
func (f *postgresFixture) setCapabilityCurrentDB(t *testing.T, capabilityID string, current bool) {
	t.Helper()
	if _, err := f.db.ExecContext(context.Background(), `UPDATE "`+f.schema+`"."publication_do_capability"
		SET current = $2 WHERE capability_id = $1`, capabilityID, current); err != nil {
		t.Fatalf("set capability current: %v", err)
	}
}

// deleteCapabilityDB removes a capability row (the Durable Object attests
// the exact typed reference is absent from its registry).
func (f *postgresFixture) deleteCapabilityDB(t *testing.T, capabilityID string) {
	t.Helper()
	if _, err := f.db.ExecContext(context.Background(), `DELETE FROM "`+f.schema+`"."publication_do_capability"
		WHERE capability_id = $1`, capabilityID); err != nil {
		t.Fatalf("delete capability: %v", err)
	}
}

// TestPostgresResidueDurablyPersisted proves the residue row is durably
// persisted at reject with owner, generation/fence, expiry, disposition and
// the exact typed staging references before any physical action, and that a
// fresh authority resumes it.
func TestPostgresResidueDurablyPersisted(t *testing.T) {
	f := newPostgresFixture(t)
	operationID := "pg-residue-durable"

	rejectPG(t, f, operationID)
	if got := f.countRows(t, "publication_residue"); got != 1 {
		t.Fatalf("publication_residue rows = %d, want 1", got)
	}
	if got := f.countRows(t, "publication_residue_staging"); got != 1 {
		t.Fatalf("publication_residue_staging rows = %d, want 1", got)
	}
	// A fresh authority over the same schema resumes the residue.
	f.rebuildAuthority(t)
	residue := f.queryResidue(t, operationID)
	if residue.Disposition != ResiduePending || residue.Owner != AuthorityTaskOrchestration ||
		len(residue.StagingRefs) != 1 || residue.StagingRefs[0].ContentDigest != testDigest("deck-content") {
		t.Fatalf("residue after restart = %#v", residue)
	}
}

// TestPostgresReleaseResidueEvidenceBacked proves the restricted release
// participant performs the physical release of the exact typed staging
// references and returns an evidence-backed released receipt; the residue
// closes and the Durable Object registry attests the release.
func TestPostgresReleaseResidueEvidenceBacked(t *testing.T) {
	f := newPostgresFixture(t)
	operationID := "pg-release-ok"

	rejectPG(t, f, operationID)
	decision, err := f.core.Mutate(context.Background(), f.releaseResidueIntent(operationID))
	if err != nil {
		t.Fatalf("release residue: %v", err)
	}
	if decision.ResidueDisposition != ResidueReleased || decision.ReleaseReceipt == nil ||
		decision.ReleaseReceipt.Outcome != ReleaseOutcomeReleased {
		t.Fatalf("release decision = %#v, want evidence-backed released", decision)
	}
	residue := f.queryResidue(t, operationID)
	if residue.Disposition != ResidueReleased || residue.ReleaseReceipt == nil || residue.RequiresReconciliation {
		t.Fatalf("residue after release = %#v", residue)
	}
	// The Durable Object release evidence row exists and the capability is
	// marked released.
	if got := f.countRows(t, "publication_do_release"); got < 1 {
		t.Fatalf("publication_do_release rows = %d, want at least 1", got)
	}
	var released bool
	if err := f.db.QueryRowContext(context.Background(),
		`SELECT released FROM "`+f.schema+`"."publication_do_capability" WHERE capability_id = $1`,
		"capability-1").Scan(&released); err != nil || !released {
		t.Fatalf("capability released flag = %v err=%v, want true", released, err)
	}
	// A duplicate release delivery is an idempotent replay with no new
	// release evidence row.
	before := f.countRows(t, "publication_do_release")
	replayed, err := f.core.Mutate(context.Background(), f.releaseResidueIntent(operationID))
	if err != nil || !replayed.Replay || replayed.ResidueDisposition != ResidueReleased {
		t.Fatalf("duplicate release = %#v err=%v, want idempotent replay", replayed, err)
	}
	if got := f.countRows(t, "publication_do_release"); got != before {
		t.Fatalf("duplicate release must not create new release evidence: before=%d after=%d", before, got)
	}
}

// TestPostgresReleaseAlreadyAbsent proves the Durable Object authority
// attests the exact references are already absent from its registry and
// the release closes with the already-absent evidence receipt.
func TestPostgresReleaseAlreadyAbsent(t *testing.T) {
	f := newPostgresFixture(t)
	operationID := "pg-release-absent"

	rejectPG(t, f, operationID)
	// The Durable Object authority registry has no capability for the exact
	// typed reference: it attests already absent.
	f.deleteCapabilityDB(t, "capability-1")
	decision, err := f.core.Mutate(context.Background(), f.releaseResidueIntent(operationID))
	if err != nil {
		t.Fatalf("release residue: %v", err)
	}
	if decision.ResidueDisposition != ResidueAlreadyAbsent ||
		decision.ReleaseReceipt == nil || decision.ReleaseReceipt.Outcome != ReleaseOutcomeAlreadyAbsent {
		t.Fatalf("release decision = %#v, want evidence-backed already absent", decision)
	}
}

// TestPostgresReleaseAmbiguousStaysReconciliationRequired proves a
// capability that is not currently resolvable (current=false) keeps the
// residue release-requested and reconciliation-required and is never
// guessed as success, failure, zero bytes or already absent; a later
// reconcile complete-release closes it with evidence once the capability is
// current again.
func TestPostgresReleaseAmbiguousStaysReconciliationRequired(t *testing.T) {
	f := newPostgresFixture(t)
	operationID := "pg-release-amb"

	rejectPG(t, f, operationID)
	f.setCapabilityCurrentDB(t, "capability-1", false)
	decision, err := f.core.Mutate(context.Background(), f.releaseResidueIntent(operationID))
	if !isCode(err, ErrorReconciliationRequired) {
		t.Fatalf("ambiguous release error = %v, want reconciliation required", err)
	}
	if decision.ResidueDisposition != ResidueReleaseRequested {
		t.Fatalf("ambiguous release must stay release-requested: %#v", decision)
	}
	residue := f.queryResidue(t, operationID)
	if residue.Disposition != ResidueReleaseRequested || !residue.RequiresReconciliation {
		t.Fatalf("ambiguous residue = %#v", residue)
	}
	// Restart; the capability becomes current; reconcile complete-release
	// re-evaluates the ORIGINAL operation and closes the residue.
	f.rebuildAuthority(t)
	f.setCapabilityCurrentDB(t, "capability-1", true)
	completed, err := f.core.Mutate(context.Background(), f.reconcileIntentWithCleanup(operationID, ReconcileCompleteRelease))
	if err != nil {
		t.Fatalf("reconcile complete release: %v", err)
	}
	if completed.ResidueDisposition != ResidueReleased {
		t.Fatalf("reconcile complete release disposition = %s, want released", completed.ResidueDisposition)
	}
}

// TestPostgresReleaseNeverTouchesActivatedMember proves release of a
// rejected operation's residue never touches an activated member reference:
// the attach rows of the activated version remain intact and a release that
// would collide with an attached reference fails closed.
func TestPostgresReleaseNeverTouchesActivatedMember(t *testing.T) {
	f := newPostgresFixture(t)
	_, activated := f.prepareVerifyActivatePG(t, "pg-activated")
	if activated.State != OperationActivated {
		t.Fatalf("activation failed: %#v", activated)
	}
	// A second operation on the same stream is rejected.
	rejectedSet := f.buildEvidenceDB(t, []ArtifactMemberSpec{f.deckMemberSpec()})
	header := f.header("pg-rejected")
	header.ExpectedStreamRevision = 1
	header.ExpectedHead = activated.ArtifactVersionID
	prepareIntent := bindDigest(NewPreparePublication(header, f.preparePayload("pg-rejected", rejectedSet, []ArtifactMemberSpec{f.deckMemberSpec()})))
	if _, err := f.core.Mutate(context.Background(), prepareIntent); err != nil {
		t.Fatalf("prepare rejected operation: %v", err)
	}
	if _, err := f.core.Mutate(context.Background(), f.rejectIntent("pg-rejected", RejectCandidateSuperseded, nil)); err != nil {
		t.Fatalf("reject: %v", err)
	}
	// The rejected operation shares the exact content with the activated
	// member; releasing its residue must fail closed because the reference
	// was attached to the activated version.
	if _, err := f.core.Mutate(context.Background(), f.releaseResidueIntent("pg-rejected")); !isCode(err, ErrorTerminalConflict) {
		t.Fatalf("release colliding with activated member error = %v, want terminal conflict", err)
	}
	// The activated attach row is untouched.
	if got := f.countRows(t, "publication_attach"); got != 1 {
		t.Fatalf("publication_attach rows = %d, want 1 (activated member untouched)", got)
	}
}

// TestPostgresCleanupDebtLifecycle proves the C05-owned Cleanup Debt is
// persisted before the first physical attempt, claimed/backoffed on
// attempts, resolved only on evidence-backed closure, and survives
// restarts.
func TestPostgresCleanupDebtLifecycle(t *testing.T) {
	f := newPostgresFixture(t)
	operationID := "pg-debt-lifecycle"

	rejectPG(t, f, operationID)
	assembly := f.assemblyReference()
	recorded, err := f.core.Mutate(context.Background(), f.recordAssemblyIntent(operationID, assembly))
	if err != nil {
		t.Fatalf("record assembly: %v", err)
	}
	if recorded.CleanupDebtID == "" || f.countRows(t, "publication_cleanup_debt") != 1 {
		t.Fatalf("record assembly must mint exactly one debt row: %#v rows=%d", recorded, f.countRows(t, "publication_cleanup_debt"))
	}
	// A retryable release attempt (capability not currently resolvable)
	// claims the debt and schedules backoff.
	f.setCapabilityCurrentDB(t, "capability-1", false)
	if _, err := f.core.Mutate(context.Background(), f.releaseResidueIntent(operationID)); !isCode(err, ErrorReconciliationRequired) {
		t.Fatalf("release with unresolved capability: %v", err)
	}
	// The capability is current again, so the release succeeds; the debt
	// records the attempt and stays open until evidence-backed resolution.
	f.setCapabilityCurrentDB(t, "capability-1", true)
	decision, err := f.core.Mutate(context.Background(), f.releaseResidueIntent(operationID))
	if err != nil || decision.ResidueDisposition != ResidueReleased {
		t.Fatalf("release after capability resolvable: %#v err=%v", decision, err)
	}
	// Restart resumes the debt with its attempt facts.
	f.rebuildAuthority(t)
	debt := f.queryDebt(t, operationID)
	if debt.DebtID != recorded.CleanupDebtID || debt.AttemptCount == 0 || debt.Status == CleanupDebtResolved {
		t.Fatalf("debt after restart = %#v", debt)
	}
	// Evidence-backed Reclaimed closes the debt with an audit fact.
	evidence := f.cleanupResolutionEvidence(assembly.IdentityDigest, assembly.Generation, assembly.Fence)
	resolved, err := f.core.Mutate(context.Background(), f.resolveDebtIntent(operationID, CleanupResolutionReclaimed, evidence))
	if err != nil {
		t.Fatalf("resolve debt: %v", err)
	}
	if resolved.CleanupDebtStatus != CleanupDebtResolved || resolved.ResolutionClass != CleanupResolutionReclaimed {
		t.Fatalf("unexpected resolution: %#v", resolved)
	}
	debt = f.queryDebt(t, operationID)
	if debt.Status != CleanupDebtResolved || debt.ResolutionAuditFactID == "" ||
		debt.ResolutionEvidence == nil || debt.ResolutionEvidence.Digest != evidence.Digest {
		t.Fatalf("unexpected resolved debt: %#v", debt)
	}
	// A wrong-producer evidence can never close a different debt.
	rejectPG(t, f, "pg-debt-2")
	f.mustRecordAssembly(t, "pg-debt-2", f.assemblyReference())
	wrongProducer := f.cleanupResolutionEvidence(assembly.IdentityDigest, assembly.Generation, assembly.Fence)
	wrongProducer.Producer.AuthorityID = "not-the-do-authority"
	wrongProducer.Digest = wrongProducer.CanonicalDigest()
	if _, err := f.core.Mutate(context.Background(), f.resolveDebtIntent("pg-debt-2", CleanupResolutionAlreadyAbsent, wrongProducer)); !isCode(err, ErrorIntegrityFailure) {
		t.Fatalf("wrong producer resolution error = %v, want integrity failure", err)
	}
}

// mustRecordAssembly records the C05-owned assembly obligation and returns
// the debt identity.
func (f *postgresFixture) mustRecordAssembly(t *testing.T, operationID string, assembly AssemblyReference) CleanupDebtID {
	t.Helper()
	decision, err := f.core.Mutate(context.Background(), f.recordAssemblyIntent(operationID, assembly))
	if err != nil {
		t.Fatalf("record assembly: %v", err)
	}
	if decision.CleanupDebtID == "" {
		t.Fatal("record assembly returned no debt id")
	}
	return decision.CleanupDebtID
}

// TestPostgresReleaseStaleFailsClosed proves a stale generation/fence on
// the release fails closed before any physical action.
func TestPostgresReleaseStaleFailsClosed(t *testing.T) {
	f := newPostgresFixture(t)
	operationID := "pg-release-stale"

	rejectPG(t, f, operationID)
	header := f.cleanupHeader(operationID)
	header.Fence = header.Fence + 7
	stale := bindDigest(NewReleaseResidue(header))
	if _, err := f.core.Mutate(context.Background(), stale); !isCode(err, ErrorStaleAuthority) {
		t.Fatalf("stale release error = %v, want stale authority", err)
	}
	if got := f.countRows(t, "publication_do_release"); got != 0 {
		t.Fatalf("stale cleanup must not produce release evidence, got %d rows", got)
	}
}

// TestPostgresResidueRestartSafeResponseLoss proves response loss on the
// release re-evaluates the original operation after restart and returns the
// same evidence-backed decision without reallocating identity.
func TestPostgresResidueRestartSafeResponseLoss(t *testing.T) {
	f := newPostgresFixture(t)
	operationID := "pg-release-loss"

	rejectPG(t, f, operationID)
	decision, err := f.core.Mutate(context.Background(), f.releaseResidueIntent(operationID))
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if decision.ResidueDisposition != ResidueReleased {
		t.Fatalf("release disposition = %s", decision.ResidueDisposition)
	}
	// Restart + duplicate delivery returns the same decision.
	f.rebuildAuthority(t)
	replayed, err := f.core.Mutate(context.Background(), f.releaseResidueIntent(operationID))
	if err != nil || !replayed.Replay || replayed.ResidueDisposition != ResidueReleased ||
		replayed.ArtifactVersionID != decision.ArtifactVersionID {
		t.Fatalf("replay after restart = %#v err=%v, want the original released decision", replayed, err)
	}
	// No version was ever created.
	if got := f.countRows(t, "publication_activated"); got != 0 {
		t.Fatalf("publication_activated rows = %d, want 0 (release never creates a version)", got)
	}
}
