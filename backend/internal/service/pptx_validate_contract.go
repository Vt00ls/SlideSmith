package service

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

const (
	pptxValidateReportSchema   = "slidesmith.pptx_validate_report.v1"
	pptxTextInventorySchema    = "slidesmith.pptx_text_inventory.v1"
	pptxValidateContractSchema = "slidesmith.pptx_validate_contract.v1"
)

type pptxValidateReportDocument struct {
	Schema     string             `json:"schema"`
	TaskID     string             `json:"task_id"`
	PhaseRunID string             `json:"phase_run_id"`
	PPTX       pptxValidatePPTX   `json:"pptx"`
	Render     pptxValidateRender `json:"render"`
	Findings   []qualityFinding   `json:"findings"`
	Summary    qualityGateSummary `json:"summary"`
	Decision   string             `json:"decision"`
}

type pptxValidatePPTX struct {
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	SlideCount int    `json:"slide_count"`
}

type pptxValidateRender struct {
	PDF                string                    `json:"pdf"`
	PDFSHA256          string                    `json:"pdf_sha256"`
	PageCount          int                       `json:"page_count"`
	Slides             []pptxValidateRenderSlide `json:"slides"`
	ContactSheet       string                    `json:"contact_sheet"`
	ContactSheetSHA256 string                    `json:"contact_sheet_sha256"`
}

type pptxValidateRenderSlide struct {
	PageID string `json:"page_id"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Blank  bool   `json:"blank"`
}

func validatePPTXValidateContract(projectPath string) (map[string]any, error) {
	return validatePPTXValidateContractForRun(projectPath, "")
}

func validatePPTXValidateContractForRun(projectPath, expectedPhaseRunID string) (map[string]any, error) {
	paths := map[string]string{
		"readback":  filepath.Join(projectPath, "validation", "pptx_readback.md"),
		"inventory": filepath.Join(projectPath, "validation", "pptx_text_inventory.json"),
		"report":    filepath.Join(projectPath, "validation", "pptx_validate_report.json"),
		"contract":  filepath.Join(projectPath, ".slidesmith", "contracts", string(PhasePPTXValidate)+".json"),
		"export":    filepath.Join(projectPath, ".slidesmith", "contracts", string(PhaseFinalizeExport)+".json"),
		"quality":   filepath.Join(projectPath, "validation", "quality_summary.json"),
	}
	for _, path := range paths {
		if err := requireContainedContractFile(projectPath, path); err != nil {
			return nil, err
		}
	}
	var report pptxValidateReportDocument
	if err := readJSONContract(paths["report"], &report); err != nil {
		return nil, fmt.Errorf("decode PPTX validate report: %w", err)
	}
	if report.Schema != pptxValidateReportSchema || report.TaskID == "" {
		return nil, fmt.Errorf("PPTX validate report schema/task binding is invalid")
	}
	if expectedPhaseRunID != "" && report.PhaseRunID != expectedPhaseRunID {
		return nil, fmt.Errorf("PPTX validate report phase_run_id = %q, expected %q", report.PhaseRunID, expectedPhaseRunID)
	}
	if err := validateQualityDecision(report.Summary, report.Decision); err != nil {
		return nil, err
	}
	if err := validateQualityFindings(report.Findings, report.Summary); err != nil {
		return nil, err
	}
	if report.Summary.Blocking != 0 || report.Summary.Error != 0 {
		return nil, fmt.Errorf("PPTX validate gate rejected publish: blocking=%d error=%d", report.Summary.Blocking, report.Summary.Error)
	}
	if report.PPTX.SlideCount <= 0 || report.Render.PageCount != report.PPTX.SlideCount || len(report.Render.Slides) != report.PPTX.SlideCount {
		return nil, fmt.Errorf("PPTX validate render/slide count mismatch")
	}
	pptxPath, err := containedProjectContractPath(projectPath, report.PPTX.Path)
	if err != nil {
		return nil, err
	}
	if err := requireContainedContractFile(projectPath, pptxPath); err != nil {
		return nil, err
	}
	info, err := os.Stat(pptxPath)
	if err != nil {
		return nil, err
	}
	pptxSHA, err := sha256File(pptxPath)
	if err != nil || pptxSHA != report.PPTX.SHA256 || info.Size() != report.PPTX.Size {
		return nil, fmt.Errorf("PPTX validate report canonical PPTX binding is stale")
	}
	for _, artifact := range append([]pptxValidateRenderSlide{{Path: report.Render.PDF, SHA256: report.Render.PDFSHA256}, {Path: report.Render.ContactSheet, SHA256: report.Render.ContactSheetSHA256}}, report.Render.Slides...) {
		path, err := containedProjectContractPath(projectPath, artifact.Path)
		if err != nil {
			return nil, err
		}
		if err := requireContainedContractFile(projectPath, path); err != nil {
			return nil, err
		}
		sha, err := sha256File(path)
		if err != nil || sha != artifact.SHA256 {
			return nil, fmt.Errorf("render artifact hash is stale: %s", artifact.Path)
		}
		if artifact.Blank {
			return nil, fmt.Errorf("render artifact is marked blank: %s", artifact.Path)
		}
	}
	var inventory struct {
		Schema     string `json:"schema"`
		TaskID     string `json:"task_id"`
		PhaseRunID string `json:"phase_run_id"`
	}
	if err := readJSONContract(paths["inventory"], &inventory); err != nil || inventory.Schema != pptxTextInventorySchema || inventory.TaskID != report.TaskID || inventory.PhaseRunID != report.PhaseRunID {
		return nil, fmt.Errorf("PPTX text inventory binding mismatch")
	}
	var contract map[string]any
	raw, err := os.ReadFile(paths["contract"])
	if err != nil || json.Unmarshal(raw, &contract) != nil {
		return nil, fmt.Errorf("PPTX validate contract is invalid")
	}
	if valueString(contract, "schema", "") != pptxValidateContractSchema || valueString(contract, "decision", "") != report.Decision {
		return nil, fmt.Errorf("PPTX validate contract schema/decision mismatch")
	}
	if expectedPhaseRunID != "" && valueString(contract, "phase_run_id", "") != expectedPhaseRunID {
		return nil, fmt.Errorf("PPTX validate contract phase run is stale")
	}
	checks := map[string]string{
		"export_contract_sha256":      paths["export"],
		"quality_summary_sha256":      paths["quality"],
		"pptx_readback_sha256":        paths["readback"],
		"pptx_text_inventory_sha256":  paths["inventory"],
		"pptx_validate_report_sha256": paths["report"],
	}
	for field, path := range checks {
		sha, err := sha256File(path)
		if err != nil || valueString(contract, field, "") != sha {
			return nil, fmt.Errorf("PPTX validate contract %s is stale", field)
		}
	}
	canonical, _ := contract["canonical_pptx"].(map[string]any)
	if canonical == nil || valueString(canonical, "path", "") != report.PPTX.Path || valueString(canonical, "sha256", "") != report.PPTX.SHA256 {
		return nil, fmt.Errorf("PPTX validate canonical binding mismatch")
	}
	contract["phase"] = string(PhasePPTXValidate)
	contract["project_path"] = projectPath
	contract["checked_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	return contract, nil
}

func validatePPTXValidateContractForTask(projectPath string, task *model.Task, expectedPhaseRunID string) (map[string]any, error) {
	contract, err := validatePPTXValidateContractForRun(projectPath, expectedPhaseRunID)
	if err != nil {
		return nil, err
	}
	if task == nil || task.Route != model.TaskRouteBeautify {
		return contract, nil
	}
	canonical, _ := contract["canonical_pptx"].(map[string]any)
	outputSHA := valueString(canonical, "sha256", "")
	fidelity, err := ValidateBeautifyFidelityReport(projectPath, task.ID, outputSHA)
	if err != nil {
		return nil, fmt.Errorf("pptx_validate.beautify_fidelity: %w", err)
	}
	bindings := map[string]any{
		"beautify_inputs_sha256":          fidelity.BeautifyInputsSHA256,
		"beautify_inventory_sha256":       fidelity.BeautifyInventorySHA256,
		"beautify_plan_sha256":            fidelity.BeautifyPlanSHA256,
		"beautify_lock_sha256":            fidelity.BeautifyLockSHA256,
		"source_pptx_sha256":              fidelity.SourcePPTXSHA256,
		"output_pptx_sha256":              fidelity.OutputPPTXSHA256,
		"source_slide_count":              fidelity.SourceSlideCount,
		"output_slide_count":              fidelity.OutputSlideCount,
		"beautify_fidelity_report_sha256": fidelity.FidelityReportSHA256,
		"beautify_fidelity_decision":      fidelity.Decision,
	}
	for field, expected := range bindings {
		actual, ok := contract[field]
		if !ok || fmt.Sprint(actual) != fmt.Sprint(expected) {
			return nil, fmt.Errorf("pptx_validate.beautify_fidelity: contract %s is stale", field)
		}
	}
	summary, ok := contract["beautify_fidelity_summary"].(map[string]any)
	if !ok || intFromAny(summary["blocking"]) != 0 || intFromAny(summary["error"]) != 0 {
		return nil, fmt.Errorf("pptx_validate.beautify_fidelity: contract summary is missing or blocking")
	}
	contract["beautify_fidelity"] = fidelity
	return contract, nil
}

func intFromAny(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return -1
	}
}

func validatePublishQualityChain(projectPath string) (map[string]any, error) {
	contract, err := validatePPTXValidateContract(projectPath)
	if err != nil {
		return nil, err
	}
	if valueString(contract, "decision", "") != "pass" && valueString(contract, "decision", "") != "pass_with_warnings" {
		return nil, fmt.Errorf("PPTX validate decision is not publishable")
	}
	return contract, nil
}

func validatePublishQualityChainForTask(projectPath string, task *model.Task) (map[string]any, error) {
	contract, err := validatePPTXValidateContractForTask(projectPath, task, "")
	if err != nil {
		return nil, err
	}
	if valueString(contract, "decision", "") != "pass" && valueString(contract, "decision", "") != "pass_with_warnings" {
		return nil, fmt.Errorf("PPTX validate decision is not publishable")
	}
	if task != nil && task.Route == model.TaskRouteBeautify {
		fidelity, ok := contract["beautify_fidelity"].(*BeautifyFidelityContract)
		if !ok || (fidelity.Decision != "pass" && fidelity.Decision != "pass_with_warnings") || fidelity.Summary.Blocking != 0 || fidelity.Summary.Error != 0 {
			return nil, fmt.Errorf("publish.beautify_gate: Beautify fidelity is not publishable")
		}
	}
	return contract, nil
}
