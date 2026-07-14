package service

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestQualityContractPassesAndRejectsStaleSummary(t *testing.T) {
	_, _, _, projectPath := retryTestService(t)
	mustWriteRetryProjectFiles(projectPath)
	writePassingQualityReportsNoTest(projectPath, "task-retry", "quality-contract")
	if _, err := validateQualityCheckContractForRun(projectPath, "quality-contract"); err != nil {
		t.Fatalf("validateQualityCheckContractForRun() error = %v", err)
	}
	mustWriteFileNoTest(projectPath, filepath.Join("validation", "quality_summary.json"), `{"schema":"slidesmith.quality_summary.v1","tampered":true}`+"\n")
	if _, err := validateQualityCheckContractForRun(projectPath, "quality-contract"); err == nil {
		t.Fatal("stale quality summary was accepted")
	}
}

func TestQualityDecisionBlocksErrorsAndAllowsWarnings(t *testing.T) {
	if err := validateQualityDecision(qualityGateSummary{Error: 1, Decision: "fail"}, "fail"); err != nil {
		t.Fatalf("valid failure decision rejected: %v", err)
	}
	if err := validateQualityDecision(qualityGateSummary{Warning: 1, Decision: "pass_with_warnings"}, "pass_with_warnings"); err != nil {
		t.Fatalf("warning decision rejected: %v", err)
	}
	if err := validateQualityDecision(qualityGateSummary{Error: 1, Decision: "pass"}, "pass"); err == nil {
		t.Fatal("error finding was allowed to pass")
	}
}

func TestFinalizeExportWritesCanonicalManifestAndRejectsAmbiguity(t *testing.T) {
	projectPath := t.TempDir()
	mustWriteFileNoTest(projectPath, filepath.Join("confirm_ui", "result.json"), `{"page_count":3}`+"\n")
	mustWritePPTXNoTest(projectPath, filepath.Join("exports", "result.pptx"), 3)
	contract, err := validatePPTXExportContract(projectPath)
	if err != nil {
		t.Fatalf("validatePPTXExportContract() error = %v", err)
	}
	canonical, ok := contract["canonical_pptx"].(map[string]any)
	if !ok || canonical["path"] != "exports/result.pptx" || canonical["sha256"] == "" {
		t.Fatalf("canonical PPTX = %#v", contract["canonical_pptx"])
	}
	assertPathExists(t, filepath.Join(projectPath, "exports", "export_manifest.json"))
	mustWritePPTXNoTest(projectPath, filepath.Join("exports", "other.pptx"), 3)
	if _, err := validatePPTXExportContract(projectPath); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("ambiguous export error = %v", err)
	}
}
