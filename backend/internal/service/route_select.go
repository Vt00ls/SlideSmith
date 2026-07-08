package service

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

const (
	routeMain         = "main"
	routeBeautify     = "beautify"
	routeTemplateFill = "template-fill"
)

type routeSelection struct {
	Route              string                `json:"route"`
	Reason             string                `json:"reason"`
	StandaloneWorkflow string                `json:"standalone_workflow"`
	CreatedAt          string                `json:"created_at"`
	SourceArtifacts    []routeSourceArtifact `json:"source_artifacts"`
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
	if err != nil {
		_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusFailed, nil, err)
		return nil, err
	}
	_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusSucceeded, selection, nil)
	return selection, nil
}

func (s *TaskService) selectRoute(ctx context.Context, task *model.Task) (*routeSelection, error) {
	artifacts, err := s.repo.ListArtifacts(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	var sources []routeSourceArtifact
	hasPPTX := false
	hasNonPPTX := false
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
		if extension == "pptx" || extension == "ppt" {
			hasPPTX = true
		} else {
			hasNonPPTX = true
		}
	}

	normalized := strings.ToLower(corpus.String())
	route := routeMain
	reason := "default main workflow for markdown/pdf/docx or general source material"
	if hasPPTX && isTemplateFillIntent(normalized, hasNonPPTX) {
		route = routeTemplateFill
		reason = "pptx template with new content or fill intent"
	} else if hasPPTX && containsAny(normalized, beautifyIntentKeywords) {
		route = routeBeautify
		reason = "pptx source with preserve text/page-count beautify intent"
	} else if hasPPTX && containsAny(normalized, materialRebuildKeywords) {
		reason = "pptx source requested as reconstruction material"
	} else if hasPPTX {
		reason = "pptx source without explicit preserve/template-fill intent"
	}

	standaloneWorkflow := ""
	if route != routeMain {
		standaloneWorkflow = route
	}
	return &routeSelection{
		Route:              route,
		Reason:             reason,
		StandaloneWorkflow: standaloneWorkflow,
		CreatedAt:          time.Now().UTC().Format(time.RFC3339Nano),
		SourceArtifacts:    sources,
	}, nil
}

func isTemplateFillIntent(value string, hasNonPPTX bool) bool {
	if !containsAny(value, templateIntentKeywords) {
		return false
	}
	return hasNonPPTX || containsAny(value, templateFillKeywords)
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
