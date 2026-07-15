package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/model"
)

type BeautifySourceSummary struct {
	Name       string `json:"name"`
	SHA256     string `json:"sha256"`
	SlideCount int    `json:"slide_count"`
	Canvas     string `json:"canvas"`
}

type BeautifyIdentitySummary struct {
	SelectedSource string   `json:"selected_source"`
	Canvas         string   `json:"canvas"`
	Palette        []string `json:"palette"`
	Fonts          []string `json:"fonts"`
	Overrides      []string `json:"overrides"`
}

type BeautifyInventoryPageSummary struct {
	SourceSlide            int `json:"source_slide"`
	TextCount              int `json:"text_count"`
	ImageCount             int `json:"image_count"`
	TableCount             int `json:"table_count"`
	ChartCount             int `json:"chart_count"`
	IgnoredCount           int `json:"ignored_count"`
	UnsupportedCount       int `json:"unsupported_count"`
	NeedsConfirmationCount int `json:"needs_confirmation_count"`
}

type BeautifyRiskSummary struct {
	ID          string `json:"id"`
	SourceSlide int    `json:"source_slide"`
	Code        string `json:"code"`
	Severity    string `json:"severity"`
	ObjectType  string `json:"object_type"`
	Message     string `json:"message"`
	Decision    string `json:"decision"`
}

type BeautifyPlanFinding struct {
	ID          string `json:"id"`
	Severity    string `json:"severity"`
	Code        string `json:"code"`
	SourceSlide int    `json:"source_slide"`
	Message     string `json:"message"`
}

type BeautifyPlanPreview struct {
	TaskID    string                  `json:"task_id"`
	Source    BeautifySourceSummary   `json:"source"`
	Identity  BeautifyIdentitySummary `json:"identity"`
	Inventory struct {
		SlideCount int                            `json:"slide_count"`
		Pages      []BeautifyInventoryPageSummary `json:"pages"`
	} `json:"inventory"`
	Risks      []BeautifyRiskSummary `json:"risks"`
	Plan       BeautifyPlanDocument  `json:"plan"`
	Findings   []BeautifyPlanFinding `json:"findings"`
	Summary    map[string]any        `json:"summary"`
	PlanSHA256 string                `json:"plan_sha256"`
	Revision   int                   `json:"revision"`
	CanEdit    bool                  `json:"can_edit"`
	CanConfirm bool                  `json:"can_confirm"`
}

func (s *TaskService) GetBeautifyPlan(ctx context.Context, taskID string) (*BeautifyPlanPreview, error) {
	task, err := s.repo.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if err := requireBeautifyPlanRoute(task); err != nil {
		return nil, err
	}
	unlock, err := s.lockBeautifyAPI(ctx, task)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return s.beautifyPlanPreview(task)
}

func (s *TaskService) SaveBeautifyPlan(ctx context.Context, taskID string, submitted map[string]any, expectedPlanSHA256 string) (*BeautifyPlanPreview, error) {
	task, err := s.repo.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if err := requireBeautifyPlanEditableTask(task); err != nil {
		return nil, err
	}
	unlock, err := s.lockBeautifyAPI(ctx, task)
	if err != nil {
		return nil, err
	}
	defer unlock()
	task, err = s.repo.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if err := requireBeautifyPlanEditableTask(task); err != nil {
		return nil, err
	}
	projectPath, err := s.findPersistentProjectPath(task)
	if err != nil {
		return nil, err
	}
	lockPath := filepath.Join(projectPath, ".slidesmith", "beautify_lock.json")
	if _, err := os.Lstat(lockPath); err == nil {
		return nil, fmt.Errorf("Beautify plan is immutable because a confirmed lock already exists")
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	canonical, canonicalRaw, currentSHA, err := readBeautifyPlanDocument(projectPath)
	if err != nil {
		return nil, err
	}
	var candidate BeautifyPlanDocument
	rawSubmitted, err := json.Marshal(submitted)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(rawSubmitted))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&candidate); err != nil {
		return nil, fmt.Errorf("decode Beautify plan: %w", err)
	}
	if err := CheckBeautifyCAS(currentSHA, canonical.Revision, expectedPlanSHA256, candidate.Revision); err != nil {
		return nil, err
	}
	if err := validateBeautifyPlanMutation(canonical, &candidate); err != nil {
		return nil, err
	}
	candidate.Status = "draft"
	candidate.Revision = canonical.Revision + 1
	candidate.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	planPath := filepath.Join(projectPath, "analysis", "beautify_plan.json")
	if err := writeJSONAtomic(planPath, &candidate); err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = writeRawAtomic(planPath, canonicalRaw, 0o600)
			_, _ = ValidateBeautifyPlanContract(projectPath, task.ID)
		}
	}()
	if _, err := ValidateBeautifyPlanContract(projectPath, task.ID); err != nil {
		return nil, err
	}
	if err := cleanupFullPPTMasterOutputsForRetry(projectPath, PhaseSpecGenerate, task.Route); err != nil {
		return nil, err
	}
	committed = true
	_ = s.event(ctx, task.ID, model.EventTypeConfirmation, "beautify_plan_saved", "Beautify visual plan choices saved", map[string]any{
		"revision": candidate.Revision,
	})
	return s.beautifyPlanPreview(task)
}

func (s *TaskService) CheckBeautifyPlan(ctx context.Context, taskID string) (*model.Task, error) {
	task, err := s.repo.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if err := requireBeautifyPlanReadableTask(task); err != nil {
		return nil, err
	}
	unlock, err := s.lockBeautifyAPI(ctx, task)
	if err != nil {
		return nil, err
	}
	defer unlock()
	projectPath, err := s.findPersistentProjectPath(task)
	if err != nil {
		return nil, err
	}
	contract, err := ValidateBeautifyPlanContract(projectPath, task.ID)
	if err != nil {
		return nil, fmt.Errorf("beautify_plan.check: %w", err)
	}
	_ = s.event(ctx, task.ID, model.EventTypeConfirmation, "beautify_plan_checked", "Beautify plan contract check passed", map[string]any{
		"revision": contract.PlanRevision,
	})
	return task, nil
}

func (s *TaskService) ConfirmBeautifyPlan(ctx context.Context, taskID string) (*model.Task, error) {
	task, err := s.repo.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if task.Status != model.TaskStatusAwaitingBeautifyConfirm {
		return nil, fmt.Errorf("task must be awaiting Beautify plan confirmation, got %q", task.Status)
	}
	if err := requireBeautifyPlanRoute(task); err != nil {
		return nil, err
	}
	unlock, err := s.lockBeautifyAPI(ctx, task)
	if err != nil {
		return nil, err
	}
	defer unlock()
	task, err = s.repo.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if task.Status != model.TaskStatusAwaitingBeautifyConfirm {
		return nil, errTaskStateChanged
	}
	projectPath, err := s.findPersistentProjectPath(task)
	if err != nil {
		return nil, err
	}
	plan, original, planSHA, err := readBeautifyPlanDocument(projectPath)
	if err != nil {
		return nil, err
	}
	if plan.Status == "confirmed" {
		contract, err := validateExistingBeautifyPlanContract(projectPath, task.ID)
		if err != nil {
			return nil, fmt.Errorf("beautify_plan.confirm recovery: %w", err)
		}
		lock, err := ValidateBeautifyLock(projectPath, task.ID)
		if err != nil {
			return nil, fmt.Errorf("beautify_lock.contract recovery: %w", err)
		}
		if contract.PlanSHA256 != planSHA || lock.PlanSHA256 != planSHA {
			return nil, fmt.Errorf("Beautify confirmation recovery plan/lock hash mismatch")
		}
		return s.completeBeautifyPlanConfirmation(ctx, task, projectPath, lock)
	}
	if plan.Status != "draft" {
		return nil, fmt.Errorf("beautify plan status must be draft or recoverable confirmed before confirmation")
	}
	lockPath := filepath.Join(projectPath, ".slidesmith", "beautify_lock.json")
	if _, err := os.Lstat(lockPath); err == nil {
		return nil, fmt.Errorf("Beautify plan is immutable because a confirmed lock already exists")
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	plan.Status = "confirmed"
	planPath := filepath.Join(projectPath, "analysis", "beautify_plan.json")
	if err := writeJSONAtomic(planPath, plan); err != nil {
		return nil, err
	}
	lockPersisted := false
	defer func() {
		if !lockPersisted {
			_ = writeRawAtomic(planPath, original, 0o600)
			_, _ = ValidateBeautifyPlanContract(projectPath, task.ID)
		}
	}()
	contract, err := ValidateBeautifyPlanContract(projectPath, task.ID)
	if err != nil {
		return nil, fmt.Errorf("beautify_plan.confirm: %w", err)
	}
	lock, err := BuildBeautifyLock(projectPath, task.ID, contract.PlanSHA256)
	if err != nil {
		return nil, fmt.Errorf("beautify_lock.contract: %w", err)
	}
	lockPersisted = true
	return s.completeBeautifyPlanConfirmation(ctx, task, projectPath, lock)
}

func (s *TaskService) completeBeautifyPlanConfirmation(ctx context.Context, task *model.Task, projectPath string, lock *BeautifyLock) (*model.Task, error) {
	lockSHA, err := sha256File(filepath.Join(projectPath, ".slidesmith", "beautify_lock.json"))
	if err != nil {
		return nil, err
	}
	if err := s.publishBeautifyPhaseArtifacts(ctx, task, projectPath, "confirmed-r"+fmt.Sprint(lock.Revision), PhaseBeautifyPlan); err != nil {
		return nil, err
	}
	if err := s.transition(ctx, task, model.TaskStatusSpecGenerating, "Beautify plan confirmed; spec generating", map[string]any{
		"beautify_lock_sha256": lockSHA,
		"plan_revision":        lock.Revision,
	}); err != nil {
		return nil, err
	}
	_ = s.event(ctx, task.ID, model.EventTypeConfirmation, "beautify_plan_confirmed", "Beautify plan confirmed and locked", map[string]any{
		"plan_revision":  lock.Revision,
		"accepted_risks": append([]string(nil), lock.AcceptedRisks...),
	})
	_ = s.event(ctx, task.ID, model.EventTypeRuntime, "queued", "Beautify spec generation queued for worker", nil)
	return task, nil
}

func (s *TaskService) RegenerateBeautifyPlan(ctx context.Context, taskID string) (*model.Task, error) {
	task, err := s.repo.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if task.Status != model.TaskStatusAwaitingBeautifyConfirm && task.Status != model.TaskStatusFailed {
		return nil, fmt.Errorf("Beautify plan regenerate is not allowed at status %q", task.Status)
	}
	if err := requireBeautifyPlanRoute(task); err != nil {
		return nil, err
	}
	unlock, err := s.lockBeautifyAPI(ctx, task)
	if err != nil {
		return nil, err
	}
	defer unlock()
	projectPath, err := s.findPersistentProjectPath(task)
	if err != nil {
		return nil, err
	}
	lockPath := filepath.Join(projectPath, ".slidesmith", "beautify_lock.json")
	if _, err := os.Lstat(lockPath); err == nil {
		return nil, fmt.Errorf("Beautify plan regeneration requires an explicit new revision because a confirmed lock exists")
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if _, err := ValidateBeautifyInventoryContract(projectPath, task.ID); err != nil {
		return nil, err
	}
	if err := cleanupFullPPTMasterOutputsForRetry(projectPath, PhaseSpecGenerate, task.Route); err != nil {
		return nil, err
	}
	for _, path := range []string{
		filepath.Join(projectPath, "analysis", "beautify_plan.json"),
		filepath.Join(projectPath, ".slidesmith", "contracts", "beautify_plan.json"),
	} {
		if err := removeRetryPath(projectPath, path); err != nil {
			return nil, err
		}
	}
	if err := s.transition(ctx, task, model.TaskStatusBeautifyPlanning, "Beautify plan regeneration queued", map[string]any{"retry_phase": retryPhaseBeautifyPlan}); err != nil {
		return nil, err
	}
	_ = s.event(ctx, task.ID, model.EventTypeRuntime, "queued", "Beautify plan regeneration queued for worker", nil)
	return task, nil
}

func (s *TaskService) beautifyPlanPreview(task *model.Task) (*BeautifyPlanPreview, error) {
	if err := requireBeautifyPlanReadableTask(task); err != nil {
		return nil, err
	}
	projectPath, err := s.findPersistentProjectPath(task)
	if err != nil {
		return nil, err
	}
	inputs, err := ValidateBeautifyInputsContract(projectPath, task.ID)
	if err != nil {
		return nil, err
	}
	if _, err := validateExistingBeautifyInventoryContract(projectPath, task.ID); err != nil {
		return nil, err
	}
	var inventory BeautifyInventoryDocument
	if err := beautifyReadJSON(projectPath, "analysis/beautify_inventory.json", &inventory); err != nil {
		return nil, err
	}
	var risks BeautifyRiskReport
	if err := beautifyReadJSON(projectPath, "analysis/beautify_risk_report.json", &risks); err != nil {
		return nil, err
	}
	plan, _, planSHA, err := readBeautifyPlanDocument(projectPath)
	if err != nil {
		return nil, err
	}
	lockExists := false
	lockPath := filepath.Join(projectPath, ".slidesmith", "beautify_lock.json")
	if _, err := os.Lstat(lockPath); err == nil {
		lockExists = true
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	recoveryCanConfirm := false
	if task.Status == model.TaskStatusAwaitingBeautifyConfirm && plan.Status == "confirmed" && lockExists {
		lock, err := ValidateBeautifyLock(projectPath, task.ID)
		if err != nil {
			return nil, fmt.Errorf("Beautify confirmation recovery lock: %w", err)
		}
		recoveryCanConfirm = lock.PlanSHA256 == planSHA
	}
	preview := &BeautifyPlanPreview{
		TaskID: task.ID,
		Source: BeautifySourceSummary{
			Name: filepath.Base(inputs.SourcePPTX.Path), SHA256: inputs.SourcePPTX.SHA256,
			SlideCount: inputs.SlideCount, Canvas: beautifyCanvasLabel(inputs.Canvas),
		},
		Plan: *plan, PlanSHA256: planSHA, Revision: plan.Revision,
		CanEdit: task.Status == model.TaskStatusAwaitingBeautifyConfirm && plan.Status == "draft" && !lockExists,
		Summary: map[string]any{"page_count_locked": true, "order_locked": true, "content_data_frozen": true},
	}
	preview.Inventory.SlideCount = inventory.SlideCount
	for _, slide := range inventory.Slides {
		preview.Inventory.Pages = append(preview.Inventory.Pages, BeautifyInventoryPageSummary{
			SourceSlide: slide.SlideIndex, TextCount: len(slide.TextBlocks), ImageCount: len(slide.Images),
			TableCount: len(slide.Tables), ChartCount: len(slide.Charts), IgnoredCount: len(slide.Ignored),
			NeedsConfirmationCount: len(slide.NeedsConfirmation),
		})
	}
	accepted := map[string]bool{}
	for _, id := range plan.AcceptedRisks {
		accepted[id] = true
	}
	blocking := 0
	for _, risk := range risks.Risks {
		message := strings.TrimSpace(risk.Message)
		if len([]rune(message)) > 240 {
			message = string([]rune(message)[:240]) + "…"
		}
		decision := "pending"
		if accepted[risk.ID] {
			decision = "accepted"
		}
		preview.Risks = append(preview.Risks, BeautifyRiskSummary{
			ID: risk.ID, SourceSlide: risk.SlideIndex, Code: risk.Rule, Severity: risk.Severity,
			ObjectType: beautifyRiskObjectType(risk), Message: message, Decision: decision,
		})
		if risk.NeedsConfirmation && !accepted[risk.ID] {
			blocking++
			preview.Findings = append(preview.Findings, BeautifyPlanFinding{
				ID: "risk:" + risk.ID, Severity: "blocking", Code: "risk_not_accepted",
				SourceSlide: risk.SlideIndex, Message: "风险需明确接受或转为主生成/人工处理。",
			})
		}
	}
	preview.Summary["risk_count"] = len(risks.Risks)
	preview.Summary["blocking"] = blocking
	preview.CanConfirm = (preview.CanEdit || recoveryCanConfirm) && blocking == 0
	preview.Identity = beautifyPlanIdentitySummary(projectPath, inputs, plan)
	return preview, nil
}

func requireBeautifyPlanRoute(task *model.Task) error {
	if task == nil || task.Route != model.TaskRouteBeautify {
		return fmt.Errorf("task route must be %q", model.TaskRouteBeautify)
	}
	return nil
}

func requireBeautifyPlanReadableTask(task *model.Task) error {
	if err := requireBeautifyPlanRoute(task); err != nil {
		return err
	}
	switch task.Status {
	case model.TaskStatusAwaitingBeautifyConfirm, model.TaskStatusSpecGenerating,
		model.TaskStatusImageAcquiring, model.TaskStatusSVGGenerating, model.TaskStatusQualityChecking,
		model.TaskStatusExporting, model.TaskStatusPPTXValidating, model.TaskStatusPublishing,
		model.TaskStatusCompleted, model.TaskStatusFailed:
		return nil
	default:
		return fmt.Errorf("Beautify plan is not readable at status %q", task.Status)
	}
}

func requireBeautifyPlanEditableTask(task *model.Task) error {
	if err := requireBeautifyPlanRoute(task); err != nil {
		return err
	}
	if task.Status != model.TaskStatusAwaitingBeautifyConfirm {
		return fmt.Errorf("Beautify plan is not editable at status %q", task.Status)
	}
	return nil
}

func (s *TaskService) lockBeautifyAPI(ctx context.Context, task *model.Task) (func(), error) {
	workspace := s.resolveTaskWorkspace(task)
	return acquireProjectPromotionLock(ctx, filepath.Join(workspace.HostDir, ".slidesmith", "beautify-api.lock"))
}

func readBeautifyPlanDocument(projectPath string) (*BeautifyPlanDocument, []byte, string, error) {
	path := filepath.Join(projectPath, "analysis", "beautify_plan.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, "", err
	}
	var plan BeautifyPlanDocument
	if err := json.Unmarshal(raw, &plan); err != nil {
		return nil, nil, "", err
	}
	sha, err := sha256File(path)
	return &plan, raw, sha, err
}

func validateBeautifyPlanMutation(canonical, candidate *BeautifyPlanDocument) error {
	if canonical == nil || candidate == nil {
		return fmt.Errorf("Beautify plan is required")
	}
	canonicalFrozen := beautifyFrozenPlanView(canonical)
	candidateFrozen := beautifyFrozenPlanView(candidate)
	if !reflect.DeepEqual(canonicalFrozen, candidateFrozen) {
		return fmt.Errorf("Beautify plan update attempted to mutate frozen page/content/data/accounting fields")
	}
	allowedIdentity := map[string]bool{"theme": true, "observed": true, "source-replica": true, "observed-identity": true, "content-matched-alternative": true}
	if !allowedIdentity[strings.TrimSpace(candidate.Identity.Source)] {
		return fmt.Errorf("Beautify identity source %q is not allowed", candidate.Identity.Source)
	}
	knownRisks := map[string]bool{}
	for _, slide := range canonical.Slides {
		for _, risk := range slide.Risks {
			knownRisks[risk] = true
		}
	}
	seen := map[string]bool{}
	for _, risk := range candidate.AcceptedRisks {
		if !knownRisks[risk] || seen[risk] {
			return fmt.Errorf("Beautify accepted risk %q is unknown or duplicated", risk)
		}
		seen[risk] = true
	}
	for _, slide := range candidate.Slides {
		if strings.TrimSpace(slide.LayoutStrategy) == "" || strings.TrimSpace(slide.PageRhythm) == "" {
			return fmt.Errorf("Beautify page %d layout strategy/rhythm is required", slide.OutputPage)
		}
		for _, value := range []string{slide.LayoutStrategy, slide.PageRhythm} {
			lower := strings.ToLower(value)
			if strings.ContainsAny(value, `/\\`) || strings.Contains(lower, "..") || strings.Contains(lower, "file:") || strings.Contains(lower, "://") || strings.ContainsAny(value, "\n\r\x00") {
				return fmt.Errorf("Beautify visual preference contains a forbidden path/command token")
			}
		}
	}
	return nil
}

func beautifyFrozenPlanView(plan *BeautifyPlanDocument) map[string]any {
	raw, _ := json.Marshal(plan)
	var view map[string]any
	_ = json.Unmarshal(raw, &view)
	delete(view, "status")
	delete(view, "revision")
	delete(view, "created_at")
	delete(view, "accepted_risks")
	if identity, ok := view["identity"].(map[string]any); ok {
		delete(identity, "source")
		delete(identity, "canvas_override")
		delete(identity, "palette_override")
		delete(identity, "typography_override")
	}
	if slides, ok := view["slides"].([]any); ok {
		for _, value := range slides {
			if slide, ok := value.(map[string]any); ok {
				delete(slide, "layout_strategy")
				delete(slide, "page_rhythm")
			}
		}
	}
	return view
}

func writeRawAtomic(path string, raw []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".beautify-raw-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if mode == 0 {
		mode = 0o600
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func beautifyCanvasLabel(canvas BeautifyCanvas) string {
	if canvas.Width <= 0 || canvas.Height <= 0 {
		return "unknown"
	}
	return fmt.Sprintf("%.3g×%.3g %s (%.4f)", canvas.Width, canvas.Height, canvas.Unit, canvas.AspectRatio)
}

func beautifyRiskObjectType(risk BeautifyRiskFinding) string {
	if len(risk.ItemIDs) == 0 {
		return "slide"
	}
	id := strings.ToLower(risk.ItemIDs[0])
	for _, kind := range []string{"text", "table", "chart", "image", "media", "ole", "smartart"} {
		if strings.Contains(id, kind) {
			return kind
		}
	}
	return "object"
}

func beautifyPlanIdentitySummary(projectPath string, inputs *BeautifyInputsContract, plan *BeautifyPlanDocument) BeautifyIdentitySummary {
	result := BeautifyIdentitySummary{SelectedSource: plan.Identity.Source, Canvas: beautifyCanvasLabel(inputs.Canvas), Palette: []string{}, Fonts: []string{}, Overrides: []string{}}
	var identity map[string]any
	if beautifyReadJSON(projectPath, inputs.Identity.Path, &identity) == nil {
		var collectStrings func(value any, out *[]string)
		collectStrings = func(value any, out *[]string) {
			switch typed := value.(type) {
			case string:
				if text := strings.TrimSpace(typed); text != "" && len(*out) < 12 {
					*out = append(*out, text)
				}
			case []any:
				for _, item := range typed {
					collectStrings(item, out)
				}
			case map[string]any:
				keys := make([]string, 0, len(typed))
				for key := range typed {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				for _, key := range keys {
					collectStrings(typed[key], out)
				}
			}
		}
		if observed, ok := identity["observed"].(map[string]any); ok {
			collectStrings(observed["colors"], &result.Palette)
			collectStrings(observed["fonts"], &result.Fonts)
		}
	}
	if plan.Identity.CanvasOverride {
		result.Overrides = append(result.Overrides, "canvas")
	}
	if plan.Identity.PaletteOverride {
		result.Overrides = append(result.Overrides, "palette")
	}
	if plan.Identity.TypographyOverride {
		result.Overrides = append(result.Overrides, "typography")
	}
	return result
}
