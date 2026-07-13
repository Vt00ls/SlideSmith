package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/config"
	"github.com/slidesmith/slidesmith/backend/internal/model"
)

const (
	runtimeManifestVersion = 1
	skillLockVersion       = 1
)

type TaskWorkspace struct {
	HostDir        string
	ComposeFile    string
	CLIComposeFile string
	ProjectPath    string
	InputPath      string
	SkillDir       string
	SourceCount    int
}

type RuntimeWorkspaceBuilder struct {
	cfg     config.AgentComposeConfig
	storage StorageService
}

type runtimeManifest struct {
	SchemaVersion    int      `json:"schema_version"`
	TaskID           string   `json:"task_id"`
	ProjectPath      string   `json:"project_path"`
	CoreSkill        string   `json:"core_skill"`
	CoreScripts      string   `json:"core_scripts"`
	PublisherScript  string   `json:"publisher_script"`
	TemplateRoots    []string `json:"template_roots"`
	SelectedTemplate string   `json:"selected_template_id,omitempty"`
	TemplateLock     string   `json:"template_lock,omitempty"`
	AssetRoots       []string `json:"asset_roots"`
	ExtensionSkills  []string `json:"extension_skills"`
	CreatedAt        string   `json:"created_at"`
}

type sourceInputsManifest struct {
	Schema    string                     `json:"schema"`
	TaskID    string                     `json:"task_id"`
	CreatedAt string                     `json:"created_at"`
	Files     []sourceInputsManifestFile `json:"files"`
}

type sourceInputsManifestFile struct {
	Name       string `json:"name"`
	UploadPath string `json:"upload_path"`
	ObjectKey  string `json:"object_key"`
	MimeType   string `json:"mime_type"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
	SourceKind string `json:"source_kind"`
	Extension  string `json:"extension"`
}

type skillLock struct {
	SchemaVersion int              `json:"schema_version"`
	Core          skillLockEntry   `json:"core"`
	Extensions    []skillLockEntry `json:"extensions"`
	CreatedAt     string           `json:"created_at"`
}

type skillLockEntry struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Source   string `json:"source"`
	Path     string `json:"path"`
	Checksum string `json:"checksum"`
}

func NewRuntimeWorkspaceBuilder(cfg config.AgentComposeConfig, storage StorageService) *RuntimeWorkspaceBuilder {
	return &RuntimeWorkspaceBuilder{cfg: cfg, storage: storage}
}

func (b *RuntimeWorkspaceBuilder) Build(ctx context.Context, task *model.Task, sources []model.Artifact) (*TaskWorkspace, error) {
	if task == nil {
		return nil, fmt.Errorf("task is nil")
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("task has no source artifact")
	}
	workspace := b.Resolve(task)
	if workspace.HostDir == "" {
		return nil, fmt.Errorf("task workspace root is empty")
	}
	if err := os.RemoveAll(workspace.HostDir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(workspace.HostDir, 0o755); err != nil {
		return nil, err
	}

	if err := b.copySeedDir(ctx, workspace.HostDir, "scripts"); err != nil {
		return nil, err
	}
	if err := b.copySeedDir(ctx, workspace.HostDir, "workflows"); err != nil {
		return nil, err
	}

	skillSource, err := b.resolvePPTMasterSkillDir()
	if err != nil {
		return nil, err
	}
	skillTarget := filepath.Join(workspace.HostDir, "skills", "ppt-master")
	if err := copyDir(ctx, skillSource, skillTarget); err != nil {
		return nil, fmt.Errorf("copy ppt-master skill: %w", err)
	}
	workspace.SkillDir = skillTarget
	if err := b.applyTemplateLock(ctx, workspace, task); err != nil {
		return nil, err
	}

	inputPath, sourceFiles, err := b.copySources(ctx, workspace.HostDir, task.ID, sources)
	if err != nil {
		return nil, err
	}
	workspace.InputPath = inputPath
	workspace.SourceCount = len(sourceFiles)
	if err := writeSourceInputsManifest(workspace, task, sourceFiles); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(filepath.Join(workspace.HostDir, "projects"), 0o755); err != nil {
		return nil, err
	}
	if err := b.writeComposeFile(workspace.ComposeFile); err != nil {
		return nil, err
	}
	if err := b.WriteRuntimeManifest(workspace, task, ""); err != nil {
		return nil, err
	}
	if err := b.writeSkillLock(workspace, skillSource); err != nil {
		return nil, err
	}
	return workspace, nil
}

func (b *RuntimeWorkspaceBuilder) Resolve(task *model.Task) *TaskWorkspace {
	runtimeProject := strings.TrimSpace(task.RuntimeProject)
	if runtimeProject == "" {
		runtimeProject = runtimeProjectName(task.ID)
	}
	hostDir := filepath.Join(b.workspaceRoot(), sanitizePathSegment(runtimeProject))
	composeFile := filepath.Join(hostDir, "agent-compose.yml")
	return &TaskWorkspace{
		HostDir:        hostDir,
		ComposeFile:    composeFile,
		CLIComposeFile: b.cliVisiblePath(composeFile),
		ProjectPath:    filepath.Join(hostDir, "projects", runtimeProject),
		SkillDir:       filepath.Join(hostDir, "skills", "ppt-master"),
	}
}

func (b *RuntimeWorkspaceBuilder) workspaceRoot() string {
	if strings.TrimSpace(b.cfg.WorkspaceRoot) != "" {
		return b.cfg.WorkspaceRoot
	}
	if strings.TrimSpace(b.cfg.WorkDir) != "" {
		return filepath.Join(b.cfg.WorkDir, "task-workspaces")
	}
	return filepath.Join("runtime", "task-workspaces")
}

func (b *RuntimeWorkspaceBuilder) copySeedDir(ctx context.Context, workspaceDir, name string) error {
	source := filepath.Join(b.cfg.WorkDir, name)
	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("seed %s not found at %s: %w", name, source, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("seed %s is not a directory: %s", name, source)
	}
	return copyDir(ctx, source, filepath.Join(workspaceDir, name))
}

func (b *RuntimeWorkspaceBuilder) copySources(ctx context.Context, workspaceDir, taskID string, sources []model.Artifact) (string, []sourceInputsManifestFile, error) {
	var firstRel string
	files := make([]sourceInputsManifestFile, 0, len(sources))
	uploadDir := filepath.Join(workspaceDir, "uploads", taskID)
	if err := os.MkdirAll(uploadDir, 0o755); err != nil {
		return "", nil, err
	}
	for _, artifact := range sources {
		if artifact.Kind != model.ArtifactKindSource {
			continue
		}
		sourcePath := b.storage.Path(artifact.ObjectKey)
		name := sanitizeFilename(artifact.Name)
		targetPath := filepath.Join(uploadDir, name)
		if err := copyFile(sourcePath, targetPath, 0o644); err != nil {
			return "", nil, err
		}
		if err := ctx.Err(); err != nil {
			return "", nil, err
		}
		rel, err := filepath.Rel(workspaceDir, targetPath)
		if err != nil {
			return "", nil, err
		}
		rel = filepath.ToSlash(rel)
		if firstRel == "" {
			firstRel = rel
		}
		sourceInfo := DetectSourceKind(artifact.Name)
		files = append(files, sourceInputsManifestFile{
			Name:       artifact.Name,
			UploadPath: rel,
			ObjectKey:  artifact.ObjectKey,
			MimeType:   artifact.MimeType,
			Size:       artifact.Size,
			SHA256:     artifact.SHA256,
			SourceKind: string(sourceInfo.Kind),
			Extension:  sourceInfo.Extension,
		})
	}
	if firstRel == "" {
		return "", nil, fmt.Errorf("task has no source artifact")
	}
	return firstRel, files, nil
}

func writeSourceInputsManifest(workspace *TaskWorkspace, task *model.Task, files []sourceInputsManifestFile) error {
	manifest := sourceInputsManifest{
		Schema:    "slidesmith.source_inputs.v1",
		TaskID:    task.ID,
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Files:     files,
	}
	return writeJSONPretty(filepath.Join(workspace.HostDir, ".slidesmith", "source_inputs.json"), manifest)
}

func (b *RuntimeWorkspaceBuilder) writeComposeFile(path string) error {
	agent := strings.TrimSpace(b.cfg.Agent)
	if agent == "" {
		agent = "ppt_master"
	}
	image := strings.TrimSpace(b.cfg.RuntimeImage)
	if image == "" {
		image = "slidesmith/ppt-master-runtime:dev"
	}
	projectName := "slidesmith-" + sanitizePathSegment(filepath.Base(filepath.Dir(path)))
	content := fmt.Sprintf(`name: %s

workspace:
  provider: local
  path: .

agents:
  %s:
    provider: codex
    image: %s
    driver:
      docker: {}
    scheduler:
      enabled: false
`, projectName, agent, image)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func (b *RuntimeWorkspaceBuilder) WriteRuntimeManifest(workspace *TaskWorkspace, task *model.Task, projectPath string) error {
	runtimeProject := strings.TrimSpace(task.RuntimeProject)
	if runtimeProject == "" {
		runtimeProject = runtimeProjectName(task.ID)
	}
	projectRel := filepath.ToSlash(filepath.Join("projects", runtimeProject))
	if strings.TrimSpace(projectPath) != "" {
		if rel, err := filepath.Rel(workspace.HostDir, projectPath); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
			projectRel = filepath.ToSlash(rel)
		}
	}
	templateRoots := []string{"skills/ppt-master/templates"}
	selectedTemplate := ""
	templateLockPath := ""
	if lock, ok, _ := decodeTemplateLock(task.TemplateLockJSON); ok {
		if rel := templateLockTemplateRoot(lock); rel != "" {
			templateRoots = []string{rel}
		}
		selectedTemplate = lock.TemplateID
		templateLockPath = ".slidesmith/template_lock.json"
	}
	manifest := runtimeManifest{
		SchemaVersion:    runtimeManifestVersion,
		TaskID:           task.ID,
		ProjectPath:      projectRel,
		CoreSkill:        "skills/ppt-master/SKILL.md",
		CoreScripts:      "skills/ppt-master/scripts",
		PublisherScript:  "scripts/ppt_runner.py",
		TemplateRoots:    templateRoots,
		SelectedTemplate: selectedTemplate,
		TemplateLock:     templateLockPath,
		AssetRoots:       []string{"skills/ppt-master/assets"},
		ExtensionSkills:  []string{},
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
	}
	return writeJSONPretty(filepath.Join(workspace.HostDir, ".slidesmith", "runtime_manifest.json"), manifest)
}

func (b *RuntimeWorkspaceBuilder) applyTemplateLock(ctx context.Context, workspace *TaskWorkspace, task *model.Task) error {
	lock, ok, err := decodeTemplateLock(task.TemplateLockJSON)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	templatesDir := filepath.Join(workspace.SkillDir, "templates")
	selectedGroup, err := templateGroupForKind(lock.Kind)
	if err != nil {
		return err
	}
	selectedDir := filepath.Join(templatesDir, selectedGroup, lock.Name)
	if info, err := os.Stat(selectedDir); err != nil || !info.IsDir() {
		if err == nil {
			err = fmt.Errorf("not a directory")
		}
		return fmt.Errorf("locked template package missing %s: %w", selectedDir, err)
	}
	for _, group := range []string{"layouts", "decks", "brands"} {
		keepName := ""
		if group == selectedGroup {
			keepName = lock.Name
		}
		if err := trimTemplatePackageGroup(ctx, filepath.Join(templatesDir, group), keepName); err != nil {
			return err
		}
	}
	if err := writeJSONPretty(filepath.Join(workspace.HostDir, ".slidesmith", "template_lock.json"), lock); err != nil {
		return err
	}
	return nil
}

func decodeTemplateLock(value string) (TemplateLock, bool, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "{}" || value == "null" {
		return TemplateLock{}, false, nil
	}
	var lock TemplateLock
	if err := json.Unmarshal([]byte(value), &lock); err != nil {
		return TemplateLock{}, false, fmt.Errorf("parse template lock: %w", err)
	}
	if strings.TrimSpace(lock.TemplateID) == "" || strings.TrimSpace(lock.Kind) == "" || strings.TrimSpace(lock.Name) == "" {
		return TemplateLock{}, false, fmt.Errorf("template lock is incomplete")
	}
	return lock, true, nil
}

func trimTemplatePackageGroup(ctx context.Context, groupDir, keepName string) error {
	entries, err := os.ReadDir(groupDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.IsDir() {
			continue
		}
		if keepName != "" && entry.Name() == keepName {
			continue
		}
		if err := os.RemoveAll(filepath.Join(groupDir, entry.Name())); err != nil {
			return err
		}
	}
	return filterTemplateIndex(groupDir, keepName)
}

func filterTemplateIndex(groupDir, keepName string) error {
	groupName := filepath.Base(groupDir)
	indexName := strings.TrimSuffix(groupName, "s") + "s_index.json"
	switch groupName {
	case "layouts":
		indexName = "layouts_index.json"
	case "decks":
		indexName = "decks_index.json"
	case "brands":
		indexName = "brands_index.json"
	}
	indexPath := filepath.Join(groupDir, indexName)
	raw, err := os.ReadFile(indexPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return err
	}
	filtered := map[string]json.RawMessage{}
	if keepName != "" {
		if value, ok := entries[keepName]; ok {
			filtered[keepName] = value
		}
	}
	return writeJSONPretty(indexPath, filtered)
}

func templateLockTemplateRoot(lock TemplateLock) string {
	group, err := templateGroupForKind(lock.Kind)
	if err != nil {
		return ""
	}
	return filepath.ToSlash(filepath.Join("skills", "ppt-master", "templates", group, lock.Name))
}

func templateGroupForKind(kind string) (string, error) {
	switch kind {
	case "layout":
		return "layouts", nil
	case "deck":
		return "decks", nil
	case "brand":
		return "brands", nil
	default:
		return "", fmt.Errorf("unsupported template kind %q", kind)
	}
}

func (b *RuntimeWorkspaceBuilder) writeSkillLock(workspace *TaskWorkspace, source string) error {
	checksum := ""
	if value, err := sha256Path(filepath.Join(workspace.SkillDir, "SKILL.md")); err == nil {
		checksum = "sha256:" + value
	}
	lock := skillLock{
		SchemaVersion: skillLockVersion,
		Core: skillLockEntry{
			Name:     "ppt-master",
			Version:  "workspace",
			Source:   source,
			Path:     "skills/ppt-master",
			Checksum: checksum,
		},
		Extensions: []skillLockEntry{},
		CreatedAt:  time.Now().UTC().Format(time.RFC3339Nano),
	}
	return writeJSONPretty(filepath.Join(workspace.HostDir, ".slidesmith", "skill_lock.json"), lock)
}

func (b *RuntimeWorkspaceBuilder) resolvePPTMasterSkillDir() (string, error) {
	candidates := []string{
		b.cfg.PPTMasterSkillDir,
		filepath.Join(b.cfg.WorkDir, "skills", "ppt-master"),
		"/opt/ppt-master/skills/ppt-master",
		"/root/ppt-master/skills/ppt-master",
		"/root/slidesmith/runtime/ppt-master-agent/skills/ppt-master",
		"/Users/vt/Dev_space/ppt-master/skills/ppt-master",
	}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		info, err := os.Stat(candidate)
		if err == nil && info.IsDir() {
			if _, err := os.Stat(filepath.Join(candidate, "SKILL.md")); err == nil {
				return candidate, nil
			}
		}
	}
	return "", fmt.Errorf("ppt-master skill source not found; set SLIDESMITH_PPT_MASTER_SKILL_DIR")
}

func (b *RuntimeWorkspaceBuilder) cliVisiblePath(hostPath string) string {
	composeFile := strings.TrimSpace(b.cfg.ComposeFile)
	workDir := strings.TrimSpace(b.cfg.WorkDir)
	if composeFile == "" || workDir == "" || !filepath.IsAbs(composeFile) || !filepath.IsAbs(workDir) {
		return hostPath
	}
	rel, err := filepath.Rel(workDir, hostPath)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return hostPath
	}
	return filepath.ToSlash(filepath.Join(filepath.Dir(composeFile), rel))
}

func writeJSONPretty(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

func sha256Path(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func sanitizePathSegment(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return "workspace"
	}
	var builder strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '-' || r == '_':
			builder.WriteRune(r)
		default:
			builder.WriteRune('_')
		}
	}
	cleaned := strings.Trim(builder.String(), "._-")
	if cleaned == "" {
		return "workspace"
	}
	return cleaned
}
