package runtimeexecution

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// LocalDevelopmentPolicy carries the explicit local-development policy that
// the owned local-development adapter applies. It is deliberately a local
// developer convenience and is never presented as production hardening.
type LocalDevelopmentPolicy struct {
	// LeaseDuration is the sandbox lease lifetime granted by the local
	// development lease acquisition adapter.
	LeaseDuration time.Duration
	// NodeReady makes the local node immediately ready for placement.
	NodeReady bool
	// WorkerClass pins the local worker capability class.
	WorkerClass WorkerClass
	// GatewayBypass disables provider Gateway/Usage prerequisites locally.
	GatewayBypass bool
}

// LocalDevelopmentJournal is a durable JSON snapshot store for the
// local-development adapter. It is an opaque owned store: it never exposes
// host paths, sessions, CLI seams, or arbitrary shell control, and it is not a
// legacy execution authority.
type LocalDevelopmentJournal struct {
	mu   sync.Mutex
	path string
	data []byte
}

// NewLocalDevelopmentJournal opens (creating if necessary) an opaque durable
// journal at the given path. An empty path keeps the journal in memory only.
func NewLocalDevelopmentJournal(path string) (*LocalDevelopmentJournal, error) {
	journal := &LocalDevelopmentJournal{path: path}
	if path == "" {
		return journal, nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, newError(ErrorInvalidRequest)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, newError(ErrorInvalidRequest)
	}
	journal.path = abs
	encoded, err := os.ReadFile(abs)
	if err == nil && len(encoded) > 0 {
		var state localDevelopmentJournalState
		if err := json.Unmarshal(encoded, &state); err != nil {
			return nil, newError(ErrorIntegrityConflict)
		}
		journal.data = append([]byte(nil), encoded...)
	}
	return journal, nil
}

type localDevelopmentJournalState struct {
	SchemaVersion          uint32                                     `json:"schema_version"`
	Runtimes               []localDevelopmentRuntimeWire              `json:"runtimes"`
	Grants                 []localDevelopmentGrantWire                `json:"grants"`
	Nodes                  []localDevelopmentNodeWire                 `json:"nodes"`
	Reservations           []localDevelopmentReservationWire          `json:"reservations"`
	MaintenanceAuthorities []localDevelopmentMaintenanceAuthorityWire `json:"maintenance_authorities"`
	NextDecision           uint64                                     `json:"next_decision"`
	NextObservation        uint64                                     `json:"next_observation"`
	NextLease              uint64                                     `json:"next_lease"`
	NextSandbox            uint64                                     `json:"next_sandbox"`
}

func (journal *LocalDevelopmentJournal) writeLocked(encoded []byte) error {
	if journal.path == "" {
		journal.data = append([]byte(nil), encoded...)
		return nil
	}
	tmp := journal.path + ".tmp"
	if err := os.WriteFile(tmp, encoded, 0o600); err != nil {
		return newError(ErrorDependencyUnavailable)
	}
	if err := os.Rename(tmp, journal.path); err != nil {
		return newError(ErrorDependencyUnavailable)
	}
	journal.data = append([]byte(nil), encoded...)
	return nil
}

// LocalDevelopmentAuthority is the owned local-development adapter for the
// Runtime Execution seam. It runs the same public Execute/Inspect semantic
// suite as every other implementation, retains canonical request, Work Item
// and grant generation, the independent Runtime fence, LeaseAcquireBy, the
// stable lease-acquire operation, the three capacity dispositions, post-lease
// C04/Gateway prerequisites, capsule, evidence and safe-error semantics. It
// uses an explicit local policy and an opaque durable journal for
// restart/reconcile, and it never restores a legacy CLI/session/recent-path/
// arbitrary-shell seam. Passing these contracts is not production hardening
// proof.
type LocalDevelopmentAuthority struct {
	store   *memoryStore
	engine  *invariantEngine
	clock   *controlledClock
	journal *LocalDevelopmentJournal
	policy  LocalDevelopmentPolicy

	mu                       sync.Mutex
	runtimeBindingValidator  RuntimeBindingValidator
	immutableInputValidator  ImmutableInputValidator
	executionCapsuleResolver ExecutionCapsuleResolver
	runtimeViewPrerequisite  RuntimeViewPrerequisitePort
	gatewayGrants            GatewayGrantAdapter
	gatewayRecovery          GatewayRecoveryAuthority
	gatewayCallAuthority     GatewayCallExternalAuthority
	usageReceipts            UsageReceiptEvidenceSource
	agentWorker              workerCapabilityAdapter
	toolWorker               workerCapabilityAdapter
}

// LocalDevelopmentConfig configures the owned local-development adapter.
type LocalDevelopmentConfig struct {
	Now                      func() time.Time
	Policy                   LocalDevelopmentPolicy
	Journal                  *LocalDevelopmentJournal
	Runtimes                 []RuntimeFixture
	AdmissionGrants          []AdmissionGrantFixture
	Nodes                    []ExecutionNodeFixture
	QuotaReservations        []QuotaReservationFixture
	MaintenanceAuthorities   []RuntimeMaintenanceAuthorityBinding
	LeaseAcquisition         LeaseAcquisitionAdapter
	RuntimeBindingValidator  RuntimeBindingValidator
	ImmutableInputValidator  ImmutableInputValidator
	ExecutionCapsuleResolver ExecutionCapsuleResolver
	RuntimeViewPrerequisite  RuntimeViewPrerequisitePort
	GatewayGrants            GatewayGrantAdapter
	GatewayRecovery          GatewayRecoveryAuthority
	GatewayCallAuthority     GatewayCallExternalAuthority
	GatewayGrantLifetime     time.Duration
	UsageReceipts            UsageReceiptEvidenceSource
}

// NewLocalDevelopmentAuthority creates the owned local-development adapter.
func NewLocalDevelopmentAuthority(config LocalDevelopmentConfig) (*LocalDevelopmentAuthority, error) {
	now := config.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	journal := config.Journal
	if journal == nil {
		journal, _ = NewLocalDevelopmentJournal("")
	}
	policy := config.Policy
	if policy.LeaseDuration == 0 {
		policy.LeaseDuration = 90 * time.Second
	}
	if policy.WorkerClass == 0 {
		policy.WorkerClass = WorkerTool
	}
	clock := &controlledClock{now: now().UTC()}
	store := newLocalDevelopmentMemoryStore(config, journal)
	leaseAcquisition := config.LeaseAcquisition
	if leaseAcquisition == nil {
		leaseAcquisition = LeaseAcquisitionAdapterFunc(func(
			_ context.Context, _ LeaseAcquisitionRequest,
		) (LeaseAcquisitionObservation, error) {
			return LeaseAcquisitionObservation{Disposition: LeaseAcquisitionReady}, nil
		})
	}
	capsuleResolver := config.ExecutionCapsuleResolver
	if capsuleResolver == nil {
		capsuleResolver = deterministicCapsuleResolver{}
	}
	authority := &LocalDevelopmentAuthority{
		store: store, clock: clock, journal: journal, policy: policy,
		runtimeBindingValidator:  config.RuntimeBindingValidator,
		immutableInputValidator:  config.ImmutableInputValidator,
		executionCapsuleResolver: capsuleResolver,
		runtimeViewPrerequisite:  config.RuntimeViewPrerequisite,
		gatewayGrants:            config.GatewayGrants,
		gatewayRecovery:          config.GatewayRecovery,
		gatewayCallAuthority:     config.GatewayCallAuthority,
		usageReceipts:            config.UsageReceipts,
	}
	authority.engine = newLocalDevelopmentEngine(store, clock, leaseAcquisition, config, authority)
	authority.persistLocked()
	return authority, nil
}

// Restart returns a fresh authority over the same durable journal, simulating
// a process restart. Committed facts are reloaded and exact replay remains
// stable.
func (authority *LocalDevelopmentAuthority) Restart() (*LocalDevelopmentAuthority, error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	journal := authority.journal
	var reloaded *LocalDevelopmentJournal
	if journal.path == "" {
		// In-memory journal: share the already-serialized state so restart
		// preserves committed facts without a file.
		reloaded = &LocalDevelopmentJournal{data: append([]byte(nil), journal.data...)}
	} else {
		var err error
		reloaded, err = NewLocalDevelopmentJournal(journal.path)
		if err != nil {
			return nil, err
		}
	}
	config := LocalDevelopmentConfig{
		Now:                     func() time.Time { return authority.clock.current() },
		Policy:                  authority.policy,
		Journal:                 reloaded,
		RuntimeBindingValidator: authority.runtimeBindingValidator,
		ImmutableInputValidator: authority.immutableInputValidator,
		RuntimeViewPrerequisite: authority.runtimeViewPrerequisite,
		GatewayGrants:           authority.gatewayGrants,
		GatewayRecovery:         authority.gatewayRecovery,
		GatewayCallAuthority:    authority.gatewayCallAuthority,
		UsageReceipts:           authority.usageReceipts,
	}
	restarted, err := NewLocalDevelopmentAuthority(config)
	if err != nil {
		return nil, err
	}
	restarted.agentWorker = authority.agentWorker
	restarted.toolWorker = authority.toolWorker
	restarted.engine.agentWorker = authority.agentWorker
	restarted.engine.toolWorker = authority.toolWorker
	return restarted, nil
}

// SetWorkerAdapters attaches the owned Agent/Tool worker capability adapters
// to the local-development adapter and its engine. Worker adapters are
// replaceable owned execution backends, never a legacy CLI/session seam.
func (authority *LocalDevelopmentAuthority) SetWorkerAdapters(
	agent workerCapabilityAdapter,
	tool workerCapabilityAdapter,
) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.agentWorker = agent
	authority.toolWorker = tool
	authority.engine.agentWorker = agent
	authority.engine.toolWorker = tool
}

// FailNextAt injects a one-shot fault at the given boundary. It mirrors the
// deterministic harness control so the shared fault matrix can exercise the
// same boundaries on the local-development adapter. It is a developer
// diagnostic control, not a production surface.
func (authority *LocalDevelopmentAuthority) FailNextAt(point FaultPoint) error {
	if point < FaultBeforeValidation || point > FaultAfterNoLeaseCommit {
		return newError(ErrorInvalidRequest)
	}
	authority.engine.controls.mu.Lock()
	defer authority.engine.controls.mu.Unlock()
	authority.engine.controls.nextFault = point
	return nil
}

// CrashNextAt injects a one-shot crash at the given boundary.
func (authority *LocalDevelopmentAuthority) CrashNextAt(boundary CrashBoundary) error {
	if boundary < CrashBeforeCommit || boundary > CrashAfterNoLeaseCommit {
		return newError(ErrorInvalidRequest)
	}
	authority.engine.controls.mu.Lock()
	defer authority.engine.controls.mu.Unlock()
	authority.engine.controls.nextCrash = boundary
	return nil
}

// LoseNextResponse injects one response loss after commit.
func (authority *LocalDevelopmentAuthority) LoseNextResponse() {
	authority.engine.controls.mu.Lock()
	defer authority.engine.controls.mu.Unlock()
	authority.engine.controls.loseResponse = true
}

func (authority *LocalDevelopmentAuthority) persistLocked() {
	authority.journal.mu.Lock()
	defer authority.journal.mu.Unlock()
	state := snapshotLocalDevelopmentState(authority.store)
	encoded, err := json.Marshal(state)
	if err != nil {
		return
	}
	_ = authority.journal.writeLocked(encoded)
}

// ---------------------------------------------------------------------------
// Seam implementations
// ---------------------------------------------------------------------------

var _ RuntimeExecution = (*LocalDevelopmentAuthority)(nil)
var _ RuntimeMaintenance = (*LocalDevelopmentAuthority)(nil)
var _ OwnedDispatch = (*LocalDevelopmentAuthority)(nil)
var _ workerProtocol = (*LocalDevelopmentAuthority)(nil)
var _ OperationalDiagnostics = (*LocalDevelopmentAuthority)(nil)

func (authority *LocalDevelopmentAuthority) Execute(ctx context.Context, command RuntimeCommand) (RuntimeDecision, error) {
	decision, err := authority.engine.Execute(ctx, command)
	if err == nil {
		authority.persistLocked()
	}
	return decision, err
}

func (authority *LocalDevelopmentAuthority) Inspect(ctx context.Context, ref RuntimeRunRef) (RuntimeSnapshot, error) {
	return authority.engine.Inspect(ctx, ref)
}

func (authority *LocalDevelopmentAuthority) Maintain(ctx context.Context, command RuntimeMaintenanceCommand) (RuntimeMaintenanceDecision, error) {
	decision, err := authority.engine.Maintain(ctx, command)
	if err == nil {
		authority.persistLocked()
	}
	return decision, err
}

func (authority *LocalDevelopmentAuthority) ClaimDispatch(ctx context.Context, request DispatchClaimRequest) (DispatchDelivery, error) {
	delivery, err := authority.engine.ClaimDispatch(ctx, request)
	if err == nil {
		authority.persistLocked()
	}
	return delivery, err
}

func (authority *LocalDevelopmentAuthority) AcknowledgeDispatch(ctx context.Context, request DispatchAcknowledgementRequest) (DispatchAcknowledgement, error) {
	ack, err := authority.engine.AcknowledgeDispatch(ctx, request)
	if err == nil {
		authority.persistLocked()
	}
	return ack, err
}

func (authority *LocalDevelopmentAuthority) accept(ctx context.Context, command workerAccept) (workerOperationAck, error) {
	ack, err := authority.engine.accept(ctx, command)
	if err == nil {
		authority.persistLocked()
	}
	return ack, err
}

func (authority *LocalDevelopmentAuthority) heartbeat(ctx context.Context, heartbeat workerHeartbeat) (workerLeaseDecision, error) {
	decision, err := authority.engine.heartbeat(ctx, heartbeat)
	if err == nil {
		authority.persistLocked()
	}
	return decision, err
}

func (authority *LocalDevelopmentAuthority) observe(ctx context.Context, request workerObserve) (workerObservationResult, error) {
	result, err := authority.engine.observe(ctx, request)
	if err == nil {
		authority.persistLocked()
	}
	return result, err
}

func (authority *LocalDevelopmentAuthority) stop(ctx context.Context, intent workerStopIntent) (workerStopAck, error) {
	ack, err := authority.engine.stop(ctx, intent)
	if err == nil {
		authority.persistLocked()
	}
	return ack, err
}

func (authority *LocalDevelopmentAuthority) Diagnose(ctx context.Context, query OperationalDiagnosticQuery) (OperationalDiagnosticView, error) {
	return authority.engine.Diagnose(ctx, query)
}

// ---------------------------------------------------------------------------
// Local store construction
// ---------------------------------------------------------------------------

func newLocalDevelopmentMemoryStore(config LocalDevelopmentConfig, journal *LocalDevelopmentJournal) *memoryStore {
	store := &memoryStore{
		runtimes:               make(map[RuntimeRunID]*runtimeRecord),
		grants:                 make(map[grantKey]AdmissionGrantFixture),
		nodes:                  make(map[ExecutionNodeID]*ExecutionNodeFixture),
		reservations:           make(map[QuotaReservationID]*QuotaReservationFixture),
		maintenance:            make(map[OperationID]RuntimeMaintenanceDecision),
		workerHeartbeats:       make(map[OperationID]retainedWorkerHeartbeat),
		maintenanceAuthorities: make(map[maintenanceAuthorityKey]maintenanceCallerAuthority),
		cleanupDebts:           make(map[string]*cleanupDebtRecord),
		cleanupProofs:          make(map[cleanupProofKey]cleanupResolutionProofState),
		nextDecision:           1,
		nextObservation:        1,
		nextLease:              1,
		nextSandbox:            1,
	}
	if journal != nil {
		journal.mu.Lock()
		state := decodeLocalDevelopmentJournalState(journal.data)
		journal.mu.Unlock()
		if state != nil {
			restoreLocalDevelopmentStore(store, *state)
		}
	}
	for _, fixture := range config.Runtimes {
		if _, exists := store.runtimes[fixture.RuntimeRunID]; exists {
			continue
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
			readiness: fixture.Readiness, runtimeViewBinding: fixture.RuntimeViewBinding,
			gateway: fixture.Gateway, usage: fixture.Usage, worker: fixture.Worker,
		}
	}
	for _, grant := range config.AdmissionGrants {
		if grant.ExecutionNodeID == (ExecutionNodeID{}) {
			grant.ExecutionNodeID = ExecutionNodeID{value: "local-execution-node-" + grant.AdmissionGrantID.String()}
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
		key := grantKey{id: grant.AdmissionGrantID, generation: grant.Generation}
		if _, exists := store.grants[key]; exists {
			continue
		}
		grant.ExpiresAt = grant.ExpiresAt.UTC()
		store.grants[key] = grant
	}
	for index := range config.Nodes {
		node := config.Nodes[index]
		if node.Occupancy == 0 {
			node.Occupancy = NodeUnoccupied
		}
		if _, exists := store.nodes[node.ExecutionNodeID]; exists {
			continue
		}
		node.AttestedAt = node.AttestedAt.UTC()
		node.ExpiresAt = node.ExpiresAt.UTC()
		node.AuthorizationExpiresAt = node.AuthorizationExpiresAt.UTC()
		copyNode := node
		store.nodes[node.ExecutionNodeID] = &copyNode
	}
	for index := range config.QuotaReservations {
		reservation := config.QuotaReservations[index]
		if _, exists := store.reservations[reservation.QuotaReservationID]; exists {
			continue
		}
		reservation.ValidFrom = reservation.ValidFrom.UTC()
		reservation.ExpiresAt = reservation.ExpiresAt.UTC()
		copyReservation := reservation
		store.reservations[reservation.QuotaReservationID] = &copyReservation
	}
	for _, binding := range config.MaintenanceAuthorities {
		key := maintenanceAuthorityKey{executionNodeID: binding.executionNodeID, kind: binding.caller.kind}
		if retained, exists := store.maintenanceAuthorities[key]; exists && retained != binding.caller {
			continue
		}
		store.maintenanceAuthorities[key] = binding.caller
	}
	return store
}

func newLocalDevelopmentEngine(
	store *memoryStore,
	clock *controlledClock,
	leaseAcquisition LeaseAcquisitionAdapter,
	config LocalDevelopmentConfig,
	authority *LocalDevelopmentAuthority,
) *invariantEngine {
	grantLifetime := config.GatewayGrantLifetime
	if grantLifetime == 0 {
		grantLifetime = time.Minute
	}
	usageReceipts := config.UsageReceipts
	if usageReceipts == nil {
		usageReceipts, _ = config.GatewayGrants.(UsageReceiptEvidenceSource)
	}
	return &invariantEngine{
		store: store, clock: clock, controls: &harnessControls{},
		leaseAcquisition:         leaseAcquisition,
		runtimeBindingValidator:  config.RuntimeBindingValidator,
		immutableInputValidator:  config.ImmutableInputValidator,
		executionCapsuleResolver: authority.executionCapsuleResolver,
		runtimeViewPrerequisite:  config.RuntimeViewPrerequisite,
		gatewayGrants:            config.GatewayGrants, gatewayRecovery: config.GatewayRecovery,
		gatewayCallAuthority: config.GatewayCallAuthority, grantLifetime: grantLifetime,
		usageReceipts: usageReceipts, agentWorker: authority.agentWorker, toolWorker: authority.toolWorker,
	}
}

// ---------------------------------------------------------------------------
// Journal snapshot codec
// ---------------------------------------------------------------------------

func decodeLocalDevelopmentJournalState(data []byte) *localDevelopmentJournalState {
	if len(data) == 0 {
		return &localDevelopmentJournalState{}
	}
	var state localDevelopmentJournalState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil
	}
	return &state
}

func snapshotLocalDevelopmentState(store *memoryStore) localDevelopmentJournalState {
	store.mu.Lock()
	defer store.mu.Unlock()
	state := localDevelopmentJournalState{
		SchemaVersion:   1,
		NextDecision:    store.nextDecision,
		NextObservation: store.nextObservation,
		NextLease:       store.nextLease,
		NextSandbox:     store.nextSandbox,
	}
	for _, record := range store.runtimes {
		wire, err := encodeLocalDevelopmentRuntime(record)
		if err != nil {
			continue
		}
		state.Runtimes = append(state.Runtimes, wire)
	}
	for _, grant := range store.grants {
		state.Grants = append(state.Grants, encodeLocalDevelopmentGrant(grant))
	}
	for _, node := range store.nodes {
		state.Nodes = append(state.Nodes, encodeLocalDevelopmentNode(*node))
	}
	for _, reservation := range store.reservations {
		state.Reservations = append(state.Reservations, encodeLocalDevelopmentReservation(*reservation))
	}
	for key, caller := range store.maintenanceAuthorities {
		state.MaintenanceAuthorities = append(state.MaintenanceAuthorities,
			encodeLocalDevelopmentMaintenanceAuthority(key, caller))
	}
	return state
}

func restoreLocalDevelopmentStore(store *memoryStore, state localDevelopmentJournalState) {
	if state.NextDecision != 0 {
		store.nextDecision = state.NextDecision
	}
	if state.NextObservation != 0 {
		store.nextObservation = state.NextObservation
	}
	if state.NextLease != 0 {
		store.nextLease = state.NextLease
	}
	if state.NextSandbox != 0 {
		store.nextSandbox = state.NextSandbox
	}
	for _, wire := range state.Runtimes {
		record, err := decodeLocalDevelopmentRuntime(wire)
		if err != nil {
			continue
		}
		store.runtimes[record.fixture.RuntimeRunID] = record
	}
	for _, wire := range state.Grants {
		grant := decodeLocalDevelopmentGrant(wire)
		store.grants[grantKey{id: grant.AdmissionGrantID, generation: grant.Generation}] = grant
	}
	for _, wire := range state.Nodes {
		node := decodeLocalDevelopmentNode(wire)
		copyNode := node
		store.nodes[node.ExecutionNodeID] = &copyNode
	}
	for _, wire := range state.Reservations {
		reservation := decodeLocalDevelopmentReservation(wire)
		copyReservation := reservation
		store.reservations[reservation.QuotaReservationID] = &copyReservation
	}
	for _, wire := range state.MaintenanceAuthorities {
		key := maintenanceAuthorityKey{
			executionNodeID: ExecutionNodeID{value: wire.ExecutionNodeID},
			kind:            wire.Kind,
		}
		store.maintenanceAuthorities[key] = maintenanceCallerAuthority{
			kind: wire.Kind, id: AuthorityID{value: wire.ID}, generation: wire.Generation,
		}
	}
}
