package handler_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/slidesmith/slidesmith/backend/internal/config"
	"github.com/slidesmith/slidesmith/backend/internal/handler"
	"github.com/slidesmith/slidesmith/backend/internal/model"
	"github.com/slidesmith/slidesmith/backend/internal/repository"
	"github.com/slidesmith/slidesmith/backend/internal/router"
	"github.com/slidesmith/slidesmith/backend/internal/service"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestTemplateFillPlanRoutesExerciseAllFiveEndpoints(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		body       func(*templateFillRouterFixture) []byte
		wantStatus string
	}{
		{
			name:   "get plan",
			method: http.MethodGet,
			path:   "/api/tasks/%s/template-fill/plan",
		},
		{
			name:   "put plan",
			method: http.MethodPut,
			path:   "/api/tasks/%s/template-fill/plan",
			body: func(*templateFillRouterFixture) []byte {
				return mustTemplateFillRouterJSON(t, map[string]any{"plan": templateFillRouterPlan("confirmed")})
			},
		},
		{
			name:       "check plan synchronously",
			method:     http.MethodPost,
			path:       "/api/tasks/%s/template-fill/check",
			wantStatus: model.TaskStatusAwaitingTemplateFillConfirm,
		},
		{
			name:       "confirm plan",
			method:     http.MethodPost,
			path:       "/api/tasks/%s/template-fill/confirm",
			wantStatus: model.TaskStatusTemplateFillChecking,
		},
		{
			name:       "regenerate plan",
			method:     http.MethodPost,
			path:       "/api/tasks/%s/template-fill/regenerate",
			wantStatus: model.TaskStatusTemplateFillPlanning,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newTemplateFillRouterFixture(t)
			body := []byte(nil)
			if test.body != nil {
				body = test.body(fixture)
			}
			req := httptest.NewRequest(test.method, fmt.Sprintf(test.path, fixture.task.ID), bytes.NewReader(body))
			if body != nil {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()
			fixture.engine.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			var response struct {
				Data map[string]any `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
			}
			if test.wantStatus != "" && response.Data["status"] != test.wantStatus {
				t.Fatalf("response status = %#v, want %q; body=%s", response.Data["status"], test.wantStatus, rec.Body.String())
			}
			if test.name == "get plan" {
				if response.Data["plan_file"] == nil || response.Data["inputs"] == nil {
					t.Fatalf("GET preview omits plan_file or inputs: %s", rec.Body.String())
				}
			}
			if test.name == "put plan" {
				plan, ok := response.Data["plan"].(map[string]any)
				if !ok || plan["status"] != "draft" {
					t.Fatalf("PUT response plan = %#v, want forced draft", response.Data["plan"])
				}
			}
		})
	}
}

func TestTemplateFillPlanRoutesReturnBadRequestForInvalidJSONRouteAndStatus(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		body   string
		mutate func(*templateFillRouterFixture)
	}{
		{
			name:   "invalid JSON",
			method: http.MethodPut,
			path:   "/api/tasks/%s/template-fill/plan",
			body:   `{"plan":`,
		},
		{
			name:   "wrong route",
			method: http.MethodGet,
			path:   "/api/tasks/%s/template-fill/plan",
			mutate: func(fixture *templateFillRouterFixture) {
				fixture.task.Route = model.TaskRouteMain
				if err := fixture.repo.SaveTask(context.Background(), fixture.task); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:   "disallowed status",
			method: http.MethodPost,
			path:   "/api/tasks/%s/template-fill/confirm",
			mutate: func(fixture *templateFillRouterFixture) {
				fixture.task.Status = model.TaskStatusCompleted
				if err := fixture.repo.SaveTask(context.Background(), fixture.task); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newTemplateFillRouterFixture(t)
			if test.mutate != nil {
				test.mutate(fixture)
			}
			req := httptest.NewRequest(test.method, fmt.Sprintf(test.path, fixture.task.ID), bytes.NewBufferString(test.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			fixture.engine.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestTemplateFillPlanRouteReturnsNotFoundForMissingTask(t *testing.T) {
	fixture := newTemplateFillRouterFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/api/tasks/missing/template-fill/plan", nil)
	rec := httptest.NewRecorder()
	fixture.engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestTaskResourcesRouteReturnsTaskScopedSafeView(t *testing.T) {
	fixture := newTemplateFillRouterFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/api/tasks/"+fixture.task.ID+"/resources", nil)
	rec := httptest.NewRecorder()
	fixture.engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("resource route status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Data struct {
			TaskID    string `json:"task_id"`
			Resources []any  `json:"resources"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.TaskID != fixture.task.ID || len(response.Data.Resources) != 0 {
		t.Fatalf("resource route response = %#v", response.Data)
	}

	missingReq := httptest.NewRequest(http.MethodGet, "/api/tasks/missing/resources", nil)
	missingRec := httptest.NewRecorder()
	fixture.engine.ServeHTTP(missingRec, missingReq)
	if missingRec.Code != http.StatusNotFound {
		t.Fatalf("missing resource route status = %d, body = %s", missingRec.Code, missingRec.Body.String())
	}
}

func TestBeautifyPlanRoutesExerciseAllFiveEndpointsWithoutLeakingFrozenContent(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		body       func(*beautifyRouterFixture) []byte
		wantStatus string
	}{
		{name: "get", method: http.MethodGet, path: "/api/tasks/%s/beautify-plan"},
		{name: "save", method: http.MethodPut, path: "/api/tasks/%s/beautify-plan", body: func(f *beautifyRouterFixture) []byte {
			preview := getBeautifyRouterPreview(t, f)
			return mustTemplateFillRouterJSON(t, map[string]any{"plan": preview.Data.Plan, "expected_plan_sha256": preview.Data.PlanSHA256})
		}},
		{name: "check", method: http.MethodPost, path: "/api/tasks/%s/beautify-plan/check", wantStatus: model.TaskStatusAwaitingBeautifyConfirm},
		{name: "confirm", method: http.MethodPost, path: "/api/tasks/%s/beautify-plan/confirm", wantStatus: model.TaskStatusSpecGenerating},
		{name: "regenerate", method: http.MethodPost, path: "/api/tasks/%s/beautify-plan/regenerate", wantStatus: model.TaskStatusBeautifyPlanning},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newBeautifyRouterFixture(t)
			var body []byte
			if test.body != nil {
				body = test.body(fixture)
			}
			req := httptest.NewRequest(test.method, fmt.Sprintf(test.path, fixture.task.ID), bytes.NewReader(body))
			if body != nil {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()
			fixture.engine.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), fixture.projectPath) || strings.Contains(rec.Body.String(), "Frozen private source sentence") {
				t.Fatalf("Beautify API leaked absolute path or frozen text: %s", rec.Body.String())
			}
			if test.wantStatus != "" {
				var response struct {
					Data model.Task `json:"data"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if response.Data.Status != test.wantStatus {
					t.Fatalf("status = %q, want %q", response.Data.Status, test.wantStatus)
				}
			}
			if test.name == "confirm" {
				if _, err := os.Stat(filepath.Join(fixture.projectPath, ".slidesmith", "beautify_lock.json")); err != nil {
					t.Fatalf("confirm did not create lock: %v", err)
				}
			}
		})
	}
}

func TestBeautifyPlanRoutesRejectInvalidJSONWrongRouteStatusCASAndFrozenMutation(t *testing.T) {
	t.Run("invalid json", func(t *testing.T) {
		fixture := newBeautifyRouterFixture(t)
		req := httptest.NewRequest(http.MethodPut, "/api/tasks/"+fixture.task.ID+"/beautify-plan", strings.NewReader(`{"plan":`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		fixture.engine.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
	})
	t.Run("wrong route", func(t *testing.T) {
		fixture := newBeautifyRouterFixture(t)
		fixture.task.Route = model.TaskRouteMain
		if err := fixture.repo.SaveTask(context.Background(), fixture.task); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		fixture.engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tasks/"+fixture.task.ID+"/beautify-plan", nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
	})
	t.Run("wrong status", func(t *testing.T) {
		fixture := newBeautifyRouterFixture(t)
		fixture.task.Status = model.TaskStatusCompleted
		if err := fixture.repo.SaveTask(context.Background(), fixture.task); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		fixture.engine.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/tasks/"+fixture.task.ID+"/beautify-plan/confirm", nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
	})
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
		sha    func(string) string
	}{
		{name: "CAS", sha: func(string) string { return strings.Repeat("0", 64) }},
		{name: "frozen mutation", mutate: func(plan map[string]any) { plan["slide_count"] = float64(2) }, sha: func(value string) string { return value }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newBeautifyRouterFixture(t)
			preview := getBeautifyRouterPreview(t, fixture)
			if test.mutate != nil {
				test.mutate(preview.Data.Plan)
			}
			body := mustTemplateFillRouterJSON(t, map[string]any{"plan": preview.Data.Plan, "expected_plan_sha256": test.sha(preview.Data.PlanSHA256)})
			req := httptest.NewRequest(http.MethodPut, "/api/tasks/"+fixture.task.ID+"/beautify-plan", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			fixture.engine.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestBeautifyPlanRouteReturnsNotFound(t *testing.T) {
	fixture := newBeautifyRouterFixture(t)
	rec := httptest.NewRecorder()
	fixture.engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tasks/missing/beautify-plan", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
}

func TestTemplateFillPlanRoutesAllowFailedCheckSaveThenConfirm(t *testing.T) {
	fixture := newTemplateFillRouterFixture(t)
	fixture.task.Status = model.TaskStatusFailed
	fixture.task.FailurePhase = "template_fill_check.contract"
	if err := fixture.repo.SaveTask(context.Background(), fixture.task); err != nil {
		t.Fatal(err)
	}
	body := mustTemplateFillRouterJSON(t, map[string]any{"plan": templateFillRouterPlan("confirmed")})
	saveReq := httptest.NewRequest(http.MethodPut, "/api/tasks/"+fixture.task.ID+"/template-fill/plan", bytes.NewReader(body))
	saveReq.Header.Set("Content-Type", "application/json")
	saveRec := httptest.NewRecorder()
	fixture.engine.ServeHTTP(saveRec, saveReq)
	if saveRec.Code != http.StatusOK {
		t.Fatalf("save status = %d, body = %s", saveRec.Code, saveRec.Body.String())
	}
	var preview struct {
		Data struct {
			CanConfirm bool `json:"can_confirm"`
		} `json:"data"`
	}
	if err := json.Unmarshal(saveRec.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if !preview.Data.CanConfirm {
		t.Fatalf("saved failed-check preview cannot confirm: %s", saveRec.Body.String())
	}

	confirmReq := httptest.NewRequest(http.MethodPost, "/api/tasks/"+fixture.task.ID+"/template-fill/confirm", nil)
	confirmRec := httptest.NewRecorder()
	fixture.engine.ServeHTTP(confirmRec, confirmReq)
	if confirmRec.Code != http.StatusOK {
		t.Fatalf("confirm status = %d, body = %s", confirmRec.Code, confirmRec.Body.String())
	}
	var confirmed struct {
		Data model.Task `json:"data"`
	}
	if err := json.Unmarshal(confirmRec.Body.Bytes(), &confirmed); err != nil {
		t.Fatal(err)
	}
	if confirmed.Data.Status != model.TaskStatusTemplateFillChecking {
		t.Fatalf("confirmed status = %q, want template_fill_checking", confirmed.Data.Status)
	}
}

type templateFillRouterFixture struct {
	engine      *gin.Engine
	repo        *repository.Repository
	task        *model.Task
	projectPath string
}

type beautifyRouterFixture struct {
	*templateFillRouterFixture
}

type beautifyRouterPreviewResponse struct {
	Data struct {
		Plan       map[string]any `json:"plan"`
		PlanSHA256 string         `json:"plan_sha256"`
	} `json:"data"`
}

func newBeautifyRouterFixture(t *testing.T) *beautifyRouterFixture {
	t.Helper()
	base := newTemplateFillRouterFixture(t)
	if err := os.RemoveAll(base.projectPath); err != nil {
		t.Fatal(err)
	}
	writeBeautifyRouterPPTX(t, filepath.Join(base.projectPath, "sources", "deck.pptx"))
	writeTemplateFillRouterFile(t, filepath.Join(base.projectPath, "sources", "deck.md"), "# Frozen private source sentence\n")
	writeTemplateFillRouterFile(t, filepath.Join(base.projectPath, "analysis", "deck.identity.json"), `{"canvas":{"width":12192000,"height":6858000}}`+"\n")
	writeTemplateFillRouterFile(t, filepath.Join(base.projectPath, "analysis", "deck.slide_library.json"), `{"slide_count":1,"canvas_px":{"width":1280,"height":720},"slides":[{"slide_index":1}]}`+"\n")
	writeTemplateFillRouterFile(t, filepath.Join(base.projectPath, "analysis", "source_profile.json"), `{"slide_count":1}`+"\n")
	writeTemplateFillRouterFile(t, filepath.Join(base.projectPath, "confirm_ui", "result.json"), `{"page_count":1,"canvas":"ppt169","status":"confirmed"}`+"\n")
	inputs, err := service.BuildBeautifyInputsContract(base.projectPath, base.task.ID, model.RunnerProfileFullPPTMaster)
	if err != nil {
		t.Fatal(err)
	}
	inventory := service.BeautifyInventoryDocument{
		Schema: "beautify_inventory.v1", TaskID: base.task.ID, SourcePPTXSHA256: inputs.SourcePPTX.SHA256,
		SlideCount: 1, Slides: []service.BeautifyInventorySlide{{
			SlideIndex: 1,
			TextBlocks: []service.BeautifyInventoryText{{ID: "text.p01.title", Role: "title", Text: "Frozen private source sentence"}},
			Tables:     []service.BeautifyInventoryTable{}, Charts: []service.BeautifyInventoryChart{}, Images: []service.BeautifyInventoryImage{},
			Ignored: []service.BeautifyContentRef{}, NeedsConfirmation: []service.BeautifyContentRef{},
		}},
	}
	writeBeautifyRouterJSON(t, filepath.Join(base.projectPath, "analysis", "beautify_inventory.json"), inventory)
	inputsSHA := beautifyRouterSHA256(t, filepath.Join(base.projectPath, ".slidesmith", "contracts", "beautify_inputs.json"))
	inventorySHA := beautifyRouterSHA256(t, filepath.Join(base.projectPath, "analysis", "beautify_inventory.json"))
	risk := service.BeautifyRiskReport{
		Schema: "slidesmith.beautify_risk_report.v1", TaskID: base.task.ID,
		InputsSHA256: inputsSHA, InventorySHA256: inventorySHA, Risks: []service.BeautifyRiskFinding{},
	}
	writeBeautifyRouterJSON(t, filepath.Join(base.projectPath, "analysis", "beautify_risk_report.json"), risk)
	if _, err := service.ValidateBeautifyInventoryContract(base.projectPath, base.task.ID); err != nil {
		t.Fatal(err)
	}
	confirmationSHA := beautifyRouterSHA256(t, filepath.Join(base.projectPath, "confirm_ui", "result.json"))
	plan := service.BeautifyPlanDocument{
		Schema: "slidesmith.beautify_plan.v1", TaskID: base.task.ID, Status: "draft", Revision: 1,
		SourcePPTXSHA256: inputs.SourcePPTX.SHA256, InventorySHA256: inventorySHA, ConfirmationSHA256: confirmationSHA,
		SlideCount: 1, Identity: service.BeautifyPlanIdentity{Source: "theme"},
		Slides: []service.BeautifyPlanSlide{{
			SourceSlide: 1, OutputPage: 1, PageRole: "cover", PageRhythm: "anchor",
			LayoutStrategy: "clarify hierarchy without changing content", TextBlockIDs: []string{"text.p01.title"},
			ImageIDs: []string{}, TableIDs: []string{}, ChartIDs: []string{},
			Ignored: []service.BeautifyContentRef{}, Unsupported: []service.BeautifyContentRef{}, Risks: []string{},
		}},
		GlobalIgnored: []service.BeautifyContentRef{}, AcceptedRisks: []string{},
	}
	writeBeautifyRouterJSON(t, filepath.Join(base.projectPath, "analysis", "beautify_plan.json"), plan)
	if _, err := service.ValidateBeautifyPlanContract(base.projectPath, base.task.ID); err != nil {
		t.Fatal(err)
	}
	lockedAt := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	base.task.Route = model.TaskRouteBeautify
	base.task.Status = model.TaskStatusAwaitingBeautifyConfirm
	base.task.RunnerProfile = model.RunnerProfileFullPPTMaster
	base.task.RunnerProfileSource = model.RunnerProfileSourceExplicitConfig
	base.task.RunnerProfileLockedAt = &lockedAt
	if err := base.repo.SaveTask(context.Background(), base.task); err != nil {
		t.Fatal(err)
	}
	return &beautifyRouterFixture{templateFillRouterFixture: base}
}

func getBeautifyRouterPreview(t *testing.T, fixture *beautifyRouterFixture) beautifyRouterPreviewResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	fixture.engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tasks/"+fixture.task.ID+"/beautify-plan", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("preview status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var response beautifyRouterPreviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func writeBeautifyRouterJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeTemplateFillRouterFile(t, path, string(raw)+"\n")
}

func beautifyRouterSHA256(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func writeBeautifyRouterPPTX(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	entries := map[string]string{
		"ppt/presentation.xml":  `<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"><p:sldSz cx="12192000" cy="6858000"/></p:presentation>`,
		"ppt/slides/slide1.xml": `<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"/>`,
	}
	for name, content := range entries {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func newTemplateFillRouterFixture(t *testing.T) *templateFillRouterFixture {
	t.Helper()
	tmp := t.TempDir()
	db, err := gorm.Open(sqlite.Open(filepath.Join(tmp, "router.sqlite")), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&model.Task{},
		&model.TaskEvent{},
		&model.Artifact{},
		&model.TaskRuntimeRun{},
		&model.TaskPhaseRun{},
		&model.TaskConfirmation{},
	); err != nil {
		t.Fatal(err)
	}
	repo := repository.New(db)
	workspaceRoot := filepath.Join(tmp, "workspaces")
	runtimeProject := "task_router_template_fill"
	workspacePath := filepath.Join(workspaceRoot, runtimeProject)
	projectPath := filepath.Join(workspacePath, "projects", runtimeProject+"_ppt169_20260713")
	writeTemplateFillRouterFile(t, filepath.Join(projectPath, "sources", "brand.pptx"), "pptx\n")
	writeTemplateFillRouterFile(t, filepath.Join(projectPath, "sources", "content.md"), "# Content\n")
	writeTemplateFillRouterFile(t, filepath.Join(projectPath, "analysis", "brand.slide_library.json"), `{"slides":[{"slide_index":1}]}`+"\n")
	writeTemplateFillRouterFile(t, filepath.Join(projectPath, "analysis", "fill_plan.json"), string(mustTemplateFillRouterJSON(t, templateFillRouterPlan("draft")))+"\n")
	task := &model.Task{
		ID:                   "task-router-template-fill",
		Title:                "Template Fill router test",
		Status:               model.TaskStatusAwaitingTemplateFillConfirm,
		Route:                model.TaskRouteTemplateFill,
		RuntimeProject:       runtimeProject,
		RuntimeWorkspacePath: workspacePath,
	}
	if err := repo.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	storage := service.NewLocalStorage(filepath.Join(tmp, "storage"))
	agent := &templateFillRouterAgent{
		projectPath: projectPath,
		sessionRoot: filepath.Join(tmp, "agent-sessions"),
	}
	tasks := service.NewTaskService(
		repo,
		storage,
		agent,
		service.NewRuntimeWorkspacePublisher(storage),
		config.AgentComposeConfig{Enabled: true, WorkspaceRoot: workspaceRoot, Agent: "ppt_master"},
	)
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	router.Register(engine, router.Handlers{Tasks: handler.NewTaskHandler(tasks)})
	return &templateFillRouterFixture{engine: engine, repo: repo, task: task, projectPath: projectPath}
}

type templateFillRouterAgent struct {
	projectPath string
	sessionRoot string
}

func (*templateFillRouterAgent) Up(context.Context, service.AgentRunRequest) error { return nil }

func (agent *templateFillRouterAgent) Run(ctx context.Context, req service.AgentRunRequest) (*service.AgentRunResult, error) {
	sessionWorkspace := filepath.Join(agent.sessionRoot, "session", "workspace")
	if err := copyTemplateFillRouterTree(ctx, req.WorkDir, sessionWorkspace); err != nil {
		return nil, err
	}
	projectRel, err := filepath.Rel(req.WorkDir, agent.projectPath)
	if err != nil {
		return nil, err
	}
	writeTemplateFillRouterFileNoTest(filepath.Join(sessionWorkspace, projectRel, "analysis", "check_report.json"), `{
  "schema": "template_fill_pptx_check.v1",
  "summary": {"ok": 1, "warn": 0, "error": 0},
  "results": []
}`+"\n")
	return &service.AgentRunResult{
		RunID:         "router-check-run",
		SessionID:     "router-check-session",
		Status:        "succeeded",
		WorkspacePath: sessionWorkspace,
	}, nil
}

func templateFillRouterPlan(status string) map[string]any {
	return map[string]any{
		"schema":            "template_fill_pptx_plan.v1",
		"status":            status,
		"source_pptx":       "sources/brand.pptx",
		"accepted_warnings": []any{},
		"slides": []any{map[string]any{
			"source_slide": 1,
			"purpose":      "cover",
			"layout_rationale": map[string]any{
				"layout_pattern": "hero",
				"why_fit":        "fits",
				"risk":           "short",
			},
			"replacements": []any{},
			"table_edits":  []any{},
			"chart_edits":  []any{},
		}},
	}
}

func mustTemplateFillRouterJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func writeTemplateFillRouterFile(t *testing.T, path, content string) {
	t.Helper()
	if err := writeTemplateFillRouterFileNoTest(path, content); err != nil {
		t.Fatal(err)
	}
}

func writeTemplateFillRouterFileNoTest(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func copyTemplateFillRouterTree(ctx context.Context, source, target string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, rel)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		return os.WriteFile(destination, raw, 0o644)
	})
}
