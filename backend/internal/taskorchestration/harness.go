package taskorchestration

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"time"
)

type HarnessConfig struct {
	Now time.Time
	IDs DeterministicIDConfig
}

type DeterministicIDConfig struct {
	DecisionStart   uint64
	AuditFactStart  uint64
	PhaseRunStart   uint64
	RuntimeRunStart uint64
	OperationStart  uint64
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
		tasks: make(map[TaskID]taskRecord),
		ids:   newDeterministicIDAllocator(config.IDs),
	}
	clock := &controlledClock{now: config.Now.UTC()}
	return newHarness(persistence, clock), nil
}

func newHarness(
	persistence *memoryPersistence,
	clock *controlledClock,
) *DeterministicHarness {
	controls := &harnessControls{}
	engine := &harnessEngine{clock: clock, persistence: persistence, controls: controls}
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
	mu    sync.Mutex
	tasks map[TaskID]taskRecord
	ids   deterministicIDAllocator
}

type deterministicIDAllocator struct {
	nextDecisionSequence   uint64
	nextAuditFactSequence  uint64
	nextPhaseRunSequence   uint64
	nextRuntimeRunSequence uint64
	nextOperationSequence  uint64
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
	return deterministicIDAllocator{
		nextDecisionSequence:   decisionStart,
		nextAuditFactSequence:  auditFactStart,
		nextPhaseRunSequence:   defaultIdentityStart(config.PhaseRunStart),
		nextRuntimeRunSequence: defaultIdentityStart(config.RuntimeRunStart),
		nextOperationSequence:  defaultIdentityStart(config.OperationStart),
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

type taskRecord struct {
	revision           TaskRevision
	activityGeneration ActivityGeneration
	owner              userOwnershipBinding
	latestDecision     TransitionDecision
	decisionCount      uint64
	enactmentCount     uint64
	aggregate          *taskAggregate
	gateDecisions      map[DecisionRequestID]gateDecisionRecord
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

type userOwnershipBinding struct {
	authorityID AuthorityID
	generation  AuthorizationGeneration
}

type harnessEngine struct {
	clock       *controlledClock
	persistence *memoryPersistence
	controls    *harnessControls
}

func (engine *harnessEngine) Decide(
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
	record, exists := engine.persistence.tasks[header.TaskID]
	if err := authorizeUserMutation(intent, record, exists); err != nil {
		return TransitionDecision{}, err
	}
	if intent.Kind() == IntentSubmitConfirmationGate {
		if prior, replay := record.gateDecisions[header.DecisionRequestID]; replay {
			if prior.digest != digest {
				return TransitionDecision{}, newError(ErrorIntegrityConflict)
			}
			return prior.decision, nil
		}
	}
	current := record.revision
	if current != header.ExpectedTaskRevision {
		return TransitionDecision{}, newError(ErrorStaleTaskRevision)
	}
	accepted := current + 1
	ids := engine.persistence.ids
	decisionID := ids.nextDecisionID()
	auditFactID := ids.nextAuditFactID()
	affectedPhaseRuns := []PhaseRunID{}
	acceptedEvidenceRefs := []EvidenceRef{}
	enactmentRefs := []EnactmentRef{}
	updatedRecord := cloneTaskRecord(record)
	if updated, affected, enactments, aggregateErr := applyAggregateIntent(
		updatedRecord, intent, &ids, decisionID,
	); aggregateErr != nil {
		return TransitionDecision{}, aggregateErr
	} else {
		updatedRecord = updated
		affectedPhaseRuns = append(affectedPhaseRuns, affected...)
		enactmentRefs = append(enactmentRefs, enactments...)
	}
	updatedRecord.activityGeneration = header.ActivityGeneration
	if facts, isEvidenceIntent, factsErr := evidenceFacts(intent); factsErr == nil && isEvidenceIntent {
		if len(affectedPhaseRuns) == 0 {
			affectedPhaseRuns = append(affectedPhaseRuns, facts.phaseRunID)
		}
		acceptedEvidenceRefs = append(acceptedEvidenceRefs, facts.evidence)
	}
	projection := taskProjection(header.TaskID, accepted, header.ActivityGeneration, updatedRecord.aggregate)
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
	updatedRecord.revision = accepted
	if updatedRecord.owner == (userOwnershipBinding{}) && intent.Kind() == IntentStartTask {
		typed := intent.(intentValue)
		updatedRecord.owner = userOwnershipBinding{
			authorityID: typed.authority.id,
			generation:  typed.authority.generation,
		}
	}
	updatedRecord.latestDecision = decision
	updatedRecord.decisionCount++
	updatedRecord.enactmentCount += uint64(len(decision.EnactmentRefs))
	if intent.Kind() == IntentSubmitConfirmationGate && updatedRecord.aggregate != nil {
		if updatedRecord.gateDecisions == nil {
			updatedRecord.gateDecisions = make(map[DecisionRequestID]gateDecisionRecord)
		}
		updatedRecord.gateDecisions[header.DecisionRequestID] = gateDecisionRecord{
			digest: digest, decision: decision,
		}
	}
	engine.persistence.tasks[header.TaskID] = updatedRecord
	engine.persistence.ids = ids
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
	if record.gateDecisions != nil {
		record.gateDecisions = make(map[DecisionRequestID]gateDecisionRecord, len(record.gateDecisions))
		for requestID, decision := range record.gateDecisions {
			record.gateDecisions[requestID] = decision
		}
	}
	if record.aggregate == nil {
		return record
	}
	aggregate := *record.aggregate
	aggregate.pinned = clonePinnedTaskStart(record.aggregate.pinned)
	aggregate.phaseRuns = make([]phaseRunRecord, len(record.aggregate.phaseRuns))
	for index, run := range record.aggregate.phaseRuns {
		aggregate.phaseRuns[index] = run
		aggregate.phaseRuns[index].runtimeRuns = append([]runtimeRunRecord(nil), run.runtimeRuns...)
	}
	record.aggregate = &aggregate
	return record
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
		return record, nil, nil, newError(ErrorTerminalConflict)
	}
	switch typed.kind {
	case IntentMakeWorkAvailable:
		return beginAggregatePhase(record, intent, ids, decisionID)
	case IntentAcceptRuntimeEvidence:
		return acceptAggregateRuntimeEvidence(record, typed)
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
	default:
		return record, nil, nil, invalidIntentError()
	}
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
	if !ok || run.lifecycleOperationID != payload.binding.OperationID ||
		run.validationOutcome != PhaseValidationAccepted {
		return record, nil, nil, newError(ErrorEvidenceScopeConflict)
	}
	phase, ok := currentPhaseDefinition(record.aggregate)
	if !ok || phase.Kind != PhaseMutating {
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
	if record.owner != requestedOwner {
		return newError(ErrorAuthorizationDenied)
	}
	return nil
}

func (engine *harnessEngine) Query(
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
		TaskID:             query.TaskID,
		TaskRevision:       record.revision,
		ActivityGeneration: record.activityGeneration,
		LatestDecisionID:   record.latestDecision.DecisionID,
		DecisionCount:      record.decisionCount,
		EnactmentCount:     record.enactmentCount,
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
