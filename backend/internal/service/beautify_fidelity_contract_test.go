package service

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateBeautifyFidelityReportPassesExactFrozenLedger(t *testing.T) {
	fixture := newBeautifyContractFixture(t, 2)
	lock := fixture.buildLock(t)
	outputSHA := strings.Repeat("c", 64)
	writePassingBeautifyFidelityReport(t, fixture, lock, outputSHA)
	contract, err := ValidateBeautifyFidelityReport(fixture.projectPath, fixture.taskID, outputSHA)
	if err != nil {
		t.Fatal(err)
	}
	if contract.Decision != "pass" || contract.SourceSlideCount != 2 || contract.OutputSlideCount != 2 || contract.BeautifyLockSHA256 == "" {
		t.Fatalf("fidelity contract = %#v", contract)
	}
}

func TestValidateBeautifyFidelityReportRejectsTextLossAndWrongOutput(t *testing.T) {
	fixture := newBeautifyContractFixture(t, 1)
	lock := fixture.buildLock(t)
	outputSHA := strings.Repeat("c", 64)
	report := writePassingBeautifyFidelityReport(t, fixture, lock, outputSHA)
	report.Pages[0].Text.Matched = 0
	report.Pages[0].Text.Missing = []string{"text.p01.title"}
	report.Pages[0].Decision = "fail"
	report.Findings = []qualityFinding{{
		ID: "beautify.text.missing.p01", Stage: "pptx_validate", Rule: "beautify.text_missing",
		Severity: "error", Status: "open", PageID: "P01", Message: "missing text",
		Evidence: map[string]any{}, Remediation: map[string]any{"owner_phase": "svg_execute"},
	}}
	report.Summary = qualityGateSummary{Error: 1, Decision: "fail"}
	report.Decision = "fail"
	writeBeautifyTestJSON(t, filepath.Join(fixture.projectPath, "validation", "beautify_fidelity_report.json"), report)
	if _, err := ValidateBeautifyFidelityReport(fixture.projectPath, fixture.taskID, outputSHA); err == nil || !strings.Contains(err.Error(), "does not preserve") {
		t.Fatalf("text loss error = %v", err)
	}

	writePassingBeautifyFidelityReport(t, fixture, lock, outputSHA)
	if _, err := ValidateBeautifyFidelityReport(fixture.projectPath, fixture.taskID, strings.Repeat("d", 64)); err == nil || !strings.Contains(err.Error(), "hash binding") {
		t.Fatalf("output hash error = %v", err)
	}
}

func TestValidateBeautifyFidelityReportRejectsCrossPageCompensation(t *testing.T) {
	fixture := newBeautifyContractFixture(t, 2)
	lock := fixture.buildLock(t)
	outputSHA := strings.Repeat("e", 64)
	report := writePassingBeautifyFidelityReport(t, fixture, lock, outputSHA)
	report.Pages[0].Text.Matched = 0
	report.Pages[1].Text.Expected = 2
	report.Pages[1].Text.Matched = 2
	writeBeautifyTestJSON(t, filepath.Join(fixture.projectPath, "validation", "beautify_fidelity_report.json"), report)
	if _, err := ValidateBeautifyFidelityReport(fixture.projectPath, fixture.taskID, outputSHA); err == nil || !strings.Contains(err.Error(), "page 1") {
		t.Fatalf("cross-page error = %v", err)
	}
}

func TestValidateBeautifyFidelityReportBindsSourceImageHashAndSize(t *testing.T) {
	fixture := newBeautifyContractFixture(t, 1)
	addBeautifySourceImageToFixture(t, fixture)
	lock := fixture.buildLock(t)
	outputSHA := strings.Repeat("f", 64)
	report := writePassingBeautifyFidelityReport(t, fixture, lock, outputSHA)
	if _, err := ValidateBeautifyFidelityReport(fixture.projectPath, fixture.taskID, outputSHA); err != nil {
		t.Fatal(err)
	}
	report.Pages[0].Images.SourceBindings[0].SHA256 = strings.Repeat("0", 64)
	writeBeautifyTestJSON(t, filepath.Join(fixture.projectPath, "validation", "beautify_fidelity_report.json"), report)
	if _, err := ValidateBeautifyFidelityReport(fixture.projectPath, fixture.taskID, outputSHA); err == nil || !strings.Contains(err.Error(), "does not preserve") {
		t.Fatalf("source image fidelity binding error = %v", err)
	}
}

func writePassingBeautifyFidelityReport(t *testing.T, fixture *beautifyContractFixture, lock *BeautifyLock, outputSHA string) BeautifyFidelityReport {
	t.Helper()
	lockSHA, _ := sha256File(filepath.Join(fixture.projectPath, ".slidesmith", "beautify_lock.json"))
	report := BeautifyFidelityReport{
		Schema: beautifyFidelityReportSchema, TaskID: fixture.taskID,
		SourcePPTXSHA256: lock.SourcePPTXSHA256, OutputPPTXSHA256: outputSHA, BeautifyLockSHA256: lockSHA,
		SourceSlideCount: lock.SlideCount, OutputSlideCount: lock.SlideCount,
		Identity: BeautifyFidelityIdentity{SelectedSource: lock.Identity.Source, Overrides: []string{}, FontSubstitutions: []string{}},
		Ignored:  append([]BeautifyLockDecision(nil), lock.Ignored...), Unsupported: append([]BeautifyLockDecision(nil), lock.Unsupported...),
		Findings: []qualityFinding{}, Summary: qualityGateSummary{Decision: "pass"}, Decision: "pass",
	}
	for _, slide := range lock.Slides {
		requiredImages := 0
		imageBindings := []BeautifyFidelityImageBinding{}
		for _, image := range slide.Images {
			if image.Required {
				requiredImages++
				imageBindings = append(imageBindings, BeautifyFidelityImageBinding{ID: image.ID, SHA256: image.SHA256, Size: image.Size})
			}
		}
		report.Pages = append(report.Pages, BeautifyFidelityPage{
			SourceSlide: slide.SourceSlide, OutputPage: slide.OutputPage,
			Text:     BeautifyFidelityText{Expected: beautifyExpectedTextBlockCount(slide.TextBlocks), Matched: beautifyExpectedTextBlockCount(slide.TextBlocks), Missing: []string{}, Changed: []string{}, Reordered: []string{}},
			Tables:   BeautifyFidelityCollection{Expected: len(slide.Tables), Matched: len(slide.Tables), Mismatches: []string{}},
			Charts:   BeautifyFidelityCollection{Expected: len(slide.Charts), Matched: len(slide.Charts), Mismatches: []string{}},
			Images:   BeautifyFidelityImages{Required: requiredImages, Used: requiredImages, Missing: []string{}, SourceBindings: imageBindings},
			Decision: "pass",
		})
	}
	writeBeautifyTestJSON(t, filepath.Join(fixture.projectPath, "validation", "beautify_fidelity_report.json"), report)
	return report
}
