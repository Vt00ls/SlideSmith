package service

import "github.com/slidesmith/slidesmith/backend/internal/model"

const (
	routeFailureUnsupportedWorkflow = "route_select.unsupported_workflow"
	routeFailureUnsupportedRoute    = "route_select.unsupported_route"
)

type routeExecutionPolicy struct {
	Route           string        `json:"route"`
	Executable      bool          `json:"executable"`
	FailurePhase    string        `json:"failure_phase,omitempty"`
	FailureMessage  string        `json:"failure_message,omitempty"`
	NextSpec        string        `json:"next_spec,omitempty"`
	SupportedRoutes []string      `json:"supported_routes"`
	KnownRoutes     []string      `json:"known_routes"`
	NextStatus      string        `json:"next_status,omitempty"`
	NextPhase       PipelinePhase `json:"next_phase,omitempty"`
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
			Route:           model.TaskRouteMain,
			Executable:      true,
			SupportedRoutes: supportedRoutes,
			KnownRoutes:     knownRoutes,
			NextStatus:      model.TaskStatusSourceConverting,
			NextPhase:       PhaseSourcePrepare,
		}
	case model.TaskRouteTemplateFill:
		return routeExecutionPolicy{
			Route:           model.TaskRouteTemplateFill,
			Executable:      false,
			FailurePhase:    routeFailureUnsupportedWorkflow,
			FailureMessage:  "route template-fill is recognized but execution workflow is not enabled in SPEC-01",
			NextSpec:        "SPEC-03-Template-Fill-PPTX.md",
			SupportedRoutes: supportedRoutes,
			KnownRoutes:     knownRoutes,
		}
	case model.TaskRouteBeautify:
		return routeExecutionPolicy{
			Route:           model.TaskRouteBeautify,
			Executable:      false,
			FailurePhase:    routeFailureUnsupportedWorkflow,
			FailureMessage:  "route beautify is recognized but execution workflow is not enabled in SPEC-01",
			NextSpec:        "SPEC-04-Beautify-PPTX.md",
			SupportedRoutes: supportedRoutes,
			KnownRoutes:     knownRoutes,
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
