package artifactpublication

// This file implements the real PostgreSQL owned persistence Mutate seam
// (child SPEC #107). Every mutation enters the same invariant engine as the
// in-memory authority: validation, canonical digests, the shared pure
// builders (decisionForRecord, buildActivationEvidence, evaluateEvidence)
// and the closed state machine. Persistence is owned by this adapter; no
// SQL, repository, or transaction handle is ever exposed. Each flow runs in
// one real PostgreSQL transaction; atomic activation is the single business
// linearization point that row-locks the stream and the original operation,
// revalidates the exact facts, attaches typed Durable Object references
// through the restricted participant, and commits the immutable version,
// members, lineage, head, terminal operation, mandatory audit, activation
// evidence and outbox all-or-none.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
)

// Mutate is the single mutation seam of the real PostgreSQL authority.
// Every intent enters this invariant engine; there is no second mutation
// surface.
func (p *PostgresAuthority) Mutate(ctx context.Context, intent PublicationIntent) (PublicationDecision, error) {
	if intent == nil {
		return PublicationDecision{}, &Error{Code: ErrorInvalidIntent}
	}
	kind := intent.kind()
	if !validIntentKind(kind) {
		return PublicationDecision{}, &Error{Code: ErrorInvalidIntent}
	}
	header := intent.header()
	if header.SchemaVersion.Major() != SchemaV1.Major() {
		return PublicationDecision{}, &Error{Code: ErrorUnsupportedSchema}
	}
	if !intent.valid() {
		return PublicationDecision{}, &Error{Code: ErrorInvalidIntent}
	}
	digest := CanonicalRequestDigest(intent)
	if digest == "" || header.Operation.RequestDigest != digest {
		return PublicationDecision{}, &Error{Code: ErrorInvalidIntent}
	}

	scope := operationScope{policyDomainID: header.PolicyDomainID, taskID: header.TaskID, operationID: header.Operation.ID}
	switch kind {
	case IntentPreparePublication:
		return p.prepareFlow(ctx, intent.(PreparePublication), header, digest, scope)
	case IntentVerifyPublication:
		return p.verifyFlow(ctx, intent.(VerifyPublication), header, digest, scope)
	case IntentActivatePublication:
		return p.activateFlow(ctx, intent.(ActivatePublication), header, digest, scope)
	case IntentRejectPublication:
		return p.rejectFlow(ctx, intent.(RejectPublication), header, digest, scope)
	case IntentCancelPublication:
		return p.cancelFlow(ctx, intent.(CancelPublication), header, digest, scope)
	case IntentReconcilePublication:
		return p.reconcileFlow(ctx, intent.(ReconcilePublication), header, digest, scope)
	case IntentRecordResidueAssembly:
		return p.recordResidueAssemblyFlow(ctx, intent.(RecordResidueAssembly), header, digest, scope)
	case IntentReleaseResidue:
		return p.releaseResidueFlow(ctx, intent.(ReleaseResidue), header, digest, scope)
	case IntentResolveCleanupDebt:
		return p.resolveCleanupDebtFlow(ctx, intent.(ResolveCleanupDebt), header, digest, scope)
	default:
		return PublicationDecision{}, &Error{Code: ErrorInvalidIntent}
	}
}

// lockOperationRow locks one operation row for update inside the given
// transaction and loads its full durable record. Locking serializes all
// mutations of one operation so replay/conflict detection and state
// transitions are deterministic, matching the single-lock semantics of the
// in-memory authority.
func (p *PostgresAuthority) lockOperationRow(ctx context.Context, tx *sql.Tx, scope operationScope) (*operationRecord, bool, error) {
	var operationID string
	err := tx.QueryRowContext(ctx, `SELECT operation_id FROM `+p.q("publication_operation")+`
		WHERE policy_domain_id = $1 AND task_id = $2 AND operation_id = $3 FOR UPDATE`,
		string(scope.policyDomainID), string(scope.taskID), string(scope.operationID)).
		Scan(&operationID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return p.loadOperation(ctx, tx, scope)
}

// checkOutcomeReplay returns the outcome to replay, or an error. When an
// outcome for this intent kind already exists, an identical digest is an
// exact replay and a different digest is a durable integrity conflict. This
// mirrors the in-memory outcomes map semantics and is evaluated inside the
// locked transaction.
func (p *PostgresAuthority) checkOutcomeReplay(ctx context.Context, tx *sql.Tx, scope operationScope, kind PublicationIntentKind, digest Digest) (*intentOutcome, error) {
	outcome, found, err := p.loadOutcome(ctx, tx, scope, kind)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	if outcome.digest == digest {
		return outcome, nil
	}
	if err := p.recordIncident(ctx, tx, scope, "same_key_different_payload", string(digest)); err != nil {
		return nil, err
	}
	// Commit the durable incident before reporting the conflict; the
	// caller's deferred rollback becomes a no-op after commit.
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return nil, &Error{Code: ErrorIntegrityConflict}
}

// recordIncident persists a content-free integrity incident inside the flow
// transaction. The caller commits the transaction immediately afterwards, so
// the incident is durable even though the conflict aborts the mutation — the
// same-connection write avoids a second pool connection while the operation
// row lock is held (which could deadlock under pool exhaustion). This
// matches the in-memory authority, where the conflict survives on the
// durable operation record.
func (p *PostgresAuthority) recordIncident(ctx context.Context, tx *sql.Tx, scope operationScope, kind, subjectID string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO `+p.q("publication_integrity_incident")+`
		(occurred_at, policy_domain_id, task_id, operation_id, kind, subject_id)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		int64(p.nowValue()), string(scope.policyDomainID), string(scope.taskID),
		string(scope.operationID), kind, subjectID)
	return err
}

// writeAudit persists one mandatory audit fact in the current transaction.
// The audit row is content-free: opaque identities, closed enum facts, and
// digests only.
func (p *PostgresAuthority) writeAudit(ctx context.Context, tx *sql.Tx, header PublicationIntentHeader, action string, state PublicationOperationState, versionID ArtifactVersionID, manifestDigest Digest, streamRevision StreamRevision) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO `+p.q("publication_audit")+`
		(occurred_at, policy_domain_id, task_id, operation_id, intent_kind, action,
		 actor_kind, actor_id, actor_generation, state, version_id, manifest_digest, stream_revision)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		int64(p.nowValue()), string(header.PolicyDomainID), string(header.TaskID),
		string(header.Operation.ID), string(action), action,
		string(header.Authority.Kind), string(header.Authority.ID), uint64(header.Authority.Generation),
		string(state), string(versionID), string(manifestDigest), uint64(streamRevision))
	return err
}

// writeAuditReturningID persists one mandatory audit fact and returns its
// opaque audit identity. It is used by resolutions whose audited audit-fact
// id must be recorded on the resolved debt.
func (p *PostgresAuthority) writeAuditReturningID(ctx context.Context, tx *sql.Tx, header PublicationIntentHeader, action string, state PublicationOperationState, versionID ArtifactVersionID, manifestDigest Digest, streamRevision StreamRevision) (string, error) {
	var auditID int64
	err := tx.QueryRowContext(ctx, `INSERT INTO `+p.q("publication_audit")+`
		(occurred_at, policy_domain_id, task_id, operation_id, intent_kind, action,
		 actor_kind, actor_id, actor_generation, state, version_id, manifest_digest, stream_revision)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING audit_id`,
		int64(p.nowValue()), string(header.PolicyDomainID), string(header.TaskID),
		string(header.Operation.ID), string(action), action,
		string(header.Authority.Kind), string(header.Authority.ID), uint64(header.Authority.Generation),
		string(state), string(versionID), string(manifestDigest), uint64(streamRevision)).
		Scan(&auditID)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("audit-%d", auditID), nil
}

// writeOutbox persists one committed Task Orchestration outbox envelope in
// the current transaction. It is written only for terminal dispositions
// (activation evidence, rejection, cancellation); the envelope is
// content-free and the outbox is owned by this adapter.
func (p *PostgresAuthority) writeOutbox(ctx context.Context, tx *sql.Tx, header PublicationIntentHeader, kind string, envelope any) error {
	encoded, err := marshalJSONSafe(envelope)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO `+p.q("publication_outbox")+`
		(policy_domain_id, task_id, operation_id, request_digest, kind, envelope, state, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		string(header.PolicyDomainID), string(header.TaskID), string(header.Operation.ID),
		string(header.Operation.RequestDigest), kind, encoded, "ready", int64(p.nowValue()))
	return err
}

// prepareFlow persists the stable operation identity, request digest,
// expected head/revision, candidate identity, immutable candidate manifest/
// lineage, members, staging references and pinned evidence references
// before any external physical action, then creates the Task's stream row.
// A second prepare under the same operation identity is an exact replay or
// a durable integrity conflict, never a second operation.
func (p *PostgresAuthority) prepareFlow(ctx context.Context, intent PreparePublication, header PublicationIntentHeader, digest Digest, scope operationScope) (PublicationDecision, error) {
	operationID := header.Operation.ID
	if err := p.injectFault(PostgresFaultBeforeRequestJournal, operationID, IntentPreparePublication, ""); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := p.ensureAuthority(header.Authority, IntentPreparePublication); err != nil {
		return PublicationDecision{}, err
	}

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	defer func() { _ = tx.Rollback() }()

	// The operation row may already exist (concurrent identical prepare or
	// replay): a plain INSERT blocks on the unique key until the other
	// transaction commits, then fails; we re-read and replay or conflict.
	record, err := p.insertOperationJournal(ctx, tx, scope, header, digest)
	if err != nil {
		return PublicationDecision{}, err
	}
	if record != nil {
		// Another transaction already created this operation: exact replay
		// takes priority, a different prepare payload is a conflict.
		return p.handleExistingOperation(ctx, tx, scope, IntentPreparePublication, digest, header)
	}

	taskKey := taskScope{policyDomainID: header.PolicyDomainID, taskID: header.TaskID}
	stream, err := p.lockStreamForUpdate(ctx, tx, taskKey)
	if err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if header.ExpectedStreamRevision != stream.revision || header.ExpectedHead != stream.currentHead {
		return PublicationDecision{}, &Error{Code: ErrorStaleAuthority}
	}
	if intent.Kind == PublicationKindManualEdit {
		if intent.Parent != stream.currentHead {
			return PublicationDecision{}, &Error{Code: ErrorStaleAuthority}
		}
		activated, found, err := p.loadActivated(ctx, tx, intent.Parent)
		if err != nil {
			return PublicationDecision{}, normalizePersistenceError(err)
		}
		if !found || activated == nil {
			return PublicationDecision{}, &Error{Code: ErrorIntegrityFailure}
		}
	}
	if !validPrepareBindings(intent) {
		return PublicationDecision{}, &Error{Code: ErrorInvalidIntent}
	}

	if err := p.injectFault(PostgresFaultBeforeCandidatePersistence, operationID, IntentPreparePublication, ""); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}

	versionID, err := p.mintVersionID(ctx, tx)
	if err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	specs := append([]ArtifactMemberSpec(nil), intent.Members...)
	sort.Slice(specs, func(i, j int) bool { return specs[i].Slot < specs[j].Slot })
	members := make([]memberRecord, 0, len(specs))
	for _, spec := range specs {
		name, ok := normalizedLogicalName(spec.LogicalName)
		if !ok {
			return PublicationDecision{}, &Error{Code: ErrorInvalidIntent}
		}
		artifactID, err := p.mintArtifactID(ctx, tx)
		if err != nil {
			return PublicationDecision{}, normalizePersistenceError(err)
		}
		members = append(members, memberRecord{
			slot: spec.Slot, artifactID: artifactID,
			kind: spec.Kind, logicalName: name, mediaType: spec.MediaType,
			size: spec.Size, contentDigest: spec.ContentDigest,
		})
	}
	sort.Slice(members, func(i, j int) bool {
		if members[i].slot == members[j].slot {
			return members[i].artifactID < members[j].artifactID
		}
		return members[i].slot < members[j].slot
	})
	staging := make([]stagingRecord, 0, len(intent.Staging))
	for _, ref := range intent.Staging {
		staging = append(staging, stagingRecord{
			slot: ref.MemberSlot, contentID: ref.ContentID, contentDigest: ref.ContentDigest,
			size: ref.Size, purpose: ref.Purpose, physicalGeneration: ref.PhysicalGeneration,
			adapterID: ref.AdapterID,
		})
	}
	sort.Slice(staging, func(i, j int) bool { return staging[i].slot < staging[j].slot })

	manifestMembers := make([]ArtifactMember, 0, len(members))
	for _, member := range members {
		manifestMembers = append(manifestMembers, ArtifactMember{
			ArtifactID: member.artifactID, Kind: member.kind, LogicalName: member.logicalName,
			MediaType: member.mediaType, Size: member.size, ContentDigest: member.contentDigest,
		})
	}
	lineage := Lineage{
		SchemaVersion: header.SchemaVersion, VersionID: versionID, TaskID: header.TaskID,
		Kind: intent.Kind, Parent: intent.Parent, OperationID: operationID,
		PhaseRunID: intent.PhaseRunID, ContractID: intent.ContractID,
		RuntimeEvidenceRoot:   runtimeEvidenceRoot(intent.RuntimeRefs),
		ValidationEvidenceRef: intent.ValidationRef,
		C04CommitEvidenceRef:  intent.C04CommitRef,
		ContentCapabilityRoot: contentCapabilityRoot(intent.ContentCapabilityRefs),
		ActivityGeneration:    header.ActivityGeneration,
		Generation:            header.Generation,
		Fence:                 header.Fence,
		SafetyEpoch:           header.SafetyEpoch,
	}
	lineageDigest := lineage.CanonicalDigest()
	manifest := ArtifactManifest{
		SchemaVersion: header.SchemaVersion, VersionID: versionID, TaskID: header.TaskID,
		Kind: intent.Kind, Parent: intent.Parent, LineageDigest: lineageDigest,
		Members: manifestMembers,
	}
	manifestDigest := manifest.CanonicalDigest()

	candidate := &candidateRecord{
		versionID: versionID, schemaVersion: header.SchemaVersion, kind: intent.Kind,
		parent: intent.Parent, contractID: intent.ContractID, phaseRunID: intent.PhaseRunID,
		members: members, staging: staging,
		runtimeRefs: cloneRuntimeRefs(intent.RuntimeRefs), validationRef: intent.ValidationRef,
		c04Ref: intent.C04CommitRef, capabilityRefs: cloneCapabilityRefs(intent.ContentCapabilityRefs),
		requiredChannels: cloneChannels(intent.RequiredChannels),
		manifestDigest:   manifestDigest, lineageDigest: lineageDigest,
	}
	if err := p.persistCandidate(ctx, tx, scope, header, stream.revision, candidate); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}

	record = &operationRecord{
		scope: scope, operationID: operationID, requestDigest: digest,
		state: OperationPrepared, generation: header.Generation, fence: header.Fence,
		safetyEpoch: header.SafetyEpoch, activityGeneration: header.ActivityGeneration,
		streamRevision: stream.revision, occurredAt: p.nowValue(),
		candidate: candidate,
	}
	decision := decisionForRecord(record, false, p.nowValue())
	outcome := intentOutcome{
		digest: digest, state: OperationPrepared, decision: decision, recordedAt: p.nowValue(),
	}
	if err := p.saveOutcome(ctx, tx, scope, IntentPreparePublication, outcome); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := p.injectFault(PostgresFaultBeforeMandatoryAudit, operationID, IntentPreparePublication, string(versionID)); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := p.writeAudit(ctx, tx, header, "prepare", OperationPrepared, versionID, manifestDigest, stream.revision); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := p.injectFault(PostgresFaultBeforeCommit, operationID, IntentPreparePublication, string(versionID)); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := tx.Commit(); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := p.injectFault(PostgresFaultAfterCommit, operationID, IntentPreparePublication, string(versionID)); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	return decision, nil
}

// handleExistingOperation resolves a prepare (or any intent) that found an
// already-created operation: exact replay wins, a different payload is a
// durable integrity conflict.
func (p *PostgresAuthority) handleExistingOperation(ctx context.Context, tx *sql.Tx, scope operationScope, kind PublicationIntentKind, digest Digest, header PublicationIntentHeader) (PublicationDecision, error) {
	outcome, err := p.checkOutcomeReplay(ctx, tx, scope, kind, digest)
	if err != nil {
		return PublicationDecision{}, err
	}
	if outcome != nil {
		decision, replayErr := replayOutcome(*outcome)
		return decision, replayErr
	}
	// An operation identity is minted once at prepare; a second prepare
	// under the same identity is always a conflict. The incident is durable.
	if err := p.recordIncident(ctx, tx, scope, "operation_identity_reuse", string(header.Operation.ID)); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := tx.Commit(); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	return PublicationDecision{}, &Error{Code: ErrorIntegrityConflict}
}

// insertOperationJournal inserts the operation journal row and returns
// (nil, nil) when the insert succeeded, or the already-existing operation
// record when a concurrent identical operation won the insert race. The
// ON CONFLICT DO NOTHING form never aborts the transaction (PostgreSQL
// aborts the whole transaction after a plain unique-violation INSERT), so
// the caller can safely re-read and replay or conflict.
func (p *PostgresAuthority) insertOperationJournal(ctx context.Context, tx *sql.Tx, scope operationScope, header PublicationIntentHeader, digest Digest) (*operationRecord, error) {
	const maxAttempts = 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		var insertedID string
		err := tx.QueryRowContext(ctx, `INSERT INTO `+p.q("publication_operation")+`
			(policy_domain_id, task_id, operation_id, request_digest, state, stream_revision,
			 generation, fence, safety_epoch, activity_generation, occurred_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			ON CONFLICT (policy_domain_id, task_id, operation_id) DO NOTHING
			RETURNING operation_id`,
			string(scope.policyDomainID), string(scope.taskID), string(scope.operationID),
			string(digest), string(OperationPrepared), 0,
			uint64(header.Generation), uint64(header.Fence), uint64(header.SafetyEpoch),
			uint64(header.ActivityGeneration), int64(p.nowValue())).Scan(&insertedID)
		if err == nil && insertedID != "" {
			return nil, nil
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		// Another transaction created (or is creating) this operation. The
		// ON CONFLICT wait finished; load the committed operation and replay
		// or conflict. If the concurrent writer aborted, retry the insert.
		existing, found, loadErr := p.lockOperationRow(ctx, tx, scope)
		if loadErr != nil {
			return nil, loadErr
		}
		if !found || existing == nil {
			continue
		}
		return existing, nil
	}
	return nil, &Error{Code: ErrorRetryableUnavailable}
}

// persistCandidate writes the candidate, members, staging references,
// pinned evidence references, and identity facts in one transaction.
func (p *PostgresAuthority) persistCandidate(ctx context.Context, tx *sql.Tx, scope operationScope, header PublicationIntentHeader, streamRevision StreamRevision, candidate *candidateRecord) error {
	channels := make([]string, 0, len(candidate.requiredChannels))
	for _, channel := range candidate.requiredChannels {
		channels = append(channels, string(channel))
	}
	channelsJSON, err := encodeJSONStringList(channels)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO `+p.q("publication_candidate")+`
		(policy_domain_id, task_id, operation_id, version_id, schema_version, kind, parent,
		 contract_id, phase_run_id, manifest_digest, lineage_digest, required_channels,
		 validation_ref_evidence_id, validation_ref_digest, c04_ref_evidence_id, c04_ref_digest)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
		string(scope.policyDomainID), string(scope.taskID), string(scope.operationID),
		string(candidate.versionID), uint64(candidate.schemaVersion), string(candidate.kind),
		string(candidate.parent), string(candidate.contractID), string(candidate.phaseRunID),
		string(candidate.manifestDigest), string(candidate.lineageDigest), channelsJSON,
		string(candidate.validationRef.EvidenceID), string(candidate.validationRef.Digest),
		string(candidate.c04Ref.EvidenceID), string(candidate.c04Ref.Digest)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO `+p.q("publication_version_fact")+`
		(version_id, policy_domain_id, task_id) VALUES ($1, $2, $3)`,
		string(candidate.versionID), string(scope.policyDomainID), string(scope.taskID)); err != nil {
		return err
	}
	for _, member := range candidate.members {
		if _, err := tx.ExecContext(ctx, `INSERT INTO `+p.q("publication_member")+`
			(policy_domain_id, task_id, operation_id, slot, artifact_id, kind, logical_name, media_type, size, content_digest)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			string(scope.policyDomainID), string(scope.taskID), string(scope.operationID),
			string(member.slot), string(member.artifactID), string(member.kind), member.logicalName,
			string(member.mediaType), member.size, string(member.contentDigest)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO `+p.q("publication_artifact_fact")+`
			(artifact_id, policy_domain_id, task_id) VALUES ($1, $2, $3)`,
			string(member.artifactID), string(scope.policyDomainID), string(scope.taskID)); err != nil {
			return err
		}
	}
	for _, staging := range candidate.staging {
		if _, err := tx.ExecContext(ctx, `INSERT INTO `+p.q("publication_staging")+`
			(policy_domain_id, task_id, operation_id, slot, content_id, content_digest, size, purpose, physical_generation, adapter_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			string(scope.policyDomainID), string(scope.taskID), string(scope.operationID),
			string(staging.slot), string(staging.contentID), string(staging.contentDigest),
			staging.size, string(staging.purpose), staging.physicalGeneration, string(staging.adapterID)); err != nil {
			return err
		}
	}
	for _, ref := range candidate.runtimeRefs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO `+p.q("publication_runtime_ref")+`
			(policy_domain_id, task_id, operation_id, channel, evidence_id, digest)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			string(scope.policyDomainID), string(scope.taskID), string(scope.operationID),
			string(ref.Channel), string(ref.EvidenceID), string(ref.Digest)); err != nil {
			return err
		}
	}
	for _, ref := range candidate.capabilityRefs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO `+p.q("publication_capability_ref")+`
			(policy_domain_id, task_id, operation_id, member_slot, capability_id, digest)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			string(scope.policyDomainID), string(scope.taskID), string(scope.operationID),
			string(ref.MemberSlot), string(ref.CapabilityID), string(ref.Digest)); err != nil {
			return err
		}
	}
	return nil
}

// mintVersionID allocates the next opaque, non-reused ArtifactVersionID
// from the owned sequence. The version fact table's primary key makes reuse
// structurally impossible.
func (p *PostgresAuthority) mintVersionID(ctx context.Context, tx *sql.Tx) (ArtifactVersionID, error) {
	var next int64
	if err := tx.QueryRowContext(ctx, "SELECT nextval('"+p.q("publication_identity_seq")+"'::regclass)").Scan(&next); err != nil {
		return "", err
	}
	return ArtifactVersionID(fmt.Sprintf("artifact-version-%016x", next)), nil
}

// mintArtifactID allocates the next opaque, non-reused ArtifactID.
func (p *PostgresAuthority) mintArtifactID(ctx context.Context, tx *sql.Tx) (ArtifactID, error) {
	var next int64
	if err := tx.QueryRowContext(ctx, "SELECT nextval('"+p.q("publication_identity_seq")+"'::regclass)").Scan(&next); err != nil {
		return "", err
	}
	return ArtifactID(fmt.Sprintf("artifact-%016x", next)), nil
}

// verifyFlow accepts the exact upstream evidence for the pinned candidate
// and records a replayable verification result through the shared evidence
// matrix. The candidate manifest is never patched.
func (p *PostgresAuthority) verifyFlow(ctx context.Context, intent VerifyPublication, header PublicationIntentHeader, digest Digest, scope operationScope) (PublicationDecision, error) {
	operationID := header.Operation.ID
	if err := p.injectFault(PostgresFaultBeforeEvidenceAcceptance, operationID, IntentVerifyPublication, ""); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := p.ensureAuthority(header.Authority, IntentVerifyPublication); err != nil {
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
	outcome, err := p.checkOutcomeReplay(ctx, tx, scope, IntentVerifyPublication, digest)
	if err != nil {
		return PublicationDecision{}, err
	}
	if outcome != nil {
		decision, replayErr := replayOutcome(*outcome)
		return decision, replayErr
	}
	if record.candidate == nil {
		return PublicationDecision{}, &Error{Code: ErrorInvalidIntent}
	}
	if header.Generation != record.generation || header.Fence != record.fence {
		return PublicationDecision{}, &Error{Code: ErrorStaleAuthority}
	}
	switch record.state {
	case OperationCancelled, OperationRejected, OperationActivated, OperationVerified:
		return PublicationDecision{}, &Error{Code: ErrorTerminalConflict}
	}

	resolver := func(id ContentCapabilityID) (ContentCapabilityEvidence, bool) {
		return p.currentContentCapabilityFrom(ctx, tx, id)
	}
	result, ambiguous, failure, evalErr := evaluateEvidence(evidenceAuthority{
		runtimeAuthorityID:       p.runtimeAuth,
		validationAuthorityID:    p.validationAuth,
		c04AuthorityID:           p.c04Auth,
		durableObjectAuthorityID: p.doAuth,
	}, resolver, record.candidate, header, intent)
	if evalErr != nil {
		return PublicationDecision{}, normalizePersistenceError(evalErr)
	}
	if err := p.injectFault(PostgresFaultBeforeVerificationResult, operationID, IntentVerifyPublication, ""); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}

	accepted := make([]evidenceAcceptedRecord, 0, len(result.accepted))
	accepted = append(accepted, result.accepted...)
	verification := &verificationRecord{}
	var outcomeErr *Error
	nextState := record.state
	switch {
	case ambiguous:
		verification = &verificationRecord{state: VerificationAmbiguous, accepted: accepted, pendingCapabilitySlots: result.pendingSlots}
		nextState = OperationReconciliationRequired
	case failure != nil:
		verification = &verificationRecord{state: VerificationFailed, failure: failure}
		nextState = OperationPrepared
		outcomeErr = &Error{Code: failureErrorCode(failure.Kind)}
	default:
		verification = &verificationRecord{state: VerificationVerified, accepted: accepted}
		nextState = OperationVerified
	}
	if err := p.persistVerification(ctx, tx, scope, record, verification, nextState); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}

	record.verification = verification
	record.state = nextState
	decision := decisionForRecord(record, false, p.nowValue())
	outcomeRecord := intentOutcome{
		digest: digest, state: nextState, decision: decision, err: outcomeErr, recordedAt: p.nowValue(),
	}
	if err := p.saveOutcome(ctx, tx, scope, IntentVerifyPublication, outcomeRecord); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := p.injectFault(PostgresFaultBeforeMandatoryAudit, operationID, IntentVerifyPublication, ""); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := p.writeAudit(ctx, tx, header, "verify", nextState, m0VersionID(record), m0ManifestDigest(record), record.streamRevision); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := p.injectFault(PostgresFaultBeforeCommit, operationID, IntentVerifyPublication, ""); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := tx.Commit(); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := p.injectFault(PostgresFaultAfterCommit, operationID, IntentVerifyPublication, ""); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if outcomeErr != nil {
		return decision, outcomeErr
	}
	return decision, nil
}

// persistVerification writes the verification result and advances the
// operation state inside the transaction.
func (p *PostgresAuthority) persistVerification(ctx context.Context, tx *sql.Tx, scope operationScope, record *operationRecord, verification *verificationRecord, state PublicationOperationState) error {
	var failureKind, failureEvidenceID, failureCapabilityID any
	if verification.failure != nil {
		failureKind = verification.failure.Kind
		failureEvidenceID = string(verification.failure.EvidenceID)
		failureCapabilityID = string(verification.failure.CapabilityID)
	}
	pendingSlots := make([]string, 0, len(verification.pendingCapabilitySlots))
	for _, slot := range verification.pendingCapabilitySlots {
		pendingSlots = append(pendingSlots, string(slot))
	}
	pendingJSON, err := encodeJSONStringList(pendingSlots)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO `+p.q("publication_verification")+`
		(policy_domain_id, task_id, operation_id, state, failure_kind, failure_evidence_id, failure_capability_id, pending_slots)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (policy_domain_id, task_id, operation_id) DO UPDATE
			SET state = EXCLUDED.state, failure_kind = EXCLUDED.failure_kind,
			    failure_evidence_id = EXCLUDED.failure_evidence_id,
			    failure_capability_id = EXCLUDED.failure_capability_id,
			    pending_slots = EXCLUDED.pending_slots`,
		string(scope.policyDomainID), string(scope.taskID), string(scope.operationID),
		string(verification.state), failureKind, failureEvidenceID, failureCapabilityID, pendingJSON); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM `+p.q("publication_evidence_accepted")+`
		WHERE policy_domain_id = $1 AND task_id = $2 AND operation_id = $3`,
		string(scope.policyDomainID), string(scope.taskID), string(scope.operationID)); err != nil {
		return err
	}
	for _, accepted := range verification.accepted {
		if _, err := tx.ExecContext(ctx, `INSERT INTO `+p.q("publication_evidence_accepted")+`
			(policy_domain_id, task_id, operation_id, kind, evidence_id, digest)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			string(scope.policyDomainID), string(scope.taskID), string(scope.operationID),
			accepted.kind, string(accepted.evidenceID), string(accepted.digest)); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE `+p.q("publication_operation")+`
		SET state = $4 WHERE policy_domain_id = $1 AND task_id = $2 AND operation_id = $3`,
		string(scope.policyDomainID), string(scope.taskID), string(scope.operationID), string(state)); err != nil {
		return err
	}
	return nil
}

// activateFlow is the single business linearization point (SPEC #105 over
// real PostgreSQL): it row-locks the stream and the original operation,
// revalidates the OperationID, request digest, expected stream
// revision/head, activity generation, publication fence and safety epoch,
// attaches the exact typed Durable Object references through the restricted
// participant, and commits the immutable Artifact Version, members, lineage,
// stream revision/current head, terminal operation, mandatory audit,
// activation evidence and outbox all-or-none. No remote I/O happens inside
// the transaction.
func (p *PostgresAuthority) activateFlow(ctx context.Context, intent ActivatePublication, header PublicationIntentHeader, digest Digest, scope operationScope) (PublicationDecision, error) {
	operationID := header.Operation.ID
	if err := p.ensureAuthority(header.Authority, IntentActivatePublication); err != nil {
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
	outcome, err := p.checkOutcomeReplay(ctx, tx, scope, IntentActivatePublication, digest)
	if err != nil {
		return PublicationDecision{}, err
	}
	if outcome != nil {
		decision, replayErr := replayOutcome(*outcome)
		return decision, replayErr
	}
	if record.candidate == nil {
		return PublicationDecision{}, &Error{Code: ErrorInvalidIntent}
	}
	// Row-lock/CAS revalidation of the original operation facts: the
	// activation intent must bind the exact operation identity, the stored
	// request digest (the canonical request that created the operation),
	// the activity generation, the publication fence, and the safety epoch.
	// The stored request digest must equal the prepare outcome digest,
	// proving the request journal is intact (SPEC #107 acceptance: request
	// digest row-lock/CAS revalidation).
	if record.requestDigest == "" || header.Generation != record.generation ||
		header.Fence != record.fence || header.SafetyEpoch != record.safetyEpoch ||
		header.ActivityGeneration != record.activityGeneration {
		return PublicationDecision{}, &Error{Code: ErrorStaleAuthority}
	}
	prepareOutcome, foundOutcome, outcomeErr := p.loadOutcome(ctx, tx, scope, IntentPreparePublication)
	if outcomeErr != nil {
		return PublicationDecision{}, normalizePersistenceError(outcomeErr)
	}
	if !foundOutcome || prepareOutcome == nil || prepareOutcome.digest != record.requestDigest {
		// The request journal is corrupt or missing: fail closed without
		// committing any version.
		return PublicationDecision{}, &Error{Code: ErrorIntegrityFailure}
	}
	switch record.state {
	case OperationCancelled, OperationRejected, OperationReconciliationRequired:
		return PublicationDecision{}, &Error{Code: ErrorTerminalConflict}
	case OperationActivated:
		return PublicationDecision{}, &Error{Code: ErrorTerminalConflict}
	case OperationPrepared:
		return PublicationDecision{}, &Error{Code: ErrorInvalidIntent}
	case OperationVerified:
		// fall through to the stream CAS.
	default:
		return PublicationDecision{}, &Error{Code: ErrorInvalidIntent}
	}

	taskKey := taskScope{policyDomainID: header.PolicyDomainID, taskID: header.TaskID}
	stream, err := p.lockStreamForUpdate(ctx, tx, taskKey)
	if err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if header.ExpectedStreamRevision != stream.revision || header.ExpectedHead != stream.currentHead {
		return PublicationDecision{}, &Error{Code: ErrorStaleAuthority}
	}
	candidate := record.candidate
	if candidate.kind == PublicationKindFirstGeneration {
		if candidate.parent != "" || header.ExpectedHead != "" {
			return PublicationDecision{}, &Error{Code: ErrorStaleAuthority}
		}
	}
	if candidate.kind == PublicationKindManualEdit {
		if candidate.parent == "" || candidate.parent != stream.currentHead {
			return PublicationDecision{}, &Error{Code: ErrorStaleAuthority}
		}
		activated, found, err := p.loadActivated(ctx, tx, candidate.parent)
		if err != nil {
			return PublicationDecision{}, normalizePersistenceError(err)
		}
		if !found || activated == nil {
			return PublicationDecision{}, &Error{Code: ErrorStaleAuthority}
		}
	}

	if err := p.injectFault(PostgresFaultBeforeActivationCommit, operationID, IntentActivatePublication, string(candidate.versionID)); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}

	nextRevision := stream.revision + 1
	evidence := buildActivationEvidence(record, header, candidate, nextRevision, p.nowValue())

	attachRefs := make([]DurableObjectAttachReference, 0, len(candidate.members))
	for _, member := range candidate.members {
		staged := candidate.stagingBySlot(member.slot)
		ref := candidate.capabilityBySlot(member.slot)
		if staged == nil {
			return PublicationDecision{}, &Error{Code: ErrorIntegrityFailure}
		}
		attachRefs = append(attachRefs, DurableObjectAttachReference{
			Slot: member.slot, ArtifactID: member.artifactID, CapabilityID: ref.CapabilityID,
			ContentID: staged.contentID, ContentDigest: staged.contentDigest, Size: staged.size,
			Purpose: staged.purpose, PhysicalGeneration: staged.physicalGeneration,
			VerificationMethod: VerificationMethodReceiptBound, AdapterID: staged.adapterID,
		})
	}

	if err := p.injectFault(PostgresFaultBeforeReferenceAttach, operationID, IntentActivatePublication, string(candidate.versionID)); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	// Restricted same-PostgreSQL Durable Object participant: attaches only
	// exact typed references inside this transaction. Its failure rolls the
	// whole activation back (no half active version, no orphan membership,
	// no readable unverified content).
	if err := p.attach.Attach(ctx, tx, header.PolicyDomainID, header.TaskID, candidate.versionID, header.SafetyEpoch, attachRefs); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}

	if err := p.injectFault(PostgresFaultBeforeMandatoryAudit, operationID, IntentActivatePublication, string(candidate.versionID)); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}

	if err := p.persistActivated(ctx, tx, scope, header, candidate, record, evidence, nextRevision); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := p.writeAudit(ctx, tx, header, "activate", OperationActivated, candidate.versionID, candidate.manifestDigest, nextRevision); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := p.injectFault(PostgresFaultBeforeOutbox, operationID, IntentActivatePublication, string(candidate.versionID)); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := p.writeOutbox(ctx, tx, header, "activation", evidence); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}

	record.streamRevision = nextRevision
	record.activationEvidence = cloneEvidence(evidence)
	record.state = OperationActivated
	decision := decisionForRecord(record, false, p.nowValue())
	outcomeRecord := intentOutcome{
		digest: digest, state: OperationActivated, decision: decision, recordedAt: p.nowValue(),
	}
	if err := p.saveOutcome(ctx, tx, scope, IntentActivatePublication, outcomeRecord); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}

	if err := p.injectFault(PostgresFaultBeforeCommit, operationID, IntentActivatePublication, string(candidate.versionID)); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := tx.Commit(); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := p.injectFault(PostgresFaultAfterCommit, operationID, IntentActivatePublication, string(candidate.versionID)); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	return decision, nil
}

// persistActivated commits the immutable Artifact Version, members,
// lineage, stream revision/current head and terminal operation state in one
// transaction, together with the activation evidence.
func (p *PostgresAuthority) persistActivated(ctx context.Context, tx *sql.Tx, scope operationScope, header PublicationIntentHeader, candidate *candidateRecord, record *operationRecord, evidence *PublicationEvidence, nextRevision StreamRevision) error {
	evidenceJSON, err := marshalJSONSafe(evidence)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO `+p.q("publication_activated")+`
		(version_id, policy_domain_id, task_id, schema_version, kind, parent, contract_id,
		 phase_run_id, operation_id, stream_revision, manifest_digest, lineage_digest, occurred_at, evidence)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
		string(candidate.versionID), string(scope.policyDomainID), string(scope.taskID),
		uint64(candidate.schemaVersion), string(candidate.kind), string(candidate.parent),
		string(candidate.contractID), string(candidate.phaseRunID), string(scope.operationID),
		uint64(nextRevision), string(candidate.manifestDigest), string(candidate.lineageDigest),
		int64(p.nowValue()), evidenceJSON); err != nil {
		return err
	}
	for _, member := range candidate.members {
		if _, err := tx.ExecContext(ctx, `INSERT INTO `+p.q("publication_activated_member")+`
			(version_id, slot, artifact_id, kind, logical_name, media_type, size, content_digest)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			string(candidate.versionID), string(member.slot), string(member.artifactID),
			string(member.kind), member.logicalName, string(member.mediaType),
			member.size, string(member.contentDigest)); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE `+p.q("publication_stream")+`
		SET revision = $3, current_head = $4
		WHERE policy_domain_id = $1 AND task_id = $2`,
		string(scope.policyDomainID), string(scope.taskID), uint64(nextRevision), string(candidate.versionID)); err != nil {
		return err
	}
	evidenceJSONOp, err := marshalJSONSafe(evidence)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE `+p.q("publication_operation")+`
		SET state = $4, stream_revision = $5, activation_evidence = $6
		WHERE policy_domain_id = $1 AND task_id = $2 AND operation_id = $3`,
		string(scope.policyDomainID), string(scope.taskID), string(scope.operationID),
		string(OperationActivated), uint64(nextRevision), evidenceJSONOp); err != nil {
		return err
	}
	return nil
}

// rejectFlow is the terminal non-activation decision for an already
// recognized canonical operation: it never creates an Artifact Version,
// member, or current-head mutation, and it records the exact typed staging
// references as C05-owned publication residue for Durable Object release.
func (p *PostgresAuthority) rejectFlow(ctx context.Context, intent RejectPublication, header PublicationIntentHeader, digest Digest, scope operationScope) (PublicationDecision, error) {
	operationID := header.Operation.ID
	if err := p.ensureAuthority(header.Authority, IntentRejectPublication); err != nil {
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
	outcome, err := p.checkOutcomeReplay(ctx, tx, scope, IntentRejectPublication, digest)
	if err != nil {
		return PublicationDecision{}, err
	}
	if outcome != nil {
		decision, replayErr := replayOutcome(*outcome)
		return decision, replayErr
	}
	switch record.state {
	case OperationCancelled, OperationRejected, OperationActivated:
		return PublicationDecision{}, &Error{Code: ErrorTerminalConflict}
	}
	if header.Generation != record.generation || header.Fence != record.fence {
		return PublicationDecision{}, &Error{Code: ErrorStaleAuthority}
	}
	record.rejectReason = intent.Reason
	record.state = OperationRejected
	record.residue = p.releaseResidue(record, header)
	if err := p.persistTerminal(ctx, tx, scope, header, record); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := p.injectFault(PostgresFaultBeforeMandatoryAudit, operationID, IntentRejectPublication, ""); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := p.writeAudit(ctx, tx, header, "reject", OperationRejected, m0VersionID(record), m0ManifestDigest(record), record.streamRevision); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := p.injectFault(PostgresFaultBeforeOutbox, operationID, IntentRejectPublication, ""); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := p.writeOutbox(ctx, tx, header, "rejection", decisionForRecord(record, false, p.nowValue())); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	decision := decisionForRecord(record, false, p.nowValue())
	outcomeRecord := intentOutcome{
		digest: digest, state: OperationRejected, decision: decision, recordedAt: p.nowValue(),
	}
	if err := p.saveOutcome(ctx, tx, scope, IntentRejectPublication, outcomeRecord); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := p.injectFault(PostgresFaultBeforeCommit, operationID, IntentRejectPublication, ""); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := tx.Commit(); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := p.injectFault(PostgresFaultAfterCommit, operationID, IntentRejectPublication, ""); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	return decision, nil
}

// cancelFlow accepts only the exact Task Orchestration or protected
// recovery authority for the current operation with a matching generation
// and fence. Cancel-first linearizes later verify/activate as stale; the
// operation becomes terminal and its staging references become residue.
// Activation-first linearizes cancel as an exact replay of the existing
// active terminal result and never deletes the version or releases its
// references as residue.
func (p *PostgresAuthority) cancelFlow(ctx context.Context, intent CancelPublication, header PublicationIntentHeader, digest Digest, scope operationScope) (PublicationDecision, error) {
	operationID := header.Operation.ID
	if err := p.ensureAuthority(header.Authority, IntentCancelPublication); err != nil {
		return PublicationDecision{}, err
	}
	if header.Authority.Kind == AuthorityRecovery && intent.Reason != CancelRecovery {
		return PublicationDecision{}, &Error{Code: ErrorInvalidIntent}
	}
	if header.Authority.Kind == AuthorityTaskOrchestration && intent.Reason != CancelTaskOrchestration {
		return PublicationDecision{}, &Error{Code: ErrorInvalidIntent}
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
	outcome, err := p.checkOutcomeReplay(ctx, tx, scope, IntentCancelPublication, digest)
	if err != nil {
		return PublicationDecision{}, err
	}
	if outcome != nil {
		decision, replayErr := replayOutcome(*outcome)
		return decision, replayErr
	}
	switch record.state {
	case OperationRejected, OperationCancelled:
		return PublicationDecision{}, &Error{Code: ErrorTerminalConflict}
	case OperationActivated:
		// Activation-first linearization: the operation is already terminal
		// with an active Artifact Version. Cancel returns the existing
		// active terminal result and must never delete the version or
		// release its references as residue.
		decision := decisionForRecord(record, true, p.nowValue())
		return decision, nil
	}
	if header.Generation != record.generation || header.Fence != record.fence {
		return PublicationDecision{}, &Error{Code: ErrorStaleAuthority}
	}
	record.cancelReason = intent.Reason
	record.state = OperationCancelled
	record.residue = p.releaseResidue(record, header)
	if err := p.persistTerminal(ctx, tx, scope, header, record); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := p.injectFault(PostgresFaultBeforeMandatoryAudit, operationID, IntentCancelPublication, ""); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := p.writeAudit(ctx, tx, header, "cancel", OperationCancelled, m0VersionID(record), m0ManifestDigest(record), record.streamRevision); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := p.injectFault(PostgresFaultBeforeOutbox, operationID, IntentCancelPublication, ""); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := p.writeOutbox(ctx, tx, header, "cancellation", decisionForRecord(record, false, p.nowValue())); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	decision := decisionForRecord(record, false, p.nowValue())
	outcomeRecord := intentOutcome{
		digest: digest, state: OperationCancelled, decision: decision, recordedAt: p.nowValue(),
	}
	if err := p.saveOutcome(ctx, tx, scope, IntentCancelPublication, outcomeRecord); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := p.injectFault(PostgresFaultBeforeCommit, operationID, IntentCancelPublication, ""); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := tx.Commit(); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := p.injectFault(PostgresFaultAfterCommit, operationID, IntentCancelPublication, ""); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	return decision, nil
}

// persistTerminal writes the terminal operation state, residue, and the
// exact typed residue staging references in one transaction.
func (p *PostgresAuthority) persistTerminal(ctx context.Context, tx *sql.Tx, scope operationScope, header PublicationIntentHeader, record *operationRecord) error {
	if _, err := tx.ExecContext(ctx, `UPDATE `+p.q("publication_operation")+`
		SET state = $4, reject_reason = $5, cancel_reason = $6, reconcile_mode = $7
		WHERE policy_domain_id = $1 AND task_id = $2 AND operation_id = $3`,
		string(scope.policyDomainID), string(scope.taskID), string(scope.operationID),
		string(record.state), nilIfEmpty(string(record.rejectReason)), nilIfEmpty(string(record.cancelReason)),
		nilIfEmpty(string(record.reconcileMode))); err != nil {
		return err
	}
	if record.residue == nil {
		return nil
	}
	return p.persistResidue(ctx, tx, scope, record.residue)
}

// persistResidue writes the full durable residue record (owner, opaque
// references, operation, generation/fence, expiry, retry, disposition and
// optional C05-owned assembly obligation) in the current transaction.
func (p *PostgresAuthority) persistResidue(ctx context.Context, tx *sql.Tx, scope operationScope, residue *residueRecord) error {
	receiptJSON := []byte(nil)
	if residue.releaseReceipt != nil {
		var err error
		receiptJSON, err = marshalJSONSafe(residue.releaseReceipt)
		if err != nil {
			return err
		}
	}
	assemblyRef, assemblyDigest := "", ""
	assemblyGeneration, assemblyFence := uint64(0), uint64(0)
	if residue.assembly != nil {
		assemblyRef = residue.assembly.Reference
		assemblyDigest = string(residue.assembly.IdentityDigest)
		assemblyGeneration = residue.assembly.Generation
		assemblyFence = residue.assembly.Fence
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO `+p.q("publication_residue")+`
		(policy_domain_id, task_id, operation_id, owner, generation, fence, release_intent, occurred_at,
		 expiry, disposition, requires_reconciliation, attempt_count, consecutive_failures, next_retry_at,
		 claim_generation, claim_fence, last_error_category, release_receipt,
		 assembly_ref, assembly_digest, assembly_generation, assembly_fence, debt_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23)
		ON CONFLICT (policy_domain_id, task_id, operation_id) DO UPDATE
			SET expiry = EXCLUDED.expiry, disposition = EXCLUDED.disposition,
			    requires_reconciliation = EXCLUDED.requires_reconciliation,
			    attempt_count = EXCLUDED.attempt_count,
			    consecutive_failures = EXCLUDED.consecutive_failures,
			    next_retry_at = EXCLUDED.next_retry_at,
			    claim_generation = EXCLUDED.claim_generation, claim_fence = EXCLUDED.claim_fence,
			    last_error_category = EXCLUDED.last_error_category,
			    release_receipt = EXCLUDED.release_receipt,
			    assembly_ref = EXCLUDED.assembly_ref, assembly_digest = EXCLUDED.assembly_digest,
			    assembly_generation = EXCLUDED.assembly_generation, assembly_fence = EXCLUDED.assembly_fence,
			    debt_id = EXCLUDED.debt_id`,
		string(scope.policyDomainID), string(scope.taskID), string(scope.operationID),
		string(residue.owner), uint64(residue.generation), uint64(residue.fence),
		residue.releaseIntent, int64(residue.occurredAt),
		int64(residue.expiry), string(residue.disposition), residue.requiresReconciliation,
		residue.attemptCount, residue.consecutiveFailures, int64(residue.nextRetryAt),
		uint64(residue.claimGeneration), uint64(residue.claimFence), string(residue.lastErrorCategory),
		receiptJSON, assemblyRef, assemblyDigest, assemblyGeneration, assemblyFence,
		string(residue.debtID)); err != nil {
		return err
	}
	for _, staging := range residue.stagingRefs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO `+p.q("publication_residue_staging")+`
			(policy_domain_id, task_id, operation_id, slot, content_id, content_digest, size, purpose, physical_generation, adapter_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			ON CONFLICT (policy_domain_id, task_id, operation_id, slot) DO UPDATE
				SET content_id = EXCLUDED.content_id, content_digest = EXCLUDED.content_digest,
				    size = EXCLUDED.size, purpose = EXCLUDED.purpose,
				    physical_generation = EXCLUDED.physical_generation, adapter_id = EXCLUDED.adapter_id`,
			string(scope.policyDomainID), string(scope.taskID), string(scope.operationID),
			string(staging.slot), string(staging.contentID), string(staging.contentDigest),
			staging.size, string(staging.purpose), staging.physicalGeneration, string(staging.adapterID)); err != nil {
			return err
		}
	}
	return nil
}

// releaseResidue records the typed staging references as C05-owned
// publication residue with a content-free release intent, the durable
// expiry, the pending disposition and the claim bound to the original
// operation generation/fence. It is the only residue transition in the
// canonical core and is persisted BEFORE any physical release action;
// physical release is owned by the Durable Object authority and only
// evidence-backed receipts close the obligation.
func (p *PostgresAuthority) releaseResidue(record *operationRecord, header PublicationIntentHeader) *residueRecord {
	if record.candidate == nil {
		return nil
	}
	now := p.nowValue()
	return &residueRecord{
		operationID: record.operationID, policyDomainID: header.PolicyDomainID,
		taskID: header.TaskID, owner: header.Authority.Kind,
		generation: record.generation, fence: record.fence,
		releaseIntent: "release_staging", stagingRefs: cloneStaging(record.candidate.staging),
		occurredAt:      now,
		expiry:          p.residueExpiry(now),
		disposition:     ResiduePending,
		claimGeneration: record.generation, claimFence: record.fence,
	}
}

// reconcileFlow only inspects or replays the original operation and its
// evidence references. It cannot allocate a new ArtifactVersionID, modify
// the manifest or parent, or create a Task retry.
func (p *PostgresAuthority) reconcileFlow(ctx context.Context, intent ReconcilePublication, header PublicationIntentHeader, digest Digest, scope operationScope) (PublicationDecision, error) {
	if err := p.ensureReconcileAuthority(header.Authority, intent.Mode); err != nil {
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
	// Reconcile always re-evaluates the original operation against the
	// current authority state (matching the in-memory authority): it never
	// replays a historical reconcile snapshot, because its job is to inspect
	// or replay the ORIGINAL operation after ambiguous or stale
	// dispositions. The fresh outcome is upserted over any earlier reconcile
	// outcome.
	switch intent.Mode {
	case ReconcileInspect:
		record.reconcileMode = ReconcileInspect
	case ReconcileCompleteRelease:
		// Release reconciliation re-evaluates the ORIGINAL operation's
		// residue against the current Durable Object registry. It never
		// creates a new ArtifactVersionID and never changes the manifest,
		// parent or head. Attempt facts (backoff, error category) stay
		// durable even when the re-evaluation reports a retryable or
		// ambiguous error.
		record.reconcileMode = ReconcileCompleteRelease
		decision, reconcileErr := p.evaluateRelease(ctx, tx, header, digest, scope, record, record.operationID)
		if err := p.writeAudit(ctx, tx, header, "reconcile", record.state, m0VersionID(record), m0ManifestDigest(record), record.streamRevision); err != nil {
			return PublicationDecision{}, normalizePersistenceError(err)
		}
		outcomeRecord := intentOutcome{
			digest: digest, state: record.state, decision: decision, recordedAt: p.nowValue(),
		}
		if err := p.saveOutcome(ctx, tx, scope, IntentReconcilePublication, outcomeRecord); err != nil {
			return PublicationDecision{}, normalizePersistenceError(err)
		}
		if err := p.injectFault(PostgresFaultBeforeCommit, scope.operationID, IntentReconcilePublication, ""); err != nil {
			return PublicationDecision{}, normalizePersistenceError(err)
		}
		if err := tx.Commit(); err != nil {
			return PublicationDecision{}, normalizePersistenceError(err)
		}
		if err := p.injectFault(PostgresFaultAfterCommit, scope.operationID, IntentReconcilePublication, ""); err != nil {
			return PublicationDecision{}, normalizePersistenceError(err)
		}
		return decision, reconcileErr
	case ReconcileCompleteVerification:
		if record.state == OperationReconciliationRequired && record.verification != nil {
			resolved, failure := p.completeVerification(ctx, tx, record)
			if failure != nil {
				record.verification.failure = failure
				record.verification.state = VerificationFailed
				record.state = OperationPrepared
			} else if resolved {
				record.verification.state = VerificationVerified
				record.state = OperationVerified
			} else {
				record.verification.state = VerificationAmbiguous
				record.state = OperationReconciliationRequired
			}
			if err := p.persistVerification(ctx, tx, scope, record, record.verification, record.state); err != nil {
				return PublicationDecision{}, normalizePersistenceError(err)
			}
		}
		record.reconcileMode = ReconcileCompleteVerification
	case ReconcileConfirmCancellation:
		if record.state != OperationCancelled {
			return PublicationDecision{}, &Error{Code: ErrorTerminalConflict}
		}
		record.reconcileMode = ReconcileConfirmCancellation
	case ReconcileConfirmRejection:
		if record.state != OperationRejected {
			return PublicationDecision{}, &Error{Code: ErrorTerminalConflict}
		}
		record.reconcileMode = ReconcileConfirmRejection
	default:
		return PublicationDecision{}, &Error{Code: ErrorInvalidIntent}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE `+p.q("publication_operation")+`
		SET reconcile_mode = $4 WHERE policy_domain_id = $1 AND task_id = $2 AND operation_id = $3`,
		string(scope.policyDomainID), string(scope.taskID), string(scope.operationID),
		string(record.reconcileMode)); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	decision := decisionForRecord(record, false, p.nowValue())
	outcomeRecord := intentOutcome{
		digest: digest, state: record.state, decision: decision, recordedAt: p.nowValue(),
	}
	if err := p.saveOutcome(ctx, tx, scope, IntentReconcilePublication, outcomeRecord); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := p.injectFault(PostgresFaultBeforeMandatoryAudit, scope.operationID, IntentReconcilePublication, ""); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := p.writeAudit(ctx, tx, header, "reconcile", record.state, m0VersionID(record), m0ManifestDigest(record), record.streamRevision); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := p.injectFault(PostgresFaultBeforeCommit, scope.operationID, IntentReconcilePublication, ""); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := tx.Commit(); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	if err := p.injectFault(PostgresFaultAfterCommit, scope.operationID, IntentReconcilePublication, ""); err != nil {
		return PublicationDecision{}, normalizePersistenceError(err)
	}
	return decision, nil
}

// completeVerification re-evaluates the recorded pending capabilities
// against the current Durable Object authority registry inside the
// reconciliation transaction. It returns resolved=true only when every
// pending capability is now current and matches the pinned reference.
func (p *PostgresAuthority) completeVerification(ctx context.Context, tx *sql.Tx, record *operationRecord) (bool, *EvidenceFailure) {
	candidate := record.candidate
	if candidate == nil {
		return false, &EvidenceFailure{Kind: "candidate_missing"}
	}
	refBySlot := make(map[MemberSlotID]ContentCapabilityRef, len(candidate.capabilityRefs))
	for _, ref := range candidate.capabilityRefs {
		refBySlot[ref.MemberSlot] = ref
	}
	pending := record.verification.pendingCapabilitySlots
	if len(pending) == 0 {
		pending = make([]MemberSlotID, 0, len(candidate.capabilityRefs))
		for _, ref := range candidate.capabilityRefs {
			pending = append(pending, ref.MemberSlot)
		}
	}
	for _, slot := range pending {
		ref := refBySlot[slot]
		current, ok := p.currentContentCapabilityFrom(ctx, tx, ref.CapabilityID)
		if !ok {
			return false, nil // still ambiguous: not resolvable
		}
		if current.Digest != ref.Digest {
			return false, &EvidenceFailure{Kind: "capability_stale", CapabilityID: ref.CapabilityID}
		}
	}
	return true, nil
}

// ensureAuthority enforces the typed mutation authority. Only the Task
// Orchestration authority may submit prepare/verify/activate/reject/
// cancel/reconcile; the protected recovery authority may only cancel; the
// protected publication cleanup authority may only record assembly
// obligations, request residue release, and resolve C05-owned Cleanup
// Debts.
func (p *PostgresAuthority) ensureAuthority(authority PublicationAuthority, kind PublicationIntentKind) *Error {
	if authority.Kind == AuthorityRecovery {
		if kind != IntentCancelPublication {
			return &Error{Code: ErrorOwnershipDenied}
		}
		if authority.ID != p.recoveryAuth {
			return &Error{Code: ErrorOwnershipDenied}
		}
		return nil
	}
	if authority.Kind == AuthorityPublicationCleanup {
		switch kind {
		case IntentRecordResidueAssembly, IntentReleaseResidue, IntentResolveCleanupDebt:
			if authority.ID != p.cleanupAuth {
				return &Error{Code: ErrorOwnershipDenied}
			}
			return nil
		default:
			return &Error{Code: ErrorOwnershipDenied}
		}
	}
	if authority.Kind != AuthorityTaskOrchestration {
		return &Error{Code: ErrorOwnershipDenied}
	}
	if authority.ID != p.toAuth {
		return &Error{Code: ErrorOwnershipDenied}
	}
	return nil
}

func (c *candidateRecord) capabilityBySlot(slot MemberSlotID) ContentCapabilityRef {
	for _, ref := range c.capabilityRefs {
		if ref.MemberSlot == slot {
			return ref
		}
	}
	return ContentCapabilityRef{}
}

// codeOf returns the safe error code of err, or an empty code.
func codeOf(err error) ErrorCode {
	var publicationError *Error
	if errors.As(err, &publicationError) && publicationError != nil {
		return publicationError.Code
	}
	return ""
}

// nilIfEmpty converts an empty string into SQL NULL.
func nilIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}
