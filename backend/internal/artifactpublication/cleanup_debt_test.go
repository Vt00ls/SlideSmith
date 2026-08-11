package artifactpublication

// This file tests the C05/Durable Object Cleanup Debt ownership boundary
// (child SPEC #108): a staging-only residue keeps a reconciliation backlog
// and never mints a DebtID; a residue that carries a C05-owned publication
// assembly resource creates exactly ONE C05-owned Cleanup Debt that
// supports claim, retry, backoff, blocker and safe error, re-verifies
// identity/generation/reference/fence before cleanup, and closes only on
// evidence-backed Reclaimed/AlreadyAbsent/RetainedByAuthority or an audited
// AcceptedException. Path disappearance, empty directories, object
// listings, logs, metrics and operator assertions can never close a debt.

import (
	"context"
	"testing"
)

// rejectedWithResidue prepares + rejects one operation and returns the
// residue view (the fixture's default release port is a no-op unless a
// test sets one).
func rejectedWithResidue(t *testing.T, f *fixture, operationID string) {
	t.Helper()
	f.mustPrepare(t, operationID, f.buildEvidence(t, []ArtifactMemberSpec{f.deckMemberSpec()}))
	if _, err := f.core.Mutate(context.Background(), f.rejectIntent(operationID, RejectCandidateSuperseded, nil)); err != nil {
		t.Fatalf("reject: %v", err)
	}
}

// TestStagingOnlyResidueKeepsBacklogWithoutDebtID proves a residue that
// carries only Durable Object typed staging references (the normal C05
// case) is a reconciliation backlog and NEVER mints a C05-owned Cleanup
// Debt: no DebtID is duplicated and no debt row exists.
func TestStagingOnlyResidueKeepsBacklogWithoutDebtID(t *testing.T) {
	f := newFixture(t)
	operationID := "op-backlog-only"

	rejectedWithResidue(t, f, operationID)
	residue := f.queryResidue(t, operationID)
	if residue.DebtID != "" || residue.AssemblyReference != "" {
		t.Fatalf("staging-only residue must not carry a debt or assembly ref: %#v", residue)
	}
	if _, err := f.core.Query(context.Background(), PublicationQuery{
		Kind: QueryCleanupDebt, PolicyDomainID: f.policyDomain, TaskID: f.taskID,
		OperationID: PublicationOperationID(operationID),
	}); !isCode(err, ErrorNotFound) {
		t.Fatalf("cleanup debt query error = %v, want not found (no debt minted)", err)
	}
}

// TestRecordResidueAssemblyMintsSingleDebtBeforePhysicalAttempt proves the
// C05-owned assembly cleanup obligation is persisted BEFORE the first
// physical attempt and mints exactly one DebtID; a different assembly
// reference under the same operation is a durable integrity conflict and
// never duplicates the debt.
func TestRecordResidueAssemblyMintsSingleDebtBeforePhysicalAttempt(t *testing.T) {
	f := newFixture(t)
	operationID := "op-assembly-debt"

	rejectedWithResidue(t, f, operationID)
	assembly := f.assemblyReference()
	decision, err := f.core.Mutate(context.Background(), f.recordAssemblyIntent(operationID, assembly))
	if err != nil {
		t.Fatalf("record assembly: %v", err)
	}
	if decision.CleanupDebtID == "" || decision.CleanupDebtStatus != CleanupDebtOpen {
		t.Fatalf("record assembly must mint an open C05-owned debt: %#v", decision)
	}
	debt := f.queryDebt(t, operationID)
	if debt.DebtID != decision.CleanupDebtID || debt.Owner != AuthorityPublicationCleanup ||
		debt.ResourceReference != assembly.Reference ||
		debt.ResourceIdentityDigest != assembly.IdentityDigest ||
		debt.ResourceGeneration != assembly.Generation || debt.ResourceFence != assembly.Fence {
		t.Fatalf("unexpected debt view: %#v", debt)
	}
	// The residue now carries the debt identity; the assembly ref is the
	// ONLY C05-owned physical resource.
	residue := f.queryResidue(t, operationID)
	if residue.DebtID != decision.CleanupDebtID || residue.AssemblyReference != assembly.Reference {
		t.Fatalf("residue must carry the debt and assembly ref: %#v", residue)
	}
	// A different assembly reference under the same operation is a durable
	// integrity conflict; no second debt is created.
	other := assembly
	other.Reference = "assembly-resource-2"
	if _, err := f.core.Mutate(context.Background(), f.recordAssemblyIntent(operationID, other)); !isCode(err, ErrorIntegrityConflict) {
		t.Fatalf("different assembly ref error = %v, want integrity conflict", err)
	}
	// The exact replay returns the original decision with the SAME DebtID.
	replayed, err := f.core.Mutate(context.Background(), f.recordAssemblyIntent(operationID, assembly))
	if err != nil || !replayed.Replay || replayed.CleanupDebtID != decision.CleanupDebtID {
		t.Fatalf("exact replay must return the original debt: %#v err=%v", replayed, err)
	}
}

// TestDebtClaimRetryBackoffAndBlockers proves the C05-owned debt lifecycle
// supports claim, retry, backoff and blocker facts: a retryable release
// failure claims the debt and schedules a backoff retry; a blocked receipt
// records blocker classes; a later successful attempt advances the debt.
func TestDebtClaimRetryBackoffAndBlockers(t *testing.T) {
	f := newFixture(t)
	operationID := "op-debt-retry"

	rejectedWithResidue(t, f, operationID)
	assembly := f.assemblyReference()
	if _, err := f.core.Mutate(context.Background(), f.recordAssemblyIntent(operationID, assembly)); err != nil {
		t.Fatalf("record assembly: %v", err)
	}

	// First release attempt is retryable-unavailable (backoff scheduled).
	f.releaseStaging = func(refs []stagingRecord, safetyEpoch SafetyEpoch) (ReleaseReceipt, bool, error) {
		return ReleaseReceipt{}, false, nil
	}
	f.rebuild()
	if _, err := f.core.Mutate(context.Background(), f.releaseResidueIntent(operationID)); !isCode(err, ErrorReconciliationRequired) {
		t.Fatalf("ambiguous release error = %v, want reconciliation required", err)
	}
	debt := f.queryDebt(t, operationID)
	if debt.Status != CleanupDebtClaimed || debt.AttemptCount != 1 ||
		debt.LastErrorCategory != ResidueErrorUnavailable {
		t.Fatalf("debt after retryable failure = %#v", debt)
	}
	if debt.NextRetryAt != f.now+60 {
		t.Fatalf("debt backoff next retry = %d, want %d", debt.NextRetryAt, f.now+60)
	}

	// A blocked receipt records blocker classes and keeps the debt open.
	f.releaseStaging = func(refs []stagingRecord, safetyEpoch SafetyEpoch) (ReleaseReceipt, bool, error) {
		receipt := f.releaseReceipt("receipt-blocked", ReleaseOutcomeBlocked)
		receipt.Blockers = CleanupBlockerLease | CleanupBlockerIncident
		receipt.Digest = receipt.CanonicalDigest()
		return receipt, true, nil
	}
	f.rebuild()
	if _, err := f.core.Mutate(context.Background(), f.releaseResidueIntent(operationID)); err != nil {
		t.Fatalf("blocked release: %v", err)
	}
	debt = f.queryDebt(t, operationID)
	if debt.Blockers != CleanupBlockerLease|CleanupBlockerIncident || debt.Status != CleanupDebtBlocked ||
		debt.RetryDisposition != CleanupRetryBlocked {
		t.Fatalf("debt after blocked receipt = %#v", debt)
	}

	// A later successful attempt advances the debt (attempt recorded, no
	// blind retry scheduled) while the debt stays open until evidence-backed
	// resolution.
	f.releaseStaging = func(refs []stagingRecord, safetyEpoch SafetyEpoch) (ReleaseReceipt, bool, error) {
		return f.releaseReceipt("receipt-ok", ReleaseOutcomeReleased), true, nil
	}
	f.rebuild()
	if _, err := f.core.Mutate(context.Background(), f.releaseResidueIntent(operationID)); err != nil {
		t.Fatalf("successful release: %v", err)
	}
	debt = f.queryDebt(t, operationID)
	if debt.AttemptCount != 3 || debt.NextRetryAt != 0 || debt.RetryDisposition != CleanupRetryReady {
		t.Fatalf("debt after successful attempt = %#v", debt)
	}
}

// TestDebtResolvesOnlyOnEvidenceBackedClosure proves the C05-owned debt
// closes only on evidence-backed Reclaimed/AlreadyAbsent/RetainedByAuthority
// or an audited AcceptedException: a wrong producer, wrong digest, wrong
// resource binding, or a path/listing/operator-style assertion fails
// closed.
func TestDebtResolvesOnlyOnEvidenceBackedClosure(t *testing.T) {
	f := newFixture(t)
	operationID := "op-debt-resolve"

	rejectedWithResidue(t, f, operationID)
	assembly := f.assemblyReference()
	if _, err := f.core.Mutate(context.Background(), f.recordAssemblyIntent(operationID, assembly)); err != nil {
		t.Fatalf("record assembly: %v", err)
	}

	// Evidence bound to a DIFFERENT resource identity can never close the
	// debt.
	wrongResource := f.cleanupResolutionEvidence(testDigest("other-resource"), 9, 8)
	if _, err := f.core.Mutate(context.Background(), f.resolveDebtIntent(operationID, CleanupResolutionReclaimed, wrongResource)); !isCode(err, ErrorIntegrityFailure) {
		t.Fatalf("wrong resource evidence error = %v, want integrity failure", err)
	}
	// A producer other than the registered Durable Object authority can
	// never close the debt.
	wrongProducer := f.cleanupResolutionEvidence(assembly.IdentityDigest, assembly.Generation, assembly.Fence)
	wrongProducer.Producer.AuthorityID = "some-other-authority"
	wrongProducer.Digest = wrongProducer.CanonicalDigest()
	if _, err := f.core.Mutate(context.Background(), f.resolveDebtIntent(operationID, CleanupResolutionReclaimed, wrongProducer)); !isCode(err, ErrorIntegrityFailure) {
		t.Fatalf("wrong producer evidence error = %v, want integrity failure", err)
	}
	// A tampered digest can never close the debt.
	tampered := f.cleanupResolutionEvidence(assembly.IdentityDigest, assembly.Generation, assembly.Fence)
	tampered.Digest = testDigest("tampered")
	if _, err := f.core.Mutate(context.Background(), f.resolveDebtIntent(operationID, CleanupResolutionReclaimed, tampered)); !isCode(err, ErrorIntegrityFailure) {
		t.Fatalf("tampered digest error = %v, want integrity failure", err)
	}
	// The debt is still open.
	debt := f.queryDebt(t, operationID)
	if debt.Status == CleanupDebtResolved {
		t.Fatalf("debt must stay open after failed closure attempts: %#v", debt)
	}

	// Evidence-backed Reclaimed closes it with the audit-bound resolution.
	evidence := f.cleanupResolutionEvidence(assembly.IdentityDigest, assembly.Generation, assembly.Fence)
	resolved, err := f.core.Mutate(context.Background(), f.resolveDebtIntent(operationID, CleanupResolutionReclaimed, evidence))
	if err != nil {
		t.Fatalf("resolve debt with evidence: %v", err)
	}
	if resolved.CleanupDebtStatus != CleanupDebtResolved || resolved.ResolutionClass != CleanupResolutionReclaimed {
		t.Fatalf("unexpected resolution decision: %#v", resolved)
	}
	debt = f.queryDebt(t, operationID)
	if debt.Status != CleanupDebtResolved || debt.ResolutionClass != CleanupResolutionReclaimed ||
		debt.ResolutionReason != CleanupResolutionReasonCleanupProven ||
		debt.ResolutionEvidence == nil || debt.ResolutionAuditFactID == "" {
		t.Fatalf("unexpected resolved debt: %#v", debt)
	}
	// A resolved debt cannot be resolved again: a different evidence payload
	// is a durable integrity conflict, and the exact same payload is an
	// idempotent replay of the original resolution.
	differentEvidence := f.cleanupResolutionEvidence(assembly.IdentityDigest, assembly.Generation, assembly.Fence)
	differentEvidence.EvidenceID = "cleanup-evidence-2"
	differentEvidence.Digest = differentEvidence.CanonicalDigest()
	if _, err := f.core.Mutate(context.Background(), f.resolveDebtIntent(operationID, CleanupResolutionReclaimed, differentEvidence)); !isCode(err, ErrorIntegrityConflict) {
		t.Fatalf("re-resolve with different evidence error = %v, want integrity conflict", err)
	}
	replayed, err := f.core.Mutate(context.Background(), f.resolveDebtIntent(operationID, CleanupResolutionReclaimed, evidence))
	if err != nil || !replayed.Replay || replayed.CleanupDebtStatus != CleanupDebtResolved {
		t.Fatalf("exact replay of resolution = %#v err=%v, want replay of the resolved decision", replayed, err)
	}
}

// TestDebtAcceptedExceptionRequiresAuditApproval proves the audited
// AcceptedException closure requires an approval reference, a future
// expiry, and records the mandatory audit fact; a missing approval or
// non-future expiry is rejected at payload validation.
func TestDebtAcceptedExceptionRequiresAuditApproval(t *testing.T) {
	f := newFixture(t)
	operationID := "op-debt-exception"

	rejectedWithResidue(t, f, operationID)
	assembly := f.assemblyReference()
	if _, err := f.core.Mutate(context.Background(), f.recordAssemblyIntent(operationID, assembly)); err != nil {
		t.Fatalf("record assembly: %v", err)
	}

	// Missing approval reference is an invalid intent.
	header := f.cleanupHeader(operationID)
	invalid := bindDigest(NewResolveCleanupDebtException(header, "", f.now+3600))
	if _, err := f.core.Mutate(context.Background(), invalid); !isCode(err, ErrorInvalidIntent) {
		t.Fatalf("exception without approval error = %v, want invalid intent", err)
	}
	// Expiry in the past is an invalid intent.
	invalidPast := bindDigest(NewResolveCleanupDebtException(header, "approval-1", f.now-1))
	if _, err := f.core.Mutate(context.Background(), invalidPast); !isCode(err, ErrorInvalidIntent) {
		t.Fatalf("exception with past expiry error = %v, want invalid intent", err)
	}
	// A valid audited exception closes the debt and records the audit fact.
	resolved, err := f.core.Mutate(context.Background(), f.resolveDebtExceptionIntent(operationID, "approval-1", f.now+86400))
	if err != nil {
		t.Fatalf("resolve debt exception: %v", err)
	}
	if resolved.CleanupDebtStatus != CleanupDebtResolved || resolved.ResolutionClass != CleanupResolutionAcceptedException {
		t.Fatalf("unexpected exception resolution: %#v", resolved)
	}
	debt := f.queryDebt(t, operationID)
	if debt.ResolutionClass != CleanupResolutionAcceptedException ||
		debt.ResolutionReason != CleanupResolutionReasonAdministratorException ||
		debt.ResolutionAuditFactID == "" || debt.ResolutionExpiresAt != f.now+86400 {
		t.Fatalf("unexpected audited exception debt: %#v", debt)
	}
}

// TestDebtStaleCleanupFailsClosed proves cleanup re-verifies the resource
// identity, generation, reference and fence BEFORE any physical action: a
// stale generation/fence fails closed and never touches the resource.
func TestDebtStaleCleanupFailsClosed(t *testing.T) {
	f := newFixture(t)
	portCalled := false
	f.releaseStaging = func(refs []stagingRecord, safetyEpoch SafetyEpoch) (ReleaseReceipt, bool, error) {
		portCalled = true
		return f.releaseReceipt("receipt-stale", ReleaseOutcomeReleased), true, nil
	}
	f.rebuild()
	operationID := "op-debt-stale"

	rejectedWithResidue(t, f, operationID)
	if _, err := f.core.Mutate(context.Background(), f.recordAssemblyIntent(operationID, f.assemblyReference())); err != nil {
		t.Fatalf("record assembly: %v", err)
	}
	header := f.cleanupHeader(operationID)
	header.Generation = header.Generation + 5
	staleRelease := bindDigest(NewReleaseResidue(header))
	if _, err := f.core.Mutate(context.Background(), staleRelease); !isCode(err, ErrorStaleAuthority) {
		t.Fatalf("stale release error = %v, want stale authority", err)
	}
	if portCalled {
		t.Fatal("stale cleanup must fail closed before any physical action")
	}
}

// TestDebtPathListingLogMetricCannotClose proves that a path disappearance,
// empty directory, object listing, log or metric can never close a debt:
// every closure must enter the typed ResolveCleanupDebt intent with
// evidence or an audited approval — there is no other mutation surface.
func TestDebtPathListingLogMetricCannotClose(t *testing.T) {
	f := newFixture(t)
	operationID := "op-debt-no-guess"

	rejectedWithResidue(t, f, operationID)
	if _, err := f.core.Mutate(context.Background(), f.recordAssemblyIntent(operationID, f.assemblyReference())); err != nil {
		t.Fatalf("record assembly: %v", err)
	}
	// The only mutation seam is Mutate with the closed intent union; an
	// unknown/unrecognized mutation value is rejected, and no intent exists
	// that accepts a path/listing/log/metric assertion.
	if _, err := f.core.Mutate(context.Background(), nil); !isCode(err, ErrorInvalidIntent) {
		t.Fatalf("nil mutation error = %v, want invalid intent", err)
	}
	debt := f.queryDebt(t, operationID)
	if debt.Status == CleanupDebtResolved {
		t.Fatalf("debt must not close without evidence: %#v", debt)
	}
	// A resolve intent with an empty evidence (a "path disappeared"
	// assertion has nothing else to say) fails payload validation.
	header := f.cleanupHeader(operationID)
	emptyEvidence := bindDigest(NewResolveCleanupDebtEvidence(header, CleanupResolutionReclaimed, CleanupResolutionEvidence{}))
	if _, err := f.core.Mutate(context.Background(), emptyEvidence); !isCode(err, ErrorInvalidIntent) {
		t.Fatalf("empty evidence resolve error = %v, want invalid intent", err)
	}
}

// TestDebtSurvivesRestartAndResolvesAfterRestart proves the C05-owned debt
// and its claim/attempt facts survive a restart and can be resolved by a
// fresh authority with the same evidence.
func TestDebtSurvivesRestartAndResolvesAfterRestart(t *testing.T) {
	f := newFixture(t)
	operationID := "op-debt-restart"

	rejectedWithResidue(t, f, operationID)
	assembly := f.assemblyReference()
	recorded, err := f.core.Mutate(context.Background(), f.recordAssemblyIntent(operationID, assembly))
	if err != nil {
		t.Fatalf("record assembly: %v", err)
	}
	// One retryable attempt records claim/backoff facts.
	f.releaseStaging = func(refs []stagingRecord, safetyEpoch SafetyEpoch) (ReleaseReceipt, bool, error) {
		return ReleaseReceipt{}, false, nil
	}
	f.rebuild()
	if _, err := f.core.Mutate(context.Background(), f.releaseResidueIntent(operationID)); !isCode(err, ErrorReconciliationRequired) {
		t.Fatalf("ambiguous release: %v", err)
	}

	// Restart: a fresh authority resumes the debt facts.
	f.rebuild()
	debt := f.queryDebt(t, operationID)
	if debt.DebtID != recorded.CleanupDebtID || debt.Status != CleanupDebtClaimed || debt.AttemptCount != 1 {
		t.Fatalf("debt after restart = %#v", debt)
	}
	// Evidence-backed resolution by the fresh authority closes the debt.
	evidence := f.cleanupResolutionEvidence(assembly.IdentityDigest, assembly.Generation, assembly.Fence)
	resolved, err := f.core.Mutate(context.Background(), f.resolveDebtIntent(operationID, CleanupResolutionAlreadyAbsent, evidence))
	if err != nil {
		t.Fatalf("resolve debt after restart: %v", err)
	}
	if resolved.CleanupDebtStatus != CleanupDebtResolved || resolved.ResolutionClass != CleanupResolutionAlreadyAbsent {
		t.Fatalf("unexpected resolution after restart: %#v", resolved)
	}
}
