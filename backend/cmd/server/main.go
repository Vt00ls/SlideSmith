package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/slidesmith/slidesmith/backend/internal/config"
	"github.com/slidesmith/slidesmith/backend/internal/database"
	"github.com/slidesmith/slidesmith/backend/internal/handler"
	"github.com/slidesmith/slidesmith/backend/internal/repository"
	"github.com/slidesmith/slidesmith/backend/internal/router"
	"github.com/slidesmith/slidesmith/backend/internal/service"
)

func main() {
	cfg := config.Load()
	if cfg.Server.GinMode != "" {
		gin.SetMode(cfg.Server.GinMode)
	}

	db, err := database.Open(cfg.Database)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	if err := database.Migrate(db); err != nil {
		log.Fatalf("migrate database: %v", err)
	}

	repo := repository.New(db)
	templates := service.NewTemplateCatalogServiceWithRepository(repo, cfg.AgentCompose.PPTMasterSkillDir)
	templateCount, err := templates.SyncFromDisk(context.Background())
	if err != nil {
		log.Fatalf("sync template registry: %v", err)
	}
	log.Printf("template registry synced from disk: %d templates", templateCount)

	storage := service.NewLocalStorage(cfg.Storage.Root)
	agent := service.NewAgentComposeCLIClient(cfg.AgentCompose)
	publisher := service.NewRuntimeWorkspacePublisher(storage)
	tasks := service.NewTaskService(repo, storage, agent, publisher, cfg.AgentCompose)

	engine := gin.New()
	engine.Use(gin.Logger(), gin.Recovery())
	router.Register(engine, router.Handlers{
		Tasks:     handler.NewTaskHandler(tasks),
		Templates: handler.NewTemplateHandler(templates),
	})

	server := &http.Server{
		Addr:    cfg.Server.HTTPAddr,
		Handler: engine,
	}

	go func() {
		log.Printf("slidesmith backend listening on %s", cfg.Server.HTTPAddr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
}
