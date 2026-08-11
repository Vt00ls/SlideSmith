package artifactpublication

// This file delivers the canonical mandatory audit contract of child SPEC
// #111 (C05-07). Every protected publication decision (prepare, verify,
// activate, reject, cancel, reconcile, record residue assembly, release
// residue, resolve cleanup debt) commits one content-free authoritative
// audit fact in the SAME transaction as the business fact. The fact is
// versioned, canonically digested, and correlatable through the opaque
// operation identity and request digest; it never contains content, member
// names, paths, object keys, buckets, mounts, vendor URLs, credentials,
// sessions, raw errors, or cross-Workspace identity canaries.
//
// The audit fact is authoritative and never a second state machine: it
// references the owning publication decision and its committed stream
// facts. External audit sinks, metrics, logs and traces are rebuildable
// projections of these retained facts; their failure never rolls back a
// committed decision (see observability.go and projection_backlog.go).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// AuditSchemaVersion is the closed schema of the mandatory audit envelope.
// The high 16 bits are the major version; the low 16 bits are the minor
// version. Unknown major versions fail closed.
type AuditSchemaVersion uint32

// AuditSchemaV1 is the initial C05 mandatory audit schema.
const AuditSchemaV1 AuditSchemaVersion = 1 << 16

// AuditIntegrityVersion is the closed integrity-encoding version of the
// audit fact digest.
type AuditIntegrityVersion uint16

// AuditIntegrityV1 is the initial digest encoding.
const AuditIntegrityV1 AuditIntegrityVersion = 1

// AuditFactDigest is the opaque canonical digest of one authoritative audit
// fact. It binds every content-free field of the fact.
type AuditFactDigest [32]byte

func (digest AuditFactDigest) String() string { return hex.EncodeToString(digest[:]) }

// AuditOwningModule identifies the Platform Control Plane module that owns
// the authoritative fact.
type AuditOwningModule uint8

const AuditModuleArtifactPublication AuditOwningModule = 1

// AuditResult is the closed result of the audited protected decision.
type AuditResult uint8

const (
	AuditResultAccepted AuditResult = iota + 1
	AuditResultDenied
	AuditResultConflict
	AuditResultDeferred
)

// AuditSourceClock identifies the diagnostic clock family that produced the
// occurred/recorded times. It is display metadata, never authority.
type AuditSourceClock uint8

const AuditSourceArtifactPublicationClock AuditSourceClock = 1

// PublicationAuditFact is the content-free authoritative audit fact of one
// protected publication decision. It binds the opaque operation identity,
// the canonical request digest, the closed intent action, the typed
// authority, the committed stream facts, and the resulting operation state.
// It deliberately carries no content, path, object key, bucket, mount,
// vendor, credential, session, locator, or free-form value.
type PublicationAuditFact struct {
	SchemaVersion       AuditSchemaVersion
	IntegrityVersion    AuditIntegrityVersion
	AuditFactID         string
	CanonicalDigest     AuditFactDigest
	OwningModule        AuditOwningModule
	PolicyDomainID      PolicyDomainID
	TaskID              TaskID
	OperationID         PublicationOperationID
	RequestID           PublicationRequestID
	RequestDigest       Digest
	Action              PublicationIntentKind
	Result              AuditResult
	AuthorityKind       EvidenceAuthorityKind
	AuthorityID         AuthorityID
	AuthorityGeneration Generation
	State               PublicationOperationState
	VersionID           ArtifactVersionID
	ManifestDigest      Digest
	LineageDigest       Digest
	StreamRevision      StreamRevision
	OccurredAt          Instant
	RecordedAt          time.Time
	SourceClock         AuditSourceClock
}

// PublicationAuditFactDigest returns the canonical integrity identity of
// one audit fact. The digest field itself is excluded so recomputation is
// stable; occurred/recorded times are included because they are part of the
// retained authoritative fact.
func PublicationAuditFactDigest(fact PublicationAuditFact) AuditFactDigest {
	encoded, _ := json.Marshal(map[string]any{
		"schema_version":       uint32(fact.SchemaVersion),
		"integrity_version":    uint64(fact.IntegrityVersion),
		"audit_fact_id":        fact.AuditFactID,
		"owning_module":        uint64(fact.OwningModule),
		"policy_domain_id":     string(fact.PolicyDomainID),
		"task_id":              string(fact.TaskID),
		"operation_id":         string(fact.OperationID),
		"request_id":           string(fact.RequestID),
		"request_digest":       string(fact.RequestDigest),
		"action":               string(fact.Action),
		"result":               uint64(fact.Result),
		"authority_kind":       string(fact.AuthorityKind),
		"authority_id":         string(fact.AuthorityID),
		"authority_generation": uint64(fact.AuthorityGeneration),
		"state":                string(fact.State),
		"version_id":           string(fact.VersionID),
		"manifest_digest":      string(fact.ManifestDigest),
		"lineage_digest":       string(fact.LineageDigest),
		"stream_revision":      uint64(fact.StreamRevision),
		"occurred_at":          int64(fact.OccurredAt),
		"recorded_at":          fact.RecordedAt.UTC().Format(time.RFC3339Nano),
		"source_clock":         uint64(fact.SourceClock),
	})
	return sha256.Sum256(encoded)
}

// newPublicationAuditFact builds one canonical accepted audit fact for an
// accepted protected decision. auditFactID is the opaque, non-reused audit
// identity allocated by the owning adapter; recordedAt is the diagnostic
// clock at persistence time.
func newPublicationAuditFact(
	auditFactID string,
	header PublicationIntentHeader,
	action PublicationIntentKind,
	state PublicationOperationState,
	versionID ArtifactVersionID,
	manifestDigest Digest,
	lineageDigest Digest,
	streamRevision StreamRevision,
	occurredAt Instant,
	recordedAt time.Time,
) PublicationAuditFact {
	fact := PublicationAuditFact{
		SchemaVersion:       AuditSchemaV1,
		IntegrityVersion:    AuditIntegrityV1,
		AuditFactID:         auditFactID,
		OwningModule:        AuditModuleArtifactPublication,
		PolicyDomainID:      header.PolicyDomainID,
		TaskID:              header.TaskID,
		OperationID:         header.Operation.ID,
		RequestID:           header.RequestID,
		RequestDigest:       header.Operation.RequestDigest,
		Action:              action,
		Result:              AuditResultAccepted,
		AuthorityKind:       header.Authority.Kind,
		AuthorityID:         header.Authority.ID,
		AuthorityGeneration: header.Authority.Generation,
		State:               state,
		VersionID:           versionID,
		ManifestDigest:      manifestDigest,
		LineageDigest:       lineageDigest,
		StreamRevision:      streamRevision,
		OccurredAt:          occurredAt,
		RecordedAt:          recordedAt.UTC(),
		SourceClock:         AuditSourceArtifactPublicationClock,
	}
	fact.CanonicalDigest = PublicationAuditFactDigest(fact)
	return fact
}

// validPublicationAuditFact fails closed on any missing, mismatched, or
// corrupt field of a reconstructed canonical audit fact.
func validPublicationAuditFact(fact PublicationAuditFact) bool {
	if fact.SchemaVersion != AuditSchemaV1 || fact.IntegrityVersion != AuditIntegrityV1 ||
		fact.OwningModule != AuditModuleArtifactPublication ||
		fact.AuditFactID == "" || fact.CanonicalDigest == (AuditFactDigest{}) ||
		fact.PolicyDomainID == "" || fact.TaskID == "" || fact.OperationID == "" ||
		fact.RequestDigest == "" || !validIntentKind(fact.Action) ||
		fact.Result == 0 || !validEvidenceAuthorityKind(fact.AuthorityKind) ||
		fact.AuthorityID == "" || fact.AuthorityGeneration == 0 ||
		!validOperationState(fact.State) || fact.RecordedAt.IsZero() ||
		fact.SourceClock != AuditSourceArtifactPublicationClock {
		return false
	}
	return fact.CanonicalDigest == PublicationAuditFactDigest(fact)
}

// auditActionForIntent maps a protected mutation intent to its canonical
// audit action. Residue/debt maintenance intents keep their own closed
// action names so the audit stream distinguishes protected publication
// decisions from protected cleanup resolutions.
func auditActionForIntent(kind PublicationIntentKind) PublicationIntentKind {
	switch kind {
	case IntentPreparePublication:
		return IntentPreparePublication
	case IntentVerifyPublication:
		return IntentVerifyPublication
	case IntentActivatePublication:
		return IntentActivatePublication
	case IntentRejectPublication:
		return IntentRejectPublication
	case IntentCancelPublication:
		return IntentCancelPublication
	case IntentReconcilePublication:
		return IntentReconcilePublication
	case IntentRecordResidueAssembly:
		return IntentRecordResidueAssembly
	case IntentReleaseResidue:
		return IntentReleaseResidue
	case IntentResolveCleanupDebt:
		return IntentResolveCleanupDebt
	default:
		return ""
	}
}
