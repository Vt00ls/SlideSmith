// Package taskworkspace owns the closed Task Workspace Lifecycle seam.
package taskworkspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

const (
	ErrorInvalidIntent        ErrorCode = "invalid_intent"
	ErrorIntegrityConflict    ErrorCode = "integrity_conflict"
	ErrorIntegrityFailure     ErrorCode = "integrity_failure"
	ErrorOwnershipDenied      ErrorCode = "ownership_denied"
	ErrorStaleAuthority       ErrorCode = "stale_authority"
	ErrorViewTerminalConflict ErrorCode = "view_terminal_conflict"
	ErrorEffectDenied         ErrorCode = "effect_denied"
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
	default:
		return "task workspace lifecycle intent is invalid"
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
	PolicyDomainID        PolicyDomainID
	TaskID                TaskID
	RuntimeViewID         RuntimeViewID
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

type Lifecycle interface {
	ConfirmTaskWorkspace(context.Context, ConfirmTaskWorkspaceRequest) (ConfirmTaskWorkspaceResult, error)
	Materialize(context.Context, MaterializeRequest) (MaterializeResult, error)
	OpenRuntimeView(context.Context, OpenRuntimeViewRequest) (OpenRuntimeViewResult, error)
	CommitRuntimeView(context.Context, CommitRuntimeViewRequest) (CommitRuntimeViewResult, error)
	DiscardRuntimeView(context.Context, DiscardRuntimeViewRequest) (DiscardRuntimeViewResult, error)
	FenceRuntimeView(context.Context, FenceRuntimeViewRequest) (FenceRuntimeViewResult, error)
}

func canonicalDigest(value any) Digest {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(encoded)
	return Digest("sha256:" + hex.EncodeToString(sum[:]))
}
