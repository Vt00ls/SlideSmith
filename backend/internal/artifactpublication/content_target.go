package artifactpublication

// This file delivers child SPEC #106 (C05-03): the locator-free opaque
// ArtifactContentTarget for authorized content delivery, the mutually
// exclusive owner/share-link/break-glass scope resolution, and the C04
// reconstruction capability that C05 issues only for a Platform-selected
// exact Artifact Version and that C04 can only verify.
//
// Identity & Ownership and Sharing choose exactly one typed scope (owner,
// share link, or break-glass) and its current availability generation; C05
// never creates a principal, share token, Access Code, Verification
// Session, or implicit administrator content authority, and scopes can
// never union. Mandatory access audit and authorization happen in the
// content delivery flow before any Durable Object open; C05's Query itself
// never creates a Durable Object read handle.

// ContentScopeKind is the closed set of authority paths that may resolve a
// content target or a C04 reconstruction capability. Exactly one kind is
// selected; combining kinds is rejected by construction and by every
// resolver/verifier.
type ContentScopeKind string

const (
	// ContentScopeOwner is the Personal Workspace owner path selected by
	// Identity & Ownership.
	ContentScopeOwner ContentScopeKind = "owner"
	// ContentScopeShareLink is the Share Grant path selected by Sharing.
	ContentScopeShareLink ContentScopeKind = "share_link"
	// ContentScopeBreakGlass is the BreakGlass grant path selected by the
	// internal BreakGlass component.
	ContentScopeBreakGlass ContentScopeKind = "break_glass"
)

func validContentScopeKind(kind ContentScopeKind) bool {
	switch kind {
	case ContentScopeOwner, ContentScopeShareLink, ContentScopeBreakGlass:
		return true
	default:
		return false
	}
}

// ScopeID is the opaque identity of one scope instance: the owner
// principal, one Share Grant, or one BreakGlass grant.
type ScopeID string

// ContentScope is exactly one typed authority path. It carries the current
// availability generation of the scope; C05 binds that generation into
// every content target and C04 reconstruction capability it issues, and
// fails closed when a presented generation is stale (rotated or revoked).
// The scope fact itself is owned by Identity & Ownership / Sharing and is
// resolved through the narrow black-box port
// (InMemoryConfig.CurrentContentScope); C05 never stores or manages the
// scope lifecycle.
type ContentScope struct {
	Kind                   ContentScopeKind
	ID                     ScopeID
	AvailabilityGeneration Generation
}

func (s ContentScope) valid() bool {
	return validContentScopeKind(s.Kind) && s.ID != "" && s.AvailabilityGeneration != 0
}

// ContentScopeKey is the exact key C05 uses to resolve the current
// availability fact of one scope instance for one exact Artifact Version.
type ContentScopeKey struct {
	PolicyDomainID    PolicyDomainID
	TaskID            TaskID
	ArtifactVersionID ArtifactVersionID
	Kind              ContentScopeKind
	ID                ScopeID
}

// ContentIntent is the closed set of short-term intents a content target
// may carry. A target is resolved for exactly one intent and never becomes
// a general-purpose download token, signed URL, or long-lived handle.
type ContentIntent string

const (
	// ContentIntentDownload is the short-term download intent.
	ContentIntentDownload ContentIntent = "download"
	// ContentIntentShareLinkDelivery is the short-term Share Link delivery
	// intent. Sharing still owns the Share Link, Access Code, Verification
	// Session, expiry, revocation, and rate limits.
	ContentIntentShareLinkDelivery ContentIntent = "share_link_delivery"
)

func validContentIntent(intent ContentIntent) bool {
	switch intent {
	case ContentIntentDownload, ContentIntentShareLinkDelivery:
		return true
	default:
		return false
	}
}

// ContentDisposition is the closed set of delivery dispositions derived
// from the member's registered media type. Active-content dispositions
// (HTML, SVG) fail closed for content targets: C05 cannot guarantee the
// safe active-content handling those formats require, and an unsafe
// active-content disposition fails closed by contract. Attachment
// dispositions (PPTX, PNG, JSON, plain text) are safe to deliver.
type ContentDisposition string

const (
	ContentDispositionAttachment ContentDisposition = "attachment"
	ContentDispositionActive     ContentDisposition = "active"
)

func validContentDisposition(disposition ContentDisposition) bool {
	switch disposition {
	case ContentDispositionAttachment, ContentDispositionActive:
		return true
	default:
		return false
	}
}

// dispositionForMediaType maps a registered media type to its delivery
// disposition. HTML and SVG are active content; every other registered
// media type is a safe attachment.
func dispositionForMediaType(media MediaType) ContentDisposition {
	switch media {
	case MediaTypeHTML, MediaTypeSVG:
		return ContentDispositionActive
	default:
		return ContentDispositionAttachment
	}
}

// ArtifactContentTarget is the locator-free opaque content target C05
// issues for content delivery (download or Share Link delivery). It binds
// the exact ArtifactVersionID, ArtifactID, manifest digest, member content
// digest, byte size, registered media type, safe logical name, the
// availability generation of the authorized scope, and exactly one
// short-term intent. It is not a ReadHandle, signed URL, object key,
// credential, content authorization, or storage locator: it never contains
// or produces a path, object key, bucket, vendor URL, signed URL,
// credential, materialization locator, ReadHandle, or bytes.
type ArtifactContentTarget struct {
	SchemaVersion          SchemaVersion
	PolicyDomainID         PolicyDomainID
	TaskID                 TaskID
	ArtifactVersionID      ArtifactVersionID
	ArtifactID             ArtifactID
	ManifestDigest         Digest
	MemberDigest           Digest
	Size                   uint64
	MediaType              MediaType
	LogicalName            string
	Disposition            ContentDisposition
	AvailabilityGeneration Generation
	Intent                 ContentIntent
	ScopeKind              ContentScopeKind
	// OccurredAt is diagnostic display metadata and never enters the
	// canonical digest.
	OccurredAt Instant
	// Digest is the canonical digest over the binding fields (excluding
	// OccurredAt and Digest itself). Verification re-derives it and fails
	// closed on any mismatch.
	Digest Digest
}

// CanonicalDigest deterministically encodes the binding facts of the
// content target. OccurredAt is diagnostic and excluded, so an exact
// re-derivation over immutable version facts always matches.
func (t ArtifactContentTarget) CanonicalDigest() Digest {
	return canonicalDigest(map[string]any{
		"schema_version":          uint32(t.SchemaVersion),
		"policy_domain_id":        string(t.PolicyDomainID),
		"task_id":                 string(t.TaskID),
		"artifact_version_id":     string(t.ArtifactVersionID),
		"artifact_id":             string(t.ArtifactID),
		"manifest_digest":         string(t.ManifestDigest),
		"member_digest":           string(t.MemberDigest),
		"size":                    t.Size,
		"media_type":              string(t.MediaType),
		"logical_name":            t.LogicalName,
		"disposition":             string(t.Disposition),
		"availability_generation": uint64(t.AvailabilityGeneration),
		"intent":                  string(t.Intent),
		"scope_kind":              string(t.ScopeKind),
	})
}

// C04ReconstructionCapability is the exact Artifact Version input
// capability C05 issues for C04 manual-edit reconstruction and that C04 can
// only verify. C05 issues it only for the exact ArtifactVersionID selected
// by the Platform (Task Orchestration authority) under exactly one typed
// scope; C04 can never ask C05 to choose a current/latest version, and C05
// can never let C04 choose a publication target. The capability binds the
// publication authority (C05's own authority identity), the policy domain,
// the Task, the exact ArtifactVersionID, the manifest digest, the
// availability generation, and the expiry declared by the Platform.
type C04ReconstructionCapability struct {
	SchemaVersion          SchemaVersion
	PublicationAuthorityID AuthorityID
	PolicyDomainID         PolicyDomainID
	TaskID                 TaskID
	ArtifactVersionID      ArtifactVersionID
	ManifestDigest         Digest
	AvailabilityGeneration Generation
	// ExpiresAt is the declared expiry instant. Verification fails closed
	// once the current diagnostic clock passes it; a fresh capability must
	// be issued for a fresh reconstruction.
	ExpiresAt Instant
	// OccurredAt is diagnostic display metadata and never enters the
	// canonical digest.
	OccurredAt Instant
	// Digest is the canonical digest over the binding fields (excluding
	// OccurredAt and Digest itself).
	Digest Digest
}

// CanonicalDigest deterministically encodes the binding facts of the C04
// reconstruction capability.
func (c C04ReconstructionCapability) CanonicalDigest() Digest {
	return canonicalDigest(map[string]any{
		"schema_version":           uint32(c.SchemaVersion),
		"publication_authority_id": string(c.PublicationAuthorityID),
		"policy_domain_id":         string(c.PolicyDomainID),
		"task_id":                  string(c.TaskID),
		"artifact_version_id":      string(c.ArtifactVersionID),
		"manifest_digest":          string(c.ManifestDigest),
		"availability_generation":  uint64(c.AvailabilityGeneration),
		"expires_at":               int64(c.ExpiresAt),
	})
}
