package router

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/slidesmith/slidesmith/backend/internal/handler"
	"github.com/slidesmith/slidesmith/backend/internal/service"
)

func TestRegisterTemplateRoutesWithoutTaskHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	Register(engine, Handlers{
		Templates: handler.NewTemplateHandler(service.NewTemplateCatalogService(t.TempDir())),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/templates", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code == http.StatusNotFound {
		t.Fatal("template list route was not registered")
	}
}

func TestRegisterTemplateAssetRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	skillDir := t.TempDir()
	mustWriteRouterTestFile(t, filepath.Join(skillDir, "templates", "layouts", "layouts_index.json"), `{
  "government_blue": {
    "summary": "Key project briefings",
    "canvas_format": "ppt169",
    "page_count": 5,
    "page_types": ["cover"]
  }
}`)
	mustWriteRouterTestFile(t, filepath.Join(skillDir, "templates", "layouts", "government_blue", "01_cover.svg"), "<svg></svg>\n")

	engine := gin.New()
	Register(engine, Handlers{
		Templates: handler.NewTemplateHandler(service.NewTemplateCatalogService(skillDir)),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/templates/layout:government_blue/assets/01_cover.svg", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("asset route status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "image/svg+xml" {
		t.Fatalf("asset content type = %q", rec.Header().Get("Content-Type"))
	}
}

func mustWriteRouterTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
