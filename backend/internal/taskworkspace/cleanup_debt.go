package taskworkspace

import (
	"context"
	"errors"
	"sort"
	"strings"
)

// ErrCleanupResultAmbiguous means the exact physical action may have
// completed, so the obligation must remain open until exact-generation
// inspection reconciles it.
var ErrCleanupResultAmbiguous = errors.New("cleanup result is ambiguous")

type (
	CleanupDebtID                    string
	CleanupOwner                     string
	CleanupResourceClass             string
	CleanupResourceID                string
	CleanupResourceGeneration        string
	CleanupDebtState                 string
	CleanupRetryGeneration           uint64
	CleanupClaimGeneration           uint64
	CleanupClaimID                   string
	CleanupResolutionClass           string
	CleanupBlocker                   string
	CleanupFailureCategory           string
	CleanupAuthorityState            string
	CleanupInspectionDisposition     string
	CleanupEvidenceID                string
	PlatformAdministratorID          string
	PlatformAdministratorAuthorityID string
	PlatformAdministratorGeneration  uint64
	CleanupExceptionReason           string
	CleanupAuditAction               string
	CleanupAuditEvidenceID           string
	LegacyMigrationID                string
	LegacyMigrationAuthorityID       string
	CommitCutoverAuthorityID         string
	CutoverGeneration                uint64
	LegacyCleanupSourceAuthority     string
)

const (
	CleanupOwnerC04              CleanupOwner = "c04_task_workspace_lifecycle"
	CleanupOwnerRuntimeExecution CleanupOwner = "runtime_execution"
	CleanupOwnerDurableObject    CleanupOwner = "durable_object"

	CleanupRuntimeView                  CleanupResourceClass = "runtime_view"
	CleanupTaskWorkspaceMaterialization CleanupResourceClass = "task_workspace_materialization"
	CleanupCheckpointSemantic           CleanupResourceClass = "checkpoint_semantic_cleanup"
	CleanupWorkspaceResidue             CleanupResourceClass = "workspace_residue"

	CleanupDebtOpen           CleanupDebtState = "Open"
	CleanupDebtClaimed        CleanupDebtState = "Claimed"
	CleanupDebtRetryScheduled CleanupDebtState = "RetryScheduled"
	CleanupDebtBlocked        CleanupDebtState = "Blocked"
	CleanupDebtResolved       CleanupDebtState = "Resolved"

	CleanupReclaimed           CleanupResolutionClass = "Reclaimed"
	CleanupAlreadyAbsent       CleanupResolutionClass = "AlreadyAbsent"
	CleanupRetainedByAuthority CleanupResolutionClass = "RetainedByAuthority"
	CleanupAcceptedException   CleanupResolutionClass = "AcceptedException"

	CleanupAuthorityClear   CleanupAuthorityState = "clear"
	CleanupAuthorityBlocked CleanupAuthorityState = "blocked"
	CleanupAuthorityUnknown CleanupAuthorityState = "unknown"

	CleanupInspectionEligible            CleanupInspectionDisposition = "eligible"
	CleanupInspectionAlreadyAbsent       CleanupInspectionDisposition = "already_absent"
	CleanupInspectionRetainedByAuthority CleanupInspectionDisposition = "retained_by_authority"
	CleanupInspectionBlocked             CleanupInspectionDisposition = "blocked"
	CleanupInspectionUnknown             CleanupInspectionDisposition = "unknown"

	CleanupReferenceBlocker  CleanupBlocker = "reference"
	CleanupLeaseBlocker      CleanupBlocker = "lease"
	CleanupGraceBlocker      CleanupBlocker = "grace"
	CleanupIncidentBlocker   CleanupBlocker = "incident"
	CleanupQuarantineBlocker CleanupBlocker = "quarantine"
	CleanupUnknownBlocker    CleanupBlocker = "unknown"

	CleanupFailureAdapterUnavailable CleanupFailureCategory = "adapter_unavailable"
	CleanupFailureAmbiguous          CleanupFailureCategory = "ambiguous_result"
	CleanupFailureInvalidEvidence    CleanupFailureCategory = "invalid_evidence"

	CleanupExceptionUnsafeToReclaim CleanupExceptionReason = "unsafe_to_reclaim"
	CleanupExceptionExternalHold    CleanupExceptionReason = "external_hold"

	CleanupAuditAcceptException  CleanupAuditAction = "accept_exception"
	CleanupAuditReopenException  CleanupAuditAction = "reopen_exception"
	CleanupAuditQueryDiagnostics CleanupAuditAction = "query_diagnostics"

	LegacyCleanupOpaqueObligation LegacyCleanupSourceAuthority = "opaque_numbered_obligation"
)

// CleanupQuantity preserves unavailable accounting as unknown instead of
// collapsing it to a fabricated zero.
type CleanupQuantity struct {
	Known bool
	Value uint64
}

func KnownCleanupQuantity(value uint64) CleanupQuantity {
	return CleanupQuantity{Known: true, Value: value}
}

func UnknownCleanupQuantity() CleanupQuantity {
	return CleanupQuantity{}
}

type CleanupCapacity struct {
	Bytes  CleanupQuantity
	Inodes CleanupQuantity
}

type CleanupRetryPolicy struct {
	ClaimLifetime  Duration
	InitialBackoff Duration
	MaximumBackoff Duration
}

type PlatformAdministratorAuthority struct {
	ID           PlatformAdministratorID
	AuthorityID  PlatformAdministratorAuthorityID
	Generation   PlatformAdministratorGeneration
	ExpiresAt    Instant
	EvidenceRoot Digest
	Digest       Digest
}

func (a PlatformAdministratorAuthority) CanonicalDigest() Digest {
	return canonicalDigest(struct {
		ID           PlatformAdministratorID
		AuthorityID  PlatformAdministratorAuthorityID
		Generation   PlatformAdministratorGeneration
		ExpiresAt    Instant
		EvidenceRoot Digest
	}{a.ID, a.AuthorityID, a.Generation, a.ExpiresAt, a.EvidenceRoot})
}

type CommitCutoverAuthority struct {
	ID                CommitCutoverAuthorityID
	MigrationID       LegacyMigrationID
	AuthorityID       LegacyMigrationAuthorityID
	CutoverGeneration CutoverGeneration
	Fence             Fence
	EvidenceRoot      Digest
	CommittedAt       Instant
	Digest            Digest
}

func (a CommitCutoverAuthority) CanonicalDigest() Digest {
	return canonicalDigest(struct {
		ID                CommitCutoverAuthorityID
		MigrationID       LegacyMigrationID
		AuthorityID       LegacyMigrationAuthorityID
		CutoverGeneration CutoverGeneration
		Fence             Fence
		EvidenceRoot      Digest
		CommittedAt       Instant
	}{a.ID, a.MigrationID, a.AuthorityID, a.CutoverGeneration, a.Fence, a.EvidenceRoot, a.CommittedAt})
}

type CleanupDebt struct {
	DebtID                      CleanupDebtID
	PolicyDomainID              PolicyDomainID
	TaskID                      TaskID
	TaskWorkspaceID             TaskWorkspaceID
	Owner                       CleanupOwner
	ResourceClass               CleanupResourceClass
	ResourceID                  CleanupResourceID
	ResourceGeneration          CleanupResourceGeneration
	Generation                  Generation
	Fence                       Fence
	Capacity                    CleanupCapacity
	EligibilityEvidenceRoot     Digest
	State                       CleanupDebtState
	CreatedAt                   Instant
	FirstAttemptAt              Instant
	LastAttemptAt               Instant
	NextRetryAt                 Instant
	AttemptCount                uint64
	ConsecutiveFailureCount     uint64
	RetryGeneration             CleanupRetryGeneration
	ClaimGeneration             CleanupClaimGeneration
	ClaimID                     CleanupClaimID
	ClaimExpiresAt              Instant
	CurrentBackoff              Duration
	Blockers                    []CleanupBlocker
	LastFailureCategory         CleanupFailureCategory
	SafeFailureEvidenceRoot     Digest
	Resolution                  CleanupResolutionClass
	ResolutionEvidenceRoot      Digest
	ResolutionAuditEvidenceRoot Digest
	ResolutionGeneration        uint64
	ResolvedByAdministratorID   PlatformAdministratorID
	ClosedReason                CleanupExceptionReason
	ResolutionDuration          Duration
	ExceptionExpiresAt          Instant
	ExceptionExpired            bool
	ReclaimedCapacity           CleanupCapacity
	LegacyMigrationID           LegacyMigrationID
	LegacyObligationNumber      uint64
	ResolvedAt                  Instant
	Operation                   Operation
}

type CreateCleanupObligationRequest struct {
	PolicyDomainID          PolicyDomainID
	TaskID                  TaskID
	TaskWorkspaceID         TaskWorkspaceID
	Owner                   CleanupOwner
	ResourceClass           CleanupResourceClass
	ResourceID              CleanupResourceID
	ResourceGeneration      CleanupResourceGeneration
	Generation              Generation
	Fence                   Fence
	Capacity                CleanupCapacity
	EligibilityEvidenceRoot Digest
	Operation               Operation
}

func (r CreateCleanupObligationRequest) CanonicalRequestDigest() Digest {
	return canonicalDigest(struct {
		Kind                    string
		PolicyDomainID          PolicyDomainID
		TaskID                  TaskID
		TaskWorkspaceID         TaskWorkspaceID
		Owner                   CleanupOwner
		ResourceClass           CleanupResourceClass
		ResourceID              CleanupResourceID
		ResourceGeneration      CleanupResourceGeneration
		Generation              Generation
		Fence                   Fence
		Capacity                CleanupCapacity
		EligibilityEvidenceRoot Digest
		OperationID             OperationID
	}{
		Kind:                    "create_cleanup_obligation",
		PolicyDomainID:          r.PolicyDomainID,
		TaskID:                  r.TaskID,
		TaskWorkspaceID:         r.TaskWorkspaceID,
		Owner:                   r.Owner,
		ResourceClass:           r.ResourceClass,
		ResourceID:              r.ResourceID,
		ResourceGeneration:      r.ResourceGeneration,
		Generation:              r.Generation,
		Fence:                   r.Fence,
		Capacity:                r.Capacity,
		EligibilityEvidenceRoot: r.EligibilityEvidenceRoot,
		OperationID:             r.Operation.ID,
	})
}

func (r CreateCleanupObligationRequest) obligationDigest() Digest {
	return canonicalDigest(struct {
		PolicyDomainID          PolicyDomainID
		TaskID                  TaskID
		TaskWorkspaceID         TaskWorkspaceID
		Owner                   CleanupOwner
		ResourceClass           CleanupResourceClass
		ResourceID              CleanupResourceID
		ResourceGeneration      CleanupResourceGeneration
		Generation              Generation
		Fence                   Fence
		Capacity                CleanupCapacity
		EligibilityEvidenceRoot Digest
	}{
		r.PolicyDomainID, r.TaskID, r.TaskWorkspaceID, r.Owner, r.ResourceClass,
		r.ResourceID, r.ResourceGeneration, r.Generation, r.Fence, r.Capacity,
		r.EligibilityEvidenceRoot,
	})
}

type AcceptLegacyCleanupObligationRequest struct {
	PolicyDomainID          PolicyDomainID
	TaskID                  TaskID
	TaskWorkspaceID         TaskWorkspaceID
	LegacyObligationNumber  uint64
	Owner                   CleanupOwner
	ResourceClass           CleanupResourceClass
	ResourceID              CleanupResourceID
	ResourceGeneration      CleanupResourceGeneration
	SourceAuthority         LegacyCleanupSourceAuthority
	CutoverGeneration       CutoverGeneration
	Generation              Generation
	Fence                   Fence
	Capacity                CleanupCapacity
	EligibilityEvidenceRoot Digest
	CommitCutoverAuthority  CommitCutoverAuthority
	Operation               Operation
}

func (r AcceptLegacyCleanupObligationRequest) CanonicalRequestDigest() Digest {
	return canonicalDigest(struct {
		Kind                    string
		PolicyDomainID          PolicyDomainID
		TaskID                  TaskID
		TaskWorkspaceID         TaskWorkspaceID
		LegacyObligationNumber  uint64
		Owner                   CleanupOwner
		ResourceClass           CleanupResourceClass
		ResourceID              CleanupResourceID
		ResourceGeneration      CleanupResourceGeneration
		SourceAuthority         LegacyCleanupSourceAuthority
		CutoverGeneration       CutoverGeneration
		Generation              Generation
		Fence                   Fence
		Capacity                CleanupCapacity
		EligibilityEvidenceRoot Digest
		CommitCutoverAuthority  CommitCutoverAuthority
		OperationID             OperationID
	}{
		"accept_legacy_cleanup_obligation", r.PolicyDomainID, r.TaskID, r.TaskWorkspaceID,
		r.LegacyObligationNumber, r.Owner, r.ResourceClass, r.ResourceID, r.ResourceGeneration,
		r.SourceAuthority, r.CutoverGeneration, r.Generation, r.Fence, r.Capacity,
		r.EligibilityEvidenceRoot, r.CommitCutoverAuthority, r.Operation.ID,
	})
}

func (r AcceptLegacyCleanupObligationRequest) obligationDigest() Digest {
	return canonicalDigest(struct {
		PolicyDomainID          PolicyDomainID
		TaskID                  TaskID
		TaskWorkspaceID         TaskWorkspaceID
		LegacyObligationNumber  uint64
		Owner                   CleanupOwner
		ResourceClass           CleanupResourceClass
		ResourceID              CleanupResourceID
		ResourceGeneration      CleanupResourceGeneration
		CutoverGeneration       CutoverGeneration
		Generation              Generation
		Fence                   Fence
		Capacity                CleanupCapacity
		EligibilityEvidenceRoot Digest
		MigrationID             LegacyMigrationID
	}{
		r.PolicyDomainID, r.TaskID, r.TaskWorkspaceID, r.LegacyObligationNumber,
		r.Owner, r.ResourceClass, r.ResourceID, r.ResourceGeneration,
		r.CutoverGeneration, r.Generation, r.Fence, r.Capacity,
		r.EligibilityEvidenceRoot, r.CommitCutoverAuthority.MigrationID,
	})
}

type InspectCleanupDebtRequest struct {
	PolicyDomainID PolicyDomainID
	TaskID         TaskID
	DebtID         CleanupDebtID
}

type ClaimCleanupDebtRequest struct {
	PolicyDomainID          PolicyDomainID
	TaskID                  TaskID
	DebtID                  CleanupDebtID
	ExpectedRetryGeneration CleanupRetryGeneration
	Operation               Operation
}

type ReconcileCleanupDebtRequest struct {
	PolicyDomainID  PolicyDomainID
	TaskID          TaskID
	DebtID          CleanupDebtID
	ClaimID         CleanupClaimID
	ClaimGeneration CleanupClaimGeneration
	RetryGeneration CleanupRetryGeneration
	Generation      Generation
	Fence           Fence
	Operation       Operation
}

func (r ReconcileCleanupDebtRequest) CanonicalRequestDigest() Digest {
	return canonicalDigest(struct {
		Kind            string
		PolicyDomainID  PolicyDomainID
		TaskID          TaskID
		DebtID          CleanupDebtID
		ClaimID         CleanupClaimID
		ClaimGeneration CleanupClaimGeneration
		RetryGeneration CleanupRetryGeneration
		Generation      Generation
		Fence           Fence
		OperationID     OperationID
	}{
		Kind:            "reconcile_cleanup_debt",
		PolicyDomainID:  r.PolicyDomainID,
		TaskID:          r.TaskID,
		DebtID:          r.DebtID,
		ClaimID:         r.ClaimID,
		ClaimGeneration: r.ClaimGeneration,
		RetryGeneration: r.RetryGeneration,
		Generation:      r.Generation,
		Fence:           r.Fence,
		OperationID:     r.Operation.ID,
	})
}

func (r ClaimCleanupDebtRequest) CanonicalRequestDigest() Digest {
	return canonicalDigest(struct {
		Kind                    string
		PolicyDomainID          PolicyDomainID
		TaskID                  TaskID
		DebtID                  CleanupDebtID
		ExpectedRetryGeneration CleanupRetryGeneration
		OperationID             OperationID
	}{
		Kind:                    "claim_cleanup_debt",
		PolicyDomainID:          r.PolicyDomainID,
		TaskID:                  r.TaskID,
		DebtID:                  r.DebtID,
		ExpectedRetryGeneration: r.ExpectedRetryGeneration,
		OperationID:             r.Operation.ID,
	})
}

type ResolveCleanupDebtRequest struct {
	PolicyDomainID          PolicyDomainID
	TaskID                  TaskID
	DebtID                  CleanupDebtID
	ExpectedRetryGeneration CleanupRetryGeneration
	Generation              Generation
	Fence                   Fence
	Resolution              CleanupResolutionClass
	AdministratorAuthority  PlatformAdministratorAuthority
	ClosedReason            CleanupExceptionReason
	Duration                Duration
	EvidenceRoot            Digest
	Operation               Operation
}

func (r ResolveCleanupDebtRequest) CanonicalRequestDigest() Digest {
	return canonicalDigest(struct {
		Kind                    string
		PolicyDomainID          PolicyDomainID
		TaskID                  TaskID
		DebtID                  CleanupDebtID
		ExpectedRetryGeneration CleanupRetryGeneration
		Generation              Generation
		Fence                   Fence
		Resolution              CleanupResolutionClass
		AdministratorAuthority  PlatformAdministratorAuthority
		ClosedReason            CleanupExceptionReason
		Duration                Duration
		EvidenceRoot            Digest
		OperationID             OperationID
	}{
		"resolve_cleanup_debt", r.PolicyDomainID, r.TaskID, r.DebtID,
		r.ExpectedRetryGeneration, r.Generation, r.Fence, r.Resolution,
		r.AdministratorAuthority, r.ClosedReason, r.Duration, r.EvidenceRoot,
		r.Operation.ID,
	})
}

type ReopenCleanupDebtRequest struct {
	PolicyDomainID               PolicyDomainID
	TaskID                       TaskID
	DebtID                       CleanupDebtID
	ExpectedResolutionGeneration uint64
	Generation                   Generation
	Fence                        Fence
	AdministratorAuthority       PlatformAdministratorAuthority
	EvidenceRoot                 Digest
	Operation                    Operation
}

func (r ReopenCleanupDebtRequest) CanonicalRequestDigest() Digest {
	return canonicalDigest(struct {
		Kind                         string
		PolicyDomainID               PolicyDomainID
		TaskID                       TaskID
		DebtID                       CleanupDebtID
		ExpectedResolutionGeneration uint64
		Generation                   Generation
		Fence                        Fence
		AdministratorAuthority       PlatformAdministratorAuthority
		EvidenceRoot                 Digest
		OperationID                  OperationID
	}{
		"reopen_cleanup_debt", r.PolicyDomainID, r.TaskID, r.DebtID,
		r.ExpectedResolutionGeneration, r.Generation, r.Fence,
		r.AdministratorAuthority, r.EvidenceRoot, r.Operation.ID,
	})
}

type CleanupAuditIntent struct {
	Action                 CleanupAuditAction
	DebtID                 CleanupDebtID
	AdministratorAuthority PlatformAdministratorAuthority
	Resolution             CleanupResolutionClass
	ClosedReason           CleanupExceptionReason
	Duration               Duration
	DecisionEvidenceRoot   Digest
	ResolutionGeneration   uint64
	Operation              Operation
}

type CleanupAuditEvidence struct {
	ID                   CleanupAuditEvidenceID
	Digest               Digest
	Action               CleanupAuditAction
	DebtID               CleanupDebtID
	AdministratorID      PlatformAdministratorID
	AuthorityGeneration  PlatformAdministratorGeneration
	Resolution           CleanupResolutionClass
	ClosedReason         CleanupExceptionReason
	Duration             Duration
	DecisionEvidenceRoot Digest
	ResolutionGeneration uint64
	OperationID          OperationID
	RecordedAt           Instant
}

func (e CleanupAuditEvidence) CanonicalDigest() Digest {
	return canonicalDigest(struct {
		ID                   CleanupAuditEvidenceID
		Action               CleanupAuditAction
		DebtID               CleanupDebtID
		AdministratorID      PlatformAdministratorID
		AuthorityGeneration  PlatformAdministratorGeneration
		Resolution           CleanupResolutionClass
		ClosedReason         CleanupExceptionReason
		Duration             Duration
		DecisionEvidenceRoot Digest
		ResolutionGeneration uint64
		OperationID          OperationID
		RecordedAt           Instant
	}{
		e.ID, e.Action, e.DebtID, e.AdministratorID, e.AuthorityGeneration,
		e.Resolution, e.ClosedReason, e.Duration, e.DecisionEvidenceRoot,
		e.ResolutionGeneration, e.OperationID, e.RecordedAt,
	})
}

type CleanupAuditPort interface {
	RecordRequired(context.Context, CleanupAuditTransaction) error
}

// CleanupAuditTransaction lets the audit facility canonicalize a mandatory
// fact while the owning lifecycle module commits that fact and its protected
// Cleanup Debt decision as one authoritative persistence mutation.
type CleanupAuditTransaction interface {
	Intent() CleanupAuditIntent
	Commit(CleanupAuditEvidence) error
}

type cleanupAuditTransaction struct {
	intent    CleanupAuditIntent
	commit    func(CleanupAuditEvidence) error
	committed bool
	evidence  CleanupAuditEvidence
}

func (t *cleanupAuditTransaction) Intent() CleanupAuditIntent {
	return t.intent
}

func (t *cleanupAuditTransaction) Commit(evidence CleanupAuditEvidence) error {
	if t.committed || !cleanupAuditEvidenceMatches(t.intent, evidence) {
		return &Error{Code: ErrorIntegrityFailure}
	}
	if err := t.commit(evidence); err != nil {
		return err
	}
	t.evidence = evidence
	t.committed = true
	return nil
}

type cleanupResourceKey struct {
	policyDomainID     PolicyDomainID
	resourceID         CleanupResourceID
	resourceGeneration CleanupResourceGeneration
}

type legacyCleanupObligationKey struct {
	migrationID LegacyMigrationID
	number      uint64
}

type cleanupDebtRecord struct {
	debt             CleanupDebt
	obligationDigest Digest
	checkpoint       *checkpointCleanupContext
}

type checkpointCleanupContext struct {
	request             ReclaimCheckpointRequest
	exactGenerationRoot Digest
	resources           []CheckpointContentGeneration
}

type cleanupOperationRecord struct {
	kind          string
	requestDigest Digest
	result        CleanupDebt
	err           *Error
	recordedAt    Instant
}

// CleanupPort is the exact-generation physical boundary. Creating an
// obligation never invokes it; reconciliation does so only after a claim.
type CleanupPort interface {
	InspectCleanup(context.Context, CleanupInspectionRequest) (CleanupInspectionEvidence, error)
	ReclaimCleanup(context.Context, CleanupAttemptRequest) (CleanupAttemptEvidence, error)
}

type CleanupInspectionRequest struct {
	PolicyDomainID     PolicyDomainID
	TaskID             TaskID
	TaskWorkspaceID    TaskWorkspaceID
	DebtID             CleanupDebtID
	Owner              CleanupOwner
	ResourceClass      CleanupResourceClass
	ResourceID         CleanupResourceID
	ResourceGeneration CleanupResourceGeneration
	RetryGeneration    CleanupRetryGeneration
	Generation         Generation
	Fence              Fence
	Operation          Operation
}

type CleanupInspectionEvidence struct {
	ID                 CleanupEvidenceID
	Digest             Digest
	PolicyDomainID     PolicyDomainID
	TaskID             TaskID
	TaskWorkspaceID    TaskWorkspaceID
	DebtID             CleanupDebtID
	Owner              CleanupOwner
	ResourceClass      CleanupResourceClass
	ResourceID         CleanupResourceID
	ResourceGeneration CleanupResourceGeneration
	RetryGeneration    CleanupRetryGeneration
	Generation         Generation
	Fence              Fence
	ReferenceState     CleanupAuthorityState
	LeaseState         CleanupAuthorityState
	GraceState         CleanupAuthorityState
	IncidentState      CleanupAuthorityState
	QuarantineState    CleanupAuthorityState
	Disposition        CleanupInspectionDisposition
	Blockers           []CleanupBlocker
	Capacity           CleanupCapacity
	ObservedAt         Instant
}

func (e CleanupInspectionEvidence) CanonicalDigest() Digest {
	blockers := append([]CleanupBlocker(nil), e.Blockers...)
	sort.Slice(blockers, func(i, j int) bool { return blockers[i] < blockers[j] })
	return canonicalDigest(struct {
		ID                 CleanupEvidenceID
		PolicyDomainID     PolicyDomainID
		TaskID             TaskID
		TaskWorkspaceID    TaskWorkspaceID
		DebtID             CleanupDebtID
		Owner              CleanupOwner
		ResourceClass      CleanupResourceClass
		ResourceID         CleanupResourceID
		ResourceGeneration CleanupResourceGeneration
		RetryGeneration    CleanupRetryGeneration
		Generation         Generation
		Fence              Fence
		ReferenceState     CleanupAuthorityState
		LeaseState         CleanupAuthorityState
		GraceState         CleanupAuthorityState
		IncidentState      CleanupAuthorityState
		QuarantineState    CleanupAuthorityState
		Disposition        CleanupInspectionDisposition
		Blockers           []CleanupBlocker
		Capacity           CleanupCapacity
		ObservedAt         Instant
	}{
		e.ID, e.PolicyDomainID, e.TaskID, e.TaskWorkspaceID, e.DebtID, e.Owner,
		e.ResourceClass, e.ResourceID, e.ResourceGeneration, e.RetryGeneration,
		e.Generation, e.Fence, e.ReferenceState, e.LeaseState, e.GraceState,
		e.IncidentState, e.QuarantineState, e.Disposition, blockers, e.Capacity,
		e.ObservedAt,
	})
}

type CleanupAttemptRequest struct {
	PolicyDomainID           PolicyDomainID
	TaskID                   TaskID
	TaskWorkspaceID          TaskWorkspaceID
	DebtID                   CleanupDebtID
	Owner                    CleanupOwner
	ResourceClass            CleanupResourceClass
	ResourceID               CleanupResourceID
	ResourceGeneration       CleanupResourceGeneration
	RetryGeneration          CleanupRetryGeneration
	Generation               Generation
	Fence                    Fence
	InspectionEvidenceDigest Digest
	Operation                Operation
}

type CleanupAttemptEvidence struct {
	ID                       CleanupEvidenceID
	Digest                   Digest
	PolicyDomainID           PolicyDomainID
	TaskID                   TaskID
	TaskWorkspaceID          TaskWorkspaceID
	DebtID                   CleanupDebtID
	Owner                    CleanupOwner
	ResourceClass            CleanupResourceClass
	ResourceID               CleanupResourceID
	ResourceGeneration       CleanupResourceGeneration
	RetryGeneration          CleanupRetryGeneration
	Generation               Generation
	Fence                    Fence
	InspectionEvidenceDigest Digest
	Outcome                  CleanupResolutionClass
	Capacity                 CleanupCapacity
	ObservedAt               Instant
}

func (e CleanupAttemptEvidence) CanonicalDigest() Digest {
	return canonicalDigest(struct {
		ID                       CleanupEvidenceID
		PolicyDomainID           PolicyDomainID
		TaskID                   TaskID
		TaskWorkspaceID          TaskWorkspaceID
		DebtID                   CleanupDebtID
		Owner                    CleanupOwner
		ResourceClass            CleanupResourceClass
		ResourceID               CleanupResourceID
		ResourceGeneration       CleanupResourceGeneration
		RetryGeneration          CleanupRetryGeneration
		Generation               Generation
		Fence                    Fence
		InspectionEvidenceDigest Digest
		Outcome                  CleanupResolutionClass
		Capacity                 CleanupCapacity
		ObservedAt               Instant
	}{
		e.ID, e.PolicyDomainID, e.TaskID, e.TaskWorkspaceID, e.DebtID, e.Owner,
		e.ResourceClass, e.ResourceID, e.ResourceGeneration, e.RetryGeneration,
		e.Generation, e.Fence, e.InspectionEvidenceDigest, e.Outcome, e.Capacity,
		e.ObservedAt,
	})
}

func (m *inMemory) CreateCleanupObligation(
	_ context.Context,
	request CreateCleanupObligationRequest,
) (CleanupDebt, error) {
	if !validCreateCleanupObligationRequest(request) {
		return CleanupDebt{}, &Error{Code: ErrorInvalidIntent}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	scope := operationScope{request.PolicyDomainID, request.TaskID, request.Operation.ID}
	if result, replayed, err := replayCleanupOperation(m.cleanupOperations, scope, "create", request.Operation); replayed {
		return result, err
	}
	workspace, ok := m.workspaces[request.TaskID]
	if !ok || workspace.policyDomainID != request.PolicyDomainID ||
		workspace.taskWorkspaceID != request.TaskWorkspaceID {
		return m.recordCleanupError(scope, "create", request.Operation, ErrorOwnershipDenied)
	}
	if workspace.generation != request.Generation || workspace.fence != request.Fence {
		return m.recordCleanupError(scope, "create", request.Operation, ErrorStaleAuthority)
	}
	key := cleanupResourceKey{request.PolicyDomainID, request.ResourceID, request.ResourceGeneration}
	if debtID, exists := m.cleanupDebtOwners[key]; exists {
		record := m.cleanupDebts[debtID]
		if record.obligationDigest != request.obligationDigest() {
			return m.recordCleanupError(scope, "create", request.Operation, ErrorIntegrityConflict)
		}
		result := cloneCleanupDebt(record.debt)
		m.cleanupOperations[scope] = cleanupOperationRecord{
			kind: "create", requestDigest: request.Operation.RequestDigest, result: result,
			recordedAt: m.now(),
		}
		return result, nil
	}
	debtID := CleanupDebtID(m.operationOpaqueID(scope, "cleanup-debt", "cleanup-debt"))
	debt := CleanupDebt{
		DebtID: debtID, PolicyDomainID: request.PolicyDomainID, TaskID: request.TaskID,
		TaskWorkspaceID: request.TaskWorkspaceID, Owner: request.Owner,
		ResourceClass: request.ResourceClass, ResourceID: request.ResourceID,
		ResourceGeneration: request.ResourceGeneration, Generation: request.Generation,
		Fence: request.Fence, Capacity: request.Capacity,
		EligibilityEvidenceRoot: request.EligibilityEvidenceRoot, State: CleanupDebtOpen,
		CreatedAt: m.now(), NextRetryAt: m.now(), Operation: request.Operation,
	}
	m.cleanupDebts[debtID] = cleanupDebtRecord{debt: debt, obligationDigest: request.obligationDigest()}
	m.cleanupDebtOwners[key] = debtID
	m.cleanupOperations[scope] = cleanupOperationRecord{
		kind: "create", requestDigest: request.Operation.RequestDigest, result: cloneCleanupDebt(debt),
		recordedAt: m.now(),
	}
	return cloneCleanupDebt(debt), nil
}

func (m *inMemory) AcceptLegacyCleanupObligation(
	_ context.Context,
	request AcceptLegacyCleanupObligationRequest,
) (CleanupDebt, error) {
	if !m.validLegacyCleanupObligationRequest(request) {
		return CleanupDebt{}, &Error{Code: ErrorInvalidIntent}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	scope := operationScope{request.PolicyDomainID, request.TaskID, request.Operation.ID}
	if result, replayed, err := replayCleanupOperation(m.cleanupOperations, scope, "accept_legacy", request.Operation); replayed {
		return result, err
	}
	workspace, ok := m.workspaces[request.TaskID]
	if !ok || workspace.policyDomainID != request.PolicyDomainID ||
		workspace.taskWorkspaceID != request.TaskWorkspaceID {
		return m.recordCleanupError(scope, "accept_legacy", request.Operation, ErrorOwnershipDenied)
	}
	if workspace.generation != request.Generation || workspace.fence != request.Fence {
		return m.recordCleanupError(scope, "accept_legacy", request.Operation, ErrorStaleAuthority)
	}
	legacyKey := legacyCleanupObligationKey{
		migrationID: request.CommitCutoverAuthority.MigrationID,
		number:      request.LegacyObligationNumber,
	}
	if debtID, exists := m.legacyCleanupDebts[legacyKey]; exists {
		record := m.cleanupDebts[debtID]
		if record.obligationDigest != request.obligationDigest() {
			return m.recordCleanupError(scope, "accept_legacy", request.Operation, ErrorIntegrityConflict)
		}
		result := cloneCleanupDebt(record.debt)
		m.cleanupOperations[scope] = cleanupOperationRecord{
			kind: "accept_legacy", requestDigest: request.Operation.RequestDigest, result: result,
			recordedAt: m.now(),
		}
		return result, nil
	}
	resourceKey := cleanupResourceKey{request.PolicyDomainID, request.ResourceID, request.ResourceGeneration}
	if _, exists := m.cleanupDebtOwners[resourceKey]; exists {
		return m.recordCleanupError(scope, "accept_legacy", request.Operation, ErrorIntegrityConflict)
	}
	debtID := CleanupDebtID(m.operationOpaqueID(scope, "cleanup-debt", "cleanup-debt"))
	debt := CleanupDebt{
		DebtID: debtID, PolicyDomainID: request.PolicyDomainID, TaskID: request.TaskID,
		TaskWorkspaceID: request.TaskWorkspaceID, Owner: request.Owner,
		ResourceClass: request.ResourceClass, ResourceID: request.ResourceID,
		ResourceGeneration: request.ResourceGeneration, Generation: request.Generation,
		Fence: request.Fence, Capacity: request.Capacity,
		EligibilityEvidenceRoot: request.EligibilityEvidenceRoot, State: CleanupDebtOpen,
		CreatedAt: m.now(), NextRetryAt: m.now(), Operation: request.Operation,
		LegacyMigrationID:      request.CommitCutoverAuthority.MigrationID,
		LegacyObligationNumber: request.LegacyObligationNumber,
	}
	m.cleanupDebts[debtID] = cleanupDebtRecord{debt: debt, obligationDigest: request.obligationDigest()}
	m.cleanupDebtOwners[resourceKey] = debtID
	m.legacyCleanupDebts[legacyKey] = debtID
	m.cleanupOperations[scope] = cleanupOperationRecord{
		kind: "accept_legacy", requestDigest: request.Operation.RequestDigest, result: cloneCleanupDebt(debt),
		recordedAt: m.now(),
	}
	return cloneCleanupDebt(debt), nil
}

func (m *inMemory) validLegacyCleanupObligationRequest(request AcceptLegacyCleanupObligationRequest) bool {
	return request.PolicyDomainID != "" && request.TaskID != "" && request.TaskWorkspaceID != "" &&
		request.LegacyObligationNumber != 0 && request.Owner == CleanupOwnerC04 &&
		validC04CleanupResourceClass(request.ResourceClass) && validOpaqueCleanupIdentity(string(request.ResourceID)) &&
		validOpaqueCleanupIdentity(string(request.ResourceGeneration)) && request.SourceAuthority == LegacyCleanupOpaqueObligation &&
		request.CutoverGeneration != 0 && request.CutoverGeneration == request.CommitCutoverAuthority.CutoverGeneration &&
		request.Generation != 0 && request.Fence != 0 && validCleanupCapacity(request.Capacity) &&
		validDigest(request.EligibilityEvidenceRoot) && request.Operation.ID != "" &&
		request.Operation.RequestDigest == request.CanonicalRequestDigest() &&
		m.commitCutoverAuthorityIsCurrent(request.CommitCutoverAuthority)
}

func (m *inMemory) commitCutoverAuthorityIsCurrent(authority CommitCutoverAuthority) bool {
	if authority.ID == "" || authority.MigrationID == "" || authority.AuthorityID == "" ||
		authority.AuthorityID != m.legacyMigrationAuthorityID || authority.CutoverGeneration == 0 ||
		authority.Fence == 0 || authority.CommittedAt == 0 || !validDigest(authority.EvidenceRoot) ||
		!validDigest(authority.Digest) || authority.Digest != authority.CanonicalDigest() ||
		m.currentCommitCutoverAuthority == nil {
		return false
	}
	current, ok := m.currentCommitCutoverAuthority(authority.ID)
	return ok && current == authority
}

func (m *inMemory) InspectCleanupDebt(
	_ context.Context,
	request InspectCleanupDebtRequest,
) (CleanupDebt, error) {
	if request.PolicyDomainID == "" || request.TaskID == "" || request.DebtID == "" {
		return CleanupDebt{}, &Error{Code: ErrorInvalidIntent}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.cleanupDebts[request.DebtID]
	if !ok || record.debt.PolicyDomainID != request.PolicyDomainID || record.debt.TaskID != request.TaskID {
		return CleanupDebt{}, &Error{Code: ErrorOwnershipDenied}
	}
	debt := cloneCleanupDebt(record.debt)
	debt.ExceptionExpired = debt.Resolution == CleanupAcceptedException &&
		debt.ExceptionExpiresAt != 0 && m.now() >= debt.ExceptionExpiresAt
	return debt, nil
}

func (m *inMemory) ClaimCleanupDebt(
	_ context.Context,
	request ClaimCleanupDebtRequest,
) (CleanupDebt, error) {
	if request.PolicyDomainID == "" || request.TaskID == "" || request.DebtID == "" ||
		request.Operation.ID == "" || request.Operation.RequestDigest != request.CanonicalRequestDigest() {
		return CleanupDebt{}, &Error{Code: ErrorInvalidIntent}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	scope := operationScope{request.PolicyDomainID, request.TaskID, request.Operation.ID}
	if result, replayed, err := replayCleanupOperation(m.cleanupOperations, scope, "claim", request.Operation); replayed {
		return result, err
	}
	record, ok := m.cleanupDebts[request.DebtID]
	if !ok || record.debt.PolicyDomainID != request.PolicyDomainID || record.debt.TaskID != request.TaskID {
		return m.recordCleanupError(scope, "claim", request.Operation, ErrorOwnershipDenied)
	}
	debt := record.debt
	if debt.State == CleanupDebtResolved || debt.RetryGeneration != request.ExpectedRetryGeneration ||
		(debt.State == CleanupDebtClaimed && m.now() < debt.ClaimExpiresAt) || m.now() < debt.NextRetryAt {
		return m.recordCleanupError(scope, "claim", request.Operation, ErrorStaleAuthority)
	}
	debt.RetryGeneration++
	debt.ClaimGeneration++
	debt.ClaimID = CleanupClaimID(m.operationOpaqueID(scope, "cleanup-claim", "cleanup-claim"))
	debt.ClaimExpiresAt = m.now() + Instant(m.cleanupRetryPolicy.ClaimLifetime)
	debt.State = CleanupDebtClaimed
	debt.Operation = request.Operation
	record.debt = debt
	m.cleanupDebts[request.DebtID] = record
	m.cleanupOperations[scope] = cleanupOperationRecord{
		kind: "claim", requestDigest: request.Operation.RequestDigest, result: cloneCleanupDebt(debt),
		recordedAt: m.now(),
	}
	return cloneCleanupDebt(debt), nil
}

func (m *inMemory) ReconcileCleanupDebt(
	ctx context.Context,
	request ReconcileCleanupDebtRequest,
) (CleanupDebt, error) {
	if request.PolicyDomainID == "" || request.TaskID == "" || request.DebtID == "" ||
		request.ClaimID == "" || request.ClaimGeneration == 0 || request.RetryGeneration == 0 ||
		request.Generation == 0 || request.Fence == 0 || request.Operation.ID == "" ||
		request.Operation.RequestDigest != request.CanonicalRequestDigest() {
		return CleanupDebt{}, &Error{Code: ErrorInvalidIntent}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	scope := operationScope{request.PolicyDomainID, request.TaskID, request.Operation.ID}
	if result, replayed, err := replayOperation[CleanupDebt](m.operations, scope, request.Operation); replayed {
		return result, err
	}
	if _, err := ensureOperationIntent(
		m, scope, request.Operation, request, reconcileCleanupDebtJournalSpec(), nil,
	); err != nil {
		return CleanupDebt{}, err
	}
	if result, replayed, err := replayCleanupOperation(m.cleanupOperations, scope, "reconcile", request.Operation); replayed {
		if err != nil {
			lifecycleErr := &Error{Code: ErrorIntegrityFailure}
			if !errors.As(err, &lifecycleErr) {
				lifecycleErr = &Error{Code: ErrorIntegrityFailure}
			}
			recordOperation(m.operations, m.now(), scope, request.Operation, CleanupDebt{}, lifecycleErr)
		} else {
			recordOperation(m.operations, m.now(), scope, request.Operation, result, nil)
		}
		return result, err
	}
	record, ok := m.cleanupDebts[request.DebtID]
	if !ok || record.debt.PolicyDomainID != request.PolicyDomainID || record.debt.TaskID != request.TaskID {
		return m.recordCleanupError(scope, "reconcile", request.Operation, ErrorOwnershipDenied)
	}
	debt := record.debt
	workspace, workspaceOK := m.workspaces[request.TaskID]
	if !workspaceOK || workspace.policyDomainID != request.PolicyDomainID ||
		workspace.taskWorkspaceID != debt.TaskWorkspaceID {
		return m.recordCleanupError(scope, "reconcile", request.Operation, ErrorOwnershipDenied)
	}
	if debt.State != CleanupDebtClaimed || debt.ClaimID != request.ClaimID ||
		debt.ClaimGeneration != request.ClaimGeneration || debt.RetryGeneration != request.RetryGeneration ||
		m.now() >= debt.ClaimExpiresAt || workspace.generation != request.Generation ||
		workspace.fence != request.Fence {
		return m.recordCleanupError(scope, "reconcile", request.Operation, ErrorStaleAuthority)
	}
	markOperationReconciliationRequired(m.operations, m.now(), scope)
	if record.checkpoint != nil {
		return m.reconcileCheckpointCleanupDebt(ctx, scope, request, record)
	}
	if m.cleanup == nil {
		return m.scheduleCleanupRetry(scope, request.Operation, record, CleanupFailureAdapterUnavailable, "", nil, false)
	}
	inspectionRequest := cleanupInspectionRequest(debt, request)
	inspection, err := m.cleanup.InspectCleanup(ctx, inspectionRequest)
	if err != nil {
		return m.scheduleCleanupRetry(scope, request.Operation, record, CleanupFailureAdapterUnavailable, "", nil, false)
	}
	if !validCleanupInspectionEvidence(inspectionRequest, inspection) {
		return m.scheduleCleanupRetry(scope, request.Operation, record, CleanupFailureInvalidEvidence, inspection.Digest, nil, false)
	}
	debt.Generation = request.Generation
	debt.Fence = request.Fence
	debt.Blockers = append([]CleanupBlocker(nil), inspection.Blockers...)
	record.debt = debt
	m.cleanupDebts[debt.DebtID] = record
	switch inspection.Disposition {
	case CleanupInspectionAlreadyAbsent:
		return m.resolveCleanupFromEvidence(scope, request.Operation, record, CleanupAlreadyAbsent, inspection.Digest, inspection.Capacity)
	case CleanupInspectionRetainedByAuthority:
		return m.resolveCleanupFromEvidence(scope, request.Operation, record, CleanupRetainedByAuthority, inspection.Digest, CleanupCapacity{})
	case CleanupInspectionBlocked, CleanupInspectionUnknown:
		return m.scheduleCleanupRetry(scope, request.Operation, record, "", inspection.Digest, inspection.Blockers, true)
	case CleanupInspectionEligible:
		// Continue to the exact physical attempt below.
	default:
		return m.scheduleCleanupRetry(scope, request.Operation, record, CleanupFailureInvalidEvidence, inspection.Digest, nil, false)
	}

	debt = record.debt
	debt.AttemptCount++
	if debt.FirstAttemptAt == 0 {
		debt.FirstAttemptAt = m.now()
	}
	debt.LastAttemptAt = m.now()
	record.debt = debt
	m.cleanupDebts[debt.DebtID] = record
	attemptRequest := cleanupAttemptRequest(debt, request, inspection.Digest)
	attempt, err := m.cleanup.ReclaimCleanup(ctx, attemptRequest)
	if err != nil {
		if errors.Is(err, ErrCleanupResultAmbiguous) {
			return m.scheduleCleanupRetry(scope, request.Operation, record, CleanupFailureAmbiguous, inspection.Digest, nil, false)
		}
		return m.scheduleCleanupRetry(scope, request.Operation, record, CleanupFailureAdapterUnavailable, inspection.Digest, nil, false)
	}
	if !validCleanupAttemptEvidence(attemptRequest, attempt) {
		return m.scheduleCleanupRetry(scope, request.Operation, record, CleanupFailureInvalidEvidence, attempt.Digest, nil, false)
	}
	return m.resolveCleanupFromEvidence(scope, request.Operation, record, attempt.Outcome, attempt.Digest, attempt.Capacity)
}

func cleanupInspectionRequest(debt CleanupDebt, request ReconcileCleanupDebtRequest) CleanupInspectionRequest {
	return CleanupInspectionRequest{
		PolicyDomainID: debt.PolicyDomainID, TaskID: debt.TaskID, TaskWorkspaceID: debt.TaskWorkspaceID,
		DebtID: debt.DebtID, Owner: debt.Owner, ResourceClass: debt.ResourceClass,
		ResourceID: debt.ResourceID, ResourceGeneration: debt.ResourceGeneration,
		RetryGeneration: debt.RetryGeneration, Generation: request.Generation, Fence: request.Fence,
		Operation: request.Operation,
	}
}

func cleanupAttemptRequest(debt CleanupDebt, request ReconcileCleanupDebtRequest, inspection Digest) CleanupAttemptRequest {
	return CleanupAttemptRequest{
		PolicyDomainID: debt.PolicyDomainID, TaskID: debt.TaskID, TaskWorkspaceID: debt.TaskWorkspaceID,
		DebtID: debt.DebtID, Owner: debt.Owner, ResourceClass: debt.ResourceClass,
		ResourceID: debt.ResourceID, ResourceGeneration: debt.ResourceGeneration,
		RetryGeneration: debt.RetryGeneration, Generation: request.Generation, Fence: request.Fence,
		InspectionEvidenceDigest: inspection, Operation: request.Operation,
	}
}

func validCleanupInspectionEvidence(request CleanupInspectionRequest, evidence CleanupInspectionEvidence) bool {
	if evidence.ID == "" || evidence.ObservedAt == 0 || !validDigest(evidence.Digest) ||
		evidence.Digest != evidence.CanonicalDigest() || evidence.PolicyDomainID != request.PolicyDomainID ||
		evidence.TaskID != request.TaskID || evidence.TaskWorkspaceID != request.TaskWorkspaceID ||
		evidence.DebtID != request.DebtID || evidence.Owner != request.Owner ||
		evidence.ResourceClass != request.ResourceClass || evidence.ResourceID != request.ResourceID ||
		evidence.ResourceGeneration != request.ResourceGeneration || evidence.RetryGeneration != request.RetryGeneration ||
		evidence.Generation != request.Generation || evidence.Fence != request.Fence ||
		!validCleanupCapacity(evidence.Capacity) || !validCleanupAuthorityState(evidence.ReferenceState) ||
		!validCleanupAuthorityState(evidence.LeaseState) || !validCleanupAuthorityState(evidence.GraceState) ||
		!validCleanupAuthorityState(evidence.IncidentState) || !validCleanupAuthorityState(evidence.QuarantineState) {
		return false
	}
	allClear := evidence.ReferenceState == CleanupAuthorityClear && evidence.LeaseState == CleanupAuthorityClear &&
		evidence.GraceState == CleanupAuthorityClear && evidence.IncidentState == CleanupAuthorityClear &&
		evidence.QuarantineState == CleanupAuthorityClear
	switch evidence.Disposition {
	case CleanupInspectionEligible, CleanupInspectionAlreadyAbsent:
		return allClear && len(evidence.Blockers) == 0
	case CleanupInspectionRetainedByAuthority:
		return len(evidence.Blockers) > 0 && (evidence.ReferenceState == CleanupAuthorityBlocked ||
			evidence.LeaseState == CleanupAuthorityBlocked || evidence.IncidentState == CleanupAuthorityBlocked)
	case CleanupInspectionBlocked, CleanupInspectionUnknown:
		return len(evidence.Blockers) > 0
	default:
		return false
	}
}

func validCleanupAttemptEvidence(request CleanupAttemptRequest, evidence CleanupAttemptEvidence) bool {
	return evidence.ID != "" && evidence.ObservedAt != 0 && validDigest(evidence.Digest) &&
		evidence.Digest == evidence.CanonicalDigest() && evidence.PolicyDomainID == request.PolicyDomainID &&
		evidence.TaskID == request.TaskID && evidence.TaskWorkspaceID == request.TaskWorkspaceID &&
		evidence.DebtID == request.DebtID && evidence.Owner == request.Owner &&
		evidence.ResourceClass == request.ResourceClass && evidence.ResourceID == request.ResourceID &&
		evidence.ResourceGeneration == request.ResourceGeneration && evidence.RetryGeneration == request.RetryGeneration &&
		evidence.Generation == request.Generation && evidence.Fence == request.Fence &&
		evidence.InspectionEvidenceDigest == request.InspectionEvidenceDigest &&
		(evidence.Outcome == CleanupReclaimed || evidence.Outcome == CleanupAlreadyAbsent) &&
		validCleanupCapacity(evidence.Capacity)
}

func validCleanupAuthorityState(state CleanupAuthorityState) bool {
	return state == CleanupAuthorityClear || state == CleanupAuthorityBlocked || state == CleanupAuthorityUnknown
}

func (m *inMemory) ResolveCleanupDebt(
	ctx context.Context,
	request ResolveCleanupDebtRequest,
) (CleanupDebt, error) {
	if request.PolicyDomainID == "" || request.TaskID == "" || request.DebtID == "" ||
		request.Generation == 0 || request.Fence == 0 || request.Resolution != CleanupAcceptedException ||
		!validCleanupExceptionReason(request.ClosedReason) || request.Duration <= 0 ||
		!validDigest(request.EvidenceRoot) || request.Operation.ID == "" ||
		request.Operation.RequestDigest != request.CanonicalRequestDigest() ||
		!m.platformAdministratorAuthorityIsCurrent(request.AdministratorAuthority) {
		return CleanupDebt{}, &Error{Code: ErrorInvalidIntent}
	}
	m.mu.Lock()
	var deliveryEvidence CleanupAuditEvidence
	defer func() {
		m.mu.Unlock()
		if deliveryEvidence.ID != "" {
			m.deliverRequiredAudit(ctx, deliveryEvidence)
		}
	}()
	scope := operationScope{request.PolicyDomainID, request.TaskID, request.Operation.ID}
	if result, replayed, err := replayCleanupOperation(m.cleanupOperations, scope, "resolve", request.Operation); replayed {
		return result, err
	}
	record, ok := m.cleanupDebts[request.DebtID]
	if !ok || record.debt.PolicyDomainID != request.PolicyDomainID || record.debt.TaskID != request.TaskID {
		return m.recordCleanupError(scope, "resolve", request.Operation, ErrorOwnershipDenied)
	}
	debt := record.debt
	workspace, workspaceOK := m.workspaces[request.TaskID]
	if !workspaceOK || workspace.policyDomainID != request.PolicyDomainID ||
		workspace.taskWorkspaceID != debt.TaskWorkspaceID {
		return m.recordCleanupError(scope, "resolve", request.Operation, ErrorOwnershipDenied)
	}
	if debt.State == CleanupDebtResolved || debt.RetryGeneration != request.ExpectedRetryGeneration ||
		workspace.generation != request.Generation || workspace.fence != request.Fence {
		return m.recordCleanupError(scope, "resolve", request.Operation, ErrorStaleAuthority)
	}
	intent := CleanupAuditIntent{
		Action: CleanupAuditAcceptException, DebtID: debt.DebtID,
		AdministratorAuthority: request.AdministratorAuthority, Resolution: request.Resolution,
		ClosedReason: request.ClosedReason, Duration: request.Duration,
		DecisionEvidenceRoot: request.EvidenceRoot, ResolutionGeneration: debt.ResolutionGeneration + 1,
		Operation: contentFreeAuditOperation(scope, request.Operation),
	}
	auditEvidence, err := m.recordRequiredCleanupAudit(ctx, intent, func(auditEvidence CleanupAuditEvidence) error {
		if !m.platformAdministratorAuthorityIsCurrent(request.AdministratorAuthority) {
			return &Error{Code: ErrorStaleAuthority}
		}
		if existing, exists := m.cleanupAuditFacts[auditEvidence.ID]; exists && existing.Digest != auditEvidence.Digest {
			return &Error{Code: ErrorIntegrityConflict}
		}
		debt.State = CleanupDebtResolved
		debt.Resolution = CleanupAcceptedException
		debt.ResolutionGeneration++
		debt.ResolvedByAdministratorID = request.AdministratorAuthority.ID
		debt.ClosedReason = request.ClosedReason
		debt.ResolutionDuration = request.Duration
		debt.ExceptionExpiresAt = m.now() + Instant(request.Duration)
		debt.ExceptionExpired = false
		debt.ResolutionEvidenceRoot = request.EvidenceRoot
		debt.ResolutionAuditEvidenceRoot = auditEvidence.Digest
		debt.ReclaimedCapacity = CleanupCapacity{}
		debt.ResolvedAt = m.now()
		debt.ClaimID = ""
		debt.ClaimExpiresAt = 0
		debt.Operation = request.Operation
		record.debt = debt
		m.cleanupAuditFacts[auditEvidence.ID] = auditEvidence
		m.cleanupDebts[debt.DebtID] = record
		m.cleanupOperations[scope] = cleanupOperationRecord{
			kind: "resolve", requestDigest: request.Operation.RequestDigest, result: cloneCleanupDebt(debt),
			recordedAt: m.now(),
		}
		return nil
	})
	if err != nil {
		return m.recordCleanupError(scope, "resolve", request.Operation, ErrorIntegrityFailure)
	}
	deliveryEvidence = auditEvidence
	return cloneCleanupDebt(debt), nil
}

func (m *inMemory) ReopenCleanupDebt(
	ctx context.Context,
	request ReopenCleanupDebtRequest,
) (CleanupDebt, error) {
	if request.PolicyDomainID == "" || request.TaskID == "" || request.DebtID == "" ||
		request.ExpectedResolutionGeneration == 0 || request.Generation == 0 || request.Fence == 0 ||
		!validDigest(request.EvidenceRoot) || request.Operation.ID == "" ||
		request.Operation.RequestDigest != request.CanonicalRequestDigest() ||
		!m.platformAdministratorAuthorityIsCurrent(request.AdministratorAuthority) {
		return CleanupDebt{}, &Error{Code: ErrorInvalidIntent}
	}
	m.mu.Lock()
	var deliveryEvidence CleanupAuditEvidence
	defer func() {
		m.mu.Unlock()
		if deliveryEvidence.ID != "" {
			m.deliverRequiredAudit(ctx, deliveryEvidence)
		}
	}()
	scope := operationScope{request.PolicyDomainID, request.TaskID, request.Operation.ID}
	if result, replayed, err := replayCleanupOperation(m.cleanupOperations, scope, "reopen", request.Operation); replayed {
		return result, err
	}
	record, ok := m.cleanupDebts[request.DebtID]
	if !ok || record.debt.PolicyDomainID != request.PolicyDomainID || record.debt.TaskID != request.TaskID {
		return m.recordCleanupError(scope, "reopen", request.Operation, ErrorOwnershipDenied)
	}
	debt := record.debt
	workspace, workspaceOK := m.workspaces[request.TaskID]
	if !workspaceOK || workspace.policyDomainID != request.PolicyDomainID ||
		workspace.taskWorkspaceID != debt.TaskWorkspaceID {
		return m.recordCleanupError(scope, "reopen", request.Operation, ErrorOwnershipDenied)
	}
	if debt.State != CleanupDebtResolved || debt.Resolution != CleanupAcceptedException ||
		debt.ResolutionGeneration != request.ExpectedResolutionGeneration ||
		debt.ExceptionExpiresAt == 0 || m.now() < debt.ExceptionExpiresAt ||
		workspace.generation != request.Generation || workspace.fence != request.Fence {
		return m.recordCleanupError(scope, "reopen", request.Operation, ErrorStaleAuthority)
	}
	intent := CleanupAuditIntent{
		Action: CleanupAuditReopenException, DebtID: debt.DebtID,
		AdministratorAuthority: request.AdministratorAuthority, Resolution: CleanupAcceptedException,
		ClosedReason: debt.ClosedReason, Duration: debt.ResolutionDuration,
		DecisionEvidenceRoot: request.EvidenceRoot, ResolutionGeneration: debt.ResolutionGeneration + 1,
		Operation: contentFreeAuditOperation(scope, request.Operation),
	}
	auditEvidence, err := m.recordRequiredCleanupAudit(ctx, intent, func(auditEvidence CleanupAuditEvidence) error {
		if !m.platformAdministratorAuthorityIsCurrent(request.AdministratorAuthority) {
			return &Error{Code: ErrorStaleAuthority}
		}
		if existing, exists := m.cleanupAuditFacts[auditEvidence.ID]; exists && existing.Digest != auditEvidence.Digest {
			return &Error{Code: ErrorIntegrityConflict}
		}
		debt.State = CleanupDebtOpen
		debt.Resolution = ""
		debt.ResolutionGeneration++
		debt.ResolvedByAdministratorID = ""
		debt.ClosedReason = ""
		debt.ResolutionDuration = 0
		debt.ExceptionExpiresAt = 0
		debt.ExceptionExpired = false
		debt.ResolutionEvidenceRoot = ""
		debt.ResolutionAuditEvidenceRoot = ""
		debt.ReclaimedCapacity = CleanupCapacity{}
		debt.ResolvedAt = 0
		debt.RetryGeneration++
		debt.NextRetryAt = m.now()
		debt.CurrentBackoff = 0
		debt.Operation = request.Operation
		record.debt = debt
		m.cleanupAuditFacts[auditEvidence.ID] = auditEvidence
		m.cleanupDebts[debt.DebtID] = record
		m.cleanupOperations[scope] = cleanupOperationRecord{
			kind: "reopen", requestDigest: request.Operation.RequestDigest, result: cloneCleanupDebt(debt),
			recordedAt: m.now(),
		}
		return nil
	})
	if err != nil {
		return m.recordCleanupError(scope, "reopen", request.Operation, ErrorIntegrityFailure)
	}
	deliveryEvidence = auditEvidence
	return cloneCleanupDebt(debt), nil
}

func (m *inMemory) platformAdministratorAuthorityIsCurrent(authority PlatformAdministratorAuthority) bool {
	if authority.ID == "" || authority.AuthorityID == "" || authority.Generation == 0 ||
		authority.AuthorityID != m.platformAdministratorAuthorityID || authority.ExpiresAt <= m.now() ||
		!validDigest(authority.EvidenceRoot) || !validDigest(authority.Digest) ||
		authority.Digest != authority.CanonicalDigest() || m.currentPlatformAdministratorAuthority == nil {
		return false
	}
	current, ok := m.currentPlatformAdministratorAuthority(authority.ID)
	return ok && current == authority
}

func (m *inMemory) recordRequiredCleanupAudit(
	ctx context.Context,
	intent CleanupAuditIntent,
	commit func(CleanupAuditEvidence) error,
) (CleanupAuditEvidence, error) {
	if m.cleanupAudit == nil {
		return CleanupAuditEvidence{}, &Error{Code: ErrorIntegrityFailure}
	}
	transaction := &cleanupAuditTransaction{intent: intent, commit: commit}
	_ = m.cleanupAudit.RecordRequired(ctx, transaction)
	if !transaction.committed {
		return CleanupAuditEvidence{}, &Error{Code: ErrorIntegrityFailure}
	}
	return transaction.evidence, nil
}

func (m *inMemory) deliverRequiredAudit(ctx context.Context, evidence CleanupAuditEvidence) {
	fact := auditDeliveryFact(evidence)
	m.mu.Lock()
	record := m.auditDeliveries[evidence.ID]
	if record.fact.AuditFactID == "" {
		record.fact = fact
	}
	if record.fact.AuditFactID != fact.AuditFactID || record.fact.Digest != fact.Digest {
		record.quarantined = true
		record.lastResult = ProjectionResultRejected
		record.safeError = SafeErrorIntegrityUnavailableContent
		m.auditDeliveries[evidence.ID] = record
		m.mu.Unlock()
		m.alertAuditDeliveryIntegrity(ctx, fact.AuditFactID)
		return
	}
	if record.delivered || record.delivering || record.quarantined || m.auditDelivery == nil {
		m.auditDeliveries[evidence.ID] = record
		m.mu.Unlock()
		return
	}
	record.attempts++
	record.attemptGeneration++
	if record.firstAttemptAt == 0 {
		record.firstAttemptAt = m.now()
	}
	record.lastAttemptAt = m.now()
	record.lastResult = ProjectionResultPending
	record.safeError = ""
	record.delivering = true
	m.auditDeliveries[evidence.ID] = record
	delivery := m.auditDelivery
	attemptGeneration := record.attemptGeneration
	m.mu.Unlock()

	err := delivery.Deliver(ctx, fact)
	m.mu.Lock()
	record = m.auditDeliveries[evidence.ID]
	if record.fact.AuditFactID == fact.AuditFactID && record.fact.Digest == fact.Digest &&
		record.attemptGeneration == attemptGeneration {
		record.delivering = false
		record.delivered = err == nil
		if err == nil {
			record.lastResult = ProjectionResultCommitted
			record.safeError = ""
		} else {
			record.lastResult = ProjectionResultRejected
			record.safeError = SafeErrorRetryableUnavailable
		}
		m.auditDeliveries[evidence.ID] = record
	}
	m.mu.Unlock()
}

func (m *inMemory) RebuildAuditDelivery(
	ctx context.Context,
	_ AuditDeliveryRebuildRequest,
) (AuditDeliveryBacklog, error) {
	m.mu.Lock()
	if m.auditDelivery == nil {
		m.mu.Unlock()
		return AuditDeliveryBacklog{}, &Error{Code: ErrorReconciliationRequired}
	}
	alerts := make([]AuditDeliveryFactID, 0)
	for id, evidence := range m.cleanupAuditFacts {
		expected := auditDeliveryFact(evidence)
		record, exists := m.auditDeliveries[id]
		if !exists || record.fact.AuditFactID == "" {
			record.fact = expected
		} else if record.fact.AuditFactID != expected.AuditFactID || record.fact.Digest != expected.Digest {
			record.quarantined = true
			record.lastResult = ProjectionResultRejected
			record.safeError = SafeErrorIntegrityUnavailableContent
			alerts = append(alerts, expected.AuditFactID)
		}
		// In-flight delivery is runtime state. Clearing it makes a committed
		// fact redeliverable after process interruption; the sink deduplicates
		// by AuditFactID and digest.
		record.delivering = false
		m.auditDeliveries[id] = record
	}
	for id, record := range m.auditDeliveries {
		if _, exists := m.cleanupAuditFacts[id]; exists {
			continue
		}
		record.quarantined = true
		record.delivering = false
		record.lastResult = ProjectionResultRejected
		record.safeError = SafeErrorIntegrityUnavailableContent
		m.auditDeliveries[id] = record
		alerts = append(alerts, record.fact.AuditFactID)
	}
	type deliveryAttempt struct {
		sourceID   CleanupAuditEvidenceID
		fact       AuditDeliveryFact
		generation uint64
	}
	pending := make([]deliveryAttempt, 0, len(m.auditDeliveries))
	for id, delivery := range m.auditDeliveries {
		if !delivery.delivered && !delivery.delivering && !delivery.quarantined {
			delivery.delivering = true
			delivery.attempts++
			delivery.attemptGeneration++
			if delivery.firstAttemptAt == 0 {
				delivery.firstAttemptAt = m.now()
			}
			delivery.lastAttemptAt = m.now()
			delivery.lastResult = ProjectionResultPending
			delivery.safeError = ""
			m.auditDeliveries[id] = delivery
			pending = append(pending, deliveryAttempt{
				sourceID: id, fact: delivery.fact, generation: delivery.attemptGeneration,
			})
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].fact.AuditFactID < pending[j].fact.AuditFactID })
	delivery := m.auditDelivery
	m.mu.Unlock()
	for _, auditFactID := range alerts {
		m.alertAuditDeliveryIntegrity(ctx, auditFactID)
	}

	for _, attempt := range pending {
		err := delivery.Deliver(ctx, attempt.fact)
		m.mu.Lock()
		record := m.auditDeliveries[attempt.sourceID]
		if record.fact.AuditFactID == attempt.fact.AuditFactID && record.fact.Digest == attempt.fact.Digest &&
			record.attemptGeneration == attempt.generation {
			record.delivering = false
			record.delivered = err == nil
			if err == nil {
				record.lastResult = ProjectionResultCommitted
				record.safeError = ""
			} else {
				record.lastResult = ProjectionResultRejected
				record.safeError = SafeErrorRetryableUnavailable
			}
			m.auditDeliveries[attempt.sourceID] = record
		}
		m.mu.Unlock()
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	var pendingCount, deliveredCount, quarantinedCount uint64
	evidence := make([]AuditDeliveryEvidence, 0, len(m.auditDeliveries))
	for _, delivery := range m.auditDeliveries {
		switch {
		case delivery.quarantined:
			quarantinedCount++
		case delivery.delivered:
			deliveredCount++
		default:
			pendingCount++
		}
		evidence = append(evidence, AuditDeliveryEvidence{
			AuditFactID: delivery.fact.AuditFactID, Digest: delivery.fact.Digest,
			AttemptCount: delivery.attempts, AttemptGeneration: delivery.attemptGeneration,
			FirstAttemptAt: delivery.firstAttemptAt,
			LastAttemptAt:  delivery.lastAttemptAt, LastResult: delivery.lastResult,
			SafeError: delivery.safeError, Quarantined: delivery.quarantined,
		})
	}
	sort.Slice(evidence, func(i, j int) bool { return evidence[i].AuditFactID < evidence[j].AuditFactID })
	return AuditDeliveryBacklog{
		Pending:         KnownQuantity{Known: true, Value: pendingCount},
		Delivered:       KnownQuantity{Known: true, Value: deliveredCount},
		Quarantined:     KnownQuantity{Known: true, Value: quarantinedCount},
		SourceWatermark: SourceWatermark{Known: true, Value: uint64(len(m.cleanupAuditFacts))},
		Evidence:        evidence,
	}, nil
}

func (m *inMemory) alertAuditDeliveryIntegrity(ctx context.Context, auditFactID AuditDeliveryFactID) {
	if m.auditDeliveryAlerts == nil || auditFactID == "" {
		return
	}
	_ = m.auditDeliveryAlerts.AlertAuditDeliveryIntegrity(ctx, AuditDeliveryIntegrityAlert{
		AuditFactID: auditFactID,
		SafeError:   SafeErrorIntegrityUnavailableContent,
	})
}

func cleanupAuditEvidenceMatches(intent CleanupAuditIntent, evidence CleanupAuditEvidence) bool {
	return evidence.ID != "" && evidence.RecordedAt != 0 && validDigest(evidence.Digest) &&
		evidence.Digest == evidence.CanonicalDigest() && evidence.Action == intent.Action &&
		evidence.DebtID == intent.DebtID && evidence.AdministratorID == intent.AdministratorAuthority.ID &&
		evidence.AuthorityGeneration == intent.AdministratorAuthority.Generation &&
		evidence.Resolution == intent.Resolution && evidence.ClosedReason == intent.ClosedReason &&
		evidence.Duration == intent.Duration && evidence.DecisionEvidenceRoot == intent.DecisionEvidenceRoot &&
		evidence.ResolutionGeneration == intent.ResolutionGeneration && evidence.OperationID == intent.Operation.ID
}

func contentFreeAuditOperation(scope operationScope, operation Operation) Operation {
	return Operation{
		ID: OperationID(canonicalDigest(struct {
			Kind           string
			PolicyDomainID PolicyDomainID
			TaskID         TaskID
			OperationID    OperationID
		}{"content_free_audit_operation", scope.policyDomainID, scope.taskID, operation.ID})),
		RequestDigest: operation.RequestDigest,
	}
}

func validCleanupExceptionReason(reason CleanupExceptionReason) bool {
	return reason == CleanupExceptionUnsafeToReclaim || reason == CleanupExceptionExternalHold
}

func (m *inMemory) resolveCleanupFromEvidence(
	scope operationScope,
	operation Operation,
	record cleanupDebtRecord,
	resolution CleanupResolutionClass,
	evidenceRoot Digest,
	capacity CleanupCapacity,
) (CleanupDebt, error) {
	debt := record.debt
	debt.State = CleanupDebtResolved
	debt.Resolution = resolution
	debt.ResolutionGeneration++
	debt.ResolutionEvidenceRoot = evidenceRoot
	debt.ResolvedAt = m.now()
	debt.ReclaimedCapacity = capacity
	debt.ClaimID = ""
	debt.ClaimExpiresAt = 0
	debt.Operation = operation
	record.debt = debt
	m.cleanupDebts[debt.DebtID] = record
	m.cleanupOperations[scope] = cleanupOperationRecord{
		kind: "reconcile", requestDigest: operation.RequestDigest, result: cloneCleanupDebt(debt),
		recordedAt: m.now(),
	}
	recordOperation(m.operations, m.now(), scope, operation, debt, nil)
	return cloneCleanupDebt(debt), nil
}

func (m *inMemory) scheduleCleanupRetry(
	scope operationScope,
	operation Operation,
	record cleanupDebtRecord,
	failure CleanupFailureCategory,
	evidenceRoot Digest,
	blockers []CleanupBlocker,
	blocked bool,
) (CleanupDebt, error) {
	debt := record.debt
	if failure != "" {
		debt.ConsecutiveFailureCount++
		debt.LastFailureCategory = failure
		evidenceRoot = canonicalDigest(struct {
			DebtID              CleanupDebtID
			Owner               CleanupOwner
			ResourceGeneration  CleanupResourceGeneration
			RetryGeneration     CleanupRetryGeneration
			Generation          Generation
			Fence               Fence
			OperationID         OperationID
			Failure             CleanupFailureCategory
			ObservedAt          Instant
			AdapterEvidenceRoot Digest
		}{
			debt.DebtID, debt.Owner, debt.ResourceGeneration, debt.RetryGeneration,
			debt.Generation, debt.Fence, operation.ID, failure, m.now(), evidenceRoot,
		})
	}
	debt.SafeFailureEvidenceRoot = evidenceRoot
	debt.Blockers = append([]CleanupBlocker(nil), blockers...)
	debt.CurrentBackoff = m.cleanupBackoff(debt.ConsecutiveFailureCount)
	debt.NextRetryAt = m.now() + Instant(debt.CurrentBackoff)
	debt.ClaimID = ""
	debt.ClaimExpiresAt = 0
	debt.State = CleanupDebtRetryScheduled
	if blocked {
		debt.State = CleanupDebtBlocked
	}
	debt.Operation = operation
	record.debt = debt
	m.cleanupDebts[debt.DebtID] = record
	m.cleanupOperations[scope] = cleanupOperationRecord{
		kind: "reconcile", requestDigest: operation.RequestDigest, result: cloneCleanupDebt(debt),
		recordedAt: m.now(),
	}
	recordOperation(m.operations, m.now(), scope, operation, debt, nil)
	return cloneCleanupDebt(debt), nil
}

func (m *inMemory) cleanupBackoff(failures uint64) Duration {
	backoff := m.cleanupRetryPolicy.InitialBackoff
	for i := uint64(1); i < failures && backoff < m.cleanupRetryPolicy.MaximumBackoff; i++ {
		if backoff > m.cleanupRetryPolicy.MaximumBackoff/2 {
			return m.cleanupRetryPolicy.MaximumBackoff
		}
		backoff *= 2
	}
	if backoff > m.cleanupRetryPolicy.MaximumBackoff {
		return m.cleanupRetryPolicy.MaximumBackoff
	}
	return backoff
}

func (m *inMemory) ensureCheckpointCleanupDebt(
	scope operationScope,
	request ReclaimCheckpointRequest,
	checkpoint checkpointRecord,
	resources []CheckpointContentGeneration,
	exactGenerationRoot Digest,
) CleanupDebt {
	key := cleanupResourceKey{
		policyDomainID:     request.PolicyDomainID,
		resourceID:         CleanupResourceID(request.CheckpointID),
		resourceGeneration: CleanupResourceGeneration(exactGenerationRoot),
	}
	if debtID, ok := m.cleanupDebtOwners[key]; ok {
		record := m.cleanupDebts[debtID]
		if record.checkpoint == nil {
			record.checkpoint = &checkpointCleanupContext{
				request: request, exactGenerationRoot: exactGenerationRoot,
				resources: append([]CheckpointContentGeneration(nil), resources...),
			}
			m.cleanupDebts[debtID] = record
		}
		return cloneCleanupDebt(record.debt)
	}
	debtID := CleanupDebtID(m.operationOpaqueID(scope, "cleanup-debt", "checkpoint-cleanup-debt"))
	eligibilityRoot := canonicalDigest(struct {
		CheckpointID        CheckpointID
		RevisionID          RevisionID
		RetentionGeneration RetentionGeneration
		EligibleAt          Instant
		ExactGenerationRoot Digest
	}{
		request.CheckpointID, checkpoint.revisionID, request.ExpectedRetentionGeneration,
		checkpoint.retention.eligibleAt, exactGenerationRoot,
	})
	debt := CleanupDebt{
		DebtID: debtID, PolicyDomainID: request.PolicyDomainID, TaskID: request.TaskID,
		TaskWorkspaceID: request.TaskWorkspaceID, Owner: CleanupOwnerC04,
		ResourceClass: CleanupCheckpointSemantic, ResourceID: key.resourceID,
		ResourceGeneration: key.resourceGeneration, Generation: request.Generation,
		Fence: request.Fence, Capacity: CleanupCapacity{}, EligibilityEvidenceRoot: eligibilityRoot,
		State: CleanupDebtOpen, CreatedAt: m.now(), NextRetryAt: m.now(), Operation: request.Operation,
	}
	obligationDigest := canonicalDigest(struct {
		Owner              CleanupOwner
		ResourceClass      CleanupResourceClass
		ResourceID         CleanupResourceID
		ResourceGeneration CleanupResourceGeneration
		EligibilityRoot    Digest
	}{debt.Owner, debt.ResourceClass, debt.ResourceID, debt.ResourceGeneration, eligibilityRoot})
	m.cleanupDebts[debtID] = cleanupDebtRecord{
		debt: debt, obligationDigest: obligationDigest,
		checkpoint: &checkpointCleanupContext{
			request: request, exactGenerationRoot: exactGenerationRoot,
			resources: append([]CheckpointContentGeneration(nil), resources...),
		},
	}
	m.cleanupDebtOwners[key] = debtID
	return cloneCleanupDebt(debt)
}

func (m *inMemory) reconcileCheckpointCleanupDebt(
	ctx context.Context,
	scope operationScope,
	request ReconcileCleanupDebtRequest,
	record cleanupDebtRecord,
) (CleanupDebt, error) {
	link := record.checkpoint
	originalScope := operationScope{
		link.request.PolicyDomainID, link.request.TaskID, link.request.Operation.ID,
	}
	if scope == originalScope {
		return m.recordCleanupError(scope, "reconcile", request.Operation, ErrorIntegrityConflict)
	}
	checkpoint, ok := m.checkpoints[link.request.CheckpointID]
	if !ok || checkpoint.taskWorkspaceID != record.debt.TaskWorkspaceID {
		return m.recordCleanupError(scope, "reconcile", request.Operation, ErrorOwnershipDenied)
	}
	_, exactGenerationRoot, trusted := checkpointContentGenerations(checkpoint)
	if !trusted || exactGenerationRoot != link.exactGenerationRoot ||
		canonicalDigest(link.resources) != link.exactGenerationRoot ||
		checkpoint.retention.generation != link.request.ExpectedRetentionGeneration {
		return m.recordCleanupError(scope, "reconcile", request.Operation, ErrorStaleAuthority)
	}
	blockers := checkpointRetentionBlockers(checkpoint.retention, m.now())
	if len(blockers) == 0 && (checkpoint.retention.eligibleAt == 0 || m.now() < checkpoint.retention.eligibleAt) {
		blockers = append(blockers, CheckpointGraceBlocker)
	}
	if len(blockers) > 0 {
		return m.completeCheckpointCleanupDebt(
			scope, request.Operation, record, link.request, checkpoint, exactGenerationRoot,
			CheckpointRetainedByAuthority, blockers, "",
		)
	}
	if m.checkpointReclamation == nil {
		return m.scheduleCleanupRetry(
			scope, request.Operation, record, CleanupFailureAdapterUnavailable, "", nil, false,
		)
	}
	debt := record.debt
	debt.AttemptCount++
	if debt.FirstAttemptAt == 0 {
		debt.FirstAttemptAt = m.now()
	}
	debt.LastAttemptAt = m.now()
	record.debt = debt
	m.cleanupDebts[debt.DebtID] = record
	attemptIntent := link.request
	attemptIntent.Generation = request.Generation
	attemptIntent.Fence = request.Fence
	attemptIntent.Operation = request.Operation
	mechanics, err := m.checkpointReclamation.ReclaimCheckpointContent(ctx, ReclaimCheckpointContentRequest{
		PolicyDomainID:      attemptIntent.PolicyDomainID,
		TaskID:              attemptIntent.TaskID,
		TaskWorkspaceID:     attemptIntent.TaskWorkspaceID,
		CheckpointID:        attemptIntent.CheckpointID,
		RevisionID:          checkpoint.revisionID,
		RetentionGeneration: attemptIntent.ExpectedRetentionGeneration,
		Resources:           append([]CheckpointContentGeneration(nil), link.resources...),
		ExactGenerationRoot: exactGenerationRoot,
		Generation:          attemptIntent.Generation,
		Fence:               attemptIntent.Fence,
		Operation:           attemptIntent.Operation,
	})
	if err != nil {
		failure := CleanupFailureAdapterUnavailable
		if errors.Is(err, ErrDurableObjectResultAmbiguous) {
			failure = CleanupFailureAmbiguous
		}
		return m.scheduleCleanupRetry(scope, request.Operation, record, failure, exactGenerationRoot, nil, false)
	}
	mechanicsBlockers, trusted := validateCheckpointContentReclamationEvidence(
		attemptIntent, exactGenerationRoot, mechanics,
	)
	if !trusted {
		return m.scheduleCleanupRetry(
			scope, request.Operation, record, CleanupFailureInvalidEvidence, mechanics.Digest, nil, false,
		)
	}
	return m.completeCheckpointCleanupDebt(
		scope, request.Operation, record, link.request, checkpoint, exactGenerationRoot,
		mechanics.Outcome, mechanicsBlockers, mechanics.Digest,
	)
}

func (m *inMemory) completeCheckpointCleanupDebt(
	scope operationScope,
	operation Operation,
	record cleanupDebtRecord,
	originalRequest ReclaimCheckpointRequest,
	checkpoint checkpointRecord,
	exactGenerationRoot Digest,
	outcome CheckpointReclamationOutcome,
	blockers []CheckpointReclamationBlocker,
	mechanicsEvidenceRoot Digest,
) (CleanupDebt, error) {
	originalScope := operationScope{
		originalRequest.PolicyDomainID, originalRequest.TaskID, originalRequest.Operation.ID,
	}
	priorEvidenceRoot := Digest("")
	if checkpoint.retention.reclaimed {
		priorEvidenceRoot = checkpoint.retention.reclamationEvidence.Digest
	}
	result := m.checkpointReclamationResult(
		originalScope, originalRequest, checkpoint, exactGenerationRoot, outcome,
		blockers, mechanicsEvidenceRoot, priorEvidenceRoot,
	)
	debt := record.debt
	debt.State = CleanupDebtResolved
	debt.Resolution = CleanupResolutionClass(outcome)
	debt.ResolutionGeneration++
	debt.ResolutionEvidenceRoot = result.Evidence.Digest
	debt.ResolvedAt = m.now()
	debt.ReclaimedCapacity = CleanupCapacity{}
	debt.Blockers = checkpointCleanupBlockers(blockers)
	debt.ClaimID = ""
	debt.ClaimExpiresAt = 0
	debt.Operation = operation
	record.debt = debt
	m.cleanupDebts[debt.DebtID] = record
	if outcome == CheckpointReclaimed || outcome == CheckpointAlreadyAbsent {
		checkpoint.retention.reclaimed = true
		checkpoint.retention.reclamationEvidence = result.Evidence
		m.checkpoints[originalRequest.CheckpointID] = checkpoint
	}
	m.cleanupOperations[scope] = cleanupOperationRecord{
		kind: "reconcile", requestDigest: operation.RequestDigest, result: cloneCleanupDebt(debt),
		recordedAt: m.now(),
	}
	recordOperation(m.operations, m.now(), scope, operation, debt, nil)
	recordOperation(m.operations, m.now(), originalScope, originalRequest.Operation, result, nil)
	return cloneCleanupDebt(debt), nil
}

func checkpointCleanupBlockers(blockers []CheckpointReclamationBlocker) []CleanupBlocker {
	result := make([]CleanupBlocker, 0, len(blockers))
	for _, blocker := range blockers {
		switch blocker {
		case CheckpointGraceBlocker:
			result = append(result, CleanupGraceBlocker)
		case CheckpointCommitLeaseBlocker, CheckpointRestoreLeaseBlocker, CheckpointDurableLeaseBlocker:
			result = append(result, CleanupLeaseBlocker)
		case CheckpointIntegrityIncidentBlocker:
			result = append(result, CleanupIncidentBlocker)
		case CheckpointQuarantineBlocker:
			result = append(result, CleanupQuarantineBlocker)
		case CheckpointRecoveryLineageBlocker, CheckpointExplicitReferenceBlocker,
			CheckpointRecoveryPointPinBlocker, CheckpointDurableReferenceBlocker:
			result = append(result, CleanupReferenceBlocker)
		default:
			result = append(result, CleanupUnknownBlocker)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func (m *inMemory) checkpointCleanupDebtID(
	request ReclaimCheckpointRequest,
	exactGenerationRoot Digest,
) CleanupDebtID {
	return m.cleanupDebtOwners[cleanupResourceKey{
		policyDomainID:     request.PolicyDomainID,
		resourceID:         CleanupResourceID(request.CheckpointID),
		resourceGeneration: CleanupResourceGeneration(exactGenerationRoot),
	}]
}

func (m *inMemory) beginCheckpointCleanupAttempt(debtID CleanupDebtID, operation Operation) {
	record := m.cleanupDebts[debtID]
	debt := record.debt
	debt.RetryGeneration++
	debt.ClaimGeneration++
	debt.ClaimID = CleanupClaimID(operation.ID)
	debt.ClaimExpiresAt = m.now() + Instant(m.cleanupRetryPolicy.ClaimLifetime)
	debt.State = CleanupDebtClaimed
	debt.AttemptCount++
	if debt.FirstAttemptAt == 0 {
		debt.FirstAttemptAt = m.now()
	}
	debt.LastAttemptAt = m.now()
	debt.Operation = operation
	record.debt = debt
	m.cleanupDebts[debtID] = record
}

func (m *inMemory) failCheckpointCleanupDebt(
	debtID CleanupDebtID,
	failure CleanupFailureCategory,
	evidenceRoot Digest,
) {
	record := m.cleanupDebts[debtID]
	debt := record.debt
	debt.State = CleanupDebtRetryScheduled
	debt.ConsecutiveFailureCount++
	debt.LastFailureCategory = failure
	debt.SafeFailureEvidenceRoot = canonicalDigest(struct {
		DebtID              CleanupDebtID
		Owner               CleanupOwner
		ResourceGeneration  CleanupResourceGeneration
		RetryGeneration     CleanupRetryGeneration
		Generation          Generation
		Fence               Fence
		Failure             CleanupFailureCategory
		ObservedAt          Instant
		AdapterEvidenceRoot Digest
	}{
		debt.DebtID, debt.Owner, debt.ResourceGeneration, debt.RetryGeneration,
		debt.Generation, debt.Fence, failure, m.now(), evidenceRoot,
	})
	debt.CurrentBackoff = m.cleanupBackoff(debt.ConsecutiveFailureCount)
	debt.NextRetryAt = m.now() + Instant(debt.CurrentBackoff)
	debt.ClaimID = ""
	debt.ClaimExpiresAt = 0
	record.debt = debt
	m.cleanupDebts[debtID] = record
}

func (m *inMemory) resolveCheckpointCleanupDebt(
	debtID CleanupDebtID,
	resolution CheckpointReclamationOutcome,
	evidenceRoot Digest,
) {
	record := m.cleanupDebts[debtID]
	debt := record.debt
	debt.State = CleanupDebtResolved
	debt.Resolution = CleanupResolutionClass(resolution)
	debt.ResolutionGeneration++
	debt.ResolutionEvidenceRoot = evidenceRoot
	debt.ReclaimedCapacity = CleanupCapacity{}
	debt.ResolvedAt = m.now()
	debt.ClaimID = ""
	debt.ClaimExpiresAt = 0
	record.debt = debt
	m.cleanupDebts[debtID] = record
}

func validCreateCleanupObligationRequest(request CreateCleanupObligationRequest) bool {
	return request.PolicyDomainID != "" && request.TaskID != "" && request.TaskWorkspaceID != "" &&
		request.Owner == CleanupOwnerC04 && validC04CleanupResourceClass(request.ResourceClass) &&
		validOpaqueCleanupIdentity(string(request.ResourceID)) &&
		validOpaqueCleanupIdentity(string(request.ResourceGeneration)) && request.Generation != 0 &&
		request.Fence != 0 && validCleanupCapacity(request.Capacity) &&
		validDigest(request.EligibilityEvidenceRoot) && request.Operation.ID != "" &&
		request.Operation.RequestDigest == request.CanonicalRequestDigest()
}

func validOpaqueCleanupIdentity(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("-_.:", character) {
			continue
		}
		return false
	}
	tokens := strings.FieldsFunc(strings.ToLower(value), func(character rune) bool {
		return !((character >= 'a' && character <= 'z') || (character >= '0' && character <= '9'))
	})
	for index, token := range tokens {
		switch token {
		case "path", "paths", "session", "sessions", "locator", "locators", "directory", "directories",
			"marker", "markers", "mtime", "log", "logs", "lastrun", "file", "http", "https", "s3":
			return false
		}
		if token == "last" && index+1 < len(tokens) && tokens[index+1] == "run" {
			return false
		}
	}
	return true
}

func validC04CleanupResourceClass(class CleanupResourceClass) bool {
	return class == CleanupRuntimeView || class == CleanupTaskWorkspaceMaterialization ||
		class == CleanupCheckpointSemantic || class == CleanupWorkspaceResidue
}

func validCleanupCapacity(capacity CleanupCapacity) bool {
	return (capacity.Bytes.Known || capacity.Bytes.Value == 0) &&
		(capacity.Inodes.Known || capacity.Inodes.Value == 0)
}

func replayCleanupOperation(
	operations map[operationScope]cleanupOperationRecord,
	scope operationScope,
	kind string,
	operation Operation,
) (CleanupDebt, bool, error) {
	record, ok := operations[scope]
	if !ok {
		return CleanupDebt{}, false, nil
	}
	if record.kind != kind || record.requestDigest != operation.RequestDigest {
		return CleanupDebt{}, true, &Error{Code: ErrorIntegrityConflict}
	}
	if record.err != nil {
		copy := *record.err
		return CleanupDebt{}, true, &copy
	}
	return cloneCleanupDebt(record.result), true, nil
}

func (m *inMemory) recordCleanupError(
	scope operationScope,
	kind string,
	operation Operation,
	code ErrorCode,
) (CleanupDebt, error) {
	err := &Error{Code: code}
	m.cleanupOperations[scope] = cleanupOperationRecord{
		kind: kind, requestDigest: operation.RequestDigest, err: err, recordedAt: m.now(),
	}
	if kind == "reconcile" {
		recordOperation(m.operations, m.now(), scope, operation, CleanupDebt{}, err)
	}
	return CleanupDebt{}, err
}

func cloneCleanupDebt(debt CleanupDebt) CleanupDebt {
	debt.Blockers = append([]CleanupBlocker(nil), debt.Blockers...)
	return debt
}
