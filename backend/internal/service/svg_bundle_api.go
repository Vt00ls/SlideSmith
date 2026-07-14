package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

type TaskSVGBundleView struct {
	TaskID          string            `json:"task_id"`
	PhaseStatus     string            `json:"phase_status"`
	Passed          bool              `json:"passed"`
	Canvas          TaskSVGCanvasView `json:"canvas"`
	PageCount       int               `json:"page_count"`
	Pages           []TaskSVGPageView `json:"pages"`
	ResourceSummary map[string]int    `json:"resource_summary"`
	ChartSummary    map[string]int    `json:"chart_summary"`
	Notes           TaskSVGNotesView  `json:"notes"`
	Errors          []string          `json:"errors"`
	Warnings        []string          `json:"warnings"`
	ArtifactIDs     map[string]string `json:"artifact_ids"`
	InventorySHA256 string            `json:"inventory_sha256"`
	PhaseRunID      string            `json:"phase_run_id"`
}

type TaskSVGCanvasView struct {
	ID     string  `json:"id"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type TaskSVGPageView struct {
	PageID        string   `json:"page_id"`
	Page          int      `json:"page"`
	Filename      string   `json:"filename"`
	SHA256        string   `json:"sha256"`
	TextCount     int      `json:"text_count"`
	ImageCount    int      `json:"image_count"`
	ChartCount    int      `json:"chart_count"`
	ResourceCount int      `json:"resource_count"`
	NotesPresent  bool     `json:"notes_present"`
	Warnings      []string `json:"warnings"`
	ArtifactID    string   `json:"artifact_id,omitempty"`
}

type TaskSVGNotesView struct {
	Present    bool `json:"present"`
	PageCount  int  `json:"page_count"`
	EmptyPages int  `json:"empty_pages"`
}

func (s *TaskService) GetSVGBundle(ctx context.Context, taskID string) (*TaskSVGBundleView, error) {
	task, err := s.repo.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	view := &TaskSVGBundleView{
		TaskID: task.ID, Pages: []TaskSVGPageView{}, ResourceSummary: map[string]int{}, ChartSummary: map[string]int{},
		Errors: []string{}, Warnings: []string{}, ArtifactIDs: map[string]string{},
	}
	phaseRuns, err := s.repo.ListPhaseRuns(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	for _, run := range phaseRuns {
		if run.Phase == string(PhaseSVGExecute) {
			view.PhaseStatus = run.Status
			view.PhaseRunID = run.ID
		}
	}
	if task.Status == model.TaskStatusFailed && strings.HasPrefix(strings.ToLower(task.FailurePhase), string(PhaseSVGExecute)) {
		view.Errors = append(view.Errors, task.FailurePhase)
	}
	projectPath, err := s.findPersistentProjectPath(task)
	if err != nil {
		return view, nil
	}
	inventoryPath := filepath.Join(projectPath, "analysis", "svg_inventory.json")
	if _, err := os.Stat(inventoryPath); os.IsNotExist(err) {
		return view, nil
	}
	if err != nil {
		return nil, err
	}
	contract, err := validateSVGBundleContract(projectPath, task.ID)
	if err != nil {
		view.Errors = append(view.Errors, "svg_execute.contract_stale")
		return view, nil
	}
	var inventory svgInventoryDocument
	if err := readJSONContract(inventoryPath, &inventory); err != nil {
		view.Errors = append(view.Errors, "svg_execute.contract_stale")
		return view, nil
	}
	var notes notesInventoryDocument
	if err := readJSONContract(filepath.Join(projectPath, "analysis", "notes_inventory.json"), &notes); err != nil {
		view.Errors = append(view.Errors, "svg_execute.contract_stale")
		return view, nil
	}
	artifacts, err := s.repo.ListArtifactsByObjectKeyPrefix(ctx, task.ID, filepath.ToSlash(filepath.Join("tasks", task.ID, "svg-bundle"))+"/")
	if err != nil {
		return nil, err
	}
	artifactByPath := map[string]string{}
	for _, artifact := range artifacts {
		var metadata struct {
			ProjectRelativePath string `json:"project_relative_path"`
			PhaseRunID          string `json:"phase_run_id"`
			ContractPassed      bool   `json:"contract_passed"`
			Diagnostic          bool   `json:"diagnostic"`
		}
		if json.Unmarshal([]byte(artifact.MetadataJSON), &metadata) != nil || !metadata.ContractPassed || metadata.Diagnostic {
			continue
		}
		if view.PhaseRunID != "" && metadata.PhaseRunID != view.PhaseRunID {
			continue
		}
		artifactByPath[metadata.ProjectRelativePath] = artifact.ID
		view.ArtifactIDs[artifact.Kind] = artifact.ID
	}
	view.Passed = true
	view.Canvas = TaskSVGCanvasView{ID: inventory.Canvas}
	if canvas, canvasErr := readConfirmedSVGCanvas(projectPath); canvasErr == nil {
		view.Canvas.Width, view.Canvas.Height = canvas.Width, canvas.Height
	}
	view.PageCount = inventory.PageCount
	view.ResourceSummary = inventory.ResourceSummary
	view.ChartSummary = inventory.ChartSummary
	view.InventorySHA256, _ = contract["svg_inventory_sha256"].(string)
	notesByPage := map[string]bool{}
	for _, page := range notes.Pages {
		notesByPage[page.PageID] = true
		if page.Empty {
			view.Notes.EmptyPages++
		}
	}
	view.Notes.Present = notes.PageCount == inventory.PageCount
	view.Notes.PageCount = notes.PageCount
	for _, page := range inventory.Pages {
		view.Pages = append(view.Pages, TaskSVGPageView{
			PageID: page.PageID, Page: page.Page, Filename: filepath.Base(filepath.FromSlash(page.Path)), SHA256: page.SHA256,
			TextCount: page.TextCount, ImageCount: page.ImageCount, ChartCount: page.ChartCount,
			ResourceCount: len(page.ResourceIDs), NotesPresent: notesByPage[page.PageID], Warnings: append([]string{}, page.Warnings...),
			ArtifactID: artifactByPath[page.Path],
		})
		view.Warnings = append(view.Warnings, page.Warnings...)
	}
	return view, nil
}
