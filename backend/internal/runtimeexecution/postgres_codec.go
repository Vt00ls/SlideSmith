package runtimeexecution

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"time"
)

type postgresRuntimeState struct {
	OperationStatus          OperationBindingStatus          `json:"operation_status"`
	OperationID              string                          `json:"operation_id"`
	OperationDigest          Digest                          `json:"operation_digest"`
	OperationGeneration      OperationGeneration             `json:"operation_generation"`
	AdmissionGrantID         string                          `json:"admission_grant_id"`
	AdmissionWorkItemID      string                          `json:"admission_work_item_id"`
	AdmissionGrantGeneration AdmissionGrantGeneration        `json:"admission_grant_generation"`
	ExecutionNodeID          string                          `json:"execution_node_id"`
	NodeCapacityGeneration   uint64                          `json:"node_capacity_generation"`
	ResourceClassID          string                          `json:"resource_class_id"`
	ExecutionPolicyID        string                          `json:"execution_policy_id"`
	SchedulerEpoch           uint64                          `json:"scheduler_epoch"`
	PolicyVersion            uint64                          `json:"policy_version"`
	LeaseAcquireStatus       LeaseAcquireStatus              `json:"lease_acquire_status"`
	LeaseAcquireOperationID  string                          `json:"lease_acquire_operation_id"`
	LeaseAcquireDigest       Digest                          `json:"lease_acquire_digest"`
	SandboxLeaseID           string                          `json:"sandbox_lease_id"`
	LeaseGeneration          LeaseGeneration                 `json:"lease_generation"`
	LeaseFence               LeaseFence                      `json:"lease_fence"`
	LeaseDisposition         LeaseDisposition                `json:"lease_disposition"`
	LeaseExpiresAt           time.Time                       `json:"lease_expires_at"`
	SandboxID                string                          `json:"sandbox_id"`
	SandboxGeneration        SandboxGeneration               `json:"sandbox_generation"`
	SandboxFence             SandboxFence                    `json:"sandbox_fence"`
	WorkerAuthorityID        string                          `json:"worker_authority_id"`
	WorkerGeneration         WorkerGeneration                `json:"worker_generation"`
	NodeAuthorityID          string                          `json:"node_authority_id"`
	AuthorizationGeneration  AuthorizationGeneration         `json:"authorization_generation"`
	AuthorizationExpiresAt   time.Time                       `json:"authorization_expires_at"`
	Node                     postgresRuntimeNodeState        `json:"node"`
	Cleanup                  postgresLeaseCleanupState       `json:"cleanup"`
	CatalogSafetyEpoch       CatalogSafetyEpoch              `json:"catalog_safety_epoch"`
	Deadline                 time.Time                       `json:"deadline"`
	LeaseAcquireBy           time.Time                       `json:"lease_acquire_by"`
	CancellationStatus       CancellationStatus              `json:"cancellation_status"`
	CancellationOperationID  string                          `json:"cancellation_operation_id"`
	CancellationReason       CancellationReason              `json:"cancellation_reason"`
	CancellationAcceptedAt   time.Time                       `json:"cancellation_accepted_at"`
	EvidenceSchemaVersion    SchemaVersion                   `json:"evidence_schema_version"`
	EvidenceRootID           string                          `json:"evidence_root_id"`
	EvidenceDigest           Digest                          `json:"evidence_digest"`
	LogicalCapacity          LogicalCapacityDisposition      `json:"logical_capacity"`
	NoLeaseCapacity          NoLeaseCapacityDisposition      `json:"no_lease_capacity"`
	PhysicalCapacity         PhysicalCapacityDisposition     `json:"physical_capacity"`
	CapacityEvidence         postgresCapacityEvidenceState   `json:"capacity_evidence"`
	PreLeaseTerminalReason   PreLeaseTerminalReason          `json:"pre_lease_terminal_reason"`
	Reconciliation           ReconciliationStatus            `json:"reconciliation"`
	Readiness                postgresReadinessState          `json:"readiness"`
	RuntimeViewBinding       postgresRuntimeViewBindingState `json:"runtime_view_binding"`
	Gateway                  postgresGatewayState            `json:"gateway"`
	Usage                    postgresUsageEvidenceState      `json:"usage"`
}

type postgresPrerequisiteFactState struct {
	State          PrerequisiteState   `json:"state"`
	OperationID    string              `json:"operation_id"`
	RequestDigest  Digest              `json:"request_digest"`
	EvidenceID     string              `json:"evidence_id"`
	EvidenceDigest Digest              `json:"evidence_digest"`
	Failure        PrerequisiteFailure `json:"failure"`
}

type postgresReadinessState struct {
	Lease           postgresPrerequisiteFactState `json:"lease"`
	RuntimeBinding  postgresPrerequisiteFactState `json:"runtime_binding"`
	RuntimeView     postgresPrerequisiteFactState `json:"runtime_view"`
	ImmutableInputs postgresPrerequisiteFactState `json:"immutable_inputs"`
	LLMGateway      postgresPrerequisiteFactState `json:"llm_gateway"`
	CapsuleReady    bool                          `json:"capsule_ready"`
}

type postgresRuntimeViewBindingState struct {
	RuntimeViewID               string                           `json:"runtime_view_id"`
	OpenOperationID             string                           `json:"open_operation_id"`
	OpenRequestDigest           Digest                           `json:"open_request_digest"`
	SandboxLeaseAuthorityDigest Digest                           `json:"sandbox_lease_authority_digest"`
	SandboxLeaseID              string                           `json:"sandbox_lease_id"`
	LeaseGeneration             LeaseGeneration                  `json:"lease_generation"`
	LeaseFence                  LeaseFence                       `json:"lease_fence"`
	Effect                      EffectClass                      `json:"effect"`
	ExpiresAt                   time.Time                        `json:"expires_at"`
	LifecycleGeneration         TaskWorkspaceLifecycleGeneration `json:"lifecycle_generation"`
	LifecycleFence              TaskWorkspaceLifecycleFence      `json:"lifecycle_fence"`
}

type postgresGatewayState struct {
	Applicability          GatewayPrerequisiteApplicability `json:"applicability"`
	Status                 GatewayGrantStatus               `json:"status"`
	Ready                  bool                             `json:"ready"`
	OperationID            string                           `json:"operation_id"`
	CanonicalRequestDigest Digest                           `json:"canonical_request_digest"`
	RequestedGeneration    GatewayGrantGeneration           `json:"requested_generation"`
	CurrentGrant           postgresGatewayGrantState        `json:"current_grant"`
}

type postgresGatewayGrantState struct {
	GatewayGrantID               string                       `json:"gateway_grant_id"`
	Generation                   GatewayGrantGeneration       `json:"generation"`
	PersonalWorkspaceID          string                       `json:"personal_workspace_id"`
	TaskID                       string                       `json:"task_id"`
	PhaseRunID                   string                       `json:"phase_run_id"`
	RuntimeRunID                 string                       `json:"runtime_run_id"`
	StartOperationID             string                       `json:"start_operation_id"`
	RuntimeBindingID             string                       `json:"runtime_binding_id"`
	RuntimeBindingDigest         Digest                       `json:"runtime_binding_digest"`
	ReleaseSafetyEpoch           ReleaseSafetyEpoch           `json:"release_safety_epoch"`
	LeaseID                      string                       `json:"lease_id"`
	LeaseGeneration              LeaseGeneration              `json:"lease_generation"`
	LeaseFence                   LeaseFence                   `json:"lease_fence"`
	RuntimeFence                 RuntimeFence                 `json:"runtime_fence"`
	QuotaReservationID           string                       `json:"quota_reservation_id"`
	QuotaReservationGeneration   QuotaReservationGeneration   `json:"quota_reservation_generation"`
	QuotaReservationMode         QuotaReservationMode         `json:"quota_reservation_mode"`
	OwnerAuthorityGeneration     AuthorizationGeneration      `json:"owner_authority_generation"`
	AuthorizationGeneration      AuthorizationGeneration      `json:"authorization_generation"`
	GatewayRoutePolicyID         string                       `json:"gateway_route_policy_id"`
	GatewayRoutePolicyGeneration GatewayRoutePolicyGeneration `json:"gateway_route_policy_generation"`
	CapabilityScope              ProviderCapabilityScope      `json:"capability_scope"`
	RecoveryGeneration           GatewayRecoveryGeneration    `json:"recovery_generation"`
	RecoveryMode                 GatewayRecoveryMode          `json:"recovery_mode"`
	RecoveryExpiresAt            time.Time                    `json:"recovery_expires_at"`
	ExpiresAt                    time.Time                    `json:"expires_at"`
	CanonicalDigest              Digest                       `json:"canonical_digest"`
}

type postgresUsageEvidenceState struct {
	Disposition  UsageEvidenceDisposition `json:"disposition"`
	ReceiptCount uint64                   `json:"receipt_count"`
	ReceiptRoot  Digest                   `json:"receipt_root"`
}

type postgresRuntimeNodeState struct {
	ExecutionNodeID       string                    `json:"execution_node_id"`
	Generation            NodeGeneration            `json:"generation"`
	Readiness             NodeReadiness             `json:"readiness"`
	AttestationID         string                    `json:"attestation_id"`
	AttestationGeneration NodeAttestationGeneration `json:"attestation_generation"`
	AttestedAt            time.Time                 `json:"attested_at"`
	ExpiresAt             time.Time                 `json:"expires_at"`
	Occupancy             NodeOccupancy             `json:"occupancy"`
	Quarantined           bool                      `json:"quarantined"`
	Containment           ContainmentStatus         `json:"containment"`
	Reset                 ResetStatus               `json:"reset"`
}

type postgresLeaseCleanupState struct {
	Status                 LeaseCleanupStatus `json:"status"`
	OperationID            string             `json:"operation_id"`
	CanonicalRequestDigest Digest             `json:"canonical_request_digest"`
	StopMainProcess        bool               `json:"stop_main_process"`
	StopChildProcesses     bool               `json:"stop_child_processes"`
	RevokeSecrets          bool               `json:"revoke_secrets"`
	RemoveNetwork          bool               `json:"remove_network"`
	FenceRuntimeView       bool               `json:"fence_runtime_view"`
	ReconcileContainment   bool               `json:"reconcile_containment"`
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
	WorkItemID             string                   `json:"work_item_id"`
	AdmissionGrantID       string                   `json:"admission_grant_id"`
	GrantGeneration        AdmissionGrantGeneration `json:"grant_generation"`
	RuntimeRunID           string                   `json:"runtime_run_id"`
	StartOperationID       string                   `json:"start_operation_id"`
	StartDigest            Digest                   `json:"start_digest"`
	ReleaseOperationID     string                   `json:"release_operation_id"`
	ReleaseOperationDigest Digest                   `json:"release_operation_digest"`
	RuntimeRevision        RuntimeRevision          `json:"runtime_revision"`
	RuntimeFence           RuntimeFence             `json:"runtime_fence"`
	SandboxLeaseID         string                   `json:"sandbox_lease_id"`
	LeaseGeneration        LeaseGeneration          `json:"lease_generation"`
	LeaseFence             LeaseFence               `json:"lease_fence"`
	SandboxID              string                   `json:"sandbox_id"`
	SandboxGeneration      SandboxGeneration        `json:"sandbox_generation"`
	SandboxFence           SandboxFence             `json:"sandbox_fence"`
	ExecutionNodeID        string                   `json:"execution_node_id"`
	NodeCapacityGeneration uint64                   `json:"node_capacity_generation"`
	ResetEvidenceID        string                   `json:"reset_evidence_id"`
	ResetEvidenceDigest    Digest                   `json:"reset_evidence_digest"`
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
		LeaseDisposition: fixture.Lease.Disposition, LeaseExpiresAt: fixture.Lease.ExpiresAt.UTC(),
		SandboxID: fixture.Lease.SandboxID.String(), SandboxGeneration: fixture.Lease.SandboxGeneration,
		SandboxFence: fixture.Lease.SandboxFence, WorkerAuthorityID: fixture.Lease.WorkerAuthorityID.String(),
		WorkerGeneration: fixture.Lease.WorkerGeneration, NodeAuthorityID: fixture.Lease.NodeAuthorityID.String(),
		AuthorizationGeneration: fixture.Lease.AuthorizationGeneration,
		AuthorizationExpiresAt:  fixture.Lease.AuthorizationExpiresAt.UTC(),
		Node: postgresRuntimeNodeState{
			ExecutionNodeID: fixture.Node.ExecutionNodeID.String(), Generation: fixture.Node.Generation,
			Readiness: fixture.Node.Readiness, AttestationID: fixture.Node.AttestationID.String(),
			AttestationGeneration: fixture.Node.AttestationGeneration,
			AttestedAt:            fixture.Node.AttestedAt.UTC(), ExpiresAt: fixture.Node.ExpiresAt.UTC(),
			Occupancy: fixture.Node.Occupancy, Quarantined: fixture.Node.Quarantined,
			Containment: fixture.Node.Containment, Reset: fixture.Node.Reset,
		},
		Cleanup: postgresLeaseCleanupState{
			Status: fixture.Cleanup.Status, OperationID: fixture.Cleanup.OperationID.String(),
			CanonicalRequestDigest: fixture.Cleanup.CanonicalRequestDigest,
			StopMainProcess:        fixture.Cleanup.StopMainProcess, StopChildProcesses: fixture.Cleanup.StopChildProcesses,
			RevokeSecrets: fixture.Cleanup.RevokeSecrets, RemoveNetwork: fixture.Cleanup.RemoveNetwork,
			FenceRuntimeView:     fixture.Cleanup.FenceRuntimeView,
			ReconcileContainment: fixture.Cleanup.ReconcileContainment,
		},
		CatalogSafetyEpoch: fixture.CatalogSafetyEpoch,
		Deadline:           fixture.Deadline.UTC(), LeaseAcquireBy: fixture.LeaseAcquireBy.UTC(),
		CancellationStatus: fixture.Cancellation.Status, CancellationOperationID: fixture.Cancellation.OperationID.String(),
		CancellationReason: fixture.Cancellation.Reason, CancellationAcceptedAt: fixture.Cancellation.AcceptedAt.UTC(),
		EvidenceSchemaVersion: fixture.EvidenceRoot.SchemaVersion, EvidenceRootID: fixture.EvidenceRoot.EvidenceRootID.String(),
		EvidenceDigest:  fixture.EvidenceRoot.Digest,
		LogicalCapacity: fixture.Capacity.LogicalRelease, NoLeaseCapacity: fixture.Capacity.NoLease,
		PhysicalCapacity: fixture.Capacity.Physical, CapacityEvidence: postgresCapacityEvidenceFromSnapshot(fixture.CapacityEvidence),
		PreLeaseTerminalReason: fixture.PreLeaseTerminalReason,
		Reconciliation:         fixture.Reconciliation,
		Readiness:              postgresReadinessFromSnapshot(fixture.Readiness),
		RuntimeViewBinding:     postgresRuntimeViewBindingFromSnapshot(fixture.RuntimeViewBinding),
		Gateway:                postgresGatewayStateFromSnapshot(fixture.Gateway),
		Usage:                  postgresUsageStateFromSnapshot(fixture.Usage),
	}
	return json.Marshal(state)
}

func postgresGatewayStateFromSnapshot(snapshot GatewayPrerequisiteSnapshot) postgresGatewayState {
	grant := snapshot.CurrentGrant
	expiresAt := grant.ExpiresAt
	if !expiresAt.IsZero() {
		expiresAt = expiresAt.UTC()
	}
	return postgresGatewayState{
		Applicability: snapshot.Applicability, Status: snapshot.Status, Ready: snapshot.Ready,
		OperationID: snapshot.OperationID.String(), CanonicalRequestDigest: snapshot.CanonicalRequestDigest,
		RequestedGeneration: snapshot.RequestedGeneration,
		CurrentGrant: postgresGatewayGrantState{
			GatewayGrantID: grant.GatewayGrantID.String(), Generation: grant.Generation,
			PersonalWorkspaceID: grant.PersonalWorkspaceID.String(), TaskID: grant.TaskID.String(),
			PhaseRunID:   grant.PhaseRunID.String(),
			RuntimeRunID: grant.RuntimeRunID.String(), StartOperationID: grant.StartOperationID.String(),
			RuntimeBindingID: grant.RuntimeBindingID.String(), RuntimeBindingDigest: grant.RuntimeBindingDigest,
			ReleaseSafetyEpoch: grant.ReleaseSafetyEpoch,
			LeaseID:            grant.LeaseID.String(), LeaseGeneration: grant.LeaseGeneration, LeaseFence: grant.LeaseFence,
			RuntimeFence: grant.RuntimeFence, QuotaReservationID: grant.QuotaReservationID.String(),
			QuotaReservationGeneration:   grant.QuotaReservationGeneration,
			QuotaReservationMode:         grant.QuotaReservationMode,
			OwnerAuthorityGeneration:     grant.OwnerAuthorityGeneration,
			AuthorizationGeneration:      grant.AuthorizationGeneration,
			GatewayRoutePolicyID:         grant.GatewayRoutePolicyID.String(),
			GatewayRoutePolicyGeneration: grant.GatewayRoutePolicyGeneration,
			CapabilityScope:              grant.CapabilityScope, RecoveryGeneration: grant.RecoveryGeneration,
			RecoveryMode: grant.RecoveryMode, RecoveryExpiresAt: grant.RecoveryExpiresAt.UTC(),
			ExpiresAt: expiresAt, CanonicalDigest: grant.CanonicalDigest,
		},
	}
}

func gatewaySnapshotFromPostgres(state postgresGatewayState) GatewayPrerequisiteSnapshot {
	expiresAt := state.CurrentGrant.ExpiresAt
	if !expiresAt.IsZero() {
		expiresAt = expiresAt.UTC()
	}
	return GatewayPrerequisiteSnapshot{
		Applicability: state.Applicability, Status: state.Status, Ready: state.Ready,
		OperationID: OperationID{value: state.OperationID}, CanonicalRequestDigest: state.CanonicalRequestDigest,
		RequestedGeneration: state.RequestedGeneration,
		CurrentGrant: GatewayGrant{
			GatewayGrantInput: GatewayGrantInput{
				GatewayGrantID:      GatewayGrantID{value: state.CurrentGrant.GatewayGrantID},
				Generation:          state.CurrentGrant.Generation,
				PersonalWorkspaceID: PersonalWorkspaceID{value: state.CurrentGrant.PersonalWorkspaceID},
				TaskID:              TaskID{value: state.CurrentGrant.TaskID}, PhaseRunID: PhaseRunID{value: state.CurrentGrant.PhaseRunID},
				RuntimeRunID:         RuntimeRunID{value: state.CurrentGrant.RuntimeRunID},
				StartOperationID:     OperationID{value: state.CurrentGrant.StartOperationID},
				RuntimeBindingID:     RuntimeBindingID{value: state.CurrentGrant.RuntimeBindingID},
				RuntimeBindingDigest: state.CurrentGrant.RuntimeBindingDigest,
				ReleaseSafetyEpoch:   state.CurrentGrant.ReleaseSafetyEpoch,
				LeaseID:              SandboxLeaseID{value: state.CurrentGrant.LeaseID},
				LeaseGeneration:      state.CurrentGrant.LeaseGeneration, LeaseFence: state.CurrentGrant.LeaseFence,
				RuntimeFence:                 state.CurrentGrant.RuntimeFence,
				QuotaReservationID:           QuotaReservationID{value: state.CurrentGrant.QuotaReservationID},
				QuotaReservationGeneration:   state.CurrentGrant.QuotaReservationGeneration,
				QuotaReservationMode:         state.CurrentGrant.QuotaReservationMode,
				OwnerAuthorityGeneration:     state.CurrentGrant.OwnerAuthorityGeneration,
				AuthorizationGeneration:      state.CurrentGrant.AuthorizationGeneration,
				GatewayRoutePolicyID:         GatewayRoutePolicyID{value: state.CurrentGrant.GatewayRoutePolicyID},
				GatewayRoutePolicyGeneration: state.CurrentGrant.GatewayRoutePolicyGeneration,
				CapabilityScope:              state.CurrentGrant.CapabilityScope,
				RecoveryGeneration:           state.CurrentGrant.RecoveryGeneration,
				RecoveryMode:                 state.CurrentGrant.RecoveryMode,
				RecoveryExpiresAt:            state.CurrentGrant.RecoveryExpiresAt.UTC(), ExpiresAt: expiresAt,
			},
			CanonicalDigest: state.CurrentGrant.CanonicalDigest,
		},
	}
}

func postgresUsageStateFromSnapshot(snapshot RuntimeUsageEvidenceSnapshot) postgresUsageEvidenceState {
	return postgresUsageEvidenceState{
		Disposition: snapshot.Disposition, ReceiptCount: snapshot.Receipts.Count,
		ReceiptRoot: snapshot.Receipts.RootDigest,
	}
}

func usageSnapshotFromPostgres(state postgresUsageEvidenceState) RuntimeUsageEvidenceSnapshot {
	return RuntimeUsageEvidenceSnapshot{
		Disposition: state.Disposition,
		Receipts:    UsageReceiptReferenceSet{Count: state.ReceiptCount, RootDigest: state.ReceiptRoot},
	}
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
			WorkItemID:             value.PhysicalCapacityReleaseReady.WorkItemID.String(),
			AdmissionGrantID:       value.PhysicalCapacityReleaseReady.AdmissionGrantID.String(),
			GrantGeneration:        value.PhysicalCapacityReleaseReady.GrantGeneration,
			RuntimeRunID:           value.PhysicalCapacityReleaseReady.RuntimeRunID.String(),
			StartOperationID:       value.PhysicalCapacityReleaseReady.StartOperationID.String(),
			StartDigest:            value.PhysicalCapacityReleaseReady.StartDigest,
			ReleaseOperationID:     value.PhysicalCapacityReleaseReady.ReleaseOperationID.String(),
			ReleaseOperationDigest: value.PhysicalCapacityReleaseReady.ReleaseOperationDigest,
			RuntimeRevision:        value.PhysicalCapacityReleaseReady.RuntimeRevision,
			RuntimeFence:           value.PhysicalCapacityReleaseReady.RuntimeFence,
			SandboxLeaseID:         value.PhysicalCapacityReleaseReady.SandboxLeaseID.String(),
			LeaseGeneration:        value.PhysicalCapacityReleaseReady.LeaseGeneration,
			LeaseFence:             value.PhysicalCapacityReleaseReady.LeaseFence,
			SandboxID:              value.PhysicalCapacityReleaseReady.SandboxID.String(),
			SandboxGeneration:      value.PhysicalCapacityReleaseReady.SandboxGeneration,
			SandboxFence:           value.PhysicalCapacityReleaseReady.SandboxFence,
			ExecutionNodeID:        value.PhysicalCapacityReleaseReady.ExecutionNodeID.String(),
			NodeCapacityGeneration: value.PhysicalCapacityReleaseReady.NodeCapacityGeneration,
			ResetEvidenceID:        value.PhysicalCapacityReleaseReady.ResetEvidenceID.String(),
			ResetEvidenceDigest:    value.PhysicalCapacityReleaseReady.ResetEvidenceDigest,
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
			WorkItemID:             WorkItemID{value: value.PhysicalCapacityReleaseReady.WorkItemID},
			AdmissionGrantID:       AdmissionGrantID{value: value.PhysicalCapacityReleaseReady.AdmissionGrantID},
			GrantGeneration:        value.PhysicalCapacityReleaseReady.GrantGeneration,
			RuntimeRunID:           RuntimeRunID{value: value.PhysicalCapacityReleaseReady.RuntimeRunID},
			StartOperationID:       OperationID{value: value.PhysicalCapacityReleaseReady.StartOperationID},
			StartDigest:            value.PhysicalCapacityReleaseReady.StartDigest,
			ReleaseOperationID:     OperationID{value: value.PhysicalCapacityReleaseReady.ReleaseOperationID},
			ReleaseOperationDigest: value.PhysicalCapacityReleaseReady.ReleaseOperationDigest,
			RuntimeRevision:        value.PhysicalCapacityReleaseReady.RuntimeRevision,
			RuntimeFence:           value.PhysicalCapacityReleaseReady.RuntimeFence,
			SandboxLeaseID:         SandboxLeaseID{value: value.PhysicalCapacityReleaseReady.SandboxLeaseID},
			LeaseGeneration:        value.PhysicalCapacityReleaseReady.LeaseGeneration,
			LeaseFence:             value.PhysicalCapacityReleaseReady.LeaseFence,
			SandboxID:              SandboxID{value: value.PhysicalCapacityReleaseReady.SandboxID},
			SandboxGeneration:      value.PhysicalCapacityReleaseReady.SandboxGeneration,
			SandboxFence:           value.PhysicalCapacityReleaseReady.SandboxFence,
			ExecutionNodeID:        ExecutionNodeID{value: value.PhysicalCapacityReleaseReady.ExecutionNodeID},
			NodeCapacityGeneration: value.PhysicalCapacityReleaseReady.NodeCapacityGeneration,
			ResetEvidenceID:        EvidenceID{value: value.PhysicalCapacityReleaseReady.ResetEvidenceID},
			ResetEvidenceDigest:    value.PhysicalCapacityReleaseReady.ResetEvidenceDigest,
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
	}
	record.acceptedStart.OperationID = record.operation.OperationID
	record.acceptedStartDigest = record.operation.Digest
	if !validRuntimeFixture(fixture) || !snapshotVariantsKnown(record) ||
		(record.operation.Generation != 0 && record.operation.Generation != operationGeneration) {
		return nil, newPersistenceError(PersistenceStateCorrupt)
	}
	return record, nil
}

func postgresPrerequisiteFactFromSnapshot(fact PrerequisiteFact) postgresPrerequisiteFactState {
	return postgresPrerequisiteFactState{
		State: fact.State, OperationID: fact.OperationID.String(), RequestDigest: fact.RequestDigest,
		EvidenceID: fact.EvidenceID.String(), EvidenceDigest: fact.EvidenceDigest, Failure: fact.Failure,
	}
}

func prerequisiteFactSnapshotFromPostgres(fact postgresPrerequisiteFactState) PrerequisiteFact {
	return PrerequisiteFact{
		State: fact.State, OperationID: OperationID{value: fact.OperationID}, RequestDigest: fact.RequestDigest,
		EvidenceID: EvidenceID{value: fact.EvidenceID}, EvidenceDigest: fact.EvidenceDigest, Failure: fact.Failure,
	}
}

func postgresReadinessFromSnapshot(readiness RuntimeReadinessSnapshot) postgresReadinessState {
	return postgresReadinessState{
		Lease:           postgresPrerequisiteFactFromSnapshot(readiness.Lease),
		RuntimeBinding:  postgresPrerequisiteFactFromSnapshot(readiness.RuntimeBinding),
		RuntimeView:     postgresPrerequisiteFactFromSnapshot(readiness.RuntimeView),
		ImmutableInputs: postgresPrerequisiteFactFromSnapshot(readiness.ImmutableInputs),
		LLMGateway:      postgresPrerequisiteFactFromSnapshot(readiness.LLMGateway), CapsuleReady: readiness.CapsuleReady,
	}
}

func readinessSnapshotFromPostgres(readiness postgresReadinessState) RuntimeReadinessSnapshot {
	return RuntimeReadinessSnapshot{
		Lease:           prerequisiteFactSnapshotFromPostgres(readiness.Lease),
		RuntimeBinding:  prerequisiteFactSnapshotFromPostgres(readiness.RuntimeBinding),
		RuntimeView:     prerequisiteFactSnapshotFromPostgres(readiness.RuntimeView),
		ImmutableInputs: prerequisiteFactSnapshotFromPostgres(readiness.ImmutableInputs),
		LLMGateway:      prerequisiteFactSnapshotFromPostgres(readiness.LLMGateway), CapsuleReady: readiness.CapsuleReady,
	}
}

func postgresRuntimeViewBindingFromSnapshot(binding RuntimeViewBindingSnapshot) postgresRuntimeViewBindingState {
	if binding == (RuntimeViewBindingSnapshot{}) {
		return postgresRuntimeViewBindingState{}
	}
	return postgresRuntimeViewBindingState{
		RuntimeViewID: binding.RuntimeViewID.String(), OpenOperationID: binding.OpenOperationID.String(),
		OpenRequestDigest:           binding.OpenRequestDigest,
		SandboxLeaseAuthorityDigest: binding.SandboxLeaseAuthorityDigest,
		SandboxLeaseID:              binding.SandboxLeaseID.String(), LeaseGeneration: binding.LeaseGeneration,
		LeaseFence: binding.LeaseFence, Effect: binding.Effect, ExpiresAt: binding.ExpiresAt.UTC(),
		LifecycleGeneration: binding.LifecycleGeneration, LifecycleFence: binding.LifecycleFence,
	}
}

func runtimeViewBindingSnapshotFromPostgres(binding postgresRuntimeViewBindingState) RuntimeViewBindingSnapshot {
	if binding.RuntimeViewID == "" && binding.OpenOperationID == "" && binding.OpenRequestDigest == (Digest{}) &&
		binding.SandboxLeaseAuthorityDigest == (Digest{}) && binding.SandboxLeaseID == "" &&
		binding.LeaseGeneration == 0 && binding.LeaseFence == 0 && binding.Effect == 0 &&
		binding.LifecycleGeneration == 0 && binding.LifecycleFence == 0 {
		return RuntimeViewBindingSnapshot{}
	}
	return RuntimeViewBindingSnapshot{
		RuntimeViewID:   RuntimeViewID{value: binding.RuntimeViewID},
		OpenOperationID: OperationID{value: binding.OpenOperationID}, OpenRequestDigest: binding.OpenRequestDigest,
		SandboxLeaseAuthorityDigest: binding.SandboxLeaseAuthorityDigest,
		SandboxLeaseID:              SandboxLeaseID{value: binding.SandboxLeaseID}, LeaseGeneration: binding.LeaseGeneration,
		LeaseFence: binding.LeaseFence, Effect: binding.Effect, ExpiresAt: binding.ExpiresAt.UTC(),
		LifecycleGeneration: binding.LifecycleGeneration, LifecycleFence: binding.LifecycleFence,
	}
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
