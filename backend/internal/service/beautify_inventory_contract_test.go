package service

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateBeautifyInventoryContractBindsPageHashesAndRisks(t *testing.T) {
	fixture := newBeautifyContractFixture(t, 2)
	contract, err := validateExistingBeautifyInventoryContract(fixture.projectPath, fixture.taskID)
	if err != nil {
		t.Fatal(err)
	}
	if contract.SlideCount != 2 || len(contract.Pages) != 2 || !beautifySHA256Pattern.MatchString(contract.Pages[0].SHA256) {
		t.Fatalf("inventory contract = %#v", contract)
	}
}

func TestValidateBeautifyInventoryContractRejectsDuplicateIDAndEscapingImage(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*beautifyContractFixture)
		want   string
	}{
		{
			name: "duplicate",
			mutate: func(f *beautifyContractFixture) {
				f.inventory.Slides[1].TextBlocks[0].ID = f.inventory.Slides[0].TextBlocks[0].ID
			},
			want: "globally unique",
		},
		{
			name: "escaping image",
			mutate: func(f *beautifyContractFixture) {
				f.inventory.Slides[0].Images = []BeautifyInventoryImage{{
					ID: "image.p01.hero", SourceOccurrence: "occ-1", SourcePath: "../secret.png",
					SHA256: strings.Repeat("a", 64), Size: 1, Required: true,
				}}
			},
			want: "escapes project",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newBeautifyContractFixture(t, 2)
			test.mutate(fixture)
			writeBeautifyTestJSON(t, filepath.Join(fixture.projectPath, "analysis", "beautify_inventory.json"), fixture.inventory)
			inventorySHA, _ := sha256File(filepath.Join(fixture.projectPath, "analysis", "beautify_inventory.json"))
			fixture.risk.InventorySHA256 = inventorySHA
			writeBeautifyTestJSON(t, filepath.Join(fixture.projectPath, "analysis", "beautify_risk_report.json"), fixture.risk)
			if _, err := ValidateBeautifyInventoryContract(fixture.projectPath, fixture.taskID); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateBeautifyInventoryContractRejectsSourceImageMutation(t *testing.T) {
	fixture := newBeautifyContractFixture(t, 1)
	image := addBeautifySourceImageToFixture(t, fixture)
	if _, err := validateExistingBeautifyInventoryContract(fixture.projectPath, fixture.taskID); err != nil {
		t.Fatal(err)
	}
	writeBeautifyTestFile(t, filepath.Join(fixture.projectPath, filepath.FromSlash(image.SourcePath)), "mutated-image")
	if _, err := validateExistingBeautifyInventoryContract(fixture.projectPath, fixture.taskID); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("source image mutation error = %v", err)
	}
}

func TestValidateBeautifyInventoryContractRejectsUnknownRiskItem(t *testing.T) {
	fixture := newBeautifyContractFixture(t, 1)
	fixture.risk.Risks = []BeautifyRiskFinding{{
		ID: "risk.p01.unknown", SlideIndex: 1, Rule: "object.unknown", Severity: "warning",
		ItemIDs: []string{"missing"}, NeedsConfirmation: true, Message: "unknown",
	}}
	writeBeautifyTestJSON(t, filepath.Join(fixture.projectPath, "analysis", "beautify_risk_report.json"), fixture.risk)
	if _, err := ValidateBeautifyInventoryContract(fixture.projectPath, fixture.taskID); err == nil || !strings.Contains(err.Error(), "unknown item") {
		t.Fatalf("error = %v", err)
	}
}
