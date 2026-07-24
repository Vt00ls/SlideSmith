package taskworkspace

import (
	"context"
	"errors"
	"sort"
)

type (
	CheckpointRetentionAuthorityID   string
	CheckpointRetentionAuthorityKind string
	CheckpointRetentionDecision      string
	CheckpointRetentionPolicyID      string
	RetentionGeneration              uint64
)

const (
	CheckpointRecoveryLineageAuthority   CheckpointRetentionAuthorityKind = "current_recovery_lineage"
	CheckpointExplicitReferenceAuthority CheckpointRetentionAuthorityKind = "explicit_reference"
	CheckpointCommitLeaseAuthority       CheckpointRetentionAuthorityKind = "commit_lease"
	CheckpointRestoreLeaseAuthority      CheckpointRetentionAuthorityKind = "restore_lease"
	CheckpointIntegrityIncidentAuthority CheckpointRetentionAuthorityKind = "integrity_incident"
	CheckpointRecoveryPointPinAuthority  CheckpointRetentionAuthorityKind = "recovery_point_pin"

	CheckpointRetained            CheckpointRetentionDecision = "retained"
	CheckpointPendingReclaim      CheckpointRetentionDecision = "pending_reclaim"
	CheckpointPhysicallyReclaimed CheckpointRetentionDecision = "reclaimed"
)

type CheckpointRetentionPolicy struct {
	ID               CheckpointRetentionPolicyID
	ReclamationGrace Duration
}

type CheckpointRetentionAuthority struct {
	ID        CheckpointRetentionAuthorityID
	Kind      CheckpointRetentionAuthorityKind
	ExpiresAt Instant
}

type InspectCheckpointRetentionRequest struct {
	PolicyDomainID  PolicyDomainID
	TaskID          TaskID
	TaskWorkspaceID TaskWorkspaceID
	CheckpointID    CheckpointID
}

type CheckpointRetention struct {
	TaskWorkspaceID     TaskWorkspaceID
	CheckpointID        CheckpointID
	RevisionID          RevisionID
	Decision            CheckpointRetentionDecision
	RetentionGeneration RetentionGeneration
	Generation          Generation
	Fence               Fence
	PolicyID            CheckpointRetentionPolicyID
	EligibleAt          Instant
	Authorities         []CheckpointRetentionAuthority
	Operation           Operation
}

type checkpointRetentionRecord struct {
	generation           RetentionGeneration
	generationAtCreation Generation
	fenceAtCreation      Fence
	policyID             CheckpointRetentionPolicyID
	eligibleAt           Instant
	authorities          map[CheckpointRetentionAuthorityID]CheckpointRetentionAuthority
	reclaimed            bool
	reclamationEvidence  CheckpointReclamationEvidence
}

func (r checkpointRetentionRecord) semanticallyRetained(now Instant) bool {
	for _, authority := range r.authorities {
		if checkpointRetentionAuthorityIsActive(authority, now) {
			return true
		}
	}
	return false
}

type ReleaseCheckpointRetentionRequest struct {
	PolicyDomainID              PolicyDomainID
	TaskID                      TaskID
	TaskWorkspaceID             TaskWorkspaceID
	CheckpointID                CheckpointID
	AuthorityID                 CheckpointRetentionAuthorityID
	ExpectedRetentionGeneration RetentionGeneration
	Generation                  Generation
	Fence                       Fence
	Operation                   Operation
}

type AttachCheckpointRetentionRequest struct {
	PolicyDomainID              PolicyDomainID
	TaskID                      TaskID
	TaskWorkspaceID             TaskWorkspaceID
	CheckpointID                CheckpointID
	ExpectedRetentionGeneration RetentionGeneration
	Generation                  Generation
	Fence                       Fence
	Authority                   CheckpointRetentionAuthority
	Operation                   Operation
}

func (r AttachCheckpointRetentionRequest) CanonicalRequestDigest() Digest {
	return canonicalDigest(struct {
		Kind                        string
		PolicyDomainID              PolicyDomainID
		TaskID                      TaskID
		TaskWorkspaceID             TaskWorkspaceID
		CheckpointID                CheckpointID
		ExpectedRetentionGeneration RetentionGeneration
		Generation                  Generation
		Fence                       Fence
		Authority                   CheckpointRetentionAuthority
		OperationID                 OperationID
	}{
		Kind:                        "attach_checkpoint_retention",
		PolicyDomainID:              r.PolicyDomainID,
		TaskID:                      r.TaskID,
		TaskWorkspaceID:             r.TaskWorkspaceID,
		CheckpointID:                r.CheckpointID,
		ExpectedRetentionGeneration: r.ExpectedRetentionGeneration,
		Generation:                  r.Generation,
		Fence:                       r.Fence,
		Authority:                   r.Authority,
		OperationID:                 r.Operation.ID,
	})
}

func (r ReleaseCheckpointRetentionRequest) CanonicalRequestDigest() Digest {
	return canonicalDigest(struct {
		Kind                        string
		PolicyDomainID              PolicyDomainID
		TaskID                      TaskID
		TaskWorkspaceID             TaskWorkspaceID
		CheckpointID                CheckpointID
		AuthorityID                 CheckpointRetentionAuthorityID
		ExpectedRetentionGeneration RetentionGeneration
		Generation                  Generation
		Fence                       Fence
		OperationID                 OperationID
	}{
		Kind:                        "release_checkpoint_retention",
		PolicyDomainID:              r.PolicyDomainID,
		TaskID:                      r.TaskID,
		TaskWorkspaceID:             r.TaskWorkspaceID,
		CheckpointID:                r.CheckpointID,
		AuthorityID:                 r.AuthorityID,
		ExpectedRetentionGeneration: r.ExpectedRetentionGeneration,
		Generation:                  r.Generation,
		Fence:                       r.Fence,
		OperationID:                 r.Operation.ID,
	})
}

func (m *inMemory) InspectCheckpointRetention(
	_ context.Context,
	request InspectCheckpointRetentionRequest,
) (CheckpointRetention, error) {
	if request.PolicyDomainID == "" || request.TaskID == "" || request.TaskWorkspaceID == "" ||
		request.CheckpointID == "" {
		return CheckpointRetention{}, &Error{Code: ErrorInvalidIntent}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	workspace, workspaceOK := m.workspaces[request.TaskID]
	checkpoint, checkpointOK := m.checkpoints[request.CheckpointID]
	if !workspaceOK || workspace.policyDomainID != request.PolicyDomainID ||
		workspace.taskWorkspaceID != request.TaskWorkspaceID || !checkpointOK ||
		checkpoint.taskWorkspaceID != request.TaskWorkspaceID {
		return CheckpointRetention{}, &Error{Code: ErrorOwnershipDenied}
	}

	return checkpointRetentionSnapshot(request.CheckpointID, checkpoint, workspace, m.now(), Operation{}), nil
}

func (m *inMemory) ReleaseCheckpointRetention(
	ctx context.Context,
	request ReleaseCheckpointRetentionRequest,
) (CheckpointRetention, error) {
	if request.PolicyDomainID == "" || request.TaskID == "" || request.TaskWorkspaceID == "" ||
		request.CheckpointID == "" || request.AuthorityID == "" || request.ExpectedRetentionGeneration == 0 ||
		request.Generation == 0 || request.Fence == 0 || request.Operation.ID == "" ||
		request.Operation.RequestDigest != request.CanonicalRequestDigest() {
		return CheckpointRetention{}, &Error{Code: ErrorInvalidIntent}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	scope := operationScope{request.PolicyDomainID, request.TaskID, request.Operation.ID}
	if result, replayed, err := replayOperation[CheckpointRetention](m.operations, scope, request.Operation); replayed {
		return result, err
	}
	if _, err := ensureOperationIntent(
		m, scope, request.Operation, request, releaseCheckpointRetentionJournalSpec(), nil,
	); err != nil {
		return CheckpointRetention{}, err
	}
	fail := func(code ErrorCode) (CheckpointRetention, error) {
		err := &Error{Code: code}
		recordOperation(m.operations, scope, request.Operation, CheckpointRetention{}, err)
		return CheckpointRetention{}, err
	}
	workspace, workspaceOK := m.workspaces[request.TaskID]
	checkpoint, checkpointOK := m.checkpoints[request.CheckpointID]
	if !workspaceOK || workspace.policyDomainID != request.PolicyDomainID ||
		workspace.taskWorkspaceID != request.TaskWorkspaceID || !checkpointOK ||
		checkpoint.taskWorkspaceID != request.TaskWorkspaceID {
		return fail(ErrorOwnershipDenied)
	}
	if workspace.generation != request.Generation || workspace.fence != request.Fence ||
		checkpoint.retention.generation != request.ExpectedRetentionGeneration {
		return fail(ErrorStaleAuthority)
	}
	authority, ok := checkpoint.retention.authorities[request.AuthorityID]
	if !ok {
		return fail(ErrorStaleAuthority)
	}
	if authority.Kind == CheckpointRecoveryLineageAuthority && workspace.currentCheckpointID == request.CheckpointID {
		return fail(ErrorStaleAuthority)
	}
	finalRelease := !checkpointRetainedWithoutAuthority(checkpoint.retention, request.AuthorityID, m.now())
	if finalRelease {
		if m.checkpointReclamation == nil {
			return fail(ErrorIntegrityFailure)
		}
		resources, exactGenerationRoot, trusted := checkpointContentGenerations(checkpoint)
		if !trusted {
			return fail(ErrorIntegrityFailure)
		}
		markOperationReconciliationRequired(m.operations, scope)
		evidence, err := m.checkpointReclamation.ReleaseCheckpointReferences(
			ctx,
			checkpointContentReferenceTransitionRequest(
				request.PolicyDomainID,
				request.TaskID,
				request.TaskWorkspaceID,
				request.CheckpointID,
				checkpoint.revisionID,
				checkpoint.retention.generation+1,
				resources,
				exactGenerationRoot,
				request.Generation,
				request.Fence,
				request.Operation,
			),
		)
		if err != nil {
			if errors.Is(err, ErrDurableObjectResultAmbiguous) {
				return CheckpointRetention{}, &Error{Code: ErrorReconciliationRequired}
			}
			return fail(ErrorIntegrityFailure)
		}
		if !checkpointContentReferenceTransitionEvidenceMatches(
			evidence,
			request,
			checkpoint.retention.generation+1,
			exactGenerationRoot,
			CheckpointContentReferencesReleased,
		) {
			return fail(ErrorIntegrityFailure)
		}
	}
	delete(checkpoint.retention.authorities, request.AuthorityID)
	checkpoint.retention.generation++
	if finalRelease {
		checkpoint.retention.policyID = m.checkpointRetentionPolicy.ID
		checkpoint.retention.eligibleAt = m.now() + Instant(m.checkpointRetentionPolicy.ReclamationGrace)
		m.releaseCheckpointContentReferences(checkpoint)
	}
	m.checkpoints[request.CheckpointID] = checkpoint
	result := checkpointRetentionSnapshot(request.CheckpointID, checkpoint, workspace, m.now(), request.Operation)
	recordOperation(m.operations, scope, request.Operation, result, nil)
	return deliverOperationResponse(m, request.Operation.ID, result)
}

func (m *inMemory) AttachCheckpointRetention(
	ctx context.Context,
	request AttachCheckpointRetentionRequest,
) (CheckpointRetention, error) {
	if request.PolicyDomainID == "" || request.TaskID == "" || request.TaskWorkspaceID == "" ||
		request.CheckpointID == "" || request.ExpectedRetentionGeneration == 0 || request.Generation == 0 ||
		request.Fence == 0 || request.Operation.ID == "" || !validCheckpointRetentionAuthority(request.Authority, m.now()) ||
		request.Operation.RequestDigest != request.CanonicalRequestDigest() {
		return CheckpointRetention{}, &Error{Code: ErrorInvalidIntent}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	scope := operationScope{request.PolicyDomainID, request.TaskID, request.Operation.ID}
	if result, replayed, err := replayOperation[CheckpointRetention](m.operations, scope, request.Operation); replayed {
		return result, err
	}
	if _, err := ensureOperationIntent(
		m, scope, request.Operation, request, attachCheckpointRetentionJournalSpec(), nil,
	); err != nil {
		return CheckpointRetention{}, err
	}
	fail := func(code ErrorCode) (CheckpointRetention, error) {
		err := &Error{Code: code}
		recordOperation(m.operations, scope, request.Operation, CheckpointRetention{}, err)
		return CheckpointRetention{}, err
	}
	workspace, workspaceOK := m.workspaces[request.TaskID]
	checkpoint, checkpointOK := m.checkpoints[request.CheckpointID]
	if !workspaceOK || workspace.policyDomainID != request.PolicyDomainID ||
		workspace.taskWorkspaceID != request.TaskWorkspaceID || !checkpointOK ||
		checkpoint.taskWorkspaceID != request.TaskWorkspaceID {
		return fail(ErrorOwnershipDenied)
	}
	if workspace.generation != request.Generation || workspace.fence != request.Fence ||
		checkpoint.retention.generation != request.ExpectedRetentionGeneration {
		return fail(ErrorStaleAuthority)
	}
	if checkpoint.retention.reclaimed {
		return fail(ErrorCheckpointNotRetained)
	}
	wasRetained := checkpoint.retention.semanticallyRetained(m.now())
	if existing, ok := checkpoint.retention.authorities[request.Authority.ID]; ok {
		if existing != request.Authority {
			return fail(ErrorIntegrityConflict)
		}
	} else {
		if !wasRetained {
			if m.checkpointReclamation == nil {
				return fail(ErrorIntegrityFailure)
			}
			resources, exactGenerationRoot, trusted := checkpointContentGenerations(checkpoint)
			if !trusted {
				return fail(ErrorIntegrityFailure)
			}
			markOperationReconciliationRequired(m.operations, scope)
			evidence, err := m.checkpointReclamation.AttachCheckpointReferences(
				ctx,
				checkpointContentReferenceTransitionRequest(
					request.PolicyDomainID,
					request.TaskID,
					request.TaskWorkspaceID,
					request.CheckpointID,
					checkpoint.revisionID,
					checkpoint.retention.generation+1,
					resources,
					exactGenerationRoot,
					request.Generation,
					request.Fence,
					request.Operation,
				),
			)
			if err != nil {
				if errors.Is(err, ErrDurableObjectResultAmbiguous) {
					return CheckpointRetention{}, &Error{Code: ErrorReconciliationRequired}
				}
				return fail(ErrorIntegrityFailure)
			}
			if !checkpointContentReferenceTransitionEvidenceMatches(
				evidence,
				ReleaseCheckpointRetentionRequest{
					PolicyDomainID:  request.PolicyDomainID,
					TaskID:          request.TaskID,
					TaskWorkspaceID: request.TaskWorkspaceID,
					CheckpointID:    request.CheckpointID,
					Generation:      request.Generation,
					Fence:           request.Fence,
					Operation:       request.Operation,
				},
				checkpoint.retention.generation+1,
				exactGenerationRoot,
				CheckpointContentReferencesAttached,
			) {
				return fail(ErrorIntegrityFailure)
			}
		}
		checkpoint.retention.authorities[request.Authority.ID] = request.Authority
		checkpoint.retention.generation++
	}
	checkpoint.retention.policyID = ""
	checkpoint.retention.eligibleAt = 0
	m.checkpoints[request.CheckpointID] = checkpoint
	m.recordDurableEvidenceIdentities(verifiedCheckpointContent(checkpoint.evidence))
	result := checkpointRetentionSnapshot(request.CheckpointID, checkpoint, workspace, m.now(), request.Operation)
	recordOperation(m.operations, scope, request.Operation, result, nil)
	return deliverOperationResponse(m, request.Operation.ID, result)
}

func checkpointRetainedWithoutAuthority(
	retention checkpointRetentionRecord,
	authorityID CheckpointRetentionAuthorityID,
	now Instant,
) bool {
	for id, authority := range retention.authorities {
		if id != authorityID && checkpointRetentionAuthorityIsActive(authority, now) {
			return true
		}
	}
	return false
}

func validCheckpointRetentionAuthority(authority CheckpointRetentionAuthority, now Instant) bool {
	if authority.ID == "" {
		return false
	}
	switch authority.Kind {
	case CheckpointExplicitReferenceAuthority, CheckpointIntegrityIncidentAuthority, CheckpointRecoveryPointPinAuthority:
		return authority.ExpiresAt == 0
	case CheckpointCommitLeaseAuthority, CheckpointRestoreLeaseAuthority:
		return authority.ExpiresAt > now
	default:
		return false
	}
}

func checkpointRetentionAuthorityIsActive(authority CheckpointRetentionAuthority, now Instant) bool {
	switch authority.Kind {
	case CheckpointCommitLeaseAuthority, CheckpointRestoreLeaseAuthority:
		return authority.ExpiresAt > now
	default:
		return true
	}
}

func (m *inMemory) releaseCheckpointContentReferences(checkpoint checkpointRecord) {
	delete(m.contentReferences, checkpoint.evidence.ManifestReference.ID)
	for _, reference := range checkpoint.evidence.ContentReferences {
		delete(m.contentReferences, reference.ID)
	}
}

func checkpointRetentionSnapshot(
	checkpointID CheckpointID,
	checkpoint checkpointRecord,
	workspace workspaceBinding,
	now Instant,
	operation Operation,
) CheckpointRetention {
	authorities := make([]CheckpointRetentionAuthority, 0, len(checkpoint.retention.authorities))
	for id, authority := range checkpoint.retention.authorities {
		if !checkpointRetentionAuthorityIsActive(authority, now) {
			continue
		}
		authority.ID = id
		authorities = append(authorities, authority)
	}
	sort.Slice(authorities, func(i, j int) bool {
		return authorities[i].ID < authorities[j].ID
	})
	decision := CheckpointRetained
	if checkpoint.retention.reclaimed {
		decision = CheckpointPhysicallyReclaimed
	} else if len(authorities) == 0 {
		decision = CheckpointPendingReclaim
	}
	return CheckpointRetention{
		TaskWorkspaceID:     checkpoint.taskWorkspaceID,
		CheckpointID:        checkpointID,
		RevisionID:          checkpoint.revisionID,
		Decision:            decision,
		RetentionGeneration: checkpoint.retention.generation,
		Generation:          workspace.generation,
		Fence:               workspace.fence,
		PolicyID:            checkpoint.retention.policyID,
		EligibleAt:          checkpoint.retention.eligibleAt,
		Authorities:         authorities,
		Operation:           operation,
	}
}
