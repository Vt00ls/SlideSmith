package runtimeexecution

import (
	"bytes"
	"encoding/hex"
	"encoding/json"

	"time"
)

// localDevelopmentRuntimeWire is the JSON-safe wire form of a local runtime
// record. Opaque identities are carried as strings; the aggregate state reuses
// the same closed wire state as the PostgreSQL codec so no second
// representation drifts.
type localDevelopmentRuntimeWire struct {
	RuntimeRunID        string                         `json:"runtime_run_id"`
	PersonalWorkspaceID string                         `json:"personal_workspace_id"`
	TaskID              string                         `json:"task_id"`
	PhaseRunID          string                         `json:"phase_run_id"`
	OwnerID             string                         `json:"owner_id"`
	OwnerGeneration     AuthorizationGeneration        `json:"owner_generation"`
	OwnerKind           AuthorityKind                  `json:"owner_kind"`
	TaskRevision        TaskRevision                   `json:"task_revision"`
	RuntimeRevision     RuntimeRevision                `json:"runtime_revision"`
	OperationGeneration OperationGeneration            `json:"operation_generation"`
	RuntimeFence        RuntimeFence                   `json:"runtime_fence"`
	SafetyEpoch         ReleaseSafetyEpoch             `json:"safety_epoch"`
	State               RuntimeState                   `json:"state"`
	Outcome             RuntimeOutcome                 `json:"outcome"`
	TerminalEvidenceID  string                         `json:"terminal_evidence_id"`
	Aggregate           postgresRuntimeState           `json:"aggregate"`
	Bindings            []localDevelopmentBindingWire  `json:"bindings"`
	Decisions           []localDevelopmentDecisionWire `json:"decisions"`
}

type localDevelopmentBindingWire struct {
	OperationID string `json:"operation_id"`
	Digest      string `json:"digest"`
}

type localDevelopmentDecisionWire struct {
	Kind            CommandKind              `json:"kind"`
	OperationID     string                   `json:"operation_id"`
	GrantID         string                   `json:"grant_id"`
	GrantGeneration AdmissionGrantGeneration `json:"grant_generation"`
	Fact            postgresDecisionState    `json:"fact"`
}

type localDevelopmentGrantWire struct {
	AdmissionGrantID       string                   `json:"admission_grant_id"`
	WorkItemID             string                   `json:"work_item_id"`
	Generation             AdmissionGrantGeneration `json:"generation"`
	PersonalWorkspaceID    string                   `json:"personal_workspace_id"`
	RuntimeRunID           string                   `json:"runtime_run_id"`
	OperationID            string                   `json:"operation_id"`
	CanonicalStartDigest   string                   `json:"canonical_start_digest"`
	ExpiresAt              time.Time                `json:"expires_at"`
	Current                bool                     `json:"current"`
	ExecutionNodeID        string                   `json:"execution_node_id"`
	NodeCapacityGeneration uint64                   `json:"node_capacity_generation"`
	SchedulerEpoch         uint64                   `json:"scheduler_epoch"`
	PolicyVersion          uint64                   `json:"policy_version"`
}

type localDevelopmentNodeWire struct {
	ExecutionNodeID         string                    `json:"execution_node_id"`
	Generation              NodeGeneration            `json:"generation"`
	Readiness               NodeReadiness             `json:"readiness"`
	AttestationID           string                    `json:"attestation_id"`
	AttestationGeneration   NodeAttestationGeneration `json:"attestation_generation"`
	AttestedAt              time.Time                 `json:"attested_at"`
	ExpiresAt               time.Time                 `json:"expires_at"`
	ResourceClassID         string                    `json:"resource_class_id"`
	ExecutionPolicyID       string                    `json:"execution_policy_id"`
	NodeAuthorityID         string                    `json:"node_authority_id"`
	WorkerAuthorityID       string                    `json:"worker_authority_id"`
	WorkerGeneration        WorkerGeneration          `json:"worker_generation"`
	AuthorizationGeneration AuthorizationGeneration   `json:"authorization_generation"`
	AuthorizationExpiresAt  time.Time                 `json:"authorization_expires_at"`
	ReleaseSafetyEpoch      ReleaseSafetyEpoch        `json:"release_safety_epoch"`
	CatalogSafetyEpoch      CatalogSafetyEpoch        `json:"catalog_safety_epoch"`
	Occupancy               NodeOccupancy             `json:"occupancy"`
	Quarantined             bool                      `json:"quarantined"`
	Containment             ContainmentStatus         `json:"containment"`
	Reset                   ResetStatus               `json:"reset"`
	ActiveRuntimeRunID      string                    `json:"active_runtime_run_id"`
	ActiveLeaseID           string                    `json:"active_lease_id"`
	LastSandboxGeneration   SandboxGeneration         `json:"last_sandbox_generation"`
	LastSandboxFence        SandboxFence              `json:"last_sandbox_fence"`
	LastResetEvidenceID     string                    `json:"last_reset_evidence_id"`
	LastResetEvidenceDigest string                    `json:"last_reset_evidence_digest"`
}

type localDevelopmentReservationWire struct {
	QuotaReservationID           string                       `json:"quota_reservation_id"`
	Generation                   QuotaReservationGeneration   `json:"generation"`
	Mode                         QuotaReservationMode         `json:"mode"`
	State                        QuotaReservationState        `json:"state"`
	PersonalWorkspaceID          string                       `json:"personal_workspace_id"`
	TaskID                       string                       `json:"task_id"`
	PhaseRunID                   string                       `json:"phase_run_id"`
	AuthorizationGeneration      AuthorizationGeneration      `json:"authorization_generation"`
	Capability                   ProviderCapability           `json:"capability"`
	GatewayRoutePolicyID         string                       `json:"gateway_route_policy_id"`
	GatewayRoutePolicyGeneration GatewayRoutePolicyGeneration `json:"gateway_route_policy_generation"`
	CapabilityScope              ProviderCapabilityScope      `json:"capability_scope"`
	ValidFrom                    time.Time                    `json:"valid_from"`
	ExpiresAt                    time.Time                    `json:"expires_at"`
}

type localDevelopmentMaintenanceAuthorityWire struct {
	ExecutionNodeID string                          `json:"execution_node_id"`
	Kind            RuntimeMaintenanceAuthorityKind `json:"kind"`
	ID              string                          `json:"id"`
	Generation      AuthorizationGeneration         `json:"generation"`
}

// encodeLocalDevelopmentRuntime produces the JSON-safe wire form.
func encodeLocalDevelopmentRuntime(record *runtimeRecord) (localDevelopmentRuntimeWire, error) {
	wire := localDevelopmentRuntimeWire{
		RuntimeRunID:        record.fixture.RuntimeRunID.String(),
		PersonalWorkspaceID: record.fixture.PersonalWorkspaceID.String(),
		TaskID:              record.fixture.TaskID.String(),
		PhaseRunID:          record.fixture.PhaseRunID.String(),
		OwnerID:             record.fixture.Owner.id.String(),
		OwnerGeneration:     record.fixture.Owner.generation,
		OwnerKind:           record.fixture.Owner.kind,
		TaskRevision:        record.fixture.TaskRevision,
		RuntimeRevision:     record.fixture.RuntimeRevision,
		OperationGeneration: record.fixture.OperationGeneration,
		RuntimeFence:        record.fixture.RuntimeFence,
		SafetyEpoch:         record.fixture.SafetyEpoch,
		State:               record.fixture.State,
		Outcome:             record.fixture.Outcome,
		TerminalEvidenceID:  record.fixture.TerminalEvidenceID.String(),
	}
	encoded, err := encodePostgresRuntimeFixture(fixtureFromRuntimeRecord(record))
	if err != nil {
		return localDevelopmentRuntimeWire{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire.Aggregate); err != nil {
		return localDevelopmentRuntimeWire{}, newPersistenceError(PersistenceStateCorrupt)
	}
	for operationID, digest := range record.bindings {
		wire.Bindings = append(wire.Bindings, localDevelopmentBindingWire{
			OperationID: operationID.String(), Digest: digest.String(),
		})
	}
	for attempt, fact := range record.decisions {
		encodedFact, err := encodePostgresDecisionFact(fact)
		if err != nil {
			return localDevelopmentRuntimeWire{}, err
		}
		var factState postgresDecisionState
		decoder := json.NewDecoder(bytes.NewReader(encodedFact))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&factState); err != nil {
			return localDevelopmentRuntimeWire{}, newPersistenceError(PersistenceStateCorrupt)
		}
		wire.Decisions = append(wire.Decisions, localDevelopmentDecisionWire{
			Kind: attempt.kind, OperationID: attempt.operationID.String(),
			GrantID: attempt.grantID.String(), GrantGeneration: attempt.grantGeneration,
			Fact: factState,
		})
	}
	return wire, nil
}

func decodeLocalDevelopmentRuntime(wire localDevelopmentRuntimeWire) (*runtimeRecord, error) {
	if !validOpaqueID(wire.RuntimeRunID) || !validOpaqueID(wire.PersonalWorkspaceID) ||
		!validOpaqueID(wire.TaskID) || !validOpaqueID(wire.PhaseRunID) || !validOpaqueID(wire.OwnerID) {
		return nil, newPersistenceError(PersistenceStateCorrupt)
	}
	encoded, err := json.Marshal(wire.Aggregate)
	if err != nil {
		return nil, newPersistenceError(PersistenceStateCorrupt)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var persisted postgresRuntimeState
	if err := decoder.Decode(&persisted); err != nil {
		return nil, newPersistenceError(PersistenceStateCorrupt)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, newPersistenceError(PersistenceStateCorrupt)
	}
	fixture := RuntimeFixture{
		PersonalWorkspaceID: PersonalWorkspaceID{value: wire.PersonalWorkspaceID},
		TaskID:              TaskID{value: wire.TaskID},
		PhaseRunID:          PhaseRunID{value: wire.PhaseRunID},
		RuntimeRunID:        RuntimeRunID{value: wire.RuntimeRunID},
		Owner:               RuntimeAuthority{id: AuthorityID{value: wire.OwnerID}, generation: wire.OwnerGeneration, kind: wire.OwnerKind},
		TaskRevision:        wire.TaskRevision, RuntimeRevision: wire.RuntimeRevision,
		OperationGeneration: wire.OperationGeneration, RuntimeFence: wire.RuntimeFence,
		SafetyEpoch: wire.SafetyEpoch, State: wire.State, Outcome: wire.Outcome,
		TerminalEvidenceID: EvidenceID{value: wire.TerminalEvidenceID},
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
			Disposition: persisted.LeaseDisposition, ExpiresAt: persisted.LeaseExpiresAt.UTC(),
			SandboxID: SandboxID{value: persisted.SandboxID}, SandboxGeneration: persisted.SandboxGeneration,
			SandboxFence:            persisted.SandboxFence,
			WorkerAuthorityID:       WorkerAuthorityID{value: persisted.WorkerAuthorityID},
			WorkerGeneration:        persisted.WorkerGeneration,
			NodeAuthorityID:         NodeAuthorityID{value: persisted.NodeAuthorityID},
			AuthorizationGeneration: persisted.AuthorizationGeneration,
			AuthorizationExpiresAt:  persisted.AuthorizationExpiresAt.UTC(),
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
		capacityEvidence: capacityEvidenceSnapshotFromPostgres(persisted.CapacityEvidence),
		node: RuntimeNodeSnapshot{
			ExecutionNodeID: ExecutionNodeID{value: persisted.Node.ExecutionNodeID},
			Generation:      persisted.Node.Generation, Readiness: persisted.Node.Readiness,
			AttestationID:         NodeAttestationID{value: persisted.Node.AttestationID},
			AttestationGeneration: persisted.Node.AttestationGeneration,
			AttestedAt:            persisted.Node.AttestedAt.UTC(), ExpiresAt: persisted.Node.ExpiresAt.UTC(),
			Occupancy: persisted.Node.Occupancy, Quarantined: persisted.Node.Quarantined,
			Containment: persisted.Node.Containment, Reset: persisted.Node.Reset,
		},
		cleanup: RuntimeLeaseCleanupSnapshot{
			Status: persisted.Cleanup.Status, OperationID: OperationID{value: persisted.Cleanup.OperationID},
			CanonicalRequestDigest: persisted.Cleanup.CanonicalRequestDigest,
			StopMainProcess:        persisted.Cleanup.StopMainProcess,
			StopChildProcesses:     persisted.Cleanup.StopChildProcesses,
			RevokeSecrets:          persisted.Cleanup.RevokeSecrets, RemoveNetwork: persisted.Cleanup.RemoveNetwork,
			FenceRuntimeView:     persisted.Cleanup.FenceRuntimeView,
			ReconcileContainment: persisted.Cleanup.ReconcileContainment,
		},
		catalogSafetyEpoch:     persisted.CatalogSafetyEpoch,
		preLeaseTerminalReason: persisted.PreLeaseTerminalReason,
		reconciliation:         persisted.Reconciliation,
		readiness:              readinessSnapshotFromPostgres(persisted.Readiness),
		runtimeViewBinding:     runtimeViewBindingSnapshotFromPostgres(persisted.RuntimeViewBinding),
		gateway:                gatewaySnapshotFromPostgres(persisted.Gateway),
		usage:                  usageSnapshotFromPostgres(persisted.Usage),
		worker:                 workerSnapshotFromPostgres(persisted.Worker),
	}
	record.acceptedStart.OperationID = record.operation.OperationID
	record.acceptedStartDigest = record.operation.Digest
	for _, binding := range wire.Bindings {
		if !validOpaqueID(binding.OperationID) {
			return nil, newPersistenceError(PersistenceStateCorrupt)
		}
		digest, err := parseDigestHex(binding.Digest)
		if err != nil {
			return nil, newPersistenceError(PersistenceStateCorrupt)
		}
		record.bindings[OperationID{value: binding.OperationID}] = digest
	}
	for _, decision := range wire.Decisions {
		encodedFact, err := json.Marshal(decision.Fact)
		if err != nil {
			return nil, newPersistenceError(PersistenceStateCorrupt)
		}
		fact, err := decodePostgresDecisionFact(encodedFact)
		if err != nil {
			return nil, err
		}
		attempt := decisionAttemptKey{
			kind: decision.Kind, operationID: OperationID{value: decision.OperationID},
			grantID: AdmissionGrantID{value: decision.GrantID}, grantGeneration: decision.GrantGeneration,
		}
		record.decisions[attempt] = fact
		if decision.Kind == CommandStartRuntimeRun && fact.Disposition == DecisionAccepted {
			record.acceptedStart = fact
			record.acceptedStartDigest = fact.CanonicalRequestDigest
		}
	}
	if !validRuntimeFixture(fixture) || !snapshotVariantsKnown(record) {
		return nil, newPersistenceError(PersistenceStateCorrupt)
	}
	return record, nil
}

func encodeLocalDevelopmentGrant(grant AdmissionGrantFixture) localDevelopmentGrantWire {
	return localDevelopmentGrantWire{
		AdmissionGrantID: grant.AdmissionGrantID.String(), WorkItemID: grant.WorkItemID.String(),
		Generation: grant.Generation, PersonalWorkspaceID: grant.PersonalWorkspaceID.String(),
		RuntimeRunID: grant.RuntimeRunID.String(), OperationID: grant.OperationID.String(),
		CanonicalStartDigest: grant.CanonicalStartDigest.String(), ExpiresAt: grant.ExpiresAt.UTC(),
		Current: grant.Current, ExecutionNodeID: grant.ExecutionNodeID.String(),
		NodeCapacityGeneration: grant.NodeCapacityGeneration, SchedulerEpoch: grant.SchedulerEpoch,
		PolicyVersion: grant.PolicyVersion,
	}
}

func decodeLocalDevelopmentGrant(wire localDevelopmentGrantWire) AdmissionGrantFixture {
	return AdmissionGrantFixture{
		AdmissionGrantID: AdmissionGrantID{value: wire.AdmissionGrantID}, WorkItemID: WorkItemID{value: wire.WorkItemID},
		Generation: wire.Generation, PersonalWorkspaceID: PersonalWorkspaceID{value: wire.PersonalWorkspaceID},
		RuntimeRunID: RuntimeRunID{value: wire.RuntimeRunID}, OperationID: OperationID{value: wire.OperationID},
		CanonicalStartDigest: parseDigestText(wire.CanonicalStartDigest), ExpiresAt: wire.ExpiresAt.UTC(),
		Current: wire.Current, ExecutionNodeID: ExecutionNodeID{value: wire.ExecutionNodeID},
		NodeCapacityGeneration: wire.NodeCapacityGeneration, SchedulerEpoch: wire.SchedulerEpoch,
		PolicyVersion: wire.PolicyVersion,
	}
}

func encodeLocalDevelopmentNode(node ExecutionNodeFixture) localDevelopmentNodeWire {
	return localDevelopmentNodeWire{
		ExecutionNodeID: node.ExecutionNodeID.String(), Generation: node.Generation,
		Readiness: node.Readiness, AttestationID: node.AttestationID.String(),
		AttestationGeneration: node.AttestationGeneration, AttestedAt: node.AttestedAt.UTC(),
		ExpiresAt: node.ExpiresAt.UTC(), ResourceClassID: node.ResourceClassID.String(),
		ExecutionPolicyID: node.ExecutionPolicyID.String(), NodeAuthorityID: node.NodeAuthorityID.String(),
		WorkerAuthorityID: node.WorkerAuthorityID.String(), WorkerGeneration: node.WorkerGeneration,
		AuthorizationGeneration: node.AuthorizationGeneration, AuthorizationExpiresAt: node.AuthorizationExpiresAt.UTC(),
		ReleaseSafetyEpoch: node.ReleaseSafetyEpoch, CatalogSafetyEpoch: node.CatalogSafetyEpoch,
		Occupancy: node.Occupancy, Quarantined: node.Quarantined, Containment: node.Containment,
		Reset: node.Reset, ActiveRuntimeRunID: node.ActiveRuntimeRunID.String(),
		ActiveLeaseID: node.ActiveLeaseID.String(), LastSandboxGeneration: node.LastSandboxGeneration,
		LastSandboxFence: node.LastSandboxFence, LastResetEvidenceID: node.LastResetEvidenceID.String(),
		LastResetEvidenceDigest: node.LastResetEvidenceDigest.String(),
	}
}

func decodeLocalDevelopmentNode(wire localDevelopmentNodeWire) ExecutionNodeFixture {
	return ExecutionNodeFixture{
		ExecutionNodeID: ExecutionNodeID{value: wire.ExecutionNodeID}, Generation: wire.Generation,
		Readiness: wire.Readiness, AttestationID: NodeAttestationID{value: wire.AttestationID},
		AttestationGeneration: wire.AttestationGeneration, AttestedAt: wire.AttestedAt.UTC(),
		ExpiresAt: wire.ExpiresAt.UTC(), ResourceClassID: ResourceClassID{value: wire.ResourceClassID},
		ExecutionPolicyID: ExecutionPolicyID{value: wire.ExecutionPolicyID}, NodeAuthorityID: NodeAuthorityID{value: wire.NodeAuthorityID},
		WorkerAuthorityID: WorkerAuthorityID{value: wire.WorkerAuthorityID}, WorkerGeneration: wire.WorkerGeneration,
		AuthorizationGeneration: wire.AuthorizationGeneration, AuthorizationExpiresAt: wire.AuthorizationExpiresAt.UTC(),
		ReleaseSafetyEpoch: wire.ReleaseSafetyEpoch, CatalogSafetyEpoch: wire.CatalogSafetyEpoch,
		Occupancy: wire.Occupancy, Quarantined: wire.Quarantined, Containment: wire.Containment,
		Reset: wire.Reset, ActiveRuntimeRunID: RuntimeRunID{value: wire.ActiveRuntimeRunID},
		ActiveLeaseID: SandboxLeaseID{value: wire.ActiveLeaseID}, LastSandboxGeneration: wire.LastSandboxGeneration,
		LastSandboxFence: wire.LastSandboxFence, LastResetEvidenceID: EvidenceID{value: wire.LastResetEvidenceID},
		LastResetEvidenceDigest: parseDigestText(wire.LastResetEvidenceDigest),
	}
}

func encodeLocalDevelopmentReservation(reservation QuotaReservationFixture) localDevelopmentReservationWire {
	return localDevelopmentReservationWire{
		QuotaReservationID: reservation.QuotaReservationID.String(), Generation: reservation.Generation,
		Mode: reservation.Mode, State: reservation.State,
		PersonalWorkspaceID: reservation.PersonalWorkspaceID.String(), TaskID: reservation.TaskID.String(),
		PhaseRunID: reservation.PhaseRunID.String(), AuthorizationGeneration: reservation.AuthorizationGeneration,
		Capability: reservation.Capability, GatewayRoutePolicyID: reservation.GatewayRoutePolicyID.String(),
		GatewayRoutePolicyGeneration: reservation.GatewayRoutePolicyGeneration,
		CapabilityScope:              reservation.CapabilityScope, ValidFrom: reservation.ValidFrom.UTC(),
		ExpiresAt: reservation.ExpiresAt.UTC(),
	}
}

func decodeLocalDevelopmentReservation(wire localDevelopmentReservationWire) QuotaReservationFixture {
	return QuotaReservationFixture{
		QuotaReservationID: QuotaReservationID{value: wire.QuotaReservationID}, Generation: wire.Generation,
		Mode: wire.Mode, State: wire.State,
		PersonalWorkspaceID: PersonalWorkspaceID{value: wire.PersonalWorkspaceID}, TaskID: TaskID{value: wire.TaskID},
		PhaseRunID: PhaseRunID{value: wire.PhaseRunID}, AuthorizationGeneration: wire.AuthorizationGeneration,
		Capability: wire.Capability, GatewayRoutePolicyID: GatewayRoutePolicyID{value: wire.GatewayRoutePolicyID},
		GatewayRoutePolicyGeneration: wire.GatewayRoutePolicyGeneration,
		CapabilityScope:              wire.CapabilityScope, ValidFrom: wire.ValidFrom.UTC(), ExpiresAt: wire.ExpiresAt.UTC(),
	}
}

func encodeLocalDevelopmentMaintenanceAuthority(key maintenanceAuthorityKey, caller maintenanceCallerAuthority) localDevelopmentMaintenanceAuthorityWire {
	return localDevelopmentMaintenanceAuthorityWire{
		ExecutionNodeID: key.executionNodeID.String(), Kind: caller.kind, ID: caller.id.String(),
		Generation: caller.generation,
	}
}

func parseDigestHex(value string) (Digest, error) {
	var digest Digest
	if value == "" {
		return digest, nil
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != len(digest) {
		return digest, newError(ErrorIntegrityConflict)
	}
	copy(digest[:], decoded)
	return digest, nil
}

func parseDigestText(value string) Digest {
	digest, _ := parseDigestHex(value)
	return digest
}
