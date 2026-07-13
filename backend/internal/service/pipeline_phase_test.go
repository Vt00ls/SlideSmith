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

func TestTemplateFillPhaseDefinitions(t *testing.T) {
	tests := []struct {
		phase      PipelinePhase
		nextStatus string
		runner     string
	}{
		{PhaseTemplateFillPlan, model.TaskStatusAwaitingTemplateFillConfirm, PhaseRunnerAgent},
		{PhaseTemplateFillCheck, model.TaskStatusTemplateFillApplying, PhaseRunnerWorker},
		{PhaseTemplateFillApply, model.TaskStatusTemplateFillValidating, PhaseRunnerWorker},
		{PhaseTemplateFillValidate, model.TaskStatusPublishing, PhaseRunnerWorker},
	}
	for _, test := range tests {
		definition, ok := PipelinePhaseDefinitionFor(test.phase)
		if !ok {
			t.Fatalf("phase %s missing", test.phase)
		}
		if definition.NextStatus != test.nextStatus || definition.Runner != test.runner {
			t.Fatalf("phase %s = %#v", test.phase, definition)
		}
		if !definition.Retryable {
			t.Fatalf("phase %s should be retryable", test.phase)
		}
	}
}

func TestTemplateFillPhaseOrderingIsRouteAware(t *testing.T) {
	want := []PipelinePhase{
		PhaseRouteSelect,
		PhaseSourcePrepare,
		PhaseTemplateFillPlan,
		PhaseTemplateFillCheck,
		PhaseTemplateFillApply,
		PhaseTemplateFillValidate,
		PhasePublish,
	}
	definitions := PipelinePhaseDefinitionsForRoute(model.TaskRouteTemplateFill)
	if len(definitions) != len(want) {
		t.Fatalf("template-fill definitions = %d, want %d", len(definitions), len(want))
	}
	for i, phase := range want {
		if definitions[i].Phase != phase {
			t.Fatalf("template-fill definition[%d] = %q, want %q", i, definitions[i].Phase, phase)
		}
	}

	next, ok := NextPipelinePhaseForRoute(model.TaskRouteTemplateFill, PhaseSourcePrepare)
	if !ok || next != PhaseTemplateFillPlan {
		t.Fatalf("template-fill source_prepare next = %q, %v; want template_fill_plan, true", next, ok)
	}
	mainNext, ok := NextPipelinePhase(PhaseSourcePrepare)
	if !ok || mainNext != PhaseProjectInit {
		t.Fatalf("main source_prepare next = %q, %v; want project_init, true", mainNext, ok)
	}
}

func TestTemplateFillArtifactKindConstants(t *testing.T) {
	tests := map[string]string{
		"plan":            model.ArtifactKindTemplateFillPlan,
		"check_report":    model.ArtifactKindTemplateFillCheckReport,
		"validate_report": model.ArtifactKindTemplateFillValidateReport,
		"readback":        model.ArtifactKindTemplateFillReadback,
	}
	want := map[string]string{
		"plan":            "template_fill_plan",
		"check_report":    "template_fill_check_report",
		"validate_report": "template_fill_validate_report",
		"readback":        "template_fill_readback",
	}
	for name, value := range tests {
		if value != want[name] {
			t.Fatalf("%s artifact kind = %q, want %q", name, value, want[name])
		}
	}
}
