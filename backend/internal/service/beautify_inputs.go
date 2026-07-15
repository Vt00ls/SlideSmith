package service

import (
	"archive/zip"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

type BeautifyInputsContract struct {
	Schema         string           `json:"schema"`
	TaskID         string           `json:"task_id"`
	Route          string           `json:"route"`
	RunnerProfile  string           `json:"runner_profile"`
	SourcePPTX     BeautifyFileRef  `json:"source_pptx"`
	SourceMarkdown BeautifyFileRef  `json:"source_markdown"`
	Identity       BeautifyFileRef  `json:"identity"`
	SlideLibrary   BeautifyFileRef  `json:"slide_library"`
	SourceProfile  BeautifyFileRef  `json:"source_profile"`
	ImageManifest  *BeautifyFileRef `json:"image_manifest,omitempty"`
	SlideCount     int              `json:"source_slide_count"`
	Canvas         BeautifyCanvas   `json:"source_canvas"`
	ImageCount     int              `json:"image_count"`
	Warnings       []string         `json:"warnings"`
	CheckedAt      string           `json:"checked_at"`
}

func discoverBeautifyInputs(projectPath, taskID, runnerProfile string) (*BeautifyInputsContract, error) {
	if strings.TrimSpace(taskID) == "" {
		return nil, fmt.Errorf("beautify inputs task_id is empty")
	}
	if strings.TrimSpace(runnerProfile) == "" {
		return nil, fmt.Errorf("beautify inputs runner_profile is empty")
	}
	sourcesPath, err := containedProjectContractPath(projectPath, "sources")
	if err != nil {
		return nil, err
	}
	info, resolvedSources, err := inspectContainedPath(projectPath, sourcesPath)
	if err != nil {
		return nil, fmt.Errorf("beautify_inventory.inputs: sources is missing or unsafe")
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("beautify_inventory.inputs: sources is not a directory")
	}
	entries, err := os.ReadDir(resolvedSources)
	if err != nil {
		return nil, err
	}
	var pptxNames []string
	var variants []string
	for _, entry := range entries {
		entryInfo, infoErr := entry.Info()
		if infoErr != nil {
			return nil, infoErr
		}
		if !entryInfo.Mode().IsRegular() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext == ".pptx" {
			pptxNames = append(pptxNames, entry.Name())
		} else if _, family := pptxDeckExtensions[ext]; family {
			variants = append(variants, entry.Name())
		}
	}
	sort.Strings(pptxNames)
	sort.Strings(variants)
	if len(variants) > 0 {
		return nil, fmt.Errorf("beautify_inventory.unsupported_source_type: only .pptx is supported; found %s", strings.Join(variants, ", "))
	}
	if len(pptxNames) == 0 {
		return nil, fmt.Errorf("beautify_inventory.inputs: exactly one .pptx source is required")
	}
	if len(pptxNames) != 1 {
		return nil, fmt.Errorf("beautify_inventory.multiple_pptx: found %d .pptx sources", len(pptxNames))
	}
	stem := strings.TrimSuffix(pptxNames[0], filepath.Ext(pptxNames[0]))
	markdownName, err := discoverBeautifySibling(entries, stem, ".md")
	if err != nil {
		return nil, fmt.Errorf("beautify_inventory.inputs: %w", err)
	}
	identityName, err := discoverBeautifyAnalysisSibling(projectPath, stem, ".identity.json")
	if err != nil {
		return nil, fmt.Errorf("beautify_inventory.missing_identity: %w", err)
	}
	libraryName, err := discoverBeautifyAnalysisSibling(projectPath, stem, ".slide_library.json")
	if err != nil {
		return nil, fmt.Errorf("beautify_inventory.missing_library: %w", err)
	}

	contract := &BeautifyInputsContract{
		Schema: beautifyInputsSchema, TaskID: taskID, Route: model.TaskRouteBeautify,
		RunnerProfile: runnerProfile, Warnings: []string{}, CheckedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	bindings := []struct {
		rel string
		out *BeautifyFileRef
	}{
		{filepath.ToSlash(filepath.Join("sources", pptxNames[0])), &contract.SourcePPTX},
		{filepath.ToSlash(filepath.Join("sources", markdownName)), &contract.SourceMarkdown},
		{filepath.ToSlash(filepath.Join("analysis", identityName)), &contract.Identity},
		{filepath.ToSlash(filepath.Join("analysis", libraryName)), &contract.SlideLibrary},
		{"analysis/source_profile.json", &contract.SourceProfile},
	}
	for _, binding := range bindings {
		ref, refErr := beautifyFileRef(projectPath, binding.rel)
		if refErr != nil {
			return nil, refErr
		}
		*binding.out = ref
	}

	pptxPath, _ := containedProjectContractPath(projectPath, contract.SourcePPTX.Path)
	contract.SlideCount, err = countPPTXSlides(pptxPath)
	if err != nil {
		return nil, fmt.Errorf("beautify_inventory.inputs: count source slides: %w", err)
	}
	if err := validateBeautifyLibrarySlideCount(projectPath, contract.SlideLibrary.Path, contract.SlideCount); err != nil {
		return nil, err
	}
	contract.Canvas, err = readBeautifyPPTXCanvas(pptxPath)
	if err != nil {
		return nil, fmt.Errorf("beautify_inventory.inputs: source canvas: %w", err)
	}

	if _, statErr := os.Lstat(filepath.Join(projectPath, "images", "image_manifest.json")); statErr == nil {
		ref, refErr := beautifyFileRef(projectPath, "images/image_manifest.json")
		if refErr != nil {
			return nil, refErr
		}
		contract.ImageManifest = &ref
		contract.ImageCount, err = beautifyManifestItemCount(projectPath, ref.Path)
		if err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(statErr) {
		return nil, statErr
	}
	return contract, nil
}

func BuildBeautifyInputsContract(projectPath, taskID, runnerProfile string) (*BeautifyInputsContract, error) {
	contract, err := discoverBeautifyInputs(projectPath, taskID, runnerProfile)
	if err != nil {
		return nil, err
	}
	if err := writeJSONAtomic(filepath.Join(projectPath, ".slidesmith", "contracts", "beautify_inputs.json"), contract); err != nil {
		return nil, err
	}
	return ValidateBeautifyInputsContract(projectPath, taskID)
}

func ValidateBeautifyInputsContract(projectPath, expectedTaskID string) (*BeautifyInputsContract, error) {
	var contract BeautifyInputsContract
	if err := beautifyReadJSON(projectPath, ".slidesmith/contracts/beautify_inputs.json", &contract); err != nil {
		return nil, err
	}
	if contract.Schema != beautifyInputsSchema || contract.Route != model.TaskRouteBeautify || contract.RunnerProfile == "" {
		return nil, fmt.Errorf("beautify inputs schema/route/profile binding is invalid")
	}
	if expectedTaskID == "" || contract.TaskID != expectedTaskID {
		return nil, fmt.Errorf("beautify inputs task_id = %q, expected %q", contract.TaskID, expectedTaskID)
	}
	for label, ref := range map[string]BeautifyFileRef{
		"source_pptx": contract.SourcePPTX, "source_markdown": contract.SourceMarkdown,
		"identity": contract.Identity, "slide_library": contract.SlideLibrary, "source_profile": contract.SourceProfile,
	} {
		if err := validateBeautifyFileRef(projectPath, ref, label); err != nil {
			return nil, err
		}
	}
	if strings.ToLower(filepath.Ext(contract.SourcePPTX.Path)) != ".pptx" {
		return nil, fmt.Errorf("beautify inputs source type is not .pptx")
	}
	if err := validateBeautifyUniquePPTXSet(projectPath, contract.SourcePPTX.Path); err != nil {
		return nil, err
	}
	pptxPath, _ := containedProjectContractPath(projectPath, contract.SourcePPTX.Path)
	slides, err := countPPTXSlides(pptxPath)
	if err != nil || slides != contract.SlideCount || slides <= 0 {
		return nil, fmt.Errorf("beautify inputs source slide count is stale")
	}
	if err := validateBeautifyLibrarySlideCount(projectPath, contract.SlideLibrary.Path, slides); err != nil {
		return nil, err
	}
	liveCanvas, err := readBeautifyPPTXCanvas(pptxPath)
	if err != nil || contract.Canvas.Width <= 0 || contract.Canvas.Height <= 0 || math.Abs(liveCanvas.AspectRatio-contract.Canvas.Width/contract.Canvas.Height) > 0.0001 {
		return nil, fmt.Errorf("beautify inputs source canvas is stale")
	}
	if contract.Canvas.AspectRatio > 0 && math.Abs(contract.Canvas.AspectRatio-contract.Canvas.Width/contract.Canvas.Height) > 0.0001 {
		return nil, fmt.Errorf("beautify inputs source canvas aspect ratio is inconsistent")
	}
	if contract.Canvas.Unit != "px" && contract.Canvas.Unit != "emu" {
		return nil, fmt.Errorf("beautify inputs source canvas unit %q is unsupported", contract.Canvas.Unit)
	}
	if contract.ImageManifest != nil {
		if err := validateBeautifyFileRef(projectPath, *contract.ImageManifest, "image_manifest"); err != nil {
			return nil, err
		}
		count, countErr := beautifyManifestItemCount(projectPath, contract.ImageManifest.Path)
		if countErr != nil || count != contract.ImageCount {
			return nil, fmt.Errorf("beautify inputs image manifest count is stale")
		}
	} else if contract.ImageCount != 0 {
		return nil, fmt.Errorf("beautify inputs image_count requires image_manifest")
	}
	return &contract, nil
}

func validateBeautifyUniquePPTXSet(projectPath, expectedRelativePath string) error {
	path, err := containedProjectContractPath(projectPath, "sources")
	if err != nil {
		return err
	}
	info, resolved, err := inspectContainedPath(projectPath, path)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("beautify inputs sources directory is missing or unsafe")
	}
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return fmt.Errorf("read beautify inputs sources directory")
	}
	var presentations []string
	for _, entry := range entries {
		entryInfo, infoErr := entry.Info()
		if infoErr != nil {
			return fmt.Errorf("inspect beautify source entry")
		}
		if !entryInfo.Mode().IsRegular() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if _, family := pptxDeckExtensions[ext]; family {
			if ext != ".pptx" {
				return fmt.Errorf("beautify_inventory.unsupported_source_type: only .pptx is supported")
			}
			presentations = append(presentations, filepath.ToSlash(filepath.Join("sources", entry.Name())))
		}
	}
	if len(presentations) != 1 || presentations[0] != expectedRelativePath {
		return fmt.Errorf("beautify_inventory.multiple_pptx: live source set does not match frozen input")
	}
	return nil
}

func discoverBeautifySibling(entries []os.DirEntry, stem, suffix string) (string, error) {
	want := templateFillCaseFold(stem + suffix)
	var matches []string
	for _, entry := range entries {
		if templateFillCaseFold(entry.Name()) == want {
			matches = append(matches, entry.Name())
		}
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("requires exactly one %s%s sibling, found %d", stem, suffix, len(matches))
	}
	return matches[0], nil
}

func discoverBeautifyAnalysisSibling(projectPath, stem, suffix string) (string, error) {
	path, err := containedProjectContractPath(projectPath, "analysis")
	if err != nil {
		return "", err
	}
	info, resolved, err := inspectContainedPath(projectPath, path)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("analysis directory is unavailable")
	}
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return "", err
	}
	return discoverBeautifySibling(entries, stem, suffix)
}

func validateBeautifyLibrarySlideCount(projectPath, relativePath string, expected int) error {
	var payload any
	if err := beautifyReadJSON(projectPath, relativePath, &payload); err != nil {
		return fmt.Errorf("beautify slide library: %w", err)
	}
	actual := 0
	switch typed := payload.(type) {
	case []any:
		actual = len(typed)
	case map[string]any:
		actual = beautifyJSONCount(typed, "slides", "pages")
		if actual == 0 {
			actual = beautifyJSONInt(typed, "slide_count", "page_count")
		}
	}
	if actual != expected {
		return fmt.Errorf("beautify slide library count = %d, source PPTX count = %d", actual, expected)
	}
	return nil
}

func beautifyManifestItemCount(projectPath, relativePath string) (int, error) {
	var payload any
	if err := beautifyReadJSON(projectPath, relativePath, &payload); err != nil {
		return 0, err
	}
	switch typed := payload.(type) {
	case []any:
		return len(typed), nil
	case map[string]any:
		return beautifyJSONCount(typed, "images", "items", "assets"), nil
	default:
		return 0, fmt.Errorf("beautify image manifest root must be an array or object")
	}
}

func beautifyJSONCount(payload map[string]any, keys ...string) int {
	for _, key := range keys {
		if values, ok := payload[key].([]any); ok {
			return len(values)
		}
	}
	return 0
}

func beautifyJSONInt(payload map[string]any, keys ...string) int {
	for _, key := range keys {
		switch value := payload[key].(type) {
		case float64:
			return int(value)
		case json.Number:
			parsed, _ := value.Int64()
			return int(parsed)
		}
	}
	return 0
}

func discoverBeautifyCanvas(projectPath string, paths ...string) BeautifyCanvas {
	for _, relativePath := range paths {
		var payload map[string]any
		if beautifyReadJSON(projectPath, relativePath, &payload) != nil {
			continue
		}
		candidates := []map[string]any{payload}
		for _, key := range []string{"canvas", "source_canvas", "page_size", "slide_size"} {
			if nested, ok := payload[key].(map[string]any); ok {
				candidates = append([]map[string]any{nested}, candidates...)
			}
		}
		for _, candidate := range candidates {
			width := beautifyNumber(candidate, "width", "width_emu", "cx")
			height := beautifyNumber(candidate, "height", "height_emu", "cy")
			if width > 0 && height > 0 && !math.IsInf(width/height, 0) {
				return BeautifyCanvas{Width: width, Height: height, Unit: beautifyString(candidate, "unit")}
			}
		}
	}
	return BeautifyCanvas{}
}

func readBeautifyPPTXCanvas(path string) (BeautifyCanvas, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return BeautifyCanvas{}, err
	}
	defer reader.Close()
	for _, entry := range reader.File {
		if filepath.ToSlash(entry.Name) != "ppt/presentation.xml" {
			continue
		}
		stream, err := entry.Open()
		if err != nil {
			return BeautifyCanvas{}, err
		}
		var document struct {
			SlideSize struct {
				CX float64 `xml:"cx,attr"`
				CY float64 `xml:"cy,attr"`
			} `xml:"sldSz"`
		}
		decodeErr := xml.NewDecoder(stream).Decode(&document)
		closeErr := stream.Close()
		if decodeErr != nil {
			return BeautifyCanvas{}, decodeErr
		}
		if closeErr != nil {
			return BeautifyCanvas{}, closeErr
		}
		if document.SlideSize.CX <= 0 || document.SlideSize.CY <= 0 {
			return BeautifyCanvas{}, fmt.Errorf("ppt/presentation.xml has no positive slide size")
		}
		return BeautifyCanvas{
			Width: document.SlideSize.CX, Height: document.SlideSize.CY, Unit: "emu",
			AspectRatio: document.SlideSize.CX / document.SlideSize.CY,
		}, nil
	}
	return BeautifyCanvas{}, fmt.Errorf("ppt/presentation.xml is missing")
}

func beautifyNumber(payload map[string]any, keys ...string) float64 {
	for _, key := range keys {
		if value, ok := payload[key].(float64); ok && !math.IsNaN(value) && !math.IsInf(value, 0) {
			return value
		}
	}
	return 0
}

func beautifyString(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return strings.TrimSpace(value)
}
