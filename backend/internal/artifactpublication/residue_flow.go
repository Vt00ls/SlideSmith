package artifactpublication

// This file implements the C05-05 residue/debt mutation flows of the
// deterministic in-memory authority: durable assembly obligation recording,
// restart-safe residue release with evidence-backed receipts, and
// evidence-backed (or audited) C05-owned Cleanup Debt resolution. The real
// PostgreSQL adapter runs the same pure transitions through its own
// persistence; the domain semantics live here and in residue.go.

import (
	"context"
	"fmt"
)

// recordResidueAssembly durably records the C05-owned physical publication
// assembly resource of a terminal non-activated operation and mints the
// single C05-owned Cleanup Debt (DebtID) for it. The obligation is
// persisted BEFORE the first physical cleanup attempt; the debt is
// idempotent (an identical assembly reference exact-replays, a different
// one is a durable integrity conflict) and is never duplicated.
func (m *inMemory) recordResidueAssembly(
	ctx context.Context,
	intent RecordResidueAssembly,
	header PublicationIntentHeader,
	digest Digest,
	scope operationScope,
	record *operationRecord,
) (PublicationDecision, error) {
	operationID := header.Operation.ID
	if err := m.ensureAuthority(header.Authority, IntentRecordResidueAssembly); err != nil {
		return PublicationDecision{}, err
	}
	if record.residue == nil {
		return PublicationDecision{}, &Error{Code: ErrorInvalidIntent}
	}
	switch record.state {
	case OperationRejected, OperationCancelled:
	default:
		return PublicationDecision{}, &Error{Code: ErrorTerminalConflict}
	}
	if header.Generation != record.generation || header.Fence != record.fence {
		return PublicationDecision{}, &Error{Code: ErrorStaleAuthority}
	}
	if record.debt != nil {
		// A crash between the debt write and the response (or a duplicate
		// re-evaluation) must never mint a second DebtID: replay the
		// recorded debt facts without duplicating.
		if record.debt.resourceRef == intent.Assembly.Reference &&
			record.debt.resourceDigest == intent.Assembly.IdentityDigest {
			return decisionForRecord(record, true, m.now()), nil
		}
		return PublicationDecision{}, &Error{Code: ErrorIntegrityConflict}
	}
	now := m.now()
	debtID := m.mintCleanupDebtID()
	record.residue.assembly = &assemblyResource{
		Reference: intent.Assembly.Reference, IdentityDigest: intent.Assembly.IdentityDigest,
		Generation: intent.Assembly.Generation, Fence: intent.Assembly.Fence,
	}
	record.residue.debtID = debtID
	record.debt = &cleanupDebtRecord{
		debtID: debtID, revision: 1, policyDomainID: header.PolicyDomainID,
		taskID: header.TaskID, operationID: operationID, owner: header.Authority.Kind,
		resourceRef: intent.Assembly.Reference, resourceDigest: intent.Assembly.IdentityDigest,
		resourceGeneration: intent.Assembly.Generation, resourceFence: intent.Assembly.Fence,
		status: CleanupDebtOpen, createdAt: now, eligibleAt: now,
		claimGeneration: record.generation, claimFence: record.fence,
		retryDisposition: CleanupRetryReady,
	}
	decision := decisionForRecord(record, false, now)
	m.recordOutcome(record, IntentRecordResidueAssembly, digest, record.state, decision, nil)
	m.recordAudit(header, IntentRecordResidueAssembly, record.state, m0VersionID(record), m0ManifestDigest(record), m0LineageDigest(record), record.streamRevision)
	if err := m.injectFault(FaultBeforeResponse, operationID, IntentRecordResidueAssembly, string(debtID)); err != nil {
		return PublicationDecision{}, normalizeError(err)
	}
	return decision, nil
}

// mintCleanupDebtID allocates the next opaque, non-reused C05-owned Cleanup
// Debt identity.
func (m *inMemory) mintCleanupDebtID() CleanupDebtID {
	m.persistence.nextDebt++
	return CleanupDebtID(fmt.Sprintf("publication-cleanup-debt-%016x", m.persistence.nextDebt))
}

// releaseResidueFlow is the protected release intent: it re-verifies the exact
// residue facts, requests the Durable Object physical release of the exact
// typed staging references, and records the evidence-backed receipt. It is
// restart-safe: every submission re-evaluates against the current Durable
// Object registry so claim loss, response loss and ack loss never freeze a
// residue and never guess success, failure, zero bytes or already absent.
func (m *inMemory) releaseResidueFlow(
	ctx context.Context,
	intent ReleaseResidue,
	header PublicationIntentHeader,
	digest Digest,
	scope operationScope,
	record *operationRecord,
) (PublicationDecision, error) {
	operationID := header.Operation.ID
	if err := m.ensureAuthority(header.Authority, IntentReleaseResidue); err != nil {
		return PublicationDecision{}, err
	}
	decision, releaseErr := m.runRelease(ctx, header, digest, scope, record, operationID, false)
	// Every protected release submission is audited with the resulting
	// residue disposition, matching the PostgreSQL adapter; an ambiguous or
	// unavailable release is still an audited protected decision and never
	// guesses closure.
	m.recordAudit(header, IntentReleaseResidue, record.state, m0VersionID(record), m0ManifestDigest(record), m0LineageDigest(record), record.streamRevision)
	return decision, releaseErr
}

// runRelease is the shared residue release evaluation used by the release
// intent and by ReconcileCompleteRelease. It never allocates a new
// ArtifactVersionID, never changes the manifest/parent/head, and only
// transitions the residue on evidence-backed receipts.
func (m *inMemory) runRelease(
	ctx context.Context,
	header PublicationIntentHeader,
	digest Digest,
	scope operationScope,
	record *operationRecord,
	operationID PublicationOperationID,
	reconcile bool,
) (PublicationDecision, error) {
	if record.residue == nil {
		return PublicationDecision{}, &Error{Code: ErrorInvalidIntent}
	}
	switch record.state {
	case OperationRejected, OperationCancelled:
	default:
		return PublicationDecision{}, &Error{Code: ErrorTerminalConflict}
	}
	// Cleanup re-verifies the original operation generation/fence BEFORE
	// any physical action; a stale cleanup fails closed.
	if header.Generation != record.generation || header.Fence != record.fence {
		return PublicationDecision{}, &Error{Code: ErrorStaleAuthority}
	}
	// Evidence-backed closed residue: no physical action; a delayed or
	// duplicate receipt/release is an idempotent replay of the recorded
	// decision.
	if residueClosedByEvidence(record.residue.disposition) {
		return decisionForRecord(record, true, m.now()), nil
	}
	now := m.now()
	// Expiry marks only that the recorded retention window passed; it never
	// guesses absence and the release obligation continues.
	if record.residue.expiry != 0 && now > record.residue.expiry {
		record.residue.disposition = ResidueExpired
	}
	// C05-owned assembly debt: re-verify its identity/fence; claim it for
	// the attempt. The staging release is independent of the assembly debt:
	// a resolved debt never blocks releasing the staging references.
	if record.debt != nil {
		if record.residue.assembly == nil ||
			record.debt.resourceDigest != record.residue.assembly.IdentityDigest ||
			record.debt.resourceGeneration != record.residue.assembly.Generation ||
			record.debt.resourceFence != record.residue.assembly.Fence {
			return PublicationDecision{}, &Error{Code: ErrorIntegrityFailure}
		}
		if record.debt.status == CleanupDebtOpen || record.debt.status == CleanupDebtRetryScheduled {
			record.debt.status = CleanupDebtClaimed
			record.debt.retryDisposition = CleanupRetryClaimed
			record.debt.claimGeneration = record.generation
			record.debt.claimFence = record.fence
			record.debt.revision++
		}
	}
	record.residue.disposition = ResidueReleaseRequested
	if m.config.ReleaseStaging == nil {
		// No Durable Object release port: the residue stays open and
		// reconciliation-required; it is never guessed closed.
		record.residue.requiresReconciliation = true
		recordResidueAttempt(record.residue, ResidueErrorUnavailable, now)
		if record.debt != nil {
			recordDebtAttempt(record.debt, ResidueErrorUnavailable, now)
			record.debt.revision++
		}
		return decisionForRecord(record, false, now), &Error{Code: ErrorReconciliationRequired}
	}
	receipt, resolved, err := m.config.ReleaseStaging(cloneStaging(record.residue.stagingRefs), header.SafetyEpoch)
	if err != nil {
		category := releaseErrorCategory(normalizeError(err))
		recordResidueAttempt(record.residue, category, now)
		if record.debt != nil {
			recordDebtAttempt(record.debt, category, now)
			record.debt.revision++
		}
		decision := decisionForRecord(record, false, now)
		return decision, normalizeError(err)
	}
	if !resolved {
		// Ambiguous Durable Object receipt: keep release-requested and
		// reconciliation-required; never guessed as success, failure, zero
		// bytes or already absent.
		record.residue.requiresReconciliation = true
		recordResidueAttempt(record.residue, ResidueErrorUnavailable, now)
		if record.debt != nil {
			recordDebtAttempt(record.debt, ResidueErrorUnavailable, now)
			record.debt.revision++
		}
		if err := m.injectReleaseFault(operationID, reconcile); err != nil {
			return PublicationDecision{}, normalizeError(err)
		}
		return decisionForRecord(record, false, now), &Error{Code: ErrorReconciliationRequired}
	}
	if !validateReleaseReceipt(receipt, m.config.DurableObjectAuthorityID) {
		return PublicationDecision{}, &Error{Code: ErrorIntegrityConflict}
	}
	record.residue.releaseReceipt = cloneReleaseReceipt(&receipt)
	record.residue.disposition = applyReleaseReceipt(receipt.Outcome)
	if residueClosedByEvidence(record.residue.disposition) {
		record.residue.requiresReconciliation = false
		recordResidueAttempt(record.residue, ResidueErrorNone, now)
		if record.debt != nil {
			recordDebtAttempt(record.debt, ResidueErrorNone, now)
			record.debt.blockers = 0
			record.debt.revision++
		}
	} else {
		record.residue.requiresReconciliation = receipt.Outcome == ReleaseOutcomeAmbiguous
		recordResidueAttempt(record.residue, ResidueErrorUnavailable, now)
		if record.debt != nil {
			recordDebtAttempt(record.debt, ResidueErrorUnavailable, now)
			record.debt.blockers = receipt.Blockers
			if receipt.Outcome == ReleaseOutcomeBlocked {
				record.debt.status = CleanupDebtBlocked
				record.debt.retryDisposition = CleanupRetryBlocked
				record.debt.nextRetryAt = 0
			}
			record.debt.revision++
		}
	}
	if err := m.injectReleaseFault(operationID, reconcile); err != nil {
		return PublicationDecision{}, normalizeError(err)
	}
	return decisionForRecord(record, false, now), nil
}

// injectReleaseFault aborts the release at the response boundary so fault
// tests can prove crash-after-physical-action-before-response re-evaluates
// on retry (claim/ack loss) without freezing the residue.
func (m *inMemory) injectReleaseFault(operationID PublicationOperationID, reconcile bool) error {
	kind := IntentReleaseResidue
	if reconcile {
		kind = IntentReconcilePublication
	}
	return m.injectFault(FaultBeforeResponse, operationID, kind, string(operationID))
}

// resolveCleanupDebt closes one C05-owned Cleanup Debt with evidence-backed
// Reclaimed/AlreadyAbsent/RetainedByAuthority (evidence produced by the
// registered Durable Object authority and bound to the exact resource
// identity/generation/fence) or an audited AcceptedException. A path
// disappearance, empty directory, object listing, log, metric or operator
// assertion is never accepted.
func (m *inMemory) resolveCleanupDebt(
	ctx context.Context,
	intent ResolveCleanupDebt,
	header PublicationIntentHeader,
	digest Digest,
	scope operationScope,
	record *operationRecord,
) (PublicationDecision, error) {
	operationID := header.Operation.ID
	if err := m.ensureAuthority(header.Authority, IntentResolveCleanupDebt); err != nil {
		return PublicationDecision{}, err
	}
	if record.debt == nil {
		return PublicationDecision{}, &Error{Code: ErrorNotFound}
	}
	if record.debt.status == CleanupDebtResolved {
		return PublicationDecision{}, &Error{Code: ErrorTerminalConflict}
	}
	if header.Generation != record.generation || header.Fence != record.fence {
		return PublicationDecision{}, &Error{Code: ErrorStaleAuthority}
	}
	if record.debt.claimGeneration != 0 &&
		(header.Generation != record.debt.claimGeneration || header.Fence != record.debt.claimFence) {
		return PublicationDecision{}, &Error{Code: ErrorStaleAuthority}
	}
	now := m.now()
	switch intent.ResolutionClass {
	case CleanupResolutionReclaimed, CleanupResolutionAlreadyAbsent, CleanupResolutionRetainedByAuthority:
		if intent.Evidence == nil || !validateCleanupResolutionEvidence(*intent.Evidence, m.config.DurableObjectAuthorityID, record.debt) {
			return PublicationDecision{}, &Error{Code: ErrorIntegrityFailure}
		}
		record.debt.resolutionClass = intent.ResolutionClass
		record.debt.resolutionReason = resolutionReasonForClass(intent.ResolutionClass)
		record.debt.resolutionEvidence = cloneResolutionEvidence(intent.Evidence)
	case CleanupResolutionAcceptedException:
		record.debt.resolutionClass = CleanupResolutionAcceptedException
		record.debt.resolutionReason = CleanupResolutionReasonAdministratorException
		record.debt.resolutionApprovalRef = intent.ApprovalReference
		record.debt.resolutionExpiresAt = intent.ExpiresAt
	default:
		return PublicationDecision{}, &Error{Code: ErrorInvalidIntent}
	}
	record.debt.resolvedAt = now
	record.debt.status = CleanupDebtResolved
	record.debt.retryDisposition = CleanupRetryNone
	record.debt.blockers = 0
	record.debt.resolutionAuditFactID = m.mintAuditFactID()
	record.debt.revision++
	decision := decisionForRecord(record, false, now)
	m.recordOutcome(record, IntentResolveCleanupDebt, digest, record.state, decision, nil)
	m.recordAudit(header, IntentResolveCleanupDebt, record.state, m0VersionID(record), m0ManifestDigest(record), m0LineageDigest(record), record.streamRevision)
	if err := m.injectFault(FaultBeforeResponse, operationID, IntentResolveCleanupDebt, string(record.debt.debtID)); err != nil {
		return PublicationDecision{}, normalizeError(err)
	}
	return decision, nil
}

// mintAuditFactID allocates the next opaque mandatory-audit fact identity
// for the in-memory authority (the PostgreSQL adapter uses its owned
// BIGSERIAL audit id).
func (m *inMemory) mintAuditFactID() string {
	m.persistence.nextAudit++
	return fmt.Sprintf("audit-%016x", m.persistence.nextAudit)
}

func cloneResolutionEvidence(evidence *CleanupResolutionEvidence) *CleanupResolutionEvidence {
	if evidence == nil {
		return nil
	}
	clone := *evidence
	return &clone
}

// ensureReconcileAuthority enforces the reconcile authority per mode.
// ReconcileCompleteRelease is a protected cleanup operation (release
// reconciliation) and requires the publication cleanup authority; all other
// modes stay on the Task Orchestration authority.
func (m *inMemory) ensureReconcileAuthority(authority PublicationAuthority, mode ReconcileMode) *Error {
	if mode == ReconcileCompleteRelease {
		if authority.Kind != AuthorityPublicationCleanup || authority.ID != m.config.CleanupAuthorityID {
			return &Error{Code: ErrorOwnershipDenied}
		}
		return nil
	}
	return m.ensureAuthority(authority, IntentReconcilePublication)
}
