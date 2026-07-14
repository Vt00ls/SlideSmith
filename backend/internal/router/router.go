package router

import (
	"github.com/gin-gonic/gin"
	"github.com/slidesmith/slidesmith/backend/internal/handler"
)

type Handlers struct {
	Tasks     *handler.TaskHandler
	Templates *handler.TemplateHandler
}

func Register(engine *gin.Engine, handlers Handlers) {
	engine.GET("/healthz", handler.Health)
	api := engine.Group("/api")
	api.GET("/health", handler.Health)

	if handlers.Templates != nil {
		api.GET("/templates", handlers.Templates.ListTemplates)
		api.GET("/templates/:id", handlers.Templates.GetTemplate)
		api.GET("/templates/:id/assets/*path", handlers.Templates.GetTemplateAsset)
	}

	if handlers.Tasks == nil {
		return
	}
	api.GET("/tasks", handlers.Tasks.ListTasks)
	api.POST("/tasks", handlers.Tasks.CreateTask)
	api.GET("/tasks/:id", handlers.Tasks.GetTask)
	api.POST("/tasks/:id/files", handlers.Tasks.UploadFile)
	api.POST("/tasks/:id/start", handlers.Tasks.StartTask)
	api.POST("/tasks/:id/cancel", handlers.Tasks.CancelTask)
	api.POST("/tasks/:id/retry", handlers.Tasks.RetryTask)
	api.POST("/tasks/:id/continue", handlers.Tasks.ContinueTask)
	api.GET("/tasks/:id/runtime-runs", handlers.Tasks.ListRuntimeRuns)
	api.GET("/tasks/:id/phase-runs", handlers.Tasks.ListPhaseRuns)
	api.GET("/tasks/:id/spec", handlers.Tasks.GetSpecPreview)
	api.GET("/tasks/:id/template-fill/plan", handlers.Tasks.GetTemplateFillPlan)
	api.PUT("/tasks/:id/template-fill/plan", handlers.Tasks.SaveTemplateFillPlan)
	api.POST("/tasks/:id/template-fill/check", handlers.Tasks.CheckTemplateFillPlan)
	api.POST("/tasks/:id/template-fill/confirm", handlers.Tasks.ConfirmTemplateFillPlan)
	api.POST("/tasks/:id/template-fill/regenerate", handlers.Tasks.RegenerateTemplateFillPlan)
	api.GET("/tasks/:id/events", handlers.Tasks.ListEvents)
	api.GET("/tasks/:id/events/stream", handlers.Tasks.StreamEvents)
	api.GET("/tasks/:id/confirmations", handlers.Tasks.ListConfirmations)
	api.POST("/tasks/:id/confirmations", handlers.Tasks.SubmitConfirmations)
	api.GET("/tasks/:id/artifacts", handlers.Tasks.ListArtifacts)
	api.GET("/tasks/:id/resources", handlers.Tasks.GetResources)
	api.GET("/tasks/:id/svg-bundle", handlers.Tasks.GetSVGBundle)
	api.GET("/tasks/:id/quality", handlers.Tasks.GetQuality)
	api.GET("/tasks/:id/artifacts/:artifactId/content", handlers.Tasks.GetArtifactContent)
	api.GET("/tasks/:id/download/pptx", handlers.Tasks.DownloadPPTX)
}
