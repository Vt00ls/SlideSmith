package service

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateBeautifyPlanContractRequiresOneToOneExactAccounting(t *testing.T) {
	fixture := newBeautifyContractFixture(t, 2)
	contract := fixture.validatePlan(t)
	if contract.SlideCount != 2 || contract.PlanStatus != "confirmed" || contract.PlanRevision != 1 {
		t.Fatalf("plan contract = %#v", contract)
	}

	fixture = newBeautifyContractFixture(t, 2)
	fixture.plan.Slides[1].OutputPage = 1
	writeBeautifyTestJSON(t, filepath.Join(fixture.projectPath, "analysis", "beautify_plan.json"), fixture.plan)
	if _, err := ValidateBeautifyPlanContract(fixture.projectPath, fixture.taskID); err == nil || !strings.Contains(err.Error(), "1:1") {
		t.Fatalf("mapping error = %v", err)
	}

	fixture = newBeautifyContractFixture(t, 1)
	fixture.plan.Slides[0].TextBlockIDs = nil
	writeBeautifyTestJSON(t, filepath.Join(fixture.projectPath, "analysis", "beautify_plan.json"), fixture.plan)
	if _, err := ValidateBeautifyPlanContract(fixture.projectPath, fixture.taskID); err == nil || !strings.Contains(err.Error(), "does not account") {
		t.Fatalf("accounting error = %v", err)
	}
}

func TestValidateBeautifyPlanContractRejectsIgnoringFrozenText(t *testing.T) {
	fixture := newBeautifyContractFixture(t, 1)
	id := fixture.plan.Slides[0].TextBlockIDs[0]
	fixture.plan.Slides[0].TextBlockIDs = nil
	fixture.plan.Slides[0].Ignored = []BeautifyContentRef{{ID: id, Reason: "try to drop text"}}
	writeBeautifyTestJSON(t, filepath.Join(fixture.projectPath, "analysis", "beautify_plan.json"), fixture.plan)
	if _, err := ValidateBeautifyPlanContract(fixture.projectPath, fixture.taskID); err == nil || !strings.Contains(err.Error(), "frozen text") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateBeautifyPlanContractRequiresRiskAcceptanceWhenConfirmed(t *testing.T) {
	fixture := newBeautifyContractFixture(t, 1)
	fixture.risk.Risks = []BeautifyRiskFinding{{
		ID: "risk.p01.hidden", SlideIndex: 1, Rule: "object.hidden", Severity: "warning",
		NeedsConfirmation: true, Message: "hidden slide",
	}}
	writeBeautifyTestJSON(t, filepath.Join(fixture.projectPath, "analysis", "beautify_risk_report.json"), fixture.risk)
	if _, err := ValidateBeautifyInventoryContract(fixture.projectPath, fixture.taskID); err != nil {
		t.Fatal(err)
	}
	fixture.plan.Slides[0].Risks = []string{"risk.p01.hidden"}
	writeBeautifyTestJSON(t, filepath.Join(fixture.projectPath, "analysis", "beautify_plan.json"), fixture.plan)
	if _, err := ValidateBeautifyPlanContract(fixture.projectPath, fixture.taskID); err == nil || !strings.Contains(err.Error(), "unaccepted risk") {
		t.Fatalf("error = %v", err)
	}
}

func TestCheckBeautifyCAS(t *testing.T) {
	hash := strings.Repeat("a", 64)
	if err := CheckBeautifyCAS(hash, 2, hash, 2); err != nil {
		t.Fatal(err)
	}
	if err := CheckBeautifyCAS(hash, 2, strings.Repeat("b", 64), 2); err == nil || !strings.Contains(err.Error(), "concurrently") {
		t.Fatalf("hash CAS error = %v", err)
	}
	if err := CheckBeautifyCAS(hash, 2, hash, 1); err == nil || !strings.Contains(err.Error(), "concurrently") {
		t.Fatalf("revision CAS error = %v", err)
	}
}
