package service

import (
	"context"
	"encoding/json"
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

func TestSelectRoutePresentationOOXMLPreserveTextIntentUsesBeautifyLikePPTX(t *testing.T) {
	for _, name := range []string{"original.pptx", "original.pptm"} {
		t.Run(name, func(t *testing.T) {
			service, task := routeSelectTestService(t, "请美化演示文稿，保留页数和文字", []model.Artifact{
				{Name: name, Kind: model.ArtifactKindSource, ObjectKey: "tasks/task-1/source/" + name},
			})
			selection, err := service.selectRoute(context.Background(), task)
			if err != nil {
				t.Fatal(err)
			}
			if selection.Route != routeBeautify || selection.StandaloneWorkflow != routeBeautify {
				t.Fatalf("route = %#v, want beautify", selection)
			}
			if selection.Reason != "pptx source with preserve text/page-count beautify intent" {
				t.Fatalf("reason = %q, want the pptx beautify reason", selection.Reason)
			}
		})
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

func TestSelectRoutePresentationOOXMLTemplateWithMarkdownUsesTemplateFillLikePPTX(t *testing.T) {
	for _, name := range []string{"brand_template.pptx", "brand_template.potx"} {
		t.Run(name, func(t *testing.T) {
			service, task := routeSelectTestService(t, "use new content", []model.Artifact{
				{Name: name, Kind: model.ArtifactKindSource, ObjectKey: "tasks/task-1/source/" + name},
				{Name: "content.md", Kind: model.ArtifactKindSource, ObjectKey: "tasks/task-1/source/content.md"},
			})
			selection, err := service.selectRoute(context.Background(), task)
			if err != nil {
				t.Fatal(err)
			}
			if selection.Route != routeTemplateFill || selection.StandaloneWorkflow != routeTemplateFill {
				t.Fatalf("route = %#v, want template-fill", selection)
			}
			if selection.Reason != "pptx template with new content or fill intent" {
				t.Fatalf("reason = %q, want the pptx template-fill reason", selection.Reason)
			}
		})
	}
}

func TestSelectRouteIncludesConfidence(t *testing.T) {
	service, task := routeSelectTestService(t, "请美化 PPTX，保留页数和文字", []model.Artifact{
		{Name: "original.pptx", Kind: model.ArtifactKindSource, ObjectKey: "tasks/task-1/source/original.pptx"},
	})
	selection, err := service.selectRoute(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Route != routeBeautify {
		t.Fatalf("route = %q, want %q", selection.Route, routeBeautify)
	}
	if selection.Confidence != 0.90 {
		t.Fatalf("confidence = %v, want 0.90", selection.Confidence)
	}
}

func TestPersistRouteSelectionUpdatesTask(t *testing.T) {
	service, task := routeSelectTestService(t, "normal markdown task", []model.Artifact{
		{Name: "input.md", Kind: model.ArtifactKindSource, ObjectKey: "tasks/task-1/source/input.md"},
	})
	selection, err := service.selectRoute(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.persistRouteSelection(context.Background(), task, selection); err != nil {
		t.Fatal(err)
	}
	updated, err := service.repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Route != routeMain {
		t.Fatalf("route = %q, want %q", updated.Route, routeMain)
	}
	if updated.RouteSelectionJSON == "" || updated.RouteSelectionJSON == "{}" {
		t.Fatalf("route_selection_json not persisted: %q", updated.RouteSelectionJSON)
	}
	var persisted routeSelection
	if err := json.Unmarshal([]byte(updated.RouteSelectionJSON), &persisted); err != nil {
		t.Fatalf("route_selection_json invalid: %v", err)
	}
	if persisted.Route != routeMain || persisted.Confidence != 0.60 {
		t.Fatalf("persisted selection = %#v, want main with 0.60 confidence", persisted)
	}
	if updated.RouteSelectedAt == nil {
		t.Fatal("route_selected_at is nil")
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
