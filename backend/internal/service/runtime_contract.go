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

func validateSpecGenerateContract(projectPath string, expectedTaskID ...string) (map[string]any, error) {
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
	taskID := ""
	if len(expectedTaskID) > 0 {
		taskID = expectedTaskID[0]
	}
	_, resourcePlanContract, err := validateResourcePlanContract(projectPath, taskID)
	if err != nil {
		return nil, err
	}
	contract := map[string]any{
		"phase":         string(PhaseSpecGenerate),
		"project_path":  projectPath,
		"design_spec":   designSpec,
		"spec_lock":     specLock,
		"page_count":    pageCount,
		"checked_at":    time.Now().UTC().Format(time.RFC3339Nano),
		"resource_plan": resourcePlanContract,
	}
	if err := writeJSONPretty(filepath.Join(projectPath, ".slidesmith", "spec_contract.json"), contract); err != nil {
		return nil, err
	}
	if _, err := writeContractReport(projectPath, string(PhaseSpecGenerate), contract); err != nil {
		return nil, err
	}
	return contract, nil
}

func validateSVGExecuteContract(projectPath string, expectedTaskID ...string) (map[string]any, error) {
	contract, err := validateSVGBundleContract(projectPath, expectedTaskID...)
	if err != nil {
		return nil, err
	}
	pptxCount, err := countRegularFiles(filepath.Join(projectPath, "exports"), "*.pptx")
	if err != nil {
		return nil, err
	}
	if pptxCount > 0 {
		return nil, fmt.Errorf("svg_execute must not create pptx exports, found %d", pptxCount)
	}
	contract["checked_at"] = time.Now().UTC().Format(time.RFC3339Nano)
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

func bindFullPhaseContract(projectPath string, phase PipelinePhase, contract map[string]any, task *model.Task, workspace *TaskWorkspace, runtimeRunID string) (map[string]any, error) {
	if contract == nil {
		contract = map[string]any{}
	}
	if task == nil || task.RunnerProfile != model.RunnerProfileFullPPTMaster || task.RunnerProfileLockedAt == nil {
		return nil, fmt.Errorf("full phase %s requires a locked full-ppt-master task profile", phase)
	}
	contract["runner_profile"] = task.RunnerProfile
	contract["runner_profile_locked_at"] = task.RunnerProfileLockedAt.UTC().Format(time.RFC3339Nano)
	contract["runtime_run_id"] = runtimeRunID
	if workspace != nil {
		manifestPath := filepath.Join(workspace.HostDir, ".slidesmith", "runtime_manifest.json")
		manifestSHA, err := sha256File(manifestPath)
		if err != nil {
			return nil, fmt.Errorf("hash runtime manifest: %w", err)
		}
		contract["runtime_manifest_sha256"] = manifestSHA
		for key, rel := range map[string]string{
			"skill_lock_sha256":    filepath.Join(".slidesmith", "skill_lock.json"),
			"template_lock_sha256": filepath.Join(".slidesmith", "template_lock.json"),
		} {
			path := filepath.Join(workspace.HostDir, rel)
			if sha, err := sha256File(path); err == nil {
				contract[key] = sha
			} else if !os.IsNotExist(err) {
				return nil, err
			}
		}
	}

	switch phase {
	case PhaseSpecGenerate:
		designSHA, err := sha256File(filepath.Join(projectPath, "design_spec.md"))
		if err != nil {
			return nil, err
		}
		lockSHA, err := sha256File(filepath.Join(projectPath, "spec_lock.md"))
		if err != nil {
			return nil, err
		}
		contract["design_spec_sha256"] = designSHA
		contract["spec_lock_sha256"] = lockSHA
		planSHA, err := sha256File(filepath.Join(projectPath, ".slidesmith", "resource_plan.json"))
		if err != nil {
			return nil, err
		}
		contract["resource_plan_sha256"] = planSHA
	case PhaseImageAcquire:
		manifestSHA, err := sha256File(filepath.Join(projectPath, ".slidesmith", "resources_manifest.json"))
		if err != nil {
			return nil, err
		}
		planSHA, err := sha256File(filepath.Join(projectPath, ".slidesmith", "resource_plan.json"))
		if err != nil {
			return nil, err
		}
		contract["resources_manifest_sha256"] = manifestSHA
		contract["resource_plan_sha256"] = planSHA
	case PhaseSVGExecute, PhaseQualityCheck, PhaseFinalizeExport:
		hashes, err := svgBundleContractHashes(projectPath)
		if err != nil {
			return nil, err
		}
		for field, sha := range hashes {
			contract[field] = sha
		}
	}
	if phase == PhaseFinalizeExport {
		pptxSHA, err := sha256RegularFiles(filepath.Join(projectPath, "exports"), "*.pptx")
		if err != nil {
			return nil, err
		}
		contract["pptx_output_sha256"] = pptxSHA
	}
	if _, err := writeContractReport(projectPath, string(phase), contract); err != nil {
		return nil, err
	}
	if phase == PhaseSpecGenerate {
		if err := writeJSONPretty(filepath.Join(projectPath, ".slidesmith", "spec_contract.json"), contract); err != nil {
			return nil, err
		}
	}
	return contract, nil
}

func validateExistingSpecContract(projectPath string, task *model.Task, workspace *TaskWorkspace) (map[string]any, error) {
	contractPath := filepath.Join(projectPath, ".slidesmith", "spec_contract.json")
	raw, err := os.ReadFile(contractPath)
	if err != nil {
		return nil, err
	}
	var contract map[string]any
	if err := json.Unmarshal(raw, &contract); err != nil {
		return nil, err
	}
	if value, _ := contract["runner_profile"].(string); task == nil || value != task.RunnerProfile {
		return nil, fmt.Errorf("spec contract runner profile %q does not match task lock", value)
	}
	checks := []struct {
		field string
		path  string
	}{
		{"design_spec_sha256", filepath.Join(projectPath, "design_spec.md")},
		{"spec_lock_sha256", filepath.Join(projectPath, "spec_lock.md")},
	}
	if workspace != nil {
		checks = append(checks, struct {
			field string
			path  string
		}{"runtime_manifest_sha256", filepath.Join(workspace.HostDir, ".slidesmith", "runtime_manifest.json")})
		for field, relativePath := range map[string]string{
			"skill_lock_sha256":    filepath.Join(".slidesmith", "skill_lock.json"),
			"template_lock_sha256": filepath.Join(".slidesmith", "template_lock.json"),
		} {
			if expected, _ := contract[field].(string); expected != "" {
				checks = append(checks, struct {
					field string
					path  string
				}{field, filepath.Join(workspace.HostDir, relativePath)})
			}
		}
	}
	for _, check := range checks {
		expected, _ := contract[check.field].(string)
		if expected == "" {
			return nil, fmt.Errorf("spec contract missing %s", check.field)
		}
		actual, err := sha256File(check.path)
		if err != nil {
			return nil, err
		}
		if actual != expected {
			return nil, fmt.Errorf("spec contract stale: %s changed", check.path)
		}
	}
	return contract, nil
}

func sha256RegularFiles(root, pattern string) (string, error) {
	files, err := listRegularFiles(root, pattern)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	for _, path := range files {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return "", err
		}
		fileSHA, err := sha256File(path)
		if err != nil {
			return "", err
		}
		_, _ = io.WriteString(hash, filepath.ToSlash(rel)+"\x00"+fileSHA+"\n")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func validateFullSVGUpstreamContract(projectPath string, upstream PipelinePhase, task *model.Task) (map[string]any, error) {
	if task == nil {
		return nil, fmt.Errorf("%s upstream contract requires task binding", upstream)
	}
	path := filepath.Join(projectPath, ".slidesmith", "contracts", string(upstream)+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s upstream contract: %w", upstream, err)
	}
	var contract map[string]any
	if err := json.Unmarshal(raw, &contract); err != nil {
		return nil, err
	}
	if profile, _ := contract["runner_profile"].(string); profile != task.RunnerProfile {
		return nil, fmt.Errorf("%s upstream contract runner profile %q does not match task lock", upstream, profile)
	}
	actualHashes, err := svgBundleContractHashes(projectPath)
	if err != nil {
		return nil, err
	}
	for field, actual := range actualHashes {
		expected, _ := contract[field].(string)
		if expected == "" {
			return nil, fmt.Errorf("%s upstream contract missing %s", upstream, field)
		}
		if actual != expected {
			return nil, fmt.Errorf("%s upstream contract is stale: %s changed", upstream, field)
		}
	}
	if _, err := validateSVGBundleContract(projectPath, task.ID); err != nil {
		return nil, fmt.Errorf("%s upstream SVG bundle is stale: %w", upstream, err)
	}
	return contract, nil
}

func buildPublishedArtifactsContract(projectPath string, storage StorageService, artifacts []model.Artifact, publishVersion, route string) (map[string]any, error) {
	if len(artifacts) == 0 {
		return nil, fmt.Errorf("publish produced no platform artifacts")
	}
	expectedPageCount, err := expectedPublishedSlideCount(projectPath, route)
	if err != nil {
		return nil, err
	}
	artifactReports := make([]map[string]any, 0, len(artifacts))
	pptxCount := 0
	requiredTemplateFillArtifacts := map[string]bool{}
	if route == model.TaskRouteTemplateFill {
		requiredTemplateFillArtifacts, err = validateTemplateFillPublishedArtifactBindings(artifacts, publishVersion)
		if err != nil {
			return nil, err
		}
	}
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
	for _, kind := range templateFillRequiredPublishedArtifactKinds() {
		if route == model.TaskRouteTemplateFill && !requiredTemplateFillArtifacts[kind] {
			return nil, fmt.Errorf("published artifacts missing required template fill artifact kind %s", kind)
		}
	}
	manifest, hasManifest, err := readProjectRuntimeArtifactManifest(projectPath)
	if err != nil {
		return nil, err
	}
	manifestReport, err := validateRuntimeManifestAgainstArtifacts(manifest, hasManifest, artifacts, publishVersion)
	if err != nil {
		return nil, err
	}
	contract := map[string]any{
		"phase":           string(PhasePublish),
		"project_path":    projectPath,
		"publish_version": publishVersion,
		"route":           route,
		"expected_pages":  expectedPageCount,
		"artifact_count":  len(artifacts),
		"pptx_count":      pptxCount,
		"manifest":        manifestReport,
		"artifacts":       artifactReports,
		"checked_at":      time.Now().UTC().Format(time.RFC3339Nano),
	}
	if route == model.TaskRouteTemplateFill {
		contract["required_template_fill_artifacts"] = requiredTemplateFillArtifacts
	}
	return contract, nil
}

func expectedPublishedSlideCount(projectPath, route string) (int, error) {
	if route == model.TaskRouteTemplateFill {
		return templateFillExpectedSlideCount(projectPath)
	}
	return confirmedPageCount(projectPath), nil
}

func templateFillRequiredPublishedArtifactKinds() []string {
	kinds := make([]string, 0, len(templateFillRequiredPublishedArtifacts()))
	for _, requirement := range templateFillRequiredPublishedArtifacts() {
		kinds = append(kinds, requirement.Kind)
	}
	return kinds
}

type templateFillPublishedArtifactRequirement struct {
	RelativePath string
	Kind         string
}

func templateFillRequiredPublishedArtifacts() []templateFillPublishedArtifactRequirement {
	return []templateFillPublishedArtifactRequirement{
		{RelativePath: "analysis/fill_plan.json", Kind: model.ArtifactKindTemplateFillPlan},
		{RelativePath: "analysis/check_report.json", Kind: model.ArtifactKindTemplateFillCheckReport},
		{RelativePath: "validation/validate_report.json", Kind: model.ArtifactKindTemplateFillValidateReport},
		{RelativePath: "validation/readback.md", Kind: model.ArtifactKindTemplateFillReadback},
	}
}

func validateTemplateFillPublishedArtifactBindings(artifacts []model.Artifact, publishVersion string) (map[string]bool, error) {
	requiredByPath := make(map[string]string, len(templateFillRequiredPublishedArtifacts()))
	requiredPathByKind := make(map[string]string, len(templateFillRequiredPublishedArtifacts()))
	foundByKind := make(map[string]bool, len(templateFillRequiredPublishedArtifacts()))
	for _, requirement := range templateFillRequiredPublishedArtifacts() {
		requiredByPath[requirement.RelativePath] = requirement.Kind
		requiredPathByKind[requirement.Kind] = requirement.RelativePath
		foundByKind[requirement.Kind] = false
	}

	seenPaths := make(map[string]bool, len(artifacts))
	pptxCount := 0
	for _, artifact := range artifacts {
		relativePath, err := exactPublishedArtifactRelativePath(artifact, publishVersion)
		if err != nil {
			return nil, err
		}
		if seenPaths[relativePath] {
			return nil, fmt.Errorf("duplicate published artifact relative path %s", relativePath)
		}
		seenPaths[relativePath] = true

		if expectedKind, required := requiredByPath[relativePath]; required {
			if artifact.Kind != expectedKind {
				return nil, fmt.Errorf("template fill artifact %s has kind %q, expected %q", relativePath, artifact.Kind, expectedKind)
			}
			foundByKind[expectedKind] = true
		} else if expectedPath, reservedKind := requiredPathByKind[artifact.Kind]; reservedKind {
			return nil, fmt.Errorf("template fill artifact kind %q must use exact relative path %s, got %s", artifact.Kind, expectedPath, relativePath)
		}

		isPPTXPath := isExactTemplateFillPPTXRelativePath(relativePath)
		if artifact.Kind == model.ArtifactKindPPTX && !isPPTXPath {
			return nil, fmt.Errorf("template fill PPTX artifact must use exact relative path exports/*.pptx, got %s", relativePath)
		}
		if isPPTXPath {
			if artifact.Kind != model.ArtifactKindPPTX {
				return nil, fmt.Errorf("template fill artifact %s has kind %q, expected %q", relativePath, artifact.Kind, model.ArtifactKindPPTX)
			}
			pptxCount++
		}
	}

	for _, requirement := range templateFillRequiredPublishedArtifacts() {
		if !foundByKind[requirement.Kind] {
			return nil, fmt.Errorf("published artifacts missing exact template fill artifact %s with kind %s", requirement.RelativePath, requirement.Kind)
		}
	}
	if pptxCount == 0 {
		return nil, fmt.Errorf("published artifacts missing exact Template Fill exports/*.pptx artifact")
	}
	return foundByKind, nil
}

func exactPublishedArtifactRelativePath(artifact model.Artifact, publishVersion string) (string, error) {
	objectKey := strings.TrimSpace(artifact.ObjectKey)
	if objectKey == "" || objectKey != filepath.ToSlash(objectKey) {
		return "", fmt.Errorf("template fill artifact object key is not canonical: %q", artifact.ObjectKey)
	}
	if strings.TrimSpace(artifact.TaskID) == "" {
		return "", fmt.Errorf("template fill artifact %s has empty task id", objectKey)
	}
	prefix := filepath.ToSlash(filepath.Join("tasks", artifact.TaskID, "artifacts", publishVersion)) + "/"
	if !strings.HasPrefix(objectKey, prefix) {
		return "", fmt.Errorf("template fill artifact object key %s is outside exact publish prefix %s", objectKey, prefix)
	}
	relativePath := strings.TrimPrefix(objectKey, prefix)
	cleanRelativePath, err := cleanArtifactRel(relativePath)
	if err != nil {
		return "", err
	}
	if cleanRelativePath != relativePath {
		return "", fmt.Errorf("template fill artifact relative path is not canonical: %s", relativePath)
	}
	return relativePath, nil
}

func isExactTemplateFillPPTXRelativePath(relativePath string) bool {
	if !strings.HasPrefix(relativePath, "exports/") {
		return false
	}
	name := strings.TrimPrefix(relativePath, "exports/")
	return name != "" && !strings.Contains(name, "/") && strings.HasSuffix(name, ".pptx")
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
