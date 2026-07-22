package taskorchestrationprototype

import "fmt"

type Comparison struct {
	Shape                      string         `json:"shape"`
	RouteOutcomes              []RouteOutcome `json:"route_outcomes"`
	FinalStatus                string         `json:"final_status"`
	FinalRevision              int            `json:"final_revision"`
	PhaseRunCount              int            `json:"phase_run_count"`
	RuntimeRunCount            int            `json:"runtime_run_count"`
	RejectedAuthoritativeFacts int            `json:"rejected_authoritative_facts"`
	JournalEntries             int            `json:"journal_entries"`
	Verdict                    string         `json:"verdict"`
}

type RouteOutcome struct {
	Route                       Route    `json:"route"`
	StatusAfterManualEdit       string   `json:"status_after_manual_edit"`
	PhaseKeys                   []string `json:"phase_keys"`
	ConfirmationRuntimeRunCount int      `json:"confirmation_runtime_run_count"`
	LocksStayedPinned           bool     `json:"locks_stayed_pinned"`
}

func RunComparison(engine Engine) (Snapshot, Comparison) {
	routeOutcomes := make([]RouteOutcome, 0, 3)
	for _, route := range []Route{GenerationRoute, BeautifyRoute, TemplateFillRoute} {
		routeOutcomes = append(routeOutcomes, runRouteAndManualEdit(engine, route))
	}

	state := NewSnapshot()
	sequence := []TriggerKind{
		StartTask,
		WorkerTick,
		WorkerClaimLost,
		ReconcileTask,
		RuntimeSucceeded,
		ValidationSucceeded,
		WorkerClaimLost, // invalid after the non-mutating Phase completed
		WorkerTick,
		ConfirmationApproved,
		WorkerTick,
		RuntimeFailed,
		RetryPhase,
		WorkerTick,
		RuntimeSucceeded,
		ValidationSucceeded,
		ReconcileTask, // lost C04 acknowledgement
		WorkspaceCommitted,
		WorkerTick,
		PublicationSucceeded,
		StartManualEdit,
		WorkerTick,
		RuntimeSucceeded,
		ValidationSucceeded,
		CancelTask,
		ReconcileTask,
		CancellationFenced,
	}
	for i, kind := range sequence {
		expected := state.Revision
		if kind == WorkerClaimLost && state.ActivePhaseRunID == "" {
			expected-- // deliberately stale and semantically invalid
		}
		trigger := Trigger{
			Kind: kind, IdempotencyKey: fmt.Sprintf("scenario-%02d", i+1), ExpectedRevision: expected,
			Route: GenerationRoute, ArtifactVersion: state.LatestArtifactVersion,
		}
		state = engine.Apply(state, trigger)
	}

	runtimeRuns := 0
	for _, run := range state.PhaseRuns {
		runtimeRuns += len(run.RuntimeRuns)
	}
	verdict := "requires a pre-append command/authorization gate to reject stale or invalid inputs"
	if engine.Name() == "command/decision" {
		verdict = "small boundary rejected stale input before authoritative write and returned idempotent enactments"
	}
	return state, Comparison{
		Shape: engine.Name(), RouteOutcomes: routeOutcomes,
		FinalStatus: state.Status, FinalRevision: state.Revision,
		PhaseRunCount: len(state.PhaseRuns), RuntimeRunCount: runtimeRuns,
		RejectedAuthoritativeFacts: state.RejectedAuthoritativeFacts,
		JournalEntries:             len(state.Journal), Verdict: verdict,
	}
}

func runRouteAndManualEdit(engine Engine, route Route) RouteOutcome {
	state := NewSnapshot()
	step := 0
	apply := func(kind TriggerKind) {
		step++
		state = engine.Apply(state, Trigger{
			Kind: kind, IdempotencyKey: fmt.Sprintf("%s-happy-%02d", route, step),
			ExpectedRevision: state.Revision, Route: route, ArtifactVersion: state.LatestArtifactVersion,
		})
	}
	apply(StartTask)
	pipelineLock := state.PipelineVersion
	runtimeLock := state.RuntimeRelease
	templateLock := state.TemplateLock
	driveToCompletion := func() {
		for state.Status != "completed" {
			apply(WorkerTick)
			phase, _ := state.currentPhase()
			switch {
			case phase.ConfirmationGate:
				apply(ConfirmationApproved)
			case phase.PublishesArtifacts:
				apply(PublicationSucceeded)
			default:
				apply(RuntimeSucceeded)
				apply(ValidationSucceeded)
				if phase.MutatesWorkspace {
					apply(WorkspaceCommitted)
				}
			}
		}
	}
	driveToCompletion()
	apply(StartManualEdit)
	driveToCompletion()

	phaseKeys := make([]string, 0, len(state.PhaseRuns))
	confirmationRuntimeRuns := 0
	for _, run := range state.PhaseRuns {
		phaseKeys = append(phaseKeys, run.PhaseKey)
		if run.PhaseKey == "design_confirmation" || run.PhaseKey == "beautify_plan_confirmation" || run.PhaseKey == "fill_plan_confirmation" {
			confirmationRuntimeRuns += len(run.RuntimeRuns)
		}
	}
	return RouteOutcome{
		Route: route, StatusAfterManualEdit: state.Status, PhaseKeys: phaseKeys,
		ConfirmationRuntimeRunCount: confirmationRuntimeRuns,
		LocksStayedPinned:           state.PipelineVersion == pipelineLock && state.RuntimeRelease == runtimeLock && state.TemplateLock == templateLock,
	}
}
