package service

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type BeautifyFidelityReport struct {
	Schema             string                   `json:"schema"`
	TaskID             string                   `json:"task_id"`
	SourcePPTXSHA256   string                   `json:"source_pptx_sha256"`
	OutputPPTXSHA256   string                   `json:"output_pptx_sha256"`
	BeautifyLockSHA256 string                   `json:"beautify_lock_sha256"`
	SourceSlideCount   int                      `json:"source_slide_count"`
	OutputSlideCount   int                      `json:"output_slide_count"`
	Pages              []BeautifyFidelityPage   `json:"pages"`
	Identity           BeautifyFidelityIdentity `json:"identity"`
	Ignored            []BeautifyLockDecision   `json:"ignored"`
	Unsupported        []BeautifyLockDecision   `json:"unsupported"`
	Findings           []qualityFinding         `json:"findings"`
	Summary            qualityGateSummary       `json:"summary"`
	Decision           string                   `json:"decision"`
}

type BeautifyFidelityPage struct {
	SourceSlide int                        `json:"source_slide"`
	OutputPage  int                        `json:"output_page"`
	Text        BeautifyFidelityText       `json:"text"`
	Tables      BeautifyFidelityCollection `json:"tables"`
	Charts      BeautifyFidelityCollection `json:"charts"`
	Images      BeautifyFidelityImages     `json:"images"`
	Decision    string                     `json:"decision"`
}

type BeautifyFidelityText struct {
	Expected  int      `json:"expected"`
	Matched   int      `json:"matched"`
	Missing   []string `json:"missing"`
	Changed   []string `json:"changed"`
	Reordered []string `json:"reordered"`
}

type BeautifyFidelityCollection struct {
	Expected   int      `json:"expected"`
	Matched    int      `json:"matched"`
	Mismatches []string `json:"mismatches"`
}

type BeautifyFidelityImages struct {
	Required       int                            `json:"required"`
	Used           int                            `json:"used"`
	Missing        []string                       `json:"missing"`
	SourceBindings []BeautifyFidelityImageBinding `json:"source_bindings"`
}

type BeautifyFidelityImageBinding struct {
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type BeautifyFidelityIdentity struct {
	SelectedSource    string   `json:"selected_source"`
	Overrides         []string `json:"overrides"`
	FontSubstitutions []string `json:"font_substitutions"`
}

type BeautifyFidelityContract struct {
	Schema                  string             `json:"schema"`
	TaskID                  string             `json:"task_id"`
	SourcePPTXSHA256        string             `json:"source_pptx_sha256"`
	OutputPPTXSHA256        string             `json:"output_pptx_sha256"`
	BeautifyInputsSHA256    string             `json:"beautify_inputs_sha256"`
	BeautifyInventorySHA256 string             `json:"beautify_inventory_sha256"`
	BeautifyPlanSHA256      string             `json:"beautify_plan_sha256"`
	BeautifyLockSHA256      string             `json:"beautify_lock_sha256"`
	FidelityReportSHA256    string             `json:"beautify_fidelity_report_sha256"`
	SourceSlideCount        int                `json:"source_slide_count"`
	OutputSlideCount        int                `json:"output_slide_count"`
	Summary                 qualityGateSummary `json:"summary"`
	Decision                string             `json:"decision"`
	CheckedAt               string             `json:"checked_at"`
}

func ValidateBeautifyFidelityReport(projectPath, expectedTaskID, expectedOutputPPTXSHA string) (*BeautifyFidelityContract, error) {
	if !beautifySHA256Pattern.MatchString(expectedOutputPPTXSHA) {
		return nil, fmt.Errorf("expected output PPTX SHA-256 is invalid")
	}
	lock, err := ValidateBeautifyLock(projectPath, expectedTaskID)
	if err != nil {
		return nil, err
	}
	lockSHA, err := sha256File(filepath.Join(projectPath, ".slidesmith", "beautify_lock.json"))
	if err != nil {
		return nil, err
	}
	var report BeautifyFidelityReport
	if err := beautifyReadJSON(projectPath, "validation/beautify_fidelity_report.json", &report); err != nil {
		return nil, err
	}
	if report.Schema != beautifyFidelityReportSchema || report.TaskID != expectedTaskID {
		return nil, fmt.Errorf("beautify fidelity report schema/task binding is invalid")
	}
	if report.SourcePPTXSHA256 != lock.SourcePPTXSHA256 || report.OutputPPTXSHA256 != expectedOutputPPTXSHA || report.BeautifyLockSHA256 != lockSHA {
		return nil, fmt.Errorf("beautify fidelity source/output/lock hash binding mismatch")
	}
	if report.SourceSlideCount != lock.SlideCount || report.OutputSlideCount != lock.SlideCount || len(report.Pages) != lock.SlideCount {
		return nil, fmt.Errorf("beautify fidelity slide count mismatch")
	}
	if err := validateBeautifyFidelityPages(report.Pages, lock); err != nil {
		return nil, err
	}
	if err := validateQualityFindings(report.Findings, report.Summary); err != nil {
		return nil, fmt.Errorf("beautify fidelity findings: %w", err)
	}
	if err := validateQualityDecision(report.Summary, report.Decision); err != nil {
		return nil, err
	}
	if report.Summary.Blocking != 0 || report.Summary.Error != 0 || (report.Decision != "pass" && report.Decision != "pass_with_warnings") {
		return nil, fmt.Errorf("beautify fidelity gate rejected publish: blocking=%d error=%d", report.Summary.Blocking, report.Summary.Error)
	}
	if strings.TrimSpace(report.Identity.SelectedSource) != strings.TrimSpace(lock.Identity.Source) {
		return nil, fmt.Errorf("beautify fidelity identity source does not match lock")
	}
	if err := validateBeautifyFidelityDecisions(report.Ignored, lock.Ignored, "ignored"); err != nil {
		return nil, err
	}
	if err := validateBeautifyFidelityDecisions(report.Unsupported, lock.Unsupported, "unsupported"); err != nil {
		return nil, err
	}
	reportSHA, err := sha256File(filepath.Join(projectPath, "validation", "beautify_fidelity_report.json"))
	if err != nil {
		return nil, err
	}
	contract := &BeautifyFidelityContract{
		Schema: "slidesmith.beautify_fidelity_contract.v1", TaskID: expectedTaskID,
		SourcePPTXSHA256: lock.SourcePPTXSHA256, OutputPPTXSHA256: expectedOutputPPTXSHA,
		BeautifyInputsSHA256: lock.InputsSHA256, BeautifyInventorySHA256: lock.InventorySHA256,
		BeautifyPlanSHA256: lock.PlanSHA256, BeautifyLockSHA256: lockSHA,
		FidelityReportSHA256: reportSHA, SourceSlideCount: lock.SlideCount,
		OutputSlideCount: report.OutputSlideCount, Summary: report.Summary, Decision: report.Decision,
		CheckedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	return contract, nil
}

func validateBeautifyFidelityPages(pages []BeautifyFidelityPage, lock *BeautifyLock) error {
	for index, page := range pages {
		number := index + 1
		if page.SourceSlide != number || page.OutputPage != number {
			return fmt.Errorf("beautify fidelity page %d is not 1:1", number)
		}
		frozen := lock.Slides[index]
		expectedImages := 0
		expectedImageBindings := map[string]BeautifyFidelityImageBinding{}
		for _, image := range frozen.Images {
			if image.Required && !beautifyDecisionContains(lock.Ignored, number, image.ID) && !beautifyDecisionContains(lock.Unsupported, number, image.ID) {
				expectedImages++
				expectedImageBindings[image.ID] = BeautifyFidelityImageBinding{ID: image.ID, SHA256: image.SHA256, Size: image.Size}
			}
		}
		actualImageBindings := map[string]BeautifyFidelityImageBinding{}
		for _, binding := range page.Images.SourceBindings {
			if binding.ID == "" || actualImageBindings[binding.ID].ID != "" {
				return fmt.Errorf("beautify fidelity page %d image source bindings are invalid", number)
			}
			actualImageBindings[binding.ID] = binding
		}
		imageBindingsMatch := len(actualImageBindings) == len(expectedImageBindings)
		if imageBindingsMatch {
			for id, expected := range expectedImageBindings {
				if actualImageBindings[id] != expected {
					imageBindingsMatch = false
					break
				}
			}
		}
		valid := page.Text.Expected == beautifyExpectedTextBlockCount(frozen.TextBlocks) && page.Text.Matched == page.Text.Expected && len(page.Text.Missing) == 0 && len(page.Text.Changed) == 0 && len(page.Text.Reordered) == 0 &&
			page.Tables.Expected == len(frozen.Tables) && page.Tables.Matched == page.Tables.Expected && len(page.Tables.Mismatches) == 0 &&
			page.Charts.Expected == len(frozen.Charts) && page.Charts.Matched == page.Charts.Expected && len(page.Charts.Mismatches) == 0 &&
			page.Images.Required == expectedImages && page.Images.Used == expectedImages && len(page.Images.Missing) == 0 && imageBindingsMatch
		if !valid || page.Decision != "pass" {
			return fmt.Errorf("beautify fidelity page %d does not preserve frozen content/data", number)
		}
	}
	return nil
}

func beautifyDecisionContains(items []BeautifyLockDecision, page int, id string) bool {
	for _, item := range items {
		if item.SlideIndex == page && item.ID == id {
			return true
		}
	}
	return false
}

func validateBeautifyFidelityDecisions(actual, expected []BeautifyLockDecision, label string) error {
	actualCopy := append([]BeautifyLockDecision(nil), actual...)
	expectedCopy := append([]BeautifyLockDecision(nil), expected...)
	sortBeautifyLockDecisions(actualCopy)
	sortBeautifyLockDecisions(expectedCopy)
	actualSHA, _ := beautifyJSONSHA256(actualCopy)
	expectedSHA, _ := beautifyJSONSHA256(expectedCopy)
	if actualSHA != expectedSHA {
		return fmt.Errorf("beautify fidelity %s decisions do not match lock", label)
	}
	return nil
}

func sortedBeautifyStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}
