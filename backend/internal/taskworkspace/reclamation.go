package taskworkspace

import (
	"context"
	"errors"
	"sort"
)

type (
	CheckpointReclamationOutcome       string
	CheckpointReclamationBlocker       string
	CheckpointMechanicsState           string
	CheckpointInventoryState           string
	CheckpointInventoryObservationKind string
	InventoryResourceID                string
	CheckpointContentReferenceState    string
)

const (
	CheckpointReclaimed           CheckpointReclamationOutcome = "Reclaimed"
	CheckpointAlreadyAbsent       CheckpointReclamationOutcome = "AlreadyAbsent"
	CheckpointRetainedByAuthority CheckpointReclamationOutcome = "RetainedByAuthority"

	CheckpointGraceBlocker             CheckpointReclamationBlocker = "grace"
	CheckpointRecoveryLineageBlocker   CheckpointReclamationBlocker = "current_recovery_lineage"
	CheckpointExplicitReferenceBlocker CheckpointReclamationBlocker = "explicit_reference"
	CheckpointCommitLeaseBlocker       CheckpointReclamationBlocker = "commit_lease"
	CheckpointRestoreLeaseBlocker      CheckpointReclamationBlocker = "restore_lease"
	CheckpointIntegrityIncidentBlocker CheckpointReclamationBlocker = "integrity_incident"
	CheckpointRecoveryPointPinBlocker  CheckpointReclamationBlocker = "recovery_point_pin"
	CheckpointUnknownStateBlocker      CheckpointReclamationBlocker = "unknown_state"
	CheckpointDurableReferenceBlocker  CheckpointReclamationBlocker = "durable_reference"
	CheckpointDurableLeaseBlocker      CheckpointReclamationBlocker = "durable_lease"
	CheckpointQuarantineBlocker        CheckpointReclamationBlocker = "quarantine"
	CheckpointUnknownInventoryBlocker  CheckpointReclamationBlocker = "unknown_inventory"

	CheckpointMechanicsClear   CheckpointMechanicsState = "clear"
	CheckpointMechanicsBlocked CheckpointMechanicsState = "blocked"
	CheckpointMechanicsUnknown CheckpointMechanicsState = "unknown"

	CheckpointInventoryPresent CheckpointInventoryState = "present"
	CheckpointInventoryAbsent  CheckpointInventoryState = "absent"
	CheckpointInventoryUnknown CheckpointInventoryState = "unknown"

	CheckpointOrphanCandidate             CheckpointInventoryObservationKind = "OrphanCandidate"
	CheckpointInventoryUnknownObservation CheckpointInventoryObservationKind = "Unknown"
	CheckpointInventoryNoCandidate        CheckpointInventoryObservationKind = "NoCandidate"

	CheckpointContentReferencesAttached CheckpointContentReferenceState = "attached"
	CheckpointContentReferencesReleased CheckpointContentReferenceState = "released"
)

// CheckpointReclamationPort performs only opaque content-reference, lease,
// quarantine, inventory, and exact physical-generation mechanics. C04 selects
// semantic retention and supplies the complete exact-generation intent.
type CheckpointReclamationPort interface {
	AttachCheckpointReferences(context.Context, CheckpointContentReferenceTransitionRequest) (CheckpointContentReferenceTransitionEvidence, error)
	ReleaseCheckpointReferences(context.Context, CheckpointContentReferenceTransitionRequest) (CheckpointContentReferenceTransitionEvidence, error)
	ReclaimCheckpointContent(context.Context, ReclaimCheckpointContentRequest) (CheckpointContentReclamationEvidence, error)
	ObserveCheckpointInventory(context.Context, ObserveCheckpointContentInventoryRequest) (CheckpointContentInventoryEvidence, error)
}

type CheckpointContentReferenceTransitionRequest struct {
	PolicyDomainID      PolicyDomainID
	TaskID              TaskID
	TaskWorkspaceID     TaskWorkspaceID
	CheckpointID        CheckpointID
	RevisionID          RevisionID
	RetentionGeneration RetentionGeneration
	Resources           []CheckpointContentGeneration
	ExactGenerationRoot Digest
	Generation          Generation
	Fence               Fence
	Operation           Operation
}

type CheckpointContentReferenceTransitionEvidence struct {
	ID                  EvidenceID
	Digest              Digest
	PolicyDomainID      PolicyDomainID
	TaskID              TaskID
	TaskWorkspaceID     TaskWorkspaceID
	CheckpointID        CheckpointID
	RetentionGeneration RetentionGeneration
	ExactGenerationRoot Digest
	State               CheckpointContentReferenceState
	Generation          Generation
	Fence               Fence
	OperationID         OperationID
	ObservedAt          Instant
}

func checkpointContentReferenceTransitionRequest(
	policyDomainID PolicyDomainID,
	taskID TaskID,
	taskWorkspaceID TaskWorkspaceID,
	checkpointID CheckpointID,
	revisionID RevisionID,
	retentionGeneration RetentionGeneration,
	resources []CheckpointContentGeneration,
	exactGenerationRoot Digest,
	generation Generation,
	fence Fence,
	operation Operation,
) CheckpointContentReferenceTransitionRequest {
	return CheckpointContentReferenceTransitionRequest{
		PolicyDomainID:      policyDomainID,
		TaskID:              taskID,
		TaskWorkspaceID:     taskWorkspaceID,
		CheckpointID:        checkpointID,
		RevisionID:          revisionID,
		RetentionGeneration: retentionGeneration,
		Resources:           append([]CheckpointContentGeneration(nil), resources...),
		ExactGenerationRoot: exactGenerationRoot,
		Generation:          generation,
		Fence:               fence,
		Operation:           operation,
	}
}

func checkpointContentReferenceTransitionEvidenceMatches(
	evidence CheckpointContentReferenceTransitionEvidence,
	request ReleaseCheckpointRetentionRequest,
	retentionGeneration RetentionGeneration,
	exactGenerationRoot Digest,
	state CheckpointContentReferenceState,
) bool {
	return evidence.ID != "" && evidence.ObservedAt != 0 && validDigest(evidence.Digest) &&
		evidence.Digest == evidence.CanonicalDigest() && evidence.PolicyDomainID == request.PolicyDomainID &&
		evidence.TaskID == request.TaskID && evidence.TaskWorkspaceID == request.TaskWorkspaceID &&
		evidence.CheckpointID == request.CheckpointID && evidence.RetentionGeneration == retentionGeneration &&
		evidence.ExactGenerationRoot == exactGenerationRoot && evidence.State == state &&
		evidence.Generation == request.Generation && evidence.Fence == request.Fence &&
		evidence.OperationID == request.Operation.ID
}

func (e CheckpointContentReferenceTransitionEvidence) CanonicalDigest() Digest {
	return canonicalDigest(struct {
		ID                  EvidenceID
		PolicyDomainID      PolicyDomainID
		TaskID              TaskID
		TaskWorkspaceID     TaskWorkspaceID
		CheckpointID        CheckpointID
		RetentionGeneration RetentionGeneration
		ExactGenerationRoot Digest
		State               CheckpointContentReferenceState
		Generation          Generation
		Fence               Fence
		OperationID         OperationID
		ObservedAt          Instant
	}{
		ID:                  e.ID,
		PolicyDomainID:      e.PolicyDomainID,
		TaskID:              e.TaskID,
		TaskWorkspaceID:     e.TaskWorkspaceID,
		CheckpointID:        e.CheckpointID,
		RetentionGeneration: e.RetentionGeneration,
		ExactGenerationRoot: e.ExactGenerationRoot,
		State:               e.State,
		Generation:          e.Generation,
		Fence:               e.Fence,
		OperationID:         e.OperationID,
		ObservedAt:          e.ObservedAt,
	})
}

type ObserveCheckpointContentInventoryRequest struct {
	PolicyDomainID  PolicyDomainID
	TaskID          TaskID
	TaskWorkspaceID TaskWorkspaceID
	Operation       Operation
}

type CheckpointContentInventoryEvidence struct {
	ID              EvidenceID
	Digest          Digest
	PolicyDomainID  PolicyDomainID
	TaskID          TaskID
	TaskWorkspaceID TaskWorkspaceID
	ResourceID      InventoryResourceID
	GenerationID    DurabilityGenerationID
	State           CheckpointInventoryState
	OperationID     OperationID
	ObservedAt      Instant
}

func (e CheckpointContentInventoryEvidence) CanonicalDigest() Digest {
	return canonicalDigest(struct {
		ID              EvidenceID
		PolicyDomainID  PolicyDomainID
		TaskID          TaskID
		TaskWorkspaceID TaskWorkspaceID
		ResourceID      InventoryResourceID
		GenerationID    DurabilityGenerationID
		State           CheckpointInventoryState
		OperationID     OperationID
		ObservedAt      Instant
	}{
		ID:              e.ID,
		PolicyDomainID:  e.PolicyDomainID,
		TaskID:          e.TaskID,
		TaskWorkspaceID: e.TaskWorkspaceID,
		ResourceID:      e.ResourceID,
		GenerationID:    e.GenerationID,
		State:           e.State,
		OperationID:     e.OperationID,
		ObservedAt:      e.ObservedAt,
	})
}

type ObserveCheckpointInventoryRequest struct {
	PolicyDomainID  PolicyDomainID
	TaskID          TaskID
	TaskWorkspaceID TaskWorkspaceID
	Operation       Operation
}

func (r ObserveCheckpointInventoryRequest) CanonicalRequestDigest() Digest {
	return canonicalDigest(struct {
		Kind            string
		PolicyDomainID  PolicyDomainID
		TaskID          TaskID
		TaskWorkspaceID TaskWorkspaceID
		OperationID     OperationID
	}{
		Kind:            "observe_checkpoint_inventory",
		PolicyDomainID:  r.PolicyDomainID,
		TaskID:          r.TaskID,
		TaskWorkspaceID: r.TaskWorkspaceID,
		OperationID:     r.Operation.ID,
	})
}

type CheckpointInventoryObservation struct {
	Kind           CheckpointInventoryObservationKind
	State          CheckpointInventoryState
	ResourceID     InventoryResourceID
	GenerationID   DurabilityGenerationID
	EvidenceDigest Digest
	ObservedAt     Instant
	Operation      Operation
}

type CheckpointContentGeneration struct {
	ContentID    ContentID
	ReferenceID  ContentReferenceID
	ReceiptID    DurabilityReceiptID
	GenerationID DurabilityGenerationID
}

type ReclaimCheckpointContentRequest struct {
	PolicyDomainID      PolicyDomainID
	TaskID              TaskID
	TaskWorkspaceID     TaskWorkspaceID
	CheckpointID        CheckpointID
	RevisionID          RevisionID
	RetentionGeneration RetentionGeneration
	Resources           []CheckpointContentGeneration
	ExactGenerationRoot Digest
	Generation          Generation
	Fence               Fence
	Operation           Operation
}

type CheckpointContentReclamationEvidence struct {
	ID                  EvidenceID
	Digest              Digest
	PolicyDomainID      PolicyDomainID
	TaskID              TaskID
	TaskWorkspaceID     TaskWorkspaceID
	CheckpointID        CheckpointID
	RetentionGeneration RetentionGeneration
	ExactGenerationRoot Digest
	ReferenceState      CheckpointMechanicsState
	LeaseState          CheckpointMechanicsState
	QuarantineState     CheckpointMechanicsState
	InventoryState      CheckpointInventoryState
	Outcome             CheckpointReclamationOutcome
	Generation          Generation
	Fence               Fence
	OperationID         OperationID
	ObservedAt          Instant
}

func (e CheckpointContentReclamationEvidence) CanonicalDigest() Digest {
	return canonicalDigest(struct {
		ID                  EvidenceID
		PolicyDomainID      PolicyDomainID
		TaskID              TaskID
		TaskWorkspaceID     TaskWorkspaceID
		CheckpointID        CheckpointID
		RetentionGeneration RetentionGeneration
		ExactGenerationRoot Digest
		ReferenceState      CheckpointMechanicsState
		LeaseState          CheckpointMechanicsState
		QuarantineState     CheckpointMechanicsState
		InventoryState      CheckpointInventoryState
		Outcome             CheckpointReclamationOutcome
		Generation          Generation
		Fence               Fence
		OperationID         OperationID
		ObservedAt          Instant
	}{
		ID:                  e.ID,
		PolicyDomainID:      e.PolicyDomainID,
		TaskID:              e.TaskID,
		TaskWorkspaceID:     e.TaskWorkspaceID,
		CheckpointID:        e.CheckpointID,
		RetentionGeneration: e.RetentionGeneration,
		ExactGenerationRoot: e.ExactGenerationRoot,
		ReferenceState:      e.ReferenceState,
		LeaseState:          e.LeaseState,
		QuarantineState:     e.QuarantineState,
		InventoryState:      e.InventoryState,
		Outcome:             e.Outcome,
		Generation:          e.Generation,
		Fence:               e.Fence,
		OperationID:         e.OperationID,
		ObservedAt:          e.ObservedAt,
	})
}

type ReclaimCheckpointRequest struct {
	PolicyDomainID              PolicyDomainID
	TaskID                      TaskID
	TaskWorkspaceID             TaskWorkspaceID
	CheckpointID                CheckpointID
	ExpectedRetentionGeneration RetentionGeneration
	Generation                  Generation
	Fence                       Fence
	Operation                   Operation
}

func (r ReclaimCheckpointRequest) CanonicalRequestDigest() Digest {
	return canonicalDigest(struct {
		Kind                        string
		PolicyDomainID              PolicyDomainID
		TaskID                      TaskID
		TaskWorkspaceID             TaskWorkspaceID
		CheckpointID                CheckpointID
		ExpectedRetentionGeneration RetentionGeneration
		Generation                  Generation
		Fence                       Fence
		OperationID                 OperationID
	}{
		Kind:                        "reclaim_checkpoint",
		PolicyDomainID:              r.PolicyDomainID,
		TaskID:                      r.TaskID,
		TaskWorkspaceID:             r.TaskWorkspaceID,
		CheckpointID:                r.CheckpointID,
		ExpectedRetentionGeneration: r.ExpectedRetentionGeneration,
		Generation:                  r.Generation,
		Fence:                       r.Fence,
		OperationID:                 r.Operation.ID,
	})
}

type CheckpointReclamationEvidence struct {
	ID                      EvidenceID
	Digest                  Digest
	PolicyDomainID          PolicyDomainID
	TaskID                  TaskID
	TaskWorkspaceID         TaskWorkspaceID
	CheckpointID            CheckpointID
	RevisionID              RevisionID
	RetentionGeneration     RetentionGeneration
	ExactGenerationRoot     Digest
	AuthorityRoot           Digest
	MechanicsEvidenceDigest Digest
	PriorEvidenceDigest     Digest
	Blockers                []CheckpointReclamationBlocker
	Outcome                 CheckpointReclamationOutcome
	Generation              Generation
	Fence                   Fence
	OperationID             OperationID
	ObservedAt              Instant
}

func (e CheckpointReclamationEvidence) CanonicalDigest() Digest {
	return canonicalDigest(struct {
		ID                      EvidenceID
		PolicyDomainID          PolicyDomainID
		TaskID                  TaskID
		TaskWorkspaceID         TaskWorkspaceID
		CheckpointID            CheckpointID
		RevisionID              RevisionID
		RetentionGeneration     RetentionGeneration
		ExactGenerationRoot     Digest
		AuthorityRoot           Digest
		MechanicsEvidenceDigest Digest
		PriorEvidenceDigest     Digest
		Blockers                []CheckpointReclamationBlocker
		Outcome                 CheckpointReclamationOutcome
		Generation              Generation
		Fence                   Fence
		OperationID             OperationID
		ObservedAt              Instant
	}{
		ID:                      e.ID,
		PolicyDomainID:          e.PolicyDomainID,
		TaskID:                  e.TaskID,
		TaskWorkspaceID:         e.TaskWorkspaceID,
		CheckpointID:            e.CheckpointID,
		RevisionID:              e.RevisionID,
		RetentionGeneration:     e.RetentionGeneration,
		ExactGenerationRoot:     e.ExactGenerationRoot,
		AuthorityRoot:           e.AuthorityRoot,
		MechanicsEvidenceDigest: e.MechanicsEvidenceDigest,
		PriorEvidenceDigest:     e.PriorEvidenceDigest,
		Blockers:                append([]CheckpointReclamationBlocker(nil), e.Blockers...),
		Outcome:                 e.Outcome,
		Generation:              e.Generation,
		Fence:                   e.Fence,
		OperationID:             e.OperationID,
		ObservedAt:              e.ObservedAt,
	})
}

type CheckpointReclamation struct {
	TaskWorkspaceID     TaskWorkspaceID
	CheckpointID        CheckpointID
	RevisionID          RevisionID
	Outcome             CheckpointReclamationOutcome
	RetentionGeneration RetentionGeneration
	Generation          Generation
	Fence               Fence
	Evidence            CheckpointReclamationEvidence
	Operation           Operation
}

func (m *inMemory) ObserveCheckpointInventory(
	ctx context.Context,
	request ObserveCheckpointInventoryRequest,
) (CheckpointInventoryObservation, error) {
	if request.PolicyDomainID == "" || request.TaskID == "" || request.TaskWorkspaceID == "" ||
		request.Operation.ID == "" || request.Operation.RequestDigest != request.CanonicalRequestDigest() {
		return CheckpointInventoryObservation{}, &Error{Code: ErrorInvalidIntent}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	workspace, ok := m.workspaces[request.TaskID]
	if !ok || workspace.policyDomainID != request.PolicyDomainID ||
		workspace.taskWorkspaceID != request.TaskWorkspaceID {
		return CheckpointInventoryObservation{}, &Error{Code: ErrorOwnershipDenied}
	}
	if m.checkpointReclamation == nil {
		return CheckpointInventoryObservation{}, &Error{Code: ErrorIntegrityFailure}
	}
	evidence, err := m.checkpointReclamation.ObserveCheckpointInventory(
		ctx,
		ObserveCheckpointContentInventoryRequest{
			PolicyDomainID:  request.PolicyDomainID,
			TaskID:          request.TaskID,
			TaskWorkspaceID: request.TaskWorkspaceID,
			Operation:       request.Operation,
		},
	)
	if err != nil || evidence.ID == "" || evidence.ResourceID == "" || evidence.GenerationID == "" ||
		evidence.ObservedAt == 0 || !validDigest(evidence.Digest) || evidence.Digest != evidence.CanonicalDigest() ||
		evidence.PolicyDomainID != request.PolicyDomainID || evidence.TaskID != request.TaskID ||
		evidence.TaskWorkspaceID != request.TaskWorkspaceID || evidence.OperationID != request.Operation.ID ||
		!validCheckpointInventoryState(evidence.State) {
		return CheckpointInventoryObservation{}, &Error{Code: ErrorIntegrityFailure}
	}
	kind := CheckpointInventoryNoCandidate
	switch evidence.State {
	case CheckpointInventoryPresent:
		kind = CheckpointOrphanCandidate
	case CheckpointInventoryUnknown:
		kind = CheckpointInventoryUnknownObservation
	}
	return CheckpointInventoryObservation{
		Kind:           kind,
		State:          evidence.State,
		ResourceID:     evidence.ResourceID,
		GenerationID:   evidence.GenerationID,
		EvidenceDigest: evidence.Digest,
		ObservedAt:     evidence.ObservedAt,
		Operation:      request.Operation,
	}, nil
}

func (m *inMemory) ReclaimCheckpoint(
	ctx context.Context,
	request ReclaimCheckpointRequest,
) (CheckpointReclamation, error) {
	if request.PolicyDomainID == "" || request.TaskID == "" || request.TaskWorkspaceID == "" ||
		request.CheckpointID == "" || request.ExpectedRetentionGeneration == 0 || request.Generation == 0 ||
		request.Fence == 0 || request.Operation.ID == "" ||
		request.Operation.RequestDigest != request.CanonicalRequestDigest() {
		return CheckpointReclamation{}, &Error{Code: ErrorInvalidIntent}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	scope := operationScope{request.PolicyDomainID, request.TaskID, request.Operation.ID}
	if result, replayed, err := replayOperation[CheckpointReclamation](m.operations, scope, request.Operation); replayed {
		return result, err
	}
	if _, err := ensureOperationIntent(
		m, scope, request.Operation, request, reclaimCheckpointJournalSpec(), nil,
	); err != nil {
		return CheckpointReclamation{}, err
	}
	fail := func(code ErrorCode) (CheckpointReclamation, error) {
		err := &Error{Code: code}
		recordOperation(m.operations, scope, request.Operation, CheckpointReclamation{}, err)
		return CheckpointReclamation{}, err
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
	resources, exactGenerationRoot, ok := checkpointContentGenerations(checkpoint)
	if !ok {
		return fail(ErrorIntegrityFailure)
	}
	if checkpoint.retention.reclaimed {
		result := m.checkpointReclamationResult(
			scope, request, checkpoint, exactGenerationRoot, CheckpointAlreadyAbsent,
			nil, "", checkpoint.retention.reclamationEvidence.Digest,
		)
		recordOperation(m.operations, scope, request.Operation, result, nil)
		return deliverOperationResponse(m, request.Operation.ID, result)
	}

	blockers := checkpointRetentionBlockers(checkpoint.retention, m.now())
	if len(blockers) == 0 && (checkpoint.retention.eligibleAt == 0 || m.now() < checkpoint.retention.eligibleAt) {
		blockers = append(blockers, CheckpointGraceBlocker)
	}
	if len(blockers) > 0 {
		result := m.checkpointReclamationResult(
			scope, request, checkpoint, exactGenerationRoot, CheckpointRetainedByAuthority,
			blockers, "", "",
		)
		recordOperation(m.operations, scope, request.Operation, result, nil)
		return deliverOperationResponse(m, request.Operation.ID, result)
	}
	if m.checkpointReclamation == nil {
		result := m.checkpointReclamationResult(
			scope, request, checkpoint, exactGenerationRoot, CheckpointRetainedByAuthority,
			[]CheckpointReclamationBlocker{CheckpointUnknownStateBlocker}, "", "",
		)
		recordOperation(m.operations, scope, request.Operation, result, nil)
		return deliverOperationResponse(m, request.Operation.ID, result)
	}

	markOperationReconciliationRequired(m.operations, scope)
	if err := m.injectFaultEvent(FaultEvent{
		Point:       FaultBeforeCheckpointReclaim,
		OperationID: request.Operation.ID,
		SubjectID:   string(request.CheckpointID),
	}); err != nil {
		return CheckpointReclamation{}, err
	}
	mechanics, err := m.checkpointReclamation.ReclaimCheckpointContent(ctx, ReclaimCheckpointContentRequest{
		PolicyDomainID:      request.PolicyDomainID,
		TaskID:              request.TaskID,
		TaskWorkspaceID:     request.TaskWorkspaceID,
		CheckpointID:        request.CheckpointID,
		RevisionID:          checkpoint.revisionID,
		RetentionGeneration: request.ExpectedRetentionGeneration,
		Resources:           append([]CheckpointContentGeneration(nil), resources...),
		ExactGenerationRoot: exactGenerationRoot,
		Generation:          request.Generation,
		Fence:               request.Fence,
		Operation:           request.Operation,
	})
	if err != nil {
		if errors.Is(err, ErrDurableObjectResultAmbiguous) {
			return CheckpointReclamation{}, &Error{Code: ErrorReconciliationRequired}
		}
		return fail(ErrorIntegrityFailure)
	}
	if err := m.injectFaultEvent(FaultEvent{
		Point:       FaultAfterCheckpointReclaim,
		OperationID: request.Operation.ID,
		SubjectID:   string(request.CheckpointID),
	}); err != nil {
		return CheckpointReclamation{}, err
	}
	mechanicsBlockers, trusted := validateCheckpointContentReclamationEvidence(request, exactGenerationRoot, mechanics)
	if !trusted {
		return fail(ErrorIntegrityFailure)
	}
	result := m.checkpointReclamationResult(
		scope, request, checkpoint, exactGenerationRoot, mechanics.Outcome,
		mechanicsBlockers, mechanics.Digest, "",
	)
	if result.Outcome == CheckpointReclaimed || result.Outcome == CheckpointAlreadyAbsent {
		checkpoint.retention.reclaimed = true
		checkpoint.retention.reclamationEvidence = result.Evidence
		m.checkpoints[request.CheckpointID] = checkpoint
	}
	recordOperation(m.operations, scope, request.Operation, result, nil)
	return deliverOperationResponse(m, request.Operation.ID, result)
}

func (m *inMemory) checkpointReclamationResult(
	scope operationScope,
	request ReclaimCheckpointRequest,
	checkpoint checkpointRecord,
	exactGenerationRoot Digest,
	outcome CheckpointReclamationOutcome,
	blockers []CheckpointReclamationBlocker,
	mechanicsEvidenceDigest Digest,
	priorEvidenceDigest Digest,
) CheckpointReclamation {
	sort.Slice(blockers, func(i, j int) bool { return blockers[i] < blockers[j] })
	evidence := CheckpointReclamationEvidence{
		ID:                      EvidenceID(m.operationOpaqueID(scope, "checkpoint-reclamation-evidence", "checkpoint-reclamation-evidence")),
		PolicyDomainID:          request.PolicyDomainID,
		TaskID:                  request.TaskID,
		TaskWorkspaceID:         request.TaskWorkspaceID,
		CheckpointID:            request.CheckpointID,
		RevisionID:              checkpoint.revisionID,
		RetentionGeneration:     request.ExpectedRetentionGeneration,
		ExactGenerationRoot:     exactGenerationRoot,
		AuthorityRoot:           canonicalDigest(blockers),
		MechanicsEvidenceDigest: mechanicsEvidenceDigest,
		PriorEvidenceDigest:     priorEvidenceDigest,
		Blockers:                append([]CheckpointReclamationBlocker(nil), blockers...),
		Outcome:                 outcome,
		Generation:              request.Generation,
		Fence:                   request.Fence,
		OperationID:             request.Operation.ID,
		ObservedAt:              m.now(),
	}
	evidence.Digest = evidence.CanonicalDigest()
	return CheckpointReclamation{
		TaskWorkspaceID:     request.TaskWorkspaceID,
		CheckpointID:        request.CheckpointID,
		RevisionID:          checkpoint.revisionID,
		Outcome:             outcome,
		RetentionGeneration: request.ExpectedRetentionGeneration,
		Generation:          request.Generation,
		Fence:               request.Fence,
		Evidence:            evidence,
		Operation:           request.Operation,
	}
}

func checkpointRetentionBlockers(
	retention checkpointRetentionRecord,
	now Instant,
) []CheckpointReclamationBlocker {
	blockers := make([]CheckpointReclamationBlocker, 0, len(retention.authorities))
	for _, authority := range retention.authorities {
		if !checkpointRetentionAuthorityIsActive(authority, now) {
			continue
		}
		switch authority.Kind {
		case CheckpointRecoveryLineageAuthority:
			blockers = append(blockers, CheckpointRecoveryLineageBlocker)
		case CheckpointExplicitReferenceAuthority:
			blockers = append(blockers, CheckpointExplicitReferenceBlocker)
		case CheckpointCommitLeaseAuthority:
			blockers = append(blockers, CheckpointCommitLeaseBlocker)
		case CheckpointRestoreLeaseAuthority:
			blockers = append(blockers, CheckpointRestoreLeaseBlocker)
		case CheckpointIntegrityIncidentAuthority:
			blockers = append(blockers, CheckpointIntegrityIncidentBlocker)
		case CheckpointRecoveryPointPinAuthority:
			blockers = append(blockers, CheckpointRecoveryPointPinBlocker)
		default:
			blockers = append(blockers, CheckpointUnknownStateBlocker)
		}
	}
	return blockers
}

func checkpointContentGenerations(
	checkpoint checkpointRecord,
) ([]CheckpointContentGeneration, Digest, bool) {
	references := canonicalContentReferences(
		checkpoint.evidence.ManifestReference,
		checkpoint.evidence.ContentReferences,
	)
	receipts := make(map[ContentID]DurabilityReceipt, len(checkpoint.evidence.DurabilityReceipts))
	for _, receipt := range checkpoint.evidence.DurabilityReceipts {
		if _, duplicate := receipts[receipt.ContentID]; duplicate {
			return nil, "", false
		}
		receipts[receipt.ContentID] = receipt
	}
	resources := make([]CheckpointContentGeneration, 0, len(references))
	for _, reference := range references {
		receipt, ok := receipts[reference.ContentID]
		if !ok || receipt.PolicyDomainID != reference.PolicyDomainID || receipt.ContentDigest != reference.ContentDigest ||
			receipt.Size != reference.Size || receipt.DurabilityGenerationID == "" {
			return nil, "", false
		}
		resources = append(resources, CheckpointContentGeneration{
			ContentID:    reference.ContentID,
			ReferenceID:  reference.ID,
			ReceiptID:    receipt.ID,
			GenerationID: receipt.DurabilityGenerationID,
		})
	}
	if len(resources) == 0 || len(resources) != len(receipts) {
		return nil, "", false
	}
	sort.Slice(resources, func(i, j int) bool {
		if resources[i].ContentID == resources[j].ContentID {
			return resources[i].ReferenceID < resources[j].ReferenceID
		}
		return resources[i].ContentID < resources[j].ContentID
	})
	return resources, canonicalDigest(resources), true
}

func validateCheckpointContentReclamationEvidence(
	request ReclaimCheckpointRequest,
	exactGenerationRoot Digest,
	evidence CheckpointContentReclamationEvidence,
) ([]CheckpointReclamationBlocker, bool) {
	if evidence.ID == "" || !validDigest(evidence.Digest) || evidence.Digest != evidence.CanonicalDigest() ||
		evidence.PolicyDomainID != request.PolicyDomainID || evidence.TaskID != request.TaskID ||
		evidence.TaskWorkspaceID != request.TaskWorkspaceID || evidence.CheckpointID != request.CheckpointID ||
		evidence.RetentionGeneration != request.ExpectedRetentionGeneration ||
		evidence.ExactGenerationRoot != exactGenerationRoot || evidence.Generation != request.Generation ||
		evidence.Fence != request.Fence || evidence.OperationID != request.Operation.ID || evidence.ObservedAt == 0 ||
		!validCheckpointMechanicsState(evidence.ReferenceState) || !validCheckpointMechanicsState(evidence.LeaseState) ||
		!validCheckpointMechanicsState(evidence.QuarantineState) || !validCheckpointInventoryState(evidence.InventoryState) {
		return nil, false
	}
	blockers := checkpointMechanicsBlockers(evidence)
	switch evidence.Outcome {
	case CheckpointReclaimed, CheckpointAlreadyAbsent:
		return blockers, len(blockers) == 0 && evidence.InventoryState == CheckpointInventoryAbsent
	case CheckpointRetainedByAuthority:
		return blockers, len(blockers) > 0
	default:
		return nil, false
	}
}

func checkpointMechanicsBlockers(evidence CheckpointContentReclamationEvidence) []CheckpointReclamationBlocker {
	var blockers []CheckpointReclamationBlocker
	if evidence.ReferenceState == CheckpointMechanicsBlocked {
		blockers = append(blockers, CheckpointDurableReferenceBlocker)
	} else if evidence.ReferenceState == CheckpointMechanicsUnknown {
		blockers = append(blockers, CheckpointUnknownStateBlocker)
	}
	if evidence.LeaseState == CheckpointMechanicsBlocked {
		blockers = append(blockers, CheckpointDurableLeaseBlocker)
	} else if evidence.LeaseState == CheckpointMechanicsUnknown {
		blockers = append(blockers, CheckpointUnknownStateBlocker)
	}
	if evidence.QuarantineState == CheckpointMechanicsBlocked {
		blockers = append(blockers, CheckpointQuarantineBlocker)
	} else if evidence.QuarantineState == CheckpointMechanicsUnknown {
		blockers = append(blockers, CheckpointUnknownStateBlocker)
	}
	if evidence.InventoryState == CheckpointInventoryUnknown {
		blockers = append(blockers, CheckpointUnknownInventoryBlocker)
	}
	return blockers
}

func validCheckpointMechanicsState(state CheckpointMechanicsState) bool {
	return state == CheckpointMechanicsClear || state == CheckpointMechanicsBlocked || state == CheckpointMechanicsUnknown
}

func validCheckpointInventoryState(state CheckpointInventoryState) bool {
	return state == CheckpointInventoryPresent || state == CheckpointInventoryAbsent || state == CheckpointInventoryUnknown
}
