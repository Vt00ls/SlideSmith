package runtimeexecution

import (
	"context"
	"time"
)

// In-memory Cleanup Debt maintenance engine. It mirrors the authoritative
// PostgreSQL lifecycle: obligation-before-attempt, claim/retry with exact
// revision/generation/fence, class-specific resolution proof, audited
// AcceptedException, and explicit exception expiry/reopen that never reports
// reclaimed capacity.

func (engine *invariantEngine) createCleanupObligation(
	command CreateCleanupObligation,
) (RuntimeMaintenanceDecision, error) {
	engine.store.mu.Lock()
	defer engine.store.mu.Unlock()
	if retained, exists, err := engine.replayMaintenanceLocked(command.OperationID, command.CanonicalRequestDigest); exists {
		return retained, err
	}
	creation := command.Obligation
	record, err := engine.authorizeCleanupRuntimeLocked(
		creation.PersonalWorkspaceID, creation.RuntimeRunID, creation.Authority,
		command.ExpectedRuntimeRevision, command.ExpectedRuntimeFence,
	)
	if err != nil {
		return RuntimeMaintenanceDecision{}, err
	}
	if _, exists := engine.store.cleanupDebts[creation.DebtID]; exists {
		return RuntimeMaintenanceDecision{}, newError(ErrorIntegrityConflict)
	}
	if !engine.hasCauseDecisionLocked(record, creation.CauseDecisionID, creation.CauseOperationID) {
		return RuntimeMaintenanceDecision{}, newError(ErrorIntegrityConflict)
	}
	status := cleanupDebtOpen
	retry := cleanupRetryReady
	if creation.Blockers.Classes != 0 {
		status = cleanupDebtBlocked
		retry = cleanupRetryBlocked
	}
	created := cleanupDebtRecord{
		DebtID: creation.DebtID, Revision: 1, OwnerModule: postgresCleanupOwnerModule,
		PersonalWorkspaceID: record.fixture.PersonalWorkspaceID, TaskID: record.fixture.TaskID,
		PhaseRunID: record.fixture.PhaseRunID, RuntimeRunID: record.fixture.RuntimeRunID,
		OwnerAuthority: record.fixture.Owner, ResourceClass: creation.ResourceClass,
		ResourceIdentityDigest: creation.ResourceIdentityDigest, ResourceGeneration: creation.ResourceGeneration,
		ResourceFence: creation.ResourceFence, CleanupIntent: creation.Intent,
		CauseDecisionID: creation.CauseDecisionID, CauseOperationID: creation.CauseOperationID,
		RetentionFactDigest: creation.RetentionFactDigest, EligibilityFactDigest: creation.EligibilityFactDigest,
		Status: status, Unresolved: true, Uncontained: creation.Uncontained,
		CreatedAt: creation.CreatedAt, EligibleAt: creation.EligibleAt, RetryDisposition: retry,
		Estimation: creation.Estimation, Blockers: creation.Blockers, LastMutationID: creation.MutationID,
	}
	created = normalizeCleanupDebtRecord(created)
	if !validCleanupDebtRecord(created) {
		return RuntimeMaintenanceDecision{}, newError(ErrorInvalidRequest)
	}
	engine.store.cleanupDebts[created.DebtID] = &created
	decision := RuntimeMaintenanceDecision{
		OperationID: command.OperationID, CanonicalRequestDigest: command.CanonicalRequestDigest,
		RuntimeRevision: record.fixture.RuntimeRevision, RuntimeFence: record.fixture.RuntimeFence,
		CleanupDebt: cleanupDebtDecisionFromRecord(created, false),
	}
	engine.store.maintenance[command.OperationID] = decision
	return decision, nil
}

func (engine *invariantEngine) recordCleanupAttempt(
	command RecordCleanupAttempt,
) (RuntimeMaintenanceDecision, error) {
	engine.store.mu.Lock()
	defer engine.store.mu.Unlock()
	if retained, exists, err := engine.replayMaintenanceLocked(command.OperationID, command.CanonicalRequestDigest); exists {
		return retained, err
	}
	attempt := command.Attempt
	record, err := engine.authorizeCleanupRuntimeLocked(
		attempt.PersonalWorkspaceID, attempt.RuntimeRunID, attempt.Authority,
		command.ExpectedRuntimeRevision, command.ExpectedRuntimeFence,
	)
	if err != nil {
		return RuntimeMaintenanceDecision{}, err
	}
	debt := engine.store.cleanupDebts[attempt.DebtID]
	if debt == nil {
		return RuntimeMaintenanceDecision{}, newError(ErrorAuthorizationDenied)
	}
	if !cleanupMutationMatchesRecord(attempt.PersonalWorkspaceID, attempt.RuntimeRunID,
		attempt.Authority, attempt.ExpectedRevision, attempt.ResourceGeneration, attempt.ResourceFence, *debt) ||
		debt.Status == cleanupDebtResolved || attempt.AttemptedAt.Before(debt.CreatedAt) {
		return RuntimeMaintenanceDecision{}, newError(ErrorIntegrityConflict)
	}
	updated := *debt
	updated.Revision++
	if updated.AttemptCount == 0 {
		updated.FirstAttemptAt = attempt.AttemptedAt
	}
	updated.LastAttemptAt = attempt.AttemptedAt
	updated.NextRetryAt = attempt.NextRetryAt
	updated.AttemptCount++
	updated.ConsecutiveFailureCount++
	updated.ClaimGeneration = attempt.ClaimGeneration
	updated.ClaimFence = attempt.ClaimFence
	updated.RetryDisposition = attempt.RetryDisposition
	updated.LastErrorCategory = attempt.FailureCategory
	updated.LastErrorDigest = attempt.LastErrorDigest
	updated.LastErrorEvidenceReference = attempt.LastErrorEvidenceReference
	updated.Estimation = attempt.Estimation
	updated.Blockers = attempt.Blockers
	updated.Uncontained = attempt.Uncontained
	updated.LastMutationID = attempt.MutationID
	if attempt.Blockers.Classes == 0 {
		updated.Status = cleanupDebtRetryScheduled
	} else {
		updated.Status = cleanupDebtBlocked
	}
	updated = normalizeCleanupDebtRecord(updated)
	if !validCleanupDebtRecord(updated) {
		return RuntimeMaintenanceDecision{}, newError(ErrorInvalidRequest)
	}
	engine.store.cleanupDebts[updated.DebtID] = &updated
	decision := RuntimeMaintenanceDecision{
		OperationID: command.OperationID, CanonicalRequestDigest: command.CanonicalRequestDigest,
		RuntimeRevision: record.fixture.RuntimeRevision, RuntimeFence: record.fixture.RuntimeFence,
		CleanupDebt: cleanupDebtDecisionFromRecord(updated, false),
	}
	engine.store.maintenance[command.OperationID] = decision
	return decision, nil
}

func (engine *invariantEngine) resolveCleanupDebt(
	command ResolveCleanupDebt,
) (RuntimeMaintenanceDecision, error) {
	engine.store.mu.Lock()
	defer engine.store.mu.Unlock()
	if retained, exists, err := engine.replayMaintenanceLocked(command.OperationID, command.CanonicalRequestDigest); exists {
		return retained, err
	}
	resolution := command.Resolution
	record, err := engine.authorizeCleanupRuntimeLocked(
		resolution.PersonalWorkspaceID, resolution.RuntimeRunID, resolution.Authority,
		command.ExpectedRuntimeRevision, command.ExpectedRuntimeFence,
	)
	if err != nil {
		return RuntimeMaintenanceDecision{}, err
	}
	debt := engine.store.cleanupDebts[resolution.DebtID]
	if debt == nil {
		return RuntimeMaintenanceDecision{}, newError(ErrorAuthorizationDenied)
	}
	if !cleanupMutationMatchesRecord(resolution.PersonalWorkspaceID, resolution.RuntimeRunID,
		resolution.Authority, resolution.ExpectedRevision, resolution.ResourceGeneration,
		resolution.ResourceFence, *debt) || debt.Status == cleanupDebtResolved {
		return RuntimeMaintenanceDecision{}, newError(ErrorIntegrityConflict)
	}
	if record.evidenceRoot != resolution.EvidenceRoot || !knownEvidenceRoot(resolution.EvidenceRoot) ||
		resolution.EvidenceRoot.EvidenceRootID == (EvidenceRootID{}) {
		return RuntimeMaintenanceDecision{}, newError(ErrorIntegrityConflict)
	}
	if resolution.Class == cleanupResolutionAcceptedException {
		if resolution.Authority.kind != AuthorityAdministrator ||
			!validOpaqueID(resolution.ApprovalReference) || resolution.ExceptionUntil.IsZero() ||
			!resolution.ExceptionUntil.After(resolution.ResolvedAt) {
			return RuntimeMaintenanceDecision{}, newError(ErrorAuthorizationDenied)
		}
	}
	resolved := *debt
	resolved.Revision++
	resolved.Status = cleanupDebtResolved
	resolved.Unresolved = false
	resolved.Uncontained = resolution.Uncontained
	resolved.NextRetryAt = time.Time{}
	resolved.RetryDisposition = cleanupRetryNone
	resolved.Blockers = resolution.RemainingBlockers
	resolved.ResolvedAt = resolution.ResolvedAt
	resolved.ResolutionClass = resolution.Class
	resolved.ResolutionReason = resolution.Reason
	resolved.ResolutionAuthority = resolution.Authority
	resolved.ResolutionAuditFactID = "cleanup-resolution-audit-" + command.CanonicalRequestDigest.String()
	resolved.ResolutionEvidenceRoot = resolution.EvidenceRoot
	resolved.ResolutionExpiresAt = resolution.ExceptionUntil
	resolved.ResolutionIncidentReference = resolution.IncidentReference
	resolved.ResolutionTicketReference = resolution.TicketReference
	resolved.ResolutionApprovalReference = resolution.ApprovalReference
	resolved.LastMutationID = resolution.MutationID
	if resolution.Class != cleanupResolutionAcceptedException {
		if err := engine.verifyCleanupResolutionProofLocked(resolved, resolution); err != nil {
			return RuntimeMaintenanceDecision{}, err
		}
	}
	resolved = normalizeCleanupDebtRecord(resolved)
	if !validCleanupDebtRecord(resolved) {
		return RuntimeMaintenanceDecision{}, newError(ErrorInvalidRequest)
	}
	engine.store.cleanupDebts[resolved.DebtID] = &resolved
	capacityReleased := resolved.ResolutionClass == cleanupResolutionReclaimed ||
		resolved.ResolutionClass == cleanupResolutionAlreadyAbsent
	decision := RuntimeMaintenanceDecision{
		OperationID: command.OperationID, CanonicalRequestDigest: command.CanonicalRequestDigest,
		RuntimeRevision: record.fixture.RuntimeRevision, RuntimeFence: record.fixture.RuntimeFence,
		CleanupDebt: cleanupDebtDecisionFromRecord(resolved, capacityReleased),
	}
	engine.store.maintenance[command.OperationID] = decision
	return decision, nil
}

func (engine *invariantEngine) expireCleanupDebtException(
	command ExpireCleanupDebtException,
) (RuntimeMaintenanceDecision, error) {
	engine.store.mu.Lock()
	defer engine.store.mu.Unlock()
	if retained, exists, err := engine.replayMaintenanceLocked(command.OperationID, command.CanonicalRequestDigest); exists {
		return retained, err
	}
	input := command.ExpireCleanupDebtExceptionInput
	record, err := engine.authorizeCleanupRuntimeLocked(
		input.PersonalWorkspaceID, input.RuntimeRunID, input.Authority,
		input.ExpectedRuntimeRevision, input.ExpectedRuntimeFence,
	)
	if err != nil {
		return RuntimeMaintenanceDecision{}, err
	}
	debt := engine.store.cleanupDebts[input.DebtID]
	if debt == nil {
		return RuntimeMaintenanceDecision{}, newError(ErrorAuthorizationDenied)
	}
	if !cleanupMutationMatchesRecord(input.PersonalWorkspaceID, input.RuntimeRunID,
		input.Authority, input.ExpectedRevision, input.ResourceGeneration, input.ResourceFence, *debt) ||
		debt.Status != cleanupDebtResolved || debt.ResolutionClass != cleanupResolutionAcceptedException ||
		input.ExpiredAt.Before(debt.ResolutionExpiresAt) {
		return RuntimeMaintenanceDecision{}, newError(ErrorIntegrityConflict)
	}
	reopened, err := reopenCleanupDebtRecord(*debt, input.ExpiredAt, input.ExpiredAt, input.MutationID())
	if err != nil {
		return RuntimeMaintenanceDecision{}, err
	}
	engine.store.cleanupDebts[reopened.DebtID] = &reopened
	decision := RuntimeMaintenanceDecision{
		OperationID: command.OperationID, CanonicalRequestDigest: command.CanonicalRequestDigest,
		RuntimeRevision: record.fixture.RuntimeRevision, RuntimeFence: record.fixture.RuntimeFence,
		CleanupDebt: cleanupDebtDecisionFromRecord(reopened, false),
	}
	decision.CleanupDebt.Expired = true
	decision.CleanupDebt.Reopened = true
	engine.store.maintenance[command.OperationID] = decision
	return decision, nil
}

func (engine *invariantEngine) reopenCleanupDebt(
	command ReopenCleanupDebt,
) (RuntimeMaintenanceDecision, error) {
	engine.store.mu.Lock()
	defer engine.store.mu.Unlock()
	if retained, exists, err := engine.replayMaintenanceLocked(command.OperationID, command.CanonicalRequestDigest); exists {
		return retained, err
	}
	input := command.ReopenCleanupDebtInput
	record, err := engine.authorizeCleanupRuntimeLocked(
		input.PersonalWorkspaceID, input.RuntimeRunID, input.Authority,
		input.ExpectedRuntimeRevision, input.ExpectedRuntimeFence,
	)
	if err != nil {
		return RuntimeMaintenanceDecision{}, err
	}
	debt := engine.store.cleanupDebts[input.DebtID]
	if debt == nil {
		return RuntimeMaintenanceDecision{}, newError(ErrorAuthorizationDenied)
	}
	if !cleanupMutationMatchesRecord(input.PersonalWorkspaceID, input.RuntimeRunID,
		input.Authority, input.ExpectedRevision, input.ResourceGeneration, input.ResourceFence, *debt) ||
		debt.Status != cleanupDebtResolved || debt.ResolutionClass != cleanupResolutionAcceptedException ||
		input.ReopenedAt.Before(debt.ResolutionExpiresAt) {
		return RuntimeMaintenanceDecision{}, newError(ErrorIntegrityConflict)
	}
	reopened, err := reopenCleanupDebtRecord(*debt, input.ReopenedAt, input.ReopenedAt, input.MutationID())
	if err != nil {
		return RuntimeMaintenanceDecision{}, err
	}
	engine.store.cleanupDebts[reopened.DebtID] = &reopened
	decision := RuntimeMaintenanceDecision{
		OperationID: command.OperationID, CanonicalRequestDigest: command.CanonicalRequestDigest,
		RuntimeRevision: record.fixture.RuntimeRevision, RuntimeFence: record.fixture.RuntimeFence,
		CleanupDebt: cleanupDebtDecisionFromRecord(reopened, false),
	}
	decision.CleanupDebt.Reopened = true
	engine.store.maintenance[command.OperationID] = decision
	return decision, nil
}

func (input ExpireCleanupDebtExceptionInput) MutationID() string {
	return input.OperationID.String()
}

func (input ReopenCleanupDebtInput) MutationID() string {
	return input.OperationID.String()
}

func (engine *invariantEngine) authorizeCleanupRuntimeLocked(
	workspaceID PersonalWorkspaceID,
	runtimeRunID RuntimeRunID,
	authority RuntimeAuthority,
	expectedRevision RuntimeRevision,
	expectedFence RuntimeFence,
) (*runtimeRecord, error) {
	record := engine.store.runtimes[runtimeRunID]
	if record == nil || record.fixture.PersonalWorkspaceID != workspaceID ||
		record.fixture.Owner != authority {
		return nil, newError(ErrorAuthorizationDenied)
	}
	if expectedRevision != record.fixture.RuntimeRevision || expectedFence != record.fixture.RuntimeFence {
		return nil, newError(ErrorIntegrityConflict)
	}
	return record, nil
}

func (engine *invariantEngine) hasCauseDecisionLocked(
	record *runtimeRecord,
	decisionID RuntimeDecisionID,
	operationID OperationID,
) bool {
	if record.acceptedStart.DecisionID == decisionID && record.acceptedStart.OperationID == operationID {
		return true
	}
	for _, fact := range record.decisions {
		if fact.DecisionID == decisionID && fact.OperationID == operationID {
			return true
		}
	}
	return false
}

func (engine *invariantEngine) verifyCleanupResolutionProofLocked(
	debt cleanupDebtRecord,
	resolution cleanupDebtResolution,
) error {
	key := cleanupProofKey{
		debtID: debt.DebtID, resolutionClass: resolution.Class,
		evidenceRootID: resolution.EvidenceRoot.EvidenceRootID.String(),
	}
	proof, exists := engine.store.cleanupProofs[key]
	if !exists {
		return newError(ErrorIntegrityConflict)
	}
	if !validCleanupResolutionProofState(proof) || !cleanupResolutionProofMatchesRecord(proof, debt) {
		return newError(ErrorIntegrityConflict)
	}
	switch proof.ResolutionClass {
	case cleanupResolutionReclaimed:
		if !proof.DeletionOrResetProven || !proof.ReferencesClear || !proof.ContainmentClear ||
			proof.Disposition != cleanupProofDeletionOrReset {
			return newError(ErrorIntegrityConflict)
		}
	case cleanupResolutionAlreadyAbsent:
		if !proof.ExactGenerationAbsent || !proof.ReferencesClear || !proof.ContainmentClear ||
			proof.Disposition != cleanupProofExactGenerationAbsent {
			return newError(ErrorIntegrityConflict)
		}
	case cleanupResolutionRetainedByAuthority:
		if proof.Disposition != cleanupProofRetainedByAuthority ||
			proof.RetainingAuthorityFactRoot == "" {
			return newError(ErrorIntegrityConflict)
		}
	default:
		return newError(ErrorIntegrityConflict)
	}
	return nil
}

func (engine *invariantEngine) Diagnose(
	ctx context.Context,
	query OperationalDiagnosticQuery,
) (OperationalDiagnosticView, error) {
	if ctx == nil || ctx.Err() != nil {
		return OperationalDiagnosticView{}, newError(ErrorDependencyUnavailable)
	}
	if !validOperationalDiagnosticQuery(query) {
		return OperationalDiagnosticView{}, newError(ErrorAuthorizationDenied)
	}
	engine.store.mu.Lock()
	defer engine.store.mu.Unlock()
	switch query.Lookup {
	case DiagnosticLookupCleanupDebt:
		debt := engine.store.cleanupDebts[query.DebtID]
		if debt == nil || debt.RuntimeRunID != query.RuntimeRunID {
			return OperationalDiagnosticView{}, newError(ErrorAuthorizationDenied)
		}
		record := engine.store.runtimes[debt.RuntimeRunID]
		if record == nil || record.fixture.PersonalWorkspaceID != query.PersonalWorkspaceID {
			return OperationalDiagnosticView{}, newError(ErrorAuthorizationDenied)
		}
		return OperationalDiagnosticView{
			Lookup: query.Lookup,
			Debt: &CleanupDebtDiagnosticView{
				DebtID: debt.DebtID, OwnerModule: debt.OwnerModule, ResourceClass: debt.ResourceClass,
				Status: debt.Status, Unresolved: debt.Unresolved, RetryDisposition: debt.RetryDisposition,
				LastError: debt.LastErrorCategory, EstimateState: debt.Estimation.State,
				Blockers: debt.Blockers.Classes, ResolutionClass: debt.ResolutionClass,
				ExceptionUntil: debt.ResolutionExpiresAt, AttemptCount: debt.AttemptCount,
				DebtRevision: debt.Revision,
			},
		}, nil
	case DiagnosticLookupExecutionNode:
		node := engine.store.nodes[query.ExecutionNodeID]
		if node == nil {
			return OperationalDiagnosticView{}, newError(ErrorAuthorizationDenied)
		}
		return OperationalDiagnosticView{
			Lookup: query.Lookup,
			Node: &NodeDiagnosticView{
				ExecutionNodeID: node.ExecutionNodeID, Generation: node.Generation,
				Readiness: node.Readiness, Occupancy: node.Occupancy, Quarantined: node.Quarantined,
				Containment: node.Containment, Reset: node.Reset,
			},
		}, nil
	case DiagnosticLookupRuntimeLease:
		record := engine.store.runtimes[query.RuntimeRunID]
		if record == nil || record.fixture.PersonalWorkspaceID != query.PersonalWorkspaceID {
			return OperationalDiagnosticView{}, newError(ErrorAuthorizationDenied)
		}
		return OperationalDiagnosticView{
			Lookup: query.Lookup,
			Runtime: &RuntimeDiagnosticView{
				RuntimeRunID: record.fixture.RuntimeRunID, RuntimeRevision: record.fixture.RuntimeRevision,
				State: record.fixture.State, Outcome: record.fixture.Outcome,
				LeaseDisposition: record.lease.Disposition,
				Physical:         record.capacity.Physical, Cleanup: record.cleanup.Status,
				Quarantined: record.node.Quarantined, Containment: record.node.Containment,
				Reset: record.node.Reset,
			},
		}, nil
	default:
		return OperationalDiagnosticView{}, newError(ErrorInvalidRequest)
	}
}
