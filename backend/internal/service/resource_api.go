package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

func (s *TaskService) GetResources(ctx context.Context, taskID string) (*TaskResourceView, error) {
	task, err := s.repo.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	view := &TaskResourceView{TaskID: task.ID, Resources: []TaskResourceItemView{}}
	phaseRuns, err := s.repo.ListPhaseRuns(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	for _, run := range phaseRuns {
		if run.Phase == string(PhaseImageAcquire) {
			view.PhaseStatus = run.Status
		}
	}
	projectPath, err := s.findPersistentProjectPath(task)
	if err != nil {
		return view, nil
	}
	manifestPath := filepath.Join(projectPath, ".slidesmith", "resources_manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if os.IsNotExist(err) {
		return view, nil
	}
	if err != nil {
		return nil, err
	}
	var manifest resourcesManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, err
	}
	if manifest.Schema != resourcesManifestSchema || manifest.TaskID != task.ID || manifest.Route != task.Route || manifest.RunnerProfile != task.RunnerProfile {
		return nil, fmt.Errorf("resource manifest task/route/profile binding mismatch")
	}
	view.Summary = manifest.Summary
	view.ManifestHash, _ = sha256File(manifestPath)
	artifacts, err := s.repo.ListArtifacts(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	artifactBySHA := map[string]string{}
	for _, artifact := range artifacts {
		if artifact.Kind == model.ArtifactKindResourceAsset || artifact.Kind == model.ArtifactKindChartData || artifact.Kind == model.ArtifactKindChartTemplate {
			artifactBySHA[artifact.SHA256] = artifact.ID
		}
	}
	for _, item := range manifest.Resources {
		resourceView := TaskResourceItemView{
			ID: item.ID, Page: item.Page, Type: item.Type, Purpose: item.Purpose,
			Required: item.Required, AcquireVia: item.AcquireVia, Provider: item.Provider,
			Status: item.Status, Fallback: item.Fallback, Publishable: item.Publishable,
		}
		if item.Output != nil {
			resourceView.ArtifactID = artifactBySHA[item.Output.SHA256]
			resourceView.MimeType = item.Output.MimeType
			resourceView.Size = item.Output.Size
			resourceView.Width = item.Output.Width
			resourceView.Height = item.Output.Height
		}
		if item.Error != nil {
			resourceView.ErrorCode, _ = item.Error["code"].(string)
			message, _ := item.Error["message"].(string)
			resourceView.ErrorCode, resourceView.Error = safeResourceAPIError(resourceView.ErrorCode, message)
		}
		view.Resources = append(view.Resources, resourceView)
	}
	return view, nil
}

var safeResourceAPIErrorCode = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,127}$`)

func safeResourceAPIError(code, message string) (string, string) {
	code = strings.ToLower(strings.TrimSpace(code))
	if !safeResourceAPIErrorCode.MatchString(code) {
		return "resource_error", "resource_error"
	}
	message = strings.ToLower(strings.TrimSpace(message))
	if message == "" || message == code || safeResourceAPIErrorCode.MatchString(message) {
		return code, code
	}
	return code, "resource_error"
}
