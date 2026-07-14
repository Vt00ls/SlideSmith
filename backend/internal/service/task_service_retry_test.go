package service

import (
	"context"
	"errors"
	"os"
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

func TestNormalizeRetryPhaseSupportsSplitPhases(t *testing.T) {
	tests := []struct {
		name         string
		requested    string
		failurePhase string
		want         string
	}{
		{name: "legacy generate alias", requested: "generate", want: retryPhaseSpecGenerate},
		{name: "svg explicit", requested: "svg_execute", want: retryPhaseSVGExecute},
		{name: "quality explicit", requested: "quality_check", want: retryPhaseQualityCheck},
		{name: "export explicit", requested: "finalize_export", want: retryPhaseFinalizeExport},
		{name: "auto svg failure", requested: "auto", failurePhase: "svg_execute.contract", want: retryPhaseSVGExecute},
		{name: "auto quality failure", requested: "", failurePhase: "quality_check.command", want: retryPhaseQualityCheck},
		{name: "auto template resolve failure", requested: "auto", failurePhase: string(PhaseTemplateResolve), want: retryPhasePrepare},
		{name: "omitted template resolve failure", requested: "", failurePhase: string(PhaseTemplateResolve), want: retryPhasePrepare},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeRetryPhase(tt.requested, tt.failurePhase)
			if err != nil {
				t.Fatalf("normalizeRetryPhase() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("normalizeRetryPhase() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeRetryPhaseTemplateFillAliases(t *testing.T) {
	tests := []struct {
		name      string
		requested string
		failure   string
		want      string
	}{
		{name: "plan canonical", requested: "template_fill_plan", want: retryPhaseTemplateFillPlan},
		{name: "plan fill alias", requested: "fill_plan", want: retryPhaseTemplateFillPlan},
		{name: "plan short alias", requested: "plan", want: retryPhaseTemplateFillPlan},
		{name: "plan status alias", requested: "template_fill_planning", want: retryPhaseTemplateFillPlan},
		{name: "check canonical", requested: "template_fill_check", want: retryPhaseTemplateFillCheck},
		{name: "check fill alias", requested: "fill_check", want: retryPhaseTemplateFillCheck},
		{name: "check short alias", requested: "check", want: retryPhaseTemplateFillCheck},
		{name: "check status alias", requested: "template_fill_checking", want: retryPhaseTemplateFillCheck},
		{name: "apply canonical", requested: "template_fill_apply", want: retryPhaseTemplateFillApply},
		{name: "apply fill alias", requested: "fill_apply", want: retryPhaseTemplateFillApply},
		{name: "apply short alias", requested: "apply", want: retryPhaseTemplateFillApply},
		{name: "apply status alias", requested: "template_fill_applying", want: retryPhaseTemplateFillApply},
		{name: "validate canonical", requested: "template_fill_validate", want: retryPhaseTemplateFillValidate},
		{name: "validate fill alias", requested: "fill_validate", want: retryPhaseTemplateFillValidate},
		{name: "validate short alias", requested: "validate", want: retryPhaseTemplateFillValidate},
		{name: "validate status alias", requested: "template_fill_validating", want: retryPhaseTemplateFillValidate},
		{name: "publish canonical", requested: "publish", want: retryPhasePublish},
		{name: "publish status alias", requested: "publishing", want: retryPhasePublish},
		{name: "publish artifact alias", requested: "artifact_publish", want: retryPhasePublish},
		{name: "auto plan", requested: "auto", failure: "template_fill_plan.contract", want: retryPhaseTemplateFillPlan},
		{name: "omitted check", failure: "template_fill_check.command", want: retryPhaseTemplateFillCheck},
		{name: "auto apply", requested: "auto", failure: "template_fill_apply.contract", want: retryPhaseTemplateFillApply},
		{name: "auto validate", requested: "auto", failure: "template_fill_validate.command", want: retryPhaseTemplateFillValidate},
		{name: "auto publish", requested: "auto", failure: "publish.contract", want: retryPhasePublish},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeRetryPhase(test.requested, test.failure)
			if err != nil {
				t.Fatalf("normalizeRetryPhase(%q, %q) error = %v", test.requested, test.failure, err)
			}
			if got != test.want {
				t.Fatalf("normalizeRetryPhase(%q, %q) = %q, want %q", test.requested, test.failure, got, test.want)
			}
		})
	}
}

func TestRetryTemplateFillPhaseCleanupMatrixAndStatuses(t *testing.T) {
	tests := []struct {
		name       string
		phase      string
		wantStatus string
		removed    []string
	}{
		{
			name:       "plan",
			phase:      string(PhaseTemplateFillPlan),
			wantStatus: model.TaskStatusTemplateFillPlanning,
			removed: []string{
				"analysis/fill_plan.json",
				"analysis/check_report.json",
				".slidesmith/contracts/template_fill_plan.json",
				".slidesmith/contracts/template_fill_check.json",
				".slidesmith/contracts/template_fill_apply.json",
				".slidesmith/contracts/template_fill_validate.json",
				".slidesmith/contracts/publish.json",
				".slidesmith/contracts/final.json",
				"exports/result.pptx",
				"validation/validate_report.json",
				"validation/readback.md",
			},
		},
		{
			name:       "check",
			phase:      string(PhaseTemplateFillCheck),
			wantStatus: model.TaskStatusTemplateFillChecking,
			removed: []string{
				"analysis/check_report.json",
				".slidesmith/contracts/template_fill_check.json",
				".slidesmith/contracts/template_fill_apply.json",
				".slidesmith/contracts/template_fill_validate.json",
				".slidesmith/contracts/publish.json",
				".slidesmith/contracts/final.json",
				"exports/result.pptx",
				"validation/validate_report.json",
				"validation/readback.md",
			},
		},
		{
			name:       "apply",
			phase:      string(PhaseTemplateFillApply),
			wantStatus: model.TaskStatusTemplateFillApplying,
			removed: []string{
				".slidesmith/contracts/template_fill_apply.json",
				".slidesmith/contracts/template_fill_validate.json",
				".slidesmith/contracts/publish.json",
				".slidesmith/contracts/final.json",
				"exports/result.pptx",
				"validation/validate_report.json",
				"validation/readback.md",
			},
		},
		{
			name:       "validate",
			phase:      string(PhaseTemplateFillValidate),
			wantStatus: model.TaskStatusTemplateFillValidating,
			removed: []string{
				".slidesmith/contracts/template_fill_validate.json",
				".slidesmith/contracts/publish.json",
				".slidesmith/contracts/final.json",
				"validation/validate_report.json",
				"validation/readback.md",
			},
		},
		{
			name:       "publish",
			phase:      string(PhasePublish),
			wantStatus: model.TaskStatusPublishing,
			removed: []string{
				".slidesmith/contracts/publish.json",
				".slidesmith/contracts/final.json",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusFailed, nil)
			task.FailurePhase = test.phase + ".contract"
			task.ErrorMessage = "phase failed"
			task.FailureMetadata = `{"phase":"` + test.phase + `.contract"}`
			if err := repo.SaveTask(context.Background(), task); err != nil {
				t.Fatal(err)
			}
			allPaths := writeTemplateFillRetryEvidence(projectPath)
			removed := retryRelativePathSet(test.removed)

			updated, err := service.RetryTask(context.Background(), task.ID, test.phase)
			if err != nil {
				t.Fatalf("RetryTask(%q) error = %v", test.phase, err)
			}
			if updated.Status != test.wantStatus {
				t.Fatalf("status = %q, want %q", updated.Status, test.wantStatus)
			}
			for _, relativePath := range allPaths {
				path := filepath.Join(projectPath, filepath.FromSlash(relativePath))
				if removed[relativePath] {
					assertPathMissing(t, path)
				} else {
					assertPathExists(t, path)
				}
			}
			persisted, err := repo.GetTask(context.Background(), task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if persisted.Status != test.wantStatus {
				t.Fatalf("persisted status = %q, want %q", persisted.Status, test.wantStatus)
			}
			if persisted.FailurePhase != "" || persisted.ErrorMessage != "" || persisted.FailureMetadata != "{}" {
				t.Fatalf("failure fields not cleared: phase=%q error=%q metadata=%q", persisted.FailurePhase, persisted.ErrorMessage, persisted.FailureMetadata)
			}
			if persisted.ExecutionClaimToken != "" || persisted.ExecutionClaimedAt != nil {
				t.Fatalf("retry leaked execution claim: token=%q claimed_at=%v", persisted.ExecutionClaimToken, persisted.ExecutionClaimedAt)
			}
		})
	}
}

func TestRetryTemplateFillPhasesRejectOtherRoutesWithoutMutation(t *testing.T) {
	for _, route := range []string{model.TaskRouteMain, model.TaskRouteBeautify} {
		t.Run(route, func(t *testing.T) {
			service, repo, task, projectPath := retryTestService(t)
			task.Route = route
			if err := repo.SaveTask(context.Background(), task); err != nil {
				t.Fatal(err)
			}
			writeRuntimeProfileManifestForTest(t, service.agentCfg.WorkspaceRoot, task)
			mustWriteRetryProjectFiles(projectPath)
			preserved := filepath.Join(projectPath, "exports", "stale.pptx")

			if _, err := service.RetryTask(context.Background(), task.ID, string(PhaseTemplateFillApply)); err == nil || !strings.Contains(err.Error(), "route") {
				t.Fatalf("RetryTask() error = %v, want route rejection", err)
			}
			assertPathExists(t, preserved)
			persisted, err := repo.GetTask(context.Background(), task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if persisted.Status != model.TaskStatusFailed || persisted.FailurePhase != task.FailurePhase {
				t.Fatalf("rejected retry mutated task = %#v", persisted)
			}
		})
	}
}

func TestRetryTemplateFillRouteRejectsMainPipelinePhaseWithoutMutation(t *testing.T) {
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusFailed, nil)
	task.FailurePhase = "quality_check.command"
	task.ErrorMessage = "wrong pipeline"
	if err := repo.SaveTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	allPaths := writeTemplateFillRetryEvidence(projectPath)

	if _, err := service.RetryTask(context.Background(), task.ID, string(PhaseQualityCheck)); err == nil || !strings.Contains(err.Error(), "route") {
		t.Fatalf("RetryTask() error = %v, want route rejection", err)
	}
	for _, relativePath := range allPaths {
		assertPathExists(t, filepath.Join(projectPath, filepath.FromSlash(relativePath)))
	}
	persisted, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.TaskStatusFailed || persisted.FailurePhase != "quality_check.command" {
		t.Fatalf("rejected retry mutated task = %#v", persisted)
	}
}

func TestRetryMainAndBeautifyQualityRecoveryRemainsUnchanged(t *testing.T) {
	for _, route := range []string{model.TaskRouteMain, model.TaskRouteBeautify} {
		t.Run(route, func(t *testing.T) {
			service, repo, task, projectPath := retryTestService(t)
			task.Route = route
			if err := repo.SaveTask(context.Background(), task); err != nil {
				t.Fatal(err)
			}
			writeRuntimeProfileManifestForTest(t, service.agentCfg.WorkspaceRoot, task)
			mustWriteRetryProjectFiles(projectPath)

			updated, err := service.RetryTask(context.Background(), task.ID, string(PhaseQualityCheck))
			if err != nil {
				t.Fatalf("RetryTask() error = %v", err)
			}
			if updated.Status != model.TaskStatusQualityChecking {
				t.Fatalf("status = %q, want %q", updated.Status, model.TaskStatusQualityChecking)
			}
			assertPathExists(t, filepath.Join(projectPath, "svg_output", "01_page_01.svg"))
			assertPathMissing(t, filepath.Join(projectPath, "exports"))
		})
	}
}

func TestRetryTemplateFillCleanupDoesNotFollowSymlinks(t *testing.T) {
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusFailed, nil)
	task.FailurePhase = "template_fill_apply.command"
	task.ErrorMessage = "apply failed"
	if err := repo.SaveTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	allPaths := writeTemplateFillRetryEvidence(projectPath)
	exportsPath := filepath.Join(projectPath, "exports")
	if err := os.RemoveAll(exportsPath); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	outsideSentinel := filepath.Join(outside, "outside.pptx")
	if err := os.WriteFile(outsideSentinel, []byte("outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, exportsPath); err != nil {
		t.Fatal(err)
	}

	if _, err := service.RetryTask(context.Background(), task.ID, string(PhaseTemplateFillApply)); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("RetryTask() error = %v, want no-follow rejection", err)
	}
	assertPathExists(t, outsideSentinel)
	if info, err := os.Lstat(exportsPath); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("unsafe symlink changed: info=%v err=%v", info, err)
	}
	for _, relativePath := range allPaths {
		if relativePath == "exports/result.pptx" {
			continue
		}
		assertPathExists(t, filepath.Join(projectPath, filepath.FromSlash(relativePath)))
	}
	persisted, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.TaskStatusFailed || persisted.ExecutionClaimToken != "" || persisted.ExecutionClaimedAt != nil {
		t.Fatalf("failed no-follow retry mutated task or leaked claim = %#v", persisted)
	}
}

func TestRetryTemplateFillActiveClaimFencesCleanup(t *testing.T) {
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusFailed, nil)
	task.FailurePhase = "template_fill_plan.command"
	task.ErrorMessage = "plan failed"
	if err := repo.SaveTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	allPaths := writeTemplateFillRetryEvidence(projectPath)
	now := time.Now().UTC()
	claimed, err := repo.ClaimTaskExecution(context.Background(), task.ID, model.TaskStatusFailed, "active-worker-claim", now, now.Add(-time.Hour))
	if err != nil || !claimed {
		t.Fatalf("ClaimTaskExecution() = %v, %v", claimed, err)
	}

	if _, err := service.RetryTask(context.Background(), task.ID, string(PhaseTemplateFillPlan)); !errors.Is(err, errTaskStateChanged) {
		t.Fatalf("RetryTask() error = %v, want errTaskStateChanged", err)
	}
	for _, relativePath := range allPaths {
		assertPathExists(t, filepath.Join(projectPath, filepath.FromSlash(relativePath)))
	}
	persisted, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.TaskStatusFailed || persisted.ExecutionClaimToken != "active-worker-claim" {
		t.Fatalf("fenced retry changed active claim = %#v", persisted)
	}
}

func TestRetryTemplateFillRestoresOutputsWhenDBTransitionFails(t *testing.T) {
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusFailed, nil)
	task.FailurePhase = "template_fill_apply.contract"
	task.ErrorMessage = "apply failed"
	if err := repo.SaveTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	allPaths := writeTemplateFillRetryEvidence(projectPath)
	injected := errors.New("injected retry transition failure")
	installTemplateFillTransitionFailure(t, repo.DB(), model.TaskStatusTemplateFillApplying, injected)

	if _, err := service.RetryTask(context.Background(), task.ID, string(PhaseTemplateFillApply)); !errors.Is(err, injected) {
		t.Fatalf("RetryTask() error = %v, want injected failure", err)
	}
	for _, relativePath := range allPaths {
		assertPathExists(t, filepath.Join(projectPath, filepath.FromSlash(relativePath)))
	}
	persisted, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.TaskStatusFailed || persisted.FailurePhase != "template_fill_apply.contract" {
		t.Fatalf("failed transition mutated task = %#v", persisted)
	}
	if persisted.ExecutionClaimToken != "" || persisted.ExecutionClaimedAt != nil {
		t.Fatalf("failed transition leaked execution claim = %#v", persisted)
	}
}

func TestRetryTemplateFillCASLossRestoresOutputsAndPreservesNewStatus(t *testing.T) {
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusFailed, nil)
	task.FailurePhase = "template_fill_check.contract"
	task.ErrorMessage = "check failed"
	if err := repo.SaveTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	allPaths := writeTemplateFillRetryEvidence(projectPath)
	service.beforeTemplateFillAPICommit = func(targetStatus string) {
		if targetStatus != model.TaskStatusTemplateFillChecking {
			return
		}
		if err := repo.DB().Model(&model.Task{}).Where("id = ?", task.ID).Update("status", model.TaskStatusCancelled).Error; err != nil {
			t.Fatal(err)
		}
	}

	if _, err := service.RetryTask(context.Background(), task.ID, string(PhaseTemplateFillCheck)); !errors.Is(err, errTaskStateChanged) {
		t.Fatalf("RetryTask() error = %v, want errTaskStateChanged", err)
	}
	for _, relativePath := range allPaths {
		assertPathExists(t, filepath.Join(projectPath, filepath.FromSlash(relativePath)))
	}
	persisted, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.TaskStatusCancelled {
		t.Fatalf("CAS loss overwrote newer status: %#v", persisted)
	}
	if persisted.ExecutionClaimToken != "" || persisted.ExecutionClaimedAt != nil {
		t.Fatalf("CAS loss leaked execution claim = %#v", persisted)
	}
}

func TestRetryQualityCheckCleansOnlyDownstreamArtifacts(t *testing.T) {
	service, repo, task, projectPath := retryTestService(t)
	mustWriteRetryProjectFiles(projectPath)
	service.agentCfg.RunnerProfile = model.RunnerProfileRealLite
	service.agentCfg.FullPPTDefaultEnabled = false

	updated, err := service.RetryTask(context.Background(), task.ID, string(PhaseQualityCheck))
	if err != nil {
		t.Fatalf("RetryTask() error = %v", err)
	}
	if updated.Status != model.TaskStatusQualityChecking {
		t.Fatalf("status = %q, want quality_checking", updated.Status)
	}
	if updated.RunnerProfile != model.RunnerProfileFullPPTMaster {
		t.Fatalf("retry changed locked profile: %#v", updated)
	}
	assertPathExists(t, filepath.Join(projectPath, "design_spec.md"))
	assertPathExists(t, filepath.Join(projectPath, "spec_lock.md"))
	assertPathExists(t, filepath.Join(projectPath, "svg_output", "01_page_01.svg"))
	assertPathMissing(t, filepath.Join(projectPath, ".slidesmith", "quality_report.json"))
	assertPathMissing(t, filepath.Join(projectPath, "svg_final"))
	assertPathMissing(t, filepath.Join(projectPath, "exports"))

	latest, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.FailurePhase != "" || latest.ErrorMessage != "" {
		t.Fatalf("failure fields not cleared: phase=%q error=%q", latest.FailurePhase, latest.ErrorMessage)
	}
}

func TestFullRetryCleanupRejectsSymlinkEscape(t *testing.T) {
	service, repo, task, projectPath := retryTestService(t)
	mustWriteRetryProjectFiles(projectPath)
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "preserve.txt")
	if err := os.WriteFile(outsideFile, []byte("preserve\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(projectPath, "svg_final")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(projectPath, "svg_final")); err != nil {
		t.Fatal(err)
	}
	task.FailurePhase = "finalize_export.contract"
	if err := repo.SaveTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RetryTask(context.Background(), task.ID, string(PhaseFinalizeExport)); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("RetryTask() error = %v, want symlink rejection", err)
	}
	assertPathExists(t, outsideFile)
}

func TestProcessTaskContinuesFromQualityCheckRetry(t *testing.T) {
	service, repo, task, projectPath := retryTestService(t)
	mustWriteRetryProjectFiles(projectPath)
	if _, err := service.RetryTask(context.Background(), task.ID, string(PhaseQualityCheck)); err != nil {
		t.Fatalf("RetryTask() error = %v", err)
	}

	if err := service.ProcessTask(context.Background(), task.ID); err != nil {
		t.Fatalf("ProcessTask() error = %v", err)
	}
	updated, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != model.TaskStatusCompleted {
		t.Fatalf("status = %q, want completed", updated.Status)
	}
	phaseRuns, err := repo.ListPhaseRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, run := range phaseRuns {
		seen[run.Phase] = true
	}
	if seen[string(PhaseSpecGenerate)] || seen[string(PhaseSVGExecute)] {
		t.Fatalf("quality retry should not rerun spec/svg phases: %#v", phaseRuns)
	}
	for _, phase := range []PipelinePhase{PhaseQualityCheck, PhaseFinalizeExport, PhasePublish} {
		if !seen[string(phase)] {
			t.Fatalf("phase %s missing after quality retry: %#v", phase, phaseRuns)
		}
	}
}

func TestProcessTaskContinuesFromFinalizeExportRetry(t *testing.T) {
	service, repo, task, projectPath := retryTestService(t)
	mustWriteRetryProjectFiles(projectPath)
	task.FailurePhase = "finalize_export.contract"
	task.ErrorMessage = "pptx missing"
	if err := repo.SaveTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}

	updated, err := service.RetryTask(context.Background(), task.ID, string(PhaseFinalizeExport))
	if err != nil {
		t.Fatalf("RetryTask() error = %v", err)
	}
	if updated.Status != model.TaskStatusExporting {
		t.Fatalf("status = %q, want exporting", updated.Status)
	}
	assertPathExists(t, filepath.Join(projectPath, "svg_output", "01_page_01.svg"))
	assertPathExists(t, filepath.Join(projectPath, "notes", "total.md"))
	assertPathExists(t, filepath.Join(projectPath, ".slidesmith", "quality_report.json"))
	assertPathMissing(t, filepath.Join(projectPath, "svg_final"))
	assertPathMissing(t, filepath.Join(projectPath, "exports"))

	if err := service.ProcessTask(context.Background(), task.ID); err != nil {
		t.Fatalf("ProcessTask() error = %v", err)
	}
	latest, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Status != model.TaskStatusCompleted {
		t.Fatalf("status = %q, want completed", latest.Status)
	}
	phaseRuns, err := repo.ListPhaseRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, run := range phaseRuns {
		seen[run.Phase] = true
	}
	for _, phase := range []PipelinePhase{PhaseSpecGenerate, PhaseSVGExecute, PhaseQualityCheck} {
		if seen[string(phase)] {
			t.Fatalf("finalize retry should not rerun %s: %#v", phase, phaseRuns)
		}
	}
	for _, phase := range []PipelinePhase{PhaseFinalizeExport, PhasePublish} {
		if !seen[string(phase)] {
			t.Fatalf("phase %s missing after finalize retry: %#v", phase, phaseRuns)
		}
	}
}

func TestProcessTaskContinuesFromPublishRetryAfterPlatformArtifactsDeleted(t *testing.T) {
	service, repo, task, projectPath := retryTestService(t)
	mustWriteRetryProjectFiles(projectPath)
	task.FailurePhase = "publish"
	task.ErrorMessage = "platform artifacts deleted"
	if err := repo.SaveTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateArtifact(context.Background(), &model.Artifact{
		TaskID:         task.ID,
		Kind:           model.ArtifactKindPPTX,
		Name:           "deleted.pptx",
		ObjectKey:      "tasks/task-retry/artifacts/deleted/exports/deleted.pptx",
		PublishVersion: "deleted",
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.DB().Where("task_id = ?", task.ID).Delete(&model.Artifact{}).Error; err != nil {
		t.Fatal(err)
	}

	updated, err := service.RetryTask(context.Background(), task.ID, string(PhasePublish))
	if err != nil {
		t.Fatalf("RetryTask() error = %v", err)
	}
	if updated.Status != model.TaskStatusPublishing {
		t.Fatalf("status = %q, want publishing", updated.Status)
	}
	if err := service.ProcessTask(context.Background(), task.ID); err != nil {
		t.Fatalf("ProcessTask() error = %v", err)
	}
	latest, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Status != model.TaskStatusCompleted {
		t.Fatalf("status = %q, want completed", latest.Status)
	}
	phaseRuns, err := repo.ListPhaseRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(phaseRuns) != 1 || phaseRuns[0].Phase != string(PhasePublish) || phaseRuns[0].Status != PhaseRunStatusSucceeded {
		t.Fatalf("publish retry should only create succeeded publish phase run: %#v", phaseRuns)
	}
	artifacts, err := repo.ListArtifacts(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	hasPPTX := false
	for _, artifact := range artifacts {
		if artifact.Kind == model.ArtifactKindPPTX {
			hasPPTX = true
		}
	}
	if !hasPPTX {
		t.Fatalf("publish retry did not recreate pptx artifact: %#v", artifacts)
	}
}

func TestMainRoutePublishStillPrefersRuntimeWorkspaceCandidate(t *testing.T) {
	service, repo, task, canonicalProject := retryTestService(t)
	mustWriteRetryProjectFiles(canonicalProject)
	mustWriteFileNoTest(canonicalProject, "design_spec.md", "# canonical workspace\n")

	runtimeWorkspace := filepath.Join(t.TempDir(), "runtime-workspace")
	runtimeProject := filepath.Join(runtimeWorkspace, "projects", task.RuntimeProject+"_ppt169_20260713")
	mustWriteRetryProjectFiles(runtimeProject)
	mustWriteFileNoTest(runtimeProject, "design_spec.md", "# runtime workspace\n")
	task.Status = model.TaskStatusPublishing
	task.Route = model.TaskRouteMain
	task.RuntimeWorkspacePath = runtimeWorkspace
	if err := repo.SaveTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}

	if err := service.ProcessTask(context.Background(), task.ID); err != nil {
		t.Fatalf("ProcessTask() error = %v", err)
	}
	artifacts, err := repo.ListArtifacts(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range artifacts {
		if artifact.Kind != model.ArtifactKindDesignSpec {
			continue
		}
		raw, readErr := os.ReadFile(service.storage.Path(artifact.ObjectKey))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(raw) != "# runtime workspace\n" {
			t.Fatalf("published design spec = %q, want runtime-workspace candidate", raw)
		}
		return
	}
	t.Fatalf("published artifacts missing design spec: %#v", artifacts)
}

func TestMainRoutePublishCleanupFailureStopsFallback(t *testing.T) {
	service, repo, task, canonicalProject := retryTestService(t)
	mustWriteRetryProjectFiles(canonicalProject)
	mustWriteFileNoTest(canonicalProject, filepath.Join("logs", "second-root-only.log"), "second\n")

	runtimeWorkspace := filepath.Join(t.TempDir(), "runtime-workspace")
	runtimeProject := filepath.Join(runtimeWorkspace, "projects", task.RuntimeProject+"_ppt169_20260713")
	mustWriteRetryProjectFiles(runtimeProject)
	mustWritePPTXNoTest(runtimeProject, filepath.Join("exports", "stale.pptx"), 2)
	mustWriteFileNoTest(runtimeProject, filepath.Join("logs", "first-root-only.log"), "first\n")
	task.Status = model.TaskStatusPublishing
	task.Route = model.TaskRouteMain
	task.RuntimeWorkspacePath = runtimeWorkspace
	if err := repo.SaveTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}

	local, ok := service.storage.(*LocalStorage)
	if !ok {
		t.Fatalf("storage = %T, want *LocalStorage", service.storage)
	}
	tracking := &publishCleanupTrackingStorage{
		StorageService:     local,
		failDeleteContains: "/logs/first-root-only.log",
	}
	service.storage = tracking
	service.publisher = NewRuntimeWorkspacePublisher(tracking)

	err := service.ProcessTask(context.Background(), task.ID)
	for _, want := range []string{
		"has 2 slides, expected 3",
		"injected publish object cleanup failure",
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("ProcessTask() error = %v, want %q", err, want)
		}
	}
	persisted, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.TaskStatusFailed || persisted.FailurePhase != "publish" {
		t.Fatalf("task after cleanup-incomplete publish = %#v", persisted)
	}
	artifacts, err := repo.ListArtifacts(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 0 {
		t.Fatalf("cleanup-incomplete publish persisted artifacts: %#v", artifacts)
	}
	firstMarker := ""
	for _, objectKey := range tracking.copiedObjectKeys {
		switch {
		case strings.Contains(objectKey, "/logs/first-root-only.log"):
			firstMarker = objectKey
		case strings.Contains(objectKey, "/logs/second-root-only.log"):
			t.Fatalf("cleanup-incomplete first root fell back to second root: %#v", tracking.copiedObjectKeys)
		}
	}
	if firstMarker == "" {
		t.Fatalf("first-root marker was not copied: %#v", tracking.copiedObjectKeys)
	}
	if _, statErr := os.Stat(local.Path(firstMarker)); statErr != nil {
		t.Fatalf("failed-delete first-root orphan %s not observable: %v", firstMarker, statErr)
	}
}

func TestMainRoutePublishStillFallsBackAfterCompleteFailedRootCleanup(t *testing.T) {
	service, _, task, canonicalProject := retryTestService(t)
	mustWriteRetryProjectFiles(canonicalProject)
	mustWriteFileNoTest(canonicalProject, filepath.Join("logs", "second-root-only.log"), "second\n")

	runtimeWorkspace := filepath.Join(t.TempDir(), "runtime-workspace")
	runtimeProject := filepath.Join(runtimeWorkspace, "projects", task.RuntimeProject+"_ppt169_20260713")
	mustWriteRetryProjectFiles(runtimeProject)
	mustWritePPTXNoTest(runtimeProject, filepath.Join("exports", "stale.pptx"), 2)
	mustWriteFileNoTest(runtimeProject, filepath.Join("logs", "first-root-only.log"), "first\n")
	task.Route = model.TaskRouteMain
	task.RuntimeWorkspacePath = runtimeWorkspace

	local, ok := service.storage.(*LocalStorage)
	if !ok {
		t.Fatalf("storage = %T, want *LocalStorage", service.storage)
	}
	tracking := &publishCleanupTrackingStorage{StorageService: local}
	service.storage = tracking
	service.publisher = NewRuntimeWorkspacePublisher(tracking)
	contract, err := service.publishRuntimeArtifacts(context.Background(), task, service.resolveTaskWorkspace(task))
	if err != nil {
		t.Fatalf("publishRuntimeArtifacts() error = %v, want fallback after complete cleanup", err)
	}
	if contract == nil {
		t.Fatal("publishRuntimeArtifacts() contract = nil")
	}
	firstMarker := ""
	secondMarker := ""
	for _, objectKey := range tracking.copiedObjectKeys {
		switch {
		case strings.Contains(objectKey, "/logs/first-root-only.log"):
			firstMarker = objectKey
		case strings.Contains(objectKey, "/logs/second-root-only.log"):
			secondMarker = objectKey
		}
	}
	if firstMarker == "" || secondMarker == "" {
		t.Fatalf("fallback markers first=%q second=%q; copied=%#v", firstMarker, secondMarker, tracking.copiedObjectKeys)
	}
	if _, statErr := os.Stat(local.Path(firstMarker)); !os.IsNotExist(statErr) {
		t.Fatalf("completely cleaned first-root object remains: %v", statErr)
	}
	if _, statErr := os.Stat(local.Path(secondMarker)); statErr != nil {
		t.Fatalf("fallback second-root object missing: %v", statErr)
	}
}

func TestMainRoutePublisherCleanupIncompleteStopsFallback(t *testing.T) {
	service, repo, task, canonicalProject := retryTestService(t)
	mustWriteRetryProjectFiles(canonicalProject)
	mustWriteFileNoTest(canonicalProject, filepath.Join("logs", "second-root-only.log"), "second\n")

	runtimeWorkspace := filepath.Join(t.TempDir(), "runtime-workspace")
	runtimeProject := filepath.Join(runtimeWorkspace, "projects", task.RuntimeProject+"_ppt169_20260713")
	mustWriteRetryProjectFiles(runtimeProject)
	mustWriteFileNoTest(runtimeProject, filepath.Join("logs", "first-root-only.log"), "first\n")
	task.Status = model.TaskStatusPublishing
	task.Route = model.TaskRouteMain
	task.RuntimeWorkspacePath = runtimeWorkspace
	if err := repo.SaveTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}

	local, ok := service.storage.(*LocalStorage)
	if !ok {
		t.Fatalf("storage = %T, want *LocalStorage", service.storage)
	}
	tracking := &publishCleanupTrackingStorage{
		StorageService:     local,
		failCopyContains:   "/logs/first-root-only.log",
		failCopyAfterWrite: true,
		failDeleteContains: "/logs/first-root-only.log",
	}
	service.storage = tracking
	service.publisher = NewRuntimeWorkspacePublisher(tracking)

	err := service.ProcessTask(context.Background(), task.ID)
	for _, want := range []string{
		"injected publish copy write-then-error",
		"runtime publish cleanup incomplete",
		"injected publish object cleanup failure",
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("ProcessTask() error = %v, want %q", err, want)
		}
	}
	if !errors.Is(err, ErrRuntimePublishCleanupIncomplete) {
		t.Fatalf("ProcessTask() error = %v, want publisher cleanup-incomplete classification", err)
	}
	for _, objectKey := range tracking.attemptedObjectKeys {
		if strings.Contains(objectKey, "/logs/second-root-only.log") {
			t.Fatalf("publisher cleanup-incomplete first root fell back to second root: %#v", tracking.attemptedObjectKeys)
		}
	}
	firstMarker := ""
	for _, objectKey := range tracking.attemptedObjectKeys {
		if strings.Contains(objectKey, "/logs/first-root-only.log") {
			firstMarker = objectKey
			break
		}
	}
	if firstMarker == "" {
		t.Fatalf("first-root write-then-error marker was not attempted: %#v", tracking.attemptedObjectKeys)
	}
	if _, statErr := os.Stat(local.Path(firstMarker)); statErr != nil {
		t.Fatalf("publisher failed-delete orphan %s not observable: %v", firstMarker, statErr)
	}
}

func TestProcessTaskCompletesTemplateFillPublishRetry(t *testing.T) {
	service, repo, task, projectPath, _ := newTemplateFillWorkflowService(t, model.TaskStatusFailed, nil)
	prepareTemplateFillPublishedProjectForTest(t, projectPath, 2)
	task.FailurePhase = "publish.contract"
	task.ErrorMessage = "template fill publish failed"
	task.FailureMetadata = `{"phase":"publish.contract"}`
	if err := repo.SaveTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	mustWriteFileNoTest(projectPath, filepath.Join(".slidesmith", "contracts", "publish.json"), "stale publish\n")
	mustWriteFileNoTest(projectPath, filepath.Join(".slidesmith", "contracts", "final.json"), "stale final\n")

	updated, err := service.RetryTask(context.Background(), task.ID, string(PhasePublish))
	if err != nil {
		t.Fatalf("RetryTask() error = %v", err)
	}
	if updated.Status != model.TaskStatusPublishing {
		t.Fatalf("status = %q, want publishing", updated.Status)
	}
	assertPathMissing(t, filepath.Join(projectPath, ".slidesmith", "contracts", "publish.json"))
	assertPathMissing(t, filepath.Join(projectPath, ".slidesmith", "contracts", "final.json"))
	assertPathExists(t, filepath.Join(projectPath, "analysis", "fill_plan.json"))
	assertPathExists(t, filepath.Join(projectPath, "analysis", "check_report.json"))
	assertPathExists(t, filepath.Join(projectPath, "validation", "validate_report.json"))
	assertPathExists(t, filepath.Join(projectPath, "validation", "readback.md"))
	assertPathExists(t, filepath.Join(projectPath, "exports", "result.pptx"))

	if err := service.ProcessTask(context.Background(), task.ID); err != nil {
		t.Fatalf("ProcessTask() error = %v", err)
	}
	latest, err := repo.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Status != model.TaskStatusCompleted || latest.CompletedAt == nil {
		t.Fatalf("completed template fill publish retry = %#v", latest)
	}
	phaseRuns, err := repo.ListPhaseRuns(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(phaseRuns) != 1 || phaseRuns[0].Phase != string(PhasePublish) || phaseRuns[0].Status != PhaseRunStatusSucceeded {
		t.Fatalf("template fill publish retry should only run publish: %#v", phaseRuns)
	}
	artifacts, err := repo.ListArtifacts(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := map[string]bool{
		model.ArtifactKindTemplateFillPlan:           false,
		model.ArtifactKindTemplateFillCheckReport:    false,
		model.ArtifactKindTemplateFillValidateReport: false,
		model.ArtifactKindTemplateFillReadback:       false,
		model.ArtifactKindPPTX:                       false,
	}
	for _, artifact := range artifacts {
		if _, ok := wantKinds[artifact.Kind]; ok {
			wantKinds[artifact.Kind] = true
		}
	}
	for kind, found := range wantKinds {
		if !found {
			t.Fatalf("template fill publish retry missing artifact kind %q: %#v", kind, artifacts)
		}
	}
}

func retryTestService(t *testing.T) (*TaskService, *repository.Repository, *model.Task, string) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
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
	tmp := t.TempDir()
	storage := NewLocalStorage(filepath.Join(tmp, "storage"))
	workspaceRoot := filepath.Join(tmp, "workspaces")
	runtimeProject := "task_retry"
	workspacePath := filepath.Join(workspaceRoot, runtimeProject)
	projectPath := filepath.Join(workspacePath, "projects", runtimeProject+"_ppt169_20260708")
	task := &model.Task{
		ID:              "task-retry",
		Title:           "Retry split phase",
		Status:          model.TaskStatusFailed,
		RuntimeProject:  runtimeProject,
		FailurePhase:    "quality_check.command",
		ErrorMessage:    "quality failed",
		FailureMetadata: `{"phase":"quality_check.command"}`,
	}
	lockRunnerProfileForTest(task, model.RunnerProfileFullPPTMaster)
	if err := repo.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	service := NewTaskService(
		repo,
		storage,
		splitGenerateFakeAgent{projectPath: projectPath, taskID: task.ID, t: t},
		NewRuntimeWorkspacePublisher(storage),
		config.AgentComposeConfig{
			Enabled:               true,
			RunnerProfile:         "full-ppt-master",
			FullPPTDefaultEnabled: true,
			ResourcePhaseEnabled:  true,
			WorkspaceRoot:         workspaceRoot,
		},
	)
	writeRuntimeProfileManifestForTest(t, workspaceRoot, task)
	return service, repo, task, projectPath
}

func mustWriteRetryProjectFiles(projectPath string) {
	mustWriteFileNoTest(projectPath, filepath.Join("sources", "input.md"), "# Source\n")
	mustWriteFileNoTest(projectPath, filepath.Join("confirm_ui", "result.json"), `{"canvas":"ppt169","page_count":3}`+"\n")
	mustWriteFileNoTest(projectPath, "design_spec.md", "# Design Spec\n")
	mustWriteFileNoTest(projectPath, "spec_lock.md", "# Spec Lock\n")
	mustWriteFileNoTest(projectPath, filepath.Join("svg_output", "01.svg"), `<svg viewBox="0 0 1280 720"></svg>`+"\n")
	mustWriteFileNoTest(projectPath, filepath.Join("svg_output", "02.svg"), `<svg viewBox="0 0 1280 720"></svg>`+"\n")
	mustWriteFileNoTest(projectPath, filepath.Join("svg_output", "03.svg"), `<svg viewBox="0 0 1280 720"></svg>`+"\n")
	mustWriteFileNoTest(projectPath, filepath.Join("notes", "total.md"), "# Notes\n")
	mustWriteFileNoTest(projectPath, filepath.Join(".slidesmith", "quality_report.json"), `{"errors":1}`+"\n")
	mustWriteFileNoTest(projectPath, filepath.Join("svg_final", "01.svg"), `<svg></svg>`+"\n")
	mustWritePPTXNoTest(projectPath, filepath.Join("exports", "stale.pptx"), 3)
	workspaceRoot := filepath.Dir(filepath.Dir(projectPath))
	mustWriteEmptyResourceContractNoTest(projectPath, workspaceRoot, "task-retry", "resource-retry-fixture")
	writeValidSVGBundleNoTest(projectPath, "task-retry", 3)
	svgSHA, err := sha256RegularFiles(filepath.Join(projectPath, "svg_output"), "*.svg")
	if err != nil {
		panic(err)
	}
	hashes, err := svgBundleContractHashes(projectPath)
	if err != nil {
		panic(err)
	}
	for _, phase := range []PipelinePhase{PhaseSVGExecute, PhaseQualityCheck} {
		contract := map[string]any{
			"phase":             string(phase),
			"runner_profile":    model.RunnerProfileFullPPTMaster,
			"svg_output_sha256": svgSHA,
		}
		for field, hash := range hashes {
			contract[field] = hash
		}
		if err := writeJSONPretty(filepath.Join(projectPath, ".slidesmith", "contracts", string(phase)+".json"), contract); err != nil {
			panic(err)
		}
	}
	manifestPath := filepath.Join(workspaceRoot, ".slidesmith", "runtime_manifest.json")
	if manifestSHA, err := sha256File(manifestPath); err == nil {
		designSHA, designErr := sha256File(filepath.Join(projectPath, "design_spec.md"))
		lockSHA, lockErr := sha256File(filepath.Join(projectPath, "spec_lock.md"))
		if designErr != nil || lockErr != nil {
			panic("hash retry spec fixture")
		}
		if err := writeJSONPretty(filepath.Join(projectPath, ".slidesmith", "spec_contract.json"), map[string]any{
			"runner_profile":          model.RunnerProfileFullPPTMaster,
			"runtime_manifest_sha256": manifestSHA,
			"design_spec_sha256":      designSHA,
			"spec_lock_sha256":        lockSHA,
		}); err != nil {
			panic(err)
		}
	}
}

func writeTemplateFillRetryEvidence(projectPath string) []string {
	paths := []string{
		"sources/brand.pptx",
		"sources/content.md",
		"analysis/source_profile.json",
		"analysis/brand.identity.json",
		"analysis/brand.slide_library.json",
		".slidesmith/contracts/source_prepare.json",
		".slidesmith/route.json",
		"analysis/fill_plan.json",
		"analysis/check_report.json",
		".slidesmith/contracts/template_fill_plan.json",
		".slidesmith/contracts/template_fill_check.json",
		".slidesmith/contracts/template_fill_apply.json",
		".slidesmith/contracts/template_fill_validate.json",
		".slidesmith/contracts/publish.json",
		".slidesmith/contracts/final.json",
		"exports/result.pptx",
		"validation/validate_report.json",
		"validation/readback.md",
		".slidesmith/artifacts.json",
		".slidesmith-artifacts.json",
	}
	for _, relativePath := range paths {
		mustWriteFileNoTest(projectPath, filepath.FromSlash(relativePath), relativePath+"\n")
	}
	return paths
}

func retryRelativePathSet(paths []string) map[string]bool {
	result := make(map[string]bool, len(paths))
	for _, path := range paths {
		result[path] = true
	}
	return result
}

func assertPathExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected path %s to exist: %v", path, err)
	}
}

func assertPathMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected path %s to be missing, err=%v", path, err)
	}
}
