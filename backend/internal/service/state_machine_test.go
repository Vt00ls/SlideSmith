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
		model.TaskStatusSVGGenerating,
		model.TaskStatusQualityChecking,
		model.TaskStatusExporting,
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
		model.TaskStatusPublishing,
	} {
		if err := machine.Validate(model.TaskStatusFailed, status); err != nil {
			t.Fatalf("failed -> %s should be allowed: %v", status, err)
		}
	}
}
