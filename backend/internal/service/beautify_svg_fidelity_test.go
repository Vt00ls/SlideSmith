package service

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

type beautifySVGFidelityFixture struct {
	contract  *beautifyContractFixture
	lock      *BeautifyLock
	lockSHA   string
	svgPath   string
	chartPath string
}

func newBeautifySVGFidelityFixture(t *testing.T) *beautifySVGFidelityFixture {
	t.Helper()
	fixture := newBeautifyContractFixture(t, 1)
	image := addBeautifySourceImageToFixture(t, fixture)
	fixture.inventory.Slides[0].TextBlocks = append(fixture.inventory.Slides[0].TextBlocks,
		BeautifyInventoryText{ID: "text.p01.subtitle", Role: "subtitle", Text: "Subtitle 1"})
	fixture.inventory.Slides[0].Tables = []BeautifyInventoryTable{{
		ID: "table.p01.metrics", RowCount: 1, ColCount: 2, Cells: [][]string{{"Cell A", "Cell B"}},
	}}
	fixture.inventory.Slides[0].Charts = []BeautifyInventoryChart{{
		ID: "chart.p01.revenue", Type: "barChart", Categories: []string{"Q1"},
		Series: []BeautifyInventoryChartSeries{{Name: "Revenue", Values: []any{float64(10)}}},
	}}
	writeBeautifyTestJSON(t, filepath.Join(fixture.projectPath, "analysis", "beautify_inventory.json"), fixture.inventory)
	inventorySHA, _ := sha256File(filepath.Join(fixture.projectPath, "analysis", "beautify_inventory.json"))
	fixture.risk.InventorySHA256 = inventorySHA
	writeBeautifyTestJSON(t, filepath.Join(fixture.projectPath, "analysis", "beautify_risk_report.json"), fixture.risk)
	if _, err := ValidateBeautifyInventoryContract(fixture.projectPath, fixture.taskID); err != nil {
		t.Fatal(err)
	}
	fixture.plan.InventorySHA256 = inventorySHA
	fixture.plan.Slides[0].TextBlockIDs = []string{"text.p01.title", "text.p01.subtitle"}
	fixture.plan.Slides[0].TableIDs = []string{"table.p01.metrics"}
	fixture.plan.Slides[0].ChartIDs = []string{"chart.p01.revenue"}
	fixture.plan.Slides[0].ImageIDs = []string{image.ID}
	writeBeautifyTestJSON(t, filepath.Join(fixture.projectPath, "analysis", "beautify_plan.json"), fixture.plan)
	lock := fixture.buildLock(t)
	lockSHA, _ := sha256File(filepath.Join(fixture.projectPath, ".slidesmith", "beautify_lock.json"))

	chartPath := filepath.Join(fixture.projectPath, "charts", "data", "revenue.json")
	writeBeautifyTestJSON(t, chartPath, map[string]any{
		"data": map[string]any{"categories": []string{"Q1"}, "series": []map[string]any{{"name": "Revenue", "values": []any{float64(10)}}}},
	})
	chartSHA, _ := sha256File(chartPath)
	imageOutput := filepath.Join(fixture.projectPath, "images", "acquired", "frozen-source.png")
	writeBeautifyTestFile(t, imageOutput, "frozen-source-image")
	imageOutputSHA, _ := sha256File(imageOutput)
	manifest := resourcesManifest{Resources: []resourceManifestItem{
		{ID: "chart-data", Page: 1, Type: "chart_data", Status: "ready", Output: &resourceManifestOutput{Path: "charts/data/revenue.json", SHA256: chartSHA}},
		{ID: "source-image", Page: 1, Type: "image", Status: "ready", Input: map[string]any{"source_reference": image.SourcePath}, Output: &resourceManifestOutput{Path: "images/acquired/frozen-source.png", SHA256: imageOutputSHA}},
	}}
	writeBeautifyTestJSON(t, filepath.Join(fixture.projectPath, ".slidesmith", "resources_manifest.json"), manifest)
	writeBeautifyTestJSON(t, filepath.Join(fixture.projectPath, "analysis", "svg_resource_usage.json"), map[string]any{
		"pages": []map[string]any{{"page_id": "P01", "resources": []map[string]any{{"resource_id": "source-image"}}}},
	})
	writeBeautifyTestJSON(t, filepath.Join(fixture.projectPath, "analysis", "chart_usage.json"), map[string]any{
		"charts": []map[string]any{{"chart_id": "chart.p01.revenue", "page_id": "P01", "data_resource_id": "chart-data", "data_sha256": chartSHA}},
	})
	svgPath := filepath.Join(fixture.projectPath, "svg_output", "01_beautify.svg")
	writeBeautifyTestFile(t, svgPath, beautifySVGFixtureText(lockSHA, "Title 1", "Subtitle 1", "Cell A", "Cell B"))
	svgSHA, _ := sha256File(svgPath)
	writeBeautifyTestJSON(t, filepath.Join(fixture.projectPath, "analysis", "svg_inventory.json"), svgInventoryDocument{
		TaskID: fixture.taskID, PageCount: 1,
		Pages: []svgInventoryPage{{PageID: "P01", SpecPageID: "P01", Page: 1, Path: "svg_output/01_beautify.svg", SHA256: svgSHA}},
	})
	return &beautifySVGFidelityFixture{contract: fixture, lock: lock, lockSHA: lockSHA, svgPath: svgPath, chartPath: chartPath}
}

func beautifySVGFixtureText(lockSHA string, values ...string) string {
	var text strings.Builder
	for _, value := range values {
		text.WriteString("<text>")
		text.WriteString(value)
		text.WriteString("</text>")
	}
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" data-page-id="P01" data-spec-page-id="P01" data-source-slide="1" data-beautify-lock-hash="%s">%s</svg>`, lockSHA, text.String())
}

func (fixture *beautifySVGFidelityFixture) rewriteSVG(t *testing.T, lockSHA string, values ...string) {
	t.Helper()
	writeBeautifyTestFile(t, fixture.svgPath, beautifySVGFixtureText(lockSHA, values...))
	var inventory svgInventoryDocument
	if err := beautifyReadJSON(fixture.contract.projectPath, "analysis/svg_inventory.json", &inventory); err != nil {
		t.Fatal(err)
	}
	inventory.Pages[0].SHA256, _ = sha256File(fixture.svgPath)
	writeBeautifyTestJSON(t, filepath.Join(fixture.contract.projectPath, "analysis", "svg_inventory.json"), inventory)
}

func TestValidateBeautifySVGFidelityPositiveAndExistingReceipt(t *testing.T) {
	fixture := newBeautifySVGFidelityFixture(t)
	if _, err := validateBeautifySVGFidelity(fixture.contract.projectPath, fixture.contract.taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := validateExistingBeautifySVGFidelity(fixture.contract.projectPath, fixture.contract.taskID); err != nil {
		t.Fatal(err)
	}
}

func TestValidateBeautifySVGFidelityRejectsRootTextTableChartAndWrongPageImage(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *beautifySVGFidelityFixture)
		want   string
	}{
		{name: "root lock hash", mutate: func(t *testing.T, f *beautifySVGFidelityFixture) {
			f.rewriteSVG(t, strings.Repeat("0", 64), "Title 1", "Subtitle 1", "Cell A", "Cell B")
		}, want: "metadata mismatch"},
		{name: "reordered text", mutate: func(t *testing.T, f *beautifySVGFidelityFixture) {
			f.rewriteSVG(t, f.lockSHA, "Subtitle 1", "Title 1", "Cell A", "Cell B")
		}, want: "reordered"},
		{name: "missing table cell", mutate: func(t *testing.T, f *beautifySVGFidelityFixture) {
			f.rewriteSVG(t, f.lockSHA, "Title 1", "Subtitle 1", "Cell A")
		}, want: "table"},
		{name: "chart data", mutate: func(t *testing.T, f *beautifySVGFidelityFixture) {
			writeBeautifyTestJSON(t, f.chartPath, map[string]any{"data": map[string]any{"categories": []string{"Q1"}, "series": []map[string]any{{"name": "Revenue", "values": []any{float64(99)}}}}})
			sha, _ := sha256File(f.chartPath)
			var manifest resourcesManifest
			_ = beautifyReadJSON(f.contract.projectPath, ".slidesmith/resources_manifest.json", &manifest)
			manifest.Resources[0].Output.SHA256 = sha
			writeBeautifyTestJSON(t, filepath.Join(f.contract.projectPath, ".slidesmith", "resources_manifest.json"), manifest)
			writeBeautifyTestJSON(t, filepath.Join(f.contract.projectPath, "analysis", "chart_usage.json"), map[string]any{"charts": []map[string]any{{"chart_id": "chart.p01.revenue", "page_id": "P01", "data_resource_id": "chart-data", "data_sha256": sha}}})
		}, want: "chart"},
		{name: "wrong page image", mutate: func(t *testing.T, f *beautifySVGFidelityFixture) {
			var manifest resourcesManifest
			_ = beautifyReadJSON(f.contract.projectPath, ".slidesmith/resources_manifest.json", &manifest)
			manifest.Resources[1].Page = 2
			writeBeautifyTestJSON(t, filepath.Join(f.contract.projectPath, ".slidesmith", "resources_manifest.json"), manifest)
		}, want: "image"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newBeautifySVGFidelityFixture(t)
			test.mutate(t, fixture)
			if _, err := validateBeautifySVGFidelity(fixture.contract.projectPath, fixture.contract.taskID); err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.want)) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateExistingBeautifySVGFidelityRechecksSVGMetadata(t *testing.T) {
	fixture := newBeautifySVGFidelityFixture(t)
	if _, err := validateBeautifySVGFidelity(fixture.contract.projectPath, fixture.contract.taskID); err != nil {
		t.Fatal(err)
	}
	fixture.rewriteSVG(t, strings.Repeat("0", 64), "Title 1", "Subtitle 1", "Cell A", "Cell B")
	var receipt beautifySVGFidelityReceipt
	if err := beautifyReadJSON(fixture.contract.projectPath, "analysis/beautify_svg_fidelity.json", &receipt); err != nil {
		t.Fatal(err)
	}
	receipt.Pages[0].SVGSHA256, _ = sha256File(fixture.svgPath)
	writeBeautifyTestJSON(t, filepath.Join(fixture.contract.projectPath, "analysis", "beautify_svg_fidelity.json"), receipt)
	if _, err := validateExistingBeautifySVGFidelity(fixture.contract.projectPath, fixture.contract.taskID); err == nil || !strings.Contains(err.Error(), "metadata mismatch") {
		t.Fatalf("existing receipt metadata error = %v", err)
	}
}
