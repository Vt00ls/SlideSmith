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

type resourcePublishCandidate struct {
	RelativePath string
	Kind         string
}

func (s *TaskService) publishResourcePhaseArtifacts(ctx context.Context, task *model.Task, projectPath, phaseRunID string, diagnosticsOnly bool) error {
	if task == nil || phaseRunID == "" {
		return fmt.Errorf("resource publisher requires task and phase run")
	}
	projectRoot, err := filepath.Abs(projectPath)
	if err != nil {
		return err
	}
	candidates := []resourcePublishCandidate{
		{RelativePath: ".slidesmith/resource_plan.json", Kind: model.ArtifactKindResourcePlan},
		{RelativePath: ".slidesmith/resource_policy.json", Kind: model.ArtifactKindResourcePolicy},
		{RelativePath: "analysis/resource_requirements.json", Kind: model.ArtifactKindResourceRequirements},
		{RelativePath: ".slidesmith/resources_manifest.json", Kind: model.ArtifactKindResourceManifest},
		{RelativePath: "analysis/image_analysis.csv", Kind: model.ArtifactKindImageAnalysis},
		{RelativePath: ".slidesmith/contracts/image_acquire.json", Kind: model.ArtifactKindManifest},
		{RelativePath: "images/image_prompts.json", Kind: model.ArtifactKindImagePromptManifest},
		{RelativePath: "images/image_prompts.md", Kind: model.ArtifactKindImagePromptReview},
		{RelativePath: "images/image_queries.json", Kind: model.ArtifactKindImageQueryManifest},
		{RelativePath: "images/image_sources.json", Kind: model.ArtifactKindImageSourceManifest},
		{RelativePath: "images/formula_manifest.json", Kind: model.ArtifactKindFormulaManifest},
	}
	manifestPath := filepath.Join(projectRoot, ".slidesmith", "resources_manifest.json")
	var manifest resourcesManifest
	if raw, readErr := os.ReadFile(manifestPath); readErr == nil {
		if decodeErr := json.Unmarshal(raw, &manifest); decodeErr != nil && !diagnosticsOnly {
			return decodeErr
		}
	}
	if !diagnosticsOnly {
		for _, item := range manifest.Resources {
			if item.Status != "ready" || !item.Publishable || item.Output == nil {
				continue
			}
			kind := model.ArtifactKindResourceAsset
			switch item.Type {
			case "chart_data":
				kind = model.ArtifactKindChartData
			case "chart_template":
				kind = model.ArtifactKindChartTemplate
			}
			candidates = append(candidates, resourcePublishCandidate{RelativePath: item.Output.Path, Kind: kind})
		}
	}
	seen := map[string]bool{}
	filtered := make([]resourcePublishCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		rel := filepath.ToSlash(filepath.Clean(filepath.FromSlash(candidate.RelativePath)))
		if rel == "." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) || seen[rel] {
			continue
		}
		path := filepath.Join(projectRoot, filepath.FromSlash(rel))
		info, resolved, inspectErr := inspectContainedPath(projectRoot, path)
		if inspectErr != nil {
			if os.IsNotExist(inspectErr) {
				continue
			}
			return inspectErr
		}
		if !info.Mode().IsRegular() || info.Size() <= 0 {
			continue
		}
		if candidate.Kind == model.ArtifactKindResourceAsset || candidate.Kind == model.ArtifactKindChartData || candidate.Kind == model.ArtifactKindChartTemplate {
			output := findManifestOutputByPath(manifest.Resources, rel)
			if output == nil {
				return fmt.Errorf("resource publisher path %s is not manifest-bound", rel)
			}
			policy, policyErr := loadResourcePolicy(projectRoot)
			if policyErr != nil {
				return policyErr
			}
			if validateErr := validateResourceOutput(projectRoot, output, policy.MaxSingleBytes); validateErr != nil {
				return validateErr
			}
		}
		_ = resolved
		seen[rel] = true
		candidate.RelativePath = rel
		filtered = append(filtered, candidate)
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].RelativePath < filtered[j].RelativePath })
	basePrefix := filepath.ToSlash(filepath.Join("tasks", task.ID, "resources")) + "/"
	versionPrefix := filepath.ToSlash(filepath.Join(basePrefix, phaseRunID)) + "/"
	previous, err := s.repo.ListArtifactsByObjectKeyPrefix(ctx, task.ID, basePrefix)
	if err != nil {
		return err
	}
	artifacts := make([]model.Artifact, 0, len(filtered))
	for _, candidate := range filtered {
		sourcePath := filepath.Join(projectRoot, filepath.FromSlash(candidate.RelativePath))
		objectKey := versionPrefix + candidate.RelativePath
		stored, err := s.storage.CopyFileToObject(ctx, objectKey, sourcePath)
		if err != nil {
			return err
		}
		if candidate.Kind == model.ArtifactKindResourceAsset || candidate.Kind == model.ArtifactKindChartData || candidate.Kind == model.ArtifactKindChartTemplate {
			expected := findManifestOutputByPath(manifest.Resources, candidate.RelativePath)
			if expected == nil || stored.SHA256 != expected.SHA256 || stored.Size != expected.Size {
				_ = s.storage.DeleteObject(context.WithoutCancel(ctx), stored.ObjectKey)
				return fmt.Errorf("published resource %s does not match manifest hash/size", candidate.RelativePath)
			}
		}
		metadata, _ := json.Marshal(map[string]any{
			"schema":                "slidesmith.resource_artifact_metadata.v1",
			"phase":                 string(PhaseImageAcquire),
			"phase_run_id":          phaseRunID,
			"project_relative_path": candidate.RelativePath,
			"diagnostic":            diagnosticsOnly,
		})
		artifacts = append(artifacts, model.Artifact{
			TaskID: task.ID, Kind: candidate.Kind, Name: filepath.Base(candidate.RelativePath),
			Storage: "local", ObjectKey: stored.ObjectKey, MimeType: stored.MimeType,
			Size: stored.Size, SHA256: stored.SHA256, MetadataJSON: string(metadata),
		})
	}
	if err := s.repo.ReplaceArtifactsByObjectKeyPrefix(ctx, task.ID, basePrefix, artifacts); err != nil {
		return err
	}
	newKeys := map[string]bool{}
	for _, artifact := range artifacts {
		newKeys[artifact.ObjectKey] = true
	}
	for _, artifact := range previous {
		if !newKeys[artifact.ObjectKey] {
			_ = s.storage.DeleteObject(context.WithoutCancel(ctx), artifact.ObjectKey)
		}
	}
	_ = s.event(ctx, task.ID, model.EventTypeArtifact, "resources_published", "Resource phase artifacts published", map[string]any{
		"phase_run_id":   phaseRunID,
		"artifact_count": len(artifacts),
		"diagnostic":     diagnosticsOnly,
	})
	return nil
}

func findManifestOutputByPath(items []resourceManifestItem, relative string) *resourceManifestOutput {
	for index := range items {
		if items[index].Output != nil && filepath.ToSlash(items[index].Output.Path) == relative {
			return items[index].Output
		}
	}
	return nil
}
