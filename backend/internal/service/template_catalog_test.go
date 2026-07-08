package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/slidesmith/slidesmith/backend/internal/model"
	"github.com/slidesmith/slidesmith/backend/internal/repository"
	"gorm.io/gorm"
)

func TestTemplateCatalogListsTemplatesFromPPTMasterSkill(t *testing.T) {
	skillDir := buildTemplateCatalogFixture(t)
	catalog := NewTemplateCatalogService(skillDir)

	items, err := catalog.ListTemplates(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	item := findTemplateItem(items, "layout:government_blue")
	if item == nil {
		t.Fatalf("missing layout:government_blue in %#v", items)
	}
	if item.Kind != "layout" || item.Name != "government_blue" || item.Canvas != "ppt169" {
		t.Fatalf("unexpected template item: %#v", item)
	}
	if item.DefaultPageCount != 5 {
		t.Fatalf("DefaultPageCount = %d, want 5", item.DefaultPageCount)
	}
	if len(item.PageTypes) != 2 || item.PageTypes[0] != "cover" || item.PageTypes[1] != "content" {
		t.Fatalf("unexpected page types: %#v", item.PageTypes)
	}
	if len(item.PreviewAssets) != 2 {
		t.Fatalf("PreviewAssets length = %d, want 2: %#v", len(item.PreviewAssets), item.PreviewAssets)
	}
	if item.PreviewAssets[0].Name != "cover" || item.PreviewAssets[0].Path != "01_cover.svg" {
		t.Fatalf("unexpected first preview: %#v", item.PreviewAssets[0])
	}
	if item.PreviewAssets[0].URL != "/api/templates/layout:government_blue/assets/01_cover.svg" {
		t.Fatalf("unexpected preview URL: %s", item.PreviewAssets[0].URL)
	}
	if item.DesignSpecPath != filepath.Join(skillDir, "templates", "layouts", "government_blue", "design_spec.md") {
		t.Fatalf("unexpected design spec path: %s", item.DesignSpecPath)
	}
}

func TestTemplateCatalogGetsTemplateByID(t *testing.T) {
	skillDir := buildTemplateCatalogFixture(t)
	catalog := NewTemplateCatalogService(skillDir)

	item, err := catalog.GetTemplate(context.Background(), "deck:中国电信")
	if err != nil {
		t.Fatal(err)
	}
	if item.PrimaryColor != "#C00000" {
		t.Fatalf("PrimaryColor = %q, want #C00000", item.PrimaryColor)
	}
	if item.PreviewAssets[0].URL != "/api/templates/deck:%E4%B8%AD%E5%9B%BD%E7%94%B5%E4%BF%A1/assets/01_cover.svg" {
		t.Fatalf("unexpected escaped preview URL: %s", item.PreviewAssets[0].URL)
	}

	_, err = catalog.GetTemplate(context.Background(), "layout:missing")
	if !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("GetTemplate missing error = %v, want ErrNotFound", err)
	}
}

func TestTemplateCatalogAssetPathIsScopedToTemplateDirectory(t *testing.T) {
	skillDir := buildTemplateCatalogFixture(t)
	catalog := NewTemplateCatalogService(skillDir)

	path, contentType, err := catalog.TemplateAssetPath(context.Background(), "layout:government_blue", "/01_cover.svg")
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(skillDir, "templates", "layouts", "government_blue", "01_cover.svg")
	wantPath, err = filepath.EvalSymlinks(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	if path != wantPath {
		t.Fatalf("asset path = %s, want %s", path, wantPath)
	}
	if contentType != "image/svg+xml" {
		t.Fatalf("content type = %q, want image/svg+xml", contentType)
	}

	_, _, err = catalog.TemplateAssetPath(context.Background(), "layout:government_blue", "../layouts_index.json")
	if !errors.Is(err, ErrInvalidTemplateAssetPath) {
		t.Fatalf("asset traversal error = %v, want ErrInvalidTemplateAssetPath", err)
	}
}

func TestTemplateCatalogSyncsRegistryFromDisk(t *testing.T) {
	ctx := context.Background()
	skillDir := buildTemplateCatalogFixture(t)
	repo := newTemplateRegistryTestRepository(t)
	catalog := NewTemplateCatalogServiceWithRepository(repo, skillDir)

	count, err := catalog.SyncFromDisk(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("SyncFromDisk count = %d, want 3", count)
	}

	items, err := catalog.ListTemplates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	item := findTemplateItem(items, "layout:government_blue")
	if item == nil {
		t.Fatalf("missing synced template in %#v", items)
	}
	if item.Status != model.TemplateStatusActive || item.Version != "workspace" {
		t.Fatalf("unexpected registry status/version: %#v", item)
	}
	if !strings.HasPrefix(item.Checksum, "sha256:") {
		t.Fatalf("registry checksum = %q, want sha256 prefix", item.Checksum)
	}
	if got := item.Compatibility["canvas"]; len(got) != 1 || got[0] != "ppt169" {
		t.Fatalf("unexpected compatibility: %#v", item.Compatibility)
	}

	if err := repo.DB().WithContext(ctx).Model(&model.TemplateRegistryEntry{}).
		Where("id = ?", "layout:government_blue").
		Updates(map[string]any{
			"version":  "2026.07",
			"status":   model.TemplateStatusDeprecated,
			"checksum": "sha256:registry-checksum",
		}).Error; err != nil {
		t.Fatal(err)
	}
	lock, err := catalog.BuildTemplateLock(ctx, "layout:government_blue")
	if err != nil {
		t.Fatal(err)
	}
	if lock.Version != "2026.07" || lock.Checksum != "sha256:registry-checksum" {
		t.Fatalf("template lock did not use registry version/checksum: %#v", lock)
	}
}

func TestTemplateCatalogRegistryRemainsStableUntilNextSync(t *testing.T) {
	ctx := context.Background()
	skillDir := buildTemplateCatalogFixture(t)
	repo := newTemplateRegistryTestRepository(t)
	catalog := NewTemplateCatalogServiceWithRepository(repo, skillDir)

	if _, err := catalog.SyncFromDisk(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(skillDir, "templates", "layouts", "government_blue")); err != nil {
		t.Fatal(err)
	}

	items, err := catalog.ListTemplates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if findTemplateItem(items, "layout:government_blue") == nil {
		t.Fatalf("registry should retain template before next sync: %#v", items)
	}

	if _, err := catalog.SyncFromDisk(ctx); err != nil {
		t.Fatal(err)
	}
	items, err = catalog.ListTemplates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if findTemplateItem(items, "layout:government_blue") != nil {
		t.Fatalf("removed template should be hidden after sync: %#v", items)
	}
	_, err = catalog.GetTemplate(ctx, "layout:government_blue")
	if !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("GetTemplate removed error = %v, want ErrNotFound", err)
	}
}

func buildTemplateCatalogFixture(t *testing.T) string {
	t.Helper()
	skillDir := filepath.Join(t.TempDir(), "ppt-master")
	mustWriteFile(t, filepath.Join(skillDir, "templates", "layouts", "layouts_index.json"), `{
  "government_blue": {
    "summary": "Key project briefings",
    "canvas_format": "ppt169",
    "page_count": 5,
    "page_types": ["cover", "content"]
  }
}`)
	mustWriteFile(t, filepath.Join(skillDir, "templates", "layouts", "government_blue", "01_cover.svg"), "<svg></svg>\n")
	mustWriteFile(t, filepath.Join(skillDir, "templates", "layouts", "government_blue", "03_content.svg"), "<svg></svg>\n")
	mustWriteFile(t, filepath.Join(skillDir, "templates", "layouts", "government_blue", "design_spec.md"), "# Spec\n")

	mustWriteFile(t, filepath.Join(skillDir, "templates", "decks", "decks_index.json"), `{
  "中国电信": {
    "summary": "China Telecom related briefings",
    "canvas_format": "ppt169",
    "page_count": 5,
    "primary_color": "#C00000"
  }
}`)
	mustWriteFile(t, filepath.Join(skillDir, "templates", "decks", "中国电信", "01_cover.svg"), "<svg></svg>\n")

	mustWriteFile(t, filepath.Join(skillDir, "templates", "brands", "brands_index.json"), `{
  "google": {
    "summary": "Google brand identity",
    "primary_color": "#4285F4"
  }
}`)
	mustWriteFile(t, filepath.Join(skillDir, "templates", "brands", "google", "google_wordmark.svg"), "<svg></svg>\n")
	return skillDir
}

func newTemplateRegistryTestRepository(t *testing.T) *repository.Repository {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.TemplateRegistryEntry{}); err != nil {
		t.Fatal(err)
	}
	return repository.New(db)
}

func findTemplateItem(items []TemplateCatalogItem, id string) *TemplateCatalogItem {
	for i := range items {
		if items[i].ID == id {
			return &items[i]
		}
	}
	return nil
}
