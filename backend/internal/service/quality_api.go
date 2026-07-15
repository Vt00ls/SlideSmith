package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

type TaskQuality struct {
	TaskID                 string                    `json:"task_id"`
	CurrentGate            string                    `json:"current_gate"`
	Decision               string                    `json:"decision"`
	WarningBadge           int                       `json:"warning_badge"`
	SVGSummary             qualityGateSummary        `json:"svg_summary"`
	PPTXSummary            qualityGateSummary        `json:"pptx_summary"`
	Findings               []TaskQualityFinding      `json:"findings"`
	ChartReceipts          []TaskChartReceiptSummary `json:"chart_receipts"`
	TextCoverage           float64                   `json:"text_coverage"`
	RenderArtifactIDs      []string                  `json:"render_artifact_ids"`
	ContactSheetArtifactID string                    `json:"contact_sheet_artifact_id"`
	ReadbackArtifactID     string                    `json:"readback_artifact_id"`
	AllowedRetryPhases     []string                  `json:"allowed_retry_phases"`
	BeautifyFidelity       *TaskBeautifyFidelity     `json:"beautify_fidelity,omitempty"`
}

type TaskBeautifyFidelity struct {
	Present          bool                     `json:"present"`
	Decision         string                   `json:"decision"`
	SourceSlideCount int                      `json:"source_slide_count"`
	OutputSlideCount int                      `json:"output_slide_count"`
	Pages            []BeautifyFidelityPage   `json:"pages"`
	Identity         BeautifyFidelityIdentity `json:"identity"`
	Ignored          []string                 `json:"ignored"`
	Unsupported      []string                 `json:"unsupported"`
	Warning          int                      `json:"warning"`
	Error            int                      `json:"error"`
	Blocking         int                      `json:"blocking"`
	ReportArtifactID string                   `json:"report_artifact_id"`
}

type TaskQualityFinding struct {
	ID         string `json:"id"`
	Rule       string `json:"rule"`
	Severity   string `json:"severity"`
	Status     string `json:"status"`
	Stage      string `json:"stage"`
	PageID     string `json:"page_id"`
	Artifact   string `json:"artifact"`
	Message    string `json:"message"`
	OwnerPhase string `json:"owner_phase"`
	RetryPhase string `json:"retry_phase"`
}

type TaskChartReceiptSummary struct {
	ChartID  string `json:"chart_id"`
	PageID   string `json:"page_id"`
	Mode     string `json:"mode"`
	Decision string `json:"decision"`
	Checks   int    `json:"checks"`
	Failures int    `json:"failures"`
}

func (s *TaskService) GetQuality(ctx context.Context, taskID string) (*TaskQuality, error) {
	task, err := s.repo.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	result := &TaskQuality{
		TaskID:      task.ID,
		CurrentGate: qualityCurrentGate(task),
		Decision:    "pending",
	}
	projectPath, err := s.findPersistentProjectPath(task)
	if err == nil {
		s.loadTaskQualityReports(projectPath, result)
	}
	artifacts, artifactErr := s.repo.ListArtifacts(ctx, task.ID)
	if artifactErr != nil {
		return nil, artifactErr
	}
	for _, artifact := range artifacts {
		switch artifact.Kind {
		case model.ArtifactKindRenderedSlide:
			result.RenderArtifactIDs = append(result.RenderArtifactIDs, artifact.ID)
		case model.ArtifactKindContactSheet:
			if result.ContactSheetArtifactID == "" {
				result.ContactSheetArtifactID = artifact.ID
			}
		case model.ArtifactKindPPTXReadback:
			if result.ReadbackArtifactID == "" {
				result.ReadbackArtifactID = artifact.ID
			}
		case model.ArtifactKindBeautifyFidelityReport:
			if result.BeautifyFidelity != nil && result.BeautifyFidelity.ReportArtifactID == "" {
				result.BeautifyFidelity.ReportArtifactID = artifact.ID
			}
		}
	}
	result.AllowedRetryPhases = qualityRetryPhases(task, result.Findings)
	return result, nil
}

func (s *TaskService) loadTaskQualityReports(projectPath string, result *TaskQuality) {
	var svg detailedQualityReport
	if err := readOptionalQualityJSON(projectPath, "validation/svg_quality_report.json", &svg); err == nil && svg.Schema == svgQualityReportSchema && svg.TaskID == result.TaskID {
		result.SVGSummary = svg.Summary
		appendSafeQualityFindings(result, svg.Findings)
		result.Decision = svg.Summary.Decision
	}
	var chart struct {
		Schema   string `json:"schema"`
		TaskID   string `json:"task_id"`
		Receipts []struct {
			ChartID     string `json:"chart_id"`
			PageID      string `json:"page_id"`
			Mode        string `json:"mode"`
			Decision    string `json:"decision"`
			Comparisons []struct {
				Passed bool `json:"passed"`
			} `json:"comparisons"`
		} `json:"receipts"`
	}
	if err := readOptionalQualityJSON(projectPath, "validation/chart_verify_report.json", &chart); err == nil && chart.Schema == chartVerifyReportSchema && chart.TaskID == result.TaskID {
		for _, receipt := range chart.Receipts {
			failures := 0
			for _, comparison := range receipt.Comparisons {
				if !comparison.Passed {
					failures++
				}
			}
			result.ChartReceipts = append(result.ChartReceipts, TaskChartReceiptSummary{
				ChartID: receipt.ChartID, PageID: receipt.PageID, Mode: receipt.Mode,
				Decision: receipt.Decision, Checks: len(receipt.Comparisons), Failures: failures,
			})
		}
	}
	var pptx struct {
		Schema       string             `json:"schema"`
		TaskID       string             `json:"task_id"`
		Summary      qualityGateSummary `json:"summary"`
		Findings     []qualityFinding   `json:"findings"`
		TextFidelity struct {
			DeckCoverage float64 `json:"deck_coverage"`
		} `json:"text_fidelity"`
	}
	if err := readOptionalQualityJSON(projectPath, "validation/pptx_validate_report.json", &pptx); err == nil && pptx.Schema == pptxValidateReportSchema && pptx.TaskID == result.TaskID {
		result.PPTXSummary = pptx.Summary
		result.TextCoverage = pptx.TextFidelity.DeckCoverage
		appendSafeQualityFindings(result, pptx.Findings)
		result.Decision = pptx.Summary.Decision
	}
	var fidelity BeautifyFidelityReport
	if err := readOptionalQualityJSON(projectPath, "validation/beautify_fidelity_report.json", &fidelity); err == nil && fidelity.Schema == beautifyFidelityReportSchema && fidelity.TaskID == result.TaskID {
		view := &TaskBeautifyFidelity{
			Present: true, Decision: fidelity.Decision, SourceSlideCount: fidelity.SourceSlideCount,
			OutputSlideCount: fidelity.OutputSlideCount, Pages: fidelity.Pages, Identity: fidelity.Identity,
			Warning: fidelity.Summary.Warning, Error: fidelity.Summary.Error, Blocking: fidelity.Summary.Blocking,
		}
		for _, item := range fidelity.Ignored {
			view.Ignored = append(view.Ignored, beautifyDecisionLabel(item))
		}
		for _, item := range fidelity.Unsupported {
			view.Unsupported = append(view.Unsupported, beautifyDecisionLabel(item))
		}
		result.BeautifyFidelity = view
		appendSafeQualityFindings(result, fidelity.Findings)
		result.Decision = fidelity.Decision
	}
	result.WarningBadge = result.SVGSummary.Warning + result.PPTXSummary.Warning
	if result.BeautifyFidelity != nil {
		result.WarningBadge += result.BeautifyFidelity.Warning
	}
	if result.Decision == "" {
		result.Decision = "pending"
	}
	sort.SliceStable(result.Findings, func(i, j int) bool {
		severity := map[string]int{"blocking": 0, "error": 1, "warning": 2, "info": 3}
		if severity[result.Findings[i].Severity] != severity[result.Findings[j].Severity] {
			return severity[result.Findings[i].Severity] < severity[result.Findings[j].Severity]
		}
		if result.Findings[i].PageID != result.Findings[j].PageID {
			return result.Findings[i].PageID < result.Findings[j].PageID
		}
		return result.Findings[i].Rule < result.Findings[j].Rule
	})
}

func beautifyDecisionLabel(item BeautifyLockDecision) string {
	return "P" + leftPadInt(item.SlideIndex, 2) + ":" + item.ID
}

func leftPadInt(value, width int) string {
	text := strconv.Itoa(value)
	for len(text) < width {
		text = "0" + text
	}
	return text
}

func readOptionalQualityJSON(projectPath, relative string, target any) error {
	path, err := containedProjectContractPath(projectPath, relative)
	if err != nil {
		return err
	}
	if err := requireContainedContractFile(projectPath, path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return err
		}
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}

func appendSafeQualityFindings(result *TaskQuality, findings []qualityFinding) {
	for _, item := range findings {
		owner, _ := item.Remediation["owner_phase"].(string)
		retry, _ := item.Remediation["retry_phase"].(string)
		result.Findings = append(result.Findings, TaskQualityFinding{
			ID: item.ID, Rule: item.Rule, Severity: item.Severity, Status: item.Status,
			Stage: item.Stage, PageID: item.PageID, Artifact: filepath.ToSlash(item.Artifact),
			Message: item.Message, OwnerPhase: owner, RetryPhase: retry,
		})
	}
}

func qualityCurrentGate(task *model.Task) string {
	if task == nil {
		return "pending"
	}
	switch task.Status {
	case model.TaskStatusQualityChecking:
		return string(PhaseQualityCheck)
	case model.TaskStatusExporting:
		return string(PhaseFinalizeExport)
	case model.TaskStatusPPTXValidating:
		return string(PhasePPTXValidate)
	case model.TaskStatusPublishing, model.TaskStatusCompleted:
		return string(PhasePublish)
	case model.TaskStatusFailed:
		for _, phase := range []PipelinePhase{PhaseQualityCheck, PhaseFinalizeExport, PhasePPTXValidate, PhasePublish} {
			if strings.HasPrefix(strings.ToLower(task.FailurePhase), string(phase)) {
				return string(phase)
			}
		}
		return "failed"
	default:
		return "pending"
	}
}

func qualityRetryPhases(task *model.Task, findings []TaskQualityFinding) []string {
	allowed := map[string]bool{}
	for _, finding := range findings {
		phase := finding.RetryPhase
		if phase == "" {
			phase = finding.OwnerPhase
		}
		switch phase {
		case retryPhaseSVGExecute, retryPhaseQualityCheck, retryPhaseFinalizeExport, retryPhasePPTXValidate:
			allowed[phase] = true
		}
	}
	if task != nil && task.Status == model.TaskStatusFailed {
		allowed[inferRetryPhase(task.FailurePhase)] = true
	}
	order := []string{retryPhaseSVGExecute, retryPhaseQualityCheck, retryPhaseFinalizeExport, retryPhasePPTXValidate, retryPhasePublish}
	result := make([]string, 0, len(allowed))
	for _, phase := range order {
		if allowed[phase] {
			result = append(result, phase)
		}
	}
	return result
}
