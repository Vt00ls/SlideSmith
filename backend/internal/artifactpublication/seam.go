package artifactpublication

import "context"

// PublicationOperationState is the closed set of durable operation states.
// Prepared and verified are non-terminal; rejected, cancelled, and
// reconciliation-required are terminal dispositions visible only through
// exact operation inspection, never through ordinary version queries.
type PublicationOperationState string

const (
	OperationPrepared               PublicationOperationState = "prepared"
	OperationVerified               PublicationOperationState = "verified"
	OperationRejected               PublicationOperationState = "rejected"
	OperationCancelled              PublicationOperationState = "cancelled"
	OperationReconciliationRequired PublicationOperationState = "reconciliation_required"
)

func validOperationState(state PublicationOperationState) bool {
	switch state {
	case OperationPrepared, OperationVerified, OperationRejected,
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

// PublicationDecision is the closed, content-free result of one mutation.
// It reports only facts that are already durable: request/decision/operation
// identity, publication-stream revision, candidate or activated version
// identity, manifest digest, state, retry/reconciliation disposition,
// accepted evidence references, and committed publication-evidence
// references. It never reports remote delivery or Task progression.
type PublicationDecision struct {
	Operation         Operation
	State             PublicationOperationState
	StreamRevision    StreamRevision
	ArtifactVersionID ArtifactVersionID
	ManifestDigest    Digest
	LineageDigest     Digest
	Verification      *VerificationResult
	Replay            bool
	IntegrityConflict bool
	RejectReason      RejectReason
	CancelReason      CancelReason
	ReconcileMode     ReconcileMode
	ResidueRelease    bool
	OccurredAt        Instant
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
)

func validQueryKind(kind PublicationQueryKind) bool {
	switch kind {
	case QueryOperation, QueryCandidate, QueryTaskStream:
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

// PublicationView is the closed, content-free read result. Ordinary queries
// expose only prepared candidates and safe operation status; prepared,
// verifying, rejected, cancelled, and residue are visible only through exact
// operation queries. Timestamps are display metadata only.
type PublicationView struct {
	Kind              PublicationQueryKind
	PolicyDomainID    PolicyDomainID
	TaskID            TaskID
	OperationID       PublicationOperationID
	State             PublicationOperationState
	StreamRevision    StreamRevision
	ArtifactVersionID ArtifactVersionID
	ManifestDigest    Digest
	LineageDigest     Digest
	PublicationKind   PublicationKind
	Parent            ArtifactVersionID
	ContractID        PublicationContractID
	Members           []ArtifactMemberView
	Verification      *VerificationResult
	ResidueRelease    bool
	CurrentHead       ArtifactVersionID
	OccurredAt        Instant
}

// PublicationCore is the single public seam of the Artifact Publication
// deep module. Mutate is the only mutation surface; Query is the only
// ordinary read surface. There is no active setter, general repository, raw
// callback ingest, or caller-provided mutable snapshot.
type PublicationCore interface {
	Mutate(context.Context, PublicationIntent) (PublicationDecision, error)
	Query(context.Context, PublicationQuery) (PublicationView, error)
}
