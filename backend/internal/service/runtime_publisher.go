package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

type RuntimeWorkspacePublisher struct {
	storage StorageService
}

type runtimeArtifactManifest struct {
	ProjectPath string                        `json:"project_path"`
	Artifacts   []runtimeArtifactManifestItem `json:"artifacts"`
}

type runtimeArtifactManifestItem struct {
	Path     string `json:"path"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
}

type publishedRuntimeArtifact struct {
	SourcePath string
	ObjectRel  string
	Kind       string
}

func NewRuntimeWorkspacePublisher(storage StorageService) *RuntimeWorkspacePublisher {
	return &RuntimeWorkspacePublisher{storage: storage}
}

func (p *RuntimeWorkspacePublisher) Publish(ctx context.Context, taskID, workspacePath, publishVersion string) ([]model.Artifact, error) {
	if workspacePath == "" {
		return nil, fmt.Errorf("workspace path is empty")
	}
	publishVersion, err := cleanPublishVersion(publishVersion)
	if err != nil {
		return nil, err
	}
	manifest, hasManifest, err := readRuntimeArtifactManifest(workspacePath)
	if err != nil {
		return nil, err
	}
	projectPath := ""
	if hasManifest {
		projectPath, err = resolveRuntimeProjectPath(workspacePath, manifest.ProjectPath)
		if err != nil {
			return nil, err
		}
	}
	if projectPath == "" {
		projectPath, err = discoverRuntimeProjectPath(workspacePath)
		if err != nil {
			return nil, err
		}
	}
	if projectPath == "" {
		return nil, fmt.Errorf("runtime project path not found in workspace %s", workspacePath)
	}
	if !hasManifest {
		manifest, hasManifest, err = readProjectRuntimeArtifactManifest(projectPath)
		if err != nil {
			return nil, err
		}
	}

	items, err := collectRuntimeArtifacts(ctx, workspacePath, projectPath, manifest, hasManifest)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("no runtime artifacts found in %s", projectPath)
	}
	if !hasArtifactKind(items, model.ArtifactKindPPTX) {
		return nil, fmt.Errorf("runtime artifacts missing exports/*.pptx in %s", projectPath)
	}

	var artifacts []model.Artifact
	for _, item := range items {
		objectKey := filepath.ToSlash(filepath.Join("tasks", taskID, "artifacts", publishVersion, item.ObjectRel))
		stored, err := p.storage.CopyFileToObject(ctx, objectKey, item.SourcePath)
		if err != nil {
			return nil, err
		}
		artifact := model.Artifact{
			TaskID:         taskID,
			Kind:           item.Kind,
			Name:           stored.Name,
			Storage:        "local",
			ObjectKey:      stored.ObjectKey,
			MimeType:       stored.MimeType,
			Size:           stored.Size,
			SHA256:         stored.SHA256,
			PublishVersion: publishVersion,
		}
		artifacts = append(artifacts, artifact)
	}
	return artifacts, nil
}

func cleanPublishVersion(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("publish version is empty")
	}
	clean, err := cleanObjectKey(value)
	if err != nil {
		return "", fmt.Errorf("invalid publish version %q: %w", value, err)
	}
	if clean == "." || strings.Contains(clean, "/") {
		return "", fmt.Errorf("invalid publish version %q", value)
	}
	return clean, nil
}

func readRuntimeArtifactManifest(workspacePath string) (runtimeArtifactManifest, bool, error) {
	manifestPath := filepath.Join(workspacePath, ".slidesmith", "artifacts.json")
	return readRuntimeArtifactManifestFile(manifestPath)
}

func readProjectRuntimeArtifactManifest(projectPath string) (runtimeArtifactManifest, bool, error) {
	for _, manifestPath := range []string{
		filepath.Join(projectPath, ".slidesmith", "artifacts.json"),
		filepath.Join(projectPath, ".slidesmith-artifacts.json"),
	} {
		manifest, ok, err := readRuntimeArtifactManifestFile(manifestPath)
		if err != nil || ok {
			return manifest, ok, err
		}
	}
	return runtimeArtifactManifest{}, false, nil
}

func readRuntimeArtifactManifestFile(manifestPath string) (runtimeArtifactManifest, bool, error) {
	raw, err := os.ReadFile(manifestPath)
	if os.IsNotExist(err) {
		return runtimeArtifactManifest{}, false, nil
	}
	if err != nil {
		return runtimeArtifactManifest{}, false, fmt.Errorf("read runtime artifact manifest: %w", err)
	}
	var manifest runtimeArtifactManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return runtimeArtifactManifest{}, false, fmt.Errorf("parse runtime artifact manifest: %w", err)
	}
	return manifest, true, nil
}

func resolveRuntimeProjectPath(workspacePath, manifestProjectPath string) (string, error) {
	if manifestProjectPath == "" {
		return "", nil
	}
	if _, err := os.Stat(manifestProjectPath); err == nil {
		return manifestProjectPath, nil
	}
	workspaceCandidate := filepath.Join(workspacePath, filepath.FromSlash(manifestProjectPath))
	if _, err := os.Stat(workspaceCandidate); err == nil {
		return workspaceCandidate, nil
	}
	projectName := filepath.Base(manifestProjectPath)
	if projectName == "." || projectName == string(filepath.Separator) {
		return "", fmt.Errorf("invalid runtime project path %q", manifestProjectPath)
	}
	candidate := filepath.Join(workspacePath, "projects", projectName)
	if _, err := os.Stat(candidate); err == nil {
		return candidate, nil
	}
	return "", fmt.Errorf("runtime project path not found: %s or %s", manifestProjectPath, candidate)
}

func discoverRuntimeProjectPath(workspacePath string) (string, error) {
	projectsDir := filepath.Join(workspacePath, "projects")
	entries, err := os.ReadDir(projectsDir)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var candidates []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		candidates = append(candidates, filepath.Join(projectsDir, entry.Name()))
	}
	return newestPath(candidates), nil
}

func collectRuntimeArtifacts(ctx context.Context, workspacePath, projectPath string, manifest runtimeArtifactManifest, hasManifest bool) ([]publishedRuntimeArtifact, error) {
	byObjectRel := map[string]publishedRuntimeArtifact{}
	addFile := func(sourcePath, objectRel string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		cleanRel, err := cleanArtifactRel(objectRel)
		if err != nil {
			return err
		}
		info, err := os.Stat(sourcePath)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		byObjectRel[cleanRel] = publishedRuntimeArtifact{
			SourcePath: sourcePath,
			ObjectRel:  cleanRel,
			Kind:       artifactKindFromRuntimePath(cleanRel),
		}
		return nil
	}

	if hasManifest {
		for _, item := range manifest.Artifacts {
			if item.Path == "" {
				continue
			}
			cleanRel, err := cleanArtifactRel(item.Path)
			if err != nil {
				return nil, err
			}
			if err := addFile(filepath.Join(projectPath, filepath.FromSlash(cleanRel)), cleanRel); err != nil {
				return nil, err
			}
		}
	}

	contractRoots := []struct {
		ProjectRel string
		ObjectRel  string
	}{
		{"sources", "source"},
		{"analysis", "analysis"},
		{"design_spec.md", "design_spec.md"},
		{"spec_lock.md", "spec_lock.md"},
		{"svg_output", "svg_output"},
		{"svg_final", "svg_final"},
		{"exports", "exports"},
		{"logs", "logs"},
		{filepath.Join(".slidesmith", "contracts"), "contracts"},
		{filepath.Join(".slidesmith", "artifacts.json"), filepath.Join("manifest", "runtime_artifacts.json")},
		{".slidesmith-artifacts.json", filepath.Join("manifest", "runtime_artifacts.json")},
	}
	for _, root := range contractRoots {
		if err := addArtifactRoot(ctx, projectPath, root.ProjectRel, root.ObjectRel, addFile); err != nil {
			return nil, err
		}
	}

	workspaceFiles := []struct {
		SourceRel string
		ObjectRel string
	}{
		{filepath.Join(".slidesmith", "events.ndjson"), filepath.Join("logs", "runtime_events.ndjson")},
		{filepath.Join(".slidesmith", "status.json"), filepath.Join("logs", "runtime_status.json")},
		{filepath.Join(".slidesmith", "artifacts.json"), filepath.Join("manifest", "runtime_artifacts.json")},
	}
	for _, file := range workspaceFiles {
		if err := addFile(filepath.Join(workspacePath, file.SourceRel), file.ObjectRel); err != nil {
			return nil, err
		}
	}

	items := make([]publishedRuntimeArtifact, 0, len(byObjectRel))
	for _, item := range byObjectRel {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].ObjectRel < items[j].ObjectRel
	})
	return items, nil
}

func addArtifactRoot(ctx context.Context, projectPath, projectRel, objectRel string, addFile func(sourcePath, objectRel string) error) error {
	sourceRoot := filepath.Join(projectPath, filepath.FromSlash(projectRel))
	info, err := os.Stat(sourceRoot)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode().IsRegular() {
		return addFile(sourceRoot, objectRel)
	}
	if !info.IsDir() {
		return nil
	}
	return filepath.WalkDir(sourceRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return err
		}
		return addFile(path, filepath.ToSlash(filepath.Join(objectRel, rel)))
	})
}

func artifactKindFromRuntimePath(path string) string {
	lowerPath := strings.ToLower(filepath.ToSlash(path))
	switch {
	case strings.HasPrefix(path, "source/"):
		return model.ArtifactKindSource
	case lowerPath == "analysis/source_profile.json":
		return model.ArtifactKindSourceProfile
	case strings.HasPrefix(lowerPath, "analysis/") && strings.HasSuffix(lowerPath, ".identity.json"):
		return model.ArtifactKindPPTXIdentity
	case strings.HasPrefix(lowerPath, "analysis/") && strings.HasSuffix(lowerPath, ".slide_library.json"):
		return model.ArtifactKindPPTXSlideLibrary
	case path == "design_spec.md":
		return model.ArtifactKindDesignSpec
	case path == "spec_lock.md":
		return model.ArtifactKindSpecLock
	case strings.HasPrefix(path, "svg_output/"):
		return model.ArtifactKindSVGOutput
	case strings.HasPrefix(path, "svg_final/"):
		return model.ArtifactKindSVGFinal
	case strings.HasPrefix(path, "exports/") && strings.HasSuffix(strings.ToLower(path), ".pptx"):
		return model.ArtifactKindPPTX
	case strings.HasPrefix(path, "logs/"):
		return model.ArtifactKindLog
	case strings.HasPrefix(path, "contracts/"):
		return model.ArtifactKindManifest
	case strings.HasPrefix(path, "manifest/"):
		return model.ArtifactKindManifest
	default:
		return model.ArtifactKindOther
	}
}

func cleanArtifactRel(path string) (string, error) {
	rel, err := cleanObjectKey(path)
	if err != nil {
		return "", fmt.Errorf("invalid artifact path %q: %w", path, err)
	}
	return rel, nil
}

func hasArtifactKind(items []publishedRuntimeArtifact, kind string) bool {
	for _, item := range items {
		if item.Kind == kind {
			return true
		}
	}
	return false
}
