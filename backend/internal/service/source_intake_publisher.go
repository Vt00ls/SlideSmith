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

type sourceIntakeArtifactFile struct {
	SourcePath string
	ProjectRel string
	ObjectRel  string
	Kind       string
}

func (s *TaskService) publishSourceIntakeArtifacts(ctx context.Context, task *model.Task, projectPath string) ([]model.Artifact, error) {
	if task == nil {
		return nil, fmt.Errorf("source intake task is nil")
	}
	if strings.TrimSpace(projectPath) == "" {
		return nil, fmt.Errorf("source intake project path is empty")
	}

	files, err := collectSourceIntakeArtifactFiles(projectPath)
	if err != nil {
		return nil, err
	}
	prefix := filepath.ToSlash(filepath.Join("tasks", task.ID, "source-intake")) + "/"
	artifacts := make([]model.Artifact, 0, len(files))
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		metadataJSON, err := json.Marshal(map[string]string{
			"schema":                "slidesmith.source_intake_artifact_metadata.v1",
			"source_phase":          string(PhaseSourcePrepare),
			"project_relative_path": file.ProjectRel,
			"route":                 task.Route,
		})
		if err != nil {
			return nil, fmt.Errorf("encode source intake metadata for %s: %w", file.ProjectRel, err)
		}
		objectKey := prefix + file.ObjectRel
		stored, err := s.storage.CopyFileToObject(ctx, objectKey, file.SourcePath)
		if err != nil {
			return nil, fmt.Errorf("copy source intake artifact %s: %w", file.ProjectRel, err)
		}
		artifacts = append(artifacts, model.Artifact{
			TaskID:         task.ID,
			Kind:           file.Kind,
			Name:           stored.Name,
			Storage:        "local",
			ObjectKey:      stored.ObjectKey,
			MimeType:       stored.MimeType,
			Size:           stored.Size,
			SHA256:         stored.SHA256,
			PublishVersion: "",
			MetadataJSON:   string(metadataJSON),
		})
	}
	if err := s.repo.ReplaceArtifactsByObjectKeyPrefix(ctx, task.ID, prefix, artifacts); err != nil {
		return nil, fmt.Errorf("persist source intake artifacts: %w", err)
	}
	return artifacts, nil
}

func collectSourceIntakeArtifactFiles(projectPath string) ([]sourceIntakeArtifactFile, error) {
	var files []sourceIntakeArtifactFile
	appendDirectoryFiles := func(projectRel string, classify func(string) (string, bool)) error {
		directoryPath := filepath.Join(projectPath, filepath.FromSlash(projectRel))
		entries, err := os.ReadDir(directoryPath)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read source intake directory %s: %w", projectRel, err)
		}
		for _, entry := range entries {
			info, err := entry.Info()
			if err != nil {
				return fmt.Errorf("inspect source intake file %s: %w", filepath.ToSlash(filepath.Join(projectRel, entry.Name())), err)
			}
			if !info.Mode().IsRegular() {
				continue
			}
			kind, ok := classify(entry.Name())
			if !ok {
				continue
			}
			rel := filepath.ToSlash(filepath.Join(projectRel, entry.Name()))
			files = append(files, sourceIntakeArtifactFile{
				SourcePath: filepath.Join(directoryPath, entry.Name()),
				ProjectRel: rel,
				ObjectRel:  rel,
				Kind:       kind,
			})
		}
		return nil
	}

	if err := appendDirectoryFiles("sources", sourceIntakeSourceKind); err != nil {
		return nil, err
	}
	if err := appendDirectoryFiles("analysis", sourceIntakeAnalysisKind); err != nil {
		return nil, err
	}
	contractRel := filepath.ToSlash(filepath.Join(".slidesmith", "contracts", "source_prepare.json"))
	contractPath := filepath.Join(projectPath, filepath.FromSlash(contractRel))
	contractInfo, err := os.Lstat(contractPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect source intake contract %s: %w", contractRel, err)
	}
	if err == nil && contractInfo.Mode().IsRegular() {
		files = append(files, sourceIntakeArtifactFile{
			SourcePath: contractPath,
			ProjectRel: contractRel,
			ObjectRel:  "contracts/source_prepare.json",
			Kind:       model.ArtifactKindManifest,
		})
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].ProjectRel < files[j].ProjectRel
	})
	return files, nil
}

func sourceIntakeSourceKind(name string) (string, bool) {
	lowerName := strings.ToLower(name)
	if strings.HasSuffix(lowerName, ".conversion_profile.json") {
		return model.ArtifactKindSourceConversionProfile, true
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".md", ".markdown", ".txt", ".text", ".csv", ".tsv":
		return model.ArtifactKindSourceMarkdown, true
	default:
		return "", false
	}
}

func sourceIntakeAnalysisKind(name string) (string, bool) {
	lowerName := strings.ToLower(name)
	switch {
	case lowerName == "source_profile.json":
		return model.ArtifactKindSourceProfile, true
	case strings.HasSuffix(lowerName, ".identity.json"):
		return model.ArtifactKindPPTXIdentity, true
	case strings.HasSuffix(lowerName, ".slide_library.json"):
		return model.ArtifactKindPPTXSlideLibrary, true
	default:
		return "", false
	}
}
