package artifactpublication

import "context"

// PublicationOperationState is the closed set of durable operation states.
// Prepared and verified are non-terminal; activated, rejected, cancelled,
// and reconciliation-required are terminal dispositions. Rejected,
// cancelled, and reconciliation-required are visible only through exact
// operation inspection, never through ordinary version queries. Activated
// is the only terminal state that also creates an immutable Artifact
// Version, advances the publication stream, and emits publication evidence.
type PublicationOperationState string

const (
	OperationPrepared               PublicationOperationState = "prepared"
	OperationVerified               PublicationOperationState = "verified"
	OperationActivated              PublicationOperationState = "activated"
	OperationRejected               PublicationOperationState = "rejected"
	OperationCancelled              PublicationOperationState = "cancelled"
	OperationReconciliationRequired PublicationOperationState = "reconciliation_required"
)

func validOperationState(state PublicationOperationState) bool {
	switch state {
	case OperationPrepared, OperationVerified, OperationActivated, OperationRejected,
		OperationCancelled, OperationReconciliationRequired:
		return true
	default:
		return false
	}
}

// VerificationState is the closed set of recorded verification results.
type VerificationState string

const (
	VerificationPending   VerificationState = "pending"
	VerificationVerified  VerificationState = "verified"
	VerificationFailed    VerificationState = "failed"
	VerificationAmbiguous VerificationState = "ambiguous"
)

func validVerificationState(state VerificationState) bool {
	switch state {
	case VerificationPending, VerificationVerified, VerificationFailed, VerificationAmbiguous:
		return true
	default:
		return false
	}
}

// EvidenceAccepted reports one accepted evidence reference.
type EvidenceAccepted struct {
	Kind       string
	EvidenceID EvidenceID
	Digest     Digest
}

// VerificationResult is the replayable verification result recorded by
// verify and replayed by exact replay and reconcile.
type VerificationResult struct {
	State            VerificationState
	AcceptedEvidence []EvidenceAccepted
	Failure          *EvidenceFailure
}

// PublicationEvidence is the committed activation evidence returned by an
// atomic activation. It binds the OperationID, ArtifactVersionID, manifest
// digest, Phase Run identity with the publication generation/fence, the
// activity generation, the safety epoch, and the exact stream revision and
// current head the activation committed. Task Orchestration consumes this
// evidence and decides Phase/Task progression itself; C05 never advances a
// Task.
type PublicationEvidence struct {
	OperationID        PublicationOperationID
	RequestDigest      Digest
	PolicyDomainID     PolicyDomainID
	TaskID             TaskID
	PhaseRunID         PhaseRunID
	ArtifactVersionID  ArtifactVersionID
	ManifestDigest     Digest
	LineageDigest      Digest
	PublicationKind    PublicationKind
	Parent             ArtifactVersionID
	StreamRevision     StreamRevision
	CurrentHead        ArtifactVersionID
	ActivityGeneration Generation
	Generation         Generation
	Fence              Fence
	SafetyEpoch        SafetyEpoch
	OccurredAt         Instant
}

// CanonicalDigest deterministically encodes the activation evidence. The
// evidence digest commits the same explicit facts Task Orchestration pinned
// in the request plus the committed stream revision and current head; it
// never includes trace IDs, delivery attempts, claims, or telemetry
// attributes.
func (e PublicationEvidence) CanonicalDigest() Digest {
	return canonicalDigest(map[string]any{
		"operation_id":        string(e.OperationID),
		"request_digest":      string(e.RequestDigest),
		"policy_domain_id":    string(e.PolicyDomainID),
		"task_id":             string(e.TaskID),
		"phase_run_id":        string(e.PhaseRunID),
		"artifact_version_id": string(e.ArtifactVersionID),
		"manifest_digest":     string(e.ManifestDigest),
		"lineage_digest":      string(e.LineageDigest),
		"publication_kind":    string(e.PublicationKind),
		"parent":              string(e.Parent),
		"stream_revision":     uint64(e.StreamRevision),
		"current_head":        string(e.CurrentHead),
		"activity_generation": uint64(e.ActivityGeneration),
		"generation":          uint64(e.Generation),
		"fence":               uint64(e.Fence),
		"safety_epoch":        uint64(e.SafetyEpoch),
	})
}

// PublicationDecision is the closed, content-free result of one mutation.
// It reports only facts that are already durable: request/decision/operation
// identity, publication-stream revision, candidate or activated version
// identity, manifest digest, state, retry/reconciliation disposition,
// accepted evidence references, and committed publication-evidence
// references. It never reports remote delivery or Task progression.
type PublicationDecision struct {
	Operation          Operation
	State              PublicationOperationState
	StreamRevision     StreamRevision
	ArtifactVersionID  ArtifactVersionID
	ManifestDigest     Digest
	LineageDigest      Digest
	Verification       *VerificationResult
	ActivationEvidence *PublicationEvidence
	Replay             bool
	IntegrityConflict  bool
	RejectReason       RejectReason
	CancelReason       CancelReason
	ReconcileMode      ReconcileMode
	ResidueRelease     bool
	// C05-05 residue/debt facts reported by residue release, assembly
	// recording and debt resolution decisions. They are content-free and
	// already durable.
	ResidueDisposition ResidueDisposition
	ResidueExpiry      Instant
	ReleaseReceipt     *ReleaseReceipt
	CleanupDebtID      CleanupDebtID
	CleanupDebtStatus  CleanupDebtStatus
	ResolutionClass    CleanupDebtResolutionClass
	OccurredAt         Instant
}

// PublicationQueryKind is the closed set of read-only query kinds. Queries
// never accept evidence, never create Durable Object handles, never write
// audit/outbox, never clean residue, and never change current version.
type PublicationQueryKind string

const (
	// QueryOperation inspects one exact operation by identity.
	QueryOperation PublicationQueryKind = "operation"
	// QueryCandidate inspects one prepared candidate by identity.
	QueryCandidate PublicationQueryKind = "candidate"
	// QueryTaskStream inspects the explicit publication stream facts
	// (revision and current head) for a Task without side effects.
	QueryTaskStream PublicationQueryKind = "task_stream"
	// QueryExactVersion resolves one activated Artifact Version by its
	// opaque identity. Candidates that were never activated are not
	// visible to this ordinary query.
	QueryExactVersion PublicationQueryKind = "exact_version"
	// QueryExactMember resolves one exact member of an activated Artifact
	// Version by ArtifactID.
	QueryExactMember PublicationQueryKind = "exact_member"
	// QueryVersionHistory returns the Task's committed version history
	// ordered exclusively by the explicit publication-stream revision.
	QueryVersionHistory PublicationQueryKind = "version_history"
	// QueryResolveContentTarget resolves one exact member of one activated
	// Artifact Version into a locator-free opaque ArtifactContentTarget
	// under exactly one typed owner/share-link/break-glass scope and one
	// short-term intent. It never creates a Durable Object read handle and
	// never performs mandatory access audit (that is owned by the content
	// delivery flow before any Durable Object open).
	QueryResolveContentTarget PublicationQueryKind = "resolve_content_target"
	// QueryVerifyContentTarget re-validates a presented ArtifactContentTarget
	// against the current immutable version facts and the current
	// availability fact of the presented scope. A revoked/rotated scope, a
	// stale availability generation, tampering, or a scope-kind union fails
	// closed. It is still a pure query: it never creates a Durable Object
	// read handle.
	QueryVerifyContentTarget PublicationQueryKind = "verify_content_target"
	// QueryIssueC04ReconstructionCapability issues the exact Artifact
	// Version input capability for C04 manual-edit reconstruction. It
	// requires the exact ArtifactVersionID selected by the Platform (Task
	// Orchestration authority), exactly one typed scope, and a declared
	// expiry; C04 can never request issuance and C05 never resolves a
	// current/latest version here.
	QueryIssueC04ReconstructionCapability PublicationQueryKind = "issue_c04_reconstruction_capability"
	// QueryVerifyC04ReconstructionCapability verifies a presented C04
	// reconstruction capability against the current version facts, the
	// publication authority identity, the current availability fact, and
	// the declared expiry.
	QueryVerifyC04ReconstructionCapability PublicationQueryKind = "verify_c04_reconstruction_capability"
	// QueryResidue inspects the durable PublicationResidue of one exact
	// operation: owner, opaque references, expiry, retry state, release
	// disposition and C05-owned debt identity. It is read-only and never
	// triggers physical release.
	QueryResidue PublicationQueryKind = "residue"
	// QueryCleanupDebt inspects one C05-owned Cleanup Debt by DebtID or by
	// exact operation. It is read-only and never triggers cleanup.
	QueryCleanupDebt PublicationQueryKind = "cleanup_debt"
)

func validQueryKind(kind PublicationQueryKind) bool {
	switch kind {
	case QueryOperation, QueryCandidate, QueryTaskStream, QueryExactVersion,
		QueryExactMember, QueryVersionHistory, QueryResolveContentTarget,
		QueryVerifyContentTarget, QueryIssueC04ReconstructionCapability,
		QueryVerifyC04ReconstructionCapability, QueryResidue, QueryCleanupDebt:
		return true
	default:
		return false
	}
}

// PublicationQuery is the closed, pure read-only query union.
type PublicationQuery struct {
	Kind              PublicationQueryKind
	PolicyDomainID    PolicyDomainID
	TaskID            TaskID
	OperationID       PublicationOperationID
	ArtifactVersionID ArtifactVersionID
	ArtifactID        ArtifactID
	// Scope is exactly one typed owner/share-link/break-glass authority
	// path. It is required by content-target resolution, content-target
	// verification, and C04 capability issuance/verification; when an exact
	// version/member query presents one, it must be the current availability
	// fact of the exact version or the lookup fails closed non-enumerating.
	// A single field makes scope union structurally impossible.
	Scope ContentScope
	// ContentIntent is the short-term intent of a resolved content target.
	ContentIntent ContentIntent
	// ExpiresAt is the expiry declared by the Platform when issuing a C04
	// reconstruction capability.
	ExpiresAt Instant
	// Authority is the typed seam authority. C04 capability issuance
	// requires exactly the Task Orchestration authority: only the Platform's
	// exact version selection can be issued.
	Authority PublicationAuthority
	// ContentTarget is the presented target for QueryVerifyContentTarget.
	ContentTarget *ArtifactContentTarget
	// C04Capability is the presented capability for
	// QueryVerifyC04ReconstructionCapability.
	C04Capability *C04ReconstructionCapability
	// CleanupDebtID identifies the C05-owned Cleanup Debt to inspect. It is
	// required by QueryCleanupDebt when the operation identity is not
	// supplied.
	CleanupDebtID CleanupDebtID
}

// ArtifactMemberView is the opaque, immutable metadata of one member
// visible through queries. It never contains a materialization locator.
type ArtifactMemberView struct {
	ArtifactID    ArtifactID
	Kind          ArtifactKind
	LogicalName   string
	MediaType     MediaType
	Size          uint64
	ContentDigest Digest
}

// ArtifactVersionView is the immutable metadata of one activated Artifact
// Version visible through ordinary version queries. History and current
// head use only explicit stream facts (the committed stream revision and
// the explicit head pointer); diagnostic time is display metadata only and
// never orders or selects versions.
type ArtifactVersionView struct {
	ArtifactVersionID ArtifactVersionID
	PublicationKind   PublicationKind
	Parent            ArtifactVersionID
	ManifestDigest    Digest
	LineageDigest     Digest
	StreamRevision    StreamRevision
	OperationID       PublicationOperationID
	ContractID        PublicationContractID
	Members           []ArtifactMemberView
}

// ResidueView is the content-free inspection view of one durable
// PublicationResidue. It exposes only the closed disposition, the opaque
// typed references, the owner, generation/fence, expiry, retry state and
// the C05-owned debt identity; it never contains a path, object key,
// bucket, vendor or locator.
type ResidueView struct {
	OperationID            PublicationOperationID
	PolicyDomainID         PolicyDomainID
	TaskID                 TaskID
	Owner                  EvidenceAuthorityKind
	Generation             Generation
	Fence                  Fence
	ReleaseIntent          string
	StagingRefs            []StagingReferenceView
	OccurredAt             Instant
	Expiry                 Instant
	Disposition            ResidueDisposition
	RequiresReconciliation bool
	AttemptCount           uint64
	ConsecutiveFailures    uint64
	NextRetryAt            Instant
	ClaimGeneration        Generation
	ClaimFence             Fence
	LastErrorCategory      ResidueErrorCategory
	ReleaseReceipt         *ReleaseReceipt
	AssemblyReference      string
	AssemblyIdentityDigest Digest
	DebtID                 CleanupDebtID
}

// StagingReferenceView is the opaque, content-free view of one typed
// staging reference.
type StagingReferenceView struct {
	Slot               MemberSlotID
	ContentID          ContentID
	ContentDigest      Digest
	Size               uint64
	Purpose            ContentPurpose
	PhysicalGeneration uint64
	AdapterID          AdapterID
}

// CleanupDebtView is the content-free inspection view of one C05-owned
// Cleanup Debt. It exposes the claim/retry/backoff/blocker facts and the
// evidence-backed (or audited) resolution; it never contains a path,
// locator or raw error.
type CleanupDebtView struct {
	DebtID                 CleanupDebtID
	Revision               uint64
	OperationID            PublicationOperationID
	PolicyDomainID         PolicyDomainID
	TaskID                 TaskID
	Owner                  EvidenceAuthorityKind
	ResourceReference      string
	ResourceIdentityDigest Digest
	ResourceGeneration     uint64
	ResourceFence          uint64
	Status                 CleanupDebtStatus
	CreatedAt              Instant
	EligibleAt             Instant
	FirstAttemptAt         Instant
	LastAttemptAt          Instant
	NextRetryAt            Instant
	AttemptCount           uint64
	ConsecutiveFailures    uint64
	ClaimGeneration        Generation
	ClaimFence             Fence
	RetryDisposition       CleanupRetryDisposition
	LastErrorCategory      ResidueErrorCategory
	Blockers               CleanupBlockerClass
	ResolvedAt             Instant
	ResolutionClass        CleanupDebtResolutionClass
	ResolutionReason       CleanupResolutionReason
	ResolutionEvidence     *CleanupResolutionEvidence
	ResolutionAuditFactID  string
	ResolutionExpiresAt    Instant
}

// PublicationView is the closed, content-free read result. Ordinary queries
// expose only activated immutable versions and safe operation status;
// prepared, verifying, rejected, cancelled, and residue are visible only
// through exact operation queries. Timestamps are display metadata only.
type PublicationView struct {
	Kind               PublicationQueryKind
	PolicyDomainID     PolicyDomainID
	TaskID             TaskID
	OperationID        PublicationOperationID
	State              PublicationOperationState
	StreamRevision     StreamRevision
	ArtifactVersionID  ArtifactVersionID
	ManifestDigest     Digest
	LineageDigest      Digest
	PublicationKind    PublicationKind
	Parent             ArtifactVersionID
	ContractID         PublicationContractID
	ArtifactID         ArtifactID
	Member             *ArtifactMemberView
	Members            []ArtifactMemberView
	History            []ArtifactVersionView
	Verification       *VerificationResult
	ActivationEvidence *PublicationEvidence
	ResidueRelease     bool
	CurrentHead        ArtifactVersionID
	// Residue is set by residue inspection queries.
	Residue *ResidueView
	// CleanupDebt is set by C05-owned Cleanup Debt inspection queries.
	CleanupDebt *CleanupDebtView
	// ContentTarget is set by content-target resolution and verification
	// queries.
	ContentTarget *ArtifactContentTarget
	// C04Capability is set by C04 capability issuance and verification
	// queries.
	C04Capability *C04ReconstructionCapability
	OccurredAt    Instant
}

// PublicationCore is the single public seam of the Artifact Publication
// deep module. Mutate is the only mutation surface; Query is the only
// ordinary read surface. There is no active setter, general repository, raw
// callback ingest, or caller-provided mutable snapshot.
type PublicationCore interface {
	Mutate(context.Context, PublicationIntent) (PublicationDecision, error)
	Query(context.Context, PublicationQuery) (PublicationView, error)
}
