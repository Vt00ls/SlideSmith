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

	contract, err := buildPublishedArtifactsContract(projectPath, storage, artifacts, version)
	if err != nil {
		t.Fatalf("buildPublishedArtifactsContract() error = %v", err)
	}
	if contract["pptx_count"] != 1 {
		t.Fatalf("pptx_count = %#v, want 1", contract["pptx_count"])
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
	}}, "v1")
	if err == nil {
		t.Fatal("buildPublishedArtifactsContract() error = nil, want missing storage object")
	}
	if !strings.Contains(err.Error(), "missing in storage") {
		t.Fatalf("error = %q, want missing storage object", err)
	}
}
