package artifactpublication

// This file is the SQL row codec of the real PostgreSQL owned persistence
// adapter. It loads the shared invariant-engine record structs
// (operationRecord, candidateRecord, streamRecord, activatedRecord,
// verificationRecord, intentOutcome) from the owned tables so the same
// pure decision/view builders run for the in-memory authority and the
// PostgreSQL adapter. Nothing here is exported: SQL shape is private.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// sqlQuerier is the narrow query surface shared by *sql.DB and *sql.Tx so
// the row loaders can run either autocommit (query) or inside a mutation
// transaction.
type sqlQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// loadStream loads one Task's explicit publication stream facts. It returns
// (nil, false) when the Task has no stream row yet.
func (p *PostgresAuthority) loadStream(ctx context.Context, querier sqlQuerier, key taskScope) (*streamRecord, bool, error) {
	var record streamRecord
	err := querier.QueryRowContext(ctx, `SELECT policy_domain_id, task_id, revision, current_head
		FROM `+p.q("publication_stream")+` WHERE policy_domain_id = $1 AND task_id = $2`,
		string(key.policyDomainID), string(key.taskID)).
		Scan(&record.policyDomainID, &record.taskID, &record.revision, &record.currentHead)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return &record, true, nil
}

// lockStreamForUpdate locks (creating when absent) one Task's stream row
// for the row-lock/CAS revalidation of prepare and activation. The create
// is idempotent so two first prepares cannot double-insert.
func (p *PostgresAuthority) lockStreamForUpdate(ctx context.Context, tx *sql.Tx, key taskScope) (*streamRecord, error) {
	if _, err := tx.ExecContext(ctx, `INSERT INTO `+p.q("publication_stream")+`
		(policy_domain_id, task_id, revision, current_head)
		VALUES ($1, $2, 0, '') ON CONFLICT DO NOTHING`,
		string(key.policyDomainID), string(key.taskID)); err != nil {
		return nil, err
	}
	var record streamRecord
	err := tx.QueryRowContext(ctx, `SELECT policy_domain_id, task_id, revision, current_head
		FROM `+p.q("publication_stream")+` WHERE policy_domain_id = $1 AND task_id = $2 FOR UPDATE`,
		string(key.policyDomainID), string(key.taskID)).
		Scan(&record.policyDomainID, &record.taskID, &record.revision, &record.currentHead)
	if err != nil {
		return nil, err
	}
	return &record, nil
}

// loadOutcome loads the single recorded outcome of one intent kind for one
// operation, or (nil, false) when none exists.
func (p *PostgresAuthority) loadOutcome(ctx context.Context, querier sqlQuerier, scope operationScope, kind PublicationIntentKind) (*intentOutcome, bool, error) {
	var digestValue string
	var state string
	var decisionJSON []byte
	var errCode sql.NullString
	var recordedAt int64
	err := querier.QueryRowContext(ctx, `SELECT digest, state, decision, err_code, recorded_at
		FROM `+p.q("publication_outcome")+`
		WHERE policy_domain_id = $1 AND task_id = $2 AND operation_id = $3 AND intent_kind = $4`,
		string(scope.policyDomainID), string(scope.taskID), string(scope.operationID), string(kind)).
		Scan(&digestValue, &state, &decisionJSON, &errCode, &recordedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, err
	}
	var decision PublicationDecision
	if err := json.Unmarshal(decisionJSON, &decision); err != nil {
		return nil, false, err
	}
	var outcomeErr *Error
	if errCode.Valid && errCode.String != "" {
		outcomeErr = &Error{Code: ErrorCode(errCode.String)}
	}
	outcome := &intentOutcome{
		digest: Digest(digestValue), state: PublicationOperationState(state),
		decision: decision, err: outcomeErr, recordedAt: Instant(recordedAt),
	}
	return outcome, true, nil
}

// saveOutcome inserts the single outcome of one intent kind. The unique
// primary key (operation, intent_kind) enforces exactly one outcome per
// kind; the insert is an idempotent upsert because reconcile (which always
// re-evaluates the original operation, like the in-memory authority) may
// record a fresh outcome over an earlier reconcile outcome. Every other
// flow locks the operation row and checks replay/conflict before saving, so
// no other kind can reach this insert twice.
func (p *PostgresAuthority) saveOutcome(ctx context.Context, tx *sql.Tx, scope operationScope, kind PublicationIntentKind, outcome intentOutcome) error {
	decisionJSON, err := marshalJSONSafe(outcome.decision)
	if err != nil {
		return err
	}
	var errCode any
	if outcome.err != nil {
		errCode = string(outcome.err.Code)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO `+p.q("publication_outcome")+`
		(policy_domain_id, task_id, operation_id, intent_kind, digest, state, decision, err_code, recorded_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (policy_domain_id, task_id, operation_id, intent_kind) DO UPDATE
			SET digest = EXCLUDED.digest, state = EXCLUDED.state, decision = EXCLUDED.decision,
			    err_code = EXCLUDED.err_code, recorded_at = EXCLUDED.recorded_at`,
		string(scope.policyDomainID), string(scope.taskID), string(scope.operationID),
		string(kind), string(outcome.digest), string(outcome.state), decisionJSON, errCode, int64(outcome.recordedAt))
	return err
}

// loadOperation loads the full durable record of one operation: journal
// fields, candidate (members, staging, pinned references), verification
// result, activation evidence, residue, and residue staging references.
func (p *PostgresAuthority) loadOperation(ctx context.Context, querier sqlQuerier, scope operationScope) (*operationRecord, bool, error) {
	var policyDomainID, taskID, operationID, requestDigest, state string
	var streamRevision, generation, fence, safetyEpoch, activityGeneration, occurredAt uint64
	var rejectReason, cancelReason, reconcileMode sql.NullString
	var integrityConflict bool
	var activationEvidenceJSON []byte
	err := querier.QueryRowContext(ctx, `SELECT policy_domain_id, task_id, operation_id, request_digest, state,
		stream_revision, generation, fence, safety_epoch, activity_generation, occurred_at,
		reject_reason, cancel_reason, reconcile_mode, integrity_conflict, activation_evidence
		FROM `+p.q("publication_operation")+`
		WHERE policy_domain_id = $1 AND task_id = $2 AND operation_id = $3`,
		string(scope.policyDomainID), string(scope.taskID), string(scope.operationID)).
		Scan(&policyDomainID, &taskID, &operationID, &requestDigest, &state,
			&streamRevision, &generation, &fence, &safetyEpoch, &activityGeneration, &occurredAt,
			&rejectReason, &cancelReason, &reconcileMode, &integrityConflict, &activationEvidenceJSON)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, err
	}
	record := &operationRecord{
		scope:              operationScope{policyDomainID: PolicyDomainID(policyDomainID), taskID: TaskID(taskID), operationID: PublicationOperationID(operationID)},
		operationID:        PublicationOperationID(operationID),
		requestDigest:      Digest(requestDigest),
		state:              PublicationOperationState(state),
		streamRevision:     StreamRevision(streamRevision),
		generation:         Generation(generation),
		fence:              Fence(fence),
		safetyEpoch:        SafetyEpoch(safetyEpoch),
		activityGeneration: Generation(activityGeneration),
		occurredAt:         Instant(occurredAt),
		integrityConflict:  integrityConflict,
	}
	if rejectReason.Valid {
		record.rejectReason = RejectReason(rejectReason.String)
	}
	if cancelReason.Valid {
		record.cancelReason = CancelReason(cancelReason.String)
	}
	if reconcileMode.Valid {
		record.reconcileMode = ReconcileMode(reconcileMode.String)
	}
	if len(activationEvidenceJSON) > 0 && string(activationEvidenceJSON) != "null" {
		var evidence PublicationEvidence
		if err := json.Unmarshal(activationEvidenceJSON, &evidence); err != nil {
			return nil, false, err
		}
		record.activationEvidence = &evidence
	}
	if record.state != OperationPrepared && record.state != OperationVerified &&
		record.state != OperationActivated && record.state != OperationRejected &&
		record.state != OperationCancelled && record.state != OperationReconciliationRequired {
		return nil, false, errStateCorrupt()
	}
	candidate, found, err := p.loadCandidate(ctx, querier, scope)
	if err != nil {
		return nil, false, err
	}
	if found {
		record.candidate = candidate
	}
	verification, found, err := p.loadVerification(ctx, querier, scope)
	if err != nil {
		return nil, false, err
	}
	if found {
		record.verification = verification
	}
	residue, found, err := p.loadResidue(ctx, querier, scope)
	if err != nil {
		return nil, false, err
	}
	if found {
		record.residue = residue
	}
	debt, found, err := p.loadDebt(ctx, querier, scope)
	if err != nil {
		return nil, false, err
	}
	if found {
		record.debt = debt
	}
	return record, true, nil
}

// loadCandidate loads the immutable candidate, its members, staging
// references, and pinned evidence references.
func (p *PostgresAuthority) loadCandidate(ctx context.Context, querier sqlQuerier, scope operationScope) (*candidateRecord, bool, error) {
	var policyDomainID, taskID, operationID, versionID string
	var schemaVersion uint64
	var kind, parent, contractID, phaseRunID, manifestDigest, lineageDigest string
	var channelsJSON []byte
	var validationRefEvidenceID, validationRefDigest, c04RefEvidenceID, c04RefDigest string
	err := querier.QueryRowContext(ctx, `SELECT policy_domain_id, task_id, operation_id, version_id, schema_version,
		kind, parent, contract_id, phase_run_id, manifest_digest, lineage_digest, required_channels,
		validation_ref_evidence_id, validation_ref_digest, c04_ref_evidence_id, c04_ref_digest
		FROM `+p.q("publication_candidate")+`
		WHERE policy_domain_id = $1 AND task_id = $2 AND operation_id = $3`,
		string(scope.policyDomainID), string(scope.taskID), string(scope.operationID)).
		Scan(&policyDomainID, &taskID, &operationID, &versionID, &schemaVersion,
			&kind, &parent, &contractID, &phaseRunID, &manifestDigest, &lineageDigest, &channelsJSON,
			&validationRefEvidenceID, &validationRefDigest, &c04RefEvidenceID, &c04RefDigest)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, err
	}
	candidate := &candidateRecord{
		versionID:      ArtifactVersionID(versionID),
		schemaVersion:  SchemaVersion(schemaVersion),
		kind:           PublicationKind(kind),
		parent:         ArtifactVersionID(parent),
		contractID:     PublicationContractID(contractID),
		phaseRunID:     PhaseRunID(phaseRunID),
		manifestDigest: Digest(manifestDigest),
		lineageDigest:  Digest(lineageDigest),
		validationRef:  EvidenceRef{EvidenceID: EvidenceID(validationRefEvidenceID), Digest: Digest(validationRefDigest)},
		c04Ref:         EvidenceRef{EvidenceID: EvidenceID(c04RefEvidenceID), Digest: Digest(c04RefDigest)},
	}
	var channels []string
	if err := json.Unmarshal(channelsJSON, &channels); err != nil {
		return nil, false, err
	}
	for _, channel := range channels {
		candidate.requiredChannels = append(candidate.requiredChannels, ChannelKind(channel))
	}
	members, err := p.loadMembers(ctx, querier, scope)
	if err != nil {
		return nil, false, err
	}
	candidate.members = members
	staging, err := p.loadStaging(ctx, querier, scope)
	if err != nil {
		return nil, false, err
	}
	candidate.staging = staging
	runtimeRefs, err := p.loadRuntimeRefs(ctx, querier, scope)
	if err != nil {
		return nil, false, err
	}
	candidate.runtimeRefs = runtimeRefs
	capabilityRefs, err := p.loadCapabilityRefs(ctx, querier, scope)
	if err != nil {
		return nil, false, err
	}
	candidate.capabilityRefs = capabilityRefs
	return candidate, true, nil
}

func (p *PostgresAuthority) loadMembers(ctx context.Context, querier sqlQuerier, scope operationScope) ([]memberRecord, error) {
	rows, err := querier.QueryContext(ctx, `SELECT slot, artifact_id, kind, logical_name, media_type, size, content_digest
		FROM `+p.q("publication_member")+`
		WHERE policy_domain_id = $1 AND task_id = $2 AND operation_id = $3
		ORDER BY slot`, string(scope.policyDomainID), string(scope.taskID), string(scope.operationID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var members []memberRecord
	for rows.Next() {
		var member memberRecord
		var slot, artifactID, kind, logicalName, mediaType, contentDigest string
		var size uint64
		if err := rows.Scan(&slot, &artifactID, &kind, &logicalName, &mediaType, &size, &contentDigest); err != nil {
			return nil, err
		}
		member = memberRecord{
			slot: MemberSlotID(slot), artifactID: ArtifactID(artifactID), kind: ArtifactKind(kind),
			logicalName: logicalName, mediaType: MediaType(mediaType), size: size, contentDigest: Digest(contentDigest),
		}
		members = append(members, member)
	}
	return members, rows.Err()
}

func (p *PostgresAuthority) loadStaging(ctx context.Context, querier sqlQuerier, scope operationScope) ([]stagingRecord, error) {
	rows, err := querier.QueryContext(ctx, `SELECT slot, content_id, content_digest, size, purpose, physical_generation, adapter_id
		FROM `+p.q("publication_staging")+`
		WHERE policy_domain_id = $1 AND task_id = $2 AND operation_id = $3
		ORDER BY slot`, string(scope.policyDomainID), string(scope.taskID), string(scope.operationID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var staging []stagingRecord
	for rows.Next() {
		var record stagingRecord
		var slot, contentID, contentDigest, purpose, adapterID string
		var size, physicalGeneration uint64
		if err := rows.Scan(&slot, &contentID, &contentDigest, &size, &purpose, &physicalGeneration, &adapterID); err != nil {
			return nil, err
		}
		record = stagingRecord{
			slot: MemberSlotID(slot), contentID: ContentID(contentID), contentDigest: Digest(contentDigest),
			size: size, purpose: ContentPurpose(purpose), physicalGeneration: physicalGeneration, adapterID: AdapterID(adapterID),
		}
		staging = append(staging, record)
	}
	return staging, rows.Err()
}

func (p *PostgresAuthority) loadRuntimeRefs(ctx context.Context, querier sqlQuerier, scope operationScope) ([]RuntimeEvidenceRef, error) {
	rows, err := querier.QueryContext(ctx, `SELECT channel, evidence_id, digest
		FROM `+p.q("publication_runtime_ref")+`
		WHERE policy_domain_id = $1 AND task_id = $2 AND operation_id = $3
		ORDER BY channel`, string(scope.policyDomainID), string(scope.taskID), string(scope.operationID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var refs []RuntimeEvidenceRef
	for rows.Next() {
		var ref RuntimeEvidenceRef
		var channel, evidenceID, digest string
		if err := rows.Scan(&channel, &evidenceID, &digest); err != nil {
			return nil, err
		}
		ref = RuntimeEvidenceRef{Channel: ChannelKind(channel), EvidenceID: EvidenceID(evidenceID), Digest: Digest(digest)}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

func (p *PostgresAuthority) loadCapabilityRefs(ctx context.Context, querier sqlQuerier, scope operationScope) ([]ContentCapabilityRef, error) {
	rows, err := querier.QueryContext(ctx, `SELECT member_slot, capability_id, digest
		FROM `+p.q("publication_capability_ref")+`
		WHERE policy_domain_id = $1 AND task_id = $2 AND operation_id = $3
		ORDER BY member_slot`, string(scope.policyDomainID), string(scope.taskID), string(scope.operationID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var refs []ContentCapabilityRef
	for rows.Next() {
		var ref ContentCapabilityRef
		var slot, capabilityID, digest string
		if err := rows.Scan(&slot, &capabilityID, &digest); err != nil {
			return nil, err
		}
		ref = ContentCapabilityRef{MemberSlot: MemberSlotID(slot), CapabilityID: ContentCapabilityID(capabilityID), Digest: Digest(digest)}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

func (p *PostgresAuthority) loadVerification(ctx context.Context, querier sqlQuerier, scope operationScope) (*verificationRecord, bool, error) {
	var state string
	var failureKind, failureEvidenceID, failureCapabilityID sql.NullString
	var pendingSlotsJSON []byte
	err := querier.QueryRowContext(ctx, `SELECT state, failure_kind, failure_evidence_id, failure_capability_id, pending_slots
		FROM `+p.q("publication_verification")+`
		WHERE policy_domain_id = $1 AND task_id = $2 AND operation_id = $3`,
		string(scope.policyDomainID), string(scope.taskID), string(scope.operationID)).
		Scan(&state, &failureKind, &failureEvidenceID, &failureCapabilityID, &pendingSlotsJSON)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, err
	}
	record := &verificationRecord{state: VerificationState(state)}
	if failureKind.Valid && failureKind.String != "" {
		record.failure = &EvidenceFailure{
			Kind:         failureKind.String,
			EvidenceID:   EvidenceID(failureEvidenceID.String),
			CapabilityID: ContentCapabilityID(failureCapabilityID.String),
		}
	}
	if len(pendingSlotsJSON) > 0 && string(pendingSlotsJSON) != "null" {
		var slots []string
		if err := json.Unmarshal(pendingSlotsJSON, &slots); err != nil {
			return nil, false, err
		}
		for _, slot := range slots {
			record.pendingCapabilitySlots = append(record.pendingCapabilitySlots, MemberSlotID(slot))
		}
	}
	accepted, err := p.loadAcceptedEvidence(ctx, querier, scope)
	if err != nil {
		return nil, false, err
	}
	record.accepted = accepted
	return record, true, nil
}

func (p *PostgresAuthority) loadAcceptedEvidence(ctx context.Context, querier sqlQuerier, scope operationScope) ([]evidenceAcceptedRecord, error) {
	rows, err := querier.QueryContext(ctx, `SELECT kind, evidence_id, digest
		FROM `+p.q("publication_evidence_accepted")+`
		WHERE policy_domain_id = $1 AND task_id = $2 AND operation_id = $3
		ORDER BY kind, evidence_id`, string(scope.policyDomainID), string(scope.taskID), string(scope.operationID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var accepted []evidenceAcceptedRecord
	for rows.Next() {
		var record evidenceAcceptedRecord
		var kind, evidenceID, digest string
		if err := rows.Scan(&kind, &evidenceID, &digest); err != nil {
			return nil, err
		}
		record = evidenceAcceptedRecord{kind: kind, evidenceID: EvidenceID(evidenceID), digest: Digest(digest)}
		accepted = append(accepted, record)
	}
	return accepted, rows.Err()
}

func (p *PostgresAuthority) loadResidue(ctx context.Context, querier sqlQuerier, scope operationScope) (*residueRecord, bool, error) {
	var owner, releaseIntent string
	var generation, fence, occurredAt uint64
	var expiry, attemptCount, consecutiveFailures, nextRetryAt uint64
	var claimGeneration, claimFence uint64
	var lastErrorCategory string
	var disposition string
	var requiresReconciliation bool
	var receiptJSON []byte
	var assemblyRef, assemblyDigest, debtID string
	var assemblyGeneration, assemblyFence uint64
	err := querier.QueryRowContext(ctx, `SELECT owner, generation, fence, release_intent, occurred_at,
		expiry, disposition, requires_reconciliation, attempt_count, consecutive_failures, next_retry_at,
		claim_generation, claim_fence, last_error_category, release_receipt,
		assembly_ref, assembly_digest, assembly_generation, assembly_fence, debt_id
		FROM `+p.q("publication_residue")+`
		WHERE policy_domain_id = $1 AND task_id = $2 AND operation_id = $3`,
		string(scope.policyDomainID), string(scope.taskID), string(scope.operationID)).
		Scan(&owner, &generation, &fence, &releaseIntent, &occurredAt,
			&expiry, &disposition, &requiresReconciliation, &attemptCount, &consecutiveFailures, &nextRetryAt,
			&claimGeneration, &claimFence, &lastErrorCategory, &receiptJSON,
			&assemblyRef, &assemblyDigest, &assemblyGeneration, &assemblyFence, &debtID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, err
	}
	record := &residueRecord{
		operationID: scope.operationID, policyDomainID: scope.policyDomainID, taskID: scope.taskID,
		owner: EvidenceAuthorityKind(owner), generation: Generation(generation), fence: Fence(fence),
		releaseIntent: releaseIntent, occurredAt: Instant(occurredAt),
		expiry: Instant(expiry), disposition: ResidueDisposition(disposition),
		requiresReconciliation: requiresReconciliation,
		attemptCount:           attemptCount, consecutiveFailures: consecutiveFailures,
		nextRetryAt: Instant(nextRetryAt), claimGeneration: Generation(claimGeneration),
		claimFence: Fence(claimFence), lastErrorCategory: ResidueErrorCategory(lastErrorCategory),
		debtID: CleanupDebtID(debtID),
	}
	if len(receiptJSON) > 0 && string(receiptJSON) != "null" {
		var receipt ReleaseReceipt
		if err := json.Unmarshal(receiptJSON, &receipt); err != nil {
			return nil, false, err
		}
		record.releaseReceipt = &receipt
	}
	if assemblyRef != "" {
		record.assembly = &assemblyResource{
			Reference: assemblyRef, IdentityDigest: Digest(assemblyDigest),
			Generation: assemblyGeneration, Fence: assemblyFence,
		}
	}
	rows, err := querier.QueryContext(ctx, `SELECT slot, content_id, content_digest, size, purpose, physical_generation, adapter_id
		FROM `+p.q("publication_residue_staging")+`
		WHERE policy_domain_id = $1 AND task_id = $2 AND operation_id = $3
		ORDER BY slot`, string(scope.policyDomainID), string(scope.taskID), string(scope.operationID))
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var staging stagingRecord
		var slot, contentID, contentDigest, purpose, adapterID string
		var size, physicalGeneration uint64
		if err := rows.Scan(&slot, &contentID, &contentDigest, &size, &purpose, &physicalGeneration, &adapterID); err != nil {
			return nil, false, err
		}
		staging = stagingRecord{
			slot: MemberSlotID(slot), contentID: ContentID(contentID), contentDigest: Digest(contentDigest),
			size: size, purpose: ContentPurpose(purpose), physicalGeneration: physicalGeneration, adapterID: AdapterID(adapterID),
		}
		record.stagingRefs = append(record.stagingRefs, staging)
	}
	return record, true, rows.Err()
}

// loadDebt loads one C05-owned Cleanup Debt for an operation, or (nil,
// false) when none exists.
func (p *PostgresAuthority) loadDebt(ctx context.Context, querier sqlQuerier, scope operationScope) (*cleanupDebtRecord, bool, error) {
	var debtID, owner, resourceRef, resourceDigest, status string
	var revision, resourceGeneration, resourceFence uint64
	var createdAt, eligibleAt, firstAttemptAt, lastAttemptAt, nextRetryAt uint64
	var attemptCount, consecutiveFailures uint64
	var claimGeneration, claimFence uint64
	var retryDisposition, lastErrorCategory string
	var blockerClasses uint64
	var resolvedAt, resolutionExpiresAt uint64
	var resolutionClass, resolutionReason, auditFactID, approvalRef string
	var evidenceJSON []byte
	err := querier.QueryRowContext(ctx, `SELECT debt_id, revision, owner, resource_ref, resource_digest,
		resource_generation, resource_fence, status, created_at, eligible_at,
		first_attempt_at, last_attempt_at, next_retry_at, attempt_count, consecutive_failures,
		claim_generation, claim_fence, retry_disposition, last_error_category, blocker_classes,
		resolved_at, resolution_class, resolution_reason, resolution_evidence,
		resolution_audit_fact_id, resolution_approval_ref, resolution_expires_at
		FROM `+p.q("publication_cleanup_debt")+`
		WHERE policy_domain_id = $1 AND task_id = $2 AND operation_id = $3`,
		string(scope.policyDomainID), string(scope.taskID), string(scope.operationID)).
		Scan(&debtID, &revision, &owner, &resourceRef, &resourceDigest,
			&resourceGeneration, &resourceFence, &status, &createdAt, &eligibleAt,
			&firstAttemptAt, &lastAttemptAt, &nextRetryAt, &attemptCount, &consecutiveFailures,
			&claimGeneration, &claimFence, &retryDisposition, &lastErrorCategory, &blockerClasses,
			&resolvedAt, &resolutionClass, &resolutionReason, &evidenceJSON,
			&auditFactID, &approvalRef, &resolutionExpiresAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, err
	}
	record := &cleanupDebtRecord{
		debtID: CleanupDebtID(debtID), revision: revision,
		policyDomainID: scope.policyDomainID, taskID: scope.taskID, operationID: scope.operationID,
		owner: EvidenceAuthorityKind(owner), resourceRef: resourceRef, resourceDigest: Digest(resourceDigest),
		resourceGeneration: resourceGeneration, resourceFence: resourceFence,
		status: CleanupDebtStatus(status), createdAt: Instant(createdAt), eligibleAt: Instant(eligibleAt),
		firstAttemptAt: Instant(firstAttemptAt), lastAttemptAt: Instant(lastAttemptAt),
		nextRetryAt: Instant(nextRetryAt), attemptCount: attemptCount, consecutiveFailures: consecutiveFailures,
		claimGeneration: Generation(claimGeneration), claimFence: Fence(claimFence),
		retryDisposition:  CleanupRetryDisposition(retryDisposition),
		lastErrorCategory: ResidueErrorCategory(lastErrorCategory), blockers: CleanupBlockerClass(blockerClasses),
		resolvedAt: Instant(resolvedAt), resolutionClass: CleanupDebtResolutionClass(resolutionClass),
		resolutionReason:      CleanupResolutionReason(resolutionReason),
		resolutionAuditFactID: auditFactID, resolutionApprovalRef: approvalRef,
		resolutionExpiresAt: Instant(resolutionExpiresAt),
	}
	if len(evidenceJSON) > 0 && string(evidenceJSON) != "null" {
		var evidence CleanupResolutionEvidence
		if err := json.Unmarshal(evidenceJSON, &evidence); err != nil {
			return nil, false, err
		}
		record.resolutionEvidence = &evidence
	}
	return record, true, nil
}

// loadDebtByID loads one C05-owned Cleanup Debt by its opaque identity and
// verifies it belongs to the given policy domain/Task (cross-workspace
// identity is never disclosed).
func (p *PostgresAuthority) loadDebtByID(ctx context.Context, querier sqlQuerier, debtID CleanupDebtID, policyDomainID PolicyDomainID, taskID TaskID) (*cleanupDebtRecord, bool, error) {
	var operationID, owner, resourceRef, resourceDigest, status string
	var revision, resourceGeneration, resourceFence uint64
	var createdAt, eligibleAt, firstAttemptAt, lastAttemptAt, nextRetryAt uint64
	var attemptCount, consecutiveFailures uint64
	var claimGeneration, claimFence uint64
	var retryDisposition, lastErrorCategory string
	var blockerClasses uint64
	var resolvedAt, resolutionExpiresAt uint64
	var resolutionClass, resolutionReason, auditFactID, approvalRef string
	var evidenceJSON []byte
	err := querier.QueryRowContext(ctx, `SELECT operation_id, revision, owner, resource_ref, resource_digest,
		resource_generation, resource_fence, status, created_at, eligible_at,
		first_attempt_at, last_attempt_at, next_retry_at, attempt_count, consecutive_failures,
		claim_generation, claim_fence, retry_disposition, last_error_category, blocker_classes,
		resolved_at, resolution_class, resolution_reason, resolution_evidence,
		resolution_audit_fact_id, resolution_approval_ref, resolution_expires_at
		FROM `+p.q("publication_cleanup_debt")+`
		WHERE debt_id = $1 AND policy_domain_id = $2 AND task_id = $3`,
		string(debtID), string(policyDomainID), string(taskID)).
		Scan(&operationID, &revision, &owner, &resourceRef, &resourceDigest,
			&resourceGeneration, &resourceFence, &status, &createdAt, &eligibleAt,
			&firstAttemptAt, &lastAttemptAt, &nextRetryAt, &attemptCount, &consecutiveFailures,
			&claimGeneration, &claimFence, &retryDisposition, &lastErrorCategory, &blockerClasses,
			&resolvedAt, &resolutionClass, &resolutionReason, &evidenceJSON,
			&auditFactID, &approvalRef, &resolutionExpiresAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, err
	}
	record := &cleanupDebtRecord{
		debtID: debtID, revision: revision,
		policyDomainID: policyDomainID, taskID: taskID, operationID: PublicationOperationID(operationID),
		owner: EvidenceAuthorityKind(owner), resourceRef: resourceRef, resourceDigest: Digest(resourceDigest),
		resourceGeneration: resourceGeneration, resourceFence: resourceFence,
		status: CleanupDebtStatus(status), createdAt: Instant(createdAt), eligibleAt: Instant(eligibleAt),
		firstAttemptAt: Instant(firstAttemptAt), lastAttemptAt: Instant(lastAttemptAt),
		nextRetryAt: Instant(nextRetryAt), attemptCount: attemptCount, consecutiveFailures: consecutiveFailures,
		claimGeneration: Generation(claimGeneration), claimFence: Fence(claimFence),
		retryDisposition:  CleanupRetryDisposition(retryDisposition),
		lastErrorCategory: ResidueErrorCategory(lastErrorCategory), blockers: CleanupBlockerClass(blockerClasses),
		resolvedAt: Instant(resolvedAt), resolutionClass: CleanupDebtResolutionClass(resolutionClass),
		resolutionReason:      CleanupResolutionReason(resolutionReason),
		resolutionAuditFactID: auditFactID, resolutionApprovalRef: approvalRef,
		resolutionExpiresAt: Instant(resolutionExpiresAt),
	}
	if len(evidenceJSON) > 0 && string(evidenceJSON) != "null" {
		var evidence CleanupResolutionEvidence
		if err := json.Unmarshal(evidenceJSON, &evidence); err != nil {
			return nil, false, err
		}
		record.resolutionEvidence = &evidence
	}
	return record, true, nil
}

// saveDebt upserts one C05-owned Cleanup Debt in the current transaction.
func (p *PostgresAuthority) saveDebt(ctx context.Context, tx *sql.Tx, scope operationScope, debt *cleanupDebtRecord) error {
	evidenceJSON := []byte(nil)
	if debt.resolutionEvidence != nil {
		var err error
		evidenceJSON, err = marshalJSONSafe(debt.resolutionEvidence)
		if err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO `+p.q("publication_cleanup_debt")+`
		(debt_id, revision, policy_domain_id, task_id, operation_id, owner, resource_ref, resource_digest,
		 resource_generation, resource_fence, status, created_at, eligible_at,
		 first_attempt_at, last_attempt_at, next_retry_at, attempt_count, consecutive_failures,
		 claim_generation, claim_fence, retry_disposition, last_error_category, blocker_classes,
		 resolved_at, resolution_class, resolution_reason, resolution_evidence,
		 resolution_audit_fact_id, resolution_approval_ref, resolution_expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18,
		        $19, $20, $21, $22, $23, $24, $25, $26, $27, $28, $29, $30)
		ON CONFLICT (debt_id) DO UPDATE
			SET revision = EXCLUDED.revision, status = EXCLUDED.status,
			    first_attempt_at = EXCLUDED.first_attempt_at, last_attempt_at = EXCLUDED.last_attempt_at,
			    next_retry_at = EXCLUDED.next_retry_at, attempt_count = EXCLUDED.attempt_count,
			    consecutive_failures = EXCLUDED.consecutive_failures,
			    claim_generation = EXCLUDED.claim_generation, claim_fence = EXCLUDED.claim_fence,
			    retry_disposition = EXCLUDED.retry_disposition,
			    last_error_category = EXCLUDED.last_error_category,
			    blocker_classes = EXCLUDED.blocker_classes,
			    resolved_at = EXCLUDED.resolved_at, resolution_class = EXCLUDED.resolution_class,
			    resolution_reason = EXCLUDED.resolution_reason,
			    resolution_evidence = EXCLUDED.resolution_evidence,
			    resolution_audit_fact_id = EXCLUDED.resolution_audit_fact_id,
			    resolution_approval_ref = EXCLUDED.resolution_approval_ref,
			    resolution_expires_at = EXCLUDED.resolution_expires_at`,
		string(debt.debtID), debt.revision, string(scope.policyDomainID), string(scope.taskID), string(scope.operationID),
		string(debt.owner), debt.resourceRef, string(debt.resourceDigest),
		debt.resourceGeneration, debt.resourceFence, string(debt.status),
		int64(debt.createdAt), int64(debt.eligibleAt),
		int64(debt.firstAttemptAt), int64(debt.lastAttemptAt), int64(debt.nextRetryAt),
		debt.attemptCount, debt.consecutiveFailures,
		uint64(debt.claimGeneration), uint64(debt.claimFence), string(debt.retryDisposition),
		string(debt.lastErrorCategory), uint64(debt.blockers),
		int64(debt.resolvedAt), string(debt.resolutionClass), string(debt.resolutionReason),
		evidenceJSON, debt.resolutionAuditFactID, debt.resolutionApprovalRef,
		int64(debt.resolutionExpiresAt))
	return err
}

// loadActivated loads one committed Artifact Version with its immutable
// members and activation evidence.
func (p *PostgresAuthority) loadActivated(ctx context.Context, querier sqlQuerier, versionID ArtifactVersionID) (*activatedRecord, bool, error) {
	var policyDomainID, taskID, kind, parent, contractID, phaseRunID, operationID string
	var schemaVersion uint64
	var streamRevision, occurredAt uint64
	var manifestDigest, lineageDigest string
	var evidenceJSON []byte
	err := querier.QueryRowContext(ctx, `SELECT policy_domain_id, task_id, schema_version, kind, parent, contract_id,
		phase_run_id, operation_id, stream_revision, manifest_digest, lineage_digest, occurred_at, evidence
		FROM `+p.q("publication_activated")+` WHERE version_id = $1`, string(versionID)).
		Scan(&policyDomainID, &taskID, &schemaVersion, &kind, &parent, &contractID,
			&phaseRunID, &operationID, &streamRevision, &manifestDigest, &lineageDigest, &occurredAt, &evidenceJSON)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, err
	}
	record := &activatedRecord{
		versionID: versionID, schemaVersion: SchemaVersion(schemaVersion),
		policyDomainID: PolicyDomainID(policyDomainID), taskID: TaskID(taskID),
		kind: PublicationKind(kind), parent: ArtifactVersionID(parent),
		contractID: PublicationContractID(contractID), phaseRunID: PhaseRunID(phaseRunID),
		operationID: PublicationOperationID(operationID), streamRevision: StreamRevision(streamRevision),
		manifestDigest: Digest(manifestDigest), lineageDigest: Digest(lineageDigest),
		occurredAt: Instant(occurredAt),
	}
	if len(evidenceJSON) > 0 && string(evidenceJSON) != "null" {
		var evidence PublicationEvidence
		if err := json.Unmarshal(evidenceJSON, &evidence); err != nil {
			return nil, false, err
		}
		record.evidence = &evidence
	}
	rows, err := querier.QueryContext(ctx, `SELECT slot, artifact_id, kind, logical_name, media_type, size, content_digest
		FROM `+p.q("publication_activated_member")+` WHERE version_id = $1 ORDER BY slot`, string(versionID))
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var member memberRecord
		var slot, artifactID, memberKind, logicalName, mediaType, contentDigest string
		var size uint64
		if err := rows.Scan(&slot, &artifactID, &memberKind, &logicalName, &mediaType, &size, &contentDigest); err != nil {
			return nil, false, err
		}
		member = memberRecord{
			slot: MemberSlotID(slot), artifactID: ArtifactID(artifactID), kind: ArtifactKind(memberKind),
			logicalName: logicalName, mediaType: MediaType(mediaType), size: size, contentDigest: Digest(contentDigest),
		}
		record.members = append(record.members, member)
	}
	return record, true, rows.Err()
}

// loadVersionByArtifact resolves the Artifact Version whose candidate
// carries the given opaque ArtifactID (candidate lookup). The version fact
// tables prove identity non-reuse.
func (p *PostgresAuthority) loadOperationByVersion(ctx context.Context, querier sqlQuerier, versionID ArtifactVersionID) (operationScope, bool, error) {
	var policyDomainID, taskID, operationID string
	err := querier.QueryRowContext(ctx, `SELECT policy_domain_id, task_id, operation_id
		FROM `+p.q("publication_candidate")+` WHERE version_id = $1`, string(versionID)).
		Scan(&policyDomainID, &taskID, &operationID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return operationScope{}, false, nil
		}
		return operationScope{}, false, err
	}
	return operationScope{
		policyDomainID: PolicyDomainID(policyDomainID), taskID: TaskID(taskID), operationID: PublicationOperationID(operationID),
	}, true, nil
}

// loadActivatedVersionsForTask loads the committed versions of one Task
// ordered exclusively by the explicit stream revision.
func (p *PostgresAuthority) loadActivatedVersionsForTask(ctx context.Context, querier sqlQuerier, policyDomainID PolicyDomainID, taskID TaskID) ([]*activatedRecord, error) {
	rows, err := querier.QueryContext(ctx, `SELECT version_id FROM `+p.q("publication_activated")+`
		WHERE policy_domain_id = $1 AND task_id = $2 ORDER BY stream_revision`,
		string(policyDomainID), string(taskID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var versions []*activatedRecord
	for rows.Next() {
		var versionID string
		if err := rows.Scan(&versionID); err != nil {
			return nil, err
		}
		activated, found, err := p.loadActivated(ctx, querier, ArtifactVersionID(versionID))
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, errStateCorrupt()
		}
		versions = append(versions, activated)
	}
	return versions, rows.Err()
}

func errStateCorrupt() error {
	return &Error{Code: ErrorIntegrityFailure}
}

// marshalJSONSafe deterministically encodes value for durable storage and
// returns a persistence-safe error instead of panicking.
func marshalJSONSafe(value any) ([]byte, error) {
	encoded, err := json.Marshal(normalizeCanonical(value))
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

// recordDecisionJSON encodes a PublicationDecision for durable storage. The
// decision is content-free by construction: opaque identities, digests,
// closed states, accepted evidence references, and safe reasons.
func recordDecisionJSON(decision PublicationDecision) ([]byte, error) {
	return marshalJSONSafe(decision)
}

// encodeJSONStringList encodes a closed enum list as a JSON array.
func encodeJSONStringList(values []string) ([]byte, error) {
	return marshalJSONSafe(values)
}
