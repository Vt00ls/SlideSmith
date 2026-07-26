package taskorchestration

import (
	"context"
	"sync"
	"time"
)

type HarnessConfig struct {
	Now time.Time
	IDs DeterministicIDConfig
}

type DeterministicIDConfig struct {
	DecisionStart  uint64
	AuditFactStart uint64
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
	nextDecisionSequence  uint64
	nextAuditFactSequence uint64
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
		nextDecisionSequence:  decisionStart,
		nextAuditFactSequence: auditFactStart,
	}
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

type taskRecord struct {
	revision           TaskRevision
	activityGeneration ActivityGeneration
	owner              userOwnershipBinding
	latestDecision     TransitionDecision
	decisionCount      uint64
	enactmentCount     uint64
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
	current := record.revision
	if current != header.ExpectedTaskRevision {
		return TransitionDecision{}, newError(ErrorStaleTaskRevision)
	}
	accepted := current + 1
	decisionID := engine.persistence.ids.nextDecisionID()
	auditFactID := engine.persistence.ids.nextAuditFactID()
	affectedPhaseRuns := []PhaseRunID{}
	acceptedEvidenceRefs := []EvidenceRef{}
	if facts, isEvidenceIntent, factsErr := evidenceFacts(intent); factsErr == nil && isEvidenceIntent {
		affectedPhaseRuns = append(affectedPhaseRuns, facts.phaseRunID)
		acceptedEvidenceRefs = append(acceptedEvidenceRefs, facts.evidence)
	}
	decision := TransitionDecision{
		DecisionID:             decisionID,
		DecisionRequestID:      header.DecisionRequestID,
		CanonicalRequestDigest: digest,
		PreviousTaskRevision:   current,
		AcceptedTaskRevision:   accepted,
		TaskProjection: TaskProjection{
			TaskID:             header.TaskID,
			TaskRevision:       accepted,
			ActivityGeneration: header.ActivityGeneration,
		},
		AffectedPhaseRuns:     affectedPhaseRuns,
		AcceptedEvidenceRefs:  acceptedEvidenceRefs,
		CommittedAt:           engine.clock.current(),
		EnactmentRefs:         []EnactmentRef{},
		MandatoryAuditFactRef: AuditFactRef{AuditFactID: auditFactID},
	}
	record.revision = accepted
	record.activityGeneration = header.ActivityGeneration
	if record.owner == (userOwnershipBinding{}) && intent.Kind() == IntentStartTask {
		typed := intent.(intentValue)
		record.owner = userOwnershipBinding{
			authorityID: typed.authority.id,
			generation:  typed.authority.generation,
		}
	}
	record.latestDecision = decision
	record.decisionCount++
	record.enactmentCount += uint64(len(decision.EnactmentRefs))
	engine.persistence.tasks[header.TaskID] = record
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
	return TaskOrchestrationView{
		TaskID:             query.TaskID,
		TaskRevision:       record.revision,
		ActivityGeneration: record.activityGeneration,
		LatestDecisionID:   record.latestDecision.DecisionID,
		DecisionCount:      record.decisionCount,
		EnactmentCount:     record.enactmentCount,
	}, nil
}
