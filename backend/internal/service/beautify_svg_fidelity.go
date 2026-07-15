package service

import (
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"
)

const beautifySVGFidelitySchema = "slidesmith.beautify_svg_fidelity.v1"

type beautifySVGFidelityReceipt struct {
	Schema             string                           `json:"schema"`
	TaskID             string                           `json:"task_id"`
	BeautifyLockSHA256 string                           `json:"beautify_lock_sha256"`
	Pages              []beautifySVGFidelityPageReceipt `json:"pages"`
	CheckedAt          string                           `json:"checked_at"`
}

type beautifySVGFidelityPageReceipt struct {
	PageID       string                    `json:"page_id"`
	SourceSlide  int                       `json:"source_slide"`
	SVG          string                    `json:"svg"`
	SVGSHA256    string                    `json:"svg_sha256"`
	TextExpected int                       `json:"text_expected"`
	TextMatched  int                       `json:"text_matched"`
	Tables       []beautifySVGFidelityItem `json:"tables"`
	Charts       []beautifySVGFidelityItem `json:"charts"`
	Images       []beautifySVGFidelityItem `json:"images"`
}

type beautifySVGFidelityItem struct {
	ID            string `json:"id"`
	ContentSHA256 string `json:"content_sha256"`
	Decision      string `json:"decision"`
	ResourceID    string `json:"resource_id,omitempty"`
	Evidence      string `json:"evidence,omitempty"`
	SourceSHA256  string `json:"source_sha256,omitempty"`
	SourceSize    int64  `json:"source_size,omitempty"`
}

func validateBeautifySVGFidelity(projectPath, expectedTaskID string) (map[string]any, error) {
	lock, err := ValidateBeautifyLock(projectPath, expectedTaskID)
	if err != nil {
		return nil, err
	}
	lockSHA, err := sha256File(filepath.Join(projectPath, ".slidesmith", "beautify_lock.json"))
	if err != nil {
		return nil, err
	}
	var inventory svgInventoryDocument
	if err := beautifyReadJSON(projectPath, "analysis/svg_inventory.json", &inventory); err != nil {
		return nil, err
	}
	if inventory.TaskID != expectedTaskID || len(inventory.Pages) != lock.SlideCount {
		return nil, fmt.Errorf("svg_execute.beautify_fidelity: SVG inventory task/page binding mismatch")
	}
	manifest, usageByPage, chartsByPage, err := loadBeautifySVGLineage(projectPath)
	if err != nil {
		return nil, err
	}
	receipt := beautifySVGFidelityReceipt{
		Schema: beautifySVGFidelitySchema, TaskID: expectedTaskID, BeautifyLockSHA256: lockSHA,
		CheckedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	for index, frozen := range lock.Slides {
		pageNumber := index + 1
		pageID := fmt.Sprintf("P%02d", pageNumber)
		pageInventory := inventory.Pages[index]
		if pageInventory.Page != pageNumber || pageInventory.PageID != pageID {
			return nil, fmt.Errorf("svg_execute.beautify_fidelity: SVG page %s order mismatch", pageID)
		}
		svgPath, err := containedProjectContractPath(projectPath, pageInventory.Path)
		if err != nil {
			return nil, err
		}
		root, text, err := readBeautifySVGText(svgPath)
		if err != nil {
			return nil, fmt.Errorf("svg_execute.beautify_fidelity: page %s: %w", pageID, err)
		}
		if root["data-page-id"] != pageID || root["data-spec-page-id"] != pageID || root["data-source-slide"] != strconv.Itoa(pageNumber) || root["data-beautify-lock-hash"] != lockSHA {
			return nil, fmt.Errorf("svg_execute.beautify_fidelity: page %s source/lock metadata mismatch", pageID)
		}
		actualSVGHash, err := sha256File(svgPath)
		if err != nil || actualSVGHash != pageInventory.SHA256 {
			return nil, fmt.Errorf("svg_execute.beautify_fidelity: page %s SVG hash is stale", pageID)
		}
		matched, err := matchBeautifyTextBlocks(text, frozen.TextBlocks)
		if err != nil {
			return nil, fmt.Errorf("svg_execute.beautify_fidelity: page %s: %w", pageID, err)
		}
		pageReceipt := beautifySVGFidelityPageReceipt{
			PageID: pageID, SourceSlide: pageNumber, SVG: filepath.ToSlash(pageInventory.Path), SVGSHA256: actualSVGHash,
			TextExpected: beautifyExpectedTextBlockCount(frozen.TextBlocks), TextMatched: matched,
		}
		for _, table := range frozen.Tables {
			tableCursor := 0
			for _, row := range table.Cells {
				for _, cell := range row {
					key := normalizeBeautifyVisibleText(cell)
					if key == "" {
						continue
					}
					position := strings.Index(text[tableCursor:], key)
					if position < 0 {
						return nil, fmt.Errorf("svg_execute.beautify_fidelity: page %s table %s cell text is missing or reordered", pageID, table.ID)
					}
					tableCursor += position + len(key)
				}
			}
			sha, _ := beautifyJSONSHA256(map[string]any{"cells": table.Cells, "row_count": table.RowCount, "column_count": table.ColCount})
			pageReceipt.Tables = append(pageReceipt.Tables, beautifySVGFidelityItem{ID: table.ID, ContentSHA256: sha, Decision: "pass", Evidence: "svg_visible_cell_text"})
		}
		for _, chart := range frozen.Charts {
			item, ok := beautifyChartReceipt(projectPath, pageID, chart, manifest, chartsByPage[pageID])
			if !ok {
				return nil, fmt.Errorf("svg_execute.beautify_fidelity: page %s chart %s lacks exact data lineage", pageID, chart.ID)
			}
			pageReceipt.Charts = append(pageReceipt.Charts, item)
		}
		for _, image := range frozen.Images {
			if !image.Required || beautifyDecisionContains(lock.Ignored, pageNumber, image.ID) || beautifyDecisionContains(lock.Unsupported, pageNumber, image.ID) {
				continue
			}
			item, ok := beautifyImageReceipt(projectPath, pageNumber, image, manifest, usageByPage[pageID])
			if !ok {
				return nil, fmt.Errorf("svg_execute.beautify_fidelity: page %s required image %s lacks manifest/usage lineage", pageID, image.ID)
			}
			pageReceipt.Images = append(pageReceipt.Images, item)
		}
		receipt.Pages = append(receipt.Pages, pageReceipt)
	}
	path := filepath.Join(projectPath, "analysis", "beautify_svg_fidelity.json")
	if err := writeJSONAtomic(path, &receipt); err != nil {
		return nil, err
	}
	sha, err := sha256File(path)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"beautify_svg_fidelity_sha256": sha,
		"beautify_lock_sha256":         lockSHA,
		"source_slide_count":           lock.SlideCount,
		"page_count":                   len(receipt.Pages),
	}, nil
}

func validateExistingBeautifySVGFidelity(projectPath, expectedTaskID string) (map[string]any, error) {
	lock, err := ValidateBeautifyLock(projectPath, expectedTaskID)
	if err != nil {
		return nil, err
	}
	lockSHA, err := sha256File(filepath.Join(projectPath, ".slidesmith", "beautify_lock.json"))
	if err != nil {
		return nil, err
	}
	var receipt beautifySVGFidelityReceipt
	if err := beautifyReadJSON(projectPath, "analysis/beautify_svg_fidelity.json", &receipt); err != nil {
		return nil, err
	}
	if receipt.Schema != beautifySVGFidelitySchema || receipt.TaskID != expectedTaskID || receipt.BeautifyLockSHA256 != lockSHA || len(receipt.Pages) != lock.SlideCount {
		return nil, fmt.Errorf("Beautify SVG fidelity receipt binding mismatch")
	}
	manifest, usageByPage, chartsByPage, err := loadBeautifySVGLineage(projectPath)
	if err != nil {
		return nil, err
	}
	for index, page := range receipt.Pages {
		expectedPage := index + 1
		pageID := fmt.Sprintf("P%02d", expectedPage)
		frozen := lock.Slides[index]
		if page.PageID != pageID || page.SourceSlide != expectedPage || page.TextExpected != beautifyExpectedTextBlockCount(frozen.TextBlocks) || page.TextMatched != page.TextExpected {
			return nil, fmt.Errorf("Beautify SVG fidelity page %d is incomplete", expectedPage)
		}
		path, err := containedProjectContractPath(projectPath, page.SVG)
		if err != nil {
			return nil, err
		}
		sha, err := sha256File(path)
		if err != nil || sha != page.SVGSHA256 {
			return nil, fmt.Errorf("Beautify SVG fidelity page %d is stale", expectedPage)
		}
		root, text, err := readBeautifySVGText(path)
		if err != nil {
			return nil, fmt.Errorf("Beautify SVG fidelity page %d is unreadable: %w", expectedPage, err)
		}
		if root["data-page-id"] != pageID || root["data-spec-page-id"] != pageID || root["data-source-slide"] != strconv.Itoa(expectedPage) || root["data-beautify-lock-hash"] != lockSHA {
			return nil, fmt.Errorf("Beautify SVG fidelity page %d source/lock metadata mismatch", expectedPage)
		}
		matched, err := matchBeautifyTextBlocks(text, frozen.TextBlocks)
		if err != nil || matched != page.TextExpected {
			return nil, fmt.Errorf("Beautify SVG fidelity page %d text is missing, changed, or reordered", expectedPage)
		}
		if err := validateExistingBeautifyTableReceipts(text, frozen.Tables, page.Tables); err != nil {
			return nil, fmt.Errorf("Beautify SVG fidelity page %d: %w", expectedPage, err)
		}
		if len(page.Charts) != len(frozen.Charts) {
			return nil, fmt.Errorf("Beautify SVG fidelity page %d chart receipts are incomplete", expectedPage)
		}
		for _, chart := range frozen.Charts {
			expected, ok := beautifyChartReceipt(projectPath, pageID, chart, manifest, chartsByPage[pageID])
			if !ok || !containsBeautifyFidelityItem(page.Charts, expected, false) {
				return nil, fmt.Errorf("Beautify SVG fidelity page %d chart %s is stale", expectedPage, chart.ID)
			}
		}
		expectedImages := make([]beautifySVGFidelityItem, 0, len(frozen.Images))
		for _, image := range frozen.Images {
			if !image.Required || beautifyDecisionContains(lock.Ignored, expectedPage, image.ID) || beautifyDecisionContains(lock.Unsupported, expectedPage, image.ID) {
				continue
			}
			expected, ok := beautifyImageReceipt(projectPath, expectedPage, image, manifest, usageByPage[pageID])
			if !ok {
				return nil, fmt.Errorf("Beautify SVG fidelity page %d image %s is stale", expectedPage, image.ID)
			}
			expectedImages = append(expectedImages, expected)
		}
		if len(page.Images) != len(expectedImages) {
			return nil, fmt.Errorf("Beautify SVG fidelity page %d image receipts are incomplete", expectedPage)
		}
		for _, expected := range expectedImages {
			if !containsBeautifyFidelityItem(page.Images, expected, true) {
				return nil, fmt.Errorf("Beautify SVG fidelity page %d image %s binding is stale", expectedPage, expected.ID)
			}
		}
	}
	sha, err := sha256File(filepath.Join(projectPath, "analysis", "beautify_svg_fidelity.json"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"beautify_svg_fidelity_sha256": sha, "beautify_lock_sha256": lockSHA, "page_count": len(receipt.Pages)}, nil
}

func matchBeautifyTextBlocks(text string, blocks []BeautifyInventoryText) (int, error) {
	cursor := 0
	matched := 0
	for _, block := range blocks {
		key := normalizeBeautifyVisibleText(block.Text)
		if key == "" {
			continue
		}
		position := strings.Index(text[cursor:], key)
		if position < 0 {
			return matched, fmt.Errorf("text block %s is missing, changed, or reordered", block.ID)
		}
		cursor += position + len(key)
		matched++
	}
	return matched, nil
}

func validateExistingBeautifyTableReceipts(text string, tables []BeautifyInventoryTable, receipts []beautifySVGFidelityItem) error {
	if len(receipts) != len(tables) {
		return fmt.Errorf("table receipts are incomplete")
	}
	for _, table := range tables {
		cursor := 0
		for _, row := range table.Cells {
			for _, cell := range row {
				key := normalizeBeautifyVisibleText(cell)
				if key == "" {
					continue
				}
				position := strings.Index(text[cursor:], key)
				if position < 0 {
					return fmt.Errorf("table %s cell text is missing or reordered", table.ID)
				}
				cursor += position + len(key)
			}
		}
		sha, _ := beautifyJSONSHA256(map[string]any{"cells": table.Cells, "row_count": table.RowCount, "column_count": table.ColCount})
		expected := beautifySVGFidelityItem{ID: table.ID, ContentSHA256: sha, Decision: "pass"}
		if !containsBeautifyFidelityItem(receipts, expected, false) {
			return fmt.Errorf("table %s receipt is stale", table.ID)
		}
	}
	return nil
}

func containsBeautifyFidelityItem(items []beautifySVGFidelityItem, expected beautifySVGFidelityItem, includeSource bool) bool {
	for _, item := range items {
		if item.ID != expected.ID || item.ContentSHA256 != expected.ContentSHA256 || item.Decision != "pass" {
			continue
		}
		if includeSource && (item.SourceSHA256 != expected.SourceSHA256 || item.SourceSize != expected.SourceSize) {
			continue
		}
		return true
	}
	return false
}

func beautifyExpectedTextBlockCount(blocks []BeautifyInventoryText) int {
	count := 0
	for _, block := range blocks {
		if normalizeBeautifyVisibleText(block.Text) != "" {
			count++
		}
	}
	return count
}

func readBeautifySVGText(path string) (map[string]string, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer file.Close()
	decoder := xml.NewDecoder(io.LimitReader(file, 16<<20))
	root := map[string]string{}
	var fragments []string
	textDepth := 0
	seenRoot := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", err
		}
		switch typed := token.(type) {
		case xml.StartElement:
			if !seenRoot {
				if typed.Name.Local != "svg" {
					return nil, "", fmt.Errorf("root is not svg")
				}
				seenRoot = true
				for _, attr := range typed.Attr {
					root[attr.Name.Local] = attr.Value
				}
			}
			if typed.Name.Local == "text" || typed.Name.Local == "tspan" {
				textDepth++
			}
		case xml.EndElement:
			if (typed.Name.Local == "text" || typed.Name.Local == "tspan") && textDepth > 0 {
				textDepth--
				fragments = append(fragments, " ")
			}
		case xml.CharData:
			if textDepth > 0 {
				fragments = append(fragments, string(typed))
			}
		}
	}
	if !seenRoot {
		return nil, "", fmt.Errorf("empty SVG")
	}
	return root, normalizeBeautifyVisibleText(strings.Join(fragments, " ")), nil
}

func normalizeBeautifyVisibleText(value string) string {
	value = norm.NFKC.String(strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\u00a0", " "))
	return strings.Join(strings.Fields(value), " ")
}

type beautifyChartUsage struct {
	ChartID        string `json:"chart_id"`
	PageID         string `json:"page_id"`
	DataResourceID string `json:"data_resource_id"`
	DataSHA256     string `json:"data_sha256"`
}

func loadBeautifySVGLineage(projectPath string) (*resourcesManifest, map[string]map[string]bool, map[string][]beautifyChartUsage, error) {
	var manifest resourcesManifest
	if err := beautifyReadJSON(projectPath, ".slidesmith/resources_manifest.json", &manifest); err != nil {
		return nil, nil, nil, err
	}
	var usage struct {
		Pages []struct {
			PageID    string `json:"page_id"`
			Resources []struct {
				ResourceID string `json:"resource_id"`
			} `json:"resources"`
		} `json:"pages"`
	}
	if err := beautifyReadJSON(projectPath, "analysis/svg_resource_usage.json", &usage); err != nil {
		return nil, nil, nil, err
	}
	usageByPage := map[string]map[string]bool{}
	for _, page := range usage.Pages {
		usageByPage[page.PageID] = map[string]bool{}
		for _, item := range page.Resources {
			usageByPage[page.PageID][item.ResourceID] = true
		}
	}
	var charts struct {
		Charts []beautifyChartUsage `json:"charts"`
	}
	if err := beautifyReadJSON(projectPath, "analysis/chart_usage.json", &charts); err != nil {
		return nil, nil, nil, err
	}
	chartsByPage := map[string][]beautifyChartUsage{}
	for _, chart := range charts.Charts {
		chartsByPage[chart.PageID] = append(chartsByPage[chart.PageID], chart)
	}
	return &manifest, usageByPage, chartsByPage, nil
}

func beautifyChartReceipt(projectPath, pageID string, chart BeautifyInventoryChart, manifest *resourcesManifest, usages []beautifyChartUsage) (beautifySVGFidelityItem, bool) {
	resources := map[string]resourceManifestItem{}
	for _, item := range manifest.Resources {
		resources[item.ID] = item
	}
	expected := map[string]any{"categories": chart.Categories, "series": chart.Series}
	expectedSHA, _ := beautifyJSONSHA256(expected)
	for _, usage := range usages {
		if usage.ChartID != chart.ID {
			continue
		}
		resource, ok := resources[usage.DataResourceID]
		if !ok || resource.Type != "chart_data" || resource.Status != "ready" || resource.Output == nil || resource.Output.SHA256 != usage.DataSHA256 {
			continue
		}
		path, err := containedProjectContractPath(projectPath, resource.Output.Path)
		if err != nil {
			continue
		}
		if sha, err := sha256File(path); err != nil || sha != resource.Output.SHA256 {
			continue
		}
		var payload map[string]any
		if beautifyReadJSON(projectPath, resource.Output.Path, &payload) != nil {
			continue
		}
		data, _ := payload["data"].(map[string]any)
		if data == nil {
			data = payload
		}
		actual := map[string]any{"categories": data["categories"], "series": data["series"]}
		actualSHA, _ := beautifyJSONSHA256(actual)
		if actualSHA == expectedSHA {
			return beautifySVGFidelityItem{ID: chart.ID, ContentSHA256: expectedSHA, Decision: "pass", ResourceID: resource.ID, Evidence: "chart_data_manifest_usage"}, true
		}
	}
	return beautifySVGFidelityItem{}, false
}

func beautifyImageReceipt(projectPath string, page int, image BeautifyInventoryImage, manifest *resourcesManifest, used map[string]bool) (beautifySVGFidelityItem, bool) {
	ids := make([]string, 0, len(manifest.Resources))
	resources := map[string]resourceManifestItem{}
	for _, item := range manifest.Resources {
		resources[item.ID] = item
		ids = append(ids, item.ID)
	}
	sort.Strings(ids)
	for _, id := range ids {
		item := resources[id]
		if !used[id] || item.Page != page || item.Type != "image" || item.Status != "ready" || item.Output == nil {
			continue
		}
		sourceReference, _ := item.Input["source_reference"].(string)
		if image.SourcePath != "" && filepath.ToSlash(filepath.Clean(filepath.FromSlash(sourceReference))) != filepath.ToSlash(filepath.Clean(filepath.FromSlash(image.SourcePath))) {
			continue
		}
		if image.SourcePath == "" && image.Filename != "" && filepath.Base(sourceReference) != filepath.Base(image.Filename) {
			continue
		}
		path, err := containedProjectContractPath(projectPath, item.Output.Path)
		if err != nil {
			continue
		}
		if sha, err := sha256File(path); err != nil || sha != item.Output.SHA256 {
			continue
		}
		content := map[string]any{
			"id": image.ID, "filename": image.Filename, "source_occurrence": image.SourceOccurrence,
			"source_path": image.SourcePath, "sha256": image.SHA256, "size": image.Size, "required": image.Required,
		}
		contentSHA, _ := beautifyJSONSHA256(content)
		return beautifySVGFidelityItem{
			ID: image.ID, ContentSHA256: contentSHA, Decision: "pass", ResourceID: id,
			Evidence: "resource_manifest_svg_usage", SourceSHA256: image.SHA256, SourceSize: image.Size,
		}, true
	}
	return beautifySVGFidelityItem{}, false
}
