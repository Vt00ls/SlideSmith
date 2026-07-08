package handler

import (
	"errors"
	"net/http"
	"path/filepath"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/slidesmith/slidesmith/backend/internal/repository"
	"github.com/slidesmith/slidesmith/backend/internal/service"
)

type TemplateHandler struct {
	templates *service.TemplateCatalogService
}

func NewTemplateHandler(templates *service.TemplateCatalogService) *TemplateHandler {
	return &TemplateHandler{templates: templates}
}

func (h *TemplateHandler) ListTemplates(ctx *gin.Context) {
	templates, err := h.templates.ListTemplates(ctx.Request.Context())
	if err != nil {
		handleTemplateError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": templates})
}

func (h *TemplateHandler) GetTemplate(ctx *gin.Context) {
	template, err := h.templates.GetTemplate(ctx.Request.Context(), ctx.Param("id"))
	if err != nil {
		handleTemplateError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"data": template})
}

func (h *TemplateHandler) GetTemplateAsset(ctx *gin.Context) {
	path, contentType, err := h.templates.TemplateAssetPath(ctx.Request.Context(), ctx.Param("id"), ctx.Param("path"))
	if err != nil {
		handleTemplateError(ctx, err)
		return
	}
	ctx.Header("Content-Type", contentType)
	ctx.Header("Content-Disposition", "inline; filename="+strconv.Quote(filepath.Base(path)))
	ctx.File(path)
}

func handleTemplateError(ctx *gin.Context, err error) {
	switch {
	case errors.Is(err, repository.ErrNotFound):
		errorJSON(ctx, http.StatusNotFound, err)
	case errors.Is(err, service.ErrInvalidTemplateAssetPath):
		errorJSON(ctx, http.StatusBadRequest, err)
	default:
		errorJSON(ctx, http.StatusInternalServerError, err)
	}
}
