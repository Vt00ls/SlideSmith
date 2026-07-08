package model

import "time"

const (
	TemplateStatusActive     = "active"
	TemplateStatusDeprecated = "deprecated"
	TemplateStatusDisabled   = "disabled"
)

type TemplateRegistryEntry struct {
	ID                string    `json:"id" gorm:"primaryKey;size:255"`
	Kind              string    `json:"kind" gorm:"not null;size:32;index"`
	Name              string    `json:"name" gorm:"not null;size:255"`
	DisplayName       string    `json:"display_name" gorm:"not null;size:255"`
	Version           string    `json:"version" gorm:"not null;size:64;default:'workspace';index"`
	Status            string    `json:"status" gorm:"not null;size:32;default:'active';index"`
	Summary           string    `json:"summary" gorm:"not null;type:text;default:''"`
	Canvas            string    `json:"canvas" gorm:"not null;size:64;default:''"`
	DefaultPageCount  int       `json:"default_page_count" gorm:"not null;default:0"`
	PageTypesJSON     string    `json:"page_types_json" gorm:"not null;type:text;default:'[]'"`
	PrimaryColor      string    `json:"primary_color" gorm:"not null;size:64;default:''"`
	PreviewAssetsJSON string    `json:"preview_assets_json" gorm:"not null;type:text;default:'[]'"`
	TemplatePath      string    `json:"template_path" gorm:"not null;type:text;default:''"`
	DesignSpecPath    string    `json:"design_spec_path" gorm:"not null;type:text;default:''"`
	Checksum          string    `json:"checksum" gorm:"not null;size:128;default:''"`
	CompatibilityJSON string    `json:"compatibility_json" gorm:"not null;type:text;default:'{}'"`
	SyncedAt          time.Time `json:"synced_at" gorm:"not null"`
	CreatedAt         time.Time `json:"created_at" gorm:"not null"`
	UpdatedAt         time.Time `json:"updated_at" gorm:"not null"`
}

func (TemplateRegistryEntry) TableName() string {
	return "template_registry_entries"
}
