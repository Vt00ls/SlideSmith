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
	TaskStatusPPTXValidating             = "pptx_validating"
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
	TaskStatusBeautifyInventoryBuilding = "beautify_inventory_building"
	TaskStatusBeautifyPlanning          = "beautify_planning"
	TaskStatusAwaitingBeautifyConfirm   = "awaiting_beautify_confirm"
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
	ArtifactKindSVGInventory            = "svg_inventory"
	ArtifactKindSVGResourceUsage        = "svg_resource_usage"
	ArtifactKindChartUsage              = "chart_usage"
	ArtifactKindNotesInventory          = "notes_inventory"
	ArtifactKindSpeakerNotes            = "speaker_notes"
	ArtifactKindSVGQualityReport        = "svg_quality_report"
	ArtifactKindChartVerifyReport       = "chart_verify_report"
	ArtifactKindQualitySummary          = "quality_summary"
	ArtifactKindPPTXReadback            = "pptx_readback"
	ArtifactKindPPTXTextInventory       = "pptx_text_inventory"
	ArtifactKindPPTXValidateReport      = "pptx_validate_report"
	ArtifactKindRenderedPDF             = "rendered_pdf"
	ArtifactKindRenderedSlide           = "rendered_slide"
	ArtifactKindContactSheet            = "contact_sheet"
	ArtifactKindVisualReviewReport      = "visual_review_report"
	ArtifactKindBeautifyInputs          = "beautify_inputs"
	ArtifactKindBeautifyInventory       = "beautify_inventory"
	ArtifactKindBeautifyRiskReport      = "beautify_risk_report"
	ArtifactKindBeautifyPlan            = "beautify_plan"
	ArtifactKindBeautifyLock            = "beautify_lock"
	ArtifactKindBeautifyFidelityReport  = "beautify_fidelity_report"
	ArtifactKindSourceSVGReference      = "source_svg_reference"
	ArtifactKindManualEditPatch         = "manual_edit_patch"
	ArtifactKindManualEditApplyReport   = "manual_edit_apply_report"
	ArtifactKindAnnotationApplyReport   = "annotation_apply_report"
	ArtifactKindManualEditDiffReport    = "manual_edit_diff_report"
	ArtifactKindManualEditLock          = "manual_edit_lock"
	ArtifactKindManualEditLog           = "manual_edit_log"
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

const (
	ArtifactVersionStatusStaging = "staging"
	ArtifactVersionStatusActive  = "active"
	ArtifactVersionStatusFailed  = "failed"

	ArtifactVersionSourceGeneration = "generation"
	ArtifactVersionSourceManualEdit = "manual_edit"
)

type TaskArtifactVersion struct {
	ID                     string     `json:"id" gorm:"primaryKey;size:64"`
	TaskID                 string     `json:"task_id" gorm:"not null;size:64;uniqueIndex:idx_task_artifact_version,priority:1;index"`
	Version                string     `json:"version" gorm:"not null;size:64;uniqueIndex:idx_task_artifact_version,priority:2"`
	Status                 string     `json:"status" gorm:"not null;size:32;index"`
	Source                 string     `json:"source" gorm:"not null;size:32;index"`
	ParentVersion          string     `json:"parent_version" gorm:"not null;size:64;default:''"`
	ArtifactManifestSHA256 string     `json:"artifact_manifest_sha256" gorm:"not null;size:64"`
	PPTXArtifactID         string     `json:"pptx_artifact_id" gorm:"not null;size:64;default:''"`
	EditSessionID          string     `json:"edit_session_id" gorm:"not null;size:64;default:'';index"`
	EditRevision           int64      `json:"edit_revision" gorm:"not null;default:0"`
	MetadataJSON           string     `json:"metadata_json" gorm:"not null;type:text;default:'{}'"`
	CreatedAt              time.Time  `json:"created_at" gorm:"not null"`
	ActivatedAt            *time.Time `json:"activated_at,omitempty" gorm:"index"`
	FailedAt               *time.Time `json:"failed_at,omitempty"`
}

func (TaskArtifactVersion) TableName() string { return "task_artifact_versions" }

const (
	EditSessionStatusDraft               = "draft"
	EditSessionStatusQueued              = "queued"
	EditSessionStatusMaterializing       = "materializing"
	EditSessionStatusApplyingDirect      = "applying_direct_edits"
	EditSessionStatusApplyingAnnotations = "applying_annotations"
	EditSessionStatusSVGValidating       = "svg_validating"
	EditSessionStatusQualityChecking     = "quality_checking"
	EditSessionStatusExporting           = "exporting"
	EditSessionStatusPPTXValidating      = "pptx_validating"
	EditSessionStatusPublishing          = "publishing"
	EditSessionStatusPublished           = "published"
	EditSessionStatusFailed              = "failed"
	EditSessionStatusStale               = "stale"
	EditSessionStatusDiscarded           = "discarded"
)

var ActiveEditSessionStatuses = []string{
	EditSessionStatusDraft,
	EditSessionStatusQueued,
	EditSessionStatusMaterializing,
	EditSessionStatusApplyingDirect,
	EditSessionStatusApplyingAnnotations,
	EditSessionStatusSVGValidating,
	EditSessionStatusQualityChecking,
	EditSessionStatusExporting,
	EditSessionStatusPPTXValidating,
	EditSessionStatusPublishing,
}

func IsActiveEditSessionStatus(status string) bool {
	for _, active := range ActiveEditSessionStatuses {
		if status == active {
			return true
		}
	}
	return false
}

type TaskEditSession struct {
	ID                         string     `json:"id" gorm:"primaryKey;size:64"`
	TaskID                     string     `json:"task_id" gorm:"not null;size:64;index"`
	BasePublishVersion         string     `json:"base_publish_version" gorm:"not null;size:64;index"`
	BaseArtifactManifestSHA256 string     `json:"base_artifact_manifest_sha256" gorm:"not null;size:64"`
	BaseSVGInventorySHA256     string     `json:"base_svg_inventory_sha256" gorm:"not null;size:64"`
	Status                     string     `json:"status" gorm:"not null;size:48;index"`
	Revision                   int64      `json:"revision" gorm:"not null;default:1"`
	DraftJSON                  string     `json:"draft" gorm:"not null;type:text;default:'{}'"`
	DraftSHA256                string     `json:"draft_sha256" gorm:"not null;size:64"`
	FrozenRevision             int64      `json:"frozen_revision" gorm:"not null;default:0"`
	FrozenPatchSHA256          string     `json:"frozen_patch_sha256" gorm:"not null;size:64;default:''"`
	ResultPublishVersion       string     `json:"result_publish_version" gorm:"not null;size:64;default:''"`
	CapabilitySnapshotJSON     string     `json:"capability_snapshot" gorm:"not null;type:text;default:'{}'"`
	ExecutionClaimToken        string     `json:"-" gorm:"not null;size:64;default:''"`
	ExecutionClaimedAt         *time.Time `json:"-"`
	LastRunID                  string     `json:"last_run_id" gorm:"not null;size:64;default:''"`
	ErrorMessage               string     `json:"error_message" gorm:"not null;type:text;default:''"`
	FailurePhase               string     `json:"failure_phase" gorm:"not null;size:128;default:''"`
	FailureMetadataJSON        string     `json:"failure_metadata" gorm:"not null;type:text;default:'{}'"`
	CreatedAt                  time.Time  `json:"created_at" gorm:"not null"`
	UpdatedAt                  time.Time  `json:"updated_at" gorm:"not null"`
	AppliedAt                  *time.Time `json:"applied_at,omitempty"`
	PublishedAt                *time.Time `json:"published_at,omitempty"`
	DiscardedAt                *time.Time `json:"discarded_at,omitempty"`
}

func (TaskEditSession) TableName() string { return "task_edit_sessions" }

const (
	EditPhaseMaterialize     = "manual_edit_materialize"
	EditPhaseApplyDirect     = "manual_edit_apply_direct"
	EditPhaseApplyAnnotation = "manual_edit_apply_annotations"
	EditPhaseSVGValidate     = "manual_edit_svg_validate"
	EditPhaseQualityCheck    = "manual_edit_quality_check"
	EditPhaseFinalizeExport  = "manual_edit_finalize_export"
	EditPhasePPTXValidate    = "manual_edit_pptx_validate"
	EditPhasePublish         = "manual_edit_publish"
)

type TaskEditRun struct {
	ID              string     `json:"id" gorm:"primaryKey;size:64"`
	TaskID          string     `json:"task_id" gorm:"not null;size:64;index"`
	EditSessionID   string     `json:"edit_session_id" gorm:"not null;size:64;index"`
	Phase           string     `json:"phase" gorm:"not null;size:64;index"`
	Attempt         int        `json:"attempt" gorm:"not null;default:1"`
	Runner          string     `json:"runner" gorm:"not null;size:64"`
	Status          string     `json:"status" gorm:"not null;size:32;index"`
	WorkspacePath   string     `json:"-" gorm:"not null;type:text;default:''"`
	RuntimeRunID    string     `json:"runtime_run_id" gorm:"not null;size:128;default:''"`
	InputJSON       string     `json:"input" gorm:"not null;type:text;default:'{}'"`
	OutputJSON      string     `json:"output" gorm:"not null;type:text;default:'{}'"`
	ErrorMessage    string     `json:"error_message" gorm:"not null;type:text;default:''"`
	FailureMetadata string     `json:"failure_metadata" gorm:"not null;type:text;default:'{}'"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at" gorm:"not null"`
	UpdatedAt       time.Time  `json:"updated_at" gorm:"not null"`
}

func (TaskEditRun) TableName() string { return "task_edit_runs" }

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
