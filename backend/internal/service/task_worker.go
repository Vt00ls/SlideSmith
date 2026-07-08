package service

import (
	"context"
	"log"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/config"
)

type TaskWorker struct {
	tasks  *TaskService
	cfg    config.WorkerConfig
	logger *log.Logger
}

func NewTaskWorker(tasks *TaskService, cfg config.WorkerConfig, logger *log.Logger) *TaskWorker {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 2 * time.Second
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 1
	}
	if logger == nil {
		logger = log.Default()
	}
	return &TaskWorker{tasks: tasks, cfg: cfg, logger: logger}
}

func (w *TaskWorker) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()

	for {
		processed, err := w.tasks.ProcessQueuedTasks(ctx, w.cfg.BatchSize)
		if err != nil {
			w.logger.Printf("process queued tasks: %v", err)
		}
		if processed > 0 {
			w.logger.Printf("processed %d queued task(s)", processed)
			continue
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
