package runtimeexecution

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type DeterministicIDConfig struct {
	DecisionStart    uint64
	ObservationStart uint64
	LeaseStart       uint64
	SandboxStart     uint64
}

type IngressObservationKind uint8

const (
	IngressMalformed IngressObservationKind = iota + 1
	IngressUnsupportedSchema
	IngressAuthorizationDenied
	IngressIntegrityConflict
)

type IngressObservation struct {
	ID   RuntimeObservationID
	Kind IngressObservationKind
}

const maxIngressObservations = 64

type FaultPoint uint8

const (
	FaultBeforeValidation FaultPoint = iota + 1
	FaultBeforeCommit
	FaultAfterCommit
	FaultBeforeResponse
	FaultBeforeLeaseCommit
	FaultAfterLeaseCommit
	FaultBeforeNoLeaseCommit
	FaultAfterNoLeaseCommit
)

type CrashBoundary uint8

const (
	CrashBeforeCommit CrashBoundary = iota + 1
	CrashAfterCommit
	CrashBeforeLeaseCommit
	CrashAfterLeaseCommit
	CrashBeforeNoLeaseCommit
	CrashAfterNoLeaseCommit
)

type RuntimeFixture struct {
	PersonalWorkspaceID    PersonalWorkspaceID
	TaskID                 TaskID
	PhaseRunID             PhaseRunID
	RuntimeRunID           RuntimeRunID
	Owner                  RuntimeAuthority
	TaskRevision           TaskRevision
	RuntimeRevision        RuntimeRevision
	OperationGeneration    OperationGeneration
	RuntimeFence           RuntimeFence
	SafetyEpoch            ReleaseSafetyEpoch
	State                  RuntimeState
	Outcome                RuntimeOutcome
	TerminalEvidenceID     EvidenceID
	Operation              RuntimeOperationBinding
	Lease                  RuntimeLeaseSnapshot
	Deadline               time.Time
	LeaseAcquireBy         time.Time
	Cancellation           RuntimeCancellationSnapshot
	EvidenceRoot           EvidenceRootSnapshot
	Capacity               RuntimeCapacitySnapshot
	CapacityEvidence       RuntimeCapacityEvidenceSnapshot
	Node                   RuntimeNodeSnapshot
	Cleanup                RuntimeLeaseCleanupSnapshot
	CatalogSafetyEpoch     CatalogSafetyEpoch
	PreLeaseTerminalReason PreLeaseTerminalReason
	Reconciliation         ReconciliationStatus
}

type AdmissionGrantFixture struct {
	AdmissionGrantID       AdmissionGrantID
	WorkItemID             WorkItemID
	Generation             AdmissionGrantGeneration
	PersonalWorkspaceID    PersonalWorkspaceID
	RuntimeRunID           RuntimeRunID
	OperationID            OperationID
	CanonicalStartDigest   Digest
	ExpiresAt              time.Time
	Current                bool
	ExecutionNodeID        ExecutionNodeID
	NodeCapacityGeneration uint64
	SchedulerEpoch         uint64
	PolicyVersion          uint64
}

type ExecutionNodeFixture struct {
	ExecutionNodeID         ExecutionNodeID
	Generation              NodeGeneration
	Readiness               NodeReadiness
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
	Occupancy               NodeOccupancy
	Quarantined             bool
	Containment             ContainmentStatus
	Reset                   ResetStatus
	ActiveRuntimeRunID      RuntimeRunID
	ActiveLeaseID           SandboxLeaseID
	LastSandboxGeneration   SandboxGeneration
	LastSandboxFence        SandboxFence
	LastResetEvidenceID     EvidenceID
	LastResetEvidenceDigest Digest
}

type QuotaReservationState uint8

const (
	QuotaReservationActive QuotaReservationState = iota + 1
	QuotaReservationInactive
)

type QuotaReservationFixture struct {
	QuotaReservationID  QuotaReservationID
	Generation          QuotaReservationGeneration
	Mode                QuotaReservationMode
	State               QuotaReservationState
	PersonalWorkspaceID PersonalWorkspaceID
	PhaseRunID          PhaseRunID
	Capability          ProviderCapability
	ValidFrom           time.Time
	ExpiresAt           time.Time
}

type HarnessConfig struct {
	Now               time.Time
	IDs               DeterministicIDConfig
	Runtimes          []RuntimeFixture
	AdmissionGrants   []AdmissionGrantFixture
	Nodes             []ExecutionNodeFixture
	QuotaReservations []QuotaReservationFixture
	LeaseAcquisition  LeaseAcquisitionAdapter
}

type DeterministicHarness struct {
	Runtime          RuntimeExecution
	Maintenance      RuntimeMaintenance
	store            *memoryStore
	clock            *controlledClock
	controls         *harnessControls
	leaseAcquisition LeaseAcquisitionAdapter
}

func NewDeterministicHarness(config HarnessConfig) (*DeterministicHarness, error) {
	if config.Now.IsZero() {
		return nil, newError(ErrorInvalidRequest)
	}
	store := &memoryStore{
		runtimes:        make(map[RuntimeRunID]*runtimeRecord),
		grants:          make(map[grantKey]AdmissionGrantFixture),
		nextDecision:    config.IDs.DecisionStart,
		nextObservation: config.IDs.ObservationStart,
		nextLease:       config.IDs.LeaseStart,
		nextSandbox:     config.IDs.SandboxStart,
		nodes:           make(map[ExecutionNodeID]*ExecutionNodeFixture),
		reservations:    make(map[QuotaReservationID]*QuotaReservationFixture),
		maintenance:     make(map[OperationID]RuntimeMaintenanceDecision),
	}
	if store.nextDecision == 0 {
		store.nextDecision = 1
	}
	if store.nextObservation == 0 {
		store.nextObservation = 1
	}
	if store.nextLease == 0 {
		store.nextLease = 1
	}
	if store.nextSandbox == 0 {
		store.nextSandbox = 1
	}
	for _, fixture := range config.Runtimes {
		if !validRuntimeFixture(fixture) || store.runtimes[fixture.RuntimeRunID] != nil {
			return nil, newError(ErrorInvalidRequest)
		}
		capacity := fixture.Capacity
		if capacity == (RuntimeCapacitySnapshot{}) {
			capacity = RuntimeCapacitySnapshot{
				LogicalRelease: LogicalCapacityHeld,
				NoLease:        NoLeaseDispositionNone,
				Physical:       PhysicalCapacityNotApplicable,
			}
		}
		reconciliation := fixture.Reconciliation
		if reconciliation == 0 {
			reconciliation = ReconciliationStable
		}
		store.runtimes[fixture.RuntimeRunID] = &runtimeRecord{
			fixture: fixture, bindings: make(map[OperationID]Digest),
			decisions: make(map[decisionAttemptKey]RuntimeDecisionFact),
			operation: fixture.Operation, lease: fixture.Lease,
			deadline: fixture.Deadline.UTC(), leaseAcquireBy: fixture.LeaseAcquireBy.UTC(),
			cancellation: fixture.Cancellation, evidenceRoot: fixture.EvidenceRoot,
			capacity: capacity, capacityEvidence: fixture.CapacityEvidence, node: fixture.Node,
			cleanup: fixture.Cleanup, catalogSafetyEpoch: fixture.CatalogSafetyEpoch,
			preLeaseTerminalReason: fixture.PreLeaseTerminalReason, reconciliation: reconciliation,
		}
	}
	for _, grant := range config.AdmissionGrants {
		if grant.ExecutionNodeID == (ExecutionNodeID{}) {
			grant.ExecutionNodeID = ExecutionNodeID{value: "execution-node-" + grant.AdmissionGrantID.String()}
		}
		if grant.NodeCapacityGeneration == 0 {
			grant.NodeCapacityGeneration = 1
		}
		if grant.SchedulerEpoch == 0 {
			grant.SchedulerEpoch = 1
		}
		if grant.PolicyVersion == 0 {
			grant.PolicyVersion = 1
		}
		if !validAdmissionGrantFixture(grant) {
			return nil, newError(ErrorInvalidRequest)
		}
		key := grantKey{id: grant.AdmissionGrantID, generation: grant.Generation}
		if _, exists := store.grants[key]; exists {
			return nil, newError(ErrorInvalidRequest)
		}
		grant.ExpiresAt = grant.ExpiresAt.UTC()
		store.grants[key] = grant
	}
	for index := range config.Nodes {
		node := config.Nodes[index]
		if node.Occupancy == 0 {
			node.Occupancy = NodeUnoccupied
		}
		if !validExecutionNodeFixture(node) || store.nodes[node.ExecutionNodeID] != nil {
			return nil, newError(ErrorInvalidRequest)
		}
		node.AttestedAt = node.AttestedAt.UTC()
		node.ExpiresAt = node.ExpiresAt.UTC()
		node.AuthorizationExpiresAt = node.AuthorizationExpiresAt.UTC()
		copyNode := node
		store.nodes[node.ExecutionNodeID] = &copyNode
	}
	for index := range config.QuotaReservations {
		reservation := config.QuotaReservations[index]
		if !validQuotaReservationFixture(reservation) || store.reservations[reservation.QuotaReservationID] != nil {
			return nil, newError(ErrorInvalidRequest)
		}
		reservation.ValidFrom = reservation.ValidFrom.UTC()
		reservation.ExpiresAt = reservation.ExpiresAt.UTC()
		copyReservation := reservation
		store.reservations[reservation.QuotaReservationID] = &copyReservation
	}
	clock := &controlledClock{now: config.Now.UTC()}
	return newHarness(store, clock, config.LeaseAcquisition), nil
}

func newHarness(store *memoryStore, clock *controlledClock, leaseAcquisition LeaseAcquisitionAdapter) *DeterministicHarness {
	controls := &harnessControls{}
	engine := &invariantEngine{store: store, clock: clock, controls: controls, leaseAcquisition: leaseAcquisition}
	return &DeterministicHarness{
		Runtime: engine, Maintenance: engine, store: store, clock: clock, controls: controls, leaseAcquisition: leaseAcquisition,
	}
}

func (harness *DeterministicHarness) AdvanceClock(elapsed time.Duration) error {
	if elapsed < 0 {
		return newError(ErrorInvalidRequest)
	}
	harness.clock.mu.Lock()
	defer harness.clock.mu.Unlock()
	harness.clock.now = harness.clock.now.Add(elapsed)
	return nil
}

func (harness *DeterministicHarness) FailNextAt(point FaultPoint) error {
	if point < FaultBeforeValidation || point > FaultAfterNoLeaseCommit {
		return newError(ErrorInvalidRequest)
	}
	harness.controls.mu.Lock()
	defer harness.controls.mu.Unlock()
	harness.controls.nextFault = point
	return nil
}

func (harness *DeterministicHarness) CrashNextAt(boundary CrashBoundary) error {
	if boundary < CrashBeforeCommit || boundary > CrashAfterNoLeaseCommit {
		return newError(ErrorInvalidRequest)
	}
	harness.controls.mu.Lock()
	defer harness.controls.mu.Unlock()
	harness.controls.nextCrash = boundary
	return nil
}

func (harness *DeterministicHarness) LoseNextResponse() {
	harness.controls.mu.Lock()
	defer harness.controls.mu.Unlock()
	harness.controls.loseResponse = true
}

func (harness *DeterministicHarness) Restart() *DeterministicHarness {
	harness.controls.mu.Lock()
	harness.controls.crashed = true
	harness.controls.mu.Unlock()
	return newHarness(harness.store, harness.clock, harness.leaseAcquisition)
}

func (harness *DeterministicHarness) IngressObservations() []IngressObservation {
	harness.store.mu.Lock()
	defer harness.store.mu.Unlock()
	return append([]IngressObservation(nil), harness.store.observations...)
}

type controlledClock struct {
	mu  sync.Mutex
	now time.Time
}

type harnessControls struct {
	mu           sync.Mutex
	nextFault    FaultPoint
	nextCrash    CrashBoundary
	loseResponse bool
	crashed      bool
}

func (controls *harnessControls) faultAt(point FaultPoint) bool {
	controls.mu.Lock()
	defer controls.mu.Unlock()
	if controls.nextFault != point {
		return false
	}
	controls.nextFault = 0
	return true
}

func (controls *harnessControls) crashAt(boundary CrashBoundary) bool {
	controls.mu.Lock()
	defer controls.mu.Unlock()
	if controls.nextCrash != boundary {
		return false
	}
	controls.nextCrash = 0
	controls.crashed = true
	return true
}

func (controls *harnessControls) takeResponseLoss() bool {
	controls.mu.Lock()
	defer controls.mu.Unlock()
	lost := controls.loseResponse
	controls.loseResponse = false
	return lost
}

func (controls *harnessControls) isCrashed() bool {
	controls.mu.Lock()
	defer controls.mu.Unlock()
	return controls.crashed
}

func (clock *controlledClock) current() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

type memoryStore struct {
	mu              sync.Mutex
	runtimes        map[RuntimeRunID]*runtimeRecord
	grants          map[grantKey]AdmissionGrantFixture
	observations    []IngressObservation
	nextDecision    uint64
	nextObservation uint64
	nextLease       uint64
	nextSandbox     uint64
	nodes           map[ExecutionNodeID]*ExecutionNodeFixture
	reservations    map[QuotaReservationID]*QuotaReservationFixture
	maintenance     map[OperationID]RuntimeMaintenanceDecision
}

type grantKey struct {
	id         AdmissionGrantID
	generation AdmissionGrantGeneration
}

type decisionAttemptKey struct {
	kind            CommandKind
	operationID     OperationID
	grantID         AdmissionGrantID
	grantGeneration AdmissionGrantGeneration
}

type runtimeRecord struct {
	fixture                RuntimeFixture
	bindings               map[OperationID]Digest
	decisions              map[decisionAttemptKey]RuntimeDecisionFact
	acceptedStart          RuntimeDecisionFact
	acceptedStartDigest    Digest
	operation              RuntimeOperationBinding
	deadline               time.Time
	leaseAcquireBy         time.Time
	lease                  RuntimeLeaseSnapshot
	cancellation           RuntimeCancellationSnapshot
	evidenceRoot           EvidenceRootSnapshot
	capacity               RuntimeCapacitySnapshot
	capacityEvidence       RuntimeCapacityEvidenceSnapshot
	preLeaseTerminalReason PreLeaseTerminalReason
	reconciliation         ReconciliationStatus
	node                   RuntimeNodeSnapshot
	catalogSafetyEpoch     CatalogSafetyEpoch
	cleanup                RuntimeLeaseCleanupSnapshot
}

type invariantEngine struct {
	store            *memoryStore
	clock            *controlledClock
	controls         *harnessControls
	leaseAcquisition LeaseAcquisitionAdapter
}

func (engine *invariantEngine) Execute(ctx context.Context, command RuntimeCommand) (RuntimeDecision, error) {
	if command == nil {
		engine.observeIngress(IngressMalformed)
		return RuntimeDecision{}, newError(ErrorInvalidRequest)
	}
	if engine.controls.isCrashed() || ctx == nil || ctx.Err() != nil {
		return RuntimeDecision{}, newError(ErrorDependencyUnavailable)
	}
	if engine.controls.faultAt(FaultBeforeValidation) {
		return RuntimeDecision{}, newError(ErrorDependencyUnavailable)
	}
	switch typed := command.(type) {
	case StartRuntimeRun:
		decision, err := engine.executeStart(typed)
		if err != nil {
			return RuntimeDecision{}, err
		}
		return engine.advancePreLease(ctx, typed, decision)
	case CancelRuntimeRun:
		return engine.executeCancel(typed)
	default:
		engine.observeIngress(IngressMalformed)
		return RuntimeDecision{}, newError(ErrorInvalidRequest)
	}
}

func (engine *invariantEngine) executeCancel(command CancelRuntimeRun) (RuntimeDecision, error) {
	if command.SchemaVersion == 0 {
		engine.observeIngress(IngressMalformed)
		return RuntimeDecision{}, newError(ErrorInvalidRequest)
	}
	if command.SchemaVersion.Major() != SchemaV1.Major() {
		engine.observeIngress(IngressUnsupportedSchema)
		return RuntimeDecision{}, newError(ErrorUnsupportedSchema)
	}
	digest, err := computeCancelDigest(command)
	if err != nil {
		engine.observeIngress(IngressMalformed)
		return RuntimeDecision{}, err
	}
	if digest != command.CanonicalRequestDigest {
		engine.observeIngress(IngressIntegrityConflict)
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	engine.store.mu.Lock()
	defer engine.store.mu.Unlock()
	record, exists := engine.store.runtimes[command.RuntimeRunID]
	if !exists || !authorized(record, command.PersonalWorkspaceID, command.Authority) {
		engine.observeIngressLocked(IngressAuthorizationDenied)
		return RuntimeDecision{}, newError(ErrorAuthorizationDenied)
	}
	if command.TaskID != record.fixture.TaskID || command.PhaseRunID != record.fixture.PhaseRunID {
		engine.observeIngressLocked(IngressAuthorizationDenied)
		return RuntimeDecision{}, newError(ErrorAuthorizationDenied)
	}
	if bound, exists := record.bindings[command.OperationID]; exists && bound != digest {
		engine.observeIngressLocked(IngressIntegrityConflict)
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	if engine.controls.crashAt(CrashBeforeCommit) || engine.controls.faultAt(FaultBeforeCommit) {
		return RuntimeDecision{}, newError(ErrorDependencyUnavailable)
	}
	record.bindings[command.OperationID] = digest
	attempt := decisionAttemptKey{kind: CommandCancelRuntimeRun, operationID: command.OperationID}
	if fact, exists := record.decisions[attempt]; exists {
		return RuntimeDecision{Fact: fact, Snapshot: snapshot(record, SnapshotSchemaCurrent)}, nil
	}
	if command.ExpectedRuntimeRevision != record.fixture.RuntimeRevision {
		return engine.reject(record, attempt, command.OperationID, digest, CommandRejectionStaleRevision)
	}
	if command.ExpectedStartOperationID != record.acceptedStart.OperationID ||
		command.ExpectedOperationGeneration != record.fixture.OperationGeneration ||
		command.ExpectedRuntimeFence != record.fixture.RuntimeFence || command.SafetyEpoch != record.fixture.SafetyEpoch ||
		command.Authority.generation != record.fixture.Owner.generation {
		return engine.reject(record, attempt, command.OperationID, digest, CommandRejectionStaleBinding)
	}
	if record.fixture.State == RuntimeTerminal || record.fixture.Outcome != RuntimeOutcomeNone {
		return engine.reject(record, attempt, command.OperationID, digest, CommandRejectionPolicy)
	}
	previous := record.fixture.RuntimeRevision
	startBinding := record.operation
	leaseBinding := record.lease
	leased := leaseBinding.AcquireStatus == LeaseGranted && leaseBinding.Disposition == LeaseActive
	var leasedNode *ExecutionNodeFixture
	if leased {
		leasedNode = engine.store.nodes[startBinding.ExecutionNodeID]
		if leasedNode == nil || leasedNode.ActiveRuntimeRunID != command.RuntimeRunID ||
			leasedNode.ActiveLeaseID != leaseBinding.LeaseID {
			return RuntimeDecision{}, newError(ErrorIntegrityConflict)
		}
	}
	record.fixture.RuntimeRevision++
	record.fixture.OperationGeneration++
	record.fixture.RuntimeFence++
	record.fixture.State = RuntimeTerminal
	record.fixture.Outcome = RuntimeCancelled
	record.operation = RuntimeOperationBinding{
		Status: OperationBound, OperationID: command.OperationID, Digest: digest,
		Generation:       record.fixture.OperationGeneration,
		AdmissionGrantID: startBinding.AdmissionGrantID, WorkItemID: startBinding.WorkItemID,
		GrantGeneration: startBinding.GrantGeneration, ExecutionNodeID: startBinding.ExecutionNodeID,
		NodeCapacityGeneration: startBinding.NodeCapacityGeneration,
		ResourceClassID:        startBinding.ResourceClassID, ExecutionPolicyID: startBinding.ExecutionPolicyID,
		SchedulerEpoch: startBinding.SchedulerEpoch, PolicyVersion: startBinding.PolicyVersion,
	}
	record.cancellation = RuntimeCancellationSnapshot{
		Status: CancellationAccepted, OperationID: command.OperationID, Reason: command.Reason,
		AcceptedAt: command.OccurredAt.UTC(),
	}
	if leased {
		record.lease.Generation++
		record.lease.Fence++
		record.lease.SandboxFence++
		record.lease.Disposition = LeaseRevoked
		leasedNode.Occupancy = NodeOccupancyUnknown
		leasedNode.Quarantined = true
		leasedNode.Containment = ContainmentPending
		leasedNode.Reset = ResetRequired
		record.node = nodeSnapshot(*leasedNode)
		record.cleanup = RuntimeLeaseCleanupSnapshot{
			Status: LeaseCleanupPending, OperationID: command.OperationID,
			CanonicalRequestDigest: digest, StopMainProcess: true, StopChildProcesses: true,
			RevokeSecrets: true, RemoveNetwork: true, FenceRuntimeView: true,
			ReconcileContainment: true,
		}
		record.capacity = RuntimeCapacitySnapshot{
			LogicalRelease: LogicalCapacityReleaseReady, NoLease: NoLeaseDispositionNone,
			Physical: PhysicalCapacityUnknownOrQuarantined,
		}
	} else {
		record.lease.AcquireStatus = LeaseNotRequested
		record.capacity = RuntimeCapacitySnapshot{
			LogicalRelease: LogicalCapacityReleaseReady,
			NoLease:        NoLeaseDispositionRecorded,
			Physical:       PhysicalCapacityNotApplicable,
		}
	}
	record.reconciliation = ReconciliationStable
	fact := RuntimeDecisionFact{
		DecisionID: engine.nextDecisionID(), Disposition: DecisionAccepted,
		OperationID: command.OperationID, CanonicalRequestDigest: digest,
		PreviousRuntimeRevision: previous, ResultingRuntimeRevision: record.fixture.RuntimeRevision,
		StateAtDecision: record.fixture.State, OutcomeAtDecision: record.fixture.Outcome,
		Retry: RetryNever, Reconciliation: ReconciliationNotRequired,
	}
	baseEvidence := RuntimeFencedOrTerminalEvidence{
		WorkItemID: startBinding.WorkItemID, AdmissionGrantID: startBinding.AdmissionGrantID,
		GrantGeneration: startBinding.GrantGeneration, RuntimeRunID: command.RuntimeRunID,
		StartOperationID: command.ExpectedStartOperationID, StartDigest: startBinding.Digest,
		TerminalDecisionID: fact.DecisionID, RuntimeRevision: record.fixture.RuntimeRevision,
		RuntimeFence: record.fixture.RuntimeFence, SchedulerEpoch: startBinding.SchedulerEpoch,
		PolicyVersion:           startBinding.PolicyVersion,
		LeaseAcquireOperationID: leaseBinding.AcquireOperationID, LeaseAcquireDigest: leaseBinding.AcquireDigest,
	}
	record.capacityEvidence = RuntimeCapacityEvidenceSnapshot{RuntimeFencedOrTerminal: baseEvidence}
	if !leased {
		record.capacityEvidence.NoLeasePhysicalDisposition = NoLeasePhysicalDispositionEvidence{
			WorkItemID: baseEvidence.WorkItemID, AdmissionGrantID: baseEvidence.AdmissionGrantID,
			GrantGeneration: baseEvidence.GrantGeneration, RuntimeRunID: baseEvidence.RuntimeRunID,
			StartOperationID: baseEvidence.StartOperationID, StartDigest: baseEvidence.StartDigest,
			TerminalDecisionID: baseEvidence.TerminalDecisionID, RuntimeRevision: baseEvidence.RuntimeRevision,
			RuntimeFence: baseEvidence.RuntimeFence, SchedulerEpoch: baseEvidence.SchedulerEpoch,
			PolicyVersion:           baseEvidence.PolicyVersion,
			LeaseAcquireOperationID: baseEvidence.LeaseAcquireOperationID,
			LeaseAcquireDigest:      baseEvidence.LeaseAcquireDigest,
			ExecutionNodeID:         startBinding.ExecutionNodeID,
			NodeCapacityGeneration:  startBinding.NodeCapacityGeneration,
		}
	}
	record.decisions[attempt] = fact
	return engine.respond(RuntimeDecision{Fact: fact, Snapshot: snapshot(record, SnapshotSchemaCurrent)})
}

func (engine *invariantEngine) advancePreLease(
	ctx context.Context,
	start StartRuntimeRun,
	startDecision RuntimeDecision,
) (RuntimeDecision, error) {
	if startDecision.Fact.Disposition != DecisionAccepted {
		return startDecision, nil
	}
	now := engine.clock.current()
	engine.store.mu.Lock()
	record := engine.store.runtimes[start.RuntimeRunID]
	if record == nil {
		engine.store.mu.Unlock()
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	if record.fixture.State == RuntimeTerminal || record.lease.AcquireStatus == LeaseGranted ||
		record.fixture.State != RuntimeWaitingForLease && record.fixture.State != RuntimeReconciling {
		startDecision.Snapshot = snapshot(record, SnapshotSchemaCurrent)
		engine.store.mu.Unlock()
		return startDecision, nil
	}
	if !now.Before(record.deadline) {
		return engine.finishInMemoryNoLeaseLocked(
			record, startDecision, RuntimeTimedOut, PreLeaseTerminalRuntimeDeadline, now,
		)
	}
	if !now.Before(record.leaseAcquireBy) {
		return engine.finishInMemoryNoLeaseLocked(
			record, startDecision, RuntimeRejected, PreLeaseTerminalAdmissionAuthorityExpired, now,
		)
	}
	request := leaseAcquisitionRequest(record)
	if engine.leaseAcquisition == nil {
		startDecision.Snapshot = snapshot(record, SnapshotSchemaCurrent)
		engine.store.mu.Unlock()
		return startDecision, nil
	}
	if !validLeaseAcquisitionRequest(request) {
		engine.store.mu.Unlock()
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	engine.store.mu.Unlock()

	observation, err := engine.leaseAcquisition.ObserveLeaseAcquisition(ctx, request)
	if err != nil {
		observation = LeaseAcquisitionObservation{Disposition: LeaseAcquisitionAmbiguousPrerequisite}
	}
	if !validLeaseAcquisitionObservation(observation) {
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}

	engine.store.mu.Lock()
	record = engine.store.runtimes[start.RuntimeRunID]
	if record == nil || record.lease.AcquireOperationID != request.OperationID ||
		record.lease.AcquireDigest != request.Digest {
		engine.store.mu.Unlock()
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	if record.fixture.State == RuntimeTerminal || record.lease.AcquireStatus == LeaseGranted {
		startDecision.Snapshot = snapshot(record, SnapshotSchemaCurrent)
		engine.store.mu.Unlock()
		return startDecision, nil
	}
	now = engine.clock.current()
	if !now.Before(record.deadline) {
		return engine.finishInMemoryNoLeaseLocked(
			record, startDecision, RuntimeTimedOut, PreLeaseTerminalRuntimeDeadline, now,
		)
	}
	if !now.Before(record.leaseAcquireBy) {
		return engine.finishInMemoryNoLeaseLocked(
			record, startDecision, RuntimeRejected, PreLeaseTerminalAdmissionAuthorityExpired, now,
		)
	}

	switch observation.Disposition {
	case LeaseAcquisitionTemporaryUnavailable:
		startDecision.Snapshot = snapshot(record, SnapshotSchemaCurrent)
		engine.store.mu.Unlock()
		return startDecision, nil
	case LeaseAcquisitionRetryablePrerequisite, LeaseAcquisitionAmbiguousPrerequisite:
		if record.fixture.State != RuntimeReconciling {
			record.fixture.RuntimeRevision++
			record.fixture.State = RuntimeReconciling
			record.lease.AcquireStatus = LeaseAcquireReconciliationRequired
			record.reconciliation = ReconciliationRequiredStatus
		}
		startDecision.Snapshot = snapshot(record, SnapshotSchemaCurrent)
		engine.store.mu.Unlock()
		return startDecision, nil
	case LeaseAcquisitionPermanentFailure:
		return engine.finishInMemoryNoLeaseLocked(
			record, startDecision, RuntimeRejected,
			terminalReasonForPermanentFailure(observation.PermanentFailure), now,
		)
	case LeaseAcquisitionReady:
		if engine.controls.crashAt(CrashBeforeLeaseCommit) || engine.controls.faultAt(FaultBeforeLeaseCommit) {
			engine.store.mu.Unlock()
			return RuntimeDecision{}, newError(ErrorDependencyUnavailable)
		}
		node := engine.store.nodes[record.operation.ExecutionNodeID]
		if !reservationEligibleForLease(engine.store.reservations, start, now) {
			return engine.finishInMemoryNoLeaseLocked(
				record, startDecision, RuntimeRejected, PreLeaseTerminalReservation, now,
			)
		}
		if !nodeEligibleForLease(node, record, now) {
			return engine.finishInMemoryNoLeaseLocked(
				record, startDecision, RuntimeRejected, PreLeaseTerminalNodeIneligible, now,
			)
		}
		record.fixture.RuntimeRevision++
		record.fixture.State = RuntimePreparingPrerequisites
		record.lease.AcquireStatus = LeaseGranted
		record.lease.LeaseID = SandboxLeaseID{value: fmt.Sprintf("sandbox-lease-%06d", engine.store.nextLease)}
		engine.store.nextLease++
		record.lease.Generation = 1
		record.lease.Fence = 1
		record.lease.Disposition = LeaseActive
		record.lease.ExpiresAt = earliestTime(now.Add(90*time.Second), record.deadline, record.leaseAcquireBy,
			node.ExpiresAt, node.AuthorizationExpiresAt)
		record.lease.SandboxID = SandboxID{value: fmt.Sprintf("sandbox-instance-%06d", engine.store.nextSandbox)}
		engine.store.nextSandbox++
		record.lease.SandboxGeneration = node.LastSandboxGeneration + 1
		record.lease.SandboxFence = node.LastSandboxFence + 1
		record.lease.WorkerAuthorityID = node.WorkerAuthorityID
		record.lease.WorkerGeneration = node.WorkerGeneration
		record.lease.NodeAuthorityID = node.NodeAuthorityID
		record.lease.AuthorizationGeneration = node.AuthorizationGeneration
		record.lease.AuthorizationExpiresAt = node.AuthorizationExpiresAt
		node.Occupancy = NodeOccupied
		node.Containment = ContainmentPending
		node.Reset = ResetRequired
		node.ActiveRuntimeRunID = record.fixture.RuntimeRunID
		node.ActiveLeaseID = record.lease.LeaseID
		record.node = nodeSnapshot(*node)
		record.capacity.Physical = PhysicalCapacityOccupied
		record.reconciliation = ReconciliationStable
		startDecision.Snapshot = snapshot(record, SnapshotSchemaCurrent)
		engine.store.mu.Unlock()
		if engine.controls.crashAt(CrashAfterLeaseCommit) || engine.controls.faultAt(FaultAfterLeaseCommit) {
			return RuntimeDecision{}, newError(ErrorReconciliationRequired)
		}
		return startDecision, nil
	default:
		engine.store.mu.Unlock()
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
}

func (engine *invariantEngine) finishInMemoryNoLeaseLocked(
	record *runtimeRecord,
	startDecision RuntimeDecision,
	outcome RuntimeOutcome,
	reason PreLeaseTerminalReason,
	occurredAt time.Time,
) (RuntimeDecision, error) {
	if reason == PreLeaseTerminalNone || outcome != RuntimeRejected && outcome != RuntimeTimedOut {
		engine.store.mu.Unlock()
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	if engine.controls.crashAt(CrashBeforeNoLeaseCommit) || engine.controls.faultAt(FaultBeforeNoLeaseCommit) {
		engine.store.mu.Unlock()
		return RuntimeDecision{}, newError(ErrorDependencyUnavailable)
	}
	startBinding := record.operation
	leaseBinding := record.lease
	previousRevision := record.fixture.RuntimeRevision
	record.fixture.RuntimeRevision++
	record.fixture.OperationGeneration++
	record.fixture.RuntimeFence++
	record.fixture.State = RuntimeTerminal
	record.fixture.Outcome = outcome
	terminalOperationID, terminalDigest := stablePreLeaseTerminalBinding(leaseBinding, outcome, reason)
	record.operation = startBinding
	record.operation.OperationID = terminalOperationID
	record.operation.Digest = terminalDigest
	record.operation.Generation = record.fixture.OperationGeneration
	record.lease.AcquireStatus = LeaseNotRequested
	record.preLeaseTerminalReason = reason
	record.capacity = RuntimeCapacitySnapshot{
		LogicalRelease: LogicalCapacityReleaseReady,
		NoLease:        NoLeaseDispositionRecorded,
		Physical:       PhysicalCapacityNotApplicable,
	}
	record.reconciliation = ReconciliationStable
	terminalFact := RuntimeDecisionFact{
		DecisionID: engine.nextDecisionID(), Disposition: DecisionAccepted,
		OperationID: terminalOperationID, CanonicalRequestDigest: terminalDigest,
		PreviousRuntimeRevision: previousRevision, ResultingRuntimeRevision: record.fixture.RuntimeRevision,
		StateAtDecision: RuntimeTerminal, OutcomeAtDecision: outcome,
		Retry: RetryNever, Reconciliation: ReconciliationNotRequired,
	}
	baseEvidence := RuntimeFencedOrTerminalEvidence{
		WorkItemID: startBinding.WorkItemID, AdmissionGrantID: startBinding.AdmissionGrantID,
		GrantGeneration: startBinding.GrantGeneration, RuntimeRunID: record.fixture.RuntimeRunID,
		StartOperationID: record.acceptedStart.OperationID, StartDigest: record.acceptedStartDigest,
		TerminalDecisionID: terminalFact.DecisionID, RuntimeRevision: record.fixture.RuntimeRevision,
		RuntimeFence: record.fixture.RuntimeFence, SchedulerEpoch: startBinding.SchedulerEpoch,
		PolicyVersion:           startBinding.PolicyVersion,
		LeaseAcquireOperationID: leaseBinding.AcquireOperationID, LeaseAcquireDigest: leaseBinding.AcquireDigest,
	}
	record.capacityEvidence = RuntimeCapacityEvidenceSnapshot{
		RuntimeFencedOrTerminal: baseEvidence,
		NoLeasePhysicalDisposition: NoLeasePhysicalDispositionEvidence{
			WorkItemID: baseEvidence.WorkItemID, AdmissionGrantID: baseEvidence.AdmissionGrantID,
			GrantGeneration: baseEvidence.GrantGeneration, RuntimeRunID: baseEvidence.RuntimeRunID,
			StartOperationID: baseEvidence.StartOperationID, StartDigest: baseEvidence.StartDigest,
			TerminalDecisionID: baseEvidence.TerminalDecisionID, RuntimeRevision: baseEvidence.RuntimeRevision,
			RuntimeFence: baseEvidence.RuntimeFence, SchedulerEpoch: baseEvidence.SchedulerEpoch,
			PolicyVersion:           baseEvidence.PolicyVersion,
			LeaseAcquireOperationID: baseEvidence.LeaseAcquireOperationID,
			LeaseAcquireDigest:      baseEvidence.LeaseAcquireDigest,
			ExecutionNodeID:         startBinding.ExecutionNodeID,
			NodeCapacityGeneration:  startBinding.NodeCapacityGeneration,
		},
	}
	_ = occurredAt
	startDecision.Snapshot = snapshot(record, SnapshotSchemaCurrent)
	engine.store.mu.Unlock()
	if engine.controls.crashAt(CrashAfterNoLeaseCommit) || engine.controls.faultAt(FaultAfterNoLeaseCommit) {
		return RuntimeDecision{}, newError(ErrorReconciliationRequired)
	}
	return startDecision, nil
}

func (engine *invariantEngine) respond(decision RuntimeDecision) (RuntimeDecision, error) {
	if engine.controls.crashAt(CrashAfterCommit) {
		return RuntimeDecision{}, newError(ErrorDependencyUnavailable)
	}
	if engine.controls.faultAt(FaultAfterCommit) || engine.controls.faultAt(FaultBeforeResponse) ||
		engine.controls.takeResponseLoss() {
		return RuntimeDecision{}, newError(ErrorReconciliationRequired)
	}
	return decision, nil
}

func (engine *invariantEngine) executeStart(command StartRuntimeRun) (RuntimeDecision, error) {
	if command.SchemaVersion == 0 {
		engine.observeIngress(IngressMalformed)
		return RuntimeDecision{}, newError(ErrorInvalidRequest)
	}
	if command.SchemaVersion.Major() != SchemaV1.Major() {
		engine.observeIngress(IngressUnsupportedSchema)
		return RuntimeDecision{}, newError(ErrorUnsupportedSchema)
	}
	if !validStart(command) {
		engine.observeIngress(IngressMalformed)
		return RuntimeDecision{}, newError(ErrorInvalidRequest)
	}
	digest, err := computeStartDigest(command)
	if err != nil {
		engine.observeIngress(IngressMalformed)
		return RuntimeDecision{}, err
	}
	if digest != command.CanonicalRequestDigest {
		engine.observeIngress(IngressIntegrityConflict)
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	if command.Authority.kind != AuthorityTaskOrchestration {
		engine.observeIngress(IngressAuthorizationDenied)
		return RuntimeDecision{}, newError(ErrorAuthorizationDenied)
	}
	engine.store.mu.Lock()
	defer engine.store.mu.Unlock()
	record, exists := engine.store.runtimes[command.RuntimeRunID]
	if !exists || !authorized(record, command.PersonalWorkspaceID, command.Authority) {
		engine.observeIngressLocked(IngressAuthorizationDenied)
		return RuntimeDecision{}, newError(ErrorAuthorizationDenied)
	}
	if command.TaskID != record.fixture.TaskID || command.PhaseRunID != record.fixture.PhaseRunID {
		engine.observeIngressLocked(IngressAuthorizationDenied)
		return RuntimeDecision{}, newError(ErrorAuthorizationDenied)
	}
	if record.acceptedStart.DecisionID != (RuntimeDecisionID{}) && command.OperationID == record.acceptedStart.OperationID {
		if digest != record.acceptedStartDigest {
			engine.observeIngressLocked(IngressIntegrityConflict)
			return RuntimeDecision{}, newError(ErrorIntegrityConflict)
		}
		return RuntimeDecision{Fact: record.acceptedStart, Snapshot: snapshot(record, SnapshotSchemaCurrent)}, nil
	}
	if bound, exists := record.bindings[command.OperationID]; exists && bound != digest {
		engine.observeIngressLocked(IngressIntegrityConflict)
		return RuntimeDecision{}, newError(ErrorIntegrityConflict)
	}
	if engine.controls.crashAt(CrashBeforeCommit) || engine.controls.faultAt(FaultBeforeCommit) {
		return RuntimeDecision{}, newError(ErrorDependencyUnavailable)
	}
	record.bindings[command.OperationID] = digest
	attempt := decisionAttemptKey{
		kind: CommandStartRuntimeRun, operationID: command.OperationID,
		grantID: command.AdmissionGrant.AdmissionGrantID, grantGeneration: command.AdmissionGrant.Generation,
	}
	if fact, exists := record.decisions[attempt]; exists {
		return RuntimeDecision{Fact: fact, Snapshot: snapshot(record, SnapshotSchemaCurrent)}, nil
	}
	grant, validGrant := engine.store.grants[grantKey{id: command.AdmissionGrant.AdmissionGrantID, generation: command.AdmissionGrant.Generation}]
	if validGrant && !grant.Current {
		return engine.reject(record, attempt, command.OperationID, digest, CommandRejectionStaleGrant)
	}
	now := engine.clock.current()
	if !validGrant || grant.WorkItemID != command.AdmissionGrant.WorkItemID ||
		grant.PersonalWorkspaceID != command.PersonalWorkspaceID || grant.RuntimeRunID != command.RuntimeRunID ||
		grant.OperationID != command.OperationID || grant.CanonicalStartDigest != digest || !grant.ExpiresAt.After(now) ||
		!command.Deadline.After(now) {
		return engine.reject(record, attempt, command.OperationID, digest, CommandRejectionPolicy)
	}
	if command.ExpectedTaskRevision != record.fixture.TaskRevision || command.ExpectedRuntimeRevision != record.fixture.RuntimeRevision {
		return engine.reject(record, attempt, command.OperationID, digest, CommandRejectionStaleRevision)
	}
	if command.ExpectedOperationGeneration != record.fixture.OperationGeneration || command.ExpectedRuntimeFence != record.fixture.RuntimeFence ||
		command.ReleaseSafetyEpoch != record.fixture.SafetyEpoch || command.Authority.generation != record.fixture.Owner.generation {
		return engine.reject(record, attempt, command.OperationID, digest, CommandRejectionStaleBinding)
	}
	if record.fixture.State != RuntimeCreated || record.fixture.Outcome != RuntimeOutcomeNone {
		return engine.reject(record, attempt, command.OperationID, digest, CommandRejectionPolicy)
	}
	previous := record.fixture.RuntimeRevision
	record.fixture.RuntimeRevision++
	record.fixture.OperationGeneration++
	record.fixture.RuntimeFence++
	record.fixture.State = RuntimeWaitingForLease
	record.fixture.Outcome = RuntimeOutcomeNone
	record.operation = RuntimeOperationBinding{
		Status: OperationBound, OperationID: command.OperationID, Digest: digest,
		Generation: record.fixture.OperationGeneration, AdmissionGrantID: command.AdmissionGrant.AdmissionGrantID,
		WorkItemID: command.AdmissionGrant.WorkItemID, GrantGeneration: command.AdmissionGrant.Generation,
		ExecutionNodeID: grant.ExecutionNodeID, NodeCapacityGeneration: grant.NodeCapacityGeneration,
		ResourceClassID: command.ResourceClassID, ExecutionPolicyID: command.ExecutionPolicyID,
		SchedulerEpoch: grant.SchedulerEpoch, PolicyVersion: grant.PolicyVersion,
	}
	record.deadline = command.Deadline.UTC()
	if command.CatalogBinding != nil {
		record.catalogSafetyEpoch = command.CatalogBinding.SafetyEpoch
	}
	record.leaseAcquireBy = earlier(command.Deadline, grant.ExpiresAt)
	record.lease.AcquireStatus = LeaseAcquirePending
	record.lease.AcquireOperationID, record.lease.AcquireDigest = stableLeaseAcquireBinding(command)
	record.capacity = RuntimeCapacitySnapshot{
		LogicalRelease: LogicalCapacityHeld, NoLease: NoLeaseDispositionNone, Physical: PhysicalCapacityNotApplicable,
	}
	record.reconciliation = ReconciliationStable
	fact := RuntimeDecisionFact{
		DecisionID: engine.nextDecisionID(), Disposition: DecisionAccepted,
		OperationID: command.OperationID, CanonicalRequestDigest: digest,
		PreviousRuntimeRevision: previous, ResultingRuntimeRevision: record.fixture.RuntimeRevision,
		StateAtDecision: record.fixture.State, OutcomeAtDecision: record.fixture.Outcome,
		Retry: RetryNever, Reconciliation: ReconciliationNotRequired,
	}
	record.decisions[attempt] = fact
	record.acceptedStart = fact
	record.acceptedStartDigest = digest
	return engine.respond(RuntimeDecision{Fact: fact, Snapshot: snapshot(record, SnapshotSchemaCurrent)})
}

func (engine *invariantEngine) reject(
	record *runtimeRecord,
	attempt decisionAttemptKey,
	operationID OperationID,
	digest Digest,
	rejection CommandRejection,
) (RuntimeDecision, error) {
	retry := RetryNever
	if rejection == CommandRejectionStaleGrant {
		retry = RetryWithUpdatedGrant
	}
	fact := RuntimeDecisionFact{
		DecisionID: engine.nextDecisionID(), Disposition: DecisionRejected, Rejection: rejection,
		OperationID: operationID, CanonicalRequestDigest: digest,
		PreviousRuntimeRevision: record.fixture.RuntimeRevision, ResultingRuntimeRevision: record.fixture.RuntimeRevision,
		StateAtDecision: record.fixture.State, OutcomeAtDecision: record.fixture.Outcome,
		TerminalEvidenceID: record.fixture.TerminalEvidenceID,
		Retry:              retry, Reconciliation: ReconciliationNotRequired,
	}
	record.decisions[attempt] = fact
	return engine.respond(RuntimeDecision{Fact: fact, Snapshot: snapshot(record, SnapshotSchemaCurrent)})
}

func (engine *invariantEngine) nextDecisionID() RuntimeDecisionID {
	id := nextRuntimeDecisionID(engine.store.nextDecision)
	engine.store.nextDecision++
	return id
}

func (engine *invariantEngine) observeIngress(kind IngressObservationKind) {
	engine.store.mu.Lock()
	defer engine.store.mu.Unlock()
	engine.observeIngressLocked(kind)
}

func (engine *invariantEngine) observeIngressLocked(kind IngressObservationKind) {
	if len(engine.store.observations) >= maxIngressObservations {
		return
	}
	engine.store.observations = append(engine.store.observations, IngressObservation{
		ID: nextRuntimeObservationID(engine.store.nextObservation), Kind: kind,
	})
	engine.store.nextObservation++
}

func (engine *invariantEngine) Inspect(ctx context.Context, ref RuntimeRunRef) (RuntimeSnapshot, error) {
	if engine.controls.isCrashed() || ctx == nil || ctx.Err() != nil {
		return RuntimeSnapshot{}, newError(ErrorDependencyUnavailable)
	}
	if ref.SchemaVersion == 0 {
		engine.observeIngress(IngressMalformed)
		return RuntimeSnapshot{}, newError(ErrorInvalidRequest)
	}
	if ref.SchemaVersion.Major() != SchemaV1.Major() ||
		(ref.ProjectionVersion != SnapshotSchemaCurrent && ref.ProjectionVersion != SnapshotSchemaV1) {
		engine.observeIngress(IngressUnsupportedSchema)
		return RuntimeSnapshot{}, newError(ErrorUnsupportedSchema)
	}
	if !validOpaqueID(ref.PersonalWorkspaceID.String()) || !validOpaqueID(ref.RuntimeRunID.String()) || !validAuthority(ref.Authority) {
		engine.observeIngress(IngressMalformed)
		return RuntimeSnapshot{}, newError(ErrorInvalidRequest)
	}
	engine.store.mu.Lock()
	defer engine.store.mu.Unlock()
	record, exists := engine.store.runtimes[ref.RuntimeRunID]
	if !exists || !authorized(record, ref.PersonalWorkspaceID, ref.Authority) {
		engine.observeIngressLocked(IngressAuthorizationDenied)
		return RuntimeSnapshot{}, newError(ErrorAuthorizationDenied)
	}
	projected, representable := renderSnapshot(record, ref.ProjectionVersion)
	if !representable {
		engine.observeIngressLocked(IngressUnsupportedSchema)
		return RuntimeSnapshot{}, newError(ErrorUnsupportedSchema)
	}
	return projected, nil
}

func authorized(record *runtimeRecord, workspaceID PersonalWorkspaceID, authority RuntimeAuthority) bool {
	return record != nil && record.fixture.PersonalWorkspaceID == workspaceID && record.fixture.Owner == authority
}

func snapshot(record *runtimeRecord, version SchemaVersion) RuntimeSnapshot {
	projected, _ := renderSnapshot(record, version)
	return projected
}

type snapshotRenderer func(*runtimeRecord) (RuntimeSnapshot, bool)

var snapshotRenderers = map[SchemaVersion]snapshotRenderer{
	SnapshotSchemaV1:      renderSnapshotV1,
	SnapshotSchemaCurrent: renderSnapshotCurrent,
}

func renderSnapshot(record *runtimeRecord, version SchemaVersion) (RuntimeSnapshot, bool) {
	renderer, registered := snapshotRenderers[version]
	if !registered {
		return RuntimeSnapshot{}, false
	}
	return renderer(record)
}

func renderSnapshotCurrent(record *runtimeRecord) (RuntimeSnapshot, bool) {
	if !snapshotVariantsKnown(record) {
		return RuntimeSnapshot{}, false
	}
	return buildSnapshot(record, SnapshotSchemaCurrent), true
}

func renderSnapshotV1(record *runtimeRecord) (RuntimeSnapshot, bool) {
	if !snapshotVariantsKnown(record) || record.capacity.Physical == PhysicalCapacityUnknownOrQuarantined ||
		record.reconciliation == ReconciliationRequiredStatus || record.lease.Disposition != LeaseDispositionNone ||
		record.node != (RuntimeNodeSnapshot{}) || record.cleanup != (RuntimeLeaseCleanupSnapshot{}) ||
		record.capacityEvidence.PhysicalCapacityReleaseReady != (PhysicalCapacityReleaseReadyEvidence{}) {
		return RuntimeSnapshot{}, false
	}
	return buildSnapshot(record, SnapshotSchemaV1), true
}

func buildSnapshot(record *runtimeRecord, version SchemaVersion) RuntimeSnapshot {
	return RuntimeSnapshot{
		SchemaVersion: version, RuntimeRunID: record.fixture.RuntimeRunID,
		RuntimeRevision: record.fixture.RuntimeRevision, State: record.fixture.State, Outcome: record.fixture.Outcome,
		Operation: record.operation, RuntimeFence: record.fixture.RuntimeFence, Lease: record.lease, Node: record.node,
		Cleanup:  record.cleanup,
		Deadline: record.deadline, LeaseAcquireBy: record.leaseAcquireBy, Cancellation: record.cancellation,
		EvidenceRoot: record.evidenceRoot, Capacity: record.capacity, CapacityEvidence: record.capacityEvidence,
		PreLeaseTerminalReason: record.preLeaseTerminalReason,
		Reconciliation:         record.reconciliation,
	}
}

func validRuntimeFixture(fixture RuntimeFixture) bool {
	validTerminalEvidence := fixture.TerminalEvidenceID == (EvidenceID{}) ||
		(validOpaqueID(fixture.TerminalEvidenceID.String()) && fixture.State == RuntimeTerminal && fixture.Outcome != RuntimeOutcomeNone)
	return validTerminalEvidence && validOpaqueID(fixture.PersonalWorkspaceID.String()) && validOpaqueID(fixture.TaskID.String()) &&
		validOpaqueID(fixture.PhaseRunID.String()) && validOpaqueID(fixture.RuntimeRunID.String()) && validAuthority(fixture.Owner) &&
		fixture.TaskRevision > 0 && fixture.RuntimeRevision > 0 && fixture.OperationGeneration > 0 && fixture.RuntimeFence > 0 &&
		fixture.SafetyEpoch > 0 && fixture.State != 0
}

func snapshotVariantsKnown(record *runtimeRecord) bool {
	if record == nil || !knownRuntimeState(record.fixture.State) || !knownRuntimeOutcome(record.fixture.Outcome) ||
		!knownOperationStatus(record.operation.Status) || !knownLeaseStatus(record.lease.AcquireStatus) ||
		!knownCancellationStatus(record.cancellation.Status) || !knownCapacity(record.capacity) ||
		!knownLeaseLifecycleSnapshot(record.lease) || !knownNodeSnapshot(record.node) ||
		!knownLeaseNodeBinding(record) ||
		!knownLeaseCleanupSnapshot(record.cleanup) ||
		!knownPhysicalReleaseEvidence(record.capacityEvidence.PhysicalCapacityReleaseReady) ||
		!knownPreLeaseTerminalReason(record.preLeaseTerminalReason) ||
		!knownReconciliation(record.reconciliation) || !knownEvidenceRoot(record.evidenceRoot) {
		return false
	}
	return true
}

func knownLeaseLifecycleSnapshot(lease RuntimeLeaseSnapshot) bool {
	if lease.Disposition == LeaseDispositionNone {
		return lease.AcquireStatus != LeaseGranted && lease.LeaseID == (SandboxLeaseID{}) &&
			lease.Generation == 0 && lease.Fence == 0 && lease.ExpiresAt.IsZero() &&
			lease.SandboxID == (SandboxID{}) && lease.SandboxGeneration == 0 && lease.SandboxFence == 0 &&
			lease.WorkerAuthorityID == (WorkerAuthorityID{}) && lease.WorkerGeneration == 0 &&
			lease.NodeAuthorityID == (NodeAuthorityID{}) && lease.AuthorizationGeneration == 0 &&
			lease.AuthorizationExpiresAt.IsZero()
	}
	return lease.Disposition >= LeaseActive && lease.Disposition <= LeaseReleased &&
		lease.AcquireStatus == LeaseGranted && validOpaqueID(lease.LeaseID.String()) &&
		lease.Generation > 0 && lease.Fence > 0 && !lease.ExpiresAt.IsZero() &&
		validOpaqueID(lease.SandboxID.String()) && lease.SandboxGeneration > 0 && lease.SandboxFence > 0 &&
		validOpaqueID(lease.WorkerAuthorityID.String()) && lease.WorkerGeneration > 0 &&
		validOpaqueID(lease.NodeAuthorityID.String()) && lease.AuthorizationGeneration > 0 &&
		!lease.AuthorizationExpiresAt.IsZero()
}

func knownLeaseNodeBinding(record *runtimeRecord) bool {
	if record.lease.Disposition == LeaseDispositionNone {
		return record.node == (RuntimeNodeSnapshot{})
	}
	return record.operation.Status == OperationBound && record.node != (RuntimeNodeSnapshot{}) &&
		record.node.ExecutionNodeID == record.operation.ExecutionNodeID &&
		uint64(record.node.Generation) == record.operation.NodeCapacityGeneration
}

func knownNodeSnapshot(node RuntimeNodeSnapshot) bool {
	if node == (RuntimeNodeSnapshot{}) {
		return true
	}
	return validOpaqueID(node.ExecutionNodeID.String()) && node.Generation > 0 &&
		node.Readiness >= NodeReadinessUnknown && node.Readiness <= NodeUnavailable &&
		validOpaqueID(node.AttestationID.String()) && node.AttestationGeneration > 0 &&
		!node.AttestedAt.IsZero() && node.ExpiresAt.After(node.AttestedAt) &&
		node.Occupancy >= NodeUnoccupied && node.Occupancy <= NodeOccupancyUnknown &&
		node.Containment >= ContainmentPending && node.Containment <= ContainmentEstablished &&
		node.Reset >= ResetRequired && node.Reset <= ResetCompleted
}

func knownLeaseCleanupSnapshot(cleanup RuntimeLeaseCleanupSnapshot) bool {
	if cleanup.Status == LeaseCleanupNone {
		return cleanup == (RuntimeLeaseCleanupSnapshot{})
	}
	return cleanup.Status >= LeaseCleanupPending && cleanup.Status <= LeaseCleanupCompleted &&
		validOpaqueID(cleanup.OperationID.String()) && cleanup.CanonicalRequestDigest != (Digest{}) &&
		cleanup.StopMainProcess && cleanup.StopChildProcesses && cleanup.RevokeSecrets &&
		cleanup.RemoveNetwork && cleanup.FenceRuntimeView && cleanup.ReconcileContainment
}

func knownPhysicalReleaseEvidence(evidence PhysicalCapacityReleaseReadyEvidence) bool {
	if evidence == (PhysicalCapacityReleaseReadyEvidence{}) {
		return true
	}
	return validOpaqueID(evidence.WorkItemID.String()) && validOpaqueID(evidence.AdmissionGrantID.String()) &&
		evidence.GrantGeneration > 0 && validOpaqueID(evidence.RuntimeRunID.String()) &&
		validOpaqueID(evidence.StartOperationID.String()) && evidence.StartDigest != (Digest{}) &&
		validOpaqueID(evidence.ReleaseOperationID.String()) && evidence.ReleaseOperationDigest != (Digest{}) &&
		evidence.RuntimeRevision > 0 && evidence.RuntimeFence > 0 &&
		validOpaqueID(evidence.SandboxLeaseID.String()) && evidence.LeaseGeneration > 0 && evidence.LeaseFence > 0 &&
		validOpaqueID(evidence.SandboxID.String()) && evidence.SandboxGeneration > 0 && evidence.SandboxFence > 0 &&
		validOpaqueID(evidence.ExecutionNodeID.String()) && evidence.NodeCapacityGeneration > 0 &&
		validOpaqueID(evidence.ResetEvidenceID.String()) && evidence.ResetEvidenceDigest != (Digest{})
}

func knownRuntimeState(state RuntimeState) bool {
	return state >= RuntimeCreated && state <= RuntimeTerminal
}

func knownRuntimeOutcome(outcome RuntimeOutcome) bool {
	return outcome >= RuntimeOutcomeNone && outcome <= RuntimeRejected
}

func knownOperationStatus(status OperationBindingStatus) bool {
	return status == OperationUnbound || status == OperationBound
}

func knownLeaseStatus(status LeaseAcquireStatus) bool {
	return status >= LeaseNotRequested && status <= LeaseAcquireReconciliationRequired
}

func nodeEligibleForLease(node *ExecutionNodeFixture, record *runtimeRecord, now time.Time) bool {
	return node != nil && node.Generation == NodeGeneration(record.operation.NodeCapacityGeneration) &&
		node.Readiness == NodeReady && !node.Quarantined && node.Occupancy == NodeUnoccupied &&
		node.Containment == ContainmentEstablished && node.Reset == ResetCompleted &&
		!node.AttestedAt.After(now) && now.Before(node.ExpiresAt) && now.Before(node.AuthorizationExpiresAt) &&
		node.AuthorizationGeneration > 0 && node.ResourceClassID == record.operation.ResourceClassID &&
		node.ExecutionPolicyID == record.operation.ExecutionPolicyID &&
		node.ReleaseSafetyEpoch == record.fixture.SafetyEpoch && validOpaqueID(node.WorkerAuthorityID.String()) &&
		node.WorkerGeneration > 0 && validOpaqueID(node.NodeAuthorityID.String())
}

func nodeSnapshot(node ExecutionNodeFixture) RuntimeNodeSnapshot {
	return RuntimeNodeSnapshot{
		ExecutionNodeID: node.ExecutionNodeID, Generation: node.Generation, Readiness: node.Readiness,
		AttestationID: node.AttestationID, AttestationGeneration: node.AttestationGeneration,
		AttestedAt: node.AttestedAt, ExpiresAt: node.ExpiresAt, Occupancy: node.Occupancy,
		Quarantined: node.Quarantined, Containment: node.Containment, Reset: node.Reset,
	}
}

func validExecutionNodeFixture(node ExecutionNodeFixture) bool {
	return validOpaqueID(node.ExecutionNodeID.String()) && node.Generation > 0 && node.Readiness == NodeReady &&
		validOpaqueID(node.AttestationID.String()) && node.AttestationGeneration > 0 && !node.AttestedAt.IsZero() &&
		!node.ExpiresAt.IsZero() && node.ExpiresAt.After(node.AttestedAt) &&
		validOpaqueID(node.ResourceClassID.String()) && validOpaqueID(node.ExecutionPolicyID.String()) &&
		validOpaqueID(node.NodeAuthorityID.String()) && validOpaqueID(node.WorkerAuthorityID.String()) &&
		node.WorkerGeneration > 0 && node.AuthorizationGeneration > 0 &&
		node.AuthorizationExpiresAt.After(node.AttestedAt) && node.ReleaseSafetyEpoch > 0 &&
		node.Occupancy >= NodeUnoccupied && node.Occupancy <= NodeOccupancyUnknown &&
		node.Containment >= ContainmentPending && node.Containment <= ContainmentEstablished &&
		node.Reset >= ResetRequired && node.Reset <= ResetCompleted
}

func validQuotaReservationFixture(reservation QuotaReservationFixture) bool {
	return validOpaqueID(reservation.QuotaReservationID.String()) && reservation.Generation > 0 &&
		(reservation.Mode == QuotaReservationObservation || reservation.Mode == QuotaReservationEnforced) &&
		(reservation.State == QuotaReservationActive || reservation.State == QuotaReservationInactive) &&
		validOpaqueID(reservation.PersonalWorkspaceID.String()) && validOpaqueID(reservation.PhaseRunID.String()) &&
		reservation.Capability == ProviderCapabilityRequired && !reservation.ValidFrom.IsZero() &&
		reservation.ExpiresAt.After(reservation.ValidFrom)
}

func reservationEligibleForLease(
	reservations map[QuotaReservationID]*QuotaReservationFixture,
	start StartRuntimeRun,
	now time.Time,
) bool {
	if start.ProviderCapability == ProviderCapabilityNone {
		return true
	}
	if start.ProviderCapability != ProviderCapabilityRequired || start.ProviderBinding == nil {
		return false
	}
	reservation := reservations[start.ProviderBinding.QuotaReservationID]
	return reservation != nil && reservation.Generation == start.ProviderBinding.Generation &&
		reservation.Mode == start.ProviderBinding.Mode && reservation.State == QuotaReservationActive &&
		reservation.PersonalWorkspaceID == start.PersonalWorkspaceID && reservation.PhaseRunID == start.PhaseRunID &&
		reservation.Capability == ProviderCapabilityRequired && !now.Before(reservation.ValidFrom) &&
		now.Before(reservation.ExpiresAt)
}

func earliestTime(values ...time.Time) time.Time {
	result := values[0]
	for _, value := range values[1:] {
		if value.Before(result) {
			result = value
		}
	}
	return result.UTC()
}

func knownCancellationStatus(status CancellationStatus) bool {
	return status == CancellationNotRequested || status == CancellationAccepted
}

func knownCapacity(capacity RuntimeCapacitySnapshot) bool {
	return capacity.LogicalRelease >= LogicalCapacityHeld && capacity.LogicalRelease <= LogicalCapacityReleaseReady &&
		capacity.NoLease >= NoLeaseDispositionNone && capacity.NoLease <= NoLeaseDispositionRecorded &&
		capacity.Physical >= PhysicalCapacityNotApplicable && capacity.Physical <= PhysicalCapacityReleaseReady
}

func knownReconciliation(status ReconciliationStatus) bool {
	return status >= ReconciliationStable && status <= ReconciliationRequiredStatus
}

func knownEvidenceRoot(root EvidenceRootSnapshot) bool {
	if root.EvidenceRootID == (EvidenceRootID{}) {
		return root.SchemaVersion == 0 && root.Digest == (Digest{})
	}
	return validOpaqueID(root.EvidenceRootID.String()) && root.SchemaVersion.Major() == SchemaV1.Major() && root.Digest != (Digest{})
}

func validAdmissionGrantFixture(fixture AdmissionGrantFixture) bool {
	return validOpaqueID(fixture.AdmissionGrantID.String()) && validOpaqueID(fixture.WorkItemID.String()) && fixture.Generation > 0 &&
		validOpaqueID(fixture.PersonalWorkspaceID.String()) && validOpaqueID(fixture.RuntimeRunID.String()) &&
		validOpaqueID(fixture.OperationID.String()) && fixture.CanonicalStartDigest != (Digest{}) && !fixture.ExpiresAt.IsZero()
}
