package service

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

const sourcePrepareContractSchema = "slidesmith.source_prepare_contract.v1"

var readableSourceExtensions = map[string]struct{}{
	".md":       {},
	".markdown": {},
	".txt":      {},
	".text":     {},
	".csv":      {},
	".tsv":      {},
}

var pptxDeckExtensions = map[string]struct{}{
	".pptx": {},
	".pptm": {},
	".ppsx": {},
	".ppsm": {},
	".potx": {},
	".potm": {},
}

type sourcePrepareContractItem struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
	Size int64  `json:"size"`
}

type sourcePrepareFile struct {
	name       string
	extension  string
	stem       string
	isProfile  bool
	isReadable bool
}

func validateSourcePrepareContract(projectPath, route string) (map[string]any, error) {
	absoluteProjectPath, err := filepath.Abs(projectPath)
	if err != nil {
		return nil, fmt.Errorf("resolve source prepare project path: %w", err)
	}
	absoluteProjectPath = filepath.Clean(absoluteProjectPath)

	sourceArtifacts, sourceFiles, warnings, err := scanSourcePrepareFiles(absoluteProjectPath)
	if err != nil {
		return nil, err
	}
	if len(sourceArtifacts) == 0 {
		return nil, fmt.Errorf("source prepare contract requires at least one regular file in sources")
	}

	normalizedMarkdownCount := 0
	conversionProfileCount := 0
	pptxDeckStems := make([]string, 0)
	for _, sourceFile := range sourceFiles {
		if sourceFile.isReadable {
			normalizedMarkdownCount++
		}
		if sourceFile.isProfile {
			conversionProfileCount++
		}
		if _, ok := pptxDeckExtensions[sourceFile.extension]; ok {
			pptxDeckStems = append(pptxDeckStems, sourceFile.stem)
		}
	}
	if route == model.TaskRouteMain && normalizedMarkdownCount == 0 {
		return nil, fmt.Errorf("source prepare contract for main requires at least one readable source file")
	}

	analysisArtifacts, analysisFiles, err := scanSourcePrepareAnalysis(absoluteProjectPath)
	if err != nil {
		return nil, err
	}
	hasSourceProfile := analysisFiles["source_profile.json"]
	if len(pptxDeckStems) > 0 {
		if !hasSourceProfile {
			return nil, fmt.Errorf("source prepare contract requires analysis/source_profile.json for PPTX sources")
		}
		for _, stem := range pptxDeckStems {
			identityName := stem + ".identity.json"
			if !analysisFiles[identityName] {
				return nil, fmt.Errorf("source prepare contract requires analysis/%s for deck %s", identityName, stem)
			}
			libraryName := stem + ".slide_library.json"
			if !analysisFiles[libraryName] {
				return nil, fmt.Errorf("source prepare contract requires analysis/%s for deck %s", libraryName, stem)
			}
		}
	}

	sourceProfile := ""
	if hasSourceProfile {
		sourceProfile = "analysis/source_profile.json"
	}
	contract := map[string]any{
		"schema":                    sourcePrepareContractSchema,
		"phase":                     string(PhaseSourcePrepare),
		"route":                     route,
		"project_path":              absoluteProjectPath,
		"source_count":              inferSourcePrepareCount(sourceFiles),
		"normalized_markdown_count": normalizedMarkdownCount,
		"conversion_profile_count":  conversionProfileCount,
		"pptx_deck_count":           len(pptxDeckStems),
		"has_source_profile":        hasSourceProfile,
		"source_profile":            sourceProfile,
		"sources":                   sourceArtifacts,
		"analysis":                  analysisArtifacts,
		"warnings":                  warnings,
		"checked_at":                time.Now().UTC().Format(time.RFC3339Nano),
	}
	if _, err := writeContractReport(absoluteProjectPath, string(PhaseSourcePrepare), contract); err != nil {
		return nil, fmt.Errorf("write source prepare contract: %w", err)
	}
	compatibilityPath := filepath.Join(absoluteProjectPath, ".slidesmith", "source_prepare_contract.json")
	if err := writeJSONPretty(compatibilityPath, contract); err != nil {
		return nil, fmt.Errorf("write source prepare compatibility contract: %w", err)
	}
	return contract, nil
}

func scanSourcePrepareFiles(projectPath string) ([]sourcePrepareContractItem, []sourcePrepareFile, []string, error) {
	sourcesPath := filepath.Join(projectPath, "sources")
	info, err := os.Stat(sourcesPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil, fmt.Errorf("source prepare contract requires sources directory: %s", sourcesPath)
		}
		return nil, nil, nil, fmt.Errorf("stat sources directory: %w", err)
	}
	if !info.IsDir() {
		return nil, nil, nil, fmt.Errorf("source prepare contract requires sources to be a directory: %s", sourcesPath)
	}
	entries, err := os.ReadDir(sourcesPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read sources directory: %w", err)
	}

	artifacts := make([]sourcePrepareContractItem, 0)
	files := make([]sourcePrepareFile, 0)
	warnings := make([]string, 0)
	for _, entry := range entries {
		entryInfo, err := entry.Info()
		if err != nil {
			return nil, nil, nil, fmt.Errorf("stat source %s: %w", entry.Name(), err)
		}
		if !entryInfo.Mode().IsRegular() {
			continue
		}
		name := entry.Name()
		lowerName := strings.ToLower(name)
		extension := strings.ToLower(filepath.Ext(name))
		stem := strings.TrimSuffix(name, filepath.Ext(name))
		kind := model.ArtifactKindSource
		isProfile := strings.HasSuffix(lowerName, ".conversion_profile.json")
		isReadable := isReadableSourceExtension(extension)
		if isProfile {
			kind = model.ArtifactKindSourceConversionProfile
			stem = name[:len(name)-len(".conversion_profile.json")]
		} else if isReadable {
			kind = model.ArtifactKindSourceMarkdown
		}
		relativePath := filepath.ToSlash(filepath.Join("sources", name))
		artifacts = append(artifacts, sourcePrepareContractItem{
			Path: relativePath,
			Kind: kind,
			Size: entryInfo.Size(),
		})
		files = append(files, sourcePrepareFile{
			name:       name,
			extension:  extension,
			stem:       stem,
			isProfile:  isProfile,
			isReadable: isReadable,
		})
		if extension == ".xls" {
			warnings = append(warnings, relativePath+": legacy .xls archived only; no Markdown conversion")
		}
	}
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].Path < artifacts[j].Path })
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	sort.Strings(warnings)
	return artifacts, files, warnings, nil
}

func scanSourcePrepareAnalysis(projectPath string) ([]sourcePrepareContractItem, map[string]bool, error) {
	analysisPath := filepath.Join(projectPath, "analysis")
	info, err := os.Stat(analysisPath)
	if err != nil {
		if os.IsNotExist(err) {
			return make([]sourcePrepareContractItem, 0), map[string]bool{}, nil
		}
		return nil, nil, fmt.Errorf("stat analysis directory: %w", err)
	}
	if !info.IsDir() {
		return nil, nil, fmt.Errorf("source prepare contract requires analysis to be a directory: %s", analysisPath)
	}
	entries, err := os.ReadDir(analysisPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read analysis directory: %w", err)
	}

	artifacts := make([]sourcePrepareContractItem, 0)
	files := make(map[string]bool)
	for _, entry := range entries {
		entryInfo, err := entry.Info()
		if err != nil {
			return nil, nil, fmt.Errorf("stat analysis file %s: %w", entry.Name(), err)
		}
		if !entryInfo.Mode().IsRegular() {
			continue
		}
		name := entry.Name()
		kind := ""
		switch {
		case name == "source_profile.json":
			kind = model.ArtifactKindSourceProfile
		case strings.HasSuffix(name, ".identity.json"):
			kind = model.ArtifactKindPPTXIdentity
		case strings.HasSuffix(name, ".slide_library.json"):
			kind = model.ArtifactKindPPTXSlideLibrary
		default:
			continue
		}
		files[name] = true
		artifacts = append(artifacts, sourcePrepareContractItem{
			Path: filepath.ToSlash(filepath.Join("analysis", name)),
			Kind: kind,
			Size: entryInfo.Size(),
		})
	}
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].Path < artifacts[j].Path })
	return artifacts, files, nil
}

func inferSourcePrepareCount(files []sourcePrepareFile) int {
	count := 0
	profileStems := make(map[string]bool)
	for _, file := range files {
		if file.isProfile {
			profileStems[file.stem] = true
			continue
		}
		count++
	}
	for _, file := range files {
		if file.isProfile || file.extension != ".md" {
			continue
		}
		for _, candidate := range files {
			if candidate.isProfile || candidate.name == file.name || candidate.stem != file.stem {
				continue
			}
			if candidate.extension == ".txt" || (!candidate.isReadable && profileStems[file.stem]) {
				count--
				break
			}
		}
	}
	return count
}

func isReadableSourceExtension(extension string) bool {
	_, ok := readableSourceExtensions[extension]
	return ok
}
