package runtimeexecution

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/taskworkspace"
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

type RuntimeMaintenanceAuthorityKind uint8

const (
	MaintenanceAuthorityWorker RuntimeMaintenanceAuthorityKind = iota + 1
	MaintenanceAuthorityScheduler
	MaintenanceAuthoritySecurity
	MaintenanceAuthorityRecovery
	MaintenanceAuthorityCleanup
)

type maintenanceCallerAuthority struct {
	kind       RuntimeMaintenanceAuthorityKind
	id         AuthorityID
	generation AuthorizationGeneration
}

type RuntimeMaintenanceAuthorityBinding struct {
	executionNodeID ExecutionNodeID
	caller          maintenanceCallerAuthority
}

func BindLeaseFencingAuthority(
	executionNodeID ExecutionNodeID,
	authority LeaseFencingAuthority,
) RuntimeMaintenanceAuthorityBinding {
	return RuntimeMaintenanceAuthorityBinding{executionNodeID: executionNodeID, caller: authority.caller()}
}

func BindSandboxResetAuthority(
	executionNodeID ExecutionNodeID,
	authority SandboxResetAuthority,
) RuntimeMaintenanceAuthorityBinding {
	return RuntimeMaintenanceAuthorityBinding{executionNodeID: executionNodeID, caller: authority.caller()}
}

func BindNodeAttestationAuthority(
	executionNodeID ExecutionNodeID,
	authority NodeAttestationAuthority,
) RuntimeMaintenanceAuthorityBinding {
	return RuntimeMaintenanceAuthorityBinding{executionNodeID: executionNodeID, caller: authority.caller()}
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

func (authority LeaseRenewalAuthority) caller() maintenanceCallerAuthority {
	return maintenanceCallerAuthority{
		kind: MaintenanceAuthorityWorker,
		id:   AuthorityID{value: authority.workerAuthorityID.String()}, generation: AuthorizationGeneration(authority.workerGeneration),
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
	kind       RuntimeMaintenanceAuthorityKind
	id         AuthorityID
	generation AuthorizationGeneration
}

func NewSecurityLeaseFencingAuthority(id AuthorityID, generation AuthorizationGeneration) LeaseFencingAuthority {
	return LeaseFencingAuthority{kind: MaintenanceAuthoritySecurity, id: id, generation: generation}
}

func NewRecoveryLeaseFencingAuthority(id AuthorityID, generation AuthorizationGeneration) LeaseFencingAuthority {
	return LeaseFencingAuthority{kind: MaintenanceAuthorityRecovery, id: id, generation: generation}
}

func (authority LeaseFencingAuthority) caller() maintenanceCallerAuthority {
	return maintenanceCallerAuthority{kind: authority.kind, id: authority.id, generation: authority.generation}
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
	id         AuthorityID
	generation AuthorizationGeneration
}

func NewSandboxResetAuthority(id AuthorityID, generation AuthorizationGeneration) SandboxResetAuthority {
	return SandboxResetAuthority{id: id, generation: generation}
}

func (authority SandboxResetAuthority) caller() maintenanceCallerAuthority {
	return maintenanceCallerAuthority{kind: MaintenanceAuthorityCleanup, id: authority.id, generation: authority.generation}
}

type NodeAttestationAuthority struct {
	kind       RuntimeMaintenanceAuthorityKind
	id         AuthorityID
	generation AuthorizationGeneration
}

func NewRecoveryNodeAttestationAuthority(id AuthorityID, generation AuthorizationGeneration) NodeAttestationAuthority {
	return NodeAttestationAuthority{kind: MaintenanceAuthorityRecovery, id: id, generation: generation}
}

func (authority NodeAttestationAuthority) caller() maintenanceCallerAuthority {
	return maintenanceCallerAuthority{kind: authority.kind, id: authority.id, generation: authority.generation}
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
	Authority               NodeAttestationAuthority
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
		!validLeaseFencingAuthority(input.Authority) ||
		input.ReleaseSafetyEpoch == 0 || input.OccurredAt.IsZero() {
		return nil, false
	}
	return []byte(strings.Join([]string{
		"slidesmith.runtime-execution.lease-fence/v1", input.OperationID.String(), input.PersonalWorkspaceID.String(),
		input.RuntimeRunID.String(), fmt.Sprint(input.ExpectedRuntimeFence), input.SandboxLeaseID.String(),
		fmt.Sprint(input.LeaseGeneration), fmt.Sprint(input.LeaseFence), input.ExecutionNodeID.String(),
		fmt.Sprint(input.NodeGeneration), fmt.Sprint(input.Reason), fmt.Sprint(input.Authority.kind), input.Authority.id.String(),
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
		input.NodeGeneration == 0 || !validSandboxResetAuthority(input.Authority) ||
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
		fmt.Sprint(input.NodeGeneration), fmt.Sprint(MaintenanceAuthorityCleanup), input.Authority.id.String(), fmt.Sprint(input.Authority.generation),
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
		!validNodeAttestationAuthority(input.Authority) ||
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
		"slidesmith.runtime-execution.node-attestation/v1", input.OperationID.String(), fmt.Sprint(input.Authority.kind),
		input.Authority.id.String(), fmt.Sprint(input.Authority.generation), input.ExecutionNodeID.String(),
		fmt.Sprint(input.NodeGeneration), input.AttestationID.String(), fmt.Sprint(input.AttestationGeneration),
		input.AttestedAt.Format(time.RFC3339Nano), input.ExpiresAt.Format(time.RFC3339Nano),
		input.ResourceClassID.String(), input.ExecutionPolicyID.String(), input.NodeAuthorityID.String(),
		input.WorkerAuthorityID.String(), fmt.Sprint(input.WorkerGeneration), fmt.Sprint(input.AuthorizationGeneration),
		input.AuthorizationExpiresAt.Format(time.RFC3339Nano), fmt.Sprint(input.ReleaseSafetyEpoch),
		fmt.Sprint(input.CatalogSafetyEpoch), input.ResetEvidenceID.String(), input.ResetEvidenceDigest.String(),
		input.OccurredAt.Format(time.RFC3339Nano),
	}, "\n")), true
}

func validMaintenanceCaller(authority maintenanceCallerAuthority) bool {
	return authority.kind >= MaintenanceAuthorityWorker && authority.kind <= MaintenanceAuthorityCleanup &&
		validOpaqueID(authority.id.String()) && authority.generation > 0
}

func validLeaseFencingAuthority(authority LeaseFencingAuthority) bool {
	return (authority.kind == MaintenanceAuthoritySecurity || authority.kind == MaintenanceAuthorityRecovery) &&
		validMaintenanceCaller(authority.caller())
}

func validSandboxResetAuthority(authority SandboxResetAuthority) bool {
	return validMaintenanceCaller(authority.caller())
}

func validNodeAttestationAuthority(authority NodeAttestationAuthority) bool {
	return authority.kind == MaintenanceAuthorityRecovery && validMaintenanceCaller(authority.caller())
}

func validMaintenanceAuthorityBinding(binding RuntimeMaintenanceAuthorityBinding) bool {
	return validOpaqueID(binding.executionNodeID.String()) && validMaintenanceCaller(binding.caller) &&
		binding.caller.kind != MaintenanceAuthorityWorker
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
		decision, err := engine.fenceSandboxLease(typed)
		if err != nil {
			return RuntimeMaintenanceDecision{}, err
		}
		if err := engine.advanceInMemoryRuntimeViewFence(
			ctx, typed.RuntimeRunID, runtimeViewFenceReason(typed.Reason),
		); err != nil {
			return RuntimeMaintenanceDecision{}, err
		}
		return decision, nil
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

func runtimeViewFenceReason(reason LeaseFenceReason) taskworkspace.RuntimeViewFenceReason {
	if reason == LeaseFenceExpired {
		return taskworkspace.RuntimeViewTimedOut
	}
	return taskworkspace.RuntimeViewRevoked
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

func (engine *invariantEngine) authorizedMaintenanceCallerLocked(
	executionNodeID ExecutionNodeID,
	caller maintenanceCallerAuthority,
) bool {
	retained, exists := engine.store.maintenanceAuthorities[maintenanceAuthorityKey{
		executionNodeID: executionNodeID,
		kind:            caller.kind,
	}]
	return exists && retained == caller
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
	if !validLeaseRenewalTransition(record, node, command, now) {
		return RuntimeMaintenanceDecision{}, newError(ErrorIntegrityConflict)
	}
	decision := applyLeaseRenewalTransition(record, node, command)
	engine.store.maintenance[command.OperationID] = decision
	return decision, nil
}

func validLeaseRenewalTransition(
	record *runtimeRecord,
	node *ExecutionNodeFixture,
	command RenewSandboxLease,
	now time.Time,
) bool {
	if record == nil || node == nil {
		return false
	}
	lease := record.lease
	authority := command.Authority
	if record.fixture.State == RuntimeTerminal || lease.AcquireStatus != LeaseGranted ||
		lease.Disposition != LeaseActive || lease.LeaseID != command.SandboxLeaseID ||
		lease.Generation != command.LeaseGeneration || lease.Fence != command.LeaseFence ||
		lease.WorkerAuthorityID != authority.workerAuthorityID || lease.WorkerGeneration != authority.workerGeneration ||
		lease.NodeAuthorityID != authority.nodeAuthorityID ||
		lease.AuthorizationGeneration != authority.authorizationGeneration ||
		command.ReleaseSafetyEpoch != record.fixture.SafetyEpoch ||
		command.CatalogSafetyEpoch != record.catalogSafetyEpoch ||
		node.ExecutionNodeID != record.operation.ExecutionNodeID || node.Generation != command.NodeGeneration ||
		node.AttestationID != command.AttestationID || node.AttestationGeneration != command.AttestationGeneration ||
		node.Readiness != NodeReady || node.Quarantined || node.Occupancy != NodeOccupied ||
		node.ActiveRuntimeRunID != command.RuntimeRunID || node.ActiveLeaseID != command.SandboxLeaseID ||
		!now.Before(node.ExpiresAt) || !now.Before(node.AuthorizationExpiresAt) ||
		!now.Before(lease.ExpiresAt) || !now.Before(lease.AuthorizationExpiresAt) || command.OccurredAt.After(now) {
		return false
	}
	maximumExpiry := earliestTime(record.deadline, record.leaseAcquireBy, node.ExpiresAt,
		node.AuthorizationExpiresAt, lease.AuthorizationExpiresAt)
	return command.RequestedExpiresAt.After(lease.ExpiresAt) && !command.RequestedExpiresAt.After(maximumExpiry)
}

func applyLeaseRenewalTransition(
	record *runtimeRecord,
	node *ExecutionNodeFixture,
	command RenewSandboxLease,
) RuntimeMaintenanceDecision {
	record.fixture.RuntimeRevision++
	record.lease.Generation++
	record.lease.Fence++
	record.lease.ExpiresAt = command.RequestedExpiresAt
	record.node = nodeSnapshot(*node)
	updateCapsuleReadiness(&record.readiness, record.runtimeViewBinding, record.lease)
	return RuntimeMaintenanceDecision{
		OperationID: command.OperationID, CanonicalRequestDigest: command.CanonicalRequestDigest,
		RuntimeRevision: record.fixture.RuntimeRevision, RuntimeFence: record.fixture.RuntimeFence,
		Lease: record.lease, Node: record.node, Cleanup: record.cleanup,
	}
}

func (engine *invariantEngine) fenceSandboxLease(
	command FenceSandboxLease,
) (RuntimeMaintenanceDecision, error) {
	engine.store.mu.Lock()
	defer engine.store.mu.Unlock()
	if retained, exists, err := engine.replayMaintenanceLocked(command.OperationID, command.CanonicalRequestDigest); exists {
		return retained, err
	}
	if !engine.authorizedMaintenanceCallerLocked(command.ExecutionNodeID, command.Authority.caller()) {
		return RuntimeMaintenanceDecision{}, newError(ErrorAuthorizationDenied)
	}
	record := engine.store.runtimes[command.RuntimeRunID]
	if record == nil || record.fixture.PersonalWorkspaceID != command.PersonalWorkspaceID {
		return RuntimeMaintenanceDecision{}, newError(ErrorAuthorizationDenied)
	}
	now := engine.clock.current()
	node := engine.store.nodes[command.ExecutionNodeID]
	if !validLeaseFenceTransition(record, node, command, now) {
		return RuntimeMaintenanceDecision{}, newError(ErrorIntegrityConflict)
	}
	decision := applyLeaseFenceTransition(record, node, command)
	engine.store.maintenance[command.OperationID] = decision
	return decision, nil
}

func validLeaseFenceTransition(
	record *runtimeRecord,
	node *ExecutionNodeFixture,
	command FenceSandboxLease,
	now time.Time,
) bool {
	if record == nil || node == nil {
		return false
	}
	lease := record.lease
	return lease.AcquireStatus == LeaseGranted && lease.Disposition == LeaseActive &&
		command.ExpectedRuntimeFence == record.fixture.RuntimeFence && lease.LeaseID == command.SandboxLeaseID &&
		lease.Generation == command.LeaseGeneration && lease.Fence == command.LeaseFence &&
		record.operation.ExecutionNodeID == command.ExecutionNodeID &&
		NodeGeneration(record.operation.NodeCapacityGeneration) == command.NodeGeneration &&
		command.ReleaseSafetyEpoch == record.fixture.SafetyEpoch &&
		node.ActiveRuntimeRunID == command.RuntimeRunID && node.ActiveLeaseID == command.SandboxLeaseID &&
		!command.OccurredAt.After(now) && (command.Reason != LeaseFenceExpired || !now.Before(lease.ExpiresAt))
}

func applyLeaseFenceTransition(
	record *runtimeRecord,
	node *ExecutionNodeFixture,
	command FenceSandboxLease,
) RuntimeMaintenanceDecision {
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
	updateCapsuleReadiness(&record.readiness, record.runtimeViewBinding, record.lease)
	node.Occupancy = NodeOccupancyUnknown
	node.Quarantined = true
	node.Containment = ContainmentPending
	node.Reset = ResetRequired
	if command.Reason == LeaseFenceExpired || command.Reason == LeaseFenceNodeLost {
		node.Readiness = NodeUnavailable
	}
	record.node = nodeSnapshot(*node)
	record.capacity = RuntimeCapacitySnapshot{
		LogicalRelease: LogicalCapacityReleaseReady,
		NoLease:        NoLeaseDispositionNone,
		Physical:       PhysicalCapacityUnknownOrQuarantined,
	}
	record.capacityEvidence = RuntimeCapacityEvidenceSnapshot{RuntimeFencedOrTerminal: RuntimeFencedOrTerminalEvidence{
		WorkItemID: record.operation.WorkItemID, AdmissionGrantID: record.operation.AdmissionGrantID,
		GrantGeneration: record.operation.GrantGeneration, RuntimeRunID: record.fixture.RuntimeRunID,
		StartOperationID: record.acceptedStart.OperationID, StartDigest: record.acceptedStartDigest,
		TerminalDecisionID: RuntimeDecisionID{value: "runtime-fence-" + command.CanonicalRequestDigest.String()},
		RuntimeRevision:    record.fixture.RuntimeRevision, RuntimeFence: record.fixture.RuntimeFence,
		SchedulerEpoch: record.operation.SchedulerEpoch, PolicyVersion: record.operation.PolicyVersion,
		LeaseAcquireOperationID: record.lease.AcquireOperationID, LeaseAcquireDigest: record.lease.AcquireDigest,
	}}
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
	return decision
}

func (engine *invariantEngine) confirmSandboxReset(
	command ConfirmSandboxReset,
) (RuntimeMaintenanceDecision, error) {
	engine.store.mu.Lock()
	defer engine.store.mu.Unlock()
	if retained, exists, err := engine.replayMaintenanceLocked(command.OperationID, command.CanonicalRequestDigest); exists {
		return retained, err
	}
	if !engine.authorizedMaintenanceCallerLocked(command.ExecutionNodeID, command.Authority.caller()) {
		return RuntimeMaintenanceDecision{}, newError(ErrorAuthorizationDenied)
	}
	record := engine.store.runtimes[command.RuntimeRunID]
	if record == nil || record.fixture.PersonalWorkspaceID != command.PersonalWorkspaceID {
		return RuntimeMaintenanceDecision{}, newError(ErrorAuthorizationDenied)
	}
	node := engine.store.nodes[command.ExecutionNodeID]
	if !validSandboxResetTransition(
		record, node, command, engine.clock.current(),
		record.runtimeViewTerminal != nil && record.runtimeViewTerminal.Kind == runtimeViewTerminalFence,
	) {
		return RuntimeMaintenanceDecision{}, newError(ErrorIntegrityConflict)
	}
	decision := applySandboxResetTransition(record, node, command)
	engine.store.maintenance[command.OperationID] = decision
	return decision, nil
}

func validSandboxResetTransition(
	record *runtimeRecord,
	node *ExecutionNodeFixture,
	command ConfirmSandboxReset,
	now time.Time,
	runtimeViewTerminalRetained bool,
) bool {
	if record == nil || node == nil {
		return false
	}
	lease := record.lease
	return lease.AcquireStatus == LeaseGranted &&
		(lease.Disposition == LeaseRevoked || lease.Disposition == LeaseExpired) &&
		record.readiness.RuntimeView.State != PrerequisiteReconciliationRequired &&
		(!record.cleanup.FenceRuntimeView || record.readiness.RuntimeView.State != PrerequisiteAccepted ||
			runtimeViewTerminalRetained) &&
		command.ExpectedRuntimeFence == record.fixture.RuntimeFence && lease.LeaseID == command.SandboxLeaseID &&
		lease.Generation == command.LeaseGeneration && lease.Fence == command.LeaseFence &&
		lease.SandboxID == command.SandboxID && lease.SandboxGeneration == command.SandboxGeneration &&
		lease.SandboxFence == command.SandboxFence && record.operation.ExecutionNodeID == command.ExecutionNodeID &&
		NodeGeneration(record.operation.NodeCapacityGeneration) == command.NodeGeneration &&
		node.ActiveRuntimeRunID == command.RuntimeRunID && node.ActiveLeaseID == command.SandboxLeaseID &&
		!command.OccurredAt.After(now) && completeResetEvidence(command.ConfirmSandboxResetInput)
}

func applySandboxResetTransition(
	record *runtimeRecord,
	node *ExecutionNodeFixture,
	command ConfirmSandboxReset,
) RuntimeMaintenanceDecision {
	record.fixture.RuntimeRevision++
	record.lease.Generation++
	record.lease.Fence++
	record.lease.SandboxFence++
	record.lease.Disposition = LeaseReleased
	updateCapsuleReadiness(&record.readiness, record.runtimeViewBinding, record.lease)
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
	return decision
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
	if !engine.authorizedMaintenanceCallerLocked(command.ExecutionNodeID, command.Authority.caller()) {
		return RuntimeMaintenanceDecision{}, newError(ErrorAuthorizationDenied)
	}
	node := engine.store.nodes[command.ExecutionNodeID]
	if !validExecutionNodeAttestationTransition(node, command, engine.clock.current()) {
		return RuntimeMaintenanceDecision{}, newError(ErrorIntegrityConflict)
	}
	decision := applyExecutionNodeAttestationTransition(node, command)
	engine.store.maintenance[command.OperationID] = decision
	return decision, nil
}

func validExecutionNodeAttestationTiming(command AttestExecutionNode, now time.Time) bool {
	return !command.AttestedAt.After(now) && now.Before(command.ExpiresAt) &&
		now.Before(command.AuthorizationExpiresAt) && !command.OccurredAt.After(now)
}

func validExecutionNodeAttestationTransition(
	node *ExecutionNodeFixture,
	command AttestExecutionNode,
	now time.Time,
) bool {
	return node != nil && validExecutionNodeAttestationTiming(command, now) && node.Quarantined &&
		node.Occupancy == NodeUnoccupied && command.NodeGeneration >= node.Generation &&
		(command.NodeGeneration > node.Generation || command.AttestationGeneration > node.AttestationGeneration) &&
		command.ResetEvidenceID == node.LastResetEvidenceID &&
		command.ResetEvidenceDigest == node.LastResetEvidenceDigest &&
		command.ResourceClassID == node.ResourceClassID && command.ExecutionPolicyID == node.ExecutionPolicyID &&
		command.ReleaseSafetyEpoch == node.ReleaseSafetyEpoch
}

func applyExecutionNodeAttestationTransition(
	node *ExecutionNodeFixture,
	command AttestExecutionNode,
) RuntimeMaintenanceDecision {
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
	return RuntimeMaintenanceDecision{
		OperationID: command.OperationID, CanonicalRequestDigest: command.CanonicalRequestDigest,
		Node: nodeSnapshot(*node),
	}
}

func executionNodeFixtureFromAttestation(command AttestExecutionNode) ExecutionNodeFixture {
	return ExecutionNodeFixture{
		ExecutionNodeID: command.ExecutionNodeID, Generation: command.NodeGeneration,
		Readiness: NodeReady, AttestationID: command.AttestationID,
		AttestationGeneration: command.AttestationGeneration, AttestedAt: command.AttestedAt,
		ExpiresAt: command.ExpiresAt, ResourceClassID: command.ResourceClassID,
		ExecutionPolicyID: command.ExecutionPolicyID, NodeAuthorityID: command.NodeAuthorityID,
		WorkerAuthorityID: command.WorkerAuthorityID, WorkerGeneration: command.WorkerGeneration,
		AuthorizationGeneration: command.AuthorizationGeneration,
		AuthorizationExpiresAt:  command.AuthorizationExpiresAt,
		ReleaseSafetyEpoch:      command.ReleaseSafetyEpoch, CatalogSafetyEpoch: command.CatalogSafetyEpoch,
		Occupancy: NodeUnoccupied, Containment: ContainmentEstablished, Reset: ResetCompleted,
		LastResetEvidenceID: command.ResetEvidenceID, LastResetEvidenceDigest: command.ResetEvidenceDigest,
	}
}

var _ RuntimeMaintenance = (*invariantEngine)(nil)
