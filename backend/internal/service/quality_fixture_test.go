package service

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

func phaseRunIDFromCommandNoTest(command string) string {
	const marker = "--phase-run-id "
	index := strings.LastIndex(command, marker)
	if index < 0 {
		panic("command is missing --phase-run-id: " + command)
	}
	value := strings.TrimSpace(command[index+len(marker):])
	if fields := strings.Fields(value); len(fields) > 0 {
		value = fields[0]
	}
	return strings.Trim(value, "'")
}

func passingQualitySummaryNoTest() map[string]any {
	return map[string]any{"blocking": 0, "error": 0, "warning": 0, "info": 0, "decision": "pass"}
}

func writePassingQualityReportsNoTest(projectPath, taskID, phaseRunID string) {
	hashes, err := svgBundleContractHashes(projectPath)
	if err != nil {
		panic(err)
	}
	summary := passingQualitySummaryNoTest()
	svgReport := map[string]any{
		"schema": svgQualityReportSchema, "task_id": taskID, "phase_run_id": phaseRunID,
		"svg_output_sha256": hashes["svg_output_sha256"], "findings": []any{}, "summary": summary,
	}
	chartReport := map[string]any{
		"schema": chartVerifyReportSchema, "task_id": taskID, "phase_run_id": phaseRunID,
		"receipts": []any{}, "findings": []any{}, "summary": summary,
	}
	svgPath := filepath.Join(projectPath, "validation", "svg_quality_report.json")
	chartPath := filepath.Join(projectPath, "validation", "chart_verify_report.json")
	if err := writeJSONPretty(svgPath, svgReport); err != nil {
		panic(err)
	}
	if err := writeJSONPretty(chartPath, chartReport); err != nil {
		panic(err)
	}
	svgSHA, _ := sha256File(svgPath)
	chartSHA, _ := sha256File(chartPath)
	qualitySummary := map[string]any{
		"schema": qualitySummarySchema, "stage": "svg_pre_export", "task_id": taskID,
		"phase_run_id": phaseRunID, "svg_output_sha256": hashes["svg_output_sha256"],
		"reports": []any{
			map[string]any{"kind": "svg", "path": "validation/svg_quality_report.json", "sha256": svgSHA},
			map[string]any{"kind": "chart", "path": "validation/chart_verify_report.json", "sha256": chartSHA},
		},
		"summary": summary, "decision": "pass",
	}
	qualityPath := filepath.Join(projectPath, "validation", "quality_summary.json")
	if err := writeJSONPretty(qualityPath, qualitySummary); err != nil {
		panic(err)
	}
	qualitySHA, _ := sha256File(qualityPath)
	if err := writeJSONPretty(filepath.Join(projectPath, ".slidesmith", "quality_report.json"), map[string]any{
		"schema": "slidesmith.quality_report_pointer.v1", "canonical_path": "validation/quality_summary.json",
		"canonical_sha256": qualitySHA, "decision": "pass", "summary": summary,
	}); err != nil {
		panic(err)
	}
	contract := map[string]any{
		"schema": qualityCheckContractSchema, "phase": string(PhaseQualityCheck), "task_id": taskID,
		"runner_profile": model.RunnerProfileFullPPTMaster,
		"phase_run_id":   phaseRunID, "quality_summary_sha256": qualitySHA,
		"svg_quality_report_sha256": svgSHA, "chart_verify_report_sha256": chartSHA,
		"svg_output_sha256": hashes["svg_output_sha256"], "decision": "pass", "summary": summary,
	}
	for field, sha := range hashes {
		contract[field] = sha
	}
	if err := writeJSONPretty(filepath.Join(projectPath, ".slidesmith", "contracts", string(PhaseQualityCheck)+".json"), contract); err != nil {
		panic(err)
	}
}

func writePassingPPTXValidateReportsNoTest(projectPath, taskID, phaseRunID string) {
	exportPath := filepath.Join(projectPath, ".slidesmith", "contracts", string(PhaseFinalizeExport)+".json")
	exportContract := readJSONMap(exportPath)
	canonical, _ := exportContract["canonical_pptx"].(map[string]any)
	if canonical == nil {
		panic("export contract missing canonical_pptx")
	}
	readbackPath := filepath.Join(projectPath, "validation", "pptx_readback.md")
	inventoryPath := filepath.Join(projectPath, "validation", "pptx_text_inventory.json")
	reportPath := filepath.Join(projectPath, "validation", "pptx_validate_report.json")
	renderRoot := filepath.Join(projectPath, "validation", "render")
	mustWriteFileNoTest(projectPath, filepath.Join("validation", "pptx_readback.md"), "## P01\n\nfixture\n")
	if err := writeJSONPretty(inventoryPath, map[string]any{
		"schema": pptxTextInventorySchema, "task_id": taskID, "phase_run_id": phaseRunID,
		"svg_pages": []any{}, "pptx_pages": []any{}, "deck_coverage": 1,
	}); err != nil {
		panic(err)
	}
	pageCount := 0
	switch value := exportContract["expected_pages"].(type) {
	case float64:
		pageCount = int(value)
	case int:
		pageCount = value
	}
	slides := make([]map[string]any, 0, pageCount)
	for page := 1; page <= pageCount; page++ {
		relative := filepath.ToSlash(filepath.Join("validation", "render", "slide-"+twoDigitNoTest(page)+".png"))
		mustWriteFileNoTest(projectPath, filepath.FromSlash(relative), "png-fixture-"+twoDigitNoTest(page))
		sha, _ := sha256File(filepath.Join(projectPath, filepath.FromSlash(relative)))
		slides = append(slides, map[string]any{"page_id": "P" + twoDigitNoTest(page), "path": relative, "sha256": sha, "width": 1280, "height": 720, "blank": false})
	}
	mustWriteFileNoTest(projectPath, filepath.Join("validation", "render", "output.pdf"), "%PDF-fixture\n")
	mustWriteFileNoTest(projectPath, filepath.Join("validation", "render", "contact_sheet.png"), "contact-fixture\n")
	pdfSHA, _ := sha256File(filepath.Join(renderRoot, "output.pdf"))
	contactSHA, _ := sha256File(filepath.Join(renderRoot, "contact_sheet.png"))
	render := map[string]any{
		"pdf": "validation/render/output.pdf", "pdf_sha256": pdfSHA, "page_count": pageCount,
		"slides": slides, "contact_sheet": "validation/render/contact_sheet.png", "contact_sheet_sha256": contactSHA,
	}
	pptxPath := filepath.Join(projectPath, filepath.FromSlash(valueString(canonical, "path", "")))
	pptxInfo, err := filepath.Glob(pptxPath)
	if err != nil || len(pptxInfo) != 1 {
		panic("canonical pptx missing")
	}
	report := map[string]any{
		"schema": pptxValidateReportSchema, "task_id": taskID, "phase_run_id": phaseRunID,
		"pptx":   map[string]any{"path": valueString(canonical, "path", ""), "sha256": valueString(canonical, "sha256", ""), "size": canonical["size"], "slide_count": pageCount},
		"render": render, "text_fidelity": map[string]any{"pages": []any{}, "deck_coverage": 1},
		"findings": []any{}, "summary": passingQualitySummaryNoTest(), "decision": "pass",
	}
	if err := writeJSONPretty(reportPath, report); err != nil {
		panic(err)
	}
	exportSHA, _ := sha256File(exportPath)
	qualitySHA, _ := sha256File(filepath.Join(projectPath, "validation", "quality_summary.json"))
	readbackSHA, _ := sha256File(readbackPath)
	inventorySHA, _ := sha256File(inventoryPath)
	reportSHA, _ := sha256File(reportPath)
	contract := map[string]any{
		"schema": pptxValidateContractSchema, "phase": string(PhasePPTXValidate), "task_id": taskID, "phase_run_id": phaseRunID,
		"export_contract_sha256": exportSHA, "quality_summary_sha256": qualitySHA,
		"canonical_pptx": canonical, "pptx_readback_sha256": readbackSHA,
		"pptx_text_inventory_sha256": inventorySHA, "pptx_validate_report_sha256": reportSHA,
		"render": render, "summary": passingQualitySummaryNoTest(), "decision": "pass",
	}
	if err := writeJSONPretty(filepath.Join(projectPath, ".slidesmith", "contracts", string(PhasePPTXValidate)+".json"), contract); err != nil {
		panic(err)
	}
}

func twoDigitNoTest(value int) string {
	return fmt.Sprintf("%02d", value)
}
