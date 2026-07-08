package repository

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/slidesmith/slidesmith/backend/internal/model"
	"gorm.io/gorm"
)

type Repository struct {
	db *gorm.DB
}

func New(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) DB() *gorm.DB {
	return r.db
}

func (r *Repository) CreateTask(ctx context.Context, task *model.Task) error {
	now := time.Now().UTC()
	if task.ID == "" {
		task.ID = uuid.NewString()
	}
	task.CreatedAt = now
	task.UpdatedAt = now
	return r.db.WithContext(ctx).Create(task).Error
}

func (r *Repository) ListTasks(ctx context.Context) ([]model.Task, error) {
	var tasks []model.Task
	err := r.db.WithContext(ctx).Order("created_at DESC").Find(&tasks).Error
	return tasks, err
}

func (r *Repository) ListTasksByStatuses(ctx context.Context, statuses []string, limit int) ([]model.Task, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var tasks []model.Task
	err := r.db.WithContext(ctx).
		Where("status IN ?", statuses).
		Order("updated_at ASC").
		Limit(limit).
		Find(&tasks).Error
	return tasks, err
}

func (r *Repository) GetTask(ctx context.Context, id string) (*model.Task, error) {
	var task model.Task
	err := r.db.WithContext(ctx).First(&task, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &task, nil
}

func (r *Repository) SaveTask(ctx context.Context, task *model.Task) error {
	task.UpdatedAt = time.Now().UTC()
	return r.db.WithContext(ctx).Save(task).Error
}

func (r *Repository) AppendEvent(ctx context.Context, event *model.TaskEvent) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var maxSeq int64
		if err := tx.Model(&model.TaskEvent{}).
			Where("task_id = ?", event.TaskID).
			Select("COALESCE(MAX(seq), 0)").
			Scan(&maxSeq).Error; err != nil {
			return err
		}
		if event.ID == "" {
			event.ID = uuid.NewString()
		}
		event.Seq = maxSeq + 1
		if event.Source == "" {
			event.Source = "platform"
		}
		if event.Payload == "" {
			event.Payload = "{}"
		}
		event.CreatedAt = time.Now().UTC()
		return tx.Create(event).Error
	})
}

func (r *Repository) ListEvents(ctx context.Context, taskID string, afterSeq int64, limit int) ([]model.TaskEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	var events []model.TaskEvent
	err := r.db.WithContext(ctx).
		Where("task_id = ? AND seq > ?", taskID, afterSeq).
		Order("seq ASC").
		Limit(limit).
		Find(&events).Error
	return events, err
}

func (r *Repository) CreateArtifact(ctx context.Context, artifact *model.Artifact) error {
	now := time.Now().UTC()
	if artifact.ID == "" {
		artifact.ID = uuid.NewString()
	}
	artifact.CreatedAt = now
	artifact.UpdatedAt = now
	return r.db.WithContext(ctx).Create(artifact).Error
}

func (r *Repository) ReplaceArtifactsByObjectKeyPrefix(ctx context.Context, taskID, objectKeyPrefix string, artifacts []model.Artifact) error {
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if objectKeyPrefix != "" {
			if err := tx.
				Where("task_id = ? AND object_key LIKE ?", taskID, objectKeyPrefix+"%").
				Delete(&model.Artifact{}).Error; err != nil {
				return err
			}
		}
		for i := range artifacts {
			artifact := artifacts[i]
			if artifact.ID == "" {
				artifact.ID = uuid.NewString()
			}
			artifact.TaskID = taskID
			artifact.CreatedAt = now
			artifact.UpdatedAt = now
			if artifact.Storage == "" {
				artifact.Storage = "local"
			}
			if err := tx.Create(&artifact).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *Repository) ListArtifacts(ctx context.Context, taskID string) ([]model.Artifact, error) {
	latestVersion, err := r.latestPublishVersion(ctx, taskID)
	if err != nil {
		return nil, err
	}
	var artifacts []model.Artifact
	query := r.db.WithContext(ctx).Where("task_id = ?", taskID)
	if latestVersion != "" {
		legacyPublishedPrefix := filepath.ToSlash(filepath.Join("tasks", taskID, "artifacts")) + "/"
		query = query.Where(
			"publish_version = ? OR (publish_version = '' AND object_key NOT LIKE ?)",
			latestVersion,
			legacyPublishedPrefix+"%",
		)
	}
	err = query.Order("created_at ASC").Find(&artifacts).Error
	return artifacts, err
}

func (r *Repository) ListArtifactsByPublishVersion(ctx context.Context, taskID, publishVersion string) ([]model.Artifact, error) {
	var artifacts []model.Artifact
	err := r.db.WithContext(ctx).
		Where("task_id = ? AND publish_version = ?", taskID, publishVersion).
		Order("object_key ASC").
		Find(&artifacts).Error
	return artifacts, err
}

func (r *Repository) latestPublishVersion(ctx context.Context, taskID string) (string, error) {
	var artifact model.Artifact
	err := r.db.WithContext(ctx).
		Where("task_id = ? AND publish_version <> ''", taskID).
		Order("created_at DESC").
		First(&artifact).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return artifact.PublishVersion, nil
}

func (r *Repository) GetArtifact(ctx context.Context, taskID, artifactID string) (*model.Artifact, error) {
	var artifact model.Artifact
	err := r.db.WithContext(ctx).
		Where("task_id = ? AND id = ?", taskID, artifactID).
		First(&artifact).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &artifact, nil
}

func (r *Repository) FirstArtifactByKind(ctx context.Context, taskID, kind string) (*model.Artifact, error) {
	var artifact model.Artifact
	err := r.db.WithContext(ctx).
		Where("task_id = ? AND kind = ?", taskID, kind).
		Order("created_at ASC").
		First(&artifact).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &artifact, nil
}

func (r *Repository) LatestPPTXArtifact(ctx context.Context, taskID string) (*model.Artifact, error) {
	var artifact model.Artifact
	err := r.db.WithContext(ctx).
		Where("task_id = ? AND kind = ?", taskID, model.ArtifactKindPPTX).
		Order("created_at DESC").
		First(&artifact).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &artifact, nil
}

func (r *Repository) CreateRuntimeRun(ctx context.Context, run *model.TaskRuntimeRun) error {
	now := time.Now().UTC()
	if run.ID == "" {
		run.ID = uuid.NewString()
	}
	run.CreatedAt = now
	run.UpdatedAt = now
	return r.db.WithContext(ctx).Create(run).Error
}

func (r *Repository) SaveRuntimeRun(ctx context.Context, run *model.TaskRuntimeRun) error {
	run.UpdatedAt = time.Now().UTC()
	return r.db.WithContext(ctx).Save(run).Error
}

func (r *Repository) ListRuntimeRuns(ctx context.Context, taskID string) ([]model.TaskRuntimeRun, error) {
	var runs []model.TaskRuntimeRun
	err := r.db.WithContext(ctx).
		Where("task_id = ?", taskID).
		Order("created_at DESC").
		Find(&runs).Error
	return runs, err
}

func (r *Repository) CreatePhaseRun(ctx context.Context, run *model.TaskPhaseRun) error {
	now := time.Now().UTC()
	if run.ID == "" {
		run.ID = uuid.NewString()
	}
	if run.Attempt <= 0 {
		var maxAttempt int
		if err := r.db.WithContext(ctx).
			Model(&model.TaskPhaseRun{}).
			Where("task_id = ? AND phase = ?", run.TaskID, run.Phase).
			Select("COALESCE(MAX(attempt), 0)").
			Scan(&maxAttempt).Error; err != nil {
			return err
		}
		run.Attempt = maxAttempt + 1
	}
	if run.InputJSON == "" {
		run.InputJSON = "{}"
	}
	if run.OutputJSON == "" {
		run.OutputJSON = "{}"
	}
	if run.FailureMetadata == "" {
		run.FailureMetadata = "{}"
	}
	run.CreatedAt = now
	run.UpdatedAt = now
	return r.db.WithContext(ctx).Create(run).Error
}

func (r *Repository) SavePhaseRun(ctx context.Context, run *model.TaskPhaseRun) error {
	run.UpdatedAt = time.Now().UTC()
	return r.db.WithContext(ctx).Save(run).Error
}

func (r *Repository) ListPhaseRuns(ctx context.Context, taskID string) ([]model.TaskPhaseRun, error) {
	var runs []model.TaskPhaseRun
	err := r.db.WithContext(ctx).
		Where("task_id = ?", taskID).
		Order("created_at ASC").
		Find(&runs).Error
	return runs, err
}

func (r *Repository) ListConfirmations(ctx context.Context, taskID string) ([]model.TaskConfirmation, error) {
	var confirmations []model.TaskConfirmation
	err := r.db.WithContext(ctx).
		Where("task_id = ?", taskID).
		Order("created_at ASC").
		Find(&confirmations).Error
	return confirmations, err
}

func (r *Repository) EnsureConfirmations(ctx context.Context, taskID string, confirmations []model.TaskConfirmation) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for i := range confirmations {
			confirmation := confirmations[i]
			var count int64
			err := tx.Model(&model.TaskConfirmation{}).
				Where("task_id = ? AND key = ?", taskID, confirmation.Key).
				Count(&count).Error
			if err != nil {
				return err
			}
			if count > 0 {
				continue
			}
			now := time.Now().UTC()
			confirmation.ID = uuid.NewString()
			confirmation.TaskID = taskID
			confirmation.Status = "pending"
			confirmation.CreatedAt = now
			confirmation.UpdatedAt = now
			if confirmation.OptionsJSON == "" {
				confirmation.OptionsJSON = "[]"
			}
			if confirmation.ValueJSON == "" {
				confirmation.ValueJSON = "null"
			}
			if err := tx.Create(&confirmation).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *Repository) UpsertConfirmationDefinitions(ctx context.Context, taskID string, confirmations []model.TaskConfirmation) error {
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for i := range confirmations {
			confirmation := confirmations[i]
			var existing model.TaskConfirmation
			err := tx.Where("task_id = ? AND key = ?", taskID, confirmation.Key).First(&existing).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				if confirmation.ID == "" {
					confirmation.ID = uuid.NewString()
				}
				confirmation.TaskID = taskID
				confirmation.Status = "pending"
				confirmation.ValueJSON = "null"
				confirmation.CreatedAt = now
				confirmation.UpdatedAt = now
				if confirmation.OptionsJSON == "" {
					confirmation.OptionsJSON = "[]"
				}
				if err := tx.Create(&confirmation).Error; err != nil {
					return err
				}
				continue
			}
			if err != nil {
				return err
			}
			updates := map[string]any{
				"label":          confirmation.Label,
				"required":       confirmation.Required,
				"options_json":   confirmation.OptionsJSON,
				"recommendation": confirmation.Recommendation,
				"status":         "pending",
				"value_json":     "null",
				"submitted_at":   nil,
				"updated_at":     now,
			}
			if updates["options_json"] == "" {
				updates["options_json"] = "[]"
			}
			if err := tx.Model(&existing).Updates(updates).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *Repository) SubmitConfirmations(ctx context.Context, taskID string, values map[string]any) error {
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for key, value := range values {
			encoded, err := json.Marshal(value)
			if err != nil {
				return err
			}
			result := tx.Model(&model.TaskConfirmation{}).
				Where("task_id = ? AND key = ?", taskID, key).
				Updates(map[string]any{
					"value_json":   string(encoded),
					"status":       "submitted",
					"submitted_at": now,
					"updated_at":   now,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				confirmation := model.TaskConfirmation{
					ID:          uuid.NewString(),
					TaskID:      taskID,
					Key:         key,
					Label:       key,
					Required:    true,
					OptionsJSON: "[]",
					ValueJSON:   string(encoded),
					Status:      "submitted",
					CreatedAt:   now,
					UpdatedAt:   now,
					SubmittedAt: &now,
				}
				if err := tx.Create(&confirmation).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

var ErrNotFound = errors.New("not found")
