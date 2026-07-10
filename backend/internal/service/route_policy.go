package service

import "github.com/slidesmith/slidesmith/backend/internal/model"

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

func routeExecutionPolicyFor(selection *routeSelection) routeExecutionPolicy {
	knownRoutes := []string{
		model.TaskRouteMain,
		model.TaskRouteTemplateFill,
		model.TaskRouteBeautify,
	}
	supportedRoutes := []string{model.TaskRouteMain}
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
			WorkflowExecutable: false,
			FailurePhase:       routeFailureWorkflowNotEnabled,
			FailureMessage:     "route template-fill source intake is complete, but the full workflow is not enabled in SPEC-02",
			UnsupportedAfter:   PhaseSourcePrepare,
			NextSpec:           "SPEC-03-Template-Fill-PPTX.md",
			SupportedRoutes:    supportedRoutes,
			KnownRoutes:        knownRoutes,
			NextStatus:         model.TaskStatusSourceConverting,
			NextPhase:          PhaseSourcePrepare,
		}
	case model.TaskRouteBeautify:
		return routeExecutionPolicy{
			Route:              model.TaskRouteBeautify,
			Executable:         true,
			WorkflowExecutable: false,
			FailurePhase:       routeFailureWorkflowNotEnabled,
			FailureMessage:     "route beautify source intake is complete, but the full workflow is not enabled in SPEC-02",
			UnsupportedAfter:   PhaseSourcePrepare,
			NextSpec:           "SPEC-04-Beautify-PPTX.md",
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
