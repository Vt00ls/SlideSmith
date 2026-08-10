package artifactpublication

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// PublicationKind is the closed set of publication kinds.
type PublicationKind string

const (
	// PublicationKindFirstGeneration publishes without a parent.
	PublicationKindFirstGeneration PublicationKind = "first_generation"
	// PublicationKindManualEdit publishes an exact child of an activated
	// parent Artifact Version selected by Task Orchestration.
	PublicationKindManualEdit PublicationKind = "manual_edit"
)

func validPublicationKind(kind PublicationKind) bool {
	switch kind {
	case PublicationKindFirstGeneration, PublicationKindManualEdit:
		return true
	default:
		return false
	}
}

// ArtifactKind is the closed set of registered member kinds.
type ArtifactKind string

const (
	ArtifactKindDeck             ArtifactKind = "deck"
	ArtifactKindPreview          ArtifactKind = "preview"
	ArtifactKindPlan             ArtifactKind = "plan"
	ArtifactKindValidationReport ArtifactKind = "validation_report"
)

func validArtifactKind(kind ArtifactKind) bool {
	switch kind {
	case ArtifactKindDeck, ArtifactKindPreview, ArtifactKindPlan, ArtifactKindValidationReport:
		return true
	default:
		return false
	}
}

// MediaType is a registered media type. Only the closed per-kind registry is
// accepted; unknown or mismatched media types fail closed.
type MediaType string

const (
	MediaTypePPTX  MediaType = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	MediaTypePNG   MediaType = "image/png"
	MediaTypeJSON  MediaType = "application/json"
	MediaTypeHTML  MediaType = "text/html"
	MediaTypeSVG   MediaType = "image/svg+xml"
	MediaTypePlain MediaType = "text/plain"
)

// allowedMediaTypes maps each member kind to its registered media types.
var allowedMediaTypes = map[ArtifactKind]map[MediaType]bool{
	ArtifactKindDeck:             {MediaTypePPTX: true},
	ArtifactKindPreview:          {MediaTypePNG: true, MediaTypeSVG: true},
	ArtifactKindPlan:             {MediaTypeJSON: true, MediaTypePlain: true},
	ArtifactKindValidationReport: {MediaTypeJSON: true, MediaTypeHTML: true, MediaTypePlain: true},
}

func validMediaType(kind ArtifactKind, media MediaType) bool {
	return allowedMediaTypes[kind][media]
}

// ContentPurpose is the closed set of purposes for which Durable Object
// content may be staged or attached.
type ContentPurpose string

const (
	ContentPurposePublicationMember ContentPurpose = "publication_member"
)

func validContentPurpose(purpose ContentPurpose) bool {
	return purpose == ContentPurposePublicationMember
}

// ChannelKind is a closed declared output channel of a Runtime Run that a
// publication contract requires evidence for (for example the deck channel).
type ChannelKind string

const (
	ChannelDeck ChannelKind = "deck"
)

func validChannelKind(channel ChannelKind) bool {
	return channel == ChannelDeck
}

// WriteIntent is the closed set of Durable Object write intents.
type WriteIntent string

const (
	WriteIntentImmutable WriteIntent = "immutable"
)

func validWriteIntent(intent WriteIntent) bool {
	return intent == WriteIntentImmutable
}

// VerificationMethod is the closed set of Durable Object content
// verification methods accepted as receipt-bound verified content.
type VerificationMethod string

const (
	VerificationMethodReceiptBound VerificationMethod = "receipt_bound_verified"
)

func validVerificationMethod(method VerificationMethod) bool {
	return method == VerificationMethodReceiptBound
}

// ArtifactMemberSpec is the caller's semantic declaration of one member of
// a candidate. It carries a stable member slot shared with Durable Object
// staging references and the canonical manifest.
type ArtifactMemberSpec struct {
	Slot          MemberSlotID
	Kind          ArtifactKind
	LogicalName   string
	MediaType     MediaType
	Size          uint64
	ContentDigest Digest
}

// ArtifactMember is the immutable canonical member fact. ArtifactID is
// allocated by the Artifact Publication authority at prepare and is
// non-reused; identical bytes in another candidate receive another
// ArtifactID.
type ArtifactMember struct {
	ArtifactID    ArtifactID
	Kind          ArtifactKind
	LogicalName   string
	MediaType     MediaType
	Size          uint64
	ContentDigest Digest
}

// normalizedLogicalName returns the NFC-normalized, trimmed logical name, or
// false when the name is unsafe (empty, contains control characters, or
// contains path-like separators that would make it a materialization
// locator).
func normalizedLogicalName(raw string) (string, bool) {
	normalized := strings.TrimSpace(norm.NFC.String(raw))
	if normalized == "" || !utf8.ValidString(normalized) {
		return "", false
	}
	if strings.ContainsAny(normalized, "/\\") {
		return "", false
	}
	for _, r := range normalized {
		if unicode.IsControl(r) {
			return "", false
		}
	}
	return normalized, true
}

// EvidenceRef is a pinned, opaque reference to one upstream evidence record.
// Verify accepts only evidence whose identity and digest match the pinned
// reference exactly.
type EvidenceRef struct {
	EvidenceID EvidenceID
	Digest     Digest
}

func (r EvidenceRef) canonical() map[string]any {
	return map[string]any{
		"evidence_id": string(r.EvidenceID),
		"digest":      string(r.Digest),
	}
}

// RuntimeEvidenceRef pins one required Runtime Evidence for one declared
// output channel.
type RuntimeEvidenceRef struct {
	Channel    ChannelKind
	EvidenceID EvidenceID
	Digest     Digest
}

func (r RuntimeEvidenceRef) canonical() map[string]any {
	return map[string]any{
		"channel":     string(r.Channel),
		"evidence_id": string(r.EvidenceID),
		"digest":      string(r.Digest),
	}
}

// ContentCapabilityRef pins the Durable Object verified-content capability
// required for one member slot.
type ContentCapabilityRef struct {
	MemberSlot   MemberSlotID
	CapabilityID ContentCapabilityID
	Digest       Digest
}

func (r ContentCapabilityRef) canonical() map[string]any {
	return map[string]any{
		"member_slot":   string(r.MemberSlot),
		"capability_id": string(r.CapabilityID),
		"digest":        string(r.Digest),
	}
}

// StagingReference is one typed Durable Object prepare receipt for one
// member slot. It is persisted at prepare and released on reject/cancel as
// C05-owned publication residue; it is never a materialization locator.
type StagingReference struct {
	MemberSlot         MemberSlotID
	ContentID          ContentID
	ContentDigest      Digest
	Size               uint64
	Purpose            ContentPurpose
	PhysicalGeneration uint64
	AdapterID          AdapterID
}

func (r StagingReference) canonical() map[string]any {
	return map[string]any{
		"member_slot":         string(r.MemberSlot),
		"content_id":          string(r.ContentID),
		"content_digest":      string(r.ContentDigest),
		"size":                r.Size,
		"purpose":             string(r.Purpose),
		"physical_generation": r.PhysicalGeneration,
		"adapter_id":          string(r.AdapterID),
	}
}

// ArtifactManifest is the immutable canonical manifest of one candidate.
// It deterministically binds the Task, ArtifactVersionID, publication kind,
// optional parent, lineage digest, and the sorted members. The manifest and
// lineage never contain object keys, paths, prefixes, buckets, mounts,
// vendors, credentials, temporary names, or materialization locations.
type ArtifactManifest struct {
	SchemaVersion SchemaVersion
	VersionID     ArtifactVersionID
	TaskID        TaskID
	Kind          PublicationKind
	Parent        ArtifactVersionID
	LineageDigest Digest
	Members       []ArtifactMember
}

func (m ArtifactManifest) CanonicalBytes() []byte {
	return canonicalJSON(m.canonicalValue())
}

func (m ArtifactManifest) CanonicalDigest() Digest {
	return canonicalDigest(m.canonicalValue())
}

func (m ArtifactManifest) canonicalValue() map[string]any {
	return map[string]any{
		"schema_version": uint32(m.SchemaVersion),
		"version_id":     string(m.VersionID),
		"task_id":        string(m.TaskID),
		"kind":           string(m.Kind),
		"parent":         string(m.Parent),
		"lineage_digest": string(m.LineageDigest),
		"members":        canonicalMembers(m.Members),
	}
}

func canonicalMembers(members []ArtifactMember) []map[string]any {
	sorted := append([]ArtifactMember(nil), members...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].ArtifactID == sorted[j].ArtifactID {
			return sorted[i].LogicalName < sorted[j].LogicalName
		}
		return sorted[i].ArtifactID < sorted[j].ArtifactID
	})
	encoded := make([]map[string]any, 0, len(sorted))
	for _, member := range sorted {
		encoded = append(encoded, map[string]any{
			"artifact_id":    string(member.ArtifactID),
			"kind":           string(member.Kind),
			"logical_name":   member.LogicalName,
			"media_type":     string(member.MediaType),
			"size":           member.Size,
			"content_digest": string(member.ContentDigest),
		})
	}
	return encoded
}

// Lineage binds the immutable lineage facts of one candidate. The manifest
// digest commits the lineage digest, and the lineage commits the pinned
// upstream evidence references and the applicable generations, fences, and
// safety epoch known at prepare. Later evidence must match these exact
// references; any member, parent, contract, or evidence-binding change
// requires a new request and a new operation.
type Lineage struct {
	SchemaVersion         SchemaVersion
	VersionID             ArtifactVersionID
	TaskID                TaskID
	Kind                  PublicationKind
	Parent                ArtifactVersionID
	OperationID           PublicationOperationID
	PhaseRunID            PhaseRunID
	ContractID            PublicationContractID
	RuntimeEvidenceRoot   Digest
	ValidationEvidenceRef EvidenceRef
	C04CommitEvidenceRef  EvidenceRef
	ContentCapabilityRoot Digest
	ActivityGeneration    Generation
	Generation            Generation
	Fence                 Fence
	SafetyEpoch           SafetyEpoch
}

func (l Lineage) CanonicalBytes() []byte {
	return canonicalJSON(l.canonicalValue())
}

func (l Lineage) CanonicalDigest() Digest {
	return canonicalDigest(l.canonicalValue())
}

func (l Lineage) canonicalValue() map[string]any {
	return map[string]any{
		"schema_version":          uint32(l.SchemaVersion),
		"version_id":              string(l.VersionID),
		"task_id":                 string(l.TaskID),
		"kind":                    string(l.Kind),
		"parent":                  string(l.Parent),
		"operation_id":            string(l.OperationID),
		"phase_run_id":            string(l.PhaseRunID),
		"contract_id":             string(l.ContractID),
		"runtime_evidence_root":   string(l.RuntimeEvidenceRoot),
		"validation_evidence_ref": l.ValidationEvidenceRef.canonical(),
		"c04_commit_evidence_ref": l.C04CommitEvidenceRef.canonical(),
		"content_capability_root": string(l.ContentCapabilityRoot),
		"activity_generation":     uint64(l.ActivityGeneration),
		"generation":              uint64(l.Generation),
		"fence":                   uint64(l.Fence),
		"safety_epoch":            uint64(l.SafetyEpoch),
	}
}

// runtimeEvidenceRoot computes the digest root over the sorted pinned
// runtime evidence references.
func runtimeEvidenceRoot(refs []RuntimeEvidenceRef) Digest {
	sorted := append([]RuntimeEvidenceRef(nil), refs...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Channel == sorted[j].Channel {
			return sorted[i].EvidenceID < sorted[j].EvidenceID
		}
		return sorted[i].Channel < sorted[j].Channel
	})
	encoded := make([]map[string]any, 0, len(sorted))
	for _, ref := range sorted {
		encoded = append(encoded, ref.canonical())
	}
	return canonicalDigest(encoded)
}

// contentCapabilityRoot computes the digest root over the sorted pinned
// content capability references.
func contentCapabilityRoot(refs []ContentCapabilityRef) Digest {
	sorted := append([]ContentCapabilityRef(nil), refs...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].MemberSlot == sorted[j].MemberSlot {
			return sorted[i].CapabilityID < sorted[j].CapabilityID
		}
		return sorted[i].MemberSlot < sorted[j].MemberSlot
	})
	encoded := make([]map[string]any, 0, len(sorted))
	for _, ref := range sorted {
		encoded = append(encoded, ref.canonical())
	}
	return canonicalDigest(encoded)
}
