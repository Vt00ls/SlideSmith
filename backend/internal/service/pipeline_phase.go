package service

import (
	"fmt"
	"strings"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

type PipelinePhase string

const (
	PhaseRouteSelect        PipelinePhase = "route_select"
	PhaseSourcePrepare      PipelinePhase = "source_prepare"
	PhaseProjectInit        PipelinePhase = "project_init"
	PhaseTemplateResolve    PipelinePhase = "template_resolve"
	PhaseAnchorConfirm      PipelinePhase = "anchor_confirm"
	PhaseRealizationConfirm PipelinePhase = "realization_confirm"
	PhaseSpecGenerate       PipelinePhase = "spec_generate"
	PhaseSpecRefine         PipelinePhase = "spec_refine"
	PhaseImageAcquire       PipelinePhase = "image_acquire"
	PhaseSVGExecute         PipelinePhase = "svg_execute"
	PhaseQualityCheck       PipelinePhase = "quality_check"
	PhaseFinalizeExport     PipelinePhase = "finalize_export"
	PhasePPTXValidate       PipelinePhase = "pptx_validate"
	PhasePublish            PipelinePhase = "publish"
)

const (
	PhaseTemplateFillPlan     PipelinePhase = "template_fill_plan"
	PhaseTemplateFillCheck    PipelinePhase = "template_fill_check"
	PhaseTemplateFillApply    PipelinePhase = "template_fill_apply"
	PhaseTemplateFillValidate PipelinePhase = "template_fill_validate"
)

const (
	PhaseRunnerRule              = "rule"
	PhaseRunnerWorker            = "worker"
	PhaseRunnerAgent             = "agent"
	PhaseRunnerLegacyAgentBundle = "legacy-agent-bundle"
	PhaseRunnerPublisher         = "publisher"
)

type PipelinePhaseDefinition struct {
	Phase             PipelinePhase `json:"phase"`
	DisplayName       string        `json:"display_name"`
	RequiredStatuses  []string      `json:"required_statuses"`
	NextStatus        string        `json:"next_status"`
	Runner            string        `json:"runner"`
	Retryable         bool          `json:"retryable"`
	BlockingUserGate  bool          `json:"blocking_user_gate"`
	RequiredArtifacts []string      `json:"required_artifacts"`
	OutputArtifacts   []string      `json:"output_artifacts"`
}

var pipelinePhaseOrder = []PipelinePhase{
	PhaseRouteSelect,
	PhaseSourcePrepare,
	PhaseProjectInit,
	PhaseTemplateResolve,
	PhaseAnchorConfirm,
	PhaseRealizationConfirm,
	PhaseSpecGenerate,
	PhaseSpecRefine,
	PhaseImageAcquire,
	PhaseSVGExecute,
	PhaseQualityCheck,
	PhaseFinalizeExport,
	PhasePPTXValidate,
	PhasePublish,
}

var pipelinePhaseOrderByRoute = map[string][]PipelinePhase{
	model.TaskRouteMain: pipelinePhaseOrder,
	model.TaskRouteTemplateFill: {
		PhaseRouteSelect,
		PhaseSourcePrepare,
		PhaseTemplateFillPlan,
		PhaseTemplateFillCheck,
		PhaseTemplateFillApply,
		PhaseTemplateFillValidate,
		PhasePublish,
	},
}

var pipelinePhaseRegistry = map[PipelinePhase]PipelinePhaseDefinition{
	PhaseRouteSelect: {
		Phase:             PhaseRouteSelect,
		DisplayName:       "Route Select",
		RequiredStatuses:  []string{model.TaskStatusRuntimePreparing, model.TaskStatusSourceConverting},
		NextStatus:        model.TaskStatusSourceConverting,
		Runner:            PhaseRunnerRule,
		Retryable:         true,
		RequiredArtifacts: []string{model.ArtifactKindSource},
		OutputArtifacts:   []string{".slidesmith/route.json"},
	},
	PhaseSourcePrepare: {
		Phase:             PhaseSourcePrepare,
		DisplayName:       "Source Prepare",
		RequiredStatuses:  []string{model.TaskStatusSourceConverting},
		NextStatus:        model.TaskStatusAwaitingAnchorConfirm,
		Runner:            PhaseRunnerAgent,
		Retryable:         true,
		RequiredArtifacts: []string{model.ArtifactKindSource},
		OutputArtifacts:   []string{"normalized markdown", "source metadata"},
	},
	PhaseTemplateFillPlan: {
		Phase:             PhaseTemplateFillPlan,
		DisplayName:       "Template Fill Plan",
		RequiredStatuses:  []string{model.TaskStatusTemplateFillPlanning},
		NextStatus:        model.TaskStatusAwaitingTemplateFillConfirm,
		Runner:            PhaseRunnerAgent,
		Retryable:         true,
		RequiredArtifacts: []string{model.ArtifactKindPPTXSlideLibrary, model.ArtifactKindSourceMarkdown},
		OutputArtifacts:   []string{"analysis/fill_plan.json", "analysis/check_report.json"},
	},
	PhaseTemplateFillCheck: {
		Phase:            PhaseTemplateFillCheck,
		DisplayName:      "Template Fill Check",
		RequiredStatuses: []string{model.TaskStatusTemplateFillChecking},
		NextStatus:       model.TaskStatusTemplateFillApplying,
		Runner:           PhaseRunnerWorker,
		Retryable:        true,
		BlockingUserGate: true,
		OutputArtifacts:  []string{"analysis/check_report.json"},
	},
	PhaseTemplateFillApply: {
		Phase:             PhaseTemplateFillApply,
		DisplayName:       "Template Fill Apply",
		RequiredStatuses:  []string{model.TaskStatusTemplateFillApplying},
		NextStatus:        model.TaskStatusTemplateFillValidating,
		Runner:            PhaseRunnerWorker,
		Retryable:         true,
		RequiredArtifacts: []string{"analysis/fill_plan.json", "analysis/check_report.json"},
		OutputArtifacts:   []string{"exports/*.pptx"},
	},
	PhaseTemplateFillValidate: {
		Phase:             PhaseTemplateFillValidate,
		DisplayName:       "Template Fill Validate",
		RequiredStatuses:  []string{model.TaskStatusTemplateFillValidating},
		NextStatus:        model.TaskStatusPublishing,
		Runner:            PhaseRunnerWorker,
		Retryable:         true,
		RequiredArtifacts: []string{"exports/*.pptx"},
		OutputArtifacts:   []string{"validation/readback.md", "validation/validate_report.json"},
	},
	PhaseProjectInit: {
		Phase:            PhaseProjectInit,
		DisplayName:      "Project Init",
		RequiredStatuses: []string{model.TaskStatusSourceConverting},
		NextStatus:       model.TaskStatusAwaitingAnchorConfirm,
		Runner:           PhaseRunnerWorker,
		Retryable:        true,
		OutputArtifacts:  []string{"projects/<runtime_project>_ppt169_<date>/", ".slidesmith/runtime_manifest.json", ".slidesmith/skill_lock.json"},
	},
	PhaseTemplateResolve: {
		Phase:            PhaseTemplateResolve,
		DisplayName:      "Template Resolve",
		RequiredStatuses: []string{model.TaskStatusSourceConverting},
		NextStatus:       model.TaskStatusAwaitingAnchorConfirm,
		Runner:           PhaseRunnerRule,
		Retryable:        true,
		OutputArtifacts:  []string{".slidesmith/template_resolution.json"},
	},
	PhaseAnchorConfirm: {
		Phase:            PhaseAnchorConfirm,
		DisplayName:      "Anchor Confirm",
		RequiredStatuses: []string{model.TaskStatusAwaitingAnchorConfirm},
		NextStatus:       model.TaskStatusRealizationDeriving,
		Runner:           PhaseRunnerAgent,
		BlockingUserGate: true,
		OutputArtifacts:  []string{"confirm_ui/recommendations.json", "confirm_ui/result.json"},
	},
	PhaseRealizationConfirm: {
		Phase:            PhaseRealizationConfirm,
		DisplayName:      "Realization Confirm",
		RequiredStatuses: []string{model.TaskStatusAwaitingRealizationConfirm},
		NextStatus:       model.TaskStatusSpecGenerating,
		Runner:           PhaseRunnerAgent,
		BlockingUserGate: true,
		OutputArtifacts:  []string{"confirm_ui/recommendations.json", "confirm_ui/result.json"},
	},
	PhaseSpecGenerate: {
		Phase:             PhaseSpecGenerate,
		DisplayName:       "Spec Generate",
		RequiredStatuses:  []string{model.TaskStatusSpecGenerating},
		NextStatus:        model.TaskStatusAwaitingSpecConfirm,
		Runner:            PhaseRunnerAgent,
		Retryable:         true,
		RequiredArtifacts: []string{"confirm_ui/result.json"},
		OutputArtifacts:   []string{"design_spec.md", "spec_lock.md", ".slidesmith/resource_plan.json", ".slidesmith/spec_contract.json"},
	},
	PhaseSpecRefine: {
		Phase:             PhaseSpecRefine,
		DisplayName:       "Spec Refine",
		RequiredStatuses:  []string{model.TaskStatusSpecGenerating},
		NextStatus:        model.TaskStatusImageAcquiring,
		Runner:            PhaseRunnerAgent,
		Retryable:         true,
		BlockingUserGate:  true,
		RequiredArtifacts: []string{"design_spec.md", "spec_lock.md"},
		OutputArtifacts:   []string{"design_spec.md", "spec_lock.md"},
	},
	PhaseImageAcquire: {
		Phase:             PhaseImageAcquire,
		DisplayName:       "Resource Acquire",
		RequiredStatuses:  []string{model.TaskStatusImageAcquiring},
		NextStatus:        model.TaskStatusSVGGenerating,
		Runner:            PhaseRunnerWorker,
		Retryable:         true,
		RequiredArtifacts: []string{"design_spec.md", "spec_lock.md", "confirm_ui/result.json", ".slidesmith/resource_plan.json"},
		OutputArtifacts:   []string{".slidesmith/resource_policy.json", "analysis/resource_requirements.json", ".slidesmith/resources_manifest.json", ".slidesmith/contracts/image_acquire.json", "images/", "icons/", "charts/", "analysis/image_analysis.csv"},
	},
	PhaseSVGExecute: {
		Phase:             PhaseSVGExecute,
		DisplayName:       "SVG Execute",
		RequiredStatuses:  []string{model.TaskStatusSVGGenerating},
		NextStatus:        model.TaskStatusQualityChecking,
		Runner:            PhaseRunnerAgent,
		Retryable:         true,
		RequiredArtifacts: []string{"design_spec.md", "spec_lock.md", ".slidesmith/resources_manifest.json", ".slidesmith/contracts/image_acquire.json"},
		OutputArtifacts: []string{
			"svg_output/*.svg", "notes/total.md", "analysis/svg_inventory.json",
			"analysis/svg_resource_usage.json", "analysis/chart_usage.json", "analysis/notes_inventory.json",
			".slidesmith/contracts/svg_execute.json",
		},
	},
	PhaseQualityCheck: {
		Phase:            PhaseQualityCheck,
		DisplayName:      "Quality Check",
		RequiredStatuses: []string{model.TaskStatusQualityChecking},
		NextStatus:       model.TaskStatusExporting,
		Runner:           PhaseRunnerWorker,
		Retryable:        true,
		RequiredArtifacts: []string{
			"svg_output/*.svg", "analysis/svg_inventory.json", "analysis/svg_resource_usage.json",
			"analysis/chart_usage.json", "analysis/notes_inventory.json", ".slidesmith/contracts/svg_execute.json",
		},
		OutputArtifacts: []string{
			"validation/svg_quality_report.json", "validation/chart_verify_report.json",
			"validation/quality_summary.json", ".slidesmith/quality_report.json",
			".slidesmith/contracts/quality_check.json",
		},
	},
	PhaseFinalizeExport: {
		Phase:             PhaseFinalizeExport,
		DisplayName:       "Finalize Export",
		RequiredStatuses:  []string{model.TaskStatusExporting},
		NextStatus:        model.TaskStatusPPTXValidating,
		Runner:            PhaseRunnerWorker,
		Retryable:         true,
		RequiredArtifacts: []string{"svg_output/*.svg", ".slidesmith/contracts/quality_check.json"},
		OutputArtifacts:   []string{"svg_final/*.svg", "exports/*.pptx", "exports/export_manifest.json", ".slidesmith/contracts/finalize_export.json"},
	},
	PhasePPTXValidate: {
		Phase:             PhasePPTXValidate,
		DisplayName:       "PPTX Validate",
		RequiredStatuses:  []string{model.TaskStatusPPTXValidating},
		NextStatus:        model.TaskStatusPublishing,
		Runner:            PhaseRunnerWorker,
		Retryable:         true,
		RequiredArtifacts: []string{"exports/*.pptx", ".slidesmith/contracts/finalize_export.json", "validation/quality_summary.json"},
		OutputArtifacts: []string{
			"validation/pptx_readback.md", "validation/pptx_text_inventory.json",
			"validation/pptx_validate_report.json", "validation/render/*.png",
			"validation/render/contact_sheet.png", ".slidesmith/contracts/pptx_validate.json",
		},
	},
	PhasePublish: {
		Phase:             PhasePublish,
		DisplayName:       "Publish",
		RequiredStatuses:  []string{model.TaskStatusPublishing},
		NextStatus:        model.TaskStatusCompleted,
		Runner:            PhaseRunnerPublisher,
		Retryable:         true,
		RequiredArtifacts: []string{"exports/*.pptx"},
		OutputArtifacts:   []string{".slidesmith/artifacts.json", "platform artifacts"},
	},
}

func PipelinePhaseDefinitions() []PipelinePhaseDefinition {
	return pipelinePhaseDefinitionsForOrder(pipelinePhaseOrder)
}

func PipelinePhaseDefinitionsForRoute(route string) []PipelinePhaseDefinition {
	return pipelinePhaseDefinitionsForOrder(pipelinePhaseOrderByRoute[route])
}

func pipelinePhaseDefinitionsForOrder(order []PipelinePhase) []PipelinePhaseDefinition {
	definitions := make([]PipelinePhaseDefinition, 0, len(order))
	for _, phase := range order {
		definitions = append(definitions, pipelinePhaseRegistry[phase])
	}
	return definitions
}

func PipelinePhaseDefinitionFor(phase PipelinePhase) (PipelinePhaseDefinition, bool) {
	definition, ok := pipelinePhaseRegistry[phase]
	return definition, ok
}

func NormalizePipelinePhase(value string) (PipelinePhase, error) {
	phase := PipelinePhase(strings.ToLower(strings.TrimSpace(value)))
	if _, ok := pipelinePhaseRegistry[phase]; !ok {
		return "", fmt.Errorf("unknown pipeline phase %q", value)
	}
	return phase, nil
}

func NextPipelinePhase(phase PipelinePhase) (PipelinePhase, bool) {
	return nextPipelinePhaseInOrder(pipelinePhaseOrder, phase)
}

func NextPipelinePhaseForRoute(route string, phase PipelinePhase) (PipelinePhase, bool) {
	return nextPipelinePhaseInOrder(pipelinePhaseOrderByRoute[route], phase)
}

func nextPipelinePhaseInOrder(order []PipelinePhase, phase PipelinePhase) (PipelinePhase, bool) {
	for i, current := range order {
		if current == phase && i+1 < len(order) {
			return order[i+1], true
		}
	}
	return "", false
}
