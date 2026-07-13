package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func validateTemplateFillPlanContract(projectPath string) (map[string]any, error) {
	contract, _, err := validateTemplateFillPlanContractSnapshot(projectPath)
	return contract, err
}

func validateTemplateFillPlanContractSnapshot(projectPath string) (map[string]any, string, error) {
	provenance, err := snapshotTemplateFillSourceProvenance(projectPath)
	if err != nil {
		return nil, "", err
	}
	return validateTemplateFillPlanContractSnapshotWithProvenance(projectPath, provenance)
}

func validateTemplateFillPlanContractSnapshotWithProvenance(projectPath string, provenance templateFillSourceProvenance) (map[string]any, string, error) {
	inputs, slides, status, planSHA256, err := readValidatedTemplateFillPlanWithSHA256AndProvenance(projectPath, provenance)
	if err != nil {
		return nil, "", err
	}
	if err := rejectTemplateFillMainRouteOutputs(inputs.ProjectPath); err != nil {
		return nil, "", err
	}

	contract := map[string]any{
		"phase":                string(PhaseTemplateFillPlan),
		"project_path":         inputs.ProjectPath,
		"source_pptx":          inputs.SourcePPTX,
		"slide_library":        inputs.SlideLibrary,
		"fill_plan":            inputs.FillPlan,
		"plan_status":          status,
		"plan_sha256":          planSHA256,
		"planned_slide_count":  len(slides),
		"content_source_count": len(inputs.ContentSources),
		"checked_at":           time.Now().UTC().Format(time.RFC3339Nano),
	}
	if _, err := writeTemplateFillContractReport(inputs.ProjectPath, PhaseTemplateFillPlan, contract); err != nil {
		return nil, "", fmt.Errorf("write template fill plan contract: %w", err)
	}
	return contract, planSHA256, nil
}

func validateTemplateFillCheckContract(projectPath string, requireNoErrors bool) (map[string]any, error) {
	return validateTemplateFillCheckContractForPlan(projectPath, requireNoErrors, "", "")
}

func validateTemplateFillCheckContractForPlan(projectPath string, requireNoErrors bool, expectedStatus, expectedSHA256 string) (map[string]any, error) {
	provenance, err := snapshotTemplateFillSourceProvenance(projectPath)
	if err != nil {
		return nil, err
	}
	return validateTemplateFillCheckContractForPlanWithProvenance(projectPath, provenance, requireNoErrors, expectedStatus, expectedSHA256)
}

func validateTemplateFillCheckContractForPlanWithProvenance(projectPath string, provenance templateFillSourceProvenance, requireNoErrors bool, expectedStatus, expectedSHA256 string) (map[string]any, error) {
	inputs, err := discoverTemplateFillInputsWithProvenance(projectPath, provenance)
	if err != nil {
		return nil, err
	}
	_, _, planStatus, planSHA256, err := readValidatedTemplateFillPlanWithSHA256AndProvenance(inputs.ProjectPath, provenance)
	if err != nil {
		return nil, err
	}
	if expectedStatus != "" && planStatus != expectedStatus {
		return nil, fmt.Errorf("template fill check plan status = %q, expected %q", planStatus, expectedStatus)
	}
	if expectedSHA256 != "" && planSHA256 != expectedSHA256 {
		return nil, fmt.Errorf("template fill check plan sha256 changed: got %s, expected %s", planSHA256, expectedSHA256)
	}
	report, checkReportSHA256, err := readTemplateFillJSONObjectWithSHA256(inputs.CheckReport, "template fill check report")
	if err != nil {
		return nil, err
	}
	if schema, ok := report["schema"].(string); !ok || schema != "template_fill_pptx_check.v1" {
		return nil, fmt.Errorf("template fill check report schema = %#v, expected %q", report["schema"], "template_fill_pptx_check.v1")
	}
	summary, err := templateFillSummary(report, "template fill check report", "ok", "warn", "error")
	if err != nil {
		return nil, err
	}
	if requireNoErrors && summary["error"].(int) != 0 {
		return nil, fmt.Errorf("template fill check report summary.error = %d", summary["error"])
	}
	if err := rejectTemplateFillMainRouteOutputs(inputs.ProjectPath); err != nil {
		return nil, err
	}

	contract := map[string]any{
		"phase":               string(PhaseTemplateFillCheck),
		"project_path":        inputs.ProjectPath,
		"check_report":        inputs.CheckReport,
		"plan_status":         planStatus,
		"plan_sha256":         planSHA256,
		"check_report_sha256": checkReportSHA256,
		"summary":             summary,
		"checked_at":          time.Now().UTC().Format(time.RFC3339Nano),
	}
	if _, err := writeTemplateFillContractReport(inputs.ProjectPath, PhaseTemplateFillCheck, contract); err != nil {
		return nil, fmt.Errorf("write template fill check contract: %w", err)
	}
	return contract, nil
}

func validateTemplateFillApplyContract(projectPath string) (map[string]any, error) {
	provenance, err := snapshotTemplateFillSourceProvenance(projectPath)
	if err != nil {
		return nil, err
	}
	return validateTemplateFillApplyContractWithProvenance(projectPath, provenance)
}

func validateTemplateFillApplyContractWithProvenance(projectPath string, provenance templateFillSourceProvenance) (map[string]any, error) {
	inputs, slides, status, _, err := readValidatedTemplateFillPlanWithSHA256AndProvenance(projectPath, provenance)
	if err != nil {
		return nil, err
	}
	if status != "confirmed" {
		return nil, fmt.Errorf("template fill plan status = %q, expected %q", status, "confirmed")
	}
	if err := rejectTemplateFillMainRouteOutputs(inputs.ProjectPath); err != nil {
		return nil, err
	}

	exportPath, err := latestTemplateFillExport(filepath.Join(inputs.ProjectPath, "exports"))
	if err != nil {
		return nil, err
	}
	slideCount, err := countPPTXSlides(exportPath)
	if err != nil {
		return nil, fmt.Errorf("template fill export %s is not a valid pptx: %w", exportPath, err)
	}
	if slideCount != len(slides) {
		return nil, fmt.Errorf("template fill export %s has %d slides, expected %d", exportPath, slideCount, len(slides))
	}

	contract := map[string]any{
		"phase":               string(PhaseTemplateFillApply),
		"project_path":        inputs.ProjectPath,
		"fill_plan":           inputs.FillPlan,
		"export":              exportPath,
		"planned_slide_count": len(slides),
		"slide_count":         slideCount,
		"checked_at":          time.Now().UTC().Format(time.RFC3339Nano),
	}
	if _, err := writeTemplateFillContractReport(inputs.ProjectPath, PhaseTemplateFillApply, contract); err != nil {
		return nil, fmt.Errorf("write template fill apply contract: %w", err)
	}
	return contract, nil
}

func validateTemplateFillValidateContract(projectPath string) (map[string]any, error) {
	provenance, err := snapshotTemplateFillSourceProvenance(projectPath)
	if err != nil {
		return nil, err
	}
	return validateTemplateFillValidateContractWithProvenance(projectPath, provenance)
}

func validateTemplateFillValidateContractWithProvenance(projectPath string, provenance templateFillSourceProvenance) (map[string]any, error) {
	inputs, err := discoverTemplateFillInputsWithProvenance(projectPath, provenance)
	if err != nil {
		return nil, err
	}
	report, err := readTemplateFillJSONObject(inputs.ValidateReport, "template fill validate report")
	if err != nil {
		return nil, err
	}
	if schema, ok := report["schema"].(string); !ok || schema != "template_fill_pptx_validate.v1" {
		return nil, fmt.Errorf("template fill validate report schema = %#v, expected %q", report["schema"], "template_fill_pptx_validate.v1")
	}
	summary, err := templateFillSummary(report, "template fill validate report", "ok", "warn", "error")
	if err != nil {
		return nil, err
	}
	if summary["error"].(int) != 0 {
		return nil, fmt.Errorf("template fill validate report summary.error = %d", summary["error"])
	}
	if err := requireNonEmptyFile(inputs.Readback); err != nil {
		return nil, err
	}
	if err := rejectTemplateFillMainRouteOutputs(inputs.ProjectPath); err != nil {
		return nil, err
	}

	contract := map[string]any{
		"phase":           string(PhaseTemplateFillValidate),
		"project_path":    inputs.ProjectPath,
		"validate_report": inputs.ValidateReport,
		"readback":        inputs.Readback,
		"summary":         summary,
		"checked_at":      time.Now().UTC().Format(time.RFC3339Nano),
	}
	if _, err := writeTemplateFillContractReport(inputs.ProjectPath, PhaseTemplateFillValidate, contract); err != nil {
		return nil, fmt.Errorf("write template fill validate contract: %w", err)
	}
	return contract, nil
}

func templateFillExpectedSlideCount(projectPath string) (int, error) {
	_, slides, _, err := readValidatedTemplateFillPlan(projectPath)
	if err != nil {
		return 0, err
	}
	return len(slides), nil
}

func readValidatedTemplateFillPlan(projectPath string) (TemplateFillInputs, []any, string, error) {
	inputs, slides, status, _, err := readValidatedTemplateFillPlanWithSHA256(projectPath)
	return inputs, slides, status, err
}

func readValidatedTemplateFillPlanWithSHA256(projectPath string) (TemplateFillInputs, []any, string, string, error) {
	provenance, err := snapshotTemplateFillSourceProvenance(projectPath)
	if err != nil {
		return TemplateFillInputs{}, nil, "", "", err
	}
	return readValidatedTemplateFillPlanWithSHA256AndProvenance(projectPath, provenance)
}

func readValidatedTemplateFillPlanWithSHA256AndProvenance(projectPath string, provenance templateFillSourceProvenance) (TemplateFillInputs, []any, string, string, error) {
	inputs, err := discoverTemplateFillInputsWithProvenance(projectPath, provenance)
	if err != nil {
		return TemplateFillInputs{}, nil, "", "", err
	}
	plan, planSHA256, err := readTemplateFillJSONObjectWithSHA256(inputs.FillPlan, "template fill plan")
	if err != nil {
		return TemplateFillInputs{}, nil, "", "", err
	}
	if schema, ok := plan["schema"].(string); !ok || schema != "template_fill_pptx_plan.v1" {
		return TemplateFillInputs{}, nil, "", "", fmt.Errorf("template fill plan schema = %#v, expected %q", plan["schema"], "template_fill_pptx_plan.v1")
	}
	status, ok := plan["status"].(string)
	if !ok || (status != "draft" && status != "confirmed") {
		return TemplateFillInputs{}, nil, "", "", fmt.Errorf("template fill plan status = %#v, expected %q or %q", plan["status"], "draft", "confirmed")
	}
	if err := validateTemplateFillSourcePPTX(plan["source_pptx"], inputs); err != nil {
		return TemplateFillInputs{}, nil, "", "", err
	}

	slides, ok := plan["slides"].([]any)
	if !ok || len(slides) == 0 {
		return TemplateFillInputs{}, nil, "", "", fmt.Errorf("template fill plan slides must be a non-empty array")
	}
	sourceSlides, err := readTemplateFillSlideIndexes(inputs.SlideLibrary)
	if err != nil {
		return TemplateFillInputs{}, nil, "", "", err
	}
	for index, rawSlide := range slides {
		slide, ok := rawSlide.(map[string]any)
		if !ok {
			return TemplateFillInputs{}, nil, "", "", fmt.Errorf("template fill plan slides[%d] must be an object", index)
		}
		sourceSlide, err := templateFillPositiveInteger(slide["source_slide"], fmt.Sprintf("template fill plan slides[%d].source_slide", index))
		if err != nil {
			return TemplateFillInputs{}, nil, "", "", err
		}
		if _, ok := sourceSlides[sourceSlide]; !ok {
			return TemplateFillInputs{}, nil, "", "", fmt.Errorf("template fill plan slides[%d].source_slide %d is not present in slide library", index, sourceSlide)
		}
		if err := requireTemplateFillNonEmptyString(slide, "purpose", fmt.Sprintf("template fill plan slides[%d]", index)); err != nil {
			return TemplateFillInputs{}, nil, "", "", err
		}
		layout, ok := slide["layout_rationale"].(map[string]any)
		if !ok {
			return TemplateFillInputs{}, nil, "", "", fmt.Errorf("template fill plan slides[%d].layout_rationale must be an object", index)
		}
		for _, field := range []string{"layout_pattern", "why_fit", "risk"} {
			if err := requireTemplateFillNonEmptyString(layout, field, fmt.Sprintf("template fill plan slides[%d].layout_rationale", index)); err != nil {
				return TemplateFillInputs{}, nil, "", "", err
			}
		}
		for _, field := range []string{"replacements", "table_edits", "chart_edits"} {
			if value, exists := slide[field]; exists {
				if _, ok := value.([]any); !ok {
					return TemplateFillInputs{}, nil, "", "", fmt.Errorf("template fill plan slides[%d].%s must be an array", index, field)
				}
			}
		}
		if value, exists := slide["notes"]; exists {
			if _, ok := value.(string); !ok {
				return TemplateFillInputs{}, nil, "", "", fmt.Errorf("template fill plan slides[%d].notes must be a string", index)
			}
		}
	}
	return inputs, slides, status, planSHA256, nil
}

func readTemplateFillJSONObject(path, label string) (map[string]any, error) {
	object, _, err := readTemplateFillJSONObjectWithSHA256(path, label)
	return object, err
}

func readTemplateFillJSONObjectWithSHA256(path, label string) (map[string]any, string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, "", fmt.Errorf("read %s: %w", label, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, "", fmt.Errorf("read %s: path is not a regular non-symlinked file: %s", label, path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, "", fmt.Errorf("read %s: %w", label, err)
	}
	defer file.Close()
	raw, err := io.ReadAll(file)
	if err != nil {
		return nil, "", fmt.Errorf("read %s: %w", label, err)
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, "", fmt.Errorf("parse %s: %w", label, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, "", fmt.Errorf("parse %s: multiple JSON values", label)
		}
		return nil, "", fmt.Errorf("parse %s: %w", label, err)
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, "", fmt.Errorf("parse %s: root must be a JSON object", label)
	}
	digest := sha256.Sum256(raw)
	return object, fmt.Sprintf("%x", digest), nil
}

func validateTemplateFillSourcePPTX(value any, inputs TemplateFillInputs) error {
	sourcePath, ok := value.(string)
	if !ok || sourcePath == "" {
		return fmt.Errorf("template fill plan source_pptx must be a project-relative string")
	}
	expectedPath, err := filepath.Rel(inputs.ProjectPath, inputs.SourcePPTX)
	if err != nil {
		return fmt.Errorf("resolve template fill plan source_pptx: %w", err)
	}
	expectedPath = filepath.ToSlash(expectedPath)
	if filepath.IsAbs(sourcePath) || sourcePath != expectedPath {
		return fmt.Errorf("template fill plan source_pptx = %q, expected canonical project-relative path %q", sourcePath, expectedPath)
	}
	candidatePath := filepath.Join(inputs.ProjectPath, filepath.FromSlash(sourcePath))
	info, resolvedPath, err := inspectContainedPath(inputs.ProjectPath, candidatePath)
	if err != nil {
		return fmt.Errorf("validate template fill plan source_pptx %q: %w", sourcePath, err)
	}
	if !info.Mode().IsRegular() || resolvedPath != inputs.SourcePPTX {
		return fmt.Errorf("template fill plan source_pptx %q does not match discovered source %q", sourcePath, expectedPath)
	}
	return nil
}

func readTemplateFillSlideIndexes(path string) (map[int]struct{}, error) {
	library, err := readTemplateFillJSONObject(path, "template fill slide library")
	if err != nil {
		return nil, err
	}
	slides, ok := library["slides"].([]any)
	if !ok {
		return nil, fmt.Errorf("template fill slide library slides must be an array")
	}
	indexes := make(map[int]struct{}, len(slides))
	for index, rawSlide := range slides {
		slide, ok := rawSlide.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("template fill slide library slides[%d] must be an object", index)
		}
		slideIndex, err := templateFillPositiveInteger(slide["slide_index"], fmt.Sprintf("template fill slide library slides[%d].slide_index", index))
		if err != nil {
			return nil, err
		}
		indexes[slideIndex] = struct{}{}
	}
	return indexes, nil
}

func requireTemplateFillNonEmptyString(object map[string]any, field, context string) error {
	value, ok := object[field].(string)
	if !ok || strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s.%s must be a non-empty string", context, field)
	}
	return nil
}

func templateFillPositiveInteger(value any, field string) (int, error) {
	integer, err := templateFillNonNegativeInteger(value, field)
	if err != nil {
		return 0, err
	}
	if integer == 0 {
		return 0, fmt.Errorf("%s must be a positive integer", field)
	}
	return integer, nil
}

func templateFillNonNegativeInteger(value any, field string) (int, error) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, fmt.Errorf("%s must be a non-negative integer", field)
	}
	parsed, err := number.Int64()
	if err != nil || parsed < 0 || int64(int(parsed)) != parsed {
		return 0, fmt.Errorf("%s must be a non-negative integer", field)
	}
	return int(parsed), nil
}

func templateFillSummary(report map[string]any, label string, fields ...string) (map[string]any, error) {
	rawSummary, ok := report["summary"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s summary must be an object", label)
	}
	summary := make(map[string]any, len(rawSummary))
	for key, value := range rawSummary {
		summary[key] = value
	}
	for _, field := range fields {
		count, err := templateFillNonNegativeInteger(rawSummary[field], label+" summary."+field)
		if err != nil {
			return nil, err
		}
		summary[field] = count
	}
	return summary, nil
}

func writeTemplateFillContractReport(projectPath string, phase PipelinePhase, contract map[string]any) (string, error) {
	canonicalProjectPath, err := resolveTemplateFillProjectPath(projectPath)
	if err != nil {
		return "", fmt.Errorf("resolve template fill contract report project: %w", err)
	}
	reportName := string(phase)
	if reportName == "" || filepath.Base(reportName) != reportName || reportName == "." || reportName == ".." {
		return "", fmt.Errorf("invalid template fill contract report name: %q", reportName)
	}
	reportPath := filepath.Join(canonicalProjectPath, ".slidesmith", "contracts", reportName+".json")
	if !pathWithinRoot(canonicalProjectPath, reportPath) {
		return "", fmt.Errorf("template fill contract report path is outside project: %s", reportPath)
	}
	if err := prepareTemplateFillContractReportPath(canonicalProjectPath, reportPath); err != nil {
		return "", err
	}
	return writeContractReport(canonicalProjectPath, reportName, contract)
}

func prepareTemplateFillContractReportPath(projectPath, reportPath string) error {
	relativePath, err := filepath.Rel(projectPath, reportPath)
	if err != nil || filepath.IsAbs(relativePath) || relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
		return fmt.Errorf("template fill contract report path is outside project: %s", reportPath)
	}

	currentPath := projectPath
	parentPath := filepath.Dir(relativePath)
	for _, component := range strings.Split(parentPath, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		if component == ".." {
			return fmt.Errorf("template fill contract report parent is outside project: %s", reportPath)
		}
		currentPath = filepath.Join(currentPath, component)
		info, err := os.Lstat(currentPath)
		if os.IsNotExist(err) {
			if err := os.Mkdir(currentPath, 0o755); err != nil && !os.IsExist(err) {
				return fmt.Errorf("create template fill contract report parent %s: %w", currentPath, err)
			}
			info, err = os.Lstat(currentPath)
		}
		if err != nil {
			return fmt.Errorf("inspect template fill contract report parent %s: %w", currentPath, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("template fill contract report parent contains symlink: %s", currentPath)
		}
		if !info.IsDir() {
			return fmt.Errorf("template fill contract report parent is not a directory: %s", currentPath)
		}
	}

	info, err := os.Lstat(reportPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect template fill contract report destination %s: %w", reportPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("template fill contract report destination is a symlink: %s", reportPath)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("template fill contract report destination is not a regular file: %s", reportPath)
	}
	return nil
}

func rejectTemplateFillMainRouteOutputs(projectPath string) error {
	for _, name := range []string{"design_spec.md", "spec_lock.md"} {
		path := filepath.Join(projectPath, name)
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("template fill must not create %s", name)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect forbidden template fill output %s: %w", name, err)
		}
	}
	for _, name := range []string{"svg_output", "svg_final"} {
		if err := rejectTemplateFillSVGFiles(projectPath, name); err != nil {
			return err
		}
	}
	return nil
}

func rejectTemplateFillSVGFiles(projectPath, directory string) error {
	root := filepath.Join(projectPath, directory)
	info, err := os.Lstat(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect template fill %s: %w", directory, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("template fill %s must be a non-symlinked directory", directory)
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("template fill output path contains symlink: %s", path)
		}
		if entry.IsDir() {
			return nil
		}
		if strings.EqualFold(filepath.Ext(entry.Name()), ".svg") {
			relativePath, err := filepath.Rel(projectPath, path)
			if err != nil {
				return err
			}
			return fmt.Errorf("template fill must not create SVG file %s", filepath.ToSlash(relativePath))
		}
		return nil
	})
}

func latestTemplateFillExport(exportsPath string) (string, error) {
	files, err := listRegularFiles(exportsPath, "*.pptx")
	if err != nil {
		return "", fmt.Errorf("list template fill exports/*.pptx: %w", err)
	}
	if len(files) == 0 {
		return "", fmt.Errorf("template fill apply requires exports/*.pptx")
	}
	latestPath := ""
	var latestModified time.Time
	for _, path := range files {
		info, err := os.Lstat(path)
		if err != nil {
			return "", fmt.Errorf("inspect template fill export %s: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", fmt.Errorf("template fill export must be a regular non-symlinked file: %s", path)
		}
		if latestPath == "" || info.ModTime().After(latestModified) || (info.ModTime().Equal(latestModified) && path > latestPath) {
			latestPath = path
			latestModified = info.ModTime()
		}
	}
	return latestPath, nil
}
