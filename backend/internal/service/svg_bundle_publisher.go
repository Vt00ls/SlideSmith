package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

type svgBundlePublishCandidate struct {
	RelativePath string
	Kind         string
	PageID       string
	Page         int
}

func (s *TaskService) publishSVGBundleArtifacts(ctx context.Context, task *model.Task, projectPath, phaseRunID string, contract map[string]any) error {
	if task == nil || phaseRunID == "" {
		return fmt.Errorf("SVG bundle publisher requires task and phase run")
	}
	if _, err := validateSVGBundleContract(projectPath, task.ID); err != nil {
		return fmt.Errorf("publish SVG bundle contract: %w", err)
	}
	var inventory svgInventoryDocument
	if err := readJSONContract(filepath.Join(projectPath, "analysis", "svg_inventory.json"), &inventory); err != nil {
		return err
	}
	inventorySHA, _ := contract["svg_inventory_sha256"].(string)
	if inventorySHA == "" {
		return fmt.Errorf("SVG bundle publisher contract is missing inventory hash")
	}
	candidates := []svgBundlePublishCandidate{
		{RelativePath: "analysis/svg_inventory.json", Kind: model.ArtifactKindSVGInventory},
		{RelativePath: "analysis/svg_resource_usage.json", Kind: model.ArtifactKindSVGResourceUsage},
		{RelativePath: "analysis/chart_usage.json", Kind: model.ArtifactKindChartUsage},
		{RelativePath: "analysis/notes_inventory.json", Kind: model.ArtifactKindNotesInventory},
		{RelativePath: "notes/total.md", Kind: model.ArtifactKindSpeakerNotes},
		{RelativePath: ".slidesmith/contracts/svg_execute.json", Kind: model.ArtifactKindManifest},
	}
	for _, page := range inventory.Pages {
		candidates = append(candidates, svgBundlePublishCandidate{
			RelativePath: page.Path, Kind: model.ArtifactKindSVGOutput, PageID: page.PageID, Page: page.Page,
		})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].RelativePath < candidates[j].RelativePath })

	basePrefix := filepath.ToSlash(filepath.Join("tasks", task.ID, "svg-bundle")) + "/"
	versionPrefix := filepath.ToSlash(filepath.Join(basePrefix, phaseRunID)) + "/"
	previous, err := s.repo.ListArtifactsByObjectKeyPrefix(ctx, task.ID, basePrefix)
	if err != nil {
		return err
	}
	artifacts := make([]model.Artifact, 0, len(candidates))
	newObjectKeys := make([]string, 0, len(candidates))
	committed := false
	defer func() {
		if committed {
			return
		}
		for _, objectKey := range newObjectKeys {
			_ = s.storage.DeleteObject(context.WithoutCancel(ctx), objectKey)
		}
	}()
	for _, candidate := range candidates {
		sourcePath, err := containedProjectContractPath(projectPath, candidate.RelativePath)
		if err != nil {
			return err
		}
		info, resolved, err := inspectContainedPath(projectPath, sourcePath)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() <= 0 {
			return fmt.Errorf("SVG bundle artifact is not a non-empty regular file: %s", candidate.RelativePath)
		}
		objectKey := versionPrefix + candidate.RelativePath
		stored, err := s.storage.CopyFileToObject(ctx, objectKey, resolved)
		if err != nil {
			return err
		}
		newObjectKeys = append(newObjectKeys, stored.ObjectKey)
		metadata, _ := json.Marshal(map[string]any{
			"schema":                "slidesmith.svg_bundle_artifact_metadata.v1",
			"phase":                 string(PhaseSVGExecute),
			"phase_run_id":          phaseRunID,
			"project_relative_path": candidate.RelativePath,
			"svg_inventory_sha256":  inventorySHA,
			"page_id":               candidate.PageID,
			"page":                  candidate.Page,
			"contract_passed":       true,
			"diagnostic":            false,
		})
		artifacts = append(artifacts, model.Artifact{
			TaskID: task.ID, Kind: candidate.Kind, Name: filepath.Base(candidate.RelativePath), Storage: "local",
			ObjectKey: stored.ObjectKey, MimeType: stored.MimeType, Size: stored.Size, SHA256: stored.SHA256,
			MetadataJSON: string(metadata),
		})
	}
	if err := s.repo.ReplaceArtifactsByObjectKeyPrefix(ctx, task.ID, basePrefix, artifacts); err != nil {
		return err
	}
	committed = true
	newKeys := make(map[string]bool, len(artifacts))
	for _, artifact := range artifacts {
		newKeys[artifact.ObjectKey] = true
	}
	for _, artifact := range previous {
		if !newKeys[artifact.ObjectKey] {
			_ = s.storage.DeleteObject(context.WithoutCancel(ctx), artifact.ObjectKey)
		}
	}
	_ = s.event(ctx, task.ID, model.EventTypeArtifact, "svg_bundle_published", "Validated SVG bundle artifacts published", map[string]any{
		"phase_run_id": phaseRunID, "artifact_count": len(artifacts), "svg_inventory_sha256": inventorySHA,
	})
	return nil
}

func (s *TaskService) cleanupSVGBundleArtifacts(ctx context.Context, taskID string) error {
	basePrefix := filepath.ToSlash(filepath.Join("tasks", taskID, "svg-bundle")) + "/"
	artifacts, err := s.repo.ListArtifactsByObjectKeyPrefix(ctx, taskID, basePrefix)
	if err != nil {
		return err
	}
	for _, artifact := range artifacts {
		if err := s.storage.DeleteObject(ctx, artifact.ObjectKey); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return s.repo.DeleteArtifactsByIDsOrObjectKeyPrefix(ctx, taskID, basePrefix, nil)
}
