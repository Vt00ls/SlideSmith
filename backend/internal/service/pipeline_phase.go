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
	PhasePublish            PipelinePhase = "publish"
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
	PhasePublish,
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
		OutputArtifacts:   []string{"design_spec.md", "spec_lock.md", ".slidesmith/spec_contract.json"},
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
		DisplayName:       "Image Acquire",
		RequiredStatuses:  []string{model.TaskStatusImageAcquiring},
		NextStatus:        model.TaskStatusSVGGenerating,
		Runner:            PhaseRunnerWorker,
		Retryable:         true,
		RequiredArtifacts: []string{"design_spec.md", "spec_lock.md"},
		OutputArtifacts:   []string{"images/", "analysis/image_analysis.csv"},
	},
	PhaseSVGExecute: {
		Phase:             PhaseSVGExecute,
		DisplayName:       "SVG Execute",
		RequiredStatuses:  []string{model.TaskStatusSVGGenerating},
		NextStatus:        model.TaskStatusQualityChecking,
		Runner:            PhaseRunnerAgent,
		Retryable:         true,
		RequiredArtifacts: []string{"design_spec.md", "spec_lock.md"},
		OutputArtifacts:   []string{"svg_output/*.svg", "notes/total.md"},
	},
	PhaseQualityCheck: {
		Phase:             PhaseQualityCheck,
		DisplayName:       "Quality Check",
		RequiredStatuses:  []string{model.TaskStatusQualityChecking},
		NextStatus:        model.TaskStatusExporting,
		Runner:            PhaseRunnerWorker,
		Retryable:         true,
		RequiredArtifacts: []string{"svg_output/*.svg"},
		OutputArtifacts:   []string{".slidesmith/quality_report.json"},
	},
	PhaseFinalizeExport: {
		Phase:             PhaseFinalizeExport,
		DisplayName:       "Finalize Export",
		RequiredStatuses:  []string{model.TaskStatusExporting},
		NextStatus:        model.TaskStatusPublishing,
		Runner:            PhaseRunnerWorker,
		Retryable:         true,
		RequiredArtifacts: []string{"svg_output/*.svg"},
		OutputArtifacts:   []string{"svg_final/*.svg", "exports/*.pptx"},
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
	definitions := make([]PipelinePhaseDefinition, 0, len(pipelinePhaseOrder))
	for _, phase := range pipelinePhaseOrder {
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
	for i, current := range pipelinePhaseOrder {
		if current == phase && i+1 < len(pipelinePhaseOrder) {
			return pipelinePhaseOrder[i+1], true
		}
	}
	return "", false
}
