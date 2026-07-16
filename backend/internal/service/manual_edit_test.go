package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/slidesmith/slidesmith/backend/internal/config"
	"github.com/slidesmith/slidesmith/backend/internal/model"
	"github.com/slidesmith/slidesmith/backend/internal/repository"
	"gorm.io/gorm"
)

type manualEditFixture struct {
	service *TaskService
	repo    *repository.Repository
	storage *LocalStorage
	task    *model.Task
	version *model.TaskArtifactVersion
}

type localManualEditAgent struct {
	upCalls  int
	runCalls int
}

func (agent *localManualEditAgent) Up(context.Context, AgentRunRequest) error {
	agent.upCalls++
	return nil
}

func (agent *localManualEditAgent) Run(ctx context.Context, req AgentRunRequest) (*AgentRunResult, error) {
	agent.runCalls++
	if agent.upCalls < agent.runCalls {
		return &AgentRunResult{Status: "failed"}, errors.New("test agent run called before up")
	}
	if req.Command == "" {
		return &AgentRunResult{Status: "failed"}, errors.New("test agent requires command")
	}
	command := exec.CommandContext(ctx, "sh", "-c", req.Command)
	command.Dir = req.WorkDir
	output, err := command.CombinedOutput()
	status, code := "succeeded", 0
	if err != nil {
		status, code = "failed", 1
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		}
	}
	return &AgentRunResult{RunID: "local-run", Status: status, ExitCode: &code, RawJSON: string(output), ErrorMessage: string(output)}, err
}

func newManualEditFixture(t *testing.T, enabled bool) *manualEditFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "edit.sqlite")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Task{}, &model.TaskEvent{}, &model.Artifact{}, &model.TaskArtifactVersion{}, &model.TaskEditSession{}, &model.TaskEditRun{}, &model.TaskPhaseRun{}); err != nil {
		t.Fatal(err)
	}
	repo := repository.New(db)
	storage := NewLocalStorage(filepath.Join(t.TempDir(), "storage"))
	now := time.Now().UTC()
	task := &model.Task{ID: "task-edit", Title: "Edit", Status: model.TaskStatusCompleted, Route: model.TaskRouteMain, RunnerProfile: model.RunnerProfileFullPPTMaster, RunnerProfileSource: model.RunnerProfileSourceExplicitConfig, RunnerProfileLockedAt: &now, RuntimeProject: "task-edit"}
	if err := repo.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	svg := `<svg xmlns="http://www.w3.org/2000/svg" width="1280" height="720" viewBox="0 0 1280 720"><rect id="bg" x="0" y="0" width="1280" height="720" fill="#fff"/><text x="100" y="120" fill="#000">Old title</text></svg>`
	svgSum := sha256.Sum256([]byte(svg))
	inventory := map[string]any{
		"schema": svgInventorySchema, "task_id": task.ID, "runner_profile": model.RunnerProfileFullPPTMaster, "canvas": "ppt169", "page_count": 1,
		"pages":   []map[string]any{{"page_id": "P01", "spec_page_id": "P01", "page": 1, "path": "svg_output/01_title.svg", "sha256": hex.EncodeToString(svgSum[:]), "width": 1280, "height": 720, "view_box": []float64{0, 0, 1280, 720}, "element_count": 2, "text_count": 1, "image_count": 0, "use_count": 0, "chart_count": 0, "formula_count": 0, "resource_ids": []string{}, "element_ids": []string{"bg", "title"}, "warnings": []string{}}},
		"summary": map[string]any{"pages": 1, "elements": 2, "texts": 1, "images": 0, "charts": 0, "formulas": 0}, "resource_summary": map[string]int{}, "chart_summary": map[string]int{},
	}
	inventoryRaw, _ := json.Marshal(inventory)
	resourceUsageRaw, _ := json.Marshal(map[string]any{
		"schema": svgResourceUsageSchema, "resources_manifest_sha256": "legacy-missing-manifest",
		"pages": []map[string]any{{"page_id": "P01", "svg": "svg_output/01_title.svg", "svg_sha256": hex.EncodeToString(svgSum[:]), "resources": []any{}}},
	})
	chartUsageRaw, _ := json.Marshal(map[string]any{
		"schema": chartUsageSchema, "resources_manifest_sha256": "legacy-missing-manifest",
		"pages":  []map[string]any{{"page_id": "P01", "svg": "svg_output/01_title.svg", "svg_sha256": hex.EncodeToString(svgSum[:])}},
		"charts": []any{},
	})
	files := []struct{ rel, kind, content string }{
		{"design_spec.md", model.ArtifactKindDesignSpec, "# spec\n"}, {"spec_lock.md", model.ArtifactKindSpecLock, "# lock\n"},
		{"svg_output/01_title.svg", model.ArtifactKindSVGOutput, svg}, {"svg_final/01_title.svg", model.ArtifactKindSVGFinal, svg},
		{"analysis/svg_inventory.json", model.ArtifactKindSVGInventory, string(inventoryRaw)},
		{"analysis/svg_resource_usage.json", model.ArtifactKindSVGResourceUsage, string(resourceUsageRaw)},
		{"analysis/chart_usage.json", model.ArtifactKindChartUsage, string(chartUsageRaw)},
		{"validation/quality_summary.json", model.ArtifactKindQualitySummary, `{"passed":true}`},
		{"exports/deck.pptx", model.ArtifactKindPPTX, "fake-pptx"},
	}
	artifacts := []model.Artifact{}
	for _, file := range files {
		source := filepath.Join(t.TempDir(), filepath.Base(file.rel))
		if err := os.WriteFile(source, []byte(file.content), 0o644); err != nil {
			t.Fatal(err)
		}
		objectKey := filepath.ToSlash(filepath.Join("tasks", task.ID, "artifacts", "v1", file.rel))
		stored, err := storage.CopyFileToObject(context.Background(), objectKey, source)
		if err != nil {
			t.Fatal(err)
		}
		artifact := model.Artifact{TaskID: task.ID, Kind: file.kind, Name: filepath.Base(file.rel), Storage: "local", ObjectKey: stored.ObjectKey, MimeType: stored.MimeType, Size: stored.Size, SHA256: stored.SHA256, PublishVersion: "v1"}
		if err := repo.CreateArtifact(context.Background(), &artifact); err != nil {
			t.Fatal(err)
		}
		artifacts = append(artifacts, artifact)
	}
	digest, err := artifactManifestDigest(artifacts)
	if err != nil {
		t.Fatal(err)
	}
	activated := now.Add(time.Minute)
	version := &model.TaskArtifactVersion{TaskID: task.ID, Version: "v1", Source: model.ArtifactVersionSourceGeneration, ArtifactManifestSHA256: digest, PPTXArtifactID: publishedPPTXArtifactID(artifacts), ActivatedAt: &activated}
	if err := repo.UpsertActiveArtifactVersion(context.Background(), version); err != nil {
		t.Fatal(err)
	}
	cfg := config.AgentComposeConfig{LivePreviewEditEnabled: enabled, LivePreviewAnnotationEnabled: false, LivePreviewMaxActiveSessions: 1, WorkspaceRoot: t.TempDir()}
	service := NewTaskService(repo, storage, nil, NewRuntimeWorkspacePublisher(storage), cfg)
	return &manualEditFixture{service: service, repo: repo, storage: storage, task: task, version: version}
}

func TestCreateEditSessionRequiresFeatureAndCompletedMainFullProfile(t *testing.T) {
	disabled := newManualEditFixture(t, false)
	if _, err := disabled.service.CreateEditSession(context.Background(), disabled.task.ID, "v1"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("disabled CreateEditSession error=%v", err)
	}
	enabled := newManualEditFixture(t, true)
	session, err := enabled.service.CreateEditSession(context.Background(), enabled.task.ID, "v1")
	if err != nil {
		t.Fatal(err)
	}
	if session.Status != model.EditSessionStatusDraft || session.Revision != 1 || session.BaseArtifactManifestSHA256 != enabled.version.ArtifactManifestSHA256 {
		t.Fatalf("session=%#v", session)
	}
	if _, err := enabled.service.CreateEditSession(context.Background(), enabled.task.ID, "v1"); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("second session error=%v", err)
	}
}

func TestEditPageSnapshotAndDraftCASFreeze(t *testing.T) {
	fixture := newManualEditFixture(t, true)
	session, err := fixture.service.CreateEditSession(context.Background(), fixture.task.ID, "v1")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := fixture.service.GetEditSessionPage(context.Background(), fixture.task.ID, session.ID, "P01")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snapshot.SVG, `data-editor-selectable="true"`) || len(snapshot.Elements) != 2 {
		t.Fatalf("snapshot=%#v svg=%s", snapshot, snapshot.SVG)
	}
	var title ManualEditSnapshotElement
	for _, element := range snapshot.Elements {
		if element.Text == "Old title" {
			title = element
		}
	}
	draft := map[string]any{"schema": manualEditDraftSchema, "pages": []any{map[string]any{"page_id": "P01", "operations": []any{map[string]any{"operation_id": "op-1", "type": "set_text", "target": map[string]any{"element_id": title.ElementID, "tag": "text", "element_fingerprint": title.ElementFingerprint}, "value": map[string]any{"text": "新标题"}, "unsafe_attr": "ignored"}}}}, "annotations": []any{}}
	raw, _ := json.Marshal(draft)
	updated, err := fixture.service.SaveEditSessionDraft(context.Background(), fixture.task.ID, session.ID, 1, raw)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 || strings.Contains(updated.DraftJSON, "unsafe_attr") {
		t.Fatalf("updated draft=%#v", updated)
	}
	if _, err := fixture.service.SaveEditSessionDraft(context.Background(), fixture.task.ID, session.ID, 1, raw); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("stale save error=%v", err)
	}
	frozen, err := fixture.service.ApplyEditSession(context.Background(), fixture.task.ID, session.ID, 2, updated.DraftSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if frozen.Status != model.EditSessionStatusQueued || frozen.FrozenPatchSHA256 != updated.DraftSHA256 {
		t.Fatalf("frozen=%#v", frozen)
	}
	persistedTask, err := fixture.repo.GetTask(context.Background(), fixture.task.ID)
	if err != nil || persistedTask.Status != model.TaskStatusCompleted {
		t.Fatalf("parent task=%#v err=%v", persistedTask, err)
	}
}

func TestSafeSVGSnapshotRejectsScriptDuplicateIDAndExternalHref(t *testing.T) {
	tests := []string{
		`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><rect id="x"/><text id="x">x</text></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><image href="https://example.com/a.png"/></svg>`,
	}
	for _, raw := range tests {
		if _, _, _, err := buildSafeSVGSnapshot([]byte(raw), "svg_output/01.svg", map[string]model.Artifact{}, NewLocalStorage(t.TempDir())); err == nil {
			t.Fatalf("unsafe snapshot accepted: %s", raw)
		}
	}
}

func TestApplyEditSessionRejectsEmptyDraft(t *testing.T) {
	fixture := newManualEditFixture(t, true)
	session, err := fixture.service.CreateEditSession(context.Background(), fixture.task.ID, "v1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.ApplyEditSession(context.Background(), fixture.task.ID, session.ID, session.Revision, session.DraftSHA256); !errors.Is(err, ErrUnprocessable) {
		t.Fatalf("empty apply error=%v", err)
	}
}

func TestMaterializeAndDirectRunnerUseVersionArtifactsAndSnapshotFingerprint(t *testing.T) {
	fixture := newManualEditFixture(t, true)
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(t.TempDir(), "ppt-master")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	agent := &localManualEditAgent{}
	fixture.service.agent = agent
	fixture.service.agentCfg.Enabled = true
	fixture.service.agentCfg.WorkDir = filepath.Join(repoRoot, "runtime", "ppt-master-agent")
	fixture.service.agentCfg.PPTMasterSkillDir = skillDir
	fixture.service.agentCfg.WorkspaceRoot = t.TempDir()
	fixture.service.workspaces = NewRuntimeWorkspaceBuilder(fixture.service.agentCfg, fixture.storage)

	session, err := fixture.service.CreateEditSession(context.Background(), fixture.task.ID, "v1")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := fixture.service.GetEditSessionPage(context.Background(), fixture.task.ID, session.ID, "P01")
	if err != nil {
		t.Fatal(err)
	}
	var title ManualEditSnapshotElement
	for _, element := range snapshot.Elements {
		if element.Text == "Old title" {
			title = element
		}
	}
	draft := ManualEditDraft{Schema: manualEditDraftSchema, Pages: []ManualEditPage{{PageID: "P01", Operations: []ManualEditOperation{{OperationID: "op-1", Type: "set_text", Target: ManualEditTarget{ElementID: title.ElementID, Tag: "text", ElementFingerprint: title.ElementFingerprint}, Value: map[string]any{"text": "Edited title"}}}}}, Annotations: []ManualEditAnnotation{}}
	raw, _ := json.Marshal(draft)
	session, err = fixture.service.SaveEditSessionDraft(context.Background(), fixture.task.ID, session.ID, 1, raw)
	if err != nil {
		t.Fatal(err)
	}
	session, err = fixture.service.ApplyEditSession(context.Background(), fixture.task.ID, session.ID, session.Revision, session.DraftSHA256)
	if err != nil {
		t.Fatal(err)
	}
	claimedAt := time.Now().UTC()
	claimed, err := fixture.repo.ClaimEditSession(context.Background(), fixture.task.ID, session.ID, "test-claim", claimedAt, claimedAt.Add(-time.Hour))
	if err != nil || !claimed {
		t.Fatalf("claim=%v err=%v", claimed, err)
	}
	session, err = fixture.repo.GetEditSession(context.Background(), fixture.task.ID, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	session.ExecutionClaimToken = "test-claim"
	session.Status = model.EditSessionStatusMaterializing
	workspace := fixture.service.editWorkspaceFor(fixture.task, session)
	if _, err := fixture.service.materializeEditWorkspace(context.Background(), fixture.task, session, workspace, &model.TaskEditRun{ID: "materialize"}); err != nil {
		t.Fatal(err)
	}
	preserveSession := *session
	preserveSession.DraftJSON = `{"schema":"slidesmith.manual_edit_draft.v1","pages":[],"annotations":[]}`
	backupRoot, preserved, err := fixture.service.stageUntouchedManualEditFinals(context.Background(), fixture.task, &preserveSession, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(preserved) != 1 {
		t.Fatalf("preserved=%#v", preserved)
	}
	finalPath := filepath.Join(workspace.Project, "svg_final", "01_title.svg")
	if err := os.WriteFile(finalPath, []byte("drift"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := restoreUntouchedManualEditFinals(workspace, backupRoot, preserved); err != nil {
		t.Fatal(err)
	}
	_ = os.RemoveAll(backupRoot)
	restoredSHA, err := sha256File(finalPath)
	if err != nil || restoredSHA != preserved["svg_final/01_title.svg"] {
		t.Fatalf("restored_sha=%s expected=%s err=%v", restoredSHA, preserved["svg_final/01_title.svg"], err)
	}
	if _, err := fixture.service.runDirectEditPhase(context.Background(), fixture.task, session, workspace, &model.TaskEditRun{ID: "direct"}); err != nil {
		t.Fatal(err)
	}
	if agent.upCalls != 1 || agent.runCalls != 1 {
		t.Fatalf("runtime calls: up=%d run=%d", agent.upCalls, agent.runCalls)
	}
	compatibility, err := prepareManualEditSVGValidationInputs(fixture.task, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if !valueBool(compatibility, "resources_manifest_synthesized", false) {
		t.Fatalf("compatibility=%#v", compatibility)
	}
	if !valueBool(compatibility, "confirmation_synthesized", false) {
		t.Fatalf("compatibility=%#v", compatibility)
	}
	var confirmation map[string]any
	if err := readManualEditJSON(filepath.Join(workspace.Project, "confirm_ui", "result.json"), &confirmation); err != nil {
		t.Fatal(err)
	}
	if valueString(confirmation, "canvas", "") != "ppt169" || int(valueNumber(confirmation["page_count"])) != 1 {
		t.Fatalf("confirmation=%#v", confirmation)
	}
	manifestSHA, err := sha256File(filepath.Join(workspace.Project, ".slidesmith", "resources_manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rebound svgResourceUsageDocument
	if err := readManualEditJSON(filepath.Join(workspace.Project, "analysis", "svg_resource_usage.json"), &rebound); err != nil {
		t.Fatal(err)
	}
	editedSHA, err := sha256File(filepath.Join(workspace.Project, "svg_output", "01_title.svg"))
	if err != nil {
		t.Fatal(err)
	}
	if rebound.ResourcesManifestSHA256 != manifestSHA || len(rebound.Pages) != 1 || rebound.Pages[0].SVGsha256 != editedSHA {
		t.Fatalf("rebound usage=%#v manifest_sha=%s edited_sha=%s", rebound, manifestSHA, editedSHA)
	}
	rawSVG, err := os.ReadFile(filepath.Join(workspace.Project, "svg_output", "01_title.svg"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rawSVG), "Edited title") {
		t.Fatalf("edited SVG=%s", rawSVG)
	}
	persistedTask, err := fixture.repo.GetTask(context.Background(), fixture.task.ID)
	if err != nil || persistedTask.Status != model.TaskStatusCompleted {
		t.Fatalf("parent task=%#v err=%v", persistedTask, err)
	}
}

func TestSyncEditRuntimeProjectCreatesStagingParent(t *testing.T) {
	fixture := newManualEditFixture(t, true)
	session, err := fixture.service.CreateEditSession(context.Background(), fixture.task.ID, "v1")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := fixture.service.GetEditSessionPage(context.Background(), fixture.task.ID, session.ID, "P01")
	if err != nil {
		t.Fatal(err)
	}
	var title ManualEditSnapshotElement
	for _, element := range snapshot.Elements {
		if element.Text == "Old title" {
			title = element
		}
	}
	draft := ManualEditDraft{Schema: manualEditDraftSchema, Pages: []ManualEditPage{{PageID: "P01", Operations: []ManualEditOperation{{OperationID: "sync-op", Type: "set_text", Target: ManualEditTarget{ElementID: title.ElementID, Tag: title.Tag, ElementFingerprint: title.ElementFingerprint}, Value: map[string]any{"text": "new"}}}}}, Annotations: []ManualEditAnnotation{}}
	raw, _ := json.Marshal(draft)
	session, err = fixture.service.SaveEditSessionDraft(context.Background(), fixture.task.ID, session.ID, session.Revision, raw)
	if err != nil {
		t.Fatal(err)
	}
	session, err = fixture.service.ApplyEditSession(context.Background(), fixture.task.ID, session.ID, session.Revision, session.DraftSHA256)
	if err != nil {
		t.Fatal(err)
	}
	claimedAt := time.Now().UTC()
	claimed, err := fixture.repo.ClaimEditSession(context.Background(), fixture.task.ID, session.ID, "sync-claim", claimedAt, claimedAt.Add(-time.Hour))
	if err != nil || !claimed {
		t.Fatalf("claim=%v err=%v", claimed, err)
	}
	session, err = fixture.repo.GetEditSession(context.Background(), fixture.task.ID, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	session.ExecutionClaimToken = "sync-claim"
	workspace := fixture.service.editWorkspaceFor(fixture.task, session)
	if err := os.MkdirAll(workspace.Project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace.Project, "marker.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	runtimeRoot := t.TempDir()
	runtimeProject := filepath.Join(runtimeRoot, "projects", workspace.ProjectName)
	if err := os.MkdirAll(runtimeProject, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimeProject, "marker.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.syncEditRuntimeProject(context.Background(), session, workspace, runtimeRoot); err != nil {
		t.Fatal(err)
	}
	marker, err := os.ReadFile(filepath.Join(workspace.Project, "marker.txt"))
	if err != nil || string(marker) != "new" {
		t.Fatalf("marker=%q err=%v", marker, err)
	}
}
