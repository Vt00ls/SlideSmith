package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/slidesmith/slidesmith/backend/internal/repository"
	"github.com/slidesmith/slidesmith/backend/internal/service"
)

type TaskHandler struct {
	tasks *service.TaskService
}

func NewTaskHandler(tasks *service.TaskService) *TaskHandler {
	return &TaskHandler{tasks: tasks}
}

func Health(ctx *gin.Context) {
	ctx.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *TaskHandler) CreateTask(ctx *gin.Context) {
	var req struct {
		Title              string `json:"title"`
		TemplateID         string `json:"template_id"`
		SelectedTemplateID string `json:"selected_template_id"`
	}
	if err := ctx.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		errorJSON(ctx, http.StatusBadRequest, err)
		return
	}
	templateID := req.TemplateID
	if templateID == "" {
		templateID = req.SelectedTemplateID
	}
	task, err := h.tasks.CreateTask(ctx.Request.Context(), req.Title, templateID)
	if err != nil {
		errorJSON(ctx, http.StatusInternalServerError, err)
		return
	}
	ctx.JSON(http.StatusCreated, gin.H{"data": task})
}

func (h *TaskHandler) ListTasks(ctx *gin.Context) {
	tasks, err := h.tasks.ListTasks(ctx.Request.Context())
	if err != nil {
		errorJSON(ctx, http.StatusInternalServerError, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": tasks})
}

func (h *TaskHandler) GetTask(ctx *gin.Context) {
	task, err := h.tasks.GetTask(ctx.Request.Context(), ctx.Param("id"))
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": task})
}

func (h *TaskHandler) UploadFile(ctx *gin.Context) {
	file, err := ctx.FormFile("file")
	if err != nil {
		errorJSON(ctx, http.StatusBadRequest, err)
		return
	}
	handle, err := file.Open()
	if err != nil {
		errorJSON(ctx, http.StatusBadRequest, err)
		return
	}
	defer handle.Close()
	artifact, err := h.tasks.UploadFile(ctx.Request.Context(), ctx.Param("id"), file.Filename, handle)
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.JSON(http.StatusCreated, gin.H{"data": artifact})
}

func (h *TaskHandler) StartTask(ctx *gin.Context) {
	task, err := h.tasks.StartTask(ctx.Request.Context(), ctx.Param("id"))
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": task})
}

func (h *TaskHandler) CancelTask(ctx *gin.Context) {
	task, err := h.tasks.CancelTask(ctx.Request.Context(), ctx.Param("id"))
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": task})
}

func (h *TaskHandler) RetryTask(ctx *gin.Context) {
	var req struct {
		Phase string `json:"phase"`
	}
	if err := ctx.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		errorJSON(ctx, http.StatusBadRequest, err)
		return
	}
	task, err := h.tasks.RetryTask(ctx.Request.Context(), ctx.Param("id"), req.Phase)
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": task})
}

func (h *TaskHandler) ContinueTask(ctx *gin.Context) {
	var req struct {
		Phase string `json:"phase"`
	}
	if err := ctx.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		errorJSON(ctx, http.StatusBadRequest, err)
		return
	}
	task, err := h.tasks.ContinueTask(ctx.Request.Context(), ctx.Param("id"), req.Phase)
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": task})
}

func (h *TaskHandler) ListRuntimeRuns(ctx *gin.Context) {
	runs, err := h.tasks.ListRuntimeRuns(ctx.Request.Context(), ctx.Param("id"))
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": runs})
}

func (h *TaskHandler) ListPhaseRuns(ctx *gin.Context) {
	runs, err := h.tasks.ListPhaseRuns(ctx.Request.Context(), ctx.Param("id"))
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": runs})
}

func (h *TaskHandler) GetSpecPreview(ctx *gin.Context) {
	spec, err := h.tasks.GetSpecPreview(ctx.Request.Context(), ctx.Param("id"))
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": spec})
}

func (h *TaskHandler) GetTemplateFillPlan(ctx *gin.Context) {
	preview, err := h.tasks.GetTemplateFillPlan(ctx.Request.Context(), ctx.Param("id"))
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": preview})
}

func (h *TaskHandler) SaveTemplateFillPlan(ctx *gin.Context) {
	var req struct {
		Plan map[string]any `json:"plan"`
	}
	if err := ctx.ShouldBindJSON(&req); err != nil {
		errorJSON(ctx, http.StatusBadRequest, err)
		return
	}
	if req.Plan == nil {
		errorJSON(ctx, http.StatusBadRequest, errors.New("template fill plan is required"))
		return
	}
	preview, err := h.tasks.SaveTemplateFillPlan(ctx.Request.Context(), ctx.Param("id"), req.Plan)
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": preview})
}

func (h *TaskHandler) CheckTemplateFillPlan(ctx *gin.Context) {
	task, err := h.tasks.CheckTemplateFillPlan(ctx.Request.Context(), ctx.Param("id"))
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": task})
}

func (h *TaskHandler) ConfirmTemplateFillPlan(ctx *gin.Context) {
	task, err := h.tasks.ConfirmTemplateFillPlan(ctx.Request.Context(), ctx.Param("id"))
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": task})
}

func (h *TaskHandler) RegenerateTemplateFillPlan(ctx *gin.Context) {
	task, err := h.tasks.RegenerateTemplateFillPlan(ctx.Request.Context(), ctx.Param("id"))
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": task})
}

func (h *TaskHandler) GetBeautifyPlan(ctx *gin.Context) {
	preview, err := h.tasks.GetBeautifyPlan(ctx.Request.Context(), ctx.Param("id"))
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": preview})
}

func (h *TaskHandler) SaveBeautifyPlan(ctx *gin.Context) {
	var req struct {
		Plan               map[string]any `json:"plan"`
		ExpectedPlanSHA256 string         `json:"expected_plan_sha256"`
	}
	if err := ctx.ShouldBindJSON(&req); err != nil {
		errorJSON(ctx, http.StatusBadRequest, err)
		return
	}
	if req.Plan == nil || req.ExpectedPlanSHA256 == "" {
		errorJSON(ctx, http.StatusBadRequest, errors.New("beautify plan and expected_plan_sha256 are required"))
		return
	}
	preview, err := h.tasks.SaveBeautifyPlan(ctx.Request.Context(), ctx.Param("id"), req.Plan, req.ExpectedPlanSHA256)
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": preview})
}

func (h *TaskHandler) CheckBeautifyPlan(ctx *gin.Context) {
	task, err := h.tasks.CheckBeautifyPlan(ctx.Request.Context(), ctx.Param("id"))
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": task})
}

func (h *TaskHandler) ConfirmBeautifyPlan(ctx *gin.Context) {
	task, err := h.tasks.ConfirmBeautifyPlan(ctx.Request.Context(), ctx.Param("id"))
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": task})
}

func (h *TaskHandler) RegenerateBeautifyPlan(ctx *gin.Context) {
	task, err := h.tasks.RegenerateBeautifyPlan(ctx.Request.Context(), ctx.Param("id"))
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": task})
}

func (h *TaskHandler) ListEvents(ctx *gin.Context) {
	afterSeq, _ := strconv.ParseInt(ctx.DefaultQuery("after_seq", "0"), 10, 64)
	limit, _ := strconv.Atoi(ctx.DefaultQuery("limit", "200"))
	events, err := h.tasks.ListEvents(ctx.Request.Context(), ctx.Param("id"), afterSeq, limit)
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": events})
}

func (h *TaskHandler) StreamEvents(ctx *gin.Context) {
	ctx.Header("Content-Type", "text/event-stream")
	ctx.Header("Cache-Control", "no-cache")
	ctx.Header("Connection", "keep-alive")
	afterSeq, _ := strconv.ParseInt(ctx.DefaultQuery("after_seq", "0"), 10, 64)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		events, err := h.tasks.ListEvents(ctx.Request.Context(), ctx.Param("id"), afterSeq, 200)
		if err != nil {
			ctx.SSEvent("error", gin.H{"error": err.Error()})
			ctx.Writer.Flush()
			return
		}
		for _, event := range events {
			afterSeq = event.Seq
			ctx.SSEvent("event", event)
		}
		ctx.Writer.Flush()

		select {
		case <-ctx.Request.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func (h *TaskHandler) ListConfirmations(ctx *gin.Context) {
	confirmations, err := h.tasks.ListConfirmations(ctx.Request.Context(), ctx.Param("id"))
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": confirmations})
}

func (h *TaskHandler) SubmitConfirmations(ctx *gin.Context) {
	var req struct {
		Values map[string]any `json:"values"`
	}
	if err := ctx.ShouldBindJSON(&req); err != nil {
		errorJSON(ctx, http.StatusBadRequest, err)
		return
	}
	if req.Values == nil {
		req.Values = map[string]any{}
	}
	task, err := h.tasks.SubmitConfirmations(ctx.Request.Context(), ctx.Param("id"), req.Values)
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": task})
}

func (h *TaskHandler) ListArtifacts(ctx *gin.Context) {
	artifacts, err := h.tasks.ListArtifacts(ctx.Request.Context(), ctx.Param("id"))
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": artifacts})
}

func (h *TaskHandler) ListArtifactVersions(ctx *gin.Context) {
	versions, err := h.tasks.ListArtifactVersions(ctx.Request.Context(), ctx.Param("id"))
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": versions})
}

func (h *TaskHandler) GetArtifactVersion(ctx *gin.Context) {
	version, err := h.tasks.GetArtifactVersion(ctx.Request.Context(), ctx.Param("id"), ctx.Param("version"))
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": version})
}

func (h *TaskHandler) ListArtifactsByVersion(ctx *gin.Context) {
	artifacts, err := h.tasks.ListArtifactsByVersion(ctx.Request.Context(), ctx.Param("id"), ctx.Param("version"))
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": artifacts})
}

func (h *TaskHandler) CreateEditSession(ctx *gin.Context) {
	var req struct {
		BasePublishVersion string `json:"base_publish_version"`
	}
	if err := ctx.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		errorJSON(ctx, http.StatusBadRequest, err)
		return
	}
	session, err := h.tasks.CreateEditSession(ctx.Request.Context(), ctx.Param("id"), req.BasePublishVersion)
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.JSON(http.StatusCreated, gin.H{"data": session})
}

func (h *TaskHandler) ListEditSessions(ctx *gin.Context) {
	sessions, err := h.tasks.ListEditSessions(ctx.Request.Context(), ctx.Param("id"))
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": sessions})
}

func (h *TaskHandler) GetEditSession(ctx *gin.Context) {
	session, err := h.tasks.GetEditSession(ctx.Request.Context(), ctx.Param("id"), ctx.Param("sessionId"))
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": session})
}

func (h *TaskHandler) SaveEditSessionDraft(ctx *gin.Context) {
	var req struct {
		ExpectedRevision int64           `json:"expected_revision"`
		Draft            json.RawMessage `json:"draft"`
	}
	if err := ctx.ShouldBindJSON(&req); err != nil {
		errorJSON(ctx, http.StatusBadRequest, err)
		return
	}
	session, err := h.tasks.SaveEditSessionDraft(ctx.Request.Context(), ctx.Param("id"), ctx.Param("sessionId"), req.ExpectedRevision, req.Draft)
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": session})
}

func (h *TaskHandler) ApplyEditSession(ctx *gin.Context) {
	var req struct {
		ExpectedRevision    int64  `json:"expected_revision"`
		ExpectedDraftSHA256 string `json:"expected_draft_sha256"`
	}
	if err := ctx.ShouldBindJSON(&req); err != nil {
		errorJSON(ctx, http.StatusBadRequest, err)
		return
	}
	session, err := h.tasks.ApplyEditSession(ctx.Request.Context(), ctx.Param("id"), ctx.Param("sessionId"), req.ExpectedRevision, req.ExpectedDraftSHA256)
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.JSON(http.StatusAccepted, gin.H{"data": session})
}

func (h *TaskHandler) DiscardEditSession(ctx *gin.Context) {
	session, err := h.tasks.DiscardEditSession(ctx.Request.Context(), ctx.Param("id"), ctx.Param("sessionId"))
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": session})
}

func (h *TaskHandler) CloneEditSession(ctx *gin.Context) {
	session, err := h.tasks.CloneEditSession(ctx.Request.Context(), ctx.Param("id"), ctx.Param("sessionId"))
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.JSON(http.StatusCreated, gin.H{"data": session})
}

func (h *TaskHandler) RetryEditSession(ctx *gin.Context) {
	var req struct {
		Phase string `json:"phase"`
	}
	if err := ctx.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		errorJSON(ctx, http.StatusBadRequest, err)
		return
	}
	session, err := h.tasks.RetryEditSession(ctx.Request.Context(), ctx.Param("id"), ctx.Param("sessionId"), req.Phase)
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.JSON(http.StatusAccepted, gin.H{"data": session})
}

func (h *TaskHandler) ListEditRuns(ctx *gin.Context) {
	runs, err := h.tasks.ListEditRuns(ctx.Request.Context(), ctx.Param("id"), ctx.Param("sessionId"))
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": runs})
}

func (h *TaskHandler) GetEditSessionPage(ctx *gin.Context) {
	page, err := h.tasks.GetEditSessionPage(ctx.Request.Context(), ctx.Param("id"), ctx.Param("sessionId"), ctx.Param("pageId"))
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.Header("Cache-Control", "no-store")
	ctx.JSON(http.StatusOK, gin.H{"data": page})
}

func (h *TaskHandler) GetSVGBundleByVersion(ctx *gin.Context) {
	bundle, err := h.tasks.GetSVGBundleByVersion(ctx.Request.Context(), ctx.Param("id"), ctx.Param("version"))
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": bundle})
}

func (h *TaskHandler) DownloadPPTXByVersion(ctx *gin.Context) {
	artifact, path, err := h.tasks.PPTXByVersion(ctx.Request.Context(), ctx.Param("id"), ctx.Param("version"))
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.FileAttachment(path, artifact.Name)
}

func (h *TaskHandler) GetResources(ctx *gin.Context) {
	resources, err := h.tasks.GetResources(ctx.Request.Context(), ctx.Param("id"))
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": resources})
}

func (h *TaskHandler) GetSVGBundle(ctx *gin.Context) {
	bundle, err := h.tasks.GetSVGBundle(ctx.Request.Context(), ctx.Param("id"))
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": bundle})
}

func (h *TaskHandler) GetQuality(ctx *gin.Context) {
	quality, err := h.tasks.GetQuality(ctx.Request.Context(), ctx.Param("id"))
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": quality})
}

func (h *TaskHandler) GetArtifactContent(ctx *gin.Context) {
	artifact, path, err := h.tasks.ArtifactFile(ctx.Request.Context(), ctx.Param("id"), ctx.Param("artifactId"))
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	if artifact.MimeType != "" {
		ctx.Header("Content-Type", artifact.MimeType)
	}
	ctx.Header("Content-Disposition", "inline; filename="+strconv.Quote(artifact.Name))
	ctx.File(path)
}

func (h *TaskHandler) DownloadPPTX(ctx *gin.Context) {
	artifact, path, err := h.tasks.LatestPPTX(ctx.Request.Context(), ctx.Param("id"))
	if err != nil {
		handleServiceError(ctx, err)
		return
	}
	ctx.FileAttachment(path, artifact.Name)
}

func handleServiceError(ctx *gin.Context, err error) {
	if errors.Is(err, repository.ErrNotFound) {
		errorJSON(ctx, http.StatusNotFound, err)
		return
	}
	if errors.Is(err, repository.ErrConflict) {
		errorJSON(ctx, http.StatusConflict, err)
		return
	}
	if errors.Is(err, repository.ErrLocked) {
		errorJSON(ctx, http.StatusLocked, err)
		return
	}
	if errors.Is(err, service.ErrUnprocessable) {
		errorJSON(ctx, http.StatusUnprocessableEntity, err)
		return
	}
	if errors.Is(err, service.ErrGone) {
		errorJSON(ctx, http.StatusGone, err)
		return
	}
	if errors.Is(err, service.ErrUnavailable) {
		errorJSON(ctx, http.StatusServiceUnavailable, err)
		return
	}
	errorJSON(ctx, http.StatusBadRequest, err)
}

func errorJSON(ctx *gin.Context, status int, err error) {
	ctx.JSON(status, gin.H{"error": err.Error()})
}
