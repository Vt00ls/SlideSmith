package service

import (
	"context"
	"encoding/json"
	"errors"
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

type sourceIntakeArtifactPlan struct {
	File         sourceIntakeArtifactFile
	ObjectKey    string
	MetadataJSON string
	Snapshot     sourceIntakeObjectSnapshot
}

type sourceIntakeObjectSnapshot struct {
	ObjectKey string
	Path      string
	Bytes     []byte
	Mode      os.FileMode
	Existed   bool
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
	plans := make([]sourceIntakeArtifactPlan, 0, len(files))
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
		snapshot, err := snapshotSourceIntakeObject(objectKey, s.storage.Path(objectKey))
		if err != nil {
			return nil, fmt.Errorf("snapshot source intake artifact %s: %w", file.ProjectRel, err)
		}
		plans = append(plans, sourceIntakeArtifactPlan{
			File:         file,
			ObjectKey:    objectKey,
			MetadataJSON: string(metadataJSON),
			Snapshot:     snapshot,
		})
	}

	artifacts := make([]model.Artifact, 0, len(plans))
	touched := 0
	for _, plan := range plans {
		if err := ctx.Err(); err != nil {
			return nil, rollbackSourceIntakeObjects(err, plans[:touched])
		}
		touched++
		stored, err := s.storage.CopyFileToObject(ctx, plan.ObjectKey, plan.File.SourcePath)
		if err != nil {
			return nil, rollbackSourceIntakeObjects(
				fmt.Errorf("copy source intake artifact %s: %w", plan.File.ProjectRel, err),
				plans[:touched],
			)
		}
		artifacts = append(artifacts, model.Artifact{
			TaskID:         task.ID,
			Kind:           plan.File.Kind,
			Name:           stored.Name,
			Storage:        "local",
			ObjectKey:      stored.ObjectKey,
			MimeType:       stored.MimeType,
			Size:           stored.Size,
			SHA256:         stored.SHA256,
			PublishVersion: "",
			MetadataJSON:   plan.MetadataJSON,
		})
	}
	if err := s.repo.ReplaceArtifactsByObjectKeyPrefix(ctx, task.ID, prefix, artifacts); err != nil {
		return nil, rollbackSourceIntakeObjects(
			fmt.Errorf("persist source intake artifacts: %w", err),
			plans[:touched],
		)
	}

	persisted, err := s.repo.ListArtifacts(ctx, task.ID)
	if err != nil {
		return nil, fmt.Errorf("load persisted source intake artifacts: %w", err)
	}
	persistedByObjectKey := make(map[string]model.Artifact, len(artifacts))
	for _, artifact := range persisted {
		if strings.HasPrefix(artifact.ObjectKey, prefix) {
			persistedByObjectKey[artifact.ObjectKey] = artifact
		}
	}
	if len(persistedByObjectKey) != len(artifacts) {
		return nil, fmt.Errorf("persisted source intake artifact count is %d, want %d", len(persistedByObjectKey), len(artifacts))
	}
	result := make([]model.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		persistedArtifact, ok := persistedByObjectKey[artifact.ObjectKey]
		if !ok {
			return nil, fmt.Errorf("persisted source intake artifact %s is missing", artifact.ObjectKey)
		}
		result = append(result, persistedArtifact)
	}
	return result, nil
}

func snapshotSourceIntakeObject(objectKey, path string) (sourceIntakeObjectSnapshot, error) {
	snapshot := sourceIntakeObjectSnapshot{ObjectKey: objectKey, Path: path}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return snapshot, nil
	}
	if err != nil {
		return sourceIntakeObjectSnapshot{}, err
	}
	if !info.Mode().IsRegular() {
		return sourceIntakeObjectSnapshot{}, fmt.Errorf("existing object is not a regular file")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return sourceIntakeObjectSnapshot{}, err
	}
	snapshot.Bytes = contents
	snapshot.Mode = info.Mode()
	snapshot.Existed = true
	return snapshot, nil
}

func rollbackSourceIntakeObjects(cause error, plans []sourceIntakeArtifactPlan) error {
	errs := []error{cause}
	for i := len(plans) - 1; i >= 0; i-- {
		if err := restoreSourceIntakeObject(plans[i].Snapshot); err != nil {
			errs = append(errs, fmt.Errorf("rollback source intake object %s: %w", plans[i].ObjectKey, err))
		}
	}
	return errors.Join(errs...)
}

func restoreSourceIntakeObject(snapshot sourceIntakeObjectSnapshot) error {
	if !snapshot.Existed {
		if err := os.Remove(snapshot.Path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(snapshot.Path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(snapshot.Path), ".source-intake-rollback-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	closed := false
	defer func() {
		if !closed {
			_ = temp.Close()
		}
		_ = os.Remove(tempPath)
	}()
	if _, err := temp.Write(snapshot.Bytes); err != nil {
		return err
	}
	if err := temp.Chmod(snapshot.Mode.Perm()); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		closed = true
		return err
	}
	closed = true
	return os.Rename(tempPath, snapshot.Path)
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
