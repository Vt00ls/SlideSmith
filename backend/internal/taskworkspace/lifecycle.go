// Package taskworkspace owns the closed Task Workspace Lifecycle seam.
package taskworkspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

type (
	PolicyDomainID          string
	TaskID                  string
	TaskWorkspaceID         string
	RevisionID              string
	MaterializationID       string
	RuntimeViewID           string
	PhaseRunID              string
	RuntimeRunID            string
	SandboxLeaseID          string
	SandboxLeaseAuthorityID string
	OperationID             string
	Digest                  string
	Generation              uint64
	Fence                   uint64
	LeaseGeneration         uint64
	LeaseFence              uint64
	Instant                 int64
	Duration                int64
	ExpiryPolicyID          string
)

type RuntimeViewEffectClass string

const (
	RuntimeViewReadOnly RuntimeViewEffectClass = "read_only"
	RuntimeViewMutating RuntimeViewEffectClass = "mutating"
)

type SandboxLeaseAuthority struct {
	ID                 SandboxLeaseID
	EvidenceID         EvidenceID
	Digest             Digest
	AuthorityID        SandboxLeaseAuthorityID
	PolicyDomainID     PolicyDomainID
	TaskID             TaskID
	PhaseRunID         PhaseRunID
	RuntimeRunID       RuntimeRunID
	RuntimeOperationID OperationID
	EffectClass        RuntimeViewEffectClass
	LeaseGeneration    LeaseGeneration
	LeaseFence         LeaseFence
	ExpiresAt          Instant
}

func (a SandboxLeaseAuthority) CanonicalDigest() Digest {
	return canonicalDigest(struct {
		ID                 SandboxLeaseID
		EvidenceID         EvidenceID
		AuthorityID        SandboxLeaseAuthorityID
		PolicyDomainID     PolicyDomainID
		TaskID             TaskID
		PhaseRunID         PhaseRunID
		RuntimeRunID       RuntimeRunID
		RuntimeOperationID OperationID
		EffectClass        RuntimeViewEffectClass
		LeaseGeneration    LeaseGeneration
		LeaseFence         LeaseFence
		ExpiresAt          Instant
	}{
		ID:                 a.ID,
		EvidenceID:         a.EvidenceID,
		AuthorityID:        a.AuthorityID,
		PolicyDomainID:     a.PolicyDomainID,
		TaskID:             a.TaskID,
		PhaseRunID:         a.PhaseRunID,
		RuntimeRunID:       a.RuntimeRunID,
		RuntimeOperationID: a.RuntimeOperationID,
		EffectClass:        a.EffectClass,
		LeaseGeneration:    a.LeaseGeneration,
		LeaseFence:         a.LeaseFence,
		ExpiresAt:          a.ExpiresAt,
	})
}

type Operation struct {
	ID            OperationID
	RequestDigest Digest
}

type ErrorCode string

type SafeErrorCategory string

const (
	ErrorInvalidIntent          ErrorCode = "invalid_intent"
	ErrorIntegrityConflict      ErrorCode = "integrity_conflict"
	ErrorIntegrityFailure       ErrorCode = "integrity_failure"
	ErrorOwnershipDenied        ErrorCode = "ownership_denied"
	ErrorStaleAuthority         ErrorCode = "stale_authority"
	ErrorViewTerminalConflict   ErrorCode = "view_terminal_conflict"
	ErrorEffectDenied           ErrorCode = "effect_denied"
	ErrorExpiryBlocked          ErrorCode = "expiry_blocked"
	ErrorCheckpointNotRetained  ErrorCode = "checkpoint_not_retained"
	ErrorRecoveryReadOnly       ErrorCode = "recovery_read_only"
	ErrorReconciliationRequired ErrorCode = "reconciliation_required"
	ErrorDurabilityUnverified   ErrorCode = "durability_unverified"
	ErrorResourceExhausted      ErrorCode = "resource_exhausted"
	ErrorRetryableUnavailable   ErrorCode = "retryable_unavailable"
	ErrorCleanupDebt            ErrorCode = "cleanup_debt"

	SafeErrorAuthorizationDenied          SafeErrorCategory = "authorization_denial"
	SafeErrorInvalidIntent                SafeErrorCategory = "invalid_intent"
	SafeErrorIdempotencyConflict          SafeErrorCategory = "idempotency_conflict"
	SafeErrorStaleRevisionGenerationFence SafeErrorCategory = "stale_revision_generation_fence"
	SafeErrorTerminalConflict             SafeErrorCategory = "terminal_conflict"
	SafeErrorIntegrityUnavailableContent  SafeErrorCategory = "integrity_unavailable_content"
	SafeErrorDurabilityUnverified         SafeErrorCategory = "durability_unverified"
	SafeErrorResourceExhausted            SafeErrorCategory = "resource_exhausted"
	SafeErrorRetryableUnavailable         SafeErrorCategory = "retryable_unavailable"
	SafeErrorReconciliationRequired       SafeErrorCategory = "ambiguous_reconciliation_required"
	SafeErrorCleanupDebt                  SafeErrorCategory = "cleanup_debt"
)

type Error struct {
	Code ErrorCode
}

func (e *Error) Error() string {
	switch e.Code {
	case ErrorIntegrityConflict:
		return "task workspace lifecycle operation integrity conflict"
	case ErrorIntegrityFailure:
		return "task workspace lifecycle evidence integrity failure"
	case ErrorOwnershipDenied:
		return "task workspace lifecycle authority denied"
	case ErrorStaleAuthority:
		return "task workspace lifecycle authority is stale"
	case ErrorViewTerminalConflict:
		return "task workspace lifecycle view is already terminal"
	case ErrorEffectDenied:
		return "task workspace lifecycle effect is not permitted"
	case ErrorExpiryBlocked:
		return "task workspace lifecycle expiry is blocked"
	case ErrorCheckpointNotRetained:
		return "task workspace lifecycle Checkpoint is not retained"
	case ErrorRecoveryReadOnly:
		return "task workspace lifecycle recovery is read-only"
	case ErrorReconciliationRequired:
		return "task workspace lifecycle operation requires reconciliation"
	case ErrorDurabilityUnverified:
		return "task workspace lifecycle durability is unverified"
	case ErrorResourceExhausted:
		return "task workspace lifecycle resource capacity is exhausted"
	case ErrorRetryableUnavailable:
		return "task workspace lifecycle dependency is temporarily unavailable"
	case ErrorCleanupDebt:
		return "task workspace lifecycle cleanup remains pending"
	default:
		return "task workspace lifecycle intent is invalid"
	}
}

func (e *Error) SafeCategory() SafeErrorCategory {
	if e == nil {
		return ""
	}
	switch e.Code {
	case ErrorOwnershipDenied:
		return SafeErrorAuthorizationDenied
	case ErrorIntegrityConflict:
		return SafeErrorIdempotencyConflict
	case ErrorStaleAuthority:
		return SafeErrorStaleRevisionGenerationFence
	case ErrorViewTerminalConflict:
		return SafeErrorTerminalConflict
	case ErrorIntegrityFailure, ErrorCheckpointNotRetained:
		return SafeErrorIntegrityUnavailableContent
	case ErrorDurabilityUnverified:
		return SafeErrorDurabilityUnverified
	case ErrorResourceExhausted:
		return SafeErrorResourceExhausted
	case ErrorRetryableUnavailable, ErrorRecoveryReadOnly:
		return SafeErrorRetryableUnavailable
	case ErrorReconciliationRequired:
		return SafeErrorReconciliationRequired
	case ErrorCleanupDebt, ErrorExpiryBlocked:
		return SafeErrorCleanupDebt
	default:
		return SafeErrorInvalidIntent
	}
}

func (e *Error) Retryable() bool {
	return e != nil && e.Code == ErrorRetryableUnavailable
}

func (e *Error) ReconciliationRequired() bool {
	return e != nil && e.Code == ErrorReconciliationRequired
}

// normalizeLifecycleError copies only a known lifecycle code and never
// retains an error chain or message.
func normalizeLifecycleError(err error) *Error {
	var lifecycleError *Error
	if errors.As(err, &lifecycleError) && lifecycleError != nil &&
		knownLifecycleErrorCode(lifecycleError.Code) {
		return &Error{Code: lifecycleError.Code}
	}
	return &Error{Code: ErrorRetryableUnavailable}
}

// normalizeProjectionAdapterError prevents a projection adapter from
// inventing lifecycle authority semantics. Only the two projection-delivery
// dispositions may cross this internal seam.
func normalizeProjectionAdapterError(err error) *Error {
	normalized := normalizeLifecycleError(err)
	if normalized.Code == ErrorRetryableUnavailable ||
		normalized.Code == ErrorReconciliationRequired {
		return normalized
	}
	return &Error{Code: ErrorRetryableUnavailable}
}

func knownLifecycleErrorCode(code ErrorCode) bool {
	switch code {
	case ErrorInvalidIntent, ErrorIntegrityConflict, ErrorIntegrityFailure,
		ErrorOwnershipDenied, ErrorStaleAuthority, ErrorViewTerminalConflict,
		ErrorEffectDenied, ErrorExpiryBlocked, ErrorCheckpointNotRetained,
		ErrorRecoveryReadOnly, ErrorReconciliationRequired, ErrorDurabilityUnverified,
		ErrorResourceExhausted, ErrorRetryableUnavailable, ErrorCleanupDebt:
		return true
	default:
		return false
	}
}

type ConfirmTaskWorkspaceRequest struct {
	PolicyDomainID PolicyDomainID
	TaskID         TaskID
	Operation      Operation
}

func (r ConfirmTaskWorkspaceRequest) CanonicalRequestDigest() Digest {
	return canonicalDigest(struct {
		Kind           string
		PolicyDomainID PolicyDomainID
		TaskID         TaskID
		OperationID    OperationID
	}{
		Kind:           "confirm_task_workspace",
		PolicyDomainID: r.PolicyDomainID,
		TaskID:         r.TaskID,
		OperationID:    r.Operation.ID,
	})
}

type ConfirmTaskWorkspaceResult struct {
	TaskWorkspaceID     TaskWorkspaceID
	CurrentRevisionID   RevisionID
	CurrentCheckpointID CheckpointID
	Generation          Generation
	Fence               Fence
}

type MaterializeRequest struct {
	PolicyDomainID  PolicyDomainID
	TaskID          TaskID
	TaskWorkspaceID TaskWorkspaceID
	RevisionID      RevisionID
	CheckpointID    CheckpointID
	Generation      Generation
	Fence           Fence
	Operation       Operation
}

func (r MaterializeRequest) CanonicalRequestDigest() Digest {
	return canonicalDigest(struct {
		Kind            string
		PolicyDomainID  PolicyDomainID
		TaskID          TaskID
		TaskWorkspaceID TaskWorkspaceID
		RevisionID      RevisionID
		CheckpointID    CheckpointID
		Generation      Generation
		Fence           Fence
		OperationID     OperationID
	}{
		Kind:            "materialize",
		PolicyDomainID:  r.PolicyDomainID,
		TaskID:          r.TaskID,
		TaskWorkspaceID: r.TaskWorkspaceID,
		RevisionID:      r.RevisionID,
		CheckpointID:    r.CheckpointID,
		Generation:      r.Generation,
		Fence:           r.Fence,
		OperationID:     r.Operation.ID,
	})
}

type MaterializeResult struct {
	MaterializationID      MaterializationID
	TaskWorkspaceID        TaskWorkspaceID
	RevisionID             RevisionID
	CheckpointID           CheckpointID
	ManifestDigest         Digest
	ContentEvidenceRoot    EvidenceRoot
	DurabilityEvidenceRoot EvidenceRoot
	CheckpointEvidence     CheckpointEvidence
	Generation             Generation
	Fence                  Fence
}

type OpenRuntimeViewRequest struct {
	PolicyDomainID        PolicyDomainID
	TaskID                TaskID
	TaskWorkspaceID       TaskWorkspaceID
	MaterializationID     MaterializationID
	BaseRevisionID        RevisionID
	PhaseRunID            PhaseRunID
	RuntimeRunID          RuntimeRunID
	RuntimeOperationID    OperationID
	SandboxLeaseAuthority SandboxLeaseAuthority
	EffectClass           RuntimeViewEffectClass
	ExpiresAt             Instant
	Generation            Generation
	Fence                 Fence
	Operation             Operation
}

func (r OpenRuntimeViewRequest) CanonicalRequestDigest() Digest {
	return canonicalDigest(struct {
		Kind                  string
		PolicyDomainID        PolicyDomainID
		TaskID                TaskID
		TaskWorkspaceID       TaskWorkspaceID
		MaterializationID     MaterializationID
		BaseRevisionID        RevisionID
		PhaseRunID            PhaseRunID
		RuntimeRunID          RuntimeRunID
		RuntimeOperationID    OperationID
		SandboxLeaseAuthority SandboxLeaseAuthority
		EffectClass           RuntimeViewEffectClass
		ExpiresAt             Instant
		Generation            Generation
		Fence                 Fence
		OperationID           OperationID
	}{
		Kind:                  "open_runtime_view",
		PolicyDomainID:        r.PolicyDomainID,
		TaskID:                r.TaskID,
		TaskWorkspaceID:       r.TaskWorkspaceID,
		MaterializationID:     r.MaterializationID,
		BaseRevisionID:        r.BaseRevisionID,
		PhaseRunID:            r.PhaseRunID,
		RuntimeRunID:          r.RuntimeRunID,
		RuntimeOperationID:    r.RuntimeOperationID,
		SandboxLeaseAuthority: r.SandboxLeaseAuthority,
		EffectClass:           r.EffectClass,
		ExpiresAt:             r.ExpiresAt,
		Generation:            r.Generation,
		Fence:                 r.Fence,
		OperationID:           r.Operation.ID,
	})
}

type OpenRuntimeViewResult struct {
	PolicyDomainID          PolicyDomainID
	TaskID                  TaskID
	RuntimeViewID           RuntimeViewID
	TaskWorkspaceID         TaskWorkspaceID
	MaterializationID       MaterializationID
	BaseRevisionID          RevisionID
	PhaseRunID              PhaseRunID
	RuntimeRunID            RuntimeRunID
	RuntimeOperationID      OperationID
	SandboxLeaseAuthority   SandboxLeaseAuthority
	EffectClass             RuntimeViewEffectClass
	ExpiresAt               Instant
	Generation              Generation
	Fence                   Fence
	ReadOnlyInputs          []ReadOnlyInputMaterialization
	SourceArtifactVersionID ArtifactVersionID
	Operation               Operation
}

type Lifecycle interface {
	ConfirmTaskWorkspace(context.Context, ConfirmTaskWorkspaceRequest) (ConfirmTaskWorkspaceResult, error)
	Materialize(context.Context, MaterializeRequest) (MaterializeResult, error)
	OpenRuntimeView(context.Context, OpenRuntimeViewRequest) (OpenRuntimeViewResult, error)
	CommitRuntimeView(context.Context, CommitRuntimeViewRequest) (CommitRuntimeViewResult, error)
	DiscardRuntimeView(context.Context, DiscardRuntimeViewRequest) (DiscardRuntimeViewResult, error)
	FenceRuntimeView(context.Context, FenceRuntimeViewRequest) (FenceRuntimeViewResult, error)
	ExpireMaterialization(context.Context, ExpireMaterializationRequest) (ExpireMaterializationResult, error)
	ExpireRuntimeView(context.Context, ExpireRuntimeViewRequest) (ExpireRuntimeViewResult, error)
	RestoreTaskWorkspace(context.Context, RestoreTaskWorkspaceRequest) (RestoreTaskWorkspaceResult, error)
	ReconstructTaskWorkspace(context.Context, ReconstructTaskWorkspaceRequest) (ReconstructTaskWorkspaceResult, error)
	InspectCheckpointRetention(context.Context, InspectCheckpointRetentionRequest) (CheckpointRetention, error)
	AttachCheckpointRetention(context.Context, AttachCheckpointRetentionRequest) (CheckpointRetention, error)
	ReleaseCheckpointRetention(context.Context, ReleaseCheckpointRetentionRequest) (CheckpointRetention, error)
	ReclaimCheckpoint(context.Context, ReclaimCheckpointRequest) (CheckpointReclamation, error)
	ObserveCheckpointInventory(context.Context, ObserveCheckpointInventoryRequest) (CheckpointInventoryObservation, error)
	CreateCleanupObligation(context.Context, CreateCleanupObligationRequest) (CleanupDebt, error)
	InspectCleanupDebt(context.Context, InspectCleanupDebtRequest) (CleanupDebt, error)
	ClaimCleanupDebt(context.Context, ClaimCleanupDebtRequest) (CleanupDebt, error)
	ReconcileCleanupDebt(context.Context, ReconcileCleanupDebtRequest) (CleanupDebt, error)
	ResolveCleanupDebt(context.Context, ResolveCleanupDebtRequest) (CleanupDebt, error)
	ReopenCleanupDebt(context.Context, ReopenCleanupDebtRequest) (CleanupDebt, error)
	RebuildAuditDelivery(context.Context, AuditDeliveryRebuildRequest) (AuditDeliveryBacklog, error)
	RebuildProjections(context.Context, ProjectionRebuildRequest) (ProjectionRebuildResult, error)
	QueryAdministratorDiagnostics(context.Context, QueryAdministratorDiagnosticsRequest) (AdministratorDiagnostics, error)
	AcceptLegacyCleanupObligation(context.Context, AcceptLegacyCleanupObligationRequest) (CleanupDebt, error)
	InspectOperation(context.Context, InspectOperationRequest) (OperationInspection, error)
	ReconcileOperation(context.Context, ReconcileOperationRequest) (OperationInspection, error)
}

func canonicalDigest(value any) Digest {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(encoded)
	return Digest("sha256:" + hex.EncodeToString(sum[:]))
}
