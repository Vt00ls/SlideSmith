package service

import (
	"testing"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

func TestStateMachineAllowsMVPPath(t *testing.T) {
	machine := NewStateMachine()
	path := []string{
		model.TaskStatusCreated,
		model.TaskStatusUploaded,
		model.TaskStatusRuntimePreparing,
		model.TaskStatusSourceConverting,
		model.TaskStatusAwaitingAnchorConfirm,
		model.TaskStatusRealizationDeriving,
		model.TaskStatusAwaitingRealizationConfirm,
		model.TaskStatusSpecGenerating,
		model.TaskStatusAwaitingSpecConfirm,
		model.TaskStatusImageAcquiring,
		model.TaskStatusSVGGenerating,
		model.TaskStatusQualityChecking,
		model.TaskStatusExporting,
		model.TaskStatusPPTXValidating,
		model.TaskStatusPublishing,
		model.TaskStatusCompleted,
	}
	for i := 0; i < len(path)-1; i++ {
		if err := machine.Validate(path[i], path[i+1]); err != nil {
			t.Fatalf("transition %s -> %s should be allowed: %v", path[i], path[i+1], err)
		}
	}
}

func TestStateMachineAllowsSpecPreviewBypass(t *testing.T) {
	machine := NewStateMachine()
	path := []string{
		model.TaskStatusAwaitingRealizationConfirm,
		model.TaskStatusSpecGenerating,
		model.TaskStatusImageAcquiring,
		model.TaskStatusSVGGenerating,
	}
	for i := 0; i < len(path)-1; i++ {
		if err := machine.Validate(path[i], path[i+1]); err != nil {
			t.Fatalf("transition %s -> %s should be allowed: %v", path[i], path[i+1], err)
		}
	}
}

func TestStateMachineRejectsSkippingConfirmation(t *testing.T) {
	machine := NewStateMachine()
	if machine.CanTransition(model.TaskStatusSourceConverting, model.TaskStatusSpecGenerating) {
		t.Fatal("source_converting should not skip awaiting_confirm")
	}
}

func TestStateMachineRejectsSkippingResourceGate(t *testing.T) {
	machine := NewStateMachine()
	for _, status := range []string{model.TaskStatusSpecGenerating, model.TaskStatusAwaitingSpecConfirm} {
		if machine.CanTransition(status, model.TaskStatusSVGGenerating) {
			t.Fatalf("%s should not skip image_acquiring", status)
		}
	}
}

func TestStateMachineAllowsLegacyConfirmationPath(t *testing.T) {
	machine := NewStateMachine()
	if err := machine.Validate(model.TaskStatusSourceConverting, model.TaskStatusAwaitingConfirm); err != nil {
		t.Fatalf("legacy awaiting_confirm should remain allowed: %v", err)
	}
	if err := machine.Validate(model.TaskStatusAwaitingConfirm, model.TaskStatusSpecGenerating); err != nil {
		t.Fatalf("legacy confirmation should still generate: %v", err)
	}
}

func TestStateMachineAllowsFailedPhaseRetries(t *testing.T) {
	machine := NewStateMachine()
	for _, status := range []string{
		model.TaskStatusRuntimePreparing,
		model.TaskStatusSpecGenerating,
		model.TaskStatusPPTXValidating,
		model.TaskStatusPublishing,
	} {
		if err := machine.Validate(model.TaskStatusFailed, status); err != nil {
			t.Fatalf("failed -> %s should be allowed: %v", status, err)
		}
	}
}

func TestStateMachineRejectsPublishBeforePPTXValidate(t *testing.T) {
	machine := NewStateMachine()
	if machine.CanTransition(model.TaskStatusExporting, model.TaskStatusPublishing) {
		t.Fatal("exporting must not skip pptx_validating")
	}
}

func TestStateMachineAllowsTemplateFillTransitions(t *testing.T) {
	machine := NewStateMachine()
	allowed := [][2]string{
		{model.TaskStatusSourceConverting, model.TaskStatusTemplateFillPlanning},
		{model.TaskStatusTemplateFillPlanning, model.TaskStatusAwaitingTemplateFillConfirm},
		{model.TaskStatusAwaitingTemplateFillConfirm, model.TaskStatusTemplateFillPlanning},
		{model.TaskStatusAwaitingTemplateFillConfirm, model.TaskStatusTemplateFillChecking},
		{model.TaskStatusTemplateFillChecking, model.TaskStatusAwaitingTemplateFillConfirm},
		{model.TaskStatusTemplateFillChecking, model.TaskStatusTemplateFillApplying},
		{model.TaskStatusTemplateFillApplying, model.TaskStatusTemplateFillValidating},
		{model.TaskStatusTemplateFillValidating, model.TaskStatusPublishing},
		{model.TaskStatusFailed, model.TaskStatusTemplateFillPlanning},
		{model.TaskStatusFailed, model.TaskStatusTemplateFillChecking},
		{model.TaskStatusFailed, model.TaskStatusTemplateFillApplying},
		{model.TaskStatusFailed, model.TaskStatusTemplateFillValidating},
	}
	for _, item := range allowed {
		if err := machine.Validate(item[0], item[1]); err != nil {
			t.Fatalf("Validate(%q, %q) error = %v", item[0], item[1], err)
		}
	}
}

func TestStateMachineRejectsMainSpecToTemplateFillApply(t *testing.T) {
	machine := NewStateMachine()
	if machine.CanTransition(model.TaskStatusSpecGenerating, model.TaskStatusTemplateFillApplying) {
		t.Fatal("spec_generating -> template_fill_applying should be rejected")
	}
}

func TestStateMachineAllowsTemplateFillCancellation(t *testing.T) {
	machine := NewStateMachine()
	for _, status := range []string{
		model.TaskStatusTemplateFillPlanning,
		model.TaskStatusAwaitingTemplateFillConfirm,
		model.TaskStatusTemplateFillChecking,
		model.TaskStatusTemplateFillApplying,
		model.TaskStatusTemplateFillValidating,
	} {
		if err := machine.Validate(status, model.TaskStatusCancelled); err != nil {
			t.Fatalf("%s -> cancelled should be allowed: %v", status, err)
		}
	}
}
