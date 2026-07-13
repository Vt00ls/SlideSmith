package service

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

func TestValidatePPTXExportContractRejectsSlideCountMismatch(t *testing.T) {
	projectPath := t.TempDir()
	mustWriteFileNoTest(projectPath, filepath.Join("confirm_ui", "result.json"), `{"page_count":3}`+"\n")
	mustWritePPTXNoTest(projectPath, filepath.Join("exports", "result.pptx"), 2)

	_, err := validatePPTXExportContract(projectPath)
	if err == nil {
		t.Fatal("validatePPTXExportContract() error = nil, want slide count mismatch")
	}
	if !strings.Contains(err.Error(), "has 2 slides, expected 3") {
		t.Fatalf("error = %q, want slide count mismatch", err)
	}
}

func TestValidatePPTXExportContractReportsSlideCount(t *testing.T) {
	projectPath := t.TempDir()
	mustWriteFileNoTest(projectPath, filepath.Join("confirm_ui", "result.json"), `{"page_count":3}`+"\n")
	mustWritePPTXNoTest(projectPath, filepath.Join("exports", "result.pptx"), 3)

	contract, err := validatePPTXExportContract(projectPath)
	if err != nil {
		t.Fatalf("validatePPTXExportContract() error = %v", err)
	}
	if contract["expected_pages"] != 3 {
		t.Fatalf("expected_pages = %#v, want 3", contract["expected_pages"])
	}
	if contract["pptx_count"] != 1 {
		t.Fatalf("pptx_count = %#v, want 1", contract["pptx_count"])
	}
}

func TestBuildPublishedArtifactsContractValidatesManifestAndStorage(t *testing.T) {
	tmp := t.TempDir()
	projectPath := filepath.Join(tmp, "project")
	mustWriteFileNoTest(projectPath, filepath.Join("confirm_ui", "result.json"), `{"page_count":3}`+"\n")
	mustWriteFileNoTest(projectPath, "design_spec.md", "# Design\n")
	mustWritePPTXNoTest(projectPath, filepath.Join("exports", "result.pptx"), 3)
	designSHA, err := sha256File(filepath.Join(projectPath, "design_spec.md"))
	if err != nil {
		t.Fatal(err)
	}
	pptxSHA, err := sha256File(filepath.Join(projectPath, "exports", "result.pptx"))
	if err != nil {
		t.Fatal(err)
	}
	manifest := runtimeArtifactManifest{
		ProjectPath: projectPath,
		Artifacts: []runtimeArtifactManifestItem{
			{Path: "design_spec.md", Size: int64(len("# Design\n")), SHA256: designSHA},
			{Path: "exports/result.pptx", SHA256: pptxSHA},
		},
	}
	rawManifest, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	mustWriteFileNoTest(projectPath, ".slidesmith-artifacts.json", string(rawManifest)+"\n")

	storage := NewLocalStorage(filepath.Join(tmp, "storage"))
	version := "v20260708T120000Z"
	designStored, err := storage.CopyFileToObject(context.Background(), "tasks/task-1/artifacts/"+version+"/design_spec.md", filepath.Join(projectPath, "design_spec.md"))
	if err != nil {
		t.Fatal(err)
	}
	pptxStored, err := storage.CopyFileToObject(context.Background(), "tasks/task-1/artifacts/"+version+"/exports/result.pptx", filepath.Join(projectPath, "exports", "result.pptx"))
	if err != nil {
		t.Fatal(err)
	}
	artifacts := []model.Artifact{
		{TaskID: "task-1", Kind: model.ArtifactKindDesignSpec, Name: designStored.Name, ObjectKey: designStored.ObjectKey, Size: designStored.Size, SHA256: designStored.SHA256, PublishVersion: version},
		{TaskID: "task-1", Kind: model.ArtifactKindPPTX, Name: pptxStored.Name, ObjectKey: pptxStored.ObjectKey, Size: pptxStored.Size, SHA256: pptxStored.SHA256, PublishVersion: version},
	}

	contract, err := buildPublishedArtifactsContract(projectPath, storage, artifacts, version, model.TaskRouteMain)
	if err != nil {
		t.Fatalf("buildPublishedArtifactsContract() error = %v", err)
	}
	if contract["pptx_count"] != 1 {
		t.Fatalf("pptx_count = %#v, want 1", contract["pptx_count"])
	}
	if contract["route"] != model.TaskRouteMain {
		t.Fatalf("route = %#v, want %q", contract["route"], model.TaskRouteMain)
	}
	manifestReport := contract["manifest"].(map[string]any)
	if manifestReport["present"] != true {
		t.Fatalf("manifest present = %#v, want true", manifestReport["present"])
	}
}

func TestBuildPublishedArtifactsContractRejectsMissingStorageObject(t *testing.T) {
	tmp := t.TempDir()
	projectPath := filepath.Join(tmp, "project")
	mustWriteFileNoTest(projectPath, filepath.Join("confirm_ui", "result.json"), `{"page_count":3}`+"\n")
	storage := NewLocalStorage(filepath.Join(tmp, "storage"))
	_, err := buildPublishedArtifactsContract(projectPath, storage, []model.Artifact{{
		TaskID:         "task-1",
		Kind:           model.ArtifactKindPPTX,
		Name:           "missing.pptx",
		ObjectKey:      "tasks/task-1/artifacts/v1/exports/missing.pptx",
		Size:           10,
		PublishVersion: "v1",
	}}, "v1", model.TaskRouteMain)
	if err == nil {
		t.Fatal("buildPublishedArtifactsContract() error = nil, want missing storage object")
	}
	if !strings.Contains(err.Error(), "missing in storage") {
		t.Fatalf("error = %q, want missing storage object", err)
	}
}

func TestBuildPublishedArtifactsContractUsesTemplateFillSlideCountAndRequiredKinds(t *testing.T) {
	tmp := t.TempDir()
	projectPath := filepath.Join(tmp, "project")
	prepareTemplateFillPublishedProjectForTest(t, projectPath, 2)

	storage := NewLocalStorage(filepath.Join(tmp, "storage"))
	version := "v20260712T120000Z"
	artifacts := copyTemplateFillPublishedArtifactsForTest(t, storage, projectPath, version)

	contract, err := buildPublishedArtifactsContract(projectPath, storage, artifacts, version, model.TaskRouteTemplateFill)
	if err != nil {
		t.Fatalf("buildPublishedArtifactsContract() error = %v", err)
	}
	if contract["route"] != model.TaskRouteTemplateFill {
		t.Fatalf("route = %#v, want %q", contract["route"], model.TaskRouteTemplateFill)
	}
	if contract["expected_pages"] != 2 {
		t.Fatalf("expected_pages = %#v, want 2", contract["expected_pages"])
	}
	required, ok := contract["required_template_fill_artifacts"].(map[string]bool)
	if !ok {
		t.Fatalf("required_template_fill_artifacts = %#v, want map[string]bool", contract["required_template_fill_artifacts"])
	}
	for _, kind := range templateFillRequiredPublishedArtifactKindsForTest() {
		if !required[kind] {
			t.Fatalf("required_template_fill_artifacts[%q] = false, want true: %#v", kind, required)
		}
	}
}

func TestBuildPublishedArtifactsContractRejectsEachMissingTemplateFillArtifactKind(t *testing.T) {
	tests := []struct {
		name      string
		omitKind  string
		wantError string
	}{
		{name: "plan", omitKind: model.ArtifactKindTemplateFillPlan, wantError: model.ArtifactKindTemplateFillPlan},
		{name: "check report", omitKind: model.ArtifactKindTemplateFillCheckReport, wantError: model.ArtifactKindTemplateFillCheckReport},
		{name: "validate report", omitKind: model.ArtifactKindTemplateFillValidateReport, wantError: model.ArtifactKindTemplateFillValidateReport},
		{name: "readback", omitKind: model.ArtifactKindTemplateFillReadback, wantError: model.ArtifactKindTemplateFillReadback},
		{name: "pptx", omitKind: model.ArtifactKindPPTX, wantError: "pptx"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tmp := t.TempDir()
			projectPath := filepath.Join(tmp, "project")
			prepareTemplateFillPublishedProjectForTest(t, projectPath, 2)
			storage := NewLocalStorage(filepath.Join(tmp, "storage"))
			version := "v20260712T120000Z"
			all := copyTemplateFillPublishedArtifactsForTest(t, storage, projectPath, version)
			artifacts := make([]model.Artifact, 0, len(all)-1)
			for _, artifact := range all {
				if artifact.Kind != test.omitKind {
					artifacts = append(artifacts, artifact)
				}
			}

			_, err := buildPublishedArtifactsContract(projectPath, storage, artifacts, version, model.TaskRouteTemplateFill)
			if err == nil {
				t.Fatalf("buildPublishedArtifactsContract() error = nil, want missing %s", test.omitKind)
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.wantError)) {
				t.Fatalf("error = %q, want missing kind %q", err, test.wantError)
			}
		})
	}
}

func TestBuildPublishedArtifactsContractMainRouteIgnoresTemplateFillPlan(t *testing.T) {
	tmp := t.TempDir()
	projectPath := filepath.Join(tmp, "project")
	prepareTemplateFillPublishedProjectForTest(t, projectPath, 2)
	mustWriteFileNoTest(projectPath, filepath.Join("confirm_ui", "result.json"), `{"page_count":3}`+"\n")
	mustWritePPTXNoTest(projectPath, filepath.Join("exports", "result.pptx"), 3)

	storage := NewLocalStorage(filepath.Join(tmp, "storage"))
	version := "v20260712T130000Z"
	stored, err := storage.CopyFileToObject(
		context.Background(),
		"tasks/task-1/artifacts/"+version+"/exports/result.pptx",
		filepath.Join(projectPath, "exports", "result.pptx"),
	)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := []model.Artifact{{
		TaskID:         "task-1",
		Kind:           model.ArtifactKindPPTX,
		Name:           stored.Name,
		ObjectKey:      stored.ObjectKey,
		Size:           stored.Size,
		SHA256:         stored.SHA256,
		PublishVersion: version,
	}}

	contract, err := buildPublishedArtifactsContract(projectPath, storage, artifacts, version, model.TaskRouteMain)
	if err != nil {
		t.Fatalf("buildPublishedArtifactsContract() error = %v", err)
	}
	if contract["expected_pages"] != 3 {
		t.Fatalf("expected_pages = %#v, want confirmed main-route count 3", contract["expected_pages"])
	}
	if _, ok := contract["required_template_fill_artifacts"]; ok {
		t.Fatalf("main-route contract unexpectedly requires template fill artifacts: %#v", contract)
	}
}

func TestBuildPublishedArtifactsContractRejectsTemplateFillPublishVersionMismatch(t *testing.T) {
	for _, mismatchKind := range append(templateFillRequiredPublishedArtifactKindsForTest(), model.ArtifactKindPPTX) {
		t.Run(mismatchKind, func(t *testing.T) {
			tmp := t.TempDir()
			projectPath := filepath.Join(tmp, "project")
			prepareTemplateFillPublishedProjectForTest(t, projectPath, 2)
			storage := NewLocalStorage(filepath.Join(tmp, "storage"))
			version := "v20260712T140000Z"
			artifacts := copyTemplateFillPublishedArtifactsForTest(t, storage, projectPath, version)
			for index := range artifacts {
				if artifacts[index].Kind == mismatchKind {
					artifacts[index].PublishVersion = "other-version"
					break
				}
			}

			_, err := buildPublishedArtifactsContract(projectPath, storage, artifacts, version, model.TaskRouteTemplateFill)
			if err == nil || !strings.Contains(err.Error(), "publish_version") {
				t.Fatalf("buildPublishedArtifactsContract() error = %v, want publish version mismatch", err)
			}
		})
	}
}

func TestBuildPublishedArtifactsContractRejectsInvalidTemplateFillPathKindBindings(t *testing.T) {
	type mutation func(*testing.T, *LocalStorage, []model.Artifact, string) []model.Artifact
	relocate := func(relativePath string) mutation {
		return func(t *testing.T, storage *LocalStorage, artifacts []model.Artifact, version string) []model.Artifact {
			t.Helper()
			lowerRelativePath := strings.ToLower(relativePath)
			index := templateFillPublishedArtifactIndexForTest(t, artifacts, model.ArtifactKindTemplateFillPlan)
			if strings.Contains(lowerRelativePath, "check_report") {
				index = templateFillPublishedArtifactIndexForTest(t, artifacts, model.ArtifactKindTemplateFillCheckReport)
			} else if strings.Contains(lowerRelativePath, "validate_report") {
				index = templateFillPublishedArtifactIndexForTest(t, artifacts, model.ArtifactKindTemplateFillValidateReport)
			} else if strings.Contains(lowerRelativePath, "readback") {
				index = templateFillPublishedArtifactIndexForTest(t, artifacts, model.ArtifactKindTemplateFillReadback)
			} else if strings.HasPrefix(lowerRelativePath, "exports/") {
				index = templateFillPublishedArtifactIndexForTest(t, artifacts, model.ArtifactKindPPTX)
			}
			relocatePublishedArtifactForTest(t, storage, &artifacts[index], version, relativePath)
			return artifacts
		}
	}
	tests := []struct {
		name   string
		mutate mutation
	}{
		{
			name: "swapped intermediate kinds",
			mutate: func(t *testing.T, _ *LocalStorage, artifacts []model.Artifact, _ string) []model.Artifact {
				t.Helper()
				plan := templateFillPublishedArtifactIndexForTest(t, artifacts, model.ArtifactKindTemplateFillPlan)
				check := templateFillPublishedArtifactIndexForTest(t, artifacts, model.ArtifactKindTemplateFillCheckReport)
				artifacts[plan].Kind, artifacts[check].Kind = artifacts[check].Kind, artifacts[plan].Kind
				return artifacts
			},
		},
		{
			name: "duplicate canonical row",
			mutate: func(t *testing.T, _ *LocalStorage, artifacts []model.Artifact, _ string) []model.Artifact {
				t.Helper()
				plan := templateFillPublishedArtifactIndexForTest(t, artifacts, model.ArtifactKindTemplateFillPlan)
				return append(artifacts, artifacts[plan])
			},
		},
		{name: "plan case variant", mutate: relocate("analysis/Fill_Plan.json")},
		{name: "plan near match", mutate: relocate("analysis/fill_plan.json.bak")},
		{name: "check report case variant", mutate: relocate("analysis/Check_Report.json")},
		{name: "check report near match", mutate: relocate("analysis/check_report.json.bak")},
		{name: "validate report case variant", mutate: relocate("validation/Validate_Report.json")},
		{name: "validate report near match", mutate: relocate("validation/validate_report.json.bak")},
		{name: "readback case variant", mutate: relocate("validation/Readback.md")},
		{name: "readback near match", mutate: relocate("validation/readback.md.bak")},
		{name: "pptx case variant", mutate: relocate("exports/result.PPTX")},
		{name: "pptx nested near match", mutate: relocate("exports/nested/result.pptx")},
		{
			name: "swapped readback and pptx kinds",
			mutate: func(t *testing.T, _ *LocalStorage, artifacts []model.Artifact, _ string) []model.Artifact {
				t.Helper()
				readback := templateFillPublishedArtifactIndexForTest(t, artifacts, model.ArtifactKindTemplateFillReadback)
				pptx := templateFillPublishedArtifactIndexForTest(t, artifacts, model.ArtifactKindPPTX)
				artifacts[readback].Kind, artifacts[pptx].Kind = artifacts[pptx].Kind, artifacts[readback].Kind
				return artifacts
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tmp := t.TempDir()
			projectPath := filepath.Join(tmp, "project")
			prepareTemplateFillPublishedProjectForTest(t, projectPath, 2)
			storage := NewLocalStorage(filepath.Join(tmp, "storage"))
			version := "v20260712T150000Z"
			artifacts := copyTemplateFillPublishedArtifactsForTest(t, storage, projectPath, version)
			artifacts = test.mutate(t, storage, artifacts, version)

			if _, err := buildPublishedArtifactsContract(projectPath, storage, artifacts, version, model.TaskRouteTemplateFill); err == nil {
				t.Fatal("buildPublishedArtifactsContract() error = nil, want exact path-kind rejection")
			}
		})
	}
}

func prepareTemplateFillPublishedProjectForTest(t *testing.T, projectPath string, slideCount int) {
	t.Helper()
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "brand.pptx"), "pptx\n")
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "content.md"), "# Content\n")
	librarySlides := make([]map[string]any, 0, slideCount)
	for index := 1; index <= slideCount; index++ {
		librarySlides = append(librarySlides, map[string]any{"slide_index": index})
	}
	library, err := json.Marshal(map[string]any{"slides": librarySlides})
	if err != nil {
		t.Fatal(err)
	}
	mustWriteFileNoTest(projectPath, filepath.Join("analysis", "brand.slide_library.json"), string(library)+"\n")
	mustWriteTemplateFillPlan(t, projectPath, "confirmed", slideCount)
	mustWriteFileNoTest(projectPath, filepath.Join("analysis", "check_report.json"), `{"schema":"template_fill_pptx_check.v1","summary":{"ok":1,"warn":0,"error":0},"results":[]}`+"\n")
	mustWriteFileNoTest(projectPath, filepath.Join("validation", "validate_report.json"), `{"schema":"template_fill_pptx_validate.v1","summary":{"ok":1,"warn":0,"error":0},"results":[]}`+"\n")
	mustWriteFileNoTest(projectPath, filepath.Join("validation", "readback.md"), "## Slide 1\n")
	mustWritePPTXNoTest(projectPath, filepath.Join("exports", "result.pptx"), slideCount)
}

func copyTemplateFillPublishedArtifactsForTest(t *testing.T, storage StorageService, projectPath, version string) []model.Artifact {
	return copyTemplateFillPublishedArtifactsForTaskTest(t, storage, projectPath, "task-1", version)
}

func copyTemplateFillPublishedArtifactsForTaskTest(t *testing.T, storage StorageService, projectPath, taskID, version string) []model.Artifact {
	t.Helper()
	items := []struct {
		Rel  string
		Kind string
	}{
		{"analysis/fill_plan.json", model.ArtifactKindTemplateFillPlan},
		{"analysis/check_report.json", model.ArtifactKindTemplateFillCheckReport},
		{"validation/validate_report.json", model.ArtifactKindTemplateFillValidateReport},
		{"validation/readback.md", model.ArtifactKindTemplateFillReadback},
		{"exports/result.pptx", model.ArtifactKindPPTX},
	}
	artifacts := make([]model.Artifact, 0, len(items))
	for _, item := range items {
		objectKey := filepath.ToSlash(filepath.Join("tasks", taskID, "artifacts", version, item.Rel))
		stored, err := storage.CopyFileToObject(context.Background(), objectKey, filepath.Join(projectPath, filepath.FromSlash(item.Rel)))
		if err != nil {
			t.Fatal(err)
		}
		artifacts = append(artifacts, model.Artifact{
			TaskID:         taskID,
			Kind:           item.Kind,
			Name:           stored.Name,
			ObjectKey:      stored.ObjectKey,
			Size:           stored.Size,
			SHA256:         stored.SHA256,
			PublishVersion: version,
		})
	}
	return artifacts
}

func templateFillPublishedArtifactIndexForTest(t *testing.T, artifacts []model.Artifact, kind string) int {
	t.Helper()
	for index := range artifacts {
		if artifacts[index].Kind == kind {
			return index
		}
	}
	t.Fatalf("published artifacts missing kind %q: %#v", kind, artifacts)
	return -1
}

func relocatePublishedArtifactForTest(t *testing.T, storage *LocalStorage, artifact *model.Artifact, version, relativePath string) {
	t.Helper()
	staging, err := storage.CopyFileToObject(
		context.Background(),
		filepath.ToSlash(filepath.Join("test-staging", strings.ReplaceAll(relativePath, "/", "_"))),
		storage.Path(artifact.ObjectKey),
	)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := storage.CopyFileToObject(
		context.Background(),
		filepath.ToSlash(filepath.Join("tasks", artifact.TaskID, "artifacts", version, filepath.FromSlash(relativePath))),
		storage.Path(staging.ObjectKey),
	)
	if err != nil {
		t.Fatal(err)
	}
	artifact.Name = stored.Name
	artifact.ObjectKey = stored.ObjectKey
	artifact.MimeType = stored.MimeType
	artifact.Size = stored.Size
	artifact.SHA256 = stored.SHA256
}

func templateFillRequiredPublishedArtifactKindsForTest() []string {
	return []string{
		model.ArtifactKindTemplateFillPlan,
		model.ArtifactKindTemplateFillCheckReport,
		model.ArtifactKindTemplateFillValidateReport,
		model.ArtifactKindTemplateFillReadback,
	}
}
