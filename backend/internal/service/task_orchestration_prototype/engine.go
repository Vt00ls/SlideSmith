package taskorchestrationprototype

import "fmt"

type Engine interface {
	Name() string
	Apply(Snapshot, Trigger) Snapshot
}

type CommandDecisionEngine struct{}

func (CommandDecisionEngine) Name() string { return "command/decision" }

func (CommandDecisionEngine) Apply(before Snapshot, trigger Trigger) Snapshot {
	if replay, ok := before.IdempotencyRecords[trigger.IdempotencyKey]; ok {
		if replay != trigger.signature() {
			before.LastOutcome = "rejected: idempotency key reused with different intent"
			return before
		}
		before.LastOutcome = "replayed: prior decision returned without another write"
		return before
	}
	if trigger.ExpectedRevision != before.Revision {
		before.LastOutcome = fmt.Sprintf("rejected before write: expected revision %d, current %d", trigger.ExpectedRevision, before.Revision)
		return before
	}
	after, note, err := reduce(before, trigger)
	if err != nil {
		before.LastOutcome = "rejected before write: " + err.Error()
		return before
	}
	after.Revision++
	after.IdempotencyRecords[trigger.IdempotencyKey] = trigger.signature()
	after.Journal = append(after.Journal, JournalEntry{
		Position: len(after.Journal) + 1,
		Kind:     "decision",
		Key:      string(trigger.Kind),
		Accepted: true,
		Note:     note,
	})
	after.LastOutcome = note
	return after
}

type EventEnactmentEngine struct{}

func (EventEnactmentEngine) Name() string { return "event/enactment" }

func (EventEnactmentEngine) Apply(before Snapshot, trigger Trigger) Snapshot {
	if replay, ok := before.IdempotencyRecords[trigger.IdempotencyKey]; ok {
		if replay != trigger.signature() {
			before.LastOutcome = "rejected event: event id reused with different payload"
			before.RejectedAuthoritativeFacts++
			before.Journal = append(before.Journal, JournalEntry{
				Position: len(before.Journal) + 1, Kind: "event", Key: string(trigger.Kind), Accepted: false,
				Note: "immutable input exists but cannot be projected",
			})
			return before
		}
		before.LastOutcome = "replayed: duplicate event ignored"
		return before
	}

	// This shape accepts the external event at its boundary before it knows
	// whether the authoritative Task projection can apply it. Rejection remains
	// visible in the event journal, demonstrating the need for a command gate.
	before.IdempotencyRecords[trigger.IdempotencyKey] = trigger.signature()
	if trigger.ExpectedRevision != before.Revision {
		before.RejectedAuthoritativeFacts++
		before.Journal = append(before.Journal, JournalEntry{
			Position: len(before.Journal) + 1, Kind: "event", Key: string(trigger.Kind), Accepted: false,
			Note: fmt.Sprintf("stale event at revision %d; projection is %d", trigger.ExpectedRevision, before.Revision),
		})
		before.LastOutcome = "event appended but rejected by projection: optimistic concurrency conflict"
		return before
	}
	after, note, err := reduce(before, trigger)
	if err != nil {
		before.RejectedAuthoritativeFacts++
		before.Journal = append(before.Journal, JournalEntry{
			Position: len(before.Journal) + 1, Kind: "event", Key: string(trigger.Kind), Accepted: false,
			Note: err.Error(),
		})
		before.LastOutcome = "event appended but rejected by projection: " + err.Error()
		return before
	}
	after.Revision++
	after.Journal = append(after.Journal, JournalEntry{
		Position: len(after.Journal) + 1, Kind: "event", Key: string(trigger.Kind), Accepted: true, Note: note,
	})
	after.LastOutcome = note
	return after
}

func reduce(state Snapshot, trigger Trigger) (Snapshot, string, error) {
	switch trigger.Kind {
	case StartTask:
		if state.Status != "ready" {
			return state, "", fmt.Errorf("Task is not ready")
		}
		if trigger.Route != GenerationRoute && trigger.Route != BeautifyRoute && trigger.Route != TemplateFillRoute {
			return state, "", fmt.Errorf("unknown Route")
		}
		state.Route = trigger.Route
		state.PipelineVersion = "pipeline/" + string(trigger.Route) + "@v1"
		state.RuntimeRelease = "runtime/ppt-master@sha256:fixed"
		if trigger.Route != TemplateFillRoute {
			state.TemplateLock = "template/catalog@version+bundle-digests"
		} else {
			state.TemplateLock = "fill-template/source-material@digest"
		}
		state.Status = "running"
		state.Activity = "generation_pipeline"
		state.CurrentPhaseIndex = 0
		return state, "Route selected and immutable Pipeline/Runtime/Template locks recorded atomically", nil

	case WorkerTick:
		if state.Status != "running" || state.ActivePhaseRunID != "" {
			return state, "", fmt.Errorf("no claimable Phase")
		}
		phase, ok := state.currentPhase()
		if !ok {
			return state, "", fmt.Errorf("pinned Pipeline has no current Phase")
		}
		attempt := 1
		for _, prior := range state.PhaseRuns {
			if prior.PhaseKey == phase.Key && prior.Attempt >= attempt {
				attempt = prior.Attempt + 1
			}
		}
		run := PhaseRun{
			ID: state.nextID("phase-run"), PhaseKey: phase.Key, Attempt: attempt,
			Outcome: "running", Fence: state.Revision + 1, RuntimeRuns: []RuntimeRun{},
		}
		state.PhaseRuns = append(state.PhaseRuns, run)
		state.ActivePhaseRunID = run.ID
		active, _ := state.activeRun()
		switch {
		case phase.ConfirmationGate:
			state.Status = "awaiting_confirmation"
			state.addEnactment("present_confirmation_gate", active)
		case phase.PublishesArtifacts:
			state.addEnactment("request_artifact_publication", active)
		default:
			state.addEnactment("request_runtime_run", active)
		}
		return state, "created one historical Phase Run from the pinned Phase definition", nil

	case WorkerClaimLost:
		if state.ActivePhaseRunID == "" {
			return state, "", fmt.Errorf("no active Phase Run delivery to lose")
		}
		return state, "delivery claim lost; authoritative Phase Run and pending enactment are unchanged", nil

	case RuntimeSucceeded, RuntimeFailed:
		run, phase, err := requireActiveRuntime(state)
		if err != nil {
			return state, "", err
		}
		outcome := "succeeded"
		if trigger.Kind == RuntimeFailed {
			outcome = "failed"
		}
		run.RuntimeRuns = append(run.RuntimeRuns, RuntimeRun{
			ID: state.nextID("runtime-run"), Outcome: outcome, LeaseRef: state.nextID("sandbox-lease"),
		})
		state.completeEnactments(run.ID, "request_runtime_run")
		if trigger.Kind == RuntimeFailed {
			run.Outcome = "failed"
			state.ActivePhaseRunID = ""
			state.Status = "failed"
			return state, "Runtime evidence recorded; Phase Run failed without advancing the Pipeline", nil
		}
		state.addEnactment("request_phase_validation", run)
		return state, fmt.Sprintf("Runtime evidence recorded for %s; validation still owns attempt outcome", phase.Key), nil

	case ValidationSucceeded, ValidationFailed:
		run, phase, err := requireActiveRuntime(state)
		if err != nil {
			return state, "", err
		}
		if len(run.RuntimeRuns) == 0 || run.RuntimeRuns[len(run.RuntimeRuns)-1].Outcome != "succeeded" {
			return state, "", fmt.Errorf("successful Runtime evidence is required before validation")
		}
		state.completeEnactments(run.ID, "request_phase_validation")
		if trigger.Kind == ValidationFailed {
			run.ValidationEvidence = "failed"
			run.Outcome = "failed"
			state.ActivePhaseRunID = ""
			state.Status = "failed"
			return state, "validation failed the Phase Run; history retained for a new-attempt retry", nil
		}
		run.ValidationEvidence = "succeeded"
		if phase.MutatesWorkspace {
			state.addEnactment("request_workspace_commit", run)
			return state, "validation succeeded; C04 commit evidence is required before Phase success", nil
		}
		finishPhase(&state, run, "succeeded")
		return state, "validation alone completed this non-mutating Phase Run", nil

	case WorkspaceCommitted:
		run, phase, err := requireActiveRuntime(state)
		if err != nil {
			return state, "", err
		}
		if !phase.MutatesWorkspace || run.ValidationEvidence != "succeeded" {
			return state, "", fmt.Errorf("validated mutating Phase Run required")
		}
		run.WorkspaceCommit = state.nextID("commit-evidence")
		state.WorkspaceRevision = state.nextID("workspace-revision")
		state.Checkpoint = state.nextID("checkpoint")
		state.completeEnactments(run.ID, "request_workspace_commit", "request_cancel_runtime", "request_fence_and_discard")
		if run.CancellationRequested {
			finishPhase(&state, run, "succeeded_before_cancel_fence")
			state.Status = "cancelled"
			return state, "C04 commit had already linearized; recorded it, then cancelled before another Phase", nil
		}
		finishPhase(&state, run, "succeeded")
		return state, "C04 commit evidence completed the Phase Run and advanced the Pipeline", nil

	case ConfirmationApproved:
		if state.Status != "awaiting_confirmation" {
			return state, "", fmt.Errorf("Task is not at a Confirmation Gate")
		}
		run, ok := state.activeRun()
		phase, phaseOK := state.currentPhase()
		if !ok || !phaseOK || !phase.ConfirmationGate {
			return state, "", fmt.Errorf("active Phase Run is not a Confirmation Gate")
		}
		state.completeEnactments(run.ID, "present_confirmation_gate")
		finishPhase(&state, run, "succeeded")
		return state, "authorized user evidence completed a zero-Runtime-Run Confirmation Gate", nil

	case PublicationSucceeded:
		run, ok := state.activeRun()
		phase, phaseOK := state.currentPhase()
		if !ok || !phaseOK || !phase.PublishesArtifacts {
			return state, "", fmt.Errorf("active Phase Run is not publication")
		}
		run.PublicationEvidence = state.nextID("publication-evidence")
		state.LatestArtifactVersion = state.nextID("artifact-version")
		state.completeEnactments(run.ID, "request_artifact_publication")
		finishPhase(&state, run, "succeeded")
		return state, "immutable Artifact Version evidence completed publication", nil

	case CancelTask:
		if state.Status == "cancelled" || state.Status == "completed" {
			return state, "", fmt.Errorf("Task has no cancellable activity")
		}
		run, ok := state.activeRun()
		if !ok {
			state.Status = "cancelled"
			return state, "Task cancelled before a Phase Run became active", nil
		}
		run.CancellationRequested = true
		state.Status = "cancelling"
		state.addEnactment("request_cancel_runtime", run)
		state.addEnactment("request_fence_and_discard", run)
		return state, "cancel requested; terminal cancellation waits for C04 fencing evidence", nil

	case CancellationFenced:
		if state.Status != "cancelling" {
			return state, "", fmt.Errorf("Task is not awaiting cancellation fencing")
		}
		run, ok := state.activeRun()
		if !ok || !run.CancellationRequested {
			return state, "", fmt.Errorf("active Phase Run has no cancellation request")
		}
		state.completeEnactments(run.ID, "request_cancel_runtime", "request_fence_and_discard")
		finishPhase(&state, run, "cancelled")
		state.Status = "cancelled"
		return state, "C04 proved no mutation can commit; cancellation is terminal", nil

	case RetryPhase:
		if state.Status != "failed" || state.ActivePhaseRunID != "" {
			return state, "", fmt.Errorf("only a failed Phase without an active attempt can retry")
		}
		state.Status = "running"
		return state, "retry retained history and made the same pinned Phase eligible for a new attempt", nil

	case ReconcileTask:
		run, ok := state.activeRun()
		if !ok {
			return state, "reconciliation found no active attempt", nil
		}
		phase, _ := state.currentPhase()
		switch {
		case state.Status == "cancelling":
			state.addEnactment("request_cancel_runtime", run)
			state.addEnactment("request_fence_and_discard", run)
		case phase.ConfirmationGate:
			state.addEnactment("present_confirmation_gate", run)
		case phase.PublishesArtifacts:
			state.addEnactment("request_artifact_publication", run)
		case run.ValidationEvidence == "succeeded" && phase.MutatesWorkspace:
			state.addEnactment("request_workspace_commit", run)
		case len(run.RuntimeRuns) > 0 && run.RuntimeRuns[len(run.RuntimeRuns)-1].Outcome == "succeeded":
			state.addEnactment("request_phase_validation", run)
		default:
			state.addEnactment("request_runtime_run", run)
		}
		return state, "reconciliation reissued the same idempotent enactment; no new Phase Run was created", nil

	case StartManualEdit:
		if state.Status != "completed" || trigger.ArtifactVersion != state.LatestArtifactVersion {
			return state, "", fmt.Errorf("manual edit must bind the latest completed Artifact Version")
		}
		state.Status = "running"
		state.Activity = "manual_edit"
		state.CurrentPhaseIndex = 0
		state.ActivePhaseRunID = ""
		return state, "manual edit entered the same orchestration seam with existing immutable locks", nil
	}
	return state, "", fmt.Errorf("unsupported trigger %q", trigger.Kind)
}

func requireActiveRuntime(state Snapshot) (*PhaseRun, PhaseDefinition, error) {
	run, ok := state.activeRun()
	phase, phaseOK := state.currentPhase()
	if !ok || !phaseOK || !phase.NeedsRuntime {
		return nil, PhaseDefinition{}, fmt.Errorf("active Phase Run does not accept Runtime evidence")
	}
	if state.Status == "cancelling" && run.CancellationRequested {
		// A terminal Runtime observation may still be recorded during fencing.
		return run, phase, nil
	}
	if state.Status != "running" {
		return nil, PhaseDefinition{}, fmt.Errorf("Task is not running")
	}
	return run, phase, nil
}

func finishPhase(state *Snapshot, run *PhaseRun, outcome string) {
	run.Outcome = outcome
	state.ActivePhaseRunID = ""
	state.CurrentPhaseIndex++
	phases := phasesFor(state.Route, state.Activity)
	if state.CurrentPhaseIndex >= len(phases) {
		state.Status = "completed"
		state.Activity = ""
		return
	}
	state.Status = "running"
}
