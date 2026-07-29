package runtimeexecution

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"
)

// RuntimeMaintenance is the protected operational interface for exact node,
// lease, containment, and reset intents. It is deliberately separate from the
// public RuntimeExecution interface.
type RuntimeMaintenance interface {
	Maintain(context.Context, RuntimeMaintenanceCommand) (RuntimeMaintenanceDecision, error)
}

type RuntimeMaintenanceCommand interface {
	runtimeMaintenanceCommand()
	maintenanceOperationID() OperationID
	maintenanceDigest() Digest
}

type LeaseRenewalAuthority struct {
	workerAuthorityID       WorkerAuthorityID
	workerGeneration        WorkerGeneration
	nodeAuthorityID         NodeAuthorityID
	authorizationGeneration AuthorizationGeneration
}

func NewLeaseRenewalAuthority(
	workerAuthorityID WorkerAuthorityID,
	workerGeneration WorkerGeneration,
	nodeAuthorityID NodeAuthorityID,
	authorizationGeneration AuthorizationGeneration,
) LeaseRenewalAuthority {
	return LeaseRenewalAuthority{
		workerAuthorityID: workerAuthorityID, workerGeneration: workerGeneration,
		nodeAuthorityID: nodeAuthorityID, authorizationGeneration: authorizationGeneration,
	}
}

type RenewSandboxLeaseInput struct {
	SchemaVersion         SchemaVersion
	OperationID           OperationID
	PersonalWorkspaceID   PersonalWorkspaceID
	RuntimeRunID          RuntimeRunID
	SandboxLeaseID        SandboxLeaseID
	LeaseGeneration       LeaseGeneration
	LeaseFence            LeaseFence
	ExecutionNodeID       ExecutionNodeID
	NodeGeneration        NodeGeneration
	AttestationID         NodeAttestationID
	AttestationGeneration NodeAttestationGeneration
	Authority             LeaseRenewalAuthority
	ReleaseSafetyEpoch    ReleaseSafetyEpoch
	CatalogSafetyEpoch    CatalogSafetyEpoch
	RequestedExpiresAt    time.Time
	OccurredAt            time.Time
}

type RenewSandboxLease struct {
	RenewSandboxLeaseInput
	CanonicalRequestDigest Digest
}

func NewRenewSandboxLease(input RenewSandboxLeaseInput) (RenewSandboxLease, error) {
	input.RequestedExpiresAt = input.RequestedExpiresAt.UTC()
	input.OccurredAt = input.OccurredAt.UTC()
	command := RenewSandboxLease{RenewSandboxLeaseInput: input}
	canonical, ok := canonicalRenewSandboxLease(command)
	if !ok {
		return RenewSandboxLease{}, newError(ErrorInvalidRequest)
	}
	command.CanonicalRequestDigest = Digest(sha256.Sum256(canonical))
	return command, nil
}

func (RenewSandboxLease) runtimeMaintenanceCommand()                  {}
func (command RenewSandboxLease) maintenanceOperationID() OperationID { return command.OperationID }
func (command RenewSandboxLease) maintenanceDigest() Digest           { return command.CanonicalRequestDigest }

type RuntimeMaintenanceDecision struct {
	OperationID                  OperationID
	CanonicalRequestDigest       Digest
	RuntimeRevision              RuntimeRevision
	RuntimeFence                 RuntimeFence
	Lease                        RuntimeLeaseSnapshot
	Node                         RuntimeNodeSnapshot
	Cleanup                      RuntimeLeaseCleanupSnapshot
	PhysicalCapacityReleaseReady PhysicalCapacityReleaseReadyEvidence
	Replayed                     bool
}

type LeaseFencingAuthority struct {
	id         NodeAuthorityID
	generation AuthorizationGeneration
}

func NewSecurityLeaseFencingAuthority(id NodeAuthorityID, generation AuthorizationGeneration) LeaseFencingAuthority {
	return LeaseFencingAuthority{id: id, generation: generation}
}

type LeaseFenceReason uint8

const (
	LeaseFenceRevoked LeaseFenceReason = iota + 1
	LeaseFenceExpired
	LeaseFenceNodeLost
)

type FenceSandboxLeaseInput struct {
	SchemaVersion        SchemaVersion
	OperationID          OperationID
	PersonalWorkspaceID  PersonalWorkspaceID
	RuntimeRunID         RuntimeRunID
	ExpectedRuntimeFence RuntimeFence
	SandboxLeaseID       SandboxLeaseID
	LeaseGeneration      LeaseGeneration
	LeaseFence           LeaseFence
	ExecutionNodeID      ExecutionNodeID
	NodeGeneration       NodeGeneration
	Reason               LeaseFenceReason
	Authority            LeaseFencingAuthority
	ReleaseSafetyEpoch   ReleaseSafetyEpoch
	OccurredAt           time.Time
}

type FenceSandboxLease struct {
	FenceSandboxLeaseInput
	CanonicalRequestDigest Digest
}

func NewFenceSandboxLease(input FenceSandboxLeaseInput) (FenceSandboxLease, error) {
	input.OccurredAt = input.OccurredAt.UTC()
	command := FenceSandboxLease{FenceSandboxLeaseInput: input}
	canonical, ok := canonicalFenceSandboxLease(command)
	if !ok {
		return FenceSandboxLease{}, newError(ErrorInvalidRequest)
	}
	command.CanonicalRequestDigest = Digest(sha256.Sum256(canonical))
	return command, nil
}

func (FenceSandboxLease) runtimeMaintenanceCommand()                  {}
func (command FenceSandboxLease) maintenanceOperationID() OperationID { return command.OperationID }
func (command FenceSandboxLease) maintenanceDigest() Digest           { return command.CanonicalRequestDigest }

type SandboxResetAuthority struct {
	id         NodeAuthorityID
	generation AuthorizationGeneration
}

func NewSandboxResetAuthority(id NodeAuthorityID, generation AuthorizationGeneration) SandboxResetAuthority {
	return SandboxResetAuthority{id: id, generation: generation}
}

type ConfirmSandboxResetInput struct {
	SchemaVersion              SchemaVersion
	OperationID                OperationID
	PersonalWorkspaceID        PersonalWorkspaceID
	RuntimeRunID               RuntimeRunID
	ExpectedRuntimeFence       RuntimeFence
	SandboxLeaseID             SandboxLeaseID
	LeaseGeneration            LeaseGeneration
	LeaseFence                 LeaseFence
	SandboxID                  SandboxID
	SandboxGeneration          SandboxGeneration
	SandboxFence               SandboxFence
	ExecutionNodeID            ExecutionNodeID
	NodeGeneration             NodeGeneration
	Authority                  SandboxResetAuthority
	EvidenceID                 EvidenceID
	EvidenceDigest             Digest
	ProcessStopped             bool
	ChildProcessesStopped      bool
	SecretsRevoked             bool
	NetworkRemoved             bool
	ContainmentEstablished     bool
	ResetCompleted             bool
	NoUnresolvedOccupancy      bool
	NoStaleWorkerAuthority     bool
	NoPriorTaskBytes           bool
	NoPriorSecrets             bool
	NoWritableCacheMutations   bool
	NoLogsOrTranscripts        bool
	NoPriorEvidence            bool
	NoProcessState             bool
	NoNetworkState             bool
	NoPriorOperationIdentities bool
	OccurredAt                 time.Time
}

type ConfirmSandboxReset struct {
	ConfirmSandboxResetInput
	CanonicalRequestDigest Digest
}

func NewConfirmSandboxReset(input ConfirmSandboxResetInput) (ConfirmSandboxReset, error) {
	input.OccurredAt = input.OccurredAt.UTC()
	command := ConfirmSandboxReset{ConfirmSandboxResetInput: input}
	canonical, ok := canonicalConfirmSandboxReset(command)
	if !ok {
		return ConfirmSandboxReset{}, newError(ErrorInvalidRequest)
	}
	command.CanonicalRequestDigest = Digest(sha256.Sum256(canonical))
	return command, nil
}

func (ConfirmSandboxReset) runtimeMaintenanceCommand()                  {}
func (command ConfirmSandboxReset) maintenanceOperationID() OperationID { return command.OperationID }
func (command ConfirmSandboxReset) maintenanceDigest() Digest           { return command.CanonicalRequestDigest }

type AttestExecutionNodeInput struct {
	SchemaVersion           SchemaVersion
	OperationID             OperationID
	ExecutionNodeID         ExecutionNodeID
	NodeGeneration          NodeGeneration
	AttestationID           NodeAttestationID
	AttestationGeneration   NodeAttestationGeneration
	AttestedAt              time.Time
	ExpiresAt               time.Time
	ResourceClassID         ResourceClassID
	ExecutionPolicyID       ExecutionPolicyID
	NodeAuthorityID         NodeAuthorityID
	WorkerAuthorityID       WorkerAuthorityID
	WorkerGeneration        WorkerGeneration
	AuthorizationGeneration AuthorizationGeneration
	AuthorizationExpiresAt  time.Time
	ReleaseSafetyEpoch      ReleaseSafetyEpoch
	CatalogSafetyEpoch      CatalogSafetyEpoch
	ResetEvidenceID         EvidenceID
	ResetEvidenceDigest     Digest
	OccurredAt              time.Time
}

type AttestExecutionNode struct {
	AttestExecutionNodeInput
	CanonicalRequestDigest Digest
}

func NewAttestExecutionNode(input AttestExecutionNodeInput) (AttestExecutionNode, error) {
	input.AttestedAt, input.ExpiresAt = input.AttestedAt.UTC(), input.ExpiresAt.UTC()
	input.AuthorizationExpiresAt, input.OccurredAt = input.AuthorizationExpiresAt.UTC(), input.OccurredAt.UTC()
	command := AttestExecutionNode{AttestExecutionNodeInput: input}
	canonical, ok := canonicalAttestExecutionNode(command)
	if !ok {
		return AttestExecutionNode{}, newError(ErrorInvalidRequest)
	}
	command.CanonicalRequestDigest = Digest(sha256.Sum256(canonical))
	return command, nil
}

func (AttestExecutionNode) runtimeMaintenanceCommand()                  {}
func (command AttestExecutionNode) maintenanceOperationID() OperationID { return command.OperationID }
func (command AttestExecutionNode) maintenanceDigest() Digest           { return command.CanonicalRequestDigest }

func canonicalRenewSandboxLease(command RenewSandboxLease) ([]byte, bool) {
	authority := command.Authority
	if command.SchemaVersion.Major() != SchemaV1.Major() || !validOpaqueID(command.OperationID.String()) ||
		!validOpaqueID(command.PersonalWorkspaceID.String()) || !validOpaqueID(command.RuntimeRunID.String()) ||
		!validOpaqueID(command.SandboxLeaseID.String()) || command.LeaseGeneration == 0 || command.LeaseFence == 0 ||
		!validOpaqueID(command.ExecutionNodeID.String()) || command.NodeGeneration == 0 ||
		!validOpaqueID(command.AttestationID.String()) || command.AttestationGeneration == 0 ||
		!validOpaqueID(authority.workerAuthorityID.String()) || authority.workerGeneration == 0 ||
		!validOpaqueID(authority.nodeAuthorityID.String()) || authority.authorizationGeneration == 0 ||
		command.ReleaseSafetyEpoch == 0 || command.RequestedExpiresAt.IsZero() || command.OccurredAt.IsZero() {
		return nil, false
	}
	canonical := strings.Join([]string{
		"slidesmith.runtime-execution.lease-renewal/v1", command.OperationID.String(),
		command.PersonalWorkspaceID.String(), command.RuntimeRunID.String(), command.SandboxLeaseID.String(),
		fmt.Sprint(command.LeaseGeneration), fmt.Sprint(command.LeaseFence), command.ExecutionNodeID.String(),
		fmt.Sprint(command.NodeGeneration), command.AttestationID.String(), fmt.Sprint(command.AttestationGeneration),
		authority.workerAuthorityID.String(), fmt.Sprint(authority.workerGeneration), authority.nodeAuthorityID.String(),
		fmt.Sprint(authority.authorizationGeneration), fmt.Sprint(command.ReleaseSafetyEpoch),
		fmt.Sprint(command.CatalogSafetyEpoch), command.RequestedExpiresAt.Format(time.RFC3339Nano),
		command.OccurredAt.Format(time.RFC3339Nano),
	}, "\n")
	return []byte(canonical), true
}

func canonicalFenceSandboxLease(command FenceSandboxLease) ([]byte, bool) {
	input := command.FenceSandboxLeaseInput
	if input.SchemaVersion.Major() != SchemaV1.Major() || !validOpaqueID(input.OperationID.String()) ||
		!validOpaqueID(input.PersonalWorkspaceID.String()) || !validOpaqueID(input.RuntimeRunID.String()) ||
		input.ExpectedRuntimeFence == 0 || !validOpaqueID(input.SandboxLeaseID.String()) ||
		input.LeaseGeneration == 0 || input.LeaseFence == 0 || !validOpaqueID(input.ExecutionNodeID.String()) ||
		input.NodeGeneration == 0 || input.Reason < LeaseFenceRevoked || input.Reason > LeaseFenceNodeLost ||
		!validOpaqueID(input.Authority.id.String()) || input.Authority.generation == 0 ||
		input.ReleaseSafetyEpoch == 0 || input.OccurredAt.IsZero() {
		return nil, false
	}
	return []byte(strings.Join([]string{
		"slidesmith.runtime-execution.lease-fence/v1", input.OperationID.String(), input.PersonalWorkspaceID.String(),
		input.RuntimeRunID.String(), fmt.Sprint(input.ExpectedRuntimeFence), input.SandboxLeaseID.String(),
		fmt.Sprint(input.LeaseGeneration), fmt.Sprint(input.LeaseFence), input.ExecutionNodeID.String(),
		fmt.Sprint(input.NodeGeneration), fmt.Sprint(input.Reason), input.Authority.id.String(),
		fmt.Sprint(input.Authority.generation), fmt.Sprint(input.ReleaseSafetyEpoch),
		input.OccurredAt.Format(time.RFC3339Nano),
	}, "\n")), true
}

func canonicalConfirmSandboxReset(command ConfirmSandboxReset) ([]byte, bool) {
	input := command.ConfirmSandboxResetInput
	if input.SchemaVersion.Major() != SchemaV1.Major() || !validOpaqueID(input.OperationID.String()) ||
		!validOpaqueID(input.PersonalWorkspaceID.String()) || !validOpaqueID(input.RuntimeRunID.String()) ||
		input.ExpectedRuntimeFence == 0 || !validOpaqueID(input.SandboxLeaseID.String()) ||
		input.LeaseGeneration == 0 || input.LeaseFence == 0 || !validOpaqueID(input.SandboxID.String()) ||
		input.SandboxGeneration == 0 || input.SandboxFence == 0 || !validOpaqueID(input.ExecutionNodeID.String()) ||
		input.NodeGeneration == 0 || !validOpaqueID(input.Authority.id.String()) || input.Authority.generation == 0 ||
		!validOpaqueID(input.EvidenceID.String()) || input.EvidenceDigest == (Digest{}) || input.OccurredAt.IsZero() {
		return nil, false
	}
	booleans := []bool{
		input.ProcessStopped, input.ChildProcessesStopped, input.SecretsRevoked, input.NetworkRemoved,
		input.ContainmentEstablished, input.ResetCompleted, input.NoUnresolvedOccupancy,
		input.NoStaleWorkerAuthority, input.NoPriorTaskBytes, input.NoPriorSecrets,
		input.NoWritableCacheMutations, input.NoLogsOrTranscripts, input.NoPriorEvidence,
		input.NoProcessState, input.NoNetworkState, input.NoPriorOperationIdentities,
	}
	parts := []string{
		"slidesmith.runtime-execution.sandbox-reset/v1", input.OperationID.String(), input.PersonalWorkspaceID.String(),
		input.RuntimeRunID.String(), fmt.Sprint(input.ExpectedRuntimeFence), input.SandboxLeaseID.String(),
		fmt.Sprint(input.LeaseGeneration), fmt.Sprint(input.LeaseFence), input.SandboxID.String(),
		fmt.Sprint(input.SandboxGeneration), fmt.Sprint(input.SandboxFence), input.ExecutionNodeID.String(),
		fmt.Sprint(input.NodeGeneration), input.Authority.id.String(), fmt.Sprint(input.Authority.generation),
		input.EvidenceID.String(), input.EvidenceDigest.String(), input.OccurredAt.Format(time.RFC3339Nano),
	}
	for _, value := range booleans {
		parts = append(parts, fmt.Sprint(value))
	}
	return []byte(strings.Join(parts, "\n")), true
}

func canonicalAttestExecutionNode(command AttestExecutionNode) ([]byte, bool) {
	input := command.AttestExecutionNodeInput
	if input.SchemaVersion.Major() != SchemaV1.Major() || !validOpaqueID(input.OperationID.String()) ||
		!validOpaqueID(input.ExecutionNodeID.String()) || input.NodeGeneration == 0 ||
		!validOpaqueID(input.AttestationID.String()) || input.AttestationGeneration == 0 || input.AttestedAt.IsZero() ||
		!input.ExpiresAt.After(input.AttestedAt) || !validOpaqueID(input.ResourceClassID.String()) ||
		!validOpaqueID(input.ExecutionPolicyID.String()) || !validOpaqueID(input.NodeAuthorityID.String()) ||
		!validOpaqueID(input.WorkerAuthorityID.String()) || input.WorkerGeneration == 0 ||
		input.AuthorizationGeneration == 0 || !input.AuthorizationExpiresAt.After(input.AttestedAt) ||
		input.ReleaseSafetyEpoch == 0 || !validOpaqueID(input.ResetEvidenceID.String()) ||
		input.ResetEvidenceDigest == (Digest{}) || input.OccurredAt.IsZero() {
		return nil, false
	}
	return []byte(strings.Join([]string{
		"slidesmith.runtime-execution.node-attestation/v1", input.OperationID.String(), input.ExecutionNodeID.String(),
		fmt.Sprint(input.NodeGeneration), input.AttestationID.String(), fmt.Sprint(input.AttestationGeneration),
		input.AttestedAt.Format(time.RFC3339Nano), input.ExpiresAt.Format(time.RFC3339Nano),
		input.ResourceClassID.String(), input.ExecutionPolicyID.String(), input.NodeAuthorityID.String(),
		input.WorkerAuthorityID.String(), fmt.Sprint(input.WorkerGeneration), fmt.Sprint(input.AuthorizationGeneration),
		input.AuthorizationExpiresAt.Format(time.RFC3339Nano), fmt.Sprint(input.ReleaseSafetyEpoch),
		fmt.Sprint(input.CatalogSafetyEpoch), input.ResetEvidenceID.String(), input.ResetEvidenceDigest.String(),
		input.OccurredAt.Format(time.RFC3339Nano),
	}, "\n")), true
}

func (engine *invariantEngine) Maintain(
	ctx context.Context,
	command RuntimeMaintenanceCommand,
) (RuntimeMaintenanceDecision, error) {
	if command == nil || ctx == nil || ctx.Err() != nil || engine.controls.isCrashed() {
		return RuntimeMaintenanceDecision{}, newError(ErrorDependencyUnavailable)
	}
	switch typed := command.(type) {
	case RenewSandboxLease:
		canonical, valid := canonicalRenewSandboxLease(typed)
		if !valid {
			return RuntimeMaintenanceDecision{}, newError(ErrorInvalidRequest)
		}
		digest := Digest(sha256.Sum256(canonical))
		if digest != typed.CanonicalRequestDigest {
			return RuntimeMaintenanceDecision{}, newError(ErrorIntegrityConflict)
		}
		return engine.renewSandboxLease(typed)
	case FenceSandboxLease:
		canonical, valid := canonicalFenceSandboxLease(typed)
		if !valid || Digest(sha256.Sum256(canonical)) != typed.CanonicalRequestDigest {
			return RuntimeMaintenanceDecision{}, newError(ErrorIntegrityConflict)
		}
		return engine.fenceSandboxLease(typed)
	case ConfirmSandboxReset:
		canonical, valid := canonicalConfirmSandboxReset(typed)
		if !valid || Digest(sha256.Sum256(canonical)) != typed.CanonicalRequestDigest {
			return RuntimeMaintenanceDecision{}, newError(ErrorIntegrityConflict)
		}
		return engine.confirmSandboxReset(typed)
	case AttestExecutionNode:
		canonical, valid := canonicalAttestExecutionNode(typed)
		if !valid || Digest(sha256.Sum256(canonical)) != typed.CanonicalRequestDigest {
			return RuntimeMaintenanceDecision{}, newError(ErrorIntegrityConflict)
		}
		return engine.attestExecutionNode(typed)
	default:
		return RuntimeMaintenanceDecision{}, newError(ErrorInvalidRequest)
	}
}

func (engine *invariantEngine) replayMaintenanceLocked(
	operationID OperationID,
	digest Digest,
) (RuntimeMaintenanceDecision, bool, error) {
	retained, exists := engine.store.maintenance[operationID]
	if !exists {
		return RuntimeMaintenanceDecision{}, false, nil
	}
	if retained.CanonicalRequestDigest != digest {
		return RuntimeMaintenanceDecision{}, true, newError(ErrorIntegrityConflict)
	}
	retained.Replayed = true
	return retained, true, nil
}

func (engine *invariantEngine) renewSandboxLease(
	command RenewSandboxLease,
) (RuntimeMaintenanceDecision, error) {
	engine.store.mu.Lock()
	defer engine.store.mu.Unlock()
	if retained, exists, err := engine.replayMaintenanceLocked(command.OperationID, command.CanonicalRequestDigest); exists {
		return retained, err
	}
	record := engine.store.runtimes[command.RuntimeRunID]
	if record == nil || record.fixture.PersonalWorkspaceID != command.PersonalWorkspaceID {
		return RuntimeMaintenanceDecision{}, newError(ErrorAuthorizationDenied)
	}
	now := engine.clock.current()
	node := engine.store.nodes[command.ExecutionNodeID]
	lease := record.lease
	authority := command.Authority
	if record.fixture.State == RuntimeTerminal || lease.AcquireStatus != LeaseGranted ||
		lease.Disposition != LeaseActive || lease.LeaseID != command.SandboxLeaseID ||
		lease.Generation != command.LeaseGeneration || lease.Fence != command.LeaseFence ||
		lease.WorkerAuthorityID != authority.workerAuthorityID || lease.WorkerGeneration != authority.workerGeneration ||
		lease.NodeAuthorityID != authority.nodeAuthorityID ||
		lease.AuthorizationGeneration != authority.authorizationGeneration ||
		command.ReleaseSafetyEpoch != record.fixture.SafetyEpoch ||
		command.CatalogSafetyEpoch != record.catalogSafetyEpoch || node == nil ||
		node.ExecutionNodeID != record.operation.ExecutionNodeID || node.Generation != command.NodeGeneration ||
		node.AttestationID != command.AttestationID || node.AttestationGeneration != command.AttestationGeneration ||
		node.Readiness != NodeReady || node.Quarantined || node.Occupancy != NodeOccupied ||
		node.ActiveRuntimeRunID != command.RuntimeRunID || node.ActiveLeaseID != command.SandboxLeaseID ||
		!now.Before(node.ExpiresAt) || !now.Before(node.AuthorizationExpiresAt) ||
		!now.Before(lease.ExpiresAt) || !now.Before(lease.AuthorizationExpiresAt) {
		return RuntimeMaintenanceDecision{}, newError(ErrorIntegrityConflict)
	}
	maximumExpiry := earliestTime(record.deadline, record.leaseAcquireBy, node.ExpiresAt,
		node.AuthorizationExpiresAt, lease.AuthorizationExpiresAt)
	if !command.RequestedExpiresAt.After(lease.ExpiresAt) || command.RequestedExpiresAt.After(maximumExpiry) ||
		command.OccurredAt.After(now) {
		return RuntimeMaintenanceDecision{}, newError(ErrorIntegrityConflict)
	}
	record.fixture.RuntimeRevision++
	record.lease.Generation++
	record.lease.Fence++
	record.lease.ExpiresAt = command.RequestedExpiresAt
	record.node = nodeSnapshot(*node)
	decision := RuntimeMaintenanceDecision{
		OperationID: command.OperationID, CanonicalRequestDigest: command.CanonicalRequestDigest,
		RuntimeRevision: record.fixture.RuntimeRevision, RuntimeFence: record.fixture.RuntimeFence,
		Lease: record.lease, Node: record.node,
	}
	engine.store.maintenance[command.OperationID] = decision
	return decision, nil
}

func (engine *invariantEngine) fenceSandboxLease(
	command FenceSandboxLease,
) (RuntimeMaintenanceDecision, error) {
	engine.store.mu.Lock()
	defer engine.store.mu.Unlock()
	if retained, exists, err := engine.replayMaintenanceLocked(command.OperationID, command.CanonicalRequestDigest); exists {
		return retained, err
	}
	record := engine.store.runtimes[command.RuntimeRunID]
	if record == nil || record.fixture.PersonalWorkspaceID != command.PersonalWorkspaceID {
		return RuntimeMaintenanceDecision{}, newError(ErrorAuthorizationDenied)
	}
	now := engine.clock.current()
	node := engine.store.nodes[command.ExecutionNodeID]
	lease := record.lease
	if lease.AcquireStatus != LeaseGranted || lease.Disposition != LeaseActive ||
		command.ExpectedRuntimeFence != record.fixture.RuntimeFence || lease.LeaseID != command.SandboxLeaseID ||
		lease.Generation != command.LeaseGeneration || lease.Fence != command.LeaseFence ||
		record.operation.ExecutionNodeID != command.ExecutionNodeID ||
		NodeGeneration(record.operation.NodeCapacityGeneration) != command.NodeGeneration ||
		command.ReleaseSafetyEpoch != record.fixture.SafetyEpoch || node == nil ||
		node.ActiveRuntimeRunID != command.RuntimeRunID || node.ActiveLeaseID != command.SandboxLeaseID ||
		command.OccurredAt.After(now) || command.Reason == LeaseFenceExpired && now.Before(lease.ExpiresAt) {
		return RuntimeMaintenanceDecision{}, newError(ErrorIntegrityConflict)
	}
	record.fixture.RuntimeRevision++
	record.fixture.RuntimeFence++
	record.fixture.State = RuntimeStopping
	record.lease.Generation++
	record.lease.Fence++
	record.lease.SandboxFence++
	if command.Reason == LeaseFenceExpired {
		record.lease.Disposition = LeaseExpired
	} else {
		record.lease.Disposition = LeaseRevoked
	}
	node.Occupancy = NodeOccupancyUnknown
	node.Quarantined = true
	node.Containment = ContainmentPending
	node.Reset = ResetRequired
	if command.Reason == LeaseFenceExpired || command.Reason == LeaseFenceNodeLost {
		node.Readiness = NodeUnavailable
	}
	record.node = nodeSnapshot(*node)
	record.capacity.Physical = PhysicalCapacityUnknownOrQuarantined
	record.cleanup = RuntimeLeaseCleanupSnapshot{
		Status: LeaseCleanupPending, OperationID: command.OperationID,
		CanonicalRequestDigest: command.CanonicalRequestDigest, StopMainProcess: true,
		StopChildProcesses: true, RevokeSecrets: true, RemoveNetwork: true,
		FenceRuntimeView: true, ReconcileContainment: true,
	}
	decision := RuntimeMaintenanceDecision{
		OperationID: command.OperationID, CanonicalRequestDigest: command.CanonicalRequestDigest,
		RuntimeRevision: record.fixture.RuntimeRevision, RuntimeFence: record.fixture.RuntimeFence,
		Lease: record.lease, Node: record.node, Cleanup: record.cleanup,
	}
	engine.store.maintenance[command.OperationID] = decision
	return decision, nil
}

func (engine *invariantEngine) confirmSandboxReset(
	command ConfirmSandboxReset,
) (RuntimeMaintenanceDecision, error) {
	engine.store.mu.Lock()
	defer engine.store.mu.Unlock()
	if retained, exists, err := engine.replayMaintenanceLocked(command.OperationID, command.CanonicalRequestDigest); exists {
		return retained, err
	}
	record := engine.store.runtimes[command.RuntimeRunID]
	if record == nil || record.fixture.PersonalWorkspaceID != command.PersonalWorkspaceID {
		return RuntimeMaintenanceDecision{}, newError(ErrorAuthorizationDenied)
	}
	node := engine.store.nodes[command.ExecutionNodeID]
	lease := record.lease
	if lease.AcquireStatus != LeaseGranted || lease.Disposition != LeaseRevoked && lease.Disposition != LeaseExpired ||
		command.ExpectedRuntimeFence != record.fixture.RuntimeFence || lease.LeaseID != command.SandboxLeaseID ||
		lease.Generation != command.LeaseGeneration || lease.Fence != command.LeaseFence ||
		lease.SandboxID != command.SandboxID || lease.SandboxGeneration != command.SandboxGeneration ||
		lease.SandboxFence != command.SandboxFence || record.operation.ExecutionNodeID != command.ExecutionNodeID ||
		NodeGeneration(record.operation.NodeCapacityGeneration) != command.NodeGeneration || node == nil ||
		node.ActiveRuntimeRunID != command.RuntimeRunID || node.ActiveLeaseID != command.SandboxLeaseID ||
		command.OccurredAt.After(engine.clock.current()) || !completeResetEvidence(command.ConfirmSandboxResetInput) {
		return RuntimeMaintenanceDecision{}, newError(ErrorIntegrityConflict)
	}
	record.fixture.RuntimeRevision++
	record.lease.Generation++
	record.lease.Fence++
	record.lease.SandboxFence++
	record.lease.Disposition = LeaseReleased
	node.Occupancy = NodeUnoccupied
	node.Containment = ContainmentEstablished
	node.Reset = ResetCompleted
	node.ActiveRuntimeRunID = RuntimeRunID{}
	node.ActiveLeaseID = SandboxLeaseID{}
	node.LastSandboxGeneration = record.lease.SandboxGeneration
	node.LastSandboxFence = record.lease.SandboxFence
	node.LastResetEvidenceID = command.EvidenceID
	node.LastResetEvidenceDigest = command.EvidenceDigest
	record.node = nodeSnapshot(*node)
	record.capacity.Physical = PhysicalCapacityReleaseReady
	record.cleanup.Status = LeaseCleanupCompleted
	release := PhysicalCapacityReleaseReadyEvidence{
		WorkItemID: record.operation.WorkItemID, AdmissionGrantID: record.operation.AdmissionGrantID,
		GrantGeneration: record.operation.GrantGeneration, RuntimeRunID: record.fixture.RuntimeRunID,
		StartOperationID: record.acceptedStart.OperationID, StartDigest: record.acceptedStartDigest,
		ReleaseOperationID: command.OperationID, ReleaseOperationDigest: command.CanonicalRequestDigest,
		RuntimeRevision: record.fixture.RuntimeRevision, RuntimeFence: record.fixture.RuntimeFence,
		SandboxLeaseID: record.lease.LeaseID, LeaseGeneration: record.lease.Generation,
		LeaseFence: record.lease.Fence, SandboxID: record.lease.SandboxID,
		SandboxGeneration: record.lease.SandboxGeneration, SandboxFence: record.lease.SandboxFence,
		ExecutionNodeID:        record.operation.ExecutionNodeID,
		NodeCapacityGeneration: record.operation.NodeCapacityGeneration,
		ResetEvidenceID:        command.EvidenceID, ResetEvidenceDigest: command.EvidenceDigest,
	}
	record.capacityEvidence.PhysicalCapacityReleaseReady = release
	decision := RuntimeMaintenanceDecision{
		OperationID: command.OperationID, CanonicalRequestDigest: command.CanonicalRequestDigest,
		RuntimeRevision: record.fixture.RuntimeRevision, RuntimeFence: record.fixture.RuntimeFence,
		Lease: record.lease, Node: record.node, Cleanup: record.cleanup,
		PhysicalCapacityReleaseReady: release,
	}
	engine.store.maintenance[command.OperationID] = decision
	return decision, nil
}

func completeResetEvidence(input ConfirmSandboxResetInput) bool {
	return input.ProcessStopped && input.ChildProcessesStopped && input.SecretsRevoked && input.NetworkRemoved &&
		input.ContainmentEstablished && input.ResetCompleted && input.NoUnresolvedOccupancy &&
		input.NoStaleWorkerAuthority && input.NoPriorTaskBytes && input.NoPriorSecrets &&
		input.NoWritableCacheMutations && input.NoLogsOrTranscripts && input.NoPriorEvidence &&
		input.NoProcessState && input.NoNetworkState && input.NoPriorOperationIdentities
}

func (engine *invariantEngine) attestExecutionNode(
	command AttestExecutionNode,
) (RuntimeMaintenanceDecision, error) {
	engine.store.mu.Lock()
	defer engine.store.mu.Unlock()
	if retained, exists, err := engine.replayMaintenanceLocked(command.OperationID, command.CanonicalRequestDigest); exists {
		return retained, err
	}
	node := engine.store.nodes[command.ExecutionNodeID]
	if node == nil || !node.Quarantined || node.Occupancy != NodeUnoccupied ||
		command.NodeGeneration < node.Generation || command.NodeGeneration == node.Generation &&
		command.AttestationGeneration <= node.AttestationGeneration ||
		command.ResetEvidenceID != node.LastResetEvidenceID ||
		command.ResetEvidenceDigest != node.LastResetEvidenceDigest || command.AttestedAt.After(engine.clock.current()) ||
		command.ResourceClassID != node.ResourceClassID || command.ExecutionPolicyID != node.ExecutionPolicyID ||
		command.ReleaseSafetyEpoch != node.ReleaseSafetyEpoch {
		return RuntimeMaintenanceDecision{}, newError(ErrorIntegrityConflict)
	}
	node.Generation = command.NodeGeneration
	node.Readiness = NodeReady
	node.AttestationID = command.AttestationID
	node.AttestationGeneration = command.AttestationGeneration
	node.AttestedAt = command.AttestedAt
	node.ExpiresAt = command.ExpiresAt
	node.NodeAuthorityID = command.NodeAuthorityID
	node.WorkerAuthorityID = command.WorkerAuthorityID
	node.WorkerGeneration = command.WorkerGeneration
	node.AuthorizationGeneration = command.AuthorizationGeneration
	node.AuthorizationExpiresAt = command.AuthorizationExpiresAt
	node.CatalogSafetyEpoch = command.CatalogSafetyEpoch
	node.Quarantined = false
	node.Containment = ContainmentEstablished
	node.Reset = ResetCompleted
	decision := RuntimeMaintenanceDecision{
		OperationID: command.OperationID, CanonicalRequestDigest: command.CanonicalRequestDigest,
		Node: nodeSnapshot(*node),
	}
	engine.store.maintenance[command.OperationID] = decision
	return decision, nil
}

var _ RuntimeMaintenance = (*invariantEngine)(nil)
