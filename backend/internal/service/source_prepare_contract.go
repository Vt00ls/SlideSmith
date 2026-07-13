package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	return validateSourcePrepareContractWithSourceCount(projectPath, route, 0)
}

func validateSourcePrepareContractWithSourceCount(projectPath, route string, manifestSourceCount int) (map[string]any, error) {
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
	pptxDeckFilesByFoldedStem := make(map[string]string)
	for _, sourceFile := range sourceFiles {
		if sourceFile.isReadable {
			normalizedMarkdownCount++
		}
		if sourceFile.isProfile {
			conversionProfileCount++
		}
		if _, ok := pptxDeckExtensions[sourceFile.extension]; ok {
			foldedStem := templateFillCaseFold(sourceFile.stem)
			if previousFile, exists := pptxDeckFilesByFoldedStem[foldedStem]; exists {
				return nil, fmt.Errorf(
					"source prepare contract deck stem collision: %s and %s share case-folded stem %q",
					previousFile,
					sourceFile.name,
					foldedStem,
				)
			}
			pptxDeckFilesByFoldedStem[foldedStem] = sourceFile.name
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
	sourceCount := inferSourcePrepareCount(sourceFiles)
	if manifestSourceCount > 0 {
		sourceCount = manifestSourceCount
	}
	contract := map[string]any{
		"schema":                    sourcePrepareContractSchema,
		"phase":                     string(PhaseSourcePrepare),
		"route":                     route,
		"project_path":              absoluteProjectPath,
		"source_count":              sourceCount,
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
	payload, err := json.MarshalIndent(contract, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal source prepare contract: %w", err)
	}
	payload = append(payload, '\n')
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, fmt.Errorf("decode source prepare contract: %w", err)
	}
	canonicalPath := filepath.Join(absoluteProjectPath, ".slidesmith", "contracts", "source_prepare.json")
	compatibilityPath := filepath.Join(absoluteProjectPath, ".slidesmith", "source_prepare_contract.json")
	if err := writeSourcePrepareContractPair(canonicalPath, compatibilityPath, payload); err != nil {
		return nil, err
	}
	return decoded, nil
}

type sourcePrepareContractTargetState struct {
	existed  bool
	contents []byte
	mode     os.FileMode
}

func writeSourcePrepareContractPair(canonicalPath, compatibilityPath string, payload []byte) error {
	canonicalState, err := inspectSourcePrepareContractTarget(canonicalPath)
	if err != nil {
		return err
	}
	compatibilityState, err := inspectSourcePrepareContractTarget(compatibilityPath)
	if err != nil {
		return err
	}
	if canonicalState.existed {
		canonicalState.contents, err = os.ReadFile(canonicalPath)
		if err != nil {
			return fmt.Errorf("read existing source prepare contract %s: %w", canonicalPath, err)
		}
	}
	for _, targetPath := range []string{canonicalPath, compatibilityPath} {
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return fmt.Errorf("create source prepare contract directory for %s: %w", targetPath, err)
		}
	}

	canonicalTemp, err := stageSourcePrepareContract(canonicalPath, payload, canonicalState.mode)
	if err != nil {
		return err
	}
	defer func() {
		if canonicalTemp != "" {
			_ = os.Remove(canonicalTemp)
		}
	}()
	compatibilityTemp, err := stageSourcePrepareContract(compatibilityPath, payload, compatibilityState.mode)
	if err != nil {
		return err
	}
	defer func() {
		if compatibilityTemp != "" {
			_ = os.Remove(compatibilityTemp)
		}
	}()

	if err := os.Rename(canonicalTemp, canonicalPath); err != nil {
		return fmt.Errorf("commit source prepare contract %s: %w", canonicalPath, err)
	}
	canonicalTemp = ""
	if err := os.Rename(compatibilityTemp, compatibilityPath); err != nil {
		commitErr := fmt.Errorf("commit source prepare compatibility contract %s: %w", compatibilityPath, err)
		if rollbackErr := restoreSourcePrepareContractTarget(canonicalPath, canonicalState); rollbackErr != nil {
			return errors.Join(commitErr, fmt.Errorf("rollback source prepare contract %s: %w", canonicalPath, rollbackErr))
		}
		return commitErr
	}
	compatibilityTemp = ""
	return nil
}

func inspectSourcePrepareContractTarget(path string) (sourcePrepareContractTargetState, error) {
	state := sourcePrepareContractTargetState{mode: 0o644}
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return state, nil
		}
		return state, fmt.Errorf("inspect source prepare contract target %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return state, fmt.Errorf("source prepare contract target is not a regular file: %s", path)
	}
	state.existed = true
	state.mode = info.Mode().Perm()
	return state, nil
}

func stageSourcePrepareContract(targetPath string, payload []byte, mode os.FileMode) (string, error) {
	tempFile, err := os.CreateTemp(filepath.Dir(targetPath), "."+filepath.Base(targetPath)+".tmp-*")
	if err != nil {
		return "", fmt.Errorf("stage source prepare contract %s: %w", targetPath, err)
	}
	tempPath := tempFile.Name()
	removeTemp := true
	defer func() {
		if tempFile != nil {
			_ = tempFile.Close()
		}
		if removeTemp {
			_ = os.Remove(tempPath)
		}
	}()
	if written, err := tempFile.Write(payload); err != nil {
		return "", fmt.Errorf("write staged source prepare contract %s: %w", targetPath, err)
	} else if written != len(payload) {
		return "", fmt.Errorf("write staged source prepare contract %s: %w", targetPath, io.ErrShortWrite)
	}
	if err := tempFile.Chmod(mode.Perm()); err != nil {
		return "", fmt.Errorf("set staged source prepare contract mode %s: %w", targetPath, err)
	}
	if err := tempFile.Sync(); err != nil {
		return "", fmt.Errorf("sync staged source prepare contract %s: %w", targetPath, err)
	}
	if err := tempFile.Close(); err != nil {
		return "", fmt.Errorf("close staged source prepare contract %s: %w", targetPath, err)
	}
	tempFile = nil
	removeTemp = false
	return tempPath, nil
}

func restoreSourcePrepareContractTarget(path string, state sourcePrepareContractTargetState) error {
	if !state.existed {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	tempPath, err := stageSourcePrepareContract(path, state.contents, state.mode)
	if err != nil {
		return err
	}
	defer os.Remove(tempPath)
	return os.Rename(tempPath, path)
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
