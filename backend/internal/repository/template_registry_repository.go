package repository

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/slidesmith/slidesmith/backend/internal/model"
	"gorm.io/gorm"
)

func (r *Repository) ListTemplateRegistryEntries(ctx context.Context, statuses []string) ([]model.TemplateRegistryEntry, error) {
	var entries []model.TemplateRegistryEntry
	query := r.db.WithContext(ctx)
	if len(statuses) > 0 {
		query = query.Where("status IN ?", statuses)
	}
	err := query.
		Order("kind ASC").
		Order("display_name ASC").
		Find(&entries).Error
	return entries, err
}

func (r *Repository) GetTemplateRegistryEntry(ctx context.Context, id string) (model.TemplateRegistryEntry, error) {
	var entry model.TemplateRegistryEntry
	result := r.db.WithContext(ctx).Where("id = ?", id).Limit(1).Find(&entry)
	if result.Error != nil {
		return model.TemplateRegistryEntry{}, result.Error
	}
	if result.RowsAffected == 0 {
		return model.TemplateRegistryEntry{}, ErrNotFound
	}
	return entry, nil
}

func (r *Repository) UpsertTemplateRegistryEntries(ctx context.Context, entries []model.TemplateRegistryEntry) error {
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for i := range entries {
			entry := normalizeTemplateRegistryEntry(entries[i], now)
			if entry.ID == "" {
				return fmt.Errorf("template registry entry id is required")
			}

			var existing model.TemplateRegistryEntry
			result := tx.Where("id = ?", entry.ID).Limit(1).Find(&existing)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				entry.CreatedAt = now
				entry.UpdatedAt = now
				if err := tx.Create(&entry).Error; err != nil {
					return err
				}
				continue
			}

			if existing.Status != "" {
				entry.Status = existing.Status
			}
			if existing.Version != "" {
				entry.Version = existing.Version
			}
			entry.CreatedAt = existing.CreatedAt
			entry.UpdatedAt = now
			if err := tx.Save(&entry).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *Repository) DisableTemplateRegistryEntriesMissingFromDisk(ctx context.Context, templatesRoot string, activeIDs []string) error {
	root := strings.TrimSpace(templatesRoot)
	if root == "" {
		return nil
	}
	root = filepath.Clean(root)
	pathPrefix := root + string(filepath.Separator) + "%"
	now := time.Now().UTC()

	query := r.db.WithContext(ctx).
		Model(&model.TemplateRegistryEntry{}).
		Where("(template_path = ? OR template_path LIKE ?) AND status <> ?", root, pathPrefix, model.TemplateStatusDisabled)
	if len(activeIDs) > 0 {
		query = query.Where("id NOT IN ?", activeIDs)
	}
	return query.Updates(map[string]any{
		"status":     model.TemplateStatusDisabled,
		"synced_at":  now,
		"updated_at": now,
	}).Error
}

func normalizeTemplateRegistryEntry(entry model.TemplateRegistryEntry, now time.Time) model.TemplateRegistryEntry {
	entry.ID = strings.TrimSpace(entry.ID)
	entry.Kind = strings.TrimSpace(entry.Kind)
	entry.Name = strings.TrimSpace(entry.Name)
	entry.DisplayName = strings.TrimSpace(entry.DisplayName)
	if entry.DisplayName == "" {
		entry.DisplayName = entry.Name
	}
	entry.Version = strings.TrimSpace(entry.Version)
	if entry.Version == "" {
		entry.Version = "workspace"
	}
	entry.Status = strings.TrimSpace(entry.Status)
	if entry.Status == "" {
		entry.Status = model.TemplateStatusActive
	}
	entry.TemplatePath = strings.TrimSpace(entry.TemplatePath)
	entry.DesignSpecPath = strings.TrimSpace(entry.DesignSpecPath)
	entry.Checksum = strings.TrimSpace(entry.Checksum)
	if strings.TrimSpace(entry.PageTypesJSON) == "" {
		entry.PageTypesJSON = "[]"
	}
	if strings.TrimSpace(entry.PreviewAssetsJSON) == "" {
		entry.PreviewAssetsJSON = "[]"
	}
	if strings.TrimSpace(entry.CompatibilityJSON) == "" {
		entry.CompatibilityJSON = "{}"
	}
	entry.SyncedAt = now
	return entry
}
