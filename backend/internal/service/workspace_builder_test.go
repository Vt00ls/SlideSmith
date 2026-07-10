package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/config"
	"github.com/slidesmith/slidesmith/backend/internal/model"
)

func TestRuntimeWorkspaceBuilderBuildsTaskWorkspace(t *testing.T) {
	tmp := t.TempDir()
	seed := filepath.Join(tmp, "seed")
	mustWriteFile(t, filepath.Join(seed, "scripts", "ppt_runner.py"), "print('runner')\n")
	mustWriteFile(t, filepath.Join(seed, "workflows", "ppt_workflow.js"), "console.log('workflow')\n")

	skillSource := filepath.Join(tmp, "ppt-master", "skills", "ppt-master")
	mustWriteFile(t, filepath.Join(skillSource, "SKILL.md"), "# PPT Master\n")
	mustWriteFile(t, filepath.Join(skillSource, "scripts", "svg_to_pptx.py"), "print('export')\n")
	mustWriteFile(t, filepath.Join(skillSource, "templates", "README.md"), "templates\n")

	storage := NewLocalStorage(filepath.Join(tmp, "storage"))
	sourceKey := filepath.ToSlash(filepath.Join("tasks", "task-1", "source", "input.md"))
	mustWriteFile(t, storage.Path(sourceKey), "# Source\n")

	cfg := config.AgentComposeConfig{
		WorkDir:           seed,
		ComposeFile:       "/data/work/agent-compose.yml",
		WorkspaceRoot:     filepath.Join(seed, "task-workspaces"),
		PPTMasterSkillDir: skillSource,
		Agent:             "ppt_master",
		RuntimeImage:      "slidesmith/ppt-master-runtime:dev",
	}
	builder := NewRuntimeWorkspaceBuilder(cfg, storage)
	task := &model.Task{
		ID:             "task-1",
		RuntimeProject: "task_1",
	}
	workspace, err := builder.Build(context.Background(), task, []model.Artifact{{
		TaskID:    task.ID,
		Kind:      model.ArtifactKindSource,
		Name:      "input.md",
		ObjectKey: sourceKey,
	}})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	requiredPaths := []string{
		filepath.Join(workspace.HostDir, "skills", "ppt-master", "SKILL.md"),
		filepath.Join(workspace.HostDir, "skills", "ppt-master", "scripts", "svg_to_pptx.py"),
		filepath.Join(workspace.HostDir, "scripts", "ppt_runner.py"),
		filepath.Join(workspace.HostDir, "workflows", "ppt_workflow.js"),
		filepath.Join(workspace.HostDir, "uploads", task.ID, "input.md"),
		filepath.Join(workspace.HostDir, ".slidesmith", "runtime_manifest.json"),
		filepath.Join(workspace.HostDir, ".slidesmith", "skill_lock.json"),
		workspace.ComposeFile,
	}
	for _, path := range requiredPaths {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s to exist: %v", path, err)
		}
	}
	if workspace.InputPath != filepath.ToSlash(filepath.Join("uploads", task.ID, "input.md")) {
		t.Fatalf("unexpected input path %q", workspace.InputPath)
	}
	if !strings.HasPrefix(workspace.CLIComposeFile, "/data/work/task-workspaces/task_1/") {
		t.Fatalf("unexpected cli compose path %q", workspace.CLIComposeFile)
	}
	rawCompose, err := os.ReadFile(workspace.ComposeFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rawCompose), "workspace:\n  provider: local\n  path: .") {
		t.Fatalf("compose file does not point to local workspace:\n%s", rawCompose)
	}
	actualProjectPath := filepath.Join(workspace.HostDir, "projects", "task_1_ppt169_20260707")
	if err := builder.WriteRuntimeManifest(workspace, task, actualProjectPath); err != nil {
		t.Fatalf("WriteRuntimeManifest() error = %v", err)
	}
	rawManifest, err := os.ReadFile(filepath.Join(workspace.HostDir, ".slidesmith", "runtime_manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rawManifest), `"project_path": "projects/task_1_ppt169_20260707"`) {
		t.Fatalf("manifest did not record actual project path:\n%s", rawManifest)
	}
}

func TestRuntimeWorkspaceBuilderWritesSourceInputsManifest(t *testing.T) {
	tmp := t.TempDir()
	seed := filepath.Join(tmp, "seed")
	mustWriteFile(t, filepath.Join(seed, "scripts", "ppt_runner.py"), "print('runner')\n")
	mustWriteFile(t, filepath.Join(seed, "workflows", "ppt_workflow.js"), "console.log('workflow')\n")

	skillSource := filepath.Join(tmp, "ppt-master", "skills", "ppt-master")
	mustWriteFile(t, filepath.Join(skillSource, "SKILL.md"), "# PPT Master\n")

	storage := NewLocalStorage(filepath.Join(tmp, "storage"))
	inputKey := filepath.ToSlash(filepath.Join("tasks", "task-1", "source", "input.md"))
	deckKey := filepath.ToSlash(filepath.Join("tasks", "task-1", "source", "deck.pptx"))
	mustWriteFile(t, storage.Path(inputKey), "# Source\n")
	mustWriteFile(t, storage.Path(deckKey), "presentation bytes\n")

	cfg := config.AgentComposeConfig{
		WorkDir:           seed,
		ComposeFile:       "/data/work/agent-compose.yml",
		WorkspaceRoot:     filepath.Join(seed, "task-workspaces"),
		PPTMasterSkillDir: skillSource,
		Agent:             "ppt_master",
		RuntimeImage:      "slidesmith/ppt-master-runtime:dev",
	}
	builder := NewRuntimeWorkspaceBuilder(cfg, storage)
	task := &model.Task{ID: "task-1", RuntimeProject: "task_1"}
	inputSHA := strings.Repeat("a", 64)
	deckSHA := strings.Repeat("b", 64)
	workspace, err := builder.Build(context.Background(), task, []model.Artifact{
		{
			TaskID:    task.ID,
			Kind:      model.ArtifactKindSource,
			Name:      "input.md",
			ObjectKey: inputKey,
			MimeType:  "text/markdown",
			Size:      9,
			SHA256:    inputSHA,
		},
		{
			TaskID:    task.ID,
			Kind:      model.ArtifactKindSource,
			Name:      "deck.pptx",
			ObjectKey: deckKey,
			MimeType:  "application/vnd.openxmlformats-officedocument.presentationml.presentation",
			Size:      19,
			SHA256:    deckSHA,
		},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if workspace.InputPath != "uploads/task-1/input.md" {
		t.Fatalf("InputPath = %q, want first source path", workspace.InputPath)
	}

	raw, err := os.ReadFile(filepath.Join(workspace.HostDir, ".slidesmith", "source_inputs.json"))
	if err != nil {
		t.Fatalf("read source inputs manifest: %v", err)
	}
	var manifest struct {
		Schema    string `json:"schema"`
		TaskID    string `json:"task_id"`
		CreatedAt string `json:"created_at"`
		Files     []struct {
			Name       string `json:"name"`
			UploadPath string `json:"upload_path"`
			ObjectKey  string `json:"object_key"`
			MimeType   string `json:"mime_type"`
			Size       int64  `json:"size"`
			SHA256     string `json:"sha256"`
			SourceKind string `json:"source_kind"`
			Extension  string `json:"extension"`
		} `json:"files"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parse source inputs manifest: %v", err)
	}
	if manifest.Schema != "slidesmith.source_inputs.v1" {
		t.Fatalf("schema = %q", manifest.Schema)
	}
	if manifest.TaskID != task.ID {
		t.Fatalf("task_id = %q, want %q", manifest.TaskID, task.ID)
	}
	if _, err := time.Parse(time.RFC3339Nano, manifest.CreatedAt); err != nil {
		t.Fatalf("created_at = %q, want RFC3339 timestamp: %v", manifest.CreatedAt, err)
	}
	if len(manifest.Files) != 2 {
		t.Fatalf("files length = %d, want 2", len(manifest.Files))
	}

	input := manifest.Files[0]
	if input.Name != "input.md" || input.UploadPath != "uploads/task-1/input.md" || input.ObjectKey != inputKey {
		t.Fatalf("first file identity = %#v", input)
	}
	if input.MimeType != "text/markdown" || input.Size != 9 || input.SHA256 != inputSHA {
		t.Fatalf("first file metadata = %#v", input)
	}
	if input.SourceKind != "markdown" || input.Extension != "md" {
		t.Fatalf("first file detection = %#v", input)
	}

	deck := manifest.Files[1]
	if deck.Name != "deck.pptx" || deck.UploadPath != "uploads/task-1/deck.pptx" || deck.ObjectKey != deckKey {
		t.Fatalf("second file identity = %#v", deck)
	}
	if deck.MimeType != "application/vnd.openxmlformats-officedocument.presentationml.presentation" || deck.Size != 19 || deck.SHA256 != deckSHA {
		t.Fatalf("second file metadata = %#v", deck)
	}
	if deck.SourceKind != "presentation" || deck.Extension != "pptx" {
		t.Fatalf("second file detection = %#v", deck)
	}
}

func TestRuntimeWorkspaceBuilderAppliesTemplateLock(t *testing.T) {
	tmp := t.TempDir()
	seed := filepath.Join(tmp, "seed")
	mustWriteFile(t, filepath.Join(seed, "scripts", "ppt_runner.py"), "print('runner')\n")
	mustWriteFile(t, filepath.Join(seed, "workflows", "ppt_workflow.js"), "console.log('workflow')\n")

	skillSource := filepath.Join(tmp, "ppt-master", "skills", "ppt-master")
	mustWriteFile(t, filepath.Join(skillSource, "SKILL.md"), "# PPT Master\n")
	mustWriteFile(t, filepath.Join(skillSource, "scripts", "svg_to_pptx.py"), "print('export')\n")
	mustWriteFile(t, filepath.Join(skillSource, "templates", "README.md"), "templates\n")
	mustWriteFile(t, filepath.Join(skillSource, "templates", "design_spec_reference.md"), "reference\n")
	mustWriteFile(t, filepath.Join(skillSource, "templates", "layouts", "layouts_index.json"), `{
  "government_blue": {"summary": "blue", "canvas_format": "ppt169", "page_count": 5},
  "pixel_retro": {"summary": "retro", "canvas_format": "ppt169", "page_count": 5}
}`)
	mustWriteFile(t, filepath.Join(skillSource, "templates", "layouts", "government_blue", "01_cover.svg"), "<svg></svg>\n")
	mustWriteFile(t, filepath.Join(skillSource, "templates", "layouts", "pixel_retro", "01_cover.svg"), "<svg></svg>\n")
	mustWriteFile(t, filepath.Join(skillSource, "templates", "decks", "decks_index.json"), `{"中国电信": {"summary": "telecom"}}`)
	mustWriteFile(t, filepath.Join(skillSource, "templates", "decks", "中国电信", "01_cover.svg"), "<svg></svg>\n")
	mustWriteFile(t, filepath.Join(skillSource, "templates", "brands", "brands_index.json"), `{"google": {"summary": "google"}}`)
	mustWriteFile(t, filepath.Join(skillSource, "templates", "brands", "google", "google_wordmark.svg"), "<svg></svg>\n")
	mustWriteFile(t, filepath.Join(skillSource, "templates", "charts", "bar_chart.svg"), "<svg></svg>\n")

	catalog := NewTemplateCatalogService(skillSource)
	lock, err := catalog.BuildTemplateLock(context.Background(), "layout:government_blue")
	if err != nil {
		t.Fatal(err)
	}
	rawLock, err := json.Marshal(lock)
	if err != nil {
		t.Fatal(err)
	}

	storage := NewLocalStorage(filepath.Join(tmp, "storage"))
	sourceKey := filepath.ToSlash(filepath.Join("tasks", "task-1", "source", "input.md"))
	mustWriteFile(t, storage.Path(sourceKey), "# Source\n")
	cfg := config.AgentComposeConfig{
		WorkDir:           seed,
		ComposeFile:       "/data/work/agent-compose.yml",
		WorkspaceRoot:     filepath.Join(seed, "task-workspaces"),
		PPTMasterSkillDir: skillSource,
		Agent:             "ppt_master",
		RuntimeImage:      "slidesmith/ppt-master-runtime:dev",
	}
	builder := NewRuntimeWorkspaceBuilder(cfg, storage)
	task := &model.Task{
		ID:                 "task-1",
		RuntimeProject:     "task_1",
		SelectedTemplateID: lock.TemplateID,
		TemplateLockJSON:   string(rawLock),
	}
	workspace, err := builder.Build(context.Background(), task, []model.Artifact{{
		TaskID:    task.ID,
		Kind:      model.ArtifactKindSource,
		Name:      "input.md",
		ObjectKey: sourceKey,
	}})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	assertExists(t, filepath.Join(workspace.SkillDir, "templates", "layouts", "government_blue", "01_cover.svg"))
	assertMissing(t, filepath.Join(workspace.SkillDir, "templates", "layouts", "pixel_retro"))
	assertMissing(t, filepath.Join(workspace.SkillDir, "templates", "decks", "中国电信"))
	assertMissing(t, filepath.Join(workspace.SkillDir, "templates", "brands", "google"))
	assertExists(t, filepath.Join(workspace.SkillDir, "templates", "charts", "bar_chart.svg"))
	assertExists(t, filepath.Join(workspace.HostDir, ".slidesmith", "template_lock.json"))

	rawIndex, err := os.ReadFile(filepath.Join(workspace.SkillDir, "templates", "layouts", "layouts_index.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rawIndex), "government_blue") || strings.Contains(string(rawIndex), "pixel_retro") {
		t.Fatalf("layout index was not filtered:\n%s", rawIndex)
	}
	rawManifest, err := os.ReadFile(filepath.Join(workspace.HostDir, ".slidesmith", "runtime_manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rawManifest), `"selected_template_id": "layout:government_blue"`) {
		t.Fatalf("manifest missing selected template:\n%s", rawManifest)
	}
	if !strings.Contains(string(rawManifest), `"skills/ppt-master/templates/layouts/government_blue"`) {
		t.Fatalf("manifest missing selected template root:\n%s", rawManifest)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %s to exist: %v", path, err)
	}
}

func assertMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be missing, err=%v", path, err)
	}
}
