package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/slidesmith/slidesmith/backend/internal/model"
	"github.com/slidesmith/slidesmith/backend/internal/repository"
)

type editWorkspace struct {
	Root        string
	Project     string
	ProjectName string
	ComposeFile string
	CLICompose  string
}

func (s *TaskService) ProcessQueuedEditSessions(ctx context.Context, limit int) (int, error) {
	staleBefore := time.Now().UTC().Add(-s.taskExecutionLeaseDuration())
	sessions, err := s.repo.ListClaimableEditSessions(ctx, staleBefore, limit)
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, candidate := range sessions {
		claimToken := uuid.NewString()
		claimedAt := time.Now().UTC()
		claimed, err := s.repo.ClaimEditSession(ctx, candidate.TaskID, candidate.ID, claimToken, claimedAt, staleBefore)
		if err != nil {
			return processed, err
		}
		if !claimed {
			continue
		}
		session, err := s.repo.GetEditSession(ctx, candidate.TaskID, candidate.ID)
		if err != nil {
			return processed, err
		}
		session.ExecutionClaimToken, session.ExecutionClaimedAt = claimToken, &claimedAt
		processed++
		if err := s.processClaimedEditSession(ctx, session); err != nil {
			// The phase helper has already persisted the failure. Keep polling other
			// work instead of changing the completed parent task.
			_ = s.event(context.WithoutCancel(ctx), session.TaskID, model.EventTypeError, "edit_session_failed", "Live Preview edit session failed", map[string]any{
				"edit_session_id": session.ID, "failure_phase": session.FailurePhase, "error": err.Error(),
			})
		}
		_, _ = s.repo.ReleaseEditSessionClaim(context.WithoutCancel(ctx), session.TaskID, session.ID, claimToken)
	}
	return processed, nil
}

func (s *TaskService) processClaimedEditSession(ctx context.Context, session *model.TaskEditSession) error {
	task, err := s.repo.GetTask(ctx, session.TaskID)
	if err != nil {
		return err
	}
	if task.Status != model.TaskStatusCompleted {
		return s.failClaimedEditSession(ctx, session, model.EditPhaseMaterialize, fmt.Errorf("parent task is no longer completed"))
	}
	workspace := s.editWorkspaceFor(task, session)
	phases := []struct {
		phase, expected, running, runner string
		work                             func(context.Context, *model.TaskEditRun) (map[string]any, error)
	}{
		{model.EditPhaseMaterialize, model.EditSessionStatusQueued, model.EditSessionStatusMaterializing, "worker", func(ctx context.Context, run *model.TaskEditRun) (map[string]any, error) {
			return s.materializeEditWorkspace(ctx, task, session, workspace, run)
		}},
		{model.EditPhaseApplyDirect, model.EditSessionStatusMaterializing, model.EditSessionStatusApplyingDirect, "python", func(ctx context.Context, run *model.TaskEditRun) (map[string]any, error) {
			return s.runDirectEditPhase(ctx, task, session, workspace, run)
		}},
		{model.EditPhaseApplyAnnotation, model.EditSessionStatusApplyingDirect, model.EditSessionStatusApplyingAnnotations, "agent", func(ctx context.Context, run *model.TaskEditRun) (map[string]any, error) {
			return s.runEditAnnotationPhase(ctx, task, session, workspace, run)
		}},
		{model.EditPhaseSVGValidate, model.EditSessionStatusApplyingAnnotations, model.EditSessionStatusSVGValidating, "python", func(ctx context.Context, run *model.TaskEditRun) (map[string]any, error) {
			return s.runEditSVGValidatePhase(ctx, task, session, workspace, run)
		}},
		{model.EditPhaseQualityCheck, model.EditSessionStatusSVGValidating, model.EditSessionStatusQualityChecking, "python", func(ctx context.Context, run *model.TaskEditRun) (map[string]any, error) {
			return s.runEditQualityPhase(ctx, task, session, workspace, run)
		}},
		{model.EditPhaseFinalizeExport, model.EditSessionStatusQualityChecking, model.EditSessionStatusExporting, "python", func(ctx context.Context, run *model.TaskEditRun) (map[string]any, error) {
			return s.runEditExportPhase(ctx, task, session, workspace, run)
		}},
		{model.EditPhasePPTXValidate, model.EditSessionStatusExporting, model.EditSessionStatusPPTXValidating, "python", func(ctx context.Context, run *model.TaskEditRun) (map[string]any, error) {
			return s.runEditPPTXValidatePhase(ctx, task, session, workspace, run)
		}},
		{model.EditPhasePublish, model.EditSessionStatusPPTXValidating, model.EditSessionStatusPublishing, "publisher", func(ctx context.Context, run *model.TaskEditRun) (map[string]any, error) {
			return s.publishManualEditVersion(ctx, task, session, workspace, run)
		}},
	}
	for _, phase := range phases {
		if _, err := s.runClaimedEditPhase(ctx, session, workspace, phase.phase, phase.expected, phase.running, phase.runner, phase.work); err != nil {
			return err
		}
	}
	persisted, err := s.repo.GetTask(ctx, task.ID)
	if err != nil {
		return err
	}
	if persisted.Status != model.TaskStatusCompleted {
		return fmt.Errorf("manual edit changed parent task status to %q", persisted.Status)
	}
	return nil
}

func (s *TaskService) runClaimedEditPhase(
	ctx context.Context, session *model.TaskEditSession, workspace editWorkspace,
	phase, expectedStatus, runningStatus, runner string,
	work func(context.Context, *model.TaskEditRun) (map[string]any, error),
) (map[string]any, error) {
	previous := session.Status
	if previous != expectedStatus {
		return nil, s.failClaimedEditSession(ctx, session, phase, fmt.Errorf("edit phase expected status %s, got %s", expectedStatus, previous))
	}
	session.Status = runningStatus
	matched, err := s.repo.UpdateClaimedEditSession(ctx, session, previous, session.ExecutionClaimToken)
	if err != nil || !matched {
		if err == nil {
			err = repository.ErrConflict
		}
		return nil, err
	}
	run := &model.TaskEditRun{
		TaskID: session.TaskID, EditSessionID: session.ID, Phase: phase, Runner: runner,
		Status: PhaseRunStatusRunning, WorkspacePath: workspace.Root, InputJSON: encodeAnyJSON(map[string]any{
			"base_publish_version": session.BasePublishVersion, "frozen_revision": session.FrozenRevision,
			"frozen_patch_sha256": session.FrozenPatchSHA256,
		}),
	}
	now := time.Now().UTC()
	run.StartedAt = &now
	if err := s.repo.CreateEditRun(ctx, run); err != nil {
		return nil, s.failClaimedEditSession(ctx, session, phase, err)
	}
	output, workErr := work(ctx, run)
	finishedAt := time.Now().UTC()
	run.FinishedAt = &finishedAt
	if workErr != nil {
		run.Status, run.ErrorMessage = PhaseRunStatusFailed, workErr.Error()
		run.OutputJSON = encodeAnyJSON(output)
		_ = s.repo.SaveEditRun(context.WithoutCancel(ctx), run)
		session.LastRunID = run.ID
		return output, s.failClaimedEditSession(ctx, session, phase, workErr)
	}
	run.Status, run.OutputJSON = PhaseRunStatusSucceeded, encodeAnyJSON(output)
	if err := s.repo.SaveEditRun(ctx, run); err != nil {
		return nil, s.failClaimedEditSession(ctx, session, phase, err)
	}
	if phase != model.EditPhasePublish {
		session.LastRunID = run.ID
		matched, err = s.repo.UpdateClaimedEditSession(ctx, session, runningStatus, session.ExecutionClaimToken)
		if err != nil || !matched {
			if err == nil {
				err = repository.ErrConflict
			}
			return nil, err
		}
	}
	_ = s.event(ctx, session.TaskID, model.EventTypeRuntime, "edit_phase_succeeded", "Live Preview edit phase succeeded", map[string]any{
		"edit_session_id": session.ID, "edit_run_id": run.ID, "phase": phase,
	})
	return output, nil
}

func (s *TaskService) failClaimedEditSession(ctx context.Context, session *model.TaskEditSession, phase string, cause error) error {
	previous := session.Status
	session.Status, session.ErrorMessage, session.FailurePhase = model.EditSessionStatusFailed, cause.Error(), phase
	session.FailureMetadataJSON = encodeAnyJSON(map[string]any{"phase": phase, "failed_at": time.Now().UTC().Format(time.RFC3339Nano), "retryable": true})
	_, _ = s.repo.UpdateClaimedEditSession(context.WithoutCancel(ctx), session, previous, session.ExecutionClaimToken)
	return cause
}

func (s *TaskService) editWorkspaceFor(task *model.Task, session *model.TaskEditSession) editWorkspace {
	root := filepath.Join(s.workspaces.workspaceRoot(), ".edit-sessions", sanitizePathSegment(task.ID), sanitizePathSegment(session.ID))
	projectName := strings.TrimSpace(task.RuntimeProject)
	if projectName == "" {
		projectName = runtimeProjectName(task.ID)
	}
	composeFile := filepath.Join(root, "agent-compose.yml")
	return editWorkspace{
		Root: root, ProjectName: projectName, Project: filepath.Join(root, "projects", projectName),
		ComposeFile: composeFile, CLICompose: s.workspaces.cliVisiblePath(composeFile),
	}
}

func (s *TaskService) materializeEditWorkspace(ctx context.Context, task *model.Task, session *model.TaskEditSession, workspace editWorkspace, run *model.TaskEditRun) (map[string]any, error) {
	root := s.workspaces.workspaceRoot()
	if !pathWithinRoot(root, workspace.Root) {
		return nil, fmt.Errorf("edit workspace escapes configured root")
	}
	if err := os.RemoveAll(workspace.Root); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(workspace.Project, 0o755); err != nil {
		return nil, err
	}
	if err := s.workspaces.copySeedDir(ctx, workspace.Root, "scripts"); err != nil {
		return nil, err
	}
	skillSource, err := s.workspaces.resolvePPTMasterSkillDir()
	if err != nil {
		return nil, err
	}
	if err := copyDir(ctx, skillSource, filepath.Join(workspace.Root, "skills", "ppt-master")); err != nil {
		return nil, err
	}
	if err := s.workspaces.writeComposeFile(workspace.ComposeFile); err != nil {
		return nil, err
	}
	taskWorkspace := &TaskWorkspace{HostDir: workspace.Root, ComposeFile: workspace.ComposeFile, CLIComposeFile: workspace.CLICompose, ProjectPath: workspace.Project, SkillDir: filepath.Join(workspace.Root, "skills", "ppt-master")}
	if err := s.workspaces.applyTemplateLock(ctx, taskWorkspace, task); err != nil {
		return nil, err
	}
	if err := s.workspaces.WriteRuntimeManifest(taskWorkspace, task, workspace.Project); err != nil {
		return nil, err
	}
	if err := s.workspaces.writeSkillLock(taskWorkspace, skillSource); err != nil {
		return nil, err
	}
	artifacts, err := s.ListArtifactsByVersion(ctx, task.ID, session.BasePublishVersion)
	if err != nil {
		return nil, err
	}
	restored := make([]map[string]any, 0, len(artifacts))
	seenTargets := map[string]bool{}
	for _, artifact := range artifacts {
		if _, err := validateStoredArtifact(s.storage, artifact); err != nil {
			return nil, err
		}
		rel := materializedArtifactRelativePath(task.ID, session.BasePublishVersion, artifact.ObjectKey)
		if rel == "" || seenTargets[rel] {
			return nil, fmt.Errorf("materialized artifact path is empty or duplicated: %s", rel)
		}
		seenTargets[rel] = true
		target := filepath.Join(workspace.Project, filepath.FromSlash(rel))
		if !pathWithinRoot(workspace.Project, target) {
			return nil, fmt.Errorf("materialized artifact escapes project: %s", rel)
		}
		if err := copyFile(s.storage.Path(artifact.ObjectKey), target, 0o644); err != nil {
			return nil, err
		}
		restored = append(restored, map[string]any{"artifact_id": artifact.ID, "path": rel, "kind": artifact.Kind, "sha256": artifact.SHA256, "size": artifact.Size})
	}
	// Downstream products are rebuilt from the edited authored SVG.
	for _, path := range []string{filepath.Join(workspace.Project, "exports"), filepath.Join(workspace.Project, "validation")} {
		if err := os.RemoveAll(path); err != nil {
			return nil, err
		}
	}
	for _, name := range []string{"quality_check.json", "finalize_export.json", "pptx_validate.json", "publish.json", "final.json"} {
		_ = os.Remove(filepath.Join(workspace.Project, ".slidesmith", "contracts", name))
	}
	_ = os.Remove(filepath.Join(workspace.Project, ".slidesmith", "artifacts.json"))
	for _, name := range []string{"manual_edit_apply_report.json", "annotation_apply_report.json", "manual_edit_diff_report.json"} {
		_ = os.Remove(filepath.Join(workspace.Project, "analysis", name))
	}
	if err := os.MkdirAll(filepath.Join(workspace.Project, "analysis"), 0o755); err != nil {
		return nil, err
	}
	if sha256StringBytes([]byte(session.DraftJSON)) != session.FrozenPatchSHA256 {
		return nil, fmt.Errorf("frozen patch hash does not match draft bytes")
	}
	if err := os.WriteFile(filepath.Join(workspace.Project, "analysis", "manual_edit_patch.json"), []byte(session.DraftJSON), 0o644); err != nil {
		return nil, err
	}
	base, err := s.loadEditBaseInventory(ctx, task.ID, session.BasePublishVersion)
	if err != nil {
		return nil, err
	}
	authoredPages, finalPages := []string{}, []string{}
	for _, page := range base.Inventory.Pages {
		authoredPages = append(authoredPages, page.Path)
		finalPages = append(finalPages, strings.Replace(page.Path, "svg_output/", "svg_final/", 1))
	}
	contract := map[string]any{
		"schema": "slidesmith.manual_edit_materialize.v1", "task_id": task.ID, "edit_session_id": session.ID,
		"base_publish_version": session.BasePublishVersion, "base_artifact_manifest_sha256": session.BaseArtifactManifestSHA256,
		"base_svg_inventory_sha256": session.BaseSVGInventorySHA256, "restored_artifacts": restored,
		"authored_pages": authoredPages, "final_pages": finalPages, "passed": true,
	}
	if err := writeJSONPretty(filepath.Join(workspace.Project, ".slidesmith", "contracts", model.EditPhaseMaterialize+".json"), contract); err != nil {
		return nil, err
	}
	return map[string]any{"workspace": "isolated", "restored_artifact_count": len(restored), "contract": contract, "edit_run_id": run.ID}, nil
}

func materializedArtifactRelativePath(taskID, version, objectKey string) string {
	rel := versionArtifactRelativePath(taskID, version, objectKey)
	switch {
	case strings.HasPrefix(rel, "contracts/"):
		return ".slidesmith/" + rel
	case rel == "manifest/runtime_artifacts.json":
		return ".slidesmith/artifacts.json"
	case rel == "manifest/quality_report.json":
		return ".slidesmith/quality_report.json"
	case rel == "manifest/resources_manifest.json":
		return ".slidesmith/resources_manifest.json"
	case rel == "manifest/manual_edit_lock.json":
		return ".slidesmith/manual_edit_lock.json"
	case strings.HasPrefix(rel, "source/"):
		return "sources/" + strings.TrimPrefix(rel, "source/")
	default:
		return rel
	}
}

func (s *TaskService) runDirectEditPhase(ctx context.Context, task *model.Task, session *model.TaskEditSession, workspace editWorkspace, run *model.TaskEditRun) (map[string]any, error) {
	projectRel := filepath.ToSlash(filepath.Join("projects", workspace.ProjectName))
	command := fmt.Sprintf("python3 scripts/manual_edit_runner.py %s --patch analysis/manual_edit_patch.json --task-id %s --session-id %s", shellArg(projectRel), shellArg(task.ID), shellArg(session.ID))
	result, err := s.runEditAgent(ctx, session, workspace, model.EditPhaseApplyDirect, command, "")
	if err != nil {
		return runtimeEditOutput(result), err
	}
	report := readJSONMap(filepath.Join(workspace.Project, "analysis", "manual_edit_apply_report.json"))
	if valueString(report, "schema", "") != "slidesmith.manual_edit_apply_report.v1" || valueString(report, "frozen_patch_sha256", "") != session.FrozenPatchSHA256 || !valueBool(report, "passed", false) {
		return nil, fmt.Errorf("manual edit apply report is invalid")
	}
	return map[string]any{"runtime": runtimeEditOutput(result), "report": report, "edit_run_id": run.ID}, nil
}

func (s *TaskService) runEditAnnotationPhase(ctx context.Context, task *model.Task, session *model.TaskEditSession, workspace editWorkspace, run *model.TaskEditRun) (map[string]any, error) {
	var draft ManualEditDraft
	if err := json.Unmarshal([]byte(session.DraftJSON), &draft); err != nil {
		return nil, err
	}
	reportPath := filepath.Join(workspace.Project, "analysis", "annotation_apply_report.json")
	if len(draft.Annotations) == 0 {
		report := readJSONMap(reportPath)
		if valueString(report, "schema", "") != "slidesmith.annotation_apply_report.v1" || !valueBool(report, "passed", false) {
			return nil, fmt.Errorf("zero-annotation receipt is missing")
		}
		if err := writeAnnotationAuditLog(workspace.Project, draft.Annotations, "skipped"); err != nil {
			return nil, err
		}
		return map[string]any{"skipped": true, "requested": 0, "report": report}, nil
	}
	var caps map[string]any
	_ = json.Unmarshal([]byte(session.CapabilitySnapshotJSON), &caps)
	if !valueBool(caps, "annotation", false) {
		return nil, fmt.Errorf("annotation capability was not enabled for this session")
	}
	annotationJSON, _ := json.Marshal(draft.Annotations)
	prompt := fmt.Sprintf(`Apply only these existing-element/page annotations to project %s: %s
Constraints: modify only listed svg_output pages and existing elements/resources; do not add/remove/reorder pages; no network, new images, chart/table data, notes, specs, locks, analysis or contracts changes. Write analysis/annotation_apply_report.json using schema slidesmith.annotation_apply_report.v1 with requested/applied/rejected, per-annotation status, pages_touched, markers_remaining=0 and passed=true only when every annotation is applied.`, filepath.ToSlash(filepath.Join("projects", workspace.ProjectName)), string(annotationJSON))
	result, err := s.runEditAgent(ctx, session, workspace, model.EditPhaseApplyAnnotation, "", prompt)
	if err != nil {
		return runtimeEditOutput(result), err
	}
	report := readJSONMap(reportPath)
	if valueString(report, "schema", "") != "slidesmith.annotation_apply_report.v1" || !valueBool(report, "passed", false) || int(valueNumber(report["requested"])) != len(draft.Annotations) || int(valueNumber(report["applied"])) != len(draft.Annotations) || int(valueNumber(report["rejected"])) != 0 {
		return nil, fmt.Errorf("annotation apply report is incomplete")
	}
	if found, err := projectFilesContain(workspace.Project, "svg_output", "data-edit-"); err != nil || found {
		return nil, fmt.Errorf("annotation markers remain after apply")
	}
	if err := writeAnnotationAuditLog(workspace.Project, draft.Annotations, "applied"); err != nil {
		return nil, err
	}
	return map[string]any{"runtime": runtimeEditOutput(result), "report": report, "edit_run_id": run.ID}, nil
}

func writeAnnotationAuditLog(projectPath string, annotations []ManualEditAnnotation, status string) error {
	path := filepath.Join(projectPath, "live_preview", "annotations.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var builder strings.Builder
	for _, annotation := range annotations {
		raw, err := json.Marshal(map[string]any{"annotation_id": annotation.AnnotationID, "page_id": annotation.PageID, "scope": annotation.Scope, "status": status})
		if err != nil {
			return err
		}
		builder.Write(raw)
		builder.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(builder.String()), 0o644)
}

func (s *TaskService) runEditSVGValidatePhase(ctx context.Context, task *model.Task, session *model.TaskEditSession, workspace editWorkspace, run *model.TaskEditRun) (map[string]any, error) {
	compatibility, err := prepareManualEditSVGValidationInputs(task, workspace)
	if err != nil {
		return nil, err
	}
	projectRel := filepath.ToSlash(filepath.Join("projects", workspace.ProjectName))
	command := fmt.Sprintf("python3 scripts/svg_bundle_inspector.py %s --resources-manifest .slidesmith/resources_manifest.json --resource-usage analysis/svg_resource_usage.json --chart-usage analysis/chart_usage.json --notes notes/total.md", shellArg(projectRel))
	result, err := s.runEditAgent(ctx, session, workspace, model.EditPhaseSVGValidate, command, "")
	if err != nil {
		return runtimeEditOutput(result), err
	}
	contract, err := validateSVGExecuteContract(workspace.Project, task.ID)
	if err != nil {
		return nil, err
	}
	taskWorkspace := &TaskWorkspace{HostDir: workspace.Root, ProjectPath: workspace.Project}
	contract["phase_run_id"], contract["manual_edit_session_id"] = run.ID, session.ID
	contract, err = bindFullPhaseContract(workspace.Project, PhaseSVGExecute, contract, task, taskWorkspace, resultRunID(result))
	if err != nil {
		return nil, err
	}
	diff, lock, err := s.buildManualEditDiffAndLock(ctx, task, session, workspace)
	if err != nil {
		return nil, err
	}
	return map[string]any{"runtime": runtimeEditOutput(result), "contract": contract, "diff": diff, "lock": lock, "legacy_compatibility": compatibility}, nil
}

type manualEditLivePage struct {
	PageID string
	Path   string
	SHA256 string
}

func prepareManualEditSVGValidationInputs(task *model.Task, workspace editWorkspace) (map[string]any, error) {
	if task == nil {
		return nil, fmt.Errorf("manual edit task is missing")
	}
	var inventory svgInventoryDocument
	inventoryPath := filepath.Join(workspace.Project, "analysis", "svg_inventory.json")
	if err := readManualEditJSON(inventoryPath, &inventory); err != nil {
		return nil, fmt.Errorf("read base SVG inventory: %w", err)
	}
	if inventory.Schema != svgInventorySchema || inventory.TaskID != task.ID || len(inventory.Pages) == 0 {
		return nil, fmt.Errorf("base SVG inventory binding is invalid")
	}
	if inventory.PageCount != len(inventory.Pages) || strings.TrimSpace(inventory.Canvas) == "" {
		return nil, fmt.Errorf("base SVG inventory canvas/page count is invalid")
	}
	confirmationPath := filepath.Join(workspace.Project, "confirm_ui", "result.json")
	confirmationSynthesized := false
	if _, err := os.Stat(confirmationPath); os.IsNotExist(err) {
		confirmation := map[string]any{
			"status": "confirmed", "canvas": inventory.Canvas, "page_count": inventory.PageCount,
			"compatibility_source": "manual_edit_legacy_backfill",
		}
		if err := writeJSONPretty(confirmationPath, confirmation); err != nil {
			return nil, err
		}
		confirmationSynthesized = true
	} else if err != nil {
		return nil, err
	} else {
		var confirmation map[string]any
		if err := readManualEditJSON(confirmationPath, &confirmation); err != nil {
			return nil, fmt.Errorf("read confirmation snapshot: %w", err)
		}
		if valueString(confirmation, "canvas", "") != inventory.Canvas || int(valueNumber(confirmation["page_count"])) != inventory.PageCount {
			return nil, fmt.Errorf("confirmation snapshot differs from base SVG inventory")
		}
	}
	livePages := make(map[string]manualEditLivePage, len(inventory.Pages))
	for _, page := range inventory.Pages {
		path := filepath.Join(workspace.Project, filepath.FromSlash(page.Path))
		if page.PageID == "" || page.Path == "" || !pathWithinRoot(workspace.Project, path) {
			return nil, fmt.Errorf("base SVG inventory page is invalid: %s", page.PageID)
		}
		sha, err := sha256File(path)
		if err != nil {
			return nil, err
		}
		livePages[page.PageID] = manualEditLivePage{PageID: page.PageID, Path: page.Path, SHA256: sha}
	}
	resourceUsagePath := filepath.Join(workspace.Project, "analysis", "svg_resource_usage.json")
	chartUsagePath := filepath.Join(workspace.Project, "analysis", "chart_usage.json")
	resourceUsage, resourceBindings, err := loadManualEditResourceUsage(resourceUsagePath, livePages)
	if err != nil {
		return nil, err
	}
	chartUsage, chartBindings, err := loadManualEditChartUsage(chartUsagePath, livePages)
	if err != nil {
		return nil, err
	}
	manifestPath := filepath.Join(workspace.Project, ".slidesmith", "resources_manifest.json")
	synthesized := false
	if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
		if resourceBindings != 0 || chartBindings != 0 {
			return nil, fmt.Errorf("legacy version is missing resources manifest for non-empty resource/chart bindings")
		}
		manifest := map[string]any{
			"schema": resourcesManifestSchema, "task_id": task.ID, "route": task.Route,
			"runner_profile": model.RunnerProfileFullPPTMaster,
			"project_path":   filepath.ToSlash(filepath.Join("projects", workspace.ProjectName)),
			"resources":      []any{},
			"summary": map[string]any{
				"total": 0, "ready": 0, "degraded": 0, "failed": 0,
				"pending": 0, "required_failed": 0, "bytes": 0,
			},
			"compatibility_source": "manual_edit_legacy_backfill",
		}
		if err := writeJSONPretty(manifestPath, manifest); err != nil {
			return nil, err
		}
		synthesized = true
	} else if err != nil {
		return nil, err
	}
	var manifest map[string]any
	if err := readManualEditJSON(manifestPath, &manifest); err != nil {
		return nil, fmt.Errorf("read resources manifest: %w", err)
	}
	if valueString(manifest, "schema", "") != resourcesManifestSchema || valueString(manifest, "task_id", "") != task.ID || valueString(manifest, "runner_profile", "") != model.RunnerProfileFullPPTMaster {
		return nil, fmt.Errorf("resources manifest binding is invalid")
	}
	manifestSHA, err := sha256File(manifestPath)
	if err != nil {
		return nil, err
	}
	resourceUsage["resources_manifest_sha256"] = manifestSHA
	chartUsage["resources_manifest_sha256"] = manifestSHA
	if err := writeJSONPretty(resourceUsagePath, resourceUsage); err != nil {
		return nil, err
	}
	if err := writeJSONPretty(chartUsagePath, chartUsage); err != nil {
		return nil, err
	}
	return map[string]any{"confirmation_synthesized": confirmationSynthesized, "resources_manifest_synthesized": synthesized, "resources_manifest_sha256": manifestSHA, "resource_bindings": resourceBindings, "chart_bindings": chartBindings}, nil
}

func loadManualEditResourceUsage(path string, livePages map[string]manualEditLivePage) (map[string]any, int, error) {
	var document map[string]any
	if err := readManualEditJSON(path, &document); err != nil {
		return nil, 0, fmt.Errorf("read SVG resource usage: %w", err)
	}
	if valueString(document, "schema", "") != svgResourceUsageSchema {
		return nil, 0, fmt.Errorf("SVG resource usage schema is invalid")
	}
	rows, ok := document["pages"].([]any)
	if !ok || len(rows) != len(livePages) {
		return nil, 0, fmt.Errorf("SVG resource usage page roster is invalid")
	}
	seen, bindingCount := map[string]bool{}, 0
	for _, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok {
			return nil, 0, fmt.Errorf("SVG resource usage page row is invalid")
		}
		pageID := valueString(row, "page_id", "")
		page, found := livePages[pageID]
		if !found || seen[pageID] || valueString(row, "svg", "") != page.Path {
			return nil, 0, fmt.Errorf("SVG resource usage binding is invalid for %s", pageID)
		}
		bindings, ok := row["resources"].([]any)
		if !ok {
			return nil, 0, fmt.Errorf("SVG resource usage resources are invalid for %s", pageID)
		}
		bindingCount += len(bindings)
		row["svg_sha256"] = page.SHA256
		seen[pageID] = true
	}
	return document, bindingCount, nil
}

func loadManualEditChartUsage(path string, livePages map[string]manualEditLivePage) (map[string]any, int, error) {
	var document map[string]any
	if err := readManualEditJSON(path, &document); err != nil {
		return nil, 0, fmt.Errorf("read chart usage: %w", err)
	}
	if valueString(document, "schema", "") != chartUsageSchema {
		return nil, 0, fmt.Errorf("chart usage schema is invalid")
	}
	charts, ok := document["charts"].([]any)
	if !ok {
		return nil, 0, fmt.Errorf("chart usage entries are invalid")
	}
	for _, raw := range charts {
		chart, ok := raw.(map[string]any)
		if !ok {
			return nil, 0, fmt.Errorf("chart usage entry is invalid")
		}
		pageID := valueString(chart, "page_id", "")
		page, found := livePages[pageID]
		if !found || valueString(chart, "svg", "") != page.Path {
			return nil, 0, fmt.Errorf("chart usage binding is invalid for %s", pageID)
		}
		if _, present := chart["svg_sha256"]; present {
			chart["svg_sha256"] = page.SHA256
		}
	}
	if rows, present := document["pages"].([]any); present {
		seen := map[string]bool{}
		for _, raw := range rows {
			row, ok := raw.(map[string]any)
			if !ok {
				return nil, 0, fmt.Errorf("chart usage page row is invalid")
			}
			pageID := valueString(row, "page_id", "")
			page, found := livePages[pageID]
			if !found || seen[pageID] || valueString(row, "svg", "") != page.Path {
				return nil, 0, fmt.Errorf("chart usage page binding is invalid for %s", pageID)
			}
			row["svg_sha256"] = page.SHA256
			seen[pageID] = true
		}
		if len(seen) != len(livePages) {
			return nil, 0, fmt.Errorf("chart usage page roster is invalid")
		}
	}
	return document, len(charts), nil
}

func readManualEditJSON(path string, destination any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, destination)
}

func (s *TaskService) runEditQualityPhase(ctx context.Context, task *model.Task, session *model.TaskEditSession, workspace editWorkspace, run *model.TaskEditRun) (map[string]any, error) {
	projectRel := filepath.ToSlash(filepath.Join("projects", workspace.ProjectName))
	command := fmt.Sprintf("python3 scripts/quality_runner.py svg %s --phase-run-id %s", shellArg(projectRel), shellArg(run.ID))
	result, err := s.runEditAgent(ctx, session, workspace, model.EditPhaseQualityCheck, command, "")
	if err != nil {
		return runtimeEditOutput(result), err
	}
	contract, err := validateQualityCheckContractForRun(workspace.Project, run.ID)
	if err != nil {
		return nil, err
	}
	contract["manual_edit_session_id"] = session.ID
	if err := bindManualEditPhaseHashes(workspace.Project, session, contract); err != nil {
		return nil, err
	}
	contract, err = bindFullPhaseContract(workspace.Project, PhaseQualityCheck, contract, task, &TaskWorkspace{HostDir: workspace.Root, ProjectPath: workspace.Project}, resultRunID(result))
	if err != nil {
		return nil, err
	}
	return map[string]any{"runtime": runtimeEditOutput(result), "contract": contract}, nil
}

func (s *TaskService) runEditExportPhase(ctx context.Context, task *model.Task, session *model.TaskEditSession, workspace editWorkspace, run *model.TaskEditRun) (map[string]any, error) {
	backupRoot, preserved, err := s.stageUntouchedManualEditFinals(ctx, task, session, workspace)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(backupRoot)
	for _, path := range []string{filepath.Join(workspace.Project, "svg_final"), filepath.Join(workspace.Project, "exports")} {
		if err := os.RemoveAll(path); err != nil {
			return nil, err
		}
	}
	projectRel := filepath.ToSlash(filepath.Join("projects", workspace.ProjectName))
	finalizeCommand := fmt.Sprintf("python3 skills/ppt-master/scripts/finalize_svg.py %s --quiet", shellArg(projectRel))
	finalizeResult, err := s.runEditAgent(ctx, session, workspace, model.EditPhaseFinalizeExport, finalizeCommand, "")
	if err != nil {
		return map[string]any{"finalize_runtime": runtimeEditOutput(finalizeResult)}, err
	}
	if err := restoreUntouchedManualEditFinals(workspace, backupRoot, preserved); err != nil {
		return nil, err
	}
	exportCommand := fmt.Sprintf("python3 skills/ppt-master/scripts/svg_to_pptx.py %s --no-notes -t none", shellArg(projectRel))
	result, err := s.runEditAgent(ctx, session, workspace, model.EditPhaseFinalizeExport, exportCommand, "")
	if err != nil {
		return map[string]any{"finalize_runtime": runtimeEditOutput(finalizeResult), "export_runtime": runtimeEditOutput(result)}, err
	}
	contract, err := validatePPTXExportContract(workspace.Project)
	if err != nil {
		return nil, err
	}
	contract["phase_run_id"], contract["manual_edit_session_id"] = run.ID, session.ID
	if err := bindManualEditPhaseHashes(workspace.Project, session, contract); err != nil {
		return nil, err
	}
	contract, err = bindFullPhaseContract(workspace.Project, PhaseFinalizeExport, contract, task, &TaskWorkspace{HostDir: workspace.Root, ProjectPath: workspace.Project}, resultRunID(result))
	if err != nil {
		return nil, err
	}
	return map[string]any{"runtime": runtimeEditOutput(result), "finalize_runtime": runtimeEditOutput(finalizeResult), "contract": contract, "preserved_untouched_final_pages": len(preserved)}, nil
}

func (s *TaskService) stageUntouchedManualEditFinals(ctx context.Context, task *model.Task, session *model.TaskEditSession, workspace editWorkspace) (string, map[string]string, error) {
	var draft ManualEditDraft
	if err := json.Unmarshal([]byte(session.DraftJSON), &draft); err != nil {
		return "", nil, err
	}
	touched := map[string]bool{}
	for _, page := range draft.Pages {
		if len(page.Operations) > 0 {
			touched[page.PageID] = true
		}
	}
	for _, annotation := range draft.Annotations {
		touched[annotation.PageID] = true
	}
	base, err := s.loadEditBaseInventory(ctx, task.ID, session.BasePublishVersion)
	if err != nil {
		return "", nil, err
	}
	backupRoot := filepath.Join(workspace.Root, ".slidesmith", "edit-final-preserve", uuid.NewString())
	preserved := map[string]string{}
	for _, page := range base.Inventory.Pages {
		if touched[page.PageID] {
			continue
		}
		finalRel := strings.Replace(page.Path, "svg_output/", "svg_final/", 1)
		artifact, ok := base.Final[finalRel]
		if !ok {
			return "", nil, fmt.Errorf("base final SVG is missing for %s", page.PageID)
		}
		if _, err := validateStoredArtifact(s.storage, artifact); err != nil {
			return "", nil, err
		}
		if err := copyFile(s.storage.Path(artifact.ObjectKey), filepath.Join(backupRoot, filepath.FromSlash(finalRel)), 0o644); err != nil {
			return "", nil, err
		}
		preserved[finalRel] = artifact.SHA256
	}
	return backupRoot, preserved, nil
}

func restoreUntouchedManualEditFinals(workspace editWorkspace, backupRoot string, preserved map[string]string) error {
	for finalRel, expectedSHA := range preserved {
		source := filepath.Join(backupRoot, filepath.FromSlash(finalRel))
		target := filepath.Join(workspace.Project, filepath.FromSlash(finalRel))
		if !pathWithinRoot(backupRoot, source) || !pathWithinRoot(workspace.Project, target) {
			return fmt.Errorf("preserved final SVG path escapes workspace: %s", finalRel)
		}
		if err := copyFile(source, target, 0o644); err != nil {
			return err
		}
		actualSHA, err := sha256File(target)
		if err != nil {
			return err
		}
		if actualSHA != expectedSHA {
			return fmt.Errorf("preserved final SVG hash mismatch: %s", finalRel)
		}
	}
	return nil
}

func (s *TaskService) runEditPPTXValidatePhase(ctx context.Context, task *model.Task, session *model.TaskEditSession, workspace editWorkspace, run *model.TaskEditRun) (map[string]any, error) {
	projectRel := filepath.ToSlash(filepath.Join("projects", workspace.ProjectName))
	command := fmt.Sprintf("python3 scripts/pptx_validate_runner.py %s --export-contract .slidesmith/contracts/finalize_export.json --phase-run-id %s", shellArg(projectRel), shellArg(run.ID))
	result, err := s.runEditAgent(ctx, session, workspace, model.EditPhasePPTXValidate, command, "")
	if err != nil {
		return runtimeEditOutput(result), err
	}
	contract, err := validatePPTXValidateContractForTask(workspace.Project, task, run.ID)
	if err != nil {
		return nil, err
	}
	contract["manual_edit_session_id"] = session.ID
	if err := bindManualEditPhaseHashes(workspace.Project, session, contract); err != nil {
		return nil, err
	}
	contract, err = bindFullPhaseContract(workspace.Project, PhasePPTXValidate, contract, task, &TaskWorkspace{HostDir: workspace.Root, ProjectPath: workspace.Project}, resultRunID(result))
	if err != nil {
		return nil, err
	}
	return map[string]any{"runtime": runtimeEditOutput(result), "contract": contract}, nil
}

func bindManualEditPhaseHashes(projectPath string, session *model.TaskEditSession, contract map[string]any) error {
	lockSHA, err := sha256File(filepath.Join(projectPath, ".slidesmith", "manual_edit_lock.json"))
	if err != nil {
		return err
	}
	contract["manual_edit_lock_sha256"] = lockSHA
	contract["manual_edit_patch_sha256"] = session.FrozenPatchSHA256
	contract["manual_edit_base_publish_version"] = session.BasePublishVersion
	return nil
}

func (s *TaskService) runEditAgent(ctx context.Context, session *model.TaskEditSession, workspace editWorkspace, phase, command, prompt string) (*AgentRunResult, error) {
	if !s.agentCfg.Enabled {
		return nil, fmt.Errorf("agent compose is disabled")
	}
	matched, err := s.repo.RenewEditSessionClaim(ctx, session.TaskID, session.ID, session.ExecutionClaimToken)
	if err != nil || !matched {
		if err == nil {
			err = repository.ErrConflict
		}
		return nil, err
	}
	req := AgentRunRequest{Phase: phase, Command: command, Prompt: prompt, WorkDir: workspace.Root, ComposeFile: workspace.CLICompose}
	if err := s.agent.Up(ctx, req); err != nil {
		return nil, fmt.Errorf("prepare edit runtime project: %w", err)
	}
	result, runErr := s.agent.Run(ctx, req)
	if runErr == nil && result != nil && result.WorkspacePath != "" {
		if syncErr := s.syncEditRuntimeProject(ctx, session, workspace, result.WorkspacePath); syncErr != nil {
			return result, syncErr
		}
	}
	return result, runErr
}

func (s *TaskService) syncEditRuntimeProject(ctx context.Context, session *model.TaskEditSession, workspace editWorkspace, runtimeWorkspacePath string) error {
	resolved, err := resolveRuntimeWorkspacePath(runtimeWorkspacePath)
	if err != nil {
		return err
	}
	source := filepath.Join(resolved, "projects", workspace.ProjectName)
	if sameFilesystemPath(source, workspace.Project) {
		return nil
	}
	if err := requireRealProjectDirectory(source, "edit runtime project"); err != nil {
		return err
	}
	stageRoot := filepath.Join(workspace.Root, ".slidesmith", "edit-sync", uuid.NewString())
	staged := filepath.Join(stageRoot, "project")
	if err := os.MkdirAll(stageRoot, 0o755); err != nil {
		return err
	}
	if err := copyProjectDirectoryStrict(ctx, source, staged); err != nil {
		_ = os.RemoveAll(stageRoot)
		return err
	}
	defer os.RemoveAll(stageRoot)
	matched, err := s.repo.RenewEditSessionClaim(ctx, session.TaskID, session.ID, session.ExecutionClaimToken)
	if err != nil || !matched {
		if err == nil {
			err = repository.ErrConflict
		}
		return err
	}
	if err := atomicExchangeDirectories(staged, workspace.Project); err != nil {
		return fmt.Errorf("promote edit runtime project: %w", err)
	}
	return nil
}

func runtimeEditOutput(result *AgentRunResult) map[string]any {
	if result == nil {
		return map[string]any{}
	}
	return map[string]any{"run_id": result.RunID, "session_id": result.SessionID, "status": result.Status, "exit_code": result.ExitCode}
}
func resultRunID(result *AgentRunResult) string {
	if result == nil {
		return ""
	}
	return result.RunID
}

func projectFilesContain(project, relative, needle string) (bool, error) {
	root := filepath.Join(project, relative)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(raw), needle) {
			return errFoundMarker
		}
		return nil
	})
	if errors.Is(err, errFoundMarker) {
		return true, nil
	}
	return false, err
}

var errFoundMarker = errors.New("marker found")

func (s *TaskService) buildManualEditDiffAndLock(ctx context.Context, task *model.Task, session *model.TaskEditSession, workspace editWorkspace) (map[string]any, map[string]any, error) {
	base, err := s.loadEditBaseInventory(ctx, task.ID, session.BasePublishVersion)
	if err != nil {
		return nil, nil, err
	}
	var draft ManualEditDraft
	if err := json.Unmarshal([]byte(session.DraftJSON), &draft); err != nil {
		return nil, nil, err
	}
	touched := map[string]bool{}
	for _, page := range draft.Pages {
		if len(page.Operations) > 0 {
			touched[page.PageID] = true
		}
	}
	for _, annotation := range draft.Annotations {
		touched[annotation.PageID] = true
	}
	pages := []map[string]any{}
	for _, page := range base.Inventory.Pages {
		path := filepath.Join(workspace.Project, filepath.FromSlash(page.Path))
		actual, err := sha256File(path)
		if err != nil {
			return nil, nil, err
		}
		changed := actual != page.SHA256
		if changed != touched[page.PageID] {
			return nil, nil, fmt.Errorf("manual edit ownership mismatch for %s: changed=%v touched=%v", page.PageID, changed, touched[page.PageID])
		}
		pages = append(pages, map[string]any{"page_id": page.PageID, "before_sha256": page.SHA256, "after_sha256": actual, "changed": changed, "owned": touched[page.PageID]})
	}
	immutableKinds := map[string]bool{model.ArtifactKindDesignSpec: true, model.ArtifactKindSpecLock: true, model.ArtifactKindResourceManifest: true, model.ArtifactKindResourceAsset: true, model.ArtifactKindChartData: true, model.ArtifactKindChartTemplate: true, model.ArtifactKindNotesInventory: true, model.ArtifactKindSpeakerNotes: true}
	for _, artifact := range base.Artifacts {
		if !immutableKinds[artifact.Kind] {
			continue
		}
		rel := materializedArtifactRelativePath(task.ID, session.BasePublishVersion, artifact.ObjectKey)
		actual, err := sha256File(filepath.Join(workspace.Project, filepath.FromSlash(rel)))
		if err != nil || actual != artifact.SHA256 {
			return nil, nil, fmt.Errorf("manual edit immutable artifact drift: %s", rel)
		}
	}
	diff := map[string]any{"schema": "slidesmith.manual_edit_diff_report.v1", "task_id": task.ID, "edit_session_id": session.ID, "base_publish_version": session.BasePublishVersion, "frozen_patch_sha256": session.FrozenPatchSHA256, "pages": pages, "touched_pages": boolMapKeys(touched), "findings": []any{}, "passed": true}
	diffPath := filepath.Join(workspace.Project, "analysis", "manual_edit_diff_report.json")
	if err := writeJSONPretty(diffPath, diff); err != nil {
		return nil, nil, err
	}
	patchSHA, err := sha256File(filepath.Join(workspace.Project, "analysis", "manual_edit_patch.json"))
	if err != nil {
		return nil, nil, err
	}
	applySHA, err := sha256File(filepath.Join(workspace.Project, "analysis", "manual_edit_apply_report.json"))
	if err != nil {
		return nil, nil, err
	}
	annotationSHA, err := sha256File(filepath.Join(workspace.Project, "analysis", "annotation_apply_report.json"))
	if err != nil {
		return nil, nil, err
	}
	diffSHA, err := sha256File(diffPath)
	if err != nil {
		return nil, nil, err
	}
	inventorySHA, err := sha256File(filepath.Join(workspace.Project, "analysis", "svg_inventory.json"))
	if err != nil {
		return nil, nil, err
	}
	lock := map[string]any{"schema": "slidesmith.manual_edit_lock.v1", "task_id": task.ID, "edit_session_id": session.ID, "base_publish_version": session.BasePublishVersion, "base_artifact_manifest_sha256": session.BaseArtifactManifestSHA256, "base_svg_inventory_sha256": session.BaseSVGInventorySHA256, "frozen_revision": session.FrozenRevision, "frozen_patch_sha256": session.FrozenPatchSHA256, "manual_edit_patch_sha256": patchSHA, "manual_edit_apply_report_sha256": applySHA, "annotation_apply_report_sha256": annotationSHA, "manual_edit_diff_report_sha256": diffSHA, "edited_svg_inventory_sha256": inventorySHA, "passed": true}
	if err := writeJSONPretty(filepath.Join(workspace.Project, ".slidesmith", "manual_edit_lock.json"), lock); err != nil {
		return nil, nil, err
	}
	return diff, lock, nil
}

func boolMapKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sortStrings(keys)
	return keys
}

func sha256StringBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

func (s *TaskService) publishManualEditVersion(ctx context.Context, task *model.Task, session *model.TaskEditSession, workspace editWorkspace, run *model.TaskEditRun) (map[string]any, error) {
	latest, err := s.repo.LatestArtifactVersion(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	if latest.Version != session.BasePublishVersion {
		return nil, fmt.Errorf("%w: manual edit parent is stale", repository.ErrConflict)
	}
	publishVersion := publishVersionName()
	published, err := s.publisher.Publish(ctx, task.ID, workspace.Root, publishVersion)
	if err != nil {
		return nil, err
	}
	cleanup := func() error {
		var cleanupErr error
		for _, artifact := range published {
			if err := s.storage.DeleteObject(context.WithoutCancel(ctx), artifact.ObjectKey); err != nil {
				cleanupErr = errors.Join(cleanupErr, err)
			}
		}
		return cleanupErr
	}
	contract, err := buildPublishedArtifactsContract(workspace.Project, s.storage, published, publishVersion, task.Route)
	if err != nil {
		return nil, errors.Join(err, cleanup())
	}
	lineage := map[string]any{"version_source": model.ArtifactVersionSourceManualEdit, "parent_publish_version": session.BasePublishVersion, "edit_session_id": session.ID, "edit_revision": session.FrozenRevision, "manual_edit_patch_sha256": session.FrozenPatchSHA256}
	lockSHA, err := sha256File(filepath.Join(workspace.Project, ".slidesmith", "manual_edit_lock.json"))
	if err != nil {
		return nil, errors.Join(err, cleanup())
	}
	lineage["manual_edit_lock_sha256"] = lockSHA
	for index := range published {
		published[index].ID = uuid.NewString()
		metadata := map[string]any{}
		_ = json.Unmarshal([]byte(published[index].MetadataJSON), &metadata)
		for key, value := range lineage {
			metadata[key] = value
		}
		published[index].MetadataJSON = encodeAnyJSON(metadata)
	}
	if err := validateManualEditPublishedKinds(published); err != nil {
		return nil, errors.Join(err, cleanup())
	}
	digest, err := artifactManifestDigest(published)
	if err != nil {
		return nil, errors.Join(err, cleanup())
	}
	pptxID := publishedPPTXArtifactID(published)
	if pptxID == "" {
		return nil, errors.Join(fmt.Errorf("manual edit publish missing pptx"), cleanup())
	}
	metadataJSON := encodeAnyJSON(lineage)
	version := &model.TaskArtifactVersion{TaskID: task.ID, Version: publishVersion, ParentVersion: session.BasePublishVersion, ArtifactManifestSHA256: digest, PPTXArtifactID: pptxID, EditSessionID: session.ID, EditRevision: session.FrozenRevision, MetadataJSON: metadataJSON}
	if err := s.repo.ActivateManualEditVersion(ctx, version, published, session.ID, session.ExecutionClaimToken); err != nil {
		return nil, errors.Join(err, cleanup())
	}
	session.Status, session.ResultPublishVersion = model.EditSessionStatusPublished, publishVersion
	_ = s.event(ctx, task.ID, model.EventTypeArtifact, "manual_edit_published", "Live Preview edit version published", map[string]any{"edit_session_id": session.ID, "parent_publish_version": session.BasePublishVersion, "publish_version": publishVersion, "artifact_manifest_sha256": digest})
	return map[string]any{"publish_version": publishVersion, "parent_publish_version": session.BasePublishVersion, "artifact_count": len(published), "contract": contract, "artifact_manifest_sha256": digest, "edit_run_id": run.ID}, nil
}

func validateManualEditPublishedKinds(artifacts []model.Artifact) error {
	required := map[string]bool{
		model.ArtifactKindDesignSpec: false, model.ArtifactKindSpecLock: false,
		model.ArtifactKindSVGOutput: false, model.ArtifactKindSVGFinal: false, model.ArtifactKindSVGInventory: false,
		model.ArtifactKindSVGQualityReport: false, model.ArtifactKindChartVerifyReport: false, model.ArtifactKindQualitySummary: false,
		model.ArtifactKindPPTXReadback: false, model.ArtifactKindPPTXTextInventory: false, model.ArtifactKindPPTXValidateReport: false, model.ArtifactKindPPTX: false,
		model.ArtifactKindManualEditPatch: false, model.ArtifactKindManualEditApplyReport: false, model.ArtifactKindAnnotationApplyReport: false,
		model.ArtifactKindManualEditDiffReport: false, model.ArtifactKindManualEditLock: false, model.ArtifactKindManualEditLog: false,
	}
	for _, artifact := range artifacts {
		if _, ok := required[artifact.Kind]; ok {
			required[artifact.Kind] = true
		}
	}
	for kind, found := range required {
		if !found {
			return fmt.Errorf("manual edit publish missing required artifact kind %s", kind)
		}
	}
	return nil
}

func (s *TaskService) RetryEditSession(ctx context.Context, taskID, sessionID, phase string) (*model.TaskEditSession, error) {
	if phase != "" {
		allowed := map[string]bool{model.EditPhaseMaterialize: true, model.EditPhaseApplyDirect: true, model.EditPhaseApplyAnnotation: true, model.EditPhaseSVGValidate: true, model.EditPhaseQualityCheck: true, model.EditPhaseFinalizeExport: true, model.EditPhasePPTXValidate: true, model.EditPhasePublish: true}
		if !allowed[phase] {
			return nil, fmt.Errorf("unsupported edit retry phase %q", phase)
		}
	}
	session, err := s.repo.RetryEditSession(ctx, taskID, sessionID)
	if err == nil {
		_ = s.event(ctx, taskID, model.EventTypeRuntime, "edit_session_retried", "Live Preview edit session retry queued", map[string]any{"edit_session_id": sessionID, "requested_phase": phase, "frozen_patch_sha256": session.FrozenPatchSHA256})
	}
	return session, err
}
