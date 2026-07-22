// Package taskorchestrationprototype is throwaway decision evidence for
// Vt00ls/SlideSmith#19. It must not be imported by production code.
package taskorchestrationprototype

import "fmt"

type Route string

const (
	GenerationRoute   Route = "generation"
	BeautifyRoute     Route = "beautify"
	TemplateFillRoute Route = "template_fill"
)

type TriggerKind string

const (
	StartTask            TriggerKind = "start_task"
	WorkerTick           TriggerKind = "worker_tick"
	WorkerClaimLost      TriggerKind = "worker_claim_lost"
	RuntimeSucceeded     TriggerKind = "runtime_succeeded"
	RuntimeFailed        TriggerKind = "runtime_failed"
	ValidationSucceeded  TriggerKind = "validation_succeeded"
	ValidationFailed     TriggerKind = "validation_failed"
	WorkspaceCommitted   TriggerKind = "workspace_committed"
	CancellationFenced   TriggerKind = "cancellation_fenced"
	ConfirmationApproved TriggerKind = "confirmation_approved"
	PublicationSucceeded TriggerKind = "publication_succeeded"
	CancelTask           TriggerKind = "cancel_task"
	RetryPhase           TriggerKind = "retry_phase"
	ReconcileTask        TriggerKind = "reconcile_task"
	StartManualEdit      TriggerKind = "start_manual_edit"
)

type Trigger struct {
	Kind             TriggerKind `json:"kind"`
	IdempotencyKey   string      `json:"idempotency_key"`
	ExpectedRevision int         `json:"expected_revision"`
	Route            Route       `json:"route,omitempty"`
	ArtifactVersion  string      `json:"artifact_version,omitempty"`
}

func (t Trigger) signature() string {
	return fmt.Sprintf("%s|%s|%s", t.Kind, t.Route, t.ArtifactVersion)
}

type PhaseDefinition struct {
	Key                string `json:"key"`
	NeedsRuntime       bool   `json:"needs_runtime"`
	MutatesWorkspace   bool   `json:"mutates_workspace"`
	ConfirmationGate   bool   `json:"confirmation_gate"`
	PublishesArtifacts bool   `json:"publishes_artifacts"`
}

type RuntimeRun struct {
	ID       string `json:"id"`
	Outcome  string `json:"outcome"`
	LeaseRef string `json:"sandbox_lease_ref"`
}

type PhaseRun struct {
	ID                    string       `json:"id"`
	PhaseKey              string       `json:"phase_key"`
	Attempt               int          `json:"attempt"`
	Outcome               string       `json:"outcome"`
	Fence                 int          `json:"fence"`
	RuntimeRuns           []RuntimeRun `json:"runtime_runs"`
	ValidationEvidence    string       `json:"validation_evidence,omitempty"`
	WorkspaceCommit       string       `json:"workspace_commit_evidence,omitempty"`
	PublicationEvidence   string       `json:"publication_evidence,omitempty"`
	CancellationRequested bool         `json:"cancellation_requested"`
}

type Enactment struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	PhaseRunID string `json:"phase_run_id,omitempty"`
	Status     string `json:"status"`
}

type JournalEntry struct {
	Position int    `json:"position"`
	Kind     string `json:"kind"`
	Key      string `json:"key"`
	Accepted bool   `json:"accepted"`
	Note     string `json:"note"`
}

type Snapshot struct {
	TaskID                     string            `json:"task_id"`
	Revision                   int               `json:"revision"`
	Status                     string            `json:"aggregate_status"`
	Activity                   string            `json:"activity,omitempty"`
	Route                      Route             `json:"route,omitempty"`
	PipelineVersion            string            `json:"pinned_pipeline_version,omitempty"`
	RuntimeRelease             string            `json:"pinned_runtime_release,omitempty"`
	TemplateLock               string            `json:"template_lock,omitempty"`
	CurrentPhaseIndex          int               `json:"current_phase_index"`
	ActivePhaseRunID           string            `json:"active_phase_run_id,omitempty"`
	PhaseRuns                  []PhaseRun        `json:"phase_runs"`
	Outbox                     []Enactment       `json:"enactment_outbox"`
	WorkspaceRevision          string            `json:"task_workspace_revision,omitempty"`
	Checkpoint                 string            `json:"checkpoint,omitempty"`
	LatestArtifactVersion      string            `json:"latest_artifact_version,omitempty"`
	IdempotencyRecords         map[string]string `json:"idempotency_records"`
	Journal                    []JournalEntry    `json:"decision_or_event_journal"`
	RejectedAuthoritativeFacts int               `json:"rejected_authoritative_facts"`
	LastOutcome                string            `json:"last_outcome"`
	counter                    int
}

func NewSnapshot() Snapshot {
	return Snapshot{
		TaskID:                "task-prototype-19",
		Status:                "ready",
		LatestArtifactVersion: "artifact-v0",
		IdempotencyRecords:    map[string]string{},
		PhaseRuns:             []PhaseRun{},
		Outbox:                []Enactment{},
		Journal:               []JournalEntry{},
	}
}

func (s *Snapshot) nextID(prefix string) string {
	s.counter++
	return fmt.Sprintf("%s-%02d", prefix, s.counter)
}

func phasesFor(route Route, activity string) []PhaseDefinition {
	if activity == "manual_edit" {
		return []PhaseDefinition{
			{Key: "manual_edit_apply", NeedsRuntime: true, MutatesWorkspace: true},
			{Key: "manual_edit_publish", PublishesArtifacts: true},
		}
	}
	switch route {
	case BeautifyRoute:
		return []PhaseDefinition{
			{Key: "beautify_inventory", NeedsRuntime: true},
			{Key: "beautify_plan_confirmation", ConfirmationGate: true},
			{Key: "beautify_realize", NeedsRuntime: true, MutatesWorkspace: true},
			{Key: "publish", PublishesArtifacts: true},
		}
	case TemplateFillRoute:
		return []PhaseDefinition{
			{Key: "fill_plan", NeedsRuntime: true},
			{Key: "fill_plan_confirmation", ConfirmationGate: true},
			{Key: "fill_apply", NeedsRuntime: true, MutatesWorkspace: true},
			{Key: "publish", PublishesArtifacts: true},
		}
	default:
		return []PhaseDefinition{
			{Key: "source_prepare", NeedsRuntime: true},
			{Key: "design_confirmation", ConfirmationGate: true},
			{Key: "deck_realize", NeedsRuntime: true, MutatesWorkspace: true},
			{Key: "publish", PublishesArtifacts: true},
		}
	}
}

func (s Snapshot) currentPhase() (PhaseDefinition, bool) {
	phases := phasesFor(s.Route, s.Activity)
	if s.CurrentPhaseIndex < 0 || s.CurrentPhaseIndex >= len(phases) {
		return PhaseDefinition{}, false
	}
	return phases[s.CurrentPhaseIndex], true
}

func (s *Snapshot) activeRun() (*PhaseRun, bool) {
	for i := range s.PhaseRuns {
		if s.PhaseRuns[i].ID == s.ActivePhaseRunID {
			return &s.PhaseRuns[i], true
		}
	}
	return nil, false
}

func (s *Snapshot) addEnactment(kind string, run *PhaseRun) {
	runID := ""
	if run != nil {
		runID = run.ID
	}
	for i := range s.Outbox {
		if s.Outbox[i].Kind == kind && s.Outbox[i].PhaseRunID == runID && s.Outbox[i].Status == "pending" {
			return
		}
	}
	s.Outbox = append(s.Outbox, Enactment{
		ID: s.nextID("effect"), Kind: kind, PhaseRunID: runID, Status: "pending",
	})
}

func (s *Snapshot) completeEnactments(runID string, kinds ...string) {
	set := map[string]bool{}
	for _, kind := range kinds {
		set[kind] = true
	}
	for i := range s.Outbox {
		if s.Outbox[i].PhaseRunID == runID && set[s.Outbox[i].Kind] {
			s.Outbox[i].Status = "observed"
		}
	}
}
