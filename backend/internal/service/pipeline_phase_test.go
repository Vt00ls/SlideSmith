package service

import (
	"testing"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

func TestPipelinePhaseRegistryDefinesOrderedPPTMasterFlow(t *testing.T) {
	definitions := PipelinePhaseDefinitions()
	if len(definitions) != 13 {
		t.Fatalf("expected 13 phase definitions, got %d", len(definitions))
	}
	if definitions[0].Phase != PhaseRouteSelect {
		t.Fatalf("first phase = %q, want %q", definitions[0].Phase, PhaseRouteSelect)
	}
	if definitions[len(definitions)-1].Phase != PhasePublish {
		t.Fatalf("last phase = %q, want %q", definitions[len(definitions)-1].Phase, PhasePublish)
	}
	next, ok := NextPipelinePhase(PhaseRouteSelect)
	if !ok || next != PhaseSourcePrepare {
		t.Fatalf("route_select next = %q, %v; want source_prepare, true", next, ok)
	}
	publish, ok := PipelinePhaseDefinitionFor(PhasePublish)
	if !ok {
		t.Fatal("publish phase missing from registry")
	}
	if !publish.Retryable || publish.NextStatus != model.TaskStatusCompleted {
		t.Fatalf("publish definition not retryable/completing: %#v", publish)
	}
}

func TestNormalizePipelinePhase(t *testing.T) {
	phase, err := NormalizePipelinePhase(" SVG_Execute ")
	if err != nil {
		t.Fatal(err)
	}
	if phase != PhaseSVGExecute {
		t.Fatalf("phase = %q, want %q", phase, PhaseSVGExecute)
	}
	if _, err := NormalizePipelinePhase("missing"); err == nil {
		t.Fatal("unknown phase should return an error")
	}
}
