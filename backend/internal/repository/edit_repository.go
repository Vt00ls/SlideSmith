package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/slidesmith/slidesmith/backend/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrConflict = errors.New("conflict")
	ErrLocked   = errors.New("locked")
)

func (r *Repository) CreateArtifactVersion(ctx context.Context, version *model.TaskArtifactVersion) error {
	if version == nil || strings.TrimSpace(version.TaskID) == "" || strings.TrimSpace(version.Version) == "" {
		return fmt.Errorf("task artifact version identity is required")
	}
	now := time.Now().UTC()
	if version.ID == "" {
		version.ID = uuid.NewString()
	}
	if version.Status == "" {
		version.Status = model.ArtifactVersionStatusStaging
	}
	if version.Source == "" {
		version.Source = model.ArtifactVersionSourceGeneration
	}
	if version.MetadataJSON == "" {
		version.MetadataJSON = "{}"
	}
	version.CreatedAt = now
	return r.db.WithContext(ctx).Create(version).Error
}

func (r *Repository) UpsertActiveArtifactVersion(ctx context.Context, version *model.TaskArtifactVersion) error {
	if version == nil || version.TaskID == "" || version.Version == "" {
		return fmt.Errorf("task artifact version identity is required")
	}
	now := time.Now().UTC()
	if version.ID == "" {
		version.ID = uuid.NewString()
	}
	if version.Source == "" {
		version.Source = model.ArtifactVersionSourceGeneration
	}
	if version.MetadataJSON == "" {
		version.MetadataJSON = "{}"
	}
	if version.CreatedAt.IsZero() {
		version.CreatedAt = now
	}
	version.Status = model.ArtifactVersionStatusActive
	if version.ActivatedAt == nil {
		version.ActivatedAt = &now
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing model.TaskArtifactVersion
		err := tx.Where("task_id = ? AND version = ?", version.TaskID, version.Version).First(&existing).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			return tx.Create(version).Error
		case err != nil:
			return err
		case existing.Status == model.ArtifactVersionStatusActive:
			*version = existing
			return nil
		default:
			result := tx.Model(&model.TaskArtifactVersion{}).
				Where("id = ? AND task_id = ? AND status = ?", existing.ID, version.TaskID, existing.Status).
				Updates(map[string]any{
					"status":                   model.ArtifactVersionStatusActive,
					"source":                   version.Source,
					"parent_version":           version.ParentVersion,
					"artifact_manifest_sha256": version.ArtifactManifestSHA256,
					"pptx_artifact_id":         version.PPTXArtifactID,
					"edit_session_id":          version.EditSessionID,
					"edit_revision":            version.EditRevision,
					"metadata_json":            version.MetadataJSON,
					"activated_at":             version.ActivatedAt,
					"failed_at":                nil,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrConflict
			}
			version.ID = existing.ID
			return nil
		}
	})
}

func (r *Repository) GetArtifactVersion(ctx context.Context, taskID, version string) (*model.TaskArtifactVersion, error) {
	if !r.db.Migrator().HasTable(&model.TaskArtifactVersion{}) {
		return nil, ErrNotFound
	}
	var row model.TaskArtifactVersion
	err := r.db.WithContext(ctx).
		Where("task_id = ? AND version = ? AND status = ?", taskID, version, model.ArtifactVersionStatusActive).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *Repository) ListArtifactVersions(ctx context.Context, taskID string) ([]model.TaskArtifactVersion, error) {
	if !r.db.Migrator().HasTable(&model.TaskArtifactVersion{}) {
		return []model.TaskArtifactVersion{}, nil
	}
	var rows []model.TaskArtifactVersion
	err := r.db.WithContext(ctx).
		Where("task_id = ? AND status = ?", taskID, model.ArtifactVersionStatusActive).
		Order("activated_at DESC, created_at DESC").
		Find(&rows).Error
	return rows, err
}

func (r *Repository) LatestArtifactVersion(ctx context.Context, taskID string) (*model.TaskArtifactVersion, error) {
	if !r.db.Migrator().HasTable(&model.TaskArtifactVersion{}) {
		return nil, ErrNotFound
	}
	var row model.TaskArtifactVersion
	err := r.db.WithContext(ctx).
		Where("task_id = ? AND status = ?", taskID, model.ArtifactVersionStatusActive).
		Order("activated_at DESC, created_at DESC").
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (r *Repository) CreateEditSession(ctx context.Context, session *model.TaskEditSession, maxActive int) error {
	if session == nil || session.TaskID == "" || session.BasePublishVersion == "" {
		return fmt.Errorf("edit session task and base version are required")
	}
	if maxActive <= 0 {
		maxActive = 1
	}
	now := time.Now().UTC()
	if session.ID == "" {
		session.ID = uuid.NewString()
	}
	if session.Status == "" {
		session.Status = model.EditSessionStatusDraft
	}
	if session.Revision <= 0 {
		session.Revision = 1
	}
	if session.DraftJSON == "" {
		session.DraftJSON = "{}"
	}
	if session.CapabilitySnapshotJSON == "" {
		session.CapabilitySnapshotJSON = "{}"
	}
	if session.FailureMetadataJSON == "" {
		session.FailureMetadataJSON = "{}"
	}
	session.CreatedAt, session.UpdatedAt = now, now
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var task model.Task
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", session.TaskID).First(&task).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		var latest model.TaskArtifactVersion
		if err := tx.Where("task_id = ? AND status = ?", session.TaskID, model.ArtifactVersionStatusActive).
			Order("activated_at DESC, created_at DESC").First(&latest).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if latest.Version != session.BasePublishVersion || latest.ArtifactManifestSHA256 != session.BaseArtifactManifestSHA256 {
			return fmt.Errorf("%w: base artifact version is not latest", ErrConflict)
		}
		var count int64
		if err := tx.Model(&model.TaskEditSession{}).
			Where("task_id = ? AND status IN ?", session.TaskID, model.ActiveEditSessionStatuses).
			Count(&count).Error; err != nil {
			return err
		}
		if count >= int64(maxActive) {
			return fmt.Errorf("%w: active edit session already exists", ErrConflict)
		}
		return tx.Create(session).Error
	})
}

func (r *Repository) GetEditSession(ctx context.Context, taskID, sessionID string) (*model.TaskEditSession, error) {
	var session model.TaskEditSession
	err := r.db.WithContext(ctx).Where("id = ? AND task_id = ?", sessionID, taskID).First(&session).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &session, nil
}

func (r *Repository) ListEditSessions(ctx context.Context, taskID string) ([]model.TaskEditSession, error) {
	var sessions []model.TaskEditSession
	err := r.db.WithContext(ctx).Where("task_id = ?", taskID).Order("created_at DESC").Find(&sessions).Error
	return sessions, err
}

func (r *Repository) SaveEditSessionDraft(ctx context.Context, taskID, sessionID string, expectedRevision int64, draftJSON, draftSHA string) (*model.TaskEditSession, error) {
	now := time.Now().UTC()
	result := r.db.WithContext(ctx).Model(&model.TaskEditSession{}).
		Where("id = ? AND task_id = ? AND status = ? AND revision = ?", sessionID, taskID, model.EditSessionStatusDraft, expectedRevision).
		Updates(map[string]any{
			"draft_json":   draftJSON,
			"draft_sha256": draftSHA,
			"revision":     gorm.Expr("revision + 1"),
			"updated_at":   now,
		})
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		current, err := r.GetEditSession(ctx, taskID, sessionID)
		if err != nil {
			return nil, err
		}
		if current.Status != model.EditSessionStatusDraft {
			return nil, fmt.Errorf("%w: edit session draft is frozen", ErrLocked)
		}
		return nil, fmt.Errorf("%w: edit session revision changed", ErrConflict)
	}
	return r.GetEditSession(ctx, taskID, sessionID)
}

func (r *Repository) FreezeEditSession(ctx context.Context, taskID, sessionID string, expectedRevision int64, expectedDraftSHA string) (*model.TaskEditSession, error) {
	var frozen model.TaskEditSession
	stale := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var session model.TaskEditSession
		if err := tx.Where("id = ? AND task_id = ?", sessionID, taskID).First(&session).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if session.Status != model.EditSessionStatusDraft {
			if session.FrozenPatchSHA256 == expectedDraftSHA && model.IsActiveEditSessionStatus(session.Status) ||
				session.Status == model.EditSessionStatusPublished && session.FrozenPatchSHA256 == expectedDraftSHA {
				frozen = session
				return nil
			}
			return fmt.Errorf("%w: edit session is already frozen", ErrConflict)
		}
		if session.Revision != expectedRevision || session.DraftSHA256 != expectedDraftSHA {
			return fmt.Errorf("%w: edit session revision or draft hash changed", ErrConflict)
		}
		var latest model.TaskArtifactVersion
		if err := tx.Where("task_id = ? AND status = ?", taskID, model.ArtifactVersionStatusActive).
			Order("activated_at DESC, created_at DESC").First(&latest).Error; err != nil {
			return err
		}
		if latest.Version != session.BasePublishVersion || latest.ArtifactManifestSHA256 != session.BaseArtifactManifestSHA256 {
			result := tx.Model(&model.TaskEditSession{}).Where("id = ? AND status = ?", session.ID, model.EditSessionStatusDraft).
				Updates(map[string]any{"status": model.EditSessionStatusStale, "updated_at": time.Now().UTC()})
			if result.Error != nil {
				return result.Error
			}
			stale = true
			return nil
		}
		now := time.Now().UTC()
		result := tx.Model(&model.TaskEditSession{}).
			Where("id = ? AND task_id = ? AND status = ? AND revision = ? AND draft_sha256 = ?", sessionID, taskID, model.EditSessionStatusDraft, expectedRevision, expectedDraftSHA).
			Updates(map[string]any{
				"status":              model.EditSessionStatusQueued,
				"frozen_revision":     expectedRevision,
				"frozen_patch_sha256": expectedDraftSHA,
				"applied_at":          now,
				"updated_at":          now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrConflict
		}
		return tx.Where("id = ? AND task_id = ?", sessionID, taskID).First(&frozen).Error
	})
	if err != nil {
		return nil, err
	}
	if stale {
		return nil, fmt.Errorf("%w: edit session base is stale", ErrConflict)
	}
	return &frozen, nil
}

func (r *Repository) DiscardEditSession(ctx context.Context, taskID, sessionID string) (*model.TaskEditSession, error) {
	now := time.Now().UTC()
	result := r.db.WithContext(ctx).Model(&model.TaskEditSession{}).
		Where("id = ? AND task_id = ? AND status = ?", sessionID, taskID, model.EditSessionStatusDraft).
		Updates(map[string]any{"status": model.EditSessionStatusDiscarded, "discarded_at": now, "updated_at": now})
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		session, err := r.GetEditSession(ctx, taskID, sessionID)
		if err != nil {
			return nil, err
		}
		if session.Status == model.EditSessionStatusDiscarded {
			return session, nil
		}
		return nil, fmt.Errorf("%w: only draft edit sessions can be discarded", ErrLocked)
	}
	return r.GetEditSession(ctx, taskID, sessionID)
}

func (r *Repository) ListClaimableEditSessions(ctx context.Context, staleBefore time.Time, limit int) ([]model.TaskEditSession, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var sessions []model.TaskEditSession
	claimableStatuses := append([]string(nil), model.ActiveEditSessionStatuses[1:]...)
	err := r.db.WithContext(ctx).
		Where("status IN ? AND (execution_claim_token = '' OR execution_claimed_at IS NULL OR execution_claimed_at < ?)", claimableStatuses, staleBefore).
		Order("updated_at ASC").Limit(limit).Find(&sessions).Error
	return sessions, err
}

func (r *Repository) ClaimEditSession(ctx context.Context, taskID, sessionID, token string, claimedAt, staleBefore time.Time) (bool, error) {
	if token == "" {
		return false, fmt.Errorf("edit claim token is required")
	}
	claimableStatuses := append([]string(nil), model.ActiveEditSessionStatuses[1:]...)
	result := r.db.WithContext(ctx).Model(&model.TaskEditSession{}).
		Where("id = ? AND task_id = ? AND status IN ?", sessionID, taskID, claimableStatuses).
		Where("execution_claim_token = '' OR execution_claimed_at IS NULL OR execution_claimed_at < ?", staleBefore).
		Updates(map[string]any{"status": model.EditSessionStatusQueued, "execution_claim_token": token, "execution_claimed_at": claimedAt.UTC(), "updated_at": claimedAt.UTC()})
	return result.RowsAffected == 1, result.Error
}

func (r *Repository) UpdateClaimedEditSession(ctx context.Context, session *model.TaskEditSession, expectedStatus, token string) (bool, error) {
	if session == nil || token == "" {
		return false, fmt.Errorf("claimed edit session and token are required")
	}
	session.UpdatedAt = time.Now().UTC()
	result := r.db.WithContext(ctx).Model(&model.TaskEditSession{}).
		Where("id = ? AND task_id = ? AND status = ? AND execution_claim_token = ?", session.ID, session.TaskID, expectedStatus, token).
		Select("status", "last_run_id", "error_message", "failure_phase", "failure_metadata_json", "result_publish_version", "published_at", "updated_at").
		Updates(session)
	return result.RowsAffected == 1, result.Error
}

func (r *Repository) ReleaseEditSessionClaim(ctx context.Context, taskID, sessionID, token string) (bool, error) {
	result := r.db.WithContext(ctx).Model(&model.TaskEditSession{}).
		Where("id = ? AND task_id = ? AND execution_claim_token = ?", sessionID, taskID, token).
		Updates(map[string]any{"execution_claim_token": "", "execution_claimed_at": nil, "updated_at": time.Now().UTC()})
	return result.RowsAffected == 1, result.Error
}

func (r *Repository) RenewEditSessionClaim(ctx context.Context, taskID, sessionID, token string) (bool, error) {
	now := time.Now().UTC()
	result := r.db.WithContext(ctx).Model(&model.TaskEditSession{}).
		Where("id = ? AND task_id = ? AND execution_claim_token = ? AND status IN ?", sessionID, taskID, token, model.ActiveEditSessionStatuses).
		Updates(map[string]any{"execution_claimed_at": now, "updated_at": now})
	return result.RowsAffected == 1, result.Error
}

func (r *Repository) RetryEditSession(ctx context.Context, taskID, sessionID string) (*model.TaskEditSession, error) {
	now := time.Now().UTC()
	result := r.db.WithContext(ctx).Model(&model.TaskEditSession{}).
		Where("id = ? AND task_id = ? AND status = ? AND frozen_patch_sha256 <> ''", sessionID, taskID, model.EditSessionStatusFailed).
		Updates(map[string]any{
			"status": model.EditSessionStatusQueued, "execution_claim_token": "", "execution_claimed_at": nil,
			"error_message": "", "failure_phase": "", "failure_metadata_json": "{}", "updated_at": now,
		})
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, fmt.Errorf("%w: failed frozen edit session is required", ErrConflict)
	}
	return r.GetEditSession(ctx, taskID, sessionID)
}

func (r *Repository) ActivateManualEditVersion(
	ctx context.Context,
	version *model.TaskArtifactVersion,
	artifacts []model.Artifact,
	sessionID, claimToken string,
) error {
	if version == nil || version.TaskID == "" || version.Version == "" || version.ParentVersion == "" || sessionID == "" || claimToken == "" {
		return fmt.Errorf("manual edit activation identity is incomplete")
	}
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var latest model.TaskArtifactVersion
		if err := tx.Where("task_id = ? AND status = ?", version.TaskID, model.ArtifactVersionStatusActive).
			Order("activated_at DESC, created_at DESC").First(&latest).Error; err != nil {
			return err
		}
		if latest.Version != version.ParentVersion {
			return fmt.Errorf("%w: manual edit parent is not latest", ErrConflict)
		}
		var session model.TaskEditSession
		if err := tx.Where("id = ? AND task_id = ? AND status = ? AND execution_claim_token = ?", sessionID, version.TaskID, model.EditSessionStatusPublishing, claimToken).
			First(&session).Error; err != nil {
			return err
		}
		if session.BasePublishVersion != version.ParentVersion || session.FrozenPatchSHA256 == "" {
			return fmt.Errorf("%w: manual edit session lineage mismatch", ErrConflict)
		}
		for index := range artifacts {
			artifact := &artifacts[index]
			if artifact.ID == "" {
				artifact.ID = uuid.NewString()
			}
			artifact.TaskID, artifact.PublishVersion = version.TaskID, version.Version
			artifact.CreatedAt, artifact.UpdatedAt = now, now
			if artifact.Storage == "" {
				artifact.Storage = "local"
			}
			if artifact.MetadataJSON == "" {
				artifact.MetadataJSON = "{}"
			}
			if err := tx.Create(artifact).Error; err != nil {
				return err
			}
		}
		version.ID = uuid.NewString()
		version.Status = model.ArtifactVersionStatusActive
		version.Source = model.ArtifactVersionSourceManualEdit
		version.EditSessionID = sessionID
		version.EditRevision = session.FrozenRevision
		version.CreatedAt, version.ActivatedAt = now, &now
		if version.MetadataJSON == "" {
			version.MetadataJSON = "{}"
		}
		if err := tx.Create(version).Error; err != nil {
			return err
		}
		result := tx.Model(&model.TaskEditSession{}).
			Where("id = ? AND task_id = ? AND status = ? AND execution_claim_token = ?", sessionID, version.TaskID, model.EditSessionStatusPublishing, claimToken).
			Updates(map[string]any{
				"status": model.EditSessionStatusPublished, "result_publish_version": version.Version,
				"published_at": now, "updated_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrConflict
		}
		return nil
	})
}

func (r *Repository) CreateEditRun(ctx context.Context, run *model.TaskEditRun) error {
	if run == nil || run.TaskID == "" || run.EditSessionID == "" || run.Phase == "" {
		return fmt.Errorf("edit run identity and phase are required")
	}
	now := time.Now().UTC()
	if run.ID == "" {
		run.ID = uuid.NewString()
	}
	if run.Attempt <= 0 {
		var max int
		if err := r.db.WithContext(ctx).Model(&model.TaskEditRun{}).
			Where("edit_session_id = ? AND phase = ?", run.EditSessionID, run.Phase).
			Select("COALESCE(MAX(attempt), 0)").Scan(&max).Error; err != nil {
			return err
		}
		run.Attempt = max + 1
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
	run.CreatedAt, run.UpdatedAt = now, now
	return r.db.WithContext(ctx).Create(run).Error
}

func (r *Repository) SaveEditRun(ctx context.Context, run *model.TaskEditRun) error {
	if run == nil {
		return fmt.Errorf("edit run is required")
	}
	run.UpdatedAt = time.Now().UTC()
	return r.db.WithContext(ctx).Save(run).Error
}

func (r *Repository) ListEditRuns(ctx context.Context, taskID, sessionID string) ([]model.TaskEditRun, error) {
	var runs []model.TaskEditRun
	err := r.db.WithContext(ctx).Where("task_id = ? AND edit_session_id = ?", taskID, sessionID).
		Order("created_at ASC").Find(&runs).Error
	return runs, err
}
