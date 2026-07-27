package taskorchestration

import "fmt"

type Route uint8

const (
	RouteGeneration Route = iota + 1
	RouteBeautify
	RouteTemplateFill
)

func (route Route) String() string {
	switch route {
	case RouteGeneration:
		return "generation"
	case RouteBeautify:
		return "beautify"
	case RouteTemplateFill:
		return "template_fill"
	default:
		return ""
	}
}

type TaskStatus uint8

const (
	TaskReady TaskStatus = iota + 1
	TaskRunning
	TaskAwaitingConfirmation
	TaskCancelling
	TaskFailed
	TaskCancelled
	TaskCompleted
)

type ActivityKind uint8

const (
	ActivityGenerationPipeline ActivityKind = iota + 1
	ActivityManualEdit
)

type PhaseKind uint8

const (
	PhaseNonMutating PhaseKind = iota + 1
	PhaseMutating
	PhaseConfirmationGate
	PhasePublication
)

type PhaseValidationContract uint8

const (
	PhaseValidationNone PhaseValidationContract = iota
	PhaseValidationAllRuntimeRunsSucceeded
)

type PhaseRunOutcome uint8

const (
	PhaseRunRunning PhaseRunOutcome = iota + 1
	PhaseRunFailed
	PhaseRunCancelled
	PhaseRunSucceeded
)

type RuntimeRunOutcome uint8

const (
	RuntimeRunPending RuntimeRunOutcome = iota + 1
	RuntimeRunSucceeded
	RuntimeRunFailed
)

type PhaseValidationOutcome uint8

const (
	PhaseValidationAccepted PhaseValidationOutcome = iota + 1
	PhaseValidationRejected
)

type TaskWorkspaceLifecycleOutcome uint8

const (
	TaskWorkspaceLifecycleCommitted TaskWorkspaceLifecycleOutcome = iota + 1
	TaskWorkspaceLifecycleRejected
	TaskWorkspaceLifecycleFenced
)

type PublicationOutcome uint8

const (
	PublicationActivated PublicationOutcome = iota + 1
	PublicationRejected
)

func runtimeRunOutcomeName(outcome RuntimeRunOutcome) string {
	switch outcome {
	case RuntimeRunSucceeded:
		return "succeeded"
	case RuntimeRunFailed:
		return "failed"
	default:
		return ""
	}
}

func phaseValidationOutcomeName(outcome PhaseValidationOutcome) string {
	switch outcome {
	case PhaseValidationAccepted:
		return "accepted"
	case PhaseValidationRejected:
		return "rejected"
	default:
		return ""
	}
}

func taskWorkspaceLifecycleOutcomeName(outcome TaskWorkspaceLifecycleOutcome) string {
	switch outcome {
	case TaskWorkspaceLifecycleCommitted:
		return "committed"
	case TaskWorkspaceLifecycleRejected:
		return "rejected"
	case TaskWorkspaceLifecycleFenced:
		return "fenced"
	default:
		return ""
	}
}

func publicationOutcomeName(outcome PublicationOutcome) string {
	switch outcome {
	case PublicationActivated:
		return "activated"
	case PublicationRejected:
		return "rejected"
	default:
		return ""
	}
}

type PipelineContractVersion uint16

const PipelineContractV1 PipelineContractVersion = 1

type PhaseDefinition struct {
	Key                 PhaseKey
	Kind                PhaseKind
	ValidationContract  PhaseValidationContract
	RequiredRuntimeRuns uint32
	RetryEligible       bool
	GateID              GateID
	NextPhase           PhaseKey
}

type RouteDefinition struct {
	Route      Route
	EntryPhase PhaseKey
	Phases     []PhaseDefinition
}

type PipelineContract struct {
	SchemaVersion        PipelineContractVersion
	PipelineVersionID    PipelineVersionID
	Routes               []RouteDefinition
	ManualEditEntryPhase PhaseKey
	ManualEditPhases     []PhaseDefinition
}

type ExecutionLock struct {
	ID                      ExecutionLockID
	PipelineVersionID       PipelineVersionID
	RuntimeReleaseID        RuntimeReleaseID
	CompatibilityApprovalID CompatibilityApprovalID
	PipelineContract        PipelineContract
}

type PinnedTaskStart struct {
	Route           Route
	TaskWorkspaceID TaskWorkspaceID
	ExecutionLock   ExecutionLock
	TemplateLockID  TemplateLockID
	Authorities     DownstreamAuthorityBindings
}

// DownstreamAuthorityBindings pins the only producer identity allowed to
// return evidence for each enactment family created by this Task. The binding
// is immutable with the Route and locks; retry and manual edit reuse it.
type DownstreamAuthorityBindings struct {
	Runtime                RuntimeAuthority
	Validator              ValidatorAuthority
	TaskWorkspaceLifecycle TaskWorkspaceLifecycleAuthority
	Publication            PublicationAuthority
	Scheduler              SchedulerAuthority
}

func (bindings DownstreamAuthorityBindings) valid() bool {
	return bindings.Runtime.value.valid() && bindings.Runtime.value.kind == AuthorityRuntime &&
		bindings.Validator.value.valid() && bindings.Validator.value.kind == AuthorityValidator &&
		bindings.TaskWorkspaceLifecycle.value.valid() &&
		bindings.TaskWorkspaceLifecycle.value.kind == AuthorityTaskWorkspaceLifecycle &&
		bindings.Publication.value.valid() && bindings.Publication.value.kind == AuthorityPublication &&
		bindings.Scheduler.value.valid() && bindings.Scheduler.value.kind == AuthorityScheduler
}

func (bindings DownstreamAuthorityBindings) canonical() map[string]any {
	return map[string]any{
		"publication":              bindings.Publication.value.canonical(),
		"runtime":                  bindings.Runtime.value.canonical(),
		"scheduler":                bindings.Scheduler.value.canonical(),
		"task_workspace_lifecycle": bindings.TaskWorkspaceLifecycle.value.canonical(),
		"validator":                bindings.Validator.value.canonical(),
	}
}

func (pinned PinnedTaskStart) valid() bool {
	if pinned.Route.String() == "" || !validOpaqueID(pinned.TaskWorkspaceID.value) ||
		!pinned.Authorities.valid() ||
		!validOpaqueID(pinned.ExecutionLock.ID.value) ||
		!validOpaqueID(pinned.ExecutionLock.PipelineVersionID.value) ||
		!validOpaqueID(pinned.ExecutionLock.RuntimeReleaseID.value) ||
		!validOpaqueID(pinned.ExecutionLock.CompatibilityApprovalID.value) ||
		pinned.ExecutionLock.PipelineVersionID != pinned.ExecutionLock.PipelineContract.PipelineVersionID ||
		pinned.ExecutionLock.PipelineContract.SchemaVersion != PipelineContractV1 {
		return false
	}
	if pinned.Route == RouteGeneration {
		if !validOpaqueID(pinned.TemplateLockID.value) {
			return false
		}
	} else if pinned.TemplateLockID != (TemplateLockID{}) {
		return false
	}
	_, ok := pinned.ExecutionLock.PipelineContract.route(pinned.Route)
	if !ok {
		return false
	}
	contract := pinned.ExecutionLock.PipelineContract
	if contract.ManualEditEntryPhase != (PhaseKey{}) || len(contract.ManualEditPhases) > 0 {
		return validPhaseGraph(contract.ManualEditEntryPhase, contract.ManualEditPhases)
	}
	return true
}

func (contract PipelineContract) route(route Route) (RouteDefinition, bool) {
	for _, definition := range contract.Routes {
		if definition.Route == route && validPhaseGraph(definition.EntryPhase, definition.Phases) {
			return cloneRouteDefinition(definition), true
		}
	}
	return RouteDefinition{}, false
}

func (contract PipelineContract) manualEdit() (PhaseKey, []PhaseDefinition, bool) {
	if !validPhaseGraph(contract.ManualEditEntryPhase, contract.ManualEditPhases) {
		return PhaseKey{}, nil, false
	}
	return contract.ManualEditEntryPhase,
		append([]PhaseDefinition(nil), contract.ManualEditPhases...), true
}

func validPhaseGraph(entry PhaseKey, phases []PhaseDefinition) bool {
	if !validOpaqueID(entry.value) || len(phases) == 0 {
		return false
	}
	definitions := make(map[PhaseKey]PhaseDefinition, len(phases))
	for _, phase := range phases {
		if !validPhaseDefinition(phase) {
			return false
		}
		if _, duplicate := definitions[phase.Key]; duplicate {
			return false
		}
		definitions[phase.Key] = phase
	}
	visited := make(map[PhaseKey]bool, len(phases))
	current := entry
	for current != (PhaseKey{}) {
		if visited[current] {
			return false
		}
		phase, exists := definitions[current]
		if !exists {
			return false
		}
		visited[current] = true
		current = phase.NextPhase
	}
	return len(visited) == len(definitions)
}

func validPhaseDefinition(phase PhaseDefinition) bool {
	if !validOpaqueID(phase.Key.value) || phase.Kind < PhaseNonMutating || phase.Kind > PhasePublication {
		return false
	}
	if phase.Kind == PhaseConfirmationGate {
		return phase.RequiredRuntimeRuns == 0 && validOpaqueID(phase.GateID.value) &&
			phase.ValidationContract == PhaseValidationNone
	}
	if phase.GateID != (GateID{}) {
		return false
	}
	if phase.Kind == PhasePublication {
		return phase.RequiredRuntimeRuns == 0 &&
			phase.ValidationContract == PhaseValidationNone
	}
	return phase.ValidationContract == PhaseValidationAllRuntimeRunsSucceeded
}

func (contract PipelineContract) canonical() map[string]any {
	routes := make([]any, 0, len(contract.Routes))
	for _, route := range contract.Routes {
		routes = append(routes, route.canonical())
	}
	manualPhases := make([]any, 0, len(contract.ManualEditPhases))
	for _, phase := range contract.ManualEditPhases {
		manualPhases = append(manualPhases, phase.canonical())
	}
	return map[string]any{
		"manual_edit_entry_phase": contract.ManualEditEntryPhase.value,
		"manual_edit_phases":      manualPhases,
		"pipeline_version_id":     contract.PipelineVersionID.value,
		"routes":                  routes,
		"schema_version":          uint64(contract.SchemaVersion),
	}
}

func (definition RouteDefinition) canonical() map[string]any {
	phases := make([]any, 0, len(definition.Phases))
	for _, phase := range definition.Phases {
		phases = append(phases, phase.canonical())
	}
	return map[string]any{
		"entry_phase": definition.EntryPhase.value,
		"phases":      phases,
		"route":       definition.Route.String(),
	}
}

func (phase PhaseDefinition) canonical() map[string]any {
	return map[string]any{
		"gate_id":               phase.GateID.value,
		"key":                   phase.Key.value,
		"kind":                  uint64(phase.Kind),
		"next_phase":            phase.NextPhase.value,
		"required_runtime_runs": uint64(phase.RequiredRuntimeRuns),
		"retry_eligible":        phase.RetryEligible,
		"validation_contract":   phaseValidationContractName(phase.ValidationContract),
	}
}

func phaseValidationContractName(contract PhaseValidationContract) string {
	switch contract {
	case PhaseValidationNone:
		return "none"
	case PhaseValidationAllRuntimeRunsSucceeded:
		return "all_runtime_runs_succeeded"
	default:
		return ""
	}
}

func clonePinnedTaskStart(pinned PinnedTaskStart) PinnedTaskStart {
	cloned := pinned
	cloned.ExecutionLock.PipelineContract.Routes = make([]RouteDefinition, len(pinned.ExecutionLock.PipelineContract.Routes))
	for index, route := range pinned.ExecutionLock.PipelineContract.Routes {
		cloned.ExecutionLock.PipelineContract.Routes[index] = cloneRouteDefinition(route)
	}
	cloned.ExecutionLock.PipelineContract.ManualEditPhases = append(
		[]PhaseDefinition(nil), pinned.ExecutionLock.PipelineContract.ManualEditPhases...,
	)
	return cloned
}

func cloneRouteDefinition(definition RouteDefinition) RouteDefinition {
	definition.Phases = append([]PhaseDefinition(nil), definition.Phases...)
	return definition
}

func phaseDefinitionByKey(phases []PhaseDefinition, key PhaseKey) (PhaseDefinition, bool) {
	for _, phase := range phases {
		if phase.Key == key {
			return phase, true
		}
	}
	return PhaseDefinition{}, false
}

func nextPhaseRunID(sequence uint64) PhaseRunID {
	return PhaseRunID{value: fmt.Sprintf("phase-run-%06d", sequence)}
}

func nextRuntimeRunID(sequence uint64) RuntimeRunID {
	return RuntimeRunID{value: fmt.Sprintf("runtime-run-%06d", sequence)}
}

func nextOperationID(sequence uint64) OperationID {
	return OperationID{value: fmt.Sprintf("operation-%06d", sequence)}
}
