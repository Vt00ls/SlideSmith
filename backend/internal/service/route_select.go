package service

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

const (
	routeMain         = model.TaskRouteMain
	routeBeautify     = model.TaskRouteBeautify
	routeTemplateFill = model.TaskRouteTemplateFill
)

type routeSelection struct {
	Route              string                  `json:"route"`
	Reason             string                  `json:"reason"`
	Confidence         float64                 `json:"confidence"`
	StandaloneWorkflow string                  `json:"standalone_workflow"`
	CreatedAt          string                  `json:"created_at"`
	SourceArtifacts    []routeSourceArtifact   `json:"source_artifacts"`
	CapabilitySnapshot routeCapabilitySnapshot `json:"capability_snapshot"`
}

type routeCapabilitySnapshot struct {
	Captured                          bool `json:"captured"`
	BeautifyEnabled                   bool `json:"beautify_enabled"`
	BeautifyFidelityStrict            bool `json:"beautify_fidelity_strict"`
	BeautifySourceSVGReferenceEnabled bool `json:"beautify_source_svg_reference_enabled"`
}

type routeSourceArtifact struct {
	Name      string `json:"name"`
	MimeType  string `json:"mime_type"`
	Extension string `json:"extension"`
	Size      int64  `json:"size"`
}

func (s *TaskService) runRouteSelect(ctx context.Context, task *model.Task, workspace *TaskWorkspace) (*routeSelection, error) {
	phaseRun, err := s.beginPhaseRun(ctx, task, PhaseRouteSelect, PhaseRunnerRule, map[string]any{
		"task_id":        task.ID,
		"title":          task.Title,
		"workspace_path": workspace.HostDir,
	})
	if err != nil {
		return nil, err
	}

	selection, err := s.selectRoute(ctx, task)
	if err == nil {
		err = writeJSONPretty(filepath.Join(workspace.HostDir, ".slidesmith", "route.json"), selection)
	}
	if err == nil {
		err = s.persistRouteSelection(ctx, task, selection)
	}
	if err != nil {
		_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusFailed, nil, err)
		return nil, err
	}
	policy := routeExecutionPolicyFor(selection, s.agentCfg.BeautifyEnabled)
	output := map[string]any{
		"selection":        selection,
		"execution_policy": policy,
	}
	_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusSucceeded, output, nil)
	_ = s.event(ctx, task.ID, model.EventTypeRuntime, "route_selected", "Route selected for task", map[string]any{
		"route":               selection.Route,
		"route_reason":        selection.Reason,
		"standalone_workflow": selection.StandaloneWorkflow,
		"executable":          policy.Executable,
		"next_spec":           policy.NextSpec,
	})
	return selection, nil
}

func (s *TaskService) persistRouteSelection(ctx context.Context, task *model.Task, selection *routeSelection) error {
	if selection == nil {
		return fmt.Errorf("route selection is nil")
	}
	if task.RouteSelectedAt != nil && strings.TrimSpace(task.RouteSelectionJSON) != "" {
		var previous routeSelection
		if json.Unmarshal([]byte(task.RouteSelectionJSON), &previous) == nil && previous.Route == selection.Route && previous.CapabilitySnapshot.Captured {
			selection.CapabilitySnapshot = previous.CapabilitySnapshot
		}
	}
	raw, err := json.Marshal(selection)
	if err != nil {
		return err
	}
	selectedAt := time.Now().UTC()
	task.Route = selection.Route
	task.RouteReason = selection.Reason
	task.RouteStandaloneWorkflow = selection.StandaloneWorkflow
	task.RouteSelectionJSON = string(raw)
	task.RouteSelectedAt = &selectedAt
	saved, err := s.repo.SaveTaskIfStatus(ctx, task, model.TaskStatusSourceConverting, task.ExecutionClaimToken)
	if err != nil {
		return err
	}
	if !saved {
		return errTaskStateChanged
	}
	return nil
}

func (s *TaskService) selectRoute(ctx context.Context, task *model.Task) (*routeSelection, error) {
	artifacts, err := s.repo.ListArtifacts(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	var sources []routeSourceArtifact
	hasPresentation := false
	hasNonPresentation := false
	var corpus strings.Builder
	corpus.WriteString(task.Title)
	for _, artifact := range artifacts {
		if artifact.Kind != model.ArtifactKindSource {
			continue
		}
		extension := strings.ToLower(strings.TrimPrefix(filepath.Ext(artifact.Name), "."))
		sources = append(sources, routeSourceArtifact{
			Name:      artifact.Name,
			MimeType:  artifact.MimeType,
			Extension: extension,
			Size:      artifact.Size,
		})
		corpus.WriteString(" ")
		corpus.WriteString(artifact.Name)
		if DetectSourceKind(artifact.Name).Kind == SourceKindPresentation {
			hasPresentation = true
		} else {
			hasNonPresentation = true
		}
	}

	normalized := strings.ToLower(corpus.String())
	route := routeMain
	reason := "default main workflow for markdown/pdf/docx or general source material"
	confidence := 0.60
	if hasPresentation && isTemplateFillIntent(normalized, hasNonPresentation) {
		route = routeTemplateFill
		reason = "pptx template with new content or fill intent"
		confidence = 0.90
	} else if hasPresentation && containsAny(normalized, beautifyIntentKeywords) {
		route = routeBeautify
		reason = "pptx source with preserve text/page-count beautify intent"
		confidence = 0.90
	} else if hasPresentation && containsAny(normalized, materialRebuildKeywords) {
		reason = "pptx source requested as reconstruction material"
		confidence = 0.80
	} else if hasPresentation {
		reason = "pptx source without explicit preserve/template-fill intent"
		confidence = 0.55
	}

	standaloneWorkflow := ""
	if route != routeMain {
		standaloneWorkflow = route
	}
	return &routeSelection{
		Route:              route,
		Reason:             reason,
		Confidence:         confidence,
		StandaloneWorkflow: standaloneWorkflow,
		CreatedAt:          time.Now().UTC().Format(time.RFC3339Nano),
		SourceArtifacts:    sources,
		CapabilitySnapshot: routeCapabilitySnapshot{
			Captured:                          true,
			BeautifyEnabled:                   s.agentCfg.BeautifyEnabled,
			BeautifyFidelityStrict:            s.agentCfg.BeautifyFidelityStrict,
			BeautifySourceSVGReferenceEnabled: s.agentCfg.BeautifySourceSVGReferenceEnabled,
		},
	}, nil
}

func isTemplateFillIntent(value string, hasNonPresentation bool) bool {
	if !containsAny(value, templateIntentKeywords) {
		return false
	}
	return hasNonPresentation || containsAny(value, templateFillKeywords)
}

func containsAny(value string, keywords []string) bool {
	for _, keyword := range keywords {
		if strings.Contains(value, strings.ToLower(keyword)) {
			return true
		}
	}
	return false
}

var templateIntentKeywords = []string{
	"template",
	"模板",
	"母版",
	"版式",
}

var templateFillKeywords = []string{
	"template-fill",
	"template fill",
	"fill",
	"new content",
	"apply template",
	"填充",
	"套用",
	"新内容",
	"应用模板",
	"使用模板",
}

var beautifyIntentKeywords = []string{
	"beautify",
	"polish",
	"美化",
	"保留页数",
	"保持页数",
	"保留文字",
	"保留文本",
	"保持文字",
	"不改文字",
	"原文不变",
}

var materialRebuildKeywords = []string{
	"作为素材",
	"素材重构",
	"参考素材",
	"重构",
	"重新设计",
	"重做",
}
