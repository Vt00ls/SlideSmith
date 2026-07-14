package model

import "time"

const (
	TaskStatusCreated                    = "created"
	TaskStatusUploaded                   = "uploaded"
	TaskStatusRuntimePreparing           = "runtime_preparing"
	TaskStatusSourceConverting           = "source_converting"
	TaskStatusAwaitingConfirm            = "awaiting_confirm"
	TaskStatusAwaitingAnchorConfirm      = "awaiting_anchor_confirm"
	TaskStatusRealizationDeriving        = "realization_deriving"
	TaskStatusAwaitingRealizationConfirm = "awaiting_realization_confirm"
	TaskStatusSpecGenerating             = "spec_generating"
	TaskStatusAwaitingSpecConfirm        = "awaiting_spec_confirm"
	TaskStatusImageAcquiring             = "image_acquiring"
	TaskStatusSVGGenerating              = "svg_generating"
	TaskStatusQualityChecking            = "quality_checking"
	TaskStatusExporting                  = "exporting"
	TaskStatusPublishing                 = "publishing"
	TaskStatusCompleted                  = "completed"
	TaskStatusFailed                     = "failed"
	TaskStatusCancelled                  = "cancelled"
)

const (
	TaskStatusTemplateFillPlanning        = "template_fill_planning"
	TaskStatusAwaitingTemplateFillConfirm = "awaiting_template_fill_confirm"
	TaskStatusTemplateFillChecking        = "template_fill_checking"
	TaskStatusTemplateFillApplying        = "template_fill_applying"
	TaskStatusTemplateFillValidating      = "template_fill_validating"
)

const (
	TaskRouteMain         = "main"
	TaskRouteBeautify     = "beautify"
	TaskRouteTemplateFill = "template-fill"
)

const (
	RunnerProfileFullPPTMaster      = "full-ppt-master"
	RunnerProfileRealLite           = "real-lite"
	RunnerProfileSmoke              = "smoke"
	RunnerProfileNativeTemplateFill = "native-template-fill"
)

const (
	RunnerProfileSourceDeploymentDefault = "deployment_default"
	RunnerProfileSourceExplicitConfig    = "explicit_config"
	RunnerProfileSourceLegacyManifest    = "legacy_manifest"
	RunnerProfileSourceLegacyEvidence    = "legacy_evidence"
)

const (
	EventTypeStatus       = "status"
	EventTypeLog          = "log"
	EventTypeRuntime      = "runtime"
	EventTypeArtifact     = "artifact"
	EventTypeConfirmation = "confirmation"
	EventTypeError        = "error"
)

const (
	ArtifactKindSource                  = "source"
	ArtifactKindSourceMarkdown          = "source_markdown"
	ArtifactKindSourceConversionProfile = "source_conversion_profile"
	ArtifactKindSourceProfile           = "source_profile"
	ArtifactKindDesignSpec              = "design_spec"
	ArtifactKindSpecLock                = "spec_lock"
	ArtifactKindSVGOutput               = "svg_output"
	ArtifactKindSVGFinal                = "svg_final"
	ArtifactKindPPTX                    = "pptx"
	ArtifactKindPPTXIdentity            = "pptx_identity"
	ArtifactKindPPTXSlideLibrary        = "pptx_slide_library"
	ArtifactKindLog                     = "log"
	ArtifactKindManifest                = "manifest"
	ArtifactKindResourcePlan            = "resource_plan"
	ArtifactKindResourcePolicy          = "resource_policy"
	ArtifactKindResourceRequirements    = "resource_requirements"
	ArtifactKindResourceManifest        = "resource_manifest"
	ArtifactKindResourceAsset           = "resource_asset"
	ArtifactKindImageAnalysis           = "image_analysis"
	ArtifactKindImagePromptManifest     = "image_prompt_manifest"
	ArtifactKindImagePromptReview       = "image_prompt_review"
	ArtifactKindImageQueryManifest      = "image_query_manifest"
	ArtifactKindImageSourceManifest     = "image_source_manifest"
	ArtifactKindFormulaManifest         = "formula_manifest"
	ArtifactKindChartData               = "chart_data"
	ArtifactKindChartTemplate           = "chart_template"
	ArtifactKindOther                   = "other"
)

const (
	ArtifactKindTemplateFillPlan           = "template_fill_plan"
	ArtifactKindTemplateFillCheckReport    = "template_fill_check_report"
	ArtifactKindTemplateFillValidateReport = "template_fill_validate_report"
	ArtifactKindTemplateFillReadback       = "template_fill_readback"
)

type Task struct {
	ID                      string     `json:"id" gorm:"primaryKey;size:64"`
	Title                   string     `json:"title" gorm:"not null;size:255"`
	Status                  string     `json:"status" gorm:"not null;size:64;index"`
	RuntimeProject          string     `json:"runtime_project" gorm:"not null;size:128;default:''"`
	LastRuntimeRunID        string     `json:"last_runtime_run_id" gorm:"not null;size:128;default:''"`
	LastRuntimeSessionID    string     `json:"last_runtime_session_id" gorm:"not null;size:128;default:''"`
	RuntimeWorkspacePath    string     `json:"runtime_workspace_path" gorm:"not null;type:text;default:''"`
	SelectedTemplateID      string     `json:"selected_template_id" gorm:"not null;size:255;default:''"`
	TemplateLockJSON        string     `json:"template_lock_json" gorm:"not null;type:text;default:'{}'"`
	Route                   string     `json:"route" gorm:"not null;size:64;default:'main';index"`
	RouteReason             string     `json:"route_reason" gorm:"not null;type:text;default:''"`
	RouteStandaloneWorkflow string     `json:"route_standalone_workflow" gorm:"not null;size:64;default:''"`
	RouteSelectionJSON      string     `json:"route_selection_json" gorm:"not null;type:text;default:'{}'"`
	RouteSelectedAt         *time.Time `json:"route_selected_at,omitempty"`
	RunnerProfile           string     `json:"runner_profile" gorm:"not null;size:64;default:'';index"`
	RunnerProfileSource     string     `json:"runner_profile_source" gorm:"not null;size:64;default:''"`
	RunnerProfileLockedAt   *time.Time `json:"runner_profile_locked_at,omitempty"`
	ErrorMessage            string     `json:"error_message" gorm:"not null;type:text;default:''"`
	FailurePhase            string     `json:"failure_phase" gorm:"not null;size:128;default:''"`
	FailureMetadata         string     `json:"failure_metadata" gorm:"not null;type:text;default:'{}'"`
	ExecutionClaimToken     string     `json:"-" gorm:"not null;size:64;default:''"`
	ExecutionClaimedAt      *time.Time `json:"-"`
	CreatedAt               time.Time  `json:"created_at" gorm:"not null"`
	UpdatedAt               time.Time  `json:"updated_at" gorm:"not null"`
	StartedAt               *time.Time `json:"started_at,omitempty"`
	CompletedAt             *time.Time `json:"completed_at,omitempty"`
	CancelledAt             *time.Time `json:"cancelled_at,omitempty"`
}

func (Task) TableName() string {
	return "tasks"
}

type TaskEvent struct {
	ID        string    `json:"id" gorm:"primaryKey;size:64"`
	TaskID    string    `json:"task_id" gorm:"not null;size:64;uniqueIndex:idx_task_events_task_seq,priority:1"`
	Seq       int64     `json:"seq" gorm:"not null;uniqueIndex:idx_task_events_task_seq,priority:2"`
	Type      string    `json:"type" gorm:"not null;size:64;index"`
	Status    string    `json:"status" gorm:"not null;size:64;default:''"`
	Message   string    `json:"message" gorm:"not null;type:text;default:''"`
	Source    string    `json:"source" gorm:"not null;size:64;default:'platform'"`
	Payload   string    `json:"payload" gorm:"not null;type:text;default:'{}'"`
	CreatedAt time.Time `json:"created_at" gorm:"not null"`
}

func (TaskEvent) TableName() string {
	return "task_events"
}

type Artifact struct {
	ID             string    `json:"id" gorm:"primaryKey;size:64"`
	TaskID         string    `json:"task_id" gorm:"not null;size:64;index"`
	Kind           string    `json:"kind" gorm:"not null;size:64;index"`
	Name           string    `json:"name" gorm:"not null;size:255"`
	Storage        string    `json:"storage" gorm:"not null;size:32;default:'local'"`
	ObjectKey      string    `json:"object_key" gorm:"not null;type:text"`
	MimeType       string    `json:"mime_type" gorm:"not null;size:255;default:''"`
	Size           int64     `json:"size" gorm:"not null;default:0"`
	SHA256         string    `json:"sha256" gorm:"not null;size:64;default:''"`
	PublishVersion string    `json:"publish_version" gorm:"not null;size:64;default:'';index"`
	MetadataJSON   string    `json:"metadata_json" gorm:"not null;type:text;default:'{}'"`
	CreatedAt      time.Time `json:"created_at" gorm:"not null"`
	UpdatedAt      time.Time `json:"updated_at" gorm:"not null"`
}

func (Artifact) TableName() string {
	return "artifacts"
}

type TaskRuntimeRun struct {
	ID                  string     `json:"id" gorm:"primaryKey;size:64"`
	TaskID              string     `json:"task_id" gorm:"not null;size:64;index"`
	ExecutionClaimToken string     `json:"-" gorm:"not null;size:64;default:''"`
	TaskStatus          string     `json:"-" gorm:"not null;size:64;default:''"`
	Runtime             string     `json:"runtime" gorm:"not null;size:64;default:'agent-compose'"`
	Agent               string     `json:"agent" gorm:"not null;size:128;default:'ppt_master'"`
	Phase               string     `json:"phase" gorm:"not null;size:64"`
	Command             string     `json:"command" gorm:"not null;type:text"`
	ExternalRunID       string     `json:"external_run_id" gorm:"not null;size:128;default:''"`
	ExternalSessionID   string     `json:"external_session_id" gorm:"not null;size:128;default:''"`
	Status              string     `json:"status" gorm:"not null;size:64;index"`
	ExitCode            *int       `json:"exit_code,omitempty"`
	WorkspacePath       string     `json:"workspace_path" gorm:"not null;type:text;default:''"`
	RawResponse         string     `json:"raw_response" gorm:"not null;type:text;default:''"`
	ErrorMessage        string     `json:"error_message" gorm:"not null;type:text;default:''"`
	StderrTail          string     `json:"stderr_tail" gorm:"not null;type:text;default:''"`
	FailurePhase        string     `json:"failure_phase" gorm:"not null;size:128;default:''"`
	FailureMetadata     string     `json:"failure_metadata" gorm:"not null;type:text;default:'{}'"`
	CreatedAt           time.Time  `json:"created_at" gorm:"not null"`
	UpdatedAt           time.Time  `json:"updated_at" gorm:"not null"`
	StartedAt           *time.Time `json:"started_at,omitempty"`
	FinishedAt          *time.Time `json:"finished_at,omitempty"`
}

func (TaskRuntimeRun) TableName() string {
	return "task_runtime_runs"
}

type TaskPhaseRun struct {
	ID                  string     `json:"id" gorm:"primaryKey;size:64"`
	TaskID              string     `json:"task_id" gorm:"not null;size:64;index"`
	ExecutionClaimToken string     `json:"-" gorm:"not null;size:64;default:''"`
	TaskStatus          string     `json:"-" gorm:"not null;size:64;default:''"`
	Phase               string     `json:"phase" gorm:"not null;size:64;index"`
	Attempt             int        `json:"attempt" gorm:"not null;default:1"`
	Runner              string     `json:"runner" gorm:"not null;size:64;default:''"`
	Status              string     `json:"status" gorm:"not null;size:64;index"`
	StartedAt           *time.Time `json:"started_at,omitempty"`
	FinishedAt          *time.Time `json:"finished_at,omitempty"`
	RuntimeRunID        string     `json:"runtime_run_id" gorm:"not null;size:128;default:''"`
	RuntimeSessionID    string     `json:"runtime_session_id" gorm:"not null;size:128;default:''"`
	WorkspacePath       string     `json:"workspace_path" gorm:"not null;type:text;default:''"`
	InputJSON           string     `json:"input_json" gorm:"not null;type:text;default:'{}'"`
	OutputJSON          string     `json:"output_json" gorm:"not null;type:text;default:'{}'"`
	ErrorMessage        string     `json:"error_message" gorm:"not null;type:text;default:''"`
	FailureMetadata     string     `json:"failure_metadata" gorm:"not null;type:text;default:'{}'"`
	CreatedAt           time.Time  `json:"created_at" gorm:"not null"`
	UpdatedAt           time.Time  `json:"updated_at" gorm:"not null"`
}

func (TaskPhaseRun) TableName() string {
	return "task_phase_runs"
}

type TaskConfirmation struct {
	ID             string     `json:"id" gorm:"primaryKey;size:64"`
	TaskID         string     `json:"task_id" gorm:"not null;size:64;uniqueIndex:idx_task_confirmations_task_key,priority:1"`
	Key            string     `json:"key" gorm:"not null;size:128;uniqueIndex:idx_task_confirmations_task_key,priority:2"`
	Label          string     `json:"label" gorm:"not null;size:255"`
	Required       bool       `json:"required" gorm:"not null;default:true"`
	OptionsJSON    string     `json:"options_json" gorm:"not null;type:text;default:'[]'"`
	Recommendation string     `json:"recommendation" gorm:"not null;type:text;default:''"`
	ValueJSON      string     `json:"value_json" gorm:"not null;type:text;default:'null'"`
	Status         string     `json:"status" gorm:"not null;size:64;default:'pending'"`
	CreatedAt      time.Time  `json:"created_at" gorm:"not null"`
	UpdatedAt      time.Time  `json:"updated_at" gorm:"not null"`
	SubmittedAt    *time.Time `json:"submitted_at,omitempty"`
}

func (TaskConfirmation) TableName() string {
	return "task_confirmations"
}
