package runtimeexecution

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"time"
)

type postgresRuntimeState struct {
	OperationStatus          OperationBindingStatus      `json:"operation_status"`
	OperationID              string                      `json:"operation_id"`
	OperationDigest          Digest                      `json:"operation_digest"`
	OperationGeneration      OperationGeneration         `json:"operation_generation"`
	AdmissionGrantID         string                      `json:"admission_grant_id"`
	AdmissionGrantGeneration AdmissionGrantGeneration    `json:"admission_grant_generation"`
	LeaseAcquireStatus       LeaseAcquireStatus          `json:"lease_acquire_status"`
	SandboxLeaseID           string                      `json:"sandbox_lease_id"`
	LeaseGeneration          LeaseGeneration             `json:"lease_generation"`
	LeaseFence               LeaseFence                  `json:"lease_fence"`
	Deadline                 time.Time                   `json:"deadline"`
	LeaseAcquireBy           time.Time                   `json:"lease_acquire_by"`
	CancellationStatus       CancellationStatus          `json:"cancellation_status"`
	CancellationOperationID  string                      `json:"cancellation_operation_id"`
	CancellationReason       CancellationReason          `json:"cancellation_reason"`
	CancellationAcceptedAt   time.Time                   `json:"cancellation_accepted_at"`
	EvidenceSchemaVersion    SchemaVersion               `json:"evidence_schema_version"`
	EvidenceRootID           string                      `json:"evidence_root_id"`
	EvidenceDigest           Digest                      `json:"evidence_digest"`
	LogicalCapacity          LogicalCapacityDisposition  `json:"logical_capacity"`
	NoLeaseCapacity          NoLeaseCapacityDisposition  `json:"no_lease_capacity"`
	PhysicalCapacity         PhysicalCapacityDisposition `json:"physical_capacity"`
	Reconciliation           ReconciliationStatus        `json:"reconciliation"`
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
		AdmissionGrantID: fixture.Operation.AdmissionGrantID.String(), AdmissionGrantGeneration: fixture.Operation.GrantGeneration,
		LeaseAcquireStatus: fixture.Lease.AcquireStatus, SandboxLeaseID: fixture.Lease.LeaseID.String(),
		LeaseGeneration: fixture.Lease.Generation, LeaseFence: fixture.Lease.Fence,
		Deadline: fixture.Deadline.UTC(), LeaseAcquireBy: fixture.LeaseAcquireBy.UTC(),
		CancellationStatus: fixture.Cancellation.Status, CancellationOperationID: fixture.Cancellation.OperationID.String(),
		CancellationReason: fixture.Cancellation.Reason, CancellationAcceptedAt: fixture.Cancellation.AcceptedAt.UTC(),
		EvidenceSchemaVersion: fixture.EvidenceRoot.SchemaVersion, EvidenceRootID: fixture.EvidenceRoot.EvidenceRootID.String(),
		EvidenceDigest:  fixture.EvidenceRoot.Digest,
		LogicalCapacity: fixture.Capacity.LogicalRelease, NoLeaseCapacity: fixture.Capacity.NoLease,
		PhysicalCapacity: fixture.Capacity.Physical, Reconciliation: fixture.Reconciliation,
	}
	return json.Marshal(state)
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
			AdmissionGrantID: AdmissionGrantID{value: persisted.AdmissionGrantID}, GrantGeneration: persisted.AdmissionGrantGeneration,
		},
		lease: RuntimeLeaseSnapshot{
			AcquireStatus: persisted.LeaseAcquireStatus, LeaseID: SandboxLeaseID{value: persisted.SandboxLeaseID},
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
		reconciliation: persisted.Reconciliation,
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
