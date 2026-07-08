package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/slidesmith/slidesmith/backend/internal/config"
	"github.com/slidesmith/slidesmith/backend/internal/database"
	"github.com/slidesmith/slidesmith/backend/internal/repository"
	"github.com/slidesmith/slidesmith/backend/internal/service"
)

func main() {
	cfg := config.Load()
	db, err := database.Open(cfg.Database)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	if err := database.Migrate(db); err != nil {
		log.Fatalf("migrate database: %v", err)
	}

	repo := repository.New(db)
	storage := service.NewLocalStorage(cfg.Storage.Root)
	agent := service.NewAgentComposeCLIClient(cfg.AgentCompose)
	publisher := service.NewRuntimeWorkspacePublisher(storage)
	tasks := service.NewTaskService(repo, storage, agent, publisher, cfg.AgentCompose)
	worker := service.NewTaskWorker(tasks, cfg.Worker, log.Default())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("slidesmith worker polling every %s batch=%d", cfg.Worker.PollInterval, cfg.Worker.BatchSize)
	if err := worker.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("worker stopped: %v", err)
	}
}
