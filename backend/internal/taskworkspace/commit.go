package taskworkspace

import (
	"encoding/json"
	"sort"
)

type (
	CheckpointID          string
	StateMemberID         string
	ContentID             string
	EvidenceID            string
	EvidenceRoot          string
	DurabilityAuthorityID string
	ValidationDecision    string
	DurabilityDecision    string
)

const (
	ValidationAccepted ValidationDecision = "accepted"
	DurabilityVerified DurabilityDecision = "verified"
)

type DeclaredStateMember struct {
	ID            StateMemberID
	LogicalMember LogicalMember
	Type          StateMemberType
	Mode          uint32
	Class         StateMemberClass
	ContentDigest Digest
	Size          uint64
}

type DeclaredStateManifest struct {
	Digest  Digest
	Members []DeclaredStateMember
}

func (m DeclaredStateManifest) CanonicalDigest() Digest {
	return canonicalDigest(m.canonicalValue())
}

func (m DeclaredStateManifest) CanonicalBytes() []byte {
	encoded, err := json.Marshal(m.canonicalValue())
	if err != nil {
		panic(err)
	}
	return encoded
}

func (m DeclaredStateManifest) canonicalValue() declaredStateManifestCanonical {
	members := append([]DeclaredStateMember(nil), m.Members...)
	sort.Slice(members, func(i, j int) bool {
		if members[i].LogicalMember == members[j].LogicalMember {
			return members[i].ID < members[j].ID
		}
		return members[i].LogicalMember < members[j].LogicalMember
	})
	return declaredStateManifestCanonical{Members: members}
}

type declaredStateManifestCanonical struct {
	Members []DeclaredStateMember
}

// CheckpointManifest is the canonical immutable manifest accepted from the
// trusted Durable Object port after it has assigned opaque content identities.
// DeclaredStateManifest remains the caller's semantic declaration and cannot
// carry or mint ContentIDs.
type CheckpointManifest struct {
	Digest              Digest
	DeclaredStateDigest Digest
	Members             []CheckpointManifestMember
}

type CheckpointManifestMember struct {
	ID            StateMemberID
	LogicalMember LogicalMember
	Type          StateMemberType
	Mode          uint32
	Class         StateMemberClass
	ContentID     ContentID
	ContentDigest Digest
	Size          uint64
}

func (m CheckpointManifest) CanonicalDigest() Digest {
	return canonicalDigest(m.canonicalValue())
}

func (m CheckpointManifest) CanonicalBytes() []byte {
	encoded, err := json.Marshal(m.canonicalValue())
	if err != nil {
		panic(err)
	}
	return encoded
}

func (m CheckpointManifest) canonicalValue() checkpointManifestCanonical {
	members := append([]CheckpointManifestMember(nil), m.Members...)
	sort.Slice(members, func(i, j int) bool {
		if members[i].LogicalMember == members[j].LogicalMember {
			return members[i].ID < members[j].ID
		}
		return members[i].LogicalMember < members[j].LogicalMember
	})
	return checkpointManifestCanonical{
		DeclaredStateDigest: m.DeclaredStateDigest,
		Members:             members,
	}
}

type checkpointManifestCanonical struct {
	DeclaredStateDigest Digest
	Members             []CheckpointManifestMember
}

type ValidationAuthorityID string

type ValidationEvidence struct {
	ID                          EvidenceID
	Digest                      Digest
	ValidationAuthorityID       ValidationAuthorityID
	PolicyDomainID              PolicyDomainID
	TaskID                      TaskID
	TaskWorkspaceID             TaskWorkspaceID
	RuntimeViewID               RuntimeViewID
	BaseRevisionID              RevisionID
	PhaseRunID                  PhaseRunID
	RuntimeRunID                RuntimeRunID
	RuntimeOperationID          OperationID
	SandboxLeaseAuthorityDigest Digest
	ManifestDigest              Digest
	Generation                  Generation
	Fence                       Fence
	Decision                    ValidationDecision
}

func (e ValidationEvidence) CanonicalDigest() Digest {
	return canonicalDigest(struct {
		ID                          EvidenceID
		ValidationAuthorityID       ValidationAuthorityID
		PolicyDomainID              PolicyDomainID
		TaskID                      TaskID
		TaskWorkspaceID             TaskWorkspaceID
		RuntimeViewID               RuntimeViewID
		BaseRevisionID              RevisionID
		PhaseRunID                  PhaseRunID
		RuntimeRunID                RuntimeRunID
		RuntimeOperationID          OperationID
		SandboxLeaseAuthorityDigest Digest
		ManifestDigest              Digest
		Generation                  Generation
		Fence                       Fence
		Decision                    ValidationDecision
	}{
		ID:                          e.ID,
		ValidationAuthorityID:       e.ValidationAuthorityID,
		PolicyDomainID:              e.PolicyDomainID,
		TaskID:                      e.TaskID,
		TaskWorkspaceID:             e.TaskWorkspaceID,
		RuntimeViewID:               e.RuntimeViewID,
		BaseRevisionID:              e.BaseRevisionID,
		PhaseRunID:                  e.PhaseRunID,
		RuntimeRunID:                e.RuntimeRunID,
		RuntimeOperationID:          e.RuntimeOperationID,
		SandboxLeaseAuthorityDigest: e.SandboxLeaseAuthorityDigest,
		ManifestDigest:              e.ManifestDigest,
		Generation:                  e.Generation,
		Fence:                       e.Fence,
		Decision:                    e.Decision,
	})
}

type CommitRuntimeViewRequest struct {
	PolicyDomainID          PolicyDomainID
	TaskID                  TaskID
	TaskWorkspaceID         TaskWorkspaceID
	RuntimeViewID           RuntimeViewID
	RuntimeOperationID      OperationID
	SandboxLeaseAuthority   SandboxLeaseAuthority
	BaseRevisionID          RevisionID
	ExpectedCurrentRevision RevisionID
	Generation              Generation
	Fence                   Fence
	ValidationEvidence      ValidationEvidence
	DeclaredStateManifest   DeclaredStateManifest
	Operation               Operation
}

func (r CommitRuntimeViewRequest) CanonicalRequestDigest() Digest {
	return canonicalDigest(struct {
		Kind                    string
		PolicyDomainID          PolicyDomainID
		TaskID                  TaskID
		TaskWorkspaceID         TaskWorkspaceID
		RuntimeViewID           RuntimeViewID
		RuntimeOperationID      OperationID
		SandboxLeaseAuthority   SandboxLeaseAuthority
		BaseRevisionID          RevisionID
		ExpectedCurrentRevision RevisionID
		Generation              Generation
		Fence                   Fence
		ValidationEvidence      ValidationEvidence
		DeclaredStateManifest   declaredStateManifestCanonical
		OperationID             OperationID
	}{
		Kind:                    "commit_runtime_view",
		PolicyDomainID:          r.PolicyDomainID,
		TaskID:                  r.TaskID,
		TaskWorkspaceID:         r.TaskWorkspaceID,
		RuntimeViewID:           r.RuntimeViewID,
		RuntimeOperationID:      r.RuntimeOperationID,
		SandboxLeaseAuthority:   r.SandboxLeaseAuthority,
		BaseRevisionID:          r.BaseRevisionID,
		ExpectedCurrentRevision: r.ExpectedCurrentRevision,
		Generation:              r.Generation,
		Fence:                   r.Fence,
		ValidationEvidence:      r.ValidationEvidence,
		DeclaredStateManifest:   r.DeclaredStateManifest.canonicalValue(),
		OperationID:             r.Operation.ID,
	})
}

type CommitRuntimeViewResult struct {
	TaskWorkspaceID          TaskWorkspaceID
	RevisionID               RevisionID
	CheckpointID             CheckpointID
	BaseRevisionID           RevisionID
	PredecessorRevisionID    RevisionID
	ManifestDigest           Digest
	ValidationEvidenceID     EvidenceID
	ValidationEvidenceDigest Digest
	ContentEvidenceRoot      EvidenceRoot
	DurabilityEvidenceRoot   EvidenceRoot
	CheckpointEvidence       CheckpointEvidence
	Generation               Generation
	PreviousFence            Fence
	Fence                    Fence
	Operation                Operation
}
