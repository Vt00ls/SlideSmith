package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/slidesmith/slidesmith/backend/internal/model"
	"github.com/slidesmith/slidesmith/backend/internal/repository"
)

const manualEditDraftSchema = "slidesmith.manual_edit_draft.v1"

type ManualEditTarget struct {
	ElementID          string `json:"element_id"`
	SourceID           string `json:"source_id,omitempty"`
	Tag                string `json:"tag"`
	ElementFingerprint string `json:"element_fingerprint"`
}

type ManualEditOperation struct {
	OperationID string           `json:"operation_id"`
	Type        string           `json:"type"`
	Target      ManualEditTarget `json:"target"`
	Value       map[string]any   `json:"value"`
}

type ManualEditPage struct {
	PageID        string                `json:"page_id"`
	BaseSVGsha256 string                `json:"base_svg_sha256"`
	Operations    []ManualEditOperation `json:"operations"`
}

type ManualEditAnnotation struct {
	AnnotationID string            `json:"annotation_id"`
	Scope        string            `json:"scope"`
	PageID       string            `json:"page_id"`
	Target       *ManualEditTarget `json:"target,omitempty"`
	Instruction  string            `json:"instruction"`
	Status       string            `json:"status"`
}

type ManualEditDraft struct {
	Schema                     string                 `json:"schema"`
	TaskID                     string                 `json:"task_id"`
	EditSessionID              string                 `json:"edit_session_id"`
	BasePublishVersion         string                 `json:"base_publish_version"`
	BaseArtifactManifestSHA256 string                 `json:"base_artifact_manifest_sha256"`
	BaseSVGInventorySHA256     string                 `json:"base_svg_inventory_sha256"`
	Pages                      []ManualEditPage       `json:"pages"`
	Annotations                []ManualEditAnnotation `json:"annotations"`
	ClientUpdatedAt            string                 `json:"client_updated_at,omitempty"`
}

type editBaseInventory struct {
	Inventory svgInventoryDocument
	Artifacts []model.Artifact
	Authored  map[string]model.Artifact
	Final     map[string]model.Artifact
}

func (s *TaskService) CreateEditSession(ctx context.Context, taskID, baseVersion string) (*model.TaskEditSession, error) {
	if !s.agentCfg.LivePreviewEditEnabled {
		return nil, fmt.Errorf("%w: live preview editing is disabled", ErrUnavailable)
	}
	task, err := s.repo.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if task.Status != model.TaskStatusCompleted || task.Route != model.TaskRouteMain || task.RunnerProfile != model.RunnerProfileFullPPTMaster {
		return nil, fmt.Errorf("%w: editing requires completed main full-ppt-master task", ErrUnprocessable)
	}
	latest, err := s.requireLatestArtifactVersion(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(baseVersion) == "" {
		baseVersion = latest.Version
	}
	if baseVersion != latest.Version {
		return nil, fmt.Errorf("%w: base publish version is not latest", repository.ErrConflict)
	}
	base, err := s.loadEditBaseInventory(ctx, taskID, baseVersion)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnprocessable, err)
	}
	sessionID := uuid.NewString()
	draft := ManualEditDraft{
		Schema: manualEditDraftSchema, TaskID: taskID, EditSessionID: sessionID,
		BasePublishVersion: baseVersion, BaseArtifactManifestSHA256: latest.ArtifactManifestSHA256,
		BaseSVGInventorySHA256: inventoryArtifactSHA(base.Artifacts), Pages: []ManualEditPage{}, Annotations: []ManualEditAnnotation{},
	}
	draftJSON, draftSHA, err := canonicalManualEditDraft(draft)
	if err != nil {
		return nil, err
	}
	capabilities, _ := json.Marshal(map[string]any{
		"schema": "slidesmith.live_preview_capabilities.v1", "direct_edit": true,
		"annotation": s.agentCfg.LivePreviewAnnotationEnabled,
		"created_at": time.Now().UTC().Format(time.RFC3339Nano),
	})
	session := &model.TaskEditSession{
		ID: sessionID, TaskID: taskID, BasePublishVersion: baseVersion,
		BaseArtifactManifestSHA256: latest.ArtifactManifestSHA256,
		BaseSVGInventorySHA256:     inventoryArtifactSHA(base.Artifacts), Status: model.EditSessionStatusDraft,
		Revision: 1, DraftJSON: string(draftJSON), DraftSHA256: draftSHA,
		CapabilitySnapshotJSON: string(capabilities),
	}
	if err := s.repo.CreateEditSession(ctx, session, s.agentCfg.LivePreviewMaxActiveSessions); err != nil {
		return nil, err
	}
	_ = s.event(ctx, taskID, model.EventTypeArtifact, "edit_session_created", "Live Preview edit session created", map[string]any{
		"edit_session_id": session.ID, "base_publish_version": baseVersion, "revision": session.Revision,
		"draft_sha256": session.DraftSHA256,
	})
	return session, nil
}

func (s *TaskService) ListEditSessions(ctx context.Context, taskID string) ([]model.TaskEditSession, error) {
	if _, err := s.repo.GetTask(ctx, taskID); err != nil {
		return nil, err
	}
	return s.repo.ListEditSessions(ctx, taskID)
}

func (s *TaskService) GetEditSession(ctx context.Context, taskID, sessionID string) (*model.TaskEditSession, error) {
	if _, err := s.repo.GetTask(ctx, taskID); err != nil {
		return nil, err
	}
	return s.repo.GetEditSession(ctx, taskID, sessionID)
}

func (s *TaskService) SaveEditSessionDraft(ctx context.Context, taskID, sessionID string, expectedRevision int64, raw json.RawMessage) (*model.TaskEditSession, error) {
	if len(raw) == 0 || len(raw) > 2*1024*1024 {
		return nil, fmt.Errorf("draft size is invalid")
	}
	session, err := s.repo.GetEditSession(ctx, taskID, sessionID)
	if err != nil {
		return nil, err
	}
	var draft ManualEditDraft
	if err := json.Unmarshal(raw, &draft); err != nil {
		return nil, fmt.Errorf("decode manual edit draft: %w", err)
	}
	base, err := s.loadEditBaseInventory(ctx, taskID, session.BasePublishVersion)
	if err != nil {
		return nil, err
	}
	if err := s.validateAndBindManualEditDraft(&draft, session, base); err != nil {
		return nil, err
	}
	canonical, digest, err := canonicalManualEditDraft(draft)
	if err != nil {
		return nil, err
	}
	updated, err := s.repo.SaveEditSessionDraft(ctx, taskID, sessionID, expectedRevision, string(canonical), digest)
	if err != nil {
		return nil, err
	}
	_ = s.event(ctx, taskID, model.EventTypeArtifact, "edit_draft_saved", "Live Preview draft saved", map[string]any{
		"edit_session_id": sessionID, "revision": updated.Revision, "draft_sha256": updated.DraftSHA256,
	})
	return updated, nil
}

func (s *TaskService) ApplyEditSession(ctx context.Context, taskID, sessionID string, expectedRevision int64, expectedDraftSHA string) (*model.TaskEditSession, error) {
	session, err := s.repo.GetEditSession(ctx, taskID, sessionID)
	if err != nil {
		return nil, err
	}
	var draft ManualEditDraft
	if err := json.Unmarshal([]byte(session.DraftJSON), &draft); err != nil {
		return nil, err
	}
	operations := 0
	for _, page := range draft.Pages {
		operations += len(page.Operations)
	}
	if operations == 0 && len(draft.Annotations) == 0 {
		return nil, fmt.Errorf("%w: edit draft has no operations or annotations", ErrUnprocessable)
	}
	frozen, err := s.repo.FreezeEditSession(ctx, taskID, sessionID, expectedRevision, expectedDraftSHA)
	if err != nil {
		return nil, err
	}
	_ = s.event(ctx, taskID, model.EventTypeArtifact, "edit_session_queued", "Live Preview edit session queued", map[string]any{
		"edit_session_id": sessionID, "frozen_revision": frozen.FrozenRevision, "frozen_patch_sha256": frozen.FrozenPatchSHA256,
	})
	return frozen, nil
}

func (s *TaskService) DiscardEditSession(ctx context.Context, taskID, sessionID string) (*model.TaskEditSession, error) {
	session, err := s.repo.DiscardEditSession(ctx, taskID, sessionID)
	if err == nil {
		_ = s.event(ctx, taskID, model.EventTypeArtifact, "edit_session_discarded", "Live Preview edit session discarded", map[string]any{"edit_session_id": sessionID})
	}
	return session, err
}

func (s *TaskService) CloneEditSession(ctx context.Context, taskID, sessionID string) (*model.TaskEditSession, error) {
	if !s.agentCfg.LivePreviewEditEnabled {
		return nil, fmt.Errorf("%w: live preview editing is disabled", ErrUnavailable)
	}
	source, err := s.repo.GetEditSession(ctx, taskID, sessionID)
	if err != nil {
		return nil, err
	}
	if model.IsActiveEditSessionStatus(source.Status) {
		return nil, fmt.Errorf("%w: active edit session cannot be cloned", repository.ErrConflict)
	}
	latest, err := s.requireLatestArtifactVersion(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if latest.Version != source.BasePublishVersion || latest.ArtifactManifestSHA256 != source.BaseArtifactManifestSHA256 {
		return nil, fmt.Errorf("%w: automatic target rebase is unavailable for stale base", repository.ErrConflict)
	}
	var draft ManualEditDraft
	if err := json.Unmarshal([]byte(source.DraftJSON), &draft); err != nil {
		return nil, err
	}
	newID := uuid.NewString()
	draft.EditSessionID = newID
	draftJSON, draftSHA, err := canonicalManualEditDraft(draft)
	if err != nil {
		return nil, err
	}
	clone := &model.TaskEditSession{
		ID: newID, TaskID: taskID, BasePublishVersion: latest.Version,
		BaseArtifactManifestSHA256: latest.ArtifactManifestSHA256, BaseSVGInventorySHA256: source.BaseSVGInventorySHA256,
		Status: model.EditSessionStatusDraft, Revision: 1, DraftJSON: string(draftJSON), DraftSHA256: draftSHA,
		CapabilitySnapshotJSON: source.CapabilitySnapshotJSON,
	}
	if err := s.repo.CreateEditSession(ctx, clone, s.agentCfg.LivePreviewMaxActiveSessions); err != nil {
		return nil, err
	}
	_ = s.event(ctx, taskID, model.EventTypeArtifact, "edit_session_cloned", "Live Preview edit session cloned", map[string]any{
		"edit_session_id": clone.ID, "source_edit_session_id": source.ID, "base_publish_version": clone.BasePublishVersion,
	})
	return clone, nil
}

func (s *TaskService) ListEditRuns(ctx context.Context, taskID, sessionID string) ([]model.TaskEditRun, error) {
	if _, err := s.repo.GetEditSession(ctx, taskID, sessionID); err != nil {
		return nil, err
	}
	return s.repo.ListEditRuns(ctx, taskID, sessionID)
}

func (s *TaskService) loadEditBaseInventory(ctx context.Context, taskID, version string) (*editBaseInventory, error) {
	artifacts, err := s.ListArtifactsByVersion(ctx, taskID, version)
	if err != nil {
		return nil, err
	}
	required := map[string]bool{
		model.ArtifactKindDesignSpec: false, model.ArtifactKindSpecLock: false,
		model.ArtifactKindSVGOutput: false, model.ArtifactKindSVGFinal: false,
		model.ArtifactKindSVGInventory: false, model.ArtifactKindQualitySummary: false, model.ArtifactKindPPTX: false,
	}
	base := &editBaseInventory{Artifacts: artifacts, Authored: map[string]model.Artifact{}, Final: map[string]model.Artifact{}}
	for _, artifact := range artifacts {
		if _, ok := required[artifact.Kind]; ok {
			required[artifact.Kind] = true
		}
		rel := versionArtifactRelativePath(taskID, version, artifact.ObjectKey)
		switch artifact.Kind {
		case model.ArtifactKindSVGInventory:
			if err := readStoredJSONArtifact(s.storage, artifact, &base.Inventory); err != nil {
				return nil, err
			}
		case model.ArtifactKindSVGOutput:
			base.Authored[rel] = artifact
		case model.ArtifactKindSVGFinal:
			base.Final[rel] = artifact
		}
	}
	for kind, found := range required {
		if !found {
			return nil, fmt.Errorf("base version missing required artifact kind %s", kind)
		}
	}
	if base.Inventory.Schema != svgInventorySchema || base.Inventory.TaskID != taskID || base.Inventory.PageCount <= 0 || len(base.Inventory.Pages) != base.Inventory.PageCount {
		return nil, fmt.Errorf("base SVG inventory is invalid")
	}
	for _, page := range base.Inventory.Pages {
		if _, ok := base.Authored[page.Path]; !ok {
			return nil, fmt.Errorf("base authored SVG is missing %s", page.Path)
		}
		finalPath := strings.Replace(page.Path, "svg_output/", "svg_final/", 1)
		if _, ok := base.Final[finalPath]; !ok {
			return nil, fmt.Errorf("base final SVG is missing %s", finalPath)
		}
	}
	if len(base.Authored) != base.Inventory.PageCount || len(base.Final) != base.Inventory.PageCount {
		return nil, fmt.Errorf("base authored/final SVG roster mismatch")
	}
	return base, nil
}

func (s *TaskService) validateAndBindManualEditDraft(draft *ManualEditDraft, session *model.TaskEditSession, base *editBaseInventory) error {
	if draft == nil || session == nil || base == nil {
		return fmt.Errorf("manual edit draft context is incomplete")
	}
	if draft.Schema != "" && draft.Schema != manualEditDraftSchema {
		return fmt.Errorf("manual edit draft schema = %q", draft.Schema)
	}
	if (draft.TaskID != "" && draft.TaskID != session.TaskID) || (draft.EditSessionID != "" && draft.EditSessionID != session.ID) ||
		(draft.BasePublishVersion != "" && draft.BasePublishVersion != session.BasePublishVersion) ||
		(draft.BaseArtifactManifestSHA256 != "" && draft.BaseArtifactManifestSHA256 != session.BaseArtifactManifestSHA256) ||
		(draft.BaseSVGInventorySHA256 != "" && draft.BaseSVGInventorySHA256 != session.BaseSVGInventorySHA256) {
		return fmt.Errorf("manual edit draft base identity cannot be changed by the client")
	}
	draft.Schema, draft.TaskID, draft.EditSessionID = manualEditDraftSchema, session.TaskID, session.ID
	draft.BasePublishVersion = session.BasePublishVersion
	draft.BaseArtifactManifestSHA256 = session.BaseArtifactManifestSHA256
	draft.BaseSVGInventorySHA256 = session.BaseSVGInventorySHA256
	if draft.Pages == nil {
		draft.Pages = []ManualEditPage{}
	}
	if draft.Annotations == nil {
		draft.Annotations = []ManualEditAnnotation{}
	}
	if len(draft.Pages) > 50 {
		return fmt.Errorf("manual edit draft touches too many pages")
	}
	pageByID := map[string]svgInventoryPage{}
	for _, page := range base.Inventory.Pages {
		pageByID[page.PageID] = page
	}
	seenPages, seenOps, operationCount := map[string]bool{}, map[string]bool{}, 0
	for pageIndex := range draft.Pages {
		page := &draft.Pages[pageIndex]
		inventoryPage, ok := pageByID[page.PageID]
		if !ok || seenPages[page.PageID] {
			return fmt.Errorf("manual edit page %q is invalid or duplicated", page.PageID)
		}
		seenPages[page.PageID] = true
		page.BaseSVGsha256 = inventoryPage.SHA256
		operationCount += len(page.Operations)
		for operationIndex := range page.Operations {
			op := &page.Operations[operationIndex]
			if op.OperationID == "" || seenOps[op.OperationID] {
				return fmt.Errorf("operation id is empty or duplicated")
			}
			seenOps[op.OperationID] = true
			if err := validateManualEditOperation(*op, inventoryPage); err != nil {
				return fmt.Errorf("operation %s: %w", op.OperationID, err)
			}
			op.Value = canonicalManualEditOperationValue(*op)
		}
	}
	if operationCount > 500 {
		return fmt.Errorf("manual edit draft has too many operations")
	}
	if len(draft.Annotations) > 100 {
		return fmt.Errorf("manual edit draft has too many annotations")
	}
	if len(draft.Annotations) > 0 && !s.agentCfg.LivePreviewAnnotationEnabled {
		return fmt.Errorf("%w: annotations are disabled", ErrUnavailable)
	}
	seenAnnotations := map[string]bool{}
	for index := range draft.Annotations {
		annotation := &draft.Annotations[index]
		if annotation.AnnotationID == "" || seenAnnotations[annotation.AnnotationID] {
			return fmt.Errorf("annotation id is empty or duplicated")
		}
		seenAnnotations[annotation.AnnotationID] = true
		if _, ok := pageByID[annotation.PageID]; !ok {
			return fmt.Errorf("annotation %s page is invalid", annotation.AnnotationID)
		}
		if annotation.Status == "" {
			annotation.Status = "pending"
		}
		if annotation.Status != "pending" {
			return fmt.Errorf("annotation %s status must be pending", annotation.AnnotationID)
		}
		if err := validateManualEditAnnotation(*annotation); err != nil {
			return err
		}
	}
	return nil
}

var (
	colorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{3}([0-9a-fA-F]{3})?([0-9a-fA-F]{2})?$`)
	fontPattern  = regexp.MustCompile(`^[\pL\pN _,'"-]{1,160}$`)
)

func validateManualEditOperation(op ManualEditOperation, page svgInventoryPage) error {
	if op.Target.ElementID == "" || op.Target.Tag == "" || !strings.HasPrefix(op.Target.ElementFingerprint, "sha256:") {
		return fmt.Errorf("target identity is incomplete")
	}
	allowed := map[string]bool{"set_text": true, "translate": true, "set_fill": true, "set_stroke": true, "set_opacity": true, "set_font_size": true, "set_font_family": true, "set_font_weight": true, "set_text_anchor": true}
	if !allowed[op.Type] {
		return fmt.Errorf("%w: unsupported operation %q", ErrUnprocessable, op.Type)
	}
	tag := strings.ToLower(op.Target.Tag)
	allowedTags := map[string]map[string]bool{
		"set_text":        {"text": true, "tspan": true},
		"translate":       {"g": true, "text": true, "tspan": true, "rect": true, "circle": true, "ellipse": true, "line": true, "polyline": true, "polygon": true, "path": true, "use": true, "image": true},
		"set_fill":        {"text": true, "tspan": true, "rect": true, "circle": true, "ellipse": true, "polygon": true, "path": true},
		"set_stroke":      {"text": true, "tspan": true, "rect": true, "circle": true, "ellipse": true, "line": true, "polyline": true, "polygon": true, "path": true},
		"set_opacity":     {"g": true, "text": true, "tspan": true, "rect": true, "circle": true, "ellipse": true, "line": true, "polyline": true, "polygon": true, "path": true, "use": true, "image": true},
		"set_font_size":   {"text": true, "tspan": true},
		"set_font_family": {"text": true, "tspan": true},
		"set_font_weight": {"text": true, "tspan": true},
		"set_text_anchor": {"text": true, "tspan": true},
	}
	if !allowedTags[op.Type][tag] {
		return fmt.Errorf("%w: operation %s does not support <%s>", ErrUnprocessable, op.Type, tag)
	}
	valueString := func(key string) (string, bool) {
		value, ok := op.Value[key].(string)
		return strings.TrimSpace(value), ok
	}
	valueNumber := func(key string) (float64, bool) {
		value, ok := op.Value[key].(float64)
		return value, ok && !math.IsNaN(value) && !math.IsInf(value, 0)
	}
	switch op.Type {
	case "set_text":
		value, ok := op.Value["text"].(string)
		if !ok || len([]byte(value)) > 5000 {
			return fmt.Errorf("text value is invalid")
		}
	case "translate":
		dx, okX := valueNumber("dx")
		dy, okY := valueNumber("dy")
		if !okX || !okY || math.Abs(dx) > page.Width || math.Abs(dy) > page.Height {
			return fmt.Errorf("translate value is invalid")
		}
	case "set_fill", "set_stroke":
		value, ok := valueString(strings.TrimPrefix(op.Type, "set_"))
		if !ok || !validManualEditColor(value) {
			return fmt.Errorf("color value is invalid")
		}
	case "set_opacity":
		value, ok := valueNumber("opacity")
		if !ok || value < 0 || value > 1 {
			return fmt.Errorf("opacity value is invalid")
		}
	case "set_font_size":
		value, ok := valueNumber("font_size")
		if !ok || value <= 0 || value > page.Height {
			return fmt.Errorf("font size is invalid")
		}
	case "set_font_family":
		value, ok := valueString("font_family")
		if !ok || !fontPattern.MatchString(value) {
			return fmt.Errorf("font family is invalid")
		}
	case "set_font_weight":
		value, ok := op.Value["font_weight"]
		if !ok || !validFontWeight(value) {
			return fmt.Errorf("font weight is invalid")
		}
	case "set_text_anchor":
		value, ok := valueString("text_anchor")
		if !ok || (value != "start" && value != "middle" && value != "end") {
			return fmt.Errorf("text anchor is invalid")
		}
	}
	return nil
}

func canonicalManualEditOperationValue(op ManualEditOperation) map[string]any {
	field := strings.TrimPrefix(op.Type, "set_")
	if op.Type == "translate" {
		return map[string]any{"dx": op.Value["dx"], "dy": op.Value["dy"]}
	}
	if op.Type == "set_text" {
		field = "text"
	}
	return map[string]any{field: op.Value[field]}
}

func validManualEditColor(value string) bool {
	if colorPattern.MatchString(value) {
		return true
	}
	switch strings.ToLower(value) {
	case "none", "black", "white", "red", "green", "blue", "gray", "grey", "transparent":
		return true
	}
	return false
}

func validFontWeight(value any) bool {
	switch typed := value.(type) {
	case string:
		switch typed {
		case "normal", "bold", "lighter", "bolder", "100", "200", "300", "400", "500", "600", "700", "800", "900":
			return true
		}
	case float64:
		return typed >= 100 && typed <= 900 && math.Mod(typed, 100) == 0
	}
	return false
}

func validateManualEditAnnotation(annotation ManualEditAnnotation) error {
	if annotation.Scope != "element" && annotation.Scope != "page" {
		return fmt.Errorf("annotation %s scope is invalid", annotation.AnnotationID)
	}
	if annotation.Scope == "element" && (annotation.Target == nil || annotation.Target.ElementID == "" || !strings.HasPrefix(annotation.Target.ElementFingerprint, "sha256:")) {
		return fmt.Errorf("annotation %s target is invalid", annotation.AnnotationID)
	}
	if annotation.Scope == "page" && annotation.Target != nil {
		return fmt.Errorf("annotation %s page scope must not have target", annotation.AnnotationID)
	}
	instruction := strings.TrimSpace(annotation.Instruction)
	if instruction == "" || len([]byte(instruction)) > 2000 {
		return fmt.Errorf("annotation %s instruction is invalid", annotation.AnnotationID)
	}
	for _, forbidden := range []string{"http://", "https://", "file://", "新增页面", "删除页面", "重排页面", "新图片", "生成图片", "网络搜索", "web search", "new image", "add page", "delete page", "chart data", "table data", "speaker notes", "演讲备注", "图表数据", "表格数据"} {
		if strings.Contains(strings.ToLower(instruction), strings.ToLower(forbidden)) {
			return fmt.Errorf("%w: annotation %s requests forbidden capability", ErrUnprocessable, annotation.AnnotationID)
		}
	}
	return nil
}

func canonicalManualEditDraft(draft ManualEditDraft) ([]byte, string, error) {
	sort.Slice(draft.Pages, func(i, j int) bool { return draft.Pages[i].PageID < draft.Pages[j].PageID })
	sort.Slice(draft.Annotations, func(i, j int) bool {
		if draft.Annotations[i].PageID == draft.Annotations[j].PageID {
			return draft.Annotations[i].AnnotationID < draft.Annotations[j].AnnotationID
		}
		return draft.Annotations[i].PageID < draft.Annotations[j].PageID
	})
	raw, err := json.Marshal(draft)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(raw)
	return raw, hex.EncodeToString(sum[:]), nil
}
