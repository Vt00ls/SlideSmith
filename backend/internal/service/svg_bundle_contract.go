package service

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	svgInventorySchema     = "slidesmith.svg_inventory.v1"
	svgResourceUsageSchema = "slidesmith.svg_resource_usage.v1"
	chartUsageSchema       = "slidesmith.chart_usage.v1"
	notesInventorySchema   = "slidesmith.notes_inventory.v1"
	svgCanvasTolerance     = 1e-6
)

var canonicalSVGFilename = regexp.MustCompile(`^[0-9]{2}_[^/\\]+\.svg$`)

type svgInventoryDocument struct {
	Schema                  string              `json:"schema"`
	TaskID                  string              `json:"task_id"`
	RunnerProfile           string              `json:"runner_profile"`
	SpecSHA256              string              `json:"spec_sha256"`
	SpecLockSHA256          string              `json:"spec_lock_sha256"`
	ResourcesManifestSHA256 string              `json:"resources_manifest_sha256"`
	Canvas                  string              `json:"canvas"`
	PageCount               int                 `json:"page_count"`
	Pages                   []svgInventoryPage  `json:"pages"`
	Summary                 svgInventorySummary `json:"summary"`
	ResourceSummary         map[string]int      `json:"resource_summary"`
	ChartSummary            map[string]int      `json:"chart_summary"`
	NotesSHA256             string              `json:"notes_sha256"`
}

type svgInventoryPage struct {
	PageID       string    `json:"page_id"`
	SpecPageID   string    `json:"spec_page_id"`
	Page         int       `json:"page"`
	Path         string    `json:"path"`
	SHA256       string    `json:"sha256"`
	Width        float64   `json:"width"`
	Height       float64   `json:"height"`
	ViewBox      []float64 `json:"view_box"`
	ElementCount int       `json:"element_count"`
	TextCount    int       `json:"text_count"`
	ImageCount   int       `json:"image_count"`
	UseCount     int       `json:"use_count"`
	ChartCount   int       `json:"chart_count"`
	FormulaCount int       `json:"formula_count"`
	ResourceIDs  []string  `json:"resource_ids"`
	ElementIDs   []string  `json:"element_ids"`
	Warnings     []string  `json:"warnings"`
}

type svgInventorySummary struct {
	Pages    int `json:"pages"`
	Elements int `json:"elements"`
	Texts    int `json:"texts"`
	Images   int `json:"images"`
	Charts   int `json:"charts"`
	Formulas int `json:"formulas"`
}

type svgResourceUsageDocument struct {
	Schema                  string                 `json:"schema"`
	ResourcesManifestSHA256 string                 `json:"resources_manifest_sha256"`
	Pages                   []svgResourceUsagePage `json:"pages"`
}

type svgResourceUsagePage struct {
	PageID    string            `json:"page_id"`
	SVG       string            `json:"svg"`
	SVGsha256 string            `json:"svg_sha256"`
	Resources []json.RawMessage `json:"resources"`
}

type chartUsageDocument struct {
	Schema                  string            `json:"schema"`
	ResourcesManifestSHA256 string            `json:"resources_manifest_sha256"`
	Charts                  []chartUsageEntry `json:"charts"`
}

type chartUsageEntry struct {
	ChartID string `json:"chart_id"`
	PageID  string `json:"page_id"`
	SVG     string `json:"svg"`
}

type notesInventoryDocument struct {
	Schema      string               `json:"schema"`
	NotesSHA256 string               `json:"notes_sha256"`
	PageCount   int                  `json:"page_count"`
	Pages       []notesInventoryPage `json:"pages"`
}

type notesInventoryPage struct {
	PageID string `json:"page_id"`
	Empty  bool   `json:"empty"`
}

type confirmedSVGCanvas struct {
	ID        string
	Width     float64
	Height    float64
	PageCount int
}

func validateSVGBundleContract(projectPath string, expectedTaskID ...string) (map[string]any, error) {
	canvas, err := readConfirmedSVGCanvas(projectPath)
	if err != nil {
		return nil, err
	}

	inventoryPath := filepath.Join(projectPath, "analysis", "svg_inventory.json")
	resourceUsagePath := filepath.Join(projectPath, "analysis", "svg_resource_usage.json")
	chartUsagePath := filepath.Join(projectPath, "analysis", "chart_usage.json")
	notesPath := filepath.Join(projectPath, "notes", "total.md")
	notesInventoryPath := filepath.Join(projectPath, "analysis", "notes_inventory.json")
	manifestPath := filepath.Join(projectPath, ".slidesmith", "resources_manifest.json")
	for _, path := range []string{inventoryPath, resourceUsagePath, chartUsagePath, notesPath, notesInventoryPath, manifestPath} {
		if err := requireContainedContractFile(projectPath, path); err != nil {
			return nil, err
		}
	}

	var inventory svgInventoryDocument
	if err := readJSONContract(inventoryPath, &inventory); err != nil {
		return nil, fmt.Errorf("decode SVG inventory: %w", err)
	}
	if inventory.Schema != svgInventorySchema {
		return nil, fmt.Errorf("SVG inventory schema = %q", inventory.Schema)
	}
	if inventory.TaskID == "" || inventory.RunnerProfile != "full-ppt-master" {
		return nil, fmt.Errorf("SVG inventory runner_profile = %q", inventory.RunnerProfile)
	}
	if len(expectedTaskID) > 0 && expectedTaskID[0] != "" && inventory.TaskID != expectedTaskID[0] {
		return nil, fmt.Errorf("SVG inventory task_id = %q, expected %q", inventory.TaskID, expectedTaskID[0])
	}
	if inventory.Canvas != canvas.ID || inventory.PageCount != canvas.PageCount || len(inventory.Pages) != canvas.PageCount {
		return nil, fmt.Errorf("SVG inventory page/canvas binding mismatch")
	}

	manifestSHA, err := sha256File(manifestPath)
	if err != nil {
		return nil, err
	}
	var manifest resourcesManifest
	if err := readJSONContract(manifestPath, &manifest); err != nil {
		return nil, fmt.Errorf("decode resources manifest: %w", err)
	}
	if manifest.Schema != resourcesManifestSchema || !isFullSVGRoute(manifest.Route) || manifest.RunnerProfile != "full-ppt-master" || manifest.TaskID != inventory.TaskID {
		return nil, fmt.Errorf("SVG inventory resources manifest binding mismatch")
	}
	if inventory.ResourcesManifestSHA256 != manifestSHA {
		return nil, fmt.Errorf("SVG inventory resources manifest hash is stale")
	}
	for _, check := range []struct {
		expected string
		path     string
	}{
		{expected: inventory.SpecSHA256, path: filepath.Join(projectPath, "design_spec.md")},
		{expected: inventory.SpecLockSHA256, path: filepath.Join(projectPath, "spec_lock.md")},
	} {
		if err := requireContainedContractFile(projectPath, check.path); err != nil {
			return nil, err
		}
		actual, hashErr := sha256File(check.path)
		if hashErr != nil {
			return nil, hashErr
		}
		if check.expected == "" || actual != check.expected {
			return nil, fmt.Errorf("SVG inventory upstream hash is stale for %s", filepath.Base(check.path))
		}
	}
	for _, resource := range manifest.Resources {
		if resource.Status != "ready" || resource.Output == nil {
			continue
		}
		outputPath, pathErr := containedProjectContractPath(projectPath, resource.Output.Path)
		if pathErr != nil {
			return nil, fmt.Errorf("ready resource %s output path: %w", resource.ID, pathErr)
		}
		info, resolvedOutput, inspectErr := inspectContainedPath(projectPath, outputPath)
		if inspectErr != nil || !info.Mode().IsRegular() || info.Size() != resource.Output.Size {
			if inspectErr != nil {
				return nil, fmt.Errorf("ready resource %s output: %w", resource.ID, inspectErr)
			}
			return nil, fmt.Errorf("ready resource %s output size/type is stale", resource.ID)
		}
		outputSHA, hashErr := sha256File(resolvedOutput)
		if hashErr != nil || outputSHA != resource.Output.SHA256 {
			return nil, fmt.Errorf("ready resource %s output hash is stale", resource.ID)
		}
	}

	liveSVGs, err := listRegularFiles(filepath.Join(projectPath, "svg_output"), "*.svg")
	if err != nil {
		return nil, err
	}
	if len(liveSVGs) != canvas.PageCount {
		return nil, fmt.Errorf("svg_execute produced %d svg files, expected %d", len(liveSVGs), canvas.PageCount)
	}
	pageByID := make(map[string]svgInventoryPage, len(inventory.Pages))
	seenPaths := make(map[string]bool, len(inventory.Pages))
	computedSummary := svgInventorySummary{Pages: len(inventory.Pages)}
	for index, page := range inventory.Pages {
		pageNumber := index + 1
		pageID := fmt.Sprintf("P%02d", pageNumber)
		if page.Page != pageNumber || page.PageID != pageID || page.SpecPageID != pageID {
			return nil, fmt.Errorf("SVG inventory page %d identity mismatch", pageNumber)
		}
		name := filepath.Base(filepath.FromSlash(page.Path))
		if page.Path != "svg_output/"+name || !isSafeCanonicalSVGFilename(name, pageNumber) {
			return nil, fmt.Errorf("SVG inventory page %s has non-canonical path %q", pageID, page.Path)
		}
		if seenPaths[page.Path] {
			return nil, fmt.Errorf("SVG inventory contains duplicate path %q", page.Path)
		}
		seenPaths[page.Path] = true
		if !sameCanvasNumber(page.Width, canvas.Width) || !sameCanvasNumber(page.Height, canvas.Height) || len(page.ViewBox) != 4 ||
			!sameCanvasNumber(page.ViewBox[0], 0) || !sameCanvasNumber(page.ViewBox[1], 0) ||
			!sameCanvasNumber(page.ViewBox[2], canvas.Width) || !sameCanvasNumber(page.ViewBox[3], canvas.Height) {
			return nil, fmt.Errorf("SVG inventory page %s canvas mismatch", pageID)
		}
		livePath, pathErr := containedProjectContractPath(projectPath, page.Path)
		if pathErr != nil {
			return nil, pathErr
		}
		info, resolvedLivePath, inspectErr := inspectContainedPath(projectPath, livePath)
		if inspectErr != nil || !info.Mode().IsRegular() {
			if inspectErr != nil {
				return nil, inspectErr
			}
			return nil, fmt.Errorf("SVG inventory page %s is not a regular file", pageID)
		}
		actualSHA, hashErr := sha256File(resolvedLivePath)
		if hashErr != nil {
			return nil, hashErr
		}
		if page.SHA256 == "" || actualSHA != page.SHA256 {
			return nil, fmt.Errorf("SVG inventory page %s hash is stale", pageID)
		}
		pageByID[pageID] = page
		computedSummary.Elements += page.ElementCount
		computedSummary.Texts += page.TextCount
		computedSummary.Images += page.ImageCount
		computedSummary.Charts += page.ChartCount
		computedSummary.Formulas += page.FormulaCount
	}
	if inventory.Summary != computedSummary {
		return nil, fmt.Errorf("SVG inventory summary does not match pages")
	}

	var resourceUsage svgResourceUsageDocument
	if err := readJSONContract(resourceUsagePath, &resourceUsage); err != nil {
		return nil, fmt.Errorf("decode SVG resource usage: %w", err)
	}
	if resourceUsage.Schema != svgResourceUsageSchema || resourceUsage.ResourcesManifestSHA256 != manifestSHA || len(resourceUsage.Pages) != canvas.PageCount {
		return nil, fmt.Errorf("SVG resource usage binding mismatch")
	}
	resourceBindings := map[string]bool{}
	resourceIDs := map[string]bool{}
	for index, row := range resourceUsage.Pages {
		pageID := fmt.Sprintf("P%02d", index+1)
		page, ok := pageByID[pageID]
		if !ok || row.PageID != pageID || row.SVG != page.Path || row.SVGsha256 != page.SHA256 {
			return nil, fmt.Errorf("SVG resource usage page binding mismatch for %s", pageID)
		}
		for _, raw := range row.Resources {
			var binding struct {
				ResourceID string `json:"resource_id"`
			}
			if err := json.Unmarshal(raw, &binding); err != nil || strings.TrimSpace(binding.ResourceID) == "" {
				return nil, fmt.Errorf("SVG resource usage has an invalid resource binding for %s", pageID)
			}
			resourceBindings[pageID+"\x00"+binding.ResourceID] = true
			resourceIDs[binding.ResourceID] = true
		}
	}
	if inventory.ResourceSummary["bindings"] != len(resourceBindings) || inventory.ResourceSummary["resources"] != len(resourceIDs) {
		return nil, fmt.Errorf("SVG resource usage summary does not match sidecar")
	}

	var chartUsage chartUsageDocument
	if err := readJSONContract(chartUsagePath, &chartUsage); err != nil {
		return nil, fmt.Errorf("decode chart usage: %w", err)
	}
	if chartUsage.Schema != chartUsageSchema || chartUsage.ResourcesManifestSHA256 != manifestSHA {
		return nil, fmt.Errorf("chart usage manifest binding mismatch")
	}
	seenCharts := make(map[string]bool, len(chartUsage.Charts))
	for _, chart := range chartUsage.Charts {
		page, ok := pageByID[chart.PageID]
		if chart.ChartID == "" || seenCharts[chart.ChartID] || !ok || chart.SVG != page.Path {
			return nil, fmt.Errorf("chart usage page/chart binding mismatch")
		}
		seenCharts[chart.ChartID] = true
	}
	if len(chartUsage.Charts) != inventory.Summary.Charts || inventory.ChartSummary["charts"] != len(chartUsage.Charts) {
		return nil, fmt.Errorf("chart usage summary does not match SVG inventory")
	}

	notesSHA, err := sha256File(notesPath)
	if err != nil {
		return nil, err
	}
	var notesInventory notesInventoryDocument
	if err := readJSONContract(notesInventoryPath, &notesInventory); err != nil {
		return nil, fmt.Errorf("decode notes inventory: %w", err)
	}
	if notesInventory.Schema != notesInventorySchema || notesInventory.NotesSHA256 != notesSHA || inventory.NotesSHA256 != notesSHA ||
		notesInventory.PageCount != canvas.PageCount || len(notesInventory.Pages) != canvas.PageCount {
		return nil, fmt.Errorf("notes inventory binding mismatch")
	}
	for index, page := range notesInventory.Pages {
		if page.PageID != fmt.Sprintf("P%02d", index+1) {
			return nil, fmt.Errorf("notes inventory page sequence mismatch")
		}
	}

	hashes, err := svgBundleContractHashes(projectPath)
	if err != nil {
		return nil, err
	}
	contract := map[string]any{
		"phase":              string(PhaseSVGExecute),
		"expected_pages":     canvas.PageCount,
		"svg_count":          len(inventory.Pages),
		"canvas":             map[string]any{"id": canvas.ID, "width": canvas.Width, "height": canvas.Height, "view_box": []float64{0, 0, canvas.Width, canvas.Height}},
		"svg_inventory":      "analysis/svg_inventory.json",
		"svg_resource_usage": "analysis/svg_resource_usage.json",
		"chart_usage":        "analysis/chart_usage.json",
		"notes":              "notes/total.md",
		"notes_inventory":    "analysis/notes_inventory.json",
		"summary":            inventory.Summary,
		"resource_summary":   inventory.ResourceSummary,
		"chart_summary":      inventory.ChartSummary,
	}
	for key, value := range hashes {
		contract[key] = value
	}
	return contract, nil
}

func readConfirmedSVGCanvas(projectPath string) (confirmedSVGCanvas, error) {
	confirmation := readJSONMap(filepath.Join(projectPath, "confirm_ui", "result.json"))
	canvasID := strings.ToLower(strings.TrimSpace(valueString(confirmation, "canvas", "")))
	canvas := confirmedSVGCanvas{ID: canvasID, PageCount: confirmedPageCount(projectPath)}
	switch canvasID {
	case "ppt169":
		canvas.Width, canvas.Height = 1280, 720
	case "ppt43":
		canvas.Width, canvas.Height = 1024, 768
	default:
		return confirmedSVGCanvas{}, fmt.Errorf("unsupported confirmed SVG canvas %q", canvasID)
	}
	if canvas.PageCount < 1 || canvas.PageCount > 99 {
		return confirmedSVGCanvas{}, fmt.Errorf("confirmed SVG page_count is outside 1..99: %d", canvas.PageCount)
	}
	return canvas, nil
}

func svgBundleContractHashes(projectPath string) (map[string]string, error) {
	paths := map[string]string{
		"svg_inventory_sha256":      filepath.Join(projectPath, "analysis", "svg_inventory.json"),
		"svg_resource_usage_sha256": filepath.Join(projectPath, "analysis", "svg_resource_usage.json"),
		"chart_usage_sha256":        filepath.Join(projectPath, "analysis", "chart_usage.json"),
		"notes_sha256":              filepath.Join(projectPath, "notes", "total.md"),
		"notes_inventory_sha256":    filepath.Join(projectPath, "analysis", "notes_inventory.json"),
		"resources_manifest_sha256": filepath.Join(projectPath, ".slidesmith", "resources_manifest.json"),
		"design_spec_sha256":        filepath.Join(projectPath, "design_spec.md"),
		"spec_lock_sha256":          filepath.Join(projectPath, "spec_lock.md"),
	}
	hashes := make(map[string]string, len(paths)+1)
	for field, path := range paths {
		if err := requireContainedContractFile(projectPath, path); err != nil {
			return nil, err
		}
		sha, err := sha256File(path)
		if err != nil {
			return nil, err
		}
		hashes[field] = sha
	}
	svgSHA, err := sha256RegularFiles(filepath.Join(projectPath, "svg_output"), "*.svg")
	if err != nil {
		return nil, err
	}
	hashes["svg_output_sha256"] = svgSHA
	return hashes, nil
}

func readJSONContract(path string, target any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}

func containedProjectContractPath(projectPath, relativePath string) (string, error) {
	if relativePath == "" || filepath.IsAbs(relativePath) || relativePath != filepath.ToSlash(relativePath) {
		return "", fmt.Errorf("bundle path is not project-relative POSIX: %q", relativePath)
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(relativePath)))
	if clean != relativePath || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("bundle path escapes project: %q", relativePath)
	}
	return filepath.Join(projectPath, filepath.FromSlash(relativePath)), nil
}

func sameCanvasNumber(left, right float64) bool {
	return !math.IsNaN(left) && !math.IsInf(left, 0) && math.Abs(left-right) <= svgCanvasTolerance
}

func requireContainedContractFile(projectPath, path string) error {
	info, _, err := inspectContainedPath(projectPath, path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 {
		return fmt.Errorf("required bundle path is not a non-empty regular file: %s", filepath.Base(path))
	}
	return nil
}

func isSafeCanonicalSVGFilename(name string, page int) bool {
	if !canonicalSVGFilename.MatchString(name) || !strings.HasPrefix(name, fmt.Sprintf("%02d_", page)) || !utf8.ValidString(name) {
		return false
	}
	slug := strings.TrimSuffix(strings.TrimPrefix(name, fmt.Sprintf("%02d_", page)), ".svg")
	if slug == "" || strings.TrimSpace(slug) != slug || slug == "." || slug == ".." {
		return false
	}
	for _, character := range slug {
		if character != '-' && character != '_' && !unicode.IsLetter(character) && !unicode.IsDigit(character) {
			return false
		}
	}
	return true
}
