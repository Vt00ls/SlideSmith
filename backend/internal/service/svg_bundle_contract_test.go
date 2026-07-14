package service

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

func writeValidSVGBundleNoTest(projectPath, taskID string, pageCount int) {
	confirmationPath := filepath.Join(projectPath, "confirm_ui", "result.json")
	confirmation := readJSONMap(confirmationPath)
	needsConfirmationWrite := confirmedPageCount(projectPath) != pageCount
	if strings.TrimSpace(valueString(confirmation, "canvas", "")) == "" {
		confirmation["canvas"] = "ppt169"
		needsConfirmationWrite = true
	}
	if needsConfirmationWrite {
		confirmation["page_count"] = pageCount
		if err := writeJSONPretty(confirmationPath, confirmation); err != nil {
			panic(err)
		}
	}
	if err := os.RemoveAll(filepath.Join(projectPath, "svg_output")); err != nil {
		panic(err)
	}
	designSHA, err := sha256File(filepath.Join(projectPath, "design_spec.md"))
	if err != nil {
		panic(err)
	}
	lockSHA, err := sha256File(filepath.Join(projectPath, "spec_lock.md"))
	if err != nil {
		panic(err)
	}
	manifestPath := filepath.Join(projectPath, ".slidesmith", "resources_manifest.json")
	manifestSHA, err := sha256File(manifestPath)
	if err != nil {
		panic(err)
	}

	pages := make([]svgInventoryPage, 0, pageCount)
	usagePages := make([]map[string]any, 0, pageCount)
	notesPages := make([]map[string]any, 0, pageCount)
	var notes strings.Builder
	for page := 1; page <= pageCount; page++ {
		pageID := fmt.Sprintf("P%02d", page)
		relativePath := fmt.Sprintf("svg_output/%02d_page_%02d.svg", page, page)
		mustWriteFileNoTest(projectPath, filepath.FromSlash(relativePath), fmt.Sprintf(
			`<svg xmlns="http://www.w3.org/2000/svg" width="1280" height="720" viewBox="0 0 1280 720" data-page-id="%s" data-spec-page-id="%s"><text id="page-title-%02d">Page %d</text></svg>`+"\n",
			pageID, pageID, page, page,
		))
		pageSHA, hashErr := sha256File(filepath.Join(projectPath, filepath.FromSlash(relativePath)))
		if hashErr != nil {
			panic(hashErr)
		}
		pages = append(pages, svgInventoryPage{
			PageID: pageID, SpecPageID: pageID, Page: page, Path: relativePath, SHA256: pageSHA,
			Width: 1280, Height: 720, ViewBox: []float64{0, 0, 1280, 720}, ElementCount: 2,
			TextCount: 1, ResourceIDs: []string{}, ElementIDs: []string{fmt.Sprintf("page-title-%02d", page)}, Warnings: []string{},
		})
		usagePages = append(usagePages, map[string]any{
			"page_id": pageID, "svg": relativePath, "svg_sha256": pageSHA, "resources": []any{},
		})
		notes.WriteString(fmt.Sprintf("## %s | Page %d\n\nSpeaker notes for page %d.\n\n", pageID, page, page))
		notesPages = append(notesPages, map[string]any{
			"page_id": pageID, "heading": fmt.Sprintf("Page %d", page), "word_count": 5, "char_count": 25, "empty": false,
		})
	}
	mustWriteFileNoTest(projectPath, filepath.Join("notes", "total.md"), notes.String())
	notesSHA, err := sha256File(filepath.Join(projectPath, "notes", "total.md"))
	if err != nil {
		panic(err)
	}
	if err := writeJSONPretty(filepath.Join(projectPath, "analysis", "svg_resource_usage.json"), map[string]any{
		"schema": svgResourceUsageSchema, "resources_manifest_sha256": manifestSHA, "pages": usagePages,
	}); err != nil {
		panic(err)
	}
	if err := writeJSONPretty(filepath.Join(projectPath, "analysis", "chart_usage.json"), map[string]any{
		"schema": chartUsageSchema, "resources_manifest_sha256": manifestSHA, "charts": []any{},
	}); err != nil {
		panic(err)
	}
	if err := writeJSONPretty(filepath.Join(projectPath, "analysis", "notes_inventory.json"), map[string]any{
		"schema": notesInventorySchema, "notes_sha256": notesSHA, "page_count": pageCount, "pages": notesPages,
	}); err != nil {
		panic(err)
	}
	if err := writeJSONPretty(filepath.Join(projectPath, "analysis", "svg_inventory.json"), svgInventoryDocument{
		Schema: svgInventorySchema, TaskID: taskID, RunnerProfile: "full-ppt-master",
		SpecSHA256: designSHA, SpecLockSHA256: lockSHA, ResourcesManifestSHA256: manifestSHA,
		Canvas: "ppt169", PageCount: pageCount, Pages: pages,
		Summary:         svgInventorySummary{Pages: pageCount, Elements: pageCount * 2, Texts: pageCount},
		ResourceSummary: map[string]int{"bindings": 0, "resources": 0}, ChartSummary: map[string]int{"charts": 0}, NotesSHA256: notesSHA,
	}); err != nil {
		panic(err)
	}
}

func TestValidateSVGBundleContractRechecksInventoryAndLiveHashes(t *testing.T) {
	projectPath := t.TempDir()
	mustWriteFileNoTest(projectPath, filepath.Join("confirm_ui", "result.json"), `{"canvas":"ppt169","page_count":3}`+"\n")
	mustWriteFileNoTest(projectPath, "design_spec.md", "# Design\n")
	mustWriteFileNoTest(projectPath, "spec_lock.md", "canvas: ppt169\nviewBox: 0 0 1280 720\n")
	if err := writeJSONPretty(filepath.Join(projectPath, ".slidesmith", "resources_manifest.json"), resourcesManifest{
		Schema: resourcesManifestSchema, TaskID: "task-svg", Route: "main", RunnerProfile: "full-ppt-master", Resources: []resourceManifestItem{},
	}); err != nil {
		t.Fatal(err)
	}
	writeValidSVGBundleNoTest(projectPath, "task-svg", 3)

	contract, err := validateSVGExecuteContract(projectPath, "task-svg")
	if err != nil {
		t.Fatalf("validateSVGExecuteContract() error = %v", err)
	}
	if contract["expected_pages"] != 3 || contract["svg_inventory_sha256"] == "" || contract["notes_inventory_sha256"] == "" {
		t.Fatalf("SVG contract missing bundle bindings: %#v", contract)
	}

	mustWriteFileNoTest(projectPath, filepath.Join("svg_output", "01_page_01.svg"), `<svg xmlns="http://www.w3.org/2000/svg"></svg>`+"\n")
	if _, err := validateSVGExecuteContract(projectPath, "task-svg"); err == nil || !strings.Contains(err.Error(), "hash is stale") {
		t.Fatalf("mutated SVG error = %v, want stale hash", err)
	}
}

func TestValidateFullSVGUpstreamContractRejectsSidecarMutation(t *testing.T) {
	projectPath := t.TempDir()
	mustWriteFileNoTest(projectPath, filepath.Join("confirm_ui", "result.json"), `{"canvas":"ppt169","page_count":3}`+"\n")
	mustWriteFileNoTest(projectPath, "design_spec.md", "# Design\n")
	mustWriteFileNoTest(projectPath, "spec_lock.md", "canvas: ppt169\n")
	if err := writeJSONPretty(filepath.Join(projectPath, ".slidesmith", "resources_manifest.json"), resourcesManifest{
		Schema: resourcesManifestSchema, TaskID: "task-svg", Route: "main", RunnerProfile: "full-ppt-master", Resources: []resourceManifestItem{},
	}); err != nil {
		t.Fatal(err)
	}
	writeValidSVGBundleNoTest(projectPath, "task-svg", 3)
	hashes, err := svgBundleContractHashes(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	contract := map[string]any{"runner_profile": "full-ppt-master"}
	for field, hash := range hashes {
		contract[field] = hash
	}
	if err := writeJSONPretty(filepath.Join(projectPath, ".slidesmith", "contracts", "svg_execute.json"), contract); err != nil {
		t.Fatal(err)
	}
	task := &model.Task{RunnerProfile: "full-ppt-master"}
	if _, err := validateFullSVGUpstreamContract(projectPath, PhaseSVGExecute, task); err != nil {
		t.Fatalf("valid upstream contract error = %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(projectPath, "analysis", "chart_usage.json"))
	if err != nil {
		t.Fatal(err)
	}
	var sidecar map[string]any
	if err := json.Unmarshal(raw, &sidecar); err != nil {
		t.Fatal(err)
	}
	sidecar["charts"] = []any{map[string]any{"chart_id": "tampered"}}
	if err := writeJSONPretty(filepath.Join(projectPath, "analysis", "chart_usage.json"), sidecar); err != nil {
		t.Fatal(err)
	}
	if _, err := validateFullSVGUpstreamContract(projectPath, PhaseSVGExecute, task); err == nil || !strings.Contains(err.Error(), "chart_usage_sha256 changed") {
		t.Fatalf("mutated sidecar error = %v", err)
	}
}

func TestValidateSVGBundleContractChecksBothEqualUpstreamHashes(t *testing.T) {
	projectPath := t.TempDir()
	mustWriteFileNoTest(projectPath, filepath.Join("confirm_ui", "result.json"), `{"canvas":"ppt169","page_count":3}`+"\n")
	mustWriteFileNoTest(projectPath, "design_spec.md", "same content\n")
	mustWriteFileNoTest(projectPath, "spec_lock.md", "same content\n")
	if err := writeJSONPretty(filepath.Join(projectPath, ".slidesmith", "resources_manifest.json"), resourcesManifest{
		Schema: resourcesManifestSchema, TaskID: "task-svg", Route: "main", RunnerProfile: "full-ppt-master", Resources: []resourceManifestItem{},
	}); err != nil {
		t.Fatal(err)
	}
	writeValidSVGBundleNoTest(projectPath, "task-svg", 3)
	if _, err := validateSVGBundleContract(projectPath, "task-svg"); err != nil {
		t.Fatalf("equal design/spec hashes should both validate: %v", err)
	}
	mustWriteFileNoTest(projectPath, "design_spec.md", "changed design only\n")
	if _, err := validateSVGBundleContract(projectPath, "task-svg"); err == nil || !strings.Contains(err.Error(), "design_spec.md") {
		t.Fatalf("changed design error = %v", err)
	}
}

func TestValidateSVGBundleContractRejectsSymlinkedSidecarAndMutatedReadyResource(t *testing.T) {
	projectPath := t.TempDir()
	mustWriteFileNoTest(projectPath, filepath.Join("confirm_ui", "result.json"), `{"canvas":"ppt169","page_count":3}`+"\n")
	mustWriteFileNoTest(projectPath, "design_spec.md", "# Design\n")
	mustWriteFileNoTest(projectPath, "spec_lock.md", "canvas: ppt169\n")
	assetPath := filepath.Join(projectPath, "images", "optional.png")
	mustWriteFileNoTest(projectPath, filepath.Join("images", "optional.png"), "ready asset")
	assetSHA, err := sha256File(assetPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSONPretty(filepath.Join(projectPath, ".slidesmith", "resources_manifest.json"), resourcesManifest{
		Schema: resourcesManifestSchema, TaskID: "task-svg", Route: "main", RunnerProfile: "full-ppt-master",
		Resources: []resourceManifestItem{{
			ID: "res-optional", Type: "image", Status: "ready", Required: false,
			Output: &resourceManifestOutput{Path: "images/optional.png", Size: int64(len("ready asset")), SHA256: assetSHA},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	writeValidSVGBundleNoTest(projectPath, "task-svg", 3)
	if _, err := validateSVGBundleContract(projectPath, "task-svg"); err != nil {
		t.Fatalf("valid ready resource error = %v", err)
	}
	mustWriteFileNoTest(projectPath, filepath.Join("images", "optional.png"), "changed asset")
	if _, err := validateSVGBundleContract(projectPath, "task-svg"); err == nil || !strings.Contains(err.Error(), "res-optional") {
		t.Fatalf("mutated ready resource error = %v", err)
	}
	mustWriteFileNoTest(projectPath, filepath.Join("images", "optional.png"), "ready asset")
	sidecar := filepath.Join(projectPath, "analysis", "chart_usage.json")
	outside := filepath.Join(t.TempDir(), "chart_usage.json")
	raw, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(sidecar); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, sidecar); err != nil {
		t.Fatal(err)
	}
	if _, err := validateSVGBundleContract(projectPath, "task-svg"); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlinked sidecar error = %v", err)
	}
}
