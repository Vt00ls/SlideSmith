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
	workspacePath, err := resolveRuntimeWorkspacePath(workspacePath)
	if err != nil {
		return nil, err
	}
	publishVersion, err = cleanPublishVersion(publishVersion)
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
	return readRuntimeArtifactManifestFile(workspacePath, manifestPath)
}

func readProjectRuntimeArtifactManifest(projectPath string) (runtimeArtifactManifest, bool, error) {
	for _, manifestPath := range []string{
		filepath.Join(projectPath, ".slidesmith", "artifacts.json"),
		filepath.Join(projectPath, ".slidesmith-artifacts.json"),
	} {
		manifest, ok, err := readRuntimeArtifactManifestFile(projectPath, manifestPath)
		if err != nil || ok {
			return manifest, ok, err
		}
	}
	return runtimeArtifactManifest{}, false, nil
}

func readRuntimeArtifactManifestFile(permittedRoot, manifestPath string) (runtimeArtifactManifest, bool, error) {
	info, resolvedManifestPath, err := inspectContainedPath(permittedRoot, manifestPath)
	if os.IsNotExist(err) {
		return runtimeArtifactManifest{}, false, nil
	}
	if err != nil {
		return runtimeArtifactManifest{}, false, fmt.Errorf("inspect runtime artifact manifest: %w", err)
	}
	if !info.Mode().IsRegular() {
		return runtimeArtifactManifest{}, false, fmt.Errorf("runtime artifact manifest is not a regular file: %s", manifestPath)
	}
	raw, err := os.ReadFile(resolvedManifestPath)
	if err != nil {
		return runtimeArtifactManifest{}, false, fmt.Errorf("read runtime artifact manifest: %w", err)
	}
	var manifest runtimeArtifactManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return runtimeArtifactManifest{}, false, fmt.Errorf("parse runtime artifact manifest: %w", err)
	}
	return manifest, true, nil
}

func resolveRuntimeWorkspacePath(workspacePath string) (string, error) {
	absPath, err := filepath.Abs(workspacePath)
	if err != nil {
		return "", fmt.Errorf("resolve runtime workspace path: %w", err)
	}
	resolvedPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return "", fmt.Errorf("resolve runtime workspace path: %w", err)
	}
	info, err := os.Stat(resolvedPath)
	if err != nil {
		return "", fmt.Errorf("stat runtime workspace path: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("runtime workspace path is not a directory: %s", resolvedPath)
	}
	return resolvedPath, nil
}

func resolveRuntimeProjectPath(workspacePath, manifestProjectPath string) (string, error) {
	if manifestProjectPath == "" {
		return "", nil
	}
	workspacePath, err := resolveRuntimeWorkspacePath(workspacePath)
	if err != nil {
		return "", err
	}
	manifestCandidate := filepath.FromSlash(manifestProjectPath)
	if !filepath.IsAbs(manifestCandidate) {
		manifestCandidate = filepath.Join(workspacePath, manifestCandidate)
	}
	resolvedCandidate, found, err := resolveRuntimeProjectCandidate(workspacePath, manifestCandidate)
	if err != nil {
		return "", err
	}
	if found {
		return resolvedCandidate, nil
	}
	if filepath.IsAbs(filepath.FromSlash(manifestProjectPath)) {
		return "", fmt.Errorf("runtime project path not found: %s", manifestProjectPath)
	}
	projectName := filepath.Base(manifestProjectPath)
	if projectName == "." || projectName == string(filepath.Separator) {
		return "", fmt.Errorf("invalid runtime project path %q", manifestProjectPath)
	}
	candidate := filepath.Join(workspacePath, "projects", projectName)
	resolvedCandidate, found, err = resolveRuntimeProjectCandidate(workspacePath, candidate)
	if err != nil {
		return "", err
	}
	if found {
		return resolvedCandidate, nil
	}
	return "", fmt.Errorf("runtime project path not found: %s or %s", manifestProjectPath, candidate)
}

func resolveRuntimeProjectCandidate(workspacePath, candidate string) (string, bool, error) {
	absCandidate, err := filepath.Abs(candidate)
	if err != nil {
		return "", false, fmt.Errorf("resolve runtime project path: %w", err)
	}
	resolvedCandidate, err := filepath.EvalSymlinks(absCandidate)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("resolve runtime project path: %w", err)
	}
	if !pathWithinRoot(workspacePath, resolvedCandidate) {
		return "", false, fmt.Errorf("runtime project path %s resolves outside runtime workspace %s", candidate, workspacePath)
	}
	info, err := os.Stat(resolvedCandidate)
	if err != nil {
		return "", false, fmt.Errorf("stat runtime project path: %w", err)
	}
	if !info.IsDir() {
		return "", false, fmt.Errorf("runtime project path is not a directory: %s", resolvedCandidate)
	}
	return resolvedCandidate, true, nil
}

func discoverRuntimeProjectPath(workspacePath string) (string, error) {
	workspacePath, err := resolveRuntimeWorkspacePath(workspacePath)
	if err != nil {
		return "", err
	}
	projectsDir := filepath.Join(workspacePath, "projects")
	info, resolvedProjectsDir, err := inspectContainedPath(workspacePath, projectsDir)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("runtime projects path is not a directory: %s", projectsDir)
	}
	entries, err := os.ReadDir(resolvedProjectsDir)
	if err != nil {
		return "", err
	}
	var candidates []string
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("runtime project discovery contains symlink: %s", filepath.Join(resolvedProjectsDir, entry.Name()))
		}
		if !entry.IsDir() {
			continue
		}
		candidate, found, err := resolveRuntimeProjectCandidate(workspacePath, filepath.Join(resolvedProjectsDir, entry.Name()))
		if err != nil {
			return "", err
		}
		if found {
			candidates = append(candidates, candidate)
		}
	}
	return newestPath(candidates), nil
}

func collectRuntimeArtifacts(ctx context.Context, workspacePath, projectPath string, manifest runtimeArtifactManifest, hasManifest bool) ([]publishedRuntimeArtifact, error) {
	byObjectRel := map[string]publishedRuntimeArtifact{}
	addFile := func(permittedRoot, sourcePath, objectRel string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		cleanRel, err := cleanArtifactRel(objectRel)
		if err != nil {
			return err
		}
		info, resolvedPath, err := inspectContainedPath(permittedRoot, sourcePath)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("runtime artifact is not a regular file: %s", sourcePath)
		}
		byObjectRel[cleanRel] = publishedRuntimeArtifact{
			SourcePath: resolvedPath,
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
			if err := addFile(projectPath, filepath.Join(projectPath, filepath.FromSlash(cleanRel)), cleanRel); err != nil {
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
		if err := addArtifactRoot(ctx, projectPath, root.ProjectRel, root.ObjectRel, func(sourcePath, objectRel string) error {
			return addFile(projectPath, sourcePath, objectRel)
		}); err != nil {
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
		if err := addFile(workspacePath, filepath.Join(workspacePath, file.SourceRel), file.ObjectRel); err != nil {
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
	info, _, err := inspectContainedPath(projectPath, sourceRoot)
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
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("runtime artifact path contains symlink: %s", path)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("runtime artifact is not a regular file: %s", path)
		}
		rel, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return err
		}
		return addFile(path, filepath.ToSlash(filepath.Join(objectRel, rel)))
	})
}

func inspectContainedPath(permittedRoot, candidate string) (os.FileInfo, string, error) {
	rootInputAbs, err := filepath.Abs(permittedRoot)
	if err != nil {
		return nil, "", err
	}
	candidateInputAbs, err := filepath.Abs(candidate)
	if err != nil {
		return nil, "", err
	}
	if !pathWithinRoot(rootInputAbs, candidateInputAbs) {
		return nil, "", fmt.Errorf("runtime artifact path %s is outside permitted root %s", candidate, permittedRoot)
	}
	rel, err := filepath.Rel(rootInputAbs, candidateInputAbs)
	if err != nil {
		return nil, "", err
	}
	rootAbs, err := filepath.EvalSymlinks(rootInputAbs)
	if err != nil {
		return nil, "", err
	}
	candidateAbs := filepath.Join(rootAbs, rel)
	current := rootAbs
	if rel != "." {
		for _, component := range strings.Split(rel, string(filepath.Separator)) {
			current = filepath.Join(current, component)
			info, err := os.Lstat(current)
			if err != nil {
				return nil, "", err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return nil, "", fmt.Errorf("runtime artifact path contains symlink component: %s", current)
			}
		}
	}
	info, err := os.Lstat(candidateAbs)
	if err != nil {
		return nil, "", err
	}
	resolvedCandidate, err := filepath.EvalSymlinks(candidateAbs)
	if err != nil {
		return nil, "", err
	}
	if !pathWithinRoot(rootAbs, resolvedCandidate) {
		return nil, "", fmt.Errorf("runtime artifact path %s resolves outside permitted root %s", candidate, permittedRoot)
	}
	return info, resolvedCandidate, nil
}

func pathWithinRoot(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
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
