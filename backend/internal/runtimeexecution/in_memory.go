package runtimeexecution

import (
	"context"
	"sync"
	"time"
)

type DeterministicIDConfig struct {
	DecisionStart    uint64
	ObservationStart uint64
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
)

type CrashBoundary uint8

const (
	CrashBeforeCommit CrashBoundary = iota + 1
	CrashAfterCommit
)

type RuntimeFixture struct {
	PersonalWorkspaceID PersonalWorkspaceID
	TaskID              TaskID
	PhaseRunID          PhaseRunID
	RuntimeRunID        RuntimeRunID
	Owner               RuntimeAuthority
	TaskRevision        TaskRevision
	RuntimeRevision     RuntimeRevision
	OperationGeneration OperationGeneration
	RuntimeFence        RuntimeFence
	SafetyEpoch         ReleaseSafetyEpoch
	State               RuntimeState
	Outcome             RuntimeOutcome
	TerminalEvidenceID  EvidenceID
	Operation           RuntimeOperationBinding
	Lease               RuntimeLeaseSnapshot
	Deadline            time.Time
	LeaseAcquireBy      time.Time
	Cancellation        RuntimeCancellationSnapshot
	EvidenceRoot        EvidenceRootSnapshot
	Capacity            RuntimeCapacitySnapshot
	Reconciliation      ReconciliationStatus
}

type AdmissionGrantFixture struct {
	AdmissionGrantID     AdmissionGrantID
	WorkItemID           WorkItemID
	Generation           AdmissionGrantGeneration
	PersonalWorkspaceID  PersonalWorkspaceID
	RuntimeRunID         RuntimeRunID
	OperationID          OperationID
	CanonicalStartDigest Digest
	ExpiresAt            time.Time
	Current              bool
}

type HarnessConfig struct {
	Now             time.Time
	IDs             DeterministicIDConfig
	Runtimes        []RuntimeFixture
	AdmissionGrants []AdmissionGrantFixture
}

type DeterministicHarness struct {
	Runtime  RuntimeExecution
	store    *memoryStore
	clock    *controlledClock
	controls *harnessControls
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
	}
	if store.nextDecision == 0 {
		store.nextDecision = 1
	}
	if store.nextObservation == 0 {
		store.nextObservation = 1
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
			capacity: capacity, reconciliation: reconciliation,
		}
	}
	for _, grant := range config.AdmissionGrants {
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
	clock := &controlledClock{now: config.Now.UTC()}
	return newHarness(store, clock), nil
}

func newHarness(store *memoryStore, clock *controlledClock) *DeterministicHarness {
	controls := &harnessControls{}
	engine := &invariantEngine{store: store, clock: clock, controls: controls}
	return &DeterministicHarness{Runtime: engine, store: store, clock: clock, controls: controls}
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
	if point < FaultBeforeValidation || point > FaultBeforeResponse {
		return newError(ErrorInvalidRequest)
	}
	harness.controls.mu.Lock()
	defer harness.controls.mu.Unlock()
	harness.controls.nextFault = point
	return nil
}

func (harness *DeterministicHarness) CrashNextAt(boundary CrashBoundary) error {
	if boundary != CrashBeforeCommit && boundary != CrashAfterCommit {
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
	return newHarness(harness.store, harness.clock)
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
	fixture             RuntimeFixture
	bindings            map[OperationID]Digest
	decisions           map[decisionAttemptKey]RuntimeDecisionFact
	acceptedStart       RuntimeDecisionFact
	acceptedStartDigest Digest
	operation           RuntimeOperationBinding
	deadline            time.Time
	leaseAcquireBy      time.Time
	lease               RuntimeLeaseSnapshot
	cancellation        RuntimeCancellationSnapshot
	evidenceRoot        EvidenceRootSnapshot
	capacity            RuntimeCapacitySnapshot
	reconciliation      ReconciliationStatus
}

type invariantEngine struct {
	store    *memoryStore
	clock    *controlledClock
	controls *harnessControls
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
		return engine.executeStart(typed)
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
	record.fixture.RuntimeRevision++
	record.fixture.OperationGeneration++
	record.fixture.RuntimeFence++
	record.fixture.State = RuntimeTerminal
	record.fixture.Outcome = RuntimeCancelled
	record.operation = RuntimeOperationBinding{
		Status: OperationBound, OperationID: command.OperationID, Digest: digest,
		Generation:       record.fixture.OperationGeneration,
		AdmissionGrantID: record.operation.AdmissionGrantID, GrantGeneration: record.operation.GrantGeneration,
	}
	record.lease.AcquireStatus = LeaseNotRequested
	record.cancellation = RuntimeCancellationSnapshot{
		Status: CancellationAccepted, OperationID: command.OperationID, Reason: command.Reason,
		AcceptedAt: command.OccurredAt.UTC(),
	}
	record.capacity = RuntimeCapacitySnapshot{
		LogicalRelease: LogicalCapacityReleaseReady,
		NoLease:        NoLeaseDispositionRecorded,
		Physical:       PhysicalCapacityNotApplicable,
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
	return engine.respond(RuntimeDecision{Fact: fact, Snapshot: snapshot(record, SnapshotSchemaCurrent)})
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
		GrantGeneration: command.AdmissionGrant.Generation,
	}
	record.deadline = command.Deadline.UTC()
	record.leaseAcquireBy = earlier(command.Deadline, grant.ExpiresAt)
	record.lease.AcquireStatus = LeaseAcquirePending
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
		record.reconciliation == ReconciliationRequiredStatus {
		return RuntimeSnapshot{}, false
	}
	return buildSnapshot(record, SnapshotSchemaV1), true
}

func buildSnapshot(record *runtimeRecord, version SchemaVersion) RuntimeSnapshot {
	return RuntimeSnapshot{
		SchemaVersion: version, RuntimeRunID: record.fixture.RuntimeRunID,
		RuntimeRevision: record.fixture.RuntimeRevision, State: record.fixture.State, Outcome: record.fixture.Outcome,
		Operation: record.operation, RuntimeFence: record.fixture.RuntimeFence, Lease: record.lease,
		Deadline: record.deadline, LeaseAcquireBy: record.leaseAcquireBy, Cancellation: record.cancellation,
		EvidenceRoot: record.evidenceRoot, Capacity: record.capacity, Reconciliation: record.reconciliation,
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
		!knownReconciliation(record.reconciliation) || !knownEvidenceRoot(record.evidenceRoot) {
		return false
	}
	return true
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
