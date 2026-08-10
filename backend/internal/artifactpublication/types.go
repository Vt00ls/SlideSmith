// Package artifactpublication owns the closed Artifact Publication seam
// (parent SPEC #103, child SPEC #104). It is the Platform Control Plane deep
// module that is the only authority for Artifact Version identity, canonical
// manifests, membership, lineage, publication operation lifecycle, and
// C05-owned publication residue. All mutation flows through one closed
// seam: Mutate(PublicationIntent) -> PublicationDecision. All ordinary reads
// flow through one pure read-only seam: Query(PublicationQuery) ->
// PublicationView.
//
// Child SPEC #104 establishes the canonical core and the complete operation
// lifecycle (prepare, verify, reject, cancel, reconcile) over a
// deterministic, restartable in-memory authority. Atomic activation of an
// Artifact Version and manual-edit lineage activation are delivered by a
// later child SPEC (#105); this package's types and invariant engine are
// built so that later adapters must reuse the same engine.
package artifactpublication

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// SchemaVersion identifies the business encoding understood by a request.
// The high 16 bits are the major version; the low 16 bits are the minor
// version. Unknown major versions fail closed; minor versions refine a major
// encoding without changing its semantics.
type SchemaVersion uint32

// SchemaV1 is the initial Artifact Publication schema.
const SchemaV1 SchemaVersion = 1 << 16

func NewSchemaVersion(major, minor uint16) SchemaVersion {
	return SchemaVersion(uint32(major)<<16 | uint32(minor))
}

func (version SchemaVersion) Major() uint16 { return uint16(uint32(version) >> 16) }
func (version SchemaVersion) Minor() uint16 { return uint16(version) }

type (
	// PolicyDomainID is the opaque identity of a User's Personal Workspace
	// policy domain. It never appears in content-bearing facts.
	PolicyDomainID string
	// TaskID is the opaque identity of a Task within a Personal Workspace.
	TaskID string
	// TaskWorkspaceID is the opaque identity of a Task's working state.
	TaskWorkspaceID string
	// RevisionID and CheckpointID are opaque C04 Task Workspace identities.
	RevisionID   string
	CheckpointID string
	// PhaseRunID and RuntimeRunID are opaque upstream execution identities.
	PhaseRunID   string
	RuntimeRunID string
	// PublicationRequestID is the caller-supplied opaque request identity.
	PublicationRequestID string
	// PublicationOperationID is the opaque, non-reused operation identity
	// minted at prepare and carried by every later intent and retry.
	PublicationOperationID string
	// ArtifactVersionID is the opaque, non-reused identity of one immutable
	// Artifact Version candidate. It is never derived from timestamps, file
	// names, directories, or object keys.
	ArtifactVersionID string
	// ArtifactID is the opaque, non-reused identity of one Artifact member.
	// Every ArtifactID belongs to exactly one Artifact Version; identical
	// bytes in different versions carry different ArtifactIDs.
	ArtifactID string
	// ContentID is the opaque Durable Object content identity. A ContentID
	// match alone never confers membership, ownership, or authorization.
	ContentID string
	// ContentCapabilityID is the opaque identity of one Durable Object
	// verified-content capability/receipt.
	ContentCapabilityID string
	// EvidenceID is the opaque identity of one upstream evidence record.
	EvidenceID string
	// AuthorityID is the opaque identity of a registered authority.
	AuthorityID string
	// AdapterID is the opaque identity of a registered adapter.
	AdapterID string
	// PublicationContractID is the opaque identity of the pinned publication
	// contract that fixes required channels and aggregation rules.
	PublicationContractID string
	// MemberSlotID is the stable per-candidate identity of one member slot,
	// shared between the Task Orchestration request, Durable Object staging
	// references, and the canonical candidate manifest.
	MemberSlotID string
	// Digest is an algorithm-qualified content or fact digest. Only sha256
	// is accepted; unknown algorithms fail closed.
	Digest string
	// Generation and Fence are the operation's publication generation and
	// fence counters.
	Generation uint64
	Fence      uint64
	// SafetyEpoch is the platform safety epoch bound by the request.
	SafetyEpoch uint64
	// StreamRevision is the explicit per-Task publication stream revision.
	// Current head is a projection of an explicit pointer, never inferred
	// from time, ID order, version strings, row insertion, or file scan.
	StreamRevision uint64
	// Instant is a diagnostic wall-clock instant in Unix seconds. It is
	// display metadata, never an authority fact.
	Instant int64
)

// Operation pairs a stable operation identity with the canonical digest of
// the request that created it. Later intents must carry the same identity
// and digest; a different payload under the same identity is a durable
// integrity conflict.
type Operation struct {
	ID            PublicationOperationID
	RequestDigest Digest
}

// validDigest reports whether value is a canonical sha256 digest.
func validDigest(value Digest) bool {
	if len(value) != len("sha256:")+64 {
		return false
	}
	if value[:len("sha256:")] != "sha256:" {
		return false
	}
	for _, r := range value[len("sha256:"):] {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

// canonicalDigest deterministically encodes value as JSON with sorted map
// keys and returns its sha256 digest. Maps are used so that integer/null
// and field-presence semantics are fixed by the caller's canonical encoding,
// not by struct field order or omitempty behavior.
func canonicalDigest(value any) Digest {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(encoded)
	return Digest("sha256:" + hex.EncodeToString(sum[:]))
}

// canonicalJSON deterministically encodes value. map[string]any values are
// recursively normalized so key order never changes the encoding.
func canonicalJSON(value any) []byte {
	encoded, err := json.Marshal(normalizeCanonical(value))
	if err != nil {
		panic(err)
	}
	return encoded
}

func normalizeCanonical(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		normalized := make(map[string]any, len(typed))
		for key, item := range typed {
			normalized[key] = normalizeCanonical(item)
		}
		return normalized
	case []any:
		normalized := make([]any, len(typed))
		for index, item := range typed {
			normalized[index] = normalizeCanonical(item)
		}
		return normalized
	default:
		return value
	}
}

// ErrorCode is a closed set of safe, content-free error codes.
type ErrorCode string

const (
	ErrorInvalidIntent          ErrorCode = "invalid_intent"
	ErrorUnsupportedSchema      ErrorCode = "unsupported_schema"
	ErrorIntegrityConflict      ErrorCode = "integrity_conflict"
	ErrorIntegrityFailure       ErrorCode = "integrity_failure"
	ErrorOwnershipDenied        ErrorCode = "ownership_denied"
	ErrorStaleAuthority         ErrorCode = "stale_authority"
	ErrorTerminalConflict       ErrorCode = "terminal_conflict"
	ErrorEvidenceMissing        ErrorCode = "evidence_missing"
	ErrorEvidenceCorrupt        ErrorCode = "evidence_corrupt"
	ErrorDurabilityUnverified   ErrorCode = "durability_unverified"
	ErrorResourceExhausted      ErrorCode = "resource_exhausted"
	ErrorRetryableUnavailable   ErrorCode = "retryable_unavailable"
	ErrorReconciliationRequired ErrorCode = "reconciliation_required"
	ErrorNotFound               ErrorCode = "not_found"
)

// SafeErrorCategory is the closed safe-error surface visible to callers.
type SafeErrorCategory string

const (
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
	SafeErrorNotFound                     SafeErrorCategory = "not_found"
)

// Error is the typed safe error returned by the Artifact Publication seam.
// It never carries content, paths, object keys, locators, credentials, or
// raw downstream error chains.
type Error struct {
	Code ErrorCode
}

func (e *Error) Error() string {
	switch e.Code {
	case ErrorInvalidIntent:
		return "artifact publication intent is invalid"
	case ErrorUnsupportedSchema:
		return "artifact publication request schema is unsupported"
	case ErrorIntegrityConflict:
		return "artifact publication operation integrity conflict"
	case ErrorIntegrityFailure:
		return "artifact publication evidence integrity failure"
	case ErrorOwnershipDenied:
		return "artifact publication authority denied"
	case ErrorStaleAuthority:
		return "artifact publication authority is stale"
	case ErrorTerminalConflict:
		return "artifact publication operation is already terminal"
	case ErrorEvidenceMissing:
		return "artifact publication required evidence is missing"
	case ErrorEvidenceCorrupt:
		return "artifact publication evidence is corrupt"
	case ErrorDurabilityUnverified:
		return "artifact publication durability is unverified"
	case ErrorResourceExhausted:
		return "artifact publication resource capacity is exhausted"
	case ErrorRetryableUnavailable:
		return "artifact publication dependency is temporarily unavailable"
	case ErrorReconciliationRequired:
		return "artifact publication operation requires reconciliation"
	case ErrorNotFound:
		return "artifact publication object was not found"
	default:
		return "artifact publication intent is invalid"
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
	case ErrorTerminalConflict:
		return SafeErrorTerminalConflict
	case ErrorIntegrityFailure, ErrorEvidenceMissing, ErrorEvidenceCorrupt:
		return SafeErrorIntegrityUnavailableContent
	case ErrorDurabilityUnverified:
		return SafeErrorDurabilityUnverified
	case ErrorResourceExhausted:
		return SafeErrorResourceExhausted
	case ErrorRetryableUnavailable:
		return SafeErrorRetryableUnavailable
	case ErrorReconciliationRequired:
		return SafeErrorReconciliationRequired
	case ErrorNotFound:
		return SafeErrorNotFound
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

func knownErrorCode(code ErrorCode) bool {
	switch code {
	case ErrorInvalidIntent, ErrorUnsupportedSchema, ErrorIntegrityConflict,
		ErrorIntegrityFailure, ErrorOwnershipDenied, ErrorStaleAuthority,
		ErrorTerminalConflict, ErrorEvidenceMissing, ErrorEvidenceCorrupt,
		ErrorDurabilityUnverified, ErrorResourceExhausted, ErrorRetryableUnavailable,
		ErrorReconciliationRequired, ErrorNotFound:
		return true
	default:
		return false
	}
}

// normalizeError copies only a known code and never retains an error chain.
func normalizeError(err error) *Error {
	var publicationError *Error
	if errors.As(err, &publicationError) && publicationError != nil &&
		knownErrorCode(publicationError.Code) {
		return &Error{Code: publicationError.Code}
	}
	return &Error{Code: ErrorRetryableUnavailable}
}
