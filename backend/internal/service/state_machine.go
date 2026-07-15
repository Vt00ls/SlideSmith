package service

import (
	"fmt"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

type StateMachine struct {
	transitions map[string]map[string]bool
}

func NewStateMachine() *StateMachine {
	allow := func(values ...string) map[string]bool {
		out := make(map[string]bool, len(values))
		for _, value := range values {
			out[value] = true
		}
		return out
	}
	return &StateMachine{transitions: map[string]map[string]bool{
		model.TaskStatusCreated: allow(
			model.TaskStatusUploaded,
			model.TaskStatusCancelled,
			model.TaskStatusFailed,
		),
		model.TaskStatusUploaded: allow(
			model.TaskStatusRuntimePreparing,
			model.TaskStatusCancelled,
			model.TaskStatusFailed,
		),
		model.TaskStatusRuntimePreparing: allow(
			model.TaskStatusSourceConverting,
			model.TaskStatusCancelled,
			model.TaskStatusFailed,
		),
		model.TaskStatusSourceConverting: allow(
			model.TaskStatusAwaitingConfirm,
			model.TaskStatusAwaitingAnchorConfirm,
			model.TaskStatusBeautifyInventoryBuilding,
			model.TaskStatusTemplateFillPlanning,
			model.TaskStatusCancelled,
			model.TaskStatusFailed,
		),
		model.TaskStatusBeautifyInventoryBuilding: allow(
			model.TaskStatusAwaitingAnchorConfirm,
			model.TaskStatusCancelled,
			model.TaskStatusFailed,
		),
		model.TaskStatusTemplateFillPlanning: allow(
			model.TaskStatusAwaitingTemplateFillConfirm,
			model.TaskStatusCancelled,
			model.TaskStatusFailed,
		),
		model.TaskStatusAwaitingTemplateFillConfirm: allow(
			model.TaskStatusTemplateFillPlanning,
			model.TaskStatusTemplateFillChecking,
			model.TaskStatusCancelled,
			model.TaskStatusFailed,
		),
		model.TaskStatusTemplateFillChecking: allow(
			model.TaskStatusAwaitingTemplateFillConfirm,
			model.TaskStatusTemplateFillApplying,
			model.TaskStatusCancelled,
			model.TaskStatusFailed,
		),
		model.TaskStatusTemplateFillApplying: allow(
			model.TaskStatusTemplateFillValidating,
			model.TaskStatusCancelled,
			model.TaskStatusFailed,
		),
		model.TaskStatusTemplateFillValidating: allow(
			model.TaskStatusPublishing,
			model.TaskStatusCancelled,
			model.TaskStatusFailed,
		),
		model.TaskStatusAwaitingConfirm: allow(
			model.TaskStatusSpecGenerating,
			model.TaskStatusCancelled,
			model.TaskStatusFailed,
		),
		model.TaskStatusAwaitingAnchorConfirm: allow(
			model.TaskStatusRealizationDeriving,
			model.TaskStatusCancelled,
			model.TaskStatusFailed,
		),
		model.TaskStatusRealizationDeriving: allow(
			model.TaskStatusAwaitingRealizationConfirm,
			model.TaskStatusCancelled,
			model.TaskStatusFailed,
		),
		model.TaskStatusAwaitingRealizationConfirm: allow(
			model.TaskStatusSpecGenerating,
			model.TaskStatusBeautifyPlanning,
			model.TaskStatusCancelled,
			model.TaskStatusFailed,
		),
		model.TaskStatusBeautifyPlanning: allow(
			model.TaskStatusAwaitingBeautifyConfirm,
			model.TaskStatusCancelled,
			model.TaskStatusFailed,
		),
		model.TaskStatusAwaitingBeautifyConfirm: allow(
			model.TaskStatusBeautifyPlanning,
			model.TaskStatusSpecGenerating,
			model.TaskStatusCancelled,
			model.TaskStatusFailed,
		),
		model.TaskStatusSpecGenerating: allow(
			model.TaskStatusAwaitingSpecConfirm,
			model.TaskStatusImageAcquiring,
			model.TaskStatusCancelled,
			model.TaskStatusFailed,
		),
		model.TaskStatusAwaitingSpecConfirm: allow(
			model.TaskStatusSpecGenerating,
			model.TaskStatusImageAcquiring,
			model.TaskStatusCancelled,
			model.TaskStatusFailed,
		),
		model.TaskStatusImageAcquiring: allow(
			model.TaskStatusSVGGenerating,
			model.TaskStatusCancelled,
			model.TaskStatusFailed,
		),
		model.TaskStatusSVGGenerating: allow(
			model.TaskStatusQualityChecking,
			model.TaskStatusCancelled,
			model.TaskStatusFailed,
		),
		model.TaskStatusQualityChecking: allow(
			model.TaskStatusExporting,
			model.TaskStatusCancelled,
			model.TaskStatusFailed,
		),
		model.TaskStatusExporting: allow(
			model.TaskStatusPPTXValidating,
			model.TaskStatusCancelled,
			model.TaskStatusFailed,
		),
		model.TaskStatusPPTXValidating: allow(
			model.TaskStatusPublishing,
			model.TaskStatusCancelled,
			model.TaskStatusFailed,
		),
		model.TaskStatusPublishing: allow(
			model.TaskStatusCompleted,
			model.TaskStatusCancelled,
			model.TaskStatusFailed,
		),
		model.TaskStatusFailed: allow(
			model.TaskStatusRuntimePreparing,
			model.TaskStatusBeautifyInventoryBuilding,
			model.TaskStatusBeautifyPlanning,
			model.TaskStatusTemplateFillPlanning,
			model.TaskStatusTemplateFillChecking,
			model.TaskStatusTemplateFillApplying,
			model.TaskStatusTemplateFillValidating,
			model.TaskStatusSpecGenerating,
			model.TaskStatusImageAcquiring,
			model.TaskStatusSVGGenerating,
			model.TaskStatusQualityChecking,
			model.TaskStatusExporting,
			model.TaskStatusPPTXValidating,
			model.TaskStatusPublishing,
			model.TaskStatusCancelled,
		),
	}}
}

func (m *StateMachine) CanTransition(from, to string) bool {
	if from == to {
		return true
	}
	return m.transitions[from][to]
}

func (m *StateMachine) Validate(from, to string) error {
	if m.CanTransition(from, to) {
		return nil
	}
	return fmt.Errorf("invalid task status transition %q -> %q", from, to)
}
