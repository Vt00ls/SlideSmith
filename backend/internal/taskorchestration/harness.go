package taskorchestration

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"sync"
	"time"
)

type HarnessConfig struct {
	Now      time.Time
	IDs      DeterministicIDConfig
	Tasks    []HarnessTaskFixture
	Recovery HarnessRecoveryFixture
}

type HarnessRecoveryFixture struct {
	Authority   RecoveryAuthority
	Generation  RecoveryGeneration
	Fence       RecoveryFence
	SafetyEpoch SafetyEpoch
	Mode        OperationalMode
}

// HarnessTaskFixture establishes authoritative preconditions for black-box
// coordination tests without adding a business mutation interface.
type HarnessTaskFixture struct {
	TaskID                TaskID
	Owner                 UserAuthority
	Reconciler            WorkerAuthority
	TaskRevision          TaskRevision
	ActivityGeneration    ActivityGeneration
	SafetyEpoch           SafetyEpoch
	PhaseRuns             []HarnessPhaseRunFixture
	RuntimeOperations     []HarnessRuntimeOperationFixture
	ValidationBindings    []HarnessValidationBindingFixture
	LifecycleOperations   []HarnessLifecycleOperationFixture
	PublicationOperations []HarnessPublicationOperationFixture
	SchedulingOperations  []HarnessSchedulingOperationFixture
}

type HarnessPhaseRunFixture struct {
	PhaseRunID PhaseRunID
	Generation PhaseRunGeneration
	Fence      PhaseRunFence
	Active     bool
}

type HarnessRuntimeOperationFixture struct {
	OperationID  OperationID
	PhaseRunID   PhaseRunID
	RuntimeRunID RuntimeRunID
	Authority    RuntimeAuthority
	Generation   RuntimeGeneration
	Fence        RuntimeFence
	SafetyEpoch  SafetyEpoch
}

type HarnessValidationBindingFixture struct {
	PhaseRunID  PhaseRunID
	Authority   ValidatorAuthority
	Generation  ProducerGeneration
	Fence       ValidationFence
	SafetyEpoch SafetyEpoch
}

type HarnessPublicationOperationFixture struct {
	OperationID OperationID
	PhaseRunID  PhaseRunID
	Authority   PublicationAuthority
	Generation  ProducerGeneration
	Fence       PublicationFence
	SafetyEpoch SafetyEpoch
}

type HarnessSchedulingOperationFixture struct {
	OperationID OperationID
	PhaseRunID  PhaseRunID
	Authority   SchedulerAuthority
	Generation  ProducerGeneration
	Fence       SchedulerFence
	SafetyEpoch SafetyEpoch
}

type LifecycleOperationPurpose uint8

const (
	LifecycleOperationCommit LifecycleOperationPurpose = iota + 1
	LifecycleOperationCancellationFence
)

type HarnessLifecycleOperationFixture struct {
	OperationID OperationID
	PhaseRunID  PhaseRunID
	Authority   TaskWorkspaceLifecycleAuthority
	Generation  TaskWorkspaceLifecycleGeneration
	Fence       TaskWorkspaceLifecycleFence
	SafetyEpoch SafetyEpoch
	Purpose     LifecycleOperationPurpose
}

type DeterministicIDConfig struct {
	DecisionStart   uint64
	AuditFactStart  uint64
	PhaseRunStart   uint64
	RuntimeRunStart uint64
	OperationStart  uint64
	CausationStart  uint64
}

type CrashBoundary uint8

const (
	CrashBeforeCommit CrashBoundary = iota + 1
	CrashAfterCommit
)

type FaultPoint uint8

const (
	FaultBeforeValidation FaultPoint = iota + 1
	FaultAfterValidation
	FaultBeforeCommit
	FaultAfterCommit
	FaultBeforeResponse
)

func (point FaultPoint) String() string {
	switch point {
	case FaultBeforeValidation:
		return "before_validation"
	case FaultAfterValidation:
		return "after_validation"
	case FaultBeforeCommit:
		return "before_commit"
	case FaultAfterCommit:
		return "after_commit"
	case FaultBeforeResponse:
		return "before_response"
	default:
		return "unknown"
	}
}

// DeterministicHarness is a test implementation of the external seams. It
// deliberately contains no Pipeline progression policy.
type DeterministicHarness struct {
	Mutations   TaskOrchestration
	Queries     TaskOrchestrationQuery
	persistence *memoryPersistence
	clock       *controlledClock
	controls    *harnessControls
}

func NewDeterministicHarness(config HarnessConfig) (*DeterministicHarness, error) {
	if config.Now.IsZero() {
		return nil, invalidIntentError()
	}
	persistence := &memoryPersistence{
		tasks:            make(map[TaskID]taskRecord),
		decisions:        make(map[decisionRequestScope]committedDecision),
		acceptedEvidence: make(map[evidenceScope]committedEvidence),
		outbox:           make(map[OperationID]authoritativeOutboxRecord),
		deliveries:       make(map[OperationID]memoryDeliveryState),
		ids:              newDeterministicIDAllocator(config.IDs),
	}
	if config.Recovery != (HarnessRecoveryFixture{}) {
		if !config.Recovery.Authority.value.valid() ||
			config.Recovery.Authority.value.kind != AuthorityRecovery ||
			config.Recovery.Generation == 0 || config.Recovery.Fence == 0 ||
			config.Recovery.SafetyEpoch == 0 ||
			operationalModeName(config.Recovery.Mode) == "" {
			return nil, invalidIntentError()
		}
		persistence.recovery = recoveryBinding{
			authority:   config.Recovery.Authority.value,
			generation:  config.Recovery.Generation,
			fence:       config.Recovery.Fence,
			safetyEpoch: config.Recovery.SafetyEpoch,
			mode:        config.Recovery.Mode,
		}
	} else {
		persistence.recovery.mode = OperationalFullReady
	}
	for _, fixture := range config.Tasks {
		record, err := taskRecordFromFixture(fixture)
		if err != nil {
			return nil, err
		}
		if _, exists := persistence.tasks[fixture.TaskID]; exists {
			return nil, invalidIntentError()
		}
		if persistence.recovery.safetyEpoch != 0 &&
			record.safetyEpoch != persistence.recovery.safetyEpoch {
			return nil, invalidIntentError()
		}
		persistence.tasks[fixture.TaskID] = record
	}
	clock := &controlledClock{now: config.Now.UTC()}
	return newHarness(persistence, clock), nil
}

func newHarness(
	persistence *memoryPersistence,
	clock *controlledClock,
) *DeterministicHarness {
	controls := &harnessControls{}
	engine := &decisionEngine{clock: clock, persistence: persistence, controls: controls}
	return &DeterministicHarness{
		Mutations:   engine,
		Queries:     engine,
		persistence: persistence,
		clock:       clock,
		controls:    controls,
	}
}

// LoseNextResponse injects one response loss after a successful commit.
func (harness *DeterministicHarness) LoseNextResponse() {
	harness.controls.mu.Lock()
	defer harness.controls.mu.Unlock()
	harness.controls.loseResponse = true
}

func (harness *DeterministicHarness) CrashNextAt(boundary CrashBoundary) error {
	if boundary != CrashBeforeCommit && boundary != CrashAfterCommit {
		return invalidIntentError()
	}
	harness.controls.mu.Lock()
	defer harness.controls.mu.Unlock()
	harness.controls.nextCrash = boundary
	return nil
}

func (harness *DeterministicHarness) FailNextAt(point FaultPoint) error {
	if point < FaultBeforeValidation || point > FaultBeforeResponse {
		return invalidIntentError()
	}
	harness.controls.mu.Lock()
	defer harness.controls.mu.Unlock()
	harness.controls.nextFault = point
	return nil
}

func (harness *DeterministicHarness) AdvanceClock(elapsed time.Duration) error {
	if elapsed < 0 {
		return invalidIntentError()
	}
	harness.clock.mu.Lock()
	defer harness.clock.mu.Unlock()
	harness.clock.now = harness.clock.now.Add(elapsed)
	return nil
}

// Restart creates new seam adapters around the same in-memory durable state.
func (harness *DeterministicHarness) Restart() *DeterministicHarness {
	harness.controls.mu.Lock()
	harness.controls.crashed = true
	harness.controls.mu.Unlock()
	return newHarness(harness.persistence, harness.clock)
}

type controlledClock struct {
	mu  sync.Mutex
	now time.Time
}

type harnessControls struct {
	mu           sync.Mutex
	loseResponse bool
	nextCrash    CrashBoundary
	nextFault    FaultPoint
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

func (clock *controlledClock) current() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

type memoryPersistence struct {
	mu               sync.Mutex
	tasks            map[TaskID]taskRecord
	decisions        map[decisionRequestScope]committedDecision
	acceptedEvidence map[evidenceScope]committedEvidence
	outbox           map[OperationID]authoritativeOutboxRecord
	deliveries       map[OperationID]memoryDeliveryState
	ids              deterministicIDAllocator
	recovery         recoveryBinding
}

type recoveryBinding struct {
	authority               authorityValue
	generation              RecoveryGeneration
	fence                   RecoveryFence
	safetyEpoch             SafetyEpoch
	activityGenerationFence ActivityGeneration
	mode                    OperationalMode
}

type decisionRequestScope struct {
	taskID            TaskID
	authority         authorityValue
	decisionRequestID DecisionRequestID
}

type committedDecision struct {
	digest   CanonicalRequestDigest
	decision TransitionDecision
}

type evidenceScope struct {
	taskID     TaskID
	evidenceID EvidenceID
}

type committedEvidence struct {
	replayDigest [32]byte
	decision     TransitionDecision
}

type deterministicIDAllocator struct {
	nextDecisionSequence   uint64
	nextAuditFactSequence  uint64
	nextPhaseRunSequence   uint64
	nextRuntimeRunSequence uint64
	nextOperationSequence  uint64
	nextCausationSequence  uint64
}

func newDeterministicIDAllocator(config DeterministicIDConfig) deterministicIDAllocator {
	decisionStart := config.DecisionStart
	if decisionStart == 0 {
		decisionStart = 1
	}
	auditFactStart := config.AuditFactStart
	if auditFactStart == 0 {
		auditFactStart = 1
	}
	operationStart := config.OperationStart
	if operationStart == 0 {
		operationStart = 1
	}
	causationStart := config.CausationStart
	if causationStart == 0 {
		causationStart = 1
	}
	return deterministicIDAllocator{
		nextDecisionSequence:   decisionStart,
		nextAuditFactSequence:  auditFactStart,
		nextPhaseRunSequence:   defaultIdentityStart(config.PhaseRunStart),
		nextRuntimeRunSequence: defaultIdentityStart(config.RuntimeRunStart),
		nextOperationSequence:  operationStart,
		nextCausationSequence:  causationStart,
	}
}

func defaultIdentityStart(value uint64) uint64 {
	if value == 0 {
		return 1
	}
	return value
}

func (allocator *deterministicIDAllocator) nextDecisionID() DecisionID {
	id := nextDecisionID(allocator.nextDecisionSequence)
	allocator.nextDecisionSequence++
	return id
}

func (allocator *deterministicIDAllocator) nextAuditFactID() AuditFactID {
	id := nextAuditFactID(allocator.nextAuditFactSequence)
	allocator.nextAuditFactSequence++
	return id
}

func (allocator *deterministicIDAllocator) nextPhaseRunID() PhaseRunID {
	id := nextPhaseRunID(allocator.nextPhaseRunSequence)
	allocator.nextPhaseRunSequence++
	return id
}

func (allocator *deterministicIDAllocator) nextRuntimeRunID() RuntimeRunID {
	id := nextRuntimeRunID(allocator.nextRuntimeRunSequence)
	allocator.nextRuntimeRunSequence++
	return id
}

func (allocator *deterministicIDAllocator) nextOperationID() OperationID {
	id := nextOperationID(allocator.nextOperationSequence)
	allocator.nextOperationSequence++
	return id
}

func (allocator *deterministicIDAllocator) nextCausationID() CausationID {
	id := nextCausationID(allocator.nextCausationSequence)
	allocator.nextCausationSequence++
	return id
}

type taskRecord struct {
	revision                 TaskRevision
	activityGeneration       ActivityGeneration
	recoveryActivityFence    ActivityGeneration
	owner                    userOwnershipBinding
	latestDecision           TransitionDecision
	decisionCount            uint64
	enactmentCount           uint64
	safetyEpoch              SafetyEpoch
	phaseRuns                map[PhaseRunID]phaseRunBinding
	runtimeOperations        map[OperationID]runtimeOperationBinding
	validationBindings       map[PhaseRunID]validationBinding
	lifecycleOperations      map[OperationID]lifecycleOperationBinding
	publicationOperations    map[OperationID]publicationOperationBinding
	schedulingOperations     map[OperationID]schedulingOperationBinding
	evidenceDiagnosticCount  uint64
	latestEvidenceDiagnostic EvidenceDiagnostic
	latestRevisionID         TaskWorkspaceRevisionID
	latestCheckpointID       CheckpointID
	cancellationState        CancellationState
	enactments               map[OperationID]EnactmentRef
	reconciler               authorityValue
	reconciliationFences     map[OperationID]ReconciliationFence
	aggregate                *taskAggregate
	gateDecisions            map[DecisionRequestID]gateDecisionRecord
}

type gateDecisionRecord struct {
	digest   CanonicalRequestDigest
	decision TransitionDecision
}

type taskAggregate struct {
	status                          TaskStatus
	route                           Route
	activity                        ActivityKind
	pinned                          PinnedTaskStart
	currentPhase                    PhaseKey
	activePhaseRunID                PhaseRunID
	phaseRuns                       []phaseRunRecord
	latestArtifactVersionID         ArtifactVersionID
	activitySourceArtifactVersionID ArtifactVersionID
}

type phaseRunRecord struct {
	id                     PhaseRunID
	phaseKey               PhaseKey
	attempt                uint32
	generation             PhaseRunGeneration
	fence                  PhaseRunFence
	outcome                PhaseRunOutcome
	validationOutcome      PhaseValidationOutcome
	lifecycleOutcome       TaskWorkspaceLifecycleOutcome
	revisionID             TaskWorkspaceRevisionID
	checkpointID           CheckpointID
	lifecycleOperationID   OperationID
	publicationOutcome     PublicationOutcome
	artifactVersionID      ArtifactVersionID
	publicationOperationID OperationID
	runtimeRuns            []runtimeRunRecord
	taskWorkspaceID        TaskWorkspaceID
	inputArtifactVersionID ArtifactVersionID
}

type runtimeRunRecord struct {
	id          RuntimeRunID
	operationID OperationID
	outcome     RuntimeRunOutcome
}

type phaseRunBinding struct {
	generation PhaseRunGeneration
	fence      PhaseRunFence
	active     bool
}

type runtimeOperationBinding struct {
	phaseRunID         PhaseRunID
	runtimeRunID       RuntimeRunID
	authority          authorityValue
	generation         RuntimeGeneration
	fence              RuntimeFence
	safetyEpoch        SafetyEpoch
	activityGeneration ActivityGeneration
	terminal           bool
}

type lifecycleOperationBinding struct {
	phaseRunID         PhaseRunID
	authority          authorityValue
	generation         TaskWorkspaceLifecycleGeneration
	fence              TaskWorkspaceLifecycleFence
	safetyEpoch        SafetyEpoch
	purpose            LifecycleOperationPurpose
	activityGeneration ActivityGeneration
	terminal           bool
}

type validationBinding struct {
	authority          authorityValue
	generation         ProducerGeneration
	fence              ValidationFence
	safetyEpoch        SafetyEpoch
	activityGeneration ActivityGeneration
	terminal           bool
}

type publicationOperationBinding struct {
	phaseRunID         PhaseRunID
	authority          authorityValue
	generation         ProducerGeneration
	fence              PublicationFence
	safetyEpoch        SafetyEpoch
	activityGeneration ActivityGeneration
	terminal           bool
}

type schedulingOperationBinding struct {
	phaseRunID         PhaseRunID
	authority          authorityValue
	generation         ProducerGeneration
	fence              SchedulerFence
	safetyEpoch        SafetyEpoch
	activityGeneration ActivityGeneration
	terminal           bool
}

func taskRecordFromFixture(fixture HarnessTaskFixture) (taskRecord, error) {
	if !validOpaqueID(fixture.TaskID.value) || !fixture.Owner.value.valid() ||
		fixture.Owner.value.kind != AuthorityUser || fixture.ActivityGeneration == 0 ||
		fixture.SafetyEpoch == 0 {
		return taskRecord{}, invalidIntentError()
	}
	record := taskRecord{
		revision:           fixture.TaskRevision,
		activityGeneration: fixture.ActivityGeneration,
		owner: userOwnershipBinding{
			authorityID: fixture.Owner.value.id,
			generation:  fixture.Owner.value.generation,
		},
		safetyEpoch:           fixture.SafetyEpoch,
		phaseRuns:             make(map[PhaseRunID]phaseRunBinding),
		runtimeOperations:     make(map[OperationID]runtimeOperationBinding),
		validationBindings:    make(map[PhaseRunID]validationBinding),
		lifecycleOperations:   make(map[OperationID]lifecycleOperationBinding),
		publicationOperations: make(map[OperationID]publicationOperationBinding),
		schedulingOperations:  make(map[OperationID]schedulingOperationBinding),
		enactments:            make(map[OperationID]EnactmentRef),
		reconciliationFences:  make(map[OperationID]ReconciliationFence),
	}
	if fixture.Reconciler != (WorkerAuthority{}) {
		if !fixture.Reconciler.value.valid() || fixture.Reconciler.value.kind != AuthorityWorker {
			return taskRecord{}, invalidIntentError()
		}
		record.reconciler = fixture.Reconciler.value
	}
	for _, phaseRun := range fixture.PhaseRuns {
		if !validOpaqueID(phaseRun.PhaseRunID.value) || phaseRun.Generation == 0 ||
			phaseRun.Fence == 0 {
			return taskRecord{}, invalidIntentError()
		}
		if _, exists := record.phaseRuns[phaseRun.PhaseRunID]; exists {
			return taskRecord{}, invalidIntentError()
		}
		record.phaseRuns[phaseRun.PhaseRunID] = phaseRunBinding{
			generation: phaseRun.Generation,
			fence:      phaseRun.Fence,
			active:     phaseRun.Active,
		}
	}
	activePhaseRuns := 0
	for _, phaseRun := range record.phaseRuns {
		if phaseRun.active {
			activePhaseRuns++
		}
	}
	if activePhaseRuns > 1 {
		return taskRecord{}, invalidIntentError()
	}
	for _, operation := range fixture.RuntimeOperations {
		if !validOpaqueID(operation.OperationID.value) ||
			!validOpaqueID(operation.RuntimeRunID.value) || !operation.Authority.value.valid() ||
			operation.Authority.value.kind != AuthorityRuntime || operation.Generation == 0 ||
			operation.Fence == 0 || operation.SafetyEpoch == 0 ||
			operation.SafetyEpoch != fixture.SafetyEpoch {
			return taskRecord{}, invalidIntentError()
		}
		if _, exists := record.phaseRuns[operation.PhaseRunID]; !exists {
			return taskRecord{}, invalidIntentError()
		}
		if record.hasOperationID(operation.OperationID) {
			return taskRecord{}, invalidIntentError()
		}
		record.runtimeOperations[operation.OperationID] = runtimeOperationBinding{
			phaseRunID:         operation.PhaseRunID,
			runtimeRunID:       operation.RuntimeRunID,
			authority:          operation.Authority.value,
			generation:         operation.Generation,
			fence:              operation.Fence,
			safetyEpoch:        operation.SafetyEpoch,
			activityGeneration: fixture.ActivityGeneration,
		}
	}
	for _, binding := range fixture.ValidationBindings {
		if !binding.Authority.value.valid() || binding.Authority.value.kind != AuthorityValidator ||
			binding.Generation == 0 || binding.Fence == 0 || binding.SafetyEpoch == 0 ||
			binding.SafetyEpoch != fixture.SafetyEpoch {
			return taskRecord{}, invalidIntentError()
		}
		if _, exists := record.phaseRuns[binding.PhaseRunID]; !exists {
			return taskRecord{}, invalidIntentError()
		}
		if _, exists := record.validationBindings[binding.PhaseRunID]; exists {
			return taskRecord{}, invalidIntentError()
		}
		record.validationBindings[binding.PhaseRunID] = validationBinding{
			authority:          binding.Authority.value,
			generation:         binding.Generation,
			fence:              binding.Fence,
			safetyEpoch:        binding.SafetyEpoch,
			activityGeneration: fixture.ActivityGeneration,
		}
	}
	for _, operation := range fixture.LifecycleOperations {
		if !validOpaqueID(operation.OperationID.value) || !operation.Authority.value.valid() ||
			operation.Authority.value.kind != AuthorityTaskWorkspaceLifecycle ||
			operation.Generation == 0 || operation.Fence == 0 || operation.SafetyEpoch == 0 ||
			operation.SafetyEpoch != fixture.SafetyEpoch ||
			(operation.Purpose != LifecycleOperationCommit &&
				operation.Purpose != LifecycleOperationCancellationFence) {
			return taskRecord{}, invalidIntentError()
		}
		if _, exists := record.phaseRuns[operation.PhaseRunID]; !exists {
			return taskRecord{}, invalidIntentError()
		}
		if record.hasOperationID(operation.OperationID) {
			return taskRecord{}, invalidIntentError()
		}
		record.lifecycleOperations[operation.OperationID] = lifecycleOperationBinding{
			phaseRunID:         operation.PhaseRunID,
			authority:          operation.Authority.value,
			generation:         operation.Generation,
			fence:              operation.Fence,
			safetyEpoch:        operation.SafetyEpoch,
			purpose:            operation.Purpose,
			activityGeneration: fixture.ActivityGeneration,
		}
	}
	for _, operation := range fixture.PublicationOperations {
		if !validOpaqueID(operation.OperationID.value) || !operation.Authority.value.valid() ||
			operation.Authority.value.kind != AuthorityPublication || operation.Generation == 0 ||
			operation.Fence == 0 || operation.SafetyEpoch == 0 ||
			operation.SafetyEpoch != fixture.SafetyEpoch {
			return taskRecord{}, invalidIntentError()
		}
		if _, exists := record.phaseRuns[operation.PhaseRunID]; !exists ||
			record.hasOperationID(operation.OperationID) {
			return taskRecord{}, invalidIntentError()
		}
		record.publicationOperations[operation.OperationID] = publicationOperationBinding{
			phaseRunID:         operation.PhaseRunID,
			authority:          operation.Authority.value,
			generation:         operation.Generation,
			fence:              operation.Fence,
			safetyEpoch:        operation.SafetyEpoch,
			activityGeneration: fixture.ActivityGeneration,
		}
	}
	for _, operation := range fixture.SchedulingOperations {
		if !validOpaqueID(operation.OperationID.value) || !operation.Authority.value.valid() ||
			operation.Authority.value.kind != AuthorityScheduler || operation.Generation == 0 ||
			operation.Fence == 0 || operation.SafetyEpoch == 0 ||
			operation.SafetyEpoch != fixture.SafetyEpoch {
			return taskRecord{}, invalidIntentError()
		}
		if _, exists := record.phaseRuns[operation.PhaseRunID]; !exists ||
			record.hasOperationID(operation.OperationID) {
			return taskRecord{}, invalidIntentError()
		}
		record.schedulingOperations[operation.OperationID] = schedulingOperationBinding{
			phaseRunID:         operation.PhaseRunID,
			authority:          operation.Authority.value,
			generation:         operation.Generation,
			fence:              operation.Fence,
			safetyEpoch:        operation.SafetyEpoch,
			activityGeneration: fixture.ActivityGeneration,
		}
	}
	return record, nil
}

func (record taskRecord) hasOperationID(operationID OperationID) bool {
	if _, exists := record.runtimeOperations[operationID]; exists {
		return true
	}
	if _, exists := record.lifecycleOperations[operationID]; exists {
		return true
	}
	if _, exists := record.publicationOperations[operationID]; exists {
		return true
	}
	if _, exists := record.schedulingOperations[operationID]; exists {
		return true
	}
	if _, exists := record.enactments[operationID]; exists {
		return true
	}
	return false
}

type userOwnershipBinding struct {
	authorityID AuthorityID
	generation  AuthorizationGeneration
}

// decisionEngine owns the closed transition algorithm. Persistence adapters
// provide its transactionally loaded state and commit the resulting snapshot.
type decisionEngine struct {
	clock       *controlledClock
	persistence *memoryPersistence
	controls    *harnessControls
}

func (engine *decisionEngine) Decide(
	ctx context.Context,
	intent TransitionIntent,
) (TransitionDecision, error) {
	if engine.controls.isCrashed() {
		return TransitionDecision{}, newError(ErrorDependencyUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return TransitionDecision{}, newError(ErrorDependencyUnavailable)
	}
	if engine.controls.faultAt(FaultBeforeValidation) {
		return TransitionDecision{}, newError(ErrorDependencyUnavailable)
	}
	digest, err := canonicalizeIntent(intent)
	if err != nil {
		return TransitionDecision{}, err
	}
	evidenceID, evidenceReplayDigest, isEvidenceIntent, err := computeEvidenceReplayDigest(intent)
	if err != nil {
		return TransitionDecision{}, err
	}
	if engine.controls.faultAt(FaultAfterValidation) {
		return TransitionDecision{}, newError(ErrorDependencyUnavailable)
	}
	if engine.controls.crashAt(CrashBeforeCommit) {
		return TransitionDecision{}, newError(ErrorDependencyUnavailable)
	}
	if engine.controls.faultAt(FaultBeforeCommit) {
		return TransitionDecision{}, newError(ErrorDependencyUnavailable)
	}

	engine.persistence.mu.Lock()
	defer engine.persistence.mu.Unlock()
	header := intent.Header()
	requestScope := decisionRequestScope{
		taskID:            header.TaskID,
		authority:         intent.(intentValue).authority,
		decisionRequestID: header.DecisionRequestID,
	}
	if committed, ok := engine.persistence.decisions[requestScope]; ok {
		if committed.digest == digest {
			return cloneTransitionDecision(committed.decision), nil
		}
		return TransitionDecision{}, newError(ErrorIntegrityConflict)
	}
	if isEvidenceIntent {
		scope := evidenceScope{taskID: header.TaskID, evidenceID: evidenceID}
		if committed, ok := engine.persistence.acceptedEvidence[scope]; ok {
			if committed.replayDigest == evidenceReplayDigest {
				engine.persistence.decisions[requestScope] = committedDecision{
					digest: digest, decision: cloneTransitionDecision(committed.decision),
				}
				return cloneTransitionDecision(committed.decision), nil
			}
			record := engine.persistence.tasks[header.TaskID]
			record.retainEvidenceDiagnostic(evidenceID, EvidenceDiagnosticScopeConflict)
			engine.persistence.tasks[header.TaskID] = record
			return TransitionDecision{}, newError(ErrorEvidenceScopeConflict)
		}
	}
	if engine.persistence.recovery.mode == OperationalReadOnly &&
		intent.Kind() != IntentApplyOperationalFence {
		return TransitionDecision{}, newError(ErrorOperationalReadOnly)
	}
	record, exists := engine.persistence.tasks[header.TaskID]
	if err := authorizeUserMutation(intent, record, exists); err != nil {
		return TransitionDecision{}, err
	}
	if !exists && intent.Kind() == IntentStartTask {
		record.initializeNewTask(
			engine.effectiveSafetyEpoch(record),
			engine.persistence.recovery.activityGenerationFence,
		)
	}
	if record.aggregate != nil && record.aggregate.status == TaskCancelling &&
		isAggregateBusinessIntent(intent.Kind()) {
		return TransitionDecision{}, newError(ErrorTerminalConflict)
	}
	current := record.revision
	if current != header.ExpectedTaskRevision {
		if isEvidenceIntent && exists {
			record.retainEvidenceDiagnostic(evidenceID, EvidenceDiagnosticStale)
			engine.persistence.tasks[header.TaskID] = record
		}
		return TransitionDecision{}, newError(ErrorStaleTaskRevision)
	}
	if exists && header.ActivityGeneration != engine.effectiveActivityGeneration(record) &&
		!allowsCommitFirstEvidenceDuringCancellation(intent, record) &&
		!(intent.Kind() == IntentBeginManualEdit &&
			header.ActivityGeneration == engine.effectiveActivityGeneration(record)+1) {
		if isEvidenceIntent {
			record.retainEvidenceDiagnostic(evidenceID, EvidenceDiagnosticStale)
			engine.persistence.tasks[header.TaskID] = record
		}
		return TransitionDecision{}, newError(ErrorStaleAuthority)
	}
	if err := validateCancellationFence(intent, record, exists); err != nil {
		if isEvidenceIntent {
			record.retainEvidenceDiagnostic(evidenceID, evidenceDiagnosticReason(err))
			engine.persistence.tasks[header.TaskID] = record
		}
		return TransitionDecision{}, err
	}
	if err := engine.validateCoordinationBindings(intent, record, exists); err != nil {
		if isEvidenceIntent && exists {
			record.retainEvidenceDiagnostic(evidenceID, evidenceDiagnosticReason(err))
			engine.persistence.tasks[header.TaskID] = record
		}
		return TransitionDecision{}, err
	}
	accepted := current + 1
	acceptedActivityGeneration := header.ActivityGeneration
	if exists {
		acceptedActivityGeneration = engine.effectiveActivityGeneration(record)
	}
	if intent.Kind() == IntentBeginManualEdit {
		acceptedActivityGeneration = header.ActivityGeneration
	}
	if intent.Kind() == IntentCancelTask {
		acceptedActivityGeneration++
	}

	ids := engine.persistence.ids
	decisionID := ids.nextDecisionID()
	auditFactID := ids.nextAuditFactID()
	affectedPhaseRuns := []PhaseRunID{}
	acceptedEvidenceRefs := []EvidenceRef{}
	enactmentRefs := []EnactmentRef{}
	newEnactmentCount := uint64(0)
	cancellationPhaseRunID := PhaseRunID{}
	hadActivePhaseRunAtCancellation := false
	if intent.Kind() == IntentCancelTask {
		cancellationPhaseRunID, hadActivePhaseRunAtCancellation = record.activePhaseRun()
	}
	updatedRecord := cloneTaskRecord(record)
	if updated, affected, enactments, aggregateErr := applyAggregateIntent(
		updatedRecord, intent, &ids, decisionID,
	); aggregateErr != nil {
		return TransitionDecision{}, aggregateErr
	} else {
		updatedRecord = updated
		affectedPhaseRuns = append(affectedPhaseRuns, affected...)
		enactmentRefs = append(enactmentRefs, enactments...)
		newEnactmentCount += uint64(len(enactments))
	}
	if intent.Kind() == IntentMakeWorkAvailable && updatedRecord.aggregate != nil &&
		updatedRecord.reconciler == (authorityValue{}) {
		updatedRecord.reconciler = intent.(intentValue).authority
	}
	engine.syncAggregateCoordination(
		&updatedRecord, enactmentRefs, acceptedActivityGeneration,
		engine.effectiveSafetyEpoch(updatedRecord),
	)
	if facts, isEvidenceIntent, factsErr := evidenceFacts(intent); factsErr == nil && isEvidenceIntent {
		if len(affectedPhaseRuns) == 0 {
			affectedPhaseRuns = append(affectedPhaseRuns, facts.phaseRunID)
		}
		acceptedEvidenceRefs = append(acceptedEvidenceRefs, facts.evidence)
	}
	if intent.Kind() == IntentCancelTask {
		if updatedRecord.cancellationState != CancellationNotRequested {
			return TransitionDecision{}, newError(ErrorTerminalConflict)
		}
		updatedRecord.cancellationState = CancellationCancelling
		activePhaseRunID, hasActivePhaseRun := updatedRecord.activePhaseRun()
		if hadActivePhaseRunAtCancellation {
			activePhaseRunID = cancellationPhaseRunID
			hasActivePhaseRun = true
		}
		if hasActivePhaseRun {
			if !containsPhaseRunID(affectedPhaseRuns, activePhaseRunID) {
				affectedPhaseRuns = append(affectedPhaseRuns, activePhaseRunID)
			}
			cancellationEnactments := engine.buildCancellationEnactments(
				&updatedRecord, activePhaseRunID, acceptedActivityGeneration,
				engine.effectiveSafetyEpoch(updatedRecord), &ids,
			)
			enactmentRefs = append(enactmentRefs, cancellationEnactments...)
			newEnactmentCount += uint64(len(cancellationEnactments))
		}
		if !hasEnactmentKind(enactmentRefs, EnactmentTaskWorkspaceLifecycle) {
			updatedRecord.cancellationState = CancellationCancelled
		}
	}
	if intent.Kind() == IntentReconcileEnactment {
		typed := intent.(intentValue)
		payload := typed.payload.(reconcilePayload)
		enactmentRefs = append(enactmentRefs, updatedRecord.enactments[payload.operationID])
		updatedRecord.reconciliationFences[payload.operationID] = payload.fence
	}
	updatedRecovery := engine.persistence.recovery
	if intent.Kind() == IntentApplyOperationalFence {
		typed := intent.(intentValue)
		binding := typed.payload.(operationalFencePayload).binding
		if isOperationalFenceCatchUp(updatedRecord, updatedRecovery, binding) {
			updatedRecovery = engine.persistence.recovery
		} else {
			updatedRecovery = recoveryBinding{
				authority:               typed.authority,
				generation:              binding.Generation,
				fence:                   binding.Fence,
				safetyEpoch:             binding.SafetyEpoch,
				activityGenerationFence: engine.persistence.recovery.activityGenerationFence + 1,
				mode:                    binding.Mode,
			}
		}
		acceptedActivityGeneration = effectiveActivityGeneration(updatedRecord, updatedRecovery)
		if binding.Mode == OperationalFullReady &&
			updatedRecord.cancellationState == CancellationCancelling {
			activePhaseRunID, hasActivePhaseRun := updatedRecord.activePhaseRun()
			if hasActivePhaseRun {
				if !containsPhaseRunID(affectedPhaseRuns, activePhaseRunID) {
					affectedPhaseRuns = append(affectedPhaseRuns, activePhaseRunID)
				}
				cancellationEnactments := engine.buildCancellationEnactments(
					&updatedRecord, activePhaseRunID, acceptedActivityGeneration,
					effectiveSafetyEpoch(updatedRecord, updatedRecovery), &ids,
				)
				enactmentRefs = append(enactmentRefs, cancellationEnactments...)
				newEnactmentCount += uint64(len(cancellationEnactments))
			}
		}
	}
	applyAcceptedCoordinationOutcome(intent, &updatedRecord)
	effectiveSafetyEpoch := effectiveSafetyEpoch(updatedRecord, updatedRecovery)
	updatedRecord.revision = accepted
	updatedRecord.activityGeneration = acceptedActivityGeneration
	updatedRecord.recoveryActivityFence = updatedRecovery.activityGenerationFence
	projection := taskProjection(
		header.TaskID, accepted, acceptedActivityGeneration, updatedRecord.aggregate,
	)
	projection.LatestRevisionID = updatedRecord.latestRevisionID
	projection.LatestCheckpointID = updatedRecord.latestCheckpointID
	projection.CancellationState = updatedRecord.cancellationState
	projection.SafetyEpoch = effectiveSafetyEpoch
	projection.OperationalMode = updatedRecovery.mode
	decision := TransitionDecision{
		DecisionID:             decisionID,
		DecisionRequestID:      header.DecisionRequestID,
		CanonicalRequestDigest: digest,
		PreviousTaskRevision:   current,
		AcceptedTaskRevision:   accepted,
		TaskProjection:         projection,
		AffectedPhaseRuns:      affectedPhaseRuns,
		AcceptedEvidenceRefs:   acceptedEvidenceRefs,
		CommittedAt:            engine.clock.current(),
		EnactmentRefs:          enactmentRefs,
		MandatoryAuditFactRef:  AuditFactRef{AuditFactID: auditFactID},
	}
	if updatedRecord.owner == (userOwnershipBinding{}) && intent.Kind() == IntentStartTask {
		typed := intent.(intentValue)
		updatedRecord.owner = userOwnershipBinding{
			authorityID: typed.authority.id,
			generation:  typed.authority.generation,
		}
	}
	if isEvidenceIntent {
		markAcceptedEvidenceTerminal(intent, &updatedRecord)
	}
	updatedRecord.latestDecision = cloneTransitionDecision(decision)
	updatedRecord.decisionCount++
	updatedRecord.enactmentCount += newEnactmentCount
	if intent.Kind() == IntentSubmitConfirmationGate && updatedRecord.aggregate != nil {
		if updatedRecord.gateDecisions == nil {
			updatedRecord.gateDecisions = make(map[DecisionRequestID]gateDecisionRecord)
		}
		updatedRecord.gateDecisions[header.DecisionRequestID] = gateDecisionRecord{
			digest: digest, decision: decision,
		}
	}
	engine.persistence.tasks[header.TaskID] = updatedRecord
	for _, enactment := range decision.EnactmentRefs {
		if _, exists := engine.persistence.outbox[enactment.OperationID]; exists {
			continue
		}
		phaseRunID, runtimeRunID := enactmentScope(updatedRecord, enactment.OperationID)
		engine.persistence.outbox[enactment.OperationID] = authoritativeOutboxRecord{
			EnactmentRef: enactment,
			DecisionID:   decision.DecisionID,
			TaskID:       decision.TaskProjection.TaskID,
			PhaseRunID:   phaseRunID,
			RuntimeRunID: runtimeRunID,
			SafetyEpoch:  decision.TaskProjection.SafetyEpoch,
			Prerequisites: DeliveryPrerequisites{
				TaskRevision:        decision.AcceptedTaskRevision,
				AcceptedEvidenceIDs: evidenceIDValues(decision.AcceptedEvidenceRefs),
			},
			CommittedAt: decision.CommittedAt,
		}
	}
	engine.persistence.decisions[requestScope] = committedDecision{
		digest: digest, decision: cloneTransitionDecision(decision),
	}
	if isEvidenceIntent {
		engine.persistence.acceptedEvidence[evidenceScope{
			taskID: header.TaskID, evidenceID: evidenceID,
		}] = committedEvidence{
			replayDigest: evidenceReplayDigest, decision: cloneTransitionDecision(decision),
		}
	}
	engine.persistence.ids = ids
	engine.persistence.recovery = updatedRecovery
	if engine.controls.faultAt(FaultAfterCommit) {
		return TransitionDecision{}, newError(ErrorReconciliationRequired)
	}
	if engine.controls.crashAt(CrashAfterCommit) {
		return TransitionDecision{}, newError(ErrorReconciliationRequired)
	}
	if engine.controls.takeResponseLoss() {
		return TransitionDecision{}, newError(ErrorReconciliationRequired)
	}
	if engine.controls.faultAt(FaultBeforeResponse) {
		return TransitionDecision{}, newError(ErrorReconciliationRequired)
	}
	return decision, nil
}

func cloneTaskRecord(record taskRecord) taskRecord {
	record.latestDecision = cloneTransitionDecision(record.latestDecision)
	if record.gateDecisions != nil {
		record.gateDecisions = make(map[DecisionRequestID]gateDecisionRecord, len(record.gateDecisions))
		for requestID, decision := range record.gateDecisions {
			decision.decision = cloneTransitionDecision(decision.decision)
			record.gateDecisions[requestID] = decision
		}
	}
	if record.phaseRuns != nil {
		record.phaseRuns = cloneMap(record.phaseRuns)
	}
	if record.runtimeOperations != nil {
		record.runtimeOperations = cloneMap(record.runtimeOperations)
	}
	if record.validationBindings != nil {
		record.validationBindings = cloneMap(record.validationBindings)
	}
	if record.lifecycleOperations != nil {
		record.lifecycleOperations = cloneMap(record.lifecycleOperations)
	}
	if record.publicationOperations != nil {
		record.publicationOperations = cloneMap(record.publicationOperations)
	}
	if record.schedulingOperations != nil {
		record.schedulingOperations = cloneMap(record.schedulingOperations)
	}
	if record.enactments != nil {
		record.enactments = cloneMap(record.enactments)
	}
	if record.reconciliationFences != nil {
		record.reconciliationFences = cloneMap(record.reconciliationFences)
	}
	if record.aggregate != nil {
		aggregate := *record.aggregate
		aggregate.pinned = clonePinnedTaskStart(record.aggregate.pinned)
		aggregate.phaseRuns = make([]phaseRunRecord, len(record.aggregate.phaseRuns))
		for index, run := range record.aggregate.phaseRuns {
			aggregate.phaseRuns[index] = run
			aggregate.phaseRuns[index].runtimeRuns = append([]runtimeRunRecord(nil), run.runtimeRuns...)
		}
		record.aggregate = &aggregate
	}
	return record
}

func cloneMap[K comparable, V any](source map[K]V) map[K]V {
	cloned := make(map[K]V, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func containsPhaseRunID(values []PhaseRunID, target PhaseRunID) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func isAggregateBusinessIntent(kind IntentKind) bool {
	switch kind {
	case IntentStartTask, IntentMakeWorkAvailable, IntentSubmitConfirmationGate,
		IntentRetryPhase, IntentCancelTask, IntentBeginManualEdit:
		return true
	default:
		return false
	}
}

func (engine *decisionEngine) syncAggregateCoordination(
	record *taskRecord,
	enactments []EnactmentRef,
	activityGeneration ActivityGeneration,
	safetyEpoch SafetyEpoch,
) {
	if record.aggregate == nil {
		return
	}
	for _, ref := range enactments {
		record.enactments[ref.OperationID] = ref
	}
	for index := range record.aggregate.phaseRuns {
		run := &record.aggregate.phaseRuns[index]
		record.phaseRuns[run.id] = phaseRunBinding{
			generation: run.generation,
			fence:      run.fence,
			active:     record.aggregate.activePhaseRunID == run.id,
		}
		if _, exists := record.validationBindings[run.id]; !exists {
			record.validationBindings[run.id] = validationBinding{
				generation:         ProducerGeneration(run.generation),
				fence:              ValidationFence(run.fence),
				safetyEpoch:        safetyEpoch,
				activityGeneration: activityGeneration,
			}
		}
		for _, runtimeRun := range run.runtimeRuns {
			if _, exists := record.runtimeOperations[runtimeRun.operationID]; exists {
				continue
			}
			record.runtimeOperations[runtimeRun.operationID] = runtimeOperationBinding{
				phaseRunID:         run.id,
				runtimeRunID:       runtimeRun.id,
				generation:         RuntimeGeneration(run.generation),
				fence:              RuntimeFence(run.fence),
				safetyEpoch:        safetyEpoch,
				activityGeneration: activityGeneration,
			}
		}
		if run.lifecycleOperationID != (OperationID{}) {
			if _, exists := record.lifecycleOperations[run.lifecycleOperationID]; !exists {
				record.lifecycleOperations[run.lifecycleOperationID] = lifecycleOperationBinding{
					phaseRunID:         run.id,
					generation:         TaskWorkspaceLifecycleGeneration(run.generation),
					fence:              TaskWorkspaceLifecycleFence(run.fence),
					safetyEpoch:        safetyEpoch,
					purpose:            LifecycleOperationCommit,
					activityGeneration: activityGeneration,
				}
			}
		}
		if run.publicationOperationID != (OperationID{}) {
			if _, exists := record.publicationOperations[run.publicationOperationID]; !exists {
				record.publicationOperations[run.publicationOperationID] = publicationOperationBinding{
					phaseRunID:         run.id,
					generation:         ProducerGeneration(run.generation),
					fence:              PublicationFence(run.fence),
					safetyEpoch:        safetyEpoch,
					activityGeneration: activityGeneration,
				}
			}
		}
	}
}

func applyAggregateIntent(
	record taskRecord,
	intent TransitionIntent,
	ids *deterministicIDAllocator,
	decisionID DecisionID,
) (taskRecord, []PhaseRunID, []EnactmentRef, error) {
	typed := intent.(intentValue)
	if typed.kind == IntentStartTask {
		pinnedPayload, pinned := typed.payload.(pinnedTaskStartPayload)
		if !pinned {
			return record, nil, nil, nil
		}
		if record.aggregate != nil {
			return record, nil, nil, newError(ErrorTerminalConflict)
		}
		route, ok := pinnedPayload.pinned.ExecutionLock.PipelineContract.route(pinnedPayload.pinned.Route)
		if !ok {
			return record, nil, nil, newError(ErrorUnsupportedPipelineContract)
		}
		record.aggregate = &taskAggregate{
			status:       TaskReady,
			route:        pinnedPayload.pinned.Route,
			activity:     ActivityGenerationPipeline,
			pinned:       clonePinnedTaskStart(pinnedPayload.pinned),
			currentPhase: route.EntryPhase,
			phaseRuns:    []phaseRunRecord{},
		}
		return record, []PhaseRunID{}, []EnactmentRef{}, nil
	}
	if record.aggregate == nil {
		return record, nil, nil, nil
	}
	if record.aggregate.status == TaskCancelling {
		switch typed.kind {
		case IntentAcceptTaskWorkspaceLifecycleEvidence:
			return acceptAggregateTaskWorkspaceLifecycleEvidence(record, typed)
		case IntentAcceptRuntimeEvidence:
			return acceptAggregateRuntimeOrCoordinationEvidence(record, typed)
		case IntentReconcileEnactment, IntentApplyOperationalFence:
			return record, nil, nil, nil
		default:
			return record, nil, nil, newError(ErrorTerminalConflict)
		}
	}
	switch typed.kind {
	case IntentMakeWorkAvailable:
		return beginAggregatePhase(record, intent, ids, decisionID)
	case IntentAcceptRuntimeEvidence:
		return acceptAggregateRuntimeOrCoordinationEvidence(record, typed)
	case IntentAcceptPhaseValidationEvidence:
		return acceptAggregateValidationEvidence(record, typed, ids, decisionID)
	case IntentAcceptTaskWorkspaceLifecycleEvidence:
		return acceptAggregateTaskWorkspaceLifecycleEvidence(record, typed)
	case IntentAcceptPublicationEvidence:
		return acceptAggregatePublicationEvidence(record, typed)
	case IntentSubmitConfirmationGate:
		return acceptAggregateConfirmation(record, typed)
	case IntentRetryPhase:
		return retryAggregatePhase(record, typed, ids, decisionID)
	case IntentBeginManualEdit:
		return beginAggregateManualEdit(record, typed)
	case IntentCancelTask:
		return cancelAggregateTask(record)
	case IntentAcceptSchedulingEvidence, IntentReconcileEnactment, IntentApplyOperationalFence:
		return record, nil, nil, nil
	default:
		return record, nil, nil, invalidIntentError()
	}
}

func acceptAggregateRuntimeOrCoordinationEvidence(
	record taskRecord,
	intent intentValue,
) (taskRecord, []PhaseRunID, []EnactmentRef, error) {
	payload := intent.payload.(runtimeEvidencePayload)
	if !aggregateHasRuntimeOperation(record.aggregate, payload.binding.OperationID) {
		return record, nil, nil, nil
	}
	return acceptAggregateRuntimeEvidence(record, intent)
}

func aggregateHasRuntimeOperation(aggregate *taskAggregate, operationID OperationID) bool {
	for _, phaseRun := range aggregate.phaseRuns {
		for _, runtimeRun := range phaseRun.runtimeRuns {
			if runtimeRun.operationID == operationID {
				return true
			}
		}
	}
	return false
}

func cancelAggregateTask(record taskRecord) (taskRecord, []PhaseRunID, []EnactmentRef, error) {
	aggregate := record.aggregate
	if aggregate.status == TaskCompleted || aggregate.status == TaskCancelled ||
		aggregate.status == TaskCancelling {
		return record, nil, nil, newError(ErrorTerminalConflict)
	}
	if aggregate.activePhaseRunID == (PhaseRunID{}) {
		aggregate.status = TaskCancelled
		aggregate.activity = 0
		return record, nil, nil, nil
	}
	run, ok := activePhaseRun(aggregate, aggregate.activePhaseRunID)
	phase, phaseOK := currentPhaseDefinition(aggregate)
	if !ok || !phaseOK {
		return record, nil, nil, newError(ErrorUnsupportedPipelineContract)
	}
	if phase.Kind == PhaseMutating {
		aggregate.status = TaskCancelling
		return record, []PhaseRunID{run.id}, nil, nil
	}
	run.outcome = PhaseRunCancelled
	aggregate.activePhaseRunID = PhaseRunID{}
	aggregate.status = TaskCancelled
	aggregate.activity = 0
	return record, []PhaseRunID{run.id}, nil, nil
}

func beginAggregateManualEdit(
	record taskRecord,
	intent intentValue,
) (taskRecord, []PhaseRunID, []EnactmentRef, error) {
	payload := intent.payload.(manualEditPayload)
	aggregate := record.aggregate
	if aggregate.status != TaskCompleted || aggregate.activity != 0 ||
		aggregate.latestArtifactVersionID != payload.artifactVersionID ||
		intent.header.ActivityGeneration != record.activityGeneration+1 {
		return record, nil, nil, newError(ErrorTerminalConflict)
	}
	entry, _, ok := aggregate.pinned.ExecutionLock.PipelineContract.manualEdit()
	if !ok {
		return record, nil, nil, newError(ErrorUnsupportedPipelineContract)
	}
	aggregate.status = TaskRunning
	aggregate.activity = ActivityManualEdit
	aggregate.currentPhase = entry
	aggregate.activePhaseRunID = PhaseRunID{}
	aggregate.activitySourceArtifactVersionID = payload.artifactVersionID
	return record, nil, nil, nil
}

func beginAggregatePhase(
	record taskRecord,
	intent TransitionIntent,
	ids *deterministicIDAllocator,
	decisionID DecisionID,
) (taskRecord, []PhaseRunID, []EnactmentRef, error) {
	aggregate := record.aggregate
	if aggregate.status != TaskReady && aggregate.status != TaskRunning ||
		aggregate.activePhaseRunID != (PhaseRunID{}) {
		return record, nil, nil, newError(ErrorTerminalConflict)
	}
	phase, ok := currentPhaseDefinition(aggregate)
	if !ok {
		return record, nil, nil, newError(ErrorUnsupportedPipelineContract)
	}
	run := phaseRunRecord{
		id:                     ids.nextPhaseRunID(),
		phaseKey:               phase.Key,
		attempt:                1,
		outcome:                PhaseRunRunning,
		taskWorkspaceID:        aggregate.pinned.TaskWorkspaceID,
		inputArtifactVersionID: aggregate.activitySourceArtifactVersionID,
	}
	for _, prior := range aggregate.phaseRuns {
		if prior.phaseKey == phase.Key && prior.attempt >= run.attempt {
			run.attempt = prior.attempt + 1
		}
	}
	run.generation = PhaseRunGeneration(run.attempt)
	run.fence = PhaseRunFence(run.attempt)
	aggregate.status = TaskRunning
	enactments := make([]EnactmentRef, 0, phase.RequiredRuntimeRuns)
	for runtimeIndex := uint32(0); runtimeIndex < phase.RequiredRuntimeRuns; runtimeIndex++ {
		runtimeRun := runtimeRunRecord{id: ids.nextRuntimeRunID(), outcome: RuntimeRunPending}
		operationID := ids.nextOperationID()
		runtimeRun.operationID = operationID
		run.runtimeRuns = append(run.runtimeRuns, runtimeRun)
		enactments = append(enactments, harnessEnactment(
			operationID, EnactmentRuntimeExecution, RuntimeFence(run.fence),
			intent.Header().ActivityGeneration, decisionID, run.id, runtimeRun.id,
			run.taskWorkspaceID, run.inputArtifactVersionID,
		))
	}
	if phase.Kind == PhaseConfirmationGate {
		operationID := ids.nextOperationID()
		enactments = append(enactments, harnessEnactment(
			operationID, EnactmentPresentConfirmationGate, ConfirmationFence(run.fence),
			intent.Header().ActivityGeneration, decisionID, run.id, RuntimeRunID{},
			run.taskWorkspaceID, run.inputArtifactVersionID,
		))
		aggregate.status = TaskAwaitingConfirmation
	}
	if phase.Kind == PhasePublication {
		operationID := ids.nextOperationID()
		run.publicationOperationID = operationID
		enactments = append(enactments, harnessEnactment(
			operationID, EnactmentArtifactPublication, PublicationFence(run.fence),
			intent.Header().ActivityGeneration, decisionID, run.id, RuntimeRunID{},
			run.taskWorkspaceID, run.inputArtifactVersionID,
		))
	}
	aggregate.phaseRuns = append(aggregate.phaseRuns, run)
	aggregate.activePhaseRunID = run.id
	return record, []PhaseRunID{run.id}, enactments, nil
}

func retryAggregatePhase(
	record taskRecord,
	intent intentValue,
	ids *deterministicIDAllocator,
	decisionID DecisionID,
) (taskRecord, []PhaseRunID, []EnactmentRef, error) {
	payload := intent.payload.(phaseRunPayload)
	aggregate := record.aggregate
	if aggregate.status != TaskFailed || aggregate.activePhaseRunID != (PhaseRunID{}) {
		return record, nil, nil, newError(ErrorTerminalConflict)
	}
	phase, ok := currentPhaseDefinition(aggregate)
	if !ok || !phase.RetryEligible {
		return record, nil, nil, newError(ErrorTerminalConflict)
	}
	var failed *phaseRunRecord
	for index := range aggregate.phaseRuns {
		if aggregate.phaseRuns[index].id == payload.phaseRunID {
			failed = &aggregate.phaseRuns[index]
		}
	}
	if failed == nil || failed.phaseKey != phase.Key || failed.outcome != PhaseRunFailed {
		return record, nil, nil, newError(ErrorTerminalConflict)
	}
	aggregate.status = TaskRunning
	return beginAggregatePhase(record, intent, ids, decisionID)
}

func acceptAggregateConfirmation(
	record taskRecord,
	intent intentValue,
) (taskRecord, []PhaseRunID, []EnactmentRef, error) {
	payload := intent.payload.(confirmationPayload)
	if record.aggregate.status != TaskAwaitingConfirmation {
		return record, nil, nil, newError(ErrorTerminalConflict)
	}
	run, ok := activePhaseRun(record.aggregate, record.aggregate.activePhaseRunID)
	if !ok {
		return record, nil, nil, newError(ErrorTerminalConflict)
	}
	phase, ok := currentPhaseDefinition(record.aggregate)
	if !ok || phase.Kind != PhaseConfirmationGate || phase.GateID != payload.gateID {
		return record, nil, nil, newError(ErrorEvidenceScopeConflict)
	}
	finishAggregatePhase(record.aggregate, run, phase)
	return record, []PhaseRunID{run.id}, nil, nil
}

func acceptAggregateRuntimeEvidence(
	record taskRecord,
	intent intentValue,
) (taskRecord, []PhaseRunID, []EnactmentRef, error) {
	payload := intent.payload.(runtimeEvidencePayload)
	if runtimeRunOutcomeName(payload.binding.Outcome) == "" {
		return record, nil, nil, newError(ErrorEvidenceInvalid)
	}
	run, ok := activePhaseRun(record.aggregate, payload.binding.PhaseRunID)
	if !ok {
		return record, nil, nil, newError(ErrorEvidenceScopeConflict)
	}
	for index := range run.runtimeRuns {
		if run.runtimeRuns[index].id != payload.binding.RuntimeRunID {
			continue
		}
		if run.runtimeRuns[index].operationID != payload.binding.OperationID {
			return record, nil, nil, newError(ErrorEvidenceScopeConflict)
		}
		run.runtimeRuns[index].outcome = payload.binding.Outcome
		return record, []PhaseRunID{run.id}, nil, nil
	}
	return record, nil, nil, newError(ErrorEvidenceScopeConflict)
}

func acceptAggregateValidationEvidence(
	record taskRecord,
	intent intentValue,
	ids *deterministicIDAllocator,
	decisionID DecisionID,
) (taskRecord, []PhaseRunID, []EnactmentRef, error) {
	payload := intent.payload.(validationEvidencePayload)
	if phaseValidationOutcomeName(payload.binding.Outcome) == "" {
		return record, nil, nil, newError(ErrorEvidenceInvalid)
	}
	run, ok := activePhaseRun(record.aggregate, payload.binding.PhaseRunID)
	if !ok {
		return record, nil, nil, newError(ErrorEvidenceScopeConflict)
	}
	phase, ok := currentPhaseDefinition(record.aggregate)
	if !ok || phase.ValidationContract == PhaseValidationNone {
		return record, nil, nil, newError(ErrorEvidenceScopeConflict)
	}
	if payload.binding.Outcome == PhaseValidationRejected {
		run.validationOutcome = payload.binding.Outcome
		failAggregatePhase(record.aggregate, run)
		return record, []PhaseRunID{run.id}, nil, nil
	}
	switch phase.ValidationContract {
	case PhaseValidationAllRuntimeRunsSucceeded:
		for _, runtimeRun := range run.runtimeRuns {
			if runtimeRun.outcome != RuntimeRunSucceeded {
				return record, nil, nil, newError(ErrorEvidenceInvalid)
			}
		}
	default:
		return record, nil, nil, newError(ErrorUnsupportedPipelineContract)
	}
	run.validationOutcome = payload.binding.Outcome
	if phase.Kind == PhaseNonMutating {
		finishAggregatePhase(record.aggregate, run, phase)
		return record, []PhaseRunID{run.id}, nil, nil
	}
	operationID := ids.nextOperationID()
	run.lifecycleOperationID = operationID
	enactment := harnessEnactment(
		operationID, EnactmentTaskWorkspaceLifecycle, TaskWorkspaceLifecycleFence(run.fence),
		intent.header.ActivityGeneration, decisionID, run.id, RuntimeRunID{},
		run.taskWorkspaceID, run.inputArtifactVersionID,
	)
	return record, []PhaseRunID{run.id}, []EnactmentRef{enactment}, nil
}

func acceptAggregateTaskWorkspaceLifecycleEvidence(
	record taskRecord,
	intent intentValue,
) (taskRecord, []PhaseRunID, []EnactmentRef, error) {
	payload := intent.payload.(taskWorkspaceLifecycleEvidencePayload)
	if taskWorkspaceLifecycleOutcomeName(payload.binding.Outcome) == "" {
		return record, nil, nil, newError(ErrorEvidenceInvalid)
	}
	run, ok := activePhaseRun(record.aggregate, payload.binding.PhaseRunID)
	if !ok {
		return record, nil, nil, newError(ErrorEvidenceScopeConflict)
	}
	phase, ok := currentPhaseDefinition(record.aggregate)
	if !ok || phase.Kind != PhaseMutating {
		return record, nil, nil, newError(ErrorEvidenceScopeConflict)
	}
	if record.aggregate.status == TaskCancelling {
		operation, operationExists := record.lifecycleOperations[payload.binding.OperationID]
		if !operationExists || operation.phaseRunID != run.id {
			return record, nil, nil, newError(ErrorEvidenceScopeConflict)
		}
		switch payload.binding.Outcome {
		case LifecycleEvidenceCommitted:
			if operation.purpose != LifecycleOperationCommit ||
				run.lifecycleOperationID != payload.binding.OperationID ||
				run.validationOutcome != PhaseValidationAccepted {
				return record, nil, nil, newError(ErrorEvidenceScopeConflict)
			}
			run.lifecycleOutcome = payload.binding.Outcome
			run.revisionID = payload.binding.RevisionID
			run.checkpointID = payload.binding.CheckpointID
			run.outcome = PhaseRunSucceeded
			return record, []PhaseRunID{run.id}, nil, nil
		case LifecycleEvidenceFenced:
			if operation.purpose != LifecycleOperationCancellationFence {
				return record, nil, nil, newError(ErrorEvidenceScopeConflict)
			}
			run.lifecycleOutcome = payload.binding.Outcome
			run.outcome = PhaseRunCancelled
			record.aggregate.activePhaseRunID = PhaseRunID{}
			record.aggregate.status = TaskCancelled
			record.aggregate.activity = 0
			return record, []PhaseRunID{run.id}, nil, nil
		default:
			return record, nil, nil, newError(ErrorEvidenceScopeConflict)
		}
	}
	if run.lifecycleOperationID != payload.binding.OperationID ||
		run.validationOutcome != PhaseValidationAccepted ||
		payload.binding.Outcome == LifecycleEvidenceFenced {
		return record, nil, nil, newError(ErrorEvidenceScopeConflict)
	}
	run.lifecycleOutcome = payload.binding.Outcome
	if payload.binding.Outcome == TaskWorkspaceLifecycleRejected {
		failAggregatePhase(record.aggregate, run)
		return record, []PhaseRunID{run.id}, nil, nil
	}
	run.revisionID = payload.binding.RevisionID
	run.checkpointID = payload.binding.CheckpointID
	finishAggregatePhase(record.aggregate, run, phase)
	return record, []PhaseRunID{run.id}, nil, nil
}

func acceptAggregatePublicationEvidence(
	record taskRecord,
	intent intentValue,
) (taskRecord, []PhaseRunID, []EnactmentRef, error) {
	payload := intent.payload.(publicationEvidencePayload)
	if publicationOutcomeName(payload.binding.Outcome) == "" {
		return record, nil, nil, newError(ErrorEvidenceInvalid)
	}
	run, ok := activePhaseRun(record.aggregate, payload.binding.PhaseRunID)
	if !ok || run.publicationOperationID != payload.binding.OperationID {
		return record, nil, nil, newError(ErrorEvidenceScopeConflict)
	}
	phase, ok := currentPhaseDefinition(record.aggregate)
	if !ok || phase.Kind != PhasePublication {
		return record, nil, nil, newError(ErrorEvidenceScopeConflict)
	}
	run.publicationOutcome = payload.binding.Outcome
	if payload.binding.Outcome == PublicationRejected {
		failAggregatePhase(record.aggregate, run)
		return record, []PhaseRunID{run.id}, nil, nil
	}
	run.artifactVersionID = payload.binding.ArtifactVersionID
	record.aggregate.latestArtifactVersionID = payload.binding.ArtifactVersionID
	finishAggregatePhase(record.aggregate, run, phase)
	return record, []PhaseRunID{run.id}, nil, nil
}

func activePhaseRun(aggregate *taskAggregate, phaseRunID PhaseRunID) (*phaseRunRecord, bool) {
	if aggregate.activePhaseRunID != phaseRunID {
		return nil, false
	}
	for index := range aggregate.phaseRuns {
		if aggregate.phaseRuns[index].id == phaseRunID {
			return &aggregate.phaseRuns[index], true
		}
	}
	return nil, false
}

func currentPhaseDefinition(aggregate *taskAggregate) (PhaseDefinition, bool) {
	switch aggregate.activity {
	case ActivityGenerationPipeline:
		route, ok := aggregate.pinned.ExecutionLock.PipelineContract.route(aggregate.route)
		if !ok {
			return PhaseDefinition{}, false
		}
		return phaseDefinitionByKey(route.Phases, aggregate.currentPhase)
	case ActivityManualEdit:
		_, phases, ok := aggregate.pinned.ExecutionLock.PipelineContract.manualEdit()
		if !ok {
			return PhaseDefinition{}, false
		}
		return phaseDefinitionByKey(phases, aggregate.currentPhase)
	default:
		return PhaseDefinition{}, false
	}
}

func finishAggregatePhase(aggregate *taskAggregate, run *phaseRunRecord, phase PhaseDefinition) {
	run.outcome = PhaseRunSucceeded
	aggregate.activePhaseRunID = PhaseRunID{}
	aggregate.currentPhase = phase.NextPhase
	if phase.NextPhase == (PhaseKey{}) {
		aggregate.status = TaskCompleted
		aggregate.activity = 0
		return
	}
	aggregate.status = TaskRunning
}

func failAggregatePhase(aggregate *taskAggregate, run *phaseRunRecord) {
	run.outcome = PhaseRunFailed
	aggregate.activePhaseRunID = PhaseRunID{}
	aggregate.status = TaskFailed
}

func harnessEnactment(
	operationID OperationID,
	kind EnactmentKind,
	fence EnactmentFenceRef,
	activityGeneration ActivityGeneration,
	decisionID DecisionID,
	phaseRunID PhaseRunID,
	runtimeRunID RuntimeRunID,
	taskWorkspaceID TaskWorkspaceID,
	inputArtifactVersionID ArtifactVersionID,
) EnactmentRef {
	payload := fmt.Sprintf(
		"%d|%s|%s|%s|%s|%s",
		kind, operationID.value, phaseRunID.value, runtimeRunID.value,
		taskWorkspaceID.value, inputArtifactVersionID.value,
	)
	digest := sha256.Sum256([]byte(payload))
	return EnactmentRef{
		OperationID:        operationID,
		Kind:               kind,
		PayloadDigest:      EnactmentPayloadDigest(digest),
		ActivityGeneration: activityGeneration,
		Fence:              fence,
		CausationID:        CausationID{value: "causation-" + decisionID.value},
	}
}

func taskProjection(
	taskID TaskID,
	revision TaskRevision,
	activityGeneration ActivityGeneration,
	aggregate *taskAggregate,
) TaskProjection {
	projection := TaskProjection{
		TaskID: taskID, TaskRevision: revision, ActivityGeneration: activityGeneration,
	}
	if aggregate != nil {
		projection.Status = aggregate.status
		projection.Route = aggregate.route
		projection.Activity = aggregate.activity
		projection.CurrentPhase = aggregate.currentPhase
		projection.ActivePhaseRunID = aggregate.activePhaseRunID
		projection.LatestArtifactVersionID = aggregate.latestArtifactVersionID
		projection.TaskWorkspaceID = aggregate.pinned.TaskWorkspaceID
	}
	return projection
}

func (record *taskRecord) initializeNewTask(
	safetyEpoch SafetyEpoch,
	recoveryActivityFence ActivityGeneration,
) {
	record.safetyEpoch = safetyEpoch
	record.recoveryActivityFence = recoveryActivityFence
	record.phaseRuns = make(map[PhaseRunID]phaseRunBinding)
	record.runtimeOperations = make(map[OperationID]runtimeOperationBinding)
	record.validationBindings = make(map[PhaseRunID]validationBinding)
	record.lifecycleOperations = make(map[OperationID]lifecycleOperationBinding)
	record.publicationOperations = make(map[OperationID]publicationOperationBinding)
	record.schedulingOperations = make(map[OperationID]schedulingOperationBinding)
	record.enactments = make(map[OperationID]EnactmentRef)
	record.reconciliationFences = make(map[OperationID]ReconciliationFence)
}

func cloneTransitionDecision(decision TransitionDecision) TransitionDecision {
	decision.AffectedPhaseRuns = cloneValues(decision.AffectedPhaseRuns)
	decision.AcceptedEvidenceRefs = cloneValues(decision.AcceptedEvidenceRefs)
	decision.EnactmentRefs = cloneValues(decision.EnactmentRefs)
	return decision
}

func cloneValues[T any](values []T) []T {
	if values == nil {
		return nil
	}
	cloned := make([]T, len(values))
	copy(cloned, values)
	return cloned
}

func allowsCommitFirstEvidenceDuringCancellation(
	intent TransitionIntent,
	record taskRecord,
) bool {
	if record.cancellationState != CancellationCancelling || record.activityGeneration == 0 {
		return false
	}
	typed, ok := intent.(intentValue)
	if !ok || typed.kind != IntentAcceptTaskWorkspaceLifecycleEvidence {
		return false
	}
	payload, ok := typed.payload.(taskWorkspaceLifecycleEvidencePayload)
	if !ok || payload.binding.Outcome != LifecycleEvidenceCommitted {
		return false
	}
	operation, exists := record.lifecycleOperations[payload.binding.OperationID]
	return exists && operation.purpose == LifecycleOperationCommit &&
		operation.activityGeneration == intent.Header().ActivityGeneration &&
		intent.Header().ActivityGeneration+1 == record.activityGeneration
}

func validateCancellationFence(
	intent TransitionIntent,
	record taskRecord,
	exists bool,
) error {
	if !exists || record.cancellationState == CancellationNotRequested {
		return nil
	}
	switch intent.Kind() {
	case IntentAcceptRuntimeEvidence,
		IntentAcceptTaskWorkspaceLifecycleEvidence,
		IntentReconcileEnactment,
		IntentApplyOperationalFence:
		return nil
	case IntentAcceptPhaseValidationEvidence,
		IntentAcceptPublicationEvidence,
		IntentAcceptSchedulingEvidence:
		return newError(ErrorStaleAuthority)
	default:
		return newError(ErrorTerminalConflict)
	}
}

func hasEnactmentKind(refs []EnactmentRef, kind EnactmentKind) bool {
	for _, ref := range refs {
		if ref.Kind == kind {
			return true
		}
	}
	return false
}

func (record taskRecord) activePhaseRun() (PhaseRunID, bool) {
	for phaseRunID, phaseRun := range record.phaseRuns {
		if phaseRun.active {
			return phaseRunID, true
		}
	}
	return PhaseRunID{}, false
}

func (engine *decisionEngine) buildCancellationEnactments(
	record *taskRecord,
	phaseRunID PhaseRunID,
	activityGeneration ActivityGeneration,
	safetyEpoch SafetyEpoch,
	ids *deterministicIDAllocator,
) []EnactmentRef {
	refs := make([]EnactmentRef, 0, 2)
	runtimeOperationByRun := make(map[RuntimeRunID]runtimeOperationBinding)
	for _, operation := range record.runtimeOperations {
		if operation.phaseRunID != phaseRunID {
			continue
		}
		selected, exists := runtimeOperationByRun[operation.runtimeRunID]
		if !exists || operation.fence > selected.fence ||
			operation.fence == selected.fence && operation.generation > selected.generation {
			runtimeOperationByRun[operation.runtimeRunID] = operation
		}
	}
	runtimeOperations := make([]runtimeOperationBinding, 0, len(runtimeOperationByRun))
	for _, operation := range runtimeOperationByRun {
		runtimeOperations = append(runtimeOperations, operation)
	}
	sort.Slice(runtimeOperations, func(left, right int) bool {
		return runtimeOperations[left].runtimeRunID.value < runtimeOperations[right].runtimeRunID.value
	})
	for _, operation := range runtimeOperations {
		operationID := nextUniqueOperationID(record, ids)
		fence := operation.fence + 1
		ref := EnactmentRef{
			OperationID: operationID,
			Kind:        EnactmentRuntimeExecution,
			PayloadDigest: computeEnactmentPayloadDigest(map[string]any{
				"activity_generation": uint64(activityGeneration),
				"phase_run_id":        phaseRunID.value,
				"purpose":             "cancel",
				"runtime_run_id":      operation.runtimeRunID.value,
			}),
			ActivityGeneration: activityGeneration,
			Fence:              fence,
			CausationID:        ids.nextCausationID(),
		}
		record.runtimeOperations[operationID] = runtimeOperationBinding{
			phaseRunID:         phaseRunID,
			runtimeRunID:       operation.runtimeRunID,
			authority:          operation.authority,
			generation:         operation.generation,
			fence:              fence,
			safetyEpoch:        safetyEpoch,
			activityGeneration: activityGeneration,
		}
		record.enactments[operationID] = ref
		refs = append(refs, ref)
	}

	var lifecycleOperation lifecycleOperationBinding
	hasLifecycleOperation := false
	for _, operation := range record.lifecycleOperations {
		if operation.phaseRunID != phaseRunID ||
			(hasLifecycleOperation && operation.fence <= lifecycleOperation.fence) {
			continue
		}
		lifecycleOperation = operation
		hasLifecycleOperation = true
	}
	if !hasLifecycleOperation && record.aggregate != nil {
		run, runExists := activePhaseRun(record.aggregate, phaseRunID)
		phase, phaseExists := currentPhaseDefinition(record.aggregate)
		if runExists && phaseExists && phase.Key == run.phaseKey && phase.Kind == PhaseMutating {
			lifecycleOperation = lifecycleOperationBinding{
				phaseRunID:  phaseRunID,
				generation:  TaskWorkspaceLifecycleGeneration(run.generation),
				fence:       TaskWorkspaceLifecycleFence(run.fence),
				safetyEpoch: safetyEpoch,
			}
			hasLifecycleOperation = true
		}
	}
	if hasLifecycleOperation {
		operationID := nextUniqueOperationID(record, ids)
		fence := lifecycleOperation.fence + 1
		ref := EnactmentRef{
			OperationID: operationID,
			Kind:        EnactmentTaskWorkspaceLifecycle,
			PayloadDigest: computeEnactmentPayloadDigest(map[string]any{
				"activity_generation": uint64(activityGeneration),
				"phase_run_id":        phaseRunID.value,
				"purpose":             "fence_discard",
			}),
			ActivityGeneration: activityGeneration,
			Fence:              fence,
			CausationID:        ids.nextCausationID(),
		}
		record.lifecycleOperations[operationID] = lifecycleOperationBinding{
			phaseRunID:         phaseRunID,
			authority:          lifecycleOperation.authority,
			generation:         lifecycleOperation.generation,
			fence:              fence,
			safetyEpoch:        safetyEpoch,
			purpose:            LifecycleOperationCancellationFence,
			activityGeneration: activityGeneration,
		}
		record.enactments[operationID] = ref
		refs = append(refs, ref)
	}
	return refs
}

func nextUniqueOperationID(
	record *taskRecord,
	ids *deterministicIDAllocator,
) OperationID {
	for {
		operationID := ids.nextOperationID()
		if record.hasOperationID(operationID) {
			continue
		}
		return operationID
	}
}

func (record *taskRecord) retainEvidenceDiagnostic(
	evidenceID EvidenceID,
	reason EvidenceDiagnosticReason,
) {
	record.evidenceDiagnosticCount++
	record.latestEvidenceDiagnostic = EvidenceDiagnostic{
		EvidenceID: evidenceID, Disposition: EvidenceDispositionNonAuthoritative, Reason: reason,
	}
}

func (engine *decisionEngine) validateCoordinationBindings(
	intent TransitionIntent,
	record taskRecord,
	exists bool,
) error {
	typed, ok := intent.(intentValue)
	if !ok {
		return invalidIntentError()
	}
	switch typed.kind {
	case IntentAcceptRuntimeEvidence:
		payload, ok := typed.payload.(runtimeEvidencePayload)
		if !ok {
			return invalidIntentError()
		}
		if !exists {
			return newError(ErrorEvidenceScopeConflict)
		}
		binding := payload.binding
		operation, exists := record.runtimeOperations[binding.OperationID]
		if !exists || operation.phaseRunID != binding.PhaseRunID ||
			operation.runtimeRunID != binding.RuntimeRunID {
			return newError(ErrorEvidenceScopeConflict)
		}
		if operation.terminal {
			return newError(ErrorEvidenceScopeConflict)
		}
		if operation.authority != (authorityValue{}) {
			if err := validateAuthorityBinding(operation.authority, typed.authority); err != nil {
				return err
			}
		}
		phaseRunBindingValidator := validatePhaseRunBinding
		if record.cancellationState == CancellationCancelled &&
			operation.activityGeneration == record.activityGeneration {
			phaseRunBindingValidator = validatePhaseRunIdentity
		}
		if err := phaseRunBindingValidator(
			record, binding.PhaseRunID, binding.PhaseRunGeneration, binding.PhaseRunFence,
		); err != nil {
			return err
		}
		if operation.generation != binding.Generation || operation.fence != binding.Fence ||
			operation.safetyEpoch != binding.SafetyEpoch ||
			engine.effectiveSafetyEpoch(record) != binding.SafetyEpoch ||
			operation.activityGeneration != intent.Header().ActivityGeneration {
			return newError(ErrorStaleAuthority)
		}
	case IntentAcceptPhaseValidationEvidence:
		payload, ok := typed.payload.(validationEvidencePayload)
		if !ok {
			return invalidIntentError()
		}
		if !exists {
			return newError(ErrorEvidenceScopeConflict)
		}
		binding := payload.binding
		expected, bindingExists := record.validationBindings[binding.PhaseRunID]
		if !bindingExists {
			return newError(ErrorEvidenceScopeConflict)
		}
		if expected.terminal {
			return newError(ErrorEvidenceScopeConflict)
		}
		if expected.authority != (authorityValue{}) {
			if err := validateAuthorityBinding(expected.authority, typed.authority); err != nil {
				return err
			}
		}
		if err := validatePhaseRunBinding(
			record, binding.PhaseRunID, binding.PhaseRunGeneration, binding.PhaseRunFence,
		); err != nil {
			return err
		}
		if expected.generation != binding.Generation || expected.fence != binding.Fence ||
			expected.safetyEpoch != binding.SafetyEpoch ||
			engine.effectiveSafetyEpoch(record) != binding.SafetyEpoch ||
			expected.activityGeneration != intent.Header().ActivityGeneration {
			return newError(ErrorStaleAuthority)
		}
	case IntentAcceptTaskWorkspaceLifecycleEvidence:
		payload, ok := typed.payload.(taskWorkspaceLifecycleEvidencePayload)
		if !ok {
			return invalidIntentError()
		}
		if !exists {
			return newError(ErrorEvidenceScopeConflict)
		}
		binding := payload.binding
		operation, exists := record.lifecycleOperations[binding.OperationID]
		if !exists || operation.phaseRunID != binding.PhaseRunID {
			return newError(ErrorEvidenceScopeConflict)
		}
		if operation.terminal {
			return newError(ErrorEvidenceScopeConflict)
		}
		if operation.authority != (authorityValue{}) {
			if err := validateAuthorityBinding(operation.authority, typed.authority); err != nil {
				return err
			}
		}
		if operation.purpose == LifecycleOperationCommit &&
			binding.Outcome != LifecycleEvidenceCommitted &&
			binding.Outcome != TaskWorkspaceLifecycleRejected ||
			operation.purpose == LifecycleOperationCancellationFence &&
				binding.Outcome != LifecycleEvidenceFenced {
			return newError(ErrorEvidenceScopeConflict)
		}
		if err := validatePhaseRunBinding(
			record, binding.PhaseRunID, binding.PhaseRunGeneration, binding.PhaseRunFence,
		); err != nil {
			return err
		}
		if operation.generation != binding.Generation || operation.fence != binding.Fence ||
			operation.safetyEpoch != binding.SafetyEpoch ||
			engine.effectiveSafetyEpoch(record) != binding.SafetyEpoch ||
			operation.activityGeneration != intent.Header().ActivityGeneration {
			return newError(ErrorStaleAuthority)
		}
	case IntentAcceptPublicationEvidence:
		payload, ok := typed.payload.(publicationEvidencePayload)
		if !ok {
			return invalidIntentError()
		}
		if !exists {
			return newError(ErrorEvidenceScopeConflict)
		}
		binding := payload.binding
		operation, operationExists := record.publicationOperations[binding.OperationID]
		if !operationExists || operation.phaseRunID != binding.PhaseRunID {
			return newError(ErrorEvidenceScopeConflict)
		}
		if operation.terminal {
			return newError(ErrorEvidenceScopeConflict)
		}
		if operation.authority != (authorityValue{}) {
			if err := validateAuthorityBinding(operation.authority, typed.authority); err != nil {
				return err
			}
		}
		if err := validatePhaseRunBinding(
			record, binding.PhaseRunID, binding.PhaseRunGeneration, binding.PhaseRunFence,
		); err != nil {
			return err
		}
		if operation.generation != binding.Generation || operation.fence != binding.Fence ||
			operation.safetyEpoch != binding.SafetyEpoch ||
			engine.effectiveSafetyEpoch(record) != binding.SafetyEpoch ||
			operation.activityGeneration != intent.Header().ActivityGeneration {
			return newError(ErrorStaleAuthority)
		}
	case IntentAcceptSchedulingEvidence:
		payload, ok := typed.payload.(schedulingEvidencePayload)
		if !ok {
			return invalidIntentError()
		}
		if !exists {
			return newError(ErrorEvidenceScopeConflict)
		}
		binding := payload.binding
		operation, operationExists := record.schedulingOperations[binding.OperationID]
		if !operationExists || operation.phaseRunID != binding.PhaseRunID {
			return newError(ErrorEvidenceScopeConflict)
		}
		if operation.terminal {
			return newError(ErrorEvidenceScopeConflict)
		}
		if err := validateAuthorityBinding(operation.authority, typed.authority); err != nil {
			return err
		}
		if err := validatePhaseRunBinding(
			record, binding.PhaseRunID, binding.PhaseRunGeneration, binding.PhaseRunFence,
		); err != nil {
			return err
		}
		if operation.generation != binding.Generation || operation.fence != binding.Fence ||
			operation.safetyEpoch != binding.SafetyEpoch ||
			engine.effectiveSafetyEpoch(record) != binding.SafetyEpoch ||
			operation.activityGeneration != intent.Header().ActivityGeneration {
			return newError(ErrorStaleAuthority)
		}
	case IntentApplyOperationalFence:
		payload, ok := typed.payload.(operationalFencePayload)
		if !ok {
			return invalidIntentError()
		}
		if !exists {
			return newError(ErrorEvidenceScopeConflict)
		}
		if engine.persistence.recovery.authority == (authorityValue{}) {
			return newError(ErrorAuthorizationDenied)
		}
		if err := validateAuthorityBinding(
			engine.persistence.recovery.authority, typed.authority,
		); err != nil {
			return err
		}
		binding := payload.binding
		if !isOperationalFenceCatchUp(record, engine.persistence.recovery, binding) &&
			(binding.Generation <= engine.persistence.recovery.generation ||
				binding.Fence <= engine.persistence.recovery.fence ||
				binding.SafetyEpoch <= engine.persistence.recovery.safetyEpoch) {
			return newError(ErrorStaleAuthority)
		}
	case IntentReconcileEnactment:
		payload, ok := typed.payload.(reconcilePayload)
		if !ok {
			return invalidIntentError()
		}
		if !exists || record.reconciler == (authorityValue{}) {
			return newError(ErrorAuthorizationDenied)
		}
		if err := validateAuthorityBinding(record.reconciler, typed.authority); err != nil {
			return err
		}
		if _, exists := record.enactments[payload.operationID]; !exists {
			return newError(ErrorEvidenceScopeConflict)
		}
		if payload.fence <= record.reconciliationFences[payload.operationID] {
			return newError(ErrorStaleAuthority)
		}
	}
	return nil
}

func validateAuthorityBinding(expected, actual authorityValue) error {
	if expected.kind != actual.kind || expected.id != actual.id || expected.reason != actual.reason {
		return newError(ErrorAuthorizationDenied)
	}
	if expected.generation != actual.generation {
		return newError(ErrorStaleAuthority)
	}
	return nil
}

func (engine *decisionEngine) effectiveActivityGeneration(
	record taskRecord,
) ActivityGeneration {
	return effectiveActivityGeneration(record, engine.persistence.recovery)
}

func effectiveActivityGeneration(
	record taskRecord,
	recovery recoveryBinding,
) ActivityGeneration {
	if recovery.activityGenerationFence <= record.recoveryActivityFence {
		return record.activityGeneration
	}
	return record.activityGeneration +
		(recovery.activityGenerationFence - record.recoveryActivityFence)
}

func isOperationalFenceCatchUp(
	record taskRecord,
	recovery recoveryBinding,
	binding OperationalFenceBinding,
) bool {
	return record.recoveryActivityFence < recovery.activityGenerationFence &&
		binding.Generation == recovery.generation &&
		binding.Fence == recovery.fence &&
		binding.SafetyEpoch == recovery.safetyEpoch &&
		binding.Mode == recovery.mode
}

func (engine *decisionEngine) effectiveSafetyEpoch(record taskRecord) SafetyEpoch {
	return effectiveSafetyEpoch(record, engine.persistence.recovery)
}

func effectiveSafetyEpoch(record taskRecord, recovery recoveryBinding) SafetyEpoch {
	if recovery.safetyEpoch != 0 {
		return recovery.safetyEpoch
	}
	if record.safetyEpoch != 0 {
		return record.safetyEpoch
	}
	return 1
}

func validatePhaseRunBinding(
	record taskRecord,
	phaseRunID PhaseRunID,
	generation PhaseRunGeneration,
	fence PhaseRunFence,
) error {
	if err := validatePhaseRunIdentity(record, phaseRunID, generation, fence); err != nil {
		return err
	}
	if !record.phaseRuns[phaseRunID].active {
		return newError(ErrorStaleAuthority)
	}
	return nil
}

func validatePhaseRunIdentity(
	record taskRecord,
	phaseRunID PhaseRunID,
	generation PhaseRunGeneration,
	fence PhaseRunFence,
) error {
	phaseRun, exists := record.phaseRuns[phaseRunID]
	if !exists {
		return newError(ErrorEvidenceScopeConflict)
	}
	if phaseRun.generation != generation || phaseRun.fence != fence {
		return newError(ErrorStaleAuthority)
	}
	return nil
}

func markAcceptedEvidenceTerminal(intent TransitionIntent, record *taskRecord) {
	typed, ok := intent.(intentValue)
	if !ok {
		return
	}
	switch typed.kind {
	case IntentAcceptRuntimeEvidence:
		payload := typed.payload.(runtimeEvidencePayload)
		operation := record.runtimeOperations[payload.binding.OperationID]
		operation.terminal = true
		record.runtimeOperations[payload.binding.OperationID] = operation
	case IntentAcceptPhaseValidationEvidence:
		payload := typed.payload.(validationEvidencePayload)
		binding := record.validationBindings[payload.binding.PhaseRunID]
		binding.terminal = true
		record.validationBindings[payload.binding.PhaseRunID] = binding
	case IntentAcceptTaskWorkspaceLifecycleEvidence:
		payload := typed.payload.(taskWorkspaceLifecycleEvidencePayload)
		operation := record.lifecycleOperations[payload.binding.OperationID]
		operation.terminal = true
		record.lifecycleOperations[payload.binding.OperationID] = operation
	case IntentAcceptPublicationEvidence:
		payload := typed.payload.(publicationEvidencePayload)
		operation := record.publicationOperations[payload.binding.OperationID]
		operation.terminal = true
		record.publicationOperations[payload.binding.OperationID] = operation
	case IntentAcceptSchedulingEvidence:
		payload := typed.payload.(schedulingEvidencePayload)
		operation := record.schedulingOperations[payload.binding.OperationID]
		operation.terminal = true
		record.schedulingOperations[payload.binding.OperationID] = operation
	}
}

func applyAcceptedCoordinationOutcome(intent TransitionIntent, record *taskRecord) {
	typed, ok := intent.(intentValue)
	if !ok || typed.kind != IntentAcceptTaskWorkspaceLifecycleEvidence {
		return
	}
	payload, ok := typed.payload.(taskWorkspaceLifecycleEvidencePayload)
	if !ok {
		return
	}
	switch payload.binding.Outcome {
	case LifecycleEvidenceCommitted:
		record.latestRevisionID = payload.binding.RevisionID
		record.latestCheckpointID = payload.binding.CheckpointID
	case LifecycleEvidenceFenced:
		if record.cancellationState == CancellationCancelling {
			record.cancellationState = CancellationCancelled
		}
	}
}

func evidenceDiagnosticReason(err error) EvidenceDiagnosticReason {
	decisionError, ok := err.(*Error)
	if !ok {
		return EvidenceDiagnosticScopeConflict
	}
	switch decisionError.Code() {
	case ErrorStaleAuthority, ErrorStaleTaskRevision:
		return EvidenceDiagnosticStale
	case ErrorAuthorizationDenied:
		return EvidenceDiagnosticUnauthorized
	default:
		return EvidenceDiagnosticScopeConflict
	}
}

func authorizeUserMutation(intent TransitionIntent, record taskRecord, exists bool) error {
	typed := intent.(intentValue)
	if typed.authority.kind != AuthorityUser {
		return nil
	}
	if !exists {
		if intent.Kind() == IntentStartTask {
			return nil
		}
		return newError(ErrorAuthorizationDenied)
	}
	requestedOwner := userOwnershipBinding{
		authorityID: typed.authority.id,
		generation:  typed.authority.generation,
	}
	if record.owner.authorityID != requestedOwner.authorityID {
		return newError(ErrorAuthorizationDenied)
	}
	if record.owner.generation != requestedOwner.generation {
		return newError(ErrorStaleAuthority)
	}
	return nil
}

func (engine *decisionEngine) Query(
	ctx context.Context,
	query TaskQuery,
) (TaskOrchestrationView, error) {
	if engine.controls.isCrashed() {
		return TaskOrchestrationView{}, newError(ErrorDependencyUnavailable)
	}
	if ctx.Err() != nil {
		return TaskOrchestrationView{}, newError(ErrorDependencyUnavailable)
	}
	if !validOpaqueID(query.TaskID.value) || !query.Authority.value.valid() ||
		query.Authority.value.kind != AuthorityUser {
		return TaskOrchestrationView{}, invalidIntentError()
	}
	engine.persistence.mu.Lock()
	defer engine.persistence.mu.Unlock()
	record, exists := engine.persistence.tasks[query.TaskID]
	requestedOwner := userOwnershipBinding{
		authorityID: query.Authority.value.id,
		generation:  query.Authority.value.generation,
	}
	if !exists || record.owner != requestedOwner {
		return TaskOrchestrationView{}, newError(ErrorAuthorizationDenied)
	}
	view := TaskOrchestrationView{
		TaskID:                   query.TaskID,
		TaskRevision:             record.revision,
		ActivityGeneration:       engine.effectiveActivityGeneration(record),
		LatestDecisionID:         record.latestDecision.DecisionID,
		DecisionCount:            record.decisionCount,
		EnactmentCount:           record.enactmentCount,
		EvidenceDiagnosticCount:  record.evidenceDiagnosticCount,
		LatestEvidenceDiagnostic: record.latestEvidenceDiagnostic,
		LatestRevisionID:         record.latestRevisionID,
		LatestCheckpointID:       record.latestCheckpointID,
		CancellationState:        record.cancellationState,
		PhaseRunCount:            uint64(len(record.phaseRuns)),
		RuntimeRunCount:          record.runtimeRunCount(),
		SafetyEpoch:              engine.effectiveSafetyEpoch(record),
		OperationalMode:          engine.persistence.recovery.mode,
	}
	if record.aggregate != nil {
		view.Status = record.aggregate.status
		view.Route = record.aggregate.route
		view.Activity = record.aggregate.activity
		view.ExecutionLockID = record.aggregate.pinned.ExecutionLock.ID
		view.TemplateLockID = record.aggregate.pinned.TemplateLockID
		view.CurrentPhase = record.aggregate.currentPhase
		view.ActivePhaseRunID = record.aggregate.activePhaseRunID
		view.LatestArtifactVersionID = record.aggregate.latestArtifactVersionID
		view.TaskWorkspaceID = record.aggregate.pinned.TaskWorkspaceID
		view.PhaseRuns = make([]PhaseRunView, len(record.aggregate.phaseRuns))
		for index, run := range record.aggregate.phaseRuns {
			view.PhaseRuns[index] = PhaseRunView{
				PhaseRunID: run.id, PhaseKey: run.phaseKey, Attempt: run.attempt,
				Generation: run.generation, Fence: run.fence,
				Outcome: run.outcome, ValidationOutcome: run.validationOutcome,
				LifecycleOutcome: run.lifecycleOutcome, RevisionID: run.revisionID,
				CheckpointID:       run.checkpointID,
				PublicationOutcome: run.publicationOutcome, ArtifactVersionID: run.artifactVersionID,
				TaskWorkspaceID: run.taskWorkspaceID, InputArtifactVersionID: run.inputArtifactVersionID,
				RuntimeRuns: make([]RuntimeRunView, len(run.runtimeRuns)),
			}
			for runtimeIndex, runtimeRun := range run.runtimeRuns {
				view.PhaseRuns[index].RuntimeRuns[runtimeIndex] = RuntimeRunView{
					RuntimeRunID: runtimeRun.id, Outcome: runtimeRun.outcome,
				}
			}
		}
	}
	return view, nil
}

func (record taskRecord) runtimeRunCount() uint64 {
	runtimeRuns := make(map[RuntimeRunID]struct{})
	for _, operation := range record.runtimeOperations {
		runtimeRuns[operation.runtimeRunID] = struct{}{}
	}
	return uint64(len(runtimeRuns))
}
