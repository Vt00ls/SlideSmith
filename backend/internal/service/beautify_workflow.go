package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

func (s *TaskService) processBeautifyInventory(ctx context.Context, task *model.Task) error {
	if task == nil || task.Route != model.TaskRouteBeautify || !s.beautifyCapabilitySnapshot(task).BeautifyEnabled || !s.useFullPPTMaster(task) {
		err := fmt.Errorf("beautify inventory requires an enabled, locked full-ppt-master beautify task")
		_ = s.failWithMetadata(ctx, task, string(PhaseBeautifyInventory)+".unsupported_profile", err, nil, nil)
		return err
	}
	if !s.agentCfg.Enabled {
		err := fmt.Errorf("agent compose disabled; worker cannot run %s", PhaseBeautifyInventory)
		_ = s.failWithMetadata(ctx, task, string(PhaseBeautifyInventory)+".agent_disabled", err, nil, nil)
		return err
	}
	workspace := s.resolveTaskWorkspace(task)
	if err := s.validateTaskRuntimeProfile(task, workspace); err != nil {
		_ = s.failWithMetadata(ctx, task, failurePhaseRuntimeProfileMismatch, err, nil, map[string]any{"phase": PhaseBeautifyInventory})
		return err
	}
	projectPath, err := s.findPersistentProjectPath(task)
	if err != nil {
		_ = s.failWithMetadata(ctx, task, "beautify_inventory.inputs", err, nil, nil)
		return err
	}
	projectRel := s.projectRel(task, projectPath)
	phaseRun, err := s.beginPhaseRun(ctx, task, PhaseBeautifyInventory, PhaseRunnerWorker, map[string]any{
		"project_path": projectRel,
		"task_id":      task.ID,
		"route":        task.Route,
	})
	if err != nil {
		return err
	}
	command := fmt.Sprintf(
		"python3 scripts/beautify_runner.py inventory %s --task-id %s --runner-profile %s --phase-run-id %s --skill-root %s",
		shellArg(projectRel), shellArg(task.ID), shellArg(task.RunnerProfile), shellArg(phaseRun.ID), shellArg("skills/ppt-master"),
	)
	phaseRun.InputJSON = encodeAnyJSON(map[string]any{
		"command":      command,
		"project_path": projectRel,
		"task_id":      task.ID,
		"route":        task.Route,
	})
	if err := s.repo.SavePhaseRun(ctx, phaseRun); err != nil {
		return err
	}
	run, runErr := s.runAgent(ctx, task, string(PhaseBeautifyInventory), AgentRunRequest{
		Command:     command,
		WorkDir:     workspace.HostDir,
		ComposeFile: workspace.CLIComposeFile,
	})
	applyRuntimeRunToPhaseRun(phaseRun, run)
	if persistErr := s.applyRuntimeRunToTask(ctx, task, run); persistErr != nil {
		_ = s.finishPhaseRun(context.WithoutCancel(ctx), phaseRun, PhaseRunStatusFailed, runtimeRunPhaseOutput(run), persistErr)
		if !errors.Is(persistErr, errTaskStateChanged) {
			_ = s.failWithMetadata(context.WithoutCancel(ctx), task, "beautify_inventory.command", persistErr, run, map[string]any{"project_path": projectRel})
		}
		return persistErr
	}
	if runErr != nil {
		_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusFailed, runtimeRunPhaseOutput(run), runErr)
		_ = s.failWithMetadata(ctx, task, "beautify_inventory.command", runErr, run, map[string]any{"project_path": projectRel})
		return runErr
	}
	if run != nil && run.WorkspacePath != "" {
		projectPath, err = s.syncRuntimeProject(ctx, task, workspace, run.WorkspacePath)
		if err != nil {
			_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusFailed, runtimeRunPhaseOutput(run), err)
			_ = s.failWithMetadata(ctx, task, "beautify_inventory.command", err, run, map[string]any{"project_path": projectRel})
			return err
		}
		projectRel = s.projectRel(task, projectPath)
	}
	inputs, err := ValidateBeautifyInputsContract(projectPath, task.ID)
	if err != nil {
		_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusFailed, runtimeRunPhaseOutput(run), err)
		_ = s.failWithMetadata(ctx, task, "beautify_inventory.inputs", err, run, map[string]any{"project_path": projectRel})
		return err
	}
	inventory, err := ValidateBeautifyInventoryContract(projectPath, task.ID)
	if err != nil {
		_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusFailed, runtimeRunPhaseOutput(run), err)
		_ = s.failWithMetadata(ctx, task, "beautify_inventory.contract", err, run, map[string]any{"project_path": projectRel})
		return err
	}
	if err := s.seedBeautifyConfirmations(ctx, task, projectPath); err != nil {
		_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusFailed, runtimeRunPhaseOutput(run), err)
		_ = s.failWithMetadata(ctx, task, "beautify_inventory.contract", err, run, map[string]any{"project_path": projectRel})
		return err
	}
	if err := s.publishBeautifyPhaseArtifacts(ctx, task, projectPath, phaseRun.ID, PhaseBeautifyInventory); err != nil {
		_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusFailed, runtimeRunPhaseOutput(run), err)
		_ = s.failWithMetadata(ctx, task, "beautify_inventory.publish", err, run, map[string]any{"project_path": projectRel})
		return err
	}
	output := runtimeRunPhaseOutput(run)
	output["project_path"] = projectRel
	output["inputs_contract"] = inputs
	output["inventory_contract"] = inventory
	if err := s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusSucceeded, output, nil); err != nil {
		return err
	}
	if err := s.transition(ctx, task, model.TaskStatusAwaitingAnchorConfirm, "Awaiting source-seeded Beautify confirmation", map[string]any{
		"phase_run_id": phaseRun.ID,
		"project_path": projectRel,
	}); err != nil {
		return err
	}
	return s.event(ctx, task.ID, model.EventTypeConfirmation, "beautify_identity_seeded", "Beautify confirmations seeded from source identity", map[string]any{
		"seed_source":  "beautify_identity",
		"project_path": projectRel,
	})
}

func (s *TaskService) processBeautifyPlan(ctx context.Context, task *model.Task) error {
	if task == nil || task.Route != model.TaskRouteBeautify || !s.beautifyCapabilitySnapshot(task).BeautifyEnabled || !s.useFullPPTMaster(task) {
		err := fmt.Errorf("beautify plan requires an enabled, locked full-ppt-master beautify task")
		_ = s.failWithMetadata(ctx, task, "beautify_plan.agent", err, nil, nil)
		return err
	}
	if !s.agentCfg.Enabled {
		err := fmt.Errorf("agent compose disabled; worker cannot run %s", PhaseBeautifyPlan)
		_ = s.failWithMetadata(ctx, task, "beautify_plan.agent", err, nil, nil)
		return err
	}
	workspace := s.resolveTaskWorkspace(task)
	if err := s.validateTaskRuntimeProfile(task, workspace); err != nil {
		_ = s.failWithMetadata(ctx, task, failurePhaseRuntimeProfileMismatch, err, nil, map[string]any{"phase": PhaseBeautifyPlan})
		return err
	}
	projectPath, err := s.findPersistentProjectPath(task)
	if err != nil {
		_ = s.failWithMetadata(ctx, task, "beautify_plan.contract", err, nil, nil)
		return err
	}
	projectRel := s.projectRel(task, projectPath)
	if _, err := ValidateBeautifyInventoryContract(projectPath, task.ID); err != nil {
		_ = s.failWithMetadata(ctx, task, "beautify_plan.contract", err, nil, map[string]any{"project_path": projectRel})
		return err
	}
	if err := s.writeConfirmationResult(ctx, task); err != nil {
		_ = s.failWithMetadata(ctx, task, "beautify_plan.contract", err, nil, map[string]any{"project_path": projectRel})
		return err
	}
	phaseRun, err := s.beginPhaseRun(ctx, task, PhaseBeautifyPlan, PhaseRunnerAgent, map[string]any{
		"project_path": projectRel,
		"task_id":      task.ID,
		"route":        task.Route,
	})
	if err != nil {
		return err
	}
	run, runErr := s.runAgent(ctx, task, string(PhaseBeautifyPlan), AgentRunRequest{
		Prompt:      s.fullPPTMasterBeautifyPlanPrompt(task, projectPath),
		WorkDir:     workspace.HostDir,
		ComposeFile: workspace.CLIComposeFile,
		Detached:    true,
	})
	applyRuntimeRunToPhaseRun(phaseRun, run)
	if persistErr := s.applyRuntimeRunToTask(ctx, task, run); persistErr != nil {
		_ = s.finishPhaseRun(context.WithoutCancel(ctx), phaseRun, PhaseRunStatusFailed, runtimeRunPhaseOutput(run), persistErr)
		return persistErr
	}
	if runErr != nil {
		_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusFailed, runtimeRunPhaseOutput(run), runErr)
		_ = s.failWithMetadata(ctx, task, "beautify_plan.agent", runErr, run, map[string]any{"project_path": projectRel})
		return runErr
	}
	if run != nil && run.WorkspacePath != "" {
		projectPath, err = s.syncRuntimeProject(ctx, task, workspace, run.WorkspacePath)
		if err != nil {
			_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusFailed, runtimeRunPhaseOutput(run), err)
			_ = s.failWithMetadata(ctx, task, "beautify_plan.agent", err, run, map[string]any{"project_path": projectRel})
			return err
		}
		projectRel = s.projectRel(task, projectPath)
	}
	contract, err := ValidateBeautifyPlanContract(projectPath, task.ID)
	if err != nil {
		_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusFailed, runtimeRunPhaseOutput(run), err)
		_ = s.failWithMetadata(ctx, task, "beautify_plan.contract", err, run, map[string]any{"project_path": projectRel})
		return err
	}
	if err := s.publishBeautifyPhaseArtifacts(ctx, task, projectPath, phaseRun.ID, PhaseBeautifyPlan); err != nil {
		_ = s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusFailed, runtimeRunPhaseOutput(run), err)
		_ = s.failWithMetadata(ctx, task, "beautify_plan.publish", err, run, map[string]any{"project_path": projectRel})
		return err
	}
	output := runtimeRunPhaseOutput(run)
	output["project_path"] = projectRel
	output["plan_contract"] = contract
	if err := s.finishPhaseRun(ctx, phaseRun, PhaseRunStatusSucceeded, output, nil); err != nil {
		return err
	}
	if err := s.transition(ctx, task, model.TaskStatusAwaitingBeautifyConfirm, "Awaiting Beautify plan confirmation", map[string]any{
		"phase_run_id": phaseRun.ID,
		"project_path": projectRel,
	}); err != nil {
		return err
	}
	return s.event(ctx, task.ID, model.EventTypeConfirmation, "beautify_plan_ready", "Beautify plan is ready for confirmation", map[string]any{"project_path": projectRel})
}

func (s *TaskService) retryBeautifyInventory(ctx context.Context, task *model.Task) (*model.Task, error) {
	if task == nil || task.Route != model.TaskRouteBeautify || !s.beautifyCapabilitySnapshot(task).BeautifyEnabled {
		return nil, fmt.Errorf("beautify inventory retry requires an enabled beautify task")
	}
	if err := s.ensureTaskRunnerProfile(ctx, task); err != nil {
		return nil, err
	}
	projectPath, err := s.findPersistentProjectPath(task)
	if err != nil {
		return nil, err
	}
	if err := cleanupFullPPTMasterOutputsForRetry(projectPath, PhaseSpecGenerate, task.Route); err != nil {
		return nil, err
	}
	for _, path := range []string{
		filepath.Join(projectPath, ".slidesmith", "contracts", "beautify_inputs.json"),
		filepath.Join(projectPath, ".slidesmith", "contracts", "beautify_inventory.json"),
		filepath.Join(projectPath, ".slidesmith", "contracts", "beautify_plan.json"),
		filepath.Join(projectPath, ".slidesmith", "beautify_lock.json"),
		filepath.Join(projectPath, "analysis", "beautify_inventory.json"),
		filepath.Join(projectPath, "analysis", "beautify_risk_report.json"),
		filepath.Join(projectPath, "analysis", "beautify_plan.json"),
		filepath.Join(projectPath, "confirm_ui"),
	} {
		if err := removeRetryPath(projectPath, path); err != nil {
			return nil, err
		}
	}
	if err := s.transition(ctx, task, model.TaskStatusBeautifyInventoryBuilding, "Retry queued from Beautify inventory", map[string]any{"retry_phase": retryPhaseBeautifyInventory}); err != nil {
		return nil, err
	}
	_ = s.event(ctx, task.ID, model.EventTypeRuntime, "queued", "Beautify inventory retry queued for worker", map[string]any{"retry_phase": retryPhaseBeautifyInventory})
	return task, nil
}

func (s *TaskService) retryBeautifyPlan(ctx context.Context, task *model.Task) (*model.Task, error) {
	if task == nil || task.Route != model.TaskRouteBeautify || !s.beautifyCapabilitySnapshot(task).BeautifyEnabled {
		return nil, fmt.Errorf("beautify plan retry requires an enabled beautify task")
	}
	if err := s.ensureTaskRunnerProfile(ctx, task); err != nil {
		return nil, err
	}
	projectPath, err := s.findPersistentProjectPath(task)
	if err != nil {
		return nil, err
	}
	if _, err := ValidateBeautifyInventoryContract(projectPath, task.ID); err != nil {
		return nil, fmt.Errorf("cannot retry Beautify plan with stale inventory: %w", err)
	}
	if err := cleanupFullPPTMasterOutputsForRetry(projectPath, PhaseSpecGenerate, task.Route); err != nil {
		return nil, err
	}
	for _, path := range []string{
		filepath.Join(projectPath, ".slidesmith", "contracts", "beautify_plan.json"),
		filepath.Join(projectPath, ".slidesmith", "beautify_lock.json"),
		filepath.Join(projectPath, "analysis", "beautify_plan.json"),
	} {
		if err := removeRetryPath(projectPath, path); err != nil {
			return nil, err
		}
	}
	if err := s.transition(ctx, task, model.TaskStatusBeautifyPlanning, "Retry queued from Beautify plan", map[string]any{"retry_phase": retryPhaseBeautifyPlan}); err != nil {
		return nil, err
	}
	_ = s.event(ctx, task.ID, model.EventTypeRuntime, "queued", "Beautify plan retry queued for worker", map[string]any{"retry_phase": retryPhaseBeautifyPlan})
	return task, nil
}

func removeRetryPath(projectPath, path string) error {
	root, err := filepath.Abs(projectPath)
	if err != nil {
		return err
	}
	candidate, err := filepath.Abs(path)
	if err != nil || !pathWithinRoot(root, candidate) || candidate == root {
		return fmt.Errorf("Beautify retry path is outside project")
	}
	info, err := os.Lstat(candidate)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("Beautify retry path must not be a symlink")
	}
	return os.RemoveAll(candidate)
}

func (s *TaskService) fullPPTMasterBeautifyPlanPrompt(task *model.Task, projectPath string) string {
	projectRel := s.projectRel(task, projectPath)
	return fmt.Sprintf(`You are the SlideSmith Beautify planning agent.

Read %[2]s/.slidesmith/contracts/beautify_inputs.json, analysis/beautify_inventory.json, analysis/beautify_risk_report.json, .slidesmith/contracts/beautify_inventory.json, confirm_ui/result.json, and the source identity file referenced by the inputs contract.

Create only %[2]s/analysis/beautify_plan.json using schema slidesmith.beautify_plan.v1 for task %[1]s.

The document must use exactly this machine shape (replace example values with live IDs and hashes):
{
  "schema": "slidesmith.beautify_plan.v1",
  "task_id": "%[1]s",
  "status": "draft",
  "revision": 1,
  "source_pptx_sha256": "<64 lowercase hex>",
  "inventory_sha256": "<64 lowercase hex>",
  "confirmation_sha256": "<64 lowercase hex>",
  "slide_count": 1,
  "identity": {
    "source": "source-replica",
    "canvas_override": false,
    "palette_override": false,
    "typography_override": false
  },
  "slides": [{
    "source_slide": 1,
    "output_page": 1,
    "page_role": "cover",
    "page_rhythm": "anchor",
    "layout_strategy": "visual-only layout strategy",
    "text_block_ids": ["text.id"],
    "image_ids": ["image.id"],
    "table_ids": [],
    "chart_ids": [],
    "ignored": [{"id": "allowed.chrome.id", "reason": "explicit non-content reason"}],
    "unsupported": [{"id": "unsupported.semantic.id", "reason": "explicit handling reason"}],
    "risks": ["risk.id"]
  }],
  "global_ignored": [],
  "accepted_risks": ["risk.id"],
  "created_at": "<RFC3339 timestamp>"
}

Hard rules:
- status is draft; source_pptx_sha256, inventory_sha256, and confirmation_sha256 are live lowercase SHA-256 values.
- slide_count equals the source count and slides map source_slide == output_page continuously 1..N.
- Account for every inventory text block, table, chart, and image ID exactly once on its owning slide.
- Text, tables, charts, and required source images cannot be ignored, rewritten, moved, merged, split, or reordered.
- layout_strategy and page_rhythm may change visual structure only. Do not include content rewriting instructions.
- Every array field must be a JSON array, never null. Empty arrays must be written as [].
- accepted_risks and each slide risks field are arrays of risk ID strings only; objects are forbidden.
- ignored, unsupported, and global_ignored are arrays of objects with exactly string id and reason fields.
- ignored may contain only explicit non-content source chrome or supported semantic omissions with a reason. unsupported and accepted_risks must reference live risk-report IDs.
- identity must contain exactly source plus the three boolean override fields shown above; do not embed canvas, theme, palette, fonts, sizes, confirmations, or other nested objects.
- Identity defaults to the confirmed source identity and records only confirmed overrides.
- Preserve all unknown/complex objects in unsupported/risks; never silently drop them.
- Do not write design_spec.md, spec_lock.md, resource_plan.json, SVG, resources, validation, or PPTX files. Do not access the network.

Validate the JSON and exact accounting with shell commands before stopping.`, task.ID, projectRel)
}

func (s *TaskService) seedBeautifyConfirmations(ctx context.Context, task *model.Task, projectPath string) error {
	inputs := readJSONMap(filepath.Join(projectPath, ".slidesmith", "contracts", "beautify_inputs.json"))
	identityPath := beautifyContractRelativePath(inputs, "source_identity", "identity", "identity_path")
	identity := map[string]any{}
	if identityPath != "" {
		identity = readJSONMap(filepath.Join(projectPath, filepath.FromSlash(identityPath)))
	}
	pageCount := beautifyContractInt(inputs, "source_slide_count", "slide_count", "page_count")
	if pageCount <= 0 {
		pageCount = beautifyContractInt(identity, "slide_count")
	}
	if pageCount <= 0 {
		return fmt.Errorf("beautify source slide count is missing")
	}
	canvas := beautifyCanvasRecommendation(inputs, identity)
	palette := beautifyIdentitySummary(identity, "theme", "observed")
	typography := beautifyTypographyRecommendation(identity)
	definitions := []model.TaskConfirmation{
		{Key: "canvas", Label: "源画布", Required: true, OptionsJSON: encodeAnyJSON([]string{canvas, "ppt169", "ppt43"}), Recommendation: canvas},
		{Key: "language", Label: "语言", Required: true, OptionsJSON: `["zh-CN","en-US"]`, Recommendation: "zh-CN"},
		{Key: "audience", Label: "目标受众", Required: true, Recommendation: "保持源演示的受众和表达目的。"},
		{Key: "content_divergence", Label: "内容边界", Required: true, Recommendation: "逐页逐字保留可见文字、表格和图表数据；只调整视觉。"},
		{Key: "delivery_purpose", Label: "阅读场景", Required: true, OptionsJSON: `["text","balanced","presentation"]`, Recommendation: "balanced"},
		{Key: "mode", Label: "叙事模式", Required: true, OptionsJSON: `["source-structure"]`, Recommendation: "source-structure"},
		{Key: "visual_style", Label: "视觉身份", Required: true, OptionsJSON: `["source-replica","observed-identity","content-matched-alternative"]`, Recommendation: "source-replica"},
		{Key: "page_count", Label: "源页数（锁定）", Required: true, OptionsJSON: `[]`, Recommendation: strconv.Itoa(pageCount)},
		{Key: "color", Label: "配色", Required: true, Recommendation: palette},
		{Key: "typography", Label: "字体", Required: true, Recommendation: typography},
		{Key: "icons", Label: "图标", Required: true, OptionsJSON: `["none","tabler-outline"]`, Recommendation: "none"},
		{Key: "formula_policy", Label: "公式", Required: true, OptionsJSON: `["none","mixed","render-all"]`, Recommendation: "none"},
		{Key: "image_usage", Label: "源图片策略", Required: true, OptionsJSON: `["provided","none","web","ai"]`, Recommendation: "provided"},
		{Key: "image_notes", Label: "图片说明", Required: false, Recommendation: "默认复用源 PPTX 中的 required 图片 occurrence。"},
		{Key: "generation_mode", Label: "生成模式", Required: true, OptionsJSON: `["continuous"]`, Recommendation: "continuous"},
		{Key: "refine_spec", Label: "规格预览", Required: false, OptionsJSON: `[false]`, Recommendation: "false"},
	}
	if err := s.repo.UpsertConfirmationDefinitions(ctx, task.ID, definitions); err != nil {
		return err
	}
	return s.event(ctx, task.ID, model.EventTypeConfirmation, "beautify_confirmation_seed", "Source identity seeded Beautify confirmation definitions", map[string]any{
		"seed_source":        "beautify_identity",
		"source_pptx_sha256": beautifyContractString(inputs, "source_pptx_sha256"),
		"locked_page_count":  pageCount,
	})
}

func beautifyContractRelativePath(value map[string]any, keys ...string) string {
	for _, key := range keys {
		switch typed := value[key].(type) {
		case string:
			if safeBeautifyRelativePath(typed) {
				return filepath.ToSlash(filepath.Clean(filepath.FromSlash(typed)))
			}
		case map[string]any:
			for _, nestedKey := range []string{"path", "relative_path"} {
				if path, _ := typed[nestedKey].(string); safeBeautifyRelativePath(path) {
					return filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
				}
			}
		}
	}
	return ""
}

func safeBeautifyRelativePath(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || filepath.IsAbs(value) || strings.Contains(value, "://") {
		return false
	}
	clean := filepath.Clean(filepath.FromSlash(value))
	return clean != "." && clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func beautifyContractInt(value map[string]any, keys ...string) int {
	for _, key := range keys {
		switch typed := value[key].(type) {
		case float64:
			return int(typed)
		case int:
			return typed
		case string:
			if parsed, err := strconv.Atoi(typed); err == nil {
				return parsed
			}
		}
	}
	return 0
}

func beautifyContractString(value map[string]any, key string) string {
	switch typed := value[key].(type) {
	case string:
		return typed
	case map[string]any:
		if text, _ := typed["sha256"].(string); text != "" {
			return text
		}
	}
	return ""
}

func beautifyCanvasRecommendation(inputs, identity map[string]any) string {
	for _, value := range []map[string]any{identity, inputs} {
		if canvas, ok := value["canvas"].(string); ok && strings.TrimSpace(canvas) != "" {
			return canvas
		}
		if canvas, ok := value["source_canvas"].(string); ok && strings.TrimSpace(canvas) != "" {
			return canvas
		}
		var canvas map[string]any
		if candidate, ok := value["canvas"].(map[string]any); ok {
			canvas = candidate
		} else if candidate, ok := value["source_canvas"].(map[string]any); ok {
			canvas = candidate
		}
		if canvas != nil {
			width, _ := canvas["width"].(float64)
			height, _ := canvas["height"].(float64)
			if height > 0 {
				ratio := width / height
				if ratio > 1.55 {
					return "ppt169"
				}
				return "ppt43"
			}
		}
	}
	return "ppt169"
}

func beautifyIdentitySummary(identity map[string]any, keys ...string) string {
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		if value, ok := identity[key]; ok {
			raw, _ := json.Marshal(value)
			if len(raw) > 240 {
				raw = raw[:240]
			}
			if len(raw) > 0 && string(raw) != "null" && string(raw) != "{}" {
				parts = append(parts, key+":"+string(raw))
			}
		}
	}
	if len(parts) == 0 {
		return "source identity"
	}
	return strings.Join(parts, " | ")
}

func beautifyTypographyRecommendation(identity map[string]any) string {
	observed, _ := identity["observed"].(map[string]any)
	for _, key := range []string{"fonts", "font_families"} {
		if value, ok := observed[key]; ok {
			raw, _ := json.Marshal(value)
			if len(raw) > 0 && len(raw) <= 240 {
				return "source observed fonts: " + string(raw)
			}
		}
	}
	return "source theme fonts with measured pt-to-px conversion"
}

type beautifyPublishCandidate struct {
	RelativePath string
	Kind         string
}

func (s *TaskService) publishBeautifyPhaseArtifacts(ctx context.Context, task *model.Task, projectPath, phaseRunID string, phase PipelinePhase) error {
	if task == nil || task.Route != model.TaskRouteBeautify || phaseRunID == "" {
		return fmt.Errorf("beautify publisher requires a beautify task and phase run")
	}
	candidates := []beautifyPublishCandidate{}
	switch phase {
	case PhaseBeautifyInventory:
		candidates = []beautifyPublishCandidate{
			{RelativePath: ".slidesmith/contracts/beautify_inputs.json", Kind: model.ArtifactKindBeautifyInputs},
			{RelativePath: "analysis/beautify_inventory.json", Kind: model.ArtifactKindBeautifyInventory},
			{RelativePath: "analysis/beautify_risk_report.json", Kind: model.ArtifactKindBeautifyRiskReport},
			{RelativePath: ".slidesmith/contracts/beautify_inventory.json", Kind: model.ArtifactKindManifest},
		}
	case PhaseBeautifyPlan:
		candidates = []beautifyPublishCandidate{
			{RelativePath: "analysis/beautify_plan.json", Kind: model.ArtifactKindBeautifyPlan},
			{RelativePath: ".slidesmith/contracts/beautify_plan.json", Kind: model.ArtifactKindManifest},
		}
		if fileExists(filepath.Join(projectPath, ".slidesmith", "beautify_lock.json")) {
			candidates = append(candidates, beautifyPublishCandidate{RelativePath: ".slidesmith/beautify_lock.json", Kind: model.ArtifactKindBeautifyLock})
		}
	default:
		return fmt.Errorf("unsupported beautify publish phase %q", phase)
	}
	projectRoot, err := filepath.Abs(projectPath)
	if err != nil {
		return err
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].RelativePath < candidates[j].RelativePath })
	prefix := filepath.ToSlash(filepath.Join("tasks", task.ID, "beautify", string(phase))) + "/"
	artifacts := make([]model.Artifact, 0, len(candidates))
	for _, candidate := range candidates {
		if !safeBeautifyRelativePath(candidate.RelativePath) {
			return fmt.Errorf("beautify artifact path is unsafe")
		}
		path := filepath.Join(projectRoot, filepath.FromSlash(candidate.RelativePath))
		info, resolved, err := inspectContainedPath(projectRoot, path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() <= 0 {
			return fmt.Errorf("beautify artifact %s is not a non-empty regular file", filepath.Base(candidate.RelativePath))
		}
		objectKey := prefix + phaseRunID + "/" + candidate.RelativePath
		stored, err := s.storage.CopyFileToObject(ctx, objectKey, resolved)
		if err != nil {
			return err
		}
		metadata, _ := json.Marshal(map[string]any{
			"schema":                "slidesmith.beautify_artifact_metadata.v1",
			"phase":                 phase,
			"phase_run_id":          phaseRunID,
			"project_relative_path": candidate.RelativePath,
			"route":                 task.Route,
		})
		artifacts = append(artifacts, model.Artifact{
			TaskID: task.ID, Kind: candidate.Kind, Name: filepath.Base(candidate.RelativePath), Storage: "local",
			ObjectKey: stored.ObjectKey, MimeType: stored.MimeType, Size: stored.Size, SHA256: stored.SHA256,
			MetadataJSON: string(metadata),
		})
	}
	if err := s.repo.ReplaceArtifactsByObjectKeyPrefix(ctx, task.ID, prefix, artifacts); err != nil {
		return err
	}
	return s.event(ctx, task.ID, model.EventTypeArtifact, "beautify_artifacts_published", "Beautify phase artifacts published", map[string]any{
		"phase": phase, "phase_run_id": phaseRunID, "artifact_count": len(artifacts),
	})
}

func readBeautifyPlanMap(projectPath string) (map[string]any, error) {
	raw, err := os.ReadFile(filepath.Join(projectPath, "analysis", "beautify_plan.json"))
	if err != nil {
		return nil, err
	}
	var plan map[string]any
	if err := json.Unmarshal(raw, &plan); err != nil {
		return nil, err
	}
	return plan, nil
}

func validateBeautifySpecGenerateContract(projectPath, expectedTaskID string) (map[string]any, error) {
	lock, err := ValidateBeautifyLock(projectPath, expectedTaskID)
	if err != nil {
		return nil, fmt.Errorf("spec_generate.beautify_fidelity: %w", err)
	}
	lockSHA, err := sha256File(filepath.Join(projectPath, ".slidesmith", "beautify_lock.json"))
	if err != nil {
		return nil, err
	}
	planContract, err := validateExistingBeautifyPlanContract(projectPath, expectedTaskID)
	if err != nil {
		return nil, fmt.Errorf("spec_generate.beautify_fidelity: %w", err)
	}
	plan, _, err := validateResourcePlanContract(projectPath, expectedTaskID)
	if err != nil {
		return nil, err
	}
	if plan.PageCount != lock.SlideCount {
		return nil, fmt.Errorf("spec_generate.beautify_fidelity: resource plan page_count %d does not match locked %d", plan.PageCount, lock.SlideCount)
	}
	for _, item := range plan.Requirements {
		if item.Type == "template_asset" || item.AcquireVia == "template" {
			return nil, fmt.Errorf("resource %s uses %q/%q in spec_generate; Beautify v1 has no platform template resolution, so bind source-deck assets as type image with acquire_via source", item.ID, item.Type, item.AcquireVia)
		}
	}
	designRaw, err := os.ReadFile(filepath.Join(projectPath, "design_spec.md"))
	if err != nil {
		return nil, err
	}
	specLockRaw, err := os.ReadFile(filepath.Join(projectPath, "spec_lock.md"))
	if err != nil {
		return nil, err
	}
	design := string(designRaw)
	specLock := string(specLockRaw)
	coverage := map[string]int{"pages": 0, "text_blocks": 0, "table_cells": 0, "charts": 0, "required_images": 0}
	for _, slide := range lock.Slides {
		pageID := fmt.Sprintf("P%02d", slide.OutputPage)
		if !beautifySpecPageDeclared(design, pageID) || !beautifySpecPageDeclared(specLock, pageID) {
			return nil, fmt.Errorf("spec_generate.beautify_fidelity: locked page %s is missing from design/spec lock", pageID)
		}
		coverage["pages"]++
		for _, block := range slide.TextBlocks {
			if !beautifySpecTextBlockPreserved(design, block) || !beautifySpecTextBlockPreserved(specLock, block) {
				return nil, fmt.Errorf("spec_generate.beautify_fidelity: page %s text block %s is not preserved verbatim", pageID, block.ID)
			}
			coverage["text_blocks"]++
		}
		for _, table := range slide.Tables {
			for rowIndex, row := range table.Cells {
				for columnIndex, cell := range row {
					if cell != "" && (!strings.Contains(design, cell) || !strings.Contains(specLock, cell)) {
						return nil, fmt.Errorf("spec_generate.beautify_fidelity: page %s table %s cell %d,%d is not preserved", pageID, table.ID, rowIndex+1, columnIndex+1)
					}
					coverage["table_cells"]++
				}
			}
		}
		for _, chart := range slide.Charts {
			if !beautifyResourcePlanContainsChartData(plan, slide.OutputPage, chart) {
				return nil, fmt.Errorf("spec_generate.beautify_fidelity: page %s chart %s has no exact frozen chart_data resource", pageID, chart.ID)
			}
			coverage["charts"]++
		}
		for _, image := range slide.Images {
			if !image.Required || beautifyDecisionContains(lock.Ignored, slide.SourceSlide, image.ID) || beautifyDecisionContains(lock.Unsupported, slide.SourceSlide, image.ID) {
				continue
			}
			if !beautifyResourcePlanContainsSourceImage(plan, slide.OutputPage, image) {
				return nil, fmt.Errorf("spec_generate.beautify_fidelity: page %s required image %s lacks source occurrence lineage", pageID, image.ID)
			}
			coverage["required_images"]++
		}
	}
	return map[string]any{
		"route":                        model.TaskRouteBeautify,
		"beautify_lock_sha256":         lockSHA,
		"beautify_plan_sha256":         planContract.PlanSHA256,
		"beautify_inventory_sha256":    lock.InventorySHA256,
		"beautify_inputs_sha256":       lock.InputsSHA256,
		"beautify_identity_sha256":     lock.IdentitySHA256,
		"source_slide_count":           lock.SlideCount,
		"source_to_spec_mapping":       append([]int(nil), lock.SlideOrder...),
		"frozen_content_data_coverage": coverage,
	}, nil
}

func beautifySpecPageDeclared(markdown, pageID string) bool {
	pageID = strings.TrimSpace(pageID)
	if pageID == "" {
		return false
	}
	pattern := regexp.MustCompile(`(?m)^\s*(?:(?:#{1,6}|[-*+])\s+)?` + regexp.QuoteMeta(pageID) + `(?:\b|\s|[|:#-])`)
	return pattern.MatchString(markdown)
}

func beautifySpecTextBlockPreserved(markdown string, block BeautifyInventoryText) bool {
	units := block.Paragraphs
	if len(units) == 0 {
		units = []string{block.Text}
	}
	preserved := false
	for _, unit := range units {
		if unit == "" {
			continue
		}
		preserved = true
		if !strings.Contains(markdown, unit) {
			return false
		}
	}
	return preserved
}

func beautifyResourcePlanContainsChartData(plan *resourcePlan, page int, chart BeautifyInventoryChart) bool {
	expected := map[string]any{"categories": chart.Categories, "series": chart.Series}
	expectedSHA, err := beautifyJSONSHA256(expected)
	if err != nil {
		return false
	}
	for _, item := range plan.Requirements {
		if item.Page != page || item.Type != "chart_data" || item.AcquireVia != "source" || !resourceCitationPresent(item.Citation) {
			continue
		}
		actualSHA, hashErr := beautifyJSONSHA256(item.Data)
		if hashErr == nil && actualSHA == expectedSHA {
			return true
		}
	}
	return false
}

func beautifyResourcePlanContainsSourceImage(plan *resourcePlan, page int, image BeautifyInventoryImage) bool {
	for _, item := range plan.Requirements {
		if item.Page != page || item.Type != "image" || item.AcquireVia != "source" || !safeBeautifyRelativePath(item.SourceReference) {
			continue
		}
		if image.SourcePath != "" && filepath.ToSlash(filepath.Clean(filepath.FromSlash(item.SourceReference))) != filepath.ToSlash(filepath.Clean(filepath.FromSlash(image.SourcePath))) {
			continue
		}
		if image.Filename != "" && image.SourcePath == "" && filepath.Base(item.SourceReference) != filepath.Base(image.Filename) {
			continue
		}
		return true
	}
	return false
}
