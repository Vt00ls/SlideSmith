package runtimeexecution

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"time"
)

type postgresRuntimeState struct {
	OperationStatus          OperationBindingStatus        `json:"operation_status"`
	OperationID              string                        `json:"operation_id"`
	OperationDigest          Digest                        `json:"operation_digest"`
	OperationGeneration      OperationGeneration           `json:"operation_generation"`
	AdmissionGrantID         string                        `json:"admission_grant_id"`
	AdmissionWorkItemID      string                        `json:"admission_work_item_id"`
	AdmissionGrantGeneration AdmissionGrantGeneration      `json:"admission_grant_generation"`
	ExecutionNodeID          string                        `json:"execution_node_id"`
	NodeCapacityGeneration   uint64                        `json:"node_capacity_generation"`
	ResourceClassID          string                        `json:"resource_class_id"`
	ExecutionPolicyID        string                        `json:"execution_policy_id"`
	SchedulerEpoch           uint64                        `json:"scheduler_epoch"`
	PolicyVersion            uint64                        `json:"policy_version"`
	LeaseAcquireStatus       LeaseAcquireStatus            `json:"lease_acquire_status"`
	LeaseAcquireOperationID  string                        `json:"lease_acquire_operation_id"`
	LeaseAcquireDigest       Digest                        `json:"lease_acquire_digest"`
	SandboxLeaseID           string                        `json:"sandbox_lease_id"`
	LeaseGeneration          LeaseGeneration               `json:"lease_generation"`
	LeaseFence               LeaseFence                    `json:"lease_fence"`
	Deadline                 time.Time                     `json:"deadline"`
	LeaseAcquireBy           time.Time                     `json:"lease_acquire_by"`
	CancellationStatus       CancellationStatus            `json:"cancellation_status"`
	CancellationOperationID  string                        `json:"cancellation_operation_id"`
	CancellationReason       CancellationReason            `json:"cancellation_reason"`
	CancellationAcceptedAt   time.Time                     `json:"cancellation_accepted_at"`
	EvidenceSchemaVersion    SchemaVersion                 `json:"evidence_schema_version"`
	EvidenceRootID           string                        `json:"evidence_root_id"`
	EvidenceDigest           Digest                        `json:"evidence_digest"`
	LogicalCapacity          LogicalCapacityDisposition    `json:"logical_capacity"`
	NoLeaseCapacity          NoLeaseCapacityDisposition    `json:"no_lease_capacity"`
	PhysicalCapacity         PhysicalCapacityDisposition   `json:"physical_capacity"`
	CapacityEvidence         postgresCapacityEvidenceState `json:"capacity_evidence"`
	PreLeaseTerminalReason   PreLeaseTerminalReason        `json:"pre_lease_terminal_reason"`
	Reconciliation           ReconciliationStatus          `json:"reconciliation"`
}

type postgresRuntimeFencedEvidenceState struct {
	WorkItemID              string                   `json:"work_item_id"`
	AdmissionGrantID        string                   `json:"admission_grant_id"`
	GrantGeneration         AdmissionGrantGeneration `json:"grant_generation"`
	RuntimeRunID            string                   `json:"runtime_run_id"`
	StartOperationID        string                   `json:"start_operation_id"`
	StartDigest             Digest                   `json:"start_digest"`
	TerminalDecisionID      string                   `json:"terminal_decision_id"`
	RuntimeRevision         RuntimeRevision          `json:"runtime_revision"`
	RuntimeFence            RuntimeFence             `json:"runtime_fence"`
	SchedulerEpoch          uint64                   `json:"scheduler_epoch"`
	PolicyVersion           uint64                   `json:"policy_version"`
	LeaseAcquireOperationID string                   `json:"lease_acquire_operation_id"`
	LeaseAcquireDigest      Digest                   `json:"lease_acquire_digest"`
}

type postgresNoLeaseEvidenceState struct {
	postgresRuntimeFencedEvidenceState
	ExecutionNodeID        string `json:"execution_node_id"`
	NodeCapacityGeneration uint64 `json:"node_capacity_generation"`
}

type postgresPhysicalReleaseEvidenceState struct {
	RuntimeRunID           string          `json:"runtime_run_id"`
	SandboxLeaseID         string          `json:"sandbox_lease_id"`
	LeaseGeneration        LeaseGeneration `json:"lease_generation"`
	LeaseFence             LeaseFence      `json:"lease_fence"`
	ExecutionNodeID        string          `json:"execution_node_id"`
	NodeCapacityGeneration uint64          `json:"node_capacity_generation"`
}

type postgresCapacityEvidenceState struct {
	RuntimeFencedOrTerminal      postgresRuntimeFencedEvidenceState   `json:"runtime_fenced_or_terminal"`
	NoLeasePhysicalDisposition   postgresNoLeaseEvidenceState         `json:"no_lease_physical_disposition"`
	PhysicalCapacityReleaseReady postgresPhysicalReleaseEvidenceState `json:"physical_capacity_release_ready"`
}

type postgresDecisionState struct {
	DecisionID               string                    `json:"decision_id"`
	Disposition              DecisionDisposition       `json:"disposition"`
	Rejection                CommandRejection          `json:"rejection"`
	OperationID              string                    `json:"operation_id"`
	CanonicalRequestDigest   Digest                    `json:"canonical_request_digest"`
	PreviousRuntimeRevision  RuntimeRevision           `json:"previous_runtime_revision"`
	ResultingRuntimeRevision RuntimeRevision           `json:"resulting_runtime_revision"`
	StateAtDecision          RuntimeState              `json:"state_at_decision"`
	OutcomeAtDecision        RuntimeOutcome            `json:"outcome_at_decision"`
	TerminalEvidenceID       string                    `json:"terminal_evidence_id"`
	Retry                    RetryDisposition          `json:"retry"`
	Reconciliation           ReconciliationDisposition `json:"reconciliation"`
}

func encodePostgresDecisionFact(fact RuntimeDecisionFact) ([]byte, error) {
	return json.Marshal(postgresDecisionState{
		DecisionID: fact.DecisionID.String(), Disposition: fact.Disposition, Rejection: fact.Rejection,
		OperationID: fact.OperationID.String(), CanonicalRequestDigest: fact.CanonicalRequestDigest,
		PreviousRuntimeRevision: fact.PreviousRuntimeRevision, ResultingRuntimeRevision: fact.ResultingRuntimeRevision,
		StateAtDecision: fact.StateAtDecision, OutcomeAtDecision: fact.OutcomeAtDecision,
		TerminalEvidenceID: fact.TerminalEvidenceID.String(), Retry: fact.Retry, Reconciliation: fact.Reconciliation,
	})
}

func decodePostgresDecisionFact(encoded []byte) (RuntimeDecisionFact, error) {
	var persisted postgresDecisionState
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&persisted); err != nil {
		return RuntimeDecisionFact{}, newPersistenceError(PersistenceStateCorrupt)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return RuntimeDecisionFact{}, newPersistenceError(PersistenceStateCorrupt)
	}
	fact := RuntimeDecisionFact{
		DecisionID: RuntimeDecisionID{value: persisted.DecisionID}, Disposition: persisted.Disposition,
		Rejection: persisted.Rejection, OperationID: OperationID{value: persisted.OperationID},
		CanonicalRequestDigest:  persisted.CanonicalRequestDigest,
		PreviousRuntimeRevision: persisted.PreviousRuntimeRevision, ResultingRuntimeRevision: persisted.ResultingRuntimeRevision,
		StateAtDecision: persisted.StateAtDecision, OutcomeAtDecision: persisted.OutcomeAtDecision,
		TerminalEvidenceID: EvidenceID{value: persisted.TerminalEvidenceID}, Retry: persisted.Retry,
		Reconciliation: persisted.Reconciliation,
	}
	if !validOpaqueID(fact.DecisionID.String()) || !validOpaqueID(fact.OperationID.String()) ||
		fact.CanonicalRequestDigest == (Digest{}) || fact.Disposition < DecisionAccepted || fact.Disposition > DecisionRejected ||
		fact.PreviousRuntimeRevision == 0 || fact.ResultingRuntimeRevision == 0 ||
		!knownRuntimeState(fact.StateAtDecision) || !knownRuntimeOutcome(fact.OutcomeAtDecision) ||
		fact.Retry < RetryNever || fact.Retry > RetryAfterDependency ||
		fact.Reconciliation < ReconciliationNotRequired || fact.Reconciliation > ReconciliationRequired {
		return RuntimeDecisionFact{}, newPersistenceError(PersistenceStateCorrupt)
	}
	return fact, nil
}

func encodePostgresRuntimeFixture(fixture RuntimeFixture) ([]byte, error) {
	state := postgresRuntimeState{
		OperationStatus: fixture.Operation.Status, OperationID: fixture.Operation.OperationID.String(),
		OperationDigest: fixture.Operation.Digest, OperationGeneration: fixture.Operation.Generation,
		AdmissionGrantID: fixture.Operation.AdmissionGrantID.String(), AdmissionWorkItemID: fixture.Operation.WorkItemID.String(),
		AdmissionGrantGeneration: fixture.Operation.GrantGeneration,
		ExecutionNodeID:          fixture.Operation.ExecutionNodeID.String(), NodeCapacityGeneration: fixture.Operation.NodeCapacityGeneration,
		ResourceClassID: fixture.Operation.ResourceClassID.String(), ExecutionPolicyID: fixture.Operation.ExecutionPolicyID.String(),
		SchedulerEpoch: fixture.Operation.SchedulerEpoch, PolicyVersion: fixture.Operation.PolicyVersion,
		LeaseAcquireStatus: fixture.Lease.AcquireStatus, LeaseAcquireOperationID: fixture.Lease.AcquireOperationID.String(),
		LeaseAcquireDigest: fixture.Lease.AcquireDigest, SandboxLeaseID: fixture.Lease.LeaseID.String(),
		LeaseGeneration: fixture.Lease.Generation, LeaseFence: fixture.Lease.Fence,
		Deadline: fixture.Deadline.UTC(), LeaseAcquireBy: fixture.LeaseAcquireBy.UTC(),
		CancellationStatus: fixture.Cancellation.Status, CancellationOperationID: fixture.Cancellation.OperationID.String(),
		CancellationReason: fixture.Cancellation.Reason, CancellationAcceptedAt: fixture.Cancellation.AcceptedAt.UTC(),
		EvidenceSchemaVersion: fixture.EvidenceRoot.SchemaVersion, EvidenceRootID: fixture.EvidenceRoot.EvidenceRootID.String(),
		EvidenceDigest:  fixture.EvidenceRoot.Digest,
		LogicalCapacity: fixture.Capacity.LogicalRelease, NoLeaseCapacity: fixture.Capacity.NoLease,
		PhysicalCapacity: fixture.Capacity.Physical, CapacityEvidence: postgresCapacityEvidenceFromSnapshot(fixture.CapacityEvidence),
		PreLeaseTerminalReason: fixture.PreLeaseTerminalReason,
		Reconciliation:         fixture.Reconciliation,
	}
	return json.Marshal(state)
}

func postgresFencedEvidenceFromRuntime(value RuntimeFencedOrTerminalEvidence) postgresRuntimeFencedEvidenceState {
	return postgresRuntimeFencedEvidenceState{
		WorkItemID: value.WorkItemID.String(), AdmissionGrantID: value.AdmissionGrantID.String(),
		GrantGeneration: value.GrantGeneration, RuntimeRunID: value.RuntimeRunID.String(),
		StartOperationID: value.StartOperationID.String(), StartDigest: value.StartDigest,
		TerminalDecisionID: value.TerminalDecisionID.String(), RuntimeRevision: value.RuntimeRevision,
		RuntimeFence: value.RuntimeFence, SchedulerEpoch: value.SchedulerEpoch, PolicyVersion: value.PolicyVersion,
		LeaseAcquireOperationID: value.LeaseAcquireOperationID.String(), LeaseAcquireDigest: value.LeaseAcquireDigest,
	}
}

func postgresCapacityEvidenceFromSnapshot(value RuntimeCapacityEvidenceSnapshot) postgresCapacityEvidenceState {
	return postgresCapacityEvidenceState{
		RuntimeFencedOrTerminal: postgresFencedEvidenceFromRuntime(value.RuntimeFencedOrTerminal),
		NoLeasePhysicalDisposition: postgresNoLeaseEvidenceState{
			postgresRuntimeFencedEvidenceState: postgresFencedEvidenceFromRuntime(RuntimeFencedOrTerminalEvidence{
				WorkItemID:              value.NoLeasePhysicalDisposition.WorkItemID,
				AdmissionGrantID:        value.NoLeasePhysicalDisposition.AdmissionGrantID,
				GrantGeneration:         value.NoLeasePhysicalDisposition.GrantGeneration,
				RuntimeRunID:            value.NoLeasePhysicalDisposition.RuntimeRunID,
				StartOperationID:        value.NoLeasePhysicalDisposition.StartOperationID,
				StartDigest:             value.NoLeasePhysicalDisposition.StartDigest,
				TerminalDecisionID:      value.NoLeasePhysicalDisposition.TerminalDecisionID,
				RuntimeRevision:         value.NoLeasePhysicalDisposition.RuntimeRevision,
				RuntimeFence:            value.NoLeasePhysicalDisposition.RuntimeFence,
				SchedulerEpoch:          value.NoLeasePhysicalDisposition.SchedulerEpoch,
				PolicyVersion:           value.NoLeasePhysicalDisposition.PolicyVersion,
				LeaseAcquireOperationID: value.NoLeasePhysicalDisposition.LeaseAcquireOperationID,
				LeaseAcquireDigest:      value.NoLeasePhysicalDisposition.LeaseAcquireDigest,
			}),
			ExecutionNodeID:        value.NoLeasePhysicalDisposition.ExecutionNodeID.String(),
			NodeCapacityGeneration: value.NoLeasePhysicalDisposition.NodeCapacityGeneration,
		},
		PhysicalCapacityReleaseReady: postgresPhysicalReleaseEvidenceState{
			RuntimeRunID:           value.PhysicalCapacityReleaseReady.RuntimeRunID.String(),
			SandboxLeaseID:         value.PhysicalCapacityReleaseReady.SandboxLeaseID.String(),
			LeaseGeneration:        value.PhysicalCapacityReleaseReady.LeaseGeneration,
			LeaseFence:             value.PhysicalCapacityReleaseReady.LeaseFence,
			ExecutionNodeID:        value.PhysicalCapacityReleaseReady.ExecutionNodeID.String(),
			NodeCapacityGeneration: value.PhysicalCapacityReleaseReady.NodeCapacityGeneration,
		},
	}
}

func runtimeFencedEvidenceFromPostgres(value postgresRuntimeFencedEvidenceState) RuntimeFencedOrTerminalEvidence {
	return RuntimeFencedOrTerminalEvidence{
		WorkItemID: WorkItemID{value: value.WorkItemID}, AdmissionGrantID: AdmissionGrantID{value: value.AdmissionGrantID},
		GrantGeneration: value.GrantGeneration, RuntimeRunID: RuntimeRunID{value: value.RuntimeRunID},
		StartOperationID: OperationID{value: value.StartOperationID}, StartDigest: value.StartDigest,
		TerminalDecisionID: RuntimeDecisionID{value: value.TerminalDecisionID}, RuntimeRevision: value.RuntimeRevision,
		RuntimeFence: value.RuntimeFence, SchedulerEpoch: value.SchedulerEpoch, PolicyVersion: value.PolicyVersion,
		LeaseAcquireOperationID: OperationID{value: value.LeaseAcquireOperationID}, LeaseAcquireDigest: value.LeaseAcquireDigest,
	}
}

func capacityEvidenceSnapshotFromPostgres(value postgresCapacityEvidenceState) RuntimeCapacityEvidenceSnapshot {
	noLeaseBase := runtimeFencedEvidenceFromPostgres(value.NoLeasePhysicalDisposition.postgresRuntimeFencedEvidenceState)
	return RuntimeCapacityEvidenceSnapshot{
		RuntimeFencedOrTerminal: runtimeFencedEvidenceFromPostgres(value.RuntimeFencedOrTerminal),
		NoLeasePhysicalDisposition: NoLeasePhysicalDispositionEvidence{
			WorkItemID: noLeaseBase.WorkItemID, AdmissionGrantID: noLeaseBase.AdmissionGrantID,
			GrantGeneration: noLeaseBase.GrantGeneration, RuntimeRunID: noLeaseBase.RuntimeRunID,
			StartOperationID: noLeaseBase.StartOperationID, StartDigest: noLeaseBase.StartDigest,
			TerminalDecisionID: noLeaseBase.TerminalDecisionID, RuntimeRevision: noLeaseBase.RuntimeRevision,
			RuntimeFence: noLeaseBase.RuntimeFence, SchedulerEpoch: noLeaseBase.SchedulerEpoch,
			PolicyVersion:           noLeaseBase.PolicyVersion,
			LeaseAcquireOperationID: noLeaseBase.LeaseAcquireOperationID,
			LeaseAcquireDigest:      noLeaseBase.LeaseAcquireDigest,
			ExecutionNodeID:         ExecutionNodeID{value: value.NoLeasePhysicalDisposition.ExecutionNodeID},
			NodeCapacityGeneration:  value.NoLeasePhysicalDisposition.NodeCapacityGeneration,
		},
		PhysicalCapacityReleaseReady: PhysicalCapacityReleaseReadyEvidence{
			RuntimeRunID:           RuntimeRunID{value: value.PhysicalCapacityReleaseReady.RuntimeRunID},
			SandboxLeaseID:         SandboxLeaseID{value: value.PhysicalCapacityReleaseReady.SandboxLeaseID},
			LeaseGeneration:        value.PhysicalCapacityReleaseReady.LeaseGeneration,
			LeaseFence:             value.PhysicalCapacityReleaseReady.LeaseFence,
			ExecutionNodeID:        ExecutionNodeID{value: value.PhysicalCapacityReleaseReady.ExecutionNodeID},
			NodeCapacityGeneration: value.PhysicalCapacityReleaseReady.NodeCapacityGeneration,
		},
	}
}

type rowScanner interface {
	Scan(...any) error
}

func scanPostgresRuntimeRecord(row rowScanner, runtimeRunID RuntimeRunID) (*runtimeRecord, error) {
	var workspaceID, taskID, phaseRunID, ownerID, terminalEvidenceID string
	var ownerGeneration AuthorizationGeneration
	var ownerKind AuthorityKind
	var taskRevision TaskRevision
	var runtimeRevision RuntimeRevision
	var operationGeneration OperationGeneration
	var runtimeFence RuntimeFence
	var safetyEpoch ReleaseSafetyEpoch
	var state RuntimeState
	var outcome RuntimeOutcome
	var encoded []byte
	if err := row.Scan(
		&workspaceID, &taskID, &phaseRunID, &ownerID, &ownerGeneration, &ownerKind,
		&taskRevision, &runtimeRevision, &operationGeneration, &runtimeFence,
		&safetyEpoch, &state, &outcome, &terminalEvidenceID, &encoded,
	); err != nil {
		return nil, err
	}
	var persisted postgresRuntimeState
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&persisted); err != nil {
		return nil, newPersistenceError(PersistenceStateCorrupt)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, newPersistenceError(PersistenceStateCorrupt)
	}
	fixture := RuntimeFixture{
		PersonalWorkspaceID: PersonalWorkspaceID{value: workspaceID}, TaskID: TaskID{value: taskID},
		PhaseRunID: PhaseRunID{value: phaseRunID}, RuntimeRunID: runtimeRunID,
		Owner:        RuntimeAuthority{id: AuthorityID{value: ownerID}, generation: ownerGeneration, kind: ownerKind},
		TaskRevision: taskRevision, RuntimeRevision: runtimeRevision, OperationGeneration: operationGeneration,
		RuntimeFence: runtimeFence, SafetyEpoch: safetyEpoch, State: state, Outcome: outcome,
		TerminalEvidenceID: EvidenceID{value: terminalEvidenceID},
	}
	record := &runtimeRecord{
		fixture: fixture, bindings: make(map[OperationID]Digest), decisions: make(map[decisionAttemptKey]RuntimeDecisionFact),
		operation: RuntimeOperationBinding{
			Status: persisted.OperationStatus, OperationID: OperationID{value: persisted.OperationID},
			Digest: persisted.OperationDigest, Generation: persisted.OperationGeneration,
			AdmissionGrantID: AdmissionGrantID{value: persisted.AdmissionGrantID}, WorkItemID: WorkItemID{value: persisted.AdmissionWorkItemID},
			GrantGeneration: persisted.AdmissionGrantGeneration, ExecutionNodeID: ExecutionNodeID{value: persisted.ExecutionNodeID},
			NodeCapacityGeneration: persisted.NodeCapacityGeneration, ResourceClassID: ResourceClassID{value: persisted.ResourceClassID},
			ExecutionPolicyID: ExecutionPolicyID{value: persisted.ExecutionPolicyID}, SchedulerEpoch: persisted.SchedulerEpoch,
			PolicyVersion: persisted.PolicyVersion,
		},
		lease: RuntimeLeaseSnapshot{
			AcquireStatus: persisted.LeaseAcquireStatus, AcquireOperationID: OperationID{value: persisted.LeaseAcquireOperationID},
			AcquireDigest: persisted.LeaseAcquireDigest, LeaseID: SandboxLeaseID{value: persisted.SandboxLeaseID},
			Generation: persisted.LeaseGeneration, Fence: persisted.LeaseFence,
		},
		deadline: persisted.Deadline.UTC(), leaseAcquireBy: persisted.LeaseAcquireBy.UTC(),
		cancellation: RuntimeCancellationSnapshot{
			Status: persisted.CancellationStatus, OperationID: OperationID{value: persisted.CancellationOperationID},
			Reason: persisted.CancellationReason, AcceptedAt: persisted.CancellationAcceptedAt.UTC(),
		},
		evidenceRoot: EvidenceRootSnapshot{
			SchemaVersion: persisted.EvidenceSchemaVersion, EvidenceRootID: EvidenceRootID{value: persisted.EvidenceRootID},
			Digest: persisted.EvidenceDigest,
		},
		capacity: RuntimeCapacitySnapshot{
			LogicalRelease: persisted.LogicalCapacity, NoLease: persisted.NoLeaseCapacity, Physical: persisted.PhysicalCapacity,
		},
		capacityEvidence:       capacityEvidenceSnapshotFromPostgres(persisted.CapacityEvidence),
		preLeaseTerminalReason: persisted.PreLeaseTerminalReason,
		reconciliation:         persisted.Reconciliation,
	}
	if !validRuntimeFixture(fixture) || !snapshotVariantsKnown(record) ||
		(record.operation.Generation != 0 && record.operation.Generation != operationGeneration) {
		return nil, newPersistenceError(PersistenceStateCorrupt)
	}
	return record, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return newPersistenceError(PersistenceStateCorrupt)
		}
		return err
	}
	return nil
}

var _ rowScanner = (*sql.Row)(nil)
