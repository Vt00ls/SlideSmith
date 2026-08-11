package artifactpublication

// This file delivers child SPEC #111 (C05-07): the reason-bound protected
// diagnostics authority. Exact metadata facts (operation state, audit
// facts, projection backlog, telemetry) are never exposed through ordinary
// queries; they are visible only through a protected, reason-bound,
// least-privilege administrator metadata scope with a bounded result, and
// every diagnostic access is itself audited. The authority is metadata-only:
// it can never mutate, activate, release, resolve, or clean anything, and it
// never grants content access or Workspace impersonation.

import "sync/atomic"

// DiagnosticAuditFaultController is a deterministic fail-closed seam for
// proving that protected diagnostics are never returned without access
// audit.
type DiagnosticAuditFaultController struct {
	next atomic.Bool
}

func (controller *DiagnosticAuditFaultController) FailNext() {
	if controller != nil {
		controller.next.Store(true)
	}
}

func (controller *DiagnosticAuditFaultController) consume() bool {
	return controller != nil && controller.next.CompareAndSwap(true, false)
}

// DiagnosticReason is the closed set of operational reasons that may unlock
// protected metadata diagnostics.
type DiagnosticReason uint8

const (
	DiagnosticReasonOperations DiagnosticReason = iota + 1
	DiagnosticReasonIntegrity
)

func (reason DiagnosticReason) valid() bool {
	return reason == DiagnosticReasonOperations || reason == DiagnosticReasonIntegrity
}

// AdministratorMetadataAuthority is a reason-bound metadata-only authority.
// It is deliberately a different type from mutation and User authorities.
type AdministratorMetadataAuthority struct {
	id         AuthorityID
	generation Generation
	reason     DiagnosticReason
}

// NewAdministratorMetadataAuthority constructs a reason-bound administrator
// metadata authority. Only the Platform may present it, and only for the
// declared closed reason.
func NewAdministratorMetadataAuthority(
	id AuthorityID,
	generation Generation,
	reason DiagnosticReason,
) AdministratorMetadataAuthority {
	return AdministratorMetadataAuthority{id: id, generation: generation, reason: reason}
}

func (authority AdministratorMetadataAuthority) valid() bool {
	return authority.id != "" && authority.generation > 0 && authority.reason.valid()
}

// reasonBound reports whether the authority is valid and carries exactly one
// closed reason.
func (authority AdministratorMetadataAuthority) reasonBound() bool {
	return authority.valid()
}

// DiagnosticLookupKind is the closed set of protected diagnostic lookups.
type DiagnosticLookupKind uint8

const (
	DiagnosticLookupOperation DiagnosticLookupKind = iota + 1
	DiagnosticLookupAuditFact
	DiagnosticLookupProjectionBacklogInspection
	DiagnosticLookupProjectionBacklogRebuild
	DiagnosticLookupTelemetrySnapshot
)

func (kind DiagnosticLookupKind) valid() bool {
	return kind >= DiagnosticLookupOperation && kind <= DiagnosticLookupTelemetrySnapshot
}

// DiagnosticOwner identifies the Platform Control Plane module owning the
// diagnosed facts.
type DiagnosticOwner uint8

const DiagnosticOwnerArtifactPublication DiagnosticOwner = iota + 1

// DiagnosticNextAction is the closed content-free operational next-action
// hint for a projection/outbox disposition.
type DiagnosticNextAction uint8

const (
	DiagnosticNextActionNone DiagnosticNextAction = iota + 1
	DiagnosticNextActionDeliver
	DiagnosticNextActionReconcile
)

// DiagnosticAuditOutcome is the closed outcome of the protected diagnostic
// access itself.
type DiagnosticAuditOutcome uint8

const (
	DiagnosticAuditAccepted DiagnosticAuditOutcome = iota + 1
	DiagnosticAuditDenied
)

// DiagnosticAuditFactRef proves the reason-bound protected query was
// recorded before its exact result was returned. It is content-free.
type DiagnosticAuditFactRef struct {
	AuditFactID     string
	CanonicalDigest ProjectionDigest
	Outcome         DiagnosticAuditOutcome
}

// OperationalDiagnosticView is the content-free, mutation-free protected
// diagnostic view. It exposes exact protected correlation only through the
// reason-bound diagnostics seam and never exposes content, paths, locators,
// credentials, or a mutation-capable repository.
type OperationalDiagnosticView struct {
	SchemaVersion          TelemetrySchemaVersion
	Owner                  DiagnosticOwner
	OperationID            PublicationOperationID
	TaskID                 TaskID
	State                  PublicationOperationState
	StreamRevision         StreamRevision
	ArtifactVersionID      ArtifactVersionID
	AuditFactID            string
	AuditCanonicalDigest   AuditFactDigest
	Verification           VerificationState
	Disposition            ResidueDisposition
	RequiresReconciliation bool
	NextAction             DiagnosticNextAction
	AccessAuditFactRef     DiagnosticAuditFactRef
}
