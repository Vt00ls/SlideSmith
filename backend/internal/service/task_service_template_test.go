package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/slidesmith/slidesmith/backend/internal/config"
	"github.com/slidesmith/slidesmith/backend/internal/model"
	"github.com/slidesmith/slidesmith/backend/internal/repository"
	"gorm.io/gorm"
)

func TestCreateTaskPersistsTemplateLock(t *testing.T) {
	skillDir := buildTemplateCatalogFixture(t)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Task{}, &model.TaskEvent{}, &model.TemplateRegistryEntry{}); err != nil {
		t.Fatal(err)
	}
	repo := repository.New(db)
	service := NewTaskService(repo, nil, nil, nil, config.AgentComposeConfig{
		PPTMasterSkillDir: skillDir,
	})
	if _, err := service.templates.SyncFromDisk(context.Background()); err != nil {
		t.Fatal(err)
	}

	task, err := service.CreateTask(context.Background(), "Template lock task", "layout:government_blue")
	if err != nil {
		t.Fatal(err)
	}
	if task.SelectedTemplateID != "layout:government_blue" {
		t.Fatalf("SelectedTemplateID = %q", task.SelectedTemplateID)
	}
	var lock TemplateLock
	if err := json.Unmarshal([]byte(task.TemplateLockJSON), &lock); err != nil {
		t.Fatalf("invalid template lock json: %v", err)
	}
	if lock.TemplateID != "layout:government_blue" || lock.Kind != "layout" || lock.Checksum == "" {
		t.Fatalf("unexpected template lock: %#v", lock)
	}
}
