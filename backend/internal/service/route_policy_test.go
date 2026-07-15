package service

import (
	"encoding/json"
	"testing"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

func TestRouteExecutionPolicyAllowsMain(t *testing.T) {
	policy := routeExecutionPolicyFor(&routeSelection{Route: model.TaskRouteMain}, false)
	if !policy.Executable {
		t.Fatalf("main policy should be executable: %#v", policy)
	}
	policyJSON := routeExecutionPolicyJSON(t, policy)
	if policyJSON["workflow_executable"] != true {
		t.Fatalf("main workflow should be executable: %#v", policy)
	}
	if _, ok := policyJSON["unsupported_after"]; ok || policy.FailurePhase != "" || policy.NextSpec != "" {
		t.Fatalf("main policy should not carry workflow-block metadata: %#v", policy)
	}
	if policy.NextStatus != model.TaskStatusSourceConverting {
		t.Fatalf("next status = %q, want %q", policy.NextStatus, model.TaskStatusSourceConverting)
	}
	if policy.NextPhase != PhaseSourcePrepare {
		t.Fatalf("next phase = %q, want %q", policy.NextPhase, PhaseSourcePrepare)
	}
}

func TestRouteExecutionPolicyAllowsTemplateFillWorkflow(t *testing.T) {
	policy := routeExecutionPolicyFor(&routeSelection{Route: model.TaskRouteTemplateFill}, false)
	if !policy.Executable {
		t.Fatalf("template-fill should be executable: %#v", policy)
	}
	policyJSON := routeExecutionPolicyJSON(t, policy)
	if policyJSON["workflow_executable"] != true {
		t.Fatalf("template-fill workflow should be executable: %#v", policy)
	}
	if _, ok := policyJSON["unsupported_after"]; ok || policy.FailurePhase != "" || policy.NextSpec != "" {
		t.Fatalf("template-fill policy should not carry workflow-block metadata: %#v", policy)
	}
	if policy.NextStatus != model.TaskStatusSourceConverting || policy.NextPhase != PhaseSourcePrepare {
		t.Fatalf("unexpected intake transition: %#v", policy)
	}
	wantSupported := []string{model.TaskRouteMain, model.TaskRouteTemplateFill, model.TaskRouteBeautify}
	if len(policy.SupportedRoutes) != len(wantSupported) {
		t.Fatalf("supported routes = %#v, want %#v", policy.SupportedRoutes, wantSupported)
	}
	for index, want := range wantSupported {
		if policy.SupportedRoutes[index] != want {
			t.Fatalf("supported routes = %#v, want %#v", policy.SupportedRoutes, wantSupported)
		}
	}
}

func TestRouteExecutionPolicyAllowsBeautifyIntakeButBlocksWorkflowWhenDisabled(t *testing.T) {
	policy := routeExecutionPolicyFor(&routeSelection{Route: model.TaskRouteBeautify}, false)
	if !policy.Executable {
		t.Fatalf("beautify should be allowed to run source intake: %#v", policy)
	}
	policyJSON := routeExecutionPolicyJSON(t, policy)
	if policyJSON["workflow_executable"] != false {
		t.Fatalf("disabled beautify workflow should be blocked: %#v", policy)
	}
	if policy.FailurePhase != "source_prepare.workflow_not_enabled" {
		t.Fatalf("failure phase = %q, want source_prepare.workflow_not_enabled", policy.FailurePhase)
	}
	if policyJSON["unsupported_after"] != string(PhaseSourcePrepare) {
		t.Fatalf("unsupported after = %#v, want %q", policyJSON["unsupported_after"], PhaseSourcePrepare)
	}
	if policy.NextSpec != "" {
		t.Fatalf("disabled workflow must not expose stale next spec = %q", policy.NextSpec)
	}
	if policy.NextStatus != model.TaskStatusSourceConverting || policy.NextPhase != PhaseSourcePrepare {
		t.Fatalf("unexpected intake transition: %#v", policy)
	}
	if policy.FailureMessage == "" {
		t.Fatalf("beautify policy should explain the workflow block: %#v", policy)
	}
	if policy.FailureMessage != "route beautify is disabled by SLIDESMITH_BEAUTIFY_ENABLED" {
		t.Fatalf("beautify failure message = %q", policy.FailureMessage)
	}
}

func TestRouteExecutionPolicyAllowsEnabledBeautifyWorkflow(t *testing.T) {
	policy := routeExecutionPolicyFor(&routeSelection{Route: model.TaskRouteBeautify}, true)
	if !policy.Executable || !policy.WorkflowExecutable {
		t.Fatalf("enabled beautify workflow should be executable: %#v", policy)
	}
	if policy.FailurePhase != "" || policy.FailureMessage != "" || policy.UnsupportedAfter != "" || policy.NextSpec != "" {
		t.Fatalf("enabled beautify should not carry workflow-block metadata: %#v", policy)
	}
	if policy.NextStatus != model.TaskStatusSourceConverting || policy.NextPhase != PhaseSourcePrepare {
		t.Fatalf("unexpected intake transition: %#v", policy)
	}
}

func TestRouteExecutionPolicyRejectsMissingSelection(t *testing.T) {
	policy := routeExecutionPolicyFor(nil, false)
	policyJSON := routeExecutionPolicyJSON(t, policy)
	if policy.Executable || policyJSON["workflow_executable"] != false {
		t.Fatalf("missing selection should not be executable: %#v", policy)
	}
	if policy.FailurePhase != routeFailureUnsupportedRoute {
		t.Fatalf("failure phase = %q, want %q", policy.FailurePhase, routeFailureUnsupportedRoute)
	}
	if _, ok := policyJSON["unsupported_after"]; ok || policy.NextSpec != "" || policy.NextStatus != "" || policy.NextPhase != "" {
		t.Fatalf("missing selection should not have a next workflow phase: %#v", policy)
	}
}

func TestRouteExecutionPolicyRejectsUnknownRoute(t *testing.T) {
	policy := routeExecutionPolicyFor(&routeSelection{Route: "unknown"}, true)
	policyJSON := routeExecutionPolicyJSON(t, policy)
	if policy.Executable || policyJSON["workflow_executable"] != false {
		t.Fatalf("unknown route should not be executable: %#v", policy)
	}
	if policy.FailurePhase != routeFailureUnsupportedRoute {
		t.Fatalf("failure phase = %q, want %q", policy.FailurePhase, routeFailureUnsupportedRoute)
	}
	if _, ok := policyJSON["unsupported_after"]; ok || policy.NextSpec != "" || policy.NextStatus != "" || policy.NextPhase != "" {
		t.Fatalf("unknown route should not have a next workflow phase: %#v", policy)
	}
	wantSupported := []string{model.TaskRouteMain, model.TaskRouteTemplateFill, model.TaskRouteBeautify}
	if len(policy.SupportedRoutes) != len(wantSupported) {
		t.Fatalf("supported routes = %#v, want %#v", policy.SupportedRoutes, wantSupported)
	}
	for index, want := range wantSupported {
		if policy.SupportedRoutes[index] != want {
			t.Fatalf("supported routes = %#v, want %#v", policy.SupportedRoutes, wantSupported)
		}
	}
	wantKnown := []string{model.TaskRouteMain, model.TaskRouteTemplateFill, model.TaskRouteBeautify}
	if len(policy.KnownRoutes) != len(wantKnown) {
		t.Fatalf("known routes = %#v, want %#v", policy.KnownRoutes, wantKnown)
	}
	for i, want := range wantKnown {
		if policy.KnownRoutes[i] != want {
			t.Fatalf("known routes = %#v, want %#v", policy.KnownRoutes, wantKnown)
		}
	}
}

func routeExecutionPolicyJSON(t *testing.T, policy routeExecutionPolicy) map[string]any {
	t.Helper()
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}
