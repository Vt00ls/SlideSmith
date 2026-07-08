package service

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

func validateSpecGenerateContract(projectPath string) (map[string]any, error) {
	designSpec := filepath.Join(projectPath, "design_spec.md")
	specLock := filepath.Join(projectPath, "spec_lock.md")
	if err := requireNonEmptyFile(designSpec); err != nil {
		return nil, err
	}
	if err := requireNonEmptyFile(specLock); err != nil {
		return nil, err
	}
	svgCount, err := countRegularFiles(filepath.Join(projectPath, "svg_output"), "*.svg")
	if err != nil {
		return nil, err
	}
	if svgCount > 0 {
		return nil, fmt.Errorf("spec_generate must not create svg_output files, found %d", svgCount)
	}
	pageCount := confirmedPageCount(projectPath)
	contract := map[string]any{
		"phase":        string(PhaseSpecGenerate),
		"project_path": projectPath,
		"design_spec":  designSpec,
		"spec_lock":    specLock,
		"page_count":   pageCount,
		"checked_at":   time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := writeJSONPretty(filepath.Join(projectPath, ".slidesmith", "spec_contract.json"), contract); err != nil {
		return nil, err
	}
	if _, err := writeContractReport(projectPath, string(PhaseSpecGenerate), contract); err != nil {
		return nil, err
	}
	return contract, nil
}

func validateSVGExecuteContract(projectPath string) (map[string]any, error) {
	expectedPageCount := confirmedPageCount(projectPath)
	svgCount, err := countRegularFiles(filepath.Join(projectPath, "svg_output"), "*.svg")
	if err != nil {
		return nil, err
	}
	if svgCount != expectedPageCount {
		return nil, fmt.Errorf("svg_execute produced %d svg files, expected %d", svgCount, expectedPageCount)
	}
	notesPath := filepath.Join(projectPath, "notes", "total.md")
	if err := requireNonEmptyFile(notesPath); err != nil {
		return nil, err
	}
	pptxCount, err := countRegularFiles(filepath.Join(projectPath, "exports"), "*.pptx")
	if err != nil {
		return nil, err
	}
	if pptxCount > 0 {
		return nil, fmt.Errorf("svg_execute must not create pptx exports, found %d", pptxCount)
	}
	contract := map[string]any{
		"phase":          string(PhaseSVGExecute),
		"project_path":   projectPath,
		"expected_pages": expectedPageCount,
		"svg_count":      svgCount,
		"notes":          notesPath,
		"checked_at":     time.Now().UTC().Format(time.RFC3339Nano),
	}
	if _, err := writeContractReport(projectPath, string(PhaseSVGExecute), contract); err != nil {
		return nil, err
	}
	return contract, nil
}

func validateQualityCheckContract(projectPath string) (map[string]any, error) {
	expectedPageCount := confirmedPageCount(projectPath)
	svgCount, err := countRegularFiles(filepath.Join(projectPath, "svg_output"), "*.svg")
	if err != nil {
		return nil, err
	}
	if svgCount != expectedPageCount {
		return nil, fmt.Errorf("quality_check saw %d svg files, expected %d", svgCount, expectedPageCount)
	}
	pptxCount, err := countRegularFiles(filepath.Join(projectPath, "exports"), "*.pptx")
	if err != nil {
		return nil, err
	}
	if pptxCount > 0 {
		return nil, fmt.Errorf("quality_check must not create pptx exports, found %d", pptxCount)
	}
	contract := map[string]any{
		"phase":          string(PhaseQualityCheck),
		"project_path":   projectPath,
		"expected_pages": expectedPageCount,
		"svg_count":      svgCount,
		"checked_at":     time.Now().UTC().Format(time.RFC3339Nano),
	}
	if _, err := writeContractReport(projectPath, string(PhaseQualityCheck), contract); err != nil {
		return nil, err
	}
	return contract, nil
}

func validatePPTXExportContract(projectPath string) (map[string]any, error) {
	pptxFiles, err := listRegularFiles(filepath.Join(projectPath, "exports"), "*.pptx")
	if err != nil {
		return nil, err
	}
	if len(pptxFiles) == 0 {
		return nil, fmt.Errorf("finalize_export did not produce exports/*.pptx in %s", projectPath)
	}
	expectedPageCount := confirmedPageCount(projectPath)
	pptxReports := make([]map[string]any, 0, len(pptxFiles))
	for _, pptxPath := range pptxFiles {
		slideCount, err := countPPTXSlides(pptxPath)
		if err != nil {
			return nil, fmt.Errorf("finalize_export produced invalid pptx %s: %w", pptxPath, err)
		}
		if slideCount != expectedPageCount {
			return nil, fmt.Errorf("finalize_export pptx %s has %d slides, expected %d", pptxPath, slideCount, expectedPageCount)
		}
		pptxReports = append(pptxReports, map[string]any{
			"path":        pptxPath,
			"slide_count": slideCount,
		})
	}
	contract := map[string]any{
		"phase":          string(PhaseFinalizeExport),
		"project_path":   projectPath,
		"expected_pages": expectedPageCount,
		"pptx_count":     len(pptxFiles),
		"pptx":           pptxReports,
		"checked_at":     time.Now().UTC().Format(time.RFC3339Nano),
	}
	if _, err := writeContractReport(projectPath, string(PhaseFinalizeExport), contract); err != nil {
		return nil, err
	}
	return contract, nil
}

func buildPublishedArtifactsContract(projectPath string, storage StorageService, artifacts []model.Artifact, publishVersion string) (map[string]any, error) {
	if len(artifacts) == 0 {
		return nil, fmt.Errorf("publish produced no platform artifacts")
	}
	expectedPageCount := confirmedPageCount(projectPath)
	artifactReports := make([]map[string]any, 0, len(artifacts))
	pptxCount := 0
	for _, artifact := range artifacts {
		report, err := validateStoredArtifact(storage, artifact)
		if err != nil {
			return nil, err
		}
		if publishVersion != "" && artifact.PublishVersion != publishVersion {
			return nil, fmt.Errorf("artifact %s publish_version = %q, expected %q", artifact.ObjectKey, artifact.PublishVersion, publishVersion)
		}
		if artifact.Kind == model.ArtifactKindPPTX {
			slideCount, err := countPPTXSlides(storage.Path(artifact.ObjectKey))
			if err != nil {
				return nil, fmt.Errorf("published pptx %s is invalid: %w", artifact.ObjectKey, err)
			}
			if slideCount != expectedPageCount {
				return nil, fmt.Errorf("published pptx %s has %d slides, expected %d", artifact.ObjectKey, slideCount, expectedPageCount)
			}
			report["slide_count"] = slideCount
			pptxCount++
		}
		artifactReports = append(artifactReports, report)
	}
	if pptxCount == 0 {
		return nil, fmt.Errorf("published artifacts missing pptx")
	}
	manifest, hasManifest, err := readProjectRuntimeArtifactManifest(projectPath)
	if err != nil {
		return nil, err
	}
	manifestReport, err := validateRuntimeManifestAgainstArtifacts(manifest, hasManifest, artifacts, publishVersion)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"phase":           string(PhasePublish),
		"project_path":    projectPath,
		"publish_version": publishVersion,
		"expected_pages":  expectedPageCount,
		"artifact_count":  len(artifacts),
		"pptx_count":      pptxCount,
		"manifest":        manifestReport,
		"artifacts":       artifactReports,
		"checked_at":      time.Now().UTC().Format(time.RFC3339Nano),
	}, nil
}

func buildFinalTaskContract(projectPath string, publishContract map[string]any) map[string]any {
	return map[string]any{
		"phase":            "final",
		"project_path":     projectPath,
		"publish_contract": publishContract,
		"checked_at":       time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func validateStoredArtifact(storage StorageService, artifact model.Artifact) (map[string]any, error) {
	path := storage.Path(artifact.ObjectKey)
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("artifact object missing in storage: %s", artifact.ObjectKey)
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("artifact object is not a regular file: %s", artifact.ObjectKey)
	}
	if artifact.Size != info.Size() {
		return nil, fmt.Errorf("artifact %s size = %d, storage size = %d", artifact.ObjectKey, artifact.Size, info.Size())
	}
	sha, err := sha256File(path)
	if err != nil {
		return nil, err
	}
	if artifact.SHA256 != "" && artifact.SHA256 != sha {
		return nil, fmt.Errorf("artifact %s sha256 mismatch", artifact.ObjectKey)
	}
	return map[string]any{
		"kind":       artifact.Kind,
		"name":       artifact.Name,
		"object_key": artifact.ObjectKey,
		"size":       artifact.Size,
		"sha256":     sha,
	}, nil
}

func validateRuntimeManifestAgainstArtifacts(manifest runtimeArtifactManifest, hasManifest bool, artifacts []model.Artifact, publishVersion string) (map[string]any, error) {
	report := map[string]any{
		"present": hasManifest,
	}
	if !hasManifest {
		return report, nil
	}
	byRel := map[string]model.Artifact{}
	for _, artifact := range artifacts {
		rel := publishedArtifactRel(artifact.ObjectKey, publishVersion)
		if rel != "" {
			byRel[rel] = artifact
		}
	}
	var verified []map[string]any
	for _, item := range manifest.Artifacts {
		if item.Path == "" {
			continue
		}
		rel, err := cleanArtifactRel(item.Path)
		if err != nil {
			return nil, err
		}
		artifact, ok := byRel[rel]
		if !ok {
			return nil, fmt.Errorf("runtime artifact manifest item %s was not published", rel)
		}
		if item.Size > 0 && artifact.Size != item.Size {
			return nil, fmt.Errorf("runtime artifact manifest item %s size = %d, published size = %d", rel, item.Size, artifact.Size)
		}
		if item.SHA256 != "" && artifact.SHA256 != item.SHA256 {
			return nil, fmt.Errorf("runtime artifact manifest item %s sha256 mismatch", rel)
		}
		verified = append(verified, map[string]any{
			"path":       rel,
			"object_key": artifact.ObjectKey,
			"size":       artifact.Size,
			"sha256":     artifact.SHA256,
		})
	}
	report["item_count"] = len(manifest.Artifacts)
	report["verified_count"] = len(verified)
	report["verified"] = verified
	return report, nil
}

func publishedArtifactRel(objectKey, publishVersion string) string {
	marker := "/artifacts/" + publishVersion + "/"
	index := strings.Index(objectKey, marker)
	if index == -1 {
		return ""
	}
	return objectKey[index+len(marker):]
}

func writeContractReport(projectPath, name string, contract map[string]any) (string, error) {
	path := filepath.Join(projectPath, ".slidesmith", "contracts", name+".json")
	if err := writeJSONPretty(path, contract); err != nil {
		return "", err
	}
	return path, nil
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func requireNonEmptyFile(path string) error {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return fmt.Errorf("required file not found: %s", path)
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("required path is not a regular file: %s", path)
	}
	if info.Size() == 0 {
		return fmt.Errorf("required file is empty: %s", path)
	}
	return nil
}

func countRegularFiles(dir, pattern string) (int, error) {
	files, err := listRegularFiles(dir, pattern)
	if err != nil {
		return 0, err
	}
	return len(files), nil
}

func listRegularFiles(dir, pattern string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		return nil, err
	}
	files := make([]string, 0, len(matches))
	for _, match := range matches {
		info, err := os.Stat(match)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if info.Mode().IsRegular() {
			files = append(files, match)
		}
	}
	return files, nil
}

func countPPTXSlides(path string) (int, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return 0, err
	}
	defer reader.Close()

	count := 0
	for _, file := range reader.File {
		name := filepath.ToSlash(file.Name)
		if !strings.HasPrefix(name, "ppt/slides/slide") || !strings.HasSuffix(name, ".xml") {
			continue
		}
		slideName := strings.TrimSuffix(strings.TrimPrefix(name, "ppt/slides/slide"), ".xml")
		if strings.Contains(slideName, "/") {
			continue
		}
		if number, err := strconv.Atoi(slideName); err == nil && number > 0 {
			count++
		}
	}
	if count == 0 {
		return 0, fmt.Errorf("pptx contains no ppt/slides/slide*.xml entries")
	}
	return count, nil
}

func confirmedPageCount(projectPath string) int {
	raw, err := os.ReadFile(filepath.Join(projectPath, "confirm_ui", "result.json"))
	if err != nil {
		return 3
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return 3
	}
	value := result["page_count"]
	switch typed := value.(type) {
	case float64:
		return clampPageCount(int(typed))
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 3
		}
		return clampPageCount(parsed)
	default:
		return 3
	}
}

func clampPageCount(value int) int {
	if value < 3 || value > 10 {
		return 3
	}
	return value
}
