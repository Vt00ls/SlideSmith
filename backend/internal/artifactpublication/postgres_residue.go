package artifactpublication

// This file implements the C05-05 residue/debt mutation flows of the real
// PostgreSQL owned persistence adapter: durable assembly obligation
// recording, restart-safe residue release through the restricted Durable
// Object release participant, and evidence-backed (or audited) C05-owned
// Cleanup Debt resolution. Each flow runs in one real PostgreSQL
// transaction; the residue, the debt, the release receipt evidence and the
// mandatory audit are all-or-none.

import (
	"context"
	"database/sql"
)

// recordResidueAssemblyFlow durably records the C05-owned physical
// publication assembly resource of a terminal non-activated operation and
// mints the single C05-owned Cleanup Debt for it in one transaction,
// BEFORE the first physical cleanup attempt.
func (p *PostgresAuthority) recordResidueAssemblyFlow(
	ctx context.Context,
	intent RecordResidueAssembly,
	header PublicationIntentHeader,
	digest Digest,
	scope operationScope,
) (PublicationDecision, error) {
	operationID := header.Operation.ID
	if err := p.ensureAuthority(header.Authority, IntentRecordResidueAssembly); err != nil {
		return PublicationDecision{}, err
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	defer func() { _ = tx.Rollback() }()

	record, found, err := p.lockOperationRow(ctx, tx, scope)
	if err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if !found || record == nil {
		return PublicationDecision{}, &Error{Code: ErrorNotFound}
	}
	outcome, err := p.checkOutcomeReplay(ctx, tx, scope, IntentRecordResidueAssembly, digest)
	if err != nil {
		return PublicationDecision{}, err
	}
	if outcome != nil {
		decision, replayErr := replayOutcome(*outcome)
		return decision, replayErr
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
		// re-evaluation) must never mint a second DebtID.
		if record.debt.resourceRef == intent.Assembly.Reference &&
			record.debt.resourceDigest == intent.Assembly.IdentityDigest {
			return decisionForRecord(record, true, p.nowValue()), nil
		}
		return PublicationDecision{}, &Error{Code: ErrorIntegrityConflict}
	}
	now := p.nowValue()
	debtID, err := p.mintCleanupDebtID(ctx, tx)
	if err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
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
	if err := p.persistResidue(ctx, tx, scope, record.residue); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := p.saveDebt(ctx, tx, scope, record.debt); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := p.injectFault(PostgresFaultBeforeMandatoryAudit, operationID, IntentRecordResidueAssembly, string(debtID)); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := p.writeAudit(ctx, tx, header, "record_assembly", record.state, m0VersionID(record), m0ManifestDigest(record), record.streamRevision); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	decision := decisionForRecord(record, false, now)
	outcomeRecord := intentOutcome{
		digest: digest, state: record.state, decision: decision, recordedAt: now,
	}
	if err := p.saveOutcome(ctx, tx, scope, IntentRecordResidueAssembly, outcomeRecord); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := p.injectFault(PostgresFaultBeforeCommit, operationID, IntentRecordResidueAssembly, string(debtID)); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := tx.Commit(); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := p.injectFault(PostgresFaultAfterCommit, operationID, IntentRecordResidueAssembly, string(debtID)); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	return decision, nil
}

// releaseResidueFlow is the protected release intent: it re-verifies the
// exact residue facts in one transaction, requests the restricted Durable
// Object participant to physically release the exact typed staging
// references, records the evidence-backed receipt, and transitions the
// residue disposition. It is restart-safe: every submission re-evaluates
// against the current Durable Object registry, so claim loss, response
// loss and ack loss never freeze a residue and never guess success,
// failure, zero bytes or already absent.
func (p *PostgresAuthority) releaseResidueFlow(
	ctx context.Context,
	intent ReleaseResidue,
	header PublicationIntentHeader,
	digest Digest,
	scope operationScope,
) (PublicationDecision, error) {
	operationID := header.Operation.ID
	if err := p.ensureAuthority(header.Authority, IntentReleaseResidue); err != nil {
		return PublicationDecision{}, err
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	defer func() { _ = tx.Rollback() }()

	record, found, err := p.lockOperationRow(ctx, tx, scope)
	if err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if !found || record == nil {
		return PublicationDecision{}, &Error{Code: ErrorNotFound}
	}
	decision, releaseErr := p.evaluateRelease(ctx, tx, header, digest, scope, record, operationID)
	if err := p.injectFault(PostgresFaultBeforeMandatoryAudit, operationID, IntentReleaseResidue, string(operationID)); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := p.writeAudit(ctx, tx, header, "release_residue", record.state, m0VersionID(record), m0ManifestDigest(record), record.streamRevision); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	outcomeRecord := intentOutcome{
		digest: digest, state: record.state, decision: decision, recordedAt: p.nowValue(),
	}
	if err := p.saveOutcome(ctx, tx, scope, IntentReleaseResidue, outcomeRecord); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := p.injectFault(PostgresFaultBeforeCommit, operationID, IntentReleaseResidue, string(operationID)); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := tx.Commit(); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := p.injectFault(PostgresFaultAfterCommit, operationID, IntentReleaseResidue, string(operationID)); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	return decision, releaseErr
}

// evaluateRelease is the shared residue release evaluation used by the
// release intent and by ReconcileCompleteRelease. It performs the residue
// and debt state transitions and persists them in the caller's
// transaction; the caller writes the mandatory audit, the outcome and the
// COMMIT. It never allocates a new ArtifactVersionID, never changes the
// manifest/parent/head, and only transitions the residue on
// evidence-backed receipts.
func (p *PostgresAuthority) evaluateRelease(
	ctx context.Context,
	tx *sql.Tx,
	header PublicationIntentHeader,
	digest Digest,
	scope operationScope,
	record *operationRecord,
	operationID PublicationOperationID,
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
		return decisionForRecord(record, true, p.nowValue()), nil
	}
	now := p.nowValue()
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
	receipt, err := p.release.Release(ctx, tx, header.PolicyDomainID, header.TaskID, scope.operationID, header.SafetyEpoch, cloneStaging(record.residue.stagingRefs))
	if err != nil {
		category := releaseErrorCategory(normalizeError(err))
		recordResidueAttempt(record.residue, category, now)
		if record.debt != nil {
			recordDebtAttempt(record.debt, category, now)
			record.debt.revision++
		}
		if err := p.persistResidue(ctx, tx, scope, record.residue); err != nil {
			return PublicationDecision{}, normalizePersistenceError(err)
		}
		if record.debt != nil {
			if err := p.saveDebt(ctx, tx, scope, record.debt); err != nil {
				return PublicationDecision{}, normalizePersistenceError(err)
			}
		}
		decision := decisionForRecord(record, false, now)
		return decision, normalizeError(err)
	}
	if !validateReleaseReceipt(receipt, p.doAuth) {
		if receipt.Outcome == ReleaseOutcomeAmbiguous {
			// Ambiguous Durable Object receipt: the residue stays
			// release-requested and reconciliation-required; it is never
			// guessed as success, failure, zero bytes or already absent.
			// No evidence receipt is recorded.
			record.residue.requiresReconciliation = true
			recordResidueAttempt(record.residue, ResidueErrorUnavailable, now)
			if record.debt != nil {
				recordDebtAttempt(record.debt, ResidueErrorUnavailable, now)
				record.debt.revision++
			}
			if err := p.persistResidue(ctx, tx, scope, record.residue); err != nil {
				return PublicationDecision{}, normalizePersistenceError(err)
			}
			if record.debt != nil {
				if err := p.saveDebt(ctx, tx, scope, record.debt); err != nil {
					return PublicationDecision{}, normalizePersistenceError(err)
				}
			}
			return decisionForRecord(record, false, now), &Error{Code: ErrorReconciliationRequired}
		}
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
		// Ambiguous or blocked: the residue stays open and
		// reconciliation-required; it is never guessed closed.
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
	if err := p.persistResidue(ctx, tx, scope, record.residue); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if record.debt != nil {
		if err := p.saveDebt(ctx, tx, scope, record.debt); err != nil {
			return PublicationDecision{}, normalizePersistenceError(err)
		}
	}
	return decisionForRecord(record, false, now), nil
}

// resolveCleanupDebtFlow closes one C05-owned Cleanup Debt with
// evidence-backed Reclaimed/AlreadyAbsent/RetainedByAuthority or an audited
// AcceptedException in one transaction. A path disappearance, empty
// directory, object listing, log, metric or operator assertion is never
// accepted as evidence.
func (p *PostgresAuthority) resolveCleanupDebtFlow(
	ctx context.Context,
	intent ResolveCleanupDebt,
	header PublicationIntentHeader,
	digest Digest,
	scope operationScope,
) (PublicationDecision, error) {
	operationID := header.Operation.ID
	if err := p.ensureAuthority(header.Authority, IntentResolveCleanupDebt); err != nil {
		return PublicationDecision{}, err
	}
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	defer func() { _ = tx.Rollback() }()

	record, found, err := p.lockOperationRow(ctx, tx, scope)
	if err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if !found || record == nil {
		return PublicationDecision{}, &Error{Code: ErrorNotFound}
	}
	outcome, err := p.checkOutcomeReplay(ctx, tx, scope, IntentResolveCleanupDebt, digest)
	if err != nil {
		return PublicationDecision{}, err
	}
	if outcome != nil {
		decision, replayErr := replayOutcome(*outcome)
		return decision, replayErr
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
	now := p.nowValue()
	switch intent.ResolutionClass {
	case CleanupResolutionReclaimed, CleanupResolutionAlreadyAbsent, CleanupResolutionRetainedByAuthority:
		if intent.Evidence == nil || !validateCleanupResolutionEvidence(*intent.Evidence, p.doAuth, record.debt) {
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
	// The mandatory audit row is written in this same transaction; its
	// BIGSERIAL id becomes the audited resolution fact id.
	if err := p.injectFault(PostgresFaultBeforeMandatoryAudit, operationID, IntentResolveCleanupDebt, string(record.debt.debtID)); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	auditID, err := p.writeAuditReturningID(ctx, tx, header, "resolve_cleanup_debt", record.state, m0VersionID(record), m0ManifestDigest(record), record.streamRevision)
	if err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	record.debt.resolutionAuditFactID = auditID
	record.debt.revision++
	if err := p.saveDebt(ctx, tx, scope, record.debt); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	decision := decisionForRecord(record, false, now)
	outcomeRecord := intentOutcome{
		digest: digest, state: record.state, decision: decision, recordedAt: now,
	}
	if err := p.saveOutcome(ctx, tx, scope, IntentResolveCleanupDebt, outcomeRecord); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := p.injectFault(PostgresFaultBeforeCommit, operationID, IntentResolveCleanupDebt, string(record.debt.debtID)); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := tx.Commit(); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := p.injectFault(PostgresFaultAfterCommit, operationID, IntentResolveCleanupDebt, string(record.debt.debtID)); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	return decision, nil
}

// ensureReconcileAuthority enforces the reconcile authority per mode.
// ReconcileCompleteRelease is a protected cleanup operation (release
// reconciliation) and requires the publication cleanup authority; all other
// modes stay on the Task Orchestration authority.
func (p *PostgresAuthority) ensureReconcileAuthority(authority PublicationAuthority, mode ReconcileMode) *Error {
	if mode == ReconcileCompleteRelease {
		if authority.Kind != AuthorityPublicationCleanup || authority.ID != p.cleanupAuth {
			return &Error{Code: ErrorOwnershipDenied}
		}
		return nil
	}
	return p.ensureAuthority(authority, IntentReconcilePublication)
}
