package service

import (
	"archive/zip"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

type beautifyContractFixture struct {
	projectPath string
	taskID      string
	inputs      *BeautifyInputsContract
	inventory   BeautifyInventoryDocument
	risk        BeautifyRiskReport
	plan        BeautifyPlanDocument
}

func newBeautifyContractFixture(t *testing.T, slides int) *beautifyContractFixture {
	t.Helper()
	projectPath := t.TempDir()
	for _, directory := range []string{"sources", "analysis", "images", "confirm_ui", "validation", filepath.Join(".slidesmith", "contracts")} {
		if err := os.MkdirAll(filepath.Join(projectPath, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeBeautifyTestPPTX(t, filepath.Join(projectPath, "sources", "deck.pptx"), slides)
	writeBeautifyTestFile(t, filepath.Join(projectPath, "sources", "deck.md"), "# Deck\n")
	writeBeautifyTestJSON(t, filepath.Join(projectPath, "analysis", "deck.identity.json"), map[string]any{"canvas": map[string]any{"width": 12192000, "height": 6858000}})
	library := make([]map[string]any, slides)
	for index := range library {
		library[index] = map[string]any{"slide_index": index + 1}
	}
	writeBeautifyTestJSON(t, filepath.Join(projectPath, "analysis", "deck.slide_library.json"), library)
	writeBeautifyTestJSON(t, filepath.Join(projectPath, "analysis", "source_profile.json"), map[string]any{"slide_count": slides})
	writeBeautifyTestJSON(t, filepath.Join(projectPath, "images", "image_manifest.json"), []map[string]any{{"filename": "source-image.png"}})
	writeBeautifyTestJSON(t, filepath.Join(projectPath, "confirm_ui", "result.json"), map[string]any{"page_count": slides, "canvas": "ppt169", "status": "confirmed"})
	inputs, err := BuildBeautifyInputsContract(projectPath, "beautify-task", model.RunnerProfileFullPPTMaster)
	if err != nil {
		t.Fatalf("BuildBeautifyInputsContract() error = %v", err)
	}
	fixture := &beautifyContractFixture{projectPath: projectPath, taskID: "beautify-task", inputs: inputs}
	fixture.inventory = BeautifyInventoryDocument{
		Schema: beautifyInventorySchema, TaskID: fixture.taskID,
		SourcePPTXSHA256: inputs.SourcePPTX.SHA256, SlideCount: slides,
	}
	for page := 1; page <= slides; page++ {
		fixture.inventory.Slides = append(fixture.inventory.Slides, BeautifyInventorySlide{
			SlideIndex: page,
			TextBlocks: []BeautifyInventoryText{{ID: fmt.Sprintf("text.p%02d.title", page), Role: "title", Text: fmt.Sprintf("Title %d", page)}},
			Tables:     []BeautifyInventoryTable{}, Charts: []BeautifyInventoryChart{}, Images: []BeautifyInventoryImage{},
			Ignored: []BeautifyContentRef{}, NeedsConfirmation: []BeautifyContentRef{},
		})
	}
	writeBeautifyTestJSON(t, filepath.Join(projectPath, "analysis", "beautify_inventory.json"), fixture.inventory)
	inputsSHA, _ := sha256File(filepath.Join(projectPath, ".slidesmith", "contracts", "beautify_inputs.json"))
	inventorySHA, _ := sha256File(filepath.Join(projectPath, "analysis", "beautify_inventory.json"))
	fixture.risk = BeautifyRiskReport{Schema: beautifyRiskReportSchema, TaskID: fixture.taskID, InputsSHA256: inputsSHA, InventorySHA256: inventorySHA, Risks: []BeautifyRiskFinding{}}
	writeBeautifyTestJSON(t, filepath.Join(projectPath, "analysis", "beautify_risk_report.json"), fixture.risk)
	if _, err := ValidateBeautifyInventoryContract(projectPath, fixture.taskID); err != nil {
		t.Fatalf("ValidateBeautifyInventoryContract() error = %v", err)
	}
	confirmationSHA, _ := sha256File(filepath.Join(projectPath, "confirm_ui", "result.json"))
	fixture.plan = BeautifyPlanDocument{
		Schema: beautifyPlanSchema, TaskID: fixture.taskID, Status: "confirmed", Revision: 1,
		SourcePPTXSHA256: inputs.SourcePPTX.SHA256, InventorySHA256: inventorySHA,
		ConfirmationSHA256: confirmationSHA, SlideCount: slides,
		Identity: BeautifyPlanIdentity{Source: "theme"}, GlobalIgnored: []BeautifyContentRef{}, AcceptedRisks: []string{},
	}
	for page := 1; page <= slides; page++ {
		fixture.plan.Slides = append(fixture.plan.Slides, BeautifyPlanSlide{
			SourceSlide: page, OutputPage: page, PageRole: "content", PageRhythm: "flow", LayoutStrategy: "re-layout without content changes",
			TextBlockIDs: []string{fmt.Sprintf("text.p%02d.title", page)}, ImageIDs: []string{}, TableIDs: []string{}, ChartIDs: []string{},
			Ignored: []BeautifyContentRef{}, Unsupported: []BeautifyContentRef{}, Risks: []string{},
		})
	}
	writeBeautifyTestJSON(t, filepath.Join(projectPath, "analysis", "beautify_plan.json"), fixture.plan)
	return fixture
}

func (fixture *beautifyContractFixture) validatePlan(t *testing.T) *BeautifyPlanContract {
	t.Helper()
	contract, err := ValidateBeautifyPlanContract(fixture.projectPath, fixture.taskID)
	if err != nil {
		t.Fatalf("ValidateBeautifyPlanContract() error = %v", err)
	}
	return contract
}

func (fixture *beautifyContractFixture) buildLock(t *testing.T) *BeautifyLock {
	t.Helper()
	plan := fixture.validatePlan(t)
	lock, err := BuildBeautifyLock(fixture.projectPath, fixture.taskID, plan.PlanSHA256)
	if err != nil {
		t.Fatalf("BuildBeautifyLock() error = %v", err)
	}
	return lock
}

func addBeautifySourceImageToFixture(t *testing.T, fixture *beautifyContractFixture) BeautifyInventoryImage {
	t.Helper()
	relative := "images/frozen-source.png"
	writeBeautifyTestFile(t, filepath.Join(fixture.projectPath, filepath.FromSlash(relative)), "frozen-source-image")
	ref, err := beautifyFileRef(fixture.projectPath, relative)
	if err != nil {
		t.Fatal(err)
	}
	image := BeautifyInventoryImage{
		ID: "image.p01.frozen", Filename: "frozen-source.png", SourceOccurrence: "P01:Picture 1",
		SourcePath: relative, SHA256: ref.SHA256, Size: ref.Size, Required: true,
	}
	fixture.inventory.Slides[0].Images = []BeautifyInventoryImage{image}
	writeBeautifyTestJSON(t, filepath.Join(fixture.projectPath, "analysis", "beautify_inventory.json"), fixture.inventory)
	inventorySHA, err := sha256File(filepath.Join(fixture.projectPath, "analysis", "beautify_inventory.json"))
	if err != nil {
		t.Fatal(err)
	}
	fixture.risk.InventorySHA256 = inventorySHA
	writeBeautifyTestJSON(t, filepath.Join(fixture.projectPath, "analysis", "beautify_risk_report.json"), fixture.risk)
	if _, err := ValidateBeautifyInventoryContract(fixture.projectPath, fixture.taskID); err != nil {
		t.Fatal(err)
	}
	fixture.plan.InventorySHA256 = inventorySHA
	fixture.plan.Slides[0].ImageIDs = []string{image.ID}
	writeBeautifyTestJSON(t, filepath.Join(fixture.projectPath, "analysis", "beautify_plan.json"), fixture.plan)
	return image
}

func writeBeautifyTestPPTX(t *testing.T, path string, slides int) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	presentation, err := writer.Create("ppt/presentation.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := presentation.Write([]byte(`<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"><p:sldSz cx="12192000" cy="6858000"/></p:presentation>`)); err != nil {
		t.Fatal(err)
	}
	for page := 1; page <= slides; page++ {
		entry, err := writer.Create(fmt.Sprintf("ppt/slides/slide%d.xml", page))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(`<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"/>`)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeBeautifyTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeBeautifyTestJSON(t *testing.T, path string, value any) {
	t.Helper()
	if err := writeJSONPretty(path, value); err != nil {
		t.Fatal(err)
	}
}
