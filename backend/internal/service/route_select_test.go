package service

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/slidesmith/slidesmith/backend/internal/model"
	"github.com/slidesmith/slidesmith/backend/internal/repository"
	"gorm.io/gorm"
)

func TestSelectRouteDefaultsMarkdownPDFDOCXToMain(t *testing.T) {
	service, task := routeSelectTestService(t, "normal markdown task", []model.Artifact{
		{Name: "input.md", Kind: model.ArtifactKindSource, ObjectKey: "tasks/task-1/source/input.md"},
	})
	selection, err := service.selectRoute(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Route != routeMain || selection.StandaloneWorkflow != "" {
		t.Fatalf("route = %#v, want main without standalone workflow", selection)
	}
}

func TestSelectRoutePPTXPreserveTextIntentUsesBeautify(t *testing.T) {
	service, task := routeSelectTestService(t, "请美化 PPTX，保留页数和文字", []model.Artifact{
		{Name: "original.pptx", Kind: model.ArtifactKindSource, ObjectKey: "tasks/task-1/source/original.pptx"},
	})
	selection, err := service.selectRoute(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Route != routeBeautify || selection.StandaloneWorkflow != routeBeautify {
		t.Fatalf("route = %#v, want beautify", selection)
	}
}

func TestSelectRoutePPTXAsMaterialStaysMain(t *testing.T) {
	service, task := routeSelectTestService(t, "把 PPTX 作为素材重构", []model.Artifact{
		{Name: "material.pptx", Kind: model.ArtifactKindSource, ObjectKey: "tasks/task-1/source/material.pptx"},
	})
	selection, err := service.selectRoute(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Route != routeMain {
		t.Fatalf("route = %#v, want main", selection)
	}
}

func TestSelectRoutePPTXTemplateWithNewContentUsesTemplateFill(t *testing.T) {
	service, task := routeSelectTestService(t, "use new content", []model.Artifact{
		{Name: "brand_template.pptx", Kind: model.ArtifactKindSource, ObjectKey: "tasks/task-1/source/brand_template.pptx"},
		{Name: "content.md", Kind: model.ArtifactKindSource, ObjectKey: "tasks/task-1/source/content.md"},
	})
	selection, err := service.selectRoute(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Route != routeTemplateFill || selection.StandaloneWorkflow != routeTemplateFill {
		t.Fatalf("route = %#v, want template-fill", selection)
	}
}

func routeSelectTestService(t *testing.T, title string, artifacts []model.Artifact) (*TaskService, *model.Task) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Task{}, &model.Artifact{}); err != nil {
		t.Fatal(err)
	}
	repo := repository.New(db)
	ctx := context.Background()
	task := &model.Task{ID: "task-1", Title: title, Status: model.TaskStatusUploaded, RuntimeProject: "task_1"}
	if err := repo.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	for i := range artifacts {
		artifacts[i].TaskID = task.ID
		if artifacts[i].Storage == "" {
			artifacts[i].Storage = "local"
		}
		if err := repo.CreateArtifact(ctx, &artifacts[i]); err != nil {
			t.Fatal(err)
		}
	}
	return &TaskService{repo: repo}, task
}
