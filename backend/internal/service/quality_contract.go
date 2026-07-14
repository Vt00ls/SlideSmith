package service

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	qualitySummarySchema       = "slidesmith.quality_summary.v1"
	svgQualityReportSchema     = "slidesmith.svg_quality_report.v1"
	chartVerifyReportSchema    = "slidesmith.chart_verify_report.v1"
	qualityCheckContractSchema = "slidesmith.quality_check_contract.v1"
)

type qualityFinding struct {
	ID          string         `json:"id"`
	Stage       string         `json:"stage"`
	Rule        string         `json:"rule"`
	Severity    string         `json:"severity"`
	Status      string         `json:"status"`
	PageID      string         `json:"page_id"`
	Artifact    string         `json:"artifact"`
	ElementIDs  []string       `json:"element_ids"`
	Message     string         `json:"message"`
	Evidence    map[string]any `json:"evidence"`
	Remediation map[string]any `json:"remediation"`
}

type qualityGateSummary struct {
	Blocking int    `json:"blocking"`
	Error    int    `json:"error"`
	Warning  int    `json:"warning"`
	Info     int    `json:"info"`
	Decision string `json:"decision"`
}

type qualityReportRef struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type qualitySummaryDocument struct {
	Schema          string             `json:"schema"`
	Stage           string             `json:"stage"`
	TaskID          string             `json:"task_id"`
	PhaseRunID      string             `json:"phase_run_id"`
	SVGOutputSHA256 string             `json:"svg_output_sha256"`
	Reports         []qualityReportRef `json:"reports"`
	Summary         qualityGateSummary `json:"summary"`
	Decision        string             `json:"decision"`
}

type detailedQualityReport struct {
	Schema     string             `json:"schema"`
	TaskID     string             `json:"task_id"`
	PhaseRunID string             `json:"phase_run_id"`
	Findings   []qualityFinding   `json:"findings"`
	Summary    qualityGateSummary `json:"summary"`
}

func validateQualityCheckContract(projectPath string) (map[string]any, error) {
	return validateQualityCheckContractForRun(projectPath, "")
}

func validateQualityCheckContractForRun(projectPath, expectedPhaseRunID string) (map[string]any, error) {
	paths := map[string]string{
		"svg":      filepath.Join(projectPath, "validation", "svg_quality_report.json"),
		"chart":    filepath.Join(projectPath, "validation", "chart_verify_report.json"),
		"summary":  filepath.Join(projectPath, "validation", "quality_summary.json"),
		"pointer":  filepath.Join(projectPath, ".slidesmith", "quality_report.json"),
		"contract": filepath.Join(projectPath, ".slidesmith", "contracts", string(PhaseQualityCheck)+".json"),
	}
	for _, path := range paths {
		if err := requireContainedContractFile(projectPath, path); err != nil {
			return nil, err
		}
	}
	var summary qualitySummaryDocument
	if err := readJSONContract(paths["summary"], &summary); err != nil {
		return nil, fmt.Errorf("decode quality summary: %w", err)
	}
	if summary.Schema != qualitySummarySchema || summary.Stage != "svg_pre_export" {
		return nil, fmt.Errorf("quality summary schema/stage is invalid")
	}
	if expectedPhaseRunID != "" && summary.PhaseRunID != expectedPhaseRunID {
		return nil, fmt.Errorf("quality summary phase_run_id = %q, expected %q", summary.PhaseRunID, expectedPhaseRunID)
	}
	if err := validateQualityDecision(summary.Summary, summary.Decision); err != nil {
		return nil, err
	}
	if summary.Summary.Blocking != 0 || summary.Summary.Error != 0 {
		return nil, fmt.Errorf("quality gate rejected export: blocking=%d error=%d", summary.Summary.Blocking, summary.Summary.Error)
	}
	expectedReports := map[string]string{
		"svg":   "validation/svg_quality_report.json",
		"chart": "validation/chart_verify_report.json",
	}
	if len(summary.Reports) != len(expectedReports) {
		return nil, fmt.Errorf("quality summary report count = %d", len(summary.Reports))
	}
	for _, report := range summary.Reports {
		expectedPath, ok := expectedReports[report.Kind]
		if !ok || report.Path != expectedPath {
			return nil, fmt.Errorf("quality summary contains invalid report binding %q", report.Path)
		}
		actual, err := sha256File(filepath.Join(projectPath, filepath.FromSlash(report.Path)))
		if err != nil || actual != report.SHA256 {
			return nil, fmt.Errorf("quality report %s hash is stale", report.Kind)
		}
	}
	for kind, item := range map[string]struct {
		path   string
		schema string
	}{
		"svg":   {paths["svg"], svgQualityReportSchema},
		"chart": {paths["chart"], chartVerifyReportSchema},
	} {
		var report detailedQualityReport
		if err := readJSONContract(item.path, &report); err != nil {
			return nil, fmt.Errorf("decode %s quality report: %w", kind, err)
		}
		if report.Schema != item.schema || report.TaskID != summary.TaskID || report.PhaseRunID != summary.PhaseRunID {
			return nil, fmt.Errorf("%s quality report binding mismatch", kind)
		}
		if err := validateQualityFindings(report.Findings, report.Summary); err != nil {
			return nil, fmt.Errorf("%s quality report: %w", kind, err)
		}
	}
	liveSVGHash, err := sha256RegularFiles(filepath.Join(projectPath, "svg_output"), "*.svg")
	if err != nil || liveSVGHash != summary.SVGOutputSHA256 {
		return nil, fmt.Errorf("quality summary SVG hash is stale")
	}
	var contract map[string]any
	raw, err := os.ReadFile(paths["contract"])
	if err != nil || json.Unmarshal(raw, &contract) != nil {
		return nil, fmt.Errorf("quality contract is invalid")
	}
	if valueString(contract, "schema", "") != qualityCheckContractSchema || valueString(contract, "decision", "") != summary.Decision {
		return nil, fmt.Errorf("quality contract schema/decision mismatch")
	}
	if expectedPhaseRunID != "" && valueString(contract, "phase_run_id", "") != expectedPhaseRunID {
		return nil, fmt.Errorf("quality contract phase run is stale")
	}
	qualitySHA, err := sha256File(paths["summary"])
	if err != nil || valueString(contract, "quality_summary_sha256", "") != qualitySHA {
		return nil, fmt.Errorf("quality contract summary hash is stale")
	}
	contract["phase"] = string(PhaseQualityCheck)
	contract["project_path"] = projectPath
	contract["expected_pages"] = confirmedPageCount(projectPath)
	contract["checked_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	return contract, nil
}

func validateQualityDecision(summary qualityGateSummary, decision string) error {
	if min(summary.Blocking, summary.Error, summary.Warning, summary.Info) < 0 {
		return fmt.Errorf("quality summary contains negative counts")
	}
	want := "pass"
	if summary.Blocking > 0 || summary.Error > 0 {
		want = "fail"
	} else if summary.Warning > 0 {
		want = "pass_with_warnings"
	}
	if summary.Decision != want || decision != want {
		return fmt.Errorf("quality decision = %q/%q, expected %q", summary.Decision, decision, want)
	}
	return nil
}

func validateQualityFindings(findings []qualityFinding, summary qualityGateSummary) error {
	counts := qualityGateSummary{}
	for _, finding := range findings {
		if finding.ID == "" || finding.Rule == "" || finding.Rule != strings.ToLower(finding.Rule) || !strings.Contains(finding.Rule, ".") {
			return fmt.Errorf("finding has invalid stable identity/rule")
		}
		if finding.Status != "open" && finding.Status != "fixed_by_retry" && finding.Status != "accepted_by_policy" {
			return fmt.Errorf("finding %s has invalid status", finding.ID)
		}
		switch finding.Severity {
		case "blocking":
			counts.Blocking++
		case "error":
			counts.Error++
		case "warning":
			counts.Warning++
		case "info":
			counts.Info++
		default:
			return fmt.Errorf("finding %s has invalid severity", finding.ID)
		}
		if len(finding.Message) > 320 || strings.ContainsRune(finding.Message, '\x00') {
			return fmt.Errorf("finding %s has unsafe message", finding.ID)
		}
	}
	if counts.Blocking != summary.Blocking || counts.Error != summary.Error || counts.Warning != summary.Warning || counts.Info != summary.Info {
		return fmt.Errorf("finding counts do not match summary")
	}
	return nil
}

func min(values ...int) int {
	result := values[0]
	for _, value := range values[1:] {
		if value < result {
			result = value
		}
	}
	return result
}

func qualityContractFailurePhase(projectPath string, phase PipelinePhase) (string, []string) {
	path := ""
	switch phase {
	case PhaseQualityCheck:
		path = filepath.Join(projectPath, "validation", "svg_quality_report.json")
	case PhasePPTXValidate:
		path = filepath.Join(projectPath, "validation", "pptx_validate_report.json")
	default:
		return string(phase) + ".contract", nil
	}
	var report detailedQualityReport
	if err := readJSONContract(path, &report); err != nil {
		return string(phase) + ".contract", nil
	}
	rules := make([]string, 0, len(report.Findings))
	for _, finding := range report.Findings {
		if finding.Severity == "blocking" || finding.Severity == "error" {
			rules = append(rules, finding.Rule)
		}
	}
	if len(rules) == 0 {
		return string(phase) + ".contract", nil
	}
	rule := rules[0]
	if phase == PhaseQualityCheck {
		switch {
		case strings.HasPrefix(rule, "chart."):
			return "quality_check.chart_verify", rules
		case strings.HasPrefix(rule, "quality.upstream"), rule == "quality.stale_export":
			return "quality_check.upstream_contract", rules
		case strings.Contains(rule, "checker"):
			return "quality_check.svg_checker", rules
		default:
			return "quality_check.geometry", rules
		}
	}
	switch {
	case strings.Contains(rule, "text"):
		return "pptx_validate.text_fidelity", rules
	case rule == "pptx.render_blank":
		return "pptx_validate.blank_page", rules
	case strings.Contains(rule, "render"):
		return "pptx_validate.render", rules
	case strings.Contains(rule, "package"), strings.Contains(rule, "relationship"), strings.Contains(rule, "xml"), strings.Contains(rule, "part_missing"):
		return "pptx_validate.package", rules
	default:
		return "pptx_validate.contract", rules
	}
}
