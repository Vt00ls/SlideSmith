package taskworkspace

import (
	"context"
	"sort"
	"time"
)

type (
	LogicalMember            string
	StateMemberType          string
	StateMemberClass         string
	ContentReferenceID       string
	ContentReferenceType     string
	DurabilityReceiptID      string
	DurableWriteID           string
	DurabilityAdapterID      string
	DurabilityGenerationID   string
	VerificationMethod       string
	CheckpointIntegrityID    string
	CheckpointIntegrityState string
)

const (
	StateMemberRegularFile  StateMemberType = "regular_file"
	StateMemberSymbolicLink StateMemberType = "symbolic_link"
	StateMemberHardLink     StateMemberType = "hard_link"

	StateMemberTaskOwnedMutable    StateMemberClass = "task_owned_mutable"
	StateMemberRuntimeRelease      StateMemberClass = "runtime_release"
	StateMemberCoreSkill           StateMemberClass = "core_skill"
	StateMemberTemplateVersion     StateMemberClass = "template_version"
	StateMemberResourceBundle      StateMemberClass = "resource_bundle"
	StateMemberSourceMaterial      StateMemberClass = "source_material"
	StateMemberImmutableInput      StateMemberClass = "immutable_input"
	StateMemberSharedCache         StateMemberClass = "shared_cache"
	StateMemberAgentComposeSession StateMemberClass = "agent_compose_session"
	StateMemberSecret              StateMemberClass = "secret"
	StateMemberLog                 StateMemberClass = "log"
	StateMemberFailedResidue       StateMemberClass = "failed_residue"
	StateMemberPublicationStaging  StateMemberClass = "publication_staging"

	CheckpointManifestReference ContentReferenceType = "checkpoint_manifest"
	CheckpointMemberReference   ContentReferenceType = "checkpoint_member"

	CheckpointIntegrityVerified CheckpointIntegrityState = "verified"

	VerificationEndToEndChecksum    VerificationMethod = "end_to_end_checksum"
	VerificationIndependentReadback VerificationMethod = "independent_readback"
)

// DurableObjectPort is the trusted verified-byte boundary used by the Task
// Workspace Lifecycle module. It accepts exact business facts selected by C04
// and never selects a Checkpoint, Revision, retention rule, or recovery target.
type DurableObjectPort interface {
	PrepareCheckpoint(context.Context, PrepareCheckpointContentRequest) (VerifiedCheckpointContent, error)
	VerifyCheckpoint(context.Context, VerifyCheckpointContentRequest) (VerifiedCheckpointContent, error)
}

type PrepareCheckpointContentRequest struct {
	PolicyDomainID    PolicyDomainID
	TaskID            TaskID
	TaskWorkspaceID   TaskWorkspaceID
	RuntimeViewID     RuntimeViewID
	RevisionID        RevisionID
	CheckpointID      CheckpointID
	Manifest          DeclaredStateManifest
	CanonicalManifest []byte
	Generation        Generation
	Fence             Fence
	Operation         Operation
}

type VerifyCheckpointContentRequest struct {
	PolicyDomainID    PolicyDomainID
	TaskID            TaskID
	TaskWorkspaceID   TaskWorkspaceID
	RevisionID        RevisionID
	CheckpointID      CheckpointID
	Manifest          CheckpointManifest
	CanonicalManifest []byte
	Expected          VerifiedCheckpointContent
	Generation        Generation
	Fence             Fence
	Operation         Operation
}

type ContentReference struct {
	ID              ContentReferenceID
	Type            ContentReferenceType
	PolicyDomainID  PolicyDomainID
	TaskID          TaskID
	TaskWorkspaceID TaskWorkspaceID
	RevisionID      RevisionID
	CheckpointID    CheckpointID
	StateMemberID   StateMemberID
	LogicalMember   LogicalMember
	ContentID       ContentID
	ContentDigest   Digest
	Size            uint64
	OperationID     OperationID
	EvidenceDigest  Digest
}

func (r ContentReference) CanonicalDigest() Digest {
	return canonicalDigest(struct {
		ID              ContentReferenceID
		Type            ContentReferenceType
		PolicyDomainID  PolicyDomainID
		TaskID          TaskID
		TaskWorkspaceID TaskWorkspaceID
		RevisionID      RevisionID
		CheckpointID    CheckpointID
		StateMemberID   StateMemberID
		LogicalMember   LogicalMember
		ContentID       ContentID
		ContentDigest   Digest
		Size            uint64
		OperationID     OperationID
	}{
		ID:              r.ID,
		Type:            r.Type,
		PolicyDomainID:  r.PolicyDomainID,
		TaskID:          r.TaskID,
		TaskWorkspaceID: r.TaskWorkspaceID,
		RevisionID:      r.RevisionID,
		CheckpointID:    r.CheckpointID,
		StateMemberID:   r.StateMemberID,
		LogicalMember:   r.LogicalMember,
		ContentID:       r.ContentID,
		ContentDigest:   r.ContentDigest,
		Size:            r.Size,
		OperationID:     r.OperationID,
	})
}

type DurabilityReceipt struct {
	ID                     DurabilityReceiptID
	DurabilityAuthorityID  DurabilityAuthorityID
	DurableWriteID         DurableWriteID
	DurabilityAdapterID    DurabilityAdapterID
	PolicyDomainID         PolicyDomainID
	ContentID              ContentID
	ContentDigest          Digest
	Size                   uint64
	DurabilityGenerationID DurabilityGenerationID
	VerificationMethod     VerificationMethod
	VerifiedAt             time.Time
	Decision               DurabilityDecision
	EvidenceDigest         Digest
}

func (r DurabilityReceipt) CanonicalDigest() Digest {
	return canonicalDigest(struct {
		ID                     DurabilityReceiptID
		DurabilityAuthorityID  DurabilityAuthorityID
		DurableWriteID         DurableWriteID
		DurabilityAdapterID    DurabilityAdapterID
		PolicyDomainID         PolicyDomainID
		ContentID              ContentID
		ContentDigest          Digest
		Size                   uint64
		DurabilityGenerationID DurabilityGenerationID
		VerificationMethod     VerificationMethod
		VerifiedAt             time.Time
		Decision               DurabilityDecision
	}{
		ID:                     r.ID,
		DurabilityAuthorityID:  r.DurabilityAuthorityID,
		DurableWriteID:         r.DurableWriteID,
		DurabilityAdapterID:    r.DurabilityAdapterID,
		PolicyDomainID:         r.PolicyDomainID,
		ContentID:              r.ContentID,
		ContentDigest:          r.ContentDigest,
		Size:                   r.Size,
		DurabilityGenerationID: r.DurabilityGenerationID,
		VerificationMethod:     r.VerificationMethod,
		VerifiedAt:             r.VerifiedAt,
		Decision:               r.Decision,
	})
}

type VerifiedCheckpointContent struct {
	Manifest           CheckpointManifest
	ManifestReference  ContentReference
	ContentReferences  []ContentReference
	DurabilityReceipts []DurabilityReceipt
}

type CheckpointIntegrityEvidence struct {
	ID                       CheckpointIntegrityID
	Digest                   Digest
	DurabilityAuthorityID    DurabilityAuthorityID
	PolicyDomainID           PolicyDomainID
	TaskID                   TaskID
	TaskWorkspaceID          TaskWorkspaceID
	RevisionID               RevisionID
	CheckpointID             CheckpointID
	ManifestDigest           Digest
	ManifestContentID        ContentID
	ValidationEvidenceID     EvidenceID
	ValidationEvidenceDigest Digest
	ContentEvidenceRoot      EvidenceRoot
	DurabilityEvidenceRoot   EvidenceRoot
	OperationID              OperationID
	RequestDigest            Digest
	Generation               Generation
	Fence                    Fence
	Decision                 CheckpointIntegrityState
}

func (e CheckpointIntegrityEvidence) CanonicalDigest() Digest {
	return canonicalDigest(struct {
		ID                       CheckpointIntegrityID
		DurabilityAuthorityID    DurabilityAuthorityID
		PolicyDomainID           PolicyDomainID
		TaskID                   TaskID
		TaskWorkspaceID          TaskWorkspaceID
		RevisionID               RevisionID
		CheckpointID             CheckpointID
		ManifestDigest           Digest
		ManifestContentID        ContentID
		ValidationEvidenceID     EvidenceID
		ValidationEvidenceDigest Digest
		ContentEvidenceRoot      EvidenceRoot
		DurabilityEvidenceRoot   EvidenceRoot
		OperationID              OperationID
		RequestDigest            Digest
		Generation               Generation
		Fence                    Fence
		Decision                 CheckpointIntegrityState
	}{
		ID:                       e.ID,
		DurabilityAuthorityID:    e.DurabilityAuthorityID,
		PolicyDomainID:           e.PolicyDomainID,
		TaskID:                   e.TaskID,
		TaskWorkspaceID:          e.TaskWorkspaceID,
		RevisionID:               e.RevisionID,
		CheckpointID:             e.CheckpointID,
		ManifestDigest:           e.ManifestDigest,
		ManifestContentID:        e.ManifestContentID,
		ValidationEvidenceID:     e.ValidationEvidenceID,
		ValidationEvidenceDigest: e.ValidationEvidenceDigest,
		ContentEvidenceRoot:      e.ContentEvidenceRoot,
		DurabilityEvidenceRoot:   e.DurabilityEvidenceRoot,
		OperationID:              e.OperationID,
		RequestDigest:            e.RequestDigest,
		Generation:               e.Generation,
		Fence:                    e.Fence,
		Decision:                 e.Decision,
	})
}

type CheckpointEvidence struct {
	Manifest           CheckpointManifest
	ManifestReference  ContentReference
	ContentReferences  []ContentReference
	DurabilityReceipts []DurabilityReceipt
	IntegrityEvidence  CheckpointIntegrityEvidence
}

func canonicalContentReferences(manifest ContentReference, members []ContentReference) []ContentReference {
	references := append([]ContentReference{manifest}, members...)
	sort.Slice(references, func(i, j int) bool {
		return references[i].ID < references[j].ID
	})
	return references
}

func canonicalDurabilityReceipts(receipts []DurabilityReceipt) []DurabilityReceipt {
	canonical := append([]DurabilityReceipt(nil), receipts...)
	sort.Slice(canonical, func(i, j int) bool {
		return canonical[i].ID < canonical[j].ID
	})
	return canonical
}
