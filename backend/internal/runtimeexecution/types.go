// Package runtimeexecution defines the closed Runtime Execution seam.
package runtimeexecution

import (
	"context"
	"encoding/hex"
	"fmt"
	"time"
)

type SchemaVersion uint32

const SchemaV1 SchemaVersion = 1 << 16

func NewSchemaVersion(major, minor uint16) SchemaVersion {
	return SchemaVersion(uint32(major)<<16 | uint32(minor))
}

func (version SchemaVersion) Major() uint16 { return uint16(uint32(version) >> 16) }
func (version SchemaVersion) Minor() uint16 { return uint16(version) }

const (
	SnapshotSchemaV1             SchemaVersion = SchemaV1
	SnapshotSchemaLeaseLifecycle SchemaVersion = SchemaVersion(1<<16 | 1)
	SnapshotSchemaCurrent        SchemaVersion = SchemaVersion(1<<16 | 2)
)

type (
	TaskRevision                     uint64
	RuntimeRevision                  uint64
	OperationGeneration              uint64
	RuntimeFence                     uint64
	LeaseGeneration                  uint64
	LeaseFence                       uint64
	SandboxGeneration                uint64
	SandboxFence                     uint64
	WorkerGeneration                 uint64
	NodeGeneration                   uint64
	NodeAttestationGeneration        uint64
	AuthorizationGeneration          uint64
	ReleaseSafetyEpoch               uint64
	CatalogSafetyEpoch               uint64
	AdmissionGrantGeneration         uint64
	QuotaReservationGeneration       uint64
	TaskWorkspaceLifecycleGeneration uint64
	TaskWorkspaceLifecycleFence      uint64
)

type PersonalWorkspaceID struct{ value string }
type TaskID struct{ value string }
type PhaseRunID struct{ value string }
type RuntimeRunID struct{ value string }
type OperationID struct{ value string }
type RuntimeDecisionID struct{ value string }
type RuntimeObservationID struct{ value string }
type AuthorityID struct{ value string }
type RuntimeBindingID struct{ value string }
type ImmutableInputIdentity struct{ value string }
type ImmutableInputManifestIdentity struct{ value string }
type ResourceClassID struct{ value string }
type ExecutionPolicyID struct{ value string }
type AdmissionGrantID struct{ value string }
type WorkItemID struct{ value string }
type SandboxLeaseID struct{ value string }
type SandboxID struct{ value string }
type WorkerAuthorityID struct{ value string }
type NodeAuthorityID struct{ value string }
type NodeAttestationID struct{ value string }
type EvidenceID struct{ value string }
type EvidenceRootID struct{ value string }
type ExecutionNodeID struct{ value string }
type TaskWorkspaceID struct{ value string }
type TaskWorkspaceRevisionID struct{ value string }
type TaskWorkspaceMaterializationID struct{ value string }
type RuntimeViewID struct{ value string }
type TemplateLockID struct{ value string }
type QuotaReservationID struct{ value string }
type GatewayRoutePolicyID struct{ value string }
type NetworkPolicyID struct{ value string }
type SecretPolicyID struct{ value string }

func newOpaqueID(value string) (string, error) {
	if !validOpaqueID(value) {
		return "", newError(ErrorInvalidRequest)
	}
	return value, nil
}

func NewPersonalWorkspaceID(value string) (PersonalWorkspaceID, error) {
	value, err := newOpaqueID(value)
	return PersonalWorkspaceID{value: value}, err
}

func NewTaskID(value string) (TaskID, error) {
	value, err := newOpaqueID(value)
	return TaskID{value: value}, err
}

func NewPhaseRunID(value string) (PhaseRunID, error) {
	value, err := newOpaqueID(value)
	return PhaseRunID{value: value}, err
}

func NewRuntimeRunID(value string) (RuntimeRunID, error) {
	value, err := newOpaqueID(value)
	return RuntimeRunID{value: value}, err
}

func NewOperationID(value string) (OperationID, error) {
	value, err := newOpaqueID(value)
	return OperationID{value: value}, err
}

func NewAuthorityID(value string) (AuthorityID, error) {
	value, err := newOpaqueID(value)
	return AuthorityID{value: value}, err
}

func NewRuntimeBindingID(value string) (RuntimeBindingID, error) {
	value, err := newOpaqueID(value)
	return RuntimeBindingID{value: value}, err
}

func NewImmutableInputIdentity(value string) (ImmutableInputIdentity, error) {
	value, err := newOpaqueID(value)
	return ImmutableInputIdentity{value: value}, err
}

func NewImmutableInputManifestIdentity(value string) (ImmutableInputManifestIdentity, error) {
	value, err := newOpaqueID(value)
	return ImmutableInputManifestIdentity{value: value}, err
}

func NewResourceClassID(value string) (ResourceClassID, error) {
	value, err := newOpaqueID(value)
	return ResourceClassID{value: value}, err
}

func NewExecutionPolicyID(value string) (ExecutionPolicyID, error) {
	value, err := newOpaqueID(value)
	return ExecutionPolicyID{value: value}, err
}

func NewAdmissionGrantID(value string) (AdmissionGrantID, error) {
	value, err := newOpaqueID(value)
	return AdmissionGrantID{value: value}, err
}

func NewWorkItemID(value string) (WorkItemID, error) {
	value, err := newOpaqueID(value)
	return WorkItemID{value: value}, err
}

func NewEvidenceID(value string) (EvidenceID, error) {
	value, err := newOpaqueID(value)
	return EvidenceID{value: value}, err
}

func NewExecutionNodeID(value string) (ExecutionNodeID, error) {
	value, err := newOpaqueID(value)
	return ExecutionNodeID{value: value}, err
}

func NewWorkerAuthorityID(value string) (WorkerAuthorityID, error) {
	value, err := newOpaqueID(value)
	return WorkerAuthorityID{value: value}, err
}

func NewNodeAuthorityID(value string) (NodeAuthorityID, error) {
	value, err := newOpaqueID(value)
	return NodeAuthorityID{value: value}, err
}

func NewNodeAttestationID(value string) (NodeAttestationID, error) {
	value, err := newOpaqueID(value)
	return NodeAttestationID{value: value}, err
}

func NewTaskWorkspaceID(value string) (TaskWorkspaceID, error) {
	value, err := newOpaqueID(value)
	return TaskWorkspaceID{value: value}, err
}

func NewTaskWorkspaceRevisionID(value string) (TaskWorkspaceRevisionID, error) {
	value, err := newOpaqueID(value)
	return TaskWorkspaceRevisionID{value: value}, err
}

func NewTaskWorkspaceMaterializationID(value string) (TaskWorkspaceMaterializationID, error) {
	value, err := newOpaqueID(value)
	return TaskWorkspaceMaterializationID{value: value}, err
}

func NewRuntimeViewID(value string) (RuntimeViewID, error) {
	value, err := newOpaqueID(value)
	return RuntimeViewID{value: value}, err
}

func NewTemplateLockID(value string) (TemplateLockID, error) {
	value, err := newOpaqueID(value)
	return TemplateLockID{value: value}, err
}

func NewQuotaReservationID(value string) (QuotaReservationID, error) {
	value, err := newOpaqueID(value)
	return QuotaReservationID{value: value}, err
}

func NewGatewayRoutePolicyID(value string) (GatewayRoutePolicyID, error) {
	value, err := newOpaqueID(value)
	return GatewayRoutePolicyID{value: value}, err
}

func NewNetworkPolicyID(value string) (NetworkPolicyID, error) {
	value, err := newOpaqueID(value)
	return NetworkPolicyID{value: value}, err
}

func NewSecretPolicyID(value string) (SecretPolicyID, error) {
	value, err := newOpaqueID(value)
	return SecretPolicyID{value: value}, err
}

func (id PersonalWorkspaceID) String() string            { return id.value }
func (id TaskID) String() string                         { return id.value }
func (id PhaseRunID) String() string                     { return id.value }
func (id RuntimeRunID) String() string                   { return id.value }
func (id OperationID) String() string                    { return id.value }
func (id RuntimeDecisionID) String() string              { return id.value }
func (id RuntimeObservationID) String() string           { return id.value }
func (id AuthorityID) String() string                    { return id.value }
func (id RuntimeBindingID) String() string               { return id.value }
func (id ImmutableInputIdentity) String() string         { return id.value }
func (id ImmutableInputManifestIdentity) String() string { return id.value }
func (id ResourceClassID) String() string                { return id.value }
func (id ExecutionPolicyID) String() string              { return id.value }
func (id AdmissionGrantID) String() string               { return id.value }
func (id WorkItemID) String() string                     { return id.value }
func (id SandboxLeaseID) String() string                 { return id.value }
func (id SandboxID) String() string                      { return id.value }
func (id WorkerAuthorityID) String() string              { return id.value }
func (id NodeAuthorityID) String() string                { return id.value }
func (id NodeAttestationID) String() string              { return id.value }
func (id EvidenceID) String() string                     { return id.value }
func (id EvidenceRootID) String() string                 { return id.value }
func (id ExecutionNodeID) String() string                { return id.value }
func (id TaskWorkspaceID) String() string                { return id.value }
func (id TaskWorkspaceRevisionID) String() string        { return id.value }
func (id TaskWorkspaceMaterializationID) String() string {
	return id.value
}
func (id RuntimeViewID) String() string        { return id.value }
func (id TemplateLockID) String() string       { return id.value }
func (id QuotaReservationID) String() string   { return id.value }
func (id GatewayRoutePolicyID) String() string { return id.value }
func (id NetworkPolicyID) String() string      { return id.value }
func (id SecretPolicyID) String() string       { return id.value }

type Digest [32]byte

type TraceID [16]byte

type TraceMetadata struct {
	TraceID TraceID
}

func (digest Digest) String() string { return hex.EncodeToString(digest[:]) }

type AuthorityKind uint8

const (
	AuthorityTaskOrchestration AuthorityKind = iota + 1
	AuthorityAdministrator
)

type RuntimeAuthority struct {
	id         AuthorityID
	generation AuthorizationGeneration
	kind       AuthorityKind
}

func NewTaskOrchestrationAuthority(id AuthorityID, generation AuthorizationGeneration) RuntimeAuthority {
	return RuntimeAuthority{id: id, generation: generation, kind: AuthorityTaskOrchestration}
}

func NewAdministratorAuthority(id AuthorityID, generation AuthorizationGeneration) RuntimeAuthority {
	return RuntimeAuthority{id: id, generation: generation, kind: AuthorityAdministrator}
}

type WorkerClass uint8

const (
	WorkerAgent WorkerClass = iota + 1
	WorkerTool
)

type EffectClass uint8

const (
	EffectReadOnly EffectClass = iota + 1
	EffectMutating
)

type CancellationPolicy uint8

const (
	CancellationFenceFirst CancellationPolicy = iota + 1
)

type CancellationReason uint8

const (
	CancellationUserRequested CancellationReason = iota + 1
	CancellationAdministratorRequested
)

type ImmutableInputBinding struct {
	Identity  ImmutableInputIdentity
	Digest    Digest
	SizeBytes uint64
}

type ImmutableInputManifestBinding struct {
	Identity                      ImmutableInputManifestIdentity
	SchemaVersion                 SchemaVersion
	Digest                        Digest
	TotalSizeBytes                uint64
	InputCount                    uint64
	MaterializationEvidenceID     EvidenceID
	MaterializationEvidenceDigest Digest
}

type RuntimeViewRequirement struct {
	TaskWorkspaceID         TaskWorkspaceID
	MaterializationID       TaskWorkspaceMaterializationID
	BaseRevisionID          TaskWorkspaceRevisionID
	LifecycleGeneration     TaskWorkspaceLifecycleGeneration
	LifecycleFence          TaskWorkspaceLifecycleFence
	ExpiryPolicy            RuntimeViewExpiryPolicy
	OpenOperationDerivation Digest
}

type CatalogExecutionBinding struct {
	TemplateLockID     TemplateLockID
	TemplateLockDigest Digest
	ClosureRootDigest  Digest
	SafetyEpoch        CatalogSafetyEpoch
}

type ProviderCapability uint8

const (
	ProviderCapabilityNone ProviderCapability = iota + 1
	ProviderCapabilityRequired
)

type QuotaReservationMode uint8

const (
	QuotaReservationObservation QuotaReservationMode = iota + 1
	QuotaReservationEnforced
)

type ProviderExecutionBinding struct {
	QuotaReservationID   QuotaReservationID
	Generation           QuotaReservationGeneration
	Mode                 QuotaReservationMode
	GatewayRoutePolicyID GatewayRoutePolicyID
}

type RuntimeViewExpiryPolicy uint8

const (
	RuntimeViewExpiryAtDeadline RuntimeViewExpiryPolicy = iota + 1
)

type AdmissionGrantProof struct {
	AdmissionGrantID AdmissionGrantID
	WorkItemID       WorkItemID
	Generation       AdmissionGrantGeneration
}

type StartRuntimeRunInput struct {
	SchemaVersion               SchemaVersion
	OperationID                 OperationID
	PersonalWorkspaceID         PersonalWorkspaceID
	TaskID                      TaskID
	PhaseRunID                  PhaseRunID
	RuntimeRunID                RuntimeRunID
	Attempt                     uint32
	ExpectedTaskRevision        TaskRevision
	ExpectedRuntimeRevision     RuntimeRevision
	ExpectedOperationGeneration OperationGeneration
	ExpectedRuntimeFence        RuntimeFence
	Authority                   RuntimeAuthority
	RuntimeBindingID            RuntimeBindingID
	RuntimeBindingDigest        Digest
	ExecutionLockDigest         Digest
	CapabilityContractDigest    Digest
	AllowedPlatformImagesDigest Digest
	ExecutorContractDigest      Digest
	ReleaseSafetyEpoch          ReleaseSafetyEpoch
	CatalogBinding              *CatalogExecutionBinding
	WorkerClass                 WorkerClass
	Effect                      EffectClass
	ImmutableInputManifest      ImmutableInputManifestBinding
	ImmutableInputs             []ImmutableInputBinding
	OutputContractDigest        Digest
	EvidenceContractDigest      Digest
	RuntimeViewRequirement      *RuntimeViewRequirement
	ResourceClassID             ResourceClassID
	ExecutionPolicyID           ExecutionPolicyID
	ProviderCapability          ProviderCapability
	ProviderBinding             *ProviderExecutionBinding
	NetworkPolicyID             NetworkPolicyID
	SecretPolicyID              SecretPolicyID
	Deadline                    time.Time
	CancellationPolicy          CancellationPolicy
	AdmissionGrant              AdmissionGrantProof
	Trace                       TraceMetadata
}

// StartRuntimeRun binds an existing Runtime Run to one canonical execution request.
type StartRuntimeRun struct {
	StartRuntimeRunInput
	CanonicalRequestDigest Digest
}

func NewStartRuntimeRun(input StartRuntimeRunInput) (StartRuntimeRun, error) {
	input.Deadline = input.Deadline.UTC()
	input.ImmutableInputs = append([]ImmutableInputBinding(nil), input.ImmutableInputs...)
	if input.RuntimeViewRequirement != nil {
		requirement := *input.RuntimeViewRequirement
		input.RuntimeViewRequirement = &requirement
	}
	if input.CatalogBinding != nil {
		binding := *input.CatalogBinding
		input.CatalogBinding = &binding
	}
	if input.ProviderBinding != nil {
		binding := *input.ProviderBinding
		input.ProviderBinding = &binding
	}
	command := StartRuntimeRun{StartRuntimeRunInput: input}
	if !validAdmissionGrantProof(command.AdmissionGrant) {
		return StartRuntimeRun{}, newError(ErrorInvalidRequest)
	}
	digest, err := computeStartDigest(command)
	if err != nil {
		return StartRuntimeRun{}, err
	}
	command.CanonicalRequestDigest = digest
	return command, nil
}

// NewCanonicalStartRuntimeRun constructs the immutable Task-owned C03 request
// before Scheduler admission. AdmissionGrant is deliberately absent from the
// canonical request and must be attached only by authenticated delivery.
func NewCanonicalStartRuntimeRun(input StartRuntimeRunInput) (StartRuntimeRun, error) {
	input.AdmissionGrant = AdmissionGrantProof{}
	input.Deadline = input.Deadline.UTC()
	input.ImmutableInputs = append([]ImmutableInputBinding(nil), input.ImmutableInputs...)
	if input.RuntimeViewRequirement != nil {
		requirement := *input.RuntimeViewRequirement
		input.RuntimeViewRequirement = &requirement
	}
	if input.CatalogBinding != nil {
		binding := *input.CatalogBinding
		input.CatalogBinding = &binding
	}
	if input.ProviderBinding != nil {
		binding := *input.ProviderBinding
		input.ProviderBinding = &binding
	}
	command := StartRuntimeRun{StartRuntimeRunInput: input}
	digest, err := computeStartDigest(command)
	if err != nil {
		return StartRuntimeRun{}, err
	}
	command.CanonicalRequestDigest = digest
	return command, nil
}

// WithAdmissionGrant attaches Scheduler proof without changing the canonical
// Task request or its digest.
func (command StartRuntimeRun) WithAdmissionGrant(grant AdmissionGrantProof) (StartRuntimeRun, error) {
	if !validAdmissionGrantProof(grant) {
		return StartRuntimeRun{}, newError(ErrorInvalidRequest)
	}
	command.AdmissionGrant = grant
	canonical, err := canonicalStartEncoding(command)
	if err != nil || canonicalRequestDigest(canonical) != command.CanonicalRequestDigest {
		return StartRuntimeRun{}, newError(ErrorIntegrityConflict)
	}
	return command, nil
}

type CancelRuntimeRunInput struct {
	SchemaVersion               SchemaVersion
	OperationID                 OperationID
	PersonalWorkspaceID         PersonalWorkspaceID
	TaskID                      TaskID
	PhaseRunID                  PhaseRunID
	RuntimeRunID                RuntimeRunID
	ExpectedRuntimeRevision     RuntimeRevision
	ExpectedStartOperationID    OperationID
	ExpectedOperationGeneration OperationGeneration
	ExpectedRuntimeFence        RuntimeFence
	Authority                   RuntimeAuthority
	Reason                      CancellationReason
	SafetyEpoch                 ReleaseSafetyEpoch
	OccurredAt                  time.Time
}

type CancelRuntimeRun struct {
	CancelRuntimeRunInput
	CanonicalRequestDigest Digest
}

func NewCancelRuntimeRun(input CancelRuntimeRunInput) (CancelRuntimeRun, error) {
	input.OccurredAt = input.OccurredAt.UTC()
	command := CancelRuntimeRun{CancelRuntimeRunInput: input}
	digest, err := computeCancelDigest(command)
	if err != nil {
		return CancelRuntimeRun{}, err
	}
	command.CanonicalRequestDigest = digest
	return command, nil
}

type CommandKind uint8

const (
	CommandStartRuntimeRun CommandKind = iota + 1
	CommandCancelRuntimeRun
)

// RuntimeCommand is closed outside this package by its private marker.
type RuntimeCommand interface {
	commandKind() CommandKind
	runtimeCommand()
}

func (StartRuntimeRun) commandKind() CommandKind  { return CommandStartRuntimeRun }
func (StartRuntimeRun) runtimeCommand()           {}
func (CancelRuntimeRun) commandKind() CommandKind { return CommandCancelRuntimeRun }
func (CancelRuntimeRun) runtimeCommand()          {}

// RuntimeExecution is the complete public Runtime Execution interface.
type RuntimeExecution interface {
	Execute(context.Context, RuntimeCommand) (RuntimeDecision, error)
	Inspect(context.Context, RuntimeRunRef) (RuntimeSnapshot, error)
}

type RuntimeRunRef struct {
	SchemaVersion       SchemaVersion
	ProjectionVersion   SchemaVersion
	PersonalWorkspaceID PersonalWorkspaceID
	RuntimeRunID        RuntimeRunID
	Authority           RuntimeAuthority
}

type DecisionDisposition uint8

const (
	DecisionAccepted DecisionDisposition = iota + 1
	DecisionRejected
)

type CommandRejection uint8

const (
	CommandRejectionNone CommandRejection = iota
	CommandRejectionPolicy
	CommandRejectionStaleRevision
	CommandRejectionStaleBinding
	CommandRejectionStaleGrant
)

type RuntimeDecisionFact struct {
	DecisionID               RuntimeDecisionID
	Disposition              DecisionDisposition
	Rejection                CommandRejection
	OperationID              OperationID
	CanonicalRequestDigest   Digest
	PreviousRuntimeRevision  RuntimeRevision
	ResultingRuntimeRevision RuntimeRevision
	StateAtDecision          RuntimeState
	OutcomeAtDecision        RuntimeOutcome
	TerminalEvidenceID       EvidenceID
	Retry                    RetryDisposition
	Reconciliation           ReconciliationDisposition
}

type RuntimeDecision struct {
	Fact     RuntimeDecisionFact
	Snapshot RuntimeSnapshot
}

type RuntimeState uint8

const (
	RuntimeCreated RuntimeState = iota + 1
	RuntimeAccepted
	RuntimeWaitingForLease
	RuntimeReconciling
	RuntimePreparingPrerequisites
	RuntimeStarting
	RuntimeRunning
	RuntimeStopping
	RuntimeTerminal
)

type RuntimeOutcome uint8

const (
	RuntimeOutcomeNone RuntimeOutcome = iota
	RuntimeSucceeded
	RuntimeFailed
	RuntimeCancelled
	RuntimeTimedOut
	RuntimeLost
	RuntimeRejected
)

type OperationBindingStatus uint8

const (
	OperationUnbound OperationBindingStatus = iota
	OperationBound
)

type RuntimeOperationBinding struct {
	Status                 OperationBindingStatus
	OperationID            OperationID
	Digest                 Digest
	Generation             OperationGeneration
	AdmissionGrantID       AdmissionGrantID
	WorkItemID             WorkItemID
	GrantGeneration        AdmissionGrantGeneration
	ExecutionNodeID        ExecutionNodeID
	NodeCapacityGeneration uint64
	ResourceClassID        ResourceClassID
	ExecutionPolicyID      ExecutionPolicyID
	SchedulerEpoch         uint64
	PolicyVersion          uint64
}

type LeaseAcquireStatus uint8

const (
	LeaseNotRequested LeaseAcquireStatus = iota
	LeaseAcquirePending
	LeaseGranted
	LeaseAcquireReconciliationRequired
)

type LeaseDisposition uint8

const (
	LeaseDispositionNone LeaseDisposition = iota
	LeaseActive
	LeaseRevoked
	LeaseExpired
	LeaseReleased
)

type NodeReadiness uint8

const (
	NodeReadinessUnknown NodeReadiness = iota
	NodeReady
	NodeUnavailable
)

type NodeOccupancy uint8

const (
	NodeUnoccupied NodeOccupancy = iota + 1
	NodeOccupied
	NodeOccupancyUnknown
)

type ContainmentStatus uint8

const (
	ContainmentPending ContainmentStatus = iota + 1
	ContainmentEstablished
)

type ResetStatus uint8

const (
	ResetRequired ResetStatus = iota + 1
	ResetCompleted
)

type RuntimeLeaseSnapshot struct {
	AcquireStatus           LeaseAcquireStatus
	AcquireOperationID      OperationID
	AcquireDigest           Digest
	LeaseID                 SandboxLeaseID
	Generation              LeaseGeneration
	Fence                   LeaseFence
	Disposition             LeaseDisposition
	ExpiresAt               time.Time
	SandboxID               SandboxID
	SandboxGeneration       SandboxGeneration
	SandboxFence            SandboxFence
	WorkerAuthorityID       WorkerAuthorityID
	WorkerGeneration        WorkerGeneration
	NodeAuthorityID         NodeAuthorityID
	AuthorizationGeneration AuthorizationGeneration
	AuthorizationExpiresAt  time.Time
}

type RuntimeNodeSnapshot struct {
	ExecutionNodeID       ExecutionNodeID
	Generation            NodeGeneration
	Readiness             NodeReadiness
	AttestationID         NodeAttestationID
	AttestationGeneration NodeAttestationGeneration
	AttestedAt            time.Time
	ExpiresAt             time.Time
	Occupancy             NodeOccupancy
	Quarantined           bool
	Containment           ContainmentStatus
	Reset                 ResetStatus
}

type LeaseCleanupStatus uint8

const (
	LeaseCleanupNone LeaseCleanupStatus = iota
	LeaseCleanupPending
	LeaseCleanupCompleted
)

type RuntimeLeaseCleanupSnapshot struct {
	Status                 LeaseCleanupStatus
	OperationID            OperationID
	CanonicalRequestDigest Digest
	StopMainProcess        bool
	StopChildProcesses     bool
	RevokeSecrets          bool
	RemoveNetwork          bool
	FenceRuntimeView       bool
	ReconcileContainment   bool
}

type CancellationStatus uint8

const (
	CancellationNotRequested CancellationStatus = iota
	CancellationAccepted
)

type RuntimeCancellationSnapshot struct {
	Status      CancellationStatus
	OperationID OperationID
	Reason      CancellationReason
	AcceptedAt  time.Time
}

type EvidenceRootSnapshot struct {
	SchemaVersion  SchemaVersion
	EvidenceRootID EvidenceRootID
	Digest         Digest
}

type LogicalCapacityDisposition uint8

const (
	LogicalCapacityHeld LogicalCapacityDisposition = iota + 1
	LogicalCapacityReleaseReady
)

type NoLeaseCapacityDisposition uint8

const (
	NoLeaseDispositionNone NoLeaseCapacityDisposition = iota
	NoLeaseDispositionRecorded
)

type PhysicalCapacityDisposition uint8

const (
	PhysicalCapacityNotApplicable PhysicalCapacityDisposition = iota + 1
	PhysicalCapacityOccupied
	PhysicalCapacityUnknownOrQuarantined
	PhysicalCapacityReleaseReady
)

type ReconciliationStatus uint8

const (
	ReconciliationStable ReconciliationStatus = iota + 1
	ReconciliationWaiting
	ReconciliationRequiredStatus
)

type RuntimeCapacitySnapshot struct {
	LogicalRelease LogicalCapacityDisposition
	NoLease        NoLeaseCapacityDisposition
	Physical       PhysicalCapacityDisposition
}

type RuntimeFencedOrTerminalEvidence struct {
	WorkItemID              WorkItemID
	AdmissionGrantID        AdmissionGrantID
	GrantGeneration         AdmissionGrantGeneration
	RuntimeRunID            RuntimeRunID
	StartOperationID        OperationID
	StartDigest             Digest
	TerminalDecisionID      RuntimeDecisionID
	RuntimeRevision         RuntimeRevision
	RuntimeFence            RuntimeFence
	SchedulerEpoch          uint64
	PolicyVersion           uint64
	LeaseAcquireOperationID OperationID
	LeaseAcquireDigest      Digest
}

type NoLeasePhysicalDispositionEvidence struct {
	WorkItemID              WorkItemID
	AdmissionGrantID        AdmissionGrantID
	GrantGeneration         AdmissionGrantGeneration
	RuntimeRunID            RuntimeRunID
	StartOperationID        OperationID
	StartDigest             Digest
	TerminalDecisionID      RuntimeDecisionID
	RuntimeRevision         RuntimeRevision
	RuntimeFence            RuntimeFence
	SchedulerEpoch          uint64
	PolicyVersion           uint64
	LeaseAcquireOperationID OperationID
	LeaseAcquireDigest      Digest
	ExecutionNodeID         ExecutionNodeID
	NodeCapacityGeneration  uint64
}

// PhysicalCapacityReleaseReadyEvidence is an independent, reset-bound fact.
// Runtime terminal and NoLeasePhysicalDisposition never imply it.
type PhysicalCapacityReleaseReadyEvidence struct {
	WorkItemID             WorkItemID
	AdmissionGrantID       AdmissionGrantID
	GrantGeneration        AdmissionGrantGeneration
	RuntimeRunID           RuntimeRunID
	StartOperationID       OperationID
	StartDigest            Digest
	ReleaseOperationID     OperationID
	ReleaseOperationDigest Digest
	RuntimeRevision        RuntimeRevision
	RuntimeFence           RuntimeFence
	SandboxLeaseID         SandboxLeaseID
	LeaseGeneration        LeaseGeneration
	LeaseFence             LeaseFence
	SandboxID              SandboxID
	SandboxGeneration      SandboxGeneration
	SandboxFence           SandboxFence
	ExecutionNodeID        ExecutionNodeID
	NodeCapacityGeneration uint64
	ResetEvidenceID        EvidenceID
	ResetEvidenceDigest    Digest
}

type RuntimeCapacityEvidenceSnapshot struct {
	RuntimeFencedOrTerminal      RuntimeFencedOrTerminalEvidence
	NoLeasePhysicalDisposition   NoLeasePhysicalDispositionEvidence
	PhysicalCapacityReleaseReady PhysicalCapacityReleaseReadyEvidence
}

type RuntimeSnapshot struct {
	SchemaVersion          SchemaVersion
	RuntimeRunID           RuntimeRunID
	RuntimeRevision        RuntimeRevision
	State                  RuntimeState
	Outcome                RuntimeOutcome
	Operation              RuntimeOperationBinding
	RuntimeFence           RuntimeFence
	Lease                  RuntimeLeaseSnapshot
	Node                   RuntimeNodeSnapshot
	Cleanup                RuntimeLeaseCleanupSnapshot
	Deadline               time.Time
	LeaseAcquireBy         time.Time
	Cancellation           RuntimeCancellationSnapshot
	EvidenceRoot           EvidenceRootSnapshot
	Capacity               RuntimeCapacitySnapshot
	CapacityEvidence       RuntimeCapacityEvidenceSnapshot
	PreLeaseTerminalReason PreLeaseTerminalReason
	Reconciliation         ReconciliationStatus
	Readiness              RuntimeReadinessSnapshot
	RuntimeViewBinding     RuntimeViewBindingSnapshot
}

type RetryDisposition uint8

const (
	RetryNever RetryDisposition = iota
	RetrySameRequest
	RetryWithUpdatedGrant
	RetryAfterDependency
)

type ReconciliationDisposition uint8

const (
	ReconciliationNotRequired ReconciliationDisposition = iota
	ReconciliationRequired
)

type ErrorCode uint8

const (
	ErrorAuthorizationDenied ErrorCode = iota + 1
	ErrorInvalidRequest
	ErrorIntegrityConflict
	ErrorUnsupportedSchema
	ErrorDependencyUnavailable
	ErrorReconciliationRequired
)

type Error struct {
	code           ErrorCode
	retry          RetryDisposition
	reconciliation ReconciliationDisposition
}

func (failure *Error) Error() string {
	if failure == nil {
		return "runtime execution request is invalid"
	}
	switch failure.code {
	case ErrorAuthorizationDenied:
		return "runtime execution authority is denied"
	case ErrorIntegrityConflict:
		return "runtime execution request conflicts with an existing binding"
	case ErrorUnsupportedSchema:
		return "runtime execution schema is unsupported"
	case ErrorDependencyUnavailable:
		return "runtime execution dependency is unavailable"
	case ErrorReconciliationRequired:
		return "runtime execution result requires reconciliation"
	default:
		return "runtime execution request is invalid"
	}
}

func (failure *Error) Code() ErrorCode {
	if failure == nil {
		return ErrorInvalidRequest
	}
	return failure.code
}

func (failure *Error) RetryDisposition() RetryDisposition {
	if failure == nil {
		return RetryNever
	}
	return failure.retry
}

func (failure *Error) ReconciliationDisposition() ReconciliationDisposition {
	if failure == nil {
		return ReconciliationNotRequired
	}
	return failure.reconciliation
}

func newError(code ErrorCode) *Error {
	failure := &Error{code: code}
	switch code {
	case ErrorDependencyUnavailable:
		failure.retry = RetryAfterDependency
	case ErrorReconciliationRequired:
		failure.retry = RetrySameRequest
		failure.reconciliation = ReconciliationRequired
	}
	return failure
}

func nextRuntimeDecisionID(sequence uint64) RuntimeDecisionID {
	return RuntimeDecisionID{value: fmt.Sprintf("runtime-decision-%06d", sequence)}
}

func nextRuntimeObservationID(sequence uint64) RuntimeObservationID {
	return RuntimeObservationID{value: fmt.Sprintf("runtime-observation-%06d", sequence)}
}
