package service

import (
	"testing"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

func TestRouteExecutionPolicyAllowsMain(t *testing.T) {
	policy := routeExecutionPolicyFor(&routeSelection{Route: model.TaskRouteMain})
	if !policy.Executable {
		t.Fatalf("main policy should be executable: %#v", policy)
	}
	if policy.NextPhase != PhaseSourcePrepare {
		t.Fatalf("next phase = %q, want %q", policy.NextPhase, PhaseSourcePrepare)
	}
}

func TestRouteExecutionPolicyBlocksTemplateFill(t *testing.T) {
	policy := routeExecutionPolicyFor(&routeSelection{Route: model.TaskRouteTemplateFill})
	if policy.Executable {
		t.Fatalf("template-fill policy should be blocked: %#v", policy)
	}
	if policy.FailurePhase != routeFailureUnsupportedWorkflow {
		t.Fatalf("failure phase = %q, want %q", policy.FailurePhase, routeFailureUnsupportedWorkflow)
	}
	if policy.NextSpec != "SPEC-03-Template-Fill-PPTX.md" {
		t.Fatalf("next spec = %q", policy.NextSpec)
	}
}

func TestRouteExecutionPolicyBlocksBeautify(t *testing.T) {
	policy := routeExecutionPolicyFor(&routeSelection{Route: model.TaskRouteBeautify})
	if policy.Executable {
		t.Fatalf("beautify policy should be blocked: %#v", policy)
	}
	if policy.FailurePhase != routeFailureUnsupportedWorkflow {
		t.Fatalf("failure phase = %q, want %q", policy.FailurePhase, routeFailureUnsupportedWorkflow)
	}
	if policy.NextSpec != "SPEC-04-Beautify-PPTX.md" {
		t.Fatalf("next spec = %q", policy.NextSpec)
	}
}
