package service

import (
	"encoding/json"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

const (
	routeFailureUnsupportedWorkflow = "route_select.unsupported_workflow"
	routeFailureUnsupportedRoute    = "route_select.unsupported_route"
	routeFailureWorkflowNotEnabled  = "source_prepare.workflow_not_enabled"
)

type routeExecutionPolicy struct {
	Route              string        `json:"route"`
	Executable         bool          `json:"executable"`
	WorkflowExecutable bool          `json:"workflow_executable"`
	FailurePhase       string        `json:"failure_phase,omitempty"`
	FailureMessage     string        `json:"failure_message,omitempty"`
	UnsupportedAfter   PipelinePhase `json:"unsupported_after,omitempty"`
	NextSpec           string        `json:"next_spec,omitempty"`
	SupportedRoutes    []string      `json:"supported_routes"`
	KnownRoutes        []string      `json:"known_routes"`
	NextStatus         string        `json:"next_status,omitempty"`
	NextPhase          PipelinePhase `json:"next_phase,omitempty"`
}

func (s *TaskService) beautifyCapabilitySnapshot(task *model.Task) routeCapabilitySnapshot {
	fallback := routeCapabilitySnapshot{
		Captured:                          true,
		BeautifyEnabled:                   s.agentCfg.BeautifyEnabled,
		BeautifyFidelityStrict:            s.agentCfg.BeautifyFidelityStrict,
		BeautifySourceSVGReferenceEnabled: s.agentCfg.BeautifySourceSVGReferenceEnabled,
	}
	if task == nil || task.RouteSelectionJSON == "" {
		return fallback
	}
	var selection routeSelection
	if json.Unmarshal([]byte(task.RouteSelectionJSON), &selection) == nil && selection.CapabilitySnapshot.Captured {
		return selection.CapabilitySnapshot
	}
	return fallback
}

func routeExecutionPolicyFor(selection *routeSelection, beautifyEnabled bool) routeExecutionPolicy {
	knownRoutes := []string{
		model.TaskRouteMain,
		model.TaskRouteTemplateFill,
		model.TaskRouteBeautify,
	}
	supportedRoutes := []string{model.TaskRouteMain, model.TaskRouteTemplateFill, model.TaskRouteBeautify}
	if selection == nil {
		return routeExecutionPolicy{
			Route:           "",
			Executable:      false,
			FailurePhase:    routeFailureUnsupportedRoute,
			FailureMessage:  "route selection is missing",
			SupportedRoutes: supportedRoutes,
			KnownRoutes:     knownRoutes,
		}
	}
	switch selection.Route {
	case model.TaskRouteMain:
		return routeExecutionPolicy{
			Route:              model.TaskRouteMain,
			Executable:         true,
			WorkflowExecutable: true,
			SupportedRoutes:    supportedRoutes,
			KnownRoutes:        knownRoutes,
			NextStatus:         model.TaskStatusSourceConverting,
			NextPhase:          PhaseSourcePrepare,
		}
	case model.TaskRouteTemplateFill:
		return routeExecutionPolicy{
			Route:              model.TaskRouteTemplateFill,
			Executable:         true,
			WorkflowExecutable: true,
			SupportedRoutes:    supportedRoutes,
			KnownRoutes:        knownRoutes,
			NextStatus:         model.TaskStatusSourceConverting,
			NextPhase:          PhaseSourcePrepare,
		}
	case model.TaskRouteBeautify:
		if selection.CapabilitySnapshot.Captured {
			beautifyEnabled = selection.CapabilitySnapshot.BeautifyEnabled
		}
		if !beautifyEnabled {
			return routeExecutionPolicy{
				Route:              model.TaskRouteBeautify,
				Executable:         true,
				WorkflowExecutable: false,
				FailurePhase:       routeFailureWorkflowNotEnabled,
				FailureMessage:     "route beautify is disabled by SLIDESMITH_BEAUTIFY_ENABLED",
				UnsupportedAfter:   PhaseSourcePrepare,
				SupportedRoutes:    supportedRoutes,
				KnownRoutes:        knownRoutes,
				NextStatus:         model.TaskStatusSourceConverting,
				NextPhase:          PhaseSourcePrepare,
			}
		}
		return routeExecutionPolicy{
			Route:              model.TaskRouteBeautify,
			Executable:         true,
			WorkflowExecutable: true,
			SupportedRoutes:    supportedRoutes,
			KnownRoutes:        knownRoutes,
			NextStatus:         model.TaskStatusSourceConverting,
			NextPhase:          PhaseSourcePrepare,
		}
	default:
		return routeExecutionPolicy{
			Route:           selection.Route,
			Executable:      false,
			FailurePhase:    routeFailureUnsupportedRoute,
			FailureMessage:  "route " + selection.Route + " is not recognized by SlideSmith",
			SupportedRoutes: supportedRoutes,
			KnownRoutes:     knownRoutes,
		}
	}
}
